package store

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/kephalaion/kephalaion/internal/contract"
	"github.com/kephalaion/kephalaion/internal/ident"
)

// sysRow ist eine Zeile SYSTEM:A:<account> samt Löschmarken, wie sie in
// documents steht.
type sysRow struct {
	id, collection       string
	deleted              bool
	revision             int64
	content              contract.AccountContent
	createdBy, updatedBy string
}

// accountRowsAll liest alle Zeilen eines Accounts, auch Löschmarken, nach
// Collection.
func accountRowsAll(t *testing.T, s *sqliteStore, account string) []sysRow {
	t.Helper()
	rows, err := s.db.QueryContext(context.Background(), `SELECT id, collection, content, deleted, revision,
		created_by, updated_by FROM documents WHERE name = ? ORDER BY collection, revision`, contract.AccountRowName(account))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []sysRow
	for rows.Next() {
		var r sysRow
		var content sql.NullString
		var deleted int
		if err := rows.Scan(&r.id, &r.collection, &content, &deleted, &r.revision, &r.createdBy, &r.updatedBy); err != nil {
			t.Fatal(err)
		}
		r.deleted = deleted != 0
		if content.Valid {
			c, err := contract.DecodeAccountContent(content.String)
			if err != nil {
				t.Fatal(err)
			}
			r.content = c
		} else if !r.deleted {
			t.Fatalf("lebende Zeile ohne Inhalt: %+v", r)
		}
		out = append(out, r)
	}
	return out
}

type fullAction struct {
	account, action string
	carrier         sql.NullString
	subject         sql.NullString
	revision        sql.NullInt64
}

