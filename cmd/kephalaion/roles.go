package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/kephalaion/kephalaion/internal/config"
	hubstore "github.com/kephalaion/kephalaion/internal/hub/store"
	"github.com/kephalaion/kephalaion/internal/node/replica"
	nodestore "github.com/kephalaion/kephalaion/internal/node/store"
	"github.com/kephalaion/kephalaion/internal/sqlitedb"
)

// roleTitle ist der Name einer Rolle am Satzanfang.
func roleTitle(r config.Role) string {
	switch r {
	case config.Hub:
		return "Hub"
	case config.Node:
		return "Node"
	}
	return string(r)
}

// roleStore ist, was Hub- und Node-Datenbank für die CLI gemeinsam haben.
type roleStore interface {
	Settings(ctx context.Context) (map[string]string, error)
	ReplaceSettings(ctx context.Context, settings map[string]string) error
	Close() error
}

// openRole öffnet die vorhandene Datenbank einer Rolle.
func openRole(ctx context.Context, r config.Role, addr config.DB) (roleStore, error) {
	switch r {
	case config.Hub:
		s, err := hubstore.Open(ctx, addr)
		if err != nil {
			return nil, err
		}
		return s, nil
	case config.Node:
		s, err := nodestore.Open(ctx, addr)
		if err != nil {
			return nil, err
		}
		return s, nil
	}
	return nil, fmt.Errorf("unbekannte Rolle %q", r)
}

// openSection zerlegt die db-Adresse aus der config und öffnet die Datenbank.
func openSection(ctx context.Context, r config.Role, s *config.Section) (roleStore, error) {
	addr, err := config.ParseDB(s.DB)
	if err != nil {
		return nil, err
	}
	return openRole(ctx, r, addr)
}

// createRole legt die Datenbank einer Rolle neu an und liefert ihre
// Schemafassung.
func createRole(ctx context.Context, r config.Role, addr config.DB) (int, error) {
	switch r {
	case config.Hub:
		s, err := hubstore.Create(ctx, addr)
		if err != nil {
			return 0, err
		}
		return hubstore.SchemaVersion, s.Close()
	case config.Node:
		s, err := nodestore.Create(ctx, addr)
		if err != nil {
			return 0, err
		}
		return nodestore.SchemaVersion, s.Close()
	}
	return 0, fmt.Errorf("unbekannte Rolle %q", r)
}

// removeDB entfernt eine frisch angelegte Datenbank wieder.
func removeDB(addr config.DB) error {
	if addr.Kind == config.SQLite {
		return sqlitedb.Remove(addr.Path)
	}
	return nil
}

// newFlagSet liefert ein FlagSet, das bei Fehlern die Hilfe ausgibt.
func newFlagSet(name, usage string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, usage) }
	return fs
}

// parseFlags wertet die Optionen aus, vor und nach Positionsargumenten:
// `hub node add laptop --description …` geht wie `hub node add --description …
// laptop`. Nach `--` ist alles Positionsargument. pos sind die
// Positionsargumente; ok ist false, wenn das Kommando mit code enden soll.
func parseFlags(fs *flag.FlagSet, args []string, usage string, maxArgs int, stderr io.Writer) (pos []string, code int, ok bool) {
	rest := args
	for {
		if err := fs.Parse(rest); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil, 0, false
			}
			return nil, 2, false
		}
		next := fs.Args()
		if len(next) == 0 {
			break
		}
		// Hat Parse an `--` aufgehört, ist der Rest Positionsargument.
		if used := len(rest) - len(next); used > 0 && rest[used-1] == "--" {
			pos = append(pos, next...)
			break
		}
		pos = append(pos, next[0])
		rest = next[1:]
	}
	if len(pos) > maxArgs {
		fmt.Fprintf(stderr, "Unerwartetes Argument: %s\n\n", pos[maxArgs])
		fmt.Fprint(stderr, usage)
		return nil, 2, false
	}
	return pos, 0, true
}

