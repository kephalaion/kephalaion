// Package assistanttest spielt die Kommandos der KI-Assistenten nach — nur für
// Tests: Kein Test ruft einen echten Assistenten auf. Der Nachbau schreibt
// die Dateien, die der echte Assistent schriebe, in ein temporäres HOME.
package assistanttest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/tailscale/hujson"

	"github.com/kephalaion/kephalaion/internal/assistant"
)

// Fake spielt claude, opencode und codex nach.
type Fake struct {
	// OpenCodeVersion ist, was opencode --version meldet.
	OpenCodeVersion string
	// Home ist das Heimatverzeichnis, in dem die Assistenten ihre Dateien
	// halten.
	Home string
	// Env ist die Umgebung der Assistenten (CLAUDE_CONFIG_DIR, CODEX_HOME).
	Env map[string]string
	// Installed nennt die Assistenten, die im PATH liegen.
	Installed map[string]bool
	// Calls sind die Aufrufe in ihrer Reihenfolge: Programm und Argumente,
	// durch Leerzeichen verbunden.
	Calls []string
	// Fail lässt Aufrufe scheitern, die mit einem der Schlüssel beginnen.
	Fail map[string]error
}

// New liefert einen Nachbau mit den genannten Assistenten im PATH.
func New(home string, installed ...string) *Fake {
	f := &Fake{Home: home, Env: map[string]string{}, Installed: map[string]bool{}, Fail: map[string]error{},
		OpenCodeVersion: "1.18.33"}
	for _, name := range installed {
		f.Installed[name] = true
	}
	return f
}

// Manager liefert einen Manager, der nur den Nachbau aufruft.
func (f *Fake) Manager() *assistant.Manager {
	return &assistant.Manager{
		Run:      f.Run,
		LookPath: f.LookPath,
		Getenv:   func(name string) string { return f.Env[name] },
		Home:     f.Home,
	}
}

// binDir ist das Verzeichnis, in dem die nachgespielten Programme „liegen“.
const binDir = "/fake/bin"

// LookPath findet die Assistenten aus Installed.
func (f *Fake) LookPath(name string) (string, error) {
	if f.Installed[name] {
		return binDir + "/" + name, nil
	}
	return "", fmt.Errorf("%s: nicht im PATH", name)
}

// Writes zählt die Aufrufe, die etwas ändern (alles außer codex mcp list und
// opencode --version).
func (f *Fake) Writes() []string {
	var out []string
	for _, c := range f.Calls {
		if !strings.HasPrefix(c, "codex mcp list") && c != "opencode --version" {
			out = append(out, c)
		}
	}
	return out
}

// Run ist der Runner des Nachbaus.
func (f *Fake) Run(_ context.Context, name string, args ...string) (string, error) {
	prog := filepath.Base(name)
	call := strings.Join(append([]string{prog}, args...), " ")
	f.Calls = append(f.Calls, call)
	if filepath.Dir(name) != binDir || !f.Installed[prog] {
		return "", fmt.Errorf("im Test aufgerufen: %s", call)
	}
	for prefix, err := range f.Fail {
		if strings.HasPrefix(call, prefix) {
			return "", err
		}
	}
	switch {
	case prog == assistant.Claude && len(args) == 6 && args[0] == "mcp" && args[1] == "add-json" &&
		args[4] == "--scope" && args[5] == "user":
		return f.claudeAdd(args[2], args[3])
	case prog == assistant.Claude && len(args) == 5 && args[0] == "mcp" && args[1] == "remove" &&
		args[3] == "--scope" && args[4] == "user":
		return f.claudeRemove(args[2])
	case prog == assistant.OpenCode && call == "opencode --version":
		return f.OpenCodeVersion + "\n", nil
	case prog == assistant.OpenCode && len(args) >= 5 && args[0] == "mcp" && args[1] == "add" && args[3] == "--url":
		return f.opencodeAdd(args[2], args[4], args[5:])
	case prog == assistant.Codex && call == "codex mcp list --json":
		return f.codexList()
	case prog == assistant.Codex && len(args) == 3 && args[0] == "mcp" && args[1] == "remove":
		return f.codexRemove(args[2])
	}
	return "", fmt.Errorf("der Nachbau kennt den Aufruf nicht: %s", call)
}

// ClaudeConfig ist die Datei, in der Claude Code die Einträge auf User-Ebene
// hält.
func (f *Fake) ClaudeConfig() string {
	if dir := f.Env["CLAUDE_CONFIG_DIR"]; dir != "" {
		return filepath.Join(dir, ".claude.json")
	}
	return filepath.Join(f.Home, ".claude.json")
}

