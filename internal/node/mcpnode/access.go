package mcpnode

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kephalaion/kephalaion/internal/contract"
	"github.com/kephalaion/kephalaion/internal/ident"
	"github.com/kephalaion/kephalaion/internal/node/replica"
	"github.com/kephalaion/kephalaion/internal/node/store"
	"github.com/kephalaion/kephalaion/internal/reqlog"
)

// Der gemeinsame Schritt der Werkzeuge, die Inhalte liefern (list, read,
// changes): Anmeldung über alle Hubs (Authenticate), Adresse auflösen,
// Replica des Hubs öffnen, Recht prüfen. Gelesen wird nur aus der Replica;
// lesbar ist eine Collection, wenn der Client an ihrem Hub gültig angemeldet
// ist und sein Account dort eine lebende Zeile SYSTEM:A: hat — es zählen also
// nur Collections, die der Node führt.

// toolError ist ein Fehler, dessen Meldung der Client sieht. Sie nennt nie
// einen Pfad, ein Token oder einen Hash. code ist der Code, den die
// Werkzeuge, die schreiben, neben der Meldung liefern (write.go); leer heißt
// invalid. Die Werkzeuge, die lesen, melden nur die Meldung.
type toolError struct {
	msg  string
	code string
}

func (e *toolError) Error() string { return e.msg }

// notReadable ist die eine Meldung für alles, was der Client nicht lesen
// darf: unbekannter Hub, unbekannte Collection, eine Collection, die der
// Node nicht führt, keine gültige Anmeldung, kein read. Sie unterscheidet die
// Fälle nicht.
func notReadable(addr string) error {
	return &toolError{msg: addr + " nicht lesbar: unbekannt, nicht auf diesem Node oder ohne gültige Anmeldung mit read",
		code: string(contract.CodeNotReadable)}
}

// neverSynced meldet einen Hub-Eintrag ohne Replica — wie never_synced in
// whoami — und nennt den Ausweg. Für die Werkzeuge, die schreiben, ist das
// nicht lesbar: Der Node prüft vor dem Schreiben gegen die Replica.
func neverSynced(hub string) error {
	return &toolError{msg: "Hub " + hub + ": noch nie abgeglichen, der Node hat keine Replica; zuerst: kephalaion node sync " +
		hub, code: string(contract.CodeNotReadable)}
}

// replicaUnreadable ist die Meldung zu einem Hub, dessen Replica sich nicht
// lesen lässt — ohne Pfad; die volle Meldung geht ins Log.
func replicaUnreadable(hub string) error {
	return &toolError{msg: "Hub " + hub + ": " + ReplicaUnreadable, code: CodeInternal}
}

// errNodeDB ist die Meldung zu einem Fehler von node.db.
var errNodeDB = &toolError{msg: "Datenbank des Nodes nicht lesbar", code: CodeInternal}

// toolFailure macht aus einem Fehler das, was der Client sieht: Ein
// toolError bleibt, ein abgebrochener ctx ist ein Fehler der Anfrage, eine
// unlesbare Replica wird zur festen Meldung ihres Hubs (die volle ins Log),
// alles andere ist ein Fehler von node.db (ins Log).
func toolFailure(ctx context.Context, err error) error {
	var te *toolError
	switch {
	case errors.As(err, &te):
		return te
	case ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
		return err
	}
	reqlog.NoteError(ctx, err)
	if ue := asUnreadable(err); ue != nil {
		return replicaUnreadable(ue.Hub)
	}
	return errNodeDB
}

// request ist der gemeinsame Stand einer Anfrage: die Anmeldungen und die
// Hub-Einträge.
type request struct {
	nodes   store.Store
	logins  Logins
	entries map[string]store.Hub
}

// requestHeader sind die HTTP-Header der Anfrage eines Werkzeugs.
func requestHeader(req *mcp.CallToolRequest) http.Header {
	if req != nil && req.Extra != nil {
		return req.Extra.Header
	}
	return nil
}

