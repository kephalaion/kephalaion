package main

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/kephalaion/kephalaion/internal/assistant"
	"github.com/kephalaion/kephalaion/internal/config"
	"github.com/kephalaion/kephalaion/internal/contract"
	"github.com/kephalaion/kephalaion/internal/contract/httpapi"
	"github.com/kephalaion/kephalaion/internal/hub/replication"
	hubstore "github.com/kephalaion/kephalaion/internal/hub/store"
	"github.com/kephalaion/kephalaion/internal/ident"
	"github.com/kephalaion/kephalaion/internal/node/mcpnode"
	"github.com/kephalaion/kephalaion/internal/node/replica"
	nodestore "github.com/kephalaion/kephalaion/internal/node/store"
	"github.com/kephalaion/kephalaion/internal/sqlitedb"
)

const nodeSyncUsage = `Aufruf:
  kephalaion node sync [<alias>]
  kephalaion node sync [<alias>] [--node url [--ca-file pfad]] [--account name]
                                 [--token-file pfad | --token-stdin]

Gleicht die Replicas dieses Nodes mit seinen Hubs ab: alle Hub-Einträge oder
nur den genannten. Je gewünschter Collection kommt alles, was sich seit dem
letzten Abgleich geändert hat, in Seiten; jede Seite ist eine Transaktion, ein
abgebrochener Abgleich setzt beim letzten Stand fort. Den ersten Abgleich
eines Eintrags legt seine Replica an (replicas/<alias>.db neben node.db).

Collections, die der Hub nicht (mehr) erlaubt oder die der Node nicht mehr
will, verschwinden aus der Replica. Nennt der Hub eine andere hub_id als
bisher, oder steht die Replica weiter als der Hub (aus einer Sicherung
zurückgespielt?), wird sie geleert und von vorn abgeglichen.

Transporte: local (der Hub derselben config, im selben Prozess), http (ein
Hub auf diesem Rechner, der mit kephalaion serve lauscht) und https (ein Hub
auf einem anderen Rechner hinter einem Reverse-Proxy, der TLS beendet; das
Zertifikat prüft der Node gegen die System-Roots oder die CA des Eintrags,
--ca-file bei node hub add). Bei Fehlern des Netzes wiederholen http und
https jede Seite bis zu dreimal, bei einem Zertifikatsfehler nie. Für ssh
meldet sync „noch nicht unterstützt“. Scheitert ein Eintrag, laufen die
übrigen weiter; der Exit-Code ist dann 1.

Als Client des Nodes:
  Mit --node, und ohne --node auf einem reinen Client, öffnet node sync
  keine Datenbank: Es fordert den Abgleich beim laufenden Node an (über MCP,
  Werkzeug sync), wartet auf sein Ergebnis und nennt den Stand danach. Ein
  reiner Client ist ein Rechner, dessen config einen Abschnitt node: hat,
  auf dem aber die Datenbank des Nodes samt ihrem Verzeichnis fehlt — etwa
  ein Devcontainer mit der config des Hosts; kephalaion status nennt das
  „läuft nicht hier“. Fehlt nur die Datei, bleibt es ein Fehler.

  Auf diesem Weg gleicht node sync genau einen Hub ab: den genannten, ohne
  <alias> den einen Hub, für den unter <config-dir>/tokens/ ein Verzeichnis
  liegt. Bei mehreren oder keinem ist es ein falscher Aufruf (Exit 2); die
  Meldung nennt die Aliase. Der Node wartet höchstens 40 s auf den Abgleich;
  dauert er länger, läuft er im Hintergrund weiter, und node sync endet mit
  sync_timeout (Exit 1). Einen Hub-Eintrag, der noch nie abgeglichen wurde,
  gleicht nur kephalaion node sync am Rechner des Nodes zum ersten Mal ab
  (never_synced). Kennt der Node das Werkzeug noch nicht, ist er zu
  aktualisieren und serve neu zu starten.

  Die Adresse des Nodes ist --node <url>, sonst listen im Abschnitt node: der
  config. Account und Token wie bei kephalaion node dir: --account, ohne
  Angabe der Standard-Account des Hubs, sonst der einzige mit Token-Datei
  unter <config-dir>/tokens/<hub>/; das Token aus dieser Datei, --token-file
  oder --token-stdin (beide nur mit --account) — nie als Argument.

  Ohne --node auf dem Rechner des Nodes gleicht node sync wie oben gegen
  node.db ab; die Optionen der Anmeldung sind dort ein falscher Aufruf.

Exit-Codes:
  0   abgeglichen
  1   Fehler: ein Eintrag gescheitert, der Node nicht erreichbar, ein Fehler
      des Werkzeugs (mit seiner Meldung)
  2   falscher Aufruf

Optionen:
  --node url           als Client des Nodes: seine Adresse ohne /mcp,
                       http://127.0.0.1:<port> oder über einen Proxy
                       https://<name>/<präfix>
  --ca-file pfad       mit --node https://…: das Zertifikat gegen diese CA
                       (PEM) prüfen statt gegen die System-Roots
  --account name       als Client des Nodes: der Account; sonst der Standard,
                       sonst der einzige mit Token-Datei
  --token-file pfad    Datei mit dem Token (eine Zeile)
  --token-stdin        das Token als eine Zeile von der Standardeingabe
  --config pfad        Ort der config (siehe kephalaion node init --help)
`

