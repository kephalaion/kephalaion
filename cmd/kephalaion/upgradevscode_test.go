package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newBinaryScript ist ein „neues Binary“ aus dem nachgespielten Release: Es
// schreibt seine Argumente nach log, gibt msg auf stderr aus und endet mit
// exit.
func newBinaryScript(log, msg string, exit int) []byte {
	return []byte("#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> '" + log + "'\n" +
		"echo 'Installiere die Erweiterung 0.2.0'\n" +
		"[ -z '" + msg + "' ] || echo '" + msg + "' >&2\n" +
		"exit " + string(rune('0'+exit)) + "\n")
}

// installExt legt die Erweiterung in die Verzeichnisse rels unter home.
func installExt(t *testing.T, home string, rels ...string) {
	t.Helper()
	for _, rel := range rels {
		if err := os.MkdirAll(filepath.Join(home, rel, "extensions", "kascada.kephalaion-0.1.0"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

// readLines liest die Zeilen einer Datei; fehlt sie, keine.
func readLines(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

// Nur Cursor hat die Erweiterung: upgrade ruft das neue Binary mit --code
// cursor, nie mit code — auch wenn code im PATH ist.
func TestUpgradeExtensionCursorOnly(t *testing.T) {
	dir := isolate(t)
	withSudo(t, false)
	fakeEditors(t, false, "code", "cursor")
	installExt(t, dir, ".cursor", ".cursor-server")
	log := filepath.Join(dir, "neu.log")
	fakeReleasesWith(t, "v0.1.0", newBinaryScript(log, "", 0))

	r := runT(t, "upgrade")
	r.want(t, 0, "Aktualisiert: v0.1.0 → v0.2.0", "Installiere die Erweiterung 0.2.0")
	if got := readLines(t, log); len(got) != 1 || got[0] != "vscode install --code cursor" {
		t.Errorf("Aufrufe des neuen Binarys: %q", got)
	}
	if strings.Contains(r.errOut, "Warnung") {
		t.Errorf("Warnung:\n%s", r.errOut)
	}
}

// Je CLI einmal; fehlt ein CLI, ein Hinweis; scheitert der Aufruf, eine
// Warnung — der Exit-Code bleibt der des Upgrades.
func TestUpgradeExtensionFailures(t *testing.T) {
	dir := isolate(t)
	withSudo(t, false)
	fakeEditors(t, false, "code")
	installExt(t, dir, ".vscode", ".vscode-server", ".vscode-oss")
	log := filepath.Join(dir, "neu.log")
	fakeReleasesWith(t, "v0.1.0", newBinaryScript(log,
		"vscode install: code --install-extension ist gescheitert: exit status 3", 1))

	r := runT(t, "upgrade")
	r.want(t, 0, "Aktualisiert: v0.1.0 → v0.2.0",
		"Erweiterung für VSCodium: codium nicht im PATH, nicht erneuert — von Hand: kephalaion vscode install --code codium",
		"upgrade: Warnung: Die Erweiterung für VS Code ist nicht erneuert (code --install-extension ist gescheitert: exit status 3). "+
			"Von Hand: kephalaion vscode install --code code")
	if got := readLines(t, log); len(got) != 1 || got[0] != "vscode install --code code" {
		t.Errorf("Aufrufe des neuen Binarys: %q", got)
	}
}

// Das neue Binary ist ohne Erweiterung gebaut: eine Warnung, nicht je Editor.
func TestUpgradeExtensionNewBinaryWithout(t *testing.T) {
	dir := isolate(t)
	withSudo(t, false)
	fakeEditors(t, false, "code", "cursor")
	installExt(t, dir, ".vscode", ".cursor")
	log := filepath.Join(dir, "neu.log")
	fakeReleasesWith(t, "v0.1.0", newBinaryScript(log, "vscode install: "+withoutExtension+".", 1))

	r := runT(t, "upgrade")
	r.want(t, 0, "Die Erweiterung für VS Code ist nicht erneuert ("+withoutExtension+")")
	if strings.Count(r.errOut, "Warnung") != 1 {
		t.Errorf("Warnungen:\n%s", r.errOut)
	}
	if got := readLines(t, log); len(got) != 1 {
		t.Errorf("Aufrufe des neuen Binarys: %q", got)
	}
}

// Unter sudo und bei der globalen Installation: nichts installieren, nur der
// Hinweis. Ohne installierte Erweiterung: kein Aufruf, kein Wort.
func TestUpgradeExtensionSkipped(t *testing.T) {
	dir := isolate(t)
	fakeEditors(t, false, "code")
	log := filepath.Join(dir, "neu.log")
	exe, _ := fakeReleasesWith(t, "v0.1.0", newBinaryScript(log, "", 0))

	withSudo(t, false)
	r := runT(t, "upgrade")
	r.want(t, 0, "Aktualisiert")
	if strings.Contains(r.out+r.errOut, "Erweiterung") || len(readLines(t, log)) != 0 {
		t.Errorf("ohne installierte Erweiterung:\n%s%s", r.out, r.errOut)
	}

	installExt(t, dir, ".vscode")
	hint := "Erweiterung für VS Code: nicht erneuert (globale Installation oder sudo) — als eigener User: kephalaion vscode install"
	withSudo(t, true)
	if err := os.WriteFile(exe, []byte("alt"), 0o755); err != nil {
		t.Fatal(err)
	}
	runT(t, "upgrade").want(t, 0, hint)

	withSudo(t, false)
	old := systemBinary
	systemBinary = exe
	t.Cleanup(func() { systemBinary = old })
	if err := os.WriteFile(exe, []byte("alt"), 0o755); err != nil {
		t.Fatal(err)
	}
	runT(t, "upgrade").want(t, 0, hint)
	if got := readLines(t, log); len(got) != 0 {
		t.Errorf("Aufrufe des neuen Binarys: %q", got)
	}
}

// Scheitert der Neustart, kommt die Erweiterung trotzdem, und der Exit-Code
// bleibt der des Neustarts.
func TestUpgradeExtensionAfterFailedRestart(t *testing.T) {
	dir := isolate(t)
	withSudo(t, false)
	f := withSystemd(t, dir)
	f.active = true
	f.restartErr = os.ErrPermission
	fakeEditors(t, false, "code")
	installExt(t, dir, ".vscode")
	log := filepath.Join(dir, "neu.log")
	fakeReleasesWith(t, "v0.1.0", newBinaryScript(log, "", 0))

	runT(t, "upgrade").want(t, 1, "der Dienst ließ sich aber nicht neu starten", "Installiere die Erweiterung 0.2.0")
	if got := readLines(t, log); len(got) != 1 || got[0] != "vscode install --code code" {
		t.Errorf("Aufrufe des neuen Binarys: %q", got)
	}
}
