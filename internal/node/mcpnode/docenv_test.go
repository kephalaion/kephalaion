package mcpnode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/oklog/ulid/v2"

	"github.com/kephalaion/kephalaion/internal/config"
	"github.com/kephalaion/kephalaion/internal/contract"
	"github.com/kephalaion/kephalaion/internal/ident"
	"github.com/kephalaion/kephalaion/internal/node/replica"
	"github.com/kephalaion/kephalaion/internal/node/store"
	"github.com/kephalaion/kephalaion/internal/reqlog"
	"github.com/kephalaion/kephalaion/internal/upgrade"
)

// docHub ist eine Attrappe von contract.Hub für die Werkzeuge des Nodes:
// Zeilen im Speicher, eine Revision je Schreibvorgang, eine Seite je Abgleich,
// dazu create, write, delete und rename mit Anmeldung, Recht und
// Vorbedingung wie am Hub, auch für Verzeichnisse. Die Zeit des Hubs (ms) setzt der Test über clock. Die Werkzeuge rufen
// sie aus dem Handler, der Test aus seiner Goroutine: mu schützt Zeilen und
// Zähler.
type docHub struct {
	mu    sync.Mutex
	id    string
	rev   int64
	clock int64
	docs  map[string]contract.Row
	// allowed sind die Collections, die der Node abgleichen darf.
	allowed map[string]bool
	// writes zählt die Aufrufe von create, write, delete und rename.
	writes int
	// fail lässt jeden Schreibvorgang mit diesem Fehler scheitern; mit
	// failAfter erst, nachdem er geschrieben hat (Antwort verloren).
	fail      error
	failAfter bool
	// answerID ist, wenn gesetzt, die hub_id in der Antwort eines
	// Schreibvorgangs — wie ein Hub, der inzwischen ein anderer ist.
	answerID string
}

func newDocHub(allowed ...string) *docHub {
	f := &docHub{id: ulid.Make().String(), clock: 1_700_000_000_000, docs: map[string]contract.Row{},
		allowed: map[string]bool{}}
	for _, c := range allowed {
		f.allowed[c] = true
	}
	return f
}

// write ist ein Schreibvorgang: eine Revision, die Zeit rückt eine Sekunde vor.
func (f *docHub) write(fn func(rev, at int64)) int64 {
	f.rev++
	f.clock += 1000
	fn(f.rev, f.clock)
	return f.rev
}

// put legt ein Dokument an oder ersetzt das lebende gleichen Namens; es
// liefert die id.
func (f *docHub) put(coll, name, content string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var id string
	f.write(func(rev, at int64) {
		for _, r := range f.docs {
			if r.Collection == coll && r.Name == name && !r.Deleted {
				id = r.ID
			}
		}
		r, ok := f.docs[id]
		if !ok {
			id = ulid.Make().String()
			r = contract.Row{ID: id, Collection: coll, Name: name, CreatedAt: at, CreatedBy: "kleist"}
		}
		r.Content, r.Revision, r.UpdatedAt, r.UpdatedBy = &content, rev, at, "kleist"
		f.docs[id] = r
	})
	return id
}

// change ändert eine Zeile per id in einem eigenen Schreibvorgang.
func (f *docHub) change(id string, fn func(r *contract.Row)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.changeRow(id, fn)
}

// changeRow ist change ohne Sperre.
func (f *docHub) changeRow(id string, fn func(r *contract.Row)) {
	f.write(func(rev, at int64) {
		r := f.docs[id]
		fn(&r)
		r.Revision, r.UpdatedAt = rev, at
		f.docs[id] = r
	})
}

func (f *docHub) rm(id string) {
	f.change(id, func(r *contract.Row) { r.Deleted, r.Content = true, nil })
}

// grant schreibt die Zeile eines Accounts in einer Collection; ohne tok wird
// sie zur Löschmarke (revoke).
func (f *docHub) grant(account, coll, tok string, rights contract.Rights) {
	f.mu.Lock()
	defer f.mu.Unlock()
	name := contract.AccountRowName(account)
	for id, r := range f.docs {
		if r.Collection == coll && r.Name == name {
			f.changeRow(id, func(r *contract.Row) { setAccount(r, account, tok, rights) })
			return
		}
	}
	f.write(func(rev, at int64) {
		r := contract.Row{ID: ulid.Make().String(), Collection: coll, Name: name, Revision: rev, CreatedAt: at,
			CreatedBy: "admin", UpdatedAt: at, UpdatedBy: "admin"}
		setAccount(&r, account, tok, rights)
		f.docs[r.ID] = r
	})
}

