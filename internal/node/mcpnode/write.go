package mcpnode

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kephalaion/kephalaion/internal/contract"
	"github.com/kephalaion/kephalaion/internal/ident"
	"github.com/kephalaion/kephalaion/internal/node/replica"
	"github.com/kephalaion/kephalaion/internal/node/store"
	"github.com/kephalaion/kephalaion/internal/reqlog"
)

// Die Werkzeuge, die schreiben (create, write, delete, rename): Der Node
// prüft Anmeldung und Lesbarkeit wie beim Lesen gegen die Replica (access.go)
// und reicht Account und Token dann an den Hub; ob geschrieben werden darf,
// entscheidet allein der Hub. Nach Erfolg schreibt der Node die Zeilen der
// Antwort in die Replica, bevor er antwortet, und stößt den Abgleich des Hubs
// an, ohne zu warten — ebenso nach unklarem Ausgang. Nie wiederholt.
//
// Jeder Fehler trägt neben der Meldung einen Code: das Ergebnis hat isError,
// der Text ist die Meldung, und die Struktur trägt error.code und
// error.message (WriteOutput). Nur ein abgebrochener Aufruf und Argumente,
// die schon das SDK gegen das Schema ablehnt, kommen ohne Code.

// HubLink ist, was die Werkzeuge, die schreiben, von cmd/kephalaion bekommen:
// den Weg zum Hub und den Anstoß des Abgleichs. Welche Umsetzung des Vertrags
// ein Eintrag bekommt, entscheidet cmd/kephalaion; internal/node kennt nur
// contract.Hub.
type HubLink struct {
	// Connect liefert die Umsetzung des Vertrags für einen Hub-Eintrag und
	// eine Funktion, die sie wegräumt. Ein Fehler heißt: nichts abgeschickt —
	// mit ErrUnsupported eingehüllt „noch nicht unterstützt“, sonst „Hub nicht
	// erreichbar“. Ohne Connect melden die Werkzeuge unsupported.
	Connect func(ctx context.Context, h store.Hub) (contract.Hub, func(), error)
	// Sync stößt den Abgleich eines Hub-Eintrags an und wartet nicht darauf;
	// ohne Sync geschieht nichts.
	Sync func(h store.Hub)
}

// ErrUnsupported meldet in HubLink.Connect einen Transport, den der Node noch
// nicht kann (https, ssh).
var ErrUnsupported = errors.New("noch nicht unterstützt")

// Die Codes der Werkzeuge, die schreiben, über die Codes des Vertrags hinaus
// (docs/begriffe.md, „write error codes“). Eine Ablehnung des Hubs trägt den
// Code des Vertrags — account_unauthenticated und not_readable als
// not_readable, dieselbe Meldung wie die Prüfung am Node.
const (
	// CodeUnreachable: Der Hub ist nicht erreicht worden; nichts gespeichert.
	CodeUnreachable = "unreachable"
	// CodeOutcomeUnknown: abgeschickt, aber keine brauchbare Antwort — der
	// Hub kann gespeichert haben. Nicht wiederholen, nachsehen.
	CodeOutcomeUnknown = "outcome_unknown"
	// CodeUnsupported: Der Hub kennt den Vorgang nicht, oder der Node kann
	// den Transport des Hub-Eintrags noch nicht; nichts gespeichert.
	CodeUnsupported = "unsupported"
	// CodeInternal: ein Fehler des Nodes selbst (node.db, Replica nicht
	// lesbar); nichts abgeschickt.
	CodeInternal = "internal"
)

// MaxRequestBytes begrenzt den Body einer Anfrage an /mcp: 7 MiB — dieselbe
// Rechnung wie httpapi.MaxWriteBodyBytes. Jedes Dokument, das der Hub annimmt
// (contract.MaxDocumentBytes), muss hindurchpassen, auch wenn JSON jedes Byte
// als \u00XX schreibt (6 Byte je Byte); dazu 1 MiB für alles andere. Das
// go-sdk nähme sonst nur 4 MiB.
const MaxRequestBytes = 6*contract.MaxDocumentBytes + 1<<20

