package mcpnode

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kephalaion/kephalaion/internal/ident"
	"github.com/kephalaion/kephalaion/internal/node/replica"
	"github.com/kephalaion/kephalaion/internal/reqlog"
)

// ChangesInput sind die Argumente von changes.
type ChangesInput struct {
	Collection string `json:"collection,omitempty" jsonschema:"<hub>:<collection> oder <hub>:, Hub-Teil entbehrlich bei nur einem Hub; leer alle lesbaren Collections aller angemeldeten Hubs"`
	Path       string `json:"path,omitempty" jsonschema:"nur Dokumente unter diesem Verzeichnis"`
	Cursor     string `json:"cursor,omitempty" jsonschema:"cursor der letzten Antwort: lückenlos weiter"`
	Since      string `json:"since,omitempty" jsonschema:"statt cursor ein Zeitpunkt (RFC 3339), Zeit des Hubs beim Schreiben; nicht lückenlos"`
	Limit      int    `json:"limit,omitempty" jsonschema:"höchstens so viele Dokumente, Standard 100, höchstens 1000"`
}

// ChangesOutput ist die Antwort von changes.
type ChangesOutput struct {
	// Changes nennt je geändertem Dokument einmal seinen aktuellen Stand,
	// nach Adresse, Revision und id.
	Changes []Change `json:"changes"`
	// Cursor fragt beim nächsten Aufruf weiter; More sagt, dass schon jetzt
	// mehr vorliegt.
	Cursor string `json:"cursor"`
	More   bool   `json:"more"`
	// Reset nennt Hubs, deren Replica seit dem cursor neu angelegt oder
	// geleert wurde: Der Aufrufer liest sie neu mit list; der cursor gilt für
	// sie ab jetzt.
	Reset []string `json:"reset,omitempty"`
	// Dropped nennt Collections des cursors, die nicht mehr lesbar sind
	// (Recht entzogen, am Node abgewählt, entfernt): Ihre Dokumente
	// verschwinden ohne Löschmarke.
	Dropped []string `json:"dropped,omitempty"`
	// UnreadableHubs nennt Hubs, deren Replica sich nicht lesen ließ; ihr
	// Stand im cursor bleibt, wie er war.
	UnreadableHubs []string `json:"unreadable_hubs,omitempty"`
}

// Change ist der aktuelle Stand eines geänderten Dokuments; ohne alten Namen
// — ein Umbenennen erkennt der Aufrufer an der id.
type Change struct {
	Address  string `json:"address"`
	Name     string `json:"name"`
	ID       string `json:"id"`
	Revision int64  `json:"revision"`
	Deleted  bool   `json:"deleted"`
	Updated  Stamp  `json:"updated"`
}

const changesDescription = "Meldet geänderte Dokumente aus der Replica, je Dokument einmal der aktuelle Stand " +
	"(auch gelöscht). Ohne cursor und since nur der cursor für ab jetzt; mit dem cursor der letzten Antwort " +
	"lückenlos weiter. reset: Hub neu mit list lesen; dropped: Collection nicht mehr lesbar."

// changesCursor ist der Cursor von changes: je Hub die generation seiner
// Replica und je Collection der Stand, bis zu dem der Aufrufer alles hat.
type changesCursor struct {
	V    int                  `json:"v"`
	Q    string               `json:"q"`
	Hubs map[string]hubCursor `json:"h"`
}

type hubCursor struct {
	G string                `json:"g"`
	C map[string]collCursor `json:"c"`
}

// collCursor ist der Stand einer Collection: alles bis Revision R, in
// Revision R nur bis einschließlich id I, wenn I gesetzt ist.
type collCursor struct {
	R int64  `json:"r"`
	I string `json:"i,omitempty"`
}

// changesMode sagt, wo eine Collection ohne Stand im cursor beginnt.
type changesMode int

const (
	fromNow   changesMode = iota // ohne cursor und since: ab jetzt
	fromStart                    // mit cursor: eine neu lesbare Collection liefert alles
	fromSince                    // mit since: ab der ersten Zeile zu oder nach since
)

// changesRun ist ein Aufruf von changes.
type changesRun struct {
	in     ChangesInput
	mode   changesMode
	since  int64
	prefix string
	limit  int
	prev   *changesCursor
	out    ChangesOutput
	next   changesCursor
}

// collWork ist eine Collection, aus der changes liest.
type collWork struct {
	coll  string
	start collCursor
	// read: ab start lesen; sonst gilt start schon als neuer Stand.
	read bool
}

func (n *Node) changes(ctx context.Context, req *mcp.CallToolRequest, in ChangesInput) (*mcp.CallToolResult, ChangesOutput, error) {
	out, err := n.doChanges(ctx, req, in)
	if err != nil {
		return nil, ChangesOutput{}, toolFailure(ctx, err)
	}
	return nil, out, nil
}

