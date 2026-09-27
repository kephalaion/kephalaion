package store

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/kephalaion/kephalaion/internal/contract"
)

// createAll legt Dokumente als auth an, je einen Schreibvorgang, und liefert
// die ids nach Name.
func createAll(t *testing.T, s *sqliteStore, auth WriteAuth, collection string, names ...string) map[string]string {
	t.Helper()
	ids := map[string]string{}
	for _, n := range names {
		res, err := s.CreateDocumentAs(context.Background(), auth, collection, n, "Inhalt von "+n)
		if err != nil {
			t.Fatalf("%s anlegen: %v", n, err)
		}
		ids[n] = res.Rows[0].ID
	}
	return ids
}

// rowNames sind die Namen der Zeilen einer Antwort.
func rowNames(rows []contract.Row) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Name
	}
	return out
}

// liveNames sind die Namen der lebenden Dokumente einer Collection.
func liveNames(t *testing.T, s Store, collection string) []string {
	t.Helper()
	docs, err := s.Documents(context.Background(), collection, "")
	if err != nil {
		t.Fatal(err)
	}
	out := []string{}
	for _, d := range docs {
		out = append(out, d.Name)
	}
	return out
}

// rename eines Dokuments: die id, der Inhalt und created_by bleiben, der Name
// ist neu, unter neuer Revision; actions nennt rename mit Account und Node.
func TestRenameDocumentAs(t *testing.T) {
	ctx := context.Background()
	f := newWriteStore(t)
	s := f.s
	ids := createAll(t, s, f.as("bob"), "a", "x.md")
	before := syncRow(t, s, "a", ids["x.md"])

	res, err := s.RenameDocumentAs(ctx, f.as("bob2"), "a", "x.md", "neu/y.md", rev(before.Revision))
	if err != nil {
		t.Fatal(err)
	}
	if res.Revision != before.Revision+1 || len(res.Rows) != 1 {
		t.Fatalf("rename: %+v", res)
	}
	r := res.Rows[0]
	if r.ID != before.ID || r.Name != "neu/y.md" || *r.Content != *before.Content || r.Revision != res.Revision ||
		r.CreatedBy != "kleist" || r.CreatedAt != before.CreatedAt || r.UpdatedBy != "kleist" || r.Deleted {
		t.Errorf("Zeile nach rename: %+v", r)
	}
	if got := syncRow(t, s, "a", r.ID); !reflect.DeepEqual(got, r) {
		t.Errorf("Antwort %+v, Abgleich %+v", r, got)
	}
	if _, err := s.Document(ctx, "a", "x.md"); !errors.Is(err, ErrNotFound) {
		t.Errorf("alter Name: %v", err)
	}
	if d, err := s.Document(ctx, "a", "neu/y.md"); err != nil || d.ID != before.ID {
		t.Errorf("neuer Name: %+v, %v", d, err)
	}
	actions := docActionsAll(t, s)
	if last := actions[len(actions)-1]; last != (docAction{"rename", r.ID, "bob2", "laptop", res.Revision}) {
		t.Errorf("actions: %+v", last)
	}

	// Rechte: Fremdes nur mit supersede; eve hat kein write.
	if _, err := s.PutDocument(ctx, "a", "admin.md", "vom Admin"); err != nil {
		t.Fatal(err)
	}
	u := snapshot(t, s)
	if _, err := s.RenameDocumentAs(ctx, f.as("alice"), "a", "admin.md", "alice.md", nil); !errors.Is(err, ErrForbidden) ||
		!strings.Contains(err.Error(), "umbenennen: gehört admin, supersede fehlt") {
		t.Errorf("alice benennt Fremdes um: %v", err)
	}
	u.check("forbidden")
	if res, err := s.RenameDocumentAs(ctx, f.as("eve"), "a", "admin.md", "abgelöst.md", nil); err != nil ||
		res.Rows[0].CreatedBy != Admin || res.Rows[0].UpdatedBy != "eve" {
		t.Errorf("eve benennt Fremdes mit supersede um: %+v, %v", res, err)
	}

	// Eine Löschmarke am Ziel hindert nicht.
	gone := createAll(t, s, f.as("bob"), "a", "weg.md")
	if _, err := s.DeleteDocumentAs(ctx, f.as("bob"), "a", "weg.md", nil, false); err != nil {
		t.Fatal(err)
	}
	moved, err := s.RenameDocumentAs(ctx, f.as("bob"), "a", "neu/y.md", "weg.md", nil)
	if err != nil || moved.Rows[0].ID != before.ID {
		t.Fatalf("auf Löschmarke: %+v, %v", moved, err)
	}
	if old := syncRow(t, s, "a", gone["weg.md"]); !old.Deleted || old.Name != "weg.md" {
		t.Errorf("Löschmarke: %+v", old)
	}
}