// CreateInput sind die Argumente von create.
type CreateInput struct {
	Collection string `json:"collection" jsonschema:"<hub>:<collection>, Hub-Teil entbehrlich bei nur einem Hub"`
	Name       string `json:"name" jsonschema:"Name des Dokuments in der Collection"`
	Content    string `json:"content" jsonschema:"der ganze Inhalt als Text, auch leer"`
}

// WriteInput sind die Argumente von write.
type WriteInput struct {
	Collection   string `json:"collection" jsonschema:"<hub>:<collection>, Hub-Teil entbehrlich bei nur einem Hub"`
	Name         string `json:"name" jsonschema:"Name des Dokuments in der Collection"`
	Content      string `json:"content" jsonschema:"der ganze neue Inhalt als Text, auch leer"`
	BaseRevision *int64 `json:"base_revision,omitempty" jsonschema:"wahlweise die Revision, auf der der Inhalt beruht (aus read); hat das Dokument eine andere: stale_revision"`
}

// DeleteInput sind die Argumente von delete.
type DeleteInput struct {
	Collection   string `json:"collection" jsonschema:"<hub>:<collection>, Hub-Teil entbehrlich bei nur einem Hub"`
	Name         string `json:"name" jsonschema:"Name des Dokuments oder Verzeichnisses in der Collection"`
	BaseRevision *int64 `json:"base_revision,omitempty" jsonschema:"wahlweise die Revision, auf der das Löschen beruht (aus read); hat das Dokument eine andere: stale_revision. Nur für Dokumente"`
	Recursive    bool   `json:"recursive,omitempty" jsonschema:"ein Verzeichnis mit allen Dokumenten darunter löschen; ohne es ist ein Verzeichnis invalid. Für ein Dokument ohne Belang"`
}

// RenameInput sind die Argumente von rename.
type RenameInput struct {
	Collection   string `json:"collection" jsonschema:"<hub>:<collection>, Hub-Teil entbehrlich bei nur einem Hub"`
	Name         string `json:"name" jsonschema:"Name des Dokuments oder Verzeichnisses in der Collection"`
	NewName      string `json:"new_name" jsonschema:"der neue Name in derselben Collection; nicht belegt, nicht unter dem alten"`
	BaseRevision *int64 `json:"base_revision,omitempty" jsonschema:"wahlweise die Revision, auf der das Umbenennen beruht (aus read); hat das Dokument eine andere: stale_revision. Nur für Dokumente"`
}

// WriteOutput ist die Antwort von create, write, delete und rename: das
// Dokument, wie der Hub es nach dem Vorgang meldet — bei delete die
// Löschmarke, bei rename unter dem neuen Namen. Nennt der Aufruf ein
// Verzeichnis (delete mit recursive, rename), ist Kind directory und die
// Antwort trägt Name, Revision, Count und Updated. Bei einem Fehler (isError)
// tragen nur Address, Name — der des Aufrufs, bei rename der alte — und
// Error etwas.
type WriteOutput struct {
	// Kind ist document oder directory; fehlt bei einem Fehler.
	Kind string `json:"kind,omitempty"`
	// Address ist die Collection als <hub>:<collection>; bei einem Fehler vor
	// dem Auflösen der Adresse die Angabe des Aufrufs.
	Address string `json:"address"`
	Name    string `json:"name"`
	ID      string `json:"id,omitempty"`
	// Revision ist die Revision des Vorgangs; bei write mit unverändertem
	// Inhalt die bestehende.
	Revision int64 `json:"revision,omitempty"`
	// Deleted: das Dokument ist gelöscht, bei einem Verzeichnis alle darunter
	// (delete).
	Deleted bool `json:"deleted,omitempty"`
	// Count ist bei einem Verzeichnis die Zahl der Dokumente, die der
	// Vorgang geschrieben hat.
	Count   int    `json:"count,omitempty"`
	Created *Stamp `json:"created,omitempty"`
	Updated *Stamp `json:"updated,omitempty"`
	// Size ist die Größe des Inhalts in Bytes; fehlt bei einer Löschmarke.
	Size *int64 `json:"size,omitempty"`
	// Note ist ein Hinweis zu einem Erfolg: Die Replica ließ sich nicht
	// schreiben; read zeigt den alten Stand, bis der Abgleich nachholt.
	Note string `json:"note,omitempty"`
	// Error ist der Fehler, nur mit isError.
	Error *WriteError `json:"error,omitempty"`
}

