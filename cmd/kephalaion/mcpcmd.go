package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/kephalaion/kephalaion/internal/assistant"
	"github.com/kephalaion/kephalaion/internal/config"
	"github.com/kephalaion/kephalaion/internal/node/mcpnode"
)

const nodeMCPUsage = `Aufruf:
  kephalaion node mcp add     [--assistant name]… [--account <hub>=<account>]… [--dry-run] [--auto]
  kephalaion node mcp remove  [--assistant name]…
  kephalaion node mcp status  [--assistant name]…
  kephalaion node mcp headers [--tokens-dir pfad] [--account <hub>=<account>]…

Meldet den Node bei den KI-Assistenten des Users als MCP-Server an: ein
Eintrag kephalaion je Assistent auf User-Ebene, für alle Hubs — die Adresse
aus listen im Abschnitt node: der config, je Hub das Header-Paar aus den
Token-Dateien. Das Token steht in keiner Konfiguration eines Assistenten.

Assistenten (--assistant, gefunden über PATH):
  claude    Claude Code: claude mcp add-json … --scope user; die Header holt
            Claude Code bei jeder Verbindung über den Helfer (headersHelper)
  opencode  OpenCode: opencode mcp add … (ab 1.17.0) in der globalen config
            (~/.config/opencode/opencode.json bzw. .jsonc). Dort steht nicht
            das Token, sondern je Hub ein Verweis auf die Token-Datei
            ({file:~/…}). remove entfernt nur den Schlüssel mcp.kephalaion,
            Kommentare bleiben. Fehlt eine Datei, auf die ein Verweis zeigt,
            ist die ganze config ungültig und OpenCode startet nicht: add
            nimmt nur vorhandene Token-Dateien auf, bereinigt Verweise auf
            fehlende und entfernt den Eintrag, wenn keine mehr da ist;
            status warnt
  codex     Codex CLI: die Tabelle [mcp_servers.kephalaion] in
            $CODEX_HOME/config.toml (sonst ~/.codex/config.toml), nur sie; der
            Rest der Datei bleibt, auch Kommentare. Die Header holt Codex je
            Verbindung über den Helfer (http_headers_helper)
  vscode    VS Code mit Copilot: Hier trägt node mcp nichts ein — den Node
            meldet die Erweiterung für VS Code

Kommandos:
  add      trägt den Node bei jedem gefundenen (oder genannten) Assistenten
           ein. Ein richtiger Eintrag bleibt unverändert, ein Eintrag
           kephalaion mit anderem Inhalt wird ersetzt. Ein nicht gefundener
           Assistent wird übergangen und genannt. Ohne Token-Datei wird
           nirgends neu eingetragen. Wirksam wird der Eintrag in einer neuen
           Sitzung des Assistenten.
  remove   entfernt den Eintrag kephalaion, nur ihn.
  status   zeigt je Assistent, ob der Node eingetragen ist, fehlt oder
           abweicht (ein Eintrag, den add ändern würde: andere Adresse,
           anderer Helfer, fremder Inhalt); bei vscode „über die Erweiterung“.
  headers  gibt die Header-Paare aller Hubs mit Token-Datei als ein
           JSON-Objekt aus: {"X-Keph-Account-<alias>":"…",
           "X-Keph-Token-<alias>":"…"}, ohne Token-Datei {}. Das ist der
           Helfer, den Claude Code und Codex bei jeder Verbindung aufrufen
           — für Assistenten, nicht für Menschen: Es ist die einzige Ausgabe
           mit Token. Im Terminal bricht headers deshalb ab. Außer im
           Terminal und bei falschem Aufruf endet es immer mit Exit 0 und
           gültigem JSON: Ein Hub ohne eindeutiges, lesbares Token (mehrere
           Accounts ohne Wahl, die gewählte Datei fehlt oder ist unlesbar)
           fehlt im Objekt, mit einer Meldung ohne Token auf stderr. Der
           Assistent verbindet dann ohne Anmeldung für diesen Hub; das
           Werkzeug whoami zeigt es.

           Die Shell eines KI-Agenten ist kein Terminal: Dort gibt headers
           die Tokens aus, und sie stünden im Kontext der KI. Ein Agent ruft
           headers deshalb nie gegen echte Token-Dateien so auf, dass die
           Ausgabe bei ihm ankommt — höchstens die Schlüssel:
             kephalaion node mcp headers | jq -r 'keys[]'

Accounts: Token-Dateien liegen unter <tokens-dir>/<hub>/<account>.token (eine
Zeile, 0600; .pending wird übergangen), wie bei kephalaion node dir. Je Hub
gilt die einzige Token-Datei. Liegen mehrere da, wählt --account
<hub>=<account>; add schreibt die Wahl für jeden Hub ausdrücklich in den
Eintrag. Ohne --account bleibt die Wahl aus dem Eintrag des Assistenten,
sonst gilt die der anderen Einträge; ein gewählter Account ohne Token-Datei
gilt als keine Wahl. Ein Hub mit mehreren Accounts ohne Wahl wird übergangen
und genannt, die übrigen werden eingetragen (Exit 1).

Automatischer Anstoß (--auto; ebenso nach kephalaion node account rotate und
check): ändert nur Assistenten, die schon einen Eintrag kephalaion haben.
Hat noch keiner der gefundenen einen, trägt er bei allen gefundenen ein. So
hält ein remove für einzelne Assistenten gegen ihn — nicht gegen ein add von
Hand ohne --assistant. Ohne Node in der config endet add --auto ohne Meldung
mit Exit 0; es meldet nur, was es ändert.

Optionen:
  --assistant name           beschränkt auf diesen Assistenten (claude,
                             opencode, codex, vscode); wiederholbar. Ohne
                             Angabe alle gefundenen
  --account <hub>=<account>  wählt den Account eines Hubs mit mehreren
                             Token-Dateien; wiederholbar
  --dry-run                  add: nur melden, was geschähe
  --auto                     add: automatischer Anstoß (siehe oben)
  --tokens-dir pfad          headers: das Verzeichnis der Token-Dateien;
                             sonst tokens/ neben der config des Users
                             (~/.config/kephalaion/tokens)
  --config pfad              add, status: Ort der config (siehe kephalaion
                             node init --help)

Exit-Codes:
  0   fertig; headers: JSON ausgegeben (auch {} und bei übergangenen Hubs)
  1   Fehler bei einem Assistenten oder ein übergangener Hub; headers: die
      Standardausgabe ist ein Terminal
  2   falscher Aufruf
`

