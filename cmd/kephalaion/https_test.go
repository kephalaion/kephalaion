package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kephalaion/kephalaion/internal/config"
	"github.com/kephalaion/kephalaion/internal/contract/httpapi"
	"github.com/kephalaion/kephalaion/internal/ident"
	"github.com/kephalaion/kephalaion/internal/loopback"
	"github.com/kephalaion/kephalaion/internal/node/mcpnode"
	"github.com/kephalaion/kephalaion/internal/testcert"
)

// caFile schreibt das Zertifikat einer Test-CA in eine Datei unter dir.
func caFile(t *testing.T, dir, name, pemText string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(pemText), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// --ca-file bei add und set: gespeichert wird der Inhalt, show nennt Subject,
// Gültigkeit und Fingerabdruck, list eine Spalte CA; nur bei https, und ein
// Wechsel des Transports verwirft sie.
func TestNodeHubCA(t *testing.T) {
	dir := isolate(t)
	cfg := setup(t, dir)
	c := "--config=" + cfg
	ca, ca2 := testcert.NewCA(t, "Test-CA"), testcert.NewCA(t, "Zweite CA")
	file, file2 := caFile(t, dir, "ca.pem", ca.PEM), caFile(t, dir, "ca2.pem", ca2.PEM)
	tok, err := ident.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	add := func(alias string, args ...string) result {
		t.Helper()
		all := append([]string{"node", "hub", "add", alias, "--node", "laptop", "--token-stdin"}, args...)
		return runIn(t, tok+"\n", append(all, c)...)
	}
	// Die Regeln greifen vor dem Lesen des Tokens: nichts von stdin gelesen.
	add("kaputt", "--transport", "https", "--address", "https://h", "--ca-file", filepath.Join(dir, "fehlt.pem")).
		want(t, 1, "--ca-file: open")
	add("kaputt", "--transport", "https", "--address", "https://h", "--ca-file", caFile(t, dir, "muell.pem", "kein PEM")).
		want(t, 1, "muell.pem: --ca-file: kein Zertifikat gefunden")
	add("kaputt", "--transport", "http", "--address", "http://localhost:1", "--ca-file", file).
		want(t, 1, "--ca-file gibt es nur bei Transport https")
	add("kaputt", "--transport", "ssh", "--address", "h", "--ca-file", file).want(t, 1, "nur bei Transport https")
	runT(t, "node", "hub", "list", c).want(t, 0, "Keine Hubs.")

	add("vm", "--transport", "https", "--address", "https://9.141.8.157", "--ca-file", file).
		want(t, 0, "Hub vm eingetragen (https https://9.141.8.157, als Node laptop)")
	add("ohne", "--transport", "https", "--address", "https://hub.example.org").want(t, 0)
	r := runT(t, "node", "hub", "list", c)
	r.want(t, 0, "CA", "vm", "ja", "ohne", "–")
	if strings.Contains(r.out, "BEGIN CERTIFICATE") {
		t.Errorf("list zeigt die CA selbst:\n%s", r.out)
	}
	r = runT(t, "node", "hub", "show", "vm", c)
	r.want(t, 0, "  CA:           CN=Test-CA, gültig ", " – ", ", SHA-256 ")
	if !strings.Contains(r.out, ca.Cert.NotAfter.UTC().Format("2006-01-02")) || strings.Contains(r.out, "BEGIN") {
		t.Errorf("show:\n%s", r.out)
	}
	runT(t, "node", "hub", "show", "ohne", c).want(t, 0, "  CA:           – (System-Roots)")
	h, err := nodeStore(t, cfg).Hub(context.Background(), "vm")
	if err != nil || h.CA != ca.PEM {
		t.Fatalf("gespeichert: %q, %v", h.CA, err)
	}

	// set: andere CA, zwei CAs in einer Datei, entfernen, bei http abgewiesen.
	runT(t, "node", "hub", "set", "vm", "--ca-file", file2, c).want(t, 0, "Hub vm geändert")
	runT(t, "node", "hub", "show", "vm", c).want(t, 0, "CN=Zweite CA")
	both := caFile(t, dir, "beide.pem", ca.PEM+ca2.PEM)
	runT(t, "node", "hub", "set", "vm", "--ca-file", both, c).want(t, 0)
	r = runT(t, "node", "hub", "show", "vm", c)
	r.want(t, 0, "CN=Test-CA", "CN=Zweite CA")
	runT(t, "node", "hub", "set", "vm", "--ca-file", "", c).want(t, 0)
	runT(t, "node", "hub", "show", "vm", c).want(t, 0, "  CA:           – (System-Roots)")
	runT(t, "node", "hub", "set", "vm", "--ca-file", file, c).want(t, 0)
	runT(t, "node", "hub", "set", "vm", "--transport", "http", "--address", "http://localhost:7434/hub", c).want(t, 0)
	if h, _ := nodeStore(t, cfg).Hub(context.Background(), "vm"); h.CA != "" || h.Transport != "http" {
		t.Errorf("nach Wechsel auf http: %+v", h)
	}
	runT(t, "node", "hub", "set", "vm", "--ca-file", file, c).want(t, 1, "nur bei Transport https")
	runT(t, "node", "hub", "set", "vm", "--transport", "https", "--address", "https://h", "--ca-file", file, c).want(t, 0)
	runT(t, "node", "hub", "show", "vm", c).want(t, 0, "CN=Test-CA")
	runT(t, "node", "hub", "--help").want(t, 0, "--ca-file pfad", "System-Roots")
}

// Export im Format 7 trägt die CA je Hub-Eintrag als Block; der Import
// nimmt sie mit und prüft sie wie die CLI. Ein Export vor Format 7 darf
// keine CA tragen und liest sich ohne.
func TestExportImportCA(t *testing.T) {
	dir := isolate(t)
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	cfgA := setup(t, a)
	fill(t, cfgA)
	ca := testcert.NewCA(t, "Test-CA")
	tok, _ := ident.NewToken()
	runIn(t, tok, "node", "hub", "add", "vm", "--node", "laptop", "--transport", "https", "--address", "https://9.141.8.157",
		"--ca-file", caFile(t, dir, "ca.pem", ca.PEM), "--token-stdin", "--config", cfgA).want(t, 0)
	exp := filepath.Join(dir, "export.yaml")
	exportTo(t, cfgA, exp)
	raw, _ := os.ReadFile(exp)
	data := string(raw)
	for _, want := range []string{"format: 7", "        ca: \"\"\n", "        ca: |\n          -----BEGIN CERTIFICATE-----\n"} {
		if !strings.Contains(data, want) {
			t.Errorf("Export ohne %q:\n%s", want, data)
		}
	}

	cfgB := setup(t, b)
	runT(t, "config", "import", "--config", cfgB, exp).want(t, 0, "3 Hubs")
	if got, want := nodeTables(t, cfgB), nodeTables(t, cfgA); !reflect.DeepEqual(got, want) {
		t.Errorf("Node-Tabellen\n%+v\nerwartet\n%+v", got, want)
	}
	runT(t, "node", "hub", "show", "vm", "--config", cfgB).want(t, 0, "CN=Test-CA")
	before := takeSnapshot(t, b, cfgB)

	file := filepath.Join(dir, "import.yaml")
	write := func(content string) {
		if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	const vmAddr = "address: https://9.141.8.157\n"
	if !strings.Contains(data, vmAddr) {
		t.Fatalf("Export ohne %q:\n%s", vmAddr, data)
	}
	for _, c := range []struct{ name, content, want string }{
		{"F6 mit CA", strings.Replace(data, "format: 7", "format: 6", 1), "Format 6 kennt keine CA (tables.node.hubs["},
		{"F6 mit CA leer", strings.Replace(toFormat6(t, data), "        hub_id: \"\"", "        ca: \"\"\n        hub_id: \"\"", 1),
			"Format 6 kennt keine CA"},
		{"F6 mit CA null", strings.Replace(toFormat6(t, data), "        hub_id: \"\"", "        ca:\n        hub_id: \"\"", 1),
			"Format 6 kennt keine CA"},
		{"CA bei http", strings.Replace(strings.Replace(data, vmAddr, "address: http://localhost:7434/hub\n", 1),
			"transport: https", "transport: http", 1), "--ca-file gibt es nur bei Transport https"},
		{"kaputte CA", strings.Replace(data, "-----BEGIN CERTIFICATE-----", "-----BEGIN ZERTIFIKAT-----", 1), "kein Zertifikat gefunden"},
	} {
		write(c.content)
		runT(t, "config", "import", "--config", cfgB, file).want(t, 1, c.want, "Nichts geschrieben")
		if after := takeSnapshot(t, b, cfgB); !reflect.DeepEqual(after, before) {
			t.Errorf("%s: verändert", c.name)
		}
	}
	// Format 6 ohne CA: der Eintrag kommt ohne CA an (System-Roots).
	write(toFormat6(t, data))
	runT(t, "config", "import", "--config", cfgB, file).want(t, 0, "3 Hubs")
	if h, err := nodeStore(t, cfgB).Hub(context.Background(), "vm"); err != nil || h.CA != "" || h.Transport != "https" {
		t.Errorf("nach Format 6: %+v, %v", h, err)
	}
	runT(t, "config", "import", "--config", cfgB, exp).want(t, 0, "3 Hubs")
	if h, _ := nodeStore(t, cfgB).Hub(context.Background(), "vm"); h.CA != ca.PEM {
		t.Errorf("nach Format 7: CA %q", h.CA)
	}
}

// proxyHub ist „Proxy plus Hub“ für die Tests: ein Server — mit TLS, wenn
// cert gesetzt ist — vor dem Hub-Listener von serve (newHubHandler hinter
// loopback.Guard), der den Vertrag unter /hub bedient; die Adresse eines
// Eintrags ist also <URL>/hub. Ohne host
// setzt er Host wie Caddy mit header_up Host {upstream_hostport} auf die
// Loopback-Adresse mit dem eigenen Port; mit host reicht er diesen Host
// durch, wie ein Proxy ohne die Zeile den Host des Aufrufers (die äußere
// Adresse), und der Hub antwortet 403.
func proxyHub(t *testing.T, e *commEnv, cert *tls.Certificate, host string) *httptest.Server {
	t.Helper()
	return proxyHubAt(t, e, cert, host, "", 0)
}

// proxyHubAt ist proxyHub mit einem Präfix, unter dem der Proxy das Binary
// anbietet (wie Caddys handle /kephalaion/hub/* mit uri strip_prefix
// /kephalaion): Er nimmt den Präfix weg, der Hub-Listener sieht /hub/v1/…;
// ohne den Präfix in der Anfrage antwortet der Proxy 404 ohne Vertragsform.
// rest sagt, was der Proxy mit dem Rest unter dem Präfix außerhalb von /hub
// tut: 0 reicht ihn an das Binary durch (Begrüßung an der Wurzel, /v1/… mit
// dem Hinweis des Hubs), 401 ist eine Anmeldung vor dem Rest (forward_auth,
// wie auf der VM), 404 die allgemeine Form, in der nur der Hub nach außen
// geht.
func proxyHubAt(t *testing.T, e *commEnv, cert *tls.Certificate, host, prefix string, rest int) *httptest.Server {
	t.Helper()
	var hub http.Handler = loopback.Guard(newHubHandler(hubStore(t, e.cfg)))
	if prefix != "" {
		inner := hub
		hub = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p := r.URL.Path
			if p != prefix && !strings.HasPrefix(p, prefix+"/") {
				http.NotFound(w, r)
				return
			}
			if rest != 0 && p != prefix+hubPath && !strings.HasPrefix(p, prefix+hubPath+"/") {
				http.Error(w, http.StatusText(rest), rest)
				return
			}
			http.StripPrefix(prefix, inner).ServeHTTP(w, r)
		})
	}
	srv := httptest.NewUnstartedServer(nil)
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Host = "localhost:" + port
		if host != "" {
			r.Host = host
		}
		hub.ServeHTTP(w, r)
	})
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	if cert == nil {
		srv.Start()
	} else {
		srv.TLS = &tls.Config{Certificates: []tls.Certificate{*cert}}
		srv.StartTLS()
	}
	t.Cleanup(srv.Close)
	return srv
}

