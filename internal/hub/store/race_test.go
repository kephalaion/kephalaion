package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/kephalaion/kephalaion/internal/contract"
	"github.com/kephalaion/kephalaion/internal/ident"
	"github.com/kephalaion/kephalaion/internal/sqlitedb"
)

// Die Tests hier belegen die Regeln, die unter PostgreSQL (READ COMMITTED)
// eine Race verhindern: Sperre der Account-Zeile zuerst, bedingtes rotate,
// principal_names. Unter SQLite reiht BEGIN IMMEDIATE die Transaktionen
// ohnehin; die Race selbst zeigen sie nicht, nur dass die Regeln gelten.

// recorder hält die Anweisungen einer Transaktion in ihrer Reihenfolge fest.
type recorder struct {
	sqlitedb.Querier
	stmts *[]string
}

func (r recorder) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	*r.stmts = append(*r.stmts, query)
	return r.Querier.ExecContext(ctx, query, args...)
}

func (r recorder) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	*r.stmts = append(*r.stmts, query)
	return r.Querier.QueryContext(ctx, query, args...)
}

func (r recorder) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	*r.stmts = append(*r.stmts, query)
	return r.Querier.QueryRowContext(ctx, query, args...)
}

// trace hält die Anweisungen jedes Schreibvorgangs an Accounts fest; je
// Vorgang eine Liste.
func trace(s *sqliteStore) *[][]string {
	var all [][]string
	s.traceTx = func(tx sqlitedb.Querier) sqlitedb.Querier {
		all = append(all, nil)
		return recorder{Querier: tx, stmts: &all[len(all)-1]}
	}
	return &all
}

// Jeder Schreibvorgang an einem Account sperrt als erste Anweisung dessen
// Zeile in accounts; rotate beginnt mit dem bedingten Schreiben.
func TestAccountLockFirst(t *testing.T) {
	ctx := context.Background()
	s := newAccountStore(t)
	all := trace(s)
	token, err := s.AddAccount(ctx, "bob", "bob", "")
	if err != nil {
		t.Fatal(err)
	}
	steps := []struct {
		name string
		fn   func() error
	}{
		{"set", func() error { return s.SetAccount(ctx, "bob", AccountChange{Description: ptr("Bob")}) }},
		{"grant", func() error { _, err := s.GrantAccount(ctx, "bob", "team-x", contract.Rights{}); return err }},
		{"user", func() error { return s.SetAccount(ctx, "bob", AccountChange{User: ptr("kleist")}) }},
		{"rotate", func() error {
			_, err := s.RotateAccount(ctx, "bob", ident.HashToken(token), ident.HashToken("keph_neu"), "laptop", []string{"team-x"})
			return err
		}},
		{"lock", func() error { return s.SetAccountLocked(ctx, "bob", true) }},
		{"unlock", func() error { return s.SetAccountLocked(ctx, "bob", false) }},
		{"revoke", func() error { return s.RevokeAccount(ctx, "bob", "team-x") }},
		{"token", func() error { _, err := s.NewAccountToken(ctx, "bob"); return err }},
		{"rm", func() error { return s.RemoveAccount(ctx, "bob") }},
	}
	for _, st := range steps {
		if err := st.fn(); err != nil {
			t.Fatalf("%s: %v", st.name, err)
		}
	}
	if len(*all) != len(steps)+1 {
		t.Fatalf("%d Schreibvorgänge aufgezeichnet, erwartet %d", len(*all), len(steps)+1)
	}
	names := append([]string{"add"}, func() []string {
		var out []string
		for _, st := range steps {
			out = append(out, st.name)
		}
		return out
	}()...)
	for i, stmts := range *all {
		want := q(queries.AccountLock)
		if names[i] == "rotate" {
			want = q(queries.AccountRotate)
		}
		if len(stmts) == 0 || stmts[0] != want {
			t.Errorf("%s: erste Anweisung %q, erwartet %q", names[i], first(stmts), want)
		}
	}
}

func first(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return s[0]
}

