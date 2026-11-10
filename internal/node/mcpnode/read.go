package mcpnode

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kephalaion/kephalaion/internal/ident"
	"github.com/kephalaion/kephalaion/internal/node/replica"
	"github.com/kephalaion/kephalaion/internal/reqlog"
	"github.com/kephalaion/kephalaion/internal/sections"
)

// ReadInput sind die Argumente von read.
type ReadInput struct {
	Collection  string `json:"collection,omitempty" jsonschema:"<hub>:<collection>, Hub-Teil entbehrlich bei nur einem Hub; <hub>: für die Wurzel des Hubs"`
	Name        string `json:"name,omitempty" jsonschema:"Dokument oder Verzeichnis in der Collection, leer für ihre Wurzel"`
	ID          string `json:"id,omitempty" jsonschema:"id des Dokuments statt name; collection dann entbehrlich bei nur einem Hub"`
	Content     *bool  `json:"content,omitempty" jsonschema:"Inhalt mitliefern, Standard ja; false nur die Angaben"`
	Frontmatter bool   `json:"frontmatter,omitempty" jsonschema:"auch das Frontmatter als JSON-Objekt: bei einem .md-Dokument seines, bei einem Verzeichnis oder der Wurzel der Collection das der README.md"`
	Section     string `json:"section,omitempty" jsonschema:"Anker eines Abschnitts (section.anchor eines Treffers von search): content ist dann nur dieser Abschnitt; nur bei einem Dokument"`
}

// ReadOutput ist die Antwort von read: kind document, directory oder none —
// none ist kein Fehler. Sie ist vollständig: Angaben, Frontmatter und der
// Inhalt eines Dokuments im Feld Content. Den Textblock des Ergebnisses setzt
// das SDK als JSON dieser Struktur; er trägt nichts darüber hinaus (Task 024).
type ReadOutput struct {
	Kind string `json:"kind"`
	// Address ist die Collection als <hub>:<collection>, bei der Wurzel eines
	// Hubs <hub>:.
	Address  string `json:"address"`
	Name     string `json:"name"`
	ID       string `json:"id,omitempty"`
	Revision int64  `json:"revision,omitempty"`
	Created  *Stamp `json:"created,omitempty"`
	Updated  *Stamp `json:"updated,omitempty"`
	// Size ist die Größe des Inhalts in Bytes.
	Size *int64 `json:"size,omitempty"`
	// Writable sagt, ob der Account das Dokument nach der Regel des Hubs
	// anlegen oder als Eigenes ändern dürfte (contract.Rights.Writable):
	// unter vendor/<name>/ der Scope vendor/<name>, direkt in vendor/ nie,
	// unter einem Verzeichnis-Scope immer, sonst write. Bei einem
	// Verzeichnis und der Wurzel der Collection, ob darunter etwas angelegt
	// werden dürfte (WritableUnder). Ob ein fremdes Dokument supersede
	// braucht, sagt es nicht — unter einem Verzeichnis-Scope braucht es ihn
	// nicht.
	Writable *bool `json:"writable,omitempty"`
	// Frontmatter ist das Frontmatter als JSON-Objekt, FrontmatterError der
	// Grund, wenn es sich nicht lesen ließ — nur mit dem Parameter
	// frontmatter (frontmatter.go).
	Frontmatter      any    `json:"frontmatter,omitempty"`
	FrontmatterError string `json:"frontmatter_error,omitempty"`
	// Content ist der Inhalt eines Dokuments, nur mit dem Parameter content
	// (Standard ja). Ein Zeiger, damit ein leeres Dokument ("") von „kein
	// Inhalt geliefert“ unterscheidbar bleibt: mit content: false, bei einem
	// Verzeichnis und bei none fehlt das Feld.
	Content *string `json:"content,omitempty" jsonschema:"Inhalt des Dokuments als Text, einschließlich Frontmatter; ein leeres Dokument hat \"\"; fehlt mit content: false, bei einem Verzeichnis und bei none; mit section nur der Abschnitt"`
	// Section ist mit dem Parameter section der gelesene Abschnitt:
	// Überschrift, Pfad der Überschriften und Anker (Task 033).
	Section *HitSection `json:"section,omitempty"`
	// Error ist der Fehler, nur mit isError und nur bei section_not_found;
	// andere Fehler von read tragen nur die Meldung.
	Error *WriteError `json:"error,omitempty"`
}

