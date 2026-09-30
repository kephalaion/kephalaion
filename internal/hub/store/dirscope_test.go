package store

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/kephalaion/kephalaion/internal/contract"
)

// newDirScopeStore ist newWriteStore mit zwei weiteren Accounts in a:
//
//	pusher  User pusher  nur der Verzeichnis-Scope docs, kein write
//	teil    User teil    nur der Verzeichnis-Scope docs/sub
//
// dazu Dokumente vom Admin unter docs/ und docs/sub/, eines in docs2/ und
// eines von bob (User kleist) unter docs/ und unter notes/.
func newDirScopeStore(t *testing.T) *writeFixture {
	t.Helper()
	ctx := context.Background()
	f := newWriteStore(t)
	s := f.s
	for _, a := range []struct {
		name   string
		rights contract.Rights
	}{
		{"pusher", contract.Rights{Dirs: []string{"docs"}}},
		{"teil", contract.Rights{Dirs: []string{"docs/sub"}}},
	} {
		token, err := s.AddAccount(ctx, a.name, a.name, "")
		if err != nil {
			t.Fatal(err)
		}
		f.tokens[a.name] = token
		if _, err := s.GrantAccount(ctx, a.name, "a", a.rights); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"docs/admin.md", "docs/sub/admin.md", "docs2/admin.md"} {
		if _, err := s.PutDocument(ctx, "a", name, "vom Admin"); err != nil {
			t.Fatal(err)
		}
	}
	createAll(t, s, f.as("bob"), "a", "docs/bob.md", "notes/bob.md")
	return f
}

// Unter dem Verzeichnis eines Verzeichnis-Scopes schreibt der Account ohne
// write und unabhängig vom Urheber: anlegen, Fremdes ändern und löschen.
// Daneben (docs2/), darüber (Wurzel) und anderswo gibt der Scope nichts; der
// Grund ist dann der der allgemeinen Regel. write ohne Scope ändert dort
// weiter nur Eigenes — der Scope nimmt niemandem etwas.
func TestDirScopeWrite(t *testing.T) {
	ctx := context.Background()
	f := newDirScopeStore(t)
	s := f.s
	pusher, bob := f.as("pusher"), f.as("bob")

	created, err := s.CreateDocumentAs(ctx, pusher, "a", "docs/neu.md", "neu")
	if err != nil {
		t.Fatalf("Scope legt an: %v", err)
	}
	if r := created.Rows[0]; r.CreatedBy != "pusher" {
		t.Errorf("Urheber: %+v", r)
	}
	if _, err := s.CreateDocumentAs(ctx, pusher, "a", "docs/tief/er/pfad.md", "x"); err != nil {
		t.Errorf("Scope legt tief an: %v", err)
	}
	if _, err := s.WriteDocumentAs(ctx, pusher, "a", "docs/admin.md", "vom Scope", nil); err != nil {
		t.Errorf("Scope ändert das Dokument des Admins: %v", err)
	}
	if _, err := s.WriteDocumentAs(ctx, pusher, "a", "docs/bob.md", "vom Scope", nil); err != nil {
		t.Errorf("Scope ändert das Dokument von bob: %v", err)
	}
	if _, err := s.DeleteDocumentAs(ctx, pusher, "a", "docs/bob.md", nil, false); err != nil {
		t.Errorf("Scope löscht das Dokument von bob: %v", err)
	}

	// Daneben, darüber, anderswo: nichts.
	for _, c := range []struct{ what, name, reason string }{
		{"in der Wurzel", "x.md", "anlegen: write fehlt"},
		{"in docs2/", "docs2/x.md", "anlegen: write fehlt"},
		{"in notes/", "notes/x.md", "anlegen: write fehlt"},
		{"das Verzeichnis selbst als Dokument", "docs", "anlegen: write fehlt"},
		{"docs tiefer", "notes/docs/x.md", "anlegen: write fehlt"},
		{"unter vendor/<name>/", "vendor/docs/x.md", "Scope vendor/docs fehlt"},
	} {
		wantForbidden(t, s, "Scope legt "+c.what+" an", func() error {
			_, err := s.CreateDocumentAs(ctx, pusher, "a", c.name, "x")
			return err
		}, c.reason)
	}
	wantForbidden(t, s, "Scope ändert Fremdes in docs2/", func() error {
		_, err := s.WriteDocumentAs(ctx, pusher, "a", "docs2/admin.md", "x", nil)
		return err
	}, "Dokument docs2/admin.md in a ändern: gehört admin, supersede fehlt")
	wantForbidden(t, s, "Scope löscht Fremdes in notes/", func() error {
		_, err := s.DeleteDocumentAs(ctx, pusher, "a", "notes/bob.md", nil, false)
		return err
	}, "gehört kleist, supersede fehlt")

	// write ohne Scope: unter docs/ Neues und Eigenes wie bisher, Fremdes
	// nicht.
	if _, err := s.CreateDocumentAs(ctx, bob, "a", "docs/bob2.md", "x"); err != nil {
		t.Errorf("write legt unter docs/ an: %v", err)
	}
	wantForbidden(t, s, "write ändert Fremdes unter docs/", func() error {
		_, err := s.WriteDocumentAs(ctx, bob, "a", "docs/neu.md", "x", nil)
		return err
	}, "Dokument docs/neu.md in a ändern: gehört pusher, supersede fehlt")
	// supersede ohne Scope: Fremdes unter docs/ wie bisher.
	if _, err := s.WriteDocumentAs(ctx, f.as("eve"), "a", "docs/neu.md", "von eve", nil); err != nil {
		t.Errorf("supersede ändert Fremdes unter docs/: %v", err)
	}
	// Der geschachtelte Scope docs/sub deckt docs/ nicht.
	wantForbidden(t, s, "Scope docs/sub legt in docs/ an", func() error {
		_, err := s.CreateDocumentAs(ctx, f.as("teil"), "a", "docs/x.md", "x")
		return err
	}, "anlegen: write fehlt")
	if _, err := s.WriteDocumentAs(ctx, f.as("teil"), "a", "docs/sub/admin.md", "von teil", nil); err != nil {
		t.Errorf("Scope docs/sub ändert darunter: %v", err)
	}
}