func lastAction(t *testing.T, s *sqliteStore) fullAction {
	t.Helper()
	var a fullAction
	err := s.db.QueryRowContext(context.Background(), `SELECT account, action, carrier, subject, revision
		FROM actions ORDER BY rowid DESC LIMIT 1`).Scan(&a.account, &a.action, &a.carrier, &a.subject, &a.revision)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func ptr(s string) *string { return &s }

func newAccountStore(t *testing.T) *sqliteStore {
	t.Helper()
	s := newStore(t)
	for _, c := range []string{"team-x", "privat"} {
		if err := s.AddCollection(context.Background(), c, ""); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func TestAccountNames(t *testing.T) {
	ctx := context.Background()
	s := newAccountStore(t)
	if _, err := s.AddNode(ctx, "laptop", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddAccount(ctx, "laptop", "laptop", ""); err == nil || !strings.Contains(err.Error(), "Node") {
		t.Errorf("Account mit Node-Namen: %v", err)
	}
	for _, bad := range []string{"admin", "Alice", "system-x", "a:b", ""} {
		if _, err := s.AddAccount(ctx, bad, bad, ""); err == nil {
			t.Errorf("Account %q angenommen", bad)
		}
	}
	if _, err := s.AddNode(ctx, "admin", ""); err == nil || !strings.Contains(err.Error(), "reserviert") {
		t.Errorf("Node admin: %v", err)
	}
	// Collections dürfen admin heißen.
	if err := s.AddCollection(ctx, "admin", ""); err != nil {
		t.Errorf("Collection admin: %v", err)
	}
	if _, err := s.AddAccount(ctx, "alice", "alice", "Alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddAccount(ctx, "alice", "alice", ""); !errors.Is(err, ErrExists) {
		t.Errorf("doppelt: %v", err)
	}
	if _, err := s.GrantAccount(ctx, "alice", "team-x", contract.Rights{}); err != nil {
		t.Fatal(err)
	}
	// Nach rm ist der Name frei — für einen Node wie für einen Account —,
	// die Löschmarke bleibt.
	if err := s.RemoveAccount(ctx, "alice"); err != nil {
		t.Fatal(err)
	}
	if rows := accountRowsAll(t, s, "alice"); len(rows) != 1 || !rows[0].deleted {
		t.Errorf("Zeilen nach rm: %+v", rows)
	}
	if _, err := s.AddNode(ctx, "alice", ""); err != nil {
		t.Errorf("Node alice nach rm: %v", err)
	}
	if err := s.RemoveNode(ctx, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddAccount(ctx, "alice", "alice", ""); err != nil {
		t.Fatalf("Account alice neu: %v", err)
	}
	// Ein neuer Account gleichen Namens schreibt über die Löschmarke.
	before := accountRowsAll(t, s, "alice")
	if _, err := s.GrantAccount(ctx, "alice", "team-x", contract.Rights{}); err != nil {
		t.Fatal(err)
	}
	after := accountRowsAll(t, s, "alice")
	if len(after) != 1 || after[0].deleted || after[0].id != before[0].id || after[0].revision <= before[0].revision {
		t.Errorf("wiederbelebt: vorher %+v, nachher %+v", before, after)
	}
}

func TestAccountLifecycle(t *testing.T) {
	ctx := context.Background()
	s := newAccountStore(t)
	token, err := s.AddAccount(ctx, "bob", "bob", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := ident.CheckToken(token); err != nil {
		t.Fatal(err)
	}
	if rev := revision(t, s); rev != 0 {
		t.Errorf("add ohne Collection zählt Revision %d", rev)
	}
	if a := lastAction(t, s); a.action != "account.add" || a.account != Admin || a.subject.String != "bob" || a.revision.Valid {
		t.Errorf("actions add = %+v", a)
	}
	hash := ident.HashToken(token)

	// grant: eine Revision, eine Zeile, Hash aus accounts.
	changed, err := s.GrantAccount(ctx, "bob", "team-x", contract.Rights{Write: true})
	if err != nil || !changed {
		t.Fatal(changed, err)
	}
	if rev := revision(t, s); rev != 1 {
		t.Errorf("Revision nach grant = %d", rev)
	}
	if a := lastAction(t, s); a.action != "account.grant" || a.subject.String != "bob:team-x" || a.revision.Int64 != 1 {
		t.Errorf("actions grant = %+v", a)
	}
	if _, err := s.GrantAccount(ctx, "bob", "privat", contract.Rights{}); err != nil {
		t.Fatal(err)
	}
	rows := accountRowsAll(t, s, "bob")
	if len(rows) != 2 || rows[0].collection != "privat" || !reflect.DeepEqual(rows[1].content, contract.AccountContent{Hash: hash, User: "bob", Rights: contract.Rights{Write: true}}) {
		t.Errorf("Zeilen = %+v", rows)
	}
	// Dieselben Rechte noch einmal: nichts geschrieben.
	rev := revision(t, s)
	if changed, err := s.GrantAccount(ctx, "bob", "team-x", contract.Rights{Write: true}); err != nil || changed {
		t.Errorf("unverändert: %v, %v", changed, err)
	}
	if revision(t, s) != rev {
		t.Error("unverändertes grant zählt eine Revision")
	}
	// grant ohne write entzieht write; supersede ist unabhängig.
	if _, err := s.GrantAccount(ctx, "bob", "team-x", contract.Rights{Supersede: true}); err != nil {
		t.Fatal(err)
	}
	a, err := s.Account(ctx, "bob")
	if err != nil {
		t.Fatal(err)
	}
	want := []AccountRight{{"privat", contract.Rights{}}, {"team-x", contract.Rights{Supersede: true}}}
	if !reflect.DeepEqual(a.Rights, want) {
		t.Errorf("Rechte = %+v", a.Rights)
	}
	if _, err := s.GrantAccount(ctx, "bob", "fehlt", contract.Rights{}); !errors.Is(err, ErrNotFound) {
		t.Errorf("grant auf fehlende Collection: %v", err)
	}
	if _, err := s.GrantAccount(ctx, "niemand", "team-x", contract.Rights{}); !errors.Is(err, ErrNotFound) {
		t.Errorf("grant auf fehlenden Account: %v", err)
	}

	// token: Hash in accounts und allen Zeilen, eine Revision.
	rev = revision(t, s)
	token2, err := s.NewAccountToken(ctx, "bob")
	if err != nil {
		t.Fatal(err)
	}
	hash2 := ident.HashToken(token2)
	a, _ = s.Account(ctx, "bob")
	if a.TokenHash != hash2 || revision(t, s) != rev+1 {
		t.Errorf("token: Hash %v, Revision %d", a.TokenHash == hash2, revision(t, s))
	}
	for _, r := range accountRowsAll(t, s, "bob") {
		if r.content.Hash != hash2 || r.revision != rev+1 {
			t.Errorf("Zeile nach token: %+v", r)
		}
	}

	// lock: alle Zeilen Löschmarken unter einer Revision, Rechte gemerkt.
	rev = revision(t, s)
	if err := s.SetAccountLocked(ctx, "bob", true); err != nil {
		t.Fatal(err)
	}
	for _, r := range accountRowsAll(t, s, "bob") {
		if !r.deleted || r.revision != rev+1 {
			t.Errorf("Zeile nach lock: %+v", r)
		}
	}
	if a := lastAction(t, s); a.action != "account.lock" || a.revision.Int64 != rev+1 {
		t.Errorf("actions lock = %+v", a)
	}
	a, _ = s.Account(ctx, "bob")
	if !a.Locked || !reflect.DeepEqual(a.Rights, want) || a.TokenHash != hash2 {
		t.Errorf("gesperrt: %+v", a)
	}
	if err := s.SetAccountLocked(ctx, "bob", true); err == nil {
		t.Error("zweimal gesperrt")
	}
	// Gesperrt ändern grant und revoke nur die gemerkten Rechte.
	rev = revision(t, s)
	if _, err := s.GrantAccount(ctx, "bob", "team-x", contract.Rights{Write: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeAccount(ctx, "bob", "privat"); err != nil {
		t.Fatal(err)
	}
	if revision(t, s) != rev {
		t.Error("grant/revoke im gesperrten Zustand zählt eine Revision")
	}
	// Eine gemerkte Collection lässt sich nicht entfernen.
	if err := s.AddCollection(ctx, "neu", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GrantAccount(ctx, "bob", "neu", contract.Rights{}); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveCollection(ctx, "neu"); !errors.Is(err, ErrInUse) || !strings.Contains(err.Error(), "bob") {
		t.Errorf("rm gemerkter Collection: %v", err)
	}
	if err := s.RevokeAccount(ctx, "bob", "neu"); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveCollection(ctx, "neu"); err != nil {
		t.Errorf("rm nach revoke: %v", err)
	}

	// unlock: Zeilen aus den gemerkten Rechten, Hash aus accounts.
	rev = revision(t, s)
	if err := s.SetAccountLocked(ctx, "bob", false); err != nil {
		t.Fatal(err)
	}
	rows = accountRowsAll(t, s, "bob")
	if len(rows) != 2 || !rows[0].deleted || rows[1].deleted || rows[1].collection != "team-x" ||
		!reflect.DeepEqual(rows[1].content, contract.AccountContent{Hash: hash2, User: "bob", Rights: contract.Rights{Write: true}}) || rows[1].revision != rev+1 {
		t.Errorf("Zeilen nach unlock: %+v", rows)
	}
	a, _ = s.Account(ctx, "bob")
	if a.Locked || len(a.Rights) != 1 {
		t.Errorf("nach unlock: %+v", a)
	}

	// revoke: Löschmarke.
	if err := s.RevokeAccount(ctx, "bob", "team-x"); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeAccount(ctx, "bob", "team-x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("revoke doppelt: %v", err)
	}
	for _, r := range accountRowsAll(t, s, "bob") {
		if !r.deleted {
			t.Errorf("Zeile nach revoke: %+v", r)
		}
	}
	if a := lastAction(t, s); a.action != "account.revoke" || a.subject.String != "bob:team-x" || !a.revision.Valid {
		t.Errorf("actions revoke = %+v", a)
	}
}

// Ein Account ohne Collection: lock, unlock und token gehen, ohne Revision.
func TestAccountWithoutCollection(t *testing.T) {
	ctx := context.Background()
	s := newAccountStore(t)
	if _, err := s.AddAccount(ctx, "carol", "carol", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAccountLocked(ctx, "carol", true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.NewAccountToken(ctx, "carol"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAccountLocked(ctx, "carol", false); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAccount(ctx, "carol", AccountChange{Description: ptr("Carol")}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAccount(ctx, "niemand", AccountChange{Description: ptr("x")}); !errors.Is(err, ErrNotFound) {
		t.Errorf("set auf Fehlendes: %v", err)
	}
	a, err := s.Account(ctx, "carol")
	if err != nil || a.Description != "Carol" || a.Locked || len(a.Rights) != 0 {
		t.Errorf("Account = %+v, %v", a, err)
	}
	if rev := revision(t, s); rev != 0 {
		t.Errorf("Revision = %d", rev)
	}
	all, err := s.Accounts(ctx)
	if err != nil || len(all) != 1 || all[0].Name != "carol" {
		t.Errorf("Accounts = %+v, %v", all, err)
	}
}

func TestRotateAccount(t *testing.T) {
	ctx := context.Background()
	s := newAccountStore(t)
	token, err := s.AddAccount(ctx, "bob", "kleist", "")
	if err != nil {
		t.Fatal(err)
	}
	old := ident.HashToken(token)
	newHash := ident.HashToken("keph_neu")
	// Ohne gemeinsame Collection: Fehler vor jeder Änderung.
	if _, err := s.RotateAccount(ctx, "bob", old, newHash, "laptop", []string{"team-x"}); !errors.Is(err, ErrNoSharedCollection) {
		t.Fatalf("ohne Collection: %v", err)
	}
	for _, c := range []string{"team-x", "privat"} {
		if _, err := s.GrantAccount(ctx, "bob", c, contract.Rights{Write: c == "team-x"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.RotateAccount(ctx, "bob", old, newHash, "laptop", []string{"anderes"}); !errors.Is(err, ErrNoSharedCollection) {
		t.Errorf("fremde Collection: %v", err)
	}
	if _, err := s.RotateAccount(ctx, "bob", newHash, newHash, "laptop", []string{"team-x"}); !errors.Is(err, ErrAccountAuth) {
		t.Errorf("falsches altes: %v", err)
	}
	if _, err := s.RotateAccount(ctx, "niemand", old, newHash, "laptop", []string{"team-x"}); !errors.Is(err, ErrAccountAuth) {
		t.Errorf("unbekannt: %v", err)
	}
	if a, _ := s.Account(ctx, "bob"); a.TokenHash != old {
		t.Fatal("Fehlversuch hat den Hash geändert")
	}
	rev := revision(t, s)
	rows, err := s.RotateAccount(ctx, "bob", old, newHash, "laptop", []string{"team-x"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Collection != "team-x" || rows[0].Name != "SYSTEM:A:bob" || rows[0].Revision != rev+1 {
		t.Errorf("Zeilen = %+v", rows)
	}
	c, err := contract.DecodeAccountContent(*rows[0].Content)
	if err != nil || c.Hash != newHash || !c.Rights.Write || c.User != "kleist" {
		t.Errorf("Inhalt = %+v, %v", c, err)
	}
	// updated_by ist der User des Accounts, created_by bleibt admin.
	if rows[0].UpdatedBy != "kleist" || rows[0].CreatedBy != Admin {
		t.Errorf("Urheber der Antwort: created_by %q, updated_by %q", rows[0].CreatedBy, rows[0].UpdatedBy)
	}
	if a, _ := s.Account(ctx, "bob"); a.TokenHash != newHash || a.User != "kleist" {
		t.Error("accounts trägt den alten Hash oder einen anderen User")
	}
	for _, r := range accountRowsAll(t, s, "bob") {
		if r.content.Hash != newHash || r.content.User != "kleist" || r.revision != rev+1 ||
			r.updatedBy != "kleist" || r.createdBy != Admin {
			t.Errorf("Zeile nach rotate: %+v", r)
		}
	}
	a := lastAction(t, s)
	if a.action != "rotate" || a.account != "bob" || a.carrier.String != "laptop" || a.subject.String != "bob" || a.revision.Int64 != rev+1 {
		t.Errorf("actions rotate = %+v", a)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM actions WHERE action = 'rotate'`).Scan(&n); err != nil || n != 1 {
		t.Errorf("%d Zeilen rotate in actions", n)
	}
	// Gesperrt: kein rotate.
	if err := s.SetAccountLocked(ctx, "bob", true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RotateAccount(ctx, "bob", newHash, old, "laptop", []string{"team-x"}); !errors.Is(err, ErrAccountAuth) {
		t.Errorf("gesperrt: %v", err)
	}
}

// Export und Import tragen die Accounts samt Rechten; der Import gleicht die
// SYSTEM:A:-Zeilen an — vorhandene ändern, fehlende Löschmarken, neue
// anlegen — unter einer Revision.
func TestImportAccounts(t *testing.T) {
	ctx := context.Background()
	src := newAccountStore(t)
	for _, name := range []string{"alice", "bob", "carol"} {
		if _, err := src.AddAccount(ctx, name, name, ""); err != nil {
			t.Fatal(err)
		}
	}
	// Zwei Accounts eines Users; der User kommt mit dem Export.
	if err := src.SetAccount(ctx, "alice", AccountChange{User: ptr("kleist")}); err != nil {
		t.Fatal(err)
	}
	if err := src.SetAccount(ctx, "bob", AccountChange{User: ptr("kleist")}); err != nil {
		t.Fatal(err)
	}
	grant := func(s *sqliteStore, name, coll string, r contract.Rights) {
		t.Helper()
		if _, err := s.GrantAccount(ctx, name, coll, r); err != nil {
			t.Fatal(err)
		}
	}
	grant(src, "alice", "team-x", contract.Rights{})
	grant(src, "bob", "team-x", contract.Rights{Write: true})
	grant(src, "bob", "privat", contract.Rights{})
	grant(src, "carol", "team-x", contract.Rights{Supersede: true})
	if err := src.SetAccountLocked(ctx, "carol", true); err != nil {
		t.Fatal(err)
	}
	tables, err := src.Tables(ctx)
	if err != nil {
		t.Fatal(err)
	}

	dst := newAccountStore(t)
	// Im Ziel: dave (fällt weg) und bob mit anderen Rechten.
	if _, err := dst.AddAccount(ctx, "dave", "dave", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := dst.AddAccount(ctx, "bob", "bob", ""); err != nil {
		t.Fatal(err)
	}
	grant(dst, "dave", "team-x", contract.Rights{})
	grant(dst, "bob", "team-x", contract.Rights{})
	rev := revision(t, dst)
	if err := dst.Import(ctx, map[string]string{}, &tables); err != nil {
		t.Fatal(err)
	}
	if got := revision(t, dst); got != rev+1 {
		t.Errorf("Revision nach Import = %d, erwartet %d", got, rev+1)
	}
	got, err := dst.Accounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, tables.Accounts) {
		t.Errorf("Accounts:\n%+v\n%+v", got, tables.Accounts)
	}
	if rows := accountRowsAll(t, dst, "dave"); len(rows) != 1 || !rows[0].deleted || rows[0].revision != rev+1 {
		t.Errorf("dave: %+v", rows)
	}
	bob := accountRowsAll(t, dst, "bob")
	if len(bob) != 2 || bob[1].collection != "team-x" || !bob[1].content.Rights.Write || bob[1].content.User != "kleist" {
		t.Errorf("bob: %+v", bob)
	}
	if mine, err := dst.AccountsOfUser(ctx, "kleist"); err != nil || len(mine) != 2 || mine[0].Name != "alice" || mine[1].Name != "bob" {
		t.Errorf("Accounts von kleist nach Import: %+v, %v", mine, err)
	}
	if rows := accountRowsAll(t, dst, "carol"); len(rows) != 0 {
		t.Errorf("gesperrte carol hat Zeilen: %+v", rows)
	}
	if a := lastAction(t, dst); a.action != "config.import" || a.revision.Int64 != rev+1 {
		t.Errorf("actions = %+v", a)
	}
	// Derselbe Import noch einmal ändert keine Zeile.
	rev = revision(t, dst)
	if err := dst.Import(ctx, map[string]string{}, &tables); err != nil {
		t.Fatal(err)
	}
	if revision(t, dst) != rev {
		t.Error("gleicher Import zählt eine Revision")
	}

	// Prüfungen wie die CLI.
	bad := tables
	bad.Accounts = append([]Account{}, tables.Accounts...)
	bad.Accounts[0].Name = "admin"
	if err := dst.Import(ctx, nil, &bad); err == nil {
		t.Error("Account admin importiert")
	}
	bad.Accounts[0].Name = "alice"
	// Der User: fehlend (leer), ungültig oder admin bricht ab — dieselbe
	// Prüfung wie die CLI.
	for _, user := range []string{"", "admin", "Kleist", "system-x", "a:b"} {
		bad.Accounts[0].User = user
		if err := dst.Import(ctx, nil, &bad); err == nil || !strings.Contains(err.Error(), "User") {
			t.Errorf("User %q importiert: %v", user, err)
		}
	}
	if got, _ := dst.Accounts(ctx); !reflect.DeepEqual(got, tables.Accounts) {
		t.Errorf("abgebrochener Import hat Accounts geändert: %+v", got)
	}
	bad.Accounts[0].User = "kleist"
	bad.Accounts[0].Rights = []AccountRight{{Collection: "fehlt"}}
	if err := dst.Import(ctx, nil, &bad); !errors.Is(err, ErrNotFound) {
		t.Errorf("Recht auf fehlende Collection: %v", err)
	}
	bad = tables
	bad.Nodes = []Node{{Name: "alice", TokenHash: ident.HashToken("x"), CreatedBy: Admin}}
	if err := dst.Import(ctx, nil, &bad); err == nil || !strings.Contains(err.Error(), "gemeinsam eindeutig") {
		t.Errorf("Node und Account gleichen Namens: %v", err)
	}

	// KeepAccounts lässt Accounts und Zeilen, prüft Nodes gegen vorhandene
	// Accounts.
	keep := Tables{Collections: tables.Collections, KeepAccounts: true,
		Nodes: []Node{{Name: "bob", TokenHash: ident.HashToken("x"), CreatedBy: Admin}}}
	if err := dst.Import(ctx, nil, &keep); err == nil || !strings.Contains(err.Error(), "Account") {
		t.Errorf("Node mit Account-Namen bei KeepAccounts: %v", err)
	}
	keep.Nodes = nil
	rev = revision(t, dst)
	if err := dst.Import(ctx, nil, &keep); err != nil {
		t.Fatal(err)
	}
	after, _ := dst.Accounts(ctx)
	if !reflect.DeepEqual(after, tables.Accounts) || revision(t, dst) != rev {
		t.Errorf("KeepAccounts hat Accounts geändert: %+v", after)
	}
}

// Löschmarken von Account-Zeilen blockieren das Entfernen einer Collection
// nicht und bleiben stehen; lebende Zeilen und gemerkte Rechte eines
// gesperrten Accounts blockieren. Eine gleichnamige neue Collection belebt
// die Marke mit grant wieder.
func TestRemoveCollectionWithAccountTombstones(t *testing.T) {
	ctx := context.Background()
	s := newAccountStore(t)
	for _, name := range []string{"alice", "bob", "carol"} {
		if _, err := s.AddAccount(ctx, name, name, ""); err != nil {
			t.Fatal(err)
		}
		if _, err := s.GrantAccount(ctx, name, "privat", contract.Rights{}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetAccountLocked(ctx, "carol", true); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveCollection(ctx, "privat"); !errors.Is(err, ErrInUse) || !strings.Contains(err.Error(), "carol") {
		t.Errorf("gemerkte Rechte: %v", err)
	}
	if err := s.RevokeAccount(ctx, "carol", "privat"); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveCollection(ctx, "privat"); !errors.Is(err, ErrInUse) || !strings.Contains(err.Error(), "2 Dokumentzeilen") {
		t.Errorf("lebende Zeilen: %v", err)
	}
	if err := s.RevokeAccount(ctx, "alice", "privat"); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveCollection(ctx, "privat"); !errors.Is(err, ErrInUse) {
		t.Errorf("lebende Zeile von bob: %v", err)
	}
	if err := s.RemoveAccount(ctx, "bob"); err != nil {
		t.Fatal(err)
	}
	marks := map[string][]sysRow{}
	for _, name := range []string{"alice", "bob", "carol"} {
		marks[name] = accountRowsAll(t, s, name)
	}
	if err := s.RemoveCollection(ctx, "privat"); err != nil {
		t.Fatalf("nur Löschmarken: %v", err)
	}
	for name, before := range marks {
		if after := accountRowsAll(t, s, name); !reflect.DeepEqual(after, before) || len(after) != 1 || !after[0].deleted {
			t.Errorf("%s: Marken nach rm: %+v", name, after)
		}
	}

	// Gleichnamig neu: grant belebt die Marke von alice wieder.
	if err := s.AddCollection(ctx, "privat", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GrantAccount(ctx, "alice", "privat", contract.Rights{}); err != nil {
		t.Fatal(err)
	}
	after := accountRowsAll(t, s, "alice")
	if len(after) != 1 || after[0].deleted || after[0].id != marks["alice"][0].id || after[0].revision <= marks["alice"][0].revision {
		t.Errorf("wiederbelebt: vorher %+v, nachher %+v", marks["alice"], after)
	}
}

// Der Import entfernt eine Collection, in der nur Löschmarken von
// Account-Zeilen stehen; die Marken bleiben.
func TestImportRemovesCollectionWithTombstones(t *testing.T) {
	ctx := context.Background()
	s := newAccountStore(t)
	if _, err := s.AddAccount(ctx, "alice", "alice", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GrantAccount(ctx, "alice", "privat", contract.Rights{}); err != nil {
		t.Fatal(err)
	}
	tables, err := s.Tables(ctx)
	if err != nil {
		t.Fatal(err)
	}
	tables.Collections = slices.DeleteFunc(tables.Collections, func(c Collection) bool { return c.Name == "privat" })
	tables.Accounts[0].Rights = nil
	// Mit lebender Zeile blockiert die Collection den Import.
	if err := s.Import(ctx, nil, &tables); !errors.Is(err, ErrInUse) {
		t.Errorf("Import mit lebender Zeile: %v", err)
	}
	if err := s.RevokeAccount(ctx, "alice", "privat"); err != nil {
		t.Fatal(err)
	}
	before := accountRowsAll(t, s, "alice")
	if err := s.Import(ctx, nil, &tables); err != nil {
		t.Fatalf("Import mit Löschmarke: %v", err)
	}
	if colls, _ := s.Collections(ctx); slices.ContainsFunc(colls, func(c Collection) bool { return c.Name == "privat" }) {
		t.Error("privat nicht entfernt")
	}
	if after := accountRowsAll(t, s, "alice"); !reflect.DeepEqual(after, before) || len(after) != 1 {
		t.Errorf("Marken nach Import: %+v", after)
	}
	// Ebenso, wenn der Import die Accounts lässt.
	if err := s.AddCollection(ctx, "privat", ""); err != nil {
		t.Fatal(err)
	}
	keep := tables
	keep.Accounts, keep.KeepAccounts = nil, true
	if err := s.Import(ctx, nil, &keep); err != nil {
		t.Fatalf("Import mit KeepAccounts: %v", err)
	}
	if after := accountRowsAll(t, s, "alice"); !reflect.DeepEqual(after, before) {
		t.Errorf("Marken nach Import mit KeepAccounts: %+v", after)
	}
}

// Der User eines Accounts: angegeben oder der Name, Namensregel wie
// Accounts, admin reserviert; er darf wie ein Node oder ein anderer Account
// heißen. set --user schreibt alle lebenden Zeilen unter einer Revision neu,
// Löschmarken und Dokumente bleiben.
func TestAccountUser(t *testing.T) {
	ctx := context.Background()
	s := newAccountStore(t)
	if _, err := s.AddNode(ctx, "laptop", ""); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"admin", "", "Kleist", "system-x", "a:b"} {
		if _, err := s.AddAccount(ctx, "bob", bad, ""); err == nil || !strings.Contains(err.Error(), "User") {
			t.Errorf("User %q angenommen: %v", bad, err)
		}
	}
	// User gleich einem Node-Namen und gleich einem anderen Account.
	if _, err := s.AddAccount(ctx, "desk", "laptop", ""); err != nil {
		t.Fatalf("User wie ein Node: %v", err)
	}
	if _, err := s.AddAccount(ctx, "bob", "bob", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddAccount(ctx, "vm", "bob", ""); err != nil {
		t.Fatalf("User wie ein Account: %v", err)
	}
	if a, err := s.Account(ctx, "desk"); err != nil || a.User != "laptop" {
		t.Errorf("desk = %+v, %v", a, err)
	}
	if mine, err := s.AccountsOfUser(ctx, "bob"); err != nil || len(mine) != 2 || mine[0].Name != "bob" || mine[1].Name != "vm" {
		t.Errorf("Accounts von bob = %+v, %v", mine, err)
	}
	if none, err := s.AccountsOfUser(ctx, "niemand"); err != nil || len(none) != 0 {
		t.Errorf("Accounts von niemand = %+v, %v", none, err)
	}

	// bob: zwei lebende Zeilen, eine Löschmarke (revoke) und eine in einer
	// entfernten Collection; dazu ein Dokument.
	if err := s.AddCollection(ctx, "alt", ""); err != nil {
		t.Fatal(err)
	}
	for _, c := range []string{"team-x", "privat", "alt"} {
		if _, err := s.GrantAccount(ctx, "bob", c, contract.Rights{Write: c == "team-x"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.RevokeAccount(ctx, "bob", "alt"); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveCollection(ctx, "alt"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddCollection(ctx, "weg", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GrantAccount(ctx, "bob", "weg", contract.Rights{}); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeAccount(ctx, "bob", "weg"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutDocument(ctx, "team-x", "notiz.md", "hallo"); err != nil {
		t.Fatal(err)
	}
	doc, err := s.Document(ctx, "team-x", "notiz.md")
	if err != nil {
		t.Fatal(err)
	}
	before := accountRowsAll(t, s, "bob")

	if err := s.SetAccount(ctx, "bob", AccountChange{User: ptr("admin")}); err == nil || !strings.Contains(err.Error(), "reserviert") {
		t.Errorf("set --user admin: %v", err)
	}
	if err := s.SetAccount(ctx, "bob", AccountChange{User: ptr("")}); err == nil {
		t.Error("set --user leer angenommen")
	}
	if err := s.SetAccount(ctx, "niemand", AccountChange{User: ptr("kleist")}); !errors.Is(err, ErrNotFound) {
		t.Errorf("set --user auf Fehlendes: %v", err)
	}
	if err := s.SetAccount(ctx, "bob", AccountChange{}); err == nil {
		t.Error("set ohne Änderung angenommen")
	}
	if got := accountRowsAll(t, s, "bob"); !reflect.DeepEqual(got, before) {
		t.Errorf("abgewiesenes set hat Zeilen geändert: %+v", got)
	}

	rev := revision(t, s)
	if err := s.SetAccount(ctx, "bob", AccountChange{User: ptr("kleist"), Description: ptr("Bob")}); err != nil {
		t.Fatal(err)
	}
	if got := revision(t, s); got != rev+1 {
		t.Errorf("Revision nach set --user = %d, erwartet %d", got, rev+1)
	}
	if a := lastAction(t, s); a.action != "account.set" || a.account != Admin || a.subject.String != "bob" || a.revision.Int64 != rev+1 {
		t.Errorf("actions set = %+v", a)
	}
	after := accountRowsAll(t, s, "bob")
	if len(after) != len(before) {
		t.Fatalf("Zeilen: vorher %+v, nachher %+v", before, after)
	}
	for i, r := range after {
		b := before[i]
		if r.deleted {
			if !reflect.DeepEqual(r, b) {
				t.Errorf("Löschmarke verändert: vorher %+v, nachher %+v", b, r)
			}
			continue
		}
		want := b.content
		want.User = "kleist"
		if r.id != b.id || !reflect.DeepEqual(r.content, want) || r.revision != rev+1 || r.updatedBy != Admin {
			t.Errorf("Zeile nach set --user: %+v", r)
		}
	}
	if a, _ := s.Account(ctx, "bob"); a.User != "kleist" || a.Description != "Bob" {
		t.Errorf("accounts nach set: %+v", a)
	}
	if d, err := s.Document(ctx, "team-x", "notiz.md"); err != nil || d != doc {
		t.Errorf("Dokument verändert: %+v, %v", d, err)
	}
	// Derselbe User noch einmal: keine Revision.
	rev = revision(t, s)
	if err := s.SetAccount(ctx, "bob", AccountChange{User: ptr("kleist")}); err != nil {
		t.Fatal(err)
	}
	if revision(t, s) != rev {
		t.Error("unveränderter User zählt eine Revision")
	}

	// Gesperrt: set --user ändert nur accounts, die Löschmarken bleiben;
	// unlock legt die Zeilen mit dem neuen User an.
	if err := s.SetAccountLocked(ctx, "bob", true); err != nil {
		t.Fatal(err)
	}
	locked := accountRowsAll(t, s, "bob")
	rev = revision(t, s)
	if err := s.SetAccount(ctx, "bob", AccountChange{User: ptr("robert")}); err != nil {
		t.Fatal(err)
	}
	if got := accountRowsAll(t, s, "bob"); !reflect.DeepEqual(got, locked) || revision(t, s) != rev {
		t.Errorf("set --user gesperrt hat Zeilen geändert: %+v", got)
	}
	if a, _ := s.Account(ctx, "bob"); a.User != "robert" {
		t.Errorf("User gesperrt = %q", a.User)
	}
	if err := s.SetAccountLocked(ctx, "bob", false); err != nil {
		t.Fatal(err)
	}
	live := 0
	for _, r := range accountRowsAll(t, s, "bob") {
		if !r.deleted {
			live++
			if r.content.User != "robert" || r.revision != rev+1 {
				t.Errorf("Zeile nach unlock: %+v", r)
			}
		}
	}
	if live != 2 {
		t.Errorf("%d lebende Zeilen nach unlock", live)
	}

	// Ohne Collection: der User steht nur in accounts; der erste grant
	// schreibt ihn in die Zeile.
	rev = revision(t, s)
	if err := s.SetAccount(ctx, "vm", AccountChange{User: ptr("kleist")}); err != nil {
		t.Fatal(err)
	}
	if revision(t, s) != rev {
		t.Error("set --user ohne Collection zählt eine Revision")
	}
	if _, err := s.GrantAccount(ctx, "vm", "team-x", contract.Rights{}); err != nil {
		t.Fatal(err)
	}
	if rows := accountRowsAll(t, s, "vm"); len(rows) != 1 || rows[0].content.User != "kleist" {
		t.Errorf("vm nach grant: %+v", rows)
	}
	if mine, _ := s.AccountsOfUser(ctx, "kleist"); len(mine) != 1 || mine[0].Name != "vm" {
		t.Errorf("Accounts von kleist = %+v", mine)
	}
}
