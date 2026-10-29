package main

import (
	"bytes"
	"context"
	"io/fs"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kephalaion/kephalaion/internal/assistant"
	"github.com/kephalaion/kephalaion/internal/config"
	"github.com/kephalaion/kephalaion/internal/contract/httpapi"
	"github.com/kephalaion/kephalaion/internal/hub/replication"
	nodestore "github.com/kephalaion/kephalaion/internal/node/store"
	"github.com/kephalaion/kephalaion/internal/testcert"
)

// setupNodeEnv ist ein Hub mit eigener config (wie auf einem anderen
// Rechner, über HTTP auf Loopback erreichbar) und ein User ohne Node, dessen
// config pro User node setup anlegt. Collections team-x, privat und
// k-playbook (aus der Liste der Quellen, fest abonniert); der Hub nennt den
// Alias vm. systemd und die Assistenten sind nachgespielt.
type setupNodeEnv struct {
	dir, hubCfg, hc string
	url             string
	srv             *httptest.Server
	sysd            *fakeSystemd
	mode            *string
	rotates         *atomic.Int32
	outputs         []string
	secrets         []string
}

func newSetupNodeEnv(t *testing.T) *setupNodeEnv {
	t.Helper()
	dir := isolate(t)
	e := &setupNodeEnv{dir: dir, hubCfg: filepath.Join(dir, "hub", "config.yaml")}
	e.hc = "--config=" + e.hubCfg
	runT(t, "hub", "init", "--db", "sqlite://"+filepath.Join(dir, "hub", "hub.db"), e.hc).want(t, 0)
	for _, c := range []string{"team-x", "privat", "k-playbook"} {
		runT(t, "hub", "collection", "add", c, e.hc).want(t, 0)
	}
	runT(t, "config", "set", "hub", "alias", "vm", e.hc).want(t, 0)
	e.srv = httptest.NewServer(httpapi.NewHandler(replication.New(hubStore(t, e.hubCfg)).WithSource(newSourceClient())))
	t.Cleanup(e.srv.Close)
	e.url = e.srv.URL + "/"
	e.sysd = withSystemd(t, dir)
	withAssistants(t, dir, assistant.Claude)
	e.mode, e.rotates = hookRotate(t)
	t.Cleanup(func() { e.noSecrets(t) })
	return e
}

// hub ruft ein Kommando mit der config des Hubs auf.
func (e *setupNodeEnv) hub(t *testing.T, args ...string) result {
	t.Helper()
	return runT(t, append(args, e.hc)...)
}

// newCode legt den Node laptop an (mit team-x) oder erzeugt ihm ein neues
// Token, und liefert den Einrichtungscode mit der Adresse des Hubs.
func (e *setupNodeEnv) newCode(t *testing.T, args ...string) string {
	t.Helper()
	cmd := []string{"hub", "node", "token", "laptop"}
	if _, err := hubStore(t, e.hubCfg).Node(context.Background(), "laptop"); err != nil {
		cmd = []string{"hub", "node", "add", "laptop", "--grant", "team-x"}
	}
	r := e.hub(t, append(append(cmd, "--address", e.url), args...)...)
	r.want(t, 0)
	code, c := codeFrom(t, r.out)
	e.secrets = append(e.secrets, code, c.Token)
	return code
}

// setup ruft node setup ohne --config auf: die config des Users.
func (e *setupNodeEnv) setup(t *testing.T, stdin string, args ...string) result {
	t.Helper()
	var out, errOut bytes.Buffer
	code := run(append([]string{"node", "setup"}, args...), strings.NewReader(stdin), &out, &errOut)
	r := result{code, out.String(), errOut.String()}
	e.outputs = append(e.outputs, r.out, r.errOut)
	if os.Getenv("SHOW_SETUP") != "" {
		t.Logf("exit %d\n%s%s", r.code, r.out, r.errOut)
	}
	return r
}

