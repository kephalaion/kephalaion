package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/kephalaion/kephalaion/internal/vscodeext"
)

// embeddedVSIX liefert die eingebettete Erweiterung; Tests setzen eigene
// Bytes oder keine, nie den tatsächlichen Inhalt des Embeds.
var embeddedVSIX = vscodeext.VSIX

// viaSudo sagt, ob dieser Aufruf als root über sudo läuft. root ohne sudo
// (etwa im Devcontainer) ist der User, der VS Code benutzt. Tests setzen es.
var viaSudo = func() bool {
	return os.Geteuid() == 0 && os.Getenv("SUDO_USER") != ""
}

// cliTimeout begrenzt einen Aufruf von code --install-extension.
const cliTimeout = 5 * time.Minute

// withoutExtension ist die Meldung eines Binarys ohne Erweiterung.
const withoutExtension = "dieses Binary ist ohne Erweiterung für VS Code gebaut (make ohne Node.js oder go build)"

const vscodeUsage = `Aufruf:
  kephalaion vscode install [--code <cli>]
  kephalaion vscode vsix -o <datei>
  kephalaion vscode status

Die Erweiterung für VS Code steckt in diesem Binary und trägt seine Version
(Tag vX.Y.Z bzw. vX.Y.Z-… → X.Y.Z, ein dev build 0.0.0). kephalaion upgrade
installiert sie mit dem neuen Binary neu, wo sie installiert ist.

Kommandos:
  install   installiert sie mit <cli> --install-extension <datei> --force —
            auch über eine höhere Fassung. Aus einem Terminal der WSL, eines
            SSH-Remotes oder Devcontainers landet sie im Server des Editors
            dort, wohin sie gehört. Danach im Editor „Developer: Reload
            Window“. Als eigener User aufrufen, nicht über sudo.
  vsix      schreibt nur die .vsix, etwa für „Extensions: Install from
            VSIX…“ oder einen Editor ohne CLI im PATH
  status    die eingebettete Version und die installierten Fassungen je
            Editor (VS Code, VS Code Insiders, Cursor, VSCodium)

Optionen:
  --code <cli>   mit install: das CLI des Editors — code, code-insiders,
                 cursor oder codium. Ohne: code, wenn es im PATH ist, sonst
                 das einzige der anderen; sind es mehrere oder keins, bricht
                 install ab und nennt die gefundenen
  -o <datei>     mit vsix: die Datei, die geschrieben wird

Exit-Code:
  0   erledigt
  1   gescheitert — ohne Erweiterung gebaut, kein eindeutiges CLI, das CLI
      meldet einen Fehler, über sudo aufgerufen
  2   falscher Aufruf
`

func runVSCode(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, vscodeUsage)
		return 2
	}
	switch args[0] {
	case "-h", "--help", "help":
		fmt.Fprint(stdout, vscodeUsage)
		return 0
	case "install":
		return runVSCodeInstall(args[1:], stdout, stderr)
	case "vsix":
		return runVSCodeVSIX(args[1:], stdout, stderr)
	case "status":
		return runVSCodeStatus(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "Unbekanntes Kommando: vscode %s\n\n", args[0])
		fmt.Fprint(stderr, vscodeUsage)
		return 2
	}
}

// parseVSCodeFlags liest die Optionen eines Unterkommandos; done ist true,
// wenn der Aufruf damit erledigt ist (Hilfe oder Fehler).
func parseVSCodeFlags(fs *flag.FlagSet, args []string, stderr io.Writer) (code int, done bool) {
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, vscodeUsage) }
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0, true
		}
		return 2, true
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "Unerwartetes Argument: %s\n\n", fs.Arg(0))
		fmt.Fprint(stderr, vscodeUsage)
		return 2, true
	}
	return 0, false
}

