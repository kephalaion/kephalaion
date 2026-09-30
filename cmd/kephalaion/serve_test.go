package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/kephalaion/kephalaion/internal/buildinfo"
	"github.com/kephalaion/kephalaion/internal/config"
	"github.com/kephalaion/kephalaion/internal/contract/httpapi"
	"github.com/kephalaion/kephalaion/internal/reqlog"
)

// syncBuffer ist ein Puffer, in den serve nebenläufig schreibt.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// running ist ein serve im Hintergrund.
type running struct {
	addrs  map[config.Role]string
	log    *syncBuffer
	cancel context.CancelFunc
	done   chan error
	once   sync.Once
}

// startServe startet serve mit cfg, beide Rollen auf Port 0, und wartet, bis
// es lauscht.
func startServe(t *testing.T, cfg config.Config) *running {
	t.Helper()
	return startServeWith(t, cfg, false)
}

// startServeWith startet serve wie startServe; system sagt, ob die globale
// config gilt.
func startServeWith(t *testing.T, cfg config.Config, system bool) *running {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	r := &running{log: &syncBuffer{}, cancel: cancel, done: make(chan error, 1)}
	ready := make(chan map[config.Role]string, 1)
	go func() {
		r.done <- serve(ctx, cfg, system, reqlog.New(r.log), func(a map[config.Role]string) { ready <- a })
	}()
	select {
	case r.addrs = <-ready:
	case err := <-r.done:
		cancel()
		t.Fatalf("serve: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("serve lauscht nicht")
	}
	t.Cleanup(func() { r.stop(t) })
	return r
}

func (r *running) stop(t *testing.T) {
	t.Helper()
	r.once.Do(func() {
		r.cancel()
		select {
		case err := <-r.done:
			if err != nil {
				t.Errorf("serve endete mit %v", err)
			}
		case <-time.After(15 * time.Second):
			t.Error("serve endet nicht")
		}
	})
}

// portZero liefert die config mit listen auf Port 0 für beide Rollen.
func portZero(t *testing.T, cfgPath string) config.Config {
	t.Helper()
	cfg, _, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Hub.Listen, cfg.Node.Listen = "127.0.0.1:0", "127.0.0.1:0"
	return cfg
}

func TestServe(t *testing.T) {
	slow(t, "das Beenden wartet oft 5 s auf eine ungenutzte Verbindung (net/http)")
	dir := isolate(t)
	cfgPath := setup(t, dir)
	c := "--config=" + cfgPath
	runT(t, "hub", "collection", "add", "team-x", c).want(t, 0)
	r := runT(t, "hub", "node", "add", "laptop", c)
	r.want(t, 0)
	token := tokenFrom(t, r.out)
	runT(t, "status", c).want(t, 0, "serve:         läuft nicht")

	srv := startServe(t, portZero(t, cfgPath))
	for _, role := range config.Roles {
		if _, port, _ := net.SplitHostPort(srv.addrs[role]); port == "0" || port == "" {
			t.Errorf("%s: Adresse %q", role, srv.addrs[role])
		}
	}
	// Der Hub beantwortet den Vertrag unter /hub.
	post := func(node, tok string) int {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPost, "http://"+srv.addrs[config.Hub]+"/hub/v1/whoami", strings.NewReader("{}"))
		req.Header.Set(httpapi.HeaderNode, node)
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var body map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&body)
		return resp.StatusCode
	}
	if code := post("laptop", token); code != 200 {
		t.Errorf("whoami: HTTP %d", code)
	}
	if code := post("Laptop", token); code != 401 {
		t.Errorf("ungültiger Name: HTTP %d", code)
	}
	if code := post(token, token); code != 401 {
		t.Errorf("Token als Name: HTTP %d", code)
	}
	// Hinter einem Proxy: die Adresse des Aufrufers aus X-Forwarded-For im Log.
	req, _ := http.NewRequest(http.MethodPost, "http://"+srv.addrs[config.Hub]+"/hub/v1/whoami", strings.NewReader("{}"))
	req.Header.Set(httpapi.HeaderNode, "laptop")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Forwarded-For", "9.141.8.157, 10.0.0.1")
	if resp, err := http.DefaultClient.Do(req); err != nil {
		t.Fatal(err)
	} else {
		resp.Body.Close()
	}
	// Der Hub prüft Host wie der Node: dieser Rechner mit dem eigenen Port.
	_, port, _ := net.SplitHostPort(srv.addrs[config.Hub])
	for host, want := range map[string]int{"localhost:" + port: 200, "evil.example:" + port: 403,
		"localhost:1": 403, "localhost": 403} {
		req, _ := http.NewRequest(http.MethodPost, "http://"+srv.addrs[config.Hub]+"/hub/v1/whoami", strings.NewReader("{}"))
		req.Host = host
		req.Header.Set(httpapi.HeaderNode, "laptop")
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("Host %q am Hub: HTTP %d, erwartet %d", host, resp.StatusCode, want)
		}
	}
	// Ein zweiter serve auf denselben Rollen scheitert an der Sperre.
	err := serve(context.Background(), portZero(t, cfgPath), false, reqlog.New(&bytes.Buffer{}), nil)
	if err == nil || !strings.Contains(err.Error(), "läuft schon") {
		t.Errorf("zweiter serve: %v", err)
	}
	// CLI-Kommandos laufen daneben; status zeigt serve.
	runT(t, "hub", "collection", "add", "privat", c).want(t, 0)
	runT(t, "status", c).want(t, 0, "serve:         läuft")

	srv.stop(t)
	log := srv.log.String()
	for _, want := range []string{"hub lauscht auf 127.0.0.1:", "node lauscht auf 127.0.0.1:",
		"hub POST /hub/v1/whoami 200", "node=laptop", "hub POST /hub/v1/whoami 401", "node=(ungültig)", "beendet",
		" via=9.141.8.157 node=laptop"} {
		if !strings.Contains(log, want) {
			t.Errorf("Log ohne %q:\n%s", want, log)
		}
	}
	if strings.Contains(log, token) || strings.Contains(log, "keph_") {
		t.Errorf("Token im Log:\n%s", log)
	}
	runT(t, "status", c).want(t, 0, "serve:         läuft nicht")
}