// WriteError ist der Fehler eines Werkzeugs, das schreibt: ein Code, den der
// Client auswertet, und eine Meldung für Menschen.
type WriteError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

const createDescription = "Legt ein Dokument an, über den Hub. Ein lebendes Dokument mit dem Namen: name_taken. " +
	"Antwort: Adresse, Name, id, Revision, angelegt, geändert, Größe; ein Fehler trägt error.code."

const writeDescription = "Ersetzt den Inhalt eines Dokuments, über den Hub. Mit base_revision nur, wenn das " +
	"Dokument noch diese Revision hat, sonst stale_revision. Antwort wie create."

const deleteDescription = "Löscht ein Dokument, über den Hub (Löschmarke). base_revision wie bei write, nur für " +
	"Dokumente. Ein Verzeichnis nur mit recursive: alle Dokumente darunter, alles oder nichts. Antwort wie create, " +
	"mit deleted; bei einem Verzeichnis kind directory und count."

const renameDescription = "Benennt ein Dokument oder ein Verzeichnis um, über den Hub, in derselben Collection; die " +
	"id bleibt, ein Verzeichnis alles oder nichts. Ist das Ziel belegt: name_taken, nichts wird überschrieben. " +
	"base_revision wie bei write, nur für Dokumente. Antwort wie create unter dem neuen Namen; bei einem " +
	"Verzeichnis kind directory und count."

// writeOp ist ein Aufruf eines Werkzeugs, das schreibt: Werkzeug, Adresse,
// Name und der Vorgang am Hub. newName ist bei rename der neue Name; um ihn
// geht dann die Antwort. dir sagt, dass der Vorgang auch ein Verzeichnis
// nehmen kann (delete, rename).
type writeOp struct {
	tool       string
	collection string
	name       string
	rename     bool
	newName    string
	dir        bool
	call       func(ctx context.Context, hub contract.Hub, node contract.NodeAuth, account contract.AccountAuth,
		collection string) (contract.WriteResponse, error)
}

// result ist der Name, um den es in der Antwort geht: bei rename der neue.
func (op writeOp) result() string {
	if op.rename {
		return op.newName
	}
	return op.name
}

func (n *Node) create(ctx context.Context, req *mcp.CallToolRequest, in CreateInput) (*mcp.CallToolResult, WriteOutput, error) {
	return n.write(ctx, req, writeOp{tool: "create", collection: in.Collection, name: in.Name,
		call: func(ctx context.Context, hub contract.Hub, node contract.NodeAuth, account contract.AccountAuth,
			coll string) (contract.WriteResponse, error) {
			return hub.Create(ctx, contract.CreateRequest{Version: contract.Version, Auth: node, Account: account,
				Collection: coll, Name: in.Name, Content: in.Content})
		}})
}

func (n *Node) replace(ctx context.Context, req *mcp.CallToolRequest, in WriteInput) (*mcp.CallToolResult, WriteOutput, error) {
	return n.write(ctx, req, writeOp{tool: "write", collection: in.Collection, name: in.Name,
		call: func(ctx context.Context, hub contract.Hub, node contract.NodeAuth, account contract.AccountAuth,
			coll string) (contract.WriteResponse, error) {
			return hub.Write(ctx, contract.WriteRequest{Version: contract.Version, Auth: node, Account: account,
				Collection: coll, Name: in.Name, Content: in.Content, BaseRevision: in.BaseRevision})
		}})
}