// stdoutIsTerminal sagt, ob w ein Terminal ist; Tests ersetzen es.
var stdoutIsTerminal = func(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && isTerminal(f.Fd())
}

// newAssistantManager liefert den Manager, der bei den Assistenten einträgt;
// Tests ersetzen ihn, damit keiner einen echten Assistenten aufruft.
var newAssistantManager = assistant.New

// assistantExecutable liefert das Binary, das der Helfer nennt; Tests setzen
// es.
var assistantExecutable = os.Executable

func runNodeMCP(args []string, stdout, stderr io.Writer) int {
	u := nodeMCPUsage
	return dispatch("node mcp", u, args, stdout, stderr, map[string]func([]string) int{
		"add":     func(a []string) int { return runNodeMCPAdd(a, stdout, stderr) },
		"remove":  func(a []string) int { return runNodeMCPRemove(a, stdout, stderr) },
		"status":  func(a []string) int { return runNodeMCPStatus(a, stdout, stderr) },
		"headers": func(a []string) int { return runNodeMCPHeaders(a, stdout, stderr) },
	})
}

// errNoNode heißt: In der gefundenen config steht kein Node.
var errNoNode = errors.New("kein Node in der config")

// assistantTarget ermittelt, was eingetragen wird: die Adresse aus listen im
// Abschnitt node: der gefundenen config, dieses Binary und das Verzeichnis
// der Token-Dateien, alle absolut.
func assistantTarget(cfgFlag string) (assistant.Target, error) {
	loc, err := config.Locate(cfgFlag)
	if err != nil {
		return assistant.Target{}, err
	}
	cfg, _, err := config.Load(loc.Path)
	if err != nil {
		return assistant.Target{}, err
	}
	listen := cfg.Listen(config.Node)
	if listen == "" {
		return assistant.Target{}, fmt.Errorf("%w %s; zuerst: kephalaion node init", errNoNode, loc.Path)
	}
	exe, err := assistantExecutable()
	if err != nil {
		return assistant.Target{}, fmt.Errorf("Pfad dieses Binarys nicht ermittelbar: %w", err)
	}
	if exe, err = filepath.Abs(exe); err != nil {
		return assistant.Target{}, err
	}
	tokens, err := assistant.TokensDir()
	if err != nil {
		return assistant.Target{}, err
	}
	return assistant.Target{URL: assistant.NodeURL(listen, mcpnode.Path), Binary: exe, TokensDir: tokens}, nil
}

// assistantFlags liest --assistant und prüft die Werte.
func assistantNames(c *command, values []string) ([]string, bool) {
	for _, v := range values {
		if err := assistant.CheckName(v); err != nil {
			fmt.Fprintf(c.stderr, "%s: %v\n", c.name, err)
			return nil, false
		}
	}
	return values, true
}

