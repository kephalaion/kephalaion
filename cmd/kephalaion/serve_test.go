package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

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
	// Der Hub beantwortet den Vertrag.
	post := func(node, tok string) int {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPost, "http://"+srv.addrs[config.Hub]+"/v1/whoami", strings.NewReader("{}"))
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
	req, _ := http.NewRequest(http.MethodPost, "http://"+srv.addrs[config.Hub]+"/v1/whoami", strings.NewReader("{}"))
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
		req, _ := http.NewRequest(http.MethodPost, "http://"+srv.addrs[config.Hub]+"/v1/whoami", strings.NewReader("{}"))
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
		"hub POST /v1/whoami 200", "node=laptop", "hub POST /v1/whoami 401", "node=(ungültig)", "beendet",
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

// Die Hilfe nennt die Pfade des Vertrags und die Werkzeuge, auch die, die
// schreiben.
func TestServeHelp(t *testing.T) {
	runT(t, "serve", "--help").want(t, 0, "/v1/sync und", "/v1/create, /v1/write, /v1/delete, /v1/rename",
		"create, write, delete und rename über den Hub", "nie ein Inhalt", "Reverse-Proxy", "X-Forwarded-For")
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
