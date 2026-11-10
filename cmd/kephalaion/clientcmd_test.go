package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kephalaion/kephalaion/internal/buildinfo"
	"github.com/kephalaion/kephalaion/internal/config"
)

// node client (Task 037): die Auskunft für Clients. Kein Test erreicht den
// Node dieses Rechners (127.0.0.1:7433 verweigert nodeDial aus TestMain) oder
// das echte Token-Verzeichnis (isolate).

// clientJSON ruft node client --json auf und prüft die Zusage: Exit 0, auf
// stdout genau ein JSON-Objekt, nichts auf stderr, kein Token.
func clientJSON(t *testing.T, args ...string) clientInfo {
	t.Helper()
	r := runT(t, append([]string{"node", "client", "--json"}, args...)...)
	if r.code != 0 {
		t.Fatalf("Exit %d\nstdout:\n%s\nstderr:\n%s", r.code, r.out, r.errOut)
	}
	if r.errOut != "" {
		t.Errorf("mit --json etwas auf stderr:\n%s", r.errOut)
	}
	if leaksToken(r.out) {
		t.Fatalf("Token in der Ausgabe:\n%s", r.out)
	}
	dec := json.NewDecoder(strings.NewReader(r.out))
	var raw map[string]any
	if err := dec.Decode(&raw); err != nil {
		t.Fatalf("kein JSON-Objekt: %v\n%s", err, r.out)
	}
	if dec.More() || strings.TrimSpace(r.out[dec.InputOffset():]) != "" {
		t.Fatalf("nach dem Objekt steht mehr:\n%s", r.out)
	}
	for _, k := range []string{"format", "kephalaion", "config", "node", "tokensDir", "hubs"} {
		if _, ok := raw[k]; !ok {
			t.Errorf("Feld %s fehlt:\n%s", k, r.out)
		}
	}
	var info clientInfo
	if err := json.Unmarshal([]byte(r.out), &info); err != nil {
		t.Fatal(err)
	}
	return info
}

// nodeAt schreibt eine config mit einem Node, der auf listen lauscht.
func nodeAt(t *testing.T, path, listen string) {
	t.Helper()
	cfg := config.Config{Node: &config.Section{DB: "sqlite:///nirgends/node.db", Listen: listen}}
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
}

// probeNodeServer ist ein Node, der auf initialize antwortet; tokens zählt
// Anfragen mit einem Token-Header.
func probeNodeServer(t *testing.T) (listen string, tokens *atomic.Int32) {
	t.Helper()
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for k := range r.Header {
			if strings.HasPrefix(strings.ToLower(k), "x-keph-token-") {
				n.Add(1)
			}
		}
		initializeOK(w, r)
	}))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://"), &n
}

func ptrIs(p *string, want string) bool { return p != nil && *p == want }

