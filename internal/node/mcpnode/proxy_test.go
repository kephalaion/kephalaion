package mcpnode

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kephalaion/kephalaion/internal/config"
	"github.com/kephalaion/kephalaion/internal/contract"
	"github.com/kephalaion/kephalaion/internal/node/replica"
	"github.com/kephalaion/kephalaion/internal/node/store"
	"github.com/kephalaion/kephalaion/internal/reqlog"
	"github.com/kephalaion/kephalaion/internal/upgrade"
)

// Der Eingang über einen Proxy (Task 023): Eine Anfrage mit X-Forwarded-For
// ohne gültige Anmeldung an einem Hub bekommt eine verdeckte Antwort — keine
// Version, kein update, keine Namen von Node und Hubs. Lokal bleibt alles.

// Namen und Version des Nodes im Test, so gewählt, dass sie in keiner
// anderen Meldung vorkommen.
const (
	proxyVersion = "9.8.7-probe"
	proxyNode    = "rechner7"
)

var proxyUpdate = upgrade.Report{State: upgrade.StateOK, CheckedAt: "2026-10-01T08:00:00Z", Version: proxyVersion,
	Latest: "v9.9.9", UpdateAvailable: true, SelfUpgrade: true, Method: upgrade.MethodSelf,
	Command: "aktualisiere-mich-jetzt", Hint: "aktualisiere-mich-jetzt"}

// proxySecrets ist, was eine verdeckte Antwort nie enthalten darf: Version,
// update und die Namen von Node und Hubs.
func proxySecrets(hubs []string) []string {
	return append([]string{proxyVersion, "v9.9.9", "aktualisiere-mich", proxyNode, `"update"`}, hubs...)
}

// proxyEnv ist ein Node mit den Hubs aliases (abgeglichen: zentrale mit
// wissen und privat, werkstatt mit notizen; lager ohne Replica), davor ein
// Nachbau des Proxys (caddyProxy): Host auf den Node, X-Forwarded-For neu,
// Antwort-Header X-Keph-Login ins Zugriffslog caddy und aus der Antwort.
type proxyEnv struct {
	nodes  store.Store
	hubs   map[string]*docHub
	tokens map[string]string
	direct string
	proxy  string
	log    *lockedBuffer
	// caddy ist das Zugriffslog des Proxy-Nachbaus, eine JSON-Zeile je
	// Anfrage über ihn — was die Jail liest.
	caddy *lockedBuffer
	// docID ist die id von wissen/a.md an zentrale.
	docID string

	mu sync.Mutex
	// rechecks sind die Hubs, für die der Node über HubLink.Recheck den
	// Abgleich angestoßen hat (Task 025).
	rechecks []string
}

func newProxyEnv(t *testing.T, aliases ...string) *proxyEnv {
	t.Helper()
	ctx := context.Background()
	nodes, err := store.Create(ctx, config.SQLiteDB(filepath.Join(t.TempDir(), "node.db")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = nodes.Close() })
	collections := map[string][]string{"zentrale": {"privat", "wissen"}, "werkstatt": {"notizen"}, "lager": {"kisten"}}
	e := &proxyEnv{nodes: nodes, hubs: map[string]*docHub{}, tokens: map[string]string{}}
	for _, alias := range aliases {
		if err := nodes.AddHub(ctx, store.Hub{Name: alias, NodeName: proxyNode, Transport: store.TransportHTTPS,
			Address: "https://hub.example.org", Token: token(t)}, false); err != nil {
			t.Fatal(err)
		}
		for _, c := range collections[alias] {
			if err := nodes.AddCollection(ctx, alias, c); err != nil {
				t.Fatal(err)
			}
		}
		if alias == "lager" {
			continue
		}
		f := newDocHub(collections[alias]...)
		e.hubs[alias] = f
		e.tokens[alias] = token(t)
		for _, c := range collections[alias] {
			f.grant("anna", c, e.tokens[alias], contract.Rights{Write: true})
		}
	}
	if f := e.hubs["zentrale"]; f != nil {
		e.docID = f.put("wissen", "a.md", "# A\n")
	}
	e.sync(t)
	// lager hat nie abgeglichen: Die Replica fehlt.
	_ = os.Remove(nodes.ReplicaPath("lager"))
	e.log = &lockedBuffer{}
	link := HubLink{
		Connect: func(_ context.Context, h store.Hub) (contract.Hub, func(), error) {
			return e.hubs[h.Name], func() {}, nil
		},
		Sync: func(store.Hub) {},
		Recheck: func(h store.Hub) {
			e.mu.Lock()
			defer e.mu.Unlock()
			e.rechecks = append(e.rechecks, h.Name)
		},
	}
	node := httptest.NewServer(reqlog.New(e.log).Middleware("node",
		NewHandler(nodes, proxyVersion, func() upgrade.Report { return proxyUpdate }, link)))
	t.Cleanup(node.Close)
	e.direct = node.URL
	e.caddy = &lockedBuffer{}
	e.proxy = caddyProxy(t, node.URL, e.caddy).URL
	return e
}

// clientIPKey trägt im ctx der Anfrage an den Node die Adresse des Clients
// am Proxy-Nachbau.
type clientIPKey struct{}

