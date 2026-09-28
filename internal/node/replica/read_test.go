package replica

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/oklog/ulid/v2"

	"github.com/kephalaion/kephalaion/internal/contract"
	"github.com/kephalaion/kephalaion/internal/sqlitedb"
)

// readReplica legt eine Replica mit Zeilen an: rows je Collection mit
// Revision; sync_state jeder Collection auf until.
func readReplica(t *testing.T, until int64, rows ...contract.Row) *Replica {
	t.Helper()
	ctx := context.Background()
	r, err := Create(ctx, filepath.Join(t.TempDir(), "replicas", "keph.db"), ulid.Make().String(), ulid.Make().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	p := page{rows: rows, advance: map[string]int64{}, until: until, now: 1}
	for _, row := range rows {
		p.advance[row.Collection] = 0
	}
	if _, err := r.apply(ctx, p); err != nil {
		t.Fatal(err)
	}
	return r
}

// row ist eine Zeile mit Revision rev; angelegt zu Zeit created, geändert zu
// rev·1000.
func row(id, coll, name string, rev, created int64, content string) contract.Row {
	return contract.Row{ID: id, Collection: coll, Name: name, Content: &content, Revision: rev,
		CreatedAt: created, CreatedBy: "anna", UpdatedAt: rev * 1000, UpdatedBy: "bert"}
}

func tomb(id, coll, name string, rev int64) contract.Row {
	return contract.Row{ID: id, Collection: coll, Name: name, Deleted: true, Revision: rev,
		CreatedAt: 1, CreatedBy: "anna", UpdatedAt: rev * 1000, UpdatedBy: "bert"}
}

// Die generation wechselt bei jeder Neuanlage und jedem reset — auch bei
// gleicher hub_id —, sonst nicht; ohne sie ist die Replica unlesbar.
func TestGeneration(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "replicas", "keph.db")
	hubID, entryID := ulid.Make().String(), ulid.Make().String()
	r, err := Create(ctx, path, hubID, entryID)
	if err != nil {
		t.Fatal(err)
	}
	g1 := r.Generation()
	if g1 == "" {
		t.Fatal("ohne generation")
	}
	if err := r.reset(ctx, hubID); err != nil {
		t.Fatal(err)
	}
	g2 := r.Generation()
	_ = r.Close()
	if g2 == g1 {
		t.Error("reset mit gleicher hub_id behält die generation")
	}
	r, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if r.Generation() != g2 {
		t.Errorf("nach Öffnen %s, erwartet %s", r.Generation(), g2)
	}
	_ = r.Close()
	if err := Remove(path); err != nil {
		t.Fatal(err)
	}
	r, err = Create(ctx, path, hubID, entryID)
	if err != nil {
		t.Fatal(err)
	}
	if r.Generation() == g2 || r.Generation() == g1 {
		t.Error("Neuanlage behält die generation")
	}
	_ = r.Close()

	db, err := sqlitedb.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM db_info WHERE key = ?`, KeyGeneration); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if _, err := Open(ctx, path); !errors.Is(err, errNoIDs) || !unreadable(err) {
		t.Errorf("ohne generation: %v", err)
	}
}

func TestEntryLookups(t *testing.T) {
	ctx := context.Background()
	acc := "{}"
	r := readReplica(t, 9,
		row("A", "wissen", "a.md", 1, 10, "äh"), // 3 Bytes
		row("B", "wissen", "dir/b.md", 2, 20, "b"),
		tomb("C", "wissen", "weg.md", 3),
		row("D", "wissen", contract.AccountRowName("bob"), 4, 1, acc),
		row("E", "wissen", "dir/sub/c.md", 5, 30, ""),
		// Zwei lebende Zeilen gleichen Namens: Es gilt die jüngste.
		row("F", "wissen", "doppelt.md", 6, 40, "alt"),
		row("G", "wissen", "doppelt.md", 7, 50, "neu"),
		row("H", "andere", "a.md", 8, 60, "x"),
	)
	e, ok, err := r.EntryByName(ctx, "wissen", "a.md", true)
	if err != nil || !ok || e.ID != "A" || e.Size != 3 || e.Content == nil || *e.Content != "äh" ||
		e.CreatedAt != 10 || e.CreatedBy != "anna" || e.UpdatedAt != 1000 || e.UpdatedBy != "bert" || e.Revision != 1 {
		t.Errorf("a.md: %+v, %v, %v", e, ok, err)
	}
	if e, ok, err := r.EntryByName(ctx, "wissen", "a.md", false); err != nil || !ok || e.Content != nil || e.Size != 3 {
		t.Errorf("a.md ohne Inhalt: %+v, %v, %v", e, ok, err)
	}
	if e, ok, _ := r.EntryByName(ctx, "wissen", "doppelt.md", true); !ok || e.ID != "G" || *e.Content != "neu" {
		t.Errorf("doppelt.md: %+v", e)
	}
	for _, name := range []string{"weg.md", "dir", "fehlt.md"} {
		if _, ok, err := r.EntryByName(ctx, "wissen", name, true); ok || err != nil {
			t.Errorf("%s: %v, %v", name, ok, err)
		}
	}
	if _, _, err := r.EntryByName(ctx, "wissen", contract.AccountRowName("bob"), true); err == nil {
		t.Error("SYSTEM:-Name ohne Fehler")
	}
	if e, ok, _ := r.EntryByID(ctx, "C", true); !ok || !e.Deleted || e.Content != nil || e.Size != 0 {
		t.Errorf("Löschmarke per id: %+v, %v", e, ok)
	}
	if e, ok, _ := r.EntryByID(ctx, "E", true); !ok || e.Name != "dir/sub/c.md" || e.Content == nil || *e.Content != "" {
		t.Errorf("per id: %+v, %v", e, ok)
	}
	for _, id := range []string{"D", "fehlt"} {
		if _, ok, err := r.EntryByID(ctx, id, true); ok || err != nil {
			t.Errorf("id %s: %v, %v", id, ok, err)
		}
	}
	for prefix, want := range map[string]bool{"dir/": true, "dir/sub/": true, "di/": false, "a.md/": false, "weg.md/": false} {
		if got, err := r.HasUnder(ctx, "wissen", prefix); err != nil || got != want {
			t.Errorf("HasUnder %s: %v, %v", prefix, got, err)
		}
	}
	if dirs, err := r.ChildDirs(ctx, "wissen", ""); err != nil || !reflect.DeepEqual(dirs, []string{"dir"}) {
		t.Errorf("ChildDirs: %v, %v", dirs, err)
	}
	if dirs, err := r.ChildDirs(ctx, "wissen", "dir/"); err != nil || !reflect.DeepEqual(dirs, []string{"sub"}) {
		t.Errorf("ChildDirs dir/: %v, %v", dirs, err)
	}
	if rev, ok, err := r.StateOf(ctx, "wissen"); err != nil || !ok || rev != 9 {
		t.Errorf("StateOf: %d, %v, %v", rev, ok, err)
	}
	if _, ok, err := r.StateOf(ctx, "fehlt"); err != nil || ok {
		t.Errorf("StateOf fehlt: %v, %v", ok, err)
	}
}

// ChangedEntries liest nach (Revision, id) weiter, auch mitten in einer
// Revision, bis upto; FirstRevisionSince findet die erste Zeile ab einer
// Zeit des Hubs.
func TestChangedEntries(t *testing.T) {
	ctx := context.Background()
	r := readReplica(t, 3,
		row("A", "wissen", "a.md", 1, 1, "a"),
		row("B", "wissen", "d/b.md", 2, 1, "b"),
		row("C", "wissen", "d/c.md", 2, 1, "c"),
		tomb("D", "wissen", "weg.md", 3),
		row("S", "wissen", contract.AccountRowName("bob"), 3, 1, "{}"),
		row("X", "andere", "x.md", 3, 1, "x"),
	)
	ids := func(es []Entry) string {
		s := ""
		for _, e := range es {
			s += e.ID
		}
		return s
	}
	cases := []struct {
		after  ChangeKey
		prefix string
		upto   int64
		limit  int
		want   string
	}{
		{ChangeKey{}, "", 3, 10, "ABCD"},
		{ChangeKey{Rev: 1}, "", 3, 10, "BCD"},
		{ChangeKey{Rev: 2, ID: "B"}, "", 3, 10, "CD"},
		{ChangeKey{Rev: 2}, "", 3, 10, "D"},
		{ChangeKey{}, "", 2, 10, "ABC"},
		{ChangeKey{}, "", 3, 2, "AB"},
		{ChangeKey{}, "d/", 3, 10, "BC"},
	}
	for _, c := range cases {
		got, err := r.ChangedEntries(ctx, "wissen", c.prefix, c.after, c.upto, c.limit)
		if err != nil || ids(got) != c.want {
			t.Errorf("%+v: %s, %v", c, ids(got), err)
		}
	}
	for since, want := range map[int64]int64{0: 1, 1500: 2, 3000: 3} {
		if rev, ok, err := r.FirstRevisionSince(ctx, "wissen", "", since); err != nil || !ok || rev != want {
			t.Errorf("since %d: %d, %v, %v", since, rev, ok, err)
		}
	}
	if _, ok, err := r.FirstRevisionSince(ctx, "wissen", "", 3001); ok || err != nil {
		t.Errorf("nach allem: %v, %v", ok, err)
	}
}

// HeadByName und HeadByID liefern den Anfang eines Inhalts in Bytes, nur von
// lebenden Zeilen ohne SYSTEM:; bei zwei lebenden Zeilen eines Namens die
// jüngste.
func TestHead(t *testing.T) {
	ctx := context.Background()
	r := readReplica(t, 6,
		row("A", "wissen", "a.md", 1, 1, "---\ntitle: alt\n---\n"),
		row("B", "wissen", "a.md", 2, 1, "---\ntitle: neu\n---\nText äöü"),
		row("C", "wissen", "leer.md", 3, 1, ""),
		tomb("D", "wissen", "weg.md", 4),
		row("S", "wissen", contract.AccountRowName("bob"), 5, 1, "{}"),
	)
	head, ok, err := r.HeadByName(ctx, "wissen", "a.md", 100)
	if err != nil || !ok || string(head) != "---\ntitle: neu\n---\nText äöü" {
		t.Errorf("a.md: %q, %v, %v", head, ok, err)
	}
	// Bytes, nicht Zeichen: ä ist zwei Bytes, der Schnitt liegt mitten darin.
	head, ok, err = r.HeadByName(ctx, "wissen", "a.md", 25)
	if err != nil || !ok || len(head) != 25 || string(head) != "---\ntitle: neu\n---\nText \xc3" {
		t.Errorf("a.md gekürzt: %q, %v, %v", head, ok, err)
	}
	head, ok, err = r.HeadByID(ctx, "B", 3)
	if err != nil || !ok || string(head) != "---" {
		t.Errorf("per id: %q, %v, %v", head, ok, err)
	}
	if head, ok, err = r.HeadByName(ctx, "wissen", "leer.md", 10); err != nil || !ok || len(head) != 0 {
		t.Errorf("leer: %q, %v, %v", head, ok, err)
	}
	// Per id zählt die Zeile selbst, auch die ältere zweite eines Namens.
	if head, ok, err := r.HeadByID(ctx, "A", 100); err != nil || !ok || string(head) != "---\ntitle: alt\n---\n" {
		t.Errorf("A: %q, %v, %v", head, ok, err)
	}
	for _, c := range []struct{ id, name string }{{"D", "weg.md"}, {"S", ""}, {"X", "fehlt.md"}} {
		if c.id != "" {
			if _, ok, err := r.HeadByID(ctx, c.id, 10); ok || err != nil {
				t.Errorf("id %s: %v, %v", c.id, ok, err)
			}
		}
		if c.name != "" {
			if _, ok, err := r.HeadByName(ctx, "wissen", c.name, 10); ok || err != nil {
				t.Errorf("Name %s: %v, %v", c.name, ok, err)
			}
		}
	}
	if _, _, err := r.HeadByName(ctx, "wissen", contract.AccountRowName("bob"), 10); err == nil {
		t.Error("SYSTEM:-Name ohne Fehler")
	}
}