// Was rename eines Dokuments ablehnt, ohne etwas zu ändern: Ziel belegt,
// Datei und Verzeichnis zugleich, ungültige Namen, veraltete Revision, nicht
// vorhanden.
func TestRenameDocumentRejected(t *testing.T) {
	ctx := context.Background()
	f := newWriteStore(t)
	s := f.s
	bob := f.as("bob")
	createAll(t, s, bob, "a", "x.md", "y.md", "dir/a.md", "p/q.md")
	if _, err := s.PutDocument(ctx, "a", "admin.md", "vom Admin"); err != nil {
		t.Fatal(err)
	}
	cur := revision(t, s)
	x, _ := s.Document(ctx, "a", "x.md")
	cases := []struct {
		name, newName string
		base          *int64
		want          error
		text          string
	}{
		{"x.md", "y.md", nil, ErrNameTaken, "Dokument y.md gibt es in a schon"},
		{"x.md", "admin.md", nil, ErrNameTaken, "admin.md"},
		{"x.md", "dir", nil, ErrPathConflict, "ein Verzeichnis mit 1 Dokumenten"},
		{"x.md", "y.md/z.md", nil, ErrPathConflict, "y.md ist in a ein Dokument"},
		// Die Quelle im Ziel belegt es selbst: p ist ein Verzeichnis.
		{"p/q.md", "p", nil, ErrPathConflict, "p ist in a ein Verzeichnis"},
		{"x.md", "x.md", nil, ErrInvalid, "der neue Name ist der alte"},
		{"x.md", "x.md/y", nil, ErrInvalid, "liegt darunter"},
		{"x.md", "SYSTEM:A:bob", nil, ErrInvalid, "neuer Name"},
		{"x.md", "", nil, ErrInvalid, "neuer Name"},
		{"x.md", "/z.md", nil, ErrInvalid, "relativer Pfad"},
		{"SYSTEM:A:bob", "z.md", nil, ErrInvalid, "vorbehalten"},
		{"x.md", "z.md", rev(x.Revision - 1), ErrStaleRevision, "hat Revision " + strconv.FormatInt(x.Revision, 10)},
		{"fehlt.md", "z.md", nil, ErrNotFound, "weder als Dokument noch als Verzeichnis"},
		// Recht vor Vorbedingung vor Ziel.
		{"admin.md", "y.md", rev(1), ErrForbidden, "supersede fehlt"},
		{"x.md", "y.md", rev(1), ErrStaleRevision, ""},
	}
	for _, c := range cases {
		u := snapshot(t, s)
		_, err := s.RenameDocumentAs(ctx, bob, "a", c.name, c.newName, c.base)
		if !errors.Is(err, c.want) || !strings.Contains(err.Error(), c.text) {
			t.Errorf("%s → %s: %v, erwartet %v mit %q", c.name, c.newName, err, c.want, c.text)
		}
		u.check(c.name + " → " + c.newName)
	}
	if revision(t, s) != cur {
		t.Error("Revision gestiegen")
	}
	if rows := accountRowsAll(t, s, "bob"); len(rows) != 2 || rows[0].deleted {
		t.Errorf("Zeilen von bob: %+v", rows)
	}
}

