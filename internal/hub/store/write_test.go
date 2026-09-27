package store

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/oklog/ulid/v2"

	"github.com/kephalaion/kephalaion/internal/contract"
	"github.com/kephalaion/kephalaion/internal/ident"
)

// writeFixture ist ein Hub mit den Collections a, b und c; der Node laptop
// darf a und c abgleichen, b nicht. Accounts, alle mit einer Zeile in a:
//
//	bob    User kleist  write in a und b
//	bob2   User kleist  write
//	eve    User eve     supersede, kein write
//	alice  User alice   write
//	leser  User leser   nur read
type writeFixture struct {
	s      *sqliteStore
	tokens map[string]string
}

func newWriteStore(t *testing.T) *writeFixture {
	t.Helper()
	ctx := context.Background()
	s := newStore(t)
	for _, c := range []string{"a", "b", "c"} {
		if err := s.AddCollection(ctx, c, ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.AddNode(ctx, "laptop", ""); err != nil {
		t.Fatal(err)
	}
	for _, c := range []string{"a", "c"} {
		if err := s.Grant(ctx, "laptop", c); err != nil {
			t.Fatal(err)
		}
	}
	type grant struct {
		collection string
		rights     contract.Rights
	}
	accounts := []struct {
		name, user string
		grants     []grant
	}{
		{"bob", "kleist", []grant{{"a", contract.Rights{Write: true}}, {"b", contract.Rights{Write: true}}}},
		{"bob2", "kleist", []grant{{"a", contract.Rights{Write: true}}}},
		{"eve", "eve", []grant{{"a", contract.Rights{Supersede: true}}}},
		{"alice", "alice", []grant{{"a", contract.Rights{Write: true}}}},
		{"leser", "leser", []grant{{"a", contract.Rights{}}}},
	}
	f := &writeFixture{s: s, tokens: map[string]string{}}
	for _, a := range accounts {
		token, err := s.AddAccount(ctx, a.name, a.user, "")
		if err != nil {
			t.Fatal(err)
		}
		f.tokens[a.name] = token
		for _, g := range a.grants {
			if _, err := s.GrantAccount(ctx, a.name, g.collection, g.rights); err != nil {
				t.Fatal(err)
			}
		}
	}
	return f
}

// as ist die Anmeldung eines Accounts über den Node laptop.
func (f *writeFixture) as(account string) WriteAuth {
	return WriteAuth{Account: account, TokenHash: ident.HashToken(f.tokens[account]), Carrier: "laptop"}
}

func rev(r int64) *int64 { return &r }

// docAction ist eine Dokument-Zeile in actions.
type docAction struct {
	action, id, account, carrier string
	revision                     int64
}

func docActionsAll(t *testing.T, s *sqliteStore) []docAction {
	t.Helper()
	rows, err := s.db.Query(`SELECT action, document_id, account, COALESCE(carrier, ''), revision FROM actions
		WHERE document_id IS NOT NULL ORDER BY rowid`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []docAction
	for rows.Next() {
		var a docAction
		if err := rows.Scan(&a.action, &a.id, &a.account, &a.carrier, &a.revision); err != nil {
			t.Fatal(err)
		}
		out = append(out, a)
	}
	return out
}

// syncRow liest die Zeile id so, wie der Abgleich sie liefert.
func syncRow(t *testing.T, s Store, collection, id string) contract.Row {
	t.Helper()
	rows, err := s.SyncRows(context.Background(), []contract.Since{{Collection: collection}}, revision(t, s), 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.ID == id {
			return r
		}
	}
	t.Fatalf("Zeile %s fehlt im Abgleich", id)
	return contract.Row{}
}

// unchanged prüft, dass ein gescheiterter Vorgang nichts geschrieben hat:
// keine Revision, keine Zeile in actions.
type unchangedCheck struct {
	t       *testing.T
	s       *sqliteStore
	rev     int64
	actions int
}

func snapshot(t *testing.T, s *sqliteStore) unchangedCheck {
	t.Helper()
	return unchangedCheck{t: t, s: s, rev: revision(t, s), actions: len(docActionsAll(t, s))}
}

func (u unchangedCheck) check(what string) {
	u.t.Helper()
	if r := revision(u.t, u.s); r != u.rev {
		u.t.Errorf("%s: Hub-Revision %d, erwartet %d", what, r, u.rev)
	}
	if n := len(docActionsAll(u.t, u.s)); n != u.actions {
		u.t.Errorf("%s: %d Dokument-Zeilen in actions, erwartet %d", what, n, u.actions)
	}
}

// Urheber: created_by/updated_by ist der User des Accounts, actions nennt je
// Dokument Account und Node; die Antwort trägt die Zeilen wie der Abgleich.
func TestWriteAsAuthor(t *testing.T) {
	ctx := context.Background()
	f := newWriteStore(t)
	s := f.s

	created, err := s.CreateDocumentAs(ctx, f.as("bob"), "a", "notes/x.md", "eins")
	if err != nil {
		t.Fatal(err)
	}
	if created.Revision != revision(t, s) || len(created.Rows) != 1 {
		t.Fatalf("anlegen: %+v, Hub-Revision %d", created, revision(t, s))
	}
	r := created.Rows[0]
	if _, err := ulid.ParseStrict(r.ID); err != nil {
		t.Errorf("id %q ist keine ULID: %v", r.ID, err)
	}
	if r.Collection != "a" || r.Name != "notes/x.md" || r.Content == nil || *r.Content != "eins" || r.Meta != nil ||
		r.Deleted || r.Revision != created.Revision || r.CreatedBy != "kleist" || r.UpdatedBy != "kleist" ||
		r.CreatedAt == 0 || r.UpdatedAt != r.CreatedAt {
		t.Errorf("Zeile nach anlegen: %+v", r)
	}
	if got := syncRow(t, s, "a", r.ID); !reflect.DeepEqual(got, r) {
		t.Errorf("Antwort %+v, Abgleich %+v", r, got)
	}

	// Ein anderer Account desselben Users ersetzt; created_by bleibt.
	written, err := s.WriteDocumentAs(ctx, f.as("bob2"), "a", "notes/x.md", "zwei", rev(created.Revision))
	if err != nil {
		t.Fatal(err)
	}
	w := written.Rows[0]
	if written.Revision != created.Revision+1 || w.ID != r.ID || *w.Content != "zwei" || w.Revision != written.Revision ||
		w.CreatedBy != "kleist" || w.UpdatedBy != "kleist" || w.CreatedAt != r.CreatedAt {
		t.Errorf("ersetzen: %+v", written)
	}
	if d, err := s.Document(ctx, "a", "notes/x.md"); err != nil || d.Content != "zwei" || d.Revision != written.Revision {
		t.Errorf("Document nach ersetzen: %+v, %v", d, err)
	}

	deleted, err := s.DeleteDocumentAs(ctx, f.as("bob"), "a", "notes/x.md", rev(written.Revision), false)
	if err != nil {
		t.Fatal(err)
	}
	d := deleted.Rows[0]
	if deleted.Revision != written.Revision+1 || d.ID != r.ID || d.Content != nil || d.Meta != nil || !d.Deleted ||
		d.Revision != deleted.Revision || d.CreatedBy != "kleist" || d.UpdatedBy != "kleist" {
		t.Errorf("Löschmarke: %+v", deleted)
	}
	if got := syncRow(t, s, "a", r.ID); !reflect.DeepEqual(got, d) {
		t.Errorf("Antwort %+v, Abgleich %+v", d, got)
	}
	if _, err := s.Document(ctx, "a", "notes/x.md"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Document nach delete: %v", err)
	}

	want := []docAction{
		{"create", r.ID, "bob", "laptop", created.Revision},
		{"update", r.ID, "bob2", "laptop", written.Revision},
		{"delete", r.ID, "bob", "laptop", deleted.Revision},
	}
	if got := docActionsAll(t, s); !reflect.DeepEqual(got, want) {
		t.Errorf("actions:\n%+v\nerwartet:\n%+v", got, want)
	}
}

// Rechte: write für Neues und Eigenes (auch eines anderen Accounts desselben
// Users), supersede für Fremdes — write ist dafür nicht nötig.
func TestWriteAsRights(t *testing.T) {
	ctx := context.Background()
	f := newWriteStore(t)
	s := f.s
	if _, err := s.PutDocument(ctx, "a", "fremd.md", "vom Admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateDocumentAs(ctx, f.as("bob"), "a", "eigen.md", "von bob"); err != nil {
		t.Fatal(err)
	}

	forbidden := []struct {
		what string
		fn   func() error
		text string
	}{
		{"alice ändert Fremdes ohne supersede", func() error {
			_, err := s.WriteDocumentAs(ctx, f.as("alice"), "a", "fremd.md", "x", nil)
			return err
		}, "gehört admin, supersede fehlt"},
		{"alice löscht Fremdes ohne supersede", func() error {
			_, err := s.DeleteDocumentAs(ctx, f.as("alice"), "a", "eigen.md", nil, false)
			return err
		}, "gehört kleist, supersede fehlt"},
		{"eve legt ohne write an", func() error {
			_, err := s.CreateDocumentAs(ctx, f.as("eve"), "a", "neu.md", "x")
			return err
		}, "anlegen: write fehlt"},
		{"leser legt an", func() error {
			_, err := s.CreateDocumentAs(ctx, f.as("leser"), "a", "neu.md", "x")
			return err
		}, "write fehlt"},
		{"leser ändert Fremdes", func() error {
			_, err := s.WriteDocumentAs(ctx, f.as("leser"), "a", "eigen.md", "x", nil)
			return err
		}, "supersede fehlt"},
		// Unveränderter Inhalt ändert nichts am Recht.
		{"alice schreibt Fremdes unverändert", func() error {
			_, err := s.WriteDocumentAs(ctx, f.as("alice"), "a", "fremd.md", "vom Admin", nil)
			return err
		}, "supersede fehlt"},
	}
	for _, c := range forbidden {
		u := snapshot(t, s)
		err := c.fn()
		if !errors.Is(err, ErrForbidden) || !strings.Contains(err.Error(), c.text) {
			t.Errorf("%s: %v", c.what, err)
		}
		u.check(c.what)
	}

	// Fremdes mit supersede, ohne write.
	res, err := s.WriteDocumentAs(ctx, f.as("eve"), "a", "fremd.md", "von eve", nil)
	if err != nil {
		t.Fatalf("eve ändert Fremdes mit supersede: %v", err)
	}
	if r := res.Rows[0]; r.CreatedBy != Admin || r.UpdatedBy != "eve" {
		t.Errorf("Urheber nach supersede: created_by %q, updated_by %q", r.CreatedBy, r.UpdatedBy)
	}
	if _, err := s.WriteDocumentAs(ctx, f.as("eve"), "a", "eigen.md", "auch von eve", nil); err != nil {
		t.Errorf("eve ändert bobs Dokument mit supersede: %v", err)
	}
	// Eigenes eines anderen Accounts desselben Users mit write.
	if _, err := s.WriteDocumentAs(ctx, f.as("bob2"), "a", "eigen.md", "von bob2", nil); err != nil {
		t.Errorf("bob2 ändert Dokument von bob: %v", err)
	}
	if _, err := s.DeleteDocumentAs(ctx, f.as("bob2"), "a", "eigen.md", nil, false); err != nil {
		t.Errorf("bob2 löscht Dokument von bob: %v", err)
	}
	if _, err := s.DeleteDocumentAs(ctx, f.as("eve"), "a", "fremd.md", nil, false); err != nil {
		t.Errorf("eve löscht Fremdes mit supersede: %v", err)
	}

	// Eigenes ohne write: bob verliert write.
	if _, err := s.CreateDocumentAs(ctx, f.as("bob"), "a", "entzogen.md", "x"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GrantAccount(ctx, "bob", "a", contract.Rights{}); err != nil {
		t.Fatal(err)
	}
	u := snapshot(t, s)
	if _, err := s.WriteDocumentAs(ctx, f.as("bob"), "a", "entzogen.md", "y", nil); !errors.Is(err, ErrForbidden) ||
		!strings.Contains(err.Error(), "ändern: write fehlt") {
		t.Errorf("Eigenes ohne write ändern: %v", err)
	}
	if _, err := s.DeleteDocumentAs(ctx, f.as("bob"), "a", "entzogen.md", nil, false); !errors.Is(err, ErrForbidden) ||
		!strings.Contains(err.Error(), "löschen: write fehlt") {
		t.Errorf("Eigenes ohne write löschen: %v", err)
	}
	u.check("Eigenes ohne write")
}

// Anmeldung und Lesbarkeit prüft der Store selbst, in der Transaktion.
func TestWriteAsAuthAndReadable(t *testing.T) {
	ctx := context.Background()
	f := newWriteStore(t)
	s := f.s
	if _, err := s.CreateDocumentAs(ctx, f.as("alice"), "a", "x.md", "x"); err != nil {
		t.Fatal(err)
	}
	ops := func(auth WriteAuth, collection string) map[string]error {
		out := map[string]error{}
		_, out["create"] = s.CreateDocumentAs(ctx, auth, collection, "neu.md", "x")
		_, out["write"] = s.WriteDocumentAs(ctx, auth, collection, "x.md", "y", nil)
		_, out["delete"] = s.DeleteDocumentAs(ctx, auth, collection, "x.md", nil, false)
		_, out["rename"] = s.RenameDocumentAs(ctx, auth, collection, "x.md", "y.md", nil)
		return out
	}
	expect := func(what string, auth WriteAuth, collection string, want error) {
		t.Helper()
		u := snapshot(t, s)
		for op, err := range ops(auth, collection) {
			if !errors.Is(err, want) {
				t.Errorf("%s, %s: %v, erwartet %v", what, op, err, want)
			}
		}
		u.check(what)
	}

	wrong := f.as("alice")
	wrong.TokenHash = ident.HashToken("keph_falsch")
	expect("falsches Token", wrong, "a", ErrAccountAuth)
	unknown := f.as("alice")
	unknown.Account = "niemand"
	expect("unbekannter Account", unknown, "a", ErrAccountAuth)
	expect("Hash als Token", WriteAuth{Account: "alice", TokenHash: ident.HashToken(f.as("alice").TokenHash), Carrier: "laptop"},
		"a", ErrAccountAuth)

	expect("unbekannte Collection", f.as("bob"), "fehlt", ErrNotReadable)
	expect("Collection für den Node nicht erlaubt", f.as("bob"), "b", ErrNotReadable)
	expect("Account ohne Zeile in der Collection", f.as("bob"), "c", ErrNotReadable)
	other := f.as("bob")
	other.Carrier = "anderer"
	expect("unbekannter Node", other, "a", ErrNotReadable)
	if err := s.RevokeAccount(ctx, "bob2", "a"); err != nil {
		t.Fatal(err)
	}
	expect("Zeile des Accounts ist Löschmarke", f.as("bob2"), "a", ErrNotReadable)

	// Gesperrt: die Anmeldung scheitert, vor der Lesbarkeit.
	if err := s.SetAccountLocked(ctx, "alice", true); err != nil {
		t.Fatal(err)
	}
	expect("gesperrt", f.as("alice"), "a", ErrAccountAuth)
	if err := s.SetAccountLocked(ctx, "alice", false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteDocumentAs(ctx, f.as("alice"), "a", "x.md", "wieder", nil); err != nil {
		t.Errorf("nach unlock: %v", err)
	}
}

// create: Name vergeben, Löschmarke, Datei und Verzeichnis zugleich, leeres
// Dokument, Name und Inhalt.
func TestCreateDocumentAs(t *testing.T) {
	ctx := context.Background()
	f := newWriteStore(t)
	s := f.s
	bob := f.as("bob")
	first, err := s.CreateDocumentAs(ctx, bob, "a", "x.md", "eins")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutDocument(ctx, "a", "admin.md", "vom Admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateDocumentAs(ctx, bob, "a", "dir/a.md", "a"); err != nil {
		t.Fatal(err)
	}

	rejects := []struct {
		name, content string
		want          []error
		text          string
	}{
		{"x.md", "zwei", []error{ErrNameTaken}, "gibt es in a schon"},
		{"admin.md", "zwei", []error{ErrNameTaken}, "gibt es in a schon"},
		{"x.md/y.md", "y", []error{ErrPathConflict}, "x.md ist in a ein Dokument"},
		{"dir", "d", []error{ErrPathConflict}, "ein Verzeichnis mit 1 Dokumenten"},
		{"SYSTEM:A:bob", "{}", []error{ErrInvalid}, "dem Hub vorbehalten"},
		{"/x.md", "", []error{ErrInvalid}, "relativer Pfad"},
		{"bin.md", "a\xffb", []error{ErrInvalid, ErrNotText}, "UTF-8"},
		{"nul.md", "a\x00b", []error{ErrInvalid, ErrNotText}, "UTF-8"},
		{"gross.md", strings.Repeat("x", MaxDocumentBytes+1), []error{ErrInvalid, ErrTooLarge}, "1 MiB"},
	}
	for _, c := range rejects {
		u := snapshot(t, s)
		_, err := s.CreateDocumentAs(ctx, bob, "a", c.name, c.content)
		for _, want := range c.want {
			if !errors.Is(err, want) {
				t.Errorf("%q: %v, erwartet %v", c.name, err, want)
			}
		}
		if err == nil || !strings.Contains(err.Error(), c.text) {
			t.Errorf("%q: Meldung %v, erwartet %q", c.name, err, c.text)
		}
		u.check(c.name)
	}
	if rows := accountRowsAll(t, s, "bob"); len(rows) != 2 || rows[0].content.User != "kleist" {
		t.Errorf("Zeilen von bob: %+v", rows)
	}

	// Genau 1 MiB und leer sind erlaubt; leer heißt Inhalt "", nicht NULL.
	if _, err := s.CreateDocumentAs(ctx, bob, "a", "gross.md", strings.Repeat("ä", MaxDocumentBytes/2)); err != nil {
		t.Errorf("genau 1 MiB: %v", err)
	}
	empty, err := s.CreateDocumentAs(ctx, bob, "a", "leer.md", "")
	if err != nil {
		t.Fatal(err)
	}
	if c := empty.Rows[0].Content; c == nil || *c != "" {
		t.Errorf("leeres Dokument: Inhalt %v", c)
	}
	if d, err := s.Document(ctx, "a", "leer.md"); err != nil || d.Content != "" || d.Deleted {
		t.Errorf("leeres Dokument gelesen: %+v, %v", d, err)
	}

	// Eine Löschmarke hindert nicht: neue id.
	if _, err := s.DeleteDocumentAs(ctx, bob, "a", "x.md", nil, false); err != nil {
		t.Fatal(err)
	}
	again, err := s.CreateDocumentAs(ctx, f.as("alice"), "a", "x.md", "neu")
	if err != nil {
		t.Fatalf("Löschmarke neu anlegen: %v", err)
	}
	if r := again.Rows[0]; r.ID == first.Rows[0].ID || r.CreatedBy != "alice" || *r.Content != "neu" {
		t.Errorf("Neuanlage: %+v", r)
	}
	if d := syncRow(t, s, "a", first.Rows[0].ID); !d.Deleted {
		t.Errorf("alte Zeile: %+v", d)
	}
}

// write und delete: Vorbedingung, unveränderter Inhalt, nicht vorhanden.
func TestWriteDeleteRevision(t *testing.T) {
	ctx := context.Background()
	f := newWriteStore(t)
	s := f.s
	bob := f.as("bob")
	created, err := s.CreateDocumentAs(ctx, bob, "a", "x.md", "eins")
	if err != nil {
		t.Fatal(err)
	}
	r1 := created.Revision
	written, err := s.WriteDocumentAs(ctx, bob, "a", "x.md", "zwei", rev(r1))
	if err != nil {
		t.Fatal(err)
	}
	r2 := written.Revision

	stale := []struct {
		what string
		fn   func() error
	}{
		{"write veraltet", func() error { _, err := s.WriteDocumentAs(ctx, bob, "a", "x.md", "drei", rev(r1)); return err }},
		// Geprüft vor dem Vergleich des Inhalts.
		{"write veraltet, unverändert", func() error {
			_, err := s.WriteDocumentAs(ctx, bob, "a", "x.md", "zwei", rev(r1))
			return err
		}},
		{"write künftig", func() error { _, err := s.WriteDocumentAs(ctx, bob, "a", "x.md", "drei", rev(r2+5)); return err }},
		{"delete veraltet", func() error { _, err := s.DeleteDocumentAs(ctx, bob, "a", "x.md", rev(r1), false); return err }},
	}
	for _, c := range stale {
		u := snapshot(t, s)
		err := c.fn()
		if !errors.Is(err, ErrStaleRevision) || !strings.Contains(err.Error(), "hat Revision "+strconv.FormatInt(r2, 10)) {
			t.Errorf("%s: %v", c.what, err)
		}
		u.check(c.what)
	}

	// Unveränderter Inhalt: keine Revision, die bestehende Zeile.
	current := syncRow(t, s, "a", created.Rows[0].ID)
	for _, base := range []*int64{nil, rev(r2)} {
		u := snapshot(t, s)
		res, err := s.WriteDocumentAs(ctx, f.as("bob2"), "a", "x.md", "zwei", base)
		if err != nil {
			t.Fatal(err)
		}
		if res.Revision != r2 || len(res.Rows) != 1 || !reflect.DeepEqual(res.Rows[0], current) {
			t.Errorf("unverändert: %+v, erwartet Zeile %+v", res, current)
		}
		u.check("unverändert")
	}

	missing := []struct {
		what string
		fn   func() error
	}{
		{"write ohne Dokument", func() error { _, err := s.WriteDocumentAs(ctx, bob, "a", "fehlt.md", "x", nil); return err }},
		{"delete ohne Dokument", func() error { _, err := s.DeleteDocumentAs(ctx, bob, "a", "fehlt.md", nil, false); return err }},
		{"write in anderer Collection", func() error { _, err := s.WriteDocumentAs(ctx, bob, "c", "x.md", "x", nil); return err }},
	}
	for _, c := range missing {
		u := snapshot(t, s)
		if err := c.fn(); !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrNotReadable) {
			t.Errorf("%s: %v", c.what, err)
		}
		u.check(c.what)
	}

	deleted, err := s.DeleteDocumentAs(ctx, bob, "a", "x.md", rev(r2), false)
	if err != nil {
		t.Fatal(err)
	}
	if deleted.Revision != r2+1 {
		t.Errorf("delete: Revision %d, erwartet %d", deleted.Revision, r2+1)
	}
	for _, c := range []struct {
		what string
		fn   func() error
	}{
		{"write auf Löschmarke", func() error { _, err := s.WriteDocumentAs(ctx, bob, "a", "x.md", "x", nil); return err }},
		{"delete auf Löschmarke", func() error { _, err := s.DeleteDocumentAs(ctx, bob, "a", "x.md", nil, false); return err }},
	} {
		u := snapshot(t, s)
		if err := c.fn(); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: %v", c.what, err)
		}
		u.check(c.what)
	}
	// SYSTEM:-Namen sind kein Ziel: die Zeile des Accounts bleibt, wie sie ist.
	for _, fn := range []func() error{
		func() error { _, err := s.WriteDocumentAs(ctx, bob, "a", "SYSTEM:A:bob", "{}", nil); return err },
		func() error { _, err := s.DeleteDocumentAs(ctx, bob, "a", "SYSTEM:A:bob", nil, false); return err },
	} {
		if err := fn(); !errors.Is(err, ErrInvalid) {
			t.Errorf("SYSTEM:-Name: %v", err)
		}
	}
	if rows := accountRowsAll(t, s, "bob"); len(rows) != 2 || rows[0].deleted || !rows[0].content.Rights.Write {
		t.Errorf("Zeilen von bob: %+v", rows)
	}
}

// Jeder Schreibvorgang über einen Node sperrt als erste Anweisung die Zeile
// des Accounts in accounts — auch wenn er danach scheitert.
func TestWriteAsLockFirst(t *testing.T) {
	ctx := context.Background()
	f := newWriteStore(t)
	s := f.s
	all := trace(s)
	wrong := f.as("bob")
	wrong.TokenHash = ident.HashToken("keph_falsch")
	steps := []func() error{
		func() error { _, err := s.CreateDocumentAs(ctx, f.as("bob"), "a", "x.md", "x"); return err },
		func() error { _, err := s.WriteDocumentAs(ctx, f.as("bob"), "a", "x.md", "y", nil); return err },
		func() error { _, err := s.WriteDocumentAs(ctx, f.as("alice"), "a", "x.md", "z", nil); return err },
		func() error { _, err := s.CreateDocumentAs(ctx, wrong, "a", "y.md", "y"); return err },
		func() error { _, err := s.RenameDocumentAs(ctx, f.as("bob"), "a", "x.md", "d/x.md", nil); return err },
		func() error { _, err := s.RenameDocumentAs(ctx, f.as("bob"), "a", "d", "e", nil); return err },
		func() error { _, err := s.DeleteDocumentAs(ctx, f.as("bob"), "a", "e", nil, false); return err },
		func() error { _, err := s.DeleteDocumentAs(ctx, f.as("bob"), "a", "e", nil, true); return err },
	}
	for _, fn := range steps {
		_ = fn()
	}
	if len(*all) != len(steps) {
		t.Fatalf("%d Schreibvorgänge aufgezeichnet, erwartet %d", len(*all), len(steps))
	}
	for i, stmts := range *all {
		if want := q(queries.AccountLock); first(stmts) != want {
			t.Errorf("Vorgang %d: erste Anweisung %q, erwartet %q", i, first(stmts), want)
		}
	}
}
