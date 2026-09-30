package assistant

import (
	"strings"
	"testing"
)

// remove nimmt nur den Schlüssel mcp.kephalaion weg: Kommentare, Kommas am
// Ende, {env:…}- und {file:…}-Verweise der übrigen Einträge bleiben.
func TestOpenCodeRemoveGolden(t *testing.T) {
	input := testdata(t, "opencode-input.jsonc")
	out, err := opencodeRemove([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "opencode-removed.jsonc", string(out))
	if strings.Contains(string(out), "kephalaion\"") || strings.Contains(string(out), "X-Keph") {
		t.Error("der Eintrag steht noch da")
	}
	for _, keep := range []string{
		"// Meine OpenCode-config", `"model": "litellm/x", // Modell`, `"{env:LITELLM_BASE_URL}"`,
		`"{file:~/.config/opencode/secrets/key}"`, `"enabled": true,`, "// Playwright: Browser-Automatisierung",
		`"command": ["npx", "-y", "@playwright/mcp@latest"], // am Ende`,
	} {
		if !strings.Contains(string(out), keep) {
			t.Errorf("es fehlt %q", keep)
		}
	}
	again, err := opencodeRemove(out)
	if err != nil || string(again) != string(out) {
		t.Errorf("ein zweites remove ändert die Datei: %v\n%s", err, again)
	}
	// Ohne Eintrag bleibt die Datei Byte für Byte.
	for _, text := range []string{"{}", "// nur Kommentar\n{\n  \"mcp\": {}, \n}\n", "{\"model\": \"x\"}"} {
		if got, err := opencodeRemove([]byte(text)); err != nil || string(got) != text {
			t.Errorf("opencodeRemove(%q) = %q, %v", text, got, err)
		}
	}
	if _, err := opencodeRemove([]byte("{kaputt")); err == nil {
		t.Error("kaputte Datei ohne Fehler")
	}
}

// Der Verweis auf eine Token-Datei: ~/…, wenn sie unter dem
// Heimatverzeichnis liegt, sonst absolut.
func TestOpenCodeTokenRef(t *testing.T) {
	c := &opencodeClient{m: &Manager{Home: "/home/anna"}}
	for file, want := range map[string]string{
		"/home/anna/.config/kephalaion/tokens/vm/alice.token": "{file:~/.config/kephalaion/tokens/vm/alice.token}",
		"/srv/tokens/vm/alice.token":                          "{file:/srv/tokens/vm/alice.token}",
		"/home/annabell/tokens/vm/a.token":                    "{file:/home/annabell/tokens/vm/a.token}",
	} {
		if got := c.tokenRef(file); got != want {
			t.Errorf("tokenRef(%s) = %s, erwartet %s", file, got, want)
		}
	}
	if got := c.resolveRef("~/x/y.token", "/etc/opencode.json"); got != "/home/anna/x/y.token" {
		t.Errorf("resolveRef ~/ = %s", got)
	}
	if got := c.resolveRef("secrets/k", "/home/anna/.config/opencode/opencode.jsonc"); got != "/home/anna/.config/opencode/secrets/k" {
		t.Errorf("resolveRef relativ = %s", got)
	}
	if versionLess([3]int{1, 17, 0}, opencodeMinVersion) || !versionLess([3]int{1, 16, 9}, opencodeMinVersion) {
		t.Error("versionLess")
	}
}

// Der Eintrag als letzter, wie opencode mcp add ihn an ein Objekt mit Komma
// am Ende hängt (jsonc-parser setzt ihn vor das Komma): remove stellt die
// Datei davor wieder her.
func TestOpenCodeRemoveLast(t *testing.T) {
	before := "{\n  \"mcp\": {\n    \"fremd\": {\n      \"type\": \"remote\",\n    },\n  },\n}\n"
	after := "{\n  \"mcp\": {\n    \"fremd\": {\n      \"type\": \"remote\",\n    },\n    \"kephalaion\": {\n      \"type\": \"remote\"\n    },\n  },\n}\n"
	got, err := opencodeRemove([]byte(after))
	if err != nil || string(got) != before {
		t.Errorf("remove:\n%s\nerwartet:\n%s", got, before)
	}
	// Ohne Komma am Ende, wie in der echten config.
	before = "{\n  \"mcp\": {\n    \"fremd\": {\n      \"type\": \"remote\"\n    }\n  }\n}\n"
	after = "{\n  \"mcp\": {\n    \"fremd\": {\n      \"type\": \"remote\"\n    },\n    \"kephalaion\": {\n      \"type\": \"remote\"\n    }\n  }\n}\n"
	got, err = opencodeRemove([]byte(after))
	if err != nil || string(got) != before {
		t.Errorf("remove ohne Komma:\n%s\nerwartet:\n%s", got, before)
	}
}