// rename mit altem und neuem Namen: innerhalb des Scopes geht es auch mit
// Fremdem, hinein und heraus braucht die allgemeine Regel an der Seite
// außerhalb; ein Verzeichnis je Dokument, alles oder nichts.
func TestDirScopeRename(t *testing.T) {
	ctx := context.Background()
	f := newDirScopeStore(t)
	s := f.s
	pusher, bob := f.as("pusher"), f.as("bob")
	createAll(t, s, pusher, "a", "docs/eigen.md")

	if _, err := s.RenameDocumentAs(ctx, pusher, "a", "docs/admin.md", "docs/sub/admin2.md", nil); err != nil {
		t.Errorf("Scope benennt Fremdes innerhalb um: %v", err)
	}
	wantForbidden(t, s, "Scope benennt heraus", func() error {
		_, err := s.RenameDocumentAs(ctx, pusher, "a", "docs/eigen.md", "eigen.md", nil)
		return err
	}, "Dokument docs/eigen.md nach eigen.md in a umbenennen: write fehlt")
	wantForbidden(t, s, "Scope benennt nach docs2/", func() error {
		_, err := s.RenameDocumentAs(ctx, pusher, "a", "docs/eigen.md", "docs2/eigen.md", nil)
		return err
	}, "nach docs2/eigen.md in a umbenennen: write fehlt")
	wantForbidden(t, s, "Scope benennt Fremdes hinein", func() error {
		_, err := s.RenameDocumentAs(ctx, pusher, "a", "notes/bob.md", "docs/bob-notes.md", nil)
		return err
	}, "Dokument notes/bob.md in a umbenennen: gehört kleist, supersede fehlt")
	// write: Eigenes hinein und heraus wie bisher.
	if _, err := s.RenameDocumentAs(ctx, bob, "a", "notes/bob.md", "docs/bob-notes.md", nil); err != nil {
		t.Errorf("write benennt Eigenes hinein: %v", err)
	}
	if _, err := s.RenameDocumentAs(ctx, bob, "a", "docs/bob.md", "notes/bob.md", nil); err != nil {
		t.Errorf("write benennt Eigenes heraus: %v", err)
	}
	// Ein Verzeichnis innerhalb: alle Dokumente darunter, gleich von wem.
	res, err := s.RenameDocumentAs(ctx, pusher, "a", "docs/sub", "docs/neu", nil)
	if err != nil {
		t.Fatalf("Scope benennt Verzeichnis innerhalb um: %v", err)
	}
	if got := rowNames(res.Rows); !reflect.DeepEqual(got, []string{"docs/neu/admin.md", "docs/neu/admin2.md"}) {
		t.Errorf("Zeilen: %v", got)
	}
	// Das Verzeichnis des Scopes selbst: der neue Name liegt außerhalb.
	wantForbidden(t, s, "Scope benennt docs um", func() error {
		_, err := s.RenameDocumentAs(ctx, pusher, "a", "docs", "docs2/docs", nil)
		return err
	}, "in a umbenennen: gehört")
	want := []string{"docs/bob-notes.md", "docs/eigen.md", "docs/neu/admin.md", "docs/neu/admin2.md", "docs2/admin.md", "notes/bob.md"}
	if got := liveNames(t, s, "a"); !reflect.DeepEqual(got, want) {
		t.Errorf("Dokumente: %v", got)
	}
}