// rename eines Verzeichnisses: alle Dokumente darunter bekommen den neuen
// Präfix, unter einer Revision, jede id bleibt; ein fremdes Dokument ohne
// supersede mittendrin lässt alles scheitern.
func TestRenameDirectoryAs(t *testing.T) {
	ctx := context.Background()
	f := newWriteStore(t)
	s := f.s
	ids := createAll(t, s, f.as("bob"), "a", "dir/a.md", "dir/sub/b.md", "dir/z.md", "dirx.md", "dir2/c.md")
	if _, err := s.PutDocument(ctx, "a", "dir/sub/m.md", "vom Admin"); err != nil {
		t.Fatal(err)
	}
	ids["dir/sub/m.md"] = mustDoc(t, s, "a", "dir/sub/m.md").ID

	// Ein fremdes Dokument ohne supersede mittendrin: nichts geändert.
	u := snapshot(t, s)
	if _, err := s.RenameDocumentAs(ctx, f.as("bob"), "a", "dir", "neu", nil); !errors.Is(err, ErrForbidden) ||
		!strings.Contains(err.Error(), "dir/sub/m.md in a umbenennen: gehört admin, supersede fehlt") {
		t.Errorf("fremdes Dokument mittendrin: %v", err)
	}
	u.check("fremdes Dokument mittendrin")
	all := []string{"dir/a.md", "dir/sub/b.md", "dir/sub/m.md", "dir/z.md", "dir2/c.md", "dirx.md"}
	if got := liveNames(t, s, "a"); !reflect.DeepEqual(got, all) {
		t.Errorf("nach Ablehnung: %v", got)
	}

	// eve hat supersede: Alles ist für sie fremd, und sie darf.
	res, err := s.RenameDocumentAs(ctx, f.as("eve"), "a", "dir", "neu/tief", nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"neu/tief/a.md", "neu/tief/sub/b.md", "neu/tief/sub/m.md", "neu/tief/z.md"}
	if res.Revision != u.rev+1 || !reflect.DeepEqual(rowNames(res.Rows), want) {
		t.Fatalf("rename: Revision %d, Zeilen %v", res.Revision, rowNames(res.Rows))
	}
	for i, r := range res.Rows {
		old := strings.Replace(want[i], "neu/tief", "dir", 1)
		if r.ID != ids[old] || r.Revision != res.Revision || r.UpdatedBy != "eve" || r.Deleted ||
			*r.Content != contentOf(old) {
			t.Errorf("Zeile %s: %+v", want[i], r)
		}
		if got := syncRow(t, s, "a", r.ID); !reflect.DeepEqual(got, r) {
			t.Errorf("Antwort %+v, Abgleich %+v", r, got)
		}
	}
	if got := liveNames(t, s, "a"); !reflect.DeepEqual(got, []string{"dir2/c.md", "dirx.md", "neu/tief/a.md",
		"neu/tief/sub/b.md", "neu/tief/sub/m.md", "neu/tief/z.md"}) {
		t.Errorf("nach rename: %v", got)
	}
	var renames []docAction
	for _, a := range docActionsAll(t, s) {
		if a.action == "rename" {
			renames = append(renames, a)
		}
	}
	if len(renames) != 4 {
		t.Fatalf("actions: %+v", renames)
	}
	for _, a := range renames {
		if a.account != "eve" || a.carrier != "laptop" || a.revision != res.Revision {
			t.Errorf("action: %+v", a)
		}
	}
}

