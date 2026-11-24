package mcpnode

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kephalaion/kephalaion/internal/contract"
	"github.com/kephalaion/kephalaion/internal/ident"
	"github.com/kephalaion/kephalaion/internal/node/replica"
	"github.com/kephalaion/kephalaion/internal/reqlog"
)

// Der Abgleich auf Anforderung (Task 042, docs/konzept.md, „Abgleich“, „Auf
// Anforderung“): Ein Client fordert den Abgleich eines Hub-Eintrags an und
// bekommt dessen Ergebnis. Der Abgleich läuft im Prozess von serve
// (cmd/kephalaion, backgroundSync); dieses Paket kennt ihn nur als wartenden
// Anstoß (HubLink.SyncWait) und dessen Ergebnis.

// SyncOutcome ist das Ergebnis eines abgewarteten Abgleichs: des ersten
// Abgleichs eines Hub-Eintrags, der nach dem Aufruf des wartenden Anstoßes
// begonnen hat. Es stammt aus diesem Abgleich selbst, nicht aus hub_sync —
// dort kann schon ein späterer stehen. Den Fehlertext des Abgleichs trägt es
// nicht: Er kann die Adresse des Hubs nennen.
type SyncOutcome struct {
	// At ist bei einem Erfolg der Zeitpunkt (ms seit 1970), den der Abgleich
	// als letzten Erfolg in hub_sync festgehalten hat.
	At int64
	// Kind ist die Art des Fehlers eines gescheiterten Abgleichs; leer heißt
	// Erfolg.
	Kind replica.ErrorKind
	// Contract sagt bei einem gescheiterten Abgleich, ob sein Fehler ein
	// Fehler des Vertrags ist (*contract.Error) — der Hub hat geantwortet und
	// abgelehnt. Nur zur Einordnung der Art hub.
	Contract bool
}

// Die Fehler des wartenden Anstoßes neben ErrUnsupported (Transport ssh) und
// dem ctx des Aufrufers: Der Abgleich, auf den gewartet wurde, hat kein
// Ergebnis.
var (
	// ErrSyncGone: Der Hub-Eintrag wurde während des Wartens entfernt oder
	// unter demselben Alias neu angelegt.
	ErrSyncGone = errors.New("Hub-Eintrag entfernt oder neu angelegt")
	// ErrSyncStopped: serve endet; es beginnt kein Abgleich mehr, ein
	// laufender ist abgebrochen.
	ErrSyncStopped = errors.New("serve endet")
)

// Die zwei Codes, die nur das Werkzeug sync meldet (docs/begriffe.md,
// „sync (tool)“). Alle übrigen sind vorhandene: invalid, not_readable,
// unknown_hub, unsupported, unreachable, unauthenticated,
// unsupported_version, hub_refused, internal.
const (
	// CodeNeverSynced: Der Hub-Eintrag hat noch keine Replica. Den ersten
	// Abgleich macht kephalaion node sync am Rechner des Nodes.
	CodeNeverSynced = "never_synced"
	// CodeSyncTimeout: Die Wartefrist ist abgelaufen, bevor der abgewartete
	// Abgleich geendet hat. Er läuft im Hintergrund weiter; gescheitert ist
	// er damit nicht.
	CodeSyncTimeout = "sync_timeout"
)

// SyncWaitLimit ist die Wartefrist des Werkzeugs sync: so lange wartet es
// höchstens auf den Abgleich. Eine feste Zahl, keine Einstellung — Clients
// richten ihre eigene Frist danach (docs/installation.md, „Einen Abgleich
// anfordern“); sie steht in der Meldung zu CodeSyncTimeout.
const SyncWaitLimit = 40 * time.Second

// syncWaitLimit ist die Wartefrist, die gilt; Tests stellen sie kürzer.
var syncWaitLimit = SyncWaitLimit

// limitText schreibt eine Frist für die Meldung: ganze Sekunden als „40 s“.
func limitText(d time.Duration) string {
	if d%time.Second == 0 {
		return fmt.Sprintf("%d s", d/time.Second)
	}
	return d.String()
}

