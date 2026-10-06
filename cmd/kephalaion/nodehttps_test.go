package main

import (
	"context"
	"crypto/tls"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kephalaion/kephalaion/internal/config"
	"github.com/kephalaion/kephalaion/internal/testcert"
)

// proxyNode ist „Proxy plus Node“ für die Tests (Task 023), wie Caddys
// handle /kephalaion/mcp und die beiden für die Routen für Accounts (Task
// 028): Nur prefix+/mcp, prefix+/account/rotate und prefix+/account/check
// reicht er an den Node-Listener
// unter nodeAddr, den Präfix nimmt er weg, Host setzt er auf den Node
// (header_up Host {upstream_hostport}) und X-Forwarded-For neu — einen
// mitgeschickten verwirft er. Alles andere beantwortet rest (ohne: 404), wie
// der Sammel-handle hinter forward_auth. Mit cert spricht er TLS. tokens
// zählt Anfragen an rest mit einem X-Keph-Token-*: Dorthin darf keines gehen.
type proxyNode struct {
	*httptest.Server
	tokens atomic.Int32
	// hits zählt die Anfragen, die den Handler erreichen.
	hits atomic.Int32
}

func newProxyNode(t *testing.T, nodeAddr string, cert *tls.Certificate, prefix string, host string,
	rest http.HandlerFunc) *proxyNode {
	t.Helper()
	target, _ := url.Parse("http://" + nodeAddr)
	rp := &httputil.ReverseProxy{Rewrite: func(pr *httputil.ProxyRequest) {
		pr.SetURL(target)
		pr.Out.URL.Path, pr.Out.URL.RawPath = strings.TrimPrefix(pr.In.URL.Path, prefix), ""
		pr.SetXForwarded()
		if host != "" {
			pr.Out.Host = host
		}
	}, ErrorLog: log.New(io.Discard, "", 0)}
	p := &proxyNode{}
	if rest == nil {
		rest = http.NotFound
	}
	p.Server = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.hits.Add(1)
		// Wie Caddy: genau /mcp und die Routen für Accounts zum Node.
		switch r.URL.Path {
		case prefix + "/mcp", prefix + "/account/rotate", prefix + "/account/check":
			rp.ServeHTTP(w, r)
			return
		}
		for k := range r.Header {
			if strings.HasPrefix(strings.ToLower(k), "x-keph-token-") {
				p.tokens.Add(1)
			}
		}
		rest(w, r)
	}))
	p.Config.ErrorLog = log.New(io.Discard, "", 0)
	if cert == nil {
		p.Start()
	} else {
		p.TLS = &tls.Config{Certificates: []tls.Certificate{*cert}}
		p.StartTLS()
	}
	t.Cleanup(p.Close)
	return p
}

// Antworten eines Proxys statt des Nodes.
var (
	proxyLogin = func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Anmeldung erforderlich", http.StatusUnauthorized)
	}
	proxyRedirect = func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/auth/login?rd="+url.QueryEscape(r.URL.Path), http.StatusFound)
	}
	proxyHTML = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, "<!doctype html><title>Anmeldung</title><form>…</form>")
	}
)

