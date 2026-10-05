package main

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/kephalaion/kephalaion/internal/assistant"
	"github.com/kephalaion/kephalaion/internal/config"
	"github.com/kephalaion/kephalaion/internal/ident"
	"github.com/kephalaion/kephalaion/internal/node/mcpnode"
)

const nodeMCPUsage = `Aufruf:
  kephalaion node mcp add     [--assistant name]… [--account <hub>=<account>]… [--dry-run] [--auto]
                              [--node url [--ca-file pfad] [--hub alias]…]
  kephalaion node mcp remove  [--assistant name]…
  kephalaion node mcp status  [--assistant name]… [--node url [--ca-file pfad] [--hub alias]…]
  kephalaion node mcp headers [--tokens-dir pfad] [--hub alias]… [--account <hub>=<account>]…

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
            meldet die Erweiterung für VS Code; sie installiert
            kephalaion vscode install

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

Ein Node auf einem anderen Rechner (--node):
  --node <url> trägt statt der Adresse aus der config diese ein: ein Node
  hinter einem Proxy, https://<name>/<präfix> (etwa
  https://<name>/kephalaion; die Basis ohne /mcp) — auch auf einem Rechner
  ohne eigene config. http geht nur zu diesem Rechner (localhost, 127.0.0.1,
  [::1]) und zu host.docker.internal. Zuerst fragen add und status den Node
  an: das Zertifikat gegen die System-Roots oder --ca-file, dann initialize
  ohne Token. Scheitert das (Zertifikat, Gegenseite ohne TLS, Präfix falsch
  oder Anmeldung des Proxys, nicht erreichbar), schreibt add nichts und sagt
  warum; status meldet es. --ca-file dient nur dieser Prüfung — die
  Assistenten prüfen das Zertifikat mit ihren eigenen Trust-Stores.

  Wahl der Hubs: An eine entfernte Adresse (https, oder
  http://host.docker.internal) gehen nur die Header-Paare der gewählten Hubs
  (--hub <alias>, wiederholbar; <alias> ist der Hub-Eintrag am entfernten
  Node). Die Wahl steht fest im Eintrag — beim Helfer als --hub, bei OpenCode
  als Verweise nur auf ihre Token-Dateien —, ein Hub, der später unter
  tokens/ hinzukommt, geht nicht mit. Ohne --hub nimmt add den Hub nur, wenn
  unter tokens/ genau einer liegt; sonst bricht es ab und schreibt nichts
  (Exit 2), auch mit eigenem Node.

  Auf einem Rechner mit eigenem Node gilt --node nur bis zum nächsten
  automatischen Anstoß (install.sh, rotate, check): Der setzt die lokale
  Adresse wieder ein; bis dahin meldet status ohne --node „weicht ab“.

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
  --auto                     add: automatischer Anstoß (siehe oben); nicht
                             mit --node
  --node url                 add, status: dieser Node statt dem der config
                             (siehe oben)
  --ca-file pfad             add, status: mit --node https://… das
                             Zertifikat bei der Prüfung gegen diese CA (PEM)
  --hub alias                add, status: die Wahl der Hubs für eine
                             entfernte Adresse; headers: nur diese Hubs;
                             wiederholbar
  --tokens-dir pfad          headers: das Verzeichnis der Token-Dateien;
                             sonst tokens/ neben der config des Users
                             (~/.config/kephalaion/tokens)
  --config pfad              Ort der config (siehe kephalaion node init
                             --help); remove braucht keine

Exit-Codes:
  0   fertig; headers: JSON ausgegeben (auch {} und bei übergangenen Hubs)
  1   Fehler bei einem Assistenten oder ein übergangener Hub; mit --node: der
      Node antwortet nicht wie erwartet; headers: die Standardausgabe ist
      ein Terminal
  2   falscher Aufruf, auch eine Adresse, die --node nicht nimmt, und eine
      entfernte Adresse ohne eindeutige Wahl der Hubs
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
		return assistant.Target{}, fmt.Errorf("%w %s; zuerst: kephalaion node init — oder ein Node auf einem anderen "+
			"Rechner mit --node https://<name>/<präfix>", errNoNode, loc.Path)
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

// remoteFlags sind --node, --ca-file und --hub von add und status.
type remoteFlags struct {
	node, caFile *string
	hubs         stringList
}

func newRemoteFlags(c *command) *remoteFlags {
	f := &remoteFlags{node: c.fs.String("node", "", ""), caFile: c.fs.String("ca-file", "", "")}
	c.fs.Var(&f.hubs, "hub", "")
	return f
}

// remoteTarget ist das Ziel mit --node: die Adresse aus --node, das Binary
// und tokens/ — ohne config. Für eine entfernte Adresse die Wahl der Hubs:
// --hub, ohne Angabe der einzige Hub unter tokens/. usage heißt falscher
// Aufruf (Exit 2).
type remoteTarget struct {
	target  assistant.Target
	addr    nodeAddress
	rootCAs *x509.CertPool
}

func (f *remoteFlags) resolve() (rt *remoteTarget, usage bool, err error) {
	if *f.node == "" {
		if *f.caFile != "" || len(f.hubs) > 0 {
			return nil, true, errors.New("--ca-file und --hub nur zusammen mit --node <url>")
		}
		return nil, false, nil
	}
	addr, err := parseNodeAddress(*f.node)
	if err != nil {
		return nil, true, err
	}
	rootCAs, err := readNodeCA(addr, *f.caFile)
	if err != nil {
		return nil, *f.caFile != "" && !addr.HTTPS, err
	}
	exe, err := assistantExecutable()
	if err != nil {
		return nil, false, fmt.Errorf("Pfad dieses Binarys nicht ermittelbar: %w", err)
	}
	if exe, err = filepath.Abs(exe); err != nil {
		return nil, false, err
	}
	tokens, err := assistant.TokensDir()
	if err != nil {
		return nil, false, err
	}
	rt = &remoteTarget{target: assistant.Target{URL: addr.Endpoint(), Binary: exe, TokensDir: tokens}, addr: addr,
		rootCAs: rootCAs}
	if !addr.Remote() {
		if len(f.hubs) > 0 {
			return nil, true, fmt.Errorf("--hub nur mit einer entfernten Adresse (https oder http://%s); an %s gehen "+
				"wie ohne --node die Paare aller Hubs mit Token-Datei", dockerHost, addr.Base)
		}
		return rt, false, nil
	}
	if rt.target.Hubs, err = chooseHubs(tokens, f.hubs); err != nil {
		return nil, true, err
	}
	return rt, false, nil
}

// chooseHubs ist die Wahl der Hubs für eine entfernte Adresse: die aus --hub
// (geprüft, sortiert, ohne Doppel), ohne Angabe der einzige Hub unter
// tokens/.
func chooseHubs(tokensDir string, flags []string) ([]string, error) {
	if len(flags) > 0 {
		var hubs []string
		for _, h := range flags {
			if err := ident.CheckName("Hub", h); err != nil {
				return nil, fmt.Errorf("--hub %q: %w", h, err)
			}
			if !slices.Contains(hubs, h) {
				hubs = append(hubs, h)
			}
		}
		sort.Strings(hubs)
		return hubs, nil
	}
	hubs, err := assistant.Hubs(tokensDir)
	if err != nil {
		return nil, err
	}
	switch len(hubs) {
	case 1:
		return hubs, nil
	case 0:
		return nil, fmt.Errorf("keine Token-Datei unter %s: für einen Node auf einem anderen Rechner zuerst die "+
			"Token-Datei des Accounts nach %s (0600) — <hub> ist der Alias des Hub-Eintrags am entfernten Node",
			tokensDir, assistant.TokenFile(tokensDir, "<hub>", "<account>"))
	}
	return nil, fmt.Errorf("mehrere Hubs unter %s (%s): an einen entfernten Node gehen nur die Header-Paare der "+
		"gewählten — --hub <alias> wählt (wiederholbar); nichts eingetragen", tokensDir, strings.Join(hubs, ", "))
}

// ownNode nennt die config mit eigenem Node, wenn es sie gibt — für den
// Hinweis, dass --node dort nur bis zum nächsten automatischen Anstoß gilt.
func ownNode(cfgFlag string) string {
	loc, err := config.Locate(cfgFlag)
	if err != nil {
		return ""
	}
	cfg, _, err := config.Load(loc.Path)
	if err != nil || cfg.Listen(config.Node) == "" {
		return ""
	}
	return loc.Path
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
	remote := newRemoteFlags(c)
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
	if *auto && *remote.node != "" {
		fmt.Fprintf(stderr, "%s: --auto ist der automatische Anstoß mit der Adresse aus der config, nicht mit --node\n", c.name)
		return 2
	}
	rt, usage, err := remote.resolve()
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", c.name, err)
		if usage {
			return 2
		}
		return 1
	}
	ctx := context.Background()
	var target assistant.Target
	if rt != nil {
		// Erst prüfen, dann eintragen: ohne Token, vor jedem Schreiben.
		if _, err := probeNode(ctx, rt.addr, rt.rootCAs, *remote.caFile); err != nil {
			fmt.Fprintf(stderr, "%s: %v\nNichts eingetragen.\n", c.name, err)
			return 1
		}
		fmt.Fprintf(stdout, "Node erreichbar: %s\n", rt.target.URL)
		target = rt.target
	} else {
		target, err = assistantTarget(*c.cfg)
		if errors.Is(err, errNoNode) && *auto {
			return 0
		}
		if err != nil {
			return c.fail(err)
		}
	}
	opts := assistant.AddOptions{Assistants: names, Choice: choice, DryRun: *dryRun, Auto: *auto}
	code := registerAssistants(ctx, c.name, target, opts, stdout, stderr)
	if rt != nil {
		if own := ownNode(*c.cfg); own != "" {
			fmt.Fprintf(stdout, "Hinweis: Dieser Rechner hat einen eigenen Node (%s). Der nächste automatische Anstoß "+
				"(install.sh, kephalaion node account rotate, check) trägt wieder dessen Adresse ein.\n", own)
		}
	}
	return code
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

// autoRegister ist der automatische Anstoß nach node account rotate und
// check, wenn sie die Token-Datei file geschrieben haben: dieselbe Regel wie
// node mcp add --auto, im Binary aufgerufen. Nur, wenn file unter dem eigenen
// tokens/ liegt — bei --token-stdin gibt es keine Datei, und global schreibt
// der Verwalter als Systembenutzer in eine Datei außerhalb. Scheitert der
// Anstoß, bleibt es bei einer Warnung: rotate bzw. check selbst ist gelungen.
func (c *command) autoRegister(file string) {
	if file == "" {
		return
	}
	dir, err := assistant.TokensDir()
	if err != nil || !withinDir(file, dir) {
		return
	}
	target, err := assistantTarget(*c.cfg)
	if errors.Is(err, errNoNode) {
		return
	}
	if err != nil {
		fmt.Fprintf(c.stderr, "%s: Warnung: bei den KI-Assistenten nicht angemeldet: %v\n", c.name, err)
		return
	}
	if registerAssistants(context.Background(), c.name, target, assistant.AddOptions{Auto: true}, c.stdout, c.stderr) != 0 {
		fmt.Fprintf(c.stderr, "%s: Warnung: Die Anmeldung bei den KI-Assistenten ist nicht vollständig "+
			"(kephalaion node mcp status); das Token selbst ist ersetzt.\n", c.name)
	}
}

// withinDir sagt, ob path in dir oder darunter liegt — beide absolut und mit
// aufgelösten Links verglichen, soweit es sie gibt.
func withinDir(path, dir string) bool {
	resolve := func(p string) string {
		if abs, err := filepath.Abs(p); err == nil {
			p = abs
		}
		if real, err := filepath.EvalSymlinks(p); err == nil {
			return real
		}
		// Die Datei selbst gibt es vielleicht nicht: ihr Verzeichnis auflösen.
		if real, err := filepath.EvalSymlinks(filepath.Dir(p)); err == nil {
			return filepath.Join(real, filepath.Base(p))
		}
		return p
	}
	rel, err := filepath.Rel(resolve(dir), resolve(path))
	return err == nil && rel != "." && !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel)
}

func runNodeMCPRemove(args []string, stdout, stderr io.Writer) int {
	// --config gilt auch hier (für Aufrufe mit einheitlichen Optionen); remove
	// braucht keine config.
	c := newCommand("node mcp remove", nodeMCPUsage, stdout, stderr)
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
	remote := newRemoteFlags(c)
	if _, code, ok := c.parse(args); !ok {
		return code
	}
	names, ok := assistantNames(c, assistants)
	if !ok {
		return 2
	}
	rt, usage, err := remote.resolve()
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", c.name, err)
		if usage {
			return 2
		}
		return 1
	}
	ctx := context.Background()
	code := 0
	var target assistant.Target
	nodeLine := ""
	if rt != nil {
		target = rt.target
		nodeLine = " — erreichbar"
		if _, err := probeNode(ctx, rt.addr, rt.rootCAs, *remote.caFile); err != nil {
			nodeLine, code = " — "+err.Error(), 1
		}
		if len(target.Hubs) > 0 {
			nodeLine += " (Hubs: " + strings.Join(target.Hubs, ", ") + ")"
		}
	} else if target, err = assistantTarget(*c.cfg); err != nil {
		return c.fail(err)
	}
	statuses, skipped, err := newAssistantManager().Status(ctx, target, names)
	if err != nil {
		return c.fail(err)
	}
	fmt.Fprintf(stdout, "Node: %s%s\n", target.URL, nodeLine)
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
	var accounts, hubFlags stringList
	fs.Var(&accounts, "account", "")
	fs.Var(&hubFlags, "hub", "")
	if _, code, ok := parseFlags(fs, args, nodeMCPUsage, 0, stderr); !ok {
		return code
	}
	choice, err := assistant.ParseChoice(accounts)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", name, err)
		return 2
	}
	// Mit --hub nur diese Hubs: der Helfer eines Eintrags mit entfernter
	// Adresse.
	var hubs []string
	if len(hubFlags) > 0 {
		if hubs, err = chooseHubs("", hubFlags); err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", name, err)
			return 2
		}
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
		logins, skipped, err := assistant.SelectedLogins(dir, hubs, choice)
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
