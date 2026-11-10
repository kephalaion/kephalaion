package store

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/kephalaion/kephalaion/internal/contract"
	"github.com/kephalaion/kephalaion/internal/hub/source"
)

// Der Neubau von docs/ (Task 033): Soll-Zustand schreiben, nur Geändertes,
// Verwaistes löschen; ohne docs/ nichts zu tun, in einer Collection mit
// Quelle abgelehnt.
func TestRebuildDocs(t *testing.T) {
	f := newWriteStore(t)
	ctx := context.Background()
	s := f.s
	by := source.Actor{Account: "bob", Carrier: "laptop"}

	// Ohne docs/: nichts zu tun, keine Revision.
	before := revision(t, s)
	res, err := s.RebuildDocs(ctx, "a", by)
	if err != nil || !res.NoDocs || res.Revision != 0 || revision(t, s) != before {
		t.Fatalf("ohne docs/: %+v, %v", res, err)
	}
	_, err = s.RebuildDocs(ctx, "gibtsnicht", by)
	wantKind(t, "unbekannt", err, ErrNotFound, "")
	if err := s.AddCollection(ctx, "k-playbook", "", false); err != nil {
		t.Fatal(err)
	}
	_, err = s.RebuildDocs(ctx, "k-playbook", by)
	wantKind(t, "Quelle", err, ErrForbidden, "fetch")

	for _, n := range []string{"docs/manual/setup.md", "docs/a/x.md"} {
		if _, err := s.CreateDocumentAs(ctx, f.as("bob"), "a", n, "---\ntitle: T "+n+"\n---\n"); err != nil {
			t.Fatal(err)
		}
	}
	res, err = s.RebuildDocs(ctx, "a", by)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"docs/README.md", "docs/a/README.md", "docs/a/index.md", "docs/a/x.md", "docs/generated/README.md",
		"docs/generated/index.md", "docs/index.md", "docs/manual/README.md", "docs/manual/index.md",
		"docs/manual/setup.md"}
	if got := liveUnder(t, s, "a", "docs"); !reflect.DeepEqual(got, want) {
		t.Errorf("docs/:\n%v\nerwartet\n%v", got, want)
	}
	if res.NoDocs || res.Created != 8 || res.Updated != 0 || res.Deleted != 0 || res.Revision != revision(t, s) {
		t.Errorf("erster Neubau: %+v", res)
	}
	idx, _ := s.Document(ctx, "a", "docs/index.md")
	if idx.CreatedBy != contract.SystemAuthor || !strings.HasPrefix(idx.Content, "---\nokf_version: \"0.2\"\n---\n") ||
		!strings.Contains(idx.Content, "* [a](a/) - *no description*") {
		t.Errorf("docs/index.md: %+v", idx)
	}
	// actions: je Dokument der anstoßende Account mit dem Node, dazu der
	// Vorgang als Ganzes.
	acts := docActionsAll(t, s)
	last := acts[len(acts)-1]
	if last.action != "create" || last.account != "bob" || last.carrier != "laptop" || last.revision != res.Revision {
		t.Errorf("actions: %+v", last)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM actions WHERE action = 'collection.rebuild_docs' AND subject = 'a'
		AND account = 'bob' AND carrier = 'laptop' AND revision = ?`, res.Revision).Scan(&n); err != nil || n != 1 {
		t.Errorf("collection.rebuild_docs: %d, %v", n, err)
	}

	// Ein zweiter Neubau ändert nichts.
	again, err := s.RebuildDocs(ctx, "a", by)
	if err != nil || again.Revision != 0 || again.Created+again.Updated+again.Deleted != 0 || again.NoDocs {
		t.Errorf("zweiter Neubau: %+v, %v", again, err)
	}

	// Die Mahnung von manual ersetzt bob; der Neubau lässt sie stehen und
	// nimmt ihre Beschreibung in den Index. docs/a umbenannt nach docs/b:
	// docs/b bekommt index.md und Mahnung, docs/a ist weg.
	if _, err := s.WriteDocumentAs(ctx, f.as("bob"), "a", "docs/manual/README.md",
		"---\ntitle: Handbuch\ndescription: Von Hand.\n---\n", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RenameDocumentAs(ctx, f.as("bob"), "a", "docs/a", "docs/b", nil); err != nil {
		t.Fatal(err)
	}
	if got := liveUnder(t, s, "a", "docs/b"); !reflect.DeepEqual(got, []string{"docs/b/x.md"}) {
		t.Errorf("nach rename: %v", got)
	}
	res, err = s.RebuildDocs(ctx, "a", by)
	if err != nil {
		t.Fatal(err)
	}
	if res.Created != 2 || res.Updated != 1 || res.Deleted != 0 {
		t.Errorf("nach rename: %+v", res)
	}
	idx, _ = s.Document(ctx, "a", "docs/index.md")
	if !strings.Contains(idx.Content, "* [Handbuch](manual/) - Von Hand.") || !strings.Contains(idx.Content, "(b/)") ||
		strings.Contains(idx.Content, "(a/)") {
		t.Errorf("docs/index.md:\n%s", idx.Content)
	}
	if d, _ := s.Document(ctx, "a", "docs/manual/README.md"); d.UpdatedBy != "kleist" {
		t.Errorf("README.md des Users ersetzt: %+v", d)
	}

	// docs/b/x.md gelöscht: index.md und Mahnung von docs/b sind verwaist.
	if _, err := s.DeleteDocumentAs(ctx, f.as("bob"), "a", "docs/b/x.md", nil, false); err != nil {
		t.Fatal(err)
	}
	res, err = s.RebuildDocs(ctx, "a", by)
	if err != nil {
		t.Fatal(err)
	}
	if res.Deleted != 2 || res.Updated != 1 || res.Created != 0 {
		t.Errorf("verwaist: %+v", res)
	}
	if got := liveUnder(t, s, "a", "docs/b"); len(got) != 0 {
		t.Errorf("docs/b/ bleibt: %v", got)
	}
	// Die festen Verzeichnisse bleiben, auch leer.
	if _, err := s.Document(ctx, "a", "docs/generated/index.md"); err != nil {
		t.Errorf("docs/generated/index.md: %v", err)
	}
}

// putLegacy legt ein Dokument an wie ein Bestand aus der Zeit vor der
// Sperre: ohne die Prüfungen der Schreibwege, nur mit der Pfadregel.
func putLegacy(t *testing.T, s *sqliteStore, collection, name, by string) {
	t.Helper()
	ctx := context.Background()
	sqlTx, tx, err := s.begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sqlTx.Rollback() }()
	w := newDocTx(tx, by, Admin, "")
	if _, err := w.create(ctx, collection, DocumentInput{Name: name, Content: "# " + name + "\n"}); err != nil {
		t.Fatal(err)
	}
	if err := sqlTx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// Review-Befund zu Task 033: Ein Bestand, der unter docs/ index.md oder
// README.md als Verzeichnis nutzt, blockiert den Neubau nicht mehr. Der
// Neubau übergeht diese Dokumente und legt auf die belegten Namen keine
// Datei; alles andere baut er. Die Dokumente bleiben änderbar, löschbar und
// lassen sich hinaus umbenennen; danach stellt der Neubau her, was fehlte.
func TestRebuildDocsReservedLegacy(t *testing.T) {
	f := newWriteStore(t)
	ctx := context.Background()
	s := f.s
	by := source.Actor{Account: "bob", Carrier: "laptop"}
	for _, n := range []string{"docs/index.md/x.md", "docs/README.md/y.md", "docs/a/x.md", "docs/a/index.md/z.md",
		"docs/b/README.md/w.md", "docs/c/y.md", "docs/c/README.md/v.md"} {
		putLegacy(t, s, "a", n, "kleist")
	}
	// Eine alte index.md und eine Mahnung darunter: Dokumente des Systems,
	// die der Neubau als verwaist löscht.
	putLegacy(t, s, "a", "docs/a/index.md/index.md", "kleist")
	putLegacy(t, s, "a", "docs/c/README.md/README.md", contract.SystemAuthor)

	res, err := s.RebuildDocs(ctx, "a", by)
	if err != nil {
		t.Fatalf("Neubau mit Bestand: %v", err)
	}
	if res.Deleted != 2 || res.Revision == 0 {
		t.Errorf("Neubau mit Bestand: %+v", res)
	}
	want := []string{"docs/README.md/y.md", "docs/a/README.md", "docs/a/index.md/z.md", "docs/a/x.md",
		"docs/b/README.md/w.md", "docs/c/README.md/v.md", "docs/c/index.md", "docs/c/y.md",
		"docs/generated/README.md", "docs/generated/index.md", "docs/index.md/x.md", "docs/manual/README.md",
		"docs/manual/index.md"}
	if got := liveUnder(t, s, "a", "docs"); !reflect.DeepEqual(got, want) {
		t.Errorf("docs/:\n%v\nerwartet\n%v", got, want)
	}
	again, err := s.RebuildDocs(ctx, "a", by)
	if err != nil || again.Revision != 0 {
		t.Errorf("zweiter Neubau: %+v, %v", again, err)
	}

	// bob (User kleist) räumt auf: ändern, löschen, hinaus umbenennen.
	if _, err := s.WriteDocumentAs(ctx, f.as("bob"), "a", "docs/index.md/x.md", "neu", nil); err != nil {
		t.Errorf("write: %v", err)
	}
	if _, err := s.RenameDocumentAs(ctx, f.as("bob"), "a", "docs/index.md/x.md", "docs/x.md", nil); err != nil {
		t.Errorf("rename hinaus: %v", err)
	}
	if _, err := s.DeleteDocumentAs(ctx, f.as("bob"), "a", "docs/README.md", nil, true); err != nil {
		t.Errorf("delete: %v", err)
	}
	if _, err := s.RenameDocumentAs(ctx, f.as("bob"), "a", "docs/a/index.md", "docs/a/sub", nil); err != nil {
		t.Errorf("rename Verzeichnis: %v", err)
	}
	// Innerhalb des belegten Namens entsteht nichts Neues.
	_, err = s.RenameDocumentAs(ctx, f.as("bob"), "a", "docs/c/README.md/v.md", "docs/c/README.md/u.md", nil)
	wantKind(t, "rename innerhalb", err, ErrGenerated, "docs/c/README.md wäre ein Verzeichnis")

	if _, err := s.RebuildDocs(ctx, "a", by); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"docs/index.md", "docs/README.md", "docs/a/index.md", "docs/a/sub/index.md"} {
		if d, err := s.Document(ctx, "a", n); err != nil || d.UpdatedBy != contract.SystemAuthor {
			t.Errorf("%s: %+v, %v", n, d, err)
		}
	}
	idx, _ := s.Document(ctx, "a", "docs/index.md")
	if !strings.Contains(idx.Content, "* [a](a/)") || !strings.Contains(idx.Content, "* [c](c/) - *no description*") ||
		!strings.Contains(idx.Content, "(x.md)") || strings.Contains(idx.Content, "(b/)") {
		t.Errorf("docs/index.md:\n%s", idx.Content)
	}
}
