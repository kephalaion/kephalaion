package store

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kephalaion/kephalaion/internal/ident"
)

func TestPendingToken(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "node.db")
	tok, err := ident.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	p := PendingTokenPath(db, "01AAA")
	if p != filepath.Join(dir, "hub-01AAA.pending") {
		t.Errorf("Ort %s", p)
	}
	if _, err := ReadPendingToken(p, "01AAA"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("ohne Datei: %v", err)
	}
	if err := WritePendingToken(p, "01AAA", "keph_kurz"); err == nil {
		t.Error("ungültiges Token vorgemerkt")
	}
	if err := WritePendingToken(p, "01AAA", tok); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(p); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("Rechte %v, %v", st.Mode(), err)
	}
	if err := WritePendingToken(p, "01AAA", tok); !errors.Is(err, ErrPendingExists) {
		t.Errorf("zweites Vormerken: %v", err)
	}
	if got, err := ReadPendingToken(p, "01AAA"); err != nil || got != tok {
		t.Errorf("lesen: %v", err)
	}
	// Gehört zu einem anderen Eintrag: nicht angenommen.
	if _, err := ReadPendingToken(p, "01BBB"); err == nil || strings.Contains(err.Error(), tok) {
		t.Errorf("fremde entry_id: %v", err)
	}
	if err := os.WriteFile(p, []byte("kaputt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPendingToken(p, "01AAA"); err == nil {
		t.Error("kaputte Datei angenommen")
	}
	if err := RemovePendingToken(p); err != nil {
		t.Fatal(err)
	}
	if err := RemovePendingToken(p); err != nil {
		t.Errorf("zweites Löschen: %v", err)
	}
}

// ReplaceHubToken schreibt nur in den Eintrag mit der entry_id; nach rm und
// add unter demselben Alias ErrEntryGone. RemoveHub räumt die .pending ab.
func TestReplaceHubTokenAndRemove(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	h := Hub{Name: "vm", NodeName: "laptop", Transport: "https", Address: "https://vm.example/hub", Token: token(t)}
	if err := s.AddHub(ctx, h, false); err != nil {
		t.Fatal(err)
	}
	first, err := s.Hub(ctx, "vm")
	if err != nil {
		t.Fatal(err)
	}
	next := token(t)
	if err := s.ReplaceHubToken(ctx, "vm", first.EntryID, next); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Hub(ctx, "vm"); got.Token != next {
		t.Error("Token nicht ersetzt")
	}
	p := s.PendingTokenPath(first.EntryID)
	if err := WritePendingToken(p, first.EntryID, token(t)); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveHub(ctx, "vm"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("RemoveHub: .pending liegt noch (%v)", err)
	}
	if err := s.AddHub(ctx, h, false); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceHubToken(ctx, "vm", first.EntryID, token(t)); !errors.Is(err, ErrEntryGone) {
		t.Errorf("alte entry_id: %v", err)
	}
}
