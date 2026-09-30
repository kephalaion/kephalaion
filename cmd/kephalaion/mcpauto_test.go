package main

import (
	"crypto/x509"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kephalaion/kephalaion/internal/assistant"
	"github.com/kephalaion/kephalaion/internal/contract"
	"github.com/kephalaion/kephalaion/internal/contract/httpapi"
)

// allAssistants sind die drei Assistenten mit eigenem Eintrag.
var allAssistants = []string{assistant.Claude, assistant.OpenCode, assistant.Codex}

// Die Regel der automatischen Anstöße (add --auto): Sie ändern nur
// eingetragene Assistenten; ist keiner eingetragen, tragen sie überall ein;
// ohne Token-Datei nirgends neu.
func TestNodeMCPAddAuto(t *testing.T) {
	e := newMCPEnv(t, allAssistants...)
	silent := func(r result) {
		t.Helper()
		if r.code != 0 || r.out != "" || r.errOut != "" {
			t.Fatalf("add --auto: Exit %d, erwartet 0 ohne Ausgabe\n%s%s", r.code, r.out, r.errOut)
		}
	}

	// Über einer Einrichtung ohne Einträge und ohne Token-Datei: nichts.
	silent(e.mcp(t, "add", "--auto"))
	if w := e.fake.Writes(); len(w) != 0 {
		t.Fatalf("ohne Token-Datei geschrieben: %v", w)
	}

	// Erster Account: bei allen gefundenen.
	e.put(t, "vm", "alice.token", dummyTokenA+"\n")
	e.mcp(t, "add", "--auto").want(t, 0, "claude: eingetragen: "+mcpURL+", vm=alice", "opencode: eingetragen", "codex: eingetragen")
	e.mcp(t, "status").want(t, 0, "claude: eingetragen", "opencode: eingetragen", "codex: eingetragen")
	// Nichts zu ändern: keine Ausgabe.
	silent(e.mcp(t, "add", "--auto"))

	// Nach remove --assistant: Der Assistent bleibt ohne Eintrag, die übrigen
	// ziehen nach (hier eine andere Adresse).
	e.mcp(t, "remove", "--assistant", "codex").want(t, 0, "codex: entfernt")
	e.listen(t, "127.0.0.1:7500")
	r := e.mcp(t, "add", "--auto")
	r.want(t, 0, "claude: eingetragen, ersetzt den Eintrag (andere Adresse", "opencode: eingetragen, ersetzt den Eintrag")
	if strings.Contains(r.out+r.errOut, "codex") {
		t.Errorf("add --auto nennt codex:\n%s%s", r.out, r.errOut)
	}
	e.mcp(t, "status").want(t, 0, "claude: eingetragen", "opencode: eingetragen", "codex: fehlt")

	// Einträge nur bei einem Teil: nur diese.
	e.mcp(t, "remove", "--assistant", "opencode").want(t, 0)
	e.put(t, "eigen", "kp.token", dummyTokenC+"\n")
	r = e.mcp(t, "add", "--auto")
	r.want(t, 0, "claude: eingetragen, ersetzt den Eintrag (anderer Helfer): http://127.0.0.1:7500/mcp, eigen=kp, vm=alice")
	if strings.Contains(r.out, "opencode") || strings.Contains(r.out, "codex") {
		t.Errorf("add --auto trägt neu ein:\n%s", r.out)
	}
	e.mcp(t, "status").want(t, 0, "claude: eingetragen", "opencode: fehlt", "codex: fehlt")

	// add von Hand ohne --assistant trägt wieder überall ein.
	e.mcp(t, "add").want(t, 0, "claude: unverändert", "opencode: eingetragen", "codex: eingetragen")

	// Nach remove bei allen: wieder überall.
	e.mcp(t, "remove").want(t, 0, "claude: entfernt", "opencode: entfernt", "codex: entfernt")
	e.mcp(t, "add", "--auto").want(t, 0, "claude: eingetragen", "opencode: eingetragen", "codex: eingetragen")

	// Ein übergangener Hub endet auch mit --auto mit Exit 1.
	e.put(t, "eigen", "zweiter.token", dummyTokenB+"\n")
	e.mcp(t, "remove").want(t, 0)
	e.mcp(t, "add", "--auto").want(t, 1, "Hub eigen übergangen", "claude: eingetragen: http://127.0.0.1:7500/mcp, vm=alice")
}

