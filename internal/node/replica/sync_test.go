package replica

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/oklog/ulid/v2"

	"github.com/kephalaion/kephalaion/internal/config"
	"github.com/kephalaion/kephalaion/internal/contract"
	"github.com/kephalaion/kephalaion/internal/ident"
	"github.com/kephalaion/kephalaion/internal/node/store"
	"github.com/kephalaion/kephalaion/internal/sqlitedb"
)

// env ist ein Node mit dem Hub-Eintrag „privat“ (Node-Name laptop) und eine
// Attrappe des Hubs, die a und b erlaubt, c nicht.
type env struct {
	t     *testing.T
	nodes store.Store
	hub   *fakeHub
	sync  *Syncer
}

func newEnv(t *testing.T, wanted ...string) *env {
	t.Helper()
	ctx := context.Background()
	nodes, err := store.Create(ctx, config.SQLiteDB(filepath.Join(t.TempDir(), "node.db")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = nodes.Close() })
	token, err := ident.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	if err := nodes.AddHub(ctx, store.Hub{Name: "privat", NodeName: "laptop", Transport: store.TransportHTTPS,
		Address: "https://hub.example.org", Token: token}, false); err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, nodes: nodes, hub: newFakeHub(token, "a", "b"), sync: &Syncer{Nodes: nodes}}
	for _, c := range wanted {
		e.want(c)
	}
	return e
}

func (e *env) want(c string) {
	e.t.Helper()
	if err := e.nodes.AddCollection(context.Background(), "privat", c); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) unwant(c string) {
	e.t.Helper()
	if err := e.nodes.RemoveCollection(context.Background(), "privat", c); err != nil {
		e.t.Fatal(err)
	}
}

// run gleicht den Eintrag ab und liefert das Ergebnis, auch mit Err.
func (e *env) run() HubResult {
	e.t.Helper()
	res, err := e.sync.Sync(context.Background(), "privat", func(store.Hub) (contract.Hub, error) { return e.hub, nil })
	if err != nil {
		e.t.Fatal(err)
	}
	if len(res) != 1 {
		e.t.Fatalf("%d Ergebnisse", len(res))
	}
	return res[0]
}

// ok gleicht ab und verlangt Erfolg.
func (e *env) ok() HubResult {
	e.t.Helper()
	res := e.run()
	if res.Err != nil {
		e.t.Fatalf("Abgleich: %v", res.Err)
	}
	return res
}

func (e *env) replica() *Replica {
	e.t.Helper()
	r, err := Open(context.Background(), e.nodes.ReplicaPath("privat"))
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { _ = r.Close() })
	return r
}

// states liefert die Stände der Replica als Map.
func (e *env) states() map[string]int64 {
	e.t.Helper()
	sts, err := e.replica().States(context.Background())
	if err != nil {
		e.t.Fatal(err)
	}
	out := map[string]int64{}
	for _, s := range sts {
		out[s.Collection] = s.Revision
	}
	return out
}

// names liefert die lebenden Namen einer Collection der Replica mit Inhalt.
func (e *env) names(collection string) map[string]string {
	e.t.Helper()
	docs, err := e.replica().Documents(context.Background(), collection, "")
	if err != nil {
		e.t.Fatal(err)
	}
	out := map[string]string{}
	for _, d := range docs {
		out[d.Name] = *d.Content
	}
	return out
}

// allRows liefert alle Zeilen der Replica, auch Löschmarken und
// SYSTEM:-Zeilen, nach id.
func (e *env) allRows() map[string]contract.Row {
	e.t.Helper()
	rows, err := e.replica().db.QueryContext(context.Background(), `SELECT `+documentColumns+` FROM documents`)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]contract.Row{}
	for rows.Next() {
		d, err := scanDocument(rows)
		if err != nil {
			e.t.Fatal(err)
		}
		out[d.ID] = d
	}
	return out
}

