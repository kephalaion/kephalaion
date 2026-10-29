package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/kephalaion/kephalaion/internal/buildinfo"
	"github.com/kephalaion/kephalaion/internal/hub/source"
	hubstore "github.com/kephalaion/kephalaion/internal/hub/store"
	"github.com/kephalaion/kephalaion/internal/setupcode"
)

const hubCollectionUsage = `Aufruf:
  kephalaion hub collection add  <name> [--description text] [--public]
  kephalaion hub collection list
  kephalaion hub collection show <name>
  kephalaion hub collection set  <name> [--description text] [--public[=false]]
  kephalaion hub collection fetch <name>
  kephalaion hub collection rm   <name>

Kommandos:
  add    legt eine Collection an. Steht der Name in der Liste der Quellen
         dieses Binarys (etwa k-playbook), bekommt sie deren Vorgaben:
         öffentlich, fest abonniert, Inhalt aus dem Release der Quelle
         unter defaults/ — niemand schreibt darin. add holt das Release
         gleich; ohne Netz entsteht sie leer, dann später fetch.
  list   zeigt alle Collections
  show   zeigt eine Collection, bei einer mit Quelle auch Quelle und
         geholten Stand
  set    ändert Beschreibung und/oder öffentlich; bei einer Collection aus
         der Liste der Quellen bleibt sie öffentlich
  fetch  holt das neueste Release der Quelle nach defaults/ und ersetzt es
         als Ganzes, in einem Schreibvorgang (Urheber system). Ist es schon
         geholt, ist nichts zu tun. Tag aus der Weiterleitung von
         releases/latest, ohne GitHub-API und Token; Proxy aus der Umgebung
         (HTTPS_PROXY). Höchstens 20 s für Tag und Laden.
  rm     entfernt eine Collection — nur, wenn kein Node sie abgleichen darf,
         kein Account Rechte in ihr hat (auch kein gesperrter) und keine
         Dokumente in ihr stehen, auch keine Löschmarken. Löschmarken von
         Accounts zählen nicht; sie bleiben stehen, und ein grant in einer
         gleichnamigen neuen Collection belebt sie wieder. Bei einer
         Collection mit Quelle entfernt rm die geholten Dokumente mit.

Öffentlich (--public): Jeder Account des Hubs liest die Collection, ohne
eigene Zeile darin; schreiben braucht weiter Rechte (hub account grant). Am
Node liest sie nur ein Account, der dort auch eine eigene Collection
desselben Hubs hat. Abgleichen darf ein Node sie weiter nur nach hub node
grant — außer einer Collection aus der Liste der Quellen, die jeder Node
von selbst abgleicht.

Namen: a–z, 0–9, '.', '_' und '-', am Anfang a–z oder 0–9, höchstens 63
Zeichen, kein Präfix system. Jede Änderung steht im Protokoll (actions) als
admin.

Optionen:
  --description text   Kurzbeschreibung
  --public             öffentlich; bei set mit --public=false wieder nicht
  --config pfad        Ort der config (siehe kephalaion hub init --help)
`