// delete mit recursive über ein Verzeichnis, das der Scope nur teilweise
// deckt: alles oder nichts — der Scope docs/sub löscht docs nicht, auch nicht
// den Teil darunter; docs/sub selbst ganz. Der Scope docs löscht docs ganz,
// gleich wer die Dokumente angelegt hat.
func TestDirScopeDeleteRecursive(t *testing.T) {
	ctx := context.Background()
	f := newDirScopeStore(t)
	s := f.s
	teil, pusher := f.as("teil"), f.as("pusher")
	createAll(t, s, teil, "a", "docs/sub/teil.md")

	wantForbidden(t, s, "docs/sub löscht docs rekursiv", func() error {
		_, err := s.DeleteDocumentAs(ctx, teil, "a", "docs", nil, true)
		return err
	}, "Dokument docs/admin.md in a löschen: gehört admin, supersede fehlt")
	res, err := s.DeleteDocumentAs(ctx, teil, "a", "docs/sub", nil, true)
	if err != nil {
		t.Fatalf("docs/sub löscht docs/sub rekursiv: %v", err)
	}
	if got := rowNames(res.Rows); !reflect.DeepEqual(got, []string{"docs/sub/admin.md", "docs/sub/teil.md"}) {
		t.Errorf("Löschmarken: %v", got)
	}
	res, err = s.DeleteDocumentAs(ctx, pusher, "a", "docs", nil, true)
	if err != nil {
		t.Fatalf("docs löscht docs rekursiv: %v", err)
	}
	if got := rowNames(res.Rows); !reflect.DeepEqual(got, []string{"docs/admin.md", "docs/bob.md"}) {
		t.Errorf("Löschmarken: %v", got)
	}
	if got := liveNames(t, s, "a"); !reflect.DeepEqual(got, []string{"docs2/admin.md", "notes/bob.md"}) {
		t.Errorf("Dokumente: %v", got)
	}
}

