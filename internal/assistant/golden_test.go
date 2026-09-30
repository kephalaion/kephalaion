package assistant

import (
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "schreibt die Dateien unter testdata/ neu")

// golden vergleicht got mit testdata/<name>; mit -update schreibt es sie.
func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("%s weicht ab:\n--- erhalten\n%s\n--- erwartet\n%s", name, got, want)
	}
}

func testdata(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// goldenEntry ist der Eintrag der Golden-Tests: zwei Hubs, ein Pfad mit
// Leerzeichen.
func goldenEntry() entry {
	tokens := "/home/anna/.config/kephalaion/tokens"
	return entry{
		Target: Target{URL: "http://127.0.0.1:7433/mcp", Binary: "/home/anna/.local/bin/kephalaion", TokensDir: tokens},
		logins: []Login{
			{Hub: "eigen", Account: "kp", File: TokenFile(tokens, "eigen", "kp")},
			{Hub: "vm", Account: "alice", File: TokenFile(tokens, "vm", "alice")},
		},
	}
}

func spacedEntry() entry {
	e := goldenEntry()
	e.Binary = `/home/anna b/bin "k"/kephalaion`
	e.TokensDir = `/home/anna b/.config/kephalaion/tokens`
	return e
}

func TestClaudeJSONGolden(t *testing.T) {
	body, err := claudeJSON(goldenEntry())
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "claude-entry.json", body+"\n")
	body, err = claudeJSON(spacedEntry())
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "claude-entry-quoted.json", body+"\n")
}

func TestCodexTableGolden(t *testing.T) {
	// Keine Datei: nur die Tabelle.
	golden(t, "codex-new.toml", codexSet("", goldenEntry()))
	// Anführungszeichen und Leerzeichen im Pfad: als TOML-Text geschrieben
	// und so wieder gelesen.
	quoted := codexSet("", spacedEntry())
	golden(t, "codex-new-quoted.toml", quoted)
	if e := codexParse(quoted); e.helper != spacedEntry().helper() || e.url != spacedEntry().URL || e.foreign != "" {
		t.Errorf("zurückgelesen: %+v", e)
	}
}

// Eine Datei mit Kommentaren und anderen Tabellen: add hängt die Tabelle an,
// remove nimmt sie wieder weg — der Rest bleibt Zeichen für Zeichen.
func TestCodexAppendGolden(t *testing.T) {
	input := testdata(t, "codex-input.toml")
	if e := codexParse(input); e.table {
		t.Fatalf("Eintrag in einem Text oder Feld erkannt: %+v", e)
	}
	added := codexSet(input, goldenEntry())
	golden(t, "codex-appended.toml", added)
	if !strings.HasPrefix(added, input) {
		t.Error("add hat den vorhandenen Text verändert")
	}
	e := codexParse(added)
	if !e.table || e.url != "http://127.0.0.1:7433/mcp" || e.helper != goldenEntry().helper() || e.foreign != "" {
		t.Errorf("zurückgelesen: %+v", e)
	}
	// Ein zweites add ändert nichts.
	if again := codexSet(added, goldenEntry()); again != added {
		t.Errorf("zweites add ändert die Datei:\n%s", again)
	}
	if removed := codexRemove(added); removed != input {
		t.Errorf("remove stellt die Datei nicht wieder her:\n%s", removed)
	}
	// Ohne Zeilenende am Schluss.
	if got := codexSet("a = 1", goldenEntry()); !strings.HasPrefix(got, "a = 1\n\n[mcp_servers.kephalaion]\n") {
		t.Errorf("ohne Zeilenende:\n%s", got)
	}
}

// Ein vorhandener Eintrag mit fremdem Inhalt: add ersetzt die Tabelle an
// ihrer Stelle und nimmt die Tabelle mit festen Headern weg; die Tabelle
// tools.whoami, Kommentare und die Nachbarn bleiben.
func TestCodexReplaceGolden(t *testing.T) {
	input := testdata(t, "codex-existing.toml")
	e := codexParse(input)
	if !e.table || e.url != "http://127.0.0.1:9999/mcp" || e.foreign == "" {
		t.Fatalf("vorhandener Eintrag: %+v", e)
	}
	replaced := codexSet(input, goldenEntry())
	golden(t, "codex-replaced.toml", replaced)
	if strings.Contains(replaced, "im-klartext") || strings.Contains(replaced, "bearer_token_env_var") {
		t.Error("fremder Inhalt bleibt stehen")
	}
	e = codexParse(replaced)
	if !e.table || e.url != "http://127.0.0.1:7433/mcp" || e.helper != goldenEntry().helper() || e.foreign != "" {
		t.Errorf("zurückgelesen: %+v", e)
	}
	removed := codexRemove(replaced)
	golden(t, "codex-removed.toml", removed)
	if strings.Contains(removed, "kephalaion]") || strings.Contains(removed, "kephalaion.") {
		t.Error("remove lässt eine Tabelle des Eintrags stehen")
	}
	if codexRemove(input) != removed {
		t.Error("remove aus der Datei mit fremdem Inhalt weicht ab")
	}
	if codexRemove(removed) != removed {
		t.Error("ein zweites remove ändert die Datei")
	}
	// Alles außerhalb der Tabellen des Eintrags steht noch da, in derselben
	// Reihenfolge, Zeichen für Zeichen.
	pos := 0
	for _, line := range []string{
		"# Meine Codex-config", `model = "gpt-5"   # Modell`, "# vor andere", "[mcp_servers.andere]", "# in andere",
		`command = "echo"`, `args = ["x"]  # Kommentar am Ende`, `env = { A = "1" }`, "# vor kephalaion", "# vor dritte",
		"[mcp_servers.dritte]", `url   =   "http://example.invalid/mcp"   # seltsam formatiert`, "[tui]",
		"# noch ein Kommentar", "notifications = false   # aus",
	} {
		for _, text := range []string{replaced, removed} {
			if !strings.Contains(text, line+"\n") {
				t.Errorf("es fehlt die Zeile %q", line)
			}
		}
		i := strings.Index(removed[pos:], line)
		if i < 0 {
			t.Fatalf("nach remove steht an anderer Stelle: %q", line)
		}
		pos += i + len(line)
	}
}