// wantRows ist, was die Attrappe für die Collections cs hat.
func (e *env) wantRows(cs ...string) map[string]contract.Row {
	out := map[string]contract.Row{}
	for id, r := range e.hub.docs {
		for _, c := range cs {
			if r.Collection == c {
				out[id] = r
			}
		}
	}
	return out
}

func (e *env) checkMirror(cs ...string) {
	e.t.Helper()
	if got, want := e.allRows(), e.wantRows(cs...); !reflect.DeepEqual(got, want) {
		e.t.Errorf("Replica weicht vom Hub ab:\n got  %v\n want %v", got, want)
	}
}

func result(res HubResult, c string) CollectionResult {
	for _, r := range res.Collections {
		if r.Collection == c {
			return r
		}
	}
	return CollectionResult{Collection: c, Status: -1}
}

func TestFirstSync(t *testing.T) {
	e := newEnv(t, "a", "b", "c")
	ctx := context.Background()
	e.hub.put("a", "x.md", "x")
	e.hub.write(func(rev int64) {
		e.hub.newDoc(rev, "a", "dir/y.md", "y")
		e.hub.newDoc(rev, "a", "SYSTEM:A:anna", "{}")
		id := e.hub.newDoc(rev, "b", "z.md", "z")
		e.hub.change(rev, id, func(r *contract.Row) { r.Meta = str(`{"k":1}`) })
	})
	gone := e.hub.put("a", "weg.md", "weg")
	e.hub.write(func(rev int64) {
		e.hub.change(rev, gone, func(r *contract.Row) { r.Content, r.Deleted = nil, true })
	})
	e.hub.put("c", "geheim.md", "nein")

	path := e.nodes.ReplicaPath("privat")
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Replica vor dem ersten Abgleich: %v", err)
	}
	res := e.ok()
	if res.HubID != e.hub.id || res.Reset != "" || res.Pages != 1 {
		t.Errorf("Ergebnis = %+v", res)
	}
	if !reflect.DeepEqual(res.Allowed, []string{"a", "b"}) {
		t.Errorf("Allowed = %v", res.Allowed)
	}
	if r := result(res, "a"); r.Status != Synced || r.Rows != 4 || r.Revision != e.hub.rev {
		t.Errorf("a = %+v", r)
	}
	if r := result(res, "c"); r.Status != NotAllowed || r.Removed != 0 {
		t.Errorf("c = %+v", r)
	}
	fi, err := os.Stat(filepath.Dir(path))
	if err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("Verzeichnis der Replicas: %v, %v", fi.Mode(), err)
	}
	fi, err = os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("Replica: %v, %v", fi.Mode(), err)
	}

	// Alle Spalten wie am Hub, auch Löschmarken, meta und SYSTEM:-Zeilen.
	e.checkMirror("a", "b")
	if got := e.states(); !reflect.DeepEqual(got, map[string]int64{"a": e.hub.rev, "b": e.hub.rev}) {
		t.Errorf("Stände = %v", got)
	}
	// Die Kopie der hub_id steht in node.db.
	if h, _ := e.nodes.Hub(ctx, "privat"); h.HubID != e.hub.id {
		t.Errorf("hubs.hub_id = %q", h.HubID)
	}
	if got := e.replica().HubID(); got != e.hub.id {
		t.Errorf("db_info.hub_id = %q", got)
	}
	// Lesen ohne Löschmarken und SYSTEM:-Zeilen.
	if got := e.names("a"); !reflect.DeepEqual(got, map[string]string{"x.md": "x", "dir/y.md": "y"}) {
		t.Errorf("a = %v", got)
	}
	r := e.replica()
	if _, err := r.Document(ctx, "a", "weg.md"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Löschmarke gelesen: %v", err)
	}
	if _, err := r.Document(ctx, "a", "SYSTEM:A:anna"); err == nil {
		t.Error("SYSTEM:-Zeile gelesen")
	}
	if _, err := r.Documents(ctx, "c", ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("nicht erlaubte Collection gelesen: %v", err)
	}
	if d, err := r.Document(ctx, "b", "z.md"); err != nil || *d.Content != "z" || *d.Meta != `{"k":1}` {
		t.Errorf("Document = %+v, %v", d, err)
	}
	if docs, err := r.Documents(ctx, "a", "dir"); err != nil || len(docs) != 1 || docs[0].Name != "dir/y.md" {
		t.Errorf("Documents(dir) = %+v, %v", docs, err)
	}
}

