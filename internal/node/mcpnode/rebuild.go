package mcpnode

import (
	"context"
	"errors"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kephalaion/kephalaion/internal/contract"
	"github.com/kephalaion/kephalaion/internal/node/store"
	"github.com/kephalaion/kephalaion/internal/reqlog"
)

// Das Werkzeug rebuild_docs (Task 033): Der Node lässt den Hub docs/ einer
// Collection neu bauen — Vorgang rebuild_docs des Vertrags — und stößt
// danach den Abgleich an. Wie fetch_release: jeder Account, der am Hub
// gültig angemeldet ist; ob die Collection lesbar ist und keine Quelle hat,
// entscheidet der Hub. Nie wiederholt; bei unklarem Ausgang darf der
// Aufrufer erneut anstoßen, dann antwortet der Hub current.

// RebuildDocsInput sind die Argumente von rebuild_docs.
type RebuildDocsInput struct {
	Collection string `json:"collection" jsonschema:"<hub>:<collection>, Hub-Teil entbehrlich bei nur einem Hub"`
}

// RebuildDocsOutput ist die Antwort von rebuild_docs. Bei einem Fehler
// (isError) tragen nur Address und Error etwas.
type RebuildDocsOutput struct {
	// Address ist die Collection als <hub>:<collection>; bei einem Fehler vor
	// dem Auflösen die Angabe des Aufrufs.
	Address string `json:"address"`
	// Status ist rebuilt (neu gebaut), current (schon aktuell) oder no_docs
	// (kein docs/ auf oberster Ebene, nichts zu tun).
	Status string `json:"status,omitempty"`
	// Revision ist die Revision des Vorgangs; fehlt, wenn sich nichts
	// geändert hat.
	Revision int64 `json:"revision,omitempty"`
	// Created, Updated und Deleted zählen die Dokumente unter docs/.
	Created int `json:"created"`
	Updated int `json:"updated"`
	Deleted int `json:"deleted"`
	// Error ist der Fehler, nur mit isError.
	Error *WriteError `json:"error,omitempty"`
}

const rebuildDocsDescription = "Lässt den Hub docs/ einer Collection neu bauen und stößt danach den Abgleich an: " +
	"docs/README.md, die festen Verzeichnisse manual/ und generated/, eine Mahnung (README.md) in jedem " +
	"Verzeichnis ohne README.md und je Verzeichnis eine index.md. Aufrufen, nachdem unter docs/ geschrieben, " +
	"umbenannt oder gelöscht wurde; es gibt keinen automatischen Neubau. Nur wo docs/ schon existiert, sonst " +
	"status no_docs; schon aktuell: status current. Antwort: Adresse, status, revision und die Zahl der " +
	"angelegten, ersetzten und gelöschten Dokumente; ein Fehler trägt error.code (not_readable, forbidden für " +
	"eine Collection mit Quelle, unreachable, outcome_unknown, unsupported). Nicht wiederholen; bei " +
	"outcome_unknown darf es erneut angestoßen werden."

func (n *Node) rebuildDocs(ctx context.Context, req *mcp.CallToolRequest, in RebuildDocsInput) (*mcp.CallToolResult, RebuildDocsOutput, error) {
	reqlog.Note(ctx, "op", "rebuild_docs")
	out := RebuildDocsOutput{Address: in.Collection}
	err := n.doRebuildDocs(ctx, req, in, &out)
	if err == nil {
		return nil, out, nil
	}
	var te *toolError
	if !errors.As(toolFailure(ctx, err), &te) {
		return nil, RebuildDocsOutput{}, err
	}
	code := te.code
	if code == "" {
		code = string(contract.CodeInvalid)
	}
	reqlog.Note(ctx, "code", code)
	fail := RebuildDocsOutput{Address: out.Address, Error: &WriteError{Code: code, Message: te.msg}}
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: te.msg}}}, fail, nil
}