// Ein zweiter rotate mit demselben alten Token scheitert auch ohne die
// Vorprüfung der Replication — direkt am Store, auch gleichzeitig.
func TestRotateTwiceSameOldHash(t *testing.T) {
	ctx := context.Background()
	s := newAccountStore(t)
	token, err := s.AddAccount(ctx, "bob", "bob", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.GrantAccount(ctx, "bob", "team-x", contract.Rights{}); err != nil {
		t.Fatal(err)
	}
	old := ident.HashToken(token)
	const n = 8
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = s.RotateAccount(ctx, "bob", old, ident.HashToken(fmt.Sprintf("keph_neu%d", i)), "laptop", []string{"team-x"})
		}()
	}
	wg.Wait()
	won := -1
	for i, err := range errs {
		switch {
		case err == nil && won < 0:
			won = i
		case err == nil:
			t.Errorf("rotate %d und %d gelangen beide", won, i)
		case !errors.Is(err, ErrAccountAuth):
			t.Errorf("rotate %d: %v", i, err)
		}
	}
	if won < 0 {
		t.Fatal("kein rotate gelang")
	}
	want := ident.HashToken(fmt.Sprintf("keph_neu%d", won))
	checkHashConsistent(t, s, "bob", want)
	var rotates int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM actions WHERE action = 'rotate'`).Scan(&rotates); err != nil || rotates != 1 {
		t.Errorf("%d Zeilen rotate in actions, %v", rotates, err)
	}
}

// Ein gesperrter Account scheitert am bedingten Schreiben: Es ist die
// einzige Anweisung, danach ist nichts geändert.
func TestRotateLockedFailsAtConditionalWrite(t *testing.T) {
	ctx := context.Background()
	s := newAccountStore(t)
	token, err := s.AddAccount(ctx, "bob", "bob", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.GrantAccount(ctx, "bob", "team-x", contract.Rights{}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAccountLocked(ctx, "bob", true); err != nil {
		t.Fatal(err)
	}
	rev := revision(t, s)
	before := accountRowsAll(t, s, "bob")
	all := trace(s)
	_, err = s.RotateAccount(ctx, "bob", ident.HashToken(token), ident.HashToken("keph_neu"), "laptop", []string{"team-x"})
	if !errors.Is(err, ErrAccountAuth) {
		t.Fatalf("gesperrt: %v", err)
	}
	if len(*all) != 1 || !slices.Equal((*all)[0], []string{q(queries.AccountRotate)}) {
		t.Errorf("Anweisungen: %q", *all)
	}
	if a, _ := s.Account(ctx, "bob"); a.TokenHash != ident.HashToken(token) || revision(t, s) != rev {
		t.Errorf("geändert: %+v", a)
	}
	if after := accountRowsAll(t, s, "bob"); !reflect.DeepEqual(after, before) {
		t.Errorf("Zeilen geändert: %+v", after)
	}
}

// checkHashConsistent prüft, dass accounts den Hash want führt und jede
// lebende Zeile des Accounts ihn trägt.
func checkHashConsistent(t *testing.T, s *sqliteStore, account, want string) {
	t.Helper()
	a, err := getAccount(context.Background(), s.db, account)
	if err != nil {
		t.Fatal(err)
	}
	if want != "" && a.TokenHash != want {
		t.Errorf("accounts: Hash %s, erwartet %s", a.TokenHash, want)
	}
	for _, r := range accountRowsAll(t, s, account) {
		if !r.deleted && r.content.Hash != a.TokenHash {
			t.Errorf("Zeile in %s trägt %s, accounts %s", r.collection, r.content.Hash, a.TokenHash)
		}
	}
}

// rotate verschränkt mit grant, revoke, lock und unlock: Danach tragen alle
// lebenden Zeilen den Hash aus accounts. Unter SQLite scheitert das auch ohne
// die Sperre nicht (BEGIN IMMEDIATE reiht die Transaktionen); die Sperre als
// erste Anweisung belegt TestAccountLockFirst.
func TestRotateInterleaved(t *testing.T) {
	ctx := context.Background()
	s := newAccountStore(t)
	token, err := s.AddAccount(ctx, "bob", "bob", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []string{"team-x", "privat"} {
		if _, err := s.GrantAccount(ctx, "bob", c, contract.Rights{}); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	wg.Add(2)
	current := ident.HashToken(token)
	go func() {
		defer wg.Done()
		for i := range 20 {
			next := ident.HashToken(fmt.Sprintf("keph_neu%d", i))
			_, err := s.RotateAccount(ctx, "bob", current, next, "laptop", []string{"team-x", "privat"})
			switch {
			case err == nil:
				current = next
			case errors.Is(err, ErrAccountAuth), errors.Is(err, ErrNoSharedCollection):
			default:
				t.Errorf("rotate: %v", err)
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := range 20 {
			var err error
			switch i % 4 {
			case 0:
				_, err = s.GrantAccount(ctx, "bob", "team-x", contract.Rights{Write: i%8 == 0})
			case 1:
				err = s.SetAccountLocked(ctx, "bob", true)
			case 2:
				err = s.SetAccountLocked(ctx, "bob", false)
			case 3:
				if err = s.RevokeAccount(ctx, "bob", "privat"); errors.Is(err, ErrNotFound) {
					_, err = s.GrantAccount(ctx, "bob", "privat", contract.Rights{})
				}
			}
			if err != nil {
				t.Errorf("Schritt %d: %v", i, err)
			}
		}
	}()
	wg.Wait()
	checkHashConsistent(t, s, "bob", current)
}

// principal names liest die Tabelle principal_names.
func principalNames(t *testing.T, s *sqliteStore) []string {
	t.Helper()
	rows, err := s.db.Query(`SELECT name || ':' || kind FROM principal_names ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			t.Fatal(err)
		}
		out = append(out, v)
	}
	return out
}