const hubNodeUsage = `Aufruf:
  kephalaion hub node add    <name> [--description text] [--grant <collection>]…
                             [--address url] [--no-code]
  kephalaion hub node list
  kephalaion hub node show   <name>
  kephalaion hub node set    <name> --description text
  kephalaion hub node rm     <name>
  kephalaion hub node lock   <name>
  kephalaion hub node unlock <name>
  kephalaion hub node grant  <node> <collection>
  kephalaion hub node revoke <node> <collection>
  kephalaion hub node token  <name> [--address url] [--no-code]

Kommandos:
  add      legt einen Node an und zeigt seinen Einrichtungscode bzw. sein
           Token — genau einmal; mit --grant gleich die Collections, die er
           abgleichen darf
  list     zeigt alle Nodes
  show     zeigt einen Node samt erlaubten Collections
  set      ändert die Beschreibung
  rm       entfernt einen Node samt seinen Rechten
  lock     sperrt einen Node; unlock hebt die Sperre auf
  grant    erlaubt dem Node, die Collection abzugleichen (replicate);
           revoke nimmt es zurück
  token    erzeugt ein neues Token und zeigt es einmal, als Einrichtungscode
           wie add; das alte gilt nicht mehr

Einrichtungscode: add und token geben einen Code aus (kephsetup_…), wenn die
settings des Hubs public_url tragen (kephalaion config set hub public_url
https://host/kephalaion/hub) oder --address kommt. Er enthält Adresse, Alias
(settings alias, wenn gesetzt), Node-Namen und Token; auf dem Rechner des
Nodes macht kephalaion node setup <code> daraus einen laufenden Node und
rotiert das Token sofort — danach ist der Code wertlos. Ohne public_url und
--address, oder mit --no-code, zeigen add und token das Token selbst; am
Node trägt es kephalaion node hub add … --node <name> --token-stdin ein.

Gespeichert wird nur der Hash des Tokens. Node-Namen folgen den Regeln für
Collections, admin ist reserviert, und sie sind gemeinsam mit den
Account-Namen eindeutig.

Optionen:
  --description text   Kurzbeschreibung
  --grant collection   die Collection gleich erlauben, wie hub node grant;
                       wiederholbar
  --address url        die Adresse des Hubs im Code statt public_url:
                       https://host[:port][/pfad], auf diesem Rechner auch
                       http://localhost:<port>/hub
  --no-code            das Token statt des Codes zeigen (der Weg über
                       node hub add … --token-stdin)
  --config pfad        Ort der config (siehe kephalaion hub init --help)
`

