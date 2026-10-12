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

// Das Werkzeug fetch_release (Task 031): Der Node lässt den Hub das neueste
// Release der Quelle einer Collection nach defaults/ holen — Vorgang fetch
// des Vertrags — und stößt danach den Abgleich an. Der Name ist bewusst nicht
// fetch: Bei Assistenten gehört er üblich zum Lesen von URLs. Vorerst darf
// es jeder Account, der am Hub gültig angemeldet ist (Todo #19); ob die
// Collection lesbar ist und eine Quelle hat, entscheidet der Hub — so geht es
// auch, bevor der Node die Collection zum ersten Mal abgeglichen hat. Nie
// wiederholt; bei unklarem Ausgang darf der Aufrufer erneut anstoßen, dann
// antwortet der Hub current.

// FetchReleaseInput sind die Argumente von fetch_release.
type FetchReleaseInput struct {
	Collection string `json:"collection" jsonschema:"<hub>:<collection> einer Collection mit Quelle, etwa keph:k-playbook; Hub-Teil entbehrlich bei nur einem Hub"`
}

// FetchReleaseOutput ist die Antwort von fetch_release. Bei einem Fehler
// (isError) tragen nur Address und Error etwas.
type FetchReleaseOutput struct {
	// Address ist die Collection als <hub>:<collection>; bei einem Fehler vor
	// dem Auflösen die Angabe des Aufrufs.
	Address string `json:"address"`
	// Status ist fetched (geholt) oder current (schon aktuell).
	Status string `json:"status,omitempty"`
	// Tag ist der Tag des Releases, das jetzt in defaults/ steht.
	Tag string `json:"tag,omitempty"`
	// Revision ist die Revision des Vorgangs; fehlt, wenn sich kein
	// Dokument geändert hat.
	Revision int64 `json:"revision,omitempty"`
	// Created, Updated und Deleted zählen die Dokumente unter defaults/.
	Created int `json:"created"`
	Updated int `json:"updated"`
	Deleted int `json:"deleted"`
	// Error ist der Fehler, nur mit isError.
	Error *WriteError `json:"error,omitempty"`
}

const fetchReleaseDescription = "Lässt den Hub das neueste Release der Quelle einer Collection nach defaults/ " +
	"holen (nur Collections mit Quelle, etwa k-playbook) und stößt danach den Abgleich an. Liest keine URLs und " +
	"keine Webseiten. Ist das neueste Release schon geholt: status current. Antwort: Adresse, status, tag, " +
	"revision und die Zahl der angelegten, ersetzten und gelöschten Dokumente; ein Fehler trägt error.code " +
	"(no_source, source_missing, source_invalid, source_unreachable, not_readable, unreachable, outcome_unknown, " +
	"unsupported). Nicht wiederholen; bei outcome_unknown darf es erneut angestoßen werden."

func (n *Node) fetchRelease(ctx context.Context, req *mcp.CallToolRequest, in FetchReleaseInput) (*mcp.CallToolResult, FetchReleaseOutput, error) {
	reqlog.Note(ctx, "op", "fetch_release")
	out := FetchReleaseOutput{Address: in.Collection}
	err := n.doFetch(ctx, req, in, &out)
	if err == nil {
		return nil, out, nil
	}
	var te *toolError
	if !errors.As(toolFailure(ctx, err), &te) {
		return nil, FetchReleaseOutput{}, err
	}
	code := te.code
	if code == "" {
		code = string(contract.CodeInvalid)
	}
	reqlog.Note(ctx, "code", code)
	fail := FetchReleaseOutput{Address: out.Address, Error: &WriteError{Code: code, Message: te.msg}}
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: te.msg}}}, fail, nil
}

func (n *Node) doFetch(ctx context.Context, req *mcp.CallToolRequest, in FetchReleaseInput, out *FetchReleaseOutput) error {
	if n.link.Connect == nil {
		return &toolError{msg: "Holen ist an diesem Eingang nicht eingerichtet", code: CodeUnsupported}
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
			return &toolError{msg: fmt.Sprintf("Hub %s: Holen über den Transport dieses Hubs wird noch nicht "+
				"unterstützt; nichts geholt", t.Hub), code: CodeUnsupported}
		}
		return &toolError{msg: fmt.Sprintf("Hub %s nicht erreichbar, nichts geholt", t.Hub), code: CodeUnreachable}
	}
	defer done()
	resp, err := hub.Fetch(ctx, contract.FetchRequest{Version: contract.Version,
		Auth:    contract.NodeAuth{Node: h.NodeName, Token: h.Token},
		Account: contract.AccountAuth{Account: l.Account, Token: headerToken(req, t.Hub)}, Collection: t.Collection})
	if err != nil {
		return n.fetchFailure(ctx, h, t, err)
	}
	*out = FetchReleaseOutput{Address: t.Address(), Status: resp.Status, Tag: resp.Tag, Revision: resp.Revision,
		Created: resp.Created, Updated: resp.Updated, Deleted: resp.Deleted}
	n.kick(h)
	return nil
}

// fetchFailure ordnet einen Fehler des Hubs bei fetch ein — wie bei den
// Werkzeugen, die schreiben (hubFailure), mit eigenen Meldungen für den
// unklaren Ausgang und einen Hub, der fetch nicht kennt.
func (n *Node) fetchFailure(ctx context.Context, h store.Hub, t target, err error) error {
	switch {
	case errors.Is(err, contract.ErrOutcomeUnknown):
		reqlog.NoteError(ctx, err)
		n.kick(h)
		return &toolError{msg: fmt.Sprintf("Hub %s: Ausgang unklar — abgeschickt, aber keine brauchbare Antwort; "+
			"geholt sein kann es. Der Abgleich ist angestoßen; erneut anstoßen ist erlaubt, dann antwortet der Hub "+
			"current, wenn er schon geholt hat.", t.Hub), code: CodeOutcomeUnknown}
	case errors.Is(err, contract.ErrUnknownOperation):
		return &toolError{msg: fmt.Sprintf("Hub %s kennt fetch noch nicht (älter als dieser Node); nichts geholt",
			t.Hub), code: CodeUnsupported}
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
	return &toolError{msg: fmt.Sprintf("Hub %s nicht erreichbar, nichts geholt", t.Hub), code: CodeUnreachable}
}