// node dir push|pull über https (Task 023): durch „Proxy plus Node“ mit
// Präfix und eigener CA; ohne sie, mit falschem Namen, abgelaufen, gegen
// eine Gegenseite ohne TLS sagt es den Grund in Klartext, und kein Token
// geht hinaus. Antwortet statt des Nodes der Proxy (401, 302, HTML, 404),
// heißt das „Präfix falsch oder Anmeldung des Proxys“ — dorthin geht kein
// Token. http nur zu Loopback und host.docker.internal.
func TestNodeDirHTTPS(t *testing.T) {
	e := newDirEnv(t)
	nodeAddr := e.srv.addrs[config.Node]
	ca, other := testcert.NewCA(t, "Proxy-CA"), testcert.NewCA(t, "Andere CA")
	caPath := caFile(t, e.dir, "ca.pem", ca.PEM)
	src, dst := filepath.Join(e.dir, "src"), filepath.Join(e.dir, "dst")
	writeLocal(t, src, map[string]string{"a.md": "a", "sub/b.md": "b"})
	dir := func(verb, node string, args ...string) result {
		t.Helper()
		local := src
		if verb == "pull" {
			local = dst
		}
		all := append([]string{"node", "dir", verb, "eigen:team-x", "vendor/x", local, "--node", node, "--account", "kp",
			"--token-file", e.fileKP}, args...)
		r := e.run(t, all...)
		e.outputs = append(e.outputs, r.out, r.errOut)
		return r
	}

	good := newProxyNode(t, nodeAddr, serverCert(ca.ServerNow(t, "127.0.0.1")), "/kephalaion", "", proxyLogin)
	base := good.URL + "/kephalaion"
	dir("push", base, "--ca-file", caPath).want(t, 0, "push eigen:team-x vendor/x/: 2 angelegt")
	dir("pull", base+"/", "--ca-file", caPath).want(t, 0, "pull eigen:team-x vendor/x/: 2 angelegt")
	if got := localTree(t, dst); !reflect.DeepEqual(got, map[string]string{"a.md": "a", "sub/b.md": "b"}) {
		t.Errorf("pull: %v", got)
	}
	if got := e.hubDocs(t, "vendor/x"); len(got) != 2 {
		t.Errorf("am Hub: %v", got)
	}

	// Zertifikat: ohne --ca-file (System-Roots), falsche CA, anderer Name,
	// abgelaufen — nichts erreicht den Proxy-Handler.
	before := good.hits.Load()
	dir("pull", base).want(t, 1, "Node unter "+base+"/mcp: Zertifikat von 127.0.0.1 nicht vertraut (Aussteller "+
		"CN=Proxy-CA; --ca-file?)")
	dir("pull", base, "--ca-file", caFile(t, e.dir, "andere.pem", other.PEM)).want(t, 1,
		"nicht vertraut (Aussteller CN=Proxy-CA; passt die CA aus --ca-file?)")
	if good.hits.Load() != before {
		t.Errorf("bei einem Zertifikatsfehler erreichte eine Anfrage den Proxy")
	}
	wrongName := newProxyNode(t, nodeAddr, serverCert(ca.ServerNow(t, "node.example.org")), "/kephalaion", "", nil)
	dir("pull", wrongName.URL+"/kephalaion", "--ca-file", caPath).want(t, 1,
		"Zertifikat gilt nicht für 127.0.0.1 (ausgestellt für node.example.org)")
	expired := newProxyNode(t, nodeAddr, serverCert(ca.ServerExpired(t, "127.0.0.1")), "/kephalaion", "", nil)
	dir("pull", expired.URL+"/kephalaion", "--ca-file", caPath).want(t, 1, "Zertifikat abgelaufen seit ")
	// https gegen den Node selbst, der nur http spricht.
	dir("pull", "https://"+nodeAddr).want(t, 1, "die Gegenseite spricht kein TLS")

	// Statt des Nodes antwortet der Proxy: Präfix falsch oder Anmeldung des
	// Proxys — nie mit Token.
	for name, rest := range map[string]http.HandlerFunc{"401": proxyLogin, "302": proxyRedirect, "HTML": proxyHTML} {
		p := newProxyNode(t, nodeAddr, serverCert(ca.ServerNow(t, "127.0.0.1")), "/kephalaion", "", rest)
		r := dir("pull", p.URL+"/falsch", "--ca-file", caPath)
		r.want(t, 1, "Präfix falsch oder Anmeldung des Proxys")
		if p.tokens.Load() != 0 {
			t.Errorf("%s: ein Token ging an den Proxy", name)
		}
		// Mit dem richtigen Präfix geht es.
		dir("pull", p.URL+"/kephalaion", "--ca-file", caPath).want(t, 0, "2 unverändert")
	}
	dir("pull", good.URL+"/falsch", "--ca-file", caPath).want(t, 1, "eine Anmeldung des Proxys, nicht der Node (HTTP 401")
	dir("pull", wrongName.URL, "--ca-file", caPath).want(t, 1) // Zertifikat zuerst
	plain404 := newProxyNode(t, nodeAddr, serverCert(ca.ServerNow(t, "127.0.0.1")), "/kephalaion", "", nil)
	dir("pull", plain404.URL+"/anders", "--ca-file", caPath).want(t, 1, "dort antwortet kein Node (HTTP 404",
		"Präfix falsch?")
	dir("pull", base+"/mcp", "--ca-file", caPath).want(t, 1, "ohne /mcp am Ende")
	// Der Proxy setzt Host nicht: die Host-Prüfung des Nodes.
	noHost := newProxyNode(t, nodeAddr, serverCert(ca.ServerNow(t, "127.0.0.1")), "/kephalaion", "node.example.org", nil)
	dir("pull", noHost.URL+"/kephalaion", "--ca-file", caPath).want(t, 1, "Host-Prüfung des Nodes schlägt fehl (HTTP 403",
		"header_up Host {upstream_hostport}")
	// Der Proxy ohne Node dahinter.
	gone := newProxyNode(t, closedAddress(t), serverCert(ca.ServerNow(t, "127.0.0.1")), "/kephalaion", "", nil)
	dir("pull", gone.URL+"/kephalaion", "--ca-file", caPath).want(t, 1, "Proxy antwortet, aber der Node dahinter nicht")

	// http nur zu Loopback und host.docker.internal; --ca-file nur mit https.
	dir("pull", "http://node.example.org:7433").want(t, 1, "http nur zu diesem Rechner", "https://<name>/<präfix>")
	dir("pull", "http://"+nodeAddr, "--ca-file", caPath).want(t, 1, "--ca-file gibt es nur mit einer https-Adresse")
	dir("pull", "ftp://"+nodeAddr).want(t, 1, "erwartet http://<host>:<port>")
	dir("pull", "https://user@"+nodeAddr).want(t, 1, "ohne Benutzer, Query und Fragment")

	// host.docker.internal ist erlaubt: direkt an den Node die
	// Host-Prüfung (der Node nimmt den Namen nicht an), über einen
	// Weiterleiter, der Host setzt, geht es.
	_, nodePort, _ := net.SplitHostPort(nodeAddr)
	fwd := newProxyNode(t, nodeAddr, nil, "", "", nil)
	_, fwdPort, _ := net.SplitHostPort(strings.TrimPrefix(fwd.URL, "http://"))
	old := nodeDial
	nodeDial = func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, _ := net.SplitHostPort(addr)
		if host == dockerHost {
			addr = net.JoinHostPort("127.0.0.1", port)
		}
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}
	t.Cleanup(func() { nodeDial = old })
	dir("pull", "http://"+dockerHost+":"+nodePort).want(t, 1, "Host-Prüfung des Nodes schlägt fehl (HTTP 403",
		"host.docker.internal nimmt der Node nicht als Host an")
	dir("pull", "http://"+dockerHost+":"+fwdPort).want(t, 0, "2 unverändert")

	for _, out := range e.outputs {
		if strings.Contains(out, e.tokenKP) || strings.Contains(out, "keph_") {
			t.Fatalf("Token in einer Ausgabe:\n%s", out)
		}
	}
}

