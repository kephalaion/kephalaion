package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/kephalaion/kephalaion/internal/contract"
	hubstore "github.com/kephalaion/kephalaion/internal/hub/store"
)

const hubAccountUsage = `Aufruf:
  kephalaion hub account add    <name> [--user user] [--description text]
  kephalaion hub account list   [--user user]
  kephalaion hub account show   <name>
  kephalaion hub account set    <name> [--user user] [--description text]
  kephalaion hub account rm     <name>
  kephalaion hub account lock   <name>
  kephalaion hub account unlock <name>
  kephalaion hub account grant  <name> <collection> [--write] [--supersede]
                                [--vendor <name>]… [--dir <pfad>]…
  kephalaion hub account revoke <name> <collection>
  kephalaion hub account token  <name>

Kommandos:
  add      legt einen Account ohne Collections an und zeigt sein
           Einrichtungstoken — genau einmal
  list     zeigt alle Accounts, mit --user nur die eines Users
  show     zeigt einen Account samt User und Rechten je Collection
  set      ändert User und/oder Beschreibung; ein neuer User steht danach in
           allen Zeilen des Accounts (ein Schreibvorgang, eine Revision) —
           vorhandene Dokumente behalten ihren User
  rm       entfernt einen Account; seine Zeilen werden Löschmarken, der Name
           ist danach wieder frei
  lock     sperrt einen Account: seine Zeilen werden Löschmarken, die Rechte
           merkt sich der Hub; unlock legt sie wieder an
  grant    setzt die Rechte in einer Collection vollständig: read immer, dazu
           --write (Eigenes anlegen, ändern, löschen), --supersede
           (Fremdes ändern, ablösen, löschen) und je --vendor <name> der Scope
           vendor/<name> (unter vendor/<name>/ schreiben — dort zählt allein
           er, ohne write und unabhängig vom Urheber; direkt in vendor/
           schreibt über einen Node niemand) und je --dir <pfad> ein
           Verzeichnis-Scope (unter <pfad>/ schreiben, auch ohne write und
           unabhängig vom Urheber; zusätzlich zu write und supersede, nimmt
           niemandem etwas; Ziel für kephalaion node dir push); ohne --write
           wird write entzogen, ohne --supersede ebenso supersede, ohne
           --vendor jeder Scope vendor/<name>, ohne --dir jeder
           Verzeichnis-Scope
  revoke   nimmt dem Account die Collection
  token    erzeugt ein neues Einrichtungstoken und zeigt es einmal; das alte
           gilt nicht mehr

Das Einrichtungstoken ist das erste Token des Accounts. Sein erster Vorgang
tauscht es gegen ein eigenes: rotate über einen Node, der eine seiner
Collections abgleichen darf. Das macht der User selbst auf seinem Rechner,
mit einem Aufruf, auch ohne eigenen Node (kephalaion node account setup <hub>
<account> <token>; über einen Node auf einem anderen Rechner mit --node
https://<name>/kephalaion); am eigenen Node geht auch kephalaion node account
rotate. Danach ist es wertlos. Gespeichert wird nur der Hash. Account-Namen folgen den
Regeln für Collections, admin ist reserviert, und sie sind gemeinsam mit den
Node-Namen eindeutig. Jede Änderung an den Rechten ist ein Schreibvorgang mit
Revision und gleicht sich zu den Nodes ab; sie steht im Protokoll (actions) als
admin.

Der User ist, wem der Account gehört — ein Merkmal, kein Zugang: kein Token,
keine Rechte. Ohne --user ist er der Name des Accounts. Mehrere Accounts
können denselben User haben. Namensregel wie bei Accounts, admin ist
reserviert; ein User darf wie ein Node heißen. Er steht in created_by und
updated_by dessen, was der Account schreibt.

Optionen:
  --user user          User des Accounts (add: ohne Angabe der Name des
                       Accounts; list: nur die Accounts dieses Users)
  --description text   Kurzbeschreibung
  --write              Recht write (bei grant)
  --supersede          Recht supersede (bei grant)
  --vendor name        Scope vendor/<name> (bei grant, wiederholbar); <name>
                       folgt der Namensregel für Collections
  --dir pfad           Verzeichnis-Scope <pfad> (bei grant, wiederholbar): ein
                       Verzeichnis der Collection, ein '/' am Ende ist
                       erlaubt; nicht die Wurzel, nicht vendor und nichts
                       darunter
  --config pfad        Ort der config (siehe kephalaion hub init --help)
`