// serverCert liefert ein Zertifikat als Zeiger für proxyHub.
func serverCert(c tls.Certificate) *tls.Certificate { return &c }

// addTLSHub trägt einen https-Eintrag für den Node laptop-tls ein, der
// beide Collections darf, mit der CA aus caPEM (leer: System-Roots).
func (e *commEnv) addTLSHub(t *testing.T, alias, address, caPEM string) {
	t.Helper()
	r := e.run(t, "hub", "node", "add", "laptop-tls")
	r.want(t, 0)
	tok := tokenFrom(t, r.out)
	for _, coll := range []string{"team-x", "privat"} {
		e.run(t, "hub", "node", "grant", "laptop-tls", coll).want(t, 0)
	}
	args := []string{"node", "hub", "add", alias, "--node", "laptop-tls", "--transport", "https", "--address", address,
		"--token-stdin"}
	if caPEM != "" {
		args = append(args, "--ca-file", caFile(t, e.dir, alias+"-ca.pem", caPEM))
	}
	e.runIn(t, tok, args...).want(t, 0)
	e.run(t, "node", "collection", "add", alias+":team-x").want(t, 0)
}

// node hub check über https: mit der richtigen CA erreichbar; sonst nennt es
// den Grund im Klartext — CA nicht vertraut, falscher Name, abgelaufen, der
// Proxy ohne Hub dahinter (502), die Host-Prüfung des Hubs (403), eine
// Adresse ohne /hub (404 ohne Vertragsform vom Hub oder vom Proxy, hinter
// einer Anmeldung 401). Am Hub
// kommt bei einem Zertifikatsfehler nichts an. Ohne Wiederholung: Der
// 502-Fall kostete sonst 3,5 s Backoff, an der Meldung ändert das nichts.
func TestNodeHubCheckHTTPS(t *testing.T) {
	noRetry(t)
	e := newCommEnv(t)
	ca, other := testcert.NewCA(t, "Richtige CA"), testcert.NewCA(t, "Andere CA")
	good := proxyHub(t, e, serverCert(ca.ServerNow(t, "127.0.0.1")), "")
	e.addTLSHub(t, "sicher", good.URL+"/hub", ca.PEM)
	e.run(t, "node", "hub", "check", "sicher").want(t, 0, "Hub sicher: erreichbar (https "+good.URL+"/hub)",
		"Node-Name:    laptop-tls", "erlaubt:      privat, team-x")
	e.run(t, "node", "sync", "sicher").want(t, 0, "Hub sicher (hub_id", "team-x: abgeglichen")

	set := func(args ...string) {
		t.Helper()
		e.run(t, append([]string{"node", "hub", "set", "sicher"}, args...)...).want(t, 0)
	}
	check := func(want ...string) {
		t.Helper()
		r := e.run(t, "node", "hub", "check", "sicher")
		r.want(t, 1, want...)
		if strings.Contains(r.out+r.errOut, "keph_") {
			t.Errorf("Token in der Ausgabe:\n%s%s", r.out, r.errOut)
		}
	}
	// Falsche CA, und gar keine (System-Roots) gegen die Test-CA.
	set("--ca-file", caFile(t, e.dir, "andere.pem", other.PEM))
	check("Hub sicher (https " + good.URL + "/hub): Zertifikat von 127.0.0.1 nicht vertraut (Aussteller CN=Richtige CA; " +
		"passt die CA aus --ca-file?) — x509: certificate signed by unknown authority")
	set("--ca-file", "")
	check("Zertifikat von 127.0.0.1 nicht vertraut (Aussteller CN=Richtige CA; --ca-file?)")
	// Zertifikat für einen anderen Namen.
	wrongName := proxyHub(t, e, serverCert(ca.ServerNow(t, "hub.example.org")), "")
	set("--address", wrongName.URL+"/hub", "--ca-file", caFile(t, e.dir, "richtig.pem", ca.PEM))
	check("Zertifikat gilt nicht für 127.0.0.1 (ausgestellt für hub.example.org)")
	// Abgelaufen.
	expired := proxyHub(t, e, serverCert(ca.ServerExpired(t, "127.0.0.1")), "")
	set("--address", expired.URL+"/hub")
	check("Zertifikat abgelaufen seit ")
	// Proxy ohne Hub dahinter.
	gateway := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "", http.StatusBadGateway)
	}))
	gateway.TLS = &tls.Config{Certificates: []tls.Certificate{ca.ServerNow(t, "127.0.0.1")}}
	gateway.StartTLS()
	t.Cleanup(gateway.Close)
	set("--address", gateway.URL+"/hub")
	check("Proxy antwortet, aber der Hub dahinter nicht (HTTP 502: Bad Gateway): läuft kephalaion serve")
	// Der Proxy setzt Host nicht, sondern reicht die äußere Adresse durch:
	// die Host-Prüfung des Hubs.
	noHost := proxyHub(t, e, serverCert(ca.ServerNow(t, "127.0.0.1")), "9.141.8.157")
	set("--address", noHost.URL+"/hub")
	check("Host-Prüfung des Hubs schlägt fehl (HTTP 403: Forbidden: Host \"9.141.8.157\" ist nicht dieser Rechner): " +
		"setzt der Proxy Host auf die Loopback-Adresse des Hubs (header_up Host {upstream_hostport}, etwa localhost:7434)?")
	// Ein Präfix in der Adresse: Der Proxy bietet das Binary unter
	// /kephalaion an und nimmt den Präfix weg, der Hub sieht /hub/v1/….
	const hint = "der Vertrag liegt unter /hub/v1/… — fehlt /hub am Ende der Adresse des Hub-Eintrags?"
	prefixed := proxyHubAt(t, e, serverCert(ca.ServerNow(t, "127.0.0.1")), "", "/kephalaion", 0)
	set("--address", prefixed.URL+"/kephalaion/hub/")
	e.run(t, "node", "hub", "check", "sicher").want(t, 0, "Hub sicher: erreichbar (https "+prefixed.URL+"/kephalaion/hub/)")
	// Ohne den Präfix: 404 des Proxys; mit Präfix, aber ohne /hub: 404 der
	// Wurzel des Binarys mit dem Hinweis — beides ohne Vertragsform, check
	// sagt in beiden Fällen, was fehlt.
	set("--address", prefixed.URL)
	check("Hub sicher (https " + prefixed.URL + "): der Pfad ist nicht der Hub (HTTP 404: 404 page not found): " +
		"fehlt /hub am Ende der Adresse (etwa " + prefixed.URL + "/hub, hinter einem Proxy https://host/kephalaion/hub), " +
		"oder kennt der Proxy die Route nicht?")
	set("--address", prefixed.URL+"/kephalaion")
	check("Hub sicher (https " + prefixed.URL + "/kephalaion): der Pfad ist nicht der Hub (HTTP 404: unbekannter Pfad " +
		"/v1/whoami: " + hint + "): fehlt /hub am Ende der Adresse (etwa " + prefixed.URL + "/kephalaion/hub, ")
	e.run(t, "node", "hub", "set", "sicher", "--address", prefixed.URL+"/kephalaion/hub?x=1").want(t, 1, "ohne Query")
	// Die VM: nur /kephalaion/hub/* ohne Anmeldung, der Rest hinter
	// forward_auth — die alte Adresse ohne /hub bekommt 401 vom Proxy.
	authed := proxyHubAt(t, e, serverCert(ca.ServerNow(t, "127.0.0.1")), "", "/kephalaion", http.StatusUnauthorized)
	set("--address", authed.URL+"/kephalaion")
	check("Hub sicher (https " + authed.URL + "/kephalaion): eine Anmeldung des Proxys, nicht der Hub (HTTP 401: Unauthorized): " +
		"die Route zum Hub muss ohne forward_auth stehen — Nodes weisen sich mit dem Token beim Hub aus; oder fehlt /hub " +
		"am Ende der Adresse (dann landet die Anfrage in der Anmeldung vor dem Rest)?")
	set("--address", authed.URL+"/kephalaion/hub")
	e.run(t, "node", "hub", "check", "sicher").want(t, 0, "Hub sicher: erreichbar (https "+authed.URL+"/kephalaion/hub)")
	// Die allgemeine Form: nur der Hub geht nach außen, der Rest ist 404 des
	// Proxys.
	closed := proxyHubAt(t, e, serverCert(ca.ServerNow(t, "127.0.0.1")), "", "/kephalaion", http.StatusNotFound)
	set("--address", closed.URL+"/kephalaion")
	check("der Pfad ist nicht der Hub (HTTP 404: Not Found): fehlt /hub am Ende der Adresse (etwa " + closed.URL +
		"/kephalaion/hub, hinter einem Proxy https://host/kephalaion/hub), oder kennt der Proxy die Route nicht?")
	set("--address", closed.URL+"/kephalaion/hub")
	e.run(t, "node", "hub", "check", "sicher").want(t, 0, "Hub sicher: erreichbar")
	// Eine Anmeldung des Proxys vor allem: 401 ohne Vertragsform.
	login := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "login required", http.StatusUnauthorized)
	}))
	login.TLS = &tls.Config{Certificates: []tls.Certificate{ca.ServerNow(t, "127.0.0.1")}}
	login.StartTLS()
	t.Cleanup(login.Close)
	set("--address", login.URL+"/hub")
	check("eine Anmeldung des Proxys, nicht der Hub (HTTP 401: login required): die Route zum Hub muss ohne forward_auth stehen")
	// Über http (ein Tunnel mit anderem Port) bleibt die Meldung ohne Proxy.
	plain := proxyHub(t, e, nil, "localhost:7434")
	set("--transport", "http", "--address", plain.URL+"/hub")
	check("Hub sicher (http http://", "Host-Prüfung des Hubs schlägt fehl (HTTP 403: Forbidden: Host \"localhost:7434\" "+
		"nennt nicht den Port, auf dem die Anfrage ankam): ein Tunnel geht nur mit gleichem Port")
}