func (n *Node) remove(ctx context.Context, req *mcp.CallToolRequest, in DeleteInput) (*mcp.CallToolResult, WriteOutput, error) {
	return n.write(ctx, req, writeOp{tool: "delete", collection: in.Collection, name: in.Name, dir: true,
		call: func(ctx context.Context, hub contract.Hub, node contract.NodeAuth, account contract.AccountAuth,
			coll string) (contract.WriteResponse, error) {
			return hub.Delete(ctx, contract.DeleteRequest{Version: contract.Version, Auth: node, Account: account,
				Collection: coll, Name: in.Name, BaseRevision: in.BaseRevision, Recursive: in.Recursive})
		}})
}

func (n *Node) rename(ctx context.Context, req *mcp.CallToolRequest, in RenameInput) (*mcp.CallToolResult, WriteOutput, error) {
	return n.write(ctx, req, writeOp{tool: "rename", collection: in.Collection, name: in.Name, rename: true,
		newName: in.NewName, dir: true,
		call: func(ctx context.Context, hub contract.Hub, node contract.NodeAuth, account contract.AccountAuth,
			coll string) (contract.WriteResponse, error) {
			return hub.Rename(ctx, contract.RenameRequest{Version: contract.Version, Auth: node, Account: account,
				Collection: coll, Name: in.Name, NewName: in.NewName, BaseRevision: in.BaseRevision})
		}})
}

// write führt einen Aufruf aus und macht aus einem Fehler das Ergebnis mit
// Code. Das Log nennt Vorgang, Hub, Node und Code — nie Token oder Inhalt.
func (n *Node) write(ctx context.Context, req *mcp.CallToolRequest, op writeOp) (*mcp.CallToolResult, WriteOutput, error) {
	reqlog.Note(ctx, "op", op.tool)
	out := WriteOutput{Address: op.collection, Name: op.name}
	err := n.doWrite(ctx, req, op, &out)
	if err == nil {
		return nil, out, nil
	}
	var te *toolError
	if !errors.As(toolFailure(ctx, err), &te) {
		// Abgebrochen: ein Fehler der Anfrage, ohne Code.
		return nil, WriteOutput{}, err
	}
	code := te.code
	if code == "" {
		code = string(contract.CodeInvalid)
	}
	reqlog.Note(ctx, "code", code)
	fail := WriteOutput{Address: out.Address, Name: out.Name, Error: &WriteError{Code: code, Message: te.msg}}
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: te.msg}}}, fail, nil
}

