package store

import (
	"context"
	"fmt"

	"github.com/oklog/ulid/v2"

	"github.com/kephalaion/kephalaion/internal/config"
	"github.com/kephalaion/kephalaion/internal/sqlitedb"
)

// Backup schreibt einen Snapshot der Hub-Datenbank addr nach dst, auch
// während serve läuft und andere schreiben, und prüft ihn: Er öffnet sich wie
// jede Hub-Datenbank (Open: Rolle hub, Schemafassung dieses Binarys), steht
// auf WAL und besteht integrity_check (sqlitedb.Snapshot). Info beschreibt die
// geschriebene Kopie — ihre Revision ist die des Snapshots, nicht die der
// Quelle danach. dst darf es noch nicht geben und entsteht mit 0600; scheitert
// etwas, bleibt von dst nichts zurück.
//
// Gesichert wird nur SQLite: Für eine andere Datenbankart ist Backup ein
// Fehler, config.ErrUnsupported.
func Backup(ctx context.Context, addr config.DB, dst string) (Info, error) {
	if addr.Kind != config.SQLite {
		return Info{}, fmt.Errorf("Datenbankart %q: hub backup sichert nur eine SQLite-Datenbank, eine andere wird %w",
			addr.Kind, config.ErrUnsupported)
	}
	if err := sqlitedb.Snapshot(ctx, addr.Path, dst); err != nil {
		return Info{}, err
	}
	info, err := backupInfo(ctx, dst)
	if err != nil {
		_ = sqlitedb.Remove(dst)
		return Info{}, fmt.Errorf("Snapshot verworfen: %w", err)
	}
	return info, nil
}

// backupInfo öffnet den geschriebenen Snapshot als Hub-Datenbank und liest
// db_info; danach ist er wieder geschlossen.
func backupInfo(ctx context.Context, path string) (Info, error) {
	s, err := Open(ctx, config.SQLiteDB(path))
	if err != nil {
		return Info{}, err
	}
	info, err := s.Info(ctx)
	if cerr := s.Close(); err == nil {
		err = cerr
	}
	return info, err
}

// ResetHubID vergibt eine neue hub_id, eine ULID wie beim Anlegen, und
// liefert die bisherige und die neue. Dokumente und Revision bleiben; eine
// Zeile hub.reset_id in actions nennt die bisherige als subject.
func (s *sqliteStore) ResetHubID(ctx context.Context) (old, fresh string, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", "", err
	}
	defer func() { _ = tx.Rollback() }()
	if old, err = sqlitedb.GetInfo(ctx, tx, keyHubID); err != nil {
		return "", "", err
	}
	fresh = ulid.Make().String()
	if err := sqlitedb.SetInfo(ctx, tx, keyHubID, fresh); err != nil {
		return "", "", err
	}
	if err := logAction(ctx, tx, "hub.reset_id", old); err != nil {
		return "", "", err
	}
	if err := tx.Commit(); err != nil {
		return "", "", err
	}
	return old, fresh, nil
}