func TestFollowUpOnlyNew(t *testing.T) {
	e := newEnv(t, "a", "b")
	e.hub.put("a", "1.md", "1")
	e.hub.put("b", "2.md", "2")
	e.ok()
	first := e.hub.rev

	// Nichts Neues: eine leere Seite, die Stände bleiben.
	res := e.ok()
	if r := result(res, "a"); r.Rows != 0 || r.Revision != first {
		t.Errorf("a = %+v", r)
	}
	e.hub.put("a", "3.md", "3")
	calls := e.hub.calls
	res = e.ok()
	req := e.hub.requests[calls]
	for _, s := range req.Collections {
		if s.Since != first {
			t.Errorf("Anfrage %s seit %d, erwartet %d", s.Collection, s.Since, first)
		}
	}
	if r := result(res, "a"); r.Rows != 1 || r.Revision != e.hub.rev {
		t.Errorf("a = %+v", r)
	}
	if r := result(res, "b"); r.Rows != 0 || r.Revision != e.hub.rev {
		t.Errorf("b = %+v", r)
	}
	e.checkMirror("a", "b")
}

// TestAbortAndResume: Bricht der Abgleich nach Seite n ab, ist bis dahin
// alles geschrieben, und der nächste setzt dort fort.
func TestAbortAndResume(t *testing.T) {
	e := newEnv(t, "a", "b")
	e.sync.PageSize = 2
	for i := range 6 {
		e.hub.put([]string{"a", "b"}[i%2], string(rune('p'+i))+".md", "x")
	}
	e.hub.failAt = 3 // Seiten 1 und 2 kommen an, der dritte Aufruf scheitert.
	res := e.run()
	if !errors.Is(res.Err, errTransport) || res.Pages != 2 {
		t.Fatalf("Ergebnis = %+v", res)
	}
	if got := e.states(); !reflect.DeepEqual(got, map[string]int64{"a": 4, "b": 4}) {
		t.Errorf("Stände nach Abbruch = %v", got)
	}
	if n := len(e.allRows()); n != 4 {
		t.Errorf("%d Zeilen nach Abbruch, erwartet 4", n)
	}
	calls := e.hub.calls
	res = e.ok()
	if req := e.hub.requests[calls]; req.Collections[0].Since != 4 || req.Collections[1].Since != 4 {
		t.Errorf("Fortsetzung fragt %+v", req.Collections)
	}
	if res.Pages != 1 {
		t.Errorf("Seiten = %d", res.Pages)
	}
	e.checkMirror("a", "b")
}

// TestPageBoundaryKeepsRevision: Eine Revision, die größer ist als die
// Seitengröße, kommt ganz und steht danach ganz in der Replica.
func TestPageBoundaryKeepsRevision(t *testing.T) {
	e := newEnv(t, "a")
	e.sync.PageSize = 2
	e.hub.put("a", "vorher.md", "v")
	e.hub.write(func(rev int64) {
		for i := range 5 {
			e.hub.newDoc(rev, "a", string(rune('a'+i))+".md", "x")
		}
	})
	e.hub.put("a", "nachher.md", "n")
	res := e.ok()
	if res.Pages != 3 {
		t.Errorf("Seiten = %d, erwartet 3", res.Pages)
	}
	e.checkMirror("a")
}

