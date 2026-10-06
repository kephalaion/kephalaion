package main

import (
	"bytes"
	"context"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/kephalaion/kephalaion/internal/assistant"
	"github.com/kephalaion/kephalaion/internal/assistant/assistanttest"
	"github.com/kephalaion/kephalaion/internal/config"
	"github.com/kephalaion/kephalaion/internal/ident"
	"github.com/kephalaion/kephalaion/internal/node/mcpnode"
	"github.com/kephalaion/kephalaion/internal/testcert"
)

// setupEnv ist commEnv mit serve (Hub und Node, sync_interval 0), einer
// config, die den Node mit dem Port von serve nennt, und Claude Code als
// nachgespieltem Assistenten. secrets sammelt alle Tokens, outputs alle
// Ausgaben — keine darf ein ganzes Token nennen.
type setupEnv struct {
	*commEnv
	srv       *running
	nodeAddr  string
	nodeURL   string
	tokensDir string
	fake      *assistanttest.Fake
	secrets   []string
	outputs   []string
}

func newSetupEnv(t *testing.T) *setupEnv {
	t.Helper()
	e := &setupEnv{commEnv: newCommEnv(t)}
	e.run(t, "config", "set", "node", "sync_interval", "0").want(t, 0)
	e.srv = startServe(t, portZero(t, e.cfg))
	e.nodeAddr = e.srv.addrs[config.Node]
	e.nodeURL = "http://" + e.nodeAddr
	cfg, _, err := config.Load(e.cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Node.Listen = e.nodeAddr
	if err := config.Save(e.cfg, cfg); err != nil {
		t.Fatal(err)
	}
	e.tokensDir = filepath.Join(e.dir, "config", "kephalaion", "tokens")
	e.fake = withAssistants(t, e.dir, assistant.Claude)
	for _, tok := range e.tokens {
		e.secrets = append(e.secrets, tok)
	}
	t.Cleanup(func() { e.noSecrets(t) })
	return e
}

// account legt einen Account mit write in team-x an und liefert sein
// Einrichtungstoken.
func (e *setupEnv) account(t *testing.T, name string) string {
	t.Helper()
	r := e.run(t, "hub", "account", "add", name)
	r.want(t, 0)
	tok := tokenFrom(t, r.out)
	e.run(t, "hub", "account", "grant", name, "team-x", "--write").want(t, 0)
	e.secrets = append(e.secrets, tok)
	return tok
}

// newToken ist ein Token, das am Hub nichts gilt.
func (e *setupEnv) newToken(t *testing.T) string {
	t.Helper()
	tok, err := ident.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	e.secrets = append(e.secrets, tok)
	return tok
}

// setup ruft node account setup mit der config der Umgebung auf.
func (e *setupEnv) setup(t *testing.T, stdin string, args ...string) result {
	t.Helper()
	return e.setupWith(t, strings.NewReader(stdin), append(args, e.c)...)
}

// setupWith ruft node account setup mit diesem stdin und ohne config auf.
func (e *setupEnv) setupWith(t *testing.T, stdin io.Reader, args ...string) result {
	t.Helper()
	var out, errOut bytes.Buffer
	code := run(append([]string{"node", "account", "setup"}, args...), stdin, &out, &errOut)
	r := result{code, out.String(), errOut.String()}
	e.outputs = append(e.outputs, r.out, r.errOut)
	if os.Getenv("SHOW_SETUP") != "" {
		t.Logf("exit %d\n%s%s", r.code, r.out, r.errOut)
	}
	return r
}

func (e *setupEnv) file(hub, account string) string {
	return filepath.Join(e.tokensDir, hub, account+".token")
}

// rotateDirect rotiert am Node vorbei an setup — wie ein rotate, dessen
// Antwort verloren ging.
func (e *setupEnv) rotateDirect(t *testing.T, hub, account, old, next string) {
	t.Helper()
	status, body := postAccount(t, http.DefaultClient, e.nodeURL+mcpnode.AccountRotatePath, hub, account, old,
		`{"new_hash":"`+ident.HashToken(next)+`"}`)
	if status != http.StatusOK {
		t.Fatalf("rotate: HTTP %d %s", status, body)
	}
}

// noSecrets prüft, dass keine Ausgabe ein ganzes Token nennt — auch keines
// aus den Token-Dateien.
func (e *setupEnv) noSecrets(t *testing.T) {
	t.Helper()
	secrets := append([]string{}, e.secrets...)
	_ = filepath.WalkDir(e.tokensDir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if data, err := os.ReadFile(p); err == nil && strings.TrimSpace(string(data)) != "" {
				secrets = append(secrets, strings.TrimSpace(string(data)))
			}
		}
		return nil
	})
	for _, out := range e.outputs {
		for _, s := range secrets {
			if strings.Contains(out, s) {
				t.Errorf("ganzes Token in einer Ausgabe:\n%s", strings.ReplaceAll(out, s, "<TOKEN>"))
			}
		}
	}
}

