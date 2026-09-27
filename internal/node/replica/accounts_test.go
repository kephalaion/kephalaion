package replica

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/oklog/ulid/v2"

	"github.com/kephalaion/kephalaion/internal/contract"
	"github.com/kephalaion/kephalaion/internal/ident"
	"github.com/kephalaion/kephalaion/internal/node/store"
)

func accountRow(t *testing.T, collection, account, token string, rev int64, r contract.Rights) contract.Row {
	t.Helper()
	content, err := contract.EncodeAccountContent(contract.AccountContent{Hash: ident.HashToken(token), User: account, Rights: r})
	if err != nil {
		t.Fatal(err)
	}
	return contract.Row{ID: ulid.Make().String(), Collection: collection, Name: contract.AccountRowName(account),
		Content: &content, Revision: rev, CreatedAt: 1, CreatedBy: "admin", UpdatedAt: 1, UpdatedBy: "admin"}
}

func (e *env) hubEntry() store.Hub {
	e.t.Helper()
	h, err := e.nodes.Hub(context.Background(), "privat")
	if err != nil {
		e.t.Fatal(err)
	}
	return h
}

// rotate schreibt Zeilen in die Replica, nur die gewünschter Collections, und
// legt sie an, wenn es sie nicht gibt; AccountRows liest sie über den Index.
func TestWriteAccountRows(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, "a")
	rows := []contract.Row{
		accountRow(t, "a", "bob", "keph_neu", 7, contract.Rights{Write: true}),
		accountRow(t, "b", "bob", "keph_neu", 7, contract.Rights{}),
	}
	written, reset, err := WriteAccountRows(ctx, e.nodes, e.hubEntry(), e.hub.id, rows)
	if err != nil || reset != "" || len(written) != 1 || written[0] != "a" {
		t.Fatalf("WriteAccountRows = %v, %q, %v", written, reset, err)
	}
	if h := e.hubEntry(); h.HubID != e.hub.id {
		t.Errorf("Kopie der hub_id = %q", h.HubID)
	}
	r, err := Open(ctx, e.nodes.ReplicaPath("privat"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.AccountRows(ctx, "bob")
	if err != nil || len(got) != 1 || got[0].Collection != "a" {
		t.Errorf("AccountRows = %+v, %v", got, err)
	}
	if other, _ := r.AccountRows(ctx, "alice"); len(other) != 0 {
		t.Errorf("alice: %+v", other)
	}
	var plan strings.Builder
	qrows, err := r.db.QueryContext(ctx, "EXPLAIN QUERY PLAN "+qAccountRows, "SYSTEM:A:bob")
	if err != nil {
		t.Fatal(err)
	}
	for qrows.Next() {
		var id, parent, notused int
		var detail string
		if err := qrows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatal(err)
		}
		plan.WriteString(detail + "\n")
	}
	qrows.Close()
	if !strings.Contains(plan.String(), "documents_system") {
		t.Errorf("AccountRows ohne Index documents_system:\n%s", plan.String())
	}
	_ = r.Close()

	// Nichts Gewünschtes: nichts geschrieben.
	written, _, err = WriteAccountRows(ctx, e.nodes, e.hubEntry(), e.hub.id, rows[1:])
	if err != nil || len(written) != 0 {
		t.Errorf("nur b: %v, %v", written, err)
	}
	// Andere hub_id: die Replica wird geleert, dann geschrieben.
	e.put(t)
	written, reset, err = WriteAccountRows(ctx, e.nodes, e.hubEntry(), ulid.Make().String(), rows)
	if err != nil || len(written) != 1 || !strings.Contains(reset, "hub_id gewechselt") {
		t.Errorf("andere hub_id: %v, %q, %v", written, reset, err)
	}
	if n := e.countRows(t); n != 1 {
		t.Errorf("%d Zeilen nach dem Wechsel, erwartet nur die Account-Zeile", n)
	}
	// Keine Account-Zeile: abgelehnt.
	bad := rows[0]
	bad.Name = "x.md"
	if _, _, err := WriteAccountRows(ctx, e.nodes, e.hubEntry(), e.hub.id, []contract.Row{bad}); err == nil {
		t.Error("Dokument als Account-Zeile angenommen")
	}
}

