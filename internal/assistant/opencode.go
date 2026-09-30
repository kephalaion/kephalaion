package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/tailscale/hujson"

	"github.com/kephalaion/kephalaion/internal/ident"
)

// opencodeClient ist OpenCode. Eingetragen wird über opencode mcp add (ab
// 1.17.0 ohne Rückfrage; ändert in der globalen config nur mcp.<name>,
// Kommentare bleiben). Das Token steht nicht in der Datei, sondern ein
// Verweis auf die Token-Datei ({file:~/…}), den OpenCode vor dem Parsen
// ersetzt. Ein mcp remove gibt es nicht: Entfernt wird der Schlüssel
// mcp.kephalaion am Text (JWCC), sonst nichts.
//
// Falle: Verweist ein {file:…} auf eine fehlende Datei, ist die ganze config
// ungültig — OpenCode startet nicht, und auch opencode mcp add scheitert.
// Deshalb nimmt add nur vorhandene Token-Dateien auf und entfernt einen
// Eintrag mit totem Verweis selbst, bevor es opencode mcp add ruft.
type opencodeClient struct {
	m   *Manager
	bin string
}

// opencodeMinVersion ist die erste Fassung, deren mcp add ohne Rückfrage
// einträgt.
var opencodeMinVersion = [3]int{1, 17, 0}

// configDir ist das Verzeichnis der globalen config von OpenCode:
// $XDG_CONFIG_HOME/opencode bzw. ~/.config/opencode.
func (c *opencodeClient) configDir() string {
	if v := c.m.Getenv("XDG_CONFIG_HOME"); v != "" && filepath.IsAbs(v) {
		return filepath.Join(v, "opencode")
	}
	return filepath.Join(c.m.Home, ".config", "opencode")
}

// configPaths liefert die Datei, die opencode mcp add schreibt — opencode.json,
// wenn es sie gibt, sonst opencode.jsonc, wenn es sie gibt, sonst
// opencode.json — und ob es beide gibt (OpenCode führt sie dann zusammen).
func (c *opencodeClient) configPaths() (path string, both bool) {
	dir := c.configDir()
	jsonPath, jsoncPath := filepath.Join(dir, "opencode.json"), filepath.Join(dir, "opencode.jsonc")
	_, errJSON := os.Stat(jsonPath)
	_, errJSONC := os.Stat(jsoncPath)
	switch {
	case errJSON == nil:
		return jsonPath, errJSONC == nil
	case errJSONC == nil:
		return jsoncPath, false
	}
	return jsonPath, false
}

func (c *opencodeClient) needsLogin() bool { return true }

// fileRef ist ein Verweis {file:<pfad>}.
var fileRef = regexp.MustCompile(`^\{file:([^}]+)\}$`)

// opencodeFound ist, was read für compare, write und remove merkt.
type opencodeFound struct {
	path string
	data []byte
	// entry ist der Wert von mcp.kephalaion; nil, wenn es ihn nicht gibt
	// oder er kein Objekt ist.
	entry map[string]any
	// dead nennt die Verweise des Eintrags auf fehlende Dateien.
	dead []string
}

// resolveRef löst den Pfad eines Verweises auf, wie OpenCode es tut: ~/ ist
// das Heimatverzeichnis, ein relativer Pfad gilt ab dem Verzeichnis der config.
func (c *opencodeClient) resolveRef(ref, configPath string) string {
	if rest, ok := strings.CutPrefix(ref, "~/"); ok {
		return filepath.Join(c.m.Home, rest)
	}
	if filepath.IsAbs(ref) {
		return ref
	}
	return filepath.Join(filepath.Dir(configPath), ref)
}

// tokenRef ist der Verweis auf eine Token-Datei, wie add ihn schreibt: ~/…,
// wenn sie unter dem Heimatverzeichnis liegt — so gilt derselbe Eintrag in
// einem Container, der die Tokens am selben Ort einbindet —, sonst absolut.
func (c *opencodeClient) tokenRef(file string) string {
	if c.m.Home != "" {
		if rel, err := filepath.Rel(c.m.Home, file); err == nil && rel != "." && !strings.HasPrefix(rel, "..") &&
			!filepath.IsAbs(rel) {
			return "{file:~/" + filepath.ToSlash(rel) + "}"
		}
	}
	return "{file:" + file + "}"
}