const hubUsage = `Aufruf:
  kephalaion hub init [--db sqlite:///pfad/hub.db] [--config pfad]
  kephalaion hub collection add|list|set|rm …
  kephalaion hub node add|list|show|set|rm|lock|unlock|grant|revoke|token …
  kephalaion hub account add|list|show|set|rm|lock|unlock|grant|revoke|token …
  kephalaion hub doc put|get|list|rm …
  kephalaion hub import <collection> <verzeichnis> [--prefix pfad/]

Kommandos:
  init         richtet den Hub ein: Datenbank, Schema, Abschnitt hub: in der config
  collection   legt die Collections des Hubs an, ändert und entfernt sie
  node         legt Nodes an, erlaubt ihnen Collections, sperrt sie, erneuert
               ihr Token
  account      legt Accounts an, setzt ihre Rechte je Collection, sperrt sie,
               erzeugt ein Einrichtungstoken
  doc          legt Dokumente an, ersetzt, liest, listet und löscht sie
  import       spielt ein Verzeichnis als Dokumente ein, in einer Revision

Hilfe: kephalaion hub collection --help, kephalaion hub node --help,
kephalaion hub account --help, kephalaion hub doc --help,
kephalaion hub import --help
`

const nodeUsage = `Aufruf:
  kephalaion node init [--db sqlite:///pfad/node.db] [--config pfad]
  kephalaion node hub add|check|list|show|set|rm|token …
  kephalaion node collection add|list|rm …
  kephalaion node account rotate|check …
  kephalaion node sync [<alias>]
  kephalaion node doc list|get …
  kephalaion node dir push|pull …
  kephalaion node whoami [<account>] [--hub <alias>] [--json]

Kommandos:
  init         richtet den Node ein: Datenbank, Schema, Abschnitt node: in der config
  hub          trägt die Hubs dieses Nodes ein: Transport, Adresse, Token;
               check fragt einen Hub, wer der Node für ihn ist
  collection   die Collections, die der Node von seinen Hubs haben will
  account      ersetzt das Token eines Accounts am Hub (rotate) und prüft es
               (check)
  sync         gleicht die Replicas mit den Hubs ab (Transport local und http);
               serve tut das im Hintergrund selbst
  doc          listet und liest Dokumente aus der Replica
  dir          gleicht einen lokalen Ordner mit einem Verzeichnis einer
               Collection ab, als Client des Nodes über MCP: push ersetzt den
               Inhalt des Verzeichnisses (vorerst nur unter vendor/), pull
               holt ihn
  whoami       zeigt Version, Hubs, Stand des Abgleichs und die bekannten
               Accounts; mit Account, was das Werkzeug whoami ihm antwortet

Hilfe: kephalaion node hub --help, kephalaion node collection --help,
kephalaion node account --help, kephalaion node sync --help,
kephalaion node doc --help, kephalaion node dir --help,
kephalaion node whoami --help
`

// runRole verteilt die Kommandos unter hub bzw. node.
func runRole(r config.Role, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	usage := hubUsage
	if r == config.Node {
		usage = nodeUsage
	}
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch {
	case args[0] == "help" || args[0] == "-h" || args[0] == "--help":
		fmt.Fprint(stdout, usage)
		return 0
	case args[0] == "init":
		return runInit(r, args[1:], stdout, stderr)
	case r == config.Hub && args[0] == "collection":
		return runHubCollection(args[1:], stdout, stderr)
	case r == config.Hub && args[0] == "node":
		return runHubNode(args[1:], stdout, stderr)
	case r == config.Hub && args[0] == "account":
		return runHubAccount(args[1:], stdout, stderr)
	case r == config.Hub && args[0] == "doc":
		return runHubDoc(args[1:], stdin, stdout, stderr)
	case r == config.Hub && args[0] == "import":
		return runHubImport(args[1:], stdout, stderr)
	case r == config.Node && args[0] == "hub":
		return runNodeHub(args[1:], stdin, stdout, stderr)
	case r == config.Node && args[0] == "collection":
		return runNodeCollection(args[1:], stdout, stderr)
	case r == config.Node && args[0] == "account":
		return runNodeAccount(args[1:], stdin, stdout, stderr)
	case r == config.Node && args[0] == "sync":
		return runNodeSync(args[1:], stdout, stderr)
	case r == config.Node && args[0] == "doc":
		return runNodeDoc(args[1:], stdout, stderr)
	case r == config.Node && args[0] == "dir":
		return runNodeDir(args[1:], stdin, stdout, stderr)
	case r == config.Node && args[0] == "whoami":
		return runNodeWhoami(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "Unbekanntes Kommando: %s %s\n\n", r, args[0])
		fmt.Fprint(stderr, usage)
		return 2
	}
}

