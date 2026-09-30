package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tailscale/hujson"

	"github.com/kephalaion/kephalaion/internal/assistant"
	"github.com/kephalaion/kephalaion/internal/assistant/assistanttest"
	"github.com/kephalaion/kephalaion/internal/config"
)

// testBinary ist der Pfad, den der Helfer in den Tests nennt.
const testBinary = "/opt/keph/bin/kephalaion"

// mcpEnv ist ein temporäres HOME mit einer config, die einen Node nennt, den
// Token-Dateien und nachgespielten Assistenten.
type mcpEnv struct {
	*tokensEnv
	fake *assistanttest.Fake
	cfg  string
}

// withAssistants ersetzt den Manager durch einen mit nachgespielten
// Assistenten und das Binary durch einen festen Pfad.
func withAssistants(t *testing.T, home string, installed ...string) *assistanttest.Fake {
	t.Helper()
	fake := assistanttest.New(home, installed...)
	oldM, oldExe := newAssistantManager, assistantExecutable
	newAssistantManager = fake.Manager
	assistantExecutable = func() (string, error) { return testBinary, nil }
	t.Cleanup(func() { newAssistantManager, assistantExecutable = oldM, oldExe })
	return fake
}

func newMCPEnv(t *testing.T, installed ...string) *mcpEnv {
	t.Helper()
	e := &mcpEnv{tokensEnv: newTokensEnv(t)}
	e.cfg = filepath.Join(e.home, "config", "kephalaion", "config.yaml")
	e.listen(t, "")
	e.fake = withAssistants(t, e.home, installed...)
	return e
}

// listen schreibt die config des Users mit einem Node, der auf addr lauscht
// (leer: der Standard).
func (e *mcpEnv) listen(t *testing.T, addr string) {
	t.Helper()
	cfg := config.Config{Node: &config.Section{DB: "sqlite://" + filepath.Join(e.home, "node.db"), Listen: addr}}
	if err := config.Save(e.cfg, cfg); err != nil {
		t.Fatal(err)
	}
}

// helper ist die Kommandozeile des Helfers mit der Wahl accounts
// (hub=account).
func (e *mcpEnv) helper(accounts ...string) string {
	s := testBinary + " node mcp headers --tokens-dir " + e.tokens
	for _, a := range accounts {
		s += " --account " + a
	}
	return s
}

// claudeEntry liest den Eintrag kephalaion aus der Datei von Claude Code.
func (e *mcpEnv) claudeEntry(t *testing.T) map[string]any {
	t.Helper()
	var file struct {
		MCPServers map[string]map[string]any `json:"mcpServers"`
	}
	data, err := os.ReadFile(e.fake.ClaudeConfig())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatal(err)
	}
	return file.MCPServers[assistant.EntryName]
}

