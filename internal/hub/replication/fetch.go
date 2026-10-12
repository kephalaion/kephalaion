package replication

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/kephalaion/kephalaion/internal/contract"
	"github.com/kephalaion/kephalaion/internal/hub/source"
	"github.com/kephalaion/kephalaion/internal/hub/store"
)

// Fetch lässt den Hub das neueste Release der Quelle einer Collection nach
// defaults/ holen (docs/vertrag.md, „fetch“). Reihenfolge: Fassung,
// Anmeldung des Nodes, Account, Lesbarkeit (not_readable), Quelle
// (no_source), dann source.Fetch — Tag, Laden, Schreiben in einer
// Transaktion des Stores, die die Zeile der Collection zuerst sperrt. Die
// hub_id der Antwort liest der Hub vorher.
func (h *Hub) Fetch(ctx context.Context, req contract.FetchRequest) (contract.FetchResponse, error) {
	if err := checkVersion(req.Version); err != nil {
		return contract.FetchResponse{}, err
	}
	acc, err := h.nodeAccess(ctx, req.Auth)
	if err != nil {
		return contract.FetchResponse{}, err
	}
	a, ok, err := h.checkAccount(ctx, req.Account)
	if err != nil {
		return contract.FetchResponse{}, err
	}
	if !ok {
		return contract.FetchResponse{}, contract.ErrAccountUnauthenticated
	}
	notReadable := &contract.Error{Code: contract.CodeNotReadable, Message: fmt.Sprintf(
		"Collection %s ist nicht lesbar: unbekannt, für diesen Node nicht erlaubt oder für Account %s nicht eingetragen",
		req.Collection, req.Account.Account)}
	c, err := h.st.Collection(ctx, req.Collection)
	if errors.Is(err, store.ErrNotFound) {
		return contract.FetchResponse{}, notReadable
	}
	if err != nil {
		return contract.FetchResponse{}, err
	}
	hasRow := slices.ContainsFunc(a.Rights, func(r store.AccountRight) bool { return r.Collection == c.Name })
	if !slices.Contains(acc.allowed, c.Name) || (!hasRow && !c.IsPublic()) {
		return contract.FetchResponse{}, notReadable
	}
	if _, ok := source.Lookup(c.Name); !ok {
		return contract.FetchResponse{}, &contract.Error{Code: contract.CodeNoSource,
			Message: fmt.Sprintf("Collection %s hat keine Quelle", c.Name)}
	}
	if h.src == nil {
		return contract.FetchResponse{}, &contract.Error{Code: contract.CodeSourceUnreachable,
			Message: "Holen ist an diesem Hub nicht eingerichtet"}
	}
	info, err := h.st.Info(ctx)
	if err != nil {
		return contract.FetchResponse{}, err
	}
	res, err := source.Fetch(ctx, h.src, h.st, c.Name, source.Actor{Account: req.Account.Account, Carrier: req.Auth.Node})
	if err != nil {
		return contract.FetchResponse{}, fetchError(err, notReadable)
	}
	out := contract.FetchResponse{HubID: info.HubID, Version: contract.Version, Collection: c.Name,
		Status: contract.FetchFetched, Tag: res.Tag, Revision: res.Revision,
		Created: res.Created, Updated: res.Updated, Deleted: res.Deleted}
	if res.Current {
		out.Status = contract.FetchCurrent
	}
	return out, nil
}

// fetchKinds ordnet den Fehlerarten des Holens die Codes des Vertrags zu.
var fetchKinds = []struct {
	kind error
	code contract.Code
}{
	{source.ErrNoSource, contract.CodeNoSource},
	{source.ErrMissing, contract.CodeSourceMissing},
	{source.ErrInvalid, contract.CodeSourceInvalid},
	{source.ErrUnreachable, contract.CodeSourceUnreachable},
}

// fetchError übersetzt einen Fehler des Holens in einen Fehler des
// Vertrags, mit der Meldung des Holens. Ist die Collection inzwischen
// entfernt, ist sie nicht lesbar. Alles andere (Datenbank, Abbruch) bleibt —
// kein Fehler des Vertrags, der Ausgang ist unklar.
func fetchError(err error, notReadable *contract.Error) error {
	for _, k := range fetchKinds {
		if errors.Is(err, k.kind) {
			return &contract.Error{Code: k.code, Message: err.Error()}
		}
	}
	if errors.Is(err, store.ErrNotFound) {
		return notReadable
	}
	return err
}
