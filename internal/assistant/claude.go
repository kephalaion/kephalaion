package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
)

// claudeClient ist Claude Code. Geschrieben wird nur über claude mcp
// add-json und claude mcp remove auf User-Ebene; gelesen wird der Eintrag aus
// der Datei, die claude dabei schreibt (mcpServers.kephalaion in
// ~/.claude.json bzw. $CLAUDE_CONFIG_DIR/.claude.json) — claude mcp get
// zeigt den Helfer nicht, sucht in allen Scopes und baut eine Verbindung auf.
type claudeClient struct {
	m   *Manager
	bin string
}

// claudeEntry ist der Eintrag, wie add-json ihn bekommt. Die Reihenfolge der
// Felder ist die der Ausgabe.
type claudeEntry struct {
	Type          string `json:"type"`
	URL           string `json:"url"`
	HeadersHelper string `json:"headersHelper"`
}

// claudeJSON ist das JSON für claude mcp add-json.
func claudeJSON(want entry) (string, error) {
	b, err := json.Marshal(claudeEntry{Type: "http", URL: want.URL, HeadersHelper: want.helper()})
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// configPath ist die Datei mit den Einträgen auf User-Ebene.
func (c *claudeClient) configPath() string {
	if dir := c.m.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return filepath.Join(dir, ".claude.json")
	}
	return filepath.Join(c.m.Home, ".claude.json")
}

func (c *claudeClient) needsLogin() bool { return false }

func (c *claudeClient) read(_ context.Context) (found, error) {
	path := c.configPath()
	data, exists, err := readFile(path)
	if err != nil {
		return found{}, err
	}
	if !exists {
		return found{}, nil
	}
	var file struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		return found{}, fmt.Errorf("%s: kein gültiges JSON: %w", path, err)
	}
	raw, ok := file.MCPServers[EntryName]
	if !ok {
		return found{}, nil
	}
	f := found{exists: true}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		// Kein Objekt: fremder Inhalt.
		f.raw = map[string]any{}
		return f, nil
	}
	f.raw = fields
	if helper, ok := fields["headersHelper"].(string); ok {
		f.choice = helperChoice(helper)
	}
	return f, nil
}

// helperChoice liest die Wahl aus der Kommandozeile eines Helfers; bei einer
// fremden Zeile ist sie leer.
func helperChoice(helper string) Choice {
	args, ok := ShellSplit(helper)
	if !ok {
		return nil
	}
	_, _, choice, ok := ParseHelperArgs(args)
	if !ok {
		return nil
	}
	return choice
}

func (c *claudeClient) compare(f found, want entry) string {
	fields, _ := f.raw.(map[string]any)
	if t, _ := fields["type"].(string); t != "http" {
		return "fremder Inhalt (kein Eintrag vom Typ http)"
	}
	if _, ok := fields["headers"]; ok {
		return "fremder Inhalt (feste Header im Eintrag)"
	}
	if u, _ := fields["url"].(string); u != want.URL {
		return fmt.Sprintf("andere Adresse (%s statt %s)", u, want.URL)
	}
	if h, _ := fields["headersHelper"].(string); h != want.helper() {
		return "anderer Helfer"
	}
	return ""
}

func (c *claudeClient) write(ctx context.Context, f found, want entry) error {
	body, err := claudeJSON(want)
	if err != nil {
		return err
	}
	// add-json scheitert, wenn der Name schon steht.
	if f.exists {
		if err := c.remove(ctx, f); err != nil {
			return err
		}
	}
	_, err = c.m.run(ctx, c.bin, "mcp", "add-json", EntryName, body, "--scope", "user")
	return err
}

func (c *claudeClient) remove(ctx context.Context, _ found) error {
	_, err := c.m.run(ctx, c.bin, "mcp", "remove", EntryName, "--scope", "user")
	return err
}