func (e *setupNodeEnv) userCfg(t *testing.T) string {
	t.Helper()
	p, err := config.UserPath()
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// entry liest den Hub-Eintrag vm; ok false, wenn es ihn (oder den Node)
// nicht gibt.
func (e *setupNodeEnv) entry(t *testing.T) (nodestore.Hub, bool) {
	t.Helper()
	cfg, _, err := config.Load(e.userCfg(t))
	if err != nil || cfg.Section(config.Node) == nil {
		return nodestore.Hub{}, false
	}
	h, err := nodeStore(t, e.userCfg(t)).Hub(context.Background(), "vm")
	if err != nil {
		return nodestore.Hub{}, false
	}
	e.secrets = append(e.secrets, h.Token)
	return h, true
}

func (e *setupNodeEnv) mustEntry(t *testing.T) nodestore.Hub {
	t.Helper()
	h, ok := e.entry(t)
	if !ok {
		t.Fatal("kein Hub-Eintrag vm")
	}
	return h
}

// pending liest die .pending des Eintrags; leer, wenn keine liegt.
func (e *setupNodeEnv) pending(t *testing.T) string {
	t.Helper()
	h := e.mustEntry(t)
	tok, err := nodestore.ReadPendingToken(nodeStore(t, e.userCfg(t)).PendingTokenPath(h.EntryID), h.EntryID)
	if err != nil {
		return ""
	}
	e.secrets = append(e.secrets, tok)
	return tok
}

// valid sagt, ob tok am Hub das Token von laptop ist.
func (e *setupNodeEnv) valid(t *testing.T, tok string) bool {
	t.Helper()
	return nodeHashOK(t, e.hubCfg, "laptop", tok)
}

// noSecrets prüft, dass keine Ausgabe ein ganzes Token oder einen ganzen
// Code nennt — auch keines aus node.db oder einer .pending.
func (e *setupNodeEnv) noSecrets(t *testing.T) {
	t.Helper()
	secrets := append([]string{}, e.secrets...)
	_ = filepath.WalkDir(e.dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && (strings.HasSuffix(p, ".pending") || strings.HasSuffix(p, ".token")) {
			if data, err := os.ReadFile(p); err == nil {
				for _, f := range strings.FieldsFunc(string(data), func(r rune) bool { return strings.ContainsRune("\"{}:,\n ", r) }) {
					if strings.HasPrefix(f, "keph_") {
						secrets = append(secrets, f)
					}
				}
			}
		}
		return nil
	})
	for _, out := range e.outputs {
		for _, s := range secrets {
			if s != "" && strings.Contains(out, s) {
				t.Errorf("ganzes Token oder ganzer Code in einer Ausgabe:\n%s", strings.ReplaceAll(out, s, "<GEHEIM>"))
			}
		}
	}
}