func initUsage(r config.Role) string {
	return fmt.Sprintf(`Aufruf:
  kephalaion %[1]s init [--db sqlite:///pfad/%[1]s.db] [--listen host:port] [--config pfad]

Richtet die Rolle %[1]s ein: legt die Datenbank samt Schema an und trägt den
Abschnitt %[1]s: in die config ein, die bei Bedarf entsteht — mit db und
listen. Keine Rückfragen. Steht die Rolle schon in der config oder gibt es die
Datenbankdatei schon, bricht init ab, statt zu überschreiben.

Optionen:
  --db adresse        Datenbank, sqlite:///<absoluter Pfad>; ohne Angabe
                      $XDG_DATA_HOME/kephalaion/%[1]s.db bzw.
                      ~/.local/share/kephalaion/%[1]s.db
                      (postgres://… ist noch nicht unterstützt)
  --listen host:port  wo kephalaion serve für diese Rolle lauscht; ohne Angabe
                      %[2]s (nur dieser Rechner). serve lauscht bisher
                      nur auf 127.0.0.1, ::1 oder localhost.
  --config pfad       Ort der config; sonst $KEPHALAION_CONFIG,
                      $XDG_CONFIG_HOME/kephalaion/config.yaml bzw.
                      ~/.config/kephalaion/config.yaml

Ohne --config und ohne KEPHALAION_CONFIG richtet init nur pro User ein: Gibt
es die globale config %[3]s, bricht es ab — Kephalaion ist
dann global eingerichtet und wird als Systembenutzer %[4]s verwaltet.

Gefunden wird die config sonst in dieser Reihenfolge: --config,
KEPHALAION_CONFIG, die config des Users, wenn es sie gibt, die globale, wenn es
sie gibt, sonst der Ort des Users (dort legt init sie an).
`, r, config.DefaultListen(r), config.SystemPath, config.SystemUser)
}

// systemInitHint ist der Weg, eine Rolle der globalen Installation
// einzurichten: als Systembenutzer, mit --config und --db.
func systemInitHint(r config.Role) string {
	return fmt.Sprintf("sudo -u %s kephalaion %s init --config %s --db sqlite://%s/%s.db", config.SystemUser,
		r, config.SystemPath, config.SystemDataDir, r)
}