func (n *Node) doChanges(ctx context.Context, req *mcp.CallToolRequest, in ChangesInput) (ChangesOutput, error) {
	run, err := newChangesRun(in)
	if err != nil {
		return ChangesOutput{}, err
	}
	r, err := n.begin(ctx, req)
	if err != nil {
		return ChangesOutput{}, err
	}
	// Die Hubs der Anfrage: einer mit collection, sonst alle Einträge und
	// die Hubs des cursors, die es nicht mehr gibt.
	var t target
	var hubs []string
	if in.Collection != "" {
		if t, err = r.resolve(in.Collection); err != nil {
			return ChangesOutput{}, err
		}
		hubs = []string{t.Hub}
	} else {
		seen := map[string]bool{}
		for h := range r.entries {
			seen[h] = true
		}
		if run.prev != nil {
			for h := range run.prev.Hubs {
				seen[h] = true
			}
		}
		for h := range seen {
			hubs = append(hubs, h)
		}
		sort.Strings(hubs)
	}
	for _, hub := range hubs {
		var a *hubAccess
		if in.Collection != "" && run.prev == nil {
			// Ohne cursor meldet eine einzelne Adresse als Fehler, was nicht
			// geht: nicht lesbar, noch nie abgeglichen, Replica nicht lesbar.
			if a, err = r.open(ctx, t); err != nil {
				return ChangesOutput{}, err
			}
		} else {
			a, err = r.openHub(ctx, hub)
			var te *toolError
			switch {
			case err == nil:
			case errors.Is(err, errNoLogin), errors.As(err, &te):
				a, err = nil, nil
			}
		}
		if err == nil {
			err = run.hub(ctx, hub, t.Collection, a)
		}
		if a != nil {
			a.Close()
		}
		if ue := asUnreadable(err); ue != nil && ctx.Err() == nil {
			reqlog.NoteError(ctx, err)
			run.unreadable(hub)
			continue
		}
		if err != nil {
			return ChangesOutput{}, err
		}
	}
	sort.Strings(run.out.Dropped)
	run.out.Cursor = encodeCursor(run.next)
	return run.out, nil
}

func newChangesRun(in ChangesInput) (*changesRun, error) {
	run := &changesRun{in: in, out: ChangesOutput{Changes: []Change{}}}
	var err error
	if run.limit, err = checkLimit(in.Limit); err != nil {
		return nil, err
	}
	if run.prefix, err = ident.DocDirPrefix(in.Path); err != nil {
		return nil, &toolError{msg: "path: " + err.Error()}
	}
	fp := fingerprint("changes", in.Collection, in.Path)
	run.next = changesCursor{V: cursorVersion, Q: fp, Hubs: map[string]hubCursor{}}
	switch {
	case in.Cursor != "" && in.Since != "":
		return nil, &toolError{msg: "cursor oder since, nicht beides"}
	case in.Cursor != "":
		run.prev = &changesCursor{}
		if err := decodeCursor(in.Cursor, run.prev); err != nil {
			return nil, err
		}
		if run.prev.V != cursorVersion || run.prev.Q != fp {
			return nil, errCursorMismatch
		}
		run.mode = fromStart
	case in.Since != "":
		ts, err := time.Parse(time.RFC3339Nano, in.Since)
		if err != nil {
			return nil, &toolError{msg: "since: erwartet einen Zeitpunkt in RFC 3339, etwa 2026-09-26T10:00:00Z"}
		}
		run.mode, run.since = fromSince, ts.UnixMilli()
	}
	return run, nil
}

// had liefert den Stand eines Hubs im cursor des Aufrufers.
func (run *changesRun) had(hub string) (hubCursor, bool) {
	if run.prev == nil {
		return hubCursor{}, false
	}
	h, ok := run.prev.Hubs[hub]
	return h, ok
}

// unreadable nimmt zurück, was ein Hub zu dieser Antwort beigetragen hat,
// und behält seinen Stand aus dem cursor: Eine Replica, die sich nicht lesen
// lässt, betrifft nur ihren Hub.
func (run *changesRun) unreadable(hub string) {
	run.out.UnreadableHubs = append(run.out.UnreadableHubs, hub)
	keep := run.out.Changes[:0]
	for _, c := range run.out.Changes {
		if !strings.HasPrefix(c.Address, hub+":") {
			keep = append(keep, c)
		}
	}
	run.out.Changes = keep
	for _, list := range []*[]string{&run.out.Dropped, &run.out.Reset} {
		kept := (*list)[:0]
		for _, s := range *list {
			if s != hub && !strings.HasPrefix(s, hub+":") {
				kept = append(kept, s)
			}
		}
		*list = kept
	}
	delete(run.next.Hubs, hub)
	if had, ok := run.had(hub); ok {
		run.next.Hubs[hub] = had
	}
}

