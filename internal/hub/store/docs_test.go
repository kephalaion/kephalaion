package store

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/kephalaion/kephalaion/internal/contract"
	"github.com/kephalaion/kephalaion/internal/hub/source"
)

// Schreibschutz unter docs/ (Task 033): erzeugte Dateien, Mahnungen und die
// Verzeichnisvorgänge nach docs/vertrag.md, „Erzeugte Dateien unter docs/“.

// putSystem schreibt ein Dokument als system, wie es der Neubau tut.
func putSystem(t *testing.T, s *sqliteStore, collection, name, content string) {
	t.Helper()
	ctx := context.Background()
	sqlTx, tx, err := s.begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sqlTx.Rollback() }()
	w := newDocTx(tx, contract.SystemAuthor, Admin, "")
	if _, err := w.put(ctx, collection, DocumentInput{Name: name, Content: content}); err != nil {
		t.Fatal(err)
	}
	if err := sqlTx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// liveUnder sind die Namen der lebenden Dokumente unter dir, nach Name.
func liveUnder(t *testing.T, s Store, collection, dir string) []string {
	t.Helper()
	docs, err := s.Documents(context.Background(), collection, dir)
	if err != nil {
		t.Fatal(err)
	}
	out := []string{}
	for _, d := range docs {
		out = append(out, d.Name)
	}
	return out
}

func wantKind(t *testing.T, what string, err, kind error, msg string) {
	t.Helper()
	if !errors.Is(err, kind) {
		t.Fatalf("%s: %v, erwartet %v", what, err, kind)
	}
	if msg != "" && !strings.Contains(err.Error(), msg) {
		t.Errorf("%s: Meldung %q ohne %q", what, err, msg)
	}
}