func TestHubIDChange(t *testing.T) {
	e := newEnv(t, "a")
	ctx := context.Background()
	e.hub.put("a", "alt.md", "alt")
	e.hub.put("a", "alt2.md", "alt")
	e.ok()
	old := e.hub.id

	// Ein anderer Hub unter demselben Eintrag, mit kleinerer Revision.
	token := e.hub.token
	e.hub = newFakeHub(token, "a", "b")
	e.hub.put("a", "neu.md", "neu")
	res := e.ok()
	if !strings.Contains(res.Reset, "hub_id gewechselt") || !strings.Contains(res.Reset, old) {
		t.Errorf("Reset = %q", res.Reset)
	}
	if got := e.names("a"); !reflect.DeepEqual(got, map[string]string{"neu.md": "neu"}) {
		t.Errorf("a = %v", got)
	}
	e.checkMirror("a")
	if got := e.replica().HubID(); got != e.hub.id {
		t.Errorf("db_info.hub_id = %q", got)
	}
	if h, _ := e.nodes.Hub(ctx, "privat"); h.HubID != e.hub.id {
		t.Errorf("hubs.hub_id = %q", h.HubID)
	}
	if got := e.states(); got["a"] != e.hub.rev {
		t.Errorf("Stände = %v", got)
	}
	// Der nächste Abgleich läuft ohne Reset.
	if res := e.ok(); res.Reset != "" {
		t.Errorf("Reset beim nächsten Abgleich: %q", res.Reset)
	}
}

// TestHubIDCopyFollowsReplica: Maßgeblich ist db_info der Replica; eine
// abweichende Kopie in node.db (etwa aus config import) wird überschrieben,
// ohne die Replica zu leeren.
func TestHubIDCopyFollowsReplica(t *testing.T) {
	e := newEnv(t, "a")
	ctx := context.Background()
	e.hub.put("a", "x.md", "x")
	e.ok()
	h, err := e.nodes.Hub(ctx, "privat")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.nodes.SetHubID(ctx, "privat", h.EntryID, ulid.Make().String()); err != nil {
		t.Fatal(err)
	}
	if res := e.ok(); res.Reset != "" {
		t.Errorf("Reset = %q", res.Reset)
	}
	if h, _ := e.nodes.Hub(ctx, "privat"); h.HubID != e.hub.id {
		t.Errorf("hubs.hub_id = %q", h.HubID)
	}
	e.checkMirror("a")
}

func TestRevokeAndUnwantRemove(t *testing.T) {
	e := newEnv(t, "a", "b")
	ctx := context.Background()
	e.hub.put("a", "1.md", "1")
	e.hub.put("a", "2.md", "2")
	e.hub.put("b", "3.md", "3")
	e.ok()

	delete(e.hub.allowed, "b")
	e.unwant("a")
	res := e.ok()
	if r := result(res, "a"); r.Status != NotWanted || r.Removed != 2 {
		t.Errorf("a = %+v", r)
	}
	if r := result(res, "b"); r.Status != NotAllowed || r.Removed != 1 {
		t.Errorf("b = %+v", r)
	}
	if len(res.Collections) != 2 {
		t.Errorf("Collections = %+v", res.Collections)
	}
	if n := len(e.allRows()); n != 0 {
		t.Errorf("%d Zeilen übrig", n)
	}
	if got := e.states(); len(got) != 0 {
		t.Errorf("Stände übrig: %v", got)
	}
	for _, c := range []string{"a", "b"} {
		if _, err := e.replica().Documents(ctx, c, ""); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: %v", c, err)
		}
	}
	// Beim nächsten Mal ist a nicht mehr zu melden; b bleibt nicht erlaubt.
	res = e.ok()
	if len(res.Collections) != 1 || result(res, "b").Status != NotAllowed || result(res, "b").Removed != 0 {
		t.Errorf("Collections = %+v", res.Collections)
	}
	// Wieder erlaubt: von vorn.
	e.hub.allowed["b"] = true
	e.ok()
	e.checkMirror("b")
}