// Die Quellen der config: des Users, global, KEPHALAION_CONFIG, --config,
// keine; eine fehlende Datei hinter --config bzw. KEPHALAION_CONFIG.
func TestNodeClientConfig(t *testing.T) {
	dir := isolate(t)
	userCfg, err := config.UserPath()
	if err != nil {
		t.Fatal(err)
	}

	// Keine config: source none, path null, Standardadresse; der Node dieses
	// Rechners antwortet im Test nie.
	info := clientJSON(t)
	if info.Format != 1 || info.Kephalaion != buildinfo.Get().Version {
		t.Errorf("format/kephalaion: %d %q", info.Format, info.Kephalaion)
	}
	if info.Config.Source != "none" || info.Config.Path != nil || info.Config.Exists || info.Config.Problem != nil {
		t.Errorf("keine config: %+v", info.Config)
	}
	if info.Node.Source != "default" || info.Node.Listen != nil || info.Node.URL != "http://127.0.0.1:7433/mcp" ||
		info.Node.Reachable == nil || *info.Node.Reachable || info.Node.Version != nil {
		t.Errorf("Node ohne config: %+v", info.Node)
	}
	if info.TokensDir != filepath.Join(dir, "config", "kephalaion", "tokens") || len(info.Hubs) != 0 || info.Hubs == nil {
		t.Errorf("tokensDir/hubs: %q %v", info.TokensDir, info.Hubs)
	}

	// Globale config.
	listen, tokens := probeNodeServer(t)
	nodeAt(t, config.SystemPath, listen)
	info = clientJSON(t)
	if info.Config.Source != "system" || !ptrIs(info.Config.Path, config.SystemPath) || !info.Config.Exists {
		t.Errorf("global: %+v", info.Config)
	}
	if info.Node.Source != "config" || !ptrIs(info.Node.Listen, listen) || info.Node.URL != "http://"+listen+"/mcp" ||
		info.Node.Reachable == nil || !*info.Node.Reachable || !ptrIs(info.Node.Version, "t035") {
		t.Errorf("Node global: %+v", info.Node)
	}

	// Die config des Users geht vor.
	nodeAt(t, userCfg, closedPort(t))
	info = clientJSON(t)
	if info.Config.Source != "user" || !ptrIs(info.Config.Path, userCfg) || !info.Config.Exists {
		t.Errorf("User: %+v", info.Config)
	}
	if info.Node.Reachable == nil || *info.Node.Reachable || info.Node.Version != nil {
		t.Errorf("Node antwortet nicht: %+v", info.Node)
	}

	// KEPHALAION_CONFIG und --config.
	envCfg := filepath.Join(dir, "env.yaml")
	nodeAt(t, envCfg, listen)
	t.Setenv("KEPHALAION_CONFIG", envCfg)
	info = clientJSON(t)
	if info.Config.Source != "env" || !ptrIs(info.Config.Path, envCfg) || !info.Config.Exists || !*info.Node.Reachable {
		t.Errorf("env: %+v %+v", info.Config, info.Node)
	}
	flagCfg := filepath.Join(dir, "flag.yaml")
	nodeAt(t, flagCfg, listen)
	info = clientJSON(t, "--config", flagCfg)
	if info.Config.Source != "flag" || !ptrIs(info.Config.Path, flagCfg) || !info.Config.Exists {
		t.Errorf("--config: %+v", info.Config)
	}

	// Fehlende Datei hinter --config bzw. KEPHALAION_CONFIG: exists false,
	// Standardadresse.
	missing := filepath.Join(dir, "fehlt.yaml")
	info = clientJSON(t, "--config", missing, "--no-probe")
	if info.Config.Source != "flag" || !ptrIs(info.Config.Path, missing) || info.Config.Exists ||
		info.Node.Source != "default" {
		t.Errorf("--config fehlt: %+v %+v", info.Config, info.Node)
	}
	t.Setenv("KEPHALAION_CONFIG", missing)
	info = clientJSON(t, "--no-probe")
	if info.Config.Source != "env" || !ptrIs(info.Config.Path, missing) || info.Config.Exists {
		t.Errorf("env fehlt: %+v", info.Config)
	}

	// --no-probe: reachable und version null, keine Anfrage.
	info = clientJSON(t, "--config", flagCfg, "--no-probe")
	if info.Node.Reachable != nil || info.Node.Version != nil {
		t.Errorf("--no-probe: %+v", info.Node)
	}
	if tokens.Load() != 0 {
		t.Errorf("Anfragen mit Token: %d", tokens.Load())
	}
}

// Eine kaputte und eine nicht lesbare config: problem ohne Inhalt der Datei,
// Standardadresse, Exit 0.
func TestNodeClientConfigProblem(t *testing.T) {
	dir := isolate(t)
	broken := filepath.Join(dir, "kaputt.yaml")
	writeText(t, broken, "node:\n  geheim-"+strings.Repeat("x", 8)+": [\n")
	info := clientJSON(t, "--config", broken, "--no-probe")
	if !info.Config.Exists || info.Config.Problem == nil || strings.Contains(*info.Config.Problem, "geheim") ||
		info.Node.Source != "default" || info.Node.URL != "http://127.0.0.1:7433/mcp" {
		t.Errorf("kaputt: %+v %+v", info.Config, info.Node)
	}
	unknown := filepath.Join(dir, "unbekannt.yaml")
	writeText(t, unknown, "node:\n  db: sqlite:///x\n  geheimschluessel: 1\n")
	info = clientJSON(t, "--config", unknown, "--no-probe")
	if info.Config.Problem == nil || strings.Contains(*info.Config.Problem, "geheimschluessel") {
		t.Errorf("unbekannter Schlüssel: %+v", info.Config)
	}
	if os.Geteuid() == 0 {
		t.Skip("als root greifen Dateirechte nicht")
	}
	locked := filepath.Join(dir, "gesperrt.yaml")
	nodeAt(t, locked, "127.0.0.1:7500")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	info = clientJSON(t, "--config", locked, "--no-probe")
	if !info.Config.Exists || info.Config.Problem == nil || !strings.Contains(*info.Config.Problem, "nicht lesbar") ||
		info.Node.Source != "default" {
		t.Errorf("nicht lesbar: %+v %+v", info.Config, info.Node)
	}
}