// Ein Name, den die andere Tabelle belegt, scheitert an der Datenbank, auch
// wenn die Vorprüfung ihn nicht sieht — mit derselben Meldung wie mit ihr.
func TestPrincipalNamesUnique(t *testing.T) {
	ctx := context.Background()
	s := newAccountStore(t)
	if _, err := s.AddNode(ctx, "laptop", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddAccount(ctx, "alice", "alice", ""); err != nil {
		t.Fatal(err)
	}
	_, withNode := s.AddAccount(ctx, "laptop", "laptop", "")
	_, withAccount := s.AddNode(ctx, "alice", "")
	for _, err := range []error{withNode, withAccount} {
		if !errors.Is(err, ErrExists) {
			t.Fatalf("Vorprüfung: %v", err)
		}
	}
	// Belegt nur in principal_names, wie von einer gleichzeitigen
	// Transaktion: Die Vorprüfung über nodes und accounts sieht nichts.
	if _, err := s.db.Exec(`INSERT INTO principal_names (name, kind) VALUES ('desktop', 'node'), ('bob', 'account')`); err != nil {
		t.Fatal(err)
	}
	_, err := s.AddAccount(ctx, "desktop", "desktop", "")
	if !errors.Is(err, ErrExists) || err.Error() != strings.ReplaceAll(withNode.Error(), "laptop", "desktop") {
		t.Errorf("Account desktop: %v, erwartet wie %q", err, withNode)
	}
	_, err = s.AddNode(ctx, "bob", "")
	if !errors.Is(err, ErrExists) || err.Error() != strings.ReplaceAll(withAccount.Error(), "alice", "bob") {
		t.Errorf("Node bob: %v, erwartet wie %q", err, withAccount)
	}
	if _, err := s.Account(ctx, "desktop"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Account desktop trotzdem angelegt: %v", err)
	}
	if _, err := s.Node(ctx, "bob"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Node bob trotzdem angelegt: %v", err)
	}

	// Nach rm ist der Name wieder frei.
	if _, err := s.db.Exec(`DELETE FROM principal_names WHERE name IN ('desktop', 'bob')`); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveNode(ctx, "laptop"); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveAccount(ctx, "alice"); err != nil {
		t.Fatal(err)
	}
	if got := principalNames(t, s); len(got) != 0 {
		t.Errorf("nach rm belegt: %v", got)
	}
	if _, err := s.AddAccount(ctx, "laptop", "laptop", ""); err != nil {
		t.Errorf("Account laptop nach rm: %v", err)
	}
	if _, err := s.AddNode(ctx, "alice", ""); err != nil {
		t.Errorf("Node alice nach rm: %v", err)
	}
	if got := principalNames(t, s); !slices.Equal(got, []string{"alice:node", "laptop:account"}) {
		t.Errorf("principal_names = %v", got)
	}
}

// Der Import baut principal_names aus nodes und accounts neu auf; ein Name
// als Node und als Account bricht ab, ohne etwas zu ändern.
func TestImportPrincipalNames(t *testing.T) {
	ctx := context.Background()
	s := newAccountStore(t)
	if _, err := s.AddNode(ctx, "laptop", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddAccount(ctx, "alice", "alice", ""); err != nil {
		t.Fatal(err)
	}
	tables, err := s.Tables(ctx)
	if err != nil {
		t.Fatal(err)
	}
	before := principalNames(t, s)

	conflict := tables
	conflict.Nodes = append(append([]Node{}, tables.Nodes...), Node{Name: "alice", TokenHash: ident.HashToken("x"), CreatedBy: Admin})
	if err := s.Import(ctx, nil, &conflict); err == nil || !strings.Contains(err.Error(), "gemeinsam eindeutig") {
		t.Errorf("Import mit Namenskonflikt: %v", err)
	}
	keep := conflict
	keep.Accounts, keep.KeepAccounts = nil, true
	if err := s.Import(ctx, nil, &keep); !errors.Is(err, ErrExists) {
		t.Errorf("Import mit Namenskonflikt, Accounts bleiben: %v", err)
	}
	after, _ := s.Tables(ctx)
	if len(after.Nodes) != 1 || len(after.Accounts) != 1 || !slices.Equal(principalNames(t, s), before) {
		t.Errorf("Import hat geändert: %+v, %v", after, principalNames(t, s))
	}

	// Ein Import tauscht Node und Account: principal_names folgt.
	swap := tables
	swap.Nodes = []Node{{Name: "bob", TokenHash: ident.HashToken("x"), CreatedBy: Admin}}
	swap.Accounts = []Account{{Name: "laptop", User: "laptop", TokenHash: ident.HashToken("y"), CreatedBy: Admin}}
	if err := s.Import(ctx, nil, &swap); err != nil {
		t.Fatal(err)
	}
	if got := principalNames(t, s); !slices.Equal(got, []string{"bob:node", "laptop:account"}) {
		t.Errorf("principal_names nach Import = %v", got)
	}
}