// CodeSectionNotFound ist der Code von read, wenn es den Anker im Dokument
// nicht gibt (Task 033, Entscheidung 16): ein Fehler der Anfrage, als Code
// neben der Meldung, kein Code des Vertrags.
const CodeSectionNotFound = "section_not_found"

const readDescription = "Liest ein Dokument aus der Replica, per collection und name oder per id; der Inhalt " +
	"steht im Feld content der Antwort. kind: document, directory oder none. Mit content: false ohne das Feld " +
	"content, nur Name, id, Revision, angelegt, geändert, Größe und writable (write in der Collection; unter " +
	"vendor/<name>/ der Scope vendor/<name>; unter einem Verzeichnis-Scope dir <pfad>/ immer). " +
	"Mit frontmatter das Frontmatter (.md, bei einem Verzeichnis das seiner README.md) als JSON-Objekt; content " +
	"bleibt der volle Text. Mit section (dem Anker eines Treffers von search) ist content nur dieser Abschnitt — " +
	"die Überschrift samt Text bis zur nächsten Überschrift —, dazu section mit heading, headings und anchor; " +
	"einen Anker, den es nicht gibt, beantwortet read mit error.code section_not_found."

func (n *Node) read(ctx context.Context, req *mcp.CallToolRequest, in ReadInput) (*mcp.CallToolResult, ReadOutput, error) {
	out, err := n.doRead(ctx, req, in)
	if err != nil {
		var te *toolError
		if errors.As(err, &te) && te.code == CodeSectionNotFound {
			// Ein Fehler der Anfrage mit Code (Entscheidung 16): isError und
			// error in der Struktur, wie bei den Werkzeugen, die schreiben.
			reqlog.Note(ctx, "code", te.code)
			fail := ReadOutput{Kind: out.Kind, Address: out.Address, Name: out.Name, ID: out.ID,
				Error: &WriteError{Code: te.code, Message: te.msg}}
			return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: te.msg}}}, fail, nil
		}
		return nil, ReadOutput{}, toolFailure(ctx, err)
	}
	return nil, out, nil
}

func (n *Node) doRead(ctx context.Context, req *mcp.CallToolRequest, in ReadInput) (ReadOutput, error) {
	withContent := in.Content == nil || *in.Content
	if in.ID != "" && in.Name != "" {
		return ReadOutput{}, &toolError{msg: "name oder id, nicht beides"}
	}
	if in.Section != "" && !withContent {
		return ReadOutput{}, &toolError{msg: "section liefert einen Abschnitt als content; nicht mit content: false"}
	}
	if in.ID == "" && in.Collection == "" {
		return ReadOutput{}, &toolError{msg: "collection fehlt; ohne collection nur mit id"}
	}
	r, err := n.begin(ctx, req)
	if err != nil {
		return ReadOutput{}, err
	}
	t, err := r.resolve(in.Collection)
	if err != nil {
		return ReadOutput{}, err
	}
	a, err := r.open(ctx, t)
	if err != nil {
		return ReadOutput{}, err
	}
	defer a.Close()
	var out ReadOutput
	switch {
	case in.ID != "":
		out, err = readByID(ctx, a, t, in.ID, withContent, in.Frontmatter)
	case t.Collection == "":
		if in.Name != "" {
			return ReadOutput{}, &toolError{msg: "name nur zusammen mit einer Collection, nicht mit der Wurzel eines Hubs"}
		}
		out = ReadOutput{Kind: KindDirectory, Address: t.Address()}
	default:
		out, err = readByName(ctx, a, t, in.Name, withContent, in.Frontmatter)
	}
	if err != nil {
		return ReadOutput{}, a.wrap(ctx, err)
	}
	if in.Section != "" {
		return readSection(out, in.Section)
	}
	return out, nil
}

// readSection schneidet aus dem gelesenen Dokument den Abschnitt mit dem
// Anker anchor heraus, aus derselben Zerlegung wie der Volltextindex
// (sections.Find). none bleibt none; ein Verzeichnis hat keine Abschnitte.
// Einen Anker, den es im Dokument nicht gibt, meldet es mit
// CodeSectionNotFound — out trägt dann Art, Adresse, Name und id.
func readSection(out ReadOutput, anchor string) (ReadOutput, error) {
	switch out.Kind {
	case KindNone:
		return out, nil
	case KindDirectory:
		return ReadOutput{}, &toolError{msg: fmt.Sprintf("section nur bei einem Dokument; %s ist ein Verzeichnis", out.Name)}
	}
	s, ok := sections.Find(*out.Content, anchor)
	if !ok {
		return out, &toolError{msg: fmt.Sprintf("Abschnitt %q gibt es in %s nicht; Anker liefert search (section.anchor)",
			anchor, out.Name), code: CodeSectionNotFound}
	}
	headings := s.Headings
	if headings == nil {
		headings = []string{}
	}
	out.Content = &s.Content
	out.Section = &HitSection{Heading: s.Heading, Headings: headings, Anchor: s.Anchor}
	return out, nil
}

