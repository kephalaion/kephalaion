package main

import (
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kephalaion/kephalaion/internal/assistant"
	"github.com/kephalaion/kephalaion/internal/testcert"
)

// fakeNodeServer antwortet auf initialize unter /mcp wie ein Node über einen
// Proxy ohne Anmeldung (Version verdeckt); alles andere 404.
func fakeNodeServer(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mcp" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-06-18","capabilities":{},`+
			`"serverInfo":{"name":"kephalaion","version":""}}}`)
	}))
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://")
}

// remoteHelper ist die Kommandozeile des Helfers eines Eintrags mit der Wahl
// der Hubs hubs und der Accounts accounts (hub=account).
func (e *mcpEnv) remoteHelper(hubs []string, accounts ...string) string {
	s := testBinary + " node mcp headers --tokens-dir " + e.tokens
	for _, h := range hubs {
		s += " --hub " + h
	}
	for _, a := range accounts {
		s += " --account " + a
	}
	return s
}

// node mcp add|status --node (Task 023) auf einem Rechner ohne eigene config:
// eingetragen wird die https-Adresse, der Helfer trägt die Wahl der Hubs
// (--hub), OpenCode verweist nur auf deren Token-Datei. Kommt danach ein Hub
// unter tokens/ hinzu, bleiben Eintrag und Ausgabe des Helfers gleich; add
// ohne --hub bricht dann ab und schreibt nichts.
func TestNodeMCPRemote(t *testing.T) {
	e := newMCPEnv(t, assistant.Claude, assistant.OpenCode, assistant.Codex)
	if err := os.Remove(e.cfg); err != nil {
		t.Fatal(err)
	}
	e.put(t, "vm", "kamran.token", dummyTokenA+"\n")
	ca := testcert.NewCA(t, "Proxy-CA")
	caPath := filepath.Join(e.home, "ca.pem")
	writeText(t, caPath, ca.PEM)
	proxy := newProxyNode(t, fakeNodeServer(t), serverCert(ca.ServerNow(t, "127.0.0.1")), "/kephalaion", "", proxyLogin)
	base := proxy.URL + "/kephalaion"
	url := base + "/mcp"

	// Ohne eigene config: status und add ohne --node nennen --node.
	e.mcp(t, "status").want(t, 1, "kein Node in der config", "--node https://<name>/<präfix>")
	r := e.mcp(t, "add", "--node", base, "--ca-file", caPath)
	r.want(t, 0, "Node erreichbar: "+url, "claude: eingetragen: "+url+", vm=kamran",
		"opencode: eingetragen: "+url+", vm=kamran", "codex: eingetragen: "+url+", vm=kamran")
	if strings.Contains(r.out, "Hinweis: Dieser Rechner hat einen eigenen Node") {
		t.Errorf("Hinweis ohne eigenen Node:\n%s", r.out)
	}
	helper := e.remoteHelper([]string{"vm"}, "vm=kamran")
	if entry := e.claudeEntry(t); entry["url"] != url || entry["headersHelper"] != helper {
		t.Fatalf("Claude Code: %v", entry)
	}
	if text := e.codexText(t); !strings.Contains(text, `url = "`+url+`"`) ||
		!strings.Contains(text, `http_headers_helper = "`+helper+`"`) {
		t.Fatalf("Codex:\n%s", text)
	}
	opencode := e.opencodeEntry(t)
	if h, _ := opencode["headers"].(map[string]any); opencode["url"] != url || len(h) != 2 ||
		h["X-Keph-Token-vm"] != e.ref("vm", "kamran") {
		t.Fatalf("OpenCode: %v", opencode)
	}
	e.mcp(t, "status", "--node", base, "--ca-file", caPath).want(t, 0, "Node: "+url+" — erreichbar (Hubs: vm)",
		"claude: eingetragen (vm=kamran)", "opencode: eingetragen (vm=kamran)", "codex: eingetragen (vm=kamran)")
	e.mcp(t, "status", "--node", proxy.URL+"/anders", "--ca-file", caPath).want(t, 1, "Präfix falsch oder Anmeldung des Proxys",
		"claude: weicht ab — andere Adresse")
	// Der Helfer gibt nur das Paar von vm aus.
	got, _ := e.headers(t, "--tokens-dir", e.tokens, "--hub", "vm", "--account", "vm=kamran")
	if len(got) != 2 || got["X-Keph-Account-vm"] != "kamran" {
		t.Fatalf("Helfer: %v", keysOf(got))
	}

	// Ein zweiter Hub kommt hinzu: Eintrag und Ausgabe des Helfers bleiben.
	e.put(t, "eigen", "kp.token", dummyTokenB+"\n")
	writes := len(e.fake.Writes())
	got, _ = e.headers(t, "--tokens-dir", e.tokens, "--hub", "vm", "--account", "vm=kamran")
	if len(got) != 2 || got["X-Keph-Account-eigen"] != "" {
		t.Errorf("Helfer nach neuem Hub: %v", keysOf(got))
	}
	e.mcp(t, "status", "--node", base, "--ca-file", caPath, "--hub", "vm").want(t, 0, "claude: eingetragen (vm=kamran)",
		"opencode: eingetragen (vm=kamran)", "codex: eingetragen (vm=kamran)")
	e.mcp(t, "add", "--node", base, "--ca-file", caPath, "--hub", "vm").want(t, 0, "claude: unverändert",
		"opencode: unverändert", "codex: unverändert")
	// Ohne Wahl bei zwei Hubs: Abbruch, nichts geschrieben.
	e.mcp(t, "add", "--node", base, "--ca-file", caPath).want(t, 2, "mehrere Hubs unter", "(eigen, vm)", "--hub <alias>")
	e.mcp(t, "status", "--node", base, "--ca-file", caPath).want(t, 2, "mehrere Hubs unter")
	if w := e.fake.Writes(); len(w) != writes {
		t.Errorf("geschrieben: %v", w[writes:])
	}
	// Beide gewählt: beide Paare, in fester Reihenfolge.
	e.mcp(t, "add", "--node", base, "--ca-file", caPath, "--hub", "vm", "--hub", "eigen").want(t, 0,
		"claude: eingetragen, ersetzt den Eintrag (anderer Helfer): "+url+", eigen=kp, vm=kamran")
	if entry := e.claudeEntry(t); entry["headersHelper"] != e.remoteHelper([]string{"eigen", "vm"}, "eigen=kp", "vm=kamran") {
		t.Errorf("zwei Hubs: %v", entry)
	}
	// Ein gewählter Hub ohne Token-Datei wird genannt.
	e.mcp(t, "add", "--node", base, "--ca-file", caPath, "--hub", "vm", "--hub", "fehlt").want(t, 1,
		"Hub fehlt übergangen: gewählt, aber keine Token-Datei")
}

// Erst prüfen, dann eintragen: Zertifikatsfehler, Gegenseite ohne TLS, 401,
// 302 und HTML des Proxys, nicht erreichbar, http zu einem fremden Host —
// add schreibt nichts, status meldet den Fall. Falsche Kombinationen sind
// falsche Aufrufe.
func TestNodeMCPRemoteCheckFirst(t *testing.T) {
	e := newMCPEnv(t, assistant.Claude, assistant.OpenCode, assistant.Codex)
	e.put(t, "vm", "kamran.token", dummyTokenA+"\n")
	node := fakeNodeServer(t)
	ca, other := testcert.NewCA(t, "Proxy-CA"), testcert.NewCA(t, "Andere CA")
	caPath, otherPath := filepath.Join(e.home, "ca.pem"), filepath.Join(e.home, "andere.pem")
	writeText(t, caPath, ca.PEM)
	writeText(t, otherPath, other.PEM)
	cert := serverCert(ca.ServerNow(t, "127.0.0.1"))
	good := newProxyNode(t, node, cert, "/kephalaion", "", proxyLogin)
	redirect := newProxyNode(t, node, cert, "/kephalaion", "", proxyRedirect)
	html := newProxyNode(t, node, cert, "/kephalaion", "", proxyHTML)
	cases := []struct {
		name string
		args []string
		code int
		want string
	}{
		{"ohne CA", []string{"--node", good.URL + "/kephalaion"}, 1, "Zertifikat von 127.0.0.1 nicht vertraut (Aussteller CN=Proxy-CA; --ca-file?)"},
		{"falsche CA", []string{"--node", good.URL + "/kephalaion", "--ca-file", otherPath}, 1, "passt die CA aus --ca-file?"},
		{"ohne TLS", []string{"--node", "https://" + node}, 1, "die Gegenseite spricht kein TLS"},
		{"401", []string{"--node", good.URL + "/falsch", "--ca-file", caPath}, 1, "eine Anmeldung des Proxys, nicht der Node (HTTP 401"},
		{"302", []string{"--node", redirect.URL + "/falsch", "--ca-file", caPath}, 1, "eine Weiterleitung (HTTP 302"},
		{"HTML", []string{"--node", html.URL + "/falsch", "--ca-file", caPath}, 1, "keine Antwort eines MCP-Servers (HTTP 200, text/html"},
		{"nicht erreichbar", []string{"--node", "https://" + closedAddress(t) + "/kephalaion"}, 1, "nicht erreichbar"},
		{"http fremd", []string{"--node", "http://node.example.org:7433"}, 2, "http nur zu diesem Rechner"},
	}
	for _, c := range cases {
		calls := len(e.fake.Calls)
		r := e.mcp(t, append([]string{"add"}, c.args...)...)
		r.want(t, c.code, c.want)
		if c.code == 1 && !strings.Contains(r.errOut, "Nichts eingetragen.") {
			t.Errorf("%s: %s", c.name, r.errOut)
		}
		if len(e.fake.Calls) != calls {
			t.Errorf("%s: add rief Assistenten auf: %v", c.name, e.fake.Calls[calls:])
		}
		if strings.Contains(c.want, "Präfix") || c.name == "401" || c.name == "302" || c.name == "HTML" {
			if !strings.Contains(r.errOut, "Präfix falsch oder Anmeldung des Proxys") {
				t.Errorf("%s ohne Hinweis auf Präfix oder Anmeldung: %s", c.name, r.errOut)
			}
		}
		st := e.mcp(t, append([]string{"status"}, c.args...)...)
		st.want(t, c.code, c.want)
	}
	for _, p := range []*proxyNode{good, redirect, html} {
		if p.tokens.Load() != 0 {
			t.Error("ein Token ging an den Proxy")
		}
	}
	if e.claudeEntry(t) != nil || e.opencodeEntry(t) != nil || e.codexText(t) != "" {
		t.Fatal("ein Eintrag entstand trotz gescheiterter Prüfung")
	}

	// Falsche Aufrufe.
	e.mcp(t, "add", "--hub", "vm").want(t, 2, "--ca-file und --hub nur zusammen mit --node")
	e.mcp(t, "add", "--ca-file", caPath).want(t, 2, "nur zusammen mit --node")
	e.mcp(t, "add", "--auto", "--node", good.URL+"/kephalaion").want(t, 2, "--auto")
	e.mcp(t, "add", "--node", "http://127.0.0.1:7433", "--hub", "vm").want(t, 2, "--hub nur mit einer entfernten Adresse")
	e.mcp(t, "add", "--node", "http://127.0.0.1:7433", "--ca-file", caPath).want(t, 2, "--ca-file gibt es nur mit einer https-Adresse")
	e.mcp(t, "add", "--node", good.URL+"/kephalaion", "--hub", "System").want(t, 2, "--hub \"System\"")
	e.mcp(t, "add", "--node", good.URL+"/kephalaion/mcp").want(t, 2, "ohne /mcp am Ende")
	e.mcp(t, "add", "--node", good.URL+"/kephalaion", "--ca-file", filepath.Join(e.home, "fehlt.pem")).want(t, 1, "--ca-file")
}

// Auf einem Rechner mit eigenem Node: --node gilt bis zum nächsten
// automatischen Anstoß — add nennt es, status ohne --node meldet „weicht
// ab“, add --auto setzt die lokale Adresse wieder ein. Ohne Wahl der Hubs
// bricht add auch hier ab, wenn unter tokens/ zwei liegen.
func TestNodeMCPRemoteWithOwnNode(t *testing.T) {
	e := newMCPEnv(t, assistant.Claude)
	e.put(t, "vm", "kamran.token", dummyTokenA+"\n")
	e.put(t, "eigen", "kp.token", dummyTokenB+"\n")
	ca := testcert.NewCA(t, "Proxy-CA")
	caPath := filepath.Join(e.home, "ca.pem")
	writeText(t, caPath, ca.PEM)
	proxy := newProxyNode(t, fakeNodeServer(t), serverCert(ca.ServerNow(t, "127.0.0.1")), "/kephalaion", "", nil)
	base := proxy.URL + "/kephalaion"
	e.mcp(t, "add").want(t, 0, "claude: eingetragen: "+mcpURL+", eigen=kp, vm=kamran")
	e.mcp(t, "add", "--node", base, "--ca-file", caPath).want(t, 2, "mehrere Hubs unter")
	r := e.mcp(t, "add", "--node", base, "--ca-file", caPath, "--hub", "vm")
	r.want(t, 0, "claude: eingetragen, ersetzt den Eintrag (andere Adresse", base+"/mcp, vm=kamran",
		"Hinweis: Dieser Rechner hat einen eigenen Node ("+e.cfg+")")
	e.mcp(t, "status").want(t, 0, "claude: weicht ab — andere Adresse ("+base+"/mcp statt "+mcpURL+")")
	e.mcp(t, "add", "--auto").want(t, 0, "claude: eingetragen, ersetzt den Eintrag")
	if entry := e.claudeEntry(t); entry["url"] != mcpURL || entry["headersHelper"] != e.helper("eigen=kp", "vm=kamran") {
		t.Errorf("nach --auto: %v", entry)
	}
	// Eine lokale Adresse über --node ist keine entfernte: alle Hubs, ohne
	// Prüfung der Wahl.
	e.mcp(t, "status", "--node", "http://127.0.0.1:"+strings.Split(fakeNodeServer(t), ":")[1]).want(t, 0,
		"claude: weicht ab — andere Adresse")
}

func keysOf(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
