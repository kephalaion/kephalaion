package mcpnode

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/oklog/ulid/v2"

	"github.com/kephalaion/kephalaion/internal/config"
	"github.com/kephalaion/kephalaion/internal/contract"
	"github.com/kephalaion/kephalaion/internal/ident"
	"github.com/kephalaion/kephalaion/internal/node/replica"
	"github.com/kephalaion/kephalaion/internal/node/store"
	"github.com/kephalaion/kephalaion/internal/sqlitedb"
	"github.com/kephalaion/kephalaion/internal/upgrade"
)

// env ist ein Node mit zwei Hub-Einträgen und ihren Replicas: keph mit
// alice (read team-x) und bob (write team-x, read privat), team.x_y mit bob
// (supersede notizen), jeweils mit eigenem Token.
type env struct {
	nodes  store.Store
	url    string
	tokens map[string]string
}

func token(t *testing.T) string {
	t.Helper()
	tok, err := ident.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

// userOf ist der User eines Accounts im Test: bob an keph gehört kleist,
// sonst ist der User der Name des Accounts.
func userOf(alias, account string) string {
	if alias == "keph" && account == "bob" {
		return "kleist"
	}
	return account
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	nodes, err := store.Create(ctx, config.SQLiteDB(filepath.Join(t.TempDir(), "node.db")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = nodes.Close() })
	e := &env{nodes: nodes, tokens: map[string]string{
		"keph/alice": token(t), "keph/bob": token(t), "team.x_y/bob": token(t)}}
	hubs := map[string][]struct {
		account, collection string
		rights              contract.Rights
	}{
		"keph":     {{"alice", "team-x", contract.Rights{}}, {"bob", "team-x", contract.Rights{Write: true}}, {"bob", "privat", contract.Rights{}}},
		"team.x_y": {{"bob", "notizen", contract.Rights{Supersede: true}}},
	}
	for alias, grants := range hubs {
		if err := nodes.AddHub(ctx, store.Hub{Name: alias, NodeName: "laptop", Transport: store.TransportHTTPS,
			Address: "https://hub.example.org", Token: token(t)}, false); err != nil {
			t.Fatal(err)
		}
		var rows []contract.Row
		for i, g := range grants {
			if err := nodes.AddCollection(ctx, alias, g.collection); err != nil && !strings.Contains(err.Error(), "gibt es schon") {
				t.Fatal(err)
			}
			content, err := contract.EncodeAccountContent(contract.AccountContent{
				Hash: ident.HashToken(e.tokens[alias+"/"+g.account]), User: userOf(alias, g.account), Rights: g.rights})
			if err != nil {
				t.Fatal(err)
			}
			rows = append(rows, contract.Row{ID: ulid.Make().String(), Collection: g.collection,
				Name: contract.AccountRowName(g.account), Content: &content, Revision: int64(i + 1),
				CreatedBy: "admin", UpdatedBy: "admin"})
		}
		h, err := nodes.Hub(ctx, alias)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := replica.WriteAccountRows(ctx, nodes, h, ulid.Make().String(), rows); err != nil {
			t.Fatal(err)
		}
	}
	srv := httptest.NewServer(NewHandler(nodes, "test", func() upgrade.Report { return testUpdate }, HubLink{}))
	t.Cleanup(srv.Close)
	e.url = srv.URL
	return e
}

// headerTransport setzt die Header, die ein Client aus seiner
// MCP-Konfiguration schickt.
type headerTransport struct {
	header http.Header
}

func (h headerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	for k, v := range h.header {
		r.Header[k] = v
	}
	return http.DefaultTransport.RoundTrip(r)
}

// whoami verbindet sich mit dem MCP-Client des SDK, ruft whoami und liefert
// die strukturierte Antwort und den rohen JSON-Text.
func (e *env) whoami(t *testing.T, header http.Header) (WhoamiOutput, string) {
	t.Helper()
	ctx := context.Background()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: e.url + Path,
		HTTPClient: &http.Client{Transport: headerTransport{header}}, DisableStandaloneSSE: true, MaxRetries: -1}, nil)
	if err != nil {
		t.Fatalf("initialize: %v", err)
	}
	defer session.Close()
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, tool := range tools.Tools {
		got = append(got, tool.Name)
	}
	if !reflect.DeepEqual(got, wantTools) {
		t.Fatalf("Werkzeuge: %v", got)
	}
	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "whoami"})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("whoami: Fehler %+v", res.Content)
	}
	raw, _ := json.Marshal(res)
	var out WhoamiOutput
	b, _ := json.Marshal(res.StructuredContent)
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out, string(raw)
}

