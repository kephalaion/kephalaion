package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kephalaion/kephalaion/internal/assistant"
	"github.com/kephalaion/kephalaion/internal/config"
)

// account ruft node account <args> auf; ein Token steht nie in der Ausgabe.
func (e *mcpEnv) account(t *testing.T, args ...string) result {
	t.Helper()
	r := runT(t, append([]string{"node", "account"}, args...)...)
	if leaksToken(r.out + r.errOut) {
		t.Fatalf("Token in der Ausgabe:\n%s%s", r.out, r.errOut)
	}
	return r
}

// defaultOf liest tokens/<hub>/default; leer, wenn es die Datei nicht gibt.
func (e *mcpEnv) defaultOf(t *testing.T, hub string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(e.tokens, hub, "default"))
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// opencodeAccount ist der Account eines Hubs im Eintrag von OpenCode.
func (e *mcpEnv) opencodeAccount(t *testing.T, hub string) string {
	t.Helper()
	headers, _ := e.opencodeEntry(t)["headers"].(map[string]any)
	a, _ := headers[assistant.AccountHeaderPrefix+hub].(string)
	return a
}

// node account default (Task 037): zeigen, setzen, entfernen; der Account
// braucht eine Token-Datei; falscher Aufruf Exit 2. Die Datei enthält nur
// den Namen, 0600.
func TestNodeAccountDefault(t *testing.T) {
	e := newMCPEnv(t)
	e.put(t, "vm", "alice.token", dummyTokenA+"\n")

	e.account(t, "default", "vm").want(t, 0, "Standard für Hub vm: keiner", "Token-Dateien: alice",
		"Es gilt: alice (einzige Token-Datei)")
	e.account(t, "default", "vm", "bob").want(t, 1, "bob hat keine Token-Datei", "kephalaion node account setup vm bob")
	if e.defaultOf(t, "vm") != "" {
		t.Fatal("Standard ohne Token-Datei gesetzt")
	}
	e.put(t, "vm", "bob.token", dummyTokenB+"\n")
	e.account(t, "default", "vm").want(t, 0, "Es gilt: keiner — Wahl fehlt (alice, bob)",
		"kephalaion node account default vm <account>")
	e.account(t, "default", "vm", "bob").want(t, 0, "Standard für Hub vm: bob (bisher keiner).")
	if got := e.defaultOf(t, "vm"); got != "bob\n" {
		t.Fatalf("default: %q", got)
	}
	wantMode(t, filepath.Join(e.tokens, "vm", "default"))
	e.account(t, "default", "vm").want(t, 0, "Standard für Hub vm: bob", "Es gilt: bob (Standard)")
	e.account(t, "default", "vm", "alice").want(t, 0, "Standard für Hub vm: alice (bisher bob).")
	e.account(t, "default", "vm", "alice").want(t, 0, "Standard für Hub vm: alice (unverändert).")

	// Ein Standard ohne Token-Datei: Hinweis, die nächste Regel greift.
	writeText(t, filepath.Join(e.tokens, "vm", "default"), "carol\n")
	e.account(t, "default", "vm").want(t, 0, "Standard für Hub vm: carol", "Wahl fehlt",
		"Hinweis: der Standard carol hat keine Token-Datei")

	e.account(t, "default", "vm", "--unset").want(t, 0, "Standard für Hub vm entfernt.")
	if exists(filepath.Join(e.tokens, "vm", "default")) {
		t.Fatal("default liegt nach --unset noch")
	}
	e.account(t, "default", "vm", "--unset").want(t, 0, "Hub vm hatte keinen Standard.")

	e.account(t, "default").want(t, 2, "Es fehlt: <hub>")
	e.account(t, "default", "VM").want(t, 2, "Hub \"VM\"")
	e.account(t, "default", "vm", "admin").want(t, 2, "reserviert")
	e.account(t, "default", "vm", "alice", "--unset").want(t, 2, "--unset")
}