// caddyProxy ist der Nachbau von Caddy vor dem Node, wie das Caddyfile der
// Vorlage ihn einrichtet (docs/installation.md, „Node für Clients anderer
// Rechner“): httputil.ReverseProxy setzt Host auf den Node und
// X-Forwarded-For neu (einen mitgeschickten verwirft er), nimmt den
// Antwort-Header X-Keph-Login aus der Antwort (header_down) und schreibt je
// Anfrage eine Zeile wie Caddys Zugriffslog nach log (log_append): JSON mit
// remote_ip aus der Verbindung, uri mit dem Präfix /kephalaion, wie der Client
// ihn schrieb, und keph_login aus dem Header — null ohne ihn. Ohne
// Request-Header, wie der Filter der Vorlage. Eine Anfrage direkt an den Node
// erzeugt keine Zeile.
func caddyProxy(t *testing.T, target string, log *lockedBuffer) *httptest.Server {
	t.Helper()
	u, err := url.Parse(target)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(&httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(u)
			pr.SetXForwarded()
			ip, _, _ := net.SplitHostPort(pr.In.RemoteAddr)
			pr.Out = pr.Out.WithContext(context.WithValue(pr.Out.Context(), clientIPKey{}, ip))
		},
		ModifyResponse: func(resp *http.Response) error {
			ip, _ := resp.Request.Context().Value(clientIPKey{}).(string)
			var login *string
			if v := resp.Header.Get(LoginHeader); v != "" {
				login = &v
			}
			resp.Header.Del(LoginHeader)
			b, err := json.Marshal(caddyLine{Level: "info", TS: float64(time.Now().UnixNano()) / 1e9,
				Logger: "http.log.access.log0", Msg: "handled request",
				Request: caddyRequest{RemoteIP: ip, RemotePort: "49222", ClientIP: ip, Proto: resp.Request.Proto,
					Method: resp.Request.Method, Host: resp.Request.Host, URI: "/kephalaion" + resp.Request.URL.RequestURI()},
				UserID: "", Duration: 0.001, Status: resp.StatusCode,
				RespHeaders: map[string][]string{"Content-Type": resp.Header.Values("Content-Type")}, KephLogin: login})
			if err != nil {
				return err
			}
			fmt.Fprintf(log, "%s\n", b)
			return nil
		},
	})
	t.Cleanup(srv.Close)
	return srv
}

// caddyLine ist eine Zeile des Zugriffslogs von Caddy in der Reihenfolge
// seiner Felder (caddy 2.11.7, lokaler Lauf 2026-10-09).
type caddyLine struct {
	Level       string              `json:"level"`
	TS          float64             `json:"ts"`
	Logger      string              `json:"logger"`
	Msg         string              `json:"msg"`
	Request     caddyRequest        `json:"request"`
	BytesRead   int                 `json:"bytes_read"`
	UserID      string              `json:"user_id"`
	Duration    float64             `json:"duration"`
	Size        int                 `json:"size"`
	Status      int                 `json:"status"`
	RespHeaders map[string][]string `json:"resp_headers"`
	KephLogin   *string             `json:"keph_login"`
}

type caddyRequest struct {
	RemoteIP   string `json:"remote_ip"`
	RemotePort string `json:"remote_port"`
	ClientIP   string `json:"client_ip"`
	Proto      string `json:"proto"`
	Method     string `json:"method"`
	Host       string `json:"host"`
	URI        string `json:"uri"`
}

// caddyLines sind die Zeilen des Zugriffslogs ab Position before.
func caddyLines(log *lockedBuffer, before int) []string {
	s := strings.TrimSpace(log.String()[before:])
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// lastCaddyLine ist die letzte Zeile des Zugriffslogs des Proxy-Nachbaus.
func lastCaddyLine(log *lockedBuffer) string {
	lines := caddyLines(log, 0)
	if len(lines) == 0 {
		return ""
	}
	return lines[len(lines)-1]
}

// sync gleicht alle Einträge mit den Fälschungen ab — wie der Abgleich im
// Hintergrund; lager ist nicht erreichbar.
func (e *proxyEnv) sync(t *testing.T) {
	t.Helper()
	s := &replica.Syncer{Nodes: e.nodes}
	if _, err := s.Sync(context.Background(), "", func(h store.Hub) (contract.Hub, error) {
		if f := e.hubs[h.Name]; f != nil {
			return f, nil
		}
		return nil, fmt.Errorf("nicht erreichbar")
	}); err != nil {
		t.Fatal(err)
	}
}

// takeRechecks liefert die Hubs der Anstöße seit dem letzten Aufruf.
func (e *proxyEnv) takeRechecks() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := e.rechecks
	e.rechecks = nil
	return out
}

// valid ist das gültige Header-Paar von anna an einem Hub, wrong eines mit
// falschem Token, unknown eines mit unbekanntem Account.
func (e *proxyEnv) valid(alias string) http.Header { return pair(alias, "anna", e.tokens[alias]) }

func (e *proxyEnv) wrong(t *testing.T, alias string) http.Header {
	return pair(alias, "anna", token(t))
}

func (e *proxyEnv) unknown(t *testing.T, alias string) http.Header {
	return pair(alias, "niemand", token(t))
}

const initializeBody = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18",` +
	`"capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`

