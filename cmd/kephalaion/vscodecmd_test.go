package main

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testVSIX baut eine .vsix mit dieser Version in extension/package.json.
func testVSIX(t *testing.T, version string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("extension/package.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(`{"name":"kephalaion","version":"` + version + `"}`)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// withVSIX setzt die eingebettete Erweiterung; data nil heißt: ohne
// Erweiterung gebaut.
func withVSIX(t *testing.T, data []byte) {
	t.Helper()
	old := embeddedVSIX
	embeddedVSIX = func() ([]byte, bool) { return data, data != nil }
	t.Cleanup(func() { embeddedVSIX = old })
}

// withSudo setzt, ob der Aufruf über sudo läuft.
func withSudo(t *testing.T, sudo bool) {
	t.Helper()
	old := viaSudo
	viaSudo = func() bool { return sudo }
	t.Cleanup(func() { viaSudo = old })
}

// fakeEditors legt gefälschte Editor-CLIs in ein eigenes Verzeichnis und
// macht es zum einzigen Eintrag im PATH — kein Test ruft das echte code. Jeder
// Aufruf schreibt eine Zeile mit Namen und Argumenten nach calls.log, dazu
// die erste Zeile der übergebenen Datei. Mit fail scheitern sie.
type fakeEditor struct {
	dir string
}

func fakeEditors(t *testing.T, fail bool, clis ...string) *fakeEditor {
	t.Helper()
	dir := t.TempDir()
	exit := "0"
	if fail {
		exit = "3"
	}
	for _, cli := range clis {
		script := "#!/bin/sh\n" +
			"printf '%s %s\\n' \"${0##*/}\" \"$*\" >> '" + filepath.Join(dir, "calls.log") + "'\n" +
			"if [ -f \"$2\" ]; then IFS= read -r line < \"$2\"; printf 'Inhalt: %s\\n' \"$line\" >> '" +
			filepath.Join(dir, "calls.log") + "'; fi\n" +
			"echo \"Extension was successfully installed.\"\n" +
			"exit " + exit + "\n"
		if err := os.WriteFile(filepath.Join(dir, cli), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	return &fakeEditor{dir: dir}
}

// calls liefert die Aufrufe der gefälschten CLIs.
func (f *fakeEditor) calls(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(f.dir, "calls.log"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func TestVSCodeInstall(t *testing.T) {
	isolate(t)
	withSudo(t, false)
	withVSIX(t, []byte("eingebettete-vsix\n"))
	f := fakeEditors(t, false, "code", "cursor")

	runT(t, "vscode", "install").want(t, 0, "in VS Code (code --install-extension … --force)",
		"Extension was successfully installed.", "In VS Code: „Developer: Reload Window“")
	calls := f.calls(t)
	if len(calls) != 2 || !strings.HasPrefix(calls[0], "code --install-extension ") ||
		!strings.HasSuffix(calls[0], ".vsix --force") || calls[1] != "Inhalt: eingebettete-vsix" {
		t.Fatalf("Aufrufe %q", calls)
	}
	// Die temporäre Datei ist wieder weg.
	file := strings.TrimSuffix(strings.TrimPrefix(calls[0], "code --install-extension "), " --force")
	if exists(file) {
		t.Errorf("temporäre Datei liegt noch: %s", file)
	}

	runT(t, "vscode", "install", "--code", "cursor").want(t, 0, "in Cursor (cursor --install-extension")
	if calls := f.calls(t); !strings.HasPrefix(calls[len(calls)-2], "cursor --install-extension ") {
		t.Errorf("Aufrufe %q", calls)
	}
}

// Ohne --code: code, wenn es im PATH ist, sonst das einzige andere; mehrere
// oder keins: Abbruch mit den gefundenen und den Wegen --code und vsix.
func TestVSCodeInstallChoice(t *testing.T) {
	isolate(t)
	withSudo(t, false)
	withVSIX(t, []byte("x\n"))

	f := fakeEditors(t, false, "cursor")
	runT(t, "vscode", "install").want(t, 0, "in Cursor (cursor ")
	if calls := f.calls(t); len(calls) != 2 || !strings.HasPrefix(calls[0], "cursor ") {
		t.Errorf("nur cursor: %q", calls)
	}

	f = fakeEditors(t, false, "cursor", "codium")
	runT(t, "vscode", "install").want(t, 1, "mehrere Editoren im PATH (cursor, codium)", "--code <cli>",
		"kephalaion vscode vsix -o")
	if calls := f.calls(t); len(calls) != 0 {
		t.Errorf("Aufrufe bei mehreren: %q", calls)
	}

	fakeEditors(t, false)
	runT(t, "vscode", "install").want(t, 1, "kein Editor im PATH (gesucht: code, code-insiders, cursor, codium)",
		"--code <cli>", "kephalaion vscode vsix -o")
	runT(t, "vscode", "install", "--code", "codium").want(t, 1, "codium ist nicht im PATH")
	runT(t, "vscode", "install", "--code", "vim").want(t, 2, `--code "vim": erlaubt sind code, code-insiders, cursor, codium`)
	runT(t, "vscode", "install", "nebenbei").want(t, 2, "Unerwartetes Argument: nebenbei")
}

func TestVSCodeInstallFails(t *testing.T) {
	isolate(t)
	withSudo(t, false)
	withVSIX(t, []byte("x\n"))
	fakeEditors(t, true, "code")
	r := runT(t, "vscode", "install")
	r.want(t, 1, "vscode install: code --install-extension ist gescheitert: exit status 3")
	if strings.Contains(r.out, "Reload Window") {
		t.Errorf("Hinweis trotz Fehler:\n%s", r.out)
	}
}

// Über sudo lehnt install ab; root ohne sudo (Devcontainer) installiert.
func TestVSCodeInstallSudo(t *testing.T) {
	isolate(t)
	withVSIX(t, []byte("x\n"))
	f := fakeEditors(t, false, "code")
	withSudo(t, true)
	runT(t, "vscode", "install").want(t, 1, "nicht über sudo", "Als eigener User aufrufen: kephalaion vscode install")
	if calls := f.calls(t); len(calls) != 0 {
		t.Errorf("Aufrufe über sudo: %q", calls)
	}
}

// Ein Binary ohne Erweiterung sagt das bei install, vsix, status und version.
func TestVSCodeWithoutExtension(t *testing.T) {
	dir := isolate(t)
	withSudo(t, false)
	withVSIX(t, nil)
	f := fakeEditors(t, false, "code")
	runT(t, "vscode", "install").want(t, 1, "ohne Erweiterung für VS Code gebaut")
	out := filepath.Join(dir, "k.vsix")
	runT(t, "vscode", "vsix", "-o", out).want(t, 1, "ohne Erweiterung für VS Code gebaut")
	if exists(out) {
		t.Error("vsix hat ohne Erweiterung geschrieben")
	}
	runT(t, "vscode", "status").want(t, 0, "Eingebettet: keine — dieses Binary ist ohne Erweiterung")
	runT(t, "version").want(t, 0, "VS Code:   Erweiterung keine — dieses Binary ist ohne Erweiterung")
	if calls := f.calls(t); len(calls) != 0 {
		t.Errorf("Aufrufe: %q", calls)
	}
}

func TestVSCodeVSIXAndStatus(t *testing.T) {
	dir := isolate(t)
	data := testVSIX(t, "0.3.0")
	withVSIX(t, data)

	out := filepath.Join(dir, "k.vsix")
	runT(t, "vscode", "vsix", "-o", out).want(t, 0, "Geschrieben: "+out)
	if b, err := os.ReadFile(out); err != nil || !bytes.Equal(b, data) {
		t.Errorf("Datei %v", err)
	}
	runT(t, "vscode", "vsix").want(t, 2, "-o <datei> fehlt")
	runT(t, "version").want(t, 0, "VS Code:   Erweiterung 0.3.0")

	runT(t, "vscode", "status").want(t, 0, "Eingebettet: 0.3.0", "Installiert: in keinem Editor gefunden")
	cursor := filepath.Join(dir, ".cursor-server", "extensions", "kascada.kephalaion-0.2.0")
	code := filepath.Join(dir, ".vscode-server", "extensions", "kascada.kephalaion-0.0.0")
	for _, d := range []string{cursor, code} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	runT(t, "vscode", "status").want(t, 0, "Eingebettet: 0.3.0", "Installiert:",
		"  VS Code (code): 0.0.0 — "+code, "  Cursor (cursor): 0.2.0 — "+cursor)

	runT(t, "vscode").want(t, 2, "kephalaion vscode install [--code <cli>]")
	runT(t, "vscode", "--help").want(t, 0, "Exit-Code:")
	runT(t, "vscode", "gibtsnicht").want(t, 2, "Unbekanntes Kommando: vscode gibtsnicht")
	runT(t, "help").want(t, 0, "vscode      installiert die eingebettete Erweiterung")
}