func runInit(r config.Role, args []string, stdout, stderr io.Writer) int {
	usage := initUsage(r)
	fs := newFlagSet(string(r)+" init", usage, stderr)
	dbFlag := fs.String("db", "", "")
	listenFlag := fs.String("listen", config.DefaultListen(r), "")
	cfgFlag := fs.String("config", "", "")
	if _, code, ok := parseFlags(fs, args, usage, 0, stderr); !ok {
		return code
	}
	fail := func(format string, a ...any) int {
		fmt.Fprintf(stderr, "%s init: "+format+"\n", append([]any{r}, a...)...)
		return 1
	}
	if err := config.CheckListen(*listenFlag); err != nil {
		return fail("%v", err)
	}

	loc, err := config.Locate(*cfgFlag)
	if err != nil {
		return fail("%v", err)
	}
	// Ohne ausdrücklichen Ort richtet init nur pro User ein: Neben einer
	// globalen Installation gibt es keine zweite (dieselben Ports, und ein
	// Client wüsste nicht, welchen Node er meint).
	if !loc.Explicit() && loc.SystemExists {
		return fail("Kephalaion ist auf diesem Rechner global eingerichtet (%s); eine Installation pro User gibt es "+
			"daneben nicht. Verwaltet wird als Systembenutzer %s, etwa:\n  %s", config.SystemPath, config.SystemUser,
			systemInitHint(r))
	}
	cfgPath := loc.Path
	cfg, _, err := config.Load(cfgPath)
	if err != nil {
		return fail("%v", err)
	}
	if s := cfg.Section(r); s != nil {
		return fail("die Rolle %s ist schon eingerichtet (config %s, db %s); init überschreibt nicht", r, cfgPath, s.DB)
	}

	var addr config.DB
	if *dbFlag == "" {
		dir, err := config.DataDir()
		if err != nil {
			return fail("%v", err)
		}
		addr = config.SQLiteDB(filepath.Join(dir, string(r)+".db"))
	} else if addr, err = config.ParseDB(*dbFlag); err != nil {
		return fail("%v", err)
	}
	if err := os.MkdirAll(filepath.Dir(addr.Path), 0o700); err != nil {
		return fail("Verzeichnis nicht anlegbar: %v", err)
	}

	ctx := context.Background()
	version, err := createRole(ctx, r, addr)
	if errors.Is(err, sqlitedb.ErrExists) {
		return fail("die Datenbankdatei %s existiert schon; init überschreibt nicht", addr.Path)
	}
	if err != nil {
		return fail("%v", err)
	}
	// Erst die Datenbank, dann die config. Scheitert die config, darf keine
	// halbe Einrichtung zurückbleiben, die ein zweites init blockiert.
	if err := config.AddRole(cfgPath, r, config.Section{DB: addr.String(), Listen: *listenFlag}); err != nil {
		if rmErr := removeDB(addr); rmErr != nil {
			return fail("%v; die angelegte Datenbank ließ sich nicht entfernen: %v", err, rmErr)
		}
		return fail("%v; die angelegte Datenbank ist wieder entfernt", err)
	}

	fmt.Fprintf(stdout, "%s eingerichtet.\n", roleTitle(r))
	fmt.Fprintf(stdout, "  Datenbank: %s (Schemafassung %d)\n", addr.Path, version)
	fmt.Fprintf(stdout, "  config:    %s (Abschnitt %s:)\n", cfgPath, r)
	fmt.Fprintf(stdout, "  listen:    %s (kephalaion serve lauscht dort)\n", *listenFlag)
	fmt.Fprintf(stdout, "Nächster Schritt: %s\n", nextStepAfterInit(loc))
	return 0
}

// nextStepAfterInit nennt nach init den Dienst: pro User service install,
// global die System-Unit.
func nextStepAfterInit(loc config.Location) string {
	if loc.System() {
		return "System-Unit ablegen: kephalaion service unit --system (siehe docs/installation.md)"
	}
	step := "Dienst einrichten: kephalaion service install"
	if loc.Source == config.FromFlag {
		step += " --config " + loc.Path
	}
	return step
}

const statusUsage = `Aufruf:
  kephalaion status [--config pfad]

Zeigt, welche config gilt und woher (--config, KEPHALAION_CONFIG, pro User oder
global), ob der Dienst eingerichtet ist und läuft (pro User bzw. bei der
globalen config die System-Unit; ohne systemd „ohne systemd“), welche Rollen auf diesem Rechner eingerichtet sind, wo ihre Datenbank
liegt, wo ihr Dienst lauscht, ob kephalaion serve für sie läuft (geprüft an
der Sperrdatei <db>.lock, ohne zu warten), und ihre Kennzahlen. Am Node steht je Hub
der Stand des Abgleichs (letzter Erfolg, letzter Fehler — von serve und node
sync), die hub_id aus seiner Replica und je gewünschter Collection der Stand
(Revision) und der letzte Abgleich; ohne Replica „noch kein Abgleich“.
Öffnet die Datenbanken nur, legt nichts an.

Gilt die globale config und darf der Aufrufer die Datenbanken nicht lesen
(sie gehören dem Systembenutzer kephalaion), nennt status die globale
Installation und den Weg, sie zu verwalten, statt eines Fehlers.

Exit-Code:
  0   alles geprüft; auch bei einer globalen Installation, deren Datenbanken
      der Aufrufer nicht lesen darf. Die Zeile Dienst ändert ihn nie.
  1   die config ist nicht lesbar, die Datenbank einer eingerichteten Rolle
      fehlt oder passt nicht, oder es gibt die config des Users und die
      globale nebeneinander (zwei Arten der Installation auf einem Rechner)

Optionen:
  --config pfad   Ort der config (siehe kephalaion hub init --help)
`