const nodeDocUsage = `Aufruf:
  kephalaion node doc list <hub>:<collection> [verzeichnis]
  kephalaion node doc get  <hub>:<collection> <name>

Kommandos:
  list   zeigt den Inhalt eines Verzeichnisses der Replica, nach Name
         sortiert; Unterverzeichnisse enden auf '/'
  get    gibt den Inhalt eines Dokuments aus der Replica aus

Beide lesen nur die Replica, so wie der letzte Abgleich (kephalaion node sync)
sie hinterlassen hat; sie fragen den Hub nicht. Gelöschte Dokumente und
Namen mit dem Präfix SYSTEM: zeigen sie nie.

Optionen:
  --config pfad   Ort der config (siehe kephalaion node init --help)
`

// connector wählt je Hub-Eintrag die Umsetzung des Vertrags — nur hier, in
// cmd/kephalaion; internal/node kennt nur contract.Hub. Für local nimmt er
// hub, wenn der Aufrufer ihn mitgibt (serve mit beiden Rollen), sonst öffnet
// er den Hub der eigenen config beim ersten Bedarf, einmal für alle
// Einträge; close schließt nur, was er selbst geöffnet hat. Für http und
// https nimmt er den Client aus internal/contract/httpapi — bei https mit der
// CA des Eintrags, sonst mit den System-Roots. ssh gibt es noch nicht.
type connector struct {
	ctx context.Context
	cfg config.Config
	hub hubstore.Store
	// owned sagt, dass der connector hub selbst geöffnet hat.
	owned bool
	// err ist der Fehler beim Öffnen des Hubs; er gilt für jeden
	// local-Eintrag.
	err error
}

// errTransportUnsupported meldet einen Transport, den der connector noch
// nicht kann (ssh).
var errTransportUnsupported = errors.New("noch nicht unterstützt")

// connectHTTP liefert die Umsetzung über HTTP, mit rootCAs für https (nil:
// die System-Roots); Tests ersetzen sie, etwa um einen unklaren Ausgang
// herbeizuführen.
var connectHTTP = func(address string, rootCAs *x509.CertPool) (contract.Hub, error) {
	return httpapi.NewClient(address, rootCAs)
}

