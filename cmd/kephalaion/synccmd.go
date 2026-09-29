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
	"syscall"

	"github.com/kephalaion/kephalaion/internal/config"
	"github.com/kephalaion/kephalaion/internal/contract"
	"github.com/kephalaion/kephalaion/internal/contract/httpapi"
	"github.com/kephalaion/kephalaion/internal/hub/replication"
	hubstore "github.com/kephalaion/kephalaion/internal/hub/store"
	"github.com/kephalaion/kephalaion/internal/ident"
	"github.com/kephalaion/kephalaion/internal/node/replica"
	nodestore "github.com/kephalaion/kephalaion/internal/node/store"
	"github.com/kephalaion/kephalaion/internal/sqlitedb"
)

const nodeSyncUsage = `Aufruf:
  kephalaion node sync [<alias>]

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
--ca-file). Bei Fehlern des Netzes wiederholen http und https jede Seite bis
zu dreimal, bei einem Zertifikatsfehler nie. Für ssh meldet sync
„noch nicht unterstützt“. Scheitert ein Eintrag, laufen die übrigen weiter;
der Exit-Code ist dann 1.

Optionen:
  --config pfad   Ort der config (siehe kephalaion node init --help)
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
var newLocalHub = func(st hubstore.Store) contract.Hub { return replication.New(st) }

// localHub ist der Hub über local. Ein Fehler von Rotate oder einem
// Schreibvorgang, der kein Fehler des Vertrags ist (Datenbank, Abbruch),
// lässt offen, ob der Hub ausgeführt hat — der neue Hash schon gilt, das
// Dokument schon geschrieben ist: Er kommt als contract.ErrOutcomeUnknown
// an, wie über HTTP ein 500. Der Hub steht in einem Feld, nicht eingebettet:
// So muss jeder neue Vorgang hier ausdrücklich stehen und kann nicht ohne
// outcome durchrutschen.
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

func runNodeSync(args []string, stdout, stderr io.Writer) int {
	c := newCommand("node sync", nodeSyncUsage, stdout, stderr)
	c.optional = 1
	pos, code, ok := c.parse(args)
	if !ok {
		return code
	}
	alias := ""
	if len(pos) > 0 {
		alias = pos[0]
	}
	// Ein Abbruch mit Strg-C beendet den Abgleich nach der laufenden Seite
	// oder in ihr; geschrieben ist nur, was als ganze Seite ankam.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := c.loadConfig()
	if err != nil {
		return c.fail(err)
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
