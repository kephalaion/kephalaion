package mcpnode

import (
	"context"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kephalaion/kephalaion/internal/ident"
	"github.com/kephalaion/kephalaion/internal/node/replica"
)

// ReadInput sind die Argumente von read.
type ReadInput struct {
	Collection  string `json:"collection,omitempty" jsonschema:"<hub>:<collection>, Hub-Teil entbehrlich bei nur einem Hub; <hub>: für die Wurzel des Hubs"`
	Name        string `json:"name,omitempty" jsonschema:"Dokument oder Verzeichnis in der Collection, leer für ihre Wurzel"`
	ID          string `json:"id,omitempty" jsonschema:"id des Dokuments statt name; collection dann entbehrlich bei nur einem Hub"`
	Content     *bool  `json:"content,omitempty" jsonschema:"Inhalt mitliefern, Standard ja; false nur die Angaben"`
	Frontmatter bool   `json:"frontmatter,omitempty" jsonschema:"auch das Frontmatter als JSON-Objekt: bei einem .md-Dokument seines, bei einem Verzeichnis oder der Wurzel der Collection das der README.md"`
}

// ReadOutput ist die Antwort von read: kind document, directory oder none —
// none ist kein Fehler. Den Inhalt eines Dokuments trägt der Text des
// Ergebnisses, nicht diese Struktur.
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
}

const readDescription = "Liest ein Dokument aus der Replica, per collection und name oder per id; der Inhalt ist " +
	"der Text des Ergebnisses. kind: document, directory oder none. Mit content: false nur Name, id, Revision, " +
	"angelegt, geändert, Größe und writable (write in der Collection; unter vendor/<name>/ der Scope vendor/<name>; " +
	"unter einem Verzeichnis-Scope dir <pfad>/ immer). " +
	"Mit frontmatter das Frontmatter (.md, bei einem Verzeichnis das seiner README.md) als JSON-Objekt; der Inhalt " +
	"bleibt der volle Text."

func (n *Node) read(ctx context.Context, req *mcp.CallToolRequest, in ReadInput) (*mcp.CallToolResult, ReadOutput, error) {
	out, content, err := n.doRead(ctx, req, in)
	if err != nil {
		return nil, ReadOutput{}, toolFailure(ctx, err)
	}
	if content == nil {
		return nil, out, nil
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: *content}}}, out, nil
}

func (n *Node) doRead(ctx context.Context, req *mcp.CallToolRequest, in ReadInput) (ReadOutput, *string, error) {
	withContent := in.Content == nil || *in.Content
	if in.ID != "" && in.Name != "" {
		return ReadOutput{}, nil, &toolError{msg: "name oder id, nicht beides"}
	}
	if in.ID == "" && in.Collection == "" {
		return ReadOutput{}, nil, &toolError{msg: "collection fehlt; ohne collection nur mit id"}
	}
	r, err := n.begin(ctx, req)
	if err != nil {
		return ReadOutput{}, nil, err
	}
	t, err := r.resolve(in.Collection)
	if err != nil {
		return ReadOutput{}, nil, err
	}
	a, err := r.open(ctx, t)
	if err != nil {
		return ReadOutput{}, nil, err
	}
	defer a.Close()
	if in.ID != "" {
		out, content, err := readByID(ctx, a, t, in.ID, withContent, in.Frontmatter)
		return out, content, a.wrap(ctx, err)
	}
	if t.Collection == "" {
		if in.Name != "" {
			return ReadOutput{}, nil, &toolError{msg: "name nur zusammen mit einer Collection, nicht mit der Wurzel eines Hubs"}
		}
		return ReadOutput{Kind: KindDirectory, Address: t.Address()}, nil, nil
	}
	out, content, err := readByName(ctx, a, t, in.Name, withContent, in.Frontmatter)
	return out, content, a.wrap(ctx, err)
}