// wantTools sind die Werkzeuge des Nodes, nach Name.
var wantTools = []string{"changes", "create", "delete", "list", "read", "rename", "whoami", "write"}

func pair(alias, account, tok string) http.Header {
	h := http.Header{}
	h.Set("X-Keph-Account-"+alias, account)
	h.Set("X-Keph-Token-"+alias, tok)
	return h
}

func merge(hs ...http.Header) http.Header {
	out := http.Header{}
	for _, h := range hs {
		for k, v := range h {
			out[k] = v
		}
	}
	return out
}

// hub liefert den Eintrag alias aus der Antwort.
func hubOf(t *testing.T, out WhoamiOutput, alias string) HubInfo {
	t.Helper()
	for _, h := range out.Hubs {
		if h.Hub == alias {
			return h
		}
	}
	t.Fatalf("Hub %s fehlt: %+v", alias, out.Hubs)
	return HubInfo{}
}

func TestWhoamiWithoutHeaders(t *testing.T) {
	e := newEnv(t)
	out, raw := e.whoami(t, nil)
	zero := int64(0)
	want := WhoamiOutput{Version: "test", Update: &testUpdate, UnknownHubs: []string{}, Hubs: []HubInfo{
		{Hub: "keph", Login: LoginMissing, Node: "laptop", Sync: SyncInfo{Revision: &zero}},
		{Hub: "team.x_y", Login: LoginMissing, Node: "laptop", Sync: SyncInfo{Revision: &zero}},
	}}
	if !reflect.DeepEqual(out, want) {
		t.Errorf("ohne Header: %+v", out)
	}
	if !strings.Contains(raw, `kephalaion test\nUpdate: v0.2.0 verfügbar`) ||
		!strings.Contains(raw, "keph (Node laptop): keine Zugangsdaten; Revision 0") {
		t.Errorf("Text:\n%s", raw)
	}
}