// Ein frischer Rechner: ein Aufruf richtet Node, Hub-Eintrag, Rotation,
// Collections, Dienst und Assistenten ein; die Übersicht zeigt alle Teile.
// Ein zweiter Aufruf mit demselben Code rotiert nicht und ändert nichts.
func TestNodeSetupFresh(t *testing.T) {
	e := newSetupNodeEnv(t)
	code := e.newCode(t)
	r := e.setup(t, "", code)
	r.want(t, 0, "Einrichtungscode gelesen: Hub vm (http ", "Node laptop", "Node eingerichtet (node init)",
		"Hub vm eingetragen und geprüft", "Token rotiert", "Der Code ist jetzt wertlos",
		"Collections abonniert: team-x", "fest abonniert: k-playbook", "Dienst eingerichtet",
		"Noch keine Token-Datei", "node account setup vm <account>",
		// Die Übersicht.
		"Übersicht", "config: "+e.userCfg(t), "node:", "listen:", "Hub-Eintrag vm (node.db)", "Node-Name:    laptop",
		"Transport:    http", "Adresse:      "+e.url, "Token:        keph_…", "Collections:    abonniert team-x",
		"Dienst:         pro User", "Log: journalctl --user -u kephalaion", "Assistenten (Node http://",
		"claude:", "Accounts:       keine unter")
	h := e.mustEntry(t)
	if !e.valid(t, h.Token) || e.valid(t, codeToken(t, code)) {
		t.Error("nicht rotiert: der Code gilt noch oder das gespeicherte Token nicht")
	}
	if strings.Join(h.Collections, ",") != "team-x" {
		t.Errorf("abonniert %v; k-playbook ist fest abonniert, privat nicht erlaubt", h.Collections)
	}
	if !e.sysd.active {
		t.Error("Dienst nicht gestartet")
	}

	// Zweiter Aufruf mit demselben Code: schon rotiert, ohne Fehlversuch.
	e.rotates.Store(0)
	r = e.setup(t, "", code)
	r.want(t, 0, "Node: schon eingerichtet", "Hub vm: schon eingetragen", "Gespeichertes Token", "Schon rotiert",
		"Dienst: schon eingerichtet")
	if strings.Contains(r.out, "Token aus dem Code") || e.rotates.Load() != 0 {
		t.Error("der zweite Aufruf hat das Code-Token geprüft oder rotiert")
	}
	if e.mustEntry(t).Token != h.Token {
		t.Error("der zweite Aufruf hat das Token geändert")
	}
	// Über stdin, mit Token-Datei: die Assistenten sind eingetragen.
	tokDir := filepath.Join(filepath.Dir(e.userCfg(t)), "tokens", "vm")
	if err := os.MkdirAll(tokDir, 0o700); err != nil {
		t.Fatal(err)
	}
	acct := newToken(t)
	e.secrets = append(e.secrets, acct)
	if err := os.WriteFile(filepath.Join(tokDir, "alice.token"), []byte(acct+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r = e.setup(t, code+"\n", "--code-stdin")
	r.want(t, 0, "claude: eingetragen", "Accounts:       alice (.token) unter", "node account list")
	if strings.Contains(r.out, "Noch keine Token-Datei") {
		t.Error("Hinweis auf node account setup trotz Token-Datei")
	}
}

// codeToken liest das Token aus einem Code.
func codeToken(t *testing.T, code string) string {
	t.Helper()
	_, c := codeFrom(t, code)
	return c.Token
}

// Falscher Aufruf und Vorprüfung: nichts geändert, der Code gilt weiter.
func TestNodeSetupUsage(t *testing.T) {
	e := newSetupNodeEnv(t)
	code := e.newCode(t)
	e.setup(t, "").want(t, 2, "als Argument oder --code-stdin")
	e.setup(t, code, code, "--code-stdin").want(t, 2, "genau eines")
	e.setup(t, "", "kephsetup_kaputt").want(t, 2, "kein Einrichtungscode")
	e.setup(t, "", newToken(t)).want(t, 2, "das ist ein Token")
	e.setup(t, "", code, "--alias", "anders").want(t, 2, "der Code nennt den Alias vm")
	e.setup(t, "", code, "--ca-file", "/gibt/es/nicht").want(t, 2, "--ca-file gibt es nur bei https")
	e.setup(t, "", code, "--token", "x").want(t, 2, "nie als Argument")
	if _, ok := e.entry(t); ok {
		t.Fatal("ein Hub-Eintrag nach falschem Aufruf")
	}
	if !e.valid(t, codeToken(t, code)) {
		t.Fatal("der Code gilt nicht mehr")
	}

	// Ohne Alias im Code: --alias ist Pflicht, dann geht es.
	e.hub(t, "config", "unset", "hub", "alias").want(t, 0)
	code = e.newCode(t)
	e.setup(t, "", code).want(t, 2, "der Code nennt keinen Alias", "--alias")
	e.setup(t, "", code, "--alias", "a:b").want(t, 2, "--alias")
	e.setup(t, "", code, "--alias", "vm", "--no-service").want(t, 0, "Hub vm eingetragen", "Dienst: übergangen")
}

// Neben der globalen config bricht node setup ab, bevor es etwas ändert.
func TestNodeSetupGlobalConfig(t *testing.T) {
	e := newSetupNodeEnv(t)
	code := e.newCode(t)
	if err := os.MkdirAll(filepath.Dir(config.SystemPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.SystemPath, []byte("node:\n  db: sqlite:///var/lib/kephalaion/node.db\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.setup(t, "", code).want(t, 1, "global eingerichtet", "nur pro User", "der Code gilt weiter")
	if _, err := os.Stat(e.userCfg(t)); err == nil {
		t.Error("config des Users angelegt")
	}
	if !e.valid(t, codeToken(t, code)) {
		t.Error("der Code gilt nicht mehr")
	}
}

// Der Alias steht schon mit anderen Angaben da: Abbruch, nichts überschrieben,
// der Code gilt weiter.
func TestNodeSetupAliasDiffers(t *testing.T) {
	e := newSetupNodeEnv(t)
	code := e.newCode(t)
	runT(t, "node", "init").want(t, 0)
	runIn(t, newToken(t), "node", "hub", "add", "vm", "--node", "laptop", "--transport", "http", "--address",
		"http://localhost:1/hub", "--token-stdin").want(t, 0)
	before := e.mustEntry(t)
	e.setup(t, "", code).want(t, 1, "Hub vm ist schon anders eingetragen", "Adresse http://localhost:1/hub statt",
		"überschreibt nichts", "Der Code gilt weiter")
	if after := e.mustEntry(t); after.Token != before.Token || after.Address != before.Address {
		t.Error("der Eintrag ist verändert")
	}
	if !e.valid(t, codeToken(t, code)) {
		t.Error("der Code gilt nicht mehr")
	}
}

// Der Hub kennt node_rotate nicht: Abbruch, .pending gelöscht, der Eintrag
// bleibt; nach dem Update setzt derselbe Aufruf fort. Danach ein neuer Code
// aus hub node token: übernommen und rotiert.
func TestNodeSetupOldHubAndNewCode(t *testing.T) {
	e := newSetupNodeEnv(t)
	code := e.newCode(t)
	*e.mode = "old"
	e.setup(t, "", code).want(t, 1, "kennt node_rotate noch nicht", "erst den Hub aktualisieren",
		"Der Hub-Eintrag vm bleibt")
	h := e.mustEntry(t)
	if h.Token != codeToken(t, code) || e.pending(t) != "" {
		t.Fatal("nach einem Hub ohne node_rotate: Token geändert oder .pending liegt")
	}
	*e.mode = ""
	e.setup(t, "", code).want(t, 0, "Hub vm: schon eingetragen", "Token rotiert")
	if !e.valid(t, e.mustEntry(t).Token) || e.valid(t, codeToken(t, code)) {
		t.Fatal("nach dem Update nicht rotiert")
	}

	// Ein neuer Code für den vorhandenen Eintrag.
	code2 := e.newCode(t)
	e.setup(t, "", code2).want(t, 0, "Gespeichertes Token", "gilt nicht", "Token aus dem Code", "Neuer Code",
		"Token rotiert")
	if !e.valid(t, e.mustEntry(t).Token) || e.valid(t, codeToken(t, code2)) {
		t.Fatal("neuer Code nicht eingelöst")
	}

	// Ein alter Code, dessen Token der Admin inzwischen ersetzt hat: Weder
	// das gespeicherte noch das aus dem Code gilt.
	e.newCode(t)
	r := e.setup(t, "", code2)
	r.want(t, 1, "Weder das gespeicherte Token noch das aus dem Code gilt", "hub node token laptop")
}

// Rotieren abgelehnt: Eintrag mit dem Token aus einem alten Code (der Hub
// war beim ersten Mal ohne node_rotate), inzwischen ersetzt.
func TestNodeSetupRotateRejected(t *testing.T) {
	e := newSetupNodeEnv(t)
	code := e.newCode(t)
	*e.mode = "old"
	e.setup(t, "", code).want(t, 1, "erst den Hub aktualisieren")
	*e.mode = ""
	e.newCode(t)
	e.setup(t, "", code).want(t, 1, "der Hub lehnt ab", "ist gelöscht", "ein alter Code", "hub node token laptop")
	if e.pending(t) != "" {
		t.Error(".pending nach Ablehnung")
	}
}

// Eine liegende .pending: gültig übernommen ohne Rotieren; ungültig, aber
// das gespeicherte gilt — verworfen, dann rotiert; ungültig ohne gültiges
// anderes — bleibt liegen.
func TestNodeSetupPending(t *testing.T) {
	e := newSetupNodeEnv(t)
	code := e.newCode(t)
	*e.mode = "lost"
	e.setup(t, "", code).want(t, 1, "Ausgang unklar", "Derselbe Aufruf klärt es")
	pending := e.pending(t)
	if pending == "" || !e.valid(t, pending) {
		t.Fatal("keine gültige .pending nach verlorener Antwort")
	}
	*e.mode = ""
	e.rotates.Store(0)
	e.setup(t, "", code).want(t, 0, "liegt von einem früheren node_rotate", "Vorgemerktes Token", "übernommen",
		"Collections abonniert: team-x")
	if e.rotates.Load() != 0 || e.mustEntry(t).Token != pending || e.pending(t) != "" {
		t.Fatal("die gültige .pending ist nicht ohne Rotieren übernommen")
	}

	// Ungültig, das gespeicherte gilt: verworfen. Der Code ist schon
	// eingelöst — also schon rotiert, weiter ohne Rotieren.
	*e.mode = "unclear"
	h := e.mustEntry(t)
	runT(t, "node", "hub", "rotate", "vm").want(t, 1, "Ausgang unklar")
	*e.mode = ""
	e.rotates.Store(0)
	e.setup(t, "", code).want(t, 0, "gilt nicht", "verworfen", "schon rotiert")
	if e.rotates.Load() != 0 || e.mustEntry(t).Token != h.Token || e.pending(t) != "" {
		t.Fatal("die ungültige .pending ist nicht verworfen")
	}

	// Ungültig, das Token aus einem neuen Code gilt: verworfen, übernommen,
	// rotiert.
	*e.mode = "unclear"
	runT(t, "node", "hub", "rotate", "vm").want(t, 1, "Ausgang unklar")
	*e.mode = ""
	code2 := e.newCode(t)
	e.setup(t, "", code2).want(t, 0, "verworfen", "Neuer Code", "Token rotiert")
	if e.valid(t, codeToken(t, code2)) || !e.valid(t, e.mustEntry(t).Token) {
		t.Fatal("neuer Code nach verworfener .pending nicht eingelöst")
	}

	// Ungültig ohne gültiges anderes: bleibt liegen.
	*e.mode = "unclear"
	runT(t, "node", "hub", "rotate", "vm").want(t, 1, "Ausgang unklar")
	*e.mode = ""
	pending = e.pending(t)
	e.newCode(t)
	e.setup(t, "", code2).want(t, 1, "weder das Token", "bleibt liegen", "hub node token laptop")
	if e.pending(t) != pending {
		t.Fatal("die ungültige .pending ist weg, obwohl kein Token gilt")
	}
}

// Der Hub in 4.3 nicht erreicht: Abbruch ohne Änderung und ohne den Hinweis
// auf hub node token. Ebenso nach dem Rotieren beim Abonnieren: derselbe
// Aufruf setzt fort.
func TestNodeSetupUnreachable(t *testing.T) {
	e := newSetupNodeEnv(t)
	code := e.newCode(t)
	e.setup(t, "", code, "--no-service").want(t, 0)
	h := e.mustEntry(t)
	code2 := e.newCode(t)
	*e.mode = "whoami-down"
	r := e.setup(t, "", code2, "--no-service")
	r.want(t, 1, "nicht erreicht oder Ausgang unklar", "Nichts geändert")
	if strings.Contains(r.errOut, "hub node token") {
		t.Error("Hinweis auf hub node token, obwohl der Hub nicht erreicht ist")
	}
	if e.mustEntry(t).Token != h.Token {
		t.Error("Token trotz nicht erreichtem Hub geändert")
	}

	// Der Hub nicht erreichbar beim ersten Mal: nichts gespeichert.
	e2 := newSetupNodeEnv(t)
	code3 := e2.newCode(t)
	e2.srv.Close()
	e2.setup(t, "", code3).want(t, 1, "Nichts gespeichert; der Code gilt weiter")
	if _, ok := e2.entry(t); ok {
		t.Error("Hub-Eintrag trotz gescheitertem Check")
	}
}

// https mit eigener CA: ohne --ca-file scheitert der erste Lauf, ohne etwas
// zu speichern; mit --ca-file gelingt er. Ein neuer Code ohne --ca-file für
// den Eintrag mit CA nimmt die gespeicherte.
func TestNodeSetupHTTPS(t *testing.T) {
	e := newSetupNodeEnv(t)
	ca := testcert.NewCA(t, "Test-CA")
	ce := &commEnv{dir: e.dir, cfg: e.hubCfg, c: e.hc}
	srv := proxyHub(t, ce, serverCert(ca.ServerNow(t, "127.0.0.1")), "")
	e.url = srv.URL + "/hub"
	code := e.newCode(t)
	r := e.setup(t, "", code, "--no-service")
	r.want(t, 1, "nicht vertraut", "--ca-file", "Nichts gespeichert")
	if _, ok := e.entry(t); ok {
		t.Fatal("Hub-Eintrag trotz Zertifikatsfehler")
	}
	caPath := caFile(t, e.dir, "ca.pem", ca.PEM)
	e.setup(t, "", code, "--ca-file", caPath, "--no-service").want(t, 0, "Hub vm eingetragen", "Transport:    https",
		"CA:           CN=Test-CA")
	if !e.valid(t, e.mustEntry(t).Token) {
		t.Fatal("über https nicht rotiert")
	}
	code2 := e.newCode(t)
	e.setup(t, "", code2, "--no-service").want(t, 0, "Neuer Code", "Token rotiert")
	if e.mustEntry(t).CA == "" || !e.valid(t, e.mustEntry(t).Token) {
		t.Fatal("neuer Code ohne --ca-file: CA verloren oder nicht rotiert")
	}
	// Eine andere CA ist eine andere Angabe.
	other := testcert.NewCA(t, "Andere CA")
	e.setup(t, "", code2, "--ca-file", caFile(t, e.dir, "andere.pem", other.PEM), "--no-service").
		want(t, 1, "schon anders eingetragen", "eine andere CA")
}

// Abbruch nach dem Rotieren (beim Abonnieren): derselbe Aufruf setzt fort,
// ohne erneut zu rotieren. Ein Dienst, der nicht startet, ist eine Warnung.
func TestNodeSetupAfterRotate(t *testing.T) {
	e := newSetupNodeEnv(t)
	code := e.newCode(t)
	*e.mode = "down-after-rotate"
	e.setup(t, "", code).want(t, 1, "Token rotiert", "Collections: whoami", "derselbe Aufruf setzt fort")
	h := e.mustEntry(t)
	if !e.valid(t, h.Token) || len(h.Collections) != 0 {
		t.Fatal("nach dem Abbruch beim Abonnieren: nicht rotiert oder schon abonniert")
	}
	*e.mode = ""
	e.rotates.Store(0)
	e.sysd.failStart = true
	r := e.setup(t, "", code)
	r.want(t, 0, "Schon rotiert", "Collections abonniert: team-x", "Warnung: service install")
	if e.rotates.Load() != 0 || e.mustEntry(t).Token != h.Token {
		t.Error("der zweite Aufruf hat rotiert")
	}
}
