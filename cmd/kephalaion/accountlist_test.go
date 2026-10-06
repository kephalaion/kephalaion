package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kephalaion/kephalaion/internal/testcert"
)

// list ruft node account list auf; die Ausgabe zählt für noSecrets.
func (e *setupEnv) list(t *testing.T, args ...string) result {
	t.Helper()
	var out, errOut bytes.Buffer
	code := run(append([]string{"node", "account", "list"}, args...), strings.NewReader(""), &out, &errOut)
	r := result{code, out.String(), errOut.String()}
	e.outputs = append(e.outputs, r.out, r.errOut)
	if os.Getenv("SHOW_SETUP") != "" {
		t.Logf("exit %d\n%s%s", r.code, r.out, r.errOut)
	}
	return r
}

// put legt tokens/<hub>/<name> mit content an.
func (e *setupEnv) put(t *testing.T, hub, name, content string) {
	t.Helper()
	dir := filepath.Join(e.tokensDir, hub)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// checks zählt die Anfragen an /account/check im Log von serve; failed die
// mit Fehlversuch über den Proxy.
func (e *setupEnv) checks() (all, failed int) {
	for _, l := range strings.Split(e.srv.log.String(), "\n") {
		if strings.Contains(l, " POST /account/check ") {
			all++
			if strings.Contains(l, " via=") && strings.Contains(l, " login=invalid ") {
				failed++
			}
		}
	}
	return all, failed
}

// rowState sucht in der Tabelle die Zeile hub, account, datei und liefert
// den Rest ab ZUSTAND.
func rowState(out, hub, account, kind string) string {
	for _, l := range strings.Split(out, "\n") {
		f := strings.Fields(l)
		if len(f) >= 4 && f[0] == hub && f[1] == account && f[2] == "."+kind {
			return strings.Join(f[3:], " ")
		}
	}
	return ""
}

// node account list: je Datei genau eine Anfrage an die Route zum Prüfen —
// gültig, ungültig, Hub dem Node unbekannt, .pending allein und neben der
// Token-Datei; eine unlesbare Datei wird nicht gefragt. Über einen
// entfernten Node (https, Test-CA) sind ungültig und unbekannt ein Zustand;
// je ungültiger Datei höchstens ein Fehlversuch. Node nicht erreichbar: alle
// nicht geprüft, keine Anfrage. Ohne Node in der config und ohne --node
// Exit 2. --json. Kein Token in der Ausgabe.
func TestNodeAccountList(t *testing.T) {
	e := newSetupEnv(t)
	tok := e.account(t, "ok1")
	e.setup(t, "", "eigen", "ok1", tok).want(t, 0)
	e.put(t, "eigen", "ok1.token.pending", e.newToken(t)+"\n")
	e.put(t, "eigen", "alt.token", e.newToken(t)+"\n")
	e.put(t, "eigen", "p.token.pending", e.newToken(t)+"\n")
	e.put(t, "eigen", "kaputt.token", "kein token\n")
	e.put(t, "nirgends", "x.token", e.newToken(t)+"\n")
	e.put(t, "eigen", ".ok1.token.tmp-123", "übergangen\n")

	before, _ := e.checks()
	r := e.list(t, e.c)
	r.want(t, 0, "Node: "+e.nodeURL, "HUB", "ZUSTAND")
	for _, c := range []struct{ hub, account, kind, want string }{
		{"eigen", "ok1", "token", "gültig ok1 team-x"},
		{"eigen", "ok1", "pending", "ungültig – –"},
		{"eigen", "alt", "token", "ungültig – –"},
		{"eigen", "p", "pending", "ungültig – –"},
		{"eigen", "kaputt", "token", "unlesbar – –"},
		{"nirgends", "x", "token", "unbekannt – –"},
	} {
		if got := rowState(r.out, c.hub, c.account, c.kind); got != c.want {
			t.Errorf("%s/%s .%s: %q, erwartet %q\n%s", c.hub, c.account, c.kind, got, c.want, r.out)
		}
	}
	if strings.Contains(r.out, ".ok1.token.tmp") {
		t.Errorf("Hilfsdatei gelistet:\n%s", r.out)
	}
	r.want(t, 0, "eigen/ok1: .pending neben der Token-Datei", "kephalaion node account check eigen ok1",
		"eigen/p: .pending offen", "kephalaion node account setup eigen p", "nirgends/x (.token): unbekannt",
		"der Node hat keinen Hub-Eintrag nirgends", "eigen/kaputt (.token): unlesbar",
		"Eine ungültige Token-Datei ist bei jedem Aufruf ein Fehlversuch")
	if after, _ := e.checks(); after-before != 5 {
		t.Errorf("Anfragen an /account/check: %d, erwartet 5 (eine je lesbarer Datei)", after-before)
	}

	// --json.
	r = e.list(t, "--json", e.c)
	r.want(t, 0)
	var out accountListOutput
	if err := json.Unmarshal([]byte(r.out), &out); err != nil || out.Node != e.nodeURL || len(out.Files) != 6 {
		t.Fatalf("--json: %v\n%s", err, r.out)
	}
	states := map[string]tokenState{}
	for _, f := range out.Files {
		states[f.Hub+"/"+f.Account+"."+f.Kind] = f.State
		if f.Collections == nil {
			t.Errorf("%s/%s: collections null", f.Hub, f.Account)
		}
	}
	want := map[string]tokenState{"eigen/ok1.token": stateValid, "eigen/ok1.pending": stateInvalid,
		"eigen/alt.token": stateInvalid, "eigen/p.pending": stateInvalid, "eigen/kaputt.token": stateUnreadable,
		"nirgends/x.token": stateUnknownHub}
	for k, v := range want {
		if states[k] != v {
			t.Errorf("--json %s: %q, erwartet %q", k, states[k], v)
		}
	}

	// Über einen entfernten Node: ungültig und unbekannt sind ein Zustand,
	// je ungültiger Datei ein Fehlversuch.
	ca := testcert.NewCA(t, "Proxy-CA")
	caPath := caFile(t, e.dir, "ca.pem", ca.PEM)
	proxy := newProxyNode(t, e.nodeAddr, serverCert(ca.ServerNow(t, "127.0.0.1")), "/kephalaion", "", proxyLogin)
	before, failedBefore := e.checks()
	r = e.list(t, "--node", proxy.URL+"/kephalaion", "--ca-file", caPath)
	r.want(t, 0, "Über einen Proxy unterscheidet der Node")
	for _, c := range []struct{ hub, account, kind, want string }{
		{"eigen", "ok1", "token", "gültig ok1 team-x"},
		{"eigen", "alt", "token", "ungültig oder unbekannt – –"},
		{"nirgends", "x", "token", "ungültig oder unbekannt – –"},
	} {
		if got := rowState(r.out, c.hub, c.account, c.kind); got != c.want {
			t.Errorf("Proxy %s/%s .%s: %q, erwartet %q\n%s", c.hub, c.account, c.kind, got, c.want, r.out)
		}
	}
	if after, failed := e.checks(); after-before != 5 || failed-failedBefore != 3 {
		t.Errorf("über den Proxy: %d Anfragen, %d Fehlversuche; erwartet 5 und 3", after-before, failed-failedBefore)
	}

	// Node nicht erreichbar: keine Anfrage, alle nicht geprüft.
	before, _ = e.checks()
	r = e.list(t, "--node", "http://127.0.0.1:1")
	r.want(t, 1, "nicht erreichbar")
	if got := rowState(r.out, "eigen", "ok1", "token"); got != "nicht geprüft – –" {
		t.Errorf("Node nicht erreichbar: %q\n%s", got, r.out)
	}
	if after, _ := e.checks(); after != before {
		t.Error("Anfragen trotz nicht erreichbarem Node")
	}

	// Ohne Node in der config und ohne --node.
	e.list(t).want(t, 2, "kein Node in der config", "--node <url>")
	e.list(t, "--ca-file", caPath).want(t, 2, "--ca-file nur zusammen mit --node")

	// Keine Token-Dateien.
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(e.dir, "leer"))
	e.list(t, "--node", "http://127.0.0.1:1").want(t, 0, "Keine Token-Dateien unter")
	r = e.list(t, "--node", "http://127.0.0.1:1", "--json")
	if r.code != 0 || !strings.Contains(r.out, `"files": []`) {
		t.Errorf("--json ohne Dateien: %d\n%s", r.code, r.out)
	}
}

// Hub nicht erreichbar: Der Node fragt ihn (whoami, mit Wiederholung) und
// antwortet „nicht geprüft“ — kein Fehler je Datei, kein Fehlversuch.
func TestNodeAccountListHubUnreachable(t *testing.T) {
	slow(t, "der Node wiederholt whoami am nicht erreichbaren Hub (0,5 s, 1 s, 2 s)")
	e := newSetupEnv(t)
	runIn(t, dummyTokenA, "node", "hub", "add", "tot", "--node", "laptop-tot", "--transport", "http", "--address",
		"http://127.0.0.1:1/hub", "--token-stdin", e.c).want(t, 0)
	e.put(t, "tot", "y.token", e.newToken(t)+"\n")
	r := e.list(t, e.c)
	r.want(t, 0, "tot/y (.token): nicht geprüft", "Hub tot nicht erreichbar")
	if got := rowState(r.out, "tot", "y", "token"); got != "nicht geprüft – –" {
		t.Errorf("Zeile: %q\n%s", got, r.out)
	}
	if strings.Contains(e.srv.log.String(), "login=invalid") {
		t.Error("nicht erreichbarer Hub als Fehlversuch")
	}
}
