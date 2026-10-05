package vscodeext

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Editor ist ein Editor der Familie VS Code: sein Name, sein CLI und die
// Verzeichnisse unter dem Heimatverzeichnis, in denen er Erweiterungen
// ablegt — lokal und als Server einer Remote-Sitzung (WSL, SSH,
// Devcontainer). Die Namen folgen dataFolderName bzw. serverDataFolderName
// aus der product.json des Editors (Befund vscode-betrieb, 2026-10-05).
type Editor struct {
	Name string
	CLI  string
	Dirs []string
}

// Editors sind die unterstützten Editoren; ihre CLIs in dieser Reihenfolge
// sind die, die vscode install --code annimmt.
var Editors = []Editor{
	{Name: "VS Code", CLI: "code", Dirs: []string{
		".vscode/extensions", ".vscode-server/extensions", ".vscode-remote/extensions",
	}},
	{Name: "VS Code Insiders", CLI: "code-insiders", Dirs: []string{
		".vscode-insiders/extensions", ".vscode-server-insiders/extensions",
	}},
	{Name: "Cursor", CLI: "cursor", Dirs: []string{
		".cursor/extensions", ".cursor-server/extensions",
	}},
	{Name: "VSCodium", CLI: "codium", Dirs: []string{
		".vscode-oss/extensions", ".vscodium-server/extensions",
	}},
}

// CLIs liefert die CLIs der unterstützten Editoren in der Reihenfolge von
// Editors.
func CLIs() []string {
	out := make([]string, len(Editors))
	for i, e := range Editors {
		out[i] = e.CLI
	}
	return out
}

// EditorOf liefert den Editor zu einem CLI.
func EditorOf(cli string) (Editor, bool) {
	for _, e := range Editors {
		if e.CLI == cli {
			return e, true
		}
	}
	return Editor{}, false
}

// Installation ist die Erweiterung, wie sie in einem Verzeichnis eines
// Editors installiert ist.
type Installation struct {
	// Editor und CLI des Editors, zu dem das Verzeichnis gehört.
	Editor, CLI string
	// Dir ist das Verzeichnis der Erweiterung selbst.
	Dir string
	// Version ist die installierte Fassung, wie der Editor sie führt.
	Version string
}

// Find sucht die Erweiterung in allen Verzeichnissen aller Editoren unter
// home, in der Reihenfolge von Editors. Maßgeblich ist je Verzeichnis
// extensions.json: Nach einem Downgrade mit --force liegt das Verzeichnis
// der höheren Fassung noch eine Weile daneben. Fehlt die Datei oder lässt
// sie sich nicht lesen, zählt das Verzeichnis mit der höchsten Fassung.
func Find(home string) []Installation {
	var out []Installation
	for _, ed := range Editors {
		for _, rel := range ed.Dirs {
			dir := filepath.Join(home, filepath.FromSlash(rel))
			path, version, ok := findIn(dir)
			if ok {
				out = append(out, Installation{Editor: ed.Name, CLI: ed.CLI, Dir: path, Version: version})
			}
		}
	}
	return out
}

// findIn sucht die Erweiterung in einem Erweiterungsverzeichnis.
func findIn(dir string) (path, version string, ok bool) {
	if b, err := os.ReadFile(filepath.Join(dir, "extensions.json")); err == nil {
		var list []struct {
			Identifier struct {
				ID string `json:"id"`
			} `json:"identifier"`
			Version          string `json:"version"`
			RelativeLocation string `json:"relativeLocation"`
		}
		if json.Unmarshal(b, &list) == nil {
			for _, e := range list {
				if !strings.EqualFold(e.Identifier.ID, ID) {
					continue
				}
				loc := e.RelativeLocation
				if loc == "" || strings.ContainsAny(loc, `/\`) {
					loc = ID + "-" + e.Version
				}
				return filepath.Join(dir, loc), e.Version, true
			}
			return "", "", false
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", "", false
	}
	var best [3]int
	for _, e := range entries {
		text, found := strings.CutPrefix(strings.ToLower(e.Name()), ID+"-")
		if !e.IsDir() || !found {
			continue
		}
		v, parsed := ParseVersion(text)
		if !ok || parsed && Less(best, v) {
			best, path, version, ok = v, filepath.Join(dir, e.Name()), text, true
		}
	}
	return path, version, ok
}

// ParseVersion liest x.y.z; ok ist false bei allem anderen.
func ParseVersion(s string) (v [3]int, ok bool) {
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return v, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return v, false
		}
		v[i] = n
	}
	return v, true
}

// Less sagt, ob a vor b liegt.
func Less(a, b [3]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}