// doWrite prüft wie beim Lesen, ruft den Hub und übernimmt die Antwort. out
// bekommt die aufgelöste Adresse, nach Erfolg das Dokument.
func (n *Node) doWrite(ctx context.Context, req *mcp.CallToolRequest, op writeOp, out *WriteOutput) error {
	if n.link.Connect == nil {
		return &toolError{msg: "Schreiben ist an diesem Eingang nicht eingerichtet", code: CodeUnsupported}
	}
	r, err := n.begin(ctx, req)
	if err != nil {
		return err
	}
	t, err := r.resolve(op.collection)
	if err != nil {
		return err
	}
	if t.Collection == "" {
		return &toolError{msg: fmt.Sprintf("Adresse %q ohne Collection; erwartet <hub>:<collection>", op.collection)}
	}
	out.Address = t.Address()
	if err := ident.CheckDocName(op.name); err != nil {
		return &toolError{msg: "name: " + err.Error()}
	}
	if op.rename {
		if err := ident.CheckDocName(op.newName); err != nil {
			return &toolError{msg: "new_name: " + err.Error()}
		}
		if err := ident.CheckRename(op.name, op.newName); err != nil {
			return &toolError{msg: err.Error()}
		}
	}
	// Dieselbe Prüfung wie beim Lesen: angemeldet, Replica da, Collection
	// lesbar. Die Replica bleibt nicht offen, während der Hub arbeitet.
	a, err := r.open(ctx, t)
	if err != nil {
		return err
	}
	a.Close()
	h := r.entries[t.Hub]
	l, _ := r.login(t.Hub)
	reqlog.Note(ctx, "hub", t.Hub)
	reqlog.Note(ctx, "node", h.NodeName)

	hub, done, err := n.link.Connect(ctx, h)
	if err != nil {
		reqlog.NoteError(ctx, err)
		if errors.Is(err, ErrUnsupported) {
			return &toolError{msg: fmt.Sprintf("Hub %s: Schreiben über den Transport dieses Hubs wird noch nicht "+
				"unterstützt; nichts gespeichert", t.Hub), code: CodeUnsupported}
		}
		return unreachableHub(t.Hub)
	}
	defer done()
	resp, err := op.call(ctx, hub, contract.NodeAuth{Node: h.NodeName, Token: h.Token},
		contract.AccountAuth{Account: l.Account, Token: headerToken(req, t.Hub)}, t.Collection)
	if err != nil {
		return n.hubFailure(ctx, h, t, err)
	}
	*out = n.accept(ctx, h, t, op, resp)
	n.kick(h)
	return nil
}

// headerToken ist das Token aus dem Header-Paar eines Hubs. Bei gültiger
// Anmeldung gibt es genau eins.
func headerToken(req *mcp.CallToolRequest, alias string) string {
	if req == nil || req.Extra == nil {
		return ""
	}
	for _, p := range HubHeaders(req.Extra.Header) {
		if p.Alias == alias {
			return p.Token
		}
	}
	return ""
}

func unreachableHub(hub string) error {
	return &toolError{msg: fmt.Sprintf("Hub %s nicht erreichbar, nichts gespeichert", hub), code: CodeUnreachable}
}

// hubFailure ordnet einen Fehler des Hubs ein (docs/vertrag.md, „Ausgang und
// Wiederholung“), in dieser Reihenfolge: ein Fehler des Vertrags ist
// abgelehnt; ErrOutcomeUnknown ist unklar — dann stößt der Node den Abgleich
// an, damit der Aufrufer nachsehen kann; ErrUnknownOperation heißt, der Hub
// kann noch nicht schreiben; jeder andere Fehler heißt nicht erreicht. Die
// Meldungen nennen weder Adresse noch Transport; die volle geht ins Log.
func (n *Node) hubFailure(ctx context.Context, h store.Hub, t target, err error) error {
	var ce *contract.Error
	switch {
	case errors.As(err, &ce):
		switch ce.Code {
		case contract.CodeAccountUnauthenticated, contract.CodeNotReadable:
			return notReadable(t.Address())
		case contract.CodeUnauthenticated:
			return &toolError{msg: fmt.Sprintf("Hub %s nimmt diesen Node nicht an; nichts gespeichert", t.Hub),
				code: string(ce.Code)}
		case contract.CodeUnsupportedVersion:
			return &toolError{msg: fmt.Sprintf("Hub %s bedient die Fassung des Vertrags dieses Nodes nicht; nichts "+
				"gespeichert", t.Hub), code: string(ce.Code)}
		}
		return &toolError{msg: fmt.Sprintf("Hub %s: %s", t.Hub, ce.Message), code: string(ce.Code)}
	case errors.Is(err, contract.ErrOutcomeUnknown):
		reqlog.NoteError(ctx, err)
		n.kick(h)
		return &toolError{msg: fmt.Sprintf("Hub %s: Ausgang unklar — abgeschickt, aber keine brauchbare Antwort; "+
			"gespeichert sein kann es. Nicht wiederholen: Der Abgleich ist angestoßen, danach mit read nachsehen "+
			"(Revision, geändert).", t.Hub), code: CodeOutcomeUnknown}
	case errors.Is(err, contract.ErrUnknownOperation):
		return &toolError{msg: fmt.Sprintf("Hub %s kann noch nicht schreiben (älter als dieser Node); nichts "+
			"gespeichert", t.Hub), code: CodeUnsupported}
	}
	reqlog.NoteError(ctx, err)
	return unreachableHub(t.Hub)
}

