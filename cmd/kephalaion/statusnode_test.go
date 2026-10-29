package main

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kephalaion/kephalaion/internal/config"
	"github.com/kephalaion/kephalaion/internal/node/mcpnode"
)

// kephalaion status am Node (Task 035): die Probe ohne Token (Antwort) und
// je Token-Datei des Users die Anmeldung beim Hub (Anmeldung) — pro User wie
// global, auch mit unlesbarer Datenbank. Kein Token in der Ausgabe, der
// Exit-Code bleibt.

// writeSystemNode legt eine globale config an, deren Node unter listen
// lauscht und dessen Datenbank in dataDir liegt.
func writeSystemNode(t *testing.T, dataDir, listen string) {
	t.Helper()
	cfg := config.Config{Node: &config.Section{DB: "sqlite://" + filepath.Join(dataDir, "node.db"), Listen: listen}}
	if err := config.Save(config.SystemPath, cfg); err != nil {
		t.Fatal(err)
	}
}

// unreadableDir legt ein Verzeichnis ohne Rechte an (als root übersprungen).
func unreadableDir(t *testing.T, dir string) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("als root greifen Dateirechte nicht")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
}

// statusT ruft status auf; die Ausgabe zählt für noSecrets.
func (e *setupEnv) statusT(t *testing.T, args ...string) result {
	t.Helper()
	r := runT(t, append([]string{"status"}, args...)...)
	e.outputs = append(e.outputs, r.out, r.errOut)
	return r
}

// nodeLines sind die Zeilen des Abschnitts node: der Ausgabe von status.
func nodeLines(out string) string {
	_, rest, ok := strings.Cut(out, "node: eingerichtet\n")
	if !ok {
		return ""
	}
	return rest
}

// Global mit unlesbarer Datenbank und laufendem Node: Antwort ja, die
// Anmeldung gültig; eine ungültige Token-Datei ist ungültig mit dem Hinweis
// zum Fehlversuch; die .pending daneben steht mit. Statt „serve: nicht
// prüfbar“ steht die Probe, der Hinweis auf sudo bleibt, Exit 0.
func TestStatusNodeProbeGlobal(t *testing.T) {
	e := newSetupEnv(t)
	tok := e.account(t, "ok1")
	e.setup(t, "", "eigen", "ok1", tok).want(t, 0)
	e.put(t, "eigen", "alt.token", e.newToken(t)+"\n")
	e.put(t, "eigen", "p.token.pending", e.newToken(t)+"\n")
	data := filepath.Join(e.dir, "var", "lib", "kephalaion")
	unreadableDir(t, data)
	writeSystemNode(t, data, e.nodeAddr)

	before, _ := e.checks()
	r := e.statusT(t)
	r.want(t, 0, "Quelle: global", "node: eingerichtet",
		"  Antwort:       ja, Version ",
		"  Anmeldung:     eigen/alt ungültig — ",
		"  Anmeldung:     eigen/ok1 gültig (User ok1, Collections team-x)",
		"  Anmeldung:     eigen/p (.pending) ungültig — ",
		"  Hinweis:       Eine ungültige Token-Datei ist bei jedem Aufruf ein Fehlversuch am Node.",
		"sudo -u kephalaion kephalaion status")
	n := nodeLines(r.out)
	if strings.Contains(n, "serve:") {
		t.Errorf("Zeile serve trotz unlesbarer Datenbank:\n%s", r.out)
	}
	if strings.Contains(r.out, "Fehler") {
		t.Errorf("Fehler statt Hinweis:\n%s", r.out)
	}
	if after, _ := e.checks(); after-before != 3 {
		t.Errorf("Anfragen an /account/check: %d, erwartet 3 (eine je Datei)", after-before)
	}
	// Eine Token-Datei, die kein Token hält, wird nicht gefragt.
	e.put(t, "eigen", "kaputt.token", "kein token\n")
	before, _ = e.checks()
	e.statusT(t).want(t, 0, "  Anmeldung:     eigen/kaputt unlesbar — ")
	if after, _ := e.checks(); after-before != 3 {
		t.Errorf("Anfragen an /account/check: %d, erwartet 3", after-before)
	}
}

// fakeNode spielt einen Node nach: initialize nach mcp, /account/check nach
// check. checks zählt die Anfragen mit Token.
func fakeNode(t *testing.T, mcp, check http.HandlerFunc) (listen string, checks *atomic.Int32) {
	t.Helper()
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case mcpnode.Path:
			mcp(w, r)
		case mcpnode.AccountCheckPath:
			n.Add(1)
			check(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://"), &n
}

// closedPort ist eine Loopback-Adresse, an der nichts lauscht: eben noch
// gebunden, dann geschlossen — die Verbindung wird sofort verweigert (an
// 127.0.0.1:1 kann eine Firewall sie still verwerfen).
func closedPort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

// initializeOK antwortet auf initialize wie ein Kephalaion-Node.
func initializeOK(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-06-18","capabilities":{},`+
		`"serverInfo":{"name":"kephalaion","version":"t035"}}}`)
}