// post schickt einen Body roh an base/mcp und liefert Status und Antwort.
func post(t *testing.T, base string, header http.Header, body string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, base+Path, strings.NewReader(body))
	for k, v := range header {
		req.Header[k] = v
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// serverVersion ist die Version aus der Antwort auf initialize.
func serverVersion(t *testing.T, base string, header http.Header) string {
	t.Helper()
	status, body := post(t, base, header, initializeBody)
	if status != http.StatusOK {
		t.Fatalf("initialize: HTTP %d %s", status, body)
	}
	var msg struct {
		Result struct {
			ServerInfo struct {
				Name    string  `json:"name"`
				Version *string `json:"version"`
			} `json:"serverInfo"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(body), &msg); err != nil || msg.Result.ServerInfo.Version == nil {
		t.Fatalf("initialize: %v %s", err, body)
	}
	if msg.Result.ServerInfo.Name != "kephalaion" {
		t.Errorf("initialize: Name %q", msg.Result.ServerInfo.Name)
	}
	return *msg.Result.ServerInfo.Version
}

// call ruft ein Werkzeug über den MCP-Client des SDK an base und liefert das
// ganze Ergebnis als JSON (Text und Struktur) und das Ergebnis selbst.
func (e *proxyEnv) call(t *testing.T, base string, header http.Header, tool string, args any) (string, *mcp.CallToolResult) {
	t.Helper()
	ctx := context.Background()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: base + Path,
		HTTPClient: &http.Client{Transport: headerTransport{header}}, DisableStandaloneSSE: true, MaxRetries: -1}, nil)
	if err != nil {
		t.Fatalf("initialize: %v", err)
	}
	defer session.Close()
	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	raw, _ := json.Marshal(res)
	return string(raw), res
}

// toolCall ist ein Aufruf eines Werkzeugs mit Argumenten.
type toolCall struct {
	tool string
	args map[string]any
}

// hiddenCalls sind alle Werkzeuge mit Adressen ohne Hub-Teil — jede Antwort,
// die einen Hub nennte, käme vom Node, nicht aus der Anfrage.
func (e *proxyEnv) hiddenCalls() []toolCall {
	return []toolCall{
		{"whoami", nil},
		{"list", nil},
		{"list", map[string]any{"collection": "wissen"}},
		{"list", map[string]any{"collection": "kisten", "path": "a"}},
		{"read", map[string]any{"collection": "wissen", "name": "a.md"}},
		{"read", map[string]any{"collection": "kisten", "name": "a.md"}},
		{"read", map[string]any{"id": e.docID}},
		{"changes", nil},
		{"changes", map[string]any{"collection": "wissen"}},
		{"create", map[string]any{"collection": "wissen", "name": "neu.md", "content": "x"}},
		{"write", map[string]any{"collection": "wissen", "name": "a.md", "content": "y"}},
		{"delete", map[string]any{"collection": "wissen", "name": "a.md"}},
		{"rename", map[string]any{"collection": "kisten", "name": "a.md", "new_name": "b.md"}},
	}
}

// writeCalls zählt die Schreibvorgänge an allen Hubs.
func (e *proxyEnv) writeCalls() int {
	n := 0
	for _, f := range e.hubs {
		n += f.writeCalls()
	}
	return n
}

// checkHidden prüft eine Antwort gegen den absoluten Maßstab: keine Version,
// kein update, kein Name von Node oder Hub.
func checkHidden(t *testing.T, what, raw string, hubs []string) {
	t.Helper()
	for _, s := range proxySecrets(hubs) {
		if strings.Contains(raw, s) {
			t.Errorf("%s nennt %q:\n%s", what, s, raw)
		}
	}
}

// Über den Proxy ohne gültige Anmeldung — ohne Header-Paar, mit falschem
// Token, mit unbekanntem Account — nennt keine Antwort Version, update oder
// einen Namen von Node oder Hub: initialize und alle Werkzeuge, auch ihre
// Fehler und unreadable_hubs; die Werkzeuge, die schreiben, erreichen den
// Hub nicht. Dazu ein Hub ohne Replica (lager) und, im zweiten Teil, einer
// mit unlesbarer.
func TestProxyHidesWithoutLogin(t *testing.T) {
	aliases := []string{"lager", "werkstatt", "zentrale"}
	e := newProxyEnv(t, aliases...)
	variants := map[string]http.Header{
		"ohne Header":         nil,
		"falsches Token":      merge(e.wrong(t, "zentrale"), e.wrong(t, "werkstatt"), e.wrong(t, "lager")),
		"unbekannter Account": e.unknown(t, "zentrale"),
		"unbekannter Hub":     e.wrong(t, "nirgends"),
	}
	check := func(t *testing.T) {
		t.Helper()
		// Je Variante parallel; der Rahmen wartet auf alle.
		t.Run("Varianten", func(t *testing.T) {
			for name, h := range variants {
				t.Run(name, func(t *testing.T) {
					t.Parallel()
					checkVariant(t, e, name, h, aliases)
				})
			}
		})
		if n := e.writeCalls(); n != 0 {
			t.Errorf("%d Schreibvorgänge erreichten den Hub", n)
		}
	}
	check(t)
	// Eine Replica, die sich nicht lesen lässt, nennt ihren Hub auch nicht
	// in unreadable_hubs.
	if err := os.WriteFile(e.nodes.ReplicaPath("werkstatt"), []byte(strings.Repeat("Müll ", 900)), 0o600); err != nil {
		t.Fatal(err)
	}
	check(t)
}

// checkVariant prüft initialize und alle Werkzeuge über den Proxy mit den
// Headern h gegen den absoluten Maßstab.
func checkVariant(t *testing.T, e *proxyEnv, name string, h http.Header, aliases []string) {
	t.Helper()
	if v := serverVersion(t, e.proxy, h); v != "" {
		t.Errorf("%s: initialize nennt die Version %q", name, v)
	}
	for _, c := range e.hiddenCalls() {
		raw, res := e.call(t, e.proxy, h, c.tool, c.args)
		checkHidden(t, fmt.Sprintf("%s: %s %v", name, c.tool, c.args), raw, aliases)
		if c.tool != "whoami" {
			continue
		}
		var out WhoamiOutput
		b, _ := json.Marshal(res.StructuredContent)
		if err := json.Unmarshal(b, &out); err != nil || !out.Hidden || len(out.Hubs) != 0 ||
			len(out.UnknownHubs) != 0 || out.Version != "" || out.Update != nil || textOf(res) != HiddenText {
			t.Errorf("%s: whoami %v %s", name, err, raw)
		}
	}
}

// Mit Hub-Teil sagt die Antwort über den Proxy ohne gültige Anmeldung nichts,
// was der Client nicht schon geschickt hat: Ein Hub, den es gibt — mit
// Replica, ohne, mit unlesbarer —, klingt wie einer, den es nicht gibt.
func TestProxyNoOracleForHubNames(t *testing.T) {
	e := newProxyEnv(t, "lager", "werkstatt", "zentrale")
	if err := os.WriteFile(e.nodes.ReplicaPath("werkstatt"), []byte(strings.Repeat("Müll ", 900)), 0o600); err != nil {
		t.Fatal(err)
	}
	h := e.wrong(t, "zentrale")
	calls := func(hub string) []toolCall {
		return []toolCall{
			{"list", map[string]any{"collection": hub + ":wissen"}},
			{"list", map[string]any{"collection": hub + ":"}},
			{"read", map[string]any{"collection": hub + ":wissen", "name": "a.md"}},
			{"read", map[string]any{"collection": hub + ":", "id": e.docID}},
			{"changes", map[string]any{"collection": hub + ":wissen"}},
			{"create", map[string]any{"collection": hub + ":wissen", "name": "neu.md", "content": "x"}},
			{"delete", map[string]any{"collection": hub + ":wissen", "name": "a.md"}},
		}
	}
	want := calls("nirgends")
	for _, hub := range []string{"zentrale", "werkstatt", "lager"} {
		for i, c := range calls(hub) {
			raw, _ := e.call(t, e.proxy, h, c.tool, c.args)
			other, _ := e.call(t, e.proxy, h, want[i].tool, want[i].args)
			if strings.ReplaceAll(raw, hub, "nirgends") != other {
				t.Errorf("%s %v unterscheidet sich von einem unbekannten Hub:\n%s\n%s", c.tool, c.args, raw, other)
			}
			checkHidden(t, c.tool, strings.ReplaceAll(raw, hub, "H"), []string{"zentrale", "werkstatt", "lager"})
		}
	}
	if n := e.writeCalls(); n != 0 {
		t.Errorf("%d Schreibvorgänge erreichten den Hub", n)
	}
}

// Mit nur einem Hub nähme resolve ohne Hub-Teil und ohne gültige Anmeldung
// diesen und nennte ihn — über den Proxy nicht.
func TestProxyHidesSingleHub(t *testing.T) {
	e := newProxyEnv(t, "zentrale")
	for _, h := range []http.Header{nil, e.wrong(t, "zentrale")} {
		for _, c := range e.hiddenCalls() {
			raw, _ := e.call(t, e.proxy, h, c.tool, c.args)
			checkHidden(t, c.tool, raw, []string{"zentrale"})
		}
	}
	// Gegenprobe lokal: Dort nennt die Meldung den einzigen Hub wie bisher.
	raw, _ := e.call(t, e.direct, nil, "read", map[string]any{"collection": "wissen", "name": "a.md"})
	if !strings.Contains(raw, "zentrale:wissen nicht lesbar") {
		t.Errorf("lokal: %s", raw)
	}
}

// Gültig an einem Hub, nicht am anderen: über den Proxy wie lokal — whoami
// mit Version, update, Node-Name und allen Hubs, initialize mit Version;
// Lesen und Schreiben am gültigen Hub gehen.
func TestProxyValidAtOneHub(t *testing.T) {
	e := newProxyEnv(t, "lager", "werkstatt", "zentrale")
	h := merge(e.valid("zentrale"), e.wrong(t, "werkstatt"), e.wrong(t, "nirgends"))
	if v := serverVersion(t, e.proxy, h); v != proxyVersion {
		t.Errorf("initialize: Version %q", v)
	}
	_, res := e.call(t, e.proxy, h, "whoami", nil)
	var out WhoamiOutput
	b, _ := json.Marshal(res.StructuredContent)
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out.Hidden || out.Version != proxyVersion || out.Update == nil || out.Update.Latest != "v9.9.9" ||
		len(out.Hubs) != 3 || !slices.Equal(out.UnknownHubs, []string{"nirgends"}) {
		t.Fatalf("whoami: %+v", out)
	}
	for _, want := range []struct{ hub, login string }{{"lager", LoginMissing}, {"werkstatt", LoginInvalid},
		{"zentrale", LoginOK}} {
		var got HubInfo
		for _, hi := range out.Hubs {
			if hi.Hub == want.hub {
				got = hi
			}
		}
		if got.Login != want.login || got.Node != proxyNode {
			t.Errorf("%s: %+v", want.hub, got)
		}
	}
	if raw, res := e.call(t, e.proxy, h, "read", map[string]any{"collection": "wissen", "name": "a.md"}); res.IsError {
		t.Errorf("read: %s", raw)
	}
	if raw, res := e.call(t, e.proxy, h, "create", map[string]any{"collection": "wissen", "name": "neu.md",
		"content": "x"}); res.IsError {
		t.Errorf("create: %s", raw)
	}
	if raw, _ := e.call(t, e.proxy, h, "read", map[string]any{"collection": "lager:kisten"}); !strings.Contains(raw,
		"Hub lager: noch nie abgeglichen") {
		t.Errorf("read an lager: %s", raw)
	}
}

// Lokal (ohne X-Forwarded-For) bleibt alles: whoami ohne Anmeldung nennt
// Version, update, alle Hubs mit Node-Name und unknown_hubs, initialize die
// Version; Meldungen nennen Hubs wie bisher.
func TestLocalUnchanged(t *testing.T) {
	e := newProxyEnv(t, "lager", "werkstatt", "zentrale")
	for _, h := range []http.Header{nil, e.wrong(t, "zentrale"), e.wrong(t, "nirgends")} {
		if v := serverVersion(t, e.direct, h); v != proxyVersion {
			t.Errorf("initialize: Version %q", v)
		}
		_, res := e.call(t, e.direct, h, "whoami", nil)
		var out WhoamiOutput
		b, _ := json.Marshal(res.StructuredContent)
		if err := json.Unmarshal(b, &out); err != nil {
			t.Fatal(err)
		}
		if out.Hidden || out.Version != proxyVersion || out.Update == nil || len(out.Hubs) != 3 ||
			out.Hubs[0].Node != proxyNode || !strings.Contains(textOf(res), proxyVersion) {
			t.Errorf("whoami lokal: %+v", out)
		}
	}
	_, res := e.call(t, e.direct, e.wrong(t, "nirgends"), "whoami", nil)
	if !strings.Contains(textOf(res), "nicht kennt: nirgends") {
		t.Errorf("unknown_hubs lokal: %s", textOf(res))
	}
	if raw, _ := e.call(t, e.direct, nil, "read", map[string]any{"collection": "lager:kisten"}); !strings.Contains(raw,
		"Hub lager: noch nie abgeglichen") {
		t.Errorf("read lokal: %s", raw)
	}
}

// Eine fremde Origin bekommt auch über den Proxy 403 — vor jeder Anmeldung,
// kein 401.
func TestProxyForeignOrigin(t *testing.T) {
	e := newProxyEnv(t, "zentrale")
	for _, origin := range []string{"https://evil.example", "https://" + strings.TrimPrefix(e.proxy, "http://")} {
		h := merge(e.valid("zentrale"), http.Header{"Origin": {origin}})
		if status, body := post(t, e.proxy, h, initializeBody); status != http.StatusForbidden {
			t.Errorf("Origin %s: HTTP %d %s", origin, status, body)
		}
	}
}

// jailFailregex ist failregex des Filters der Jail für Fehlversuche, Zeichen
// für Zeichen wie in der Vorlage (docs/installation.md, „Fehlversuche: eine
// Jail auf dem Zugriffslog des Proxys“, filter.d/kephalaion-mcp.conf), nach
// der der Filter auf dem Rechner des Proxys steht (Task 025): nicht
// verankert, die JSON-Zeile des Zugriffslogs von Caddy mit remote_ip und uri
// einer der drei Routen im selben request-Objekt und keph_login invalid
// dahinter. TestJailFilterIsTemplate hält beide gleich.
const jailFailregex = `"request":\{"remote_ip":"<ADDR>",[^}]*"uri":"/kephalaion/(?:mcp|account/rotate|account/check)(?:\?[^"]*)?"[^}]*\}.*"keph_login":"invalid"`

// jailAddr steht für <ADDR>, das fail2ban durch eine IPv4- oder
// IPv6-Adresse ersetzt, und fängt sie. Vereinfacht: fail2bans Ausdruck für
// IPv6 braucht Lookbehind, den regexp nicht kennt.
const jailAddr = `\[?(?:::f{4,6}:)?((?:\d{1,3}\.){3}\d{1,3}|[0-9A-Fa-f]*:[0-9A-Fa-f:.]*)\]?`

// jailFilter ist der Filter der Vorlage als regexp.
var jailFilter = regexp.MustCompile(strings.Replace(jailFailregex, "<ADDR>", jailAddr, 1))

// jailHits sind die Adressen, für die der Filter in einer Zeile des
// Zugriffslogs von Caddy trifft, je Treffer eine; eine leere Zeile (keine
// Anfrage über den Proxy) trifft nie. Geprüft in jeder Form, in der fail2ban
// die Zeile bekommen kann: aus dem Journal mit dessen Präfix und als Zeile
// einer Datei; beide Formen ergeben dasselbe.
func jailHits(t *testing.T, line string) []string {
	t.Helper()
	var first []string
	for i, form := range []string{"vm caddy[4711]: " + line, line} {
		hits := []string{}
		for _, m := range jailFilter.FindAllStringSubmatch(form, -1) {
			hits = append(hits, m[1])
		}
		if i == 0 {
			first = hits
		} else if !slices.Equal(hits, first) {
			t.Errorf("Filter: %v in %q, aber %v im Journal", hits, form, first)
		}
	}
	return first
}

// Der Filter im Test ist der der Vorlage: failregex im Block von
// filter.d/kephalaion-mcp.conf in docs/installation.md. Fehlt docs/ (eine
// Kopie nur mit dem Code, etwa make mutate), entfällt der Vergleich.
func TestJailFilterIsTemplate(t *testing.T) {
	docs := filepath.Join("..", "..", "..", "docs")
	if _, err := os.Stat(docs); os.IsNotExist(err) {
		t.Skip("ohne docs/")
	}
	text, err := os.ReadFile(filepath.Join(docs, "installation.md"))
	if err != nil {
		t.Fatal(err)
	}
	_, block, ok := strings.Cut(string(text), "# /etc/fail2ban/filter.d/kephalaion-mcp.conf\n")
	block, _, _ = strings.Cut(block, "```")
	var found []string
	for _, line := range strings.Split(block, "\n") {
		if v, ok := strings.CutPrefix(line, "failregex = "); ok {
			found = append(found, v)
		}
	}
	if !ok || len(found) != 1 || found[0] != jailFailregex {
		t.Errorf("failregex in docs/installation.md: %q, im Test: %q", found, jailFailregex)
	}
}

// Fehlversuche im Log: Jede Anfrage mit mindestens einem ungültigen
// Header-Paar trägt im Log des Nodes login=invalid, einmal, direkt hinter
// via, und über den Proxy den Antwort-Header, den der Proxy als keph_login
// in seine Zeile schreibt — die der Filter der Jail trifft, mit der Adresse
// der Verbindung; ohne Header-Paar, gültig oder nur mit unbekanntem Alias
// nicht. Lokal steht der Vermerk ohne via, und beim Proxy entsteht keine
// Zeile — die Jail zählt dort nichts.
func TestFailedLoginLog(t *testing.T) {
	e := newProxyEnv(t, "werkstatt", "zentrale")
	cases := []struct {
		name   string
		base   string
		header http.Header
		failed bool
	}{
		{"ohne Header", e.proxy, nil, false},
		{"gültig", e.proxy, e.valid("zentrale"), false},
		{"falsches Token", e.proxy, e.wrong(t, "zentrale"), true},
		{"unbekannter Account", e.proxy, e.unknown(t, "zentrale"), true},
		{"gültig und falsch", e.proxy, merge(e.valid("zentrale"), e.wrong(t, "werkstatt")), true},
		{"zwei falsche", e.proxy, merge(e.wrong(t, "zentrale"), e.wrong(t, "werkstatt")), true},
		{"nur der Account", e.proxy, http.Header{"X-Keph-Account-Zentrale": {"anna"}}, true},
		{"unbekannter Hub", e.proxy, e.wrong(t, "nirgends"), false},
		{"lokal falsch", e.direct, e.wrong(t, "zentrale"), true},
	}
	for _, c := range cases {
		before, beforeCaddy := len(e.log.String()), len(e.caddy.String())
		post(t, c.base, c.header, initializeBody)
		lines := strings.Split(strings.TrimSpace(e.log.String()[before:]), "\n")
		if len(lines) != 1 {
			t.Fatalf("%s: %d Zeilen: %q", c.name, len(lines), lines)
		}
		line := lines[0]
		if strings.Count(line, "login=invalid") != map[bool]int{false: 0, true: 1}[c.failed] {
			t.Errorf("%s: %s", c.name, line)
		}
		caddy := caddyLines(e.caddy, beforeCaddy)
		if c.base == e.direct {
			if len(caddy) != 0 {
				t.Errorf("%s: Zeile bei Caddy ohne Proxy: %q", c.name, caddy)
			}
			continue
		}
		if len(caddy) != 1 {
			t.Fatalf("%s: %d Zeilen bei Caddy: %q", c.name, len(caddy), caddy)
		}
		hits := jailHits(t, caddy[0])
		want := []string{}
		if c.failed {
			want = []string{"127.0.0.1"}
		}
		if !slices.Equal(hits, want) {
			t.Errorf("%s: der Filter trifft %v, erwartet %v: %s", c.name, hits, want, caddy[0])
		}
		if strings.Contains(caddy[0], `"headers"`) || !strings.Contains(caddy[0], `"uri":"/kephalaion/mcp"`) {
			t.Errorf("%s: Zeile bei Caddy: %s", c.name, caddy[0])
		}
	}
}

// Fälschen: Ein mitgeschicktes X-Forwarded-For verwirft der Proxy, die
// Zeile bei Caddy trägt die Adresse der Verbindung; ein Account-Name, der
// etwas einschleusen will, steht im Log des Nodes maskiert und bei Caddy gar
// nicht (keine Request-Header). Eine Query, die den Ausdruck der Jail
// nachstellt, trifft nicht: Der Client muss sie kodieren, und keph_login
// kommt nur aus der Antwort des Nodes. Ein lokaler Prozess mit gefälschtem
// X-Forwarded-For erzeugt im Log des Nodes via=6.6.6.6 — bei Caddy keine
// Zeile: Der Weg, eine fremde Adresse sperren zu lassen, ist zu (Task 025).
func TestFailedLoginLogNotForgeable(t *testing.T) {
	e := newProxyEnv(t, "zentrale")
	h := http.Header{
		"X-Keph-Account-Zentrale": {"x via=6.6.6.6 login=invalid"},
		"X-Keph-Token-Zentrale":   {token(t)},
		"X-Forwarded-For":         {"6.6.6.6"},
	}
	before, beforeCaddy := len(e.log.String()), len(e.caddy.String())
	post(t, e.proxy, h, initializeBody)
	line := strings.TrimSpace(e.log.String()[before:])
	if strings.Contains(line, "6.6.6.6") || strings.Count(line, "via=") != 1 ||
		!strings.Contains(line, "via=127.0.0.1 login=invalid account=(ungültig)") {
		t.Errorf("Zeile: %s", line)
	}
	caddy := caddyLines(e.caddy, beforeCaddy)
	if len(caddy) != 1 || strings.Contains(caddy[0], "6.6.6.6") || strings.Contains(caddy[0], "login=invalid") {
		t.Fatalf("Zeile bei Caddy: %q", caddy)
	}
	if hits := jailHits(t, caddy[0]); !slices.Equal(hits, []string{"127.0.0.1"}) {
		t.Errorf("Filter: %v in %s", hits, caddy[0])
	}
	// Der Filter ist nicht verankert. Ein Name bringt seinen Ausdruck nicht
	// in die Zeile bei Caddy (keine Request-Header), eine Query nur kodiert —
	// und keph_login steht nur, wenn der Node den Header setzt: neben einer
	// gültigen Anmeldung an allen Hubs der Anfrage nicht.
	inject := `"},"keph_login":"invalid"`
	for _, c := range []struct {
		name   string
		path   string
		header http.Header
		hits   []string
	}{
		{"Name", Path, http.Header{"X-Keph-Account-Zentrale": {inject}, "X-Keph-Token-Zentrale": {token(t)}},
			[]string{"127.0.0.1"}},
		{"Name, gültig", Path, merge(e.valid("zentrale"), http.Header{"X-Keph-Account-Nirgends": {inject},
			"X-Keph-Token-Nirgends": {token(t)}}), []string{}},
		{"Query, gültig", Path + "?x=" + url.QueryEscape(inject), e.valid("zentrale"), []string{}},
		{"Query, falsch", Path + "?x=" + url.QueryEscape(inject), e.wrong(t, "zentrale"), []string{"127.0.0.1"}},
	} {
		beforeCaddy = len(e.caddy.String())
		req, _ := http.NewRequest(http.MethodPost, e.proxy+c.path, strings.NewReader(initializeBody))
		for k, v := range c.header {
			req.Header[k] = v
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		caddy = caddyLines(e.caddy, beforeCaddy)
		if len(caddy) != 1 {
			t.Fatalf("%s: %d Zeilen bei Caddy", c.name, len(caddy))
		}
		if hits := jailHits(t, caddy[0]); resp.StatusCode != http.StatusOK || !slices.Equal(hits, c.hits) {
			t.Errorf("%s: HTTP %d, Filter %v, erwartet %v: %s", c.name, resp.StatusCode, hits, c.hits, caddy[0])
		}
	}
	// Ohne Proxy gilt das X-Forwarded-For eines lokalen Prozesses nur für
	// das Verdecken und den Vermerk im Log des Nodes; bei Caddy entsteht
	// keine Zeile, die Jail zählt nichts.
	before, beforeCaddy = len(e.log.String()), len(e.caddy.String())
	post(t, e.direct, h, initializeBody)
	if line := e.log.String()[before:]; !strings.Contains(line, "via=6.6.6.6 login=invalid account=(ungültig)") {
		t.Errorf("lokal: %s", line)
	}
	if caddy := caddyLines(e.caddy, beforeCaddy); len(caddy) != 0 {
		t.Errorf("lokal: Zeile bei Caddy: %q", caddy)
	}
}

// loginHeader schickt body an base/mcp und liefert den Antwort-Header
// X-Keph-Login (leer, wenn er fehlt); die Anfrage muss 200 ergeben.
func loginHeader(t *testing.T, base string, header http.Header, body string) string {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, base+Path, strings.NewReader(body))
	for k, v := range header {
		req.Header[k] = v
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("HTTP %d %s", resp.StatusCode, b)
	}
	if n := len(resp.Header.Values(LoginHeader)); n > 1 {
		t.Errorf("%s steht %d-mal", LoginHeader, n)
	}
	return resp.Header.Get(LoginHeader)
}

const whoamiBody = `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"whoami","arguments":{}}}`

// kephLogin ist der Wert von keph_login in einer Zeile des Zugriffslogs von
// Caddy: "invalid" oder leer (null).
func kephLogin(t *testing.T, line string) string {
	t.Helper()
	var l struct {
		KephLogin *string `json:"keph_login"`
	}
	if err := json.Unmarshal([]byte(line), &l); err != nil {
		t.Fatalf("Zeile bei Caddy: %v: %q", err, line)
	}
	if l.KephLogin == nil {
		return ""
	}
	return *l.KephLogin
}

// forwarded ist h mit X-Forwarded-For, wie der Proxy es setzt — für eine
// Anfrage direkt an den Node, die der Node für eine über den Proxy hält.
func forwarded(h http.Header) http.Header {
	return merge(h, http.Header{"X-Forwarded-For": {"203.0.113.9"}})
}

// Der Antwort-Header X-Keph-Login: invalid (Task 025): Der Node setzt ihn
// auf jede Anfrage mit X-Forwarded-For und mindestens einem ungültigen Paar
// zu einem Hub, den er kennt — falsches Token, unbekannter Account, Hub ohne
// Replica, halbes Paar, auch neben einem gültigen Paar, bei initialize wie
// bei einem Werkzeug. Nicht ohne Paar, nicht mit Paaren nur zu unbekannten
// Aliasen, nicht bei gültiger Anmeldung an allen Hubs der Anfrage — und ohne
// X-Forwarded-For nie, auch nicht mit falschem Token; dort bleibt
// login=invalid im Log. Über den Proxy erreicht er den Client nie: Caddy
// schreibt ihn als keph_login in seine Zeile und nimmt ihn aus der Antwort.
func TestProxyLoginHeader(t *testing.T) {
	e := newProxyEnv(t, "lager", "werkstatt", "zentrale")
	cases := []struct {
		name   string
		header http.Header
		want   string
	}{
		{"ohne Paar", nil, ""},
		{"gültig", e.valid("zentrale"), ""},
		{"gültig an zwei Hubs", merge(e.valid("zentrale"), e.valid("werkstatt")), ""},
		{"unbekannter Alias", e.wrong(t, "nirgends"), ""},
		{"gültig und unbekannter Alias", merge(e.valid("zentrale"), e.wrong(t, "nirgends")), ""},
		{"falsches Token", e.wrong(t, "zentrale"), LoginInvalid},
		{"unbekannter Account", e.unknown(t, "zentrale"), LoginInvalid},
		{"Hub ohne Replica", e.wrong(t, "lager"), LoginInvalid},
		{"gültig und falsch", merge(e.valid("zentrale"), e.wrong(t, "werkstatt")), LoginInvalid},
		{"halbes Paar", http.Header{"X-Keph-Account-Zentrale": {"anna"}}, LoginInvalid},
	}
	for _, c := range cases {
		for _, body := range []string{initializeBody, whoamiBody} {
			// Der Node setzt ihn (direkt, mit X-Forwarded-For wie vom Proxy).
			if got := loginHeader(t, e.direct, forwarded(c.header), body); got != c.want {
				t.Errorf("mit X-Forwarded-For, %s: %s=%q, erwartet %q", c.name, LoginHeader, got, c.want)
			}
			// Über den Proxy: nie in der Antwort, in der Zeile bei Caddy.
			if got := loginHeader(t, e.proxy, c.header, body); got != "" {
				t.Errorf("über den Proxy, %s: %s=%q in der Antwort", c.name, LoginHeader, got)
			}
			if got := kephLogin(t, lastCaddyLine(e.caddy)); got != c.want {
				t.Errorf("über den Proxy, %s: keph_login=%q, erwartet %q", c.name, got, c.want)
			}
		}
	}
	// Lokal nie, auch nicht bei einem Fehlversuch; login=invalid im Log
	// bleibt (je Fall einmal).
	before := strings.Count(e.log.String(), "login=invalid")
	for _, c := range cases {
		if got := loginHeader(t, e.direct, c.header, whoamiBody); got != "" {
			t.Errorf("lokal, %s: %s=%q", c.name, LoginHeader, got)
		}
	}
	if n := strings.Count(e.log.String(), "login=invalid") - before; n != 5 {
		t.Errorf("lokal %d-mal login=invalid im Log, erwartet 5:\n%s", n, e.log.String())
	}
}

// Fenster nach rotate (Task 025, Etappe 3): Wird über den Proxy ein Paar zu
// einem Account abgewiesen, den die Replica kennt, stößt der Node den
// Abgleich dieses Hub-Eintrags an (HubLink.Recheck) — die Antwort bleibt,
// wie sie ist. Nach rotate am Hub prüft der Node das neue Token gegen die
// alte Zeile; nach dem angestoßenen Abgleich gilt es. Kein Anstoß ohne
// Paar, bei gültiger Anmeldung, unbekanntem Alias, unbekanntem Account,
// halbem Paar, Hub ohne Replica — und lokal nie.
func TestProxyRecheckAfterRotate(t *testing.T) {
	e := newProxyEnv(t, "lager", "werkstatt", "zentrale")
	for name, h := range map[string]http.Header{
		"ohne Paar":           nil,
		"gültig":              e.valid("zentrale"),
		"unbekannter Alias":   e.wrong(t, "nirgends"),
		"unbekannter Account": e.unknown(t, "zentrale"),
		"halbes Paar":         {"X-Keph-Account-Zentrale": {"anna"}},
		"Hub ohne Replica":    e.wrong(t, "lager"),
	} {
		e.call(t, e.proxy, h, "whoami", nil)
		if r := e.takeRechecks(); len(r) != 0 {
			t.Errorf("über den Proxy, %s: Anstoß %v", name, r)
		}
	}
	// Lokal nie, auch nicht bei falschem Token zu einem bekannten Account.
	e.call(t, e.direct, e.wrong(t, "zentrale"), "whoami", nil)
	if r := e.takeRechecks(); len(r) != 0 {
		t.Errorf("lokal: Anstoß %v", r)
	}
	// Je Anfrage ein Anstoß je betroffenem Hub (der SDK-Client schickt je
	// Aufruf mehrere Anfragen, deshalb hier rohe POSTs): falsches Token zu
	// einem bekannten Account, auch neben einem gültigen Paar, auch bei
	// initialize.
	whoamiVia := func(h http.Header) (string, []string) {
		t.Helper()
		status, body := post(t, e.proxy, h, whoamiBody)
		if status != http.StatusOK {
			t.Fatalf("whoami: HTTP %d %s", status, body)
		}
		return body, e.takeRechecks()
	}
	if _, r := whoamiVia(e.wrong(t, "zentrale")); !slices.Equal(r, []string{"zentrale"}) {
		t.Errorf("falsches Token: Anstoß %v", r)
	}
	if _, r := whoamiVia(merge(e.valid("zentrale"), e.wrong(t, "werkstatt"))); !slices.Equal(r, []string{"werkstatt"}) {
		t.Errorf("gültig und falsch: Anstoß %v", r)
	}
	serverVersion(t, e.proxy, merge(e.wrong(t, "zentrale"), e.wrong(t, "werkstatt")))
	if r := e.takeRechecks(); !slices.Equal(r, []string{"werkstatt", "zentrale"}) {
		t.Errorf("initialize mit zwei falschen: Anstoß %v", r)
	}

	// rotate am Hub: Die Replica hat noch die alte Zeile. Das neue Token ist
	// ungültig und stößt an; nach dem Abgleich gilt es, das alte nicht mehr.
	next := token(t)
	for _, c := range []string{"privat", "wissen"} {
		e.hubs["zentrale"].grant("anna", c, next, contract.Rights{Write: true})
	}
	fresh := pair("zentrale", "anna", next)
	body, r := whoamiVia(fresh)
	if !strings.Contains(body, `"hidden":true`) {
		t.Fatalf("neues Token vor dem Abgleich: %s", body)
	}
	if !slices.Equal(r, []string{"zentrale"}) {
		t.Errorf("neues Token vor dem Abgleich: Anstoß %v", r)
	}
	e.sync(t)
	body, r = whoamiVia(fresh)
	if strings.Contains(body, `"hidden":true`) || !strings.Contains(body, `"hub":"zentrale","login":"ok"`) {
		t.Fatalf("neues Token nach dem Abgleich: %s", body)
	}
	if len(r) != 0 {
		t.Errorf("neues Token nach dem Abgleich: Anstoß %v", r)
	}
	// Eine laufende Verbindung mit dem alten Token: ungültig, bekannt — ein
	// Anstoß je Anfrage (die Drosselung liegt beim Empfänger).
	if _, r := whoamiVia(e.valid("zentrale")); !slices.Equal(r, []string{"zentrale"}) {
		t.Errorf("altes Token nach dem Abgleich: Anstoß %v", r)
	}
}
