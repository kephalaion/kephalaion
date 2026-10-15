package store

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/kephalaion/kephalaion/internal/contract"
)

// Der Idempotenzschlüssel der Schreibvorgänge (Task 032).

// withKey ist die Anmeldung eines Accounts mit einem Schlüssel.
func (f *writeFixture) withKey(account, key string) WriteAuth {
	a := f.as(account)
	a.RequestID = key
	return a
}

func countRequests(t *testing.T, s *sqliteStore) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM requests`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func countActions(t *testing.T, s *sqliteStore) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM actions`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func wantReplay(t *testing.T, what string, res WriteResult, err error, rev int64, ids []string) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
	if !res.Replayed || res.Revision != rev || !reflect.DeepEqual(res.IDs, ids) || res.Rows == nil || len(res.Rows) != 0 {
		t.Errorf("%s: %+v, erwartet replayed mit Revision %d und ids %v, ohne Zeilen", what, res, rev, ids)
	}
}

// Ein Vorgang mit Schlüssel wirkt einmal: Die Wiederholung schreibt nichts —
// keine Revision, keine Zeile in actions — und liefert Revision und ids des
// ersten. Ohne Schlüssel bleibt alles wie bisher.
func TestRequestIDReplay(t *testing.T) {
	ctx := context.Background()
	f := newWriteStore(t)
	s := f.s
	first, err := s.CreateDocumentAs(ctx, f.withKey("bob", "k-1"), "a", "x.md", "eins")
	if err != nil || first.Replayed || first.IDs != nil || len(first.Rows) != 1 {
		t.Fatalf("create: %+v, %v", first, err)
	}
	rev0, actions := revision(t, s), countActions(t, s)
	for range 2 {
		res, err := s.CreateDocumentAs(ctx, f.withKey("bob", "k-1"), "a", "x.md", "eins")
		wantReplay(t, "Wiederholung von create", res, err, first.Revision, []string{first.Rows[0].ID})
	}
	if revision(t, s) != rev0 || countActions(t, s) != actions {
		t.Errorf("Wiederholung hat geschrieben: Revision %d → %d, actions %d → %d",
			rev0, revision(t, s), actions, countActions(t, s))
	}

	// Ohne Schlüssel ist ein zweites create wie bisher name_taken, und
	// nichts wird gemerkt.
	before := countRequests(t, s)
	if _, err := s.CreateDocumentAs(ctx, f.as("bob"), "a", "x.md", "eins"); !errors.Is(err, ErrNameTaken) {
		t.Errorf("create ohne Schlüssel: %v, erwartet ErrNameTaken", err)
	}
	if _, err := s.CreateDocumentAs(ctx, f.as("bob"), "a", "y.md", "y"); err != nil {
		t.Fatal(err)
	}
	if n := countRequests(t, s); n != before {
		t.Errorf("ohne Schlüssel gemerkt: %d → %d", before, n)
	}

	// write mit unverändertem Inhalt ist ein Erfolg ohne neue Revision; er
	// wird gemerkt, die Wiederholung liefert dieselbe Revision.
	same, err := s.WriteDocumentAs(ctx, f.withKey("bob", "k-2"), "a", "x.md", "eins", nil)
	if err != nil || same.Revision != first.Revision {
		t.Fatalf("write unverändert: %+v, %v", same, err)
	}
	res, err := s.WriteDocumentAs(ctx, f.withKey("bob", "k-2"), "a", "x.md", "eins", nil)
	wantReplay(t, "Wiederholung von write", res, err, first.Revision, []string{first.Rows[0].ID})

	// write mit Vorbedingung: Die Wiederholung scheitert nicht an der
	// eigenen, inzwischen veralteten Revision.
	w, err := s.WriteDocumentAs(ctx, f.withKey("bob", "k-3"), "a", "x.md", "zwei", rev(first.Revision))
	if err != nil {
		t.Fatal(err)
	}
	res, err = s.WriteDocumentAs(ctx, f.withKey("bob", "k-3"), "a", "x.md", "zwei", rev(first.Revision))
	wantReplay(t, "Wiederholung von write mit base_revision", res, err, w.Revision, []string{first.Rows[0].ID})

	// delete: nach dem Löschen gibt es das Dokument nicht mehr, die
	// Wiederholung ist trotzdem replayed, nicht not_found.
	d, err := s.DeleteDocumentAs(ctx, f.withKey("bob", "k-4"), "a", "x.md", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	res, err = s.DeleteDocumentAs(ctx, f.withKey("bob", "k-4"), "a", "x.md", nil, false)
	wantReplay(t, "Wiederholung von delete", res, err, d.Revision, []string{first.Rows[0].ID})
}

// Derselbe Schlüssel mit anderem Fingerabdruck — anderer Inhalt, anderer
// Name, anderer Vorgang, andere Vorbedingung — ist ungültig, nicht still eine
// Wiederholung; die Meldung sagt es.
func TestRequestIDOtherOperation(t *testing.T) {
	ctx := context.Background()
	f := newWriteStore(t)
	s := f.s
	if _, err := s.GrantAccount(ctx, "bob", "c", contract.Rights{Write: true}); err != nil {
		t.Fatal(err)
	}
	created, err := s.CreateDocumentAs(ctx, f.withKey("bob", "k"), "a", "x.md", "eins")
	if err != nil {
		t.Fatal(err)
	}
	rev0 := revision(t, s)
	for name, fn := range map[string]func() error{
		"anderer Inhalt": func() error {
			_, err := s.CreateDocumentAs(ctx, f.withKey("bob", "k"), "a", "x.md", "zwei")
			return err
		},
		"anderer Name": func() error {
			_, err := s.CreateDocumentAs(ctx, f.withKey("bob", "k"), "a", "z.md", "eins")
			return err
		},
		"andere Collection": func() error {
			_, err := s.CreateDocumentAs(ctx, f.withKey("bob", "k"), "c", "x.md", "eins")
			return err
		},
		"anderer Vorgang": func() error {
			_, err := s.WriteDocumentAs(ctx, f.withKey("bob", "k"), "a", "x.md", "eins", nil)
			return err
		},
		"delete": func() error {
			_, err := s.DeleteDocumentAs(ctx, f.withKey("bob", "k"), "a", "x.md", nil, false)
			return err
		},
		"rename": func() error {
			_, err := s.RenameDocumentAs(ctx, f.withKey("bob", "k"), "a", "x.md", "y.md", nil)
			return err
		},
	} {
		err := fn()
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "anderen Vorgang") {
			t.Errorf("%s: %v, erwartet ErrInvalid mit Hinweis auf den anderen Vorgang", name, err)
		}
	}
	// Auch die Vorbedingung und recursive gehören zum Fingerabdruck.
	if _, err := s.WriteDocumentAs(ctx, f.withKey("bob", "w"), "a", "x.md", "drei", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteDocumentAs(ctx, f.withKey("bob", "w"), "a", "x.md", "drei", rev(created.Revision)); !errors.Is(err, ErrInvalid) {
		t.Errorf("andere base_revision: %v, erwartet ErrInvalid", err)
	}
	if _, err := s.DeleteDocumentAs(ctx, f.withKey("bob", "d"), "a", "x.md", nil, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteDocumentAs(ctx, f.withKey("bob", "d"), "a", "x.md", nil, true); !errors.Is(err, ErrInvalid) {
		t.Errorf("anderes recursive: %v, erwartet ErrInvalid", err)
	}
	if got := revision(t, s); got != rev0+2 {
		t.Errorf("Revision %d, erwartet %d: Abgelehnte haben geschrieben", got, rev0+2)
	}

	// Die Form: ungültig noch vor der Datenbank.
	for _, bad := range []string{"a b", strings.Repeat("x", contract.MaxRequestIDLen+1), "ä"} {
		if _, err := s.CreateDocumentAs(ctx, f.withKey("bob", bad), "a", "neu.md", ""); !errors.Is(err, ErrInvalid) {
			t.Errorf("Schlüssel %q: %v, erwartet ErrInvalid", bad, err)
		}
	}
}

// Gemerkt wird nur ein Erfolg: Eine Ablehnung hinterlässt nichts, ihre
// Wiederholung wird neu geprüft und kann gelingen.
func TestRequestIDRejectionNotRemembered(t *testing.T) {
	ctx := context.Background()
	f := newWriteStore(t)
	s := f.s
	if _, err := s.CreateDocumentAs(ctx, f.as("alice"), "a", "x.md", "von alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateDocumentAs(ctx, f.withKey("bob", "k"), "a", "x.md", "von bob"); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("create: %v, erwartet ErrNameTaken", err)
	}
	if n := countRequests(t, s); n != 0 {
		t.Errorf("Ablehnung gemerkt: %d Einträge", n)
	}
	if _, err := s.DeleteDocumentAs(ctx, f.as("alice"), "a", "x.md", nil, false); err != nil {
		t.Fatal(err)
	}
	res, err := s.CreateDocumentAs(ctx, f.withKey("bob", "k"), "a", "x.md", "von bob")
	if err != nil || res.Replayed || len(res.Rows) != 1 || res.Rows[0].CreatedBy != "kleist" {
		t.Errorf("zweiter Versuch: %+v, %v, erwartet ein neues Dokument", res, err)
	}
}

// Derselbe Schlüssel bei zwei Accounts sind zwei Schlüssel — auch bei zwei
// Accounts desselben Users.
func TestRequestIDPerAccount(t *testing.T) {
	ctx := context.Background()
	f := newWriteStore(t)
	s := f.s
	a, err := s.CreateDocumentAs(ctx, f.withKey("bob", "k"), "a", "a.md", "a")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateDocumentAs(ctx, f.withKey("bob2", "k"), "a", "b.md", "b")
	if err != nil || b.Replayed || b.Revision == a.Revision {
		t.Fatalf("bob2 mit demselben Schlüssel: %+v, %v", b, err)
	}
	// Derselbe Vorgang unter demselben Schlüssel bei einem anderen Account
	// ist keine Wiederholung: name_taken wie ohne Schlüssel.
	if _, err := s.CreateDocumentAs(ctx, f.withKey("bob2", "k2"), "a", "a.md", "a"); !errors.Is(err, ErrNameTaken) {
		t.Errorf("anderer Account: %v, erwartet ErrNameTaken", err)
	}
	res, err := s.CreateDocumentAs(ctx, f.withKey("bob", "k"), "a", "a.md", "a")
	wantReplay(t, "bob", res, err, a.Revision, []string{a.Rows[0].ID})
	res, err = s.CreateDocumentAs(ctx, f.withKey("bob2", "k"), "a", "b.md", "b")
	wantReplay(t, "bob2", res, err, b.Revision, []string{b.Rows[0].ID})
}

// Nach Ablauf der Frist ist der Schlüssel vergessen: Jeder Schreibvorgang
// räumt ab, eine Wiederholung schreibt wie neu.
func TestRequestIDExpires(t *testing.T) {
	ctx := context.Background()
	f := newWriteStore(t)
	s := f.s
	if _, err := s.CreateDocumentAs(ctx, f.withKey("bob", "alt"), "a", "x.md", "x"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateDocumentAs(ctx, f.withKey("alice", "frisch"), "a", "y.md", "y"); err != nil {
		t.Fatal(err)
	}
	// Der Eintrag von bob ist eine Sekunde über der Frist, der von alice eine
	// Sekunde darunter.
	ttl := contract.RequestIDTTL.Milliseconds()
	if _, err := s.db.Exec(`UPDATE requests SET at = at - ? WHERE request_id = 'alt'`, ttl+1000); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE requests SET at = at - ? WHERE request_id = 'frisch'`, ttl-1000); err != nil {
		t.Fatal(err)
	}
	// Ein beliebiger Schreibvorgang eines anderen Accounts räumt ab.
	if _, err := s.CreateDocumentAs(ctx, f.as("eve"), "a", "z.md", "z"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("eve: %v", err)
	}
	if n := countRequests(t, s); n != 2 {
		t.Fatalf("%d Einträge nach einer Ablehnung, erwartet 2 (sie rollt zurück)", n)
	}
	if _, err := s.CreateDocumentAs(ctx, f.as("leser"), "c", "z.md", "z"); !errors.Is(err, ErrNotReadable) {
		t.Fatalf("leser: %v", err)
	}
	if _, err := s.WriteDocumentAs(ctx, f.as("alice"), "a", "y.md", "y2", nil); err != nil {
		t.Fatal(err)
	}
	var left []string
	rows, err := s.db.Query(`SELECT request_id FROM requests ORDER BY request_id`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		left = append(left, id)
	}
	rows.Close()
	if !reflect.DeepEqual(left, []string{"frisch"}) {
		t.Errorf("nach dem Aufräumen: %v, erwartet [frisch]", left)
	}
	// Vergessen: dasselbe create schreibt wie neu und scheitert am eigenen
	// Dokument.
	if _, err := s.CreateDocumentAs(ctx, f.withKey("bob", "alt"), "a", "x.md", "x"); !errors.Is(err, ErrNameTaken) {
		t.Errorf("nach Ablauf: %v, erwartet ErrNameTaken", err)
	}
	if _, err := s.CreateDocumentAs(ctx, f.withKey("alice", "frisch"), "a", "y.md", "y"); err != nil {
		t.Errorf("innerhalb der Frist: %v", err)
	}
}

// Verzeichnisse: Die Wiederholung von rename und delete eines Verzeichnisses
// nennt die ids aller Dokumente in der Reihenfolge der Zeilen; nach dem
// gelungenen rename gibt es den alten Namen nicht mehr, die Wiederholung
// ist trotzdem replayed.
func TestRequestIDDirectory(t *testing.T) {
	ctx := context.Background()
	f := newWriteStore(t)
	s := f.s
	for _, n := range []string{"d/a.md", "d/b/c.md", "x.md"} {
		if _, err := s.CreateDocumentAs(ctx, f.as("bob"), "a", n, n); err != nil {
			t.Fatal(err)
		}
	}
	ren, err := s.RenameDocumentAs(ctx, f.withKey("bob", "r"), "a", "d", "e", nil)
	if err != nil || len(ren.Rows) != 2 {
		t.Fatalf("rename: %+v, %v", ren, err)
	}
	ids := []string{ren.Rows[0].ID, ren.Rows[1].ID}
	res, err := s.RenameDocumentAs(ctx, f.withKey("bob", "r"), "a", "d", "e", nil)
	wantReplay(t, "Wiederholung von rename eines Verzeichnisses", res, err, ren.Revision, ids)

	doc, err := s.RenameDocumentAs(ctx, f.withKey("bob", "r2"), "a", "x.md", "y.md", nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err = s.RenameDocumentAs(ctx, f.withKey("bob", "r2"), "a", "x.md", "y.md", nil)
	wantReplay(t, "Wiederholung von rename eines Dokuments", res, err, doc.Revision, []string{doc.Rows[0].ID})

	del, err := s.DeleteDocumentAs(ctx, f.withKey("bob", "d"), "a", "e", nil, true)
	if err != nil {
		t.Fatal(err)
	}
	res, err = s.DeleteDocumentAs(ctx, f.withKey("bob", "d"), "a", "e", nil, true)
	wantReplay(t, "Wiederholung von delete eines Verzeichnisses", res, err, del.Revision, ids)
}

// Account, Lesbarkeit und Quelle gelten auch für eine Wiederholung: Ein
// inzwischen gesperrter Account bekommt ErrAccountAuth, nicht Replayed; ein
// falsches Token ebenso. Das Recht dagegen prüft die Wiederholung nicht.
func TestRequestIDStillAuthenticated(t *testing.T) {
	ctx := context.Background()
	f := newWriteStore(t)
	s := f.s
	created, err := s.CreateDocumentAs(ctx, f.withKey("bob", "k"), "a", "x.md", "x")
	if err != nil {
		t.Fatal(err)
	}
	wrong := f.withKey("bob", "k")
	wrong.TokenHash = strings.Repeat("0", 64)
	if _, err := s.CreateDocumentAs(ctx, wrong, "a", "x.md", "x"); !errors.Is(err, ErrAccountAuth) {
		t.Errorf("falsches Token: %v, erwartet ErrAccountAuth", err)
	}
	other := f.withKey("bob", "k")
	other.Carrier = "fremd"
	if _, err := s.CreateDocumentAs(ctx, other, "a", "x.md", "x"); !errors.Is(err, ErrNotReadable) {
		t.Errorf("anderer Node: %v, erwartet ErrNotReadable", err)
	}
	// Recht entzogen: Die Wiederholung schreibt nichts, also braucht sie es
	// nicht.
	if _, err := s.GrantAccount(ctx, "bob", "a", contract.Rights{}); err != nil {
		t.Fatal(err)
	}
	res, err := s.CreateDocumentAs(ctx, f.withKey("bob", "k"), "a", "x.md", "x")
	wantReplay(t, "ohne write", res, err, created.Revision, []string{created.Rows[0].ID})
	if err := s.SetAccountLocked(ctx, "bob", true); err != nil {
		t.Fatal(err)
	}
	if res, err := s.CreateDocumentAs(ctx, f.withKey("bob", "k"), "a", "x.md", "x"); !errors.Is(err, ErrAccountAuth) {
		t.Errorf("gesperrt: %+v, %v, erwartet ErrAccountAuth", res, err)
	}
}

// Zwei gleichzeitige Aufrufe mit demselben Schlüssel: genau eine Revision,
// die übrigen sind Wiederholungen.
func TestRequestIDConcurrent(t *testing.T) {
	ctx := context.Background()
	f := newWriteStore(t)
	s := f.s
	rev0 := revision(t, s)
	const n = 8
	var wg sync.WaitGroup
	results := make([]WriteResult, n)
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = s.CreateDocumentAs(ctx, f.withKey("bob", "gleich"), "a", "x.md", "x")
		}()
	}
	wg.Wait()
	wrote := -1
	for i := range n {
		switch {
		case errs[i] != nil:
			t.Errorf("Aufruf %d: %v", i, errs[i])
		case !results[i].Replayed && wrote >= 0:
			t.Errorf("Aufruf %d und %d haben beide geschrieben", wrote, i)
		case !results[i].Replayed:
			wrote = i
		}
	}
	if wrote < 0 {
		t.Fatal("keiner hat geschrieben")
	}
	for i := range n {
		if i != wrote && errs[i] == nil {
			wantReplay(t, "Wiederholung", results[i], nil, results[wrote].Revision, []string{results[wrote].Rows[0].ID})
		}
	}
	if got := revision(t, s); got != rev0+1 {
		t.Errorf("Revision %d, erwartet %d", got, rev0+1)
	}
}