// wantMode verlangt eine Datei mit 0600.
func wantMode(t *testing.T, path string) {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("%s: %v, erwartet 0600", path, fi.Mode().Perm())
	}
}

// failReader meldet, ob jemand stdin gelesen hat.
type failReader struct{ read atomic.Bool }

func (f *failReader) Read([]byte) (int, error) {
	f.read.Store(true)
	return 0, io.EOF
}

// setup lokal, mit dem Node aus der config, direkt nach hub account add und
// grant (kein Abgleich): Token als Argument über local zum Hub, über stdin
// über HTTP; je Schritt eine Zeile, Tokens nur gekürzt; die Token-Datei mit
// 0600, keine .pending; der Node bei Claude Code eingetragen wie --auto; MCP
// nimmt das neue Token sofort. Ein zweiter Aufruf bricht ab, bevor er das
// Token liest.
func TestNodeAccountSetupLocal(t *testing.T) {
	e := newSetupEnv(t)
	tok := e.account(t, "neu")
	r := e.setup(t, "", "eigen", "neu", tok)
	r.want(t, 0, "Node: "+e.nodeURL, "Node erreichbar.", "Einrichtungstoken für eigen/neu: "+ident.MaskToken(tok),
		"Neues Token erzeugt (keph_…", ", vorgemerkt in "+e.file("eigen", "neu")+".pending (0600)",
		"Rotiere das Token über den Node …", "Token rotiert: Account neu (User neu), Collections team-x",
		"Token in "+e.file("eigen", "neu")+" gespeichert (0600).", "Trage den Node bei den KI-Assistenten ein",
		"claude: eingetragen: "+e.nodeURL+"/mcp, eigen=neu")
	wantMode(t, e.file("eigen", "neu"))
	if exists(e.file("eigen", "neu") + ".pending") {
		t.Error(".pending liegt nach Erfolg noch")
	}
	next := readFileToken(t, e.file("eigen", "neu"))
	if next == tok {
		t.Fatal("das Einrichtungstoken steht in der Token-Datei")
	}
	who := mcpWhoami(t, e.nodeURL+"/mcp", map[string][2]string{"eigen": {"neu", next}})
	if len(who.Hubs) == 0 || who.Hubs[0].Hub != "eigen" || who.Hubs[0].Login != mcpnode.LoginOK {
		t.Errorf("MCP mit dem neuen Token: %+v", who.Hubs)
	}
	if entry := e.fake.Writes(); len(entry) == 0 {
		t.Error("nichts bei Claude Code eingetragen")
	}

	// Über stdin, Hub über HTTP.
	tok2 := e.account(t, "neu2")
	e.setup(t, tok2+"\n", "fern", "neu2", "--token-stdin").want(t, 0,
		"Einrichtungstoken für fern/neu2: "+ident.MaskToken(tok2), "Token rotiert: Account neu2 (User neu2)",
		"Token in "+e.file("fern", "neu2")+" gespeichert (0600).")
	wantMode(t, e.file("fern", "neu2"))

	// Die Token-Datei gibt es schon: Abbruch, bevor stdin gelesen wird.
	fr := &failReader{}
	e.setupWith(t, fr, "eigen", "neu", "--token-stdin", e.c).want(t, 1, e.file("eigen", "neu")+" gibt es schon",
		"kephalaion node account list")
	if fr.read.Load() {
		t.Error("stdin gelesen, obwohl die Token-Datei schon liegt")
	}
	if got := readFileToken(t, e.file("eigen", "neu")); got != next {
		t.Error("die Token-Datei wurde ersetzt")
	}
}

