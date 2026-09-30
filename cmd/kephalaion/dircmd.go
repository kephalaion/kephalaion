package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/kephalaion/kephalaion/internal/config"
	"github.com/kephalaion/kephalaion/internal/contract"
	"github.com/kephalaion/kephalaion/internal/dirsync"
	"github.com/kephalaion/kephalaion/internal/ident"
	"github.com/kephalaion/kephalaion/internal/node/mcpnode"
)

const nodeDirUsage = `Aufruf:
  kephalaion node dir push <hub>:<collection> <verzeichnis> <lokaler-ordner>
                           [--last pfad] [--exclude glob]… [--dry-run] [--timeout dauer]
  kephalaion node dir pull <hub>:<collection> <verzeichnis> <lokaler-ordner>
                           [--delete] [--exclude glob]… [--dry-run] [--timeout dauer]
  Anmeldung: [--node url] [--account name] [--token-file pfad | --token-stdin]

Gleicht einen lokalen Ordner mit einem Verzeichnis einer Collection ab — als
Client des Nodes über MCP, mit Account und Token des Aufrufers. Verglichen
wird der Inhalt, nicht das Datum. Der Abgleich besteht aus Einzelvorgängen
(list, read, create, write, delete), je Ebene nach Name: erst löschen, was
fehlt oder die andere Art hat, dann anlegen und schreiben, dann absteigen.
Ein zweiter Lauf ändert nur, was noch abweicht.

Kommandos:
  push   ersetzt den Inhalt von <verzeichnis> durch den des Ordners: was dort
         fehlt, wird gelöscht. Nur unter vendor/<name>/ und in Verzeichnissen,
         für die der Account einen Verzeichnis-Scope hat (am Hub: kephalaion
         hub account grant … --dir <pfad>) — gleich dem Scope oder darunter,
         nie an die Wurzel. Die Scopes fragt push vor dem ersten Vorgang beim
         Node ab (whoami, auch mit --dry-run); der Node kennt den Stand des
         letzten Abgleichs — nach einer neuen Freigabe oder einem Entzug erst
         kephalaion node sync <hub>. Der Hub prüft jeden Vorgang trotzdem
         selbst. Ein anderes Ziel ist ein falscher Aufruf. Vorab liest
         push den ganzen Ordner ein: Jede Datei muss UTF-8 ohne NUL und
         höchstens 1 MiB sein und einen gültigen Namen haben — sonst bricht
         push mit allen Treffern ab, ohne zu schreiben (--exclude). Symlinks
         und andere Nicht-Dateien werden übergangen und gemeldet; leere
         Ordner entstehen im Store nicht.
  pull   holt <verzeichnis> in den Ordner: Dateien schreiben, deren Inhalt
         abweicht oder die fehlen (neu 0644, Ordner 0755), lokal gelöscht
         wird nur mit --delete. Nie außerhalb des Ordners; keinem Symlink
         darin wird gefolgt — an einer Stelle, die geschrieben werden müsste,
         ist er ein gemeldeter Fehler. Ändert sich der Store während des
         Lesens, beginnt pull von vorn (höchstens dreimal).

.git und Treffer von --exclude bleiben in Quelle und Ziel unberührt: weder
angelegt noch geändert noch gelöscht, auch nicht mit --delete.

Abbrechen (SIGINT, SIGTERM) und --timeout wirken zwischen zwei Vorgängen: Der
laufende geht zu Ende, dann meldet die Kommandozeile, wie weit sie kam —
erneut ausführen setzt fort. stale_revision, name_taken, path_conflict,
not_found (jemand schrieb dazwischen) und ein unklarer Ausgang werden
gemeldet, nicht wiederholt; der Lauf geht weiter und endet unvollständig.
forbidden, not_readable und ein nicht erreichbarer Node brechen ab.

Anmeldung:
  Die Adresse des Nodes kommt aus listen im Abschnitt node: der config
  (--config), sonst --node <url> (etwa im Devcontainer). Der Account ist
  --account, ohne Angabe der einzige unter <config-dir>/tokens/<hub>/
  (<account>.token; .pending wird übergangen); bei mehreren nennt der Fehler
  sie. Das Token kommt aus dieser Datei, --token-file oder --token-stdin —
  nie als Argument, nie in einer Ausgabe.

Ausgabe: eine Zeile je Vorgang (+ angelegt, ~ geändert, - gelöscht,
! gemeldet oder übergangen), zuletzt der Bericht: angelegt, geändert,
gelöscht, unverändert, übergangen, gemeldet, Dauer.

Exit-Codes:
  0   fertig, vollständig
  1   Fehler (Vorabprüfung, Node nicht erreichbar, verboten, nicht lesbar)
  2   falscher Aufruf, auch ein Ziel von push ohne Freigabe (nichts
      geschrieben)
  3   unvollständig: abgebrochen, Höchstzeit oder gemeldete Konflikte —
      erneut ausführen

Optionen:
  --node url           Adresse des Nodes, http://127.0.0.1:<port>; sonst aus
                       der config
  --account name       der Account; sonst der einzige mit Token-Datei
  --token-file pfad    Datei mit dem Token (eine Zeile)
  --token-stdin        das Token als eine Zeile von der Standardeingabe
  --exclude glob       Namen (Datei oder Ordner, jede Ebene) auslassen;
                       wiederholbar, Glob wie bei mask, ohne '/'
  --last pfad          push: diese Datei zuletzt schreiben (relativ zum
                       Ordner, mit '/'), nur nach einem Lauf ohne Fehler —
                       etwa VERSION als Zeichen eines vollständigen Laufs
  --delete             pull: lokal löschen, was im Store fehlt (auch Ordner)
  --dry-run            nur lesen und melden, was geschähe
  --timeout dauer      Höchstzeit, etwa 10m; danach zwischen zwei Vorgängen
                       Schluss (Exit 3)
  --config pfad        Ort der config (siehe kephalaion node init --help)
`

