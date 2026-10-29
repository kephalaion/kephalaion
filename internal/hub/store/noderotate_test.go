package store

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/kephalaion/kephalaion/internal/ident"
)

// nodeHash liest den gespeicherten Hash eines Nodes.
func nodeHash(t *testing.T, s *sqliteStore, name string) string {
	t.Helper()
	n, err := s.Node(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	return n.TokenHash
}

// RotateNode gelingt einmal; ein zweites Mal mit dem alten Token scheitert,
// ebenso ein gesperrter Node. Die Zeile in actions nennt den Node als
// Account, Träger und Ziel, ohne Revision; Fehlversuche stehen nicht darin.
func TestRotateNode(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	token, err := s.AddNode(ctx, "laptop", "")
	if err != nil {
		t.Fatal(err)
	}
	revBefore, err := s.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	old, next := ident.HashToken(token), ident.HashToken("keph_neu")
	if err := s.RotateNode(ctx, "laptop", old, next); err != nil {
		t.Fatal(err)
	}
	if got := nodeHash(t, s, "laptop"); got != next {
		t.Errorf("Hash nach rotate %s, erwartet %s", got, next)
	}
	a := lastAction(t, s)
	if a.account != "laptop" || a.action != "node.rotate" || a.carrier.String != "laptop" ||
		a.subject.String != "laptop" || a.revision.Valid {
		t.Errorf("actions: %+v", a)
	}
	info, err := s.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if info.Revision != revBefore.Revision {
		t.Errorf("Revision %d → %d; node_rotate ändert keine Dokumente", revBefore.Revision, info.Revision)
	}

	// Ein zweites Mal mit dem alten Hash: abgelehnt, nichts geändert.
	if err := s.RotateNode(ctx, "laptop", old, ident.HashToken("keph_dritt")); !errors.Is(err, ErrNodeAuth) {
		t.Errorf("zweites Mal mit altem Token: %v", err)
	}
	if got := nodeHash(t, s, "laptop"); got != next {
		t.Errorf("Hash nach abgelehntem rotate %s", got)
	}
	// Gesperrt: abgelehnt, auch mit dem gültigen Hash.
	if err := s.SetNodeLocked(ctx, "laptop", true); err != nil {
		t.Fatal(err)
	}
	if err := s.RotateNode(ctx, "laptop", next, ident.HashToken("keph_dritt")); !errors.Is(err, ErrNodeAuth) {
		t.Errorf("gesperrt: %v", err)
	}
	// Unbekannt.
	if err := s.RotateNode(ctx, "fremd", next, ident.HashToken("keph_dritt")); !errors.Is(err, ErrNodeAuth) {
		t.Errorf("unbekannt: %v", err)
	}
	if err := s.RotateNode(ctx, "laptop", next, "kein-hash"); err == nil {
		t.Error("ungültiger neuer Hash angenommen")
	}
	var rotates int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM actions WHERE action = 'node.rotate'`).Scan(&rotates); err != nil || rotates != 1 {
		t.Errorf("%d Zeilen node.rotate in actions, %v", rotates, err)
	}
}

// Von gleichzeitigen RotateNode mit demselben alten Token gelingt genau
// einer.
func TestRotateNodeConcurrent(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	token, err := s.AddNode(ctx, "laptop", "")
	if err != nil {
		t.Fatal(err)
	}
	old := ident.HashToken(token)
	const n = 8
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = s.RotateNode(ctx, "laptop", old, ident.HashToken(fmt.Sprintf("keph_neu%d", i)))
		}()
	}
	wg.Wait()
	won := -1
	for i, err := range errs {
		switch {
		case err == nil && won < 0:
			won = i
		case err == nil:
			t.Errorf("node_rotate %d und %d gelangen beide", won, i)
		case !errors.Is(err, ErrNodeAuth):
			t.Errorf("node_rotate %d: %v", i, err)
		}
	}
	if won < 0 {
		t.Fatal("kein node_rotate gelang")
	}
	if got := nodeHash(t, s, "laptop"); got != ident.HashToken(fmt.Sprintf("keph_neu%d", won)) {
		t.Errorf("Hash %s gehört nicht zum Gewinner %d", got, won)
	}
	var rotates int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM actions WHERE action = 'node.rotate'`).Scan(&rotates); err != nil || rotates != 1 {
		t.Errorf("%d Zeilen node.rotate in actions, %v", rotates, err)
	}
}