// SyncInput sind die Argumente von sync.
type SyncInput struct {
	Hub string `json:"hub,omitempty" jsonschema:"Alias des Hub-Eintrags an diesem Node, wie whoami ihn nennt; entbehrlich bei nur einem Hub"`
}

// SyncOutput ist die Antwort von sync. Ein Erfolg (ohne isError) trägt Hub
// und Sync, und zugesichert ist: Sync.Revision steht in der Antwort (Wert ab
// 0), Sync.LastError ist leer, Sync.LastSuccess ist der Zeitpunkt des
// abgewarteten Abgleichs. Bei einem Fehler (isError) tragen nur Hub — die
// Angabe des Aufrufs, nach dem Auflösen der Alias — und Error etwas.
type SyncOutput struct {
	// Hub ist der Alias des Hub-Eintrags.
	Hub string `json:"hub"`
	// Sync ist der Stand nach dem Abgleich; fehlt bei einem Fehler.
	Sync *SyncInfo `json:"sync,omitempty"`
	// Error ist der Fehler, nur mit isError.
	Error *WriteError `json:"error,omitempty"`
}

// syncDescription nennt die Wartefrist als Zahl, aus SyncWaitLimit.
var syncDescription = "Fordert einen Abgleich eines Hubs an und wartet auf sein Ergebnis: Der Node holt vom Hub, " +
	"was sich geändert hat, in seine Replica. Nötig nur, wenn es auf den Stand jetzt ankommt — etwa bevor ein " +
	"Client prüft, was andere inzwischen geschrieben haben; sonst zeigen read, list, changes und search den Stand " +
	"des letzten Abgleichs, den der Node regelmäßig selbst macht (whoami nennt ihn). Eigene Schreibvorgänge stehen " +
	"sofort in der Replica. hub: der Alias des Hub-Eintrags, entbehrlich bei nur einem Hub; aufrufen darf, wer an " +
	"diesem Hub gültig angemeldet ist. Die Antwort kommt erst, wenn ein Abgleich geendet hat, der nach der Anfrage " +
	"begonnen hat, höchstens nach " + limitText(SyncWaitLimit) + ". Erfolg: hub und sync mit revision, last_success des abgewarteten Abgleichs " +
	"und ohne last_error. Ein Fehler trägt error.code: sync_timeout (Frist abgelaufen, der Abgleich läuft im " +
	"Hintergrund weiter), never_synced (noch keine Replica: zuerst kephalaion node sync am Rechner des Nodes), " +
	"unreachable, unauthenticated, unsupported_version, hub_refused, unknown_hub, not_readable, invalid, " +
	"unsupported, internal."

func (n *Node) sync(ctx context.Context, req *mcp.CallToolRequest, in SyncInput) (*mcp.CallToolResult, SyncOutput, error) {
	reqlog.Note(ctx, "op", "sync")
	out := SyncOutput{Hub: in.Hub}
	err := n.doSync(ctx, req, in, &out)
	if err == nil {
		text := fmt.Sprintf("Hub %s: %s", out.Hub, DescribeSync(*out.Sync))
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}, out, nil
	}
	var te *toolError
	if !errors.As(toolFailure(ctx, err), &te) {
		// Vom Aufrufer abgebrochen: ein Fehler der Anfrage, ohne Code.
		return nil, SyncOutput{}, err
	}
	code := te.code
	if code == "" {
		code = string(contract.CodeInvalid)
	}
	reqlog.Note(ctx, "code", code)
	fail := SyncOutput{Hub: out.Hub, Error: &WriteError{Code: code, Message: te.msg}}
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: te.msg}}}, fail, nil
}