func TestServeLoopbackOnly(t *testing.T) {
	dir := isolate(t)
	cfgPath := setup(t, dir)
	for _, bad := range []struct{ hub, node string }{
		{"0.0.0.0:0", "127.0.0.1:0"},
		{"127.0.0.1:0", "192.168.1.10:7433"},
		{"127.0.0.1:0", "[::]:0"},
	} {
		cfg := portZero(t, cfgPath)
		cfg.Hub.Listen, cfg.Node.Listen = bad.hub, bad.node
		err := serve(context.Background(), cfg, false, reqlog.New(&bytes.Buffer{}), nil)
		if err == nil || !strings.Contains(err.Error(), "nur auf diesem Rechner") {
			t.Errorf("%+v: %v", bad, err)
		}
	}
	// localhost und ::1 gehen.
	cfg := portZero(t, cfgPath)
	cfg.Hub.Listen, cfg.Node.Listen = "localhost:0", "[::1]:0"
	if ln, err := net.Listen("tcp", "[::1]:0"); err == nil {
		ln.Close()
		startServe(t, cfg)
	}
}

// Die Hilfe nennt die Pfade des Vertrags unter /hub und die Werkzeuge, auch
// die, die schreiben.
func TestServeHelp(t *testing.T) {
	runT(t, "serve", "--help").want(t, 0, "/hub/v1/sync und", "/hub/v1/create, /hub/v1/write,",
		"http://localhost:7434/hub", "/v1/… an der\n         Wurzel antwortet 404",
		"create, write, delete und rename über den Hub", "nie ein Inhalt", "Reverse-Proxy", "X-Forwarded-For")
}

