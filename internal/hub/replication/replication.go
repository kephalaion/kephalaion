// Package replication ist die Seite des Hubs im Vertrag (docs/vertrag.md):
// Es setzt contract.Hub über dem Hub-Store um. Hier stehen Fassung, Form der
// Anfrage, Anmeldung von Node und Account, erlaubte Collections, der Schnitt
// der Seiten und die Codes der Fehler; der Store liefert Zeilen und schreibt
// rotate und die Schreibvorgänge (write.go) je in einer Transaktion, in der
// er Account, Lesbarkeit und Recht prüft. So gilt die Logik für jede
// Umsetzung des Stores, und jeder Transport (local, HTTP) ruft dieselbe
// Prüfung.
package replication

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"slices"

	"github.com/kephalaion/kephalaion/internal/contract"
	"github.com/kephalaion/kephalaion/internal/hub/store"
	"github.com/kephalaion/kephalaion/internal/ident"
)

// MaxPageSize ist die Obergrenze des Hubs für die Seitengröße; eine größere
// Anfrage wird darauf begrenzt.
const MaxPageSize = 5000

// dummyHash wird verglichen, wenn es den Node nicht gibt, dummyAccountHash,
// wenn es den Account nicht gibt: So kostet ein unbekannter Name dieselbe
// Arbeit wie ein falsches Token.
var (
	dummyHash        = ident.HashToken("keph_unbekannter-node")
	dummyAccountHash = ident.HashToken("keph_unbekannter-account")
)

// Hub setzt contract.Hub über einem Hub-Store um.
type Hub struct {
	st          store.Store
	maxPageSize int
}

var _ contract.Hub = (*Hub)(nil)

// New liefert die Seite des Hubs über st. Der Store bleibt beim Aufrufer.
func New(st store.Store) *Hub {
	return &Hub{st: st, maxPageSize: MaxPageSize}
}

// authenticate prüft Name, Token und Sperre des Nodes und liefert seine
// erlaubten Collections. Unbekannt, falsches Token und gesperrt ergeben
// denselben Fehler; der Hash wird in jedem Fall in konstanter Zeit
// verglichen.
func (h *Hub) authenticate(ctx context.Context, auth contract.NodeAuth) ([]string, error) {
	n, err := h.st.Node(ctx, auth.Node)
	known := err == nil
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	want := dummyHash
	if known {
		want = n.TokenHash
	}
	match := subtle.ConstantTimeCompare([]byte(ident.HashToken(auth.Token)), []byte(want)) == 1
	if !known || !match || n.Locked {
		return nil, contract.ErrUnauthenticated
	}
	return n.Collections, nil
}

// checkAccount prüft Name, Token und Sperre eines Accounts gegen accounts —
// dort steht der maßgebliche Hash. Unbekannt, falsches Token und gesperrt
// ergeben ok false; der Hash wird in jedem Fall in konstanter Zeit
// verglichen.
func (h *Hub) checkAccount(ctx context.Context, a contract.AccountAuth) (acc store.Account, ok bool, err error) {
	acc, err = h.st.Account(ctx, a.Account)
	known := err == nil
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return store.Account{}, false, err
	}
	want := dummyAccountHash
	if known {
		want = acc.TokenHash
	}
	match := subtle.ConstantTimeCompare([]byte(ident.HashToken(a.Token)), []byte(want)) == 1
	return acc, known && match && !acc.Locked, nil
}

// checkVersion prüft die Fassung des Nodes, vor allem anderen.
func checkVersion(v int) error {
	if v != contract.Version {
		return &contract.Error{Code: contract.CodeUnsupportedVersion,
			Message: fmt.Sprintf("Fassung %d nicht unterstützt, der Hub spricht Fassung %d", v, contract.Version)}
	}
	return nil
}