// Über serve: ein https-Eintrag mit Test-CA gegen „Proxy plus Hub“ — der
// Abgleich im Hintergrund holt Zeilen, create über MCP geht durch, und der
// Anstoß danach bringt das Dokument in die Replica. Mit falscher CA steht die
// Art des Fehlers einmal im Log, create meldet „nicht erreichbar“, und am
// Hub kommt nichts an.
func TestServeHTTPS(t *testing.T) {
	slow(t, "serve, wartet auf Runden des Abgleichs (sync_interval 1s)")
	e := newCommEnv(t)
	ns := nodeStore(t, e.cfg)
	ca, other := testcert.NewCA(t, "Richtige CA"), testcert.NewCA(t, "Andere CA")
	var calls atomic.Int32
	inner := proxyHub(t, e, serverCert(ca.ServerNow(t, "127.0.0.1")), "")
	counted := inner.Config.Handler
	inner.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		counted.ServeHTTP(w, r)
	})
	e.addTLSHub(t, "sicher", inner.URL+"/hub", ca.PEM)
	e.runIn(t, "Inhalt", "hub", "doc", "put", "team-x", "a.md").want(t, 0)
	e.run(t, "config", "set", "node", "sync_interval", "1s").want(t, 0)
	// Nach sync_interval 0 sieht der Abgleich alle syncIdle nach, ob er
	// wieder an ist; kurz, damit das Einschalten unten nicht 30 s wartet.
	oldIdle := syncIdle
	syncIdle = 100 * time.Millisecond
	t.Cleanup(func() { syncIdle = oldIdle })
	srv := startServe(t, portZero(t, e.cfg))
	eventuallyLog(t, srv, "a.md in der Replica von sicher", func() bool { return e.docIs(t, "sicher:team-x", "a.md", "Inhalt") })
	eventuallyLog(t, srv, "Logzeile zu sicher", func() bool {
		return strings.Contains(srv.log.String(), "Abgleich sicher: ") && syncStatus(t, ns, "sicher").OKAt != 0
	})

	endpoint := "http://" + srv.addrs[config.Node] + mcpnode.Path
	bob := map[string][2]string{"sicher": {"bob", e.tokens["bob"]}}
	var out mcpnode.WriteOutput
	mcpTool(t, endpoint, bob, "create", mcpnode.CreateInput{Collection: "sicher:team-x", Name: "neu.md", Content: "über TLS"}, &out)
	if out.Error != nil || out.Revision == 0 || out.Updated == nil || out.Updated.By != "kleist" {
		t.Fatalf("create über https: %+v", out)
	}
	e.run(t, "hub", "doc", "get", "team-x", "neu.md").want(t, 0, "über TLS")
	// Der Anstoß ohne Runden: sync_interval 0, dann ein zweites create —
	// danach hält der angestoßene Abgleich einen neuen Erfolg fest.
	e.run(t, "config", "set", "node", "sync_interval", "0").want(t, 0)
	eventuallyLog(t, srv, "Abgleich aus", func() bool {
		return strings.Contains(srv.log.String(), "Abgleich im Hintergrund aus (sync_interval 0)")
	})
	time.Sleep(50 * time.Millisecond)
	last := syncStatus(t, ns, "sicher").OKAt
	mcpTool(t, endpoint, bob, "create", mcpnode.CreateInput{Collection: "sicher:team-x", Name: "zwei.md", Content: "zwei"}, &out)
	if out.Error != nil {
		t.Fatalf("zweites create: %+v", out)
	}
	eventuallyLog(t, srv, "angestoßener Abgleich nach create", func() bool { return syncStatus(t, ns, "sicher").OKAt > last })
	if !e.docIs(t, "sicher:team-x", "zwei.md", "zwei") {
		t.Error("zwei.md fehlt in der Replica")
	}

	// Falsche CA: einmal im Log, Fehlerart unreachable, create nicht
	// erreichbar; am Hub kommt nichts mehr an.
	before := calls.Load()
	e.run(t, "node", "hub", "set", "sicher", "--ca-file", caFile(t, e.dir, "andere.pem", other.PEM)).want(t, 0)
	e.run(t, "config", "set", "node", "sync_interval", "1s").want(t, 0)
	eventuallyLog(t, srv, "Fehler in hub_sync", func() bool { return syncStatus(t, ns, "sicher").ErrKind == "unreachable" })
	rounds(t, ns, "sicher", 2)
	log := srv.log.String()
	if n := countLines(log, "Abgleich sicher gescheitert"); n != 1 || !strings.Contains(log, "unknown authority") {
		t.Errorf("%d Fehlerzeilen:\n%s", n, log)
	}
	mcpTool(t, endpoint, bob, "create", mcpnode.CreateInput{Collection: "sicher:team-x", Name: "drei.md", Content: "drei"}, &out)
	wantWriteCode(t, "falsche CA", out, "unreachable", "Hub sicher nicht erreichbar, nichts gespeichert")
	e.run(t, "hub", "doc", "get", "team-x", "drei.md").want(t, 1)
	if n := calls.Load(); n != before {
		t.Errorf("%d Anfragen kamen trotz falscher CA am Hub an", n-before)
	}
	if doc, text := e.docIs(t, "sicher:team-x", "neu.md", "über TLS"), "gelesen wird weiter"; !doc {
		t.Error(text)
	}
	srv.stop(t)
	if log := srv.log.String(); strings.Contains(log, "keph_") {
		t.Errorf("Token im Log:\n%s", log)
	}
}

