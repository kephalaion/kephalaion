package mcpnode

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

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
// Nachbau des Proxys: httputil.ReverseProxy setzt Host auf den Node und
// X-Forwarded-For neu, einen mitgeschickten verwirft er — wie Caddy.
type proxyEnv struct {
	nodes  store.Store
	hubs   map[string]*docHub
	tokens map[string]string
	direct string
	proxy  string
	log    *lockedBuffer
	// docID ist die id von wissen/a.md an zentrale.
	docID string
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
	s := &replica.Syncer{Nodes: nodes}
	if _, err := s.Sync(ctx, "", func(h store.Hub) (contract.Hub, error) {
		if f := e.hubs[h.Name]; f != nil {
			return f, nil
		}
		return nil, fmt.Errorf("nicht erreichbar")
	}); err != nil {
		t.Fatal(err)
	}
	// lager hat nie abgeglichen: Die Replica fehlt.
	_ = os.Remove(nodes.ReplicaPath("lager"))
	e.log = &lockedBuffer{}
	link := HubLink{
		Connect: func(_ context.Context, h store.Hub) (contract.Hub, func(), error) {
			return e.hubs[h.Name], func() {}, nil
		},
		Sync: func(store.Hub) {},
	}
	node := httptest.NewServer(reqlog.New(e.log).Middleware("node",
		NewHandler(nodes, proxyVersion, func() upgrade.Report { return proxyUpdate }, link)))
	t.Cleanup(node.Close)
	e.direct = node.URL
	target, _ := url.Parse(node.URL)
	proxy := httptest.NewServer(&httputil.ReverseProxy{Rewrite: func(pr *httputil.ProxyRequest) {
		pr.SetURL(target)
		pr.SetXForwarded()
	}})
	t.Cleanup(proxy.Close)
	e.proxy = proxy.URL
	return e
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

// failLine ist der Filter der Jail für Fehlversuche, wie er auf dem Rechner
// des Proxys steht: Zeile des Node-Listeners an /mcp, via an fester Stelle
// hinter der Dauer und direkt danach login=invalid.
var failLine = regexp.MustCompile(`^\S+ node \S+ /mcp \d{3} \S+ via=(\S+) login=invalid(?: |$)`)

// Fehlversuche im Log: Jede Anfrage mit mindestens einem ungültigen
// Header-Paar trägt login=invalid, einmal, direkt hinter via; ohne
// Header-Paar, gültig oder nur mit unbekanntem Alias nicht. Lokal steht der
// Vermerk ohne via — die Jail zählt nur Zeilen mit via.
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
		before := len(e.log.String())
		post(t, c.base, c.header, initializeBody)
		lines := strings.Split(strings.TrimSpace(e.log.String()[before:]), "\n")
		if len(lines) != 1 {
			t.Fatalf("%s: %d Zeilen: %q", c.name, len(lines), lines)
		}
		line := lines[0]
		// Ohne die Zeit vorn, wie fail2ban sie bekommt.
		_, msg, _ := strings.Cut(line, " ")
		m := failLine.FindStringSubmatch("- " + msg)
		switch {
		case strings.Count(line, "login=invalid") != map[bool]int{false: 0, true: 1}[c.failed]:
			t.Errorf("%s: %s", c.name, line)
		case c.failed && c.base == e.proxy && (m == nil || m[1] != "127.0.0.1"):
			t.Errorf("%s: der Filter trifft nicht: %s", c.name, line)
		case (!c.failed || c.base == e.direct) && m != nil:
			t.Errorf("%s: der Filter trifft: %s", c.name, line)
		}
	}
}

// Ein Account-Name, der ein falsches via einschleusen will, steht maskiert in
// der Zeile; ein mitgeschicktes X-Forwarded-For verwirft der Proxy. Die
// Zeile trägt nur das via des Proxys.
func TestFailedLoginLogNotForgeable(t *testing.T) {
	e := newProxyEnv(t, "zentrale")
	h := http.Header{
		"X-Keph-Account-Zentrale": {"x via=6.6.6.6 login=invalid"},
		"X-Keph-Token-Zentrale":   {token(t)},
		"X-Forwarded-For":         {"6.6.6.6"},
	}
	before := len(e.log.String())
	post(t, e.proxy, h, initializeBody)
	line := strings.TrimSpace(e.log.String()[before:])
	if strings.Contains(line, "6.6.6.6") || strings.Count(line, "via=") != 1 ||
		!strings.Contains(line, "via=127.0.0.1 login=invalid account=(ungültig)") {
		t.Errorf("Zeile: %s", line)
	}
	_, msg, _ := strings.Cut(line, " ")
	if m := failLine.FindStringSubmatch("- " + msg); m == nil || m[1] != "127.0.0.1" {
		t.Errorf("Filter: %v in %s", m, line)
	}
	// Ohne Proxy gilt das X-Forwarded-For eines lokalen Prozesses: Er
	// verdeckt nur sich selbst, kann aber eine Zeile mit fremdem via
	// erzeugen — eine Grenze, die konzept.md nennt (wer auf dem Rechner
	// des Nodes ist, kann eine Adresse sperren lassen).
	before = len(e.log.String())
	post(t, e.direct, h, initializeBody)
	if line := e.log.String()[before:]; !strings.Contains(line, "via=6.6.6.6 login=invalid account=(ungültig)") {
		t.Errorf("lokal: %s", line)
	}
}