func runHubCollection(args []string, stdout, stderr io.Writer) int {
	u := hubCollectionUsage
	return dispatch("hub collection", u, args, stdout, stderr, map[string]func([]string) int{
		"add": func(a []string) int {
			c := newCommand("hub collection add", u, stdout, stderr, "<name>")
			desc := c.fs.String("description", "", "")
			public := c.fs.Bool("public", false, "")
			return c.hubDo(a, func(ctx context.Context, s hubstore.Store, pos []string) error {
				if err := s.AddCollection(ctx, pos[0], *desc, *public); err != nil {
					return err
				}
				fmt.Fprintf(stdout, "Collection %s angelegt.\n", pos[0])
				if e, ok := source.Lookup(pos[0]); ok {
					fmt.Fprintf(stdout, "Aus der Liste der Quellen: öffentlich, fest abonniert, Inhalt aus %s (%s) unter %s/.\n",
						e.Git, e.Asset, source.DefaultsDir)
					// Holen beim Anlegen; ohne Netz bleibt sie leer.
					res, err := source.Fetch(ctx, newSourceClient(), s, pos[0], source.Actor{Account: hubstore.Admin})
					if err != nil {
						fmt.Fprintf(stdout, "Noch nichts geholt: %v\nHolen mit: kephalaion hub collection fetch %s\n", err, pos[0])
						return nil
					}
					printFetch(stdout, pos[0], res)
				}
				return nil
			})
		},
		"list": func(a []string) int {
			c := newCommand("hub collection list", u, stdout, stderr)
			return c.hubDo(a, func(ctx context.Context, s hubstore.Store, _ []string) error {
				colls, err := s.Collections(ctx)
				if err != nil {
					return err
				}
				if len(colls) == 0 {
					fmt.Fprintln(stdout, "Keine Collections.")
					return nil
				}
				tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
				fmt.Fprintln(tw, "NAME\tÖFFENTLICH\tQUELLE\tBESCHREIBUNG")
				for _, col := range colls {
					src := "-"
					if e, ok := source.Lookup(col.Name); ok {
						src = e.Repo
					}
					fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", col.Name, yesNo(col.IsPublic()), src, orDash(col.Description))
				}
				return tw.Flush()
			})
		},
		"show": func(a []string) int {
			c := newCommand("hub collection show", u, stdout, stderr, "<name>")
			return c.hubDo(a, func(ctx context.Context, s hubstore.Store, pos []string) error {
				col, err := s.Collection(ctx, pos[0])
				if err != nil {
					return err
				}
				fmt.Fprintf(stdout, "Collection %s\n", col.Name)
				fmt.Fprintf(stdout, "  Beschreibung: %s\n", orDash(col.Description))
				fmt.Fprintf(stdout, "  öffentlich:   %s\n", yesNo(col.IsPublic()))
				if e, ok := source.Lookup(col.Name); ok {
					fmt.Fprintf(stdout, "  Quelle:       %s, Asset %s, höchstens %d Bytes (Liste der Quellen)\n",
						e.Git, e.Asset, e.MaxBytes)
					fmt.Fprintf(stdout, "  abonniert:    fest, von jedem Node\n")
					if col.FetchedTag == "" {
						fmt.Fprintf(stdout, "  geholt:       noch nichts (kephalaion hub collection fetch %s)\n", col.Name)
					} else {
						fmt.Fprintf(stdout, "  geholt:       %s am %s\n", col.FetchedTag, formatMillis(col.FetchedAt))
					}
				} else {
					fmt.Fprintf(stdout, "  Quelle:       keine\n")
				}
				fmt.Fprintf(stdout, "  angelegt:     %s von %s\n", formatMillis(col.CreatedAt), col.CreatedBy)
				return nil
			})
		},
		"set": func(a []string) int {
			c := newCommand("hub collection set", u, stdout, stderr, "<name>")
			desc := c.fs.String("description", "", "")
			public := c.fs.Bool("public", false, "")
			return c.hubDo(a, func(ctx context.Context, s hubstore.Store, pos []string) error {
				var change hubstore.CollectionChange
				if c.isSet("description") {
					change.Description = desc
				}
				if c.isSet("public") {
					change.Public = public
				}
				if change.Description == nil && change.Public == nil {
					return fmt.Errorf("nichts zu ändern; erwartet --description oder --public")
				}
				if err := s.SetCollection(ctx, pos[0], change); err != nil {
					return err
				}
				fmt.Fprintf(stdout, "Collection %s geändert.\n", pos[0])
				return nil
			})
		},
		"fetch": func(a []string) int {
			c := newCommand("hub collection fetch", u, stdout, stderr, "<name>")
			return c.hubDo(a, func(ctx context.Context, s hubstore.Store, pos []string) error {
				if _, err := s.Collection(ctx, pos[0]); err != nil {
					return err
				}
				res, err := source.Fetch(ctx, newSourceClient(), s, pos[0], source.Actor{Account: hubstore.Admin})
				if err != nil {
					return err
				}
				printFetch(stdout, pos[0], res)
				return nil
			})
		},
		"rm": func(a []string) int {
			c := newCommand("hub collection rm", u, stdout, stderr, "<name>")
			return c.hubDo(a, func(ctx context.Context, s hubstore.Store, pos []string) error {
				if err := s.RemoveCollection(ctx, pos[0]); err != nil {
					return err
				}
				fmt.Fprintf(stdout, "Collection %s entfernt.\n", pos[0])
				return nil
			})
		},
	})
}

