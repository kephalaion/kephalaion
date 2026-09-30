package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// codexClient ist die Codex CLI. codex mcp add kennt keine eigenen Header,
// und codex mcp add und remove schreiben die ganze Tabelle mcp_servers neu
// (Kommentare der Nachbarn gehen verloren). Deshalb ändert der Client die
// Tabelle [mcp_servers.kephalaion] in $CODEX_HOME/config.toml selbst, am
// Text, und lässt den Rest der Datei, wie er ist. Codex selbst wird nur
// gefragt, ob es die Datei danach noch liest (codex mcp list --json), und
// entfernt einen Eintrag, der nicht als eigene Tabelle dasteht.
type codexClient struct {
	m   *Manager
	bin string
}

// codexTablePath ist der Name der Tabelle des Eintrags.
var codexTablePath = []string{"mcp_servers", EntryName}

// Die Schlüssel der Tabelle, die der Eintrag trägt.
const (
	codexURLKey    = "url"
	codexHelperKey = "http_headers_helper"
)

// codexForeignKeys sind Schlüssel (und Tabellen darunter), die in einem
// eigenen Eintrag nichts zu suchen haben: eine andere Art der Anmeldung —
// womöglich mit einem Token im Klartext — oder ein anderer Transport.
var codexForeignKeys = map[string]bool{
	"http_headers": true, "env_http_headers": true, "bearer_token": true, "bearer_token_env_var": true,
	"command": true, "args": true, "env": true, "env_vars": true, "cwd": true,
}

// codexBlock sind die Zeilen der Tabelle, wie add sie schreibt.
func codexBlock(want entry) []string {
	return []string{
		"[" + strings.Join(codexTablePath, ".") + "]",
		codexURLKey + " = " + tomlString(want.URL),
		codexHelperKey + " = " + tomlString(want.helper()),
	}
}

// codexEntry ist der Eintrag, wie er in der Datei steht.
type codexEntry struct {
	// table: Er steht als eigene Tabelle [mcp_servers.kephalaion] da.
	table bool
	// url und helper sind die Werte der beiden Schlüssel, soweit sie
	// einzeilige Texte sind.
	url, helper string
	// foreign nennt, was in der Tabelle fremd ist; leer heißt nichts.
	foreign string
}

// codexParse liest den Eintrag aus dem Text der Datei.
func codexParse(text string) codexEntry {
	lines, tables := tomlTables(text)
	var e codexEntry
	for _, t := range tables {
		switch {
		case pathEqual(t.path, codexTablePath):
			if e.table || t.array {
				e.foreign = "die Tabelle steht mehrfach da"
			}
			e.table = true
			pairs, ok := tomlPairs(lines[t.header+1 : t.end])
			if !ok {
				e.foreign = "mehrzeilige Werte in der Tabelle"
			}
			for _, p := range pairs {
				switch {
				case len(p.key) == 1 && p.key[0] == codexURLKey && p.isText:
					e.url = p.text
				case len(p.key) == 1 && p.key[0] == codexHelperKey && p.isText:
					e.helper = p.text
				case codexForeignKeys[p.key[0]]:
					e.foreign = "der Schlüssel " + p.key[0]
				}
			}
		case pathBelow(t.path, codexTablePath) && codexForeignKeys[t.path[len(codexTablePath)]]:
			e.table = true
			e.foreign = "die Tabelle " + strings.Join(t.path, ".")
		}
	}
	return e
}

// codexSet schreibt die Tabelle des Eintrags in den Text: an die Stelle der
// vorhandenen, sonst ans Ende. Tabellen darunter mit fremder Anmeldung
// (http_headers, env_http_headers) fallen weg, andere (etwa tools.<name>)
// bleiben. Alles andere bleibt Zeichen für Zeichen.
func codexSet(text string, want entry) string {
	lines, tables := tomlTables(text)
	block := codexBlock(want)
	var out []string
	pos, placed := 0, false
	for _, t := range tables {
		main := pathEqual(t.path, codexTablePath)
		foreign := pathBelow(t.path, codexTablePath) && codexForeignKeys[t.path[len(codexTablePath)]]
		if !main && !foreign {
			continue
		}
		out = append(out, lines[pos:t.header]...)
		pos = contentEnd(lines, t.header, t.end)
		if main && !placed {
			out = append(out, block...)
			placed = true
			continue
		}
		// Eine entfernte Tabelle lässt keine doppelte Leerzeile zurück.
		for pos < len(lines) && strings.TrimSpace(lines[pos]) == "" && (len(out) == 0 || strings.TrimSpace(out[len(out)-1]) == "") {
			pos++
		}
	}
	out = append(out, lines[pos:]...)
	if placed {
		return strings.Join(out, "\n")
	}
	// Ans Ende: nach einer Leerzeile, mit Zeilenende.
	body := strings.TrimRight(strings.Join(out, "\n"), "\n")
	if body != "" {
		body += "\n\n"
	}
	return body + strings.Join(block, "\n") + "\n"
}

// codexRemove entfernt die Tabelle des Eintrags und alle Tabellen darunter
// aus dem Text. Kommentare und Leerzeilen nach der letzten Zeile mit Inhalt
// gehören zur nächsten Tabelle und bleiben.
func codexRemove(text string) string {
	lines, tables := tomlTables(text)
	var out []string
	pos := 0
	for _, t := range tables {
		if !pathEqual(t.path, codexTablePath) && !pathBelow(t.path, codexTablePath) {
			continue
		}
		out = append(out, lines[pos:t.header]...)
		pos = contentEnd(lines, t.header, t.end)
		// Keine doppelte Leerzeile an der Nahtstelle, keine am Anfang.
		for pos < len(lines) && strings.TrimSpace(lines[pos]) == "" && (len(out) == 0 || strings.TrimSpace(out[len(out)-1]) == "") {
			pos++
		}
	}
	out = append(out, lines[pos:]...)
	// Die Leerzeile vor der entfernten Tabelle am Ende der Datei fällt weg.
	for len(out) >= 2 && out[len(out)-1] == "" && strings.TrimSpace(out[len(out)-2]) == "" {
		out = out[:len(out)-1]
	}
	return strings.Join(out, "\n")
}

