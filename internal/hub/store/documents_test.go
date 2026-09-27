package store

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/oklog/ulid/v2"
)

// newDocStore legt einen Hub mit den Collections a und b an.
func newDocStore(t *testing.T) Store {
	t.Helper()
	ctx := context.Background()
	s, err := Create(ctx, newDB(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	for _, c := range []string{"a", "b"} {
		if err := s.AddCollection(ctx, c, ""); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func revision(t *testing.T, s Store) int64 {
	t.Helper()
	info, err := s.Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return info.Revision
}

// docActions liest die Dokument-Zeilen in actions: Aktion, id, Revision.
// Die CLI am Hub schreibt als admin ohne Träger.
func docActions(t *testing.T, s Store) []string {
	t.Helper()
	rows, err := s.(*sqliteStore).db.Query(`SELECT action, document_id, revision, account, carrier FROM actions
		WHERE document_id IS NOT NULL ORDER BY rowid`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var action, id, account string
		var carrier sql.NullString
		var rev int64
		if err := rows.Scan(&action, &id, &rev, &account, &carrier); err != nil {
			t.Fatal(err)
		}
		if account != Admin || carrier.Valid {
			t.Errorf("actions: Account %q, Träger %v", account, carrier)
		}
		out = append(out, action+" "+id+" "+strconv.FormatInt(rev, 10))
	}
	return out
}

func TestPutGetReplaceUnchanged(t *testing.T) {
	ctx := context.Background()
	s := newDocStore(t)

	r, err := s.PutDocument(ctx, "a", "tasks/001-x.md", "---\ntitel: x\n---\nText\n")
	if err != nil {
		t.Fatal(err)
	}
	if r.Outcome != Created || r.Revision != 1 || revision(t, s) != 1 {
		t.Fatalf("anlegen: %+v, Hub-Revision %d", r, revision(t, s))
	}
	if _, err := ulid.ParseStrict(r.ID); err != nil {
		t.Errorf("id %q ist keine ULID: %v", r.ID, err)
	}
	d, err := s.Document(ctx, "a", "tasks/001-x.md")
	if err != nil {
		t.Fatal(err)
	}
	if d.ID != r.ID || d.Content != "---\ntitel: x\n---\nText\n" || d.Meta != "" || d.Deleted ||
		d.Revision != 1 || d.CreatedBy != Admin || d.UpdatedBy != Admin || d.CreatedAt == 0 {
		t.Errorf("Document = %+v", d)
	}

	// Unveränderter Inhalt: keine Revision, keine Zeile in actions.
	r2, err := s.PutDocument(ctx, "a", "tasks/001-x.md", d.Content)
	if err != nil {
		t.Fatal(err)
	}
	if r2.Outcome != Unchanged || r2.Revision != 1 || r2.ID != r.ID || revision(t, s) != 1 {
		t.Errorf("unverändert: %+v, Hub-Revision %d", r2, revision(t, s))
	}

	// Ersetzen: dieselbe id, neue Revision.
	r3, err := s.PutDocument(ctx, "a", "tasks/001-x.md", "neu")
	if err != nil {
		t.Fatal(err)
	}
	if r3.Outcome != Replaced || r3.Revision != 2 || r3.ID != r.ID {
		t.Errorf("ersetzen: %+v", r3)
	}
	d, _ = s.Document(ctx, "a", "tasks/001-x.md")
	if d.Content != "neu" || d.Revision != 2 || d.CreatedAt > d.UpdatedAt {
		t.Errorf("nach ersetzen: %+v", d)
	}

	// Derselbe Name in einer anderen Collection ist ein anderes Dokument.
	r4, err := s.PutDocument(ctx, "b", "tasks/001-x.md", "neu")
	if err != nil || r4.Outcome != Created || r4.ID == r.ID || r4.Revision != 3 {
		t.Errorf("andere Collection: %+v, %v", r4, err)
	}

	want := []string{"create " + r.ID + " 1", "update " + r.ID + " 2", "create " + r4.ID + " 3"}
	if got := docActions(t, s); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("actions:\n%s\nerwartet:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if st, _ := s.Stats(ctx); st.Documents != 2 {
		t.Errorf("Stats = %+v", st)
	}
}

func TestPutRejects(t *testing.T) {
	ctx := context.Background()
	s := newDocStore(t)
	cases := []struct {
		coll, name, content string
		want                error
		text                string
	}{
		{"fehlt", "x.md", "", ErrNotFound, "Collection fehlt"},
		{"a", "SYSTEM:A:kleist", "{}", nil, "vorbehalten"},
		{"a", "/x.md", "", nil, "relativer Pfad"},
		{"a", "x.md", "a\xffb", ErrNotText, "UTF-8"},
		{"a", "x.md", "a\x00b", ErrNotText, "UTF-8"},
		{"a", "x.md", strings.Repeat("x", MaxDocumentBytes+1), ErrTooLarge, "1 MiB"},
	}
	for _, c := range cases {
		_, err := s.PutDocument(ctx, c.coll, c.name, c.content)
		if err == nil || (c.want != nil && !errors.Is(err, c.want)) || !strings.Contains(err.Error(), c.text) {
			t.Errorf("%s/%q: %v", c.coll, c.name, err)
		}
	}
	if _, err := s.PutDocument(ctx, "a", "x.md", strings.Repeat("ä", MaxDocumentBytes/2)); err != nil {
		t.Errorf("genau 1 MiB: %v", err)
	}
	if revision(t, s) != 1 {
		t.Errorf("Hub-Revision %d, erwartet 1", revision(t, s))
	}
}

func TestPathConflict(t *testing.T) {
	ctx := context.Background()
	s := newDocStore(t)
	if _, err := s.PutDocument(ctx, "a", "tasks", "Datei"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutDocument(ctx, "a", "docs/x/y.md", "y"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"tasks/001.md", "tasks/done/001.md", "docs", "docs/x"} {
		_, err := s.PutDocument(ctx, "a", name, "z")
		if !errors.Is(err, ErrPathConflict) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Ähnliche Namen stören nicht: docs.md, docs-x, tasks0 liegen nicht unter
	// docs/ bzw. tasks/.
	for _, name := range []string{"docs.md", "docs-x/y.md", "tasks0", "tasksx/a.md", "docs/x.md"} {
		if _, err := s.PutDocument(ctx, "a", name, "z"); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	// In einer anderen Collection gilt der Konflikt nicht.
	if _, err := s.PutDocument(ctx, "b", "tasks/001.md", "z"); err != nil {
		t.Errorf("andere Collection: %v", err)
	}
	// Nach dem Löschen ist der Weg frei — Löschmarken zählen nicht.
	if _, err := s.DeleteDocument(ctx, "a", "tasks"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutDocument(ctx, "a", "tasks/001.md", "z"); err != nil {
		t.Errorf("nach Löschmarke: %v", err)
	}
	if _, err := s.PutDocument(ctx, "a", "tasks", "wieder Datei"); !errors.Is(err, ErrPathConflict) {
		t.Errorf("tasks ist jetzt Verzeichnis: %v", err)
	}
}

func TestDeleteAndRecreate(t *testing.T) {
	ctx := context.Background()
	s := newDocStore(t)
	first, err := s.PutDocument(ctx, "a", "x.md", "eins")
	if err != nil {
		t.Fatal(err)
	}
	del, err := s.DeleteDocument(ctx, "a", "x.md")
	if err != nil {
		t.Fatal(err)
	}
	if !del.Deleted || del.ID != first.ID || del.Revision != 2 || del.Content != "" {
		t.Errorf("Löschmarke = %+v", del)
	}
	if _, err := s.Document(ctx, "a", "x.md"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Document nach rm: %v", err)
	}
	if _, err := s.DeleteDocument(ctx, "a", "x.md"); !errors.Is(err, ErrNotFound) {
		t.Errorf("zweites rm: %v", err)
	}
	if revision(t, s) != 2 {
		t.Errorf("zweites rm zählte eine Revision: %d", revision(t, s))
	}
	var content, meta any
	var deleted, rev int64
	err = s.(*sqliteStore).db.QueryRow(`SELECT content, meta, deleted, revision FROM documents WHERE id = ?`, first.ID).
		Scan(&content, &meta, &deleted, &rev)
	if err != nil || content != nil || meta != nil || deleted != 1 || rev != 2 {
		t.Errorf("Zeile der Löschmarke: content=%v meta=%v deleted=%d rev=%d err=%v", content, meta, deleted, rev, err)
	}

	again, err := s.PutDocument(ctx, "a", "x.md", "eins")
	if err != nil {
		t.Fatal(err)
	}
	if again.Outcome != Created || again.ID == first.ID || again.Revision != 3 {
		t.Errorf("Neuanlage = %+v", again)
	}
	d, err := s.Document(ctx, "a", "x.md")
	if err != nil || d.ID != again.ID || d.Content != "eins" {
		t.Errorf("Document = %+v, %v", d, err)
	}
	want := []string{"create " + first.ID + " 1", "delete " + first.ID + " 2", "create " + again.ID + " 3"}
	if got := docActions(t, s); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("actions = %v", got)
	}
	if st, _ := s.Stats(ctx); st.Documents != 1 {
		t.Errorf("Stats = %+v", st)
	}
}

func TestDocumentsList(t *testing.T) {
	ctx := context.Background()
	s := newDocStore(t)
	for _, n := range []string{"tasks/002.md", "tasks/001.md", "tasks/done/000.md", "tasks0", "readme.md", "weg.md"} {
		if _, err := s.PutDocument(ctx, "a", n, n); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.PutDocument(ctx, "b", "tasks/003.md", "b"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteDocument(ctx, "a", "weg.md"); err != nil {
		t.Fatal(err)
	}
	// Eine SYSTEM:-Zeile, wie sie der Hub selbst schreibt, erscheint nie.
	if _, err := s.(*sqliteStore).db.Exec(`INSERT INTO documents
		(id, collection, name, content, revision, created_at, created_by, updated_at, updated_by)
		VALUES ('sys', 'a', 'SYSTEM:A:kleist', '{}', 9, 0, 'x', 0, 'x')`); err != nil {
		t.Fatal(err)
	}
	names := func(dir string) string {
		t.Helper()
		docs, err := s.Documents(ctx, "a", dir)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, d := range docs {
			out = append(out, d.Name)
		}
		return strings.Join(out, " ")
	}
	if got := names(""); got != "readme.md tasks/001.md tasks/002.md tasks/done/000.md tasks0" {
		t.Errorf("alle: %s", got)
	}
	for _, dir := range []string{"tasks", "tasks/"} {
		if got := names(dir); got != "tasks/001.md tasks/002.md tasks/done/000.md" {
			t.Errorf("%s: %s", dir, got)
		}
	}
	if got := names("leer"); got != "" {
		t.Errorf("leer: %s", got)
	}
	if _, err := s.Documents(ctx, "fehlt", ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("fehlende Collection: %v", err)
	}
	if _, err := s.Documents(ctx, "a", "../x"); err == nil {
		t.Error("ungültiges Verzeichnis angenommen")
	}
	if _, err := s.Document(ctx, "a", "SYSTEM:A:kleist"); err == nil {
		t.Error("SYSTEM:-Zeile über Document lesbar")
	}
}

func TestImportDocuments(t *testing.T) {
	ctx := context.Background()
	s := newDocStore(t)
	if _, err := s.PutDocument(ctx, "a", "alt.md", "alt"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutDocument(ctx, "a", "gleich.md", "gleich"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutDocument(ctx, "a", "bleibt.md", "bleibt"); err != nil {
		t.Fatal(err)
	}
	res, err := s.ImportDocuments(ctx, "a", []DocumentInput{
		{"alt.md", "neu"}, {"gleich.md", "gleich"}, {"neu/a.md", "a"}, {"neu/b.md", "b"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Revision != 4 || revision(t, s) != 4 {
		t.Errorf("Revision %d, Hub %d, erwartet 4", res.Revision, revision(t, s))
	}
	var got []string
	for _, r := range res.Results {
		got = append(got, r.Name+"="+r.Outcome.String()+"@"+strconv.FormatInt(r.Revision, 10))
	}
	if want := "alt.md=ersetzt@4 gleich.md=unverändert@2 neu/a.md=angelegt@4 neu/b.md=angelegt@4"; strings.Join(got, " ") != want {
		t.Errorf("Ergebnis: %s", strings.Join(got, " "))
	}
	// Alle geänderten Zeilen tragen dieselbe Revision; je Dokument eine Zeile in actions.
	acts := docActions(t, s)
	if len(acts) != 6 {
		t.Errorf("actions = %v", acts)
	}
	for _, a := range acts[3:] {
		if !strings.HasSuffix(a, " 4") {
			t.Errorf("action %q nicht in Revision 4", a)
		}
	}
	// Fehlendes wird nicht gelöscht.
	if d, err := s.Document(ctx, "a", "bleibt.md"); err != nil || d.Content != "bleibt" {
		t.Errorf("bleibt.md: %+v, %v", d, err)
	}

	// Nichts geändert: keine Revision.
	res, err = s.ImportDocuments(ctx, "a", []DocumentInput{{"alt.md", "neu"}})
	if err != nil || res.Revision != 0 || revision(t, s) != 4 {
		t.Errorf("unverändert: %+v, %v, Hub %d", res, err, revision(t, s))
	}

	// Ganz oder gar nicht: ein Konflikt mitten im Import schreibt nichts.
	_, err = s.ImportDocuments(ctx, "a", []DocumentInput{{"x.md", "x"}, {"alt.md/y.md", "y"}})
	if !errors.Is(err, ErrPathConflict) {
		t.Errorf("Konflikt: %v", err)
	}
	if _, err := s.Document(ctx, "a", "x.md"); !errors.Is(err, ErrNotFound) || revision(t, s) != 4 {
		t.Errorf("nach abgebrochenem Import: %v, Hub %d", err, revision(t, s))
	}

	// Geprüft wird vor der Transaktion.
	for _, docs := range [][]DocumentInput{
		{{"d.md", "1"}, {"d.md", "2"}},
		{{"ok.md", "1"}, {"SYSTEM:A:x", "{}"}},
		{{"ok.md", "a\xff"}},
	} {
		if _, err := s.ImportDocuments(ctx, "a", docs); err == nil {
			t.Errorf("%v angenommen", docs)
		}
	}
	if _, err := s.ImportDocuments(ctx, "fehlt", nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("fehlende Collection: %v", err)
	}
}
