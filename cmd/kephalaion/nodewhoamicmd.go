package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/kephalaion/kephalaion/internal/buildinfo"
	"github.com/kephalaion/kephalaion/internal/config"
	"github.com/kephalaion/kephalaion/internal/ident"
	"github.com/kephalaion/kephalaion/internal/node/mcpnode"
	nodestore "github.com/kephalaion/kephalaion/internal/node/store"
	"github.com/kephalaion/kephalaion/internal/upgrade"
)

const nodeWhoamiUsage = `Aufruf:
  kephalaion node whoami [--hub <alias>]
  kephalaion node whoami <account> [--hub <alias>] [--json]

Ohne Account: die Version, je Hub der Name dieses Nodes am Hub und der
Stand des Abgleichs, dazu die Accounts, die der Node aus seinen Replicas
kennt (lebende Zeilen SYSTEM:A:), mit User, Collections und Rechten. Ein
gesperrter Account fehlt, sobald der Abgleich die Sperre gebracht hat.

Mit Account: was das MCP-Werkzeug whoami einem Client antworten würde, der
für diesen Account an jedem Hub gültige Zugangsdaten schickt — login ok, wo
der Account in der Replica steht, sonst missing. Aus derselben Funktion wie
das Werkzeug.

Lässt sich die Replica eines Hubs nicht lesen, steht der Hub ohne Accounts
und mit „Replica nicht lesbar“ da; die übrigen Hubs gelten weiter. Die volle
Meldung, samt Pfad, steht auf stderr (wie in kephalaion status).

Die Zeile Update bzw. das Feld update sagt wie im Werkzeug, ob es eine neuere
Version gibt und wie das Upgrade geht; node whoami fragt dafür wie
kephalaion upgrade --check direkt bei GitHub (serve dagegen höchstens einmal
am Tag).

Ohne Token: Wer die Kommandozeile aufruft, kann die Datenbanken des Nodes
ohnehin lesen. Ob ein Token gilt, prüft kephalaion node account check.

Optionen:
  --hub alias     nur diesen Hub-Eintrag
  --json          die Antwort als JSON, in der Struktur des Werkzeugs (nur
                  mit <account>)
  --config pfad   Ort der config (siehe kephalaion node init --help)
`