// headers sind die Header des gewünschten Eintrags.
func (c *opencodeClient) headers(want entry) map[string]string {
	h := map[string]string{}
	for _, l := range want.logins {
		h[AccountHeaderPrefix+l.Hub] = l.Account
		h[TokenHeaderPrefix+l.Hub] = c.tokenRef(l.File)
	}
	return h
}

func (c *opencodeClient) read(_ context.Context) (found, error) {
	path, both := c.configPaths()
	data, exists, err := readFile(path)
	if err != nil {
		return found{}, err
	}
	of := opencodeFound{path: path, data: data}
	f := found{raw: of}
	if both {
		f.warnings = append(f.warnings, fmt.Sprintf("neben %s liegt eine zweite config (opencode.json und opencode.jsonc); "+
			"OpenCode führt beide zusammen — eine davon auflösen", path))
	}
	if !exists {
		return f, nil
	}
	// Standardize schreibt in den Puffer, den es bekommt: eine Kopie, damit
	// data mit Kommentaren bleibt, wie es war (remove schreibt es zurück).
	std, err := hujson.Standardize(bytes.Clone(data))
	if err != nil {
		return found{}, fmt.Errorf("%s: kein gültiges JSONC: %w", path, err)
	}
	var file struct {
		MCP map[string]json.RawMessage `json:"mcp"`
	}
	if err := json.Unmarshal(std, &file); err != nil {
		return found{}, fmt.Errorf("%s: %w", path, err)
	}
	raw, ok := file.MCP[EntryName]
	if !ok {
		return f, nil
	}
	f.exists = true
	var entryMap map[string]any
	if json.Unmarshal(raw, &entryMap) == nil {
		of.entry = entryMap
	}
	f.choice = Choice{}
	headers, _ := of.entry["headers"].(map[string]any)
	for _, k := range sortedKeys(headers) {
		v, _ := headers[k].(string)
		lower := strings.ToLower(k)
		if hub, ok := strings.CutPrefix(lower, strings.ToLower(AccountHeaderPrefix)); ok &&
			ident.CheckName("Hub", hub) == nil && ident.CheckPrincipalName("Account", v) == nil {
			f.choice[hub] = v
		}
		if m := fileRef.FindStringSubmatch(v); m != nil {
			if fi, err := os.Stat(c.resolveRef(m[1], path)); err != nil || fi.IsDir() {
				of.dead = append(of.dead, m[1])
			}
		}
	}
	for _, d := range of.dead {
		f.warnings = append(f.warnings, fmt.Sprintf("der Eintrag verweist auf eine fehlende Datei (%s) — so ist die ganze "+
			"config ungültig und OpenCode startet nicht; kephalaion node mcp add bereinigt ihn", d))
	}
	f.raw = of
	return f, nil
}

func (c *opencodeClient) compare(f found, want entry) string {
	of := f.raw.(opencodeFound)
	e := of.entry
	if e == nil {
		return "fremder Inhalt (kein Objekt)"
	}
	if t, _ := e["type"].(string); t != "remote" {
		return "fremder Inhalt (kein Eintrag vom Typ remote)"
	}
	var extra []string
	for k := range e {
		if k != "type" && k != "url" && k != "headers" {
			extra = append(extra, k)
		}
	}
	if len(extra) > 0 {
		sort.Strings(extra)
		return "fremder Inhalt (" + strings.Join(extra, ", ") + ")"
	}
	if u, _ := e["url"].(string); u != want.URL {
		return fmt.Sprintf("andere Adresse (%s statt %s)", u, want.URL)
	}
	if len(of.dead) > 0 {
		return "Verweis auf eine fehlende Token-Datei"
	}
	have, _ := e["headers"].(map[string]any)
	wantHeaders := c.headers(want)
	if len(have) != len(wantHeaders) {
		return "andere Header"
	}
	for k, v := range wantHeaders {
		if got, _ := have[k].(string); got != v {
			return "andere Header"
		}
	}
	return ""
}

