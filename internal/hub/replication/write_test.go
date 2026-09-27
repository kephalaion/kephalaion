package replication

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kephalaion/kephalaion/internal/contract"
	"github.com/kephalaion/kephalaion/internal/contract/httpapi"
	"github.com/kephalaion/kephalaion/internal/hub/store"
)

// writers legt die Accounts für die Schreibvorgänge an. laptop darf a und b
// abgleichen, c nicht.
//
//	bob       User kleist  write in a und c
//	bob2      User kleist  write in a
//	eve       User eve     supersede in a, kein write
//	leser     User leser   nur read in a
//	gesperrt  User gesperrt write in a, gesperrt
//
// Liefert die Anmeldung je Account.
func (f *fixture) writers(t *testing.T) map[string]contract.AccountAuth {
	t.Helper()
	ctx := context.Background()
	accounts := []struct {
		name, user string
		grants     map[string]contract.Rights
	}{
		{"bob", "kleist", map[string]contract.Rights{"a": {Write: true}, "c": {Write: true}}},
		{"bob2", "kleist", map[string]contract.Rights{"a": {Write: true}}},
		{"eve", "eve", map[string]contract.Rights{"a": {Supersede: true}}},
		{"leser", "leser", map[string]contract.Rights{"a": {}}},
		{"gesperrt", "gesperrt", map[string]contract.Rights{"a": {Write: true}}},
	}
	out := map[string]contract.AccountAuth{}
	for _, a := range accounts {
		tok, err := f.st.AddAccount(ctx, a.name, a.user, "")
		if err != nil {
			t.Fatal(err)
		}
		out[a.name] = contract.AccountAuth{Account: a.name, Token: tok}
		for c, r := range a.grants {
			if _, err := f.st.GrantAccount(ctx, a.name, c, r); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := f.st.SetAccountLocked(ctx, "gesperrt", true); err != nil {
		t.Fatal(err)
	}
	return out
}

func (f *fixture) createReq(acc contract.AccountAuth, collection, name, content string) contract.CreateRequest {
	return contract.CreateRequest{Version: contract.Version, Auth: f.node(), Account: acc, Collection: collection,
		Name: name, Content: content}
}

func (f *fixture) writeReq(acc contract.AccountAuth, collection, name, content string, base *int64) contract.WriteRequest {
	return contract.WriteRequest{Version: contract.Version, Auth: f.node(), Account: acc, Collection: collection,
		Name: name, Content: content, BaseRevision: base}
}

func (f *fixture) deleteReq(acc contract.AccountAuth, collection, name string, base *int64) contract.DeleteRequest {
	return contract.DeleteRequest{Version: contract.Version, Auth: f.node(), Account: acc, Collection: collection,
		Name: name, BaseRevision: base}
}

func (f *fixture) revision(t *testing.T) int64 {
	t.Helper()
	info, err := f.st.Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return info.Revision
}

func rev(r int64) *int64 { return &r }

// syncedRow liest die Zeile id so, wie der Abgleich sie liefert.
func (f *fixture) syncedRow(t *testing.T, collection, id string) contract.Row {
	t.Helper()
	for _, r := range f.sync(t, f.request(contract.DefaultPageSize, contract.Since{Collection: collection})).Rows {
		if r.ID == id {
			return r
		}
	}
	t.Fatalf("Zeile %s nicht im Abgleich", id)
	return contract.Row{}
}

// checkWritten prüft eine Antwort mit genau einer Zeile: Kopf, Revision des
// Hubs, Zeile wie im Abgleich.
func (f *fixture) checkWritten(t *testing.T, what string, resp contract.WriteResponse, err error) contract.Row {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
	info, _ := f.st.Info(context.Background())
	if resp.HubID != info.HubID || resp.Version != contract.Version || len(resp.Rows) != 1 ||
		resp.Rows[0].Revision != resp.Revision {
		t.Fatalf("%s: Antwort %+v", what, resp)
	}
	if got := f.syncedRow(t, resp.Rows[0].Collection, resp.Rows[0].ID); !reflect.DeepEqual(got, resp.Rows[0]) {
		t.Errorf("%s: Zeile der Antwort %+v, im Abgleich %+v", what, resp.Rows[0], got)
	}
	return resp.Rows[0]
}

func TestCreateWriteDelete(t *testing.T) {
	forTransports(t, func(t *testing.T, newFixture func(*testing.T) *fixture) {
		f := newFixture(t)
		ctx := context.Background()
		acc := f.writers(t)

		// create: der User in created_by/updated_by, Account und Node in
		// actions.
		resp, err := f.api().Create(ctx, f.createReq(acc["bob"], "a", "notiz.md", "eins"))
		row := f.checkWritten(t, "create", resp, err)
		if row.Name != "notiz.md" || row.Content == nil || *row.Content != "eins" || row.CreatedBy != "kleist" ||
			row.UpdatedBy != "kleist" || row.Deleted || resp.Revision != f.revision(t) {
			t.Errorf("create: %+v", row)
		}
		var account, carrier, action string
		if err := f.raw(t).QueryRow(`SELECT account, carrier, action FROM actions WHERE document_id = ?`, row.ID).
			Scan(&account, &carrier, &action); err != nil || account != "bob" || carrier != "laptop" || action != "create" {
			t.Errorf("actions: %s %s %s, %v", account, carrier, action, err)
		}

		// write: Eigenes über einen anderen Account desselben Users, mit
		// der Revision als Vorbedingung.
		resp, err = f.api().Write(ctx, f.writeReq(acc["bob2"], "a", "notiz.md", "zwei", rev(resp.Revision)))
		row2 := f.checkWritten(t, "write", resp, err)
		if row2.ID != row.ID || *row2.Content != "zwei" || resp.Revision <= row.Revision || row2.UpdatedBy != "kleist" {
			t.Errorf("write: %+v", row2)
		}
		// Unveränderter Inhalt: keine neue Revision, die bestehende Zeile.
		before := f.revision(t)
		resp, err = f.api().Write(ctx, f.writeReq(acc["bob"], "a", "notiz.md", "zwei", rev(row2.Revision)))
		if row3 := f.checkWritten(t, "write unverändert", resp, err); !reflect.DeepEqual(row3, row2) ||
			resp.Revision != row2.Revision || f.revision(t) != before {
			t.Errorf("unverändert: %+v, Revision %d → %d", row3, before, f.revision(t))
		}
		// Ohne Vorbedingung; Fremdes mit supersede, ohne write.
		f.put(t, "a", "fremd.md")
		resp, err = f.api().Write(ctx, f.writeReq(acc["eve"], "a", "fremd.md", "abgelöst", nil))
		if row := f.checkWritten(t, "write mit supersede", resp, err); row.CreatedBy != "admin" || row.UpdatedBy != "eve" {
			t.Errorf("supersede: %+v", row)
		}

		// delete: Löschmarke unter neuer Revision.
		resp, err = f.api().Delete(ctx, f.deleteReq(acc["bob"], "a", "notiz.md", rev(row2.Revision)))
		del := f.checkWritten(t, "delete", resp, err)
		if del.ID != row.ID || !del.Deleted || del.Content != nil || del.Revision <= row2.Revision || del.UpdatedBy != "kleist" {
			t.Errorf("delete: %+v", del)
		}
		// Die Löschmarke hindert create nicht; das Dokument bekommt eine
		// neue id. Leer ist erlaubt.
		resp, err = f.api().Create(ctx, f.createReq(acc["bob"], "a", "notiz.md", ""))
		if again := f.checkWritten(t, "create nach delete", resp, err); again.ID == row.ID || *again.Content != "" {
			t.Errorf("neu angelegt: %+v", again)
		}
		// Fremdes löschen mit supersede.
		resp, err = f.api().Delete(ctx, f.deleteReq(acc["eve"], "a", "fremd.md", nil))
		f.checkWritten(t, "delete mit supersede", resp, err)
	})
}

// writeFailure ist ein Schreibvorgang, der scheitern muss.
type writeFailure struct {
	name string
	call func(ctx context.Context, hub contract.Hub) (contract.WriteResponse, error)
	want error
	// msg steht in der Meldung, wenn gesetzt.
	msg string
}

// Jeder Code der Schreibvorgänge, ohne dass sich am Hub etwas ändert; dazu
// die Reihenfolge der Prüfung: Fassung, Form, Node, Account, Lesbarkeit.
func TestWriteCodes(t *testing.T) {
	forTransports(t, func(t *testing.T, newFixture func(*testing.T) *fixture) {
		f := newFixture(t)
		ctx := context.Background()
		acc := f.writers(t)
		f.put(t, "a", "fremd.md")
		resp, err := f.api().Create(ctx, f.createReq(acc["bob"], "a", "eigen.md", "x"))
		if err != nil {
			t.Fatal(err)
		}
		cur := resp.Revision
		if _, err := f.api().Create(ctx, f.createReq(acc["bob"], "a", "dir/drin.md", "x")); err != nil {
			t.Fatal(err)
		}
		bob, leser := acc["bob"], acc["leser"]
		create := func(r contract.CreateRequest) func(context.Context, contract.Hub) (contract.WriteResponse, error) {
			return func(ctx context.Context, h contract.Hub) (contract.WriteResponse, error) { return h.Create(ctx, r) }
		}
		write := func(r contract.WriteRequest) func(context.Context, contract.Hub) (contract.WriteResponse, error) {
			return func(ctx context.Context, h contract.Hub) (contract.WriteResponse, error) { return h.Write(ctx, r) }
		}
		del := func(r contract.DeleteRequest) func(context.Context, contract.Hub) (contract.WriteResponse, error) {
			return func(ctx context.Context, h contract.Hub) (contract.WriteResponse, error) { return h.Delete(ctx, r) }
		}
		badNode := func(r contract.CreateRequest) contract.CreateRequest { r.Auth.Token = bob.Token; return r }
		version := func(r contract.CreateRequest, v int) contract.CreateRequest { r.Version = v; return r }
		unknown := contract.AccountAuth{Account: "dave", Token: bob.Token}
		wrongToken := contract.AccountAuth{Account: "bob", Token: leser.Token}

		failures := []writeFailure{
			{"name_taken", create(f.createReq(bob, "a", "eigen.md", "y")), contract.ErrNameTaken, "eigen.md"},
			{"name_taken fremd", create(f.createReq(bob, "a", "fremd.md", "y")), contract.ErrNameTaken, ""},
			{"path_conflict unter Datei", create(f.createReq(bob, "a", "eigen.md/x.md", "y")), contract.ErrPathConflict, ""},
			{"path_conflict Verzeichnis", create(f.createReq(bob, "a", "dir", "y")), contract.ErrPathConflict, ""},
			{"stale_revision write", write(f.writeReq(bob, "a", "eigen.md", "y", rev(cur-1))), contract.ErrStaleRevision, "Revision " + strconv.FormatInt(cur, 10)},
			{"stale_revision delete", del(f.deleteReq(bob, "a", "eigen.md", rev(cur+100))), contract.ErrStaleRevision, ""},
			{"not_found write", write(f.writeReq(bob, "a", "fehlt.md", "y", nil)), contract.ErrNotFound, "fehlt.md"},
			{"not_found delete", del(f.deleteReq(bob, "a", "fehlt.md", nil)), contract.ErrNotFound, ""},
			{"forbidden create ohne write", create(f.createReq(leser, "a", "neu.md", "y")), contract.ErrForbidden, "write fehlt"},
			{"forbidden create mit supersede", create(f.createReq(acc["eve"], "a", "neu.md", "y")), contract.ErrForbidden, ""},
			{"forbidden Fremdes", write(f.writeReq(bob, "a", "fremd.md", "y", nil)), contract.ErrForbidden, "gehört admin, supersede fehlt"},
			{"forbidden Fremdes löschen", del(f.deleteReq(leser, "a", "eigen.md", nil)), contract.ErrForbidden, "gehört kleist"},
			{"not_readable unbekannt", create(f.createReq(bob, "gibtsnicht", "x.md", "y")), contract.ErrNotReadable, ""},
			{"not_readable Node ohne replicate", create(f.createReq(bob, "c", "x.md", "y")), contract.ErrNotReadable, ""},
			{"not_readable ohne Zeile", create(f.createReq(bob, "b", "x.md", "y")), contract.ErrNotReadable, ""},
			{"not_readable kein Name", create(f.createReq(bob, "SYSTEM:x", "x.md", "y")), contract.ErrNotReadable, ""},
			{"invalid SYSTEM:", create(f.createReq(bob, "a", "SYSTEM:A:bob", "y")), contract.ErrInvalid, "SYSTEM:"},
			{"invalid SYSTEM: löschen", del(f.deleteReq(bob, "a", "SYSTEM:A:bob", nil)), contract.ErrInvalid, ""},
			{"invalid Name leer", create(f.createReq(bob, "a", "", "y")), contract.ErrInvalid, ""},
			{"invalid Name absolut", write(f.writeReq(bob, "a", "/eigen.md", "y", nil)), contract.ErrInvalid, ""},
			{"invalid NUL", write(f.writeReq(bob, "a", "eigen.md", "a\x00b", nil)), contract.ErrInvalid, "UTF-8"},
			{"invalid kein UTF-8", create(f.createReq(bob, "a", "neu.md", "\xff")), contract.ErrInvalid, "UTF-8"},
			{"invalid Name kein UTF-8", write(f.writeReq(bob, "a", "eigen\xff.md", "y", nil)), contract.ErrInvalid, "UTF-8"},
			{"invalid Name kein UTF-8 löschen", del(f.deleteReq(bob, "a", "eigen\xff.md", nil)), contract.ErrInvalid, "UTF-8"},
			{"invalid zu groß", create(f.createReq(bob, "a", "neu.md", strings.Repeat("x", contract.MaxDocumentBytes+1))),
				contract.ErrInvalid, "1 MiB"},
			{"invalid base 0", write(f.writeReq(bob, "a", "eigen.md", "y", rev(0))), contract.ErrInvalid, "base_revision"},
			{"invalid base negativ", del(f.deleteReq(bob, "a", "eigen.md", rev(-1))), contract.ErrInvalid, ""},
			{"account unbekannt", create(f.createReq(unknown, "a", "neu.md", "y")), contract.ErrAccountUnauthenticated, ""},
			{"account falsches Token", write(f.writeReq(wrongToken, "a", "eigen.md", "y", nil)), contract.ErrAccountUnauthenticated, ""},
			{"account gesperrt", create(f.createReq(acc["gesperrt"], "a", "neu.md", "y")), contract.ErrAccountUnauthenticated, ""},
			{"account leer", del(f.deleteReq(contract.AccountAuth{}, "a", "eigen.md", nil)), contract.ErrAccountUnauthenticated, ""},
			{"Node falsch", create(badNode(f.createReq(bob, "a", "neu.md", "y"))), contract.ErrUnauthenticated, ""},
			{"Fassung 2", create(version(f.createReq(bob, "a", "neu.md", "y"), 2)), contract.ErrUnsupportedVersion, ""},
			// Reihenfolge.
			{"Fassung vor Form", create(version(f.createReq(bob, "a", "SYSTEM:x", "y"), 0)), contract.ErrUnsupportedVersion, ""},
			{"Form vor Node", create(badNode(f.createReq(bob, "a", "SYSTEM:x", "y"))), contract.ErrInvalid, ""},
			{"Node vor Account", create(badNode(f.createReq(unknown, "a", "neu.md", "y"))), contract.ErrUnauthenticated, ""},
			{"Account vor Lesbarkeit", create(f.createReq(unknown, "gibtsnicht", "neu.md", "y")), contract.ErrAccountUnauthenticated, ""},
			{"Lesbarkeit vor Recht", create(f.createReq(leser, "b", "neu.md", "y")), contract.ErrNotReadable, ""},
			{"Recht vor Vorbedingung", write(f.writeReq(leser, "a", "eigen.md", "y", rev(1))), contract.ErrForbidden, ""},
		}
		before := f.revision(t)
		var accountMsgs []string
		for _, c := range failures {
			resp, err := c.call(ctx, f.api())
			var ce *contract.Error
			if !errors.Is(err, c.want) || !errors.As(err, &ce) {
				t.Errorf("%s: %v, erwartet %v", c.name, err, c.want)
				continue
			}
			if !strings.Contains(err.Error(), c.msg) {
				t.Errorf("%s: Meldung %q ohne %q", c.name, err, c.msg)
			}
			if resp.HubID != "" || resp.Rows != nil {
				t.Errorf("%s: Antwort trotz Fehler: %+v", c.name, resp)
			}
			if c.want == contract.ErrAccountUnauthenticated {
				accountMsgs = append(accountMsgs, err.Error())
			}
		}
		for _, m := range accountMsgs {
			if m != accountMsgs[0] {
				t.Errorf("Meldungen verschieden: %q und %q", accountMsgs[0], m)
			}
		}
		if after := f.revision(t); after != before {
			t.Errorf("Fehlversuche haben geschrieben: Revision %d → %d", before, after)
		}
	})
}

// Ein Dokument an der Größengrenze aus Zeichen, die JSON auf sechs Byte
// aufbläht, kommt über jeden Transport an und ganz zurück; ein Byte mehr
// lehnt der Hub selbst ab (invalid), nicht die Grenze des Bodys.
func TestWriteAtSizeLimit(t *testing.T) {
	forTransports(t, func(t *testing.T, newFixture func(*testing.T) *fixture) {
		f := newFixture(t)
		ctx := context.Background()
		acc := f.writers(t)
		content := strings.Repeat("\x01<>&", contract.MaxDocumentBytes/4)
		resp, err := f.api().Create(ctx, f.createReq(acc["bob"], "a", "gross.md", content))
		if row := f.checkWritten(t, "an der Grenze", resp, err); *row.Content != content {
			t.Error("Inhalt verändert")
		}
		resp, err = f.api().Write(ctx, f.writeReq(acc["bob"], "a", "gross.md", content+"\x01", nil))
		var ce *contract.Error
		if !errors.As(err, &ce) || ce.Code != contract.CodeInvalid || !strings.Contains(ce.Message, "1 MiB") {
			t.Errorf("ein Byte zu groß: %v", err)
		}
	})
}

// countingStore zählt die Schreibvorgänge und lässt sie nach dem Commit
// scheitern, wenn fail gilt — wie eine Datenbank, die danach ausfällt.
type countingStore struct {
	store.Store
	calls atomic.Int32
	fail  bool
}

var errAfterCommit = errors.New("Datenbank weg")

func (s *countingStore) after(res store.WriteResult, err error) (store.WriteResult, error) {
	s.calls.Add(1)
	if err == nil && s.fail {
		return store.WriteResult{}, errAfterCommit
	}
	return res, err
}

func (s *countingStore) CreateDocumentAs(ctx context.Context, auth store.WriteAuth, collection, name, content string) (store.WriteResult, error) {
	return s.after(s.Store.CreateDocumentAs(ctx, auth, collection, name, content))
}

func (s *countingStore) WriteDocumentAs(ctx context.Context, auth store.WriteAuth, collection, name, content string, base *int64) (store.WriteResult, error) {
	return s.after(s.Store.WriteDocumentAs(ctx, auth, collection, name, content, base))
}

func (s *countingStore) DeleteDocumentAs(ctx context.Context, auth store.WriteAuth, collection, name string, base *int64, recursive bool) (store.WriteResult, error) {
	return s.after(s.Store.DeleteDocumentAs(ctx, auth, collection, name, base, recursive))
}

func (s *countingStore) RenameDocumentAs(ctx context.Context, auth store.WriteAuth, collection, name, newName string, base *int64) (store.WriteResult, error) {
	return s.after(s.Store.RenameDocumentAs(ctx, auth, collection, name, newName, base))
}

// overHTTP stellt den Hub über st hinter handler (nil: unverändert) und
// liefert einen Client mit Wiederholungen — wiederholen darf er trotzdem
// nicht.
func overHTTP(t *testing.T, st store.Store, wrap func(http.Handler) http.Handler) *httpapi.Client {
	t.Helper()
	var h http.Handler = httpapi.NewHandler(New(st))
	if wrap != nil {
		h = wrap(h)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := httpapi.NewClient(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	c.Retries, c.Backoff = 3, time.Millisecond
	return c
}

// Scheitert der Hub nach dem Commit (5xx), ist der Ausgang unklar: genau
// ein Aufruf, und das Dokument steht am Hub genau einmal.
func TestWriteNotRepeatedAfter5xx(t *testing.T) {
	f := newLocalFixture(t)
	ctx := context.Background()
	acc := f.writers(t)
	st := &countingStore{Store: f.st, fail: true}
	c := overHTTP(t, st, nil)

	resp, err := c.Create(ctx, f.createReq(acc["bob"], "a", "neu.md", "eins"))
	if !errors.Is(err, contract.ErrOutcomeUnknown) || st.calls.Load() != 1 || resp.Rows != nil {
		t.Fatalf("create: %d Aufrufe, %v", st.calls.Load(), err)
	}
	doc, err := f.st.Document(ctx, "a", "neu.md")
	if err != nil || doc.Content != "eins" {
		t.Fatalf("am Hub: %+v, %v", doc, err)
	}
	st.calls.Store(0)
	if _, err := c.Write(ctx, f.writeReq(acc["bob"], "a", "neu.md", "zwei", rev(doc.Revision))); !errors.Is(err, contract.ErrOutcomeUnknown) ||
		st.calls.Load() != 1 {
		t.Errorf("write: %d Aufrufe, %v", st.calls.Load(), err)
	}
	st.calls.Store(0)
	if _, err := c.Rename(ctx, f.renameReq(acc["bob"], "a", "neu.md", "dir/neu.md", nil)); !errors.Is(err, contract.ErrOutcomeUnknown) ||
		st.calls.Load() != 1 {
		t.Errorf("rename: %d Aufrufe, %v", st.calls.Load(), err)
	}
	st.calls.Store(0)
	if _, err := c.Delete(ctx, f.deleteDirReq(acc["bob"], "a", "dir")); !errors.Is(err, contract.ErrOutcomeUnknown) ||
		st.calls.Load() != 1 {
		t.Errorf("delete: %d Aufrufe, %v", st.calls.Load(), err)
	}
	if _, err := f.st.Document(ctx, "a", "dir/neu.md"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("nach delete: %v", err)
	}
	// Über local meldet der Hub denselben Fehler als gewöhnlichen; unklar
	// macht ihn localHub in cmd/kephalaion.
	if _, err := New(st).Create(ctx, f.createReq(acc["bob"], "a", "lokal.md", "x")); !errors.Is(err, errAfterCommit) {
		t.Errorf("local: %v", err)
	}
}

// Kommt die Antwort nicht rechtzeitig, ist der Ausgang unklar — auch wenn
// der Hub geschrieben hat; der Client versucht es kein zweites Mal.
func TestWriteNotRepeatedAfterTimeout(t *testing.T) {
	f := newLocalFixture(t)
	ctx := context.Background()
	acc := f.writers(t)
	var calls atomic.Int32
	c := overHTTP(t, f.st, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			next.ServeHTTP(httptest.NewRecorder(), r) // der Hub schreibt, die Antwort geht verloren
			select {
			case <-r.Context().Done():
			case <-time.After(5 * time.Second):
			}
		})
	})
	c.ShortTimeout = 100 * time.Millisecond
	_, err := c.Create(ctx, f.createReq(acc["bob"], "a", "neu.md", "eins"))
	if !errors.Is(err, contract.ErrOutcomeUnknown) || calls.Load() != 1 {
		t.Errorf("create: %d Aufrufe, %v", calls.Load(), err)
	}
	if doc, err := f.st.Document(ctx, "a", "neu.md"); err != nil || doc.Content != "eins" {
		t.Errorf("am Hub: %+v, %v", doc, err)
	}
}

// Nimmt der Hub keine Verbindung an, ist nichts gespeichert und der Fehler
// eindeutig: weder unklar noch ein Fehler des Vertrags.
func TestWriteRefused(t *testing.T) {
	f := newLocalFixture(t)
	ctx := context.Background()
	acc := f.writers(t)
	srv := httptest.NewServer(httpapi.NewHandler(f.hub))
	c, err := httpapi.NewClient(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	srv.Close()
	before := f.revision(t)
	_, err = c.Create(ctx, f.createReq(acc["bob"], "a", "neu.md", "eins"))
	var ce *contract.Error
	if err == nil || errors.Is(err, contract.ErrOutcomeUnknown) || errors.Is(err, contract.ErrUnknownOperation) ||
		errors.As(err, &ce) {
		t.Errorf("Verbindung verweigert: %v", err)
	}
	if _, err := f.st.Document(ctx, "a", "neu.md"); !errors.Is(err, store.ErrNotFound) || f.revision(t) != before {
		t.Errorf("gespeichert: %v", err)
	}
}

// infoAfterWrite lässt Info scheitern, sobald ein Schreibvorgang gelaufen
// ist — wie eine Datenbank, die nach dem Commit ausfällt.
type infoAfterWrite struct {
	countingStore
}

func (s *infoAfterWrite) Info(ctx context.Context) (store.Info, error) {
	if s.calls.Load() > 0 {
		return store.Info{}, errAfterCommit
	}
	return s.Store.Info(ctx)
}

// Nach dem Commit liest ein Schreibvorgang nichts mehr: Die Antwort steht
// vorher fest, ein Fehler danach kann den Erfolg nicht kippen.
func TestWriteNothingAfterCommit(t *testing.T) {
	f := newLocalFixture(t)
	acc := f.writers(t)
	st := &infoAfterWrite{countingStore{Store: f.st}}
	info, _ := f.st.Info(context.Background())
	resp, err := New(st).Create(context.Background(), f.createReq(acc["bob"], "a", "neu.md", "eins"))
	if err != nil || st.calls.Load() != 1 || resp.HubID != info.HubID || len(resp.Rows) != 1 {
		t.Errorf("create nach dem Commit: %+v, %v", resp, err)
	}
}