func runNodeWhoami(args []string, stdout, stderr io.Writer) int {
	c := newCommand("node whoami", nodeWhoamiUsage, stdout, stderr)
	c.optional = 1
	hub := c.fs.String("hub", "", "")
	asJSON := c.fs.Bool("json", false, "")
	return c.nodeDo(args, func(ctx context.Context, s nodestore.Store, _ bool, pos []string) error {
		if *hub != "" {
			if _, err := s.Hub(ctx, *hub); err != nil {
				return err
			}
		}
		version := buildinfo.Get().Version
		if len(pos) == 0 && *asJSON {
			return errors.New("--json gibt es nur mit <account>: die Struktur ist die des Werkzeugs whoami")
		}
		update := cliUpdate(ctx, *c.cfg)
		if len(pos) == 0 {
			return printNodeOverview(ctx, stdout, stderr, s, version, update, *hub)
		}
		account := pos[0]
		if err := ident.CheckPrincipalName("Account", account); err != nil {
			return err
		}
		logins, err := mcpnode.AccountLogins(ctx, s, account)
		if err != nil {
			return err
		}
		logins.Hubs = onlyHub(logins.Hubs, *hub)
		out, text, unread, err := mcpnode.Whoami(ctx, s, version, update, logins)
		if err != nil {
			return err
		}
		reportUnreadable(stderr, unread)
		if *asJSON {
			enc := json.NewEncoder(stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(out)
		}
		fmt.Fprintln(stdout, text)
		return nil
	})
}

// cliUpdateTimeout begrenzt die Frage nach einer neuen Version in node whoami.
const cliUpdateTimeout = 10 * time.Second

// cliUpdate fragt für node whoami direkt bei GitHub nach einer neuen Version,
// wie upgrade --check, aus der Sicht dieses Aufrufs; serve fragt dagegen
// höchstens einmal am Tag und antwortet über MCP aus dem Speicher.
func cliUpdate(ctx context.Context, cfgFlag string) upgrade.Report {
	ctx, cancel := context.WithTimeout(ctx, cliUpdateTimeout)
	defer cancel()
	u := newUpgrader(io.Discard)
	if loc, err := config.Locate(cfgFlag); err == nil {
		u.System = loc.System()
	}
	return u.Report(ctx)
}

// onlyUnreadable grenzt die Meldungen zu unlesbaren Replicas auf einen Alias
// ein; leer lässt alle.
func onlyUnreadable(list []*mcpnode.UnreadableError, alias string) []*mcpnode.UnreadableError {
	if alias == "" {
		return list
	}
	var out []*mcpnode.UnreadableError
	for _, ue := range list {
		if ue.Hub == alias {
			out = append(out, ue)
		}
	}
	return out
}

// onlyHub grenzt die Anmeldungen auf einen Alias ein; leer lässt alle.
func onlyHub(logins []mcpnode.Login, alias string) []mcpnode.Login {
	if alias == "" {
		return logins
	}
	var out []mcpnode.Login
	for _, l := range logins {
		if l.Hub == alias {
			out = append(out, l)
		}
	}
	return out
}

// reportUnreadable meldet Replicas, die sich nicht lesen ließen, mit der
// vollen Meldung auf stderr — Diagnose wie in status; die Ausgabe auf stdout
// nennt nur den festen Satz, keinen Pfad. Je Hub eine Zeile.
func reportUnreadable(stderr io.Writer, unread ...[]*mcpnode.UnreadableError) {
	seen := map[string]bool{}
	for _, list := range unread {
		for _, ue := range list {
			if !seen[ue.Hub] {
				seen[ue.Hub] = true
				fmt.Fprintf(stderr, "node whoami: %v\n", ue)
			}
		}
	}
}

// printNodeOverview zeigt Version, je Hub Node-Name und Stand des Abgleichs
// — aus Whoami, ohne Anmeldung — und die bekannten Accounts. Ein Hub, dessen
// Replica sich nicht lesen lässt, steht mit dem Hinweis darauf und ohne
// Accounts da.
func printNodeOverview(ctx context.Context, w, stderr io.Writer, s nodestore.Store, version string,
	update upgrade.Report, alias string) error {
	hubs, err := s.Hubs(ctx)
	if err != nil {
		return err
	}
	var none mcpnode.Logins
	for _, h := range hubs {
		none.Hubs = append(none.Hubs, mcpnode.Login{Hub: h.Name, Node: h.NodeName, State: mcpnode.LoginMissing})
	}
	none.Hubs = onlyHub(none.Hubs, alias)
	out, _, unread, err := mcpnode.Whoami(ctx, s, version, update, none)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "kephalaion %s\n", out.Version)
	if out.Update != nil {
		fmt.Fprintln(w, out.Update.Summary())
	}
	if len(out.Hubs) == 0 {
		fmt.Fprintln(w, "Keine Hubs.")
		return nil
	}
	fmt.Fprintln(w, "Hubs:")
	for _, h := range out.Hubs {
		fmt.Fprintf(w, "  %s (Node %s): %s\n", h.Hub, h.Node, mcpnode.DescribeSync(h.Sync))
	}
	accounts, more, err := mcpnode.KnownAccounts(ctx, s)
	if err != nil {
		return err
	}
	reportUnreadable(stderr, unread, onlyUnreadable(more, alias))
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	n := 0
	for _, a := range accounts {
		if alias != "" && a.Hub != alias {
			continue
		}
		if n == 0 {
			fmt.Fprintln(w, "Accounts:")
			fmt.Fprintln(tw, "  HUB\tACCOUNT\tUSER\tCOLLECTIONS")
		}
		n++
		parts := make([]string, 0, len(a.Rights))
		for _, r := range a.Rights {
			parts = append(parts, fmt.Sprintf("%s (%s)", ident.Address(a.Hub, r.Collection), r.Rights))
		}
		fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\n", a.Hub, a.Account, a.User, strings.Join(parts, ", "))
	}
	if n == 0 {
		fmt.Fprintln(w, "Accounts: keine bekannt (erst nach rotate oder Abgleich)")
		return nil
	}
	return tw.Flush()
}
