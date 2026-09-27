package replication

import (
	"context"
	"errors"
	"fmt"

	"github.com/kephalaion/kephalaion/internal/contract"
	"github.com/kephalaion/kephalaion/internal/hub/store"
	"github.com/kephalaion/kephalaion/internal/ident"
)

// Die Schreibvorgänge create, write, delete und rename (docs/vertrag.md,
// „Schreibvorgänge“). Reihenfolge wie
// bei rotate: Fassung, Form der Anfrage (invalid), Anmeldung des Nodes; den
// Rest — Account, Lesbarkeit, Recht, Name und Vorbedingung — prüft der Store
// in der Transaktion des Vorgangs, deren erste Anweisung die Zeile des
// Accounts sperrt. Die hub_id der Antwort liest der Hub vorher; Revision und
// Zeilen liest der Store vor dem Commit. Nach dem Commit liest nichts mehr.

// checkWriteForm prüft Fassung und Form einer Schreibanfrage, ohne
// Datenbank: den Namen, den Inhalt (nil bei delete) und die Revision, auf der
// der Vorgang beruht. Die Collection prüft sie nicht — eine ungültige gibt
// es nicht, das ist not_readable.
func checkWriteForm(version int, name string, content *string, base *int64) error {
	if err := checkVersion(version); err != nil {
		return err
	}
	if err := ident.CheckDocName(name); err != nil {
		return contract.Invalid(err.Error())
	}
	if content != nil {
		if err := store.CheckContent(*content); err != nil {
			return contract.Invalid(fmt.Sprintf("Dokument %s: %v", name, err))
		}
	}
	if base != nil && *base < 1 {
		return contract.Invalid(fmt.Sprintf("base_revision %d, erwartet ≥ 1", *base))
	}
	return nil
}

// write meldet den Node an, liest die hub_id und führt fn als den
// Schreibvorgang des Accounts aus, mit dem Node als Träger.
func (h *Hub) write(ctx context.Context, node contract.NodeAuth, account contract.AccountAuth,
	fn func(auth store.WriteAuth) (store.WriteResult, error)) (contract.WriteResponse, error) {
	if _, err := h.authenticate(ctx, node); err != nil {
		return contract.WriteResponse{}, err
	}
	info, err := h.st.Info(ctx)
	if err != nil {
		return contract.WriteResponse{}, err
	}
	res, err := fn(store.WriteAuth{Account: account.Account, TokenHash: ident.HashToken(account.Token), Carrier: node.Node})
	if err != nil {
		return contract.WriteResponse{}, writeError(err)
	}
	return contract.WriteResponse{HubID: info.HubID, Version: contract.Version, Revision: res.Revision,
		Rows: append([]contract.Row{}, res.Rows...)}, nil
}

// writeKinds ordnet den Fehlerarten des Stores die Codes des Vertrags zu.
var writeKinds = []struct {
	kind error
	code contract.Code
}{
	{store.ErrNotReadable, contract.CodeNotReadable},
	{store.ErrForbidden, contract.CodeForbidden},
	{store.ErrNameTaken, contract.CodeNameTaken},
	{store.ErrPathConflict, contract.CodePathConflict},
	{store.ErrStaleRevision, contract.CodeStaleRevision},
	{store.ErrNotFound, contract.CodeNotFound},
}

// writeError übersetzt einen Fehler des Stores in einen Fehler des
// Vertrags, mit der Meldung des Stores. Unbekannt, falsches Token und
// gesperrt sind dieselbe Antwort. Alles andere (Datenbank, Abbruch) bleibt,
// wie es ist — kein Fehler des Vertrags, der Ausgang ist unklar.
func writeError(err error) error {
	if errors.Is(err, store.ErrAccountAuth) {
		return contract.ErrAccountUnauthenticated
	}
	for _, k := range writeKinds {
		if errors.Is(err, k.kind) {
			return &contract.Error{Code: k.code, Message: err.Error()}
		}
	}
	if errors.Is(err, store.ErrInvalid) {
		return contract.Invalid(err.Error())
	}
	return err
}

// Create legt ein Dokument an: write in der Collection, kein lebendes
// Dokument mit dem Namen (sonst name_taken).
func (h *Hub) Create(ctx context.Context, req contract.CreateRequest) (contract.WriteResponse, error) {
	if err := checkWriteForm(req.Version, req.Name, &req.Content, nil); err != nil {
		return contract.WriteResponse{}, err
	}
	return h.write(ctx, req.Auth, req.Account, func(auth store.WriteAuth) (store.WriteResult, error) {
		return h.st.CreateDocumentAs(ctx, auth, req.Collection, req.Name, req.Content)
	})
}

// Write ersetzt den Inhalt eines lebenden Dokuments: write für Eigenes,
// supersede für Fremdes; base_revision geprüft vor dem Vergleich des
// Inhalts.
func (h *Hub) Write(ctx context.Context, req contract.WriteRequest) (contract.WriteResponse, error) {
	if err := checkWriteForm(req.Version, req.Name, &req.Content, req.BaseRevision); err != nil {
		return contract.WriteResponse{}, err
	}
	return h.write(ctx, req.Auth, req.Account, func(auth store.WriteAuth) (store.WriteResult, error) {
		return h.st.WriteDocumentAs(ctx, auth, req.Collection, req.Name, req.Content, req.BaseRevision)
	})
}

// Delete setzt eine Löschmarke auf ein lebendes Dokument, mit recursive auf
// alle Dokumente unter einem Verzeichnis; Rechte wie bei Write, je Dokument,
// base_revision nur für ein Dokument.
func (h *Hub) Delete(ctx context.Context, req contract.DeleteRequest) (contract.WriteResponse, error) {
	if err := checkWriteForm(req.Version, req.Name, nil, req.BaseRevision); err != nil {
		return contract.WriteResponse{}, err
	}
	return h.write(ctx, req.Auth, req.Account, func(auth store.WriteAuth) (store.WriteResult, error) {
		return h.st.DeleteDocumentAs(ctx, auth, req.Collection, req.Name, req.BaseRevision, req.Recursive)
	})
}

// Rename gibt einem Dokument oder allen Dokumenten unter einem Verzeichnis
// einen neuen Namen; Rechte und base_revision wie bei Delete. Der neue Name
// gehört zur Form: gültig, weder gleich dem alten noch darunter.
func (h *Hub) Rename(ctx context.Context, req contract.RenameRequest) (contract.WriteResponse, error) {
	if err := checkWriteForm(req.Version, req.Name, nil, req.BaseRevision); err != nil {
		return contract.WriteResponse{}, err
	}
	if err := ident.CheckRename(req.Name, req.NewName); err != nil {
		return contract.WriteResponse{}, contract.Invalid(err.Error())
	}
	return h.write(ctx, req.Auth, req.Account, func(auth store.WriteAuth) (store.WriteResult, error) {
		return h.st.RenameDocumentAs(ctx, auth, req.Collection, req.Name, req.NewName, req.BaseRevision)
	})
}
