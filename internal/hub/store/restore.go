package store

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/kephalaion/kephalaion/internal/sqlitedb"
)

// dumpMaxRevision sind die höchsten Revisionen, die Zeilen nennen; Restore
// prüft sie gegen die Revision des Abzugs.
var dumpMaxRevision = []struct{ table, query string }{
	{"documents", `SELECT COALESCE(MAX(revision), 0) FROM documents`},
	{"actions", `SELECT COALESCE(MAX(revision), 0) FROM actions`},
}

// ErrNotEmpty meldet, dass Restore in einen Hub schreiben sollte, der schon
// Zeilen hat.
var ErrNotEmpty = errors.New("Hub nicht leer")

// RestoreInput ist, was Restore schreibt.
type RestoreInput struct {
	// Revision ist die Revision des Abzugs; sie wird die des Hubs.
	Revision int64
	// HubID übernimmt die hub_id des Abzugs; leer behält der Hub seine.
	HubID string
	// Rows gibt alle Zeilen an insert, Tabelle für Tabelle in der
	// Reihenfolge von DumpTables (Fremdschlüssel), values wie bei Dump.
	// Liefert Rows einen Fehler, wird nichts geschrieben.
	Rows func(insert func(table string, values []any) error) error
}

// Restore schreibt einen Abzug in einen leeren Hub, in einer Transaktion:
// alles oder nichts. Leer heißt: keine Tabelle des Abzugs hat eine Zeile
// (requests zählt nicht) — ein frisches hub init. Sonst ErrNotEmpty mit der
// belegten Tabelle, ohne Änderung. Darüber hinaus schreibt Restore nichts:
// keine Zeile in actions, in db_info nur die Revision und mit HubID die
// hub_id. Nennt eine Zeile eine Revision über der des Abzugs, ist der Abzug
// in sich nicht stimmig und Restore schreibt nichts.
func (s *sqliteStore) Restore(ctx context.Context, in RestoreInput) error {
	if in.Revision < 0 {
		return fmt.Errorf("Revision %d ist keine Zahl ≥ 0", in.Revision)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, t := range dumpTables {
		n, err := count(ctx, tx, dumpCount(t))
		if err != nil {
			return fmt.Errorf("%s zählen: %w", t.Name, err)
		}
		if n > 0 {
			return fmt.Errorf("%w: Tabelle %s hat %d Zeilen — hub restore schreibt nur in einen frisch "+
				"angelegten Hub (kephalaion hub init)", ErrNotEmpty, t.Name, n)
		}
	}
	byName := map[string]DumpTable{}
	for _, t := range dumpTables {
		byName[t.Name] = t
	}
	insert := func(table string, values []any) error {
		t, ok := byName[table]
		if !ok {
			return fmt.Errorf("unbekannte Tabelle %s", table)
		}
		if len(values) != len(t.Columns) {
			return fmt.Errorf("%s: %d Werte für %d Spalten", table, len(values), len(t.Columns))
		}
		if _, err := tx.ExecContext(ctx, q(dumpInsert(t)), values...); err != nil {
			return fmt.Errorf("%s schreiben: %w", table, err)
		}
		return nil
	}
	if err := in.Rows(insert); err != nil {
		return err
	}
	for _, m := range dumpMaxRevision {
		rev, err := count(ctx, tx, m.query)
		if err != nil {
			return fmt.Errorf("%s: Revision lesen: %w", m.table, err)
		}
		if rev > in.Revision {
			return fmt.Errorf("%s nennt Revision %d, der Abzug nur %d — nicht stimmig",
				m.table, rev, in.Revision)
		}
	}
	if err := sqlitedb.SetInfo(ctx, tx, keyRevision, strconv.FormatInt(in.Revision, 10)); err != nil {
		return err
	}
	if in.HubID != "" {
		if err := sqlitedb.SetInfo(ctx, tx, keyHubID, in.HubID); err != nil {
			return err
		}
	}
	return tx.Commit()
}