func runVSCodeInstall(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("vscode install", flag.ContinueOnError)
	cli := fs.String("code", "", "")
	if code, done := parseVSCodeFlags(fs, args, stderr); done {
		return code
	}
	if *cli != "" {
		if _, ok := vscodeext.EditorOf(*cli); !ok {
			fmt.Fprintf(stderr, "vscode install: --code %q: erlaubt sind %s\n", *cli, strings.Join(vscodeext.CLIs(), ", "))
			return 2
		}
	}
	data, ok := embeddedVSIX()
	if !ok {
		fmt.Fprintf(stderr, "vscode install: %s.\n", withoutExtension)
		return 1
	}
	if viaSudo() {
		fmt.Fprintln(stderr, "vscode install: nicht über sudo — die Erweiterung gehört dem User, der den Editor benutzt.\n"+
			"Als eigener User aufrufen: kephalaion vscode install")
		return 1
	}
	chosen, err := chooseCLI(*cli)
	if err != nil {
		fmt.Fprintf(stderr, "vscode install: %v\n", err)
		return 1
	}
	ed, _ := vscodeext.EditorOf(chosen)
	version, err := vscodeext.Version(data)
	if err != nil {
		version = "?"
	}
	fmt.Fprintf(stdout, "Installiere die Erweiterung %s in %s (%s --install-extension … --force)\n", version, ed.Name, chosen)
	if err := installVSIX(chosen, data, stdout, stderr); err != nil {
		fmt.Fprintf(stderr, "vscode install: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "In %s: „Developer: Reload Window“, damit die neue Fassung läuft.\n", ed.Name)
	return 0
}

// chooseCLI wählt das CLI: das genannte, wenn es im PATH ist; ohne Angabe
// code, wenn es im PATH ist, sonst das einzige der anderen.
func chooseCLI(cli string) (string, error) {
	const vsixHint = "oder die Datei schreiben (kephalaion vscode vsix -o kephalaion.vsix) und im Editor " +
		"„Extensions: Install from VSIX…“"
	if cli != "" {
		if _, err := exec.LookPath(cli); err != nil {
			return "", fmt.Errorf("%s ist nicht im PATH — %s", cli, vsixHint)
		}
		return cli, nil
	}
	var found []string
	for _, c := range vscodeext.CLIs() {
		if _, err := exec.LookPath(c); err == nil {
			found = append(found, c)
		}
	}
	switch {
	case len(found) > 0 && found[0] == "code":
		return "code", nil
	case len(found) == 1:
		return found[0], nil
	case len(found) == 0:
		return "", fmt.Errorf("kein Editor im PATH (gesucht: %s) — mit --code <cli> wählen, %s",
			strings.Join(vscodeext.CLIs(), ", "), vsixHint)
	}
	return "", fmt.Errorf("mehrere Editoren im PATH (%s), aber nicht code — einen wählen mit --code <cli>, %s",
		strings.Join(found, ", "), vsixHint)
}

// installVSIX schreibt die .vsix in eine temporäre Datei, ruft
// <cli> --install-extension <datei> --force und entfernt die Datei.
func installVSIX(cli string, data []byte, stdout, stderr io.Writer) (err error) {
	f, err := os.CreateTemp("", "kephalaion-*.vsix")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), cliTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, cli, "--install-extension", f.Name(), "--force")
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("%s antwortet nicht (nach %s abgebrochen)", cli, cliTimeout)
		}
		return fmt.Errorf("%s --install-extension ist gescheitert: %v", cli, err)
	}
	return nil
}

func runVSCodeVSIX(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("vscode vsix", flag.ContinueOnError)
	out := fs.String("o", "", "")
	if code, done := parseVSCodeFlags(fs, args, stderr); done {
		return code
	}
	if *out == "" {
		fmt.Fprint(stderr, "vscode vsix: -o <datei> fehlt\n\n")
		fmt.Fprint(stderr, vscodeUsage)
		return 2
	}
	data, ok := embeddedVSIX()
	if !ok {
		fmt.Fprintf(stderr, "vscode vsix: %s.\n", withoutExtension)
		return 1
	}
	if err := os.WriteFile(*out, data, 0o644); err != nil {
		fmt.Fprintf(stderr, "vscode vsix: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Geschrieben: %s\n", *out)
	return 0
}

func runVSCodeStatus(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("vscode status", flag.ContinueOnError)
	if code, done := parseVSCodeFlags(fs, args, stderr); done {
		return code
	}
	fmt.Fprintf(stdout, "Eingebettet: %s\n", embeddedText())
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(stderr, "vscode status: %v\n", err)
		return 1
	}
	found := vscodeext.Find(home)
	if len(found) == 0 {
		fmt.Fprintf(stdout, "Installiert: in keinem Editor gefunden (%s)\n", vscodeext.ID)
		return 0
	}
	fmt.Fprintln(stdout, "Installiert:")
	for _, in := range found {
		fmt.Fprintf(stdout, "  %s (%s): %s — %s\n", in.Editor, in.CLI, in.Version, in.Dir)
	}
	return 0
}

// embeddedText beschreibt die eingebettete Erweiterung in einer Zeile.
func embeddedText() string {
	data, ok := embeddedVSIX()
	if !ok {
		return "keine — " + withoutExtension
	}
	v, err := vscodeext.Version(data)
	if err != nil {
		return "unlesbar — " + err.Error()
	}
	return v
}
