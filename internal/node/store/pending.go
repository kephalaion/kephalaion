package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/kephalaion/kephalaion/internal/ident"
)

// Die .pending eines Hub-Eintrags (Task 034): das neue Token des Nodes
// während node_rotate, geschrieben bevor der Vorgang abgeschickt wird, damit
// es einen Absturz übersteht. Je Eintrag eine Datei neben node.db, die
// entry_id im Namen und im Inhalt — sie gehört zur entry_id, nicht zum
// Alias: Nach node hub rm und add unter demselben Alias gilt eine alte nicht.
// Keine Spalte in hubs (kein neues Schema), nicht im Export.

// pendingFile ist der Inhalt der Datei.
type pendingFile struct {
	EntryID string `json:"entry_id"`
	Token   string `json:"token"`
}

// PendingTokenPath ist der Ort der .pending eines Hub-Eintrags neben
// nodeDB: hub-<entry_id>.pending. Die entry_id ist eine ULID und taugt als
// Dateiname.
func PendingTokenPath(nodeDB, entryID string) string {
	return filepath.Join(filepath.Dir(nodeDB), "hub-"+entryID+".pending")
}

func (s *sqliteStore) PendingTokenPath(entryID string) string {
	return PendingTokenPath(s.path, entryID)
}

// ErrPendingExists meldet, dass für den Eintrag schon ein Token vorgemerkt
// ist; es ist erst zu klären.
var ErrPendingExists = errors.New("es ist schon ein Token vorgemerkt")

// WritePendingToken legt die .pending mit 0600 an, synchron auf der Platte.
// Gibt es sie schon, scheitert es mit ErrPendingExists.
func WritePendingToken(path, entryID, token string) error {
	if err := ident.CheckToken(token); err != nil {
		return err
	}
	data, err := json.Marshal(pendingFile{EntryID: entryID, Token: token})
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("%s: %w", path, ErrPendingExists)
	}
	if err != nil {
		return err
	}
	fail := func(err error) error {
		_ = f.Close()
		_ = os.Remove(path)
		return fmt.Errorf("%s: %w", path, err)
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		return fail(err)
	}
	if err := f.Sync(); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// ReadPendingToken liest das vorgemerkte Token eines Eintrags. Gibt es keine
// Datei, ist der Fehler fs.ErrNotExist. Eine Datei, die nicht zur entry_id
// gehört oder kein gültiges Token hält, ist ein Fehler; die Meldung nennt
// kein Token.
func ReadPendingToken(path, entryID string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var p pendingFile
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return "", fmt.Errorf("%s ist nicht lesbar", path)
	}
	if p.EntryID != entryID {
		return "", fmt.Errorf("%s gehört zu einem anderen Hub-Eintrag", path)
	}
	if err := ident.CheckToken(p.Token); err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	return p.Token, nil
}

// RemovePendingToken löscht die .pending; fehlt sie, ist das kein Fehler.
func RemovePendingToken(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// ReplaceHubToken schreibt das Token eines Eintrags, nur solange er noch
// der mit entryID ist (sonst ErrEntryGone) — so landet ein rotiertes Token
// nie in einem Eintrag, der inzwischen neu angelegt ist.
func (s *sqliteStore) ReplaceHubToken(ctx context.Context, name, entryID, token string) error {
	if err := ident.CheckToken(token); err != nil {
		return fmt.Errorf("Hub %s: %w", name, err)
	}
	res, err := s.db.ExecContext(ctx, qHubTokenOf, token, name, entryID)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return fmt.Errorf("Hub-Eintrag %s %w", name, ErrEntryGone)
	}
	return nil
}
