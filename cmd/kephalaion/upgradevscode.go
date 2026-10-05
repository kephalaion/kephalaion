package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/kephalaion/kephalaion/internal/config"
	"github.com/kephalaion/kephalaion/internal/vscodeext"
)

// systemBinary ist der Ort des Binarys der globalen Installation; Tests
// lenken ihn um.
var systemBinary = config.SystemBinary

// upgradeExtension erneuert nach einem Upgrade die Erweiterung für VS Code
// mit dem neuen Binary exe — die neue Erweiterung steckt in ihm, nicht in
// diesem Prozess. Je CLI eines Editors, in dem sie installiert ist, einmal
// exe vscode install --code <cli>. Alles, was dabei scheitert, ist nur eine
// Warnung: Der Exit-Code des Upgrades bleibt, wie er ist.
func upgradeExtension(ctx context.Context, exe string, stdout, stderr io.Writer) {
	const manual = "kephalaion vscode install"
	if viaSudo() || sameFile(exe, systemBinary) {
		fmt.Fprintf(stdout, "Erweiterung für VS Code: nicht erneuert (globale Installation oder sudo) — "+
			"als eigener User: %s\n", manual)
		return
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	var clis []string
	seen := map[string]bool{}
	for _, in := range vscodeext.Find(home) {
		if !seen[in.CLI] {
			seen[in.CLI] = true
			clis = append(clis, in.CLI)
		}
	}
	for _, cli := range clis {
		ed, _ := vscodeext.EditorOf(cli)
		cmdline := manual + " --code " + cli
		if _, err := exec.LookPath(cli); err != nil {
			fmt.Fprintf(stdout, "Erweiterung für %s: %s nicht im PATH, nicht erneuert — von Hand: %s\n", ed.Name, cli, cmdline)
			continue
		}
		var errBuf bytes.Buffer
		cmd := exec.CommandContext(ctx, exe, "vscode", "install", "--code", cli)
		cmd.Stdout, cmd.Stderr = stdout, &errBuf
		err := cmd.Run()
		if err == nil {
			_, _ = stderr.Write(errBuf.Bytes())
			continue
		}
		cause := failureCause(errBuf.String(), err)
		fmt.Fprintf(stderr, "upgrade: Warnung: Die Erweiterung für %s ist nicht erneuert (%s). Von Hand: %s\n",
			ed.Name, cause, cmdline)
		if strings.Contains(cause, withoutExtension) {
			return // gilt für jeden Editor
		}
	}
}

// failureCause nimmt aus der Fehlerausgabe von vscode install die Meldung des
// Kommandos selbst, sonst ihre erste Zeile, sonst den Fehler des Aufrufs.
func failureCause(out string, err error) string {
	first := ""
	lines := strings.Split(out, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if msg, ok := strings.CutPrefix(line, "vscode install: "); ok {
			return strings.TrimSuffix(msg, ".")
		}
		if line != "" {
			first = line
		}
	}
	if first != "" {
		return first
	}
	return err.Error()
}

// sameFile sagt, ob a und b dieselbe Datei sind, Links aufgelöst.
func sameFile(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && ra == rb
}