// signalContext ist der ctx, den SIGINT und SIGTERM beenden; Tests ersetzen
// ihn.
var signalContext = func(ctx context.Context) (context.Context, context.CancelFunc) {
	return signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
}

// exitIncomplete ist der Exit-Code eines unvollständigen Laufs.
const exitIncomplete = 3

func runNodeDir(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	u := nodeDirUsage
	leaf := func(name string, pull bool) func([]string) int {
		return func(a []string) int {
			c := newCommand("node dir "+name, u, stdout, stderr, "<hub>:<collection>", "<verzeichnis>", "<lokaler-ordner>")
			d := &dirCommand{c: c, pull: pull, stdin: stdin}
			d.node = c.fs.String("node", "", "")
			d.account = c.fs.String("account", "", "")
			d.tokenFile = c.fs.String("token-file", "", "")
			d.tokenStdin = c.fs.Bool("token-stdin", false, "")
			c.fs.Var(&d.exclude, "exclude", "")
			d.dryRun = c.fs.Bool("dry-run", false, "")
			d.timeout = c.fs.Duration("timeout", 0, "")
			if pull {
				d.del = c.fs.Bool("delete", false, "")
			} else {
				d.last = c.fs.String("last", "", "")
			}
			pos, code, ok := c.parse(a)
			if !ok {
				return code
			}
			return d.run(pos)
		}
	}
	return dispatch("node dir", u, args, stdout, stderr, map[string]func([]string) int{
		"push": leaf("push", false),
		"pull": leaf("pull", true),
	})
}

// dirCommand ist ein Aufruf von push oder pull mit seinen Optionen.
type dirCommand struct {
	c          *command
	pull       bool
	stdin      io.Reader
	node       *string
	account    *string
	tokenFile  *string
	tokenStdin *bool
	exclude    stringList
	dryRun     *bool
	timeout    *time.Duration
	del        *bool
	last       *string
}