// default zieht OpenCode nach: Ein Eintrag mit dem bisherigen Standard (ohne
// Standard: der bisher einzigen Token-Datei) folgt dem neuen; einer mit
// anderem Account bleibt und wird genannt — ebenso eine ausdrückliche Wahl
// im Helfer von Claude Code.
func TestNodeAccountDefaultFollow(t *testing.T) {
	e := newMCPEnv(t, assistant.OpenCode)
	e.put(t, "vm", "alice.token", dummyTokenA+"\n")
	e.mcp(t, "add").want(t, 0, "opencode: eingetragen: "+mcpURL+", vm=alice")

	// Bisher die einzige Token-Datei: Standard alice, der Eintrag bleibt.
	writes := len(e.fake.Writes())
	e.account(t, "default", "vm", "alice").want(t, 0, "Standard für Hub vm: alice (bisher keiner; es galt die einzige "+
		"Token-Datei alice).")
	if len(e.fake.Writes()) != writes {
		t.Errorf("default ohne Änderung schreibt: %v", e.fake.Writes()[writes:])
	}

	// Ein neuer Standard: Der Eintrag mit dem bisherigen folgt.
	e.put(t, "vm", "bob.token", dummyTokenB+"\n")
	r := e.account(t, "default", "vm", "bob")
	r.want(t, 0, "Standard für Hub vm: bob (bisher alice).", "opencode: eingetragen, ersetzt den Eintrag")
	if got := e.opencodeAccount(t, "vm"); got != "bob" {
		t.Fatalf("OpenCode trägt %q, erwartet bob", got)
	}
	if strings.Contains(r.out, "ausdrückliche Wahl") {
		t.Errorf("ein gefolgter Eintrag wird genannt:\n%s", r.out)
	}

	// Ein anderer Account im Eintrag ist eine ausdrückliche Wahl: Sie bleibt
	// und wird genannt.
	e.put(t, "vm", "carol.token", dummyTokenC+"\n")
	e.mcp(t, "add", "--account", "vm=carol").want(t, 0, "opencode: eingetragen, ersetzt den Eintrag")
	e.account(t, "default", "vm", "alice").want(t, 0, "Standard für Hub vm: alice (bisher bob).",
		"opencode: ausdrückliche Wahl vm=carol im Eintrag bleibt, nicht der Standard alice",
		"kephalaion node mcp add --assistant opencode --account vm=alice")
	if got := e.opencodeAccount(t, "vm"); got != "carol" {
		t.Fatalf("OpenCode trägt %q, erwartet carol", got)
	}
	// add ohne --account lässt ihn stehen, solange die Token-Datei liegt.
	e.mcp(t, "add").want(t, 0, "opencode: unverändert: "+mcpURL+", vm=carol")
}

// Eine ausdrückliche Wahl im Helfer von Claude Code, die vom neuen Standard
// abweicht, bleibt und wird genannt, mit dem Weg zurück zum Standard.
func TestNodeAccountDefaultExplicitHelper(t *testing.T) {
	e := newMCPEnv(t, assistant.Claude)
	e.put(t, "vm", "alice.token", dummyTokenA+"\n")
	e.put(t, "vm", "bob.token", dummyTokenB+"\n")
	e.mcp(t, "add", "--account", "vm=alice").want(t, 0, "claude: eingetragen: "+mcpURL+", vm=alice")
	e.account(t, "default", "vm", "bob").want(t, 0, "Standard für Hub vm: bob (bisher keiner).",
		"claude: ausdrückliche Wahl vm=alice im Eintrag bleibt, nicht der Standard bob",
		"kephalaion node mcp remove --assistant claude, danach kephalaion node mcp add --assistant claude")
	if e.claudeEntry(t)["headersHelper"] != e.helper("vm=alice") {
		t.Errorf("Helfer: %v", e.claudeEntry(t)["headersHelper"])
	}
}

// Ohne eigenen Node trägt default nichts ein: Claude Code und Codex folgen
// dem Standard über den Helfer, für OpenCode nennt es den Weg mit --node.
func TestNodeAccountDefaultNoNode(t *testing.T) {
	e := newMCPEnv(t, assistant.OpenCode)
	e.put(t, "vm", "alice.token", dummyTokenA+"\n")
	e.mcp(t, "add").want(t, 0, "opencode: eingetragen")
	if err := config.Save(e.cfg, config.Config{}); err != nil {
		t.Fatal(err)
	}
	e.put(t, "vm", "bob.token", dummyTokenB+"\n")
	writes := len(e.fake.Writes())
	e.account(t, "default", "vm", "bob").want(t, 0, "Standard für Hub vm: bob", "Kein Node in der config",
		"Claude Code und Codex folgen dem Standard ohne node mcp add",
		"opencode: trägt vm=alice fest im Eintrag, nicht den Standard bob",
		"kephalaion node mcp add --node http://127.0.0.1:7433 --assistant opencode --account vm=bob")
	if len(e.fake.Writes()) != writes {
		t.Errorf("ohne Node eingetragen: %v", e.fake.Writes()[writes:])
	}
}