// begin meldet die Anfrage über alle Hubs an. Ist die Antwort verdeckt (über
// einen Proxy, an keinem Hub gültig angemeldet), kennt die Anfrage keinen
// Hub-Eintrag und keine Anmeldung: Jede Meldung danach klingt, als hätte der
// Node keinen Hub — eine Adresse ohne Hub-Teil nimmt keinen Eintrag, eine mit
// Hub-Teil ist nicht lesbar, ob es den Hub gibt oder nicht, und keine Replica
// wird geöffnet.
func (n *Node) begin(ctx context.Context, req *mcp.CallToolRequest) (*request, error) {
	header := requestHeader(req)
	logins, err := n.Authenticate(ctx, header)
	if err != nil {
		return nil, err
	}
	if hides(header, logins) {
		return &request{nodes: n.nodes, logins: Logins{Unknown: []string{}}, entries: map[string]store.Hub{}}, nil
	}
	hubs, err := n.nodes.Hubs(ctx)
	if err != nil {
		return nil, err
	}
	r := &request{nodes: n.nodes, logins: logins, entries: make(map[string]store.Hub, len(hubs))}
	for _, h := range hubs {
		r.entries[h.Name] = h
	}
	return r, nil
}

// login liefert die Anmeldung an einem Hub; ok ist false ohne Eintrag.
func (r *request) login(hub string) (Login, bool) {
	for _, l := range r.logins.Hubs {
		if l.Hub == hub {
			return l, true
		}
	}
	return Login{}, false
}

// validHubs sind die Aliase der Hubs, an denen der Client gültig angemeldet
// ist, nach Alias.
func (r *request) validHubs() []string {
	var out []string
	for _, l := range r.logins.Valid() {
		out = append(out, l.Hub)
	}
	return out
}

// target ist eine aufgelöste Adresse: ein Hub und darin eine Collection,
// oder die Wurzel des Hubs (Collection leer).
type target struct {
	Hub        string
	Collection string
}

// Address ist die Adresse des Ziels: <hub>:<collection>, bei der Wurzel
// <hub>:.
func (t target) Address() string { return t.Hub + ":" + t.Collection }

// resolve löst eine Adresse auf: <hub>:<collection>, <hub>: für die Wurzel
// eines Hubs, <collection> ohne Hub-Teil, oder leer — dann nur den Hub (für
// read per id). Der fehlende Hub-Teil ist der eine Hub, an dem der Client
// gültig angemeldet ist; ist er an keinem angemeldet und hat der Node genau
// einen Eintrag, dieser. Sonst nennt der Fehler die möglichen Hubs.
func (r *request) resolve(addr string) (target, error) {
	what := fmt.Sprintf("Adresse %q", addr)
	if addr == "" {
		what = "ohne collection"
	}
	hub, coll, withHub := strings.Cut(addr, ":")
	if !withHub {
		coll = addr
		valid := r.validHubs()
		switch {
		case len(valid) == 1:
			hub = valid[0]
		case len(valid) == 0 && len(r.entries) == 1:
			for h := range r.entries {
				hub = h
			}
		case len(valid) == 0:
			return target{}, &toolError{msg: what + " ohne Hub: an keinem Hub gültig angemeldet; erwartet <hub>:<collection>"}
		default:
			return target{}, &toolError{msg: fmt.Sprintf("%s ohne Hub: angemeldet an %s; erwartet <hub>:<collection>",
				what, strings.Join(valid, ", "))}
		}
	}
	if err := ident.CheckName("Hub", hub); err != nil {
		return target{}, &toolError{msg: fmt.Sprintf("%s: %v", what, err)}
	}
	if coll != "" || (!withHub && addr != "") {
		if err := ident.CheckName("Collection", coll); err != nil {
			return target{}, &toolError{msg: fmt.Sprintf("%s: %v", what, err)}
		}
	}
	return target{Hub: hub, Collection: coll}, nil
}

// hubAccess ist die geöffnete Replica eines Hubs mit den lesbaren
// Collections des Clients darin. Close schließt sie.
type hubAccess struct {
	hub    string
	rep    *replica.Replica
	rights map[string]contract.Rights
}

func (a *hubAccess) Close() { _ = a.rep.Close() }