// claudeFile liest die Datei von Claude Code als Objekt mit rohen Werten.
func (f *Fake) claudeFile() (file, servers map[string]json.RawMessage, err error) {
	file = map[string]json.RawMessage{}
	servers = map[string]json.RawMessage{}
	data, err := os.ReadFile(f.ClaudeConfig())
	if errors.Is(err, os.ErrNotExist) {
		return file, servers, nil
	}
	if err != nil {
		return nil, nil, err
	}
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, nil, err
	}
	if raw, ok := file["mcpServers"]; ok {
		if err := json.Unmarshal(raw, &servers); err != nil {
			return nil, nil, err
		}
	}
	return file, servers, nil
}

func (f *Fake) claudeSave(file, servers map[string]json.RawMessage) error {
	raw, err := json.Marshal(servers)
	if err != nil {
		return err
	}
	file["mcpServers"] = raw
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(f.ClaudeConfig()), 0o700); err != nil {
		return err
	}
	return os.WriteFile(f.ClaudeConfig(), append(data, '\n'), 0o600)
}

func (f *Fake) claudeAdd(name, body string) (string, error) {
	if !json.Valid([]byte(body)) {
		return "", errors.New("Invalid JSON")
	}
	file, servers, err := f.claudeFile()
	if err != nil {
		return "", err
	}
	if _, ok := servers[name]; ok {
		return "", fmt.Errorf("MCP server %s already exists in user config", name)
	}
	servers[name] = json.RawMessage(body)
	if err := f.claudeSave(file, servers); err != nil {
		return "", err
	}
	return fmt.Sprintf("Added http MCP server %s to user config\n", name), nil
}

func (f *Fake) claudeRemove(name string) (string, error) {
	file, servers, err := f.claudeFile()
	if err != nil {
		return "", err
	}
	if _, ok := servers[name]; !ok {
		return "", fmt.Errorf("No MCP server named %q in user scope", name)
	}
	delete(servers, name)
	if err := f.claudeSave(file, servers); err != nil {
		return "", err
	}
	return fmt.Sprintf("Removed MCP server %s from user config\n", name), nil
}

// CodexConfig ist die config von Codex.
func (f *Fake) CodexConfig() string {
	if dir := f.Env["CODEX_HOME"]; dir != "" {
		return filepath.Join(dir, "config.toml")
	}
	return filepath.Join(f.Home, ".codex", "config.toml")
}

var (
	codexHeader = regexp.MustCompile(`^\s*\[\s*mcp_servers\s*\.\s*"?([A-Za-z0-9_-]+)"?\s*\]`)
	codexDotted = regexp.MustCompile(`^\s*mcp_servers\.([A-Za-z0-9_-]+)\.url\s*=\s*"([^"]*)"`)
	codexInline = regexp.MustCompile(`^\s*mcp_servers\s*=\s*\{`)
	codexURL    = regexp.MustCompile(`^\s*url\s*=\s*"([^"]*)"`)
	anyHeader   = regexp.MustCompile(`^\s*\[`)
)

// codexServers liest die Einträge aus der config, so weit der Nachbau TOML
// versteht: Tabellen [mcp_servers.<name>] und gepunktete Schlüssel
// mcp_servers.<name>.url. Wie Codex lehnt er eine Tabelle ab, die zweimal
// dasteht oder eine Inline-Tabelle mcp_servers erweitert.
func (f *Fake) codexServers() (map[string]string, error) {
	data, err := os.ReadFile(f.CodexConfig())
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	servers := map[string]string{}
	current, inline := "", false
	for i, line := range strings.Split(string(data), "\n") {
		switch {
		case codexInline.MatchString(line):
			inline = true
		case codexHeader.MatchString(line):
			current = codexHeader.FindStringSubmatch(line)[1]
			if _, dup := servers[current]; dup {
				return nil, fmt.Errorf("failed to load bootstrap configuration: config.toml:%d: duplicate key", i+1)
			}
			if inline {
				return nil, fmt.Errorf("failed to load bootstrap configuration: config.toml:%d: cannot extend value of type inline table", i+1)
			}
			servers[current] = ""
		case anyHeader.MatchString(line):
			current = ""
		case codexDotted.MatchString(line):
			m := codexDotted.FindStringSubmatch(line)
			servers[m[1]] = m[2]
		case current != "" && codexURL.MatchString(line):
			servers[current] = codexURL.FindStringSubmatch(line)[1]
		}
	}
	return servers, nil
}

func (f *Fake) codexList() (string, error) {
	servers, err := f.codexServers()
	if err != nil {
		return "", err
	}
	type transport struct {
		Type string `json:"type"`
		URL  string `json:"url"`
	}
	type server struct {
		Name      string    `json:"name"`
		Transport transport `json:"transport"`
	}
	list := []server{}
	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		list = append(list, server{Name: name, Transport: transport{Type: "streamable_http", URL: servers[name]}})
	}
	out, err := json.Marshal(list)
	return string(out), err
}