func runStatus(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("status", statusUsage, stderr)
	cfgFlag := fs.String("config", "", "")
	if _, code, ok := parseFlags(fs, args, statusUsage, 0, stderr); !ok {
		return code
	}
	loc, err := config.Locate(*cfgFlag)
	if err != nil {
		fmt.Fprintf(stderr, "status: %v\n", err)
		return 1
	}
	cfg, exists, err := config.Load(loc.Path)
	if err != nil {
		fmt.Fprintf(stderr, "status: %v\n", err)
		return 1
	}
	printConfigLine(stdout, loc, exists)

	ctx := context.Background()
	fmt.Fprintf(stdout, "Dienst: %s\n", serviceLine(ctx, loc))
	failed := false
	if loc.BothKinds() {
		fmt.Fprintf(stdout, "Fehler: zwei Arten der Installation auf diesem Rechner — es gibt die config pro User (%s) "+
			"und die globale (%s). Weg: die Installation pro User entfernen (docs/installation.md, "+
			"„Installation pro User entfernen“).\n", loc.UserPath, config.SystemPath)
		failed = true
	}
	for _, r := range config.Roles {
		fmt.Fprintln(stdout)
		sec := cfg.Section(r)
		if sec == nil {
			fmt.Fprintf(stdout, "%s: nicht eingerichtet\n", r)
			continue
		}
		fmt.Fprintf(stdout, "%s: eingerichtet\n", r)
		fmt.Fprintf(stdout, "  db:            %s\n", sec.DB)
		listen := cfg.Listen(r)
		if sec.Listen == "" {
			listen += " (Standard, nicht in der config)"
		}
		fmt.Fprintf(stdout, "  listen:        %s\n", listen)
		if loc.System() && systemDBUnreadable(sec) {
			// Ein anderer User als der Systembenutzer: kein Fehler der
			// Installation, nur nicht von hier aus prüfbar.
			fmt.Fprintf(stdout, "  serve:         nicht prüfbar (siehe Dienst)\n")
			fmt.Fprintf(stdout, "  Hinweis:       globale Installation; die Datenbank gehört dem Systembenutzer %s. "+
				"Verwaltet wird als dieser, etwa: sudo -u %s kephalaion status\n", config.SystemUser, config.SystemUser)
			continue
		}
		fmt.Fprintf(stdout, "  serve:         %s\n", serveState(sec))
		if err := printRoleStatus(ctx, stdout, r, sec); err != nil {
			fmt.Fprintf(stdout, "  Fehler:        %v\n", err)
			failed = true
		}
	}
	if cfg.Empty() {
		fmt.Fprintln(stdout)
		fmt.Fprintln(stdout, "Keine Rolle eingerichtet. Einrichten mit:")
		fmt.Fprintln(stdout, "  kephalaion hub init    für den Hub")
		fmt.Fprintln(stdout, "  kephalaion node init   für den Node")
	}
	if failed {
		return 1
	}
	return 0
}

// systemDBUnreadable sagt, ob die Datenbank einer Rolle mangels Rechten nicht
// erreichbar ist — der Fall eines anderen Users bei einer globalen
// Installation (/var/lib/kephalaion ist 0700).
func systemDBUnreadable(sec *config.Section) bool {
	addr, err := config.ParseDB(sec.DB)
	if err != nil || addr.Kind != config.SQLite {
		return false
	}
	f, err := os.Open(addr.Path)
	if err != nil {
		return errors.Is(err, fs.ErrPermission)
	}
	_ = f.Close()
	return false
}

// serveState sagt, ob ein serve die Sperre der Rolle hält — geprüft ohne zu
// warten und ohne etwas anzulegen.
func serveState(sec *config.Section) string {
	addr, err := config.ParseDB(sec.DB)
	if err != nil || addr.Kind != config.SQLite {
		return "unbekannt"
	}
	running, err := serveRunning(addr.Path)
	switch {
	case err != nil:
		return fmt.Sprintf("unbekannt (%v)", err)
	case running:
		return "läuft"
	}
	return "läuft nicht"
}