func (l *connector) connect(h nodestore.Hub) (contract.Hub, error) {
	switch h.Transport {
	case nodestore.TransportLocal:
	case nodestore.TransportHTTP, nodestore.TransportHTTPS:
		// Adresse und CA hat der Node-Store geprüft: http nur auf diesem
		// Rechner, https mit https:// und einer lesbaren CA.
		if err := nodestore.CheckHub(h, true); err != nil {
			return nil, err
		}
		pool, err := certPool(h)
		if err != nil {
			return nil, err
		}
		return connectHTTP(h.Address, pool)
	default:
		return nil, fmt.Errorf("Transport %s wird %w; bisher gehen local, http und https", h.Transport, errTransportUnsupported)
	}
	if l.cfg.Section(config.Hub) == nil {
		return nil, errors.New("Transport local verlangt einen Hub in derselben config, dort ist keiner " +
			"eingerichtet (Abschnitt hub:)")
	}
	if l.hub == nil && l.err == nil {
		l.hub, l.err = openHubOf(l.ctx, l.cfg)
		l.owned = l.err == nil
		if l.err != nil {
			l.err = fmt.Errorf("Hub der eigenen config: %w", l.err)
		}
	}
	if l.err != nil {
		return nil, l.err
	}
	return localHub{newLocalHub(l.hub)}, nil
}

// certPool liefert die CA eines https-Eintrags als Pool, oder nil für die
// System-Roots.
func certPool(h nodestore.Hub) (*x509.CertPool, error) {
	if h.CA == "" {
		return nil, nil
	}
	certs, err := nodestore.ParseCA(h.CA)
	if err != nil {
		return nil, fmt.Errorf("Hub %s: %w", h.Name, err)
	}
	pool := x509.NewCertPool()
	for _, c := range certs {
		pool.AddCert(c)
	}
	return pool, nil
}

// newLocalHub liefert die Seite des Hubs über dem eigenen Store; Tests
// ersetzen sie, etwa um einen Fehler nach dem Commit herbeizuführen.
var newLocalHub = func(st hubstore.Store) contract.Hub { return replication.New(st).WithSource(newSourceClient()) }

// localHub ist der Hub über local. Ein Fehler von Rotate, RotateNode oder
// einem Schreibvorgang, der kein Fehler des Vertrags ist (Datenbank, Abbruch),
// lässt offen, ob der Hub ausgeführt hat — der neue Hash schon gilt, das
// Dokument schon geschrieben ist: Er kommt als contract.ErrOutcomeUnknown
// an, wie über HTTP ein 500. Der Hub steht in einem Feld, nicht eingebettet:
// So muss jeder neue Vorgang hier ausdrücklich stehen und kann nicht ohne
// outcome durchrutschen. Die Anfragen gehen unverändert durch, samt
// RequestID; wiederholt wird auch hier nie.
type localHub struct {
	hub contract.Hub
}

var _ contract.Hub = localHub{}

func (l localHub) Whoami(ctx context.Context, req contract.WhoamiRequest) (contract.WhoamiResponse, error) {
	return l.hub.Whoami(ctx, req)
}

func (l localHub) Sync(ctx context.Context, req contract.SyncRequest) (contract.SyncResponse, error) {
	return l.hub.Sync(ctx, req)
}

func (l localHub) Rotate(ctx context.Context, req contract.RotateRequest) (contract.RotateResponse, error) {
	return outcome(l.hub.Rotate(ctx, req))
}

func (l localHub) RotateNode(ctx context.Context, req contract.NodeRotateRequest) (contract.NodeRotateResponse, error) {
	return outcome(l.hub.RotateNode(ctx, req))
}

func (l localHub) Create(ctx context.Context, req contract.CreateRequest) (contract.WriteResponse, error) {
	return outcome(l.hub.Create(ctx, req))
}

func (l localHub) Write(ctx context.Context, req contract.WriteRequest) (contract.WriteResponse, error) {
	return outcome(l.hub.Write(ctx, req))
}

func (l localHub) Delete(ctx context.Context, req contract.DeleteRequest) (contract.WriteResponse, error) {
	return outcome(l.hub.Delete(ctx, req))
}

func (l localHub) Rename(ctx context.Context, req contract.RenameRequest) (contract.WriteResponse, error) {
	return outcome(l.hub.Rename(ctx, req))
}

func (l localHub) Fetch(ctx context.Context, req contract.FetchRequest) (contract.FetchResponse, error) {
	return outcome(l.hub.Fetch(ctx, req))
}