func runHubNode(args []string, stdout, stderr io.Writer) int {
	u := hubNodeUsage
	simple := func(name, done string, fn func(ctx context.Context, s hubstore.Store, node string) error) func([]string) int {
		return func(a []string) int {
			c := newCommand("hub node "+name, u, stdout, stderr, "<name>")
			return c.hubDo(a, func(ctx context.Context, s hubstore.Store, pos []string) error {
				if err := fn(ctx, s, pos[0]); err != nil {
					return err
				}
				fmt.Fprintf(stdout, "Node %s %s.\n", pos[0], done)
				return nil
			})
		}
	}
	grant := func(name, done string, fn func(ctx context.Context, s hubstore.Store, node, coll string) error) func([]string) int {
		return func(a []string) int {
			c := newCommand("hub node "+name, u, stdout, stderr, "<node>", "<collection>")
			return c.hubDo(a, func(ctx context.Context, s hubstore.Store, pos []string) error {
				if err := fn(ctx, s, pos[0], pos[1]); err != nil {
					return err
				}
				fmt.Fprintf(stdout, "Node %s: %s %s.\n", pos[0], pos[1], done)
				return nil
			})
		}
	}
	return dispatch("hub node", u, args, stdout, stderr, map[string]func([]string) int{
		"add": func(a []string) int {
			c := newCommand("hub node add", u, stdout, stderr, "<name>")
			desc := c.fs.String("description", "", "")
			var grants stringList
			c.fs.Var(&grants, "grant", "")
			out := newCodeFlags(c)
			return c.hubDo(a, func(ctx context.Context, s hubstore.Store, pos []string) error {
				name := pos[0]
				// Vorher prüfen, was ohne den Node geht: Adresse und
				// Collections. Ein Recht, das danach doch scheitert, nennt
				// die Meldung; der Code steht dann trotzdem da.
				if err := out.check(); err != nil {
					return err
				}
				seen := map[string]bool{}
				for _, g := range grants {
					if seen[g] {
						return fmt.Errorf("--grant %s steht zweimal", g)
					}
					seen[g] = true
					if _, err := s.Collection(ctx, g); err != nil {
						return fmt.Errorf("--grant: %w", err)
					}
				}
				token, err := s.AddNode(ctx, name, *desc)
				if err != nil {
					return err
				}
				fmt.Fprintf(stdout, "Node %s angelegt.\n", name)
				var failed []string
				for _, g := range grants {
					if err := s.Grant(ctx, name, g); err != nil {
						failed = append(failed, fmt.Sprintf("%s: %v", g, err))
						continue
					}
					fmt.Fprintf(stdout, "Node %s: %s erlaubt.\n", name, g)
				}
				if err := out.print(ctx, stdout, s, name, token); err != nil {
					return err
				}
				if len(failed) > 0 {
					return fmt.Errorf("nicht erlaubt (nachholen mit kephalaion hub node grant %s <collection>):\n  %s",
						name, strings.Join(failed, "\n  "))
				}
				return nil
			})
		},
		"list": func(a []string) int {
			c := newCommand("hub node list", u, stdout, stderr)
			return c.hubDo(a, func(ctx context.Context, s hubstore.Store, _ []string) error {
				nodes, err := s.Nodes(ctx)
				if err != nil {
					return err
				}
				if len(nodes) == 0 {
					fmt.Fprintln(stdout, "Keine Nodes.")
					return nil
				}
				tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
				fmt.Fprintln(tw, "NAME\tSTATUS\tCOLLECTIONS\tBESCHREIBUNG")
				for _, n := range nodes {
					fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", n.Name, lockState(n.Locked), joinOrNone(n.Collections), orDash(n.Description))
				}
				return tw.Flush()
			})
		},
		"show": func(a []string) int {
			c := newCommand("hub node show", u, stdout, stderr, "<name>")
			return c.hubDo(a, func(ctx context.Context, s hubstore.Store, pos []string) error {
				n, err := s.Node(ctx, pos[0])
				if err != nil {
					return err
				}
				fmt.Fprintf(stdout, "Node %s\n", n.Name)
				fmt.Fprintf(stdout, "  Beschreibung: %s\n", orDash(n.Description))
				fmt.Fprintf(stdout, "  Status:       %s\n", lockState(n.Locked))
				fmt.Fprintf(stdout, "  Collections:  %s\n", joinOrNone(n.Collections))
				fmt.Fprintf(stdout, "  Token:        nur als Hash gespeichert\n")
				fmt.Fprintf(stdout, "  angelegt:     %s von %s\n", formatMillis(n.CreatedAt), n.CreatedBy)
				return nil
			})
		},
		"set": func(a []string) int {
			c := newCommand("hub node set", u, stdout, stderr, "<name>")
			desc := c.fs.String("description", "", "")
			return c.hubDo(a, func(ctx context.Context, s hubstore.Store, pos []string) error {
				if !c.isSet("description") {
					return fmt.Errorf("nichts zu ändern; erwartet --description")
				}
				if err := s.SetNodeDescription(ctx, pos[0], *desc); err != nil {
					return err
				}
				fmt.Fprintf(stdout, "Node %s geändert.\n", pos[0])
				return nil
			})
		},
		"rm": simple("rm", "entfernt", func(ctx context.Context, s hubstore.Store, n string) error {
			return s.RemoveNode(ctx, n)
		}),
		"lock": simple("lock", "gesperrt", func(ctx context.Context, s hubstore.Store, n string) error {
			return s.SetNodeLocked(ctx, n, true)
		}),
		"unlock": simple("unlock", "entsperrt", func(ctx context.Context, s hubstore.Store, n string) error {
			return s.SetNodeLocked(ctx, n, false)
		}),
		"grant": grant("grant", "erlaubt", func(ctx context.Context, s hubstore.Store, n, col string) error {
			return s.Grant(ctx, n, col)
		}),
		"revoke": grant("revoke", "nicht mehr erlaubt", func(ctx context.Context, s hubstore.Store, n, col string) error {
			return s.Revoke(ctx, n, col)
		}),
		"token": func(a []string) int {
			c := newCommand("hub node token", u, stdout, stderr, "<name>")
			out := newCodeFlags(c)
			out.renew = true
			return c.hubDo(a, func(ctx context.Context, s hubstore.Store, pos []string) error {
				if err := out.check(); err != nil {
					return err
				}
				token, err := s.NewNodeToken(ctx, pos[0])
				if err != nil {
					return err
				}
				fmt.Fprintf(stdout, "Node %s: neues Token, das alte gilt nicht mehr.\n", pos[0])
				if err := out.print(ctx, stdout, s, pos[0], token); err != nil {
					return err
				}
				fmt.Fprintln(stdout, "Bis dahin weist der Hub den Node ab.")
				return nil
			})
		},
	})
}