// Global ohne antwortenden Node: Antwort nein mit Grund, jede Datei nicht
// geprüft, keine Anfrage mit Token, Exit 0 wie bisher.
func TestStatusNodeProbeNoAnswer(t *testing.T) {
	dir := isolate(t)
	listen, checks := fakeNode(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unexpected", http.StatusInternalServerError)
	})
	data := filepath.Join(dir, "var", "lib", "kephalaion")
	unreadableDir(t, data)
	writeSystemNode(t, data, listen)
	tokens := filepath.Join(dir, "config", "kephalaion", "tokens", "eigen")
	if err := os.MkdirAll(tokens, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tokens, "ok1.token"), []byte(dummyTokenA+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := runT(t, "status")
	r.want(t, 0, "Quelle: global", "  Antwort:       nein — Node unter http://"+listen+"/mcp: Proxy antwortet, aber der "+
		"Node dahinter nicht (HTTP 503",
		"  Anmeldung:     eigen/ok1 nicht geprüft — Node antwortet nicht",
		"sudo -u kephalaion kephalaion status")
	if checks.Load() != 0 {
		t.Errorf("%d Anfragen mit Token an einen Node ohne Antwort", checks.Load())
	}
	if strings.Contains(r.out, dummyTokenA) {
		t.Error("Token in der Ausgabe")
	}
	// Nichts lauscht: ebenfalls nein, mit Grund.
	writeSystemNode(t, data, closedPort(t))
	runT(t, "status").want(t, 0, "  Antwort:       nein — ", "nicht erreichbar",
		"  Anmeldung:     eigen/ok1 nicht geprüft — Node antwortet nicht")
}

// Der Hub antwortet nicht innerhalb der Frist: nicht geprüft, und status
// kehrt nach der kurzen Frist zurück — nicht nach den 90 s von node account
// list.
func TestStatusNodeProbeCheckTimeout(t *testing.T) {
	dir := isolate(t)
	old := statusCheckTimeout
	statusCheckTimeout = 300 * time.Millisecond
	t.Cleanup(func() { statusCheckTimeout = old })
	listen, checks := fakeNode(t, initializeOK, func(w http.ResponseWriter, r *http.Request) {
		// Hängt, bis der Client aufgibt.
		select {
		case <-r.Context().Done():
		case <-time.After(10 * time.Second):
		}
	})
	cfg := filepath.Join(dir, "config", "kephalaion", "config.yaml")
	runT(t, "node", "init", "--config", cfg, "--db", "sqlite://"+filepath.Join(dir, "node.db"), "--listen", listen).want(t, 0)
	tokens := filepath.Join(dir, "config", "kephalaion", "tokens", "eigen")
	if err := os.MkdirAll(tokens, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tokens, "ok1.token"), []byte(dummyTokenA+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	r := runT(t, "status")
	took := time.Since(start)
	r.want(t, 0, "Quelle: pro User", "  serve:         ", "  Antwort:       ja, Version t035",
		"  Anmeldung:     eigen/ok1 nicht geprüft — ")
	if took > 3*time.Second {
		t.Errorf("status brauchte %v, erwartet die kurze Frist", took)
	}
	if checks.Load() != 1 {
		t.Errorf("%d Anfragen an /account/check, erwartet 1", checks.Load())
	}
	if strings.Contains(r.out, dummyTokenA) {
		t.Error("Token in der Ausgabe")
	}
}

// Pro User: Antwort neben serve; ohne Token-Dateien der Hinweis auf setup —
// nicht für den Systembenutzer; ein Token-Verzeichnis ohne Rechte nennt den
// Grund ohne Hinweis. Exit 0 in jedem Fall.
func TestStatusNodeProbeTokens(t *testing.T) {
	dir := isolate(t)
	listen, _ := fakeNode(t, initializeOK, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unexpected", http.StatusInternalServerError)
	})
	runT(t, "node", "init", "--db", "sqlite://"+filepath.Join(dir, "node.db"), "--listen", listen).want(t, 0)
	tokensDir := filepath.Join(dir, "config", "kephalaion", "tokens")

	r := runT(t, "status")
	r.want(t, 0, "  serve:         läuft nicht\n  Antwort:       ja, Version t035\n",
		"  Anmeldung:     keine Token-Dateien unter "+tokensDir+" — einen Account einrichten: kephalaion node account "+
			"setup <hub> <account> <einrichtungstoken>")

	// Der Systembenutzer meldet sich nicht an: kein Hinweis auf setup.
	oldUser := currentUserName
	currentUserName = func() string { return config.SystemUser }
	r = runT(t, "status")
	currentUserName = oldUser
	r.want(t, 0, "  Anmeldung:     keine Token-Dateien unter "+tokensDir+"\n")
	if strings.Contains(r.out, "account setup") {
		t.Errorf("Hinweis auf setup für den Systembenutzer:\n%s", r.out)
	}

	// Das Token-Verzeichnis ohne Rechte: der Grund, kein Hinweis auf setup.
	unreadableDir(t, tokensDir)
	r = runT(t, "status")
	r.want(t, 0, "  Anmeldung:     Token-Verzeichnis "+tokensDir+" nicht lesbar (permission denied)")
	if strings.Contains(r.out, "account setup") {
		t.Errorf("Hinweis auf setup trotz unlesbarem Verzeichnis:\n%s", r.out)
	}
}