// parseNodeAddress: die Basis ohne /mcp, http nur lokal und zu
// host.docker.internal, entfernt ist alles außer http zu Loopback.
func TestParseNodeAddress(t *testing.T) {
	cases := []struct {
		raw, base string
		remote    bool
		err       string
	}{
		{"http://127.0.0.1:7433", "http://127.0.0.1:7433", false, ""},
		{"http://localhost:7433/", "http://localhost:7433", false, ""},
		{"http://[::1]:7433", "http://[::1]:7433", false, ""},
		{"http://host.docker.internal:7433", "http://host.docker.internal:7433", true, ""},
		{"https://node.example.org/kephalaion/", "https://node.example.org/kephalaion", true, ""},
		{"https://127.0.0.1:8443/kephalaion", "https://127.0.0.1:8443/kephalaion", true, ""},
		{"http://node.example.org", "", false, "http nur zu diesem Rechner"},
		{"http://9.141.8.157:7433", "", false, "http nur zu diesem Rechner"},
		{"https://node.example.org/kephalaion/mcp", "", false, "ohne /mcp am Ende"},
		{"https://node.example.org/k?x=1", "", false, "ohne Benutzer, Query und Fragment"},
		{"https://a:b@node.example.org/k", "", false, "ohne Benutzer"},
		{"localhost:7433", "", false, "erwartet http://"},
		{"node.example.org", "", false, "erwartet http://"},
	}
	for _, c := range cases {
		a, err := parseNodeAddress(c.raw)
		switch {
		case c.err != "" && (err == nil || !strings.Contains(err.Error(), c.err)):
			t.Errorf("%s: Fehler %v, erwartet %q", c.raw, err, c.err)
		case c.err == "" && err != nil:
			t.Errorf("%s: %v", c.raw, err)
		case c.err == "" && (a.Base != c.base || a.Remote() != c.remote || a.Endpoint() != c.base+"/mcp"):
			t.Errorf("%s: %+v remote %v", c.raw, a, a.Remote())
		}
	}
}