// Der Hub-Listener ordnet seine Teile selbst: an der Wurzel die Begrüßung
// mit der Version, an /hub und /hub/ der kurze Text ohne, unter /hub/ der
// Vertrag (405 und 404 dort in Vertragsform), /v1/… an der Wurzel — die
// alte Adresse ohne /hub — 404 text/plain mit dem Hinweis, alles andere 404
// text/plain, auch ein Pfad, den der Mux sonst per 301 bereinigte. Kein
// Pfad und keine Methode bekommt ein 3xx.
func TestServeHubListener(t *testing.T) {
	dir := isolate(t)
	cfgPath := setup(t, dir)
	srv := startServe(t, portZero(t, cfgPath))
	base := "http://" + srv.addrs[config.Hub]
	// Ohne Keep-Alive: Eine offene Verbindung hielte das Beenden auf.
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	version := buildinfo.Get().Version
	const plain, jsonType = "text/plain; charset=utf-8", "application/json"
	for _, c := range []struct {
		method, path string
		status       int
		ctype        string
		want         []string // im Body
		without      []string // nicht im Body
	}{
		{http.MethodGet, "/", 200, plain, []string{"Kephalaion " + version, "Rolle hub", "/hub/v1/<vorgang>"}, nil},
		{http.MethodHead, "/", 200, plain, nil, []string{"Kephalaion"}},
		{http.MethodGet, "/hub", 200, plain, []string{"Kephalaion Hub", "POST /hub/v1/<vorgang>"}, []string{version}},
		{http.MethodGet, "/hub/", 200, plain, []string{"Kephalaion Hub", "POST /hub/v1/<vorgang>"}, []string{version}},
		{http.MethodHead, "/hub/", 200, plain, nil, []string{"Kephalaion"}},
		{http.MethodPost, "/hub/", 405, plain, []string{"nur GET"}, nil},
		{http.MethodPost, "/", 405, plain, []string{"nur GET"}, nil},
		{http.MethodGet, "/hub/v1/whoami", 405, jsonType, []string{`"code":"invalid"`, "nur POST"}, nil},
		{http.MethodPost, "/hub/nix", 404, jsonType, []string{`"code":"invalid"`, "unbekannter Pfad; erwartet POST /v1/"}, nil},
		{http.MethodPost, "/hub/v1/nix", 404, jsonType, []string{`"code":"invalid"`, "unbekannter Vorgang"}, nil},
		{http.MethodPost, "/hub/v1/whoami", 401, jsonType, []string{`"code":"unauthenticated"`}, nil},
		{http.MethodPost, "/v1/whoami", 404, plain, []string{"unbekannter Pfad /v1/whoami: der Vertrag liegt unter /hub/v1/…",
			"fehlt /hub am Ende der Adresse des Hub-Eintrags?"}, []string{`"code"`, "invalid"}},
		{http.MethodGet, "/v1", 404, plain, []string{"unbekannter Pfad /v1:", "fehlt /hub"}, nil},
		{http.MethodPost, "/v1/", 404, plain, []string{"fehlt /hub"}, nil},
		{http.MethodGet, "/nix", 404, plain, []string{"unbekannter Pfad /nix"}, []string{`"code"`, "/hub"}},
		{http.MethodPost, "/hubx", 404, plain, []string{"unbekannter Pfad /hubx"}, nil},
		{http.MethodGet, "/hub//v1/whoami", 404, plain, []string{"unbekannter Pfad"}, []string{`"code"`}},
		{http.MethodPost, "/hub/v1//whoami", 404, plain, []string{"unbekannter Pfad"}, []string{`"code"`}},
		{http.MethodGet, "/hub/../hub/", 404, plain, []string{"unbekannter Pfad"}, []string{"Kephalaion"}},
		{http.MethodGet, "/./hub", 404, plain, []string{"unbekannter Pfad"}, []string{"Kephalaion"}},
		{http.MethodGet, "//hub/", 404, plain, []string{"unbekannter Pfad"}, []string{"Kephalaion"}},
	} {
		req, _ := http.NewRequest(c.method, base+c.path, strings.NewReader("{}"))
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", c.method, c.path, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		name := c.method + " " + c.path
		if resp.StatusCode != c.status {
			t.Errorf("%s: HTTP %d, erwartet %d; Body %q", name, resp.StatusCode, c.status, body)
		}
		if resp.StatusCode >= 300 && resp.StatusCode < 400 || resp.Header.Get("Location") != "" {
			t.Errorf("%s: Umleitung (HTTP %d, Location %q)", name, resp.StatusCode, resp.Header.Get("Location"))
		}
		if got := resp.Header.Get("Content-Type"); got != c.ctype {
			t.Errorf("%s: Content-Type %q, erwartet %q", name, got, c.ctype)
		}
		if c.ctype == plain && resp.Header.Get("Cache-Control") != "no-store" {
			t.Errorf("%s: Cache-Control %q", name, resp.Header.Get("Cache-Control"))
		}
		if c.method == http.MethodHead && len(body) != 0 {
			t.Errorf("%s: Body %q, erwartet keinen", name, body)
		}
		for _, w := range c.want {
			if !strings.Contains(string(body), w) {
				t.Errorf("%s: Body ohne %q:\n%s", name, w, body)
			}
		}
		for _, w := range c.without {
			if strings.Contains(string(body), w) {
				t.Errorf("%s: Body mit %q:\n%s", name, w, body)
			}
		}
	}
	// Die Host-Prüfung liegt vor allem, auch vor der Begrüßung.
	req, _ := http.NewRequest(http.MethodGet, base+"/", nil)
	req.Host = "evil.example:80"
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Errorf("Begrüßung mit fremdem Host: HTTP %d, erwartet 403", resp.StatusCode)
	}
	// Ein Eintrag mit alter Adresse (ohne /hub): check nennt den Hinweis des
	// Hubs und sagt, was fehlt; mit /hub ist der Hub erreichbar.
	c := "--config=" + cfgPath
	r := runT(t, "hub", "node", "add", "laptop", c)
	r.want(t, 0)
	runIn(t, tokenFrom(t, r.out), "node", "hub", "add", "alt", "--node", "laptop", "--transport", "http",
		"--address", base, "--token-stdin", c).want(t, 0)
	runT(t, "node", "hub", "check", "alt", c).want(t, 1, "Hub alt (http "+base+"): der Pfad ist nicht der Hub "+
		"(HTTP 404: unbekannter Pfad /v1/whoami: der Vertrag liegt unter /hub/v1/… — fehlt /hub am Ende der Adresse "+
		"des Hub-Eintrags?): fehlt /hub am Ende der Adresse (etwa "+base+"/hub, hinter einem Proxy "+
		"https://host/kephalaion/hub), oder kennt der Proxy die Route nicht?")
	runT(t, "node", "hub", "set", "alt", "--address", base+"/hub", c).want(t, 0)
	runT(t, "node", "hub", "check", "alt", c).want(t, 0, "Hub alt: erreichbar (http "+base+"/hub)", "Node-Name:    laptop")
	srv.stop(t)
	if log := srv.log.String(); !strings.Contains(log, "hub GET / 200") || !strings.Contains(log, "hub POST /v1/whoami 404") ||
		!strings.Contains(log, "hub GET /hub//v1/whoami 404") {
		t.Errorf("Log ohne die Zeilen der Wurzel:\n%s", log)
	}
}