func runHubAccount(args []string, stdout, stderr io.Writer) int {
	u := hubAccountUsage
	simple := func(name, done string, fn func(ctx context.Context, s hubstore.Store, account string) error) func([]string) int {
		return func(a []string) int {
			c := newCommand("hub account "+name, u, stdout, stderr, "<name>")
			return c.hubDo(a, func(ctx context.Context, s hubstore.Store, pos []string) error {
				if err := fn(ctx, s, pos[0]); err != nil {
					return err
				}
				fmt.Fprintf(stdout, "Account %s %s.\n", pos[0], done)
				return nil
			})
		}
	}
	return dispatch("hub account", u, args, stdout, stderr, map[string]func([]string) int{
		"add": func(a []string) int {
			c := newCommand("hub account add", u, stdout, stderr, "<name>")
			desc := c.fs.String("description", "", "")
			user := c.fs.String("user", "", "")
			return c.hubDo(a, func(ctx context.Context, s hubstore.Store, pos []string) error {
				if !c.isSet("user") {
					*user = pos[0]
				}
				token, err := s.AddAccount(ctx, pos[0], *user, *desc)
				if err != nil {
					return err
				}
				fmt.Fprintf(stdout, "Account %s angelegt (User %s), noch ohne Collections (kephalaion hub account grant).\n", pos[0], *user)
				printAccountToken(stdout, token, pos[0])
				return nil
			})
		},
		"list": func(a []string) int {
			c := newCommand("hub account list", u, stdout, stderr)
			user := c.fs.String("user", "", "")
			return c.hubDo(a, func(ctx context.Context, s hubstore.Store, _ []string) error {
				var accounts []hubstore.Account
				var err error
				if c.isSet("user") {
					if err := hubstore.CheckUser(*user); err != nil {
						return err
					}
					accounts, err = s.AccountsOfUser(ctx, *user)
				} else {
					accounts, err = s.Accounts(ctx)
				}
				if err != nil {
					return err
				}
				if len(accounts) == 0 {
					if c.isSet("user") {
						fmt.Fprintf(stdout, "Keine Accounts des Users %s.\n", *user)
					} else {
						fmt.Fprintln(stdout, "Keine Accounts.")
					}
					return nil
				}
				tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
				fmt.Fprintln(tw, "NAME\tUSER\tSTATUS\tRECHTE\tBESCHREIBUNG")
				for _, acc := range accounts {
					fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", acc.Name, acc.User, lockState(acc.Locked), rightsSummary(acc.Rights), orDash(acc.Description))
				}
				return tw.Flush()
			})
		},
		"show": func(a []string) int {
			c := newCommand("hub account show", u, stdout, stderr, "<name>")
			return c.hubDo(a, func(ctx context.Context, s hubstore.Store, pos []string) error {
				acc, err := s.Account(ctx, pos[0])
				if err != nil {
					return err
				}
				fmt.Fprintf(stdout, "Account %s\n", acc.Name)
				fmt.Fprintf(stdout, "  User:         %s\n", acc.User)
				fmt.Fprintf(stdout, "  Beschreibung: %s\n", orDash(acc.Description))
				fmt.Fprintf(stdout, "  Status:       %s\n", lockState(acc.Locked))
				label := "Rechte:"
				if acc.Locked {
					label = "Rechte (gemerkt, gelten erst nach unlock):"
				}
				if len(acc.Rights) == 0 {
					fmt.Fprintf(stdout, "  %s keine\n", label)
				} else {
					fmt.Fprintf(stdout, "  %s\n", label)
					for _, r := range acc.Rights {
						fmt.Fprintf(stdout, "    %s: %s\n", r.Collection, r.Rights)
					}
				}
				fmt.Fprintf(stdout, "  Token:        nur als Hash gespeichert\n")
				fmt.Fprintf(stdout, "  angelegt:     %s von %s\n", formatMillis(acc.CreatedAt), acc.CreatedBy)
				return nil
			})
		},
		"set": func(a []string) int {
			c := newCommand("hub account set", u, stdout, stderr, "<name>")
			desc := c.fs.String("description", "", "")
			user := c.fs.String("user", "", "")
			return c.hubDo(a, func(ctx context.Context, s hubstore.Store, pos []string) error {
				var ch hubstore.AccountChange
				if c.isSet("description") {
					ch.Description = desc
				}
				if c.isSet("user") {
					ch.User = user
				}
				if ch.Description == nil && ch.User == nil {
					return fmt.Errorf("nichts zu ändern; erwartet --user und/oder --description")
				}
				if err := s.SetAccount(ctx, pos[0], ch); err != nil {
					return err
				}
				fmt.Fprintf(stdout, "Account %s geändert.\n", pos[0])
				return nil
			})
		},
		"rm": simple("rm", "entfernt; seine Zeilen sind Löschmarken", func(ctx context.Context, s hubstore.Store, n string) error {
			return s.RemoveAccount(ctx, n)
		}),
		"lock": simple("lock", "gesperrt; die Rechte sind gemerkt", func(ctx context.Context, s hubstore.Store, n string) error {
			return s.SetAccountLocked(ctx, n, true)
		}),
		"unlock": simple("unlock", "entsperrt", func(ctx context.Context, s hubstore.Store, n string) error {
			return s.SetAccountLocked(ctx, n, false)
		}),
		"grant": func(a []string) int {
			c := newCommand("hub account grant", u, stdout, stderr, "<name>", "<collection>")
			write := c.fs.Bool("write", false, "")
			supersede := c.fs.Bool("supersede", false, "")
			var vendor, dirs stringList
			c.fs.Var(&vendor, "vendor", "")
			c.fs.Var(&dirs, "dir", "")
			return c.hubDo(a, func(ctx context.Context, s hubstore.Store, pos []string) error {
				// Ein '/' am Ende ist erlaubt, wie bei einem Verzeichnis sonst;
				// gespeichert wird der Name ohne. "/" ist die Wurzel — leer,
				// kein Scope.
				for i, d := range dirs {
					dirs[i] = strings.TrimSuffix(d, "/")
				}
				rights, err := contract.NormalizeRights(contract.Rights{Write: *write, Supersede: *supersede, Vendor: vendor, Dirs: dirs})
				if err != nil {
					return err
				}
				changed, err := s.GrantAccount(ctx, pos[0], pos[1], rights)
				if err != nil {
					return err
				}
				if !changed {
					fmt.Fprintf(stdout, "Account %s: %s unverändert (%s).\n", pos[0], pos[1], rights)
					return nil
				}
				fmt.Fprintf(stdout, "Account %s: %s erlaubt (%s).\n", pos[0], pos[1], rights)
				return nil
			})
		},
		"revoke": func(a []string) int {
			c := newCommand("hub account revoke", u, stdout, stderr, "<name>", "<collection>")
			return c.hubDo(a, func(ctx context.Context, s hubstore.Store, pos []string) error {
				if err := s.RevokeAccount(ctx, pos[0], pos[1]); err != nil {
					return err
				}
				fmt.Fprintf(stdout, "Account %s: %s nicht mehr erlaubt.\n", pos[0], pos[1])
				return nil
			})
		},
		"token": func(a []string) int {
			c := newCommand("hub account token", u, stdout, stderr, "<name>")
			return c.hubDo(a, func(ctx context.Context, s hubstore.Store, pos []string) error {
				token, err := s.NewAccountToken(ctx, pos[0])
				if err != nil {
					return err
				}
				fmt.Fprintf(stdout, "Account %s: neues Einrichtungstoken, das alte gilt nicht mehr.\n", pos[0])
				printAccountToken(stdout, token, pos[0])
				return nil
			})
		},
	})
}