func (l localHub) RebuildDocs(ctx context.Context, req contract.RebuildDocsRequest) (contract.RebuildDocsResponse, error) {
	return outcome(l.hub.RebuildDocs(ctx, req))
}

// outcome hüllt einen Fehler, der kein Fehler des Vertrags ist, in
// contract.ErrOutcomeUnknown.
func outcome[T any](resp T, err error) (T, error) {
	var ce *contract.Error
	if err != nil && !errors.As(err, &ce) {
		var zero T
		return zero, fmt.Errorf("%w: %v", contract.ErrOutcomeUnknown, err)
	}
	return resp, err
}

func (l *connector) close() {
	if l.owned && l.hub != nil {
		_ = l.hub.Close()
	}
}

// syncCallMargin ist, wie lange node sync als Client über die Wartefrist des
// Nodes hinaus auf dessen Antwort wartet.
const syncCallMargin = 20 * time.Second

func runNodeSync(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	c := newCommand("node sync", nodeSyncUsage, stdout, stderr)
	c.optional = 1
	login := addNodeLogin(c, stdin)
	pos, code, ok := c.parse(args)
	if !ok {
		return code
	}
	alias := ""
	if len(pos) > 0 {
		alias = pos[0]
	}
	// Ein Abbruch mit Strg-C beendet den Abgleich nach der laufenden Seite
	// oder in ihr; geschrieben ist nur, was als ganze Seite ankam. Als Client
	// des Nodes endet nur das Warten, der Abgleich läuft dort weiter.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Ein ausdrückliches --node geht immer über MCP, auf jedem Rechner und
	// ohne die config; sonst entscheidet die Erkennung des reinen Clients —
	// dieselbe wie bei status.
	if *login.node != "" {
		return syncViaNode(ctx, c, login, alias)
	}
	loc, err := config.Locate(*c.cfg)
	if err != nil {
		return c.fail(err)
	}
	cfg, _, err := config.Load(loc.Path)
	if err != nil {
		return c.fail(err)
	}
	if sec := cfg.Section(config.Node); sec != nil {
		if _, ok := pureClient(loc, sec); ok {
			return syncViaNode(ctx, c, login, alias)
		}
	}
	if given := login.given(); len(given) > 0 {
		// Gemeldet, bevor node.db geöffnet wird.
		fmt.Fprintf(stderr, "node sync: %s gilt nur als Client des Nodes (mit --node oder auf einem reinen Client); "+
			"hier gleicht node sync gegen die Datenbank des Nodes ab\n", strings.Join(given, ", "))
		return 2
	}
	nodes, err := openNodeOf(ctx, cfg)
	if err != nil {
		return c.fail(err)
	}
	defer nodes.Close()
	conn := &connector{ctx: ctx, cfg: cfg}
	defer conn.close()

	s := &replica.Syncer{Nodes: nodes}
	results, err := s.Sync(ctx, alias, conn.connect)
	if err != nil {
		return c.fail(err)
	}
	if len(results) == 0 {
		fmt.Fprintln(stdout, "Keine Hubs.")
		return 0
	}
	failed := false
	for _, res := range results {
		printSyncResult(stdout, res)
		if res.Err != nil {
			fmt.Fprintf(stderr, "node sync: Hub %s: %v\n", res.Hub, res.Err)
			failed = true
		}
	}
	if failed {
		return 1
	}
	return 0
}