// Die Weboberfläche hinter einem Proxy mit Präfix (wie Caddys handle
// /kephalaion/* mit uri strip_prefix /kephalaion): Die Seite liegt unter
// <präfix>/, und was sie relativ nennt — gui/style.css, gui/app.js,
// gui/icon.svg, der Eingang gui/api/whoami —, landet unter dem Präfix beim
// Binary, das ihn nicht kennt. Der Hub-Weg <präfix>/hub/v1/… bleibt, wie er
// war. Steht vor dem Rest eine Anmeldung (die VM), ist deren 401 keine
// Antwort des Hubs: kein JSON mit code — daran erkennt die Seite die
// abgelaufene Anmeldung.
func TestGUIBehindPrefix(t *testing.T) {
	e := newCommEnv(t)
	const browser = "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(method, target, accept, ctype, body string, header map[string]string) (*http.Response, string) {
		t.Helper()
		req, err := http.NewRequest(method, target, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if accept != "" {
			req.Header.Set("Accept", accept)
		}
		if ctype != "" {
			req.Header.Set("Content-Type", ctype)
		}
		for k, v := range header {
			req.Header.Set(k, v)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		if resp.StatusCode >= 300 && resp.StatusCode < 400 || resp.Header.Get("Location") != "" {
			t.Errorf("%s %s: Umleitung (HTTP %d, Location %q)", method, target, resp.StatusCode, resp.Header.Get("Location"))
		}
		return resp, string(raw)
	}
	open := proxyHubAt(t, e, nil, "", "/kephalaion", 0)
	pageURL, err := url.Parse(open.URL + "/kephalaion/")
	if err != nil {
		t.Fatal(err)
	}
	// rel löst einen Pfad der Seite auf, wie der Browser es tut.
	rel := func(ref string) string {
		t.Helper()
		u, err := pageURL.Parse(ref)
		if err != nil {
			t.Fatal(err)
		}
		return u.String()
	}

	resp, page := do(http.MethodGet, pageURL.String(), browser, "", "", nil)
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "text/html; charset=utf-8" ||
		!strings.Contains(page, "Kephalaion-Account") {
		t.Fatalf("Seite unter dem Präfix: HTTP %d, Content-Type %q:\n%s", resp.StatusCode, resp.Header.Get("Content-Type"), page)
	}
	// curl an derselben Stelle: weiter die Begrüßung.
	if resp, body := do(http.MethodGet, pageURL.String(), "*/*", "", "", nil); resp.StatusCode != 200 ||
		!strings.HasPrefix(body, "Kephalaion ") || strings.Contains(body, "<html") {
		t.Errorf("Begrüßung unter dem Präfix: HTTP %d %q", resp.StatusCode, body)
	}
	// Die Verweise der Seite, so wie sie im HTML stehen.
	for ref, ctype := range map[string]string{"gui/style.css": "text/css; charset=utf-8",
		"gui/app.js": "text/javascript; charset=utf-8", "gui/icon.svg": "image/svg+xml"} {
		if !strings.Contains(page, `"`+ref+`"`) {
			t.Errorf("die Seite nennt %s nicht", ref)
		}
		if want := open.URL + "/kephalaion/" + ref; rel(ref) != want {
			t.Errorf("%s löst sich zu %s auf, erwartet %s", ref, rel(ref), want)
		}
		if resp, _ := do(http.MethodGet, rel(ref), "*/*", "", "", nil); resp.StatusCode != 200 || resp.Header.Get("Content-Type") != ctype {
			t.Errorf("%s: HTTP %d, Content-Type %q", rel(ref), resp.StatusCode, resp.Header.Get("Content-Type"))
		}
	}
	// Der Eingang, relativ wie in app.js.
	_, script := do(http.MethodGet, rel("gui/app.js"), "*/*", "", "", nil)
	if !strings.Contains(script, `"gui/api/whoami"`) {
		t.Fatal("app.js nennt gui/api/whoami nicht")
	}
	payload := func(account, token string) string {
		b, _ := json.Marshal(map[string]string{"account": account, "token": token})
		return string(b)
	}
	resp, body := do(http.MethodPost, rel("gui/api/whoami"), "application/json", "application/json", payload("bob", e.tokens["bob"]), nil)
	if resp.StatusCode != 200 || strings.TrimSpace(body) != `{"account":"bob","user":"kleist","description":"","collections":[`+
		`{"name":"privat","description":"","rights":{"write":false,"supersede":false,"vendor":[]}},`+
		`{"name":"team-x","description":"","rights":{"write":true,"supersede":false,"vendor":[]}}]}` {
		t.Errorf("whoami der Seite unter dem Präfix: HTTP %d %s", resp.StatusCode, body)
	}
	resp, body = do(http.MethodPost, rel("gui/api/whoami"), "application/json", "application/json", payload("bob", e.tokens["alice"]), nil)
	if resp.StatusCode != 401 || !strings.Contains(body, `"code":"unauthenticated"`) ||
		resp.Header.Get("Content-Type") != "application/json; charset=utf-8" {
		t.Errorf("falsches Token unter dem Präfix: HTTP %d %s", resp.StatusCode, body)
	}
	// Ohne den Präfix kennt der Proxy nichts davon.
	if resp, _ := do(http.MethodGet, open.URL+"/gui/app.js", "*/*", "", "", nil); resp.StatusCode != 404 {
		t.Errorf("/gui/app.js ohne Präfix: HTTP %d", resp.StatusCode)
	}

	// Der Hub-Weg bleibt unverändert: der Vertrag unter <präfix>/hub/v1/…,
	// in Vertragsform.
	r := e.run(t, "hub", "node", "add", "laptop-gui")
	r.want(t, 0)
	node := map[string]string{httpapi.HeaderNode: "laptop-gui", "Authorization": "Bearer " + tokenFrom(t, r.out)}
	resp, body = do(http.MethodPost, open.URL+"/kephalaion/hub/v1/whoami", "", "application/json", "{}", node)
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "application/json" || !strings.Contains(body, `"node":"laptop-gui"`) {
		t.Errorf("Hub-Weg unter dem Präfix: HTTP %d %s", resp.StatusCode, body)
	}
	resp, body = do(http.MethodPost, open.URL+"/kephalaion/hub/v1/whoami", "", "application/json", "{}", nil)
	if resp.StatusCode != 401 || resp.Header.Get("Content-Type") != "application/json" || !strings.Contains(body, `"code":"unauthenticated"`) {
		t.Errorf("Hub-Weg ohne Anmeldung: HTTP %d %s", resp.StatusCode, body)
	}
	if resp, body := do(http.MethodGet, open.URL+"/kephalaion/hub", browser, "", "", nil); resp.StatusCode != 200 ||
		!strings.HasPrefix(body, "Kephalaion Hub.") {
		t.Errorf("/kephalaion/hub mit Accept text/html: HTTP %d %q — dort gibt es keine Seite", resp.StatusCode, body)
	}

	// Die VM: vor dem Rest steht eine Anmeldung. Ohne Sitzung antwortet sie,
	// nicht der Hub — auf die Seite wie auf ihren Eingang, ohne JSON und ohne
	// code; der Hub-Weg geht weiter ohne sie.
	authed := proxyHubAt(t, e, nil, "", "/kephalaion", http.StatusUnauthorized)
	if resp, _ := do(http.MethodGet, authed.URL+"/kephalaion/", browser, "", "", nil); resp.StatusCode != 401 {
		t.Errorf("Seite hinter der Anmeldung ohne Sitzung: HTTP %d", resp.StatusCode)
	}
	resp, body = do(http.MethodPost, authed.URL+"/kephalaion/gui/api/whoami", "application/json", "application/json",
		payload("bob", e.tokens["bob"]), nil)
	if resp.StatusCode != 401 || strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") || strings.Contains(body, `"code"`) {
		t.Errorf("Eingang hinter der Anmeldung ohne Sitzung: HTTP %d, Content-Type %q, %q", resp.StatusCode,
			resp.Header.Get("Content-Type"), body)
	}
	if resp, _ := do(http.MethodPost, authed.URL+"/kephalaion/hub/v1/whoami", "", "application/json", "{}", node); resp.StatusCode != 200 {
		t.Errorf("Hub-Weg neben der Anmeldung: HTTP %d", resp.StatusCode)
	}
}