func setAccount(r *contract.Row, account, tok string, rights contract.Rights) {
	if tok == "" {
		r.Deleted, r.Content = true, nil
		return
	}
	c, err := contract.EncodeAccountContent(contract.AccountContent{Hash: ident.HashToken(tok), User: account,
		Rights: rights})
	if err != nil {
		panic(err)
	}
	r.Deleted, r.Content = false, &c
}

func (f *docHub) Sync(_ context.Context, req contract.SyncRequest) (contract.SyncResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	resp := contract.SyncResponse{HubID: f.id, Version: contract.Version, HubRevision: f.rev, Until: f.rev,
		Collections: []contract.CollectionStatus{}, Allowed: []string{}, Rows: []contract.Row{}}
	for c := range f.allowed {
		resp.Allowed = append(resp.Allowed, c)
	}
	sort.Strings(resp.Allowed)
	since := map[string]int64{}
	for _, s := range req.Collections {
		resp.Collections = append(resp.Collections, contract.CollectionStatus{Collection: s.Collection,
			Allowed: f.allowed[s.Collection]})
		if f.allowed[s.Collection] {
			since[s.Collection] = s.Since
		}
	}
	for _, r := range f.docs {
		if s, ok := since[r.Collection]; ok && r.Revision > s {
			resp.Rows = append(resp.Rows, r)
		}
	}
	slices.SortFunc(resp.Rows, func(a, b contract.Row) int {
		if a.Revision != b.Revision {
			return int(a.Revision - b.Revision)
		}
		if a.ID < b.ID {
			return -1
		}
		return 1
	})
	return resp, nil
}

func (f *docHub) Whoami(context.Context, contract.WhoamiRequest) (contract.WhoamiResponse, error) {
	return contract.WhoamiResponse{}, errors.New("whoami: in der Attrappe nicht umgesetzt")
}

func (f *docHub) Rotate(context.Context, contract.RotateRequest) (contract.RotateResponse, error) {
	return contract.RotateResponse{}, errors.New("rotate: in der Attrappe nicht umgesetzt")
}

// writeCalls liefert, wie oft create, write oder delete ankam.
func (f *docHub) writeCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.writes
}

// failWith lässt die folgenden Schreibvorgänge mit err scheitern; after:
// erst nachdem sie geschrieben haben.
func (f *docHub) failWith(err error, after bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail, f.failAfter = err, after
}

// live liefert das lebende Dokument eines Namens.
func (f *docHub) live(coll, name string) (contract.Row, bool) {
	for _, r := range f.docs {
		if r.Collection == coll && r.Name == name && !r.Deleted {
			return r, true
		}
	}
	return contract.Row{}, false
}

// account prüft einen Account wie der Hub: Ohne Zeile mit passendem Hash ist
// er nicht angemeldet; ohne lebende Zeile in der Collection, oder wenn der
// Node sie nicht abgleichen darf, ist sie nicht lesbar.
func (f *docHub) account(acc contract.AccountAuth, coll string) (contract.AccountContent, error) {
	known := false
	var out *contract.AccountContent
	for _, r := range f.docs {
		if r.Name != contract.AccountRowName(acc.Account) || r.Deleted || r.Content == nil {
			continue
		}
		c, err := contract.DecodeAccountContent(*r.Content)
		if err != nil || c.Hash != ident.HashToken(acc.Token) {
			continue
		}
		known = true
		if r.Collection == coll {
			out = &c
		}
	}
	switch {
	case !known:
		return contract.AccountContent{}, contract.ErrAccountUnauthenticated
	case out == nil || !f.allowed[coll]:
		return contract.AccountContent{}, &contract.Error{Code: contract.CodeNotReadable,
			Message: "Collection " + coll + " ist nicht lesbar"}
	}
	return *out, nil
}

// may prüft das Recht an einem lebenden Dokument: write für Eigenes,
// supersede für Fremdes.
func may(c contract.AccountContent, doc contract.Row) error {
	if doc.CreatedBy == c.User && !c.Rights.Write {
		return &contract.Error{Code: contract.CodeForbidden, Message: fmt.Sprintf("Dokument %s: write fehlt", doc.Name)}
	}
	if doc.CreatedBy != c.User && !c.Rights.Supersede {
		return &contract.Error{Code: contract.CodeForbidden,
			Message: fmt.Sprintf("Dokument %s: gehört %s, supersede fehlt", doc.Name, doc.CreatedBy)}
	}
	return nil
}