// put gleicht einmal ab, damit die Replica ein Dokument hat.
func (e *env) put(t *testing.T) {
	t.Helper()
	e.hub.put("a", "doc.md", "x")
	if res := e.run(); res.Err != nil {
		t.Fatal(res.Err)
	}
}

func (e *env) countRows(t *testing.T) int {
	t.Helper()
	r, err := Open(context.Background(), e.nodes.ReplicaPath("privat"))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var n int
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM documents`).Scan(&n); err != nil && err != sql.ErrNoRows {
		t.Fatal(err)
	}
	return n
}

func TestAdoptHubID(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, "a")
	// Ohne Replica: nur die Kopie.
	reset, err := AdoptHubID(ctx, e.nodes, e.hubEntry(), e.hub.id)
	if err != nil || reset != "" || e.hubEntry().HubID != e.hub.id {
		t.Fatalf("erster Kontakt: %q, %v, %q", reset, err, e.hubEntry().HubID)
	}
	e.put(t)
	if reset, err := AdoptHubID(ctx, e.nodes, e.hubEntry(), e.hub.id); err != nil || reset != "" || e.countRows(t) != 1 {
		t.Errorf("gleiche hub_id: %q, %v", reset, err)
	}
	other := ulid.Make().String()
	reset, err = AdoptHubID(ctx, e.nodes, e.hubEntry(), other)
	if err != nil || !strings.Contains(reset, "hub_id gewechselt") || e.countRows(t) != 0 || e.hubEntry().HubID != other {
		t.Errorf("andere hub_id: %q, %v", reset, err)
	}
}

// docRow ist eine Zeile, wie ein Schreibvorgang sie liefert.
func docRow(id, collection, name string, content *string, rev int64) contract.Row {
	return contract.Row{ID: id, Collection: collection, Name: name, Content: content, Deleted: content == nil,
		Revision: rev, CreatedAt: 1, CreatedBy: "kleist", UpdatedAt: rev, UpdatedBy: "kleist"}
}

// Die Zeilen eines Schreibvorgangs kommen sofort in die Replica: nur
// gewünschte Collections, die die Replica schon führt, nie zurück, ohne den
// Stand zu ändern. Fehlt die Replica oder passt die hub_id nicht, schreibt es
// nichts (ErrChanged); SYSTEM:-Zeilen nimmt es nicht an.
func TestWriteRows(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, "a", "b")
	h := e.hubEntry()
	id := ulid.Make().String()
	if err := WriteRows(ctx, e.nodes, h, e.hub.id, []contract.Row{docRow(id, "a", "x.md", str("eins"), 5)}); !errors.Is(err, ErrChanged) {
		t.Fatalf("ohne Replica: %v", err)
	}
	e.hub.allowed = map[string]bool{"a": true}
	e.put(t)
	h = e.hubEntry()
	before := e.states()

	rows := []contract.Row{docRow(id, "a", "x.md", str("eins"), 5), docRow(ulid.Make().String(), "c", "y.md", str("fremd"), 5)}
	if err := WriteRows(ctx, e.nodes, h, e.hub.id, rows); err != nil {
		t.Fatal(err)
	}
	if got := e.names("a"); got["x.md"] != "eins" || got["doc.md"] != "x" {
		t.Errorf("nach create: %v", got)
	}
	if after := e.states(); !reflect.DeepEqual(after, before) {
		t.Errorf("Stand geändert: %v → %v", before, after)
	}
	if _, ok := e.allRows()[rows[1].ID]; ok {
		t.Error("Zeile einer nicht gewünschten Collection geschrieben")
	}
	// b ist gewünscht, aber die Replica führt sie nicht (der Hub erlaubt sie
	// nicht): übergangen.
	other := docRow(ulid.Make().String(), "b", "z.md", str("z"), 6)
	if err := WriteRows(ctx, e.nodes, h, e.hub.id, []contract.Row{other}); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.allRows()[other.ID]; ok {
		t.Error("Zeile einer Collection ohne Stand geschrieben")
	}
	// Nie zurück: eine ältere Revision ersetzt die neuere nicht; die
	// Löschmarke mit neuerer schon.
	if err := WriteRows(ctx, e.nodes, h, e.hub.id, []contract.Row{docRow(id, "a", "x.md", str("alt"), 4)}); err != nil {
		t.Fatal(err)
	}
	if got := e.names("a"); got["x.md"] != "eins" {
		t.Errorf("ältere Revision geschrieben: %v", got)
	}
	if err := WriteRows(ctx, e.nodes, h, e.hub.id, []contract.Row{docRow(id, "a", "x.md", nil, 7)}); err != nil {
		t.Fatal(err)
	}
	if got := e.names("a"); len(got) != 1 {
		t.Errorf("nach delete: %v", got)
	}
	// Andere hub_id: nichts geschrieben.
	late := docRow(ulid.Make().String(), "a", "w.md", str("w"), 8)
	if err := WriteRows(ctx, e.nodes, h, ulid.Make().String(), []contract.Row{late}); !errors.Is(err, ErrChanged) {
		t.Errorf("andere hub_id: %v", err)
	}
	// Ein anderer Eintrag gleichen Namens: nichts geschrieben.
	stale := h
	stale.EntryID = ulid.Make().String()
	if err := WriteRows(ctx, e.nodes, stale, e.hub.id, []contract.Row{late}); !errors.Is(err, ErrChanged) {
		t.Errorf("anderer Eintrag: %v", err)
	}
	// Eine SYSTEM:-Zeile ist kein Dokument.
	acc := accountRow(t, "a", "bob", "keph_x", 9, contract.Rights{})
	if err := WriteRows(ctx, e.nodes, h, e.hub.id, []contract.Row{acc}); err == nil {
		t.Error("Account-Zeile als Dokument angenommen")
	}
	if _, ok := e.allRows()[late.ID]; ok {
		t.Error("abgelehnte Zeile geschrieben")
	}
}

// Die Zeilen eines rename ersetzen die alten per id: Der alte Name ist
// sofort weg, der neue da — ein Dokument wie ein ganzes Verzeichnis, ohne
// den Stand zu ändern.
func TestWriteRowsRename(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, "a")
	e.hub.allowed = map[string]bool{"a": true}
	e.put(t)
	h := e.hubEntry()
	x, d1, d2 := ulid.Make().String(), ulid.Make().String(), ulid.Make().String()
	if err := WriteRows(ctx, e.nodes, h, e.hub.id, []contract.Row{docRow(x, "a", "x.md", str("x"), 5),
		docRow(d1, "a", "dir/a.md", str("a"), 6), docRow(d2, "a", "dir/sub/b.md", str("b"), 6)}); err != nil {
		t.Fatal(err)
	}
	before := e.states()
	renamed := []contract.Row{docRow(x, "a", "archiv/x.md", str("x"), 7)}
	if err := WriteRows(ctx, e.nodes, h, e.hub.id, renamed); err != nil {
		t.Fatal(err)
	}
	dir := []contract.Row{docRow(d1, "a", "neu/a.md", str("a"), 8), docRow(d2, "a", "neu/sub/b.md", str("b"), 8)}
	if err := WriteRows(ctx, e.nodes, h, e.hub.id, dir); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"doc.md": "x", "archiv/x.md": "x", "neu/a.md": "a", "neu/sub/b.md": "b"}
	if got := e.names("a"); !reflect.DeepEqual(got, want) {
		t.Errorf("nach rename: %v", got)
	}
	rep := e.replica()
	if _, err := rep.Document(ctx, "a", "x.md"); !errors.Is(err, ErrNotFound) {
		t.Errorf("alter Name: %v", err)
	}
	if d, err := rep.Document(ctx, "a", "neu/sub/b.md"); err != nil || d.ID != d2 || d.Revision != 8 {
		t.Errorf("neuer Name: %+v, %v", d, err)
	}
	if after := e.states(); !reflect.DeepEqual(after, before) {
		t.Errorf("Stand geändert: %v → %v", before, after)
	}
}