// newSourceClient liefert den Client, mit dem der Hub Releases holt; Tests
// lenken ihn auf ein nachgespieltes GitHub (TestMain).
var newSourceClient = func() *source.Client {
	return source.NewClient("kephalaion/" + buildinfo.Version)
}

// printFetch zeigt das Ergebnis eines Holens.
func printFetch(w io.Writer, name string, res source.Result) {
	if res.Current {
		fmt.Fprintf(w, "Collection %s: schon aktuell (%s).\n", name, res.Tag)
		return
	}
	fmt.Fprintf(w, "Collection %s: %s geholt — %d angelegt, %d ersetzt, %d gelöscht", name, res.Tag,
		res.Created, res.Updated, res.Deleted)
	if res.Revision > 0 {
		fmt.Fprintf(w, " (Revision %d)", res.Revision)
	}
	fmt.Fprintln(w, ".")
}

// hubDo wertet die Argumente aus, öffnet den Hub und führt fn aus.
func (c *command) hubDo(args []string, fn func(ctx context.Context, s hubstore.Store, pos []string) error) int {
	pos, code, ok := c.parse(args)
	if !ok {
		return code
	}
	ctx := context.Background()
	s, err := c.openHub(ctx)
	if err != nil {
		return c.fail(err)
	}
	defer s.Close()
	if err := fn(ctx, s, pos); err != nil {
		return c.fail(err)
	}
	return 0
}

// codeFlags sind --address und --no-code von hub node add und token: ob
// und mit welcher Adresse sie einen Einrichtungscode statt des Tokens
// ausgeben.
type codeFlags struct {
	address *string
	noCode  *bool
	// renew: hub node token — der Weg am Node ist dann node hub token bzw.
	// ein erneutes node setup.
	renew bool
}

