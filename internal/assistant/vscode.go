package assistant

import (
	"github.com/kephalaion/kephalaion/internal/vscodeext"
)

// VSCodeExtension ist die Kennung der Erweiterung für VS Code (publisher.name
// aus vscode/package.json). Bei VS Code trägt node mcp nichts ein: Die
// Erweiterung meldet den Node selbst als MCP-Server.
const VSCodeExtension = vscodeext.ID

// vscodeMCPSince ist die erste Fassung der Erweiterung, die den Node als
// MCP-Server meldet.
var vscodeMCPSince = [3]int{0, 0, 6}

// vscodeDetail sagt, ob die Erweiterung installiert ist und ob ihre Fassung
// den MCP-Server schon meldet, soweit sich das an den Verzeichnissen der
// Editoren ablesen lässt (vscodeext.Find; die neueste Fassung zählt). 0.0.0
// ist ein dev build und gilt nie als zu alt.
func (m *Manager) vscodeDetail() string {
	var best [3]int
	var found *vscodeext.Installation
	for _, in := range vscodeext.Find(m.Home) {
		v, ok := vscodeext.ParseVersion(in.Version)
		if found == nil || ok && vscodeext.Less(best, v) {
			best, found = v, &in
		}
	}
	switch {
	case found == nil:
		return "Erweiterung " + VSCodeExtension + " nicht gefunden"
	case found.Version == vscodeext.DevVersion:
		return "Erweiterung installiert (" + found.Dir + ", dev build)"
	case vscodeext.Less(best, vscodeMCPSince):
		return "Erweiterung " + found.Version + " installiert (" + found.Dir + ") — sie meldet den MCP-Server erst ab 0.0.6"
	}
	return "Erweiterung installiert (" + found.Dir + ")"
}