// Der Anstoß nach node account rotate und check: nur für eine Token-Datei
// unter dem eigenen tokens/, nach dem Schreiben; ein Fehler dabei ist nur eine
// Warnung.
func TestAccountRotateRegistersAssistants(t *testing.T) {
	e := newCommEnv(t)
	fake := withAssistants(t, e.dir, allAssistants...)
	tokens, err := assistant.TokensDir()
	if err != nil {
		t.Fatal(err)
	}
	put := func(hub, account, token string) string {
		t.Helper()
		p := filepath.Join(tokens, hub, account+".token")
		writeText(t, p, token+"\n")
		return p
	}
	helper := func() string {
		t.Helper()
		env := &mcpEnv{tokensEnv: &tokensEnv{home: e.dir, tokens: tokens}, fake: fake}
		h, _ := env.claudeEntry(t)["headersHelper"].(string)
		return h
	}

	// Ohne Einträge, Datei außerhalb von tokens/: kein Anstoß.
	outside := e.tokenFile(t, "carol", e.tokens["carol"])
	r := e.run(t, "node", "account", "rotate", "eigen", "carol", "--token-file", outside)
	r.want(t, 0, "Token ersetzt")
	if len(fake.Calls) != 0 || strings.Contains(r.out, "claude") {
		t.Fatalf("Anstoß für eine Datei außerhalb von tokens/: %v\n%s", fake.Calls, r.out)
	}
	// --token-stdin: keine Datei, kein Anstoß.
	r = e.runIn(t, readFileToken(t, outside), "node", "account", "rotate", "eigen", "carol", "--token-stdin")
	r.want(t, 0, "Token ersetzt")
	if len(fake.Calls) != 0 {
		t.Fatalf("Anstoß bei --token-stdin: %v", fake.Calls)
	}

	// Erster Account unter tokens/: Eintrag bei allen gefundenen, nach dem
	// Schreiben der Datei.
	alice := put("eigen", "alice", e.tokens["alice"])
	r = e.run(t, "node", "account", "rotate", "eigen", "alice", "--token-file", alice)
	r.want(t, 0, "Token ersetzt", "claude: eingetragen: "+mcpURL+", eigen=alice", "opencode: eingetragen", "codex: eingetragen",
		"Wirksam in einer neuen Sitzung")
	if strings.Index(r.out, "Token ersetzt") > strings.Index(r.out, "claude: eingetragen") {
		t.Errorf("Anstoß vor dem Ersetzen gemeldet:\n%s", r.out)
	}
	if strings.Contains(r.out+r.errOut, readFileToken(t, alice)) {
		t.Fatal("Token in der Ausgabe")
	}
	if !strings.Contains(helper(), "--account eigen=alice") {
		t.Errorf("Helfer: %s", helper())
	}

	// Ein weiteres rotate desselben Accounts: Die Einträge stimmen, keine
	// Ausgabe dazu.
	r = e.run(t, "node", "account", "rotate", "eigen", "alice", "--token-file", alice)
	r.want(t, 0, "Token ersetzt")
	if strings.Contains(r.out, "claude:") {
		t.Errorf("unveränderte Einträge gemeldet:\n%s", r.out)
	}

	// Einträge nur bei einem Teil: Ein neuer Hub kommt nur zu ihnen.
	e.run(t, "node", "mcp", "remove", "--assistant", "codex", "--assistant", "opencode").want(t, 0)
	bob := put("fern", "bob", e.tokens["bob"])
	hookHTTP(t, func(address string, rootCAs *x509.CertPool) (contract.Hub, error) {
		return httpapi.NewClient(address, rootCAs)
	})
	r = e.run(t, "node", "account", "rotate", "fern", "bob", "--token-file", bob)
	r.want(t, 0, "Token ersetzt", "claude: eingetragen, ersetzt den Eintrag (anderer Helfer): "+mcpURL+", eigen=alice, fern=bob")
	if strings.Contains(r.out, "opencode") || strings.Contains(r.out, "codex") {
		t.Errorf("rotate trägt neu ein:\n%s", r.out)
	}

	// check nach einem unklaren rotate: Ersetzt es die Datei, folgt der
	// Anstoß. Nach remove bei allen trägt er wieder überall ein.
	e.run(t, "node", "mcp", "remove").want(t, 0)
	hookHTTP(t, func(address string, rootCAs *x509.CertPool) (contract.Hub, error) {
		c, err := httpapi.NewClient(address, rootCAs)
		return lostAnswer{Hub: c, apply: true}, err
	})
	e.run(t, "node", "account", "rotate", "fern", "bob", "--token-file", bob).want(t, 1, "Ausgang unklar")
	r = e.run(t, "node", "account", "check", "fern", "bob", "--token-file", bob)
	r.want(t, 0, "Das neue Token gilt", "claude: eingetragen", "opencode: eingetragen", "codex: eingetragen")

	// Scheitert der Anstoß, bleibt rotate gelungen: nur eine Warnung.
	e.run(t, "node", "mcp", "remove").want(t, 0)
	fake.Fail["claude mcp add-json"] = os.ErrPermission
	hookHTTP(t, func(address string, rootCAs *x509.CertPool) (contract.Hub, error) {
		return httpapi.NewClient(address, rootCAs)
	})
	r = e.run(t, "node", "account", "rotate", "eigen", "alice", "--token-file", alice)
	r.want(t, 0, "Token ersetzt", "claude: Fehler:", "Warnung: Die Anmeldung bei den KI-Assistenten ist nicht vollständig",
		"das Token selbst ist ersetzt", "codex: eingetragen")
}

// withinDir vergleicht aufgelöst: auch über einen Link, nie außerhalb.
func TestWithinDir(t *testing.T) {
	dir := t.TempDir()
	tokens := filepath.Join(dir, "tokens")
	if err := os.MkdirAll(filepath.Join(tokens, "vm"), 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(tokens, link); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]bool{
		filepath.Join(tokens, "vm", "a.token"):         true,
		filepath.Join(link, "vm", "a.token"):           true,
		filepath.Join(tokens, "..", "a.token"):         false,
		filepath.Join(dir, "tokens-andere", "a.token"): false,
		tokens: false,
		filepath.Join(tokens, "vm", "fehlt", "a.token"): true,
	} {
		if got := withinDir(path, tokens); got != want {
			t.Errorf("withinDir(%s) = %v, erwartet %v", path, got, want)
		}
	}
}