// wrap ordnet einen Fehler beim Lesen der Replica ein: Ein toolError bleibt,
// sonst ist die Replica nicht lesbar, außer bei abgebrochenem ctx.
func (a *hubAccess) wrap(ctx context.Context, err error) error {
	var te *toolError
	if err == nil || errors.As(err, &te) {
		return err
	}
	return unreadable(ctx, a.hub, err)
}

// readable sagt, ob der Client die Collection lesen darf.
func (a *hubAccess) readable(coll string) bool {
	_, ok := a.rights[coll]
	return ok
}

// collections sind die lesbaren Collections, nach Name.
func (a *hubAccess) collections() []string {
	out := make([]string, 0, len(a.rights))
	for c := range a.rights {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// open öffnet die Replica eines Hubs für das Ziel t. Fehler in dieser
// Reihenfolge: ein Hub ohne Eintrag ist nicht lesbar; eine Replica, die sich
// nicht lesen lässt, betrifft nur diesen Hub (UnreadableError); ohne Replica
// „noch nie abgeglichen“ — auch ohne gültige Anmeldung, wie in whoami. Ohne
// gültige Anmeldung ist die Wurzel des Hubs nicht lesbar, ebenso eine
// Collection ohne Recht.
func (r *request) open(ctx context.Context, t target) (*hubAccess, error) {
	a, err := r.openHub(ctx, t.Hub)
	if err != nil {
		if errors.Is(err, errNoLogin) {
			return nil, notReadable(t.Address())
		}
		return nil, err
	}
	if t.Collection != "" && !a.readable(t.Collection) {
		a.Close()
		return nil, notReadable(t.Address())
	}
	return a, nil
}

// errNoLogin meldet in openHub einen Hub ohne gültige Anmeldung.
var errNoLogin = errors.New("keine gültige Anmeldung")

// openHub öffnet die Replica eines Hubs; siehe open. Ohne gültige Anmeldung
// ist der Fehler errNoLogin.
func (r *request) openHub(ctx context.Context, hub string) (*hubAccess, error) {
	h, ok := r.entries[hub]
	l, _ := r.login(hub)
	if !ok {
		return nil, errNoLogin
	}
	if l.Unreadable != nil {
		return nil, l.Unreadable
	}
	rep, err := openReplica(ctx, r.nodes, h)
	if err != nil {
		return nil, err
	}
	if rep == nil {
		return nil, neverSynced(hub)
	}
	if l.State != LoginOK {
		_ = rep.Close()
		return nil, errNoLogin
	}
	a := &hubAccess{hub: hub, rep: rep, rights: make(map[string]contract.Rights, len(l.Rights))}
	for _, right := range l.Rights {
		a.rights[right.Collection] = right.Rights
	}
	return a, nil
}

// eachValid öffnet nacheinander die Replica jedes Hubs, an dem der Client
// gültig angemeldet ist, nach Alias, und ruft fn. Eine Replica, die sich nicht
// lesen lässt, betrifft nur ihren Hub: Er steht in unread (die volle Meldung
// im Log), die übrigen laufen weiter. Ein Hub, dessen Replica inzwischen
// fehlt, trägt nichts bei.
func (r *request) eachValid(ctx context.Context, fn func(a *hubAccess) error) (unread []string, err error) {
	unread = []string{}
	for _, l := range r.logins.Hubs {
		if l.Unreadable != nil {
			reqlog.NoteError(ctx, l.Unreadable)
			unread = append(unread, l.Hub)
			continue
		}
		if l.State != LoginOK {
			continue
		}
		a, err := r.openHub(ctx, l.Hub)
		var te *toolError
		switch {
		case asUnreadable(err) != nil && ctx.Err() == nil:
			reqlog.NoteError(ctx, err)
			unread = append(unread, l.Hub)
			continue
		case errors.As(err, &te), errors.Is(err, errNoLogin):
			continue
		case err != nil:
			return nil, err
		}
		err = a.wrap(ctx, fn(a))
		a.Close()
		if ue := asUnreadable(err); ue != nil && ctx.Err() == nil {
			reqlog.NoteError(ctx, err)
			unread = append(unread, l.Hub)
			continue
		}
		if err != nil {
			return nil, err
		}
	}
	return unread, nil
}
