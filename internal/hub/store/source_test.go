package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/oklog/ulid/v2"

	"github.com/kephalaion/kephalaion/internal/contract"
	"github.com/kephalaion/kephalaion/internal/sqlitedb"
)

// Task 031: öffentliche Collections und Collections aus der Liste der
// Quellen (k-playbook).

// Eine Collection aus der Liste ist immer öffentlich; public lässt sich dort
// nicht abschalten, sonst schon.
func TestCollectionPublic(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	if err := s.AddCollection(ctx, "k-playbook", "Vorgaben", false); err != nil {
		t.Fatal(err)
	}
	if err := s.AddCollection(ctx, "team", "", false); err != nil {
		t.Fatal(err)
	}
	if err := s.AddCollection(ctx, "offen", "", true); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"k-playbook": true, "team": false, "offen": true}
	for name, pub := range want {
		c, err := s.Collection(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		if c.Public != pub || c.IsPublic() != pub || c.FetchedTag != "" || c.FetchedAt != 0 {
			t.Errorf("%s: %+v", name, c)
		}
	}
	off, on := false, true
	if err := s.SetCollection(ctx, "k-playbook", CollectionChange{Public: &off}); err == nil ||
		!strings.Contains(err.Error(), "bleibt öffentlich") {
		t.Errorf("k-playbook abschalten: %v", err)
	}
	if err := s.SetCollection(ctx, "offen", CollectionChange{Public: &off}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetCollection(ctx, "team", CollectionChange{Public: &on, Description: ptr("Team")}); err != nil {
		t.Fatal(err)
	}
	if c, _ := s.Collection(ctx, "offen"); c.IsPublic() {
		t.Errorf("offen: %+v", c)
	}
	if c, _ := s.Collection(ctx, "team"); !c.IsPublic() || c.Description != "Team" {
		t.Errorf("team: %+v", c)
	}
	if _, err := s.Collection(ctx, "fehlt"); !errors.Is(err, ErrNotFound) {
		t.Errorf("fehlt: %v", err)
	}
	if err := s.SetCollection(ctx, "fehlt", CollectionChange{Public: &on}); !errors.Is(err, ErrNotFound) {
		t.Errorf("set fehlt: %v", err)
	}
}

// In einer Collection mit Quelle schreibt niemand: kein Account über einen
// Node (forbidden, nicht not_readable — sie ist für jeden Node erlaubt und
// öffentlich), nicht der Admin, kein grant.
func TestSourceCollectionLocked(t *testing.T) {
	ctx := context.Background()
	f := newWriteStore(t)
	s := f.s
	if err := s.AddCollection(ctx, "k-playbook", "", false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutDocument(ctx, "k-playbook", "x.md", "x"); !errors.Is(err, ErrForbidden) ||
		!strings.Contains(err.Error(), "hub collection fetch k-playbook") {
		t.Errorf("put: %v", err)
	}
	if _, err := s.ImportDocuments(ctx, "k-playbook", []DocumentInput{{Name: "x.md", Content: "x"}}); !errors.Is(err, ErrForbidden) {
		t.Errorf("import: %v", err)
	}
	if _, err := s.DeleteDocument(ctx, "k-playbook", "x.md"); !errors.Is(err, ErrForbidden) {
		t.Errorf("rm: %v", err)
	}
	if _, err := s.GrantAccount(ctx, "bob", "k-playbook", contract.Rights{Write: true}); !errors.Is(err, ErrForbidden) {
		t.Errorf("grant: %v", err)
	}
	// Der Node laptop darf k-playbook ohne grant abgleichen; bob hat keine
	// Zeile, liest sie aber (öffentlich) — schreiben ist forbidden.
	if _, err := s.CreateDocumentAs(ctx, f.as("bob"), "k-playbook", "x.md", "x"); !errors.Is(err, ErrForbidden) {
		t.Errorf("create: %v", err)
	}
	if _, err := s.DeleteDocumentAs(ctx, f.as("bob"), "k-playbook", "defaults", nil, true); !errors.Is(err, ErrForbidden) {
		t.Errorf("delete: %v", err)
	}
	// Account ungültig bleibt account_unauthenticated.
	bad := f.as("bob")
	bad.TokenHash = strings.Repeat("0", 64)
	if _, err := s.CreateDocumentAs(ctx, bad, "k-playbook", "x.md", "x"); !errors.Is(err, ErrAccountAuth) {
		t.Errorf("falsches Token: %v", err)
	}
}

// Eine Collection, die nur öffentlich ist: lesbar für jeden Account, wenn der
// Node sie abgleichen darf — schreiben ohne Zeile ist forbidden; ohne grant
// des Nodes bleibt sie not_readable.
func TestPublicCollectionWrite(t *testing.T) {
	ctx := context.Background()
	f := newWriteStore(t)
	s := f.s
	on := true
	// c: laptop darf abgleichen, niemand hat eine Zeile; b: laptop nicht.
	for _, c := range []string{"b", "c"} {
		if err := s.SetCollection(ctx, c, CollectionChange{Public: &on}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.CreateDocumentAs(ctx, f.as("alice"), "c", "x.md", "x"); !errors.Is(err, ErrForbidden) {
		t.Errorf("c ohne Zeile: %v", err)
	}
	if _, err := s.CreateDocumentAs(ctx, f.as("bob"), "b", "x.md", "x"); !errors.Is(err, ErrNotReadable) {
		t.Errorf("b ohne grant des Nodes: %v", err)
	}
	// Mit Zeile und Recht schreibt der Account wie sonst.
	if _, err := s.GrantAccount(ctx, "alice", "c", contract.Rights{Write: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateDocumentAs(ctx, f.as("alice"), "c", "x.md", "x"); err != nil {
		t.Errorf("c mit Recht: %v", err)
	}
}

// hub collection rm entfernt bei einer Collection mit Quelle die
// Dokumentzeilen samt Löschmarken; ein Node mit grant hält sie weiter.
func TestRemoveSourceCollection(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	if err := s.AddCollection(ctx, "k-playbook", "", false); err != nil {
		t.Fatal(err)
	}
	insertRaw(t, s, "k-playbook", "defaults/a.md", false)
	insertRaw(t, s, "k-playbook", "defaults/alt.md", true)
	if _, err := s.AddNode(ctx, "laptop", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.Grant(ctx, "laptop", "k-playbook"); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveCollection(ctx, "k-playbook"); !errors.Is(err, ErrInUse) {
		t.Fatalf("rm mit grant: %v", err)
	}
	if n, _ := count(ctx, s.db, queries.CollectionCountDocs, "k-playbook"); n != 2 {
		t.Errorf("Zeilen nach abgelehntem rm: %d", n)
	}
	if err := s.Revoke(ctx, "laptop", "k-playbook"); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveCollection(ctx, "k-playbook"); err != nil {
		t.Fatalf("rm: %v", err)
	}
	if n, _ := count(ctx, s.db, `SELECT COUNT(*) FROM documents WHERE collection = $1`, "k-playbook"); n != 0 {
		t.Errorf("Zeilen nach rm: %d", n)
	}
	// Eine gewöhnliche Collection mit Dokumenten bleibt.
	if err := s.AddCollection(ctx, "team", "", false); err != nil {
		t.Fatal(err)
	}
	insertRaw(t, s, "team", "a.md", false)
	if err := s.RemoveCollection(ctx, "team"); !errors.Is(err, ErrInUse) {
		t.Errorf("rm team: %v", err)
	}
}

// insertRaw legt eine Zeile direkt an, an den Schreibwegen vorbei.
func insertRaw(t *testing.T, s *sqliteStore, collection, name string, deleted bool) {
	t.Helper()
	del, content := 0, any("x")
	if deleted {
		del, content = 1, nil
	}
	now := sqlitedb.NowMillis()
	if _, err := s.db.Exec(q(`INSERT INTO documents (id, collection, name, content, meta, deleted, revision,
		created_at, created_by, updated_at, updated_by) VALUES ($1, $2, $3, $4, NULL, $5, 1, $6, 'system', $6, 'system')`),
		ulid.Make().String(), collection, name, content, del, now); err != nil {
		t.Fatal(err)
	}
}

// Rechte auf einer Collection mit Quelle weist der Import ab; public
// übernimmt er, bei einer aus der Liste immer.
func TestImportSourceCollection(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	tables := Tables{
		Collections: []Collection{
			{Name: "k-playbook", CreatedAt: 1, CreatedBy: Admin},
			{Name: "offen", Public: true, CreatedAt: 1, CreatedBy: Admin},
			{Name: "zu", CreatedAt: 1, CreatedBy: Admin},
		},
		Accounts: []Account{{Name: "bob", User: "bob", TokenHash: strings.Repeat("a", 64), CreatedAt: 1, CreatedBy: Admin,
			Rights: []AccountRight{{Collection: "k-playbook"}}}},
	}
	if err := s.Import(ctx, nil, &tables); err == nil || !strings.Contains(err.Error(), "vor dem Export umbenennen") {
		t.Fatalf("Import mit Rechten auf k-playbook: %v", err)
	}
	tables.Accounts[0].Rights = []AccountRight{{Collection: "zu"}}
	if err := s.Import(ctx, nil, &tables); err != nil {
		t.Fatal(err)
	}
	colls, err := s.Collections(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, c := range colls {
		got[c.Name] = c.Public
	}
	if !got["k-playbook"] || !got["offen"] || got["zu"] {
		t.Errorf("public nach Import: %v", got)
	}
}