// Whoami bestätigt den Node. Mit Account-Teil prüft es den Account gegen
// accounts und nennt seinen User und, welche seiner Collections dieser Node
// abgleichen darf. Ein Account, der nicht gilt, ist kein Fehler, sondern
// Valid false — ohne User.
func (h *Hub) Whoami(ctx context.Context, req contract.WhoamiRequest) (contract.WhoamiResponse, error) {
	if err := checkVersion(req.Version); err != nil {
		return contract.WhoamiResponse{}, err
	}
	allowed, err := h.authenticate(ctx, req.Auth)
	if err != nil {
		return contract.WhoamiResponse{}, err
	}
	info, err := h.st.Info(ctx)
	if err != nil {
		return contract.WhoamiResponse{}, err
	}
	resp := contract.WhoamiResponse{HubID: info.HubID, Version: contract.Version, Node: req.Auth.Node,
		Allowed: append([]string{}, allowed...)}
	if req.Account != nil {
		acc, ok, err := h.checkAccount(ctx, *req.Account)
		if err != nil {
			return contract.WhoamiResponse{}, err
		}
		st := &contract.AccountStatus{Account: req.Account.Account, Valid: ok, Collections: []string{}}
		if ok {
			st.User = acc.User
			for _, r := range acc.Rights {
				if slices.Contains(allowed, r.Collection) {
					st.Collections = append(st.Collections, r.Collection)
				}
			}
		}
		resp.Account = st
	}
	return resp, nil
}

// Rotate ersetzt das Token eines Accounts. Reihenfolge: Fassung, Form des
// neuen Hashes, Anmeldung des Nodes, dann das alte Token gegen accounts
// (gesperrt gilt nicht). Der Store prüft in seiner Transaktion noch einmal
// und ersetzt den Hash in accounts und allen Zeilen unter einer Revision; hat
// der Account keine der Collections dieses Nodes, ändert er nichts.
// Fehlversuche stehen nicht in actions.
func (h *Hub) Rotate(ctx context.Context, req contract.RotateRequest) (contract.RotateResponse, error) {
	if err := checkVersion(req.Version); err != nil {
		return contract.RotateResponse{}, err
	}
	if !contract.IsTokenHash(req.NewHash) {
		return contract.RotateResponse{}, contract.Invalid("new_hash ist kein sha256 in Hex (64 Zeichen 0-9a-f)")
	}
	allowed, err := h.authenticate(ctx, req.Auth)
	if err != nil {
		return contract.RotateResponse{}, err
	}
	_, ok, err := h.checkAccount(ctx, contract.AccountAuth{Account: req.Account, Token: req.Token})
	if err != nil {
		return contract.RotateResponse{}, err
	}
	if !ok {
		return contract.RotateResponse{}, contract.ErrAccountUnauthenticated
	}
	// Alles, was die Antwort braucht, steht vor dem Commit fest: Ein Fehler
	// danach hielte der Node sonst für ein Scheitern, obwohl der neue Hash
	// schon gilt.
	info, err := h.st.Info(ctx)
	if err != nil {
		return contract.RotateResponse{}, err
	}
	rows, err := h.st.RotateAccount(ctx, req.Account, ident.HashToken(req.Token), req.NewHash, req.Auth.Node, allowed)
	switch {
	case errors.Is(err, store.ErrAccountAuth):
		return contract.RotateResponse{}, contract.ErrAccountUnauthenticated
	case errors.Is(err, store.ErrNoSharedCollection):
		return contract.RotateResponse{}, contract.ErrNoSharedCollection
	case err != nil:
		return contract.RotateResponse{}, err
	}
	return contract.RotateResponse{HubID: info.HubID, Version: contract.Version, Rows: append([]contract.Row{}, rows...)}, nil
}

// checkRequest prüft die Anfrage ohne Datenbank.
func checkRequest(req contract.SyncRequest) error {
	if err := checkVersion(req.Version); err != nil {
		return err
	}
	if req.PageSize <= 0 {
		return contract.Invalid(fmt.Sprintf("Seitengröße %d, erwartet > 0", req.PageSize))
	}
	seen := map[string]bool{}
	for _, c := range req.Collections {
		if seen[c.Collection] {
			return contract.Invalid(fmt.Sprintf("Collection %q steht zweimal in der Anfrage", c.Collection))
		}
		seen[c.Collection] = true
		if c.Since < 0 {
			return contract.Invalid(fmt.Sprintf("Collection %q: seit %d, erwartet ≥ 0", c.Collection, c.Since))
		}
	}
	return nil
}