// loginList nennt die Anmeldungen eines Eintrags: „vm=alice, eigen=kp“.
func loginList(logins []assistant.Login) string {
	if len(logins) == 0 {
		return "ohne Anmeldung"
	}
	parts := make([]string, len(logins))
	for i, l := range logins {
		parts[i] = l.Hub + "=" + l.Account
	}
	return strings.Join(parts, ", ")
}

func runNodeMCPAdd(args []string, stdout, stderr io.Writer) int {
	c := newCommand("node mcp add", nodeMCPUsage, stdout, stderr)
	var assistants, accounts stringList
	c.fs.Var(&assistants, "assistant", "")
	c.fs.Var(&accounts, "account", "")
	dryRun := c.fs.Bool("dry-run", false, "")
	auto := c.fs.Bool("auto", false, "")
	if _, code, ok := c.parse(args); !ok {
		return code
	}
	names, ok := assistantNames(c, assistants)
	if !ok {
		return 2
	}
	choice, err := assistant.ParseChoice(accounts)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", c.name, err)
		return 2
	}
	target, err := assistantTarget(*c.cfg)
	if errors.Is(err, errNoNode) && *auto {
		return 0
	}
	if err != nil {
		return c.fail(err)
	}
	opts := assistant.AddOptions{Assistants: names, Choice: choice, DryRun: *dryRun, Auto: *auto}
	return registerAssistants(context.Background(), c.name, target, opts, stdout, stderr)
}

// registerAssistants trägt ein und meldet das Ergebnis; der Exit-Code ist 1
// bei einem Fehler oder einem übergangenen Hub. Ein automatischer Anstoß
// meldet nur, was er ändert.
func registerAssistants(ctx context.Context, name string, target assistant.Target, opts assistant.AddOptions,
	stdout, stderr io.Writer) int {
	rep, err := newAssistantManager().Add(ctx, target, opts)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", name, err)
		return 1
	}
	if opts.DryRun {
		fmt.Fprintln(stdout, "dry-run, nichts wird geschrieben:")
	}
	changed := false
	for _, r := range rep.Results {
		switch r.Outcome {
		case assistant.Failed:
			fmt.Fprintf(stderr, "%s: %s: Fehler: %v\n", name, r.Assistant, r.Err)
		case assistant.Registered:
			changed = true
			how := ""
			if r.Replaced {
				how = ", ersetzt den Eintrag (" + r.Detail + ")"
			}
			fmt.Fprintf(stdout, "%s: eingetragen%s: %s, %s\n", r.Assistant, how, target.URL, loginList(r.Logins))
		case assistant.Removed:
			changed = true
			fmt.Fprintf(stdout, "%s: entfernt (%s)\n", r.Assistant, r.Detail)
		case assistant.Unchanged:
			if !opts.Auto {
				fmt.Fprintf(stdout, "%s: unverändert: %s, %s\n", r.Assistant, target.URL, loginList(r.Logins))
			}
		case assistant.Skipped:
			if !opts.Auto {
				fmt.Fprintf(stdout, "%s: übergangen: %s\n", r.Assistant, r.Detail)
			}
		}
	}
	for _, n := range rep.Notes {
		fmt.Fprintf(stderr, "%s: %s\n", name, n)
	}
	for _, s := range rep.SkippedHubs {
		fmt.Fprintf(stderr, "%s: Hub %s übergangen: %s\n", name, s.Hub, s.Reason)
		if len(s.Accounts) > 0 {
			fmt.Fprintf(stderr, "  Wählen: kephalaion node mcp add --account %s=<account> [--assistant <name>] — ohne --assistant "+
				"trägt add bei allen gefundenen Assistenten ein, auch nach einem remove für einzelne.\n", s.Hub)
		}
	}
	if rep.NoLogin && !opts.Auto && len(rep.SkippedHubs) == 0 {
		fmt.Fprintf(stdout, "Keine Token-Datei unter %s: Ohne Anmeldung wird nicht neu eingetragen. Zuerst den ersten Account "+
			"einrichten — kephalaion node account rotate <hub> <account> --token-file %s; rotate trägt danach selbst ein.\n",
			target.TokensDir, assistant.TokenFile(target.TokensDir, "<hub>", "<account>"))
	}
	if changed && !opts.DryRun {
		fmt.Fprintln(stdout, "Wirksam in einer neuen Sitzung des Assistenten.")
	}
	if rep.Failed() {
		return 1
	}
	return 0
}