func newCodeFlags(c *command) *codeFlags {
	return &codeFlags{address: c.fs.String("address", "", ""), noCode: c.fs.Bool("no-code", false, "")}
}

// check prüft die Angaben, bevor etwas geschrieben wird.
func (f *codeFlags) check() error {
	if *f.address == "" {
		return nil
	}
	if *f.noCode {
		return errors.New("--address gilt für den Code; --no-code passt nicht dazu")
	}
	if err := setupcode.CheckAddress(*f.address); err != nil {
		return fmt.Errorf("--address: %w", err)
	}
	return nil
}

// print zeigt das neue Token eines Nodes — das einzige Mal: als
// Einrichtungscode, wenn public_url gesetzt ist oder --address kam und nicht
// --no-code; sonst das Token selbst mit dem Weg über node hub add. Der Code
// steht genau einmal da, das Token dann nirgends.
func (f *codeFlags) print(ctx context.Context, w io.Writer, s hubstore.Store, node, token string) error {
	settings, err := s.Settings(ctx)
	if err != nil {
		return err
	}
	address := *f.address
	if address == "" {
		address = settings[hubstore.SettingPublicURL]
	}
	if *f.noCode || address == "" {
		cmd := "kephalaion node hub add <alias> --transport … --node " + node + " --token-stdin"
		if f.renew {
			cmd = "kephalaion node hub token <alias> --token-stdin"
		}
		printToken(w, token, cmd)
		if !*f.noCode {
			fmt.Fprintln(w, "Einen Einrichtungscode statt des Tokens (für kephalaion node setup) gibt es mit "+
				"kephalaion config set hub public_url https://<host>/kephalaion/hub oder --address.")
		}
		return nil
	}
	alias := settings[hubstore.SettingAlias]
	code, err := setupcode.Encode(setupcode.Code{Address: address, Alias: alias, Node: node, Token: token})
	if err != nil {
		// Eine public_url aus einem Import, die nicht passt: Das Token
		// steht nirgends sonst — ohne Ausgabe wäre es verloren.
		printToken(w, token, "kephalaion node hub add <alias> --transport … --node "+node+" --token-stdin")
		return fmt.Errorf("kein Einrichtungscode: %w", err)
	}
	aliasText := alias
	if aliasText == "" {
		aliasText = "keiner — node setup braucht dann --alias <alias> (am Hub setzen: kephalaion config set hub alias <alias>)"
	}
	fmt.Fprintln(w, "Einrichtungscode (wird nicht wieder angezeigt, gespeichert ist nur der Hash des Tokens darin):")
	fmt.Fprintf(w, "  %s\n", code)
	fmt.Fprintf(w, "Darin: Adresse %s, Alias %s, Node %s.\n", address, aliasText, node)
	fmt.Fprintln(w, "Auf dem Rechner des Nodes, als der User, dem er gehört — rotiert das Token sofort, danach ist der "+
		"Code wertlos:")
	fmt.Fprintln(w, "  kephalaion node setup <code>")
	fmt.Fprintln(w, "  (oder ohne Shell-Verlauf: kephalaion node setup --code-stdin)")
	return nil
}

// printToken zeigt ein neues Token — das einzige Mal — und wie es an den
// Node kommt.
func printToken(w io.Writer, token, nodeCmd string) {
	fmt.Fprintln(w, "Token (wird nicht wieder angezeigt, gespeichert ist nur der Hash):")
	fmt.Fprintf(w, "  %s\n", token)
	fmt.Fprintln(w, "Am Node eintragen, über stdin, nie als Argument:")
	fmt.Fprintf(w, "  %s\n", nodeCmd)
}

func lockState(locked bool) string {
	if locked {
		return "gesperrt"
	}
	return "aktiv"
}

func formatMillis(ms int64) string {
	return time.UnixMilli(ms).Local().Format("2006-01-02 15:04")
}