func checkBase(doc contract.Row, base *int64) error {
	if base != nil && *base != doc.Revision {
		return &contract.Error{Code: contract.CodeStaleRevision,
			Message: fmt.Sprintf("Dokument %s hat Revision %d, der Vorgang beruht auf %d", doc.Name, doc.Revision, *base)}
	}
	return nil
}

// under liefert die lebenden Dokumente unter dem Verzeichnis name, nach Name.
func (f *docHub) under(coll, name string) []contract.Row {
	var out []contract.Row
	for _, r := range f.docs {
		if r.Collection == coll && !r.Deleted && strings.HasPrefix(r.Name, name+"/") {
			out = append(out, r)
		}
	}
	slices.SortFunc(out, func(a, b contract.Row) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// pathConflict sagt, ob name zugleich Datei und Verzeichnis wäre: ein
// lebendes Dokument darüber oder darunter.
func (f *docHub) pathConflict(coll, name string) bool {
	for _, r := range f.docs {
		if r.Collection == coll && !r.Deleted && (strings.HasPrefix(name, r.Name+"/") ||
			strings.HasPrefix(r.Name, name+"/")) {
			return true
		}
	}
	return false
}

func conflict() error {
	return &contract.Error{Code: contract.CodePathConflict, Message: "Name wäre zugleich Datei und Verzeichnis"}
}

func notFound(name string) error {
	return &contract.Error{Code: contract.CodeNotFound, Message: name + " gibt es weder als Dokument noch als Verzeichnis"}
}

// do führt einen Schreibvorgang aus: fn prüft und schreibt und liefert die
// Zeilen; fail und answerID wirken danach.
func (f *docHub) do(acc contract.AccountAuth, coll, name string, fn func(c contract.AccountContent) ([]contract.Row, error)) (contract.WriteResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes++
	if f.fail != nil && !f.failAfter {
		return contract.WriteResponse{}, f.fail
	}
	if err := ident.CheckDocName(name); err != nil {
		return contract.WriteResponse{}, contract.Invalid(err.Error())
	}
	c, err := f.account(acc, coll)
	if err != nil {
		return contract.WriteResponse{}, err
	}
	rows, err := fn(c)
	if err != nil {
		return contract.WriteResponse{}, err
	}
	if f.fail != nil {
		return contract.WriteResponse{}, f.fail
	}
	id := f.id
	if f.answerID != "" {
		id = f.answerID
	}
	return contract.WriteResponse{HubID: id, Version: contract.Version, Revision: rows[0].Revision, Rows: rows}, nil
}

func (f *docHub) Create(_ context.Context, req contract.CreateRequest) (contract.WriteResponse, error) {
	return f.do(req.Account, req.Collection, req.Name, func(c contract.AccountContent) ([]contract.Row, error) {
		if !c.Rights.Write {
			return nil, &contract.Error{Code: contract.CodeForbidden, Message: "anlegen: write fehlt"}
		}
		if len(req.Content) > contract.MaxDocumentBytes {
			return nil, contract.Invalid("Inhalt zu groß")
		}
		if _, ok := f.live(req.Collection, req.Name); ok {
			return nil, &contract.Error{Code: contract.CodeNameTaken, Message: "Dokument " + req.Name + " gibt es schon"}
		}
		if f.pathConflict(req.Collection, req.Name) {
			return nil, conflict()
		}
		var row contract.Row
		f.write(func(rev, at int64) {
			content := req.Content
			row = contract.Row{ID: ulid.Make().String(), Collection: req.Collection, Name: req.Name, Content: &content,
				Revision: rev, CreatedAt: at, CreatedBy: c.User, UpdatedAt: at, UpdatedBy: c.User}
			f.docs[row.ID] = row
		})
		return []contract.Row{row}, nil
	})
}

func (f *docHub) Write(_ context.Context, req contract.WriteRequest) (contract.WriteResponse, error) {
	return f.do(req.Account, req.Collection, req.Name, func(c contract.AccountContent) ([]contract.Row, error) {
		doc, ok := f.live(req.Collection, req.Name)
		if !ok {
			return nil, &contract.Error{Code: contract.CodeNotFound, Message: "Dokument " + req.Name + " gibt es nicht"}
		}
		if err := may(c, doc); err != nil {
			return nil, err
		}
		if err := checkBase(doc, req.BaseRevision); err != nil {
			return nil, err
		}
		if *doc.Content == req.Content {
			return []contract.Row{doc}, nil
		}
		f.write(func(rev, at int64) {
			content := req.Content
			doc.Content, doc.Revision, doc.UpdatedAt, doc.UpdatedBy = &content, rev, at, c.User
			f.docs[doc.ID] = doc
		})
		return []contract.Row{doc}, nil
	})
}

// source liefert, was name bezeichnet, wie der Hub: ein Dokument oder die
// Dokumente unter einem Verzeichnis. Ein Verzeichnis nimmt keine Vorbedingung;
// das Recht gilt je Dokument.
func (f *docHub) source(c contract.AccountContent, coll, name string, base *int64) (docs []contract.Row, dir bool, err error) {
	if doc, ok := f.live(coll, name); ok {
		if err := may(c, doc); err != nil {
			return nil, false, err
		}
		return []contract.Row{doc}, false, checkBase(doc, base)
	}
	docs = f.under(coll, name)
	if len(docs) == 0 {
		return nil, false, notFound(name)
	}
	if base != nil {
		return nil, true, contract.Invalid(name + " ist ein Verzeichnis; base_revision gilt nur für Dokumente")
	}
	for _, d := range docs {
		if err := may(c, d); err != nil {
			return nil, true, err
		}
	}
	return docs, true, nil
}

func (f *docHub) Delete(_ context.Context, req contract.DeleteRequest) (contract.WriteResponse, error) {
	return f.do(req.Account, req.Collection, req.Name, func(c contract.AccountContent) ([]contract.Row, error) {
		if !req.Recursive {
			if _, ok := f.live(req.Collection, req.Name); !ok && len(f.under(req.Collection, req.Name)) > 0 {
				return nil, contract.Invalid(req.Name + " ist ein Verzeichnis; löschen nur mit recursive")
			}
		}
		docs, _, err := f.source(c, req.Collection, req.Name, req.BaseRevision)
		if err != nil {
			return nil, err
		}
		f.write(func(rev, at int64) {
			for i := range docs {
				docs[i].Content, docs[i].Deleted, docs[i].Revision, docs[i].UpdatedAt, docs[i].UpdatedBy = nil, true, rev, at, c.User
				f.docs[docs[i].ID] = docs[i]
			}
		})
		return docs, nil
	})
}

func (f *docHub) Rename(_ context.Context, req contract.RenameRequest) (contract.WriteResponse, error) {
	if err := ident.CheckRename(req.Name, req.NewName); err != nil {
		return contract.WriteResponse{}, contract.Invalid(err.Error())
	}
	return f.do(req.Account, req.Collection, req.Name, func(c contract.AccountContent) ([]contract.Row, error) {
		docs, dir, err := f.source(c, req.Collection, req.Name, req.BaseRevision)
		if err != nil {
			return nil, err
		}
		_, taken := f.live(req.Collection, req.NewName)
		switch {
		case !dir && taken:
			return nil, &contract.Error{Code: contract.CodeNameTaken, Message: "Dokument " + req.NewName + " gibt es schon"}
		case dir && !taken && len(f.under(req.Collection, req.NewName)) > 0:
			return nil, &contract.Error{Code: contract.CodeNameTaken, Message: "Verzeichnis " + req.NewName + " gibt es schon"}
		case taken || f.pathConflict(req.Collection, req.NewName):
			return nil, conflict()
		}
		f.write(func(rev, at int64) {
			for i := range docs {
				docs[i].Name, _ = ident.DocRenamed(docs[i].Name, req.Name, req.NewName)
				docs[i].Revision, docs[i].UpdatedAt, docs[i].UpdatedBy = rev, at, c.User
				f.docs[docs[i].ID] = docs[i]
			}
		})
		return docs, nil
	})
}

// docEnv ist ein Node mit Hubs aus Attrappen: keph mit wissen und privat,
// team mit notizen. anna liest alles und darf in keph:wissen schreiben, otto
// liest nur keph:wissen.
type docEnv struct {
	nodes  store.Store
	url    string
	hubs   map[string]*docHub
	tokens map[string]string
	// connect ist der Weg zum Hub der Werkzeuge, die schreiben; ohne ihn
	// die Attrappe des Hubs.
	connect func(h store.Hub) (contract.Hub, error)
	// log ist das Log der Anfragen an den Node.
	log *lockedBuffer

	mu sync.Mutex
	// kicks sind die angestoßenen Abgleiche, nach Alias.
	kicks []string
}

// lockedBuffer ist ein Puffer für das Log, sicher über Goroutinen.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// link ist der Weg zum Hub, wie cmd/kephalaion ihn gibt: die Attrappe (oder
// connect) und ein Anstoß, der sich merkt, welcher Hub abgleichen soll.
func (e *docEnv) link() HubLink {
	return HubLink{
		Connect: func(_ context.Context, h store.Hub) (contract.Hub, func(), error) {
			if e.connect != nil {
				hub, err := e.connect(h)
				return hub, func() {}, err
			}
			return e.hubs[h.Name], func() {}, nil
		},
		Sync: func(h store.Hub) {
			e.mu.Lock()
			defer e.mu.Unlock()
			e.kicks = append(e.kicks, h.Name)
		},
	}
}

// takeKicks liefert die angestoßenen Abgleiche seit dem letzten Aufruf.
func (e *docEnv) takeKicks() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := e.kicks
	e.kicks = nil
	return out
}

func newDocEnv(t *testing.T) *docEnv {
	t.Helper()
	ctx := context.Background()
	nodes, err := store.Create(ctx, config.SQLiteDB(filepath.Join(t.TempDir(), "node.db")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = nodes.Close() })
	e := &docEnv{nodes: nodes, hubs: map[string]*docHub{"keph": newDocHub("wissen", "privat"), "team": newDocHub("notizen")},
		tokens: map[string]string{"keph/anna": token(t), "keph/otto": token(t), "team/anna": token(t)}}
	for alias, f := range e.hubs {
		if err := nodes.AddHub(ctx, store.Hub{Name: alias, NodeName: "laptop", Transport: store.TransportHTTPS,
			Address: "https://hub.example.org", Token: token(t)}, false); err != nil {
			t.Fatal(err)
		}
		for _, c := range slices.Sorted(maps.Keys(f.allowed)) {
			if err := nodes.AddCollection(ctx, alias, c); err != nil {
				t.Fatal(err)
			}
		}
	}
	e.hubs["keph"].grant("anna", "wissen", e.tokens["keph/anna"], contract.Rights{Write: true})
	e.hubs["keph"].grant("anna", "privat", e.tokens["keph/anna"], contract.Rights{})
	e.hubs["keph"].grant("otto", "wissen", e.tokens["keph/otto"], contract.Rights{})
	e.hubs["team"].grant("anna", "notizen", e.tokens["team/anna"], contract.Rights{})
	e.sync(t)
	e.log = &lockedBuffer{}
	srv := httptest.NewServer(reqlog.New(e.log).Middleware("node",
		NewHandler(nodes, "test", func() upgrade.Report { return testUpdate }, e.link())))
	t.Cleanup(srv.Close)
	e.url = srv.URL
	return e
}

// sync gleicht alle Hubs ab.
func (e *docEnv) sync(t *testing.T) {
	t.Helper()
	s := &replica.Syncer{Nodes: e.nodes}
	res, err := s.Sync(context.Background(), "", func(h store.Hub) (contract.Hub, error) { return e.hubs[h.Name], nil })
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res {
		if r.Err != nil {
			t.Fatalf("Abgleich %s: %v", r.Hub, r.Err)
		}
	}
}

// anna sind die Header von anna an beiden Hubs, otto die von otto an keph.
func (e *docEnv) anna() http.Header {
	return merge(pair("keph", "anna", e.tokens["keph/anna"]), pair("team", "anna", e.tokens["team/anna"]))
}

func (e *docEnv) otto() http.Header { return pair("keph", "otto", e.tokens["keph/otto"]) }

// call ruft ein Werkzeug über den MCP-Client des SDK und liefert das
// Ergebnis; out nimmt die strukturierte Antwort auf. Ein Fehler des
// Werkzeugs steht in errText.
func (e *docEnv) call(t *testing.T, header http.Header, tool string, args any, out any) (res *mcp.CallToolResult, errText string) {
	t.Helper()
	ctx := context.Background()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: e.url + Path,
		HTTPClient: &http.Client{Transport: headerTransport{header}}, DisableStandaloneSSE: true, MaxRetries: -1}, nil)
	if err != nil {
		t.Fatalf("initialize: %v", err)
	}
	defer session.Close()
	res, err = session.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		return res, textOf(res)
	}
	if out != nil {
		b, _ := json.Marshal(res.StructuredContent)
		if err := json.Unmarshal(b, out); err != nil {
			t.Fatal(err)
		}
	}
	return res, ""
}

func textOf(res *mcp.CallToolResult) string {
	s := ""
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			s += tc.Text
		}
	}
	return s
}