// Beenden per Signal: serve über die Kommandozeile, SIGTERM an den eigenen
// Prozess, Exit-Code 0.
func TestServeSignal(t *testing.T) {
	dir := isolate(t)
	cfgPath := setup(t, dir)
	cfg := portZero(t, cfgPath)
	for _, r := range config.Roles {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		cfg.Section(r).Listen = ln.Addr().String()
		ln.Close()
	}
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	ready := make(chan struct{})
	serveReady = func(map[config.Role]string) { close(ready) }
	defer func() { serveReady = nil }()
	var out, errOut syncBuffer
	done := make(chan int, 1)
	go func() { done <- run([]string{"serve", "--config", cfgPath}, strings.NewReader(""), &out, &errOut) }()
	select {
	case <-ready:
	case code := <-done:
		t.Fatalf("serve endete mit %d: %s", code, errOut.String())
	case <-time.After(10 * time.Second):
		t.Fatal("serve lauscht nicht")
	}
	if err := syscall.Kill(syscall.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-done:
		if code != 0 || !strings.Contains(errOut.String(), "beendet") {
			t.Errorf("Exit-Code %d, Log:\n%s", code, errOut.String())
		}
	case <-time.After(15 * time.Second):
		t.Fatal("serve endet nicht nach SIGTERM")
	}
}