// readByName liest einen Namen einer lesbaren Collection: Dokument,
// Verzeichnis (ein lebendes Dokument darunter; leer ist die Wurzel) oder
// nichts. Ein '/' am Ende ist erlaubt. Mit withFM dazu das Frontmatter: das
// des Dokuments, bei Verzeichnis und Wurzel das der README.md. writable
// folgt je Name der Regel des Hubs (contract.Rights).
func readByName(ctx context.Context, a *hubAccess, t target, name string, withContent, withFM bool) (ReadOutput, *string, error) {
	rights := a.rights[t.Collection]
	out := ReadOutput{Kind: KindNone, Address: t.Address(), Name: name}
	name = strings.TrimSuffix(name, "/")
	if name == "" {
		writable := rights.WritableUnder("")
		out.Kind, out.Name, out.Writable = KindDirectory, "", &writable
		if withFM {
			var err error
			if out.Frontmatter, out.FrontmatterError, err = readmeFrontmatter(ctx, a.rep, t.Collection, ""); err != nil {
				return ReadOutput{}, nil, err
			}
		}
		return out, nil, nil
	}
	if err := ident.CheckDocName(name); err != nil {
		return ReadOutput{}, nil, &toolError{msg: "name: " + err.Error()}
	}
	out.Name = name
	e, ok, err := a.rep.EntryByName(ctx, t.Collection, name, withContent)
	if err != nil {
		return ReadOutput{}, nil, err
	}
	if ok {
		out = documentOutput(t, e, rights.Writable(e.Name))
		if withFM {
			if out.Frontmatter, out.FrontmatterError, err = documentFrontmatter(ctx, a.rep, e.Name, e.ID, e.Content); err != nil {
				return ReadOutput{}, nil, err
			}
		}
		return out, e.Content, nil
	}
	dir, err := a.rep.HasUnder(ctx, t.Collection, name+"/")
	if err != nil {
		return ReadOutput{}, nil, err
	}
	if dir {
		writable := rights.WritableUnder(name)
		out.Kind, out.Writable = KindDirectory, &writable
		if withFM {
			if out.Frontmatter, out.FrontmatterError, err = readmeFrontmatter(ctx, a.rep, t.Collection, name+"/"); err != nil {
				return ReadOutput{}, nil, err
			}
		}
	}
	return out, nil, nil
}

// readByID liest eine id im Hub des Ziels; nennt das Ziel eine Collection,
// nur dort. Eine Löschmarke, eine SYSTEM:-Zeile und ein Dokument in einer
// Collection, die der Client nicht lesen darf, sind none — wie eine
// unbekannte id. Mit withFM dazu das Frontmatter des Dokuments.
func readByID(ctx context.Context, a *hubAccess, t target, id string, withContent, withFM bool) (ReadOutput, *string, error) {
	none := ReadOutput{Kind: KindNone, Address: t.Address(), ID: id}
	e, ok, err := a.rep.EntryByID(ctx, id, withContent)
	if err != nil {
		return ReadOutput{}, nil, err
	}
	if !ok || e.Deleted || !a.readable(e.Collection) || (t.Collection != "" && e.Collection != t.Collection) {
		return none, nil, nil
	}
	dt := target{Hub: t.Hub, Collection: e.Collection}
	out := documentOutput(dt, e, a.rights[e.Collection].Writable(e.Name))
	if withFM {
		if out.Frontmatter, out.FrontmatterError, err = documentFrontmatter(ctx, a.rep, e.Name, e.ID, e.Content); err != nil {
			return ReadOutput{}, nil, err
		}
	}
	return out, e.Content, nil
}

func documentOutput(t target, e replica.Entry, writable bool) ReadOutput {
	size := e.Size
	return ReadOutput{Kind: KindDocument, Address: t.Address(), Name: e.Name, ID: e.ID, Revision: e.Revision,
		Created: stamp(e.CreatedAt, e.CreatedBy), Updated: stamp(e.UpdatedAt, e.UpdatedBy), Size: &size,
		Writable: &writable}
}