// version liest die Fassung von opencode --version: drei Zahlen.
func (c *opencodeClient) version(ctx context.Context) ([3]int, string, error) {
	out, err := c.m.run(ctx, c.bin, "--version")
	if err != nil {
		return [3]int{}, "", err
	}
	text := strings.TrimSpace(out)
	m := regexp.MustCompile(`(\d+)\.(\d+)\.(\d+)`).FindStringSubmatch(text)
	if m == nil {
		return [3]int{}, text, fmt.Errorf("opencode --version: unbekannte Fassung %q", text)
	}
	var v [3]int
	for i := range v {
		v[i], _ = strconv.Atoi(m[i+1])
	}
	return v, m[0], nil
}

func versionLess(a, b [3]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

func (c *opencodeClient) write(ctx context.Context, f found, want entry) error {
	v, text, err := c.version(ctx)
	if err != nil {
		return err
	}
	if versionLess(v, opencodeMinVersion) {
		return fmt.Errorf("OpenCode %s fragt bei mcp add nach; nötig ist 1.17.0 oder neuer (opencode upgrade)", text)
	}
	of := f.raw.(opencodeFound)
	// Ein toter Verweis macht die config ungültig, und opencode mcp add
	// scheiterte daran: den Eintrag zuerst selbst entfernen.
	if len(of.dead) > 0 {
		if err := c.remove(ctx, f); err != nil {
			return err
		}
	}
	args := []string{"mcp", "add", EntryName, "--url", want.URL}
	headers := c.headers(want)
	for _, k := range sortedKeys(headers) {
		args = append(args, "--header", k+"="+headers[k])
	}
	if _, err := c.m.run(ctx, c.bin, args...); err != nil {
		return err
	}
	// Nachsehen, ob der Eintrag so dasteht, wie er soll.
	after, err := c.read(ctx)
	if err != nil {
		return err
	}
	if !after.exists {
		return errors.New("opencode mcp add meldete Erfolg, aber der Eintrag fehlt")
	}
	if diff := c.compare(after, want); diff != "" {
		return fmt.Errorf("opencode mcp add schrieb einen anderen Eintrag: %s", diff)
	}
	return nil
}

func (c *opencodeClient) remove(_ context.Context, f found) error {
	of := f.raw.(opencodeFound)
	out, err := opencodeRemove(of.data)
	if err != nil {
		return fmt.Errorf("%s: %w", of.path, err)
	}
	return writeFile(of.path, out)
}

// opencodeRemove entfernt den Schlüssel mcp.kephalaion aus einer config im
// JWCC-Format; alles andere bleibt, auch Kommentare und Kommas am Ende.
func opencodeRemove(data []byte) ([]byte, error) {
	v, err := hujson.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("kein gültiges JSONC: %w", err)
	}
	if v.Find("/mcp/"+EntryName) == nil {
		return data, nil
	}
	// Steht der Eintrag zuletzt und folgt ihm ein Komma, trug das Objekt ein
	// Komma am Ende (opencode mcp add hängt ihn davor an); Patch nähme es mit
	// weg — danach setzt es der neue letzte Eintrag wieder.
	trailing := false
	if obj, ok := v.Find("/mcp").Value.(*hujson.Object); ok && len(obj.Members) > 0 {
		last := obj.Members[len(obj.Members)-1]
		trailing = last.Name.Value.(hujson.Literal).String() == EntryName && last.Value.AfterExtra != nil
	}
	if err := v.Patch([]byte(`[{"op":"remove","path":"/mcp/` + EntryName + `"}]`)); err != nil {
		return nil, err
	}
	if obj, ok := v.Find("/mcp").Value.(*hujson.Object); ok && trailing && len(obj.Members) > 0 {
		if last := &obj.Members[len(obj.Members)-1].Value; last.AfterExtra == nil {
			last.AfterExtra = hujson.Extra{}
		}
	}
	return v.Pack(), nil
}