func TestWhoami(t *testing.T) {
	e := newEnv(t)
	bob := e.tokens["keph/bob"]
	out, raw := e.whoami(t, pair("keph", "bob", bob))
	want := HubInfo{Hub: "keph", Login: LoginOK, Node: "laptop", Sync: hubOf(t, out, "keph").Sync, Account: "bob",
		User: "kleist", Collections: []CollectionRights{
			{Collection: "privat", Address: "keph:privat", Rights: []string{"read"}, Dirs: []string{}},
			{Collection: "team-x", Address: "keph:team-x", Rights: []string{"read", "write"}, Dirs: []string{}},
		}}
	if got := hubOf(t, out, "keph"); !reflect.DeepEqual(got, want) {
		t.Errorf("bob: %+v", got)
	}
	if got := hubOf(t, out, "team.x_y"); got.Login != LoginMissing || got.Account != "" {
		t.Errorf("anderer Hub ohne Header: %+v", got)
	}
	if !strings.Contains(raw, "keph (Node laptop): angemeldet als bob (User kleist): keph:privat (read), keph:team-x (read, write)") {
		t.Errorf("Text ohne User:\n%s", raw)
	}
	e.noSecrets(t, raw, bob)

	// Falsches Token und unbekannter Account: dieselbe Antwort, invalid.
	wrong, rawWrong := e.whoami(t, pair("keph", "bob", e.tokens["keph/alice"]))
	unknown, rawUnknown := e.whoami(t, pair("keph", "dave", bob))
	if hubOf(t, wrong, "keph").Login != LoginInvalid || !reflect.DeepEqual(wrong, unknown) || rawWrong != rawUnknown {
		t.Errorf("falsch %+v, unbekannt %+v", wrong.Hubs, unknown.Hubs)
	}
	if strings.Contains(rawWrong, "bob") || strings.Contains(rawWrong, "dave") || strings.Contains(rawWrong, "kleist") ||
		strings.Contains(rawWrong, `"user"`) || strings.Contains(rawWrong, `"account"`) {
		t.Errorf("Antwort nennt den vorgelegten Account oder einen User:\n%s", rawWrong)
	}
	if !strings.Contains(rawWrong, "keph (Node laptop): Anmeldung ungültig") {
		t.Errorf("Text:\n%s", rawWrong)
	}

	// Unbekannter Alias: gemeldet, nur der Alias, die anderen gelten
	// getrennt. Groß- und Kleinschreibung der Header zählt nicht, ein Alias
	// mit '.' und '_' geht.
	h := merge(pair("keph", "alice", e.tokens["keph/alice"]), pair("fremd", "bob", bob))
	h["X-KEPH-ACCOUNT-TEAM.X_Y"] = []string{"bob"}
	h["x-keph-token-team.x_y"] = []string{e.tokens["team.x_y/bob"]}
	out, raw = e.whoami(t, h)
	if len(out.Hubs) != 2 || !reflect.DeepEqual(out.UnknownHubs, []string{"fremd"}) {
		t.Errorf("unbekannter Alias: %+v", out)
	}
	if k := hubOf(t, out, "keph"); k.Login != LoginOK || k.Account != "alice" || k.User != "alice" || len(k.Collections) != 1 {
		t.Errorf("keph: %+v", k)
	}
	if x := hubOf(t, out, "team.x_y"); x.Login != LoginOK || x.User != "bob" ||
		!reflect.DeepEqual(x.Collections[0].Rights, []string{"read", "supersede"}) {
		t.Errorf("team.x_y: %+v", x)
	}
	if !strings.Contains(raw, "Zugangsdaten für Hubs, die dieser Node nicht kennt: fremd") {
		t.Errorf("Text:\n%s", raw)
	}
	// Das Token eines Hubs gilt nicht am anderen.
	out, _ = e.whoami(t, pair("team.x_y", "bob", bob))
	if hubOf(t, out, "team.x_y").Login != LoginInvalid {
		t.Error("Token von keph gilt an team.x_y")
	}
	// Nur ein Header des Paars: invalid.
	half := http.Header{}
	half.Set("X-Keph-Account-keph", "bob")
	out, _ = e.whoami(t, half)
	if hubOf(t, out, "keph").Login != LoginInvalid {
		t.Errorf("halbes Paar: %+v", out.Hubs)
	}
}

// Ein Hub-Eintrag ohne Replica: login invalid, auch mit richtigen
// Zugangsdaten — den Grund zeigt sync („noch nie abgeglichen“).
func TestWhoamiWithoutReplica(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if err := e.nodes.AddHub(ctx, store.Hub{Name: "neu", NodeName: "rechner", Transport: store.TransportHTTPS,
		Address: "https://neu.example.org", Token: token(t)}, false); err != nil {
		t.Fatal(err)
	}
	out, raw := e.whoami(t, pair("neu", "bob", e.tokens["keph/bob"]))
	got := hubOf(t, out, "neu")
	if got.Login != LoginInvalid || got.Node != "rechner" || !reflect.DeepEqual(got.Sync, SyncInfo{NeverSynced: true}) {
		t.Errorf("ohne Replica: %+v", got)
	}
	if !strings.Contains(raw, "neu (Node rechner): Anmeldung ungültig; noch nie abgeglichen") ||
		!strings.Contains(raw, `"never_synced":true`) {
		t.Errorf("Text:\n%s", raw)
	}
}

