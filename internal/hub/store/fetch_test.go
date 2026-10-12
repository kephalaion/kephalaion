package store

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/kephalaion/kephalaion/internal/hub/source"
)

func files(kv ...string) []source.File {
	var out []source.File
	for i := 0; i+1 < len(kv); i += 2 {
		out = append(out, source.File{Name: kv[i], Content: kv[i+1]})
	}
	return out
}

// ReplaceDefaults ersetzt defaults/ als Ganzes unter einer Revision: löscht,
// was fehlt, ersetzt, was abweicht, legt an, was neu ist; Urheber system,
// actions mit Account und Träger; derselbe Tag ist schon aktuell. Was
// außerhalb von defaults/ liegt, bleibt.
func TestReplaceDefaults(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	if err := s.AddCollection(ctx, "k-playbook", "", false); err != nil {
		t.Fatal(err)
	}
	insertRaw(t, s, "k-playbook", "notiz.md", false)
	all := trace(s)
	res, err := s.ReplaceDefaults(ctx, "k-playbook", "v1", files("README.md", "r1", "rules/a.md", "a1", "rules/b.md", "b1"),
		source.Actor{Account: "bob", Carrier: "laptop"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Current || res.Tag != "v1" || res.Created != 3 || res.Updated != 0 || res.Deleted != 0 || res.Revision == 0 {
		t.Fatalf("erstes Holen: %+v", res)
	}
	if stmts := (*all)[0]; stmts[0] != q(queries.CollectionLock) {
		t.Errorf("erste Anweisung %q", stmts[0])
	}
	docs, err := s.Documents(ctx, "k-playbook", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 4 {
		t.Fatalf("Dokumente: %+v", docs)
	}
	for _, d := range docs {
		if strings.HasPrefix(d.Name, "defaults/") && (d.CreatedBy != "system" || d.UpdatedBy != "system" || d.Revision != res.Revision) {
			t.Errorf("%s: %+v", d.Name, d)
		}
	}
	c, _ := s.Collection(ctx, "k-playbook")
	if c.FetchedTag != "v1" || c.FetchedAt == 0 {
		t.Errorf("geholter Stand: %+v", c)
	}
	var n int64
	if n, _ = count(ctx, s.db, `SELECT COUNT(*) FROM actions WHERE account = 'bob' AND carrier = 'laptop'
		AND action = 'create' AND revision = $1`, res.Revision); n != 3 {
		t.Errorf("actions create: %d", n)
	}
	if n, _ = count(ctx, s.db, `SELECT COUNT(*) FROM actions WHERE action = 'collection.fetch'
		AND subject = 'k-playbook v1' AND account = 'bob'`); n != 1 {
		t.Errorf("actions collection.fetch: %d", n)
	}

	// Derselbe Tag: nichts zu tun, auch mit anderem Inhalt.
	res2, err := s.ReplaceDefaults(ctx, "k-playbook", "v1", files("x.md", "x"), source.Actor{})
	if err != nil || !res2.Current || res2.Tag != "v1" {
		t.Errorf("schon aktuell: %+v %v", res2, err)
	}

	// Neuer Stand: rules/a.md geändert, rules/b.md fehlt, c.md neu,
	// README.md gleich.
	res3, err := s.ReplaceDefaults(ctx, "k-playbook", "v2", files("README.md", "r1", "rules/a.md", "a2", "c.md", "c"),
		source.Actor{Account: Admin})
	if err != nil {
		t.Fatal(err)
	}
	if res3.Created != 1 || res3.Updated != 1 || res3.Deleted != 1 || res3.Revision <= res.Revision {
		t.Errorf("zweites Holen: %+v", res3)
	}
	if d, err := s.Document(ctx, "k-playbook", "README.md"); err == nil || !errors.Is(err, ErrNotFound) {
		t.Errorf("README.md außerhalb von defaults: %+v %v", d, err)
	}
	if d, _ := s.Document(ctx, "k-playbook", "defaults/README.md"); d.Revision != res.Revision {
		t.Errorf("unverändertes README: Revision %d", d.Revision)
	}
	if _, err := s.Document(ctx, "k-playbook", "notiz.md"); err != nil {
		t.Errorf("notiz.md außerhalb von defaults/: %v", err)
	}

	// Aus der Datei c.md wird ein Verzeichnis: Erst wird gelöscht, dann
	// angelegt — kein Konflikt.
	res4, err := s.ReplaceDefaults(ctx, "k-playbook", "v3", files("c.md/x", "x"), source.Actor{})
	if err != nil {
		t.Fatalf("Datei wird Verzeichnis: %v", err)
	}
	if res4.Created != 1 || res4.Deleted != 3 {
		t.Errorf("drittes Holen: %+v", res4)
	}

	// Ohne Quelle, ohne Collection, ungültige Datei: Fehler, nichts geändert.
	if err := s.AddCollection(ctx, "team", "", false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReplaceDefaults(ctx, "team", "v1", nil, source.Actor{}); err == nil {
		t.Error("ohne Quelle geholt")
	}
	if _, err := s.ReplaceDefaults(ctx, "k-playbook", "v4", files("a.md", "\x00"), source.Actor{}); err == nil {
		t.Error("NUL geholt")
	}
	if err := s.RemoveCollection(ctx, "k-playbook"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReplaceDefaults(ctx, "k-playbook", "v4", files("a.md", "a"), source.Actor{}); !errors.Is(err, ErrNotFound) {
		t.Errorf("ohne Collection: %v", err)
	}
}

// Zwei gleichzeitige Läufe mit demselben Tag: Einer schreibt, der andere
// sieht danach den Tag und ist schon aktuell.
func TestReplaceDefaultsConcurrent(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	if err := s.AddCollection(ctx, "k-playbook", "", false); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make([]source.Result, 4)
	errs := make([]error, 4)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = s.ReplaceDefaults(ctx, "k-playbook", "v1", files("a.md", "a"), source.Actor{})
		}()
	}
	wg.Wait()
	written := 0
	for i, r := range results {
		if errs[i] != nil {
			t.Fatalf("Lauf %d: %v", i, errs[i])
		}
		if !r.Current {
			written++
		}
	}
	if written != 1 {
		t.Errorf("%d Läufe haben geschrieben: %+v", written, results)
	}
	if n, _ := count(ctx, s.db, `SELECT COUNT(*) FROM documents WHERE collection = 'k-playbook'`); n != 1 {
		t.Errorf("%d Zeilen", n)
	}
}
