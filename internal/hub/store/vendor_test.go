package store

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/kephalaion/kephalaion/internal/contract"
)

// vendorFixture ist newWriteStore mit zwei weiteren Accounts in a:
//
//	kp    User kp    nur der Scope vendor/k-playbook, kein write
//	voll  User voll  write und supersede, kein Scope
//
// dazu ein Dokument vom Admin unter vendor/k-playbook/ und eines direkt in
// vendor/.
func newVendorStore(t *testing.T) *writeFixture {
	t.Helper()
	ctx := context.Background()
	f := newWriteStore(t)
	s := f.s
	for _, a := range []struct {
		name   string
		rights contract.Rights
	}{
		{"kp", contract.Rights{Vendor: []string{"k-playbook"}}},
		{"voll", contract.Rights{Write: true, Supersede: true}},
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
	for _, name := range []string{"vendor/k-playbook/rules/a.md", "vendor/direkt.md"} {
		if _, err := s.PutDocument(ctx, "a", name, "vom Admin"); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

// wantForbidden prüft, dass fn mit ErrForbidden und dem Grund scheitert,
// ohne etwas zu schreiben.
func wantForbidden(t *testing.T, s *sqliteStore, what string, fn func() error, reason string) {
	t.Helper()
	u := snapshot(t, s)
	err := fn()
	if !errors.Is(err, ErrForbidden) || !strings.Contains(err.Error(), reason) {
		t.Errorf("%s: %v, erwartet forbidden mit %q", what, err, reason)
	}
	u.check(what)
}

// Unter vendor/<name>/ zählt allein der Scope: Ein Account nur mit dem Scope
// legt an, ändert und löscht dort — auch Fremdes vom Admin —, sonst nirgends.
// write und supersede ohne den Scope schreiben dort nicht; direkt in vendor/
// schreibt niemand. Die CLI am Hub darf überall.
func TestVendorScope(t *testing.T) {
	ctx := context.Background()
	f := newVendorStore(t)
	s := f.s
	kp, voll := f.as("kp"), f.as("voll")

	// Der Scope: anlegen, Fremdes ändern, löschen — ohne write, ohne
	// supersede.
	created, err := s.CreateDocumentAs(ctx, kp, "a", "vendor/k-playbook/rules/neu.md", "neu")
	if err != nil {
		t.Fatalf("Scope legt an: %v", err)
	}
	if r := created.Rows[0]; r.CreatedBy != "kp" {
		t.Errorf("Urheber: %+v", r)
	}
	if _, err := s.WriteDocumentAs(ctx, kp, "a", "vendor/k-playbook/rules/a.md", "vom Scope", nil); err != nil {
		t.Errorf("Scope ändert das Dokument des Admins: %v", err)
	}
	if _, err := s.DeleteDocumentAs(ctx, kp, "a", "vendor/k-playbook/rules/a.md", nil, false); err != nil {
		t.Errorf("Scope löscht das Dokument des Admins: %v", err)
	}
	if _, err := s.CreateDocumentAs(ctx, kp, "a", "vendor/k-playbook/tief/er/pfad.md", "x"); err != nil {
		t.Errorf("Scope legt tief an: %v", err)
	}

	// Sonst nirgends: nicht in der Wurzel, nicht unter einem anderen Scope,
	// nicht direkt in vendor/, auch nicht Eigenes.
	wantForbidden(t, s, "Scope legt in der Wurzel an", func() error {
		_, err := s.CreateDocumentAs(ctx, kp, "a", "x.md", "x")
		return err
	}, "anlegen: write fehlt")
	wantForbidden(t, s, "Scope legt unter anderem Scope an", func() error {
		_, err := s.CreateDocumentAs(ctx, kp, "a", "vendor/anders/x.md", "x")
		return err
	}, "Scope vendor/anders fehlt")
	wantForbidden(t, s, "Scope legt direkt in vendor/ an", func() error {
		_, err := s.CreateDocumentAs(ctx, kp, "a", "vendor/x.md", "x")
		return err
	}, "direkt in vendor/ schreibt niemand")
	wantForbidden(t, s, "Scope legt vendor an", func() error {
		_, err := s.CreateDocumentAs(ctx, kp, "a", "vendor", "x")
		return err
	}, "direkt in vendor/ schreibt niemand")
	wantForbidden(t, s, "Scope ändert direkt in vendor/", func() error {
		_, err := s.WriteDocumentAs(ctx, kp, "a", "vendor/direkt.md", "x", nil)
		return err
	}, "direkt in vendor/ schreibt niemand")
	if _, err := s.PutDocument(ctx, "a", "eigen.md", "x"); err != nil {
		t.Fatal(err)
	}
	wantForbidden(t, s, "Scope ändert Fremdes außerhalb", func() error {
		_, err := s.WriteDocumentAs(ctx, kp, "a", "eigen.md", "y", nil)
		return err
	}, "gehört admin, supersede fehlt")

	// write und supersede ohne Scope: nicht unter vendor/<name>/, auch
	// nicht Eigenes dort (voll hat es nie angelegt — aber ein Dokument von
	// bob unter vendor/ gibt es nicht, weil bob es nicht anlegen kann).
	wantForbidden(t, s, "write+supersede legt unter vendor/<name>/ an", func() error {
		_, err := s.CreateDocumentAs(ctx, voll, "a", "vendor/k-playbook/rules/voll.md", "x")
		return err
	}, "anlegen: Scope vendor/k-playbook fehlt")
	wantForbidden(t, s, "write+supersede ändert unter vendor/<name>/", func() error {
		_, err := s.WriteDocumentAs(ctx, voll, "a", "vendor/k-playbook/rules/neu.md", "x", nil)
		return err
	}, "ändern: Scope vendor/k-playbook fehlt")
	wantForbidden(t, s, "write+supersede löscht unter vendor/<name>/", func() error {
		_, err := s.DeleteDocumentAs(ctx, voll, "a", "vendor/k-playbook/rules/neu.md", nil, false)
		return err
	}, "löschen: Scope vendor/k-playbook fehlt")
	wantForbidden(t, s, "write+supersede direkt in vendor/", func() error {
		_, err := s.WriteDocumentAs(ctx, voll, "a", "vendor/direkt.md", "x", nil)
		return err
	}, "direkt in vendor/ schreibt niemand")
	wantForbidden(t, s, "write legt direkt in vendor/ an", func() error {
		_, err := s.CreateDocumentAs(ctx, f.as("bob"), "a", "vendor/neu.md", "x")
		return err
	}, "direkt in vendor/ schreibt niemand")

	// Die CLI am Hub darf wie überall.
	if _, err := s.PutDocument(ctx, "a", "vendor/k-playbook/rules/neu.md", "vom Admin"); err != nil {
		t.Errorf("Admin unter vendor/<name>/: %v", err)
	}
	if _, err := s.DeleteDocument(ctx, "a", "vendor/direkt.md"); err != nil {
		t.Errorf("Admin direkt in vendor/: %v", err)
	}
	// Nur die Kleinschreibung ist besonders: Vendor/ folgt der allgemeinen
	// Regel.
	if _, err := s.CreateDocumentAs(ctx, f.as("bob"), "a", "Vendor/k-playbook/x.md", "x"); err != nil {
		t.Errorf("Vendor/ mit write: %v", err)
	}
	wantForbidden(t, s, "Scope in Vendor/", func() error {
		_, err := s.CreateDocumentAs(ctx, kp, "a", "Vendor/k-playbook/y.md", "x")
		return err
	}, "write fehlt")
}

// rename wird mit altem und neuem Namen geprüft: hinein, heraus und innerhalb
// von vendor/<name>/ — bei einem Verzeichnis je Dokument, alles oder nichts.
func TestVendorRename(t *testing.T) {
	ctx := context.Background()
	f := newVendorStore(t)
	s := f.s
	kp, bob, voll := f.as("kp"), f.as("bob"), f.as("voll")
	createAll(t, s, bob, "a", "notes/x.md", "notes/y.md")
	createAll(t, s, kp, "a", "vendor/k-playbook/b.md", "vendor/k-playbook/sub/c.md")

	// Hinein braucht beides: das Recht am alten (write bei Eigenem) und den
	// Scope am neuen Namen.
	wantForbidden(t, s, "bob benennt hinein", func() error {
		_, err := s.RenameDocumentAs(ctx, bob, "a", "notes/x.md", "vendor/k-playbook/x.md", nil)
		return err
	}, "notes/x.md nach vendor/k-playbook/x.md in a umbenennen: Scope vendor/k-playbook fehlt")
	wantForbidden(t, s, "kp benennt Fremdes hinein", func() error {
		_, err := s.RenameDocumentAs(ctx, kp, "a", "notes/x.md", "vendor/k-playbook/x.md", nil)
		return err
	}, "notes/x.md in a umbenennen: gehört kleist, supersede fehlt")
	wantForbidden(t, s, "kp benennt heraus", func() error {
		_, err := s.RenameDocumentAs(ctx, kp, "a", "vendor/k-playbook/b.md", "b.md", nil)
		return err
	}, "vendor/k-playbook/b.md nach b.md in a umbenennen: write fehlt")
	wantForbidden(t, s, "voll benennt heraus", func() error {
		_, err := s.RenameDocumentAs(ctx, voll, "a", "vendor/k-playbook/b.md", "b.md", nil)
		return err
	}, "vendor/k-playbook/b.md in a umbenennen: Scope vendor/k-playbook fehlt")
	wantForbidden(t, s, "kp benennt direkt nach vendor/", func() error {
		_, err := s.RenameDocumentAs(ctx, kp, "a", "vendor/k-playbook/b.md", "vendor/b.md", nil)
		return err
	}, "direkt in vendor/ schreibt niemand")
	wantForbidden(t, s, "kp benennt unter anderen Scope", func() error {
		_, err := s.RenameDocumentAs(ctx, kp, "a", "vendor/k-playbook/b.md", "vendor/anders/b.md", nil)
		return err
	}, "Scope vendor/anders fehlt")

	// Innerhalb mit dem Scope, auch das Dokument des Admins.
	if _, err := s.RenameDocumentAs(ctx, kp, "a", "vendor/k-playbook/rules/a.md", "vendor/k-playbook/rules/b.md", nil); err != nil {
		t.Errorf("kp benennt das Dokument des Admins um: %v", err)
	}
	if _, err := s.RenameDocumentAs(ctx, kp, "a", "vendor/k-playbook/b.md", "vendor/k-playbook/sub/b.md", nil); err != nil {
		t.Errorf("kp benennt innerhalb um: %v", err)
	}
	// Ein Verzeichnis innerhalb: alle Dokumente darunter.
	res, err := s.RenameDocumentAs(ctx, kp, "a", "vendor/k-playbook/sub", "vendor/k-playbook/neu", nil)
	if err != nil {
		t.Fatalf("kp benennt Verzeichnis um: %v", err)
	}
	if got := rowNames(res.Rows); !reflect.DeepEqual(got, []string{"vendor/k-playbook/neu/b.md", "vendor/k-playbook/neu/c.md"}) {
		t.Errorf("Zeilen: %v", got)
	}
	// Ein Verzeichnis heraus: je Dokument, das erste verbotene lässt alles
	// scheitern — mit dem Scope fehlt write für das Ziel.
	wantForbidden(t, s, "kp benennt Verzeichnis heraus", func() error {
		_, err := s.RenameDocumentAs(ctx, kp, "a", "vendor/k-playbook/neu", "neu", nil)
		return err
	}, "vendor/k-playbook/neu/b.md nach neu/b.md in a umbenennen: write fehlt")
	// Hinein mit einem Verzeichnis von bob: der Scope fehlt am neuen Namen.
	wantForbidden(t, s, "bob benennt Verzeichnis hinein", func() error {
		_, err := s.RenameDocumentAs(ctx, bob, "a", "notes", "vendor/k-playbook/notes", nil)
		return err
	}, "notes/x.md nach vendor/k-playbook/notes/x.md in a umbenennen: Scope vendor/k-playbook fehlt")
	// Ganz vendor/<name> umbenennen: der neue Name liegt nicht mehr im Scope.
	wantForbidden(t, s, "kp benennt vendor/k-playbook um", func() error {
		_, err := s.RenameDocumentAs(ctx, kp, "a", "vendor/k-playbook", "vendor/anders", nil)
		return err
	}, "Scope vendor/anders fehlt")
	// Der Admin darf hinein und heraus; danach gehört das Dokument weiter
	// kleist, und kp darf es unter vendor/ trotzdem ändern.
	if _, err := s.RenameDocumentAs(ctx, voll, "a", "notes/y.md", "notes/z.md", nil); err != nil {
		t.Errorf("voll benennt Fremdes außerhalb um: %v", err)
	}
	want := []string{"notes/x.md", "notes/z.md", "vendor/direkt.md", "vendor/k-playbook/neu/b.md",
		"vendor/k-playbook/neu/c.md", "vendor/k-playbook/rules/b.md"}
	if got := liveNames(t, s, "a"); !reflect.DeepEqual(got, want) {
		t.Errorf("Dokumente: %v", got)
	}
}

// delete mit recursive über vendor/<name>/: mit dem Scope alle Dokumente
// darunter, gleich wer sie angelegt hat; ohne den Scope nichts, auch nicht
// mit write und supersede. Das Verzeichnis vendor selbst enthält Dokumente
// direkt darin und darunter — dort scheitert der Scope am direkten.
func TestVendorDeleteRecursive(t *testing.T) {
	ctx := context.Background()
	f := newVendorStore(t)
	s := f.s
	kp, voll := f.as("kp"), f.as("voll")
	createAll(t, s, kp, "a", "vendor/k-playbook/b.md", "vendor/k-playbook/sub/c.md")

	wantForbidden(t, s, "voll löscht vendor/<name> rekursiv", func() error {
		_, err := s.DeleteDocumentAs(ctx, voll, "a", "vendor/k-playbook", nil, true)
		return err
	}, "Scope vendor/k-playbook fehlt")
	wantForbidden(t, s, "kp löscht vendor rekursiv", func() error {
		_, err := s.DeleteDocumentAs(ctx, kp, "a", "vendor", nil, true)
		return err
	}, "vendor/direkt.md in a löschen: direkt in vendor/ schreibt niemand")
	wantForbidden(t, s, "voll löscht vendor rekursiv", func() error {
		_, err := s.DeleteDocumentAs(ctx, voll, "a", "vendor", nil, true)
		return err
	}, "direkt in vendor/ schreibt niemand")
	res, err := s.DeleteDocumentAs(ctx, kp, "a", "vendor/k-playbook", nil, true)
	if err != nil {
		t.Fatalf("kp löscht vendor/k-playbook rekursiv: %v", err)
	}
	if got := rowNames(res.Rows); !reflect.DeepEqual(got, []string{"vendor/k-playbook/b.md", "vendor/k-playbook/rules/a.md",
		"vendor/k-playbook/sub/c.md"}) {
		t.Errorf("Löschmarken: %v", got)
	}
	if got := liveNames(t, s, "a"); !reflect.DeepEqual(got, []string{"vendor/direkt.md"}) {
		t.Errorf("Dokumente: %v", got)
	}
}

// grant prüft den Namen des Scopes; die Rechte stehen in der Zeile sortiert
// und ohne Doppel; show nennt sie. Ein gesperrter Account merkt den Scope,
// unlock legt die Zeile damit wieder an, und das Schreiben geht danach
// wieder — während der Sperre nicht.
func TestVendorGrantAndLock(t *testing.T) {
	ctx := context.Background()
	f := newVendorStore(t)
	s := f.s
	kp := f.as("kp")

	for _, bad := range []string{"", "K-Playbook", "system", "a/b", "a:b"} {
		u := snapshot(t, s)
		if _, err := s.GrantAccount(ctx, "kp", "a", contract.Rights{Vendor: []string{bad}}); err == nil ||
			!strings.Contains(err.Error(), "Scope vendor/<name>") {
			t.Errorf("Scope %q angenommen: %v", bad, err)
		}
		u.check("Scope " + bad)
	}
	changed, err := s.GrantAccount(ctx, "kp", "a", contract.Rights{Vendor: []string{"k-playbook", "k-playbook"}})
	if err != nil || changed {
		t.Errorf("dieselben Rechte mit Doppel: changed %v, %v", changed, err)
	}
	changed, err = s.GrantAccount(ctx, "kp", "a", contract.Rights{Write: true, Vendor: []string{"zwei", "k-playbook"}})
	if err != nil || !changed {
		t.Fatalf("grant: changed %v, %v", changed, err)
	}
	rows := accountRowsAll(t, s, "kp")
	if len(rows) != 1 || !reflect.DeepEqual(rows[0].content.Rights, contract.Rights{Write: true, Vendor: []string{"k-playbook", "zwei"}}) {
		t.Errorf("Zeile: %+v", rows)
	}
	a, err := s.Account(ctx, "kp")
	if err != nil || a.Rights[0].String() != "read, write, vendor/k-playbook, vendor/zwei" {
		t.Errorf("Account: %+v, %v", a, err)
	}
	// Ohne --vendor geht jeder Scope: read bleibt.
	if _, err := s.GrantAccount(ctx, "kp", "a", contract.Rights{Vendor: []string{"k-playbook"}}); err != nil {
		t.Fatal(err)
	}

	// Gesperrt: die Zeile ist Löschmarke, das Schreiben scheitert an der
	// Anmeldung; die gemerkten Rechte tragen den Scope.
	if err := s.SetAccountLocked(ctx, "kp", true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateDocumentAs(ctx, kp, "a", "vendor/k-playbook/gesperrt.md", "x"); !errors.Is(err, ErrAccountAuth) {
		t.Errorf("gesperrt: %v", err)
	}
	a, _ = s.Account(ctx, "kp")
	if !a.Locked || len(a.Rights) != 1 || !reflect.DeepEqual(a.Rights[0].Rights, contract.Rights{Vendor: []string{"k-playbook"}}) {
		t.Errorf("gemerkte Rechte: %+v", a)
	}
	// Während der Sperre einen zweiten Scope geben: nur gemerkt.
	if _, err := s.GrantAccount(ctx, "kp", "a", contract.Rights{Vendor: []string{"k-playbook", "drei"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAccountLocked(ctx, "kp", false); err != nil {
		t.Fatal(err)
	}
	rows = accountRowsAll(t, s, "kp")
	if len(rows) != 1 || rows[0].deleted || !reflect.DeepEqual(rows[0].content.Rights, contract.Rights{Vendor: []string{"drei", "k-playbook"}}) {
		t.Errorf("Zeile nach unlock: %+v", rows)
	}
	if _, err := s.CreateDocumentAs(ctx, kp, "a", "vendor/k-playbook/wieder.md", "x"); err != nil {
		t.Errorf("nach unlock: %v", err)
	}
	if _, err := s.CreateDocumentAs(ctx, kp, "a", "vendor/drei/x.md", "x"); err != nil {
		t.Errorf("zweiter Scope nach unlock: %v", err)
	}

	// Import prüft die Scopes wie grant.
	tables, err := s.Tables(ctx)
	if err != nil {
		t.Fatal(err)
	}
	bad := tables
	bad.Accounts = append([]Account{}, tables.Accounts...)
	for i, a := range bad.Accounts {
		if a.Name == "kp" {
			bad.Accounts[i].Rights = []AccountRight{{Collection: "a", Rights: contract.Rights{Vendor: []string{"Falsch"}}}}
		}
	}
	if err := CheckTables(bad); err == nil || !strings.Contains(err.Error(), "Account kp in a: Scope vendor/<name>") {
		t.Errorf("CheckTables mit falschem Scope: %v", err)
	}
	if err := CheckTables(tables); err != nil {
		t.Errorf("CheckTables: %v", err)
	}
}