// syncViaNode ist node sync als Client des Nodes (Task 042): Es fordert den
// Abgleich eines Hubs über das MCP-Werkzeug sync an und nennt das Ergebnis.
// Der Hub ist alias, ohne ihn der eine Hub unter tokens/; das Kommando
// schickt ihn immer mit. Einen Node ohne das Werkzeug erkennt es an der
// Werkzeugliste, bevor es aufruft.
func syncViaNode(ctx context.Context, c *command, login *nodeLogin, alias string) int {
	usage := func(format string, a ...any) int {
		fmt.Fprintf(c.stderr, "node sync: "+format+"\n", a...)
		return 2
	}
	if err := login.check(); err != nil {
		return usage("%v", err)
	}
	hub := alias
	if hub != "" {
		if err := ident.CheckName("Hub", hub); err != nil {
			return usage("%v", err)
		}
	} else {
		base, err := assistant.TokensDir()
		if err != nil {
			return c.fail(err)
		}
		hubs, err := assistant.Hubs(base)
		if err != nil {
			return c.fail(err)
		}
		switch len(hubs) {
		case 1:
			hub = hubs[0]
		case 0:
			return usage("als Client des Nodes gleicht node sync genau einen Hub ab, und unter %s liegt keiner; "+
				"den Alias angeben: kephalaion node sync <alias>", base)
		default:
			return usage("als Client des Nodes gleicht node sync genau einen Hub ab, unter %s liegen mehrere: %s; "+
				"den Alias angeben: kephalaion node sync <alias>", base, strings.Join(hubs, ", "))
		}
	}
	addr, err := login.address()
	if err != nil {
		return c.fail(err)
	}
	rootCAs, err := readNodeCA(addr, *login.caFile)
	if err != nil {
		return c.fail(err)
	}
	account, token, err := login.credentials(hub)
	if err != nil {
		return c.fail(err)
	}
	session, done, err := connectNodeSession(ctx, addr, rootCAs, *login.caFile, hub, account, token)
	if err != nil {
		return c.fail(err)
	}
	defer done()
	known := false
	for tool, err := range session.Tools(ctx, nil) {
		if err != nil {
			return c.fail(fmt.Errorf("Werkzeuge des Nodes unter %s nicht lesbar: %w", addr.Endpoint(), err))
		}
		if tool.Name == "sync" {
			known = true
		}
	}
	if !known {
		return c.fail(fmt.Errorf("der Node unter %s kennt das Werkzeug sync nicht — er ist älter als diese "+
			"Kommandozeile; den Node aktualisieren und serve neu starten", addr.Endpoint()))
	}
	// Der Node antwortet nach höchstens seiner Wartefrist; die eigene liegt
	// darüber und greift nur, wenn er gar nicht antwortet.
	limit := mcpnode.SyncWaitLimit + syncCallMargin
	callCtx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	var out mcpnode.SyncOutput
	if _, err := callTool(callCtx, session, "sync", mcpnode.SyncInput{Hub: hub}, &out); err != nil {
		if ctx.Err() == nil && callCtx.Err() != nil {
			err = fmt.Errorf("der Node unter %s antwortet nicht in %s", addr.Endpoint(), limit)
		}
		return c.fail(err)
	}
	if out.Sync == nil {
		return c.fail(errors.New("der Node meldet Erfolg ohne Stand des Abgleichs"))
	}
	fmt.Fprintf(c.stdout, "Hub %s: %s (über den Node %s)\n", out.Hub, mcpnode.DescribeSync(*out.Sync), addr.Base)
	return 0
}

// printSyncResult zeigt das Ergebnis eines Hub-Eintrags: Kopfzeile, Reset,
// je Collection, was geschah, und was der Hub sonst noch erlaubt. Den
// Fehler eines Eintrags meldet der Aufrufer.
func printSyncResult(w io.Writer, res replica.HubResult) {
	head := "Hub " + res.Hub
	if res.HubID != "" {
		head += " (hub_id " + res.HubID + ")"
	}
	switch {
	case res.Err != nil:
		head += ": gescheitert"
	case res.Pages == 1:
		head += ": 1 Seite"
	default:
		head += fmt.Sprintf(": %d Seiten", res.Pages)
	}
	fmt.Fprintln(w, head)
	if res.Reset != "" {
		fmt.Fprintf(w, "  %s\n", res.Reset)
	}
	for _, cr := range res.Collections {
		fmt.Fprintf(w, "  %s: %s\n", cr.Collection, describeCollectionResult(cr, res.Err != nil))
	}
	if res.Err == nil && len(res.Collections) == 0 {
		fmt.Fprintln(w, "  keine Collections gewünscht")
	}
	var more []string
	for _, a := range res.Allowed {
		if !slices.ContainsFunc(res.Collections, func(cr replica.CollectionResult) bool {
			return cr.Collection == a && cr.Status == replica.Synced
		}) {
			more = append(more, a)
		}
	}
	if len(more) > 0 {
		fmt.Fprintf(w, "  vom Hub außerdem erlaubt: %s\n", joinOrNone(more))
	}
}