// accept übernimmt die Antwort eines Erfolgs: Die Zeilen kommen in die
// Replica, bevor der Client antwortet — so liefert read sofort die neue
// Revision, nach rename den neuen Namen (die Zeile ersetzt die alte per id).
// Scheitert das, bleibt es ein Erfolg mit Hinweis; der Abgleich holt nach.
// Auch bei abgebrochenem Aufruf wird noch geschrieben: Der Hub hat
// gespeichert.
func (n *Node) accept(ctx context.Context, h store.Hub, t target, op writeOp, resp contract.WriteResponse) WriteOutput {
	name := op.result()
	out := WriteOutput{Kind: KindDocument, Address: t.Address(), Name: name, Revision: resp.Revision}
	doc, dir, err := answerShape(t.Collection, name, op.dir, resp.Rows)
	if err == nil {
		err = replica.WriteRows(context.WithoutCancel(ctx), n.nodes, h, resp.HubID, resp.Rows)
	}
	if err != nil {
		reqlog.NoteError(ctx, err)
		out.Note = "Am Hub gespeichert, aber nicht in die Replica übernommen: read zeigt den alten Stand, bis der " +
			"Abgleich nachholt."
	}
	switch {
	case doc != nil:
		out.ID, out.Deleted = doc.ID, doc.Deleted
		out.Created, out.Updated = stamp(doc.CreatedAt, doc.CreatedBy), stamp(doc.UpdatedAt, doc.UpdatedBy)
		if doc.Content != nil {
			size := int64(len(*doc.Content))
			out.Size = &size
		}
	case dir:
		// Alle Zeilen eines Vorgangs tragen dieselbe Revision, dieselbe Zeit
		// und denselben Urheber.
		first := resp.Rows[0]
		out.Kind, out.Count, out.Deleted = KindDirectory, len(resp.Rows), first.Deleted
		out.Updated = stamp(first.UpdatedAt, first.UpdatedBy)
	}
	return out
}

// answerShape prüft die Zeilen einer Antwort: alle aus der Collection des
// Ziels, und entweder genau eine unter name — ein Dokument, dann doc — oder,
// wenn der Vorgang ein Verzeichnis nehmen kann (dirOK), mindestens eine und
// alle unter name/ — ein Verzeichnis, dann dir. Sonst ein Fehler; ein
// eindeutiges Dokument liefert es auch dann.
func answerShape(collection, name string, dirOK bool, rows []contract.Row) (doc *contract.Row, dir bool, err error) {
	at, under := 0, 0
	for i, r := range rows {
		switch {
		case r.Collection != collection:
			err = fmt.Errorf("der Hub antwortet mit einer Zeile aus Collection %q", r.Collection)
		case r.Name == name:
			at++
			doc = &rows[i]
		case dirOK && strings.HasPrefix(r.Name, name+"/"):
			under++
		default:
			err = fmt.Errorf("der Hub antwortet mit %s, erwartet %s", r.Name, name)
		}
	}
	if at != 1 {
		doc = nil
	}
	switch {
	case err != nil:
		return doc, false, err
	case at == 1 && under == 0:
		return doc, false, nil
	case at == 0 && under > 0:
		return nil, true, nil
	}
	return doc, false, fmt.Errorf("der Hub antwortet mit %d Zeilen für %s und %d darunter, erwartet ein Dokument "+
		"oder ein Verzeichnis", at, name, under)
}

// kick stößt den Abgleich eines Hub-Eintrags an, ohne zu warten.
func (n *Node) kick(h store.Hub) {
	if n.link.Sync != nil {
		n.link.Sync(h)
	}
}