// Sync liefert eine Seite des Abgleichs. Reihenfolge: Anfrage prüfen,
// anmelden, dann die Hub-Revision H lesen und erst danach die Zeilen, nur
// solche mit revision ≤ H — so passen Seite und H zusammen, ohne
// Lese-Transaktion.
func (h *Hub) Sync(ctx context.Context, req contract.SyncRequest) (contract.SyncResponse, error) {
	if err := checkRequest(req); err != nil {
		return contract.SyncResponse{}, err
	}
	allowed, err := h.authenticate(ctx, req.Auth)
	if err != nil {
		return contract.SyncResponse{}, err
	}
	info, err := h.st.Info(ctx)
	if err != nil {
		return contract.SyncResponse{}, err
	}
	resp := contract.SyncResponse{
		HubID:       info.HubID,
		Version:     contract.Version,
		Collections: make([]contract.CollectionStatus, 0, len(req.Collections)),
		Allowed:     append([]string{}, allowed...),
		Rows:        []contract.Row{},
		HubRevision: info.Revision,
	}
	isAllowed := map[string]bool{}
	for _, c := range allowed {
		isAllowed[c] = true
	}
	var since []contract.Since
	for _, c := range req.Collections {
		ok := isAllowed[c.Collection]
		resp.Collections = append(resp.Collections, contract.CollectionStatus{Collection: c.Collection, Allowed: ok})
		if ok {
			since = append(since, c)
		}
	}
	pageSize := min(req.PageSize, h.maxPageSize)
	resp.Rows, resp.Until, resp.More, err = h.page(ctx, since, info.Revision, pageSize)
	if err != nil {
		return contract.SyncResponse{}, err
	}
	return resp, nil
}

// page schneidet eine Seite an einer Revisionsgrenze: ganze Revisionen, bis
// pageSize erreicht ist; eine einzelne größere Revision kommt ganz. until
// ist die Revision der letzten Zeile, wenn more gilt, sonst hubRev.
func (h *Hub) page(ctx context.Context, since []contract.Since, hubRev int64, pageSize int) (rows []contract.Row, until int64, more bool, err error) {
	// Eine Zeile mehr zeigt, ob die letzte Revision über die Seite reicht.
	rows, err = h.st.SyncRows(ctx, since, hubRev, pageSize+1)
	if err != nil {
		return nil, 0, false, err
	}
	if len(rows) <= pageSize {
		return rows, hubRev, false, nil
	}
	last := rows[pageSize-1].Revision
	if rows[pageSize].Revision != last {
		// Die Seite endet genau an einer Revisionsgrenze.
		return rows[:pageSize], last, true, nil
	}
	// Die letzte Revision reicht über die Seite: weglassen, was von ihr
	// darauf steht.
	cut := pageSize
	for cut > 0 && rows[cut-1].Revision == last {
		cut--
	}
	if cut > 0 {
		return rows[:cut], rows[cut-1].Revision, true, nil
	}
	// Die ganze Seite ist eine Revision, und sie ist größer als die Seite:
	// sie kommt ganz. Danach eine Zeile weiter schauen, ob mehr folgt.
	rows, err = h.st.SyncRows(ctx, raise(since, last-1), last, 0)
	if err != nil {
		return nil, 0, false, err
	}
	next, err := h.st.SyncRows(ctx, raise(since, last), hubRev, 1)
	if err != nil {
		return nil, 0, false, err
	}
	if len(next) == 0 {
		return rows, hubRev, false, nil
	}
	return rows, last, true, nil
}

// raise hebt jedes seit auf mindestens floor.
func raise(since []contract.Since, floor int64) []contract.Since {
	out := make([]contract.Since, len(since))
	for i, c := range since {
		out[i] = contract.Since{Collection: c.Collection, Since: max(c.Since, floor)}
	}
	return out
}