// Eine Wildcard in listen wird Loopback.
func TestNodeClientWildcard(t *testing.T) {
	dir := isolate(t)
	listen, _ := probeNodeServer(t)
	port := listen[strings.LastIndex(listen, ":")+1:]
	cfg := filepath.Join(dir, "c.yaml")
	nodeAt(t, cfg, "0.0.0.0:"+port)
	info := clientJSON(t, "--config", cfg)
	if !ptrIs(info.Node.Listen, "0.0.0.0:"+port) || info.Node.URL != "http://127.0.0.1:"+port+"/mcp" ||
		info.Node.Reachable == nil || !*info.Node.Reachable {
		t.Errorf("0.0.0.0: %+v", info.Node)
	}
	nodeAt(t, cfg, "[::]:7433")
	info = clientJSON(t, "--config", cfg, "--no-probe")
	if info.Node.URL != "http://[::1]:7433/mcp" {
		t.Errorf("[::]: %+v", info.Node)
	}
}

// Die Wahl je Hub: Standard, einzige, Wahl fehlt mit Kandidaten, --hub ohne
// Verzeichnis, .pending allein und neben der .token, --account mit und ohne
// Token-Datei (kein Rückfall).
func TestNodeClientHubs(t *testing.T) {
	e := newTokensEnv(t)
	e.put(t, "eins", "kp.token", dummyTokenA+"\n")
	e.put(t, "eins", "kp.token.pending", dummyTokenB+"\n")
	e.put(t, "zwei", "alice.token", dummyTokenA+"\n")
	e.put(t, "zwei", "bob.token", dummyTokenB+"\n")
	e.put(t, "warte", "neu.token.pending", dummyTokenC+"\n")
	byAlias := func(info clientInfo) map[string]clientHub {
		m := map[string]clientHub{}
		for _, h := range info.Hubs {
			m[h.Alias] = h
		}
		return m
	}

	info := clientJSON(t, "--no-probe")
	hubs := byAlias(info)
	if len(info.Hubs) != 3 || info.Hubs[0].Alias != "eins" || info.Hubs[2].Alias != "zwei" {
		t.Fatalf("Hubs: %+v", info.Hubs)
	}
	if h := hubs["eins"]; h.Choice != "only" || !ptrIs(h.Account, "kp") || !ptrIs(h.TokenFile, filepath.Join(e.tokens, "eins", "kp.token")) ||
		!reflect.DeepEqual(h.Candidates, []string{"kp"}) || !reflect.DeepEqual(h.Pending, []string{"kp"}) {
		t.Errorf("eins: %+v", h)
	}
	if h := hubs["zwei"]; h.Choice != "missing" || h.Account != nil || h.TokenFile != nil ||
		!reflect.DeepEqual(h.Candidates, []string{"alice", "bob"}) || len(h.Notes) == 0 {
		t.Errorf("zwei: %+v", h)
	}
	if h := hubs["warte"]; h.Choice != "missing" || len(h.Candidates) != 0 || h.Candidates == nil ||
		!reflect.DeepEqual(h.Pending, []string{"neu"}) {
		t.Errorf("warte: %+v", h)
	}

	// Standard.
	writeText(t, filepath.Join(e.tokens, "zwei", "default"), "bob\n")
	hubs = byAlias(clientJSON(t, "--no-probe"))
	if h := hubs["zwei"]; h.Choice != "default" || !ptrIs(h.Account, "bob") ||
		!ptrIs(h.TokenFile, filepath.Join(e.tokens, "zwei", "bob.token")) {
		t.Errorf("Standard: %+v", h)
	}

	// --account vor dem Standard; ohne Token-Datei kein Rückfall.
	hubs = byAlias(clientJSON(t, "--no-probe", "--account", "zwei=alice"))
	if h := hubs["zwei"]; h.Choice != "override" || !ptrIs(h.Account, "alice") {
		t.Errorf("--account: %+v", h)
	}
	hubs = byAlias(clientJSON(t, "--no-probe", "--account", "zwei=carol", "--account", "fern=x"))
	if h := hubs["zwei"]; h.Choice != "override" || !ptrIs(h.Account, "carol") || h.TokenFile != nil || len(h.Notes) != 1 ||
		!strings.Contains(h.Notes[0], "carol") {
		t.Errorf("--account ohne Token-Datei: %+v", h)
	}
	if h, ok := hubs["fern"]; !ok || h.Choice != "override" || h.TokenFile != nil {
		t.Errorf("Hub nur aus --account: %+v", h)
	}

	// --hub: genau die genannten, auch ohne Verzeichnis.
	info = clientJSON(t, "--no-probe", "--hub", "zwei", "--hub", "nirgends", "--hub", "zwei")
	if len(info.Hubs) != 2 || info.Hubs[0].Alias != "nirgends" || info.Hubs[1].Alias != "zwei" {
		t.Fatalf("--hub: %+v", info.Hubs)
	}
	if h := info.Hubs[0]; h.Choice != "missing" || h.Account != nil || len(h.Candidates) != 0 || h.Candidates == nil ||
		h.Pending == nil || len(h.Notes) != 1 {
		t.Errorf("--hub ohne Verzeichnis: %+v", h)
	}
	runT(t, "node", "client", "--hub", "zwei", "--account", "eins=kp").want(t, 2, "steht nicht unter --hub")
}

