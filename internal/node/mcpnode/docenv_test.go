package mcpnode

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"sort"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/oklog/ulid/v2"

	"github.com/kephalaion/kephalaion/internal/config"
	"github.com/kephalaion/kephalaion/internal/contract"
	"github.com/kephalaion/kephalaion/internal/ident"
	"github.com/kephalaion/kephalaion/internal/node/replica"
	"github.com/kephalaion/kephalaion/internal/node/store"
	"github.com/kephalaion/kephalaion/internal/upgrade"
)

// docHub ist eine Attrappe von contract.Hub für die Werkzeuge, die lesen:
// Zeilen im Speicher, eine Revision je Schreibvorgang, eine Seite je Abgleich.
// Die Zeit des Hubs (ms) setzt der Test über clock.
type docHub struct {
	id    string
	rev   int64
	clock int64
	docs  map[string]contract.Row
	// allowed sind die Collections, die der Node abgleichen darf.
	allowed map[string]bool
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
	name := contract.AccountRowName(account)
	for id, r := range f.docs {
		if r.Collection == coll && r.Name == name {
			f.change(id, func(r *contract.Row) { setAccount(r, account, tok, rights) })
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

func (f *docHub) Create(context.Context, contract.CreateRequest) (contract.WriteResponse, error) {
	return contract.WriteResponse{}, errors.New("create: in der Attrappe nicht umgesetzt")
}

func (f *docHub) Write(context.Context, contract.WriteRequest) (contract.WriteResponse, error) {
	return contract.WriteResponse{}, errors.New("write: in der Attrappe nicht umgesetzt")
}

func (f *docHub) Delete(context.Context, contract.DeleteRequest) (contract.WriteResponse, error) {
	return contract.WriteResponse{}, errors.New("delete: in der Attrappe nicht umgesetzt")
}

// docEnv ist ein Node mit Hubs aus Attrappen: keph mit wissen und privat,
// team mit notizen. anna liest alles und darf in keph:wissen schreiben, otto
// liest nur keph:wissen.
type docEnv struct {
	nodes  store.Store
	url    string
	hubs   map[string]*docHub
	tokens map[string]string
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
	srv := httptest.NewServer(NewHandler(nodes, "test", func() upgrade.Report { return testUpdate }))
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