func runNodeMCPRemove(args []string, stdout, stderr io.Writer) int {
	c := &command{name: "node mcp remove", usage: nodeMCPUsage, stdout: stdout, stderr: stderr}
	c.fs = newFlagSet(c.name, c.usage, stderr)
	var assistants stringList
	c.fs.Var(&assistants, "assistant", "")
	if _, code, ok := c.parse(args); !ok {
		return code
	}
	names, ok := assistantNames(c, assistants)
	if !ok {
		return 2
	}
	rep := newAssistantManager().Remove(context.Background(), names, false)
	for _, r := range rep.Results {
		switch r.Outcome {
		case assistant.Failed:
			fmt.Fprintf(stderr, "%s: %s: Fehler: %v\n", c.name, r.Assistant, r.Err)
		case assistant.Removed:
			fmt.Fprintf(stdout, "%s: entfernt\n", r.Assistant)
		case assistant.Unchanged:
			fmt.Fprintf(stdout, "%s: unverändert: %s\n", r.Assistant, r.Detail)
		case assistant.Skipped:
			fmt.Fprintf(stdout, "%s: übergangen: %s\n", r.Assistant, r.Detail)
		}
	}
	if rep.Failed() {
		return 1
	}
	return 0
}

func runNodeMCPStatus(args []string, stdout, stderr io.Writer) int {
	c := newCommand("node mcp status", nodeMCPUsage, stdout, stderr)
	var assistants stringList
	c.fs.Var(&assistants, "assistant", "")
	if _, code, ok := c.parse(args); !ok {
		return code
	}
	names, ok := assistantNames(c, assistants)
	if !ok {
		return 2
	}
	target, err := assistantTarget(*c.cfg)
	if err != nil {
		return c.fail(err)
	}
	statuses, skipped, err := newAssistantManager().Status(context.Background(), target, names)
	if err != nil {
		return c.fail(err)
	}
	fmt.Fprintf(stdout, "Node: %s\n", target.URL)
	code := 0
	for _, st := range statuses {
		line := fmt.Sprintf("%s: %s", st.Assistant, st.State)
		switch st.State {
		case assistant.StateRegistered:
			line += " (" + loginList(st.Logins) + ")"
		case assistant.StateDiffers, assistant.StateExtension:
			line += " — " + st.Detail
		case assistant.StateNotFound:
			line += " (" + st.Assistant + " liegt nicht im PATH)"
		case assistant.StateUnknown:
			line += " — " + st.Err.Error()
			code = 1
		}
		fmt.Fprintln(stdout, line)
		for _, w := range st.Warnings {
			fmt.Fprintf(stdout, "  Warnung: %s\n", w)
		}
	}
	for _, s := range skipped {
		fmt.Fprintf(stdout, "Hub %s ohne Anmeldung: %s\n", s.Hub, s.Reason)
	}
	return code
}

// runNodeMCPHeaders ist der Helfer der Assistenten: die Header-Paare als JSON
// auf stdout. Es ist die einzige Ausgabe mit Token — nie im Terminal, und die
// Meldungen auf stderr nennen nie eines.
func runNodeMCPHeaders(args []string, stdout, stderr io.Writer) int {
	const name = "node mcp headers"
	fs := newFlagSet(name, nodeMCPUsage, stderr)
	tokensDir := fs.String("tokens-dir", "", "")
	var accounts stringList
	fs.Var(&accounts, "account", "")
	if _, code, ok := parseFlags(fs, args, nodeMCPUsage, 0, stderr); !ok {
		return code
	}
	choice, err := assistant.ParseChoice(accounts)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", name, err)
		return 2
	}
	if stdoutIsTerminal(stdout) {
		fmt.Fprintf(stderr, "%s: gibt Tokens aus und schreibt deshalb nicht in ein Terminal. Es ist der Helfer, "+
			"den die Assistenten aufrufen; welche Hubs er nennt, zeigt: kephalaion node mcp headers | jq -r 'keys[]'\n", name)
		return 1
	}
	headers := map[string]string{}
	dir, err := resolveTokensDir(*tokensDir)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", name, err)
	} else {
		logins, skipped, err := assistant.Logins(dir, choice)
		if err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", name, err)
		}
		var unread []assistant.SkippedHub
		headers, unread = assistant.Headers(logins)
		for _, s := range append(skipped, unread...) {
			fmt.Fprintf(stderr, "%s: Hub %s ohne Anmeldung: %s\n", name, s.Hub, s.Reason)
		}
	}
	out, err := json.Marshal(headers)
	if err != nil {
		// Eine Map aus Texten lässt sich immer schreiben; trotzdem bleibt die
		// Zusage: gültiges JSON, Exit 0.
		fmt.Fprintf(stderr, "%s: %v\n", name, err)
		out = []byte("{}")
	}
	fmt.Fprintf(stdout, "%s\n", out)
	return 0
}

// resolveTokensDir ist --tokens-dir als absoluter Pfad, ohne Angabe tokens/
// neben der config des Users.
func resolveTokensDir(flagValue string) (string, error) {
	if flagValue != "" {
		return filepath.Abs(flagValue)
	}
	return assistant.TokensDir()
}