// doSync prüft, stößt an, wartet und liest den Stand — in der Reihenfolge
// der Tabelle „Codes je Fall“ (docs/konzept.md, „Abgleich“): Eingang, node.db,
// das Feld hub, verdeckt, Eintrag, Replica (checkHub: lesbar, vorhanden, dann
// erst die Anmeldung), Anstoß mit Warten, Revision. Angestoßen wird erst nach
// gültiger Anmeldung am Hub des Eintrags.
func (n *Node) doSync(ctx context.Context, req *mcp.CallToolRequest, in SyncInput, out *SyncOutput) error {
	if n.link.SyncWait == nil {
		return &toolError{msg: "Abgleich auf Anforderung ist an diesem Eingang nicht eingerichtet", code: CodeUnsupported}
	}
	r, err := n.begin(ctx, req)
	if err != nil {
		return err
	}
	hub, err := r.syncHub(in.Hub)
	if err != nil {
		return err
	}
	out.Hub = hub
	if _, ok := r.entries[hub]; !ok {
		if r.hidden {
			return syncNoLogin(hub)
		}
		return &toolError{msg: fmt.Sprintf("Hub %s: dieser Node hat keinen Hub-Eintrag mit diesem Alias%s", hub,
			r.aliasHint()), code: CodeUnknownHub}
	}
	h, _, rep, err := r.checkHub(ctx, hub)
	reqlog.Note(ctx, "hub", hub)
	reqlog.Note(ctx, "node", h.NodeName)
	switch {
	case errors.Is(err, errNeverSynced):
		return &toolError{msg: fmt.Sprintf("Hub %s: noch nie abgeglichen, der Node hat keine Replica. Den ersten "+
			"Abgleich macht kephalaion node sync %s am Rechner des Nodes — nicht dieses Werkzeug und nicht der "+
			"Client.", hub, hub), code: CodeNeverSynced}
	case errors.Is(err, errNoLogin):
		return syncNoLogin(hub)
	case err != nil:
		return err
	}
	_ = rep.Close()

	// Die Frist gilt nur dem Warten: Läuft sie ab, läuft der Abgleich weiter.
	wctx, cancel := context.WithTimeout(ctx, syncWaitLimit)
	defer cancel()
	res, err := n.link.SyncWait(wctx, h)
	switch {
	case err == nil:
	case ctx.Err() != nil:
		// Der Aufrufer hat abgebrochen; toolFailure reicht es als Fehler der
		// Anfrage durch.
		return err
	case errors.Is(err, ErrUnsupported):
		return &toolError{msg: fmt.Sprintf("Hub %s: Abgleich über den Transport dieses Hubs wird noch nicht "+
			"unterstützt", hub), code: CodeUnsupported}
	case errors.Is(err, ErrSyncGone):
		return &toolError{msg: fmt.Sprintf("Hub %s: der Hub-Eintrag wurde während des Abgleichs entfernt oder neu "+
			"angelegt; kein Ergebnis. Den Aufruf wiederholen.", hub), code: CodeInternal}
	case errors.Is(err, ErrSyncStopped):
		return &toolError{msg: fmt.Sprintf("Hub %s: der Node beendet sich; kein Ergebnis. Den Aufruf wiederholen, "+
			"sobald der Node wieder läuft.", hub), code: CodeInternal}
	case wctx.Err() != nil || errors.Is(err, context.DeadlineExceeded):
		// Die Wartefrist, nicht der Aufrufer. Vor toolFailure zum Fehler mit
		// Code: Dort wäre jedes DeadlineExceeded ein Fehler der Anfrage ohne
		// Code.
		return &toolError{msg: fmt.Sprintf("Hub %s: der Abgleich ist nach %s noch nicht beendet (Wartefrist). Er "+
			"läuft im Hintergrund weiter; gescheitert ist er damit nicht — etwa ein Hub, der gerade nicht antwortet. "+
			"Den Aufruf wiederholen; den Stand nennt whoami.", hub, limitText(syncWaitLimit)), code: CodeSyncTimeout}
	default:
		reqlog.NoteError(ctx, err)
		return &toolError{msg: fmt.Sprintf("Hub %s: Abgleich nicht angestoßen", hub), code: CodeInternal}
	}
	if res.Kind != "" {
		return syncFailed(hub, res)
	}
	// Nur die Revision kommt aus der Replica — der Stand beim Antworten;
	// Erfolg und Zeitpunkt sind die des abgewarteten Abgleichs, gleich was
	// hub_sync inzwischen zeigt.
	rev, err := replicaRevision(ctx, n.nodes, h)
	if err != nil {
		return err
	}
	if rev == nil {
		return &toolError{msg: fmt.Sprintf("Hub %s: abgeglichen, aber die Replica ist inzwischen nicht mehr da "+
			"(Hub-Eintrag entfernt oder neu angelegt?). Den Aufruf wiederholen.", hub), code: CodeInternal}
	}
	out.Sync = &SyncInfo{LastSuccess: formatTime(res.At), Revision: rev}
	return nil
}