// printConfigLine zeigt Ort und Quelle der config.
func printConfigLine(w io.Writer, loc config.Location, exists bool) {
	state := "vorhanden"
	if !exists {
		state = "fehlt"
	}
	fmt.Fprintf(w, "config: %s (%s)\n", loc.Path, state)
	fmt.Fprintf(w, "Quelle: %s\n", describeSource(loc))
}

// describeSource sagt, woher der Ort der config kommt.
func describeSource(loc config.Location) string {
	var s string
	switch loc.Source {
	case config.FromFlag:
		s = "--config"
	case config.FromEnv:
		s = config.EnvConfig
	case config.FromUser:
		s = "pro User"
	case config.FromSystem:
		s = "global (Installation für alle User, verwaltet als Systembenutzer " + config.SystemUser + ")"
	}
	if loc.System() && loc.Source != config.FromSystem {
		s += ", die globale config"
	}
	return s
}

// printRoleStatus öffnet die Datenbank einer Rolle und gibt ihre Kennzahlen
// aus.
func printRoleStatus(ctx context.Context, w io.Writer, r config.Role, sec *config.Section) error {
	s, err := openSection(ctx, r, sec)
	if err != nil {
		return err
	}
	defer s.Close()
	switch st := s.(type) {
	case hubstore.Store:
		return printHubStatus(ctx, w, st)
	case nodestore.Store:
		return printNodeStatus(ctx, w, st)
	}
	return nil
}

func printHubStatus(ctx context.Context, w io.Writer, st hubstore.Store) error {
	info, err := st.Info(ctx)
	if err != nil {
		return err
	}
	stats, err := st.Stats(ctx)
	if err != nil {
		return err
	}
	colls, err := st.Collections(ctx)
	if err != nil {
		return err
	}
	nodes, err := st.Nodes(ctx)
	if err != nil {
		return err
	}
	accounts, err := st.Accounts(ctx)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(colls))
	for _, c := range colls {
		names = append(names, c.Name)
	}
	fmt.Fprintf(w, "  Schemafassung: %d\n", info.SchemaVersion)
	fmt.Fprintf(w, "  hub_id:        %s\n", info.HubID)
	fmt.Fprintf(w, "  Revision:      %d\n", info.Revision)
	fmt.Fprintf(w, "  Dokumente:     %d\n", stats.Documents)
	fmt.Fprintf(w, "  Collections:   %s\n", joinOrNone(names))
	accountNames := make([]string, 0, len(accounts))
	for _, a := range accounts {
		n := a.Name
		if a.Locked {
			n += " (gesperrt)"
		}
		accountNames = append(accountNames, n)
	}
	fmt.Fprintf(w, "  Accounts:      %s\n", joinOrNone(accountNames))
	if len(nodes) == 0 {
		fmt.Fprintf(w, "  Nodes:         keine\n")
		return nil
	}
	fmt.Fprintf(w, "  Nodes:\n")
	for _, n := range nodes {
		fmt.Fprintf(w, "    %s: %s, erlaubt: %s\n", n.Name, lockState(n.Locked), joinOrNone(n.Collections))
	}
	return nil
}

func printNodeStatus(ctx context.Context, w io.Writer, st nodestore.Store) error {
	info, err := st.Info(ctx)
	if err != nil {
		return err
	}
	hubs, err := st.Hubs(ctx)
	if err != nil {
		return err
	}
	status, err := st.SyncStatus(ctx)
	if err != nil {
		return err
	}
	settings, err := st.Settings(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "  Schemafassung: %d\n", info.SchemaVersion)
	fmt.Fprintf(w, "  Abgleich:      %s\n", describeSyncInterval(settings))
	if len(hubs) == 0 {
		fmt.Fprintf(w, "  Hubs:          keine\n")
		return nil
	}
	fmt.Fprintf(w, "  Hubs:\n")
	for _, h := range hubs {
		fmt.Fprintf(w, "    %s: %s, als Node %s\n", h.Name, describeTransport(h), h.NodeName)
		printReplicaStatus(ctx, w, st, h)
		fmt.Fprintf(w, "      Abgleich:    %s\n", describeSyncStatus(status[h.Name]))
	}
	return nil
}