// grant prüft die Verzeichnis-Scopes; sie stehen in der Zeile sortiert und
// ohne Doppel, show nennt sie als „dir <pfad>/“. Ein gesperrter Account
// merkt sie, unlock legt die Zeile damit wieder an; während der Sperre
// schreibt er nicht. Import prüft sie wie grant.
func TestDirScopeGrantAndLock(t *testing.T) {
	ctx := context.Background()
	f := newDirScopeStore(t)
	s := f.s
	pusher := f.as("pusher")

	for _, c := range []struct{ dir, want string }{
		{"", "Verzeichnis-Scope: Verzeichnis fehlt"},
		{"vendor", "unter vendor/ gilt allein der Scope vendor/<name>"},
		{"vendor/x", "unter vendor/ gilt allein"},
		{"..", "'.' und '..'"},
		{"docs/", "endet mit '/'"},
	} {
		u := snapshot(t, s)
		if _, err := s.GrantAccount(ctx, "pusher", "a", contract.Rights{Dirs: []string{c.dir}}); err == nil ||
			!strings.Contains(err.Error(), c.want) {
			t.Errorf("Verzeichnis-Scope %q angenommen: %v", c.dir, err)
		}
		u.check("Verzeichnis-Scope " + c.dir)
	}
	changed, err := s.GrantAccount(ctx, "pusher", "a", contract.Rights{Dirs: []string{"docs", "docs"}})
	if err != nil || changed {
		t.Errorf("dieselben Rechte mit Doppel: changed %v, %v", changed, err)
	}
	changed, err = s.GrantAccount(ctx, "pusher", "a", contract.Rights{Vendor: []string{"k"}, Dirs: []string{"docs", "a/b"}})
	if err != nil || !changed {
		t.Fatalf("grant: changed %v, %v", changed, err)
	}
	rows := accountRowsAll(t, s, "pusher")
	if len(rows) != 1 || !reflect.DeepEqual(rows[0].content.Rights, contract.Rights{Vendor: []string{"k"}, Dirs: []string{"a/b", "docs"}}) {
		t.Errorf("Zeile: %+v", rows)
	}
	a, err := s.Account(ctx, "pusher")
	if err != nil || a.Rights[0].String() != "read, vendor/k, dir a/b/, dir docs/" {
		t.Errorf("Account: %+v, %v", a, err)
	}
	// Ohne Verzeichnis-Scope ist er entzogen: danach nicht mehr unter docs/.
	if _, err := s.GrantAccount(ctx, "pusher", "a", contract.Rights{}); err != nil {
		t.Fatal(err)
	}
	wantForbidden(t, s, "nach Entzug", func() error {
		_, err := s.CreateDocumentAs(ctx, pusher, "a", "docs/x.md", "x")
		return err
	}, "write fehlt")
	if _, err := s.GrantAccount(ctx, "pusher", "a", contract.Rights{Dirs: []string{"docs"}}); err != nil {
		t.Fatal(err)
	}

	if err := s.SetAccountLocked(ctx, "pusher", true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateDocumentAs(ctx, pusher, "a", "docs/gesperrt.md", "x"); !errors.Is(err, ErrAccountAuth) {
		t.Errorf("gesperrt: %v", err)
	}
	a, _ = s.Account(ctx, "pusher")
	if !a.Locked || len(a.Rights) != 1 || !reflect.DeepEqual(a.Rights[0].Rights, contract.Rights{Dirs: []string{"docs"}}) {
		t.Errorf("gemerkte Rechte: %+v", a)
	}
	// Während der Sperre einen zweiten geben: nur gemerkt.
	if _, err := s.GrantAccount(ctx, "pusher", "a", contract.Rights{Dirs: []string{"notes", "docs"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAccountLocked(ctx, "pusher", false); err != nil {
		t.Fatal(err)
	}
	rows = accountRowsAll(t, s, "pusher")
	last := rows[len(rows)-1]
	if last.deleted || !reflect.DeepEqual(last.content.Rights, contract.Rights{Dirs: []string{"docs", "notes"}}) {
		t.Errorf("Zeile nach unlock: %+v", rows)
	}
	if _, err := s.WriteDocumentAs(ctx, pusher, "a", "notes/bob.md", "nach unlock", nil); err != nil {
		t.Errorf("zweiter Scope nach unlock: %v", err)
	}

	// Import prüft die Verzeichnis-Scopes wie grant.
	tables, err := s.Tables(ctx)
	if err != nil {
		t.Fatal(err)
	}
	bad := tables
	bad.Accounts = append([]Account{}, tables.Accounts...)
	for i, a := range bad.Accounts {
		if a.Name == "pusher" {
			bad.Accounts[i].Rights = []AccountRight{{Collection: "a", Rights: contract.Rights{Dirs: []string{"vendor/x"}}}}
		}
	}
	if err := CheckTables(bad); err == nil || !strings.Contains(err.Error(), "Account pusher in a: Verzeichnis-Scope \"vendor/x\"") {
		t.Errorf("CheckTables mit falschem Verzeichnis-Scope: %v", err)
	}
	if err := CheckTables(tables); err != nil {
		t.Errorf("CheckTables: %v", err)
	}
}