// setup und der Standard: Der erste Account eines Hubs wird Standard; kommt
// an einem Hub ohne Standard ein zweiter dazu, wird vorher der bisher
// einzige Standard, und die Ausgabe sagt es; ein weiterer lässt ihn, wie er
// ist, und nennt den Weg, ihn zu ändern.
func TestNodeAccountSetupDefault(t *testing.T) {
	e := newSetupEnv(t)
	tok := e.account(t, "eins")
	e.setup(t, "", "eigen", "eins", tok).want(t, 0, "Standard für Hub eigen: eins (der erste Account des Hubs).")
	def := filepath.Join(e.tokensDir, "eigen", "default")
	if got := readText(t, def); got != "eins\n" {
		t.Fatalf("default: %q", got)
	}
	wantMode(t, def)

	// Wie vor Task 037: ein Account ohne Standard.
	if err := os.Remove(def); err != nil {
		t.Fatal(err)
	}
	tok2 := e.account(t, "zwei")
	e.setup(t, "", "eigen", "zwei", tok2).want(t, 0,
		"Standard für Hub eigen: eins — bisher die einzige Token-Datei; damit die Wahl bleibt, wenn zwei dazukommt.",
		"Standard für Hub eigen bleibt eins; ändern: kephalaion node account default eigen zwei")
	if got := readText(t, def); got != "eins\n" {
		t.Fatalf("default nach dem zweiten: %q", got)
	}
	tok3 := e.account(t, "drei")
	r := e.setup(t, "", "eigen", "drei", tok3)
	r.want(t, 0, "Standard für Hub eigen bleibt eins; ändern: kephalaion node account default eigen drei")
	if strings.Contains(r.out, "bisher die einzige") {
		t.Errorf("Standard neu gesetzt:\n%s", r.out)
	}

	// list markiert den Standard; mehrere ohne Standard: Wahl fehlt.
	r = e.list(t, e.c)
	if got := rowState(r.out, "eigen", "eins", "token"); !strings.HasSuffix(got, " ja") {
		t.Errorf("eins ohne Markierung: %q\n%s", got, r.out)
	}
	if got := rowState(r.out, "eigen", "zwei", "token"); strings.HasSuffix(got, " ja") {
		t.Errorf("zwei markiert: %q\n%s", got, r.out)
	}
	if err := os.Remove(def); err != nil {
		t.Fatal(err)
	}
	e.list(t, e.c).want(t, 0, "eigen: kein Standard bei mehreren Token-Dateien (drei, eins, zwei) — Wahl fehlt",
		"kephalaion node account default eigen <account>")
	writeText(t, def, "vier\n")
	e.list(t, e.c).want(t, 0, "eigen: der Standard vier hat keine Token-Datei")
}

// Ein Helfer ohne ausdrückliche Wahl folgt dem neuen Standard ohne add:
// default ändert den Eintrag nicht und nennt ihn nicht; die Header-Paare
// des Helfers tragen den neuen Standard.
func TestNodeAccountDefaultHelperFollows(t *testing.T) {
	e := newMCPEnv(t, assistant.Claude, assistant.Codex)
	e.put(t, "vm", "alice.token", dummyTokenA+"\n")
	e.mcp(t, "add").want(t, 0, "claude: eingetragen: "+mcpURL+", vm=alice")
	helper := e.claudeEntry(t)["headersHelper"]
	e.put(t, "vm", "bob.token", dummyTokenB+"\n")
	writes := len(e.fake.Writes())
	r := e.account(t, "default", "vm", "bob")
	r.want(t, 0, "Standard für Hub vm: bob")
	if strings.Contains(r.out, "ausdrückliche Wahl") || len(e.fake.Writes()) != writes {
		t.Errorf("default ändert oder nennt den Helfer:\n%s\n%v", r.out, e.fake.Writes()[writes:])
	}
	if e.claudeEntry(t)["headersHelper"] != helper {
		t.Errorf("Helfer geändert: %v", e.claudeEntry(t)["headersHelper"])
	}
	got, _ := e.headers(t, "--tokens-dir", e.tokens)
	wantHeaders(t, got, map[string]string{"X-Keph-Account-vm": "bob", "X-Keph-Token-vm": dummyTokenB})
	e.mcp(t, "status").want(t, 0, "claude: eingetragen (vm=bob)", "codex: eingetragen (vm=bob)")
}