// describeSyncInterval beschreibt den Abstand des Abgleichs im Hintergrund
// aus den settings.
func describeSyncInterval(settings map[string]string) string {
	d, err := nodestore.SyncInterval(settings)
	switch {
	case err != nil:
		return fmt.Sprintf("im Hintergrund alle %s (%v)", d, err)
	case d == 0:
		return "im Hintergrund aus (sync_interval 0)"
	}
	if _, set := settings[nodestore.SettingSyncInterval]; !set {
		return fmt.Sprintf("im Hintergrund alle %s (Standard)", d)
	}
	return fmt.Sprintf("im Hintergrund alle %s", d)
}

// describeSyncStatus beschreibt den Stand des Abgleichs eines Eintrags
// (hub_sync), wie node sync und serve ihn festhalten.
func describeSyncStatus(st nodestore.SyncStatus) string {
	ok := "noch nie gelungen"
	if st.OKAt != 0 {
		ok = "zuletzt gelungen " + formatSeconds(st.OKAt)
	}
	if st.Err == "" {
		if st.OKAt == 0 {
			return "noch keiner festgehalten"
		}
		return ok
	}
	return fmt.Sprintf("%s; gescheitert %s: %s", ok, formatSeconds(st.ErrAt), st.Err)
}

// formatSeconds zeigt einen Zeitpunkt (ms seit Epoche) sekundengenau in
// Ortszeit.
func formatSeconds(ms int64) string {
	return time.UnixMilli(ms).Local().Format("2006-01-02 15:04:05")
}

// printReplicaStatus zeigt den Abgleich eines Hub-Eintrags aus seiner
// Replica: die hub_id — dort die maßgebliche — und je Collection Stand und
// letzten Abgleich. Eine fehlende oder unlesbare Replica ist kein Fehler der
// Rolle: Sie ist abgeleitet, der nächste Abgleich legt sie (neu) an.
func printReplicaStatus(ctx context.Context, w io.Writer, st nodestore.Store, h nodestore.Hub) {
	r, err := replica.Open(ctx, st.ReplicaPath(h.Name))
	if errors.Is(err, sqlitedb.ErrNotFound) {
		fmt.Fprintf(w, "      hub_id:      noch kein Abgleich\n")
		fmt.Fprintf(w, "      Collections: %s\n", joinOrNone(h.Collections))
		return
	}
	if err != nil {
		fmt.Fprintf(w, "      Replica:     %v (der nächste Abgleich legt sie neu an)\n", err)
		fmt.Fprintf(w, "      Collections: %s\n", joinOrNone(h.Collections))
		return
	}
	defer r.Close()
	fmt.Fprintf(w, "      hub_id:      %s\n", r.HubID())
	states, err := r.States(ctx)
	if err != nil {
		fmt.Fprintf(w, "      Replica:     %v\n", err)
		return
	}
	byName := make(map[string]replica.State, len(states))
	for _, s := range states {
		byName[s.Collection] = s
	}
	if len(h.Collections) == 0 {
		fmt.Fprintf(w, "      Collections: keine\n")
	} else {
		fmt.Fprintf(w, "      Collections:\n")
	}
	for _, c := range h.Collections {
		if s, ok := byName[c]; ok {
			fmt.Fprintf(w, "        %s: Revision %d, abgeglichen %s\n", c, s.Revision, formatMillis(s.SyncedAt))
			delete(byName, c)
		} else {
			fmt.Fprintf(w, "        %s: noch nicht abgeglichen (oder vom Hub nicht erlaubt)\n", c)
		}
	}
	for _, s := range states {
		if _, ok := byName[s.Collection]; ok {
			fmt.Fprintf(w, "        %s: nicht mehr gewünscht, Revision %d; geht mit dem nächsten Abgleich\n",
				s.Collection, s.Revision)
		}
	}
}
