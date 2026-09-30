package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/kephalaion/kephalaion/internal/assistant"
)

const nodeMCPUsage = `Aufruf:
  kephalaion node mcp headers [--tokens-dir pfad] [--account <hub>=<account>]…

Meldet den Node bei den KI-Assistenten des Users als MCP-Server an.

Kommandos:
  headers  gibt die Header-Paare aller Hubs mit Token-Datei als ein
           JSON-Objekt aus: {"X-Keph-Account-<alias>":"…",
           "X-Keph-Token-<alias>":"…"}, ohne Token-Datei {}. Das ist der
           Helfer, den Claude Code und Codex bei jeder Verbindung aufrufen
           (headersHelper, http_headers_helper) — für Assistenten, nicht für
           Menschen: Es ist die einzige Ausgabe mit Token. Im Terminal bricht
           headers deshalb ab. Außer im Terminal und bei falschem Aufruf endet
           es immer mit Exit 0 und gültigem JSON: Ein Hub ohne eindeutiges,
           lesbares Token (mehrere Accounts ohne Wahl, die gewählte Datei
           fehlt oder ist unlesbar) fehlt im Objekt, mit einer Meldung ohne
           Token auf stderr. Der Assistent verbindet dann ohne Anmeldung für
           diesen Hub; das Werkzeug whoami zeigt es.

           Die Shell eines KI-Agenten ist kein Terminal: Dort gibt headers
           die Tokens aus, und sie stünden im Kontext der KI. Ein Agent ruft
           headers deshalb nie gegen echte Token-Dateien so auf, dass die
           Ausgabe bei ihm ankommt — höchstens die Schlüssel:
             kephalaion node mcp headers | jq -r 'keys[]'

Token-Dateien: <tokens-dir>/<hub>/<account>.token (eine Zeile, 0600; .pending
wird übergangen), wie bei kephalaion node dir. Ohne --account gilt je Hub die
einzige Token-Datei.

Optionen:
  --tokens-dir pfad          das Verzeichnis der Token-Dateien; sonst tokens/
                             neben der config des Users
                             (~/.config/kephalaion/tokens)
  --account <hub>=<account>  wählt den Account eines Hubs mit mehreren
                             Token-Dateien; wiederholbar

Exit-Codes von headers:
  0   JSON ausgegeben (auch {} und bei übergangenen Hubs)
  1   die Standardausgabe ist ein Terminal
  2   falscher Aufruf
`

// stdoutIsTerminal sagt, ob w ein Terminal ist; Tests ersetzen es.
var stdoutIsTerminal = func(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && isTerminal(f.Fd())
}

func runNodeMCP(args []string, stdout, stderr io.Writer) int {
	u := nodeMCPUsage
	return dispatch("node mcp", u, args, stdout, stderr, map[string]func([]string) int{
		"headers": func(a []string) int { return runNodeMCPHeaders(a, stdout, stderr) },
	})
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
		var unread []assistant.Skipped
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
