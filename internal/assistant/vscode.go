package assistant

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// VSCodeExtension ist die Kennung der Erweiterung für VS Code (publisher.name
// aus vscode/package.json). Bei VS Code trägt node mcp nichts ein: Die
// Erweiterung meldet den Node selbst als MCP-Server.
const VSCodeExtension = "kascada.kephalaion"

// vscodeMCPSince ist die erste Fassung der Erweiterung, die den Node als
// MCP-Server meldet.
var vscodeMCPSince = [3]int{0, 0, 6}

// vscodeExtensionDirs sind die Verzeichnisse unter dem Heimatverzeichnis, in
// denen VS Code Erweiterungen ablegt: lokal, als Server einer Remote-Sitzung
// (WSL, SSH, Devcontainer) und bei den Insiders-Fassungen.
var vscodeExtensionDirs = []string{
	".vscode/extensions", ".vscode-server/extensions", ".vscode-remote/extensions",
	".vscode-insiders/extensions", ".vscode-server-insiders/extensions",
}

// parseVersion liest x.y.z; ok ist false bei allem anderen.
func parseVersion(s string) (v [3]int, ok bool) {
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

// vscodeDetail sagt, ob die Erweiterung installiert ist und ob ihre Fassung
// den MCP-Server schon meldet, soweit sich das an den Verzeichnissen von
// VS Code ablesen lässt (die neueste Fassung zählt).
func (m *Manager) vscodeDetail() string {
	var best [3]int
	bestDir, bestText := "", ""
	for _, rel := range vscodeExtensionDirs {
		dir := filepath.Join(m.Home, filepath.FromSlash(rel))
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			text, ok := strings.CutPrefix(e.Name(), VSCodeExtension+"-")
			if !e.IsDir() || !ok {
				continue
			}
			v, ok := parseVersion(text)
			if bestDir == "" || ok && versionLess(best, v) {
				best, bestDir, bestText = v, filepath.Join(dir, e.Name()), text
			}
		}
	}
	switch {
	case bestDir == "":
		return "Erweiterung " + VSCodeExtension + " nicht gefunden"
	case versionLess(best, vscodeMCPSince):
		return "Erweiterung " + bestText + " installiert (" + bestDir + ") — sie meldet den MCP-Server erst ab 0.0.6"
	}
	return "Erweiterung installiert (" + bestDir + ")"
}