func (e *mcpEnv) codexText(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(e.fake.CodexConfig())
	if errors.Is(err, os.ErrNotExist) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func writeText(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

// mcp ruft node mcp <args> auf; ein Token steht nie in der Ausgabe.
func (e *mcpEnv) mcp(t *testing.T, args ...string) result {
	t.Helper()
	r := runT(t, append([]string{"node", "mcp"}, args...)...)
	if leaksToken(r.out + r.errOut) {
		t.Fatalf("Token in der Ausgabe:\n%s%s", r.out, r.errOut)
	}
	return r
}

const mcpURL = "http://127.0.0.1:7433/mcp"

// add, status und remove bei Claude Code und Codex: eintragen, unverändert
// lassen, entfernen — und nichts außer dem eigenen Eintrag ändern.
func TestNodeMCPAddStatusRemove(t *testing.T) {
	e := newMCPEnv(t, assistant.Claude, assistant.Codex)
	e.put(t, "vm", "alice.token", dummyTokenA+"\n")
	// Fremdes in beiden Dateien: bleibt.
	writeText(t, e.fake.ClaudeConfig(), `{"numStartups": 7, "mcpServers": {"andere": {"type": "http", "url": "http://x/mcp"}}}`+"\n")
	codexBefore := "# Meine config\nmodel = \"gpt-5\"   # Modell\n\n# vor andere\n[mcp_servers.andere]\ncommand = \"echo\"  # am Ende\n"
	writeText(t, e.fake.CodexConfig(), codexBefore)

	e.mcp(t, "status").want(t, 0, "Node: "+mcpURL, "claude: fehlt", "codex: fehlt", "vscode: über die Erweiterung")

	r := e.mcp(t, "add")
	r.want(t, 0, "claude: eingetragen: "+mcpURL+", vm=alice", "codex: eingetragen: "+mcpURL+", vm=alice",
		"Wirksam in einer neuen Sitzung")
	entry := e.claudeEntry(t)
	if entry["type"] != "http" || entry["url"] != mcpURL || entry["headersHelper"] != e.helper("vm=alice") || len(entry) != 3 {
		t.Fatalf("Eintrag bei Claude Code: %v", entry)
	}
	wantCodex := codexBefore + "\n[mcp_servers.kephalaion]\nurl = \"" + mcpURL + "\"\nhttp_headers_helper = \"" + e.helper("vm=alice") + "\"\n"
	if got := e.codexText(t); got != wantCodex {
		t.Fatalf("config.toml:\n%s\nerwartet:\n%s", got, wantCodex)
	}
	// Das Token steht in keiner der beiden Dateien.
	for _, p := range []string{e.fake.ClaudeConfig(), e.fake.CodexConfig()} {
		if leaksToken(readText(t, p)) {
			t.Errorf("Token in %s", p)
		}
	}
	if !strings.Contains(readText(t, e.fake.ClaudeConfig()), `"andere"`) || !strings.Contains(readText(t, e.fake.ClaudeConfig()), "numStartups") {
		t.Error("fremde Einträge bei Claude Code sind weg")
	}

	e.mcp(t, "status").want(t, 0, "claude: eingetragen (vm=alice)", "codex: eingetragen (vm=alice)")

	// Wiederholen: nichts wird geschrieben.
	writes := len(e.fake.Writes())
	e.mcp(t, "add").want(t, 0, "claude: unverändert: "+mcpURL+", vm=alice", "codex: unverändert")
	if got := e.fake.Writes(); len(got) != writes {
		t.Errorf("ein zweites add schreibt: %v", got[writes:])
	}
	if got := e.codexText(t); got != wantCodex {
		t.Errorf("ein zweites add ändert config.toml:\n%s", got)
	}

	// remove entfernt nur den Eintrag; die Datei von Codex ist wieder wie
	// vorher, Zeichen für Zeichen.
	e.mcp(t, "remove").want(t, 0, "claude: entfernt", "codex: entfernt")
	if e.claudeEntry(t) != nil {
		t.Error("Eintrag bei Claude Code steht noch")
	}
	if got := e.codexText(t); got != codexBefore {
		t.Errorf("config.toml nach remove:\n%q\nerwartet:\n%q", got, codexBefore)
	}
	if !strings.Contains(readText(t, e.fake.ClaudeConfig()), `"andere"`) {
		t.Error("remove hat fremde Einträge bei Claude Code entfernt")
	}
	e.mcp(t, "status").want(t, 0, "claude: fehlt", "codex: fehlt")
	e.mcp(t, "remove").want(t, 0, "claude: unverändert: kein Eintrag", "codex: unverändert: kein Eintrag")

	// --assistant beschränkt.
	e.mcp(t, "add", "--assistant", "codex").want(t, 0, "codex: eingetragen")
	e.mcp(t, "status").want(t, 0, "claude: fehlt", "codex: eingetragen (vm=alice)")
	e.mcp(t, "status", "--assistant", "codex").want(t, 0, "codex: eingetragen")
	if r := e.mcp(t, "status", "--assistant", "codex"); strings.Contains(r.out, "claude") || strings.Contains(r.out, "vscode") {
		t.Errorf("status --assistant codex nennt andere:\n%s", r.out)
	}
	e.mcp(t, "remove", "--assistant", "claude", "--assistant", "codex").want(t, 0, "claude: unverändert", "codex: entfernt")
}

// Die Zustände von status — eingetragen, fehlt, weicht ab — bei Claude Code
// und Codex, und was add aus einem abweichenden Eintrag macht.
func TestNodeMCPStatusStates(t *testing.T) {
	e := newMCPEnv(t, assistant.Claude, assistant.Codex)
	e.put(t, "vm", "alice.token", dummyTokenA+"\n")
	e.mcp(t, "add").want(t, 0)

	// Andere Adresse: listen ändert sich.
	e.listen(t, "127.0.0.1:7500")
	e.mcp(t, "status").want(t, 0, "Node: http://127.0.0.1:7500/mcp",
		"claude: weicht ab — andere Adresse ("+mcpURL+" statt http://127.0.0.1:7500/mcp)",
		"codex: weicht ab — andere Adresse")
	e.mcp(t, "add").want(t, 0, "claude: eingetragen, ersetzt den Eintrag (andere Adresse", "codex: eingetragen, ersetzt den Eintrag")
	e.mcp(t, "status").want(t, 0, "claude: eingetragen (vm=alice)", "codex: eingetragen (vm=alice)")
	if e.claudeEntry(t)["url"] != "http://127.0.0.1:7500/mcp" || !strings.Contains(e.codexText(t), `url = "http://127.0.0.1:7500/mcp"`) {
		t.Error("die neue Adresse steht nicht in den Einträgen")
	}
	// listen ohne Host meint diesen Rechner.
	e.listen(t, "")
	e.mcp(t, "add").want(t, 0, "ersetzt den Eintrag")

	// Anderer Helfer: das Binary liegt woanders.
	old := assistantExecutable
	assistantExecutable = func() (string, error) { return "/usr/local/bin/kephalaion", nil }
	e.mcp(t, "status").want(t, 0, "claude: weicht ab — anderer Helfer", "codex: weicht ab — anderer Helfer")
	assistantExecutable = old
	e.mcp(t, "status").want(t, 0, "claude: eingetragen", "codex: eingetragen")

	// Fremder Inhalt: feste Header bei Claude Code, ein anderer Transport bei
	// Codex. add ersetzt beide, das Token im Klartext verschwindet.
	writeText(t, e.fake.ClaudeConfig(), `{"mcpServers": {"kephalaion": {"type": "http", "url": "`+mcpURL+
		`", "headers": {"X-Keph-Token-vm": "im-klartext"}}}}`)
	writeText(t, e.fake.CodexConfig(), "[mcp_servers.kephalaion]\ncommand = \"kephalaion\"\nargs = [\"serve\"]\n")
	e.mcp(t, "status").want(t, 0, "claude: weicht ab — fremder Inhalt (feste Header im Eintrag)",
		"codex: weicht ab — fremder Inhalt (der Schlüssel")
	e.mcp(t, "add").want(t, 0, "claude: eingetragen, ersetzt den Eintrag (fremder Inhalt", "codex: eingetragen, ersetzt den Eintrag (fremder Inhalt")
	if strings.Contains(readText(t, e.fake.ClaudeConfig()), "im-klartext") || strings.Contains(e.codexText(t), "command") {
		t.Error("fremder Inhalt steht noch da")
	}
	e.mcp(t, "status").want(t, 0, "claude: eingetragen (vm=alice)", "codex: eingetragen (vm=alice)")

	// Ein Eintrag anderer Art bei Claude Code.
	writeText(t, e.fake.ClaudeConfig(), `{"mcpServers": {"kephalaion": {"command": "kephalaion", "args": ["x"]}}}`)
	e.mcp(t, "status", "--assistant", "claude").want(t, 0, "claude: weicht ab — fremder Inhalt (kein Eintrag vom Typ http)")

	// Eine Datei, die sich nicht lesen lässt: unbekannt, Exit 1; add fasst sie
	// nicht an.
	writeText(t, e.fake.ClaudeConfig(), "{kaputt")
	e.mcp(t, "status").want(t, 1, "claude: unbekannt — ", "kein gültiges JSON", "codex: eingetragen")
	e.mcp(t, "add").want(t, 1, "claude: Fehler:", "codex: unverändert")
	if readText(t, e.fake.ClaudeConfig()) != "{kaputt" {
		t.Error("add hat die unlesbare Datei geändert")
	}
}

// vscode: add und remove tragen nichts ein und nennen die Erweiterung;
// status sagt, ob sie installiert ist.
func TestNodeMCPVSCode(t *testing.T) {
	e := newMCPEnv(t)
	e.put(t, "vm", "alice.token", dummyTokenA+"\n")
	e.mcp(t, "add", "--assistant", "vscode").want(t, 0, "vscode: übergangen: über die Erweiterung für VS Code")
	e.mcp(t, "remove", "--assistant", "vscode").want(t, 0, "vscode: übergangen: über die Erweiterung für VS Code")
	e.mcp(t, "status").want(t, 0, "vscode: über die Erweiterung — Erweiterung kascada.kephalaion nicht gefunden",
		"claude: nicht gefunden (claude liegt nicht im PATH)", "codex: nicht gefunden")
	old := filepath.Join(e.home, ".vscode-server", "extensions", "kascada.kephalaion-0.0.5")
	if err := os.MkdirAll(old, 0o755); err != nil {
		t.Fatal(err)
	}
	e.mcp(t, "status", "--assistant", "vscode").want(t, 0,
		"vscode: über die Erweiterung — Erweiterung 0.0.5 installiert ("+old+") — sie meldet den MCP-Server erst ab 0.0.6")
	ext := filepath.Join(e.home, ".vscode-server", "extensions", "kascada.kephalaion-0.0.10")
	if err := os.MkdirAll(ext, 0o755); err != nil {
		t.Fatal(err)
	}
	e.mcp(t, "status", "--assistant", "vscode").want(t, 0, "vscode: über die Erweiterung — Erweiterung installiert ("+ext+")")
	if len(e.fake.Calls) != 0 {
		t.Errorf("Aufrufe für vscode: %v", e.fake.Calls)
	}
}

// Ein Assistent, der nicht im PATH liegt, wird übergangen und genannt — kein
// Fehler. Ein Fehler bei einem Assistenten hält die anderen nicht auf.
func TestNodeMCPNotFoundAndErrors(t *testing.T) {
	e := newMCPEnv(t, assistant.Claude)
	e.put(t, "vm", "alice.token", dummyTokenA+"\n")
	e.mcp(t, "add").want(t, 0, "claude: eingetragen", "codex: übergangen: nicht gefunden (codex liegt nicht im PATH)")
	e.mcp(t, "remove").want(t, 0, "claude: entfernt", "codex: übergangen: nicht gefunden")

	e.fake.Installed[assistant.Codex] = true
	e.fake.Fail["claude mcp add-json"] = errors.New("claude mcp add-json kephalaion: exit status 1: kaputt")
	e.mcp(t, "add").want(t, 1, "node mcp add: claude: Fehler: ", "kaputt", "codex: eingetragen")
	delete(e.fake.Fail, "claude mcp add-json")

	// Falscher Aufruf.
	e.mcp(t, "add", "--assistant", "cursor").want(t, 2, "--assistant \"cursor\"", "claude, opencode, codex, vscode")
	e.mcp(t, "add", "--account", "vm").want(t, 2, "<hub>=<account>")
	e.mcp(t, "add", "zuviel").want(t, 2, "Unerwartetes Argument")
	e.mcp(t, "remove", "--assistant", "x").want(t, 2)
	e.mcp(t, "status", "--assistant", "x").want(t, 2)
	e.mcp(t, "remove", "--dry-run").want(t, 2)
}

// --dry-run meldet, was geschähe, und schreibt nichts.
func TestNodeMCPDryRun(t *testing.T) {
	e := newMCPEnv(t, assistant.Claude, assistant.Codex)
	e.put(t, "vm", "alice.token", dummyTokenA+"\n")
	r := e.mcp(t, "add", "--dry-run")
	r.want(t, 0, "dry-run, nichts wird geschrieben:", "claude: eingetragen: "+mcpURL+", vm=alice", "codex: eingetragen")
	if strings.Contains(r.out, "Wirksam") {
		t.Errorf("dry-run spricht von Wirkung:\n%s", r.out)
	}
	if len(e.fake.Writes()) != 0 || e.claudeEntry(t) != nil || e.codexText(t) != "" {
		t.Errorf("dry-run hat geschrieben: %v", e.fake.Writes())
	}
	e.mcp(t, "add").want(t, 0)
	writes := len(e.fake.Writes())
	e.listen(t, "127.0.0.1:7600")
	e.mcp(t, "add", "--dry-run").want(t, 0, "claude: eingetragen, ersetzt den Eintrag (andere Adresse")
	if len(e.fake.Writes()) != writes || e.claudeEntry(t)["url"] != mcpURL {
		t.Error("dry-run hat einen Eintrag ersetzt")
	}
}

// Ohne Node in der config bricht add mit Hinweis ab; ohne Token-Datei trägt
// es nirgends neu ein und bereinigt vorhandene Einträge.
func TestNodeMCPNoNodeNoToken(t *testing.T) {
	e := newMCPEnv(t, assistant.Claude, assistant.Codex)
	if err := os.Remove(e.cfg); err != nil {
		t.Fatal(err)
	}
	e.mcp(t, "add").want(t, 1, "kein Node in der config "+e.cfg, "kephalaion node init")
	e.mcp(t, "status").want(t, 1, "kein Node in der config", "kephalaion node init")
	r := e.mcp(t, "add", "--auto")
	if r.code != 0 || r.out != "" || r.errOut != "" {
		t.Fatalf("add --auto ohne Node: Exit %d\n%s%s", r.code, r.out, r.errOut)
	}
	// Nur ein Hub in der config: auch kein Node.
	if err := config.Save(e.cfg, config.Config{Hub: &config.Section{DB: "sqlite:///x/hub.db"}}); err != nil {
		t.Fatal(err)
	}
	e.mcp(t, "add").want(t, 1, "kein Node in der config")
	if len(e.fake.Calls) != 0 {
		t.Errorf("Aufrufe ohne Node: %v", e.fake.Calls)
	}

	// Mit Node, ohne Token-Datei: nichts eingetragen, Hinweis auf den ersten
	// Account, Exit 0.
	e.listen(t, "")
	e.mcp(t, "add").want(t, 0, "claude: übergangen: keine Token-Datei", "codex: übergangen: keine Token-Datei",
		"Keine Token-Datei unter "+e.tokens, "kephalaion node account rotate")
	if len(e.fake.Writes()) != 0 {
		t.Errorf("ohne Token-Datei geschrieben: %v", e.fake.Writes())
	}

	// Ein vorhandener Eintrag wird auch dann bereinigt: Die Wahl eines
	// Accounts ohne Token-Datei fällt heraus.
	file := e.put(t, "vm", "alice.token", dummyTokenA+"\n")
	e.mcp(t, "add").want(t, 0, "claude: eingetragen", "codex: eingetragen")
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	e.mcp(t, "add").want(t, 0, "claude: eingetragen, ersetzt den Eintrag (anderer Helfer): "+mcpURL+", ohne Anmeldung",
		"codex: eingetragen, ersetzt den Eintrag (anderer Helfer)")
	if e.claudeEntry(t)["headersHelper"] != e.helper() {
		t.Errorf("Helfer ohne Token-Datei: %v", e.claudeEntry(t)["headersHelper"])
	}
	e.mcp(t, "status").want(t, 0, "claude: eingetragen (ohne Anmeldung)")
}

// Mehrere Accounts für einen Hub: ohne Wahl wird der Hub übergangen und
// genannt (Exit 1), die übrigen werden eingetragen; --account wählt, und die
// Wahl bleibt im Eintrag.
func TestNodeMCPAccounts(t *testing.T) {
	e := newMCPEnv(t, assistant.Claude, assistant.Codex)
	e.put(t, "vm", "alice.token", dummyTokenA+"\n")
	e.put(t, "vm", "bob.token", dummyTokenB+"\n")

	// Nur der Hub mit zwei Accounts: nichts einzutragen.
	r := e.mcp(t, "add")
	r.want(t, 1, "Hub vm übergangen: mehrere Accounts mit Token-Datei (alice, bob), keiner gewählt",
		"kephalaion node mcp add --account vm=<account> [--assistant <name>]", "auch nach einem remove für einzelne",
		"claude: übergangen: keine Token-Datei")
	if len(e.fake.Writes()) != 0 {
		t.Errorf("geschrieben: %v", e.fake.Writes())
	}

	// Ein zweiter Hub mit einem Account: Er wird eingetragen, vm übergangen.
	e.put(t, "eigen", "kp.token", dummyTokenC+"\n")
	e.mcp(t, "add").want(t, 1, "claude: eingetragen: "+mcpURL+", eigen=kp", "codex: eingetragen", "Hub vm übergangen")
	if e.claudeEntry(t)["headersHelper"] != e.helper("eigen=kp") {
		t.Errorf("Helfer: %v", e.claudeEntry(t)["headersHelper"])
	}
	e.mcp(t, "status").want(t, 0, "claude: eingetragen (eigen=kp)", "Hub vm ohne Anmeldung: mehrere Accounts")

	// --account wählt; die Wahl steht für jeden Hub ausdrücklich im Eintrag.
	e.mcp(t, "add", "--account", "vm=bob").want(t, 0, "claude: eingetragen, ersetzt den Eintrag (anderer Helfer): "+mcpURL+", eigen=kp, vm=bob")
	if e.claudeEntry(t)["headersHelper"] != e.helper("eigen=kp", "vm=bob") {
		t.Errorf("Helfer: %v", e.claudeEntry(t)["headersHelper"])
	}
	if !strings.Contains(e.codexText(t), "--account eigen=kp --account vm=bob") {
		t.Errorf("config.toml:\n%s", e.codexText(t))
	}
	// Ohne --account bleibt die Wahl aus dem Eintrag.
	e.mcp(t, "add").want(t, 0, "claude: unverändert: "+mcpURL+", eigen=kp, vm=bob", "codex: unverändert")
	e.mcp(t, "status").want(t, 0, "claude: eingetragen (eigen=kp, vm=bob)", "codex: eingetragen (eigen=kp, vm=bob)")

	// Ein Assistent ohne Eintrag nimmt die Wahl der anderen.
	e.mcp(t, "remove", "--assistant", "codex").want(t, 0)
	e.mcp(t, "add", "--assistant", "codex").want(t, 0, "codex: eingetragen: "+mcpURL+", eigen=kp, vm=bob")

	// Eine neue Wahl gilt für die genannten Assistenten.
	e.mcp(t, "add", "--account", "vm=alice", "--assistant", "claude").want(t, 0, "claude: eingetragen, ersetzt", "vm=alice")
	e.mcp(t, "status").want(t, 0, "claude: eingetragen (eigen=kp, vm=alice)", "codex: eingetragen (eigen=kp, vm=bob)")

	// Ein gewählter Account ohne Token-Datei: mit --account ein übergangener
	// Hub, aus dem Eintrag keine Wahl.
	e.mcp(t, "add", "--account", "vm=carol").want(t, 1, "Hub vm übergangen: der gewählte Account carol hat keine Token-Datei")
	e.mcp(t, "add", "--account", "vm=alice").want(t, 0)
	if err := os.Remove(filepath.Join(e.tokens, "vm", "alice.token")); err != nil {
		t.Fatal(err)
	}
	e.mcp(t, "add").want(t, 0, "claude: eingetragen, ersetzt den Eintrag (anderer Helfer): "+mcpURL+", eigen=kp, vm=bob")
}

// Zu einem Hub kommt ein zweiter Account hinzu: headers — mit dem Aufruf aus
// dem Eintrag — und ein erneutes add bleiben beim bisher gewählten.
func TestNodeMCPSecondAccount(t *testing.T) {
	e := newMCPEnv(t, assistant.Claude, assistant.Codex)
	e.put(t, "vm", "alice.token", dummyTokenA+"\n")
	e.mcp(t, "add").want(t, 0, "claude: eingetragen: "+mcpURL+", vm=alice")

	helperHeaders := func() map[string]string {
		t.Helper()
		helper, _ := e.claudeEntry(t)["headersHelper"].(string)
		args, ok := assistant.ShellSplit(helper)
		if !ok || len(args) < 2 || args[0] != testBinary {
			t.Fatalf("Helfer: %q", helper)
		}
		got, _ := e.headers(t, args[4:]...)
		return got
	}
	want := map[string]string{"X-Keph-Account-vm": "alice", "X-Keph-Token-vm": dummyTokenA}
	wantHeaders(t, helperHeaders(), want)

	e.put(t, "vm", "bob.token", dummyTokenB+"\n")
	wantHeaders(t, helperHeaders(), want)
	writes := len(e.fake.Writes())
	e.mcp(t, "add").want(t, 0, "claude: unverändert: "+mcpURL+", vm=alice", "codex: unverändert: "+mcpURL+", vm=alice")
	if len(e.fake.Writes()) != writes {
		t.Errorf("add nach dem zweiten Account schreibt: %v", e.fake.Writes()[writes:])
	}
	wantHeaders(t, helperHeaders(), want)
	e.mcp(t, "status").want(t, 0, "claude: eingetragen (vm=alice)", "codex: eingetragen (vm=alice)")

	// Ein neuer Hub mit einer Token-Datei erscheint im Helfer ohne neues add.
	e.put(t, "eigen", "kp.token", dummyTokenC+"\n")
	wantHeaders(t, helperHeaders(), map[string]string{
		"X-Keph-Account-vm": "alice", "X-Keph-Token-vm": dummyTokenA,
		"X-Keph-Account-eigen": "kp", "X-Keph-Token-eigen": dummyTokenC,
	})
}

// Codex: CODEX_HOME gilt; ein Eintrag in anderer Form geht über codex mcp
// remove; liest Codex die Datei nach dem Schreiben nicht, ist sie wieder wie
// vorher.
func TestNodeMCPCodexFile(t *testing.T) {
	e := newMCPEnv(t, assistant.Codex)
	e.put(t, "vm", "alice.token", dummyTokenA+"\n")
	e.fake.Env["CODEX_HOME"] = filepath.Join(e.home, "codex-home")
	e.mcp(t, "add").want(t, 0, "codex: eingetragen")
	if got := readText(t, filepath.Join(e.home, "codex-home", "config.toml")); !strings.HasPrefix(got, "[mcp_servers.kephalaion]\n") {
		t.Fatalf("config.toml unter CODEX_HOME:\n%s", got)
	}
	if exists(filepath.Join(e.home, ".codex", "config.toml")) {
		t.Error("trotz CODEX_HOME nach ~/.codex geschrieben")
	}

	// Gepunktete Schlüssel statt einer Tabelle: fremder Inhalt; add lässt
	// Codex ihn entfernen und hängt die Tabelle an.
	dotted := "model = \"gpt-5\"\nmcp_servers.kephalaion.url = \"http://alt/mcp\"\n"
	writeText(t, e.fake.CodexConfig(), dotted)
	e.mcp(t, "status").want(t, 0, "codex: weicht ab — fremder Inhalt (der Eintrag steht nicht als eigene Tabelle da)")
	e.mcp(t, "add").want(t, 0, "codex: eingetragen, ersetzt den Eintrag")
	if got := e.codexText(t); strings.Contains(got, "http://alt/mcp") || !strings.Contains(got, "[mcp_servers.kephalaion]\n") {
		t.Errorf("config.toml:\n%s", got)
	}
	writeText(t, e.fake.CodexConfig(), dotted)
	e.mcp(t, "remove").want(t, 0, "codex: entfernt")
	if got := e.codexText(t); got != "model = \"gpt-5\"\n" {
		t.Errorf("config.toml nach remove:\n%q", got)
	}

	// Eine Inline-Tabelle mcp_servers lässt sich nicht erweitern: Codex läse
	// die Datei nicht mehr. add stellt sie wieder her und meldet den Fehler.
	inline := "mcp_servers = { andere = { command = \"echo\" } }\n"
	writeText(t, e.fake.CodexConfig(), inline)
	e.mcp(t, "add").want(t, 1, "codex: Fehler:", "ist wiederhergestellt")
	if got := e.codexText(t); got != inline {
		t.Errorf("config.toml nach dem Fehler:\n%q", got)
	}
	// Gab es die Datei nicht, ist sie danach wieder weg.
	if err := os.Remove(e.fake.CodexConfig()); err != nil {
		t.Fatal(err)
	}
	e.fake.Fail["codex mcp list"] = errors.New("codex mcp list --json: exit status 1: kaputt")
	e.mcp(t, "add").want(t, 1, "codex: Fehler:", "kaputt")
	if exists(e.fake.CodexConfig()) {
		t.Error("nach dem Fehler bleibt eine neue config.toml liegen")
	}
}

// Claude Code: CLAUDE_CONFIG_DIR gilt.
func TestNodeMCPClaudeConfigDir(t *testing.T) {
	e := newMCPEnv(t, assistant.Claude)
	e.put(t, "vm", "alice.token", dummyTokenA+"\n")
	e.fake.Env["CLAUDE_CONFIG_DIR"] = filepath.Join(e.home, "claude-dir")
	e.mcp(t, "add").want(t, 0, "claude: eingetragen")
	if !exists(filepath.Join(e.home, "claude-dir", ".claude.json")) || exists(filepath.Join(e.home, ".claude.json")) {
		t.Error("CLAUDE_CONFIG_DIR nicht beachtet")
	}
	e.mcp(t, "status").want(t, 0, "claude: eingetragen (vm=alice)")
	want := []string{
		"claude mcp add-json kephalaion " + `{"type":"http","url":"` + mcpURL + `","headersHelper":"` + e.helper("vm=alice") + `"}` + " --scope user",
	}
	if got := e.fake.Calls; len(got) != 1 || got[0] != want[0] {
		t.Errorf("Aufrufe:\n%v\nerwartet:\n%v", got, want)
	}
	// Ersetzen: erst remove, dann add-json — add-json scheitert bei
	// vorhandenem Namen.
	e.listen(t, "127.0.0.1:7700")
	e.fake.Calls = nil
	e.mcp(t, "add").want(t, 0, "ersetzt den Eintrag")
	if got := e.fake.Calls; len(got) != 2 || got[0] != "claude mcp remove kephalaion --scope user" ||
		!strings.HasPrefix(got[1], "claude mcp add-json kephalaion ") {
		t.Errorf("Aufrufe: %v", got)
	}
}

// opencodeEntry liest den Eintrag kephalaion aus der config von OpenCode.
func (e *mcpEnv) opencodeEntry(t *testing.T) map[string]any {
	t.Helper()
	data, err := os.ReadFile(e.fake.OpenCodeConfig())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	std, err := hujson.Standardize(data)
	if err != nil {
		t.Fatalf("config von OpenCode ungültig: %v\n%s", err, data)
	}
	var file struct {
		MCP map[string]map[string]any `json:"mcp"`
	}
	if err := json.Unmarshal(std, &file); err != nil {
		t.Fatal(err)
	}
	return file.MCP[assistant.EntryName]
}

// ref ist der Verweis auf die Token-Datei eines Accounts, wie add ihn
// schreibt: unter HOME als ~/….
func (e *mcpEnv) ref(hub, account string) string {
	rel, _ := filepath.Rel(e.home, filepath.Join(e.tokens, hub, account+".token"))
	return "{file:~/" + filepath.ToSlash(rel) + "}"
}

const opencodeBefore = `// Meine config
{
  "model": "x", // Modell
  "mcp": {
    // fremd, mit Verweis
    "fremd": {
      "type": "remote",
      "url": "http://example.invalid/mcp",
      "headers": { "X-A": "{env:A}" },
    },
  },
}
`

// OpenCode: add über opencode mcp add mit Verweisen auf die Token-Dateien,
// remove nur des Schlüssels, status mit den drei Zuständen.
func TestNodeMCPOpenCode(t *testing.T) {
	e := newMCPEnv(t, assistant.OpenCode)
	e.put(t, "vm", "alice.token", dummyTokenA+"\n")
	cfg := filepath.Join(e.home, ".config", "opencode", "opencode.jsonc")
	writeText(t, cfg, opencodeBefore)

	e.mcp(t, "status").want(t, 0, "opencode: fehlt", "claude: nicht gefunden")
	e.mcp(t, "add").want(t, 0, "opencode: eingetragen: "+mcpURL+", vm=alice")
	entry := e.opencodeEntry(t)
	headers, _ := entry["headers"].(map[string]any)
	if entry["type"] != "remote" || entry["url"] != mcpURL || len(entry) != 3 || len(headers) != 2 ||
		headers["X-Keph-Account-vm"] != "alice" || headers["X-Keph-Token-vm"] != e.ref("vm", "alice") {
		t.Fatalf("Eintrag bei OpenCode: %v", entry)
	}
	text := readText(t, cfg)
	if leaksToken(text) {
		t.Fatal("Token in der config von OpenCode")
	}
	for _, keep := range []string{"// Meine config", `"model": "x", // Modell`, "// fremd, mit Verweis", `"{env:A}"`} {
		if !strings.Contains(text, keep) {
			t.Errorf("add verliert %q:\n%s", keep, text)
		}
	}
	want := []string{"opencode --version", "opencode mcp add kephalaion --url " + mcpURL +
		" --header X-Keph-Account-vm=alice --header X-Keph-Token-vm=" + e.ref("vm", "alice")}
	if got := e.fake.Calls; strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("Aufrufe:\n%s\nerwartet:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	e.mcp(t, "status").want(t, 0, "opencode: eingetragen (vm=alice)")
	writes := len(e.fake.Writes())
	e.mcp(t, "add").want(t, 0, "opencode: unverändert")
	if len(e.fake.Writes()) != writes {
		t.Errorf("ein zweites add schreibt: %v", e.fake.Writes()[writes:])
	}

	// Weicht ab: andere Adresse, weitere Schlüssel (oauth), fremder Typ.
	e.listen(t, "127.0.0.1:7500")
	e.mcp(t, "status").want(t, 0, "opencode: weicht ab — andere Adresse")
	e.listen(t, "")
	// Von Hand oauth: false dazu — die Form des Eintrags ist die von
	// opencode mcp add, deshalb über den Wert der Adresse.
	text = readText(t, cfg)
	quotedURL := `"` + mcpURL + `"`
	if strings.Count(text, quotedURL) != 1 {
		t.Fatalf("Adresse nicht genau einmal:\n%s", text)
	}
	writeText(t, cfg, strings.Replace(text, quotedURL, quotedURL+`, "oauth": false`, 1))
	e.mcp(t, "status").want(t, 0, "opencode: weicht ab — fremder Inhalt (oauth)")
	e.mcp(t, "add").want(t, 0, "opencode: eingetragen, ersetzt den Eintrag (fremder Inhalt (oauth))")
	if _, ok := e.opencodeEntry(t)["oauth"]; ok {
		t.Error("oauth steht noch im Eintrag")
	}

	// remove: nur der Schlüssel, der Rest wie vorher.
	e.mcp(t, "remove").want(t, 0, "opencode: entfernt")
	if got := readText(t, cfg); got != opencodeBefore {
		t.Errorf("config nach remove:\n%s\nerwartet:\n%s", got, opencodeBefore)
	}
	e.mcp(t, "remove").want(t, 0, "opencode: unverändert: kein Eintrag")
	e.mcp(t, "status").want(t, 0, "opencode: fehlt")
}

// Die Falle: Ein Verweis auf eine fehlende Token-Datei macht die ganze config
// von OpenCode ungültig. status warnt; add nimmt den Hub heraus (opencode mcp
// add allein scheiterte daran) und entfernt den Eintrag, wenn keine
// Token-Datei mehr da ist — auch als automatischer Anstoß.
func TestNodeMCPOpenCodeMissingTokenFile(t *testing.T) {
	e := newMCPEnv(t, assistant.Claude, assistant.OpenCode)
	cfg := filepath.Join(e.home, ".config", "opencode", "opencode.jsonc")
	writeText(t, cfg, opencodeBefore)
	e.put(t, "vm", "alice.token", dummyTokenA+"\n")
	eigen := e.put(t, "eigen", "kp.token", dummyTokenC+"\n")
	e.mcp(t, "add").want(t, 0, "opencode: eingetragen: "+mcpURL+", eigen=kp, vm=alice")

	if err := os.Remove(eigen); err != nil {
		t.Fatal(err)
	}
	r := e.mcp(t, "status")
	r.want(t, 0, "opencode: weicht ab — Verweis auf eine fehlende Token-Datei",
		"Warnung: der Eintrag verweist auf eine fehlende Datei (~/", "OpenCode startet nicht", "kephalaion node mcp add bereinigt")
	e.mcp(t, "add", "--auto").want(t, 0, "opencode: eingetragen, ersetzt den Eintrag (Verweis auf eine fehlende Token-Datei)")
	headers, _ := e.opencodeEntry(t)["headers"].(map[string]any)
	if len(headers) != 2 || headers["X-Keph-Token-vm"] != e.ref("vm", "alice") {
		t.Fatalf("Header nach dem Bereinigen: %v", headers)
	}
	e.mcp(t, "status").want(t, 0, "opencode: eingetragen (vm=alice)")
	if r := e.mcp(t, "status"); strings.Contains(r.out, "Warnung") {
		t.Errorf("Warnung nach dem Bereinigen:\n%s", r.out)
	}

	// Keine Token-Datei mehr: Der Eintrag bei OpenCode fällt weg, Claude Code
	// behält seinen (der Helfer ohne Wahl) — auch mit --auto.
	if err := os.Remove(filepath.Join(e.tokens, "vm", "alice.token")); err != nil {
		t.Fatal(err)
	}
	e.mcp(t, "add", "--auto").want(t, 0, "opencode: entfernt (keine Token-Datei mehr)", "claude: eingetragen, ersetzt den Eintrag")
	if e.opencodeEntry(t) != nil {
		t.Error("Eintrag bei OpenCode ohne Token-Datei")
	}
	if got := readText(t, cfg); got != opencodeBefore {
		t.Errorf("config nach dem Entfernen:\n%s", got)
	}
	// Ohne Token-Datei kommt kein neuer Eintrag.
	e.mcp(t, "add").want(t, 0, "opencode: übergangen: keine Token-Datei")
	e.mcp(t, "status").want(t, 0, "opencode: fehlt", "claude: eingetragen (ohne Anmeldung)")
}

// OpenCode: Fassung vor 1.17.0 ist ein Fehler mit Hinweis; XDG_CONFIG_HOME
// gilt; opencode.json vor opencode.jsonc, beide nebeneinander mit Warnung.
func TestNodeMCPOpenCodeFiles(t *testing.T) {
	e := newMCPEnv(t, assistant.OpenCode)
	e.put(t, "vm", "alice.token", dummyTokenA+"\n")
	e.fake.OpenCodeVersion = "opencode 1.16.2"
	e.mcp(t, "add").want(t, 1, "opencode: Fehler: OpenCode 1.16.2 fragt bei mcp add nach; nötig ist 1.17.0", "opencode upgrade")
	if w := e.fake.Writes(); len(w) != 0 {
		t.Errorf("geschrieben trotz alter Fassung: %v", w)
	}
	e.fake.OpenCodeVersion = "1.17.0"

	// Ohne config legt opencode mcp add opencode.json an, unter
	// XDG_CONFIG_HOME.
	xdg := filepath.Join(e.home, "xdg")
	e.fake.Env["XDG_CONFIG_HOME"] = xdg
	e.mcp(t, "add").want(t, 0, "opencode: eingetragen")
	if !exists(filepath.Join(xdg, "opencode", "opencode.json")) {
		t.Fatal("keine opencode.json unter XDG_CONFIG_HOME")
	}
	e.mcp(t, "status").want(t, 0, "opencode: eingetragen (vm=alice)")
	writeText(t, filepath.Join(xdg, "opencode", "opencode.jsonc"), "{}\n")
	e.mcp(t, "status").want(t, 0, "opencode: eingetragen", "Warnung: neben ", "opencode.json und opencode.jsonc")

	// Eine config, die kein JSONC ist: status unbekannt, add fasst sie nicht an.
	writeText(t, filepath.Join(xdg, "opencode", "opencode.json"), "{kaputt")
	e.mcp(t, "status").want(t, 1, "opencode: unbekannt — ", "kein gültiges JSONC")
	e.mcp(t, "add").want(t, 1, "opencode: Fehler:")
	if got := readText(t, filepath.Join(xdg, "opencode", "opencode.json")); got != "{kaputt" {
		t.Errorf("add hat die kaputte Datei geändert: %s", got)
	}
}