// TestDifferentStatesOverPages: a steht schon auf dem Stand des Hubs, b ist
// neu und holt viele ältere Revisionen in mehreren Seiten nach. Deren until
// liegt unter dem Stand von a; a fällt nie zurück.
func TestDifferentStatesOverPages(t *testing.T) {
	e := newEnv(t, "a")
	e.sync.PageSize = 1
	for i := range 5 {
		e.hub.put("b", string(rune('p'+i))+".md", "b")
	}
	e.hub.put("a", "a1.md", "a")
	e.ok()
	top := e.hub.rev // 6
	if got := e.states(); got["a"] != top {
		t.Fatalf("Stände = %v", got)
	}

	e.want("b")
	e.hub.put("a", "a2.md", "a")
	calls := e.hub.calls
	res := e.ok()
	if res.Pages != 6 {
		t.Errorf("Seiten = %d, erwartet 6", res.Pages)
	}
	last := int64(0)
	for i, req := range e.hub.requests[calls:] {
		for _, s := range req.Collections {
			if s.Collection == "a" {
				if s.Since < top || s.Since < last {
					t.Errorf("Anfrage %d: a seit %d — zurückgefallen (vorher %d, Stand %d)", i, s.Since, last, top)
				}
				last = s.Since
			}
		}
	}
	if got := e.states(); !reflect.DeepEqual(got, map[string]int64{"a": e.hub.rev, "b": e.hub.rev}) {
		t.Errorf("Stände = %v", got)
	}
	e.checkMirror("a", "b")
}

// TestSinceAboveHubRevision: Der Hub wurde aus einer Sicherung mit gleicher
// hub_id zurückgespielt. Die Replica wird geleert und ganz von vorn
// abgeglichen — auch Collections, die für sich nicht über der Revision
// liegen.
func TestSinceAboveHubRevision(t *testing.T) {
	e := newEnv(t, "a", "b")
	e.hub.put("a", "1.md", "1")
	e.hub.put("b", "2.md", "2")
	e.hub.put("a", "3.md", "3")
	e.hub.put("a", "4.md", "4")
	e.ok()

	e.hub.restore(2)
	res := e.ok()
	if !strings.Contains(res.Reset, "über der Revision 2") {
		t.Errorf("Reset = %q", res.Reset)
	}
	e.checkMirror("a", "b")
	if got := e.states(); !reflect.DeepEqual(got, map[string]int64{"a": 2, "b": 2}) {
		t.Errorf("Stände = %v", got)
	}
	// Die Anfrage nach dem Leeren fragt ab 0.
	last := e.hub.requests[len(e.hub.requests)-1]
	for _, s := range last.Collections {
		if s.Since != 0 {
			t.Errorf("nach dem Leeren: %s seit %d", s.Collection, s.Since)
		}
	}
}

// TestRenameWithNameSwap: A heißt x und wird y, B wird neu als x angelegt,
// dann ändert sich A. B kommt vor der Umbenennung von A an — ohne
// eindeutigen Namensindex scheitert das nicht. Der echte Hub kann noch
// nicht umbenennen; nur gegen die Attrappe.
func TestRenameWithNameSwap(t *testing.T) {
	e := newEnv(t, "a")
	ctx := context.Background()
	idA := e.hub.put("a", "x", "A")
	e.ok()

	e.hub.write(func(rev int64) { e.hub.change(rev, idA, func(r *contract.Row) { r.Name = "y" }) })
	idB := e.hub.put("a", "x", "B")
	e.hub.write(func(rev int64) { e.hub.change(rev, idA, func(r *contract.Row) { r.Content = str("A2") }) })
	e.ok()

	e.checkMirror("a")
	r := e.replica()
	if d, err := r.Document(ctx, "a", "x"); err != nil || d.ID != idB {
		t.Errorf("x = %+v, %v", d, err)
	}
	if d, err := r.Document(ctx, "a", "y"); err != nil || d.ID != idA || *d.Content != "A2" {
		t.Errorf("y = %+v, %v", d, err)
	}
	if got := e.names("a"); !reflect.DeepEqual(got, map[string]string{"x": "B", "y": "A2"}) {
		t.Errorf("a = %v", got)
	}
}