// stringList ist eine wiederholbare Option: jeder Wert kommt dazu.
type stringList []string

func (l *stringList) String() string     { return strings.Join(*l, ",") }
func (l *stringList) Set(v string) error { *l = append(*l, v); return nil }

// printAccountToken zeigt ein Einrichtungstoken — das einzige Mal — und wie
// der Account es gegen sein eigenes tauscht.
func printAccountToken(w io.Writer, token, account string) {
	fmt.Fprintln(w, "Einrichtungstoken (wird nicht wieder angezeigt, gespeichert ist nur der Hash):")
	fmt.Fprintf(w, "  %s\n", token)
	fmt.Fprintln(w, "Als Erstes gegen ein eigenes Token tauschen, danach ist es wertlos. Der User")
	fmt.Fprintln(w, "richtet den Account damit auf seinem Rechner selbst ein (<hub> ist der Alias")
	fmt.Fprintln(w, "des Hub-Eintrags am Node; ohne Node in seiner config mit --node <url>):")
	fmt.Fprintf(w, "  kephalaion node account setup <hub> %s <einrichtungstoken>\n", account)
	fmt.Fprintln(w, "Am eigenen Node über eine Datei oder stdin, nie als Argument:")
	fmt.Fprintf(w, "  kephalaion node account rotate <hub> %s --token-file <pfad>\n", account)
}

// rightsSummary fasst die Rechte eines Accounts in einer Zeile zusammen:
// „privat (read), team-x (read, write)“.
func rightsSummary(rights []hubstore.AccountRight) string {
	if len(rights) == 0 {
		return "keine"
	}
	parts := make([]string, 0, len(rights))
	for _, r := range rights {
		parts = append(parts, fmt.Sprintf("%s (%s)", r.Collection, r.Rights))
	}
	return strings.Join(parts, ", ")
}