// Der Stand des Abgleichs aus hub_sync und der Replica: letzter Erfolg,
// Revision, letzter Fehler als Art — ohne die Meldung, die die Adresse
// nennt.
func TestWhoamiSyncState(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	h, err := e.nodes.Hub(ctx, "keph")
	if err != nil {
		t.Fatal(err)
	}
	ok := time.Date(2026, 9, 26, 10, 0, 0, 0, time.UTC).UnixMilli()
	if err := e.nodes.RecordSync(ctx, "keph", h.EntryID, store.SyncRecord{At: ok}); err != nil {
		t.Fatal(err)
	}
	if err := e.nodes.RecordSync(ctx, "keph", h.EntryID, store.SyncRecord{At: ok + 60000,
		Err: "Hub http://127.0.0.1:9: dial tcp: connection refused", ErrKind: string(replica.KindUnreachable)}); err != nil {
		t.Fatal(err)
	}
	out, raw := e.whoami(t, pair("keph", "bob", e.tokens["keph/bob"]))
	zero := int64(0)
	want := SyncInfo{LastSuccess: "2026-09-26T10:00:00Z", Revision: &zero, LastError: "Hub nicht erreichbar",
		LastErrorAt: "2026-09-26T10:01:00Z"}
	if got := hubOf(t, out, "keph").Sync; !reflect.DeepEqual(got, want) {
		t.Errorf("sync: %+v", got)
	}
	if !strings.Contains(raw, "abgeglichen 2026-09-26T10:00:00Z, Revision 0; letzter Fehler 2026-09-26T10:01:00Z: Hub nicht erreichbar") {
		t.Errorf("Text:\n%s", raw)
	}
	for _, not := range []string{"127.0.0.1", "connection refused", "dial"} {
		if strings.Contains(raw, not) {
			t.Errorf("Antwort enthält %q:\n%s", not, raw)
		}
	}
	e.noSecrets(t, raw, e.tokens["keph/bob"])
}

// noSecrets prüft die rohe Antwort: kein Token, kein Hash, keine Adresse,
// kein Transport, keine hub_id.
func (e *env) noSecrets(t *testing.T, raw, tok string) {
	t.Helper()
	ctx := context.Background()
	secrets := []string{tok, ident.HashToken(tok), "keph_", `"hash"`, "hub.example.org", "https", "hub_id", "transport"}
	hubs, err := e.nodes.Hubs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hubs {
		secrets = append(secrets, h.Token, h.EntryID)
		if rep, err := replica.Open(ctx, e.nodes.ReplicaPath(h.Name)); err == nil {
			secrets = append(secrets, rep.HubID())
			_ = rep.Close()
		}
	}
	for _, secret := range secrets {
		if strings.Contains(raw, secret) {
			t.Errorf("Antwort enthält %q:\n%s", secret, raw)
		}
	}
}