// TestDuplicateLiveNameReadsNewest: Tragen zwei lebende Zeilen denselben
// Namen (etwa mitten in einer Umbenennung), liest die Replica die jüngste
// und listet den Namen einmal.
func TestDuplicateLiveNameReadsNewest(t *testing.T) {
	e := newEnv(t, "a")
	ctx := context.Background()
	e.hub.put("a", "x", "alt")
	e.ok()
	r := e.replica()
	newer := contract.Row{ID: ulid.Make().String(), Collection: "a", Name: "x", Content: str("neu"),
		Revision: 9, CreatedBy: "admin", UpdatedBy: "admin"}
	if _, err := r.apply(ctx, page{rows: []contract.Row{newer}}); err != nil {
		t.Fatal(err)
	}
	if d, err := r.Document(ctx, "a", "x"); err != nil || *d.Content != "neu" {
		t.Errorf("x = %+v, %v", d, err)
	}
	if got := e.names("a"); !reflect.DeepEqual(got, map[string]string{"x": "neu"}) {
		t.Errorf("a = %v", got)
	}
}

// TestHubErrorNoReplica: Ein Fehler des Hubs ist ein Fehler dieses Eintrags;
// vor der ersten Antwort entsteht keine Replica.
func TestHubErrorNoReplica(t *testing.T) {
	e := newEnv(t, "a")
	e.hub.token = "keph_anderes"
	res := e.run()
	if !errors.Is(res.Err, contract.ErrUnauthenticated) {
		t.Errorf("Err = %v", res.Err)
	}
	if _, err := os.Stat(e.nodes.ReplicaPath("privat")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Replica trotz Fehler: %v", err)
	}
	if h, _ := e.nodes.Hub(context.Background(), "privat"); h.HubID != "" {
		t.Errorf("hubs.hub_id = %q", h.HubID)
	}
}