// readByName liest einen Namen einer lesbaren Collection: Dokument,
// Verzeichnis (ein lebendes Dokument darunter; leer ist die Wurzel) oder
// nichts. Ein '/' am Ende ist erlaubt. Mit withFM dazu das Frontmatter: das
// des Dokuments, bei Verzeichnis und Wurzel das der README.md. Mit
// withContent trägt ein Dokument seinen Inhalt in Content. writable folgt je
// Name der Regel des Hubs (contract.Rights).
func readByName(ctx context.Context, a *hubAccess, t target, name string, withContent, withFM bool) (ReadOutput, error) {
	rights := a.rights[t.Collection]
	out := ReadOutput{Kind: KindNone, Address: t.Address(), Name: name}
	name = strings.TrimSuffix(name, "/")
	if name == "" {
		writable := rights.WritableUnder("")
		out.Kind, out.Name, out.Writable = KindDirectory, "", &writable
		if withFM {
			var err error
			if out.Frontmatter, out.FrontmatterError, err = readmeFrontmatter(ctx, a.rep, t.Collection, ""); err != nil {
				return ReadOutput{}, err
			}
		}
		return out, nil
	}
	if err := ident.CheckDocName(name); err != nil {
		return ReadOutput{}, &toolError{msg: "name: " + err.Error()}
	}
	out.Name = name
	e, ok, err := a.rep.EntryByName(ctx, t.Collection, name, withContent)
	if err != nil {
		return ReadOutput{}, err
	}
	if ok {
		out = documentOutput(t, e, rights.Writable(e.Name))
		if withFM {
			if out.Frontmatter, out.FrontmatterError, err = documentFrontmatter(ctx, a.rep, e.Name, e.ID, e.Content); err != nil {
				return ReadOutput{}, err
			}
		}
		if withContent {
			out.Content = e.Content
		}
		return out, nil
	}
	dir, err := a.rep.HasUnder(ctx, t.Collection, name+"/")
	if err != nil {
		return ReadOutput{}, err
	}
	if dir {
		writable := rights.WritableUnder(name)
		out.Kind, out.Writable = KindDirectory, &writable
		if withFM {
			if out.Frontmatter, out.FrontmatterError, err = readmeFrontmatter(ctx, a.rep, t.Collection, name+"/"); err != nil {
				return ReadOutput{}, err
			}
		}
	}
	return out, nil
}

// readByID liest eine id im Hub des Ziels; nennt das Ziel eine Collection,
// nur dort. Eine Löschmarke, eine SYSTEM:-Zeile und ein Dokument in einer
// Collection, die der Client nicht lesen darf, sind none — wie eine
// unbekannte id. Mit withFM dazu das Frontmatter des Dokuments, mit
// withContent sein Inhalt in Content.
func readByID(ctx context.Context, a *hubAccess, t target, id string, withContent, withFM bool) (ReadOutput, error) {
	none := ReadOutput{Kind: KindNone, Address: t.Address(), ID: id}
	e, ok, err := a.rep.EntryByID(ctx, id, withContent)
	if err != nil {
		return ReadOutput{}, err
	}
	if !ok || e.Deleted || !a.readable(e.Collection) || (t.Collection != "" && e.Collection != t.Collection) {
		return none, nil
	}
	dt := target{Hub: t.Hub, Collection: e.Collection}
	out := documentOutput(dt, e, a.rights[e.Collection].Writable(e.Name))
	if withFM {
		if out.Frontmatter, out.FrontmatterError, err = documentFrontmatter(ctx, a.rep, e.Name, e.ID, e.Content); err != nil {
			return ReadOutput{}, err
		}
	}
	if withContent {
		out.Content = e.Content
	}
	return out, nil
}

func documentOutput(t target, e replica.Entry, writable bool) ReadOutput {
	size := e.Size
	return ReadOutput{Kind: KindDocument, Address: t.Address(), Name: e.Name, ID: e.ID, Revision: e.Revision,
		Created: stamp(e.CreatedAt, e.CreatedBy), Updated: stamp(e.UpdatedAt, e.UpdatedBy), Size: &size,
		Writable: &writable}
}