// Jeder Einzelvorgang auf einer erzeugten Datei ist ErrGenerated, gleich
// mit welchem Recht; außerhalb von docs/ ist der Name frei.
func TestGeneratedSingleOps(t *testing.T) {
	f := newWriteStore(t)
	ctx := context.Background()
	s := f.s
	putSystem(t, s, "a", "docs/index.md", "# Directories\n")
	putSystem(t, s, "a", "docs/README.md", "# Docs\n")
	putSystem(t, s, "a", "docs/x/index.md", "# Documents\n")
	if _, err := s.CreateDocumentAs(ctx, f.as("bob"), "a", "docs/x/a.md", "a"); err != nil {
		t.Fatal(err)
	}
	for _, acc := range []string{"bob", "eve"} {
		as := f.as(acc)
		_, err := s.CreateDocumentAs(ctx, as, "a", "docs/y/index.md", "x")
		wantKind(t, acc+" create index.md", err, ErrGenerated, "erzeugte Datei")
		_, err = s.WriteDocumentAs(ctx, as, "a", "docs/index.md", "x", nil)
		wantKind(t, acc+" write docs/index.md", err, ErrGenerated, "")
		_, err = s.WriteDocumentAs(ctx, as, "a", "docs/README.md", "x", nil)
		wantKind(t, acc+" write docs/README.md", err, ErrGenerated, "")
		_, err = s.DeleteDocumentAs(ctx, as, "a", "docs/x/index.md", nil, false)
		wantKind(t, acc+" delete", err, ErrGenerated, "")
		_, err = s.RenameDocumentAs(ctx, as, "a", "docs/x/index.md", "docs/x/alt.md", nil)
		wantKind(t, acc+" rename alter Name", err, ErrGenerated, "")
	}
	_, err := s.RenameDocumentAs(ctx, f.as("bob"), "a", "docs/x/a.md", "docs/x/sub/index.md", nil)
	wantKind(t, "rename neuer Name", err, ErrGenerated, "docs/x/sub/index.md")
	_, err = s.CreateDocumentAs(ctx, f.as("bob"), "a", "docs/README.md", "x")
	wantKind(t, "create docs/README.md", err, ErrGenerated, "")
	// Außerhalb von docs/ und in einem tieferen docs ist der Name frei.
	for _, name := range []string{"notizen/index.md", "a/docs/index.md", "README.md", "index.md"} {
		if _, err := s.CreateDocumentAs(ctx, f.as("bob"), "a", name, "x"); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if got := liveUnder(t, s, "a", "docs"); !reflect.DeepEqual(got, []string{"docs/README.md", "docs/index.md",
		"docs/x/a.md", "docs/x/index.md"}) {
		t.Errorf("docs/: %v", got)
	}
}

// Eine Mahnung ersetzt jeder mit write, ohne supersede, und übernimmt sie
// damit; wer supersede hat, ebenso. create scheitert am belegten Namen,
// ein rename einzeln ist ErrGenerated, delete geht mit demselben Recht.
func TestNoticeTakeOver(t *testing.T) {
	f := newWriteStore(t)
	ctx := context.Background()
	s := f.s
	for _, d := range []string{"m1", "m2", "m3", "m4"} {
		putSystem(t, s, "a", "docs/"+d+"/README.md", "---\ngenerated:\n  by: kephalaion\n---\n")
	}
	_, err := s.CreateDocumentAs(ctx, f.as("alice"), "a", "docs/m1/README.md", "x")
	wantKind(t, "create auf Mahnung", err, ErrNameTaken, "")
	_, err = s.WriteDocumentAs(ctx, f.as("leser"), "a", "docs/m1/README.md", "x", nil)
	wantKind(t, "leser", err, ErrForbidden, "write fehlt")

	// alice hat write, kein supersede.
	res, err := s.WriteDocumentAs(ctx, f.as("alice"), "a", "docs/m1/README.md", "# m1\n", nil)
	if err != nil {
		t.Fatal(err)
	}
	if r := res.Rows[0]; r.CreatedBy != "alice" || r.UpdatedBy != "alice" {
		t.Errorf("übernommen: created_by %s, updated_by %s", r.CreatedBy, r.UpdatedBy)
	}
	// Danach ihr eigenes Dokument: ein zweites write geht, bob braucht
	// supersede.
	if _, err := s.WriteDocumentAs(ctx, f.as("alice"), "a", "docs/m1/README.md", "# m1 neu\n", nil); err != nil {
		t.Fatal(err)
	}
	_, err = s.WriteDocumentAs(ctx, f.as("bob"), "a", "docs/m1/README.md", "x", nil)
	wantKind(t, "bob auf alices README", err, ErrForbidden, "gehört alice")

	// eve hat supersede, kein write.
	if _, err := s.WriteDocumentAs(ctx, f.as("eve"), "a", "docs/m2/README.md", "# m2\n", nil); err != nil {
		t.Fatal(err)
	}
	// Löschen mit write.
	if _, err := s.DeleteDocumentAs(ctx, f.as("alice"), "a", "docs/m3/README.md", nil, false); err != nil {
		t.Fatal(err)
	}
	// Einzeln umbenennen: nie.
	_, err = s.RenameDocumentAs(ctx, f.as("bob"), "a", "docs/m4/README.md", "docs/m4/alt.md", nil)
	wantKind(t, "rename Mahnung", err, ErrGenerated, "Mahnung")
	// Eine README.md, die nicht system zuletzt schrieb, ist keine Mahnung.
	if _, err := s.CreateDocumentAs(ctx, f.as("bob"), "a", "docs/eigen/README.md", "x"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RenameDocumentAs(ctx, f.as("bob"), "a", "docs/eigen/README.md", "docs/eigen/alt.md", nil); err != nil {
		t.Fatal(err)
	}
	// Der Admin ersetzt eine Mahnung wie jede Datei und übernimmt sie.
	if _, err := s.PutDocument(ctx, "a", "docs/m4/README.md", "# m4\n"); err != nil {
		t.Fatal(err)
	}
	if d, _ := s.Document(ctx, "a", "docs/m4/README.md"); d.CreatedBy != Admin {
		t.Errorf("Admin übernimmt nicht: %s", d.CreatedBy)
	}
}

// delete mit recursive nimmt erzeugte Dateien und Mahnungen mit; das Recht
// an ihnen ist das, eine Mahnung zu ersetzen.
func TestDeleteRecursiveTakesSystemDocs(t *testing.T) {
	f := newWriteStore(t)
	ctx := context.Background()
	s := f.s
	if _, err := s.CreateDocumentAs(ctx, f.as("bob"), "a", "docs/a/x.md", "x"); err != nil {
		t.Fatal(err)
	}
	putSystem(t, s, "a", "docs/a/index.md", "i")
	putSystem(t, s, "a", "docs/a/README.md", "m")
	putSystem(t, s, "a", "docs/nur/index.md", "i")
	putSystem(t, s, "a", "docs/nur/README.md", "m")

	_, err := s.DeleteDocumentAs(ctx, f.as("leser"), "a", "docs/nur", nil, true)
	wantKind(t, "leser", err, ErrForbidden, "write fehlt")
	res, err := s.DeleteDocumentAs(ctx, f.as("bob"), "a", "docs/a", nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 3 {
		t.Errorf("Zeilen: %+v", res.Rows)
	}
	for _, r := range res.Rows {
		if !r.Deleted || r.Revision != res.Revision {
			t.Errorf("%s: %+v", r.Name, r)
		}
	}
	// eve hat nur supersede: Das genügt für das Verzeichnis aus
	// Dokumenten des Systems.
	if _, err := s.DeleteDocumentAs(ctx, f.as("eve"), "a", "docs/nur", nil, true); err != nil {
		t.Fatal(err)
	}
	if got := liveUnder(t, s, "a", "docs"); len(got) != 0 {
		t.Errorf("docs/ nicht leer: %v", got)
	}
}

// rename eines Verzeichnisses innerhalb von docs/ und aus docs/ hinaus:
// index.md und Mahnung bekommen eine Löschmarke, die übrigen wandern; die
// Zeilen der Antwort tragen erst die neuen Namen, dann die Löschmarken.
func TestRenameDirDropsSystemDocs(t *testing.T) {
	for _, target := range []string{"docs/b", "alt/a"} {
		t.Run(target, func(t *testing.T) {
			f := newWriteStore(t)
			ctx := context.Background()
			s := f.s
			for _, n := range []string{"docs/a/x.md", "docs/a/sub/y.md", "docs/a/sub/README.md"} {
				if _, err := s.CreateDocumentAs(ctx, f.as("bob"), "a", n, "x"); err != nil {
					t.Fatal(err)
				}
			}
			putSystem(t, s, "a", "docs/a/index.md", "i")
			putSystem(t, s, "a", "docs/a/README.md", "m")
			putSystem(t, s, "a", "docs/a/sub/index.md", "i")

			_, err := s.RenameDocumentAs(ctx, f.as("leser"), "a", "docs/a", target, nil)
			wantKind(t, "leser", err, ErrForbidden, "")
			res, err := s.RenameDocumentAs(ctx, f.as("bob"), "a", "docs/a", target, nil)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, r := range res.Rows {
				mark := ""
				if r.Deleted {
					mark = " (gelöscht)"
				}
				got = append(got, r.Name+mark)
			}
			want := []string{target + "/sub/README.md", target + "/sub/y.md", target + "/x.md",
				"docs/a/README.md (gelöscht)", "docs/a/index.md (gelöscht)", "docs/a/sub/index.md (gelöscht)"}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("Zeilen:\n%v\nerwartet\n%v", got, want)
			}
			if names := liveUnder(t, s, "a", target); !reflect.DeepEqual(names, want[:3]) {
				t.Errorf("unter %s: %v", target, names)
			}
			if names := liveUnder(t, s, "a", "docs/a"); len(names) != 0 {
				t.Errorf("docs/a/ bleibt: %v", names)
			}
		})
	}
}

// rename nach docs/ hinein: abgelehnt, wenn eine erzeugte Datei entstünde —
// eine index.md in beliebiger Tiefe, eine README.md, die zu docs/README.md
// würde —; sonst wie jedes rename.
func TestRenameIntoDocs(t *testing.T) {
	f := newWriteStore(t)
	ctx := context.Background()
	s := f.s
	for _, n := range []string{"notizen/x.md", "notizen/sub/index.md", "entwurf/README.md", "entwurf/y.md",
		"sauber/README.md", "sauber/z.md"} {
		if _, err := s.CreateDocumentAs(ctx, f.as("bob"), "a", n, "x"); err != nil {
			t.Fatal(err)
		}
	}
	_, err := s.RenameDocumentAs(ctx, f.as("bob"), "a", "notizen", "docs/notizen", nil)
	wantKind(t, "index.md hinein", err, ErrGenerated, "notizen/sub/index.md")
	_, err = s.RenameDocumentAs(ctx, f.as("bob"), "a", "entwurf", "docs", nil)
	wantKind(t, "README.md zu docs/README.md", err, ErrGenerated, "docs/README.md")
	// entwurf als Unterverzeichnis: README.md beschreibt es, erlaubt.
	if _, err := s.RenameDocumentAs(ctx, f.as("bob"), "a", "entwurf", "docs/entwurf", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RenameDocumentAs(ctx, f.as("bob"), "a", "sauber", "docs/sauber", nil); err != nil {
		t.Fatal(err)
	}
	if got := liveUnder(t, s, "a", "notizen"); len(got) != 2 {
		t.Errorf("notizen/ verändert: %v", got)
	}
	// Die README.md eines Users unter docs/ ist keine Mahnung.
	if d, _ := s.Document(ctx, "a", "docs/entwurf/README.md"); contract.IsNotice(d.Name, d.UpdatedBy) {
		t.Error("README.md gilt als Mahnung")
	}
}

// Der Admin schreibt keine erzeugte Datei; der Import übergeht sie und
// nennt sie.
func TestGeneratedAdmin(t *testing.T) {
	f := newWriteStore(t)
	ctx := context.Background()
	s := f.s
	_, err := s.PutDocument(ctx, "a", "docs/index.md", "x")
	wantKind(t, "put", err, ErrGenerated, "rebuild-docs")
	putSystem(t, s, "a", "docs/README.md", "x")
	_, err = s.DeleteDocument(ctx, "a", "docs/README.md")
	wantKind(t, "rm", err, ErrGenerated, "")
	res, err := s.ImportDocuments(ctx, "a", []DocumentInput{
		{Name: "docs/index.md", Content: "i"}, {Name: "docs/x.md", Content: "x"},
		{Name: "docs/a/index.md", Content: "i"}, {Name: "docs/README.md", Content: "r"}, {Name: "index.md", Content: "frei"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"docs/index.md", "docs/a/index.md", "docs/README.md"}; !reflect.DeepEqual(res.Generated, want) {
		t.Errorf("Generated: %v", res.Generated)
	}
	if len(res.Results) != 2 || res.Results[0].Name != "docs/x.md" || res.Results[1].Name != "index.md" {
		t.Errorf("Results: %+v", res.Results)
	}
	if d, _ := s.Document(ctx, "a", "docs/README.md"); d.Content != "x" || d.UpdatedBy != contract.SystemAuthor {
		t.Errorf("docs/README.md überschrieben: %+v", d)
	}
}

// Review-Befund zu Task 033: Ein neuer Name, der unter docs/ index.md oder
// README.md als Verzeichnis nutzt, nähme dem Neubau den Platz und ließe ihn
// an path_conflict scheitern. create, rename (auch eines Verzeichnisses) und
// hub doc put lehnen ihn mit ErrGenerated ab, der Import übergeht ihn;
// außerhalb von docs/ ist er frei. Danach läuft der Neubau durch.
func TestReservedDirNames(t *testing.T) {
	f := newWriteStore(t)
	ctx := context.Background()
	s := f.s
	for _, n := range []string{"docs/a/x.md", "notizen/index.md/x.md", "entwurf/index.md/y.md", "lose.md"} {
		if _, err := s.CreateDocumentAs(ctx, f.as("bob"), "a", n, "x"); err != nil {
			t.Fatalf("%s: %v", n, err)
		}
	}
	for name, dir := range map[string]string{
		"docs/index.md/x.md":      "docs/index.md",
		"docs/README.md/x.md":     "docs/README.md",
		"docs/a/index.md/y.md":    "docs/a/index.md",
		"docs/neu/index.md/y.md":  "docs/neu/index.md",
		"docs/a/README.md/z.md":   "docs/a/README.md",
		"docs/a/README.md/b/c.md": "docs/a/README.md",
	} {
		// Gleich mit welchem Recht: bob hat write, eve nur supersede.
		for _, acc := range []string{"bob", "eve"} {
			_, err := s.CreateDocumentAs(ctx, f.as(acc), "a", name, "x")
			wantKind(t, acc+" create "+name, err, ErrGenerated, dir+" wäre ein Verzeichnis")
		}
		_, err := s.RenameDocumentAs(ctx, f.as("bob"), "a", "lose.md", name, nil)
		wantKind(t, "rename nach "+name, err, ErrGenerated, dir+" wäre ein Verzeichnis")
		_, err = s.PutDocument(ctx, "a", name, "x")
		wantKind(t, "put "+name, err, ErrGenerated, dir+" wäre ein Verzeichnis")
	}
	// Ein Verzeichnis nach docs/ hinein, das index.md als Verzeichnis
	// mitbrächte, oder eines, das selbst zu docs/index.md würde.
	_, err := s.RenameDocumentAs(ctx, f.as("bob"), "a", "entwurf", "docs/entwurf", nil)
	wantKind(t, "Verzeichnis hinein", err, ErrGenerated, "entwurf/index.md/y.md")
	_, err = s.RenameDocumentAs(ctx, f.as("bob"), "a", "docs/a", "docs/README.md", nil)
	wantKind(t, "Verzeichnis zu docs/README.md", err, ErrGenerated, "docs/README.md wäre ein Verzeichnis")
	// Der Import übergeht solche Namen und nennt sie.
	res, err := s.ImportDocuments(ctx, "a", []DocumentInput{
		{Name: "docs/b/x.md", Content: "x"}, {Name: "docs/b/index.md/y.md", Content: "y"},
		{Name: "docs/README.md/z.md", Content: "z"}, {Name: "frei/index.md/z.md", Content: "z"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"docs/b/index.md/y.md", "docs/README.md/z.md"}; !reflect.DeepEqual(res.Reserved, want) {
		t.Errorf("Reserved: %v", res.Reserved)
	}
	if len(res.Results) != 2 || res.Results[0].Name != "docs/b/x.md" || res.Results[1].Name != "frei/index.md/z.md" {
		t.Errorf("Results: %+v", res.Results)
	}
	if got := liveUnder(t, s, "a", "docs"); !reflect.DeepEqual(got, []string{"docs/a/x.md", "docs/b/x.md"}) {
		t.Errorf("docs/: %v", got)
	}
	// Der Neubau, an dem der Befund scheiterte, läuft durch.
	rb, err := s.RebuildDocs(ctx, "a", source.Actor{Account: "bob"})
	if err != nil || rb.Revision == 0 {
		t.Fatalf("Neubau: %+v, %v", rb, err)
	}
	for _, n := range []string{"docs/index.md", "docs/README.md", "docs/a/index.md", "docs/a/README.md"} {
		if _, err := s.Document(ctx, "a", n); err != nil {
			t.Errorf("%s: %v", n, err)
		}
	}
}