// usageError ist ein falscher Aufruf: Meldung und Exit-Code 2.
func (d *dirCommand) usageError(format string, a ...any) int {
	fmt.Fprintf(d.c.stderr, "%s: "+format+"\n", append([]any{d.c.name}, a...)...)
	return 2
}

func (d *dirCommand) run(pos []string) int {
	hub, collection, err := ident.ParseAddress(pos[0])
	if err != nil {
		return d.usageError("%v", err)
	}
	dir, local := pos[1], pos[2]
	if _, err := ident.DocDirPrefix(dir); err != nil {
		return d.usageError("%v", err)
	}
	// push: vendor/<name> ohne Verbindung; jedes andere Ziel prüft
	// checkDirScope nach dem Verbinden.
	var vendor bool
	if !d.pull {
		if vendor, err = dirsync.CheckPushDir(dir); err != nil {
			return d.usageError("%v", err)
		}
	}
	opts := dirsync.Options{Exclude: d.exclude, DryRun: *d.dryRun, Out: d.c.stdout}
	if d.last != nil {
		opts.Last = *d.last
	}
	if d.del != nil {
		opts.Delete = *d.del
	}
	if *d.timeout < 0 {
		return d.usageError("--timeout %s: erwartet eine Dauer ab 0", *d.timeout)
	}
	if *d.tokenFile != "" && *d.tokenStdin {
		return d.usageError("--token-file und --token-stdin schließen sich aus; das Token wird nie als Argument übergeben")
	}
	if (*d.tokenFile != "" || *d.tokenStdin) && *d.account == "" {
		return d.usageError("--account fehlt: mit --token-file oder --token-stdin ist der Account anzugeben")
	}
	if *d.account != "" {
		if err := ident.CheckPrincipalName("Account", *d.account); err != nil {
			return d.usageError("%v", err)
		}
	}
	endpoint, err := d.endpoint()
	if err != nil {
		return d.c.fail(err)
	}
	account, token, err := d.credentials(hub)
	if err != nil {
		return d.c.fail(err)
	}

	ctx, stop := signalContext(context.Background())
	defer stop()
	if *d.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *d.timeout)
		defer cancel()
	}
	// Die Verbindung selbst und jeder Vorgang laufen ohne den Abbruch: Der
	// laufende Vorgang geht zu Ende.
	tgt, closeNode, err := connectNode(context.WithoutCancel(ctx), endpoint, hub, collection, account, token)
	if err != nil {
		return d.c.fail(err)
	}
	defer closeNode()
	if !d.pull && !vendor {
		if code, ok := d.checkDirScope(context.WithoutCancel(ctx), tgt, dir); !ok {
			return code
		}
	}
	what := fmt.Sprintf("%s %s:%s %s", d.c.name[len("node dir "):], hub, collection, dirsync.DirName(dir))
	if opts.DryRun {
		fmt.Fprintf(d.c.stdout, "%s (dry-run, nichts wird geschrieben):\n", what)
	}
	var rep *dirsync.Report
	if d.pull {
		rep, err = dirsync.Pull(ctx, tgt, dir, local, opts)
	} else {
		rep, err = dirsync.Push(ctx, tgt, dir, local, opts)
	}
	if err != nil {
		var pe *dirsync.PrecheckError
		if !errors.As(err, &pe) && rep != nil && rep.Duration > 0 {
			fmt.Fprintf(d.c.stdout, "%s: %s; abgebrochen\n", what, rep.Summary())
		}
		return d.c.fail(err)
	}
	fmt.Fprintf(d.c.stdout, "%s: %s\n", what, rep.Summary())
	if !rep.Complete() {
		return exitIncomplete
	}
	return 0
}