func TestHostAndOrigin(t *testing.T) {
	e := newEnv(t)
	_, port, _ := strings.Cut(strings.TrimPrefix(e.url, "http://"), ":")
	cases := []struct {
		host, origin string
		status       int
	}{
		{"127.0.0.1:" + port, "", http.StatusOK},
		{"localhost:" + port, "http://localhost:" + port, http.StatusOK},
		{"localhost:" + port, "http://127.0.0.1:3000", http.StatusOK},
		{"evil.example:" + port, "", http.StatusForbidden},
		{"localhost:1", "", http.StatusForbidden},
		{"localhost", "", http.StatusForbidden},
		{"127.0.0.1:" + port, "http://evil.example", http.StatusForbidden},
		{"127.0.0.1:" + port, "https://localhost:" + port, http.StatusForbidden},
		{"127.0.0.1:" + port, "null", http.StatusForbidden},
	}
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`
	for _, c := range cases {
		req, _ := http.NewRequest(http.MethodPost, e.url+Path, strings.NewReader(body))
		req.Host = c.host
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		if c.origin != "" {
			req.Header.Set("Origin", c.origin)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != c.status {
			t.Errorf("Host %q, Origin %q: HTTP %d, erwartet %d", c.host, c.origin, resp.StatusCode, c.status)
		}
	}
	resp, err := http.Get(e.url + "/anderes")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("anderer Pfad: HTTP %d", resp.StatusCode)
	}
}

func TestHubHeaders(t *testing.T) {
	h := http.Header{}
	h.Set("X-Keph-Account-my-hub", "alice")
	h.Set("X-Keph-Token-my-hub", "keph_x")
	h.Set("X-Keph-Account-Böse", "x")
	h.Add("X-Keph-Token-doppelt", "a")
	h.Add("X-Keph-Token-doppelt", "b")
	h.Set("X-Keph-Account-doppelt", "bob")
	got := HubHeaders(h)
	want := []HubHeader{{Alias: "doppelt", Account: "bob"}, {Alias: "my-hub", Account: "alice", Token: "keph_x", Complete: true}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("HubHeaders = %+v", got)
	}
}

// Eine Account-Zeile ohne user — von einem Hub vor Task 006, bis der nächste
// Abgleich die Replica verwirft — ist ein Fehler dieser Zeile: nicht
// angemeldet, kein Absturz. Eine gültige Zeile daneben gilt weiter.
func TestWhoamiRowWithoutUser(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	tok := token(t)
	old := `{"hash":"` + ident.HashToken(tok) + `","rights":{"write":true,"supersede":false}}`
	good, err := contract.EncodeAccountContent(contract.AccountContent{Hash: ident.HashToken(tok), User: "carl"})
	if err != nil {
		t.Fatal(err)
	}
	row := func(collection, account, content string) contract.Row {
		return contract.Row{ID: ulid.Make().String(), Collection: collection, Name: contract.AccountRowName(account),
			Content: &content, Revision: 9, CreatedBy: "admin", UpdatedBy: "admin"}
	}
	h, err := e.nodes.Hub(ctx, "keph")
	if err != nil {
		t.Fatal(err)
	}
	rep, err := replica.Open(ctx, e.nodes.ReplicaPath("keph"))
	if err != nil {
		t.Fatal(err)
	}
	hubID := rep.HubID()
	_ = rep.Close()
	rows := []contract.Row{row("team-x", "carl", old), row("team-x", "dora", old), row("privat", "dora", good)}
	if _, _, err := replica.WriteAccountRows(ctx, e.nodes, h, hubID, rows); err != nil {
		t.Fatal(err)
	}
	out, raw := e.whoami(t, pair("keph", "carl", tok))
	if got := hubOf(t, out, "keph"); got.Login != LoginInvalid || got.Account != "" ||
		!strings.Contains(raw, "keph (Node laptop): Anmeldung ungültig") {
		t.Errorf("Zeile ohne user: %+v\n%s", got, raw)
	}
	out, _ = e.whoami(t, pair("keph", "dora", tok))
	if got := hubOf(t, out, "keph"); got.Login != LoginOK || got.User != "carl" || len(got.Collections) != 1 ||
		got.Collections[0].Collection != "privat" {
		t.Errorf("gültige Zeile neben einer ohne user: %+v", got)
	}
}

// breakReplicas trägt zwei Hubs mit kaputter Replica ein: alt mit Replica
// alter Schemafassung — zuletzt gelungen, danach ein Fehler in hub_sync —
// und kaputt mit einer Datei aus Müll-Bytes, nie gelungen, aber mit einem
// alten Fehler. bob hat an beiden dasselbe Token wie an keph.
func (e *env) breakReplicas(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	for _, alias := range []string{"alt", "kaputt"} {
		if err := e.nodes.AddHub(ctx, store.Hub{Name: alias, NodeName: "laptop", Transport: store.TransportHTTPS,
			Address: "https://hub.example.org", Token: token(t)}, false); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.nodes.AddCollection(ctx, "alt", "team-x"); err != nil {
		t.Fatal(err)
	}
	alt, err := e.nodes.Hub(ctx, "alt")
	if err != nil {
		t.Fatal(err)
	}
	content, err := contract.EncodeAccountContent(contract.AccountContent{Hash: ident.HashToken(e.tokens["keph/bob"]),
		User: "kleist"})
	if err != nil {
		t.Fatal(err)
	}
	rows := []contract.Row{{ID: ulid.Make().String(), Collection: "team-x", Name: contract.AccountRowName("bob"),
		Content: &content, Revision: 1, CreatedBy: "admin", UpdatedBy: "admin"}}
	if _, _, err := replica.WriteAccountRows(ctx, e.nodes, alt, ulid.Make().String(), rows); err != nil {
		t.Fatal(err)
	}
	db, err := sqlitedb.Open(ctx, e.nodes.ReplicaPath("alt"))
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlitedb.SetInfo(ctx, db, sqlitedb.KeySchemaVersion, "2"); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	ok := time.Date(2026, 9, 26, 10, 0, 0, 0, time.UTC).UnixMilli()
	if err := e.nodes.RecordSync(ctx, "alt", alt.EntryID, store.SyncRecord{At: ok}); err != nil {
		t.Fatal(err)
	}
	if err := e.nodes.RecordSync(ctx, "alt", alt.EntryID, store.SyncRecord{At: ok + 60000, Err: "Hub weg",
		ErrKind: string(replica.KindUnreachable)}); err != nil {
		t.Fatal(err)
	}
	kaputt, err := e.nodes.Hub(ctx, "kaputt")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.nodes.ReplicaPath("kaputt"), []byte(strings.Repeat("kein SQLite, nur Müll ", 400)),
		0o600); err != nil {
		t.Fatal(err)
	}
	if err := e.nodes.RecordSync(ctx, "kaputt", kaputt.EntryID, store.SyncRecord{At: ok, Err: "Hub weg",
		ErrKind: string(replica.KindUnreachable)}); err != nil {
		t.Fatal(err)
	}
}

// Eine Replica, die sich nicht lesen lässt — alte Schemafassung oder Müll —,
// betrifft nur ihren Hub: login missing auch mit Header-Paar, keine
// Revision, als letzter Fehler „Replica nicht lesbar“ ohne Zeit statt des
// Fehlers aus hub_sync, never_synced nur ohne Erfolg. Die übrigen Hubs
// erscheinen vollständig; die Antwort nennt keinen Pfad und keine Meldung.
func TestWhoamiUnreadableReplica(t *testing.T) {
	e := newEnv(t)
	e.breakReplicas(t)
	bob := e.tokens["keph/bob"]
	out, raw := e.whoami(t, merge(pair("keph", "bob", bob), pair("alt", "bob", bob), pair("kaputt", "bob", bob)))
	if got := hubOf(t, out, "keph"); got.Login != LoginOK || got.User != "kleist" || len(got.Collections) != 2 ||
		got.Sync.Revision == nil {
		t.Errorf("gesunder Hub: %+v", got)
	}
	if got := hubOf(t, out, "team.x_y"); got.Login != LoginMissing || got.Sync.Revision == nil || got.Sync.LastError != "" {
		t.Errorf("gesunder Hub ohne Header: %+v", got)
	}
	wantAlt := HubInfo{Hub: "alt", Login: LoginMissing, Node: "laptop",
		Sync: SyncInfo{LastSuccess: "2026-09-26T10:00:00Z", LastError: ReplicaUnreadable}}
	if got := hubOf(t, out, "alt"); !reflect.DeepEqual(got, wantAlt) {
		t.Errorf("alt: %+v", got)
	}
	wantKaputt := HubInfo{Hub: "kaputt", Login: LoginMissing, Node: "laptop",
		Sync: SyncInfo{NeverSynced: true, LastError: ReplicaUnreadable}}
	if got := hubOf(t, out, "kaputt"); !reflect.DeepEqual(got, wantKaputt) {
		t.Errorf("kaputt: %+v", got)
	}
	for _, want := range []string{
		"keph (Node laptop): angemeldet als bob (User kleist)",
		"alt (Node laptop): Anmeldung nicht prüfbar; abgeglichen 2026-09-26T10:00:00Z; letzter Fehler: Replica nicht lesbar",
		"kaputt (Node laptop): Anmeldung nicht prüfbar; noch nie abgeglichen; letzter Fehler: Replica nicht lesbar",
	} {
		if !strings.Contains(raw, want) {
			t.Errorf("Text ohne %q:\n%s", want, raw)
		}
	}
	e.noSecrets(t, raw, bob)
	dir := filepath.Dir(e.nodes.ReplicaPath("alt"))
	for _, not := range []string{dir, "replicas", ".db", "Schemafassung", "db_info", "database", "last_error_at",
		"Hub weg", "nicht erreichbar", "entry_id", "alt:team-x"} {
		if strings.Contains(raw, not) {
			t.Errorf("Antwort enthält %q:\n%s", not, raw)
		}
	}

	// Ohne Header dasselbe Bild; AccountLogins (node whoami <account>) auch.
	out, _ = e.whoami(t, nil)
	if got := hubOf(t, out, "alt"); !reflect.DeepEqual(got, wantAlt) {
		t.Errorf("alt ohne Header: %+v", got)
	}
	ctx := context.Background()
	logins, err := AccountLogins(ctx, e.nodes, "bob")
	if err != nil {
		t.Fatal(err)
	}
	cli, _, unread, err := Whoami(ctx, e.nodes, "test", testUpdate, logins)
	if err != nil {
		t.Fatal(err)
	}
	if got := hubOf(t, cli, "keph"); got.Login != LoginOK {
		t.Errorf("AccountLogins keph: %+v", got)
	}
	if got := hubOf(t, cli, "kaputt"); !reflect.DeepEqual(got, wantKaputt) {
		t.Errorf("AccountLogins kaputt: %+v", got)
	}
	if len(unread) != 2 || unread[0].Hub != "alt" || unread[1].Hub != "kaputt" ||
		!strings.Contains(unread[1].Error(), e.nodes.ReplicaPath("kaputt")) {
		t.Errorf("Meldungen: %v", unread)
	}
	accounts, unreadAccounts, err := KnownAccounts(ctx, e.nodes)
	if err != nil || len(unreadAccounts) != 2 {
		t.Fatalf("KnownAccounts: %v, %v", unreadAccounts, err)
	}
	for _, a := range accounts {
		if a.Hub == "alt" || a.Hub == "kaputt" {
			t.Errorf("Account aus kaputter Replica: %+v", a)
		}
	}
}

// Ein abgebrochener ctx ist ein Fehler der Anfrage, keine unlesbare Replica.
func TestWhoamiCanceled(t *testing.T) {
	e := newEnv(t)
	e.breakReplicas(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h, err := e.nodes.Hub(context.Background(), "kaputt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := openReplica(ctx, e.nodes, h); err == nil || asUnreadable(err) != nil {
		t.Errorf("openReplica mit abgebrochenem ctx: %v", err)
	}
	if _, err := accountRows(ctx, e.nodes, h, "bob"); err == nil || asUnreadable(err) != nil {
		t.Errorf("accountRows mit abgebrochenem ctx: %v", err)
	}
	if _, _, _, err := Whoami(ctx, e.nodes, "test", testUpdate, Logins{}); err == nil {
		t.Error("Whoami mit abgebrochenem ctx ohne Fehler")
	}
	if _, err := AccountLogins(ctx, e.nodes, "bob"); err == nil {
		t.Error("AccountLogins mit abgebrochenem ctx ohne Fehler")
	}
	if _, err := e.whoamiErr(t, ctx); err == nil {
		t.Error("Authenticate mit abgebrochenem ctx ohne Fehler")
	}
}

func (e *env) whoamiErr(t *testing.T, ctx context.Context) (Logins, error) {
	t.Helper()
	n := &Node{nodes: e.nodes, version: "test"}
	return n.Authenticate(ctx, pair("kaputt", "bob", e.tokens["keph/bob"]))
}

// DescribeSync kommt ohne Revision und ohne Zeit des Fehlers aus.
func TestDescribeSync(t *testing.T) {
	rev := int64(7)
	cases := []struct {
		in   SyncInfo
		want string
	}{
		{SyncInfo{NeverSynced: true}, "noch nie abgeglichen"},
		{SyncInfo{Revision: &rev}, "Revision 7"},
		{SyncInfo{LastSuccess: "T", Revision: &rev}, "abgeglichen T, Revision 7"},
		{SyncInfo{LastSuccess: "T", Revision: &rev, LastError: "x", LastErrorAt: "U"}, "abgeglichen T, Revision 7; letzter Fehler U: x"},
		{SyncInfo{LastSuccess: "T", LastError: ReplicaUnreadable}, "abgeglichen T; letzter Fehler: Replica nicht lesbar"},
		{SyncInfo{LastError: ReplicaUnreadable}, "letzter Fehler: Replica nicht lesbar"},
		{SyncInfo{}, ""},
	}
	for _, c := range cases {
		if got := DescribeSync(c.in); got != c.want {
			t.Errorf("DescribeSync(%+v) = %q, erwartet %q", c.in, got, c.want)
		}
	}
}

// testUpdate ist die Antwort auf die Frage nach einer neuen Version, die der
// Handler in den Tests bekommt.
var testUpdate = upgrade.Report{State: upgrade.StateOK, CheckedAt: "2026-09-26T08:00:00Z", Version: "test",
	Latest: "v0.2.0", UpdateAvailable: true, Method: upgrade.MethodAdmin,
	Command: "sudo kephalaion upgrade && sudo systemctl restart kephalaion", Hint: "globale Installation"}
