package assistant

import (
	"os"
	"path/filepath"
	"strings"
)

// VSCodeExtension ist die Kennung der Erweiterung für VS Code (publisher.name
// aus vscode/package.json). Bei VS Code trägt node mcp nichts ein: Die
// Erweiterung meldet den Node selbst als MCP-Server.
const VSCodeExtension = "kascada.kephalaion"

// vscodeExtensionDirs sind die Verzeichnisse unter dem Heimatverzeichnis, in
// denen VS Code Erweiterungen ablegt: lokal, als Server einer Remote-Sitzung
// (WSL, SSH, Devcontainer) und bei den Insiders-Fassungen.
var vscodeExtensionDirs = []string{
	".vscode/extensions", ".vscode-server/extensions", ".vscode-remote/extensions",
	".vscode-insiders/extensions", ".vscode-server-insiders/extensions",
}

// vscodeDetail sagt, ob die Erweiterung installiert ist, soweit sich das an
// den Verzeichnissen von VS Code ablesen lässt.
func (m *Manager) vscodeDetail() string {
	for _, rel := range vscodeExtensionDirs {
		dir := filepath.Join(m.Home, filepath.FromSlash(rel))
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() && strings.HasPrefix(e.Name(), VSCodeExtension+"-") {
				return "Erweiterung installiert (" + filepath.Join(dir, e.Name()) + ")"
			}
		}
	}
	return "Erweiterung " + VSCodeExtension + " nicht gefunden"
}
