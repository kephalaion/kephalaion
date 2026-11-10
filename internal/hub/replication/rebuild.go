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

// RebuildDocs lässt den Hub docs/ einer Collection neu bauen
// (docs/vertrag.md, „rebuild_docs“; Task 033). Reihenfolge: Fassung,
// Anmeldung des Nodes, Account, Lesbarkeit (not_readable), Quelle
// (forbidden), dann der Neubau in einer Transaktion des Stores, die die
// Zeile der Collection zuerst sperrt. Ein Recht über read hinaus braucht der
// Account nicht. Die hub_id der Antwort liest der Hub vorher.
func (h *Hub) RebuildDocs(ctx context.Context, req contract.RebuildDocsRequest) (contract.RebuildDocsResponse, error) {
	if err := checkVersion(req.Version); err != nil {
		return contract.RebuildDocsResponse{}, err
	}
	acc, err := h.nodeAccess(ctx, req.Auth)
	if err != nil {
		return contract.RebuildDocsResponse{}, err
	}
	a, ok, err := h.checkAccount(ctx, req.Account)
	if err != nil {
		return contract.RebuildDocsResponse{}, err
	}
	if !ok {
		return contract.RebuildDocsResponse{}, contract.ErrAccountUnauthenticated
	}
	notReadable := &contract.Error{Code: contract.CodeNotReadable, Message: fmt.Sprintf(
		"Collection %s ist nicht lesbar: unbekannt, für diesen Node nicht erlaubt oder für Account %s nicht eingetragen",
		req.Collection, req.Account.Account)}
	c, err := h.st.Collection(ctx, req.Collection)
	if errors.Is(err, store.ErrNotFound) {
		return contract.RebuildDocsResponse{}, notReadable
	}
	if err != nil {
		return contract.RebuildDocsResponse{}, err
	}
	hasRow := slices.ContainsFunc(a.Rights, func(r store.AccountRight) bool { return r.Collection == c.Name })
	if !slices.Contains(acc.allowed, c.Name) || (!hasRow && !c.IsPublic()) {
		return contract.RebuildDocsResponse{}, notReadable
	}
	info, err := h.st.Info(ctx)
	if err != nil {
		return contract.RebuildDocsResponse{}, err
	}
	res, err := h.st.RebuildDocs(ctx, c.Name, source.Actor{Account: req.Account.Account, Carrier: req.Auth.Node})
	switch {
	case errors.Is(err, store.ErrNotFound):
		// Inzwischen entfernt.
		return contract.RebuildDocsResponse{}, notReadable
	case errors.Is(err, store.ErrForbidden):
		return contract.RebuildDocsResponse{}, &contract.Error{Code: contract.CodeForbidden, Message: err.Error()}
	case err != nil:
		return contract.RebuildDocsResponse{}, err
	}
	out := contract.RebuildDocsResponse{HubID: info.HubID, Version: contract.Version, Collection: c.Name,
		Status: contract.RebuildRebuilt, Revision: res.Revision,
		Created: res.Created, Updated: res.Updated, Deleted: res.Deleted}
	switch {
	case res.NoDocs:
		out.Status = contract.RebuildNoDocs
	case res.Revision == 0:
		out.Status = contract.RebuildCurrent
	}
	return out, nil
}