// syncHub wählt den Hub-Eintrag eines Aufrufs von sync. Ein angegebener Alias
// wird nur als Name geprüft und nie als Adresse aufgelöst (resolve nähme ihn
// als Collection, und der Hub käme aus der Regel für den fehlenden Hub-Teil);
// ohne Angabe gilt diese Regel (defaultHub). Die Meldung nennt das Feld hub
// und, wenn die Antwort nicht verdeckt ist, die Aliase, die in Frage kommen.
func (r *request) syncHub(alias string) (string, error) {
	if alias != "" {
		if err := ident.CheckName("Hub", alias); err != nil {
			return "", &toolError{msg: fmt.Sprintf("Feld hub: %v. Erwartet ist der Alias eines Hub-Eintrags%s", err,
				r.aliasHint())}
		}
		return alias, nil
	}
	hub, valid, ok := r.defaultHub()
	switch {
	case ok:
		return hub, nil
	case len(valid) > 1:
		return "", &toolError{msg: fmt.Sprintf("Feld hub fehlt: an mehreren Hubs gültig angemeldet (%s); es nennt "+
			"den Alias des Hub-Eintrags", strings.Join(valid, ", "))}
	}
	return "", &toolError{msg: "Feld hub fehlt: an keinem Hub gültig angemeldet; es nennt den Alias des " +
		"Hub-Eintrags" + r.aliasHint()}
}

// aliasHint nennt die Aliase der Hub-Einträge des Nodes als Zusatz zu einer
// Meldung — verdeckt und ohne Eintrag leer.
func (r *request) aliasHint() string {
	if r.hidden || len(r.entries) == 0 {
		return ""
	}
	aliases := make([]string, 0, len(r.entries))
	for a := range r.entries {
		aliases = append(aliases, a)
	}
	sort.Strings(aliases)
	return " (Hubs dieses Nodes: " + strings.Join(aliases, ", ") + ")"
}

// syncNoLogin ist die eine Meldung für einen Aufruf ohne gültige Anmeldung am
// Hub — und, verdeckt, für jeden Alias, ob es ihn gibt oder nicht.
func syncNoLogin(hub string) error {
	return &toolError{msg: "Hub " + hub + ": kein Abgleich — unbekannt oder ohne gültige Anmeldung an diesem Hub",
		code: string(contract.CodeNotReadable)}
}

// syncFailed macht aus einem gescheiterten Abgleich den Fehler des Werkzeugs:
// der Code nach Art (Tabelle „Codes je Fall“), die Meldung aus dem Alias und
// dem festen Satz der Art — nie aus dem Fehlertext des Abgleichs.
func syncFailed(hub string, res SyncOutcome) error {
	code := CodeInternal
	switch res.Kind {
	case replica.KindUnreachable, replica.KindConnect:
		code = CodeUnreachable
	case replica.KindHub:
		// Ohne Fehler des Vertrags hat nicht der Hub geantwortet, sondern
		// etwas davor — ein Proxy vor einem gestoppten Hub etwa.
		code = CodeUnreachable
		if res.Contract {
			code = CodeHubRefused
		}
	case replica.KindUnauthenticated:
		code = string(contract.CodeUnauthenticated)
	case replica.KindVersion:
		code = string(contract.CodeUnsupportedVersion)
	case replica.KindProtocol:
		code = CodeHubRefused
	}
	return &toolError{msg: fmt.Sprintf("Hub %s: Abgleich gescheitert — %s", hub, res.Kind.Text()), code: code}
}