// TestSyncAllConnectError: Scheitert die Verbindung zu einem Eintrag, laufen
// die übrigen weiter.
func TestSyncAllConnectError(t *testing.T) {
	e := newEnv(t, "a")
	ctx := context.Background()
	if err := e.nodes.AddHub(ctx, store.Hub{Name: "fern", NodeName: "laptop", Transport: store.TransportSSH,
		Address: "hub", Token: e.hub.token}, false); err != nil {
		t.Fatal(err)
	}
	e.hub.put("a", "x.md", "x")
	errSSH := errors.New("Transport ssh noch nicht unterstützt")
	res, err := e.sync.Sync(ctx, "", func(h store.Hub) (contract.Hub, error) {
		if h.Transport == store.TransportSSH {
			return nil, errSSH
		}
		return e.hub, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 || res[0].Hub != "fern" || !errors.Is(res[0].Err, errSSH) || res[1].Hub != "privat" || res[1].Err != nil {
		t.Fatalf("Ergebnisse = %+v", res)
	}
	e.checkMirror("a")
	if _, err := e.sync.Sync(ctx, "unbekannt", nil); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unbekannter Alias: %v", err)
	}
}

// TestStaleSchemaRecreated: Eine Replica mit anderer Schemafassung wird
// verworfen und neu angelegt — sie ist abgeleitet.
func TestStaleSchemaRecreated(t *testing.T) {
	e := newEnv(t, "a")
	ctx := context.Background()
	e.hub.put("a", "x.md", "x")
	e.ok()
	db, err := sqlitedb.Open(ctx, e.nodes.ReplicaPath("privat"))
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlitedb.SetInfo(ctx, db, sqlitedb.KeySchemaVersion, "99"); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	res := e.ok()
	if !strings.Contains(res.Reset, "Schemafassung 99") {
		t.Errorf("Reset = %q", res.Reset)
	}
	e.checkMirror("a")
}

// TestNoProgress: Meldet der Hub more, ohne voranzukommen, bricht der Node
// ab, statt ewig zu fragen.
func TestNoProgress(t *testing.T) {
	e := newEnv(t, "a")
	stuck := stuckHub{id: e.hub.id}
	res, err := e.sync.Sync(context.Background(), "privat", func(store.Hub) (contract.Hub, error) { return stuck, nil })
	if err != nil {
		t.Fatal(err)
	}
	if res[0].Err == nil || !strings.Contains(res[0].Err.Error(), "kommt aber nicht") {
		t.Errorf("Err = %v", res[0].Err)
	}
}

// stuckHub bettet contract.Hub ein, damit es die Schnittstelle erfüllt; nur
// Sync ist umgesetzt.
type stuckHub struct {
	contract.Hub
	id string
}

func (s stuckHub) Sync(_ context.Context, req contract.SyncRequest) (contract.SyncResponse, error) {
	resp := contract.SyncResponse{HubID: s.id, Version: contract.Version, HubRevision: 5, More: true}
	for _, c := range req.Collections {
		resp.Collections = append(resp.Collections, contract.CollectionStatus{Collection: c.Collection, Allowed: true})
	}
	return resp, nil
}

// TestUnreadableRecreated: Eine eindeutig unlesbare Replica — Müll-Bytes,
// eine leere Datei ohne db_info, eine ohne entry_id — verwirft der Abgleich
// und legt sie neu an; Reset nennt den Grund.
func TestUnreadableRecreated(t *testing.T) {
	cases := []struct {
		name  string
		spoil func(t *testing.T, path string)
	}{
		{"Müll", func(t *testing.T, path string) {
			if err := Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(strings.Repeat("kein SQLite ", 500)), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"leer", func(t *testing.T, path string) {
			if err := Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"ohne entry_id", func(t *testing.T, path string) {
			db, err := sqlitedb.Open(context.Background(), path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err := db.Exec(`DELETE FROM db_info WHERE key = ?`, KeyEntryID); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t, "a")
			e.hub.put("a", "x.md", "x")
			e.ok()
			path := e.nodes.ReplicaPath("privat")
			c.spoil(t, path)
			if _, err := Open(context.Background(), path); err == nil {
				t.Fatal("Replica lässt sich noch öffnen")
			}
			res := e.ok()
			if !strings.Contains(res.Reset, "die Replica war nicht lesbar") || !strings.HasSuffix(res.Reset, "; neu angelegt") {
				t.Errorf("Reset = %q", res.Reset)
			}
			e.checkMirror("a")
			if got := e.replica().EntryID(); got == "" {
				t.Error("neue Replica ohne entry_id")
			}
		})
	}
}

// TestUnreadableKept: Bei abgebrochenem ctx und bei einer Datei fremder Rolle
// verwirft der Abgleich nichts; die Datei bleibt, wie sie ist.
func TestUnreadableKept(t *testing.T) {
	e := newEnv(t, "a")
	ctx := context.Background()
	h, err := e.nodes.Hub(ctx, "privat")
	if err != nil {
		t.Fatal(err)
	}
	path := e.nodes.ReplicaPath("privat")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	junk := []byte(strings.Repeat("kein SQLite ", 500))
	if err := os.WriteFile(path, junk, 0o600); err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if r, _, err := openForSync(canceled, e.nodes, h); err == nil || r != nil {
		t.Errorf("abgebrochen: %v", err)
	}
	if b, err := os.ReadFile(path); err != nil || string(b) != string(junk) {
		t.Errorf("Datei nach Abbruch verändert: %v", err)
	}

	// Eine Datenbank fremder Rolle.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	db, err := sqlitedb.Create(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlitedb.CreateSchema(ctx, db, "", "node", 1, nil); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	res := e.run()
	var wr *sqlitedb.WrongRoleError
	if !errors.As(res.Err, &wr) || res.Reset != "" {
		t.Errorf("fremde Rolle: Err = %v, Reset = %q", res.Err, res.Reset)
	}
	db, err = sqlitedb.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := sqlitedb.CheckInfo(ctx, db, "node", 1); err != nil {
		t.Errorf("Datei fremder Rolle verändert: %v", err)
	}
}

// TestCreateReplacedBeforeOpen: Ersetzt ein anderer die Datei zwischen Link
// und Open durch die Replica eines anderen Eintrags, liefert Create
// ErrChanged und schreibt nichts hinein.
func TestCreateReplacedBeforeOpen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "replicas", "privat.db")
	hubID, mine, other := ulid.Make().String(), ulid.Make().String(), ulid.Make().String()
	afterLink = func(path string) {
		afterLink = nil
		if err := Remove(path); err != nil {
			t.Fatal(err)
		}
		r, err := Create(ctx, path, hubID, other)
		if err != nil {
			t.Fatal(err)
		}
		_ = r.Close()
	}
	t.Cleanup(func() { afterLink = nil })
	if r, err := Create(ctx, path, hubID, mine); !errors.Is(err, ErrChanged) || r != nil {
		t.Fatalf("Create: %v", err)
	}
	r, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if r.EntryID() != other || r.HubID() != hubID {
		t.Errorf("Replica gehört zu %s/%s", r.EntryID(), r.HubID())
	}
	var n int
	if err := r.db.QueryRow(`SELECT (SELECT COUNT(*) FROM documents) + (SELECT COUNT(*) FROM sync_state)`).Scan(&n); err != nil || n != 0 {
		t.Errorf("%d Zeilen geschrieben, %v", n, err)
	}
}

// TestSyncCreateReplaced: Im Abgleich setzt ErrChanged aus Create neu auf;
// der Eintrag gilt noch, also verwirft der nächste Versuch die fremde
// Replica und legt die eigene an.
func TestSyncCreateReplaced(t *testing.T) {
	e := newEnv(t, "a")
	e.hub.put("a", "x.md", "x")
	afterLink = func(path string) {
		afterLink = nil
		if err := Remove(path); err != nil {
			t.Fatal(err)
		}
		r, err := Create(context.Background(), path, e.hub.id, ulid.Make().String())
		if err != nil {
			t.Fatal(err)
		}
		_ = r.Close()
	}
	t.Cleanup(func() { afterLink = nil })
	res := e.ok()
	if !strings.Contains(res.Reset, "früheren Eintrag") {
		t.Errorf("Reset = %q", res.Reset)
	}
	h, err := e.nodes.Hub(context.Background(), "privat")
	if err != nil {
		t.Fatal(err)
	}
	if got := e.replica().EntryID(); got != h.EntryID {
		t.Errorf("Replica gehört zu %s, erwartet %s", got, h.EntryID)
	}
	e.checkMirror("a")
}

// Ein Zertifikatsfehler und eine Gegenseite ohne TLS zählen als nicht
// erreicht: Der Handshake scheiterte, bevor etwas abgeschickt war.
func TestHubKindTLS(t *testing.T) {
	for name, err := range map[string]error{
		"unbekannte CA":   &tls.CertificateVerificationError{Err: x509.UnknownAuthorityError{}},
		"falscher Name":   &tls.CertificateVerificationError{Err: x509.HostnameError{Host: "h"}},
		"abgelaufen":      &tls.CertificateVerificationError{Err: x509.CertificateInvalidError{Reason: x509.Expired}},
		"kein TLS":        tls.RecordHeaderError{Msg: "first record does not look like a TLS handshake"},
		"Klartextantwort": http.ErrSchemeMismatch,
	} {
		if kind := hubKind(fmt.Errorf("Hub https://h: %w", err)); kind != KindUnreachable {
			t.Errorf("%s: %q, erwartet unreachable", name, kind)
		}
	}
	if kind := hubKind(errors.New("irgendwas")); kind != KindHub {
		t.Errorf("sonstiges: %q", kind)
	}
}