// configPath ist $CODEX_HOME/config.toml, ohne CODEX_HOME ~/.codex/config.toml.
func (c *codexClient) configPath() string {
	if dir := c.m.Getenv("CODEX_HOME"); dir != "" {
		return filepath.Join(dir, "config.toml")
	}
	return filepath.Join(c.m.Home, ".codex", "config.toml")
}

func (c *codexClient) needsLogin() bool { return false }

// codexFound ist, was read für compare, write und remove merkt.
type codexFound struct {
	entry codexEntry
	// text ist der Inhalt der Datei, fileExists, ob es sie gibt.
	text       string
	fileExists bool
}

// codexServer ist ein Eintrag aus codex mcp list --json.
type codexServer struct {
	Name      string `json:"name"`
	Transport struct {
		Type string `json:"type"`
		URL  string `json:"url"`
	} `json:"transport"`
}

// list fragt Codex nach seinen Einträgen: Es liest dabei seine config, ein
// Fehler heißt, dass sie ungültig ist.
func (c *codexClient) list(ctx context.Context) (map[string]codexServer, error) {
	out, err := c.m.run(ctx, c.bin, "mcp", "list", "--json")
	if err != nil {
		return nil, err
	}
	var servers []codexServer
	if err := json.Unmarshal([]byte(out), &servers); err != nil {
		return nil, fmt.Errorf("codex mcp list --json: keine lesbare Antwort: %w", err)
	}
	byName := map[string]codexServer{}
	for _, s := range servers {
		byName[s.Name] = s
	}
	return byName, nil
}

func (c *codexClient) read(ctx context.Context) (found, error) {
	path := c.configPath()
	data, exists, err := readFile(path)
	if err != nil {
		return found{}, err
	}
	cf := codexFound{text: string(data), fileExists: exists, entry: codexParse(string(data))}
	f := found{exists: cf.entry.table, raw: cf}
	if cf.entry.table {
		f.choice = helperChoice(cf.entry.helper)
		return f, nil
	}
	// Keine eigene Tabelle. Nennt die Datei den Namen trotzdem, kann der
	// Eintrag in anderer Form dastehen (Inline-Tabelle, gepunktete
	// Schlüssel): Das weiß nur Codex.
	if strings.Contains(cf.text, EntryName) {
		servers, err := c.list(ctx)
		if err != nil {
			return found{}, err
		}
		_, f.exists = servers[EntryName]
	}
	return f, nil
}

func (c *codexClient) compare(f found, want entry) string {
	e := f.raw.(codexFound).entry
	switch {
	case !e.table:
		return "fremder Inhalt (der Eintrag steht nicht als eigene Tabelle da)"
	case e.foreign != "":
		return "fremder Inhalt (" + e.foreign + ")"
	case e.url != want.URL:
		return fmt.Sprintf("andere Adresse (%s statt %s)", e.url, want.URL)
	case e.helper != want.helper():
		return "anderer Helfer"
	}
	return ""
}

func (c *codexClient) write(ctx context.Context, f found, want entry) error {
	cf := f.raw.(codexFound)
	path := c.configPath()
	text := cf.text
	if f.exists && !cf.entry.table {
		// Ein Eintrag in anderer Form: Nur Codex kann ihn entfernen.
		if _, err := c.m.run(ctx, c.bin, "mcp", "remove", EntryName); err != nil {
			return err
		}
		data, _, err := readFile(path)
		if err != nil {
			return err
		}
		text = string(data)
	}
	if err := writeFile(path, []byte(codexSet(text, want))); err != nil {
		return err
	}
	servers, err := c.list(ctx)
	if err == nil {
		if s, ok := servers[EntryName]; !ok || s.Transport.URL != want.URL {
			err = errors.New("Codex liest den Eintrag nicht so, wie er geschrieben wurde")
		}
	}
	if err != nil {
		return c.rollback(path, cf, err)
	}
	return nil
}

func (c *codexClient) remove(ctx context.Context, f found) error {
	cf := f.raw.(codexFound)
	path := c.configPath()
	if !cf.entry.table {
		_, err := c.m.run(ctx, c.bin, "mcp", "remove", EntryName)
		return err
	}
	if err := writeFile(path, []byte(codexRemove(cf.text))); err != nil {
		return err
	}
	servers, err := c.list(ctx)
	if err == nil {
		if _, still := servers[EntryName]; still {
			err = errors.New("Codex nennt den Eintrag noch")
		}
	}
	if err != nil {
		return c.rollback(path, cf, err)
	}
	return nil
}

// rollback stellt die Datei wieder her, wie sie vor dem Schreiben war: Eine
// config, die Codex nicht liest, legte Codex ganz lahm.
func (c *codexClient) rollback(path string, cf codexFound, cause error) error {
	var err error
	if cf.fileExists {
		err = writeFile(path, []byte(cf.text))
	} else {
		err = os.Remove(path)
	}
	if err != nil {
		return fmt.Errorf("%w; %s ließ sich nicht wiederherstellen: %v", cause, path, err)
	}
	return fmt.Errorf("%w; %s ist wiederhergestellt", cause, path)
}