func (n *Node) doRebuildDocs(ctx context.Context, req *mcp.CallToolRequest, in RebuildDocsInput, out *RebuildDocsOutput) error {
	if n.link.Connect == nil {
		return &toolError{msg: "Neubau ist an diesem Eingang nicht eingerichtet", code: CodeUnsupported}
	}
	r, err := n.begin(ctx, req)
	if err != nil {
		return err
	}
	t, err := r.resolve(in.Collection)
	if err != nil {
		return err
	}
	if t.Collection == "" {
		return &toolError{msg: fmt.Sprintf("Adresse %q ohne Collection; erwartet <hub>:<collection>", in.Collection)}
	}
	out.Address = t.Address()
	h, ok := r.entries[t.Hub]
	l, _ := r.login(t.Hub)
	switch {
	case !ok:
		return notReadable(t.Address())
	case l.Unreadable != nil:
		return l.Unreadable
	case l.State != LoginOK:
		return notReadable(t.Address())
	}
	reqlog.Note(ctx, "hub", t.Hub)
	reqlog.Note(ctx, "node", h.NodeName)
	hub, done, err := n.link.Connect(ctx, h)
	if err != nil {
		reqlog.NoteError(ctx, err)
		if errors.Is(err, ErrUnsupported) {
			return &toolError{msg: fmt.Sprintf("Hub %s: Neubau über den Transport dieses Hubs wird noch nicht "+
				"unterstützt; nichts gebaut", t.Hub), code: CodeUnsupported}
		}
		return &toolError{msg: fmt.Sprintf("Hub %s nicht erreichbar, nichts gebaut", t.Hub), code: CodeUnreachable}
	}
	defer done()
	resp, err := hub.RebuildDocs(ctx, contract.RebuildDocsRequest{Version: contract.Version,
		Auth:    contract.NodeAuth{Node: h.NodeName, Token: h.Token},
		Account: contract.AccountAuth{Account: l.Account, Token: headerToken(req, t.Hub)}, Collection: t.Collection})
	if err != nil {
		return n.rebuildFailure(ctx, h, t, err)
	}
	*out = RebuildDocsOutput{Address: t.Address(), Status: resp.Status, Revision: resp.Revision,
		Created: resp.Created, Updated: resp.Updated, Deleted: resp.Deleted}
	n.kick(h)
	return nil
}

// rebuildFailure ordnet einen Fehler des Hubs bei rebuild_docs ein, wie
// fetchFailure.
func (n *Node) rebuildFailure(ctx context.Context, h store.Hub, t target, err error) error {
	switch {
	case errors.Is(err, contract.ErrOutcomeUnknown):
		reqlog.NoteError(ctx, err)
		n.kick(h)
		return &toolError{msg: fmt.Sprintf("Hub %s: Ausgang unklar — abgeschickt, aber keine brauchbare Antwort; "+
			"gebaut sein kann es. Der Abgleich ist angestoßen; erneut anstoßen ist erlaubt, dann antwortet der Hub "+
			"current, wenn er schon gebaut hat.", t.Hub), code: CodeOutcomeUnknown}
	case errors.Is(err, contract.ErrUnknownOperation):
		return &toolError{msg: fmt.Sprintf("Hub %s kennt rebuild_docs noch nicht (älter als dieser Node); nichts "+
			"gebaut", t.Hub), code: CodeUnsupported}
	}
	var ce *contract.Error
	if errors.As(err, &ce) {
		switch ce.Code {
		case contract.CodeAccountUnauthenticated, contract.CodeNotReadable:
			return notReadable(t.Address())
		}
		return &toolError{msg: fmt.Sprintf("Hub %s: %s", t.Hub, ce.Message), code: string(ce.Code)}
	}
	reqlog.NoteError(ctx, err)
	return &toolError{msg: fmt.Sprintf("Hub %s nicht erreichbar, nichts gebaut", t.Hub), code: CodeUnreachable}
}