// setup über einen entfernten Node (https mit Test-CA, ohne eigene config):
// ohne --node Exit 2; mit --node und --ca-file eingerichtet und wie node mcp
// add --node … --hub <hub> eingetragen. Das neue Token gilt am Node.
func TestNodeAccountSetupRemote(t *testing.T) {
	e := newSetupEnv(t)
	tok := e.account(t, "mac")
	ca := testcert.NewCA(t, "Proxy-CA")
	caPath := caFile(t, e.dir, "ca.pem", ca.PEM)
	proxy := newProxyNode(t, e.nodeAddr, serverCert(ca.ServerNow(t, "127.0.0.1")), "/kephalaion", "", proxyLogin)
	base := proxy.URL + "/kephalaion"

	fr := &failReader{}
	e.setupWith(t, fr, "eigen", "mac", "--token-stdin").want(t, 2, "kein Node in der config", "--node <url>")
	e.setupWith(t, fr, "eigen", "mac", "--token-stdin", "--ca-file", caPath).want(t, 2, "--ca-file nur zusammen mit --node")
	e.setupWith(t, fr, "eigen", "mac", "--token-stdin", "--node", "http://node.example.org").want(t, 2,
		"http nur zu diesem Rechner")
	// Ohne --ca-file: Zertifikat nicht vertraut, vor dem Token.
	e.setupWith(t, fr, "eigen", "mac", "--token-stdin", "--node", base).want(t, 1, "nicht vertraut",
		"das Einrichtungstoken gilt weiter")
	if fr.read.Load() {
		t.Error("stdin gelesen vor der Prüfung")
	}
	if exists(e.file("eigen", "mac") + ".pending") {
		t.Error(".pending ohne rotate")
	}

	r := e.setupWith(t, strings.NewReader(""), "eigen", "mac", tok, "--node", base, "--ca-file", caPath)
	r.want(t, 0, "Node: "+base, "Token rotiert: Account mac (User mac), Collections team-x",
		"claude: eingetragen: "+base+"/mcp, eigen=mac")
	if entry := e.fake.Writes(); len(entry) == 0 {
		t.Fatal("nichts eingetragen")
	}
	helper := testBinary + " node mcp headers --tokens-dir " + e.tokensDir + " --hub eigen --account eigen=mac"
	if got := claudeHelper(t, e.fake); got != helper {
		t.Errorf("Helfer %q, erwartet %q", got, helper)
	}
	wantMode(t, e.file("eigen", "mac"))
	next := readFileToken(t, e.file("eigen", "mac"))
	status, _ := postAccount(t, http.DefaultClient, e.nodeURL+mcpnode.AccountCheckPath, "eigen", "mac", next, "")
	if status != http.StatusOK {
		t.Errorf("das neue Token gilt nicht: HTTP %d", status)
	}
}

// claudeHelper liest den Helfer des Eintrags kephalaion bei Claude Code.
func claudeHelper(t *testing.T, fake *assistanttest.Fake) string {
	t.Helper()
	e := &mcpEnv{fake: fake}
	entry := e.claudeEntry(t)
	h, _ := entry["headersHelper"].(string)
	return h
}