// probeNode mit einer Antwort auf initialize als SSE (text/event-stream): Es
// zählt der letzte data-Block. Ohne data oder mit kaputtem JSON gilt die
// Gegenseite nicht als Kephalaion-Node.
func TestProbeNodeSSE(t *testing.T) {
	const node = `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-06-18","capabilities":{},` +
		`"serverInfo":{"name":"kephalaion","version":"1.2.3"}}}`
	const other = `{"jsonrpc":"2.0","id":1,"result":{"serverInfo":{"name":"anderer","version":"9"}}}`
	cases := []struct {
		name, body, version, err string
	}{
		{"gültig", "event: message\ndata: " + node + "\n\n", "1.2.3", ""},
		{"ohne Leerzeichen, CRLF", "event: message\r\ndata:" + node + "\r\n\r\n", "1.2.3", ""},
		{"verdeckt", "event: message\ndata: " + strings.Replace(node, `"1.2.3"`, `""`, 1) + "\n\n", "", ""},
		{"der letzte Block zählt", "data: " + other + "\n\ndata: " + node + "\n\n", "1.2.3", ""},
		{"der letzte Block ist nicht der Node", "data: " + node + "\n\ndata: " + other + "\n\n", "",
			`nicht als Kephalaion-Node (serverInfo "anderer")`},
		{"ohne data", "event: message\nid: 1\n\n", "", `nicht als Kephalaion-Node (serverInfo "")`},
		{"leer", "", "", `nicht als Kephalaion-Node (serverInfo "")`},
		{"kaputtes JSON", "event: message\ndata: {\"jsonrpc\":\"2.0\",\"result\":{\"serverInfo\":\n\n", "",
			`nicht als Kephalaion-Node (serverInfo "")`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
				_, _ = io.WriteString(w, c.body)
			}))
			defer srv.Close()
			addr, err := parseNodeAddress(srv.URL)
			if err != nil {
				t.Fatal(err)
			}
			info, err := probeNode(context.Background(), addr, nil, "")
			switch {
			case c.err == "" && (err != nil || info.Version != c.version):
				t.Errorf("Version %q, Fehler %v; erwartet %q", info.Version, err, c.version)
			case c.err != "" && (err == nil || !strings.Contains(err.Error(), c.err) ||
				!strings.Contains(err.Error(), "Präfix falsch oder Anmeldung des Proxys")):
				t.Errorf("Fehler %v, erwartet %q", err, c.err)
			}
		})
	}
}
