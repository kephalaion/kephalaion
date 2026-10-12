package store

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/kephalaion/kephalaion/internal/ident"
)

func newStore(t *testing.T) *sqliteStore {
	t.Helper()
	s, err := Create(context.Background(), newDB(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s.(*sqliteStore)
}

// insertDoc legt eine Dokumentzeile von Hand an; Schreibwege für Dokumente
// gibt es noch nicht.
func insertDoc(t *testing.T, s *sqliteStore, id, coll, name string, deleted int) {
	t.Helper()
	_, err := s.db.ExecContext(context.Background(), `INSERT INTO documents
		(id, collection, name, deleted, revision, created_at, created_by, updated_at, updated_by)
		VALUES (?, ?, ?, ?, 1, 0, 'x', 0, 'x')`, id, coll, name, deleted)
	if err != nil {
		t.Fatal(err)
	}
}

type actionRow struct {
	account, action string
	subject         sql.NullString
}

func actions(t *testing.T, s *sqliteStore) []actionRow {
	t.Helper()
	rows, err := s.db.QueryContext(context.Background(),
		`SELECT account, action, subject FROM actions ORDER BY rowid`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []actionRow
	for rows.Next() {
		var a actionRow
		if err := rows.Scan(&a.account, &a.action, &a.subject); err != nil {
			t.Fatal(err)
		}
		out = append(out, a)
	}
	return out
}

func TestCollections(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	if err := s.AddCollection(ctx, "team-x", "Team X", false); err != nil {
		t.Fatal(err)
	}
	if err := s.AddCollection(ctx, "privat", "", false); err != nil {
		t.Fatal(err)
	}
	if err := s.AddCollection(ctx, "team-x", "", false); !errors.Is(err, ErrExists) {
		t.Errorf("doppelt: %v", err)
	}
	for _, bad := range []string{"Team", "a:b", "system", "SYSTEM-x", ""} {
		if err := s.AddCollection(ctx, bad, "", false); err == nil {
			t.Errorf("%q hätte abgelehnt werden sollen", bad)
		}
	}
	got, err := s.Collections(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "privat" || got[1].Name != "team-x" ||
		got[1].Description != "Team X" || got[1].CreatedBy != Admin || got[1].CreatedAt == 0 {
		t.Errorf("Collections = %+v", got)
	}
	if err := s.SetCollection(ctx, "privat", desc("Eigenes")); err != nil {
		t.Fatal(err)
	}
	if err := s.SetCollection(ctx, "fehlt", desc("x")); !errors.Is(err, ErrNotFound) {
		t.Errorf("set auf Fehlendes: %v", err)
	}
	got, _ = s.Collections(ctx)
	if got[0].Description != "Eigenes" {
		t.Errorf("Beschreibung = %q", got[0].Description)
	}
	if err := s.RemoveCollection(ctx, "privat"); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveCollection(ctx, "privat"); !errors.Is(err, ErrNotFound) {
		t.Errorf("rm auf Fehlendes: %v", err)
	}
}

func TestRemoveCollectionInUse(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	for _, c := range []string{"a", "b", "c"} {
		if err := s.AddCollection(ctx, c, "", false); err != nil {
			t.Fatal(err)
		}
	}
	// a: erlaubt für einen Node.
	if _, err := s.AddNode(ctx, "laptop", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.Grant(ctx, "laptop", "a"); err != nil {
		t.Fatal(err)
	}
	err := s.RemoveCollection(ctx, "a")
	if !errors.Is(err, ErrInUse) || !strings.Contains(err.Error(), "laptop") {
		t.Errorf("rm mit Recht: %v", err)
	}
	// b: nur eine Löschmarke.
	insertDoc(t, s, "1", "b", "weg.md", 1)
	if err := s.RemoveCollection(ctx, "b"); !errors.Is(err, ErrInUse) {
		t.Errorf("rm mit Löschmarke: %v", err)
	}
	// c: nur eine SYSTEM:-Zeile.
	insertDoc(t, s, "2", "c", "SYSTEM:A:kleist", 0)
	if err := s.RemoveCollection(ctx, "c"); !errors.Is(err, ErrInUse) {
		t.Errorf("rm mit SYSTEM:-Zeile: %v", err)
	}
	got, _ := s.Collections(ctx)
	if len(got) != 3 {
		t.Errorf("Collections = %+v", got)
	}
}

func TestNodes(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	token, err := s.AddNode(ctx, "laptop", "Notebook")
	if err != nil {
		t.Fatal(err)
	}
	if err := ident.CheckToken(token); err != nil {
		t.Errorf("Token-Format: %v", err)
	}
	if _, err := s.AddNode(ctx, "laptop", ""); !errors.Is(err, ErrExists) {
		t.Errorf("doppelt: %v", err)
	}
	for _, bad := range []string{"Laptop", "a:b", "system1", "-x"} {
		if _, err := s.AddNode(ctx, bad, ""); err == nil {
			t.Errorf("%q hätte abgelehnt werden sollen", bad)
		}
	}
	n, err := s.Node(ctx, "laptop")
	if err != nil {
		t.Fatal(err)
	}
	if n.TokenHash != ident.HashToken(token) || n.Locked || n.Description != "Notebook" || n.CreatedBy != Admin {
		t.Errorf("Node = %+v", n)
	}
	// Gespeichert ist nur der Hash, nirgends das Token.
	var hits int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM nodes WHERE token_hash = ? OR description = ?`,
		token, token).Scan(&hits); err != nil || hits != 0 {
		t.Errorf("Token im Klartext gespeichert: %d, %v", hits, err)
	}

	if err := s.SetNodeLocked(ctx, "laptop", true); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.Node(ctx, "laptop"); !n.Locked {
		t.Error("nicht gesperrt")
	}
	if err := s.SetNodeLocked(ctx, "laptop", false); err != nil {
		t.Fatal(err)
	}
	if err := s.SetNodeLocked(ctx, "fehlt", true); !errors.Is(err, ErrNotFound) {
		t.Errorf("lock auf Fehlendes: %v", err)
	}

	token2, err := s.NewNodeToken(ctx, "laptop")
	if err != nil {
		t.Fatal(err)
	}
	if token2 == token {
		t.Fatal("neues Token gleich dem alten")
	}
	if n, _ := s.Node(ctx, "laptop"); n.TokenHash != ident.HashToken(token2) {
		t.Error("Hash nicht ersetzt")
	}
	if _, err := s.NewNodeToken(ctx, "fehlt"); !errors.Is(err, ErrNotFound) {
		t.Errorf("token auf Fehlendes: %v", err)
	}
	if _, err := s.Node(ctx, "fehlt"); !errors.Is(err, ErrNotFound) {
		t.Errorf("show auf Fehlendes: %v", err)
	}
}

func TestGrants(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	if _, err := s.AddNode(ctx, "laptop", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.Grant(ctx, "laptop", "team-x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("grant ohne Collection: %v", err)
	}
	if err := s.AddCollection(ctx, "team-x", "", false); err != nil {
		t.Fatal(err)
	}
	if err := s.Grant(ctx, "fehlt", "team-x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("grant ohne Node: %v", err)
	}
	if err := s.Grant(ctx, "laptop", "team-x"); err != nil {
		t.Fatal(err)
	}
	if err := s.Grant(ctx, "laptop", "team-x"); !errors.Is(err, ErrExists) {
		t.Errorf("grant doppelt: %v", err)
	}
	if n, _ := s.Node(ctx, "laptop"); !reflect.DeepEqual(n.Collections, []string{"team-x"}) {
		t.Errorf("Collections = %v", n.Collections)
	}
	nodes, err := s.Nodes(ctx)
	if err != nil || len(nodes) != 1 || !reflect.DeepEqual(nodes[0].Collections, []string{"team-x"}) {
		t.Errorf("Nodes = %+v, %v", nodes, err)
	}
	if err := s.Revoke(ctx, "laptop", "team-x"); err != nil {
		t.Fatal(err)
	}
	if err := s.Revoke(ctx, "laptop", "team-x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("revoke doppelt: %v", err)
	}
	// rm entfernt die Rechte mit.
	if err := s.Grant(ctx, "laptop", "team-x"); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveNode(ctx, "laptop"); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveNode(ctx, "laptop"); !errors.Is(err, ErrNotFound) {
		t.Errorf("rm doppelt: %v", err)
	}
	tables, _ := s.Tables(ctx)
	if len(tables.Grants) != 0 || len(tables.Nodes) != 0 {
		t.Errorf("Reste: %+v", tables)
	}
	// Danach lässt sich die Collection entfernen.
	if err := s.RemoveCollection(ctx, "team-x"); err != nil {
		t.Error(err)
	}
}

// Ein Account-Name ist auch als Node-Name belegt; eine Zeile SYSTEM:A:<name>
// allein belegt nichts mehr — geprüft wird über die Tabellen.
func TestNodeNameTakenByAccount(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	if _, err := s.AddAccount(ctx, "laptop", "laptop", ""); err != nil {
		t.Fatal(err)
	}
	_, err := s.AddNode(ctx, "laptop", "")
	if err == nil || !strings.Contains(err.Error(), "Account") {
		t.Fatalf("AddNode: %v", err)
	}
	if _, err := s.Node(ctx, "laptop"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Node trotzdem angelegt: %v", err)
	}
	insertDoc(t, s, "1", "irgendwo", "SYSTEM:A:desktop", 1)
	if _, err := s.AddNode(ctx, "desktop", ""); err != nil {
		t.Error(err)
	}
}

// set ändert nur die Beschreibung; Rechte und Token-Hash bleiben.
func TestSetKeepsGrantsAndToken(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	if err := s.AddCollection(ctx, "team-x", "alt", false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddNode(ctx, "laptop", "alt"); err != nil {
		t.Fatal(err)
	}
	if err := s.Grant(ctx, "laptop", "team-x"); err != nil {
		t.Fatal(err)
	}
	before, _ := s.Node(ctx, "laptop")
	if err := s.SetNodeDescription(ctx, "laptop", "neu"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetCollection(ctx, "team-x", desc("neu")); err != nil {
		t.Fatal(err)
	}
	after, _ := s.Node(ctx, "laptop")
	if after.Description != "neu" || after.TokenHash != before.TokenHash ||
		!reflect.DeepEqual(after.Collections, []string{"team-x"}) || after.CreatedAt != before.CreatedAt {
		t.Errorf("vorher %+v, nachher %+v", before, after)
	}
	colls, _ := s.Collections(ctx)
	if colls[0].Description != "neu" {
		t.Errorf("Collection = %+v", colls[0])
	}
	if err := s.SetNodeDescription(ctx, "fehlt", "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("set auf Fehlendes: %v", err)
	}
}

func TestActions(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	steps := []func() error{
		func() error { return s.AddCollection(ctx, "team-x", "", false) },
		func() error { return s.SetCollection(ctx, "team-x", desc("d")) },
		func() error { _, err := s.AddNode(ctx, "laptop", ""); return err },
		func() error { return s.SetNodeDescription(ctx, "laptop", "d") },
		func() error { return s.Grant(ctx, "laptop", "team-x") },
		func() error { return s.SetNodeLocked(ctx, "laptop", true) },
		func() error { return s.SetNodeLocked(ctx, "laptop", false) },
		func() error { _, err := s.NewNodeToken(ctx, "laptop"); return err },
		func() error { return s.Revoke(ctx, "laptop", "team-x") },
		func() error { return s.RemoveNode(ctx, "laptop") },
		func() error { return s.RemoveCollection(ctx, "team-x") },
		// Gescheiterte Änderungen schreiben nichts.
		func() error { _ = s.RemoveCollection(ctx, "fehlt"); return nil },
	}
	for i, step := range steps {
		if err := step(); err != nil {
			t.Fatalf("Schritt %d: %v", i, err)
		}
	}
	want := []struct{ action, subject string }{
		{"collection.add", "team-x"},
		{"collection.set", "team-x"},
		{"node.add", "laptop"},
		{"node.set", "laptop"},
		{"node.grant", "laptop:team-x"},
		{"node.lock", "laptop"},
		{"node.unlock", "laptop"},
		{"node.token", "laptop"},
		{"node.revoke", "laptop:team-x"},
		{"node.rm", "laptop"},
		{"collection.rm", "team-x"},
	}
	got := actions(t, s)
	if len(got) != len(want) {
		t.Fatalf("%d Zeilen in actions, erwartet %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		g := got[i]
		if g.account != Admin || g.action != w.action || g.subject.String != w.subject {
			t.Errorf("actions[%d] = %+v, erwartet %s %s", i, g, w.action, w.subject)
		}
	}
	// Keine Revision: lokale Tabellen gleichen sich nicht ab.
	if info, _ := s.Info(ctx); info.Revision != 0 {
		t.Errorf("Revision = %d", info.Revision)
	}
}

func TestCheckTables(t *testing.T) {
	hash := ident.HashToken("keph_x")
	ok := Tables{
		Collections: []Collection{{Name: "a", CreatedBy: Admin}},
		Nodes:       []Node{{Name: "n", TokenHash: hash, CreatedBy: Admin}},
		Grants:      []Grant{{Node: "n", Collection: "a"}},
	}
	if err := CheckTables(ok); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(t *Tables){
		"Name":          func(t *Tables) { t.Collections[0].Name = "A" },
		"doppelt":       func(t *Tables) { t.Collections = append(t.Collections, t.Collections[0]) },
		"Hash":          func(t *Tables) { t.Nodes[0].TokenHash = "keph_x" },
		"Node-Name":     func(t *Tables) { t.Nodes[0].Name = "system" },
		"Recht Node":    func(t *Tables) { t.Grants[0].Node = "x" },
		"Recht Coll":    func(t *Tables) { t.Grants[0].Collection = "x" },
		"Recht doppelt": func(t *Tables) { t.Grants = append(t.Grants, t.Grants[0]) },
		"created_by":    func(t *Tables) { t.Nodes[0].CreatedBy = "" },
	}
	for name, mod := range cases {
		tt := Tables{
			Collections: append([]Collection{}, ok.Collections...),
			Nodes:       append([]Node{}, ok.Nodes...),
			Grants:      append([]Grant{}, ok.Grants...),
		}
		mod(&tt)
		if err := CheckTables(tt); err == nil {
			t.Errorf("%s: hätte scheitern sollen", name)
		}
	}
}

// desc ist eine Änderung nur der Beschreibung.
func desc(d string) CollectionChange { return CollectionChange{Description: &d} }