func TestCodexParse(t *testing.T) {
	cases := []struct {
		name, text string
		want       codexEntry
	}{
		{"leer", "", codexEntry{}},
		{"nur andere", "[mcp_servers.andere]\nurl = \"x\"\n", codexEntry{}},
		{"eigene", "[mcp_servers.kephalaion]\nurl = \"http://a/mcp\"\nhttp_headers_helper = \"/k node mcp headers --tokens-dir /t\"\n",
			codexEntry{table: true, url: "http://a/mcp", helper: "/k node mcp headers --tokens-dir /t"}},
		{"literal und Kommentar", "[mcp_servers.kephalaion] # x\nurl = 'http://a/mcp' # y\nenabled = true\n",
			codexEntry{table: true, url: "http://a/mcp"}},
		{"fester Header als Schlüssel", "[mcp_servers.kephalaion]\nurl = \"u\"\nhttp_headers.X-Keph-Token-vm = \"t\"\n",
			codexEntry{table: true, url: "u", foreign: "der Schlüssel http_headers"}},
		{"stdio", "[mcp_servers.kephalaion]\ncommand = \"kephalaion\"\n",
			codexEntry{table: true, foreign: "der Schlüssel command"}},
		{"Tabelle darunter", "[mcp_servers.kephalaion.env_http_headers]\nX = \"Y\"\n",
			codexEntry{table: true, foreign: "die Tabelle mcp_servers.kephalaion.env_http_headers"}},
		{"nur tools darunter", "[mcp_servers.kephalaion.tools.whoami]\napproval_mode = \"approve\"\n", codexEntry{}},
		{"mehrzeilig", "[mcp_servers.kephalaion]\nurl = \"u\"\nargs = [\n  \"x\",\n]\n",
			codexEntry{table: true, url: "u", foreign: "der Schlüssel args"}},
		{"gepunktet", "mcp_servers.kephalaion.url = \"u\"\n", codexEntry{}},
	}
	for _, c := range cases {
		if got := codexParse(c.text); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %+v, erwartet %+v", c.name, got, c.want)
		}
	}
}

func TestTOMLStrings(t *testing.T) {
	for _, s := range []string{"", "einfach", `mit "Anführung" und \ Schrägstrich`, "Zeile\nTab\t", "ä ö ü €", "\x01\x7f"} {
		quoted := tomlString(s)
		got, ok := tomlTextValue(quoted + "  # Kommentar")
		if !ok || got != s {
			t.Errorf("tomlString(%q) = %s, zurück %q, %v", s, quoted, got, ok)
		}
	}
	for _, bad := range []string{`"offen`, `"a" b`, `"""x"""`, `12`, `"\q"`, `'offen`} {
		if _, ok := tomlTextValue(bad); ok {
			t.Errorf("tomlTextValue(%s): gilt als Text", bad)
		}
	}
	for line, want := range map[string][]string{
		"[a]":                        {"a"},
		"  [ a . b ]  # c":           {"a", "b"},
		`[a."b c".'d']`:              {"a", "b c", "d"},
		"[[a.b]]":                    {"a", "b"},
		"[mcp_servers.kephalaion]":   {"mcp_servers", "kephalaion"},
		`[mcp_servers."kephalaion"]`: {"mcp_servers", "kephalaion"},
	} {
		if got, _, ok := tomlHeader(line); !ok || !reflect.DeepEqual(got, want) {
			t.Errorf("tomlHeader(%q) = %v, %v", line, got, ok)
		}
	}
	for _, line := range []string{"a = 1", "[a", "[a] b", "[3, 4]", "[a b]", "[]", `["a", "b"]`, "# [a]"} {
		if _, _, ok := tomlHeader(line); ok {
			t.Errorf("tomlHeader(%q): gilt als Kopf", line)
		}
	}
}