func describeCollectionResult(cr replica.CollectionResult, failed bool) string {
	switch cr.Status {
	case replica.NotAllowed, replica.NotWanted:
		// Nicht erlaubt ohne entfernte Zeilen: Die Replica hatte nichts davon.
		s := cr.Status.String()
		switch {
		case cr.Removed > 0:
			s += ", " + rowsText(cr.Removed) + " aus der Replica entfernt"
		case cr.Status == replica.NotWanted:
			s += ", aus der Replica entfernt"
		}
		return s
	}
	if failed && cr.Revision == 0 && cr.Rows == 0 {
		return "nicht abgeglichen"
	}
	return fmt.Sprintf("%s, %s, Revision %d", cr.Status, rowsText(int64(cr.Rows)), cr.Revision)
}

// rowsText zählt Zeilen: „1 Zeile“, „3 Zeilen“. Zeilen, nicht Dokumente:
// Löschmarken zählen mit.
func rowsText(n int64) string {
	if n == 1 {
		return "1 Zeile"
	}
	return fmt.Sprintf("%d Zeilen", n)
}

func runNodeDoc(args []string, stdout, stderr io.Writer) int {
	u := nodeDocUsage
	return dispatch("node doc", u, args, stdout, stderr, map[string]func([]string) int{
		"list": func(a []string) int {
			c := newCommand("node doc list", u, stdout, stderr, "<hub>:<collection>")
			c.optional = 1
			return c.replicaDo(a, func(ctx context.Context, r *replica.Replica, coll string, pos []string) error {
				dir := ""
				if len(pos) > 1 {
					dir = pos[1]
				}
				docs, err := r.Documents(ctx, coll, dir)
				if err != nil {
					return err
				}
				listed := make([]listedDoc, 0, len(docs))
				for _, d := range docs {
					listed = append(listed, listedDoc{d.Name, d.Revision, d.UpdatedAt, d.UpdatedBy})
				}
				return printListing(stdout, pos[0], dir, listed)
			})
		},
		"get": func(a []string) int {
			c := newCommand("node doc get", u, stdout, stderr, "<hub>:<collection>", "<name>")
			return c.replicaDo(a, func(ctx context.Context, r *replica.Replica, coll string, pos []string) error {
				d, err := r.Document(ctx, coll, pos[1])
				if err != nil {
					return err
				}
				if d.Content == nil {
					return nil
				}
				_, err = io.WriteString(stdout, *d.Content)
				return err
			})
		},
	})
}

// replicaDo wertet die Argumente aus, zerlegt die Adresse im ersten, öffnet
// den Node und die Replica des Hub-Eintrags und führt fn mit der Collection
// aus.
func (c *command) replicaDo(args []string, fn func(ctx context.Context, r *replica.Replica, coll string, pos []string) error) int {
	return c.nodeDo(args, func(ctx context.Context, s nodestore.Store, _ bool, pos []string) error {
		hub, coll, err := ident.ParseAddress(pos[0])
		if err != nil {
			return err
		}
		if _, err := s.Hub(ctx, hub); err != nil {
			return err
		}
		r, err := replica.Open(ctx, s.ReplicaPath(hub))
		if errors.Is(err, sqlitedb.ErrNotFound) {
			return fmt.Errorf("Hub %s: noch kein Abgleich; zuerst: kephalaion node sync %s", hub, hub)
		}
		if err != nil {
			return err
		}
		defer r.Close()
		return fn(ctx, r, coll, pos)
	})
}