// hub trägt einen Hub bei: weggefallene Collections, reset, und aus jeder
// lesbaren Collection (nur only, wenn gesetzt) die Änderungen, solange die
// Seite nicht voll ist. a ist nil ohne gültige Anmeldung oder Replica.
// Fehler beim Lesen der Replica sind UnreadableError.
func (run *changesRun) hub(ctx context.Context, hub, only string, a *hubAccess) error {
	had, hadHub := run.had(hub)
	readable := map[string]bool{}
	if a != nil {
		for _, c := range a.collections() {
			if only == "" || c == only {
				readable[c] = true
			}
		}
	}
	for c := range had.C {
		if !readable[c] {
			run.out.Dropped = append(run.out.Dropped, ident.Address(hub, c))
		}
	}
	if len(readable) == 0 {
		return nil
	}
	// Die generation stammt vom Öffnen, vor allem Gelesenen: Leert ein
	// Abgleich die Replica danach, merkt es der nächste Aufruf.
	gen := a.rep.Generation()
	reset := hadHub && had.G != gen
	if reset {
		run.out.Reset = append(run.out.Reset, hub)
	}
	hc := hubCursor{G: gen, C: map[string]collCursor{}}
	run.next.Hubs[hub] = hc
	colls := make([]string, 0, len(readable))
	for c := range readable {
		colls = append(colls, c)
	}
	sort.Strings(colls)
	for _, c := range colls {
		w, err := run.start(ctx, a.rep, c, had, reset)
		if err != nil {
			return a.wrap(ctx, err)
		}
		hc.C[c] = w.start
		if !w.read || run.out.More {
			continue
		}
		end, err := run.read(ctx, a.rep, hub, w)
		if err != nil {
			return a.wrap(ctx, err)
		}
		hc.C[c] = end
	}
	return nil
}

// start bestimmt, ab wo eine Collection zu lesen ist: nach reset und ohne
// cursor ab jetzt (der Stand der Replica), mit Stand im cursor ab dort,
// neu lesbar mit cursor von vorn, mit since ab der ersten Zeile zu oder nach
// dem Zeitpunkt.
func (run *changesRun) start(ctx context.Context, rep *replica.Replica, c string, had hubCursor, reset bool) (collWork, error) {
	w := collWork{coll: c}
	cur, known := had.C[c]
	switch {
	case !reset && known:
		w.start, w.read = cur, true
		return w, nil
	case !reset && run.mode == fromStart:
		w.read = true
		return w, nil
	case !reset && run.mode == fromSince:
		first, ok, err := rep.FirstRevisionSince(ctx, c, run.prefix, run.since)
		if err != nil {
			return w, err
		}
		if ok {
			w.start, w.read = collCursor{R: first - 1}, true
			return w, nil
		}
	}
	rev, _, err := rep.StateOf(ctx, c)
	w.start = collCursor{R: rev}
	return w, err
}

// read liest die Änderungen einer Collection ab w.start bis zu ihrem Stand in
// der Replica, höchstens bis die Seite voll ist, und liefert den neuen Stand.
// Der Stand geht nie zurück: Wird eine Collection am Node abgewählt und
// wieder gewählt, gleicht sie von vorn ab; was darunter liegt, hat der
// Aufrufer schon.
func (run *changesRun) read(ctx context.Context, rep *replica.Replica, hub string, w collWork) (collCursor, error) {
	upto, _, err := rep.StateOf(ctx, w.coll)
	if err != nil {
		return w.start, err
	}
	rest := run.limit - len(run.out.Changes)
	rows, err := rep.ChangedEntries(ctx, w.coll, run.prefix, replica.ChangeKey{Rev: w.start.R, ID: w.start.I}, upto, rest+1)
	if err != nil {
		return w.start, err
	}
	end := w.start
	if upto >= w.start.R {
		end = collCursor{R: upto}
	}
	if len(rows) > rest {
		run.out.More = true
		rows = rows[:rest]
		end = w.start
		if rest > 0 {
			last := rows[rest-1]
			end = collCursor{R: last.Revision, I: last.ID}
		}
	}
	addr := ident.Address(hub, w.coll)
	for _, e := range rows {
		run.out.Changes = append(run.out.Changes, Change{Address: addr, Name: e.Name, ID: e.ID, Revision: e.Revision,
			Deleted: e.Deleted, Updated: *stamp(e.UpdatedAt, e.UpdatedBy)})
	}
	return end, nil
}
