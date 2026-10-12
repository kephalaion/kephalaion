package main

import (
	"context"
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/kephalaion/kephalaion/internal/hub/source"
	hubstore "github.com/kephalaion/kephalaion/internal/hub/store"
)

const hubCollectionUsage = `Aufruf:
  kephalaion hub collection add  <name> [--description text] [--public]
  kephalaion hub collection list
  kephalaion hub collection show <name>
  kephalaion hub collection set  <name> [--description text] [--public[=false]]
  kephalaion hub collection rm   <name>

Kommandos:
  add    legt eine Collection an. Steht der Name in der Liste der Quellen
         dieses Binarys (etwa k-playbook), bekommt sie deren Vorgaben:
         öffentlich, fest abonniert, Inhalt aus dem Release der Quelle
         unter defaults/ — niemand schreibt darin.
  list   zeigt alle Collections
  show   zeigt eine Collection, bei einer mit Quelle auch Quelle und
         geholten Stand
  set    ändert Beschreibung und/oder öffentlich; bei einer Collection aus
         der Liste der Quellen bleibt sie öffentlich
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
  kephalaion hub node add    <name> [--description text]
  kephalaion hub node list
  kephalaion hub node show   <name>
  kephalaion hub node set    <name> --description text
  kephalaion hub node rm     <name>
  kephalaion hub node lock   <name>
  kephalaion hub node unlock <name>
  kephalaion hub node grant  <node> <collection>
  kephalaion hub node revoke <node> <collection>
  kephalaion hub node token  <name>

Kommandos:
  add      legt einen Node an und zeigt sein Token — genau einmal
  list     zeigt alle Nodes
  show     zeigt einen Node samt erlaubten Collections
  set      ändert die Beschreibung
  rm       entfernt einen Node samt seinen Rechten
  lock     sperrt einen Node; unlock hebt die Sperre auf
  grant    erlaubt dem Node, die Collection abzugleichen (replicate);
           revoke nimmt es zurück
  token    erzeugt ein neues Token und zeigt es einmal; das alte gilt nicht mehr

Gespeichert wird nur der Hash des Tokens. Am Node wird es mit
kephalaion node hub add … --node <name> --token-stdin eingetragen. Node-Namen folgen den
Regeln für Collections, admin ist reserviert, und sie sind gemeinsam mit den
Account-Namen eindeutig.

Optionen:
  --description text   Kurzbeschreibung
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
			return c.hubDo(a, func(ctx context.Context, s hubstore.Store, pos []string) error {
				token, err := s.AddNode(ctx, pos[0], *desc)
				if err != nil {
					return err
				}
				fmt.Fprintf(stdout, "Node %s angelegt.\n", pos[0])
				printToken(stdout, token, "kephalaion node hub add <alias> --transport … --node "+pos[0]+" --token-stdin")
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
			return c.hubDo(a, func(ctx context.Context, s hubstore.Store, pos []string) error {
				token, err := s.NewNodeToken(ctx, pos[0])
				if err != nil {
					return err
				}
				fmt.Fprintf(stdout, "Node %s: neues Token, das alte gilt nicht mehr.\n", pos[0])
				printToken(stdout, token, "kephalaion node hub token <alias> --token-stdin")
				fmt.Fprintln(stdout, "Bis dahin weist der Hub den Node ab.")
				return nil
			})
		},
	})
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