func mustDoc(t *testing.T, s Store, collection, name string) Document {
	t.Helper()
	d, err := s.Document(context.Background(), collection, name)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func contentOf(name string) string {
	if name == "dir/sub/m.md" {
		return "vom Admin"
	}
	return "Inhalt von " + name
}

// Was rename eines Verzeichnisses ablehnt: kein Zusammenlegen mit einem
// vorhandenen Verzeichnis, Datei und Verzeichnis zugleich, keine
// Vorbedingung, jeder neue Name nach den Pfadregeln.
func TestRenameDirectoryRejected(t *testing.T) {
	ctx := context.Background()
	f := newWriteStore(t)
	s := f.s
	bob := f.as("bob")
	long := strings.Repeat("s", 250)
	deep := "d/" + strings.Join([]string{long, long, long, long}, "/")
	createAll(t, s, bob, "a", "dir/a.md", "dir/sub/b.md", "ziel/x.md", "datei.md", "x/y/a.md", deep)
	cases := []struct {
		name, newName string
		base          *int64
		want          error
		text          string
	}{
		{"dir", "ziel", nil, ErrNameTaken, "Verzeichnis ziel gibt es in a schon, mit 1 Dokumenten; umbenennen legt nicht zusammen"},
		{"dir", "datei.md", nil, ErrPathConflict, "Verzeichnis datei.md: datei.md ist in a ein Dokument"},
		{"dir", "datei.md/unter", nil, ErrPathConflict, "datei.md ist in a ein Dokument"},
		// Die Quelle im Ziel belegt es selbst.
		{"x/y", "x", nil, ErrNameTaken, "Verzeichnis x gibt es in a schon"},
		{"dir", "dir/sub/neu", nil, ErrInvalid, "liegt darunter"},
		{"dir", "dir", nil, ErrInvalid, "der neue Name ist der alte"},
		{"dir", "neu", rev(1), ErrInvalid, "dir ist in a ein Verzeichnis; base_revision gilt nur für Dokumente"},
		{"d", "e" + strings.Repeat("e", 30), nil, ErrInvalid, "länger als 1024 Bytes"},
		{"leer", "neu", nil, ErrNotFound, "weder als Dokument noch als Verzeichnis"},
	}
	for _, c := range cases {
		u := snapshot(t, s)
		_, err := s.RenameDocumentAs(ctx, bob, "a", c.name, c.newName, c.base)
		if !errors.Is(err, c.want) || !strings.Contains(err.Error(), c.text) {
			t.Errorf("%s → %s: %v, erwartet %v mit %q", c.name, c.newName, err, c.want, c.text)
		}
		u.check(c.name + " → " + c.newName)
	}
	// Ohne write auch Eigenes nicht, ohne Zeile nicht lesbar.
	if _, err := s.RenameDocumentAs(ctx, f.as("leser"), "a", "dir", "neu", nil); !errors.Is(err, ErrForbidden) {
		t.Errorf("leser: %v", err)
	}
	if _, err := s.RenameDocumentAs(ctx, bob, "c", "dir", "neu", nil); !errors.Is(err, ErrNotReadable) {
		t.Errorf("Collection c: %v", err)
	}
}

// delete eines Verzeichnisses: nur mit recursive, dann alle Dokumente
// darunter Löschmarken unter einer Revision, alles oder nichts; Nachbarn mit
// gleichem Präfix bleiben.
func TestDeleteDirectoryAs(t *testing.T) {
	ctx := context.Background()
	f := newWriteStore(t)
	s := f.s
	bob := f.as("bob")
	ids := createAll(t, s, bob, "a", "dir/a.md", "dir/sub/b.md", "dirx.md", "dir2/c.md", "einzeln.md")
	if _, err := s.PutDocument(ctx, "a", "dir/sub/m.md", "vom Admin"); err != nil {
		t.Fatal(err)
	}

	rejects := []struct {
		what      string
		auth      WriteAuth
		name      string
		base      *int64
		recursive bool
		want      error
		text      string
	}{
		{"ohne recursive", bob, "dir", nil, false, ErrInvalid, "dir ist in a ein Verzeichnis mit 3 Dokumenten; löschen nur mit recursive"},
		{"Unterverzeichnis ohne recursive", bob, "dir/sub", nil, false, ErrInvalid, "Verzeichnis mit 2 Dokumenten"},
		{"mit base_revision", bob, "dir", rev(1), true, ErrInvalid, "base_revision gilt nur für Dokumente"},
		{"fremdes Dokument mittendrin", bob, "dir", nil, true, ErrForbidden, "dir/sub/m.md in a löschen: gehört admin, supersede fehlt"},
		{"leser", f.as("leser"), "dir", nil, true, ErrForbidden, "supersede fehlt"},
		{"weder noch", bob, "fehlt", nil, true, ErrNotFound, "weder als Dokument noch als Verzeichnis"},
		{"Präfix ohne /", bob, "di", nil, true, ErrNotFound, ""},
		// Die Wurzel einer Collection ist kein Name.
		{"Wurzel", bob, "", nil, true, ErrInvalid, "Name fehlt"},
		{"Wurzel mit /", bob, "/", nil, true, ErrInvalid, ""},
		{"Verzeichnis mit /", bob, "dir/", nil, true, ErrInvalid, ""},
	}
	for _, c := range rejects {
		u := snapshot(t, s)
		_, err := s.DeleteDocumentAs(ctx, c.auth, "a", c.name, c.base, c.recursive)
		if !errors.Is(err, c.want) || !strings.Contains(err.Error(), c.text) {
			t.Errorf("%s: %v, erwartet %v mit %q", c.what, err, c.want, c.text)
		}
		u.check(c.what)
	}

	// Mit supersede: alles darunter, eine Revision, nach Name.
	before := revision(t, s)
	res, err := s.DeleteDocumentAs(ctx, f.as("eve"), "a", "dir", nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Revision != before+1 || !reflect.DeepEqual(rowNames(res.Rows), []string{"dir/a.md", "dir/sub/b.md", "dir/sub/m.md"}) {
		t.Fatalf("delete: Revision %d, Zeilen %v", res.Revision, rowNames(res.Rows))
	}
	for _, r := range res.Rows {
		if !r.Deleted || r.Content != nil || r.Revision != res.Revision || r.UpdatedBy != "eve" {
			t.Errorf("Löschmarke: %+v", r)
		}
		if got := syncRow(t, s, "a", r.ID); !reflect.DeepEqual(got, r) {
			t.Errorf("Antwort %+v, Abgleich %+v", r, got)
		}
	}
	if res.Rows[0].ID != ids["dir/a.md"] {
		t.Errorf("id: %+v", res.Rows[0])
	}
	if got := liveNames(t, s, "a"); !reflect.DeepEqual(got, []string{"dir2/c.md", "dirx.md", "einzeln.md"}) {
		t.Errorf("nach delete: %v", got)
	}
	n := 0
	for _, a := range docActionsAll(t, s) {
		if a.action == "delete" {
			n++
			if a.revision != res.Revision || a.account != "eve" {
				t.Errorf("action: %+v", a)
			}
		}
	}
	if n != 3 {
		t.Errorf("%d Zeilen delete in actions", n)
	}
	// Danach nur Löschmarken: weder Dokument noch Verzeichnis.
	if _, err := s.DeleteDocumentAs(ctx, bob, "a", "dir", nil, true); !errors.Is(err, ErrNotFound) {
		t.Errorf("nochmals: %v", err)
	}
	// recursive gilt für ein Dokument nicht: es wird gelöscht wie ohne.
	one, err := s.DeleteDocumentAs(ctx, bob, "a", "einzeln.md", nil, true)
	if err != nil || len(one.Rows) != 1 || one.Rows[0].ID != ids["einzeln.md"] || !one.Rows[0].Deleted {
		t.Errorf("Dokument mit recursive: %+v, %v", one, err)
	}
	if rows := accountRowsAll(t, s, "bob"); len(rows) != 2 || rows[0].deleted {
		t.Errorf("Zeilen von bob: %+v", rows)
	}
}