// --format 1 geht, eine unbekannte Fassung ist ein falscher Aufruf, der die
// getragenen nennt; die Ausgabe ohne --json ist lesbar und ohne Token.
func TestNodeClientFormatAndText(t *testing.T) {
	e := newTokensEnv(t)
	e.put(t, "vm", "alice.token", dummyTokenA+"\n")
	e.put(t, "vm", "bob.token", dummyTokenB+"\n")
	if info := clientJSON(t, "--format", "1", "--no-probe"); info.Format != 1 {
		t.Errorf("format %d", info.Format)
	}
	r := runT(t, "node", "client", "--json", "--format", "2")
	r.want(t, 2, "--format 2", "trägt die Fassungen 1")
	if r.out != "" {
		t.Errorf("stdout bei falschem Aufruf:\n%s", r.out)
	}
	runT(t, "node", "client", "--format", "x").want(t, 2)
	runT(t, "node", "client", "--hub", "VM").want(t, 2, "--hub")
	runT(t, "node", "client", "--account", "vm").want(t, 2, "<hub>=<account>")

	r = runT(t, "node", "client")
	r.want(t, 0, "config:    keine", "Node:      http://127.0.0.1:7433/mcp (Standardadresse) — nicht erreichbar",
		"Tokens:    "+e.tokens, "Hub vm: Wahl fehlt", "Kandidaten: alice, bob")
	if leaksToken(r.out + r.errOut) {
		t.Fatalf("Token in der Ausgabe:\n%s", r.out)
	}
	writeText(t, filepath.Join(e.tokens, "vm", "default"), "bob\n")
	runT(t, "node", "client", "--no-probe").want(t, 0, "— nicht geprüft",
		"Hub vm: bob (Standard), "+filepath.Join(e.tokens, "vm", "bob.token"))
}

// Kein Token in irgendeiner Ausgabe, keine Anfrage mit Token an den Node —
// auch nicht mit Token-Dateien, .pending und Standard an mehreren Hubs. Mit
// --json steht auf stdout nur das Objekt, stderr bleibt leer.
func TestNodeClientNoToken(t *testing.T) {
	e := newTokensEnv(t)
	e.put(t, "vm", "alice.token", dummyTokenA+"\n")
	e.put(t, "vm", "alice.token.pending", dummyTokenB+"\n")
	e.put(t, "vm", "bob.token", dummyTokenC+"\n")
	writeText(t, filepath.Join(e.tokens, "vm", "default"), "bob\n")
	e.put(t, "eigen", "kp.token", dummyTokenB+"\n")
	listen, tokens := probeNodeServer(t)
	cfg := filepath.Join(e.home, "c.yaml")
	nodeAt(t, cfg, listen)
	for _, args := range [][]string{
		{"--config", cfg},
		{"--config", cfg, "--json"},
		{"--config", cfg, "--json", "--account", "vm=alice"},
		{"--config", cfg, "--hub", "eigen", "--account", "eigen=kp"},
	} {
		r := runT(t, append([]string{"node", "client"}, args...)...)
		r.want(t, 0)
		if leaksToken(r.out + r.errOut) {
			t.Fatalf("%v: Token in der Ausgabe:\n%s%s", args, r.out, r.errOut)
		}
		if slices.Contains(args, "--json") {
			var v map[string]any
			if err := json.Unmarshal([]byte(r.out), &v); err != nil || r.errOut != "" {
				t.Fatalf("%v: stdout kein einzelnes Objekt (%v) oder stderr nicht leer:\n%s%s", args, err, r.out, r.errOut)
			}
		}
	}
	if tokens.Load() != 0 {
		t.Errorf("Anfragen mit Token an den Node: %d", tokens.Load())
	}
}