// checkDirScope prüft das Ziel von push außerhalb von vendor/ gegen die
// Verzeichnis-Scopes des Accounts in der Collection, wie der Node sie kennt
// (whoami, Stand des letzten Abgleichs): gleich einem oder darunter. Sonst
// ein falscher Aufruf mit den freigegebenen Verzeichnissen und dem Weg zur
// Freigabe — bevor irgendetwas geschrieben wird, auch bei --dry-run.
func (d *dirCommand) checkDirScope(ctx context.Context, tgt *mcpTarget, dir string) (code int, ok bool) {
	dirs, err := tgt.DirScopes(ctx)
	if err != nil {
		return d.c.fail(err), false
	}
	if dirsync.CoveredByDirScope(dir, dirs) {
		return 0, true
	}
	granted := "keine"
	if len(dirs) > 0 {
		labels := make([]string, len(dirs))
		for i, s := range dirs {
			labels[i] = s + "/"
		}
		granted = strings.Join(labels, ", ")
	}
	return d.usageError("push schreibt nur unter %s/<name>/ und in Verzeichnisse mit Verzeichnis-Scope; %s hat in %s "+
		"keinen für %s (freigegeben: %s).\n"+
		"Freigeben am Hub: kephalaion hub account grant %s %s --dir <pfad> (grant setzt die Rechte vollständig — "+
		"die übrigen wiederholen).\n"+
		"Eine eben erteilte Freigabe kennt der Node erst nach dem Abgleich: kephalaion node sync %s",
		contract.VendorDir, tgt.account, tgt.addr, dirsync.DirName(dir), granted, tgt.account, tgt.collection, tgt.hub), false
}

// endpoint ist die Adresse des MCP-Eingangs: --node, sonst listen im
// Abschnitt node: der config.
func (d *dirCommand) endpoint() (string, error) {
	if *d.node != "" {
		u := strings.TrimRight(*d.node, "/")
		if !strings.HasPrefix(u, "http://") {
			return "", fmt.Errorf("--node %q: erwartet http://<host>:<port> (nur dieser Rechner)", *d.node)
		}
		return u + mcpnode.Path, nil
	}
	loc, err := config.Locate(*d.c.cfg)
	if err != nil {
		return "", err
	}
	cfg, _, err := config.Load(loc.Path)
	if err != nil {
		return "", err
	}
	listen := cfg.Listen(config.Node)
	if listen == "" {
		return "", fmt.Errorf("kein Node in der config %s; Adresse mit --node <url> angeben", loc.Path)
	}
	if strings.HasPrefix(listen, ":") {
		listen = "127.0.0.1" + listen
	}
	return "http://" + listen + mcpnode.Path, nil
}

// tokensDir ist das Verzeichnis der Token-Dateien: tokens/ neben der config
// des Users, wie die Erweiterung für VS Code es liest.
func tokensDir() (string, error) {
	p, err := config.UserPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(p), "tokens"), nil
}

// credentials liefert Account und Token: --token-file oder --token-stdin
// mit --account, sonst die Token-Datei unter tokens/<hub>/ — die des
// Accounts oder die einzige.
func (d *dirCommand) credentials(hub string) (account, token string, err error) {
	switch {
	case *d.tokenFile != "":
		token, err = readTokenFile(*d.tokenFile)
		return *d.account, token, err
	case *d.tokenStdin:
		token, err = readToken(d.stdin)
		return *d.account, token, err
	}
	base, err := tokensDir()
	if err != nil {
		return "", "", err
	}
	dir := filepath.Join(base, hub)
	account = *d.account
	if account == "" {
		names, err := tokenAccounts(dir)
		if err != nil {
			return "", "", err
		}
		switch len(names) {
		case 0:
			return "", "", fmt.Errorf("keine Token-Datei unter %s (<account>.token); --account mit --token-file oder --token-stdin", dir)
		case 1:
			account = names[0]
		default:
			return "", "", fmt.Errorf("mehrere Token-Dateien unter %s: %s; --account wählt", dir, strings.Join(names, ", "))
		}
	}
	token, err = readTokenFile(filepath.Join(dir, account+".token"))
	return account, token, err
}

// tokenAccounts nennt die Accounts mit Token-Datei in dir (<account>.token,
// .pending übergangen), nach Name; ein fehlendes dir ist leer.
func tokenAccounts(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if name, ok := strings.CutSuffix(e.Name(), ".token"); ok && !e.IsDir() {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out, nil
}