// codexRemove entfernt einen Eintrag wie codex mcp remove — hier nur die
// gepunkteten Schlüssel, die Form, für die node mcp Codex selbst bemüht.
func (f *Fake) codexRemove(name string) (string, error) {
	data, err := os.ReadFile(f.CodexConfig())
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Sprintf("No MCP server named '%s' found.\n", name), nil
	}
	if err != nil {
		return "", err
	}
	var kept []string
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "mcp_servers."+name+".") {
			kept = append(kept, line)
		}
	}
	if err := os.WriteFile(f.CodexConfig(), []byte(strings.Join(kept, "\n")), 0o600); err != nil {
		return "", err
	}
	return fmt.Sprintf("Removed global MCP server '%s'.\n", name), nil
}

// OpenCodeConfig ist die Datei, die opencode mcp add schreibt: opencode.json,
// wenn es sie gibt, sonst opencode.jsonc, wenn es sie gibt, sonst
// opencode.json.
func (f *Fake) OpenCodeConfig() string {
	dir := filepath.Join(f.Home, ".config", "opencode")
	if x := f.Env["XDG_CONFIG_HOME"]; x != "" {
		dir = filepath.Join(x, "opencode")
	}
	for _, name := range []string{"opencode.json", "opencode.jsonc"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return filepath.Join(dir, name)
		}
	}
	return filepath.Join(dir, "opencode.json")
}

var opencodeFileRef = regexp.MustCompile(`\{file:([^}]+)\}`)

// opencodeAdd spielt opencode mcp add <name> --url <url> --header K=V… nach:
// Wie OpenCode scheitert es, wenn ein {file:…} der config auf eine fehlende
// Datei verweist; sonst setzt es mcp.<name> und lässt den Rest der Datei, wie
// er ist.
func (f *Fake) opencodeAdd(name, url string, rest []string) (string, error) {
	headers := map[string]string{}
	for i := 0; i < len(rest); i += 2 {
		if rest[i] != "--header" || i+1 >= len(rest) {
			return "", fmt.Errorf("der Nachbau kennt die Option nicht: %s", rest[i])
		}
		k, v, ok := strings.Cut(rest[i+1], "=")
		if !ok || k == "" {
			return "", fmt.Errorf("Invalid HTTP header: %s. Expected KEY=VALUE", rest[i+1])
		}
		headers[k] = v
	}
	path := f.OpenCodeConfig()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		data = []byte("{}")
	} else if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "//") {
			continue
		}
		for _, m := range opencodeFileRef.FindAllStringSubmatch(line, -1) {
			ref := m[1]
			if r, ok := strings.CutPrefix(ref, "~/"); ok {
				ref = filepath.Join(f.Home, r)
			} else if !filepath.IsAbs(ref) {
				ref = filepath.Join(filepath.Dir(path), ref)
			}
			if _, err := os.Stat(ref); err != nil {
				return "", fmt.Errorf("Configuration is invalid at %s: bad file reference: %q %s does not exist", path, m[0], ref)
			}
		}
	}
	entry := map[string]any{"type": "remote", "url": url}
	if len(headers) > 0 {
		entry["headers"] = headers
	}
	v, err := hujson.Parse(data)
	if err != nil {
		return "", err
	}
	op := map[string]any{"op": "add", "path": "/mcp/" + name, "value": entry}
	if v.Find("/mcp") == nil {
		op = map[string]any{"op": "add", "path": "/mcp", "value": map[string]any{name: entry}}
	}
	patch, err := json.Marshal([]any{op})
	if err != nil {
		return "", err
	}
	// Wie jsonc-parser: Ein neuer Eintrag kommt vor ein Komma am Ende, das
	// Komma bleibt.
	trailing := false
	if mcp := v.Find("/mcp"); mcp != nil {
		if obj, ok := mcp.Value.(*hujson.Object); ok && len(obj.Members) > 0 {
			trailing = obj.Members[len(obj.Members)-1].Value.AfterExtra != nil
		}
	}
	if err := v.Patch(patch); err != nil {
		return "", err
	}
	if obj, ok := v.Find("/mcp").Value.(*hujson.Object); ok && trailing && len(obj.Members) > 0 {
		if last := &obj.Members[len(obj.Members)-1].Value; last.AfterExtra == nil {
			last.AfterExtra = hujson.Extra{}
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, v.Pack(), 0o600); err != nil {
		return "", err
	}
	return fmt.Sprintf("MCP server %q added to %s\n", name, path), nil
}