// Eine liegende .pending klärt setup über die Route zum Prüfen, ohne zu
// rotieren: gilt sie, wird sie die Token-Datei (das Einrichtungstoken wird
// nicht gelesen); gilt sie nicht, aber das Einrichtungstoken, wird sie
// gelöscht (Exit 3, danach geht setup); gilt keins, bleibt sie mit Hinweis
// auf den Admin; über einen entfernten Node „ungültig oder unbekannt“:
// bleibt; nicht geprüft: bleibt.
func TestNodeAccountSetupPending(t *testing.T) {
	e := newSetupEnv(t)
	putPending := func(hub, account, tok string) string {
		t.Helper()
		p := e.file(hub, account) + ".pending"
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(tok+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}

	// Gilt: übernommen, ohne das Einrichtungstoken zu lesen.
	s1, n1 := e.account(t, "p1"), e.newToken(t)
	e.rotateDirect(t, "eigen", "p1", s1, n1)
	pend := putPending("eigen", "p1", n1)
	fr := &failReader{}
	e.setupWith(t, fr, "eigen", "p1", "--token-stdin", e.c).want(t, 0, "kein rotate",
		"Neues Token aus "+pend+" ("+ident.MaskToken(n1)+"): gültig", "Der rotate kam an",
		"Token in "+e.file("eigen", "p1")+" gespeichert", "claude: eingetragen")
	if fr.read.Load() || exists(pend) || readFileToken(t, e.file("eigen", "p1")) != n1 {
		t.Errorf("gilt: stdin gelesen %v, .pending da %v", fr.read.Load(), exists(pend))
	}

	// Gilt nicht, das Einrichtungstoken schon: gelöscht, Exit 3; danach geht
	// setup.
	s2 := e.account(t, "p2")
	pend = putPending("eigen", "p2", e.newToken(t))
	e.setup(t, "", "eigen", "p2", s2).want(t, exitRetry, ": ungültig", "Einrichtungstoken ("+ident.MaskToken(s2)+
		"): gültig", "Der rotate kam nicht an; "+pend+" gelöscht", "erneut aufrufen")
	if exists(pend) || exists(e.file("eigen", "p2")) {
		t.Fatal("nach Exit 3: .pending oder Token-Datei da")
	}
	e.setup(t, "", "eigen", "p2", s2).want(t, 0, "Token rotiert: Account p2")

	// Keins gilt: bleibt, Hinweis auf den Admin.
	s3, n3 := e.account(t, "p3"), e.newToken(t)
	e.rotateDirect(t, "eigen", "p3", s3, n3)
	wrong := e.newToken(t)
	pend = putPending("eigen", "p3", wrong)
	e.setup(t, "", "eigen", "p3", s3).want(t, 1, "Neues Token aus "+pend, ": ungültig", "Einrichtungstoken ("+
		ident.MaskToken(s3)+"): ungültig", "Weder das Token in "+pend+" noch das Einrichtungstoken gilt",
		"bleibt liegen", "kephalaion hub account token p3")
	if !exists(pend) || exists(e.file("eigen", "p3")) {
		t.Fatal("keins gilt: .pending weg oder Token-Datei da")
	}
	wantMode(t, pend)

	// Über einen entfernten Node: beide „ungültig oder unbekannt“ — bleibt.
	ca := testcert.NewCA(t, "Proxy-CA")
	caPath := caFile(t, e.dir, "ca.pem", ca.PEM)
	proxy := newProxyNode(t, e.nodeAddr, serverCert(ca.ServerNow(t, "127.0.0.1")), "/kephalaion", "", proxyLogin)
	e.setupWith(t, strings.NewReader(""), "eigen", "p3", s3, "--node", proxy.URL+"/kephalaion", "--ca-file",
		caPath).want(t, 1, ": ungültig oder unbekannt", "Einrichtungstoken ("+ident.MaskToken(s3)+
		"): ungültig oder unbekannt", "bleibt liegen", "hub account token p3")
	if !exists(pend) {
		t.Fatal("über den Proxy: .pending gelöscht")
	}

	// Nicht geprüft (der Hub nimmt den Node nicht an — falsches Token des
	// Nodes): bleibt, das Einrichtungstoken wird nicht gelesen.
	runIn(t, dummyTokenA, "node", "hub", "add", "kaputt", "--node", "laptop-http", "--transport", "http",
		"--address", e.url, "--token-stdin", e.c).want(t, 0)
	pend = putPending("kaputt", "p4", e.newToken(t))
	fr = &failReader{}
	e.setupWith(t, fr, "kaputt", "p4", "--token-stdin", e.c).want(t, 1, ": nicht geprüft", "nicht zu entscheiden",
		"Hub kaputt nimmt diesen Node (laptop-http) nicht an", "bleibt liegen")
	if !exists(pend) || fr.read.Load() {
		t.Errorf("nicht geprüft: .pending da %v, stdin gelesen %v", exists(pend), fr.read.Load())
	}
}

// lossyProxy reicht alles an den Node unter nodeAddr durch, außer
// /account/rotate: mode 502 schickt die Anfrage an den Node und antwortet
// 502 (die Antwort ging verloren), abort schickt sie und bricht die
// Verbindung ab, 404 antwortet 404 ohne den Node — wie ein Proxy ohne Route.
func lossyProxy(t *testing.T, nodeAddr, mode string) string {
	t.Helper()
	target, _ := url.Parse("http://" + nodeAddr)
	rp := &httputil.ReverseProxy{Rewrite: func(pr *httputil.ProxyRequest) {
		pr.SetURL(target)
		pr.SetXForwarded()
	}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != mcpnode.AccountRotatePath {
			rp.ServeHTTP(w, r)
			return
		}
		if mode == "404" {
			http.NotFound(w, r)
			return
		}
		rp.ServeHTTP(httptest.NewRecorder(), r)
		if mode == "502" {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			_ = conn.Close()
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// Die Ausgänge eines rotate durch setup, gegen den echten Node: eindeutig
// abgelehnt und Hub nicht erreicht löschen .pending; ein 502 nach dem
// Abschicken und eine abgebrochene Verbindung lassen sie liegen (das neue
// Token gilt dann schon — der nächste Aufruf übernimmt es); ein
// Verbindungsfehler und ein 404 ohne Code des Nodes beim rotate löschen sie.
func TestNodeAccountSetupOutcomes(t *testing.T) {
	slow(t, "serve mit Hub und Node, viele Aufrufe über den Node")
	e := newSetupEnv(t)

	// Abgelehnt: falsches Einrichtungstoken.
	e.account(t, "o1")
	e.setup(t, "", "eigen", "o1", e.newToken(t)).want(t, 1, "nicht angemeldet", "ist gelöscht",
		"kephalaion hub account token o1")
	if exists(e.file("eigen", "o1") + ".pending") {
		t.Error("abgelehnt: .pending liegt")
	}

	// Hub nicht erreicht.
	runIn(t, dummyTokenA, "node", "hub", "add", "tot", "--node", "laptop-tot", "--transport", "http", "--address",
		"http://127.0.0.1:1/hub", "--token-stdin", e.c).want(t, 0)
	e.setup(t, "", "tot", "o2", e.newToken(t)).want(t, 1, "Hub tot nicht erreichbar", "ist gelöscht",
		"gilt weiter")
	if exists(e.file("tot", "o2") + ".pending") {
		t.Error("nicht erreicht: .pending liegt")
	}

	// 502 nach dem Abschicken, abgebrochene Verbindung: unklar, .pending
	// bleibt; derselbe Aufruf ohne den Proxy übernimmt sie.
	for _, mode := range []string{"502", "abort"} {
		acc := "o-" + mode
		s := e.account(t, acc)
		e.setupWith(t, strings.NewReader(""), "eigen", acc, s, "--node", lossyProxy(t, e.nodeAddr, mode)).want(t, 1,
			"Ausgang unklar", "bleibt liegen", "kephalaion node account setup eigen "+acc)
		pend := e.file("eigen", acc) + ".pending"
		if !exists(pend) || exists(e.file("eigen", acc)) {
			t.Fatalf("%s: .pending da %v", mode, exists(pend))
		}
		wantMode(t, pend)
		e.setup(t, "", "eigen", acc, s).want(t, 0, "gültig", "Der rotate kam an")
	}

	// 404 ohne Code des Nodes: nicht erreicht, gelöscht; das
	// Einrichtungstoken gilt weiter.
	s := e.account(t, "o3")
	e.setupWith(t, strings.NewReader(""), "eigen", "o3", s, "--node", lossyProxy(t, e.nodeAddr, "404")).want(t, 1,
		"dort antwortet kein Node", "ist gelöscht")
	if exists(e.file("eigen", "o3") + ".pending") {
		t.Error("404: .pending liegt")
	}

	// Verbindungsfehler beim rotate (nach der Prüfung): gelöscht.
	var dials atomic.Int32
	old := nodeDial
	nodeDial = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if dials.Add(1) == 1 {
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		}
		return nil, &net.OpError{Op: "dial", Net: network, Err: syscall.ECONNREFUSED}
	}
	r := e.setup(t, "", "eigen", "o3", s, "--node", e.nodeURL)
	nodeDial = old
	r.want(t, 1, "nicht erreichbar", "ist gelöscht")
	if exists(e.file("eigen", "o3") + ".pending") {
		t.Error("Verbindungsfehler: .pending liegt")
	}
	// Das Einrichtungstoken gilt noch.
	e.setup(t, "", "eigen", "o3", s).want(t, 0, "Token rotiert: Account o3")
}

// fakeAccountNode antwortet auf initialize wie ein Node und an den Routen für
// Accounts mit status und body.
func fakeAccountNode(t *testing.T, status int, body string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/mcp":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-06-18",`+
				`"capabilities":{},"serverInfo":{"name":"kephalaion","version":""}}}`)
		case mcpnode.AccountRotatePath, mcpnode.AccountCheckPath:
			if strings.HasPrefix(body, "{") {
				w.Header().Set("Content-Type", "application/json")
			}
			w.WriteHeader(status)
			_, _ = io.WriteString(w, body)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// Wie setup die Antwort auf rotate einordnet: Ein Code des Nodes zählt nur
// mit seinem Status; outcome_unknown, ein Status des Proxys und eine
// Antwort ohne Code des Nodes lassen .pending liegen, die übrigen Codes
// des Nodes und ein 404 ohne Code löschen sie.
func TestNodeAccountSetupAnswers(t *testing.T) {
	e := newSetupEnv(t)
	for _, c := range []struct {
		status int
		body   string
		keep   bool
		want   string
	}{
		{504, `{"code":"outcome_unknown","message":"Ausgang unklar"}`, true, "Ausgang unklar"},
		{502, ``, true, "keine Antwort des Nodes (HTTP 502"},
		{502, `{"code":"unreachable","message":"x"}`, true, "keine Antwort des Nodes"},
		{401, `Anmeldung erforderlich`, true, "keine Antwort des Nodes (HTTP 401"},
		{200, `{}`, true, "HTTP 200, aber keine Antwort des Nodes"},
		{503, `{"code":"unreachable","message":"Hub nicht erreichbar; nichts geändert"}`, false, "später erneut"},
		{403, `{"code":"account_unauthenticated","message":"nicht angemeldet","hidden":true}`, false,
			"kennt der Node unter"},
		{403, `{"code":"unknown_hub","message":"der Node hat keinen Hub-Eintrag eigen"}`, false, "node hub list"},
		{409, `{"code":"no_shared_collection","message":"keine Collection"}`, false, "hub account grant"},
		{500, `{"code":"internal","message":"Fehler des Nodes"}`, false, "gilt weiter"},
		{502, `{"code":"hub_refused","message":"Hub nimmt den Node nicht an"}`, false, "gilt weiter"},
		{404, `404 page not found`, false, "dort antwortet kein Node"},
	} {
		tok := e.newToken(t)
		r := e.setupWith(t, strings.NewReader(""), "eigen", "x", tok, "--node", fakeAccountNode(t, c.status, c.body))
		r.want(t, 1, c.want)
		pend := e.file("eigen", "x") + ".pending"
		if exists(pend) != c.keep {
			t.Errorf("HTTP %d %s: .pending da %v, erwartet %v\n%s", c.status, c.body, exists(pend), c.keep, r.errOut)
		}
		_ = os.Remove(pend)
	}
}

// Falsche Aufrufe: Exit 2, kein Token in der Ausgabe.
func TestNodeAccountSetupUsage(t *testing.T) {
	e := newSetupEnv(t)
	tok := e.newToken(t)
	for _, args := range [][]string{
		{"eigen", "x"},
		{"eigen", "x", tok, "--token-stdin"},
		{"Eigen", "x", tok},
		{"eigen", "admin", tok},
		{"eigen", "x", "keph_kurz"},
		{"eigen", "x", tok, "--token", tok},
		{"eigen", "x", tok, "überzählig"},
	} {
		e.setup(t, "", args...).want(t, 2)
	}
	if exists(e.tokensDir) {
		t.Error("ein falscher Aufruf hat tokens/ angelegt")
	}
}
