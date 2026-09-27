package mcpnode

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kephalaion/kephalaion/internal/ident"
	"github.com/kephalaion/kephalaion/internal/node/replica"
)

// Grenzen von limit in list und changes.
const (
	// DefaultLimit gilt ohne limit.
	DefaultLimit = 100
	// MaxLimit ist der Höchstwert; ein größeres limit wird darauf gekürzt.
	MaxLimit = 1000
)

// Die Arten eines Eintrags in list und read.
const (
	KindCollection = "collection"
	KindDirectory  = "directory"
	KindDocument   = "document"
	KindNone       = "none"
)

// ListInput sind die Argumente von list.
type ListInput struct {
	Collection string `json:"collection,omitempty" jsonschema:"<hub>:<collection>, Hub-Teil entbehrlich bei nur einem Hub; <hub>: listet die Collections des Hubs, leer alle lesbaren"`
	Path       string `json:"path,omitempty" jsonschema:"Verzeichnis in der Collection, leer für die Wurzel"`
	Recursive  bool   `json:"recursive,omitempty" jsonschema:"auch Dokumente in Unterverzeichnissen, dann ohne Verzeichnisse als Einträge"`
	Sort       string `json:"sort,omitempty" jsonschema:"name (Standard), created oder updated"`
	Order      string `json:"order,omitempty" jsonschema:"asc (Standard) oder desc"`
	Limit      int    `json:"limit,omitempty" jsonschema:"höchstens so viele Einträge, Standard 100, höchstens 1000"`
	Cursor     string `json:"cursor,omitempty" jsonschema:"cursor der letzten Antwort, um weiterzublättern"`
	Mask       string `json:"mask,omitempty" jsonschema:"Glob auf das letzte Segment, nur für Dokumente, etwa *.md"`
}

// ListOutput ist die Antwort von list.
type ListOutput struct {
	Entries []ListEntry `json:"entries"`
	// More sagt, dass weitere Einträge folgen; Cursor holt sie.
	More   bool   `json:"more"`
	Cursor string `json:"cursor,omitempty"`
	// UnreadableHubs nennt Hubs, deren Replica sich nicht lesen ließ; nur
	// beim Auflisten der Collections aller Hubs.
	UnreadableHubs []string `json:"unreadable_hubs,omitempty"`
}

// ListEntry ist ein Eintrag von list: eine Collection (Name und Adresse), ein
// Verzeichnis (nur der Name) oder ein Dokument. Name ist der volle Name in
// der Collection, auch bei Verzeichnissen.
type ListEntry struct {
	Kind     string `json:"kind"`
	Name     string `json:"name"`
	Address  string `json:"address,omitempty"`
	ID       string `json:"id,omitempty"`
	Revision int64  `json:"revision,omitempty"`
	Created  *Stamp `json:"created,omitempty"`
	Updated  *Stamp `json:"updated,omitempty"`
	// Size ist die Größe des Inhalts in Bytes.
	Size *int64 `json:"size,omitempty"`
}

const listDescription = "Listet ein Verzeichnis einer Collection aus der Replica: zuerst die Unterverzeichnisse " +
	"(ohne recursive), dann die Dokumente mit Name, id, Revision, angelegt und geändert, Größe. Ohne collection " +
	"die lesbaren Collections. Mit more weiter über cursor."

// listCursor ist der Cursor von list: die Stelle nach dem letzten Eintrag
// und der Fingerabdruck der Anfrage. Dir sagt, dass der letzte Eintrag ein
// Verzeichnis oder eine Collection war.
type listCursor struct {
	V    int    `json:"v"`
	Q    string `json:"q"`
	Dir  bool   `json:"d,omitempty"`
	Name string `json:"n"`
	At   int64  `json:"t,omitempty"`
}

// listParams sind die geprüften Argumente von list.
type listParams struct {
	in    ListInput
	sort  string
	desc  bool
	limit int
	after *listCursor
	fp    string
}

func checkListInput(in ListInput) (listParams, error) {
	p := listParams{in: in, sort: in.Sort, limit: in.Limit}
	if p.sort == "" {
		p.sort = replica.SortName
	}
	if p.sort != replica.SortName && p.sort != replica.SortCreated && p.sort != replica.SortUpdated {
		return p, &toolError{msg: fmt.Sprintf("sort %q: erwartet name, created oder updated", in.Sort)}
	}
	switch in.Order {
	case "", "asc":
	case "desc":
		p.desc = true
	default:
		return p, &toolError{msg: fmt.Sprintf("order %q: erwartet asc oder desc", in.Order)}
	}
	var err error
	if p.limit, err = checkLimit(in.Limit); err != nil {
		return p, err
	}
	if in.Mask != "" {
		if _, err := path.Match(in.Mask, ""); err != nil || strings.Contains(in.Mask, "/") {
			return p, &toolError{msg: fmt.Sprintf("mask %q: kein gültiger Glob für ein Segment", in.Mask)}
		}
	}
	p.fp = fingerprint("list", in.Collection, in.Path, strconv.FormatBool(in.Recursive), p.sort,
		strconv.FormatBool(p.desc), in.Mask)
	if in.Cursor != "" {
		var c listCursor
		if err := decodeCursor(in.Cursor, &c); err != nil {
			return p, err
		}
		if c.V != cursorVersion || c.Q != p.fp {
			return p, errCursorMismatch
		}
		p.after = &c
	}
	return p, nil
}

// checkLimit prüft limit: 0 heißt DefaultLimit, mehr als MaxLimit wird
// gekürzt.
func checkLimit(limit int) (int, error) {
	switch {
	case limit < 0:
		return 0, &toolError{msg: fmt.Sprintf("limit %d: erwartet eine Zahl ab 1", limit)}
	case limit == 0:
		return DefaultLimit, nil
	case limit > MaxLimit:
		return MaxLimit, nil
	}
	return limit, nil
}

// page sammelt die Einträge einer Seite: höchstens limit, danach nur noch
// die Feststellung, dass mehr folgt.
type listPage struct {
	p    listParams
	out  ListOutput
	last listCursor
}

// add nimmt einen Eintrag auf und merkt sich seine Stelle; ist die Seite
// voll, setzt es More und liefert false.
func (pg *listPage) add(e ListEntry, at listCursor) bool {
	if len(pg.out.Entries) == pg.p.limit {
		pg.out.More = true
		return false
	}
	pg.out.Entries = append(pg.out.Entries, e)
	pg.last = at
	return true
}

func (pg *listPage) finish() ListOutput {
	if pg.out.More {
		pg.last.V, pg.last.Q = cursorVersion, pg.p.fp
		pg.out.Cursor = encodeCursor(pg.last)
	}
	return pg.out
}

func (n *Node) list(ctx context.Context, req *mcp.CallToolRequest, in ListInput) (*mcp.CallToolResult, ListOutput, error) {
	out, err := n.doList(ctx, req, in)
	if err != nil {
		return nil, ListOutput{}, toolFailure(ctx, err)
	}
	return nil, out, nil
}

func (n *Node) doList(ctx context.Context, req *mcp.CallToolRequest, in ListInput) (ListOutput, error) {
	p, err := checkListInput(in)
	if err != nil {
		return ListOutput{}, err
	}
	r, err := n.begin(ctx, req)
	if err != nil {
		return ListOutput{}, err
	}
	pg := &listPage{p: p, out: ListOutput{Entries: []ListEntry{}}}
	if in.Collection == "" {
		if in.Path != "" {
			return ListOutput{}, &toolError{msg: "path nur zusammen mit collection"}
		}
		var colls []ListEntry
		unread, err := r.eachValid(ctx, func(a *hubAccess) error {
			colls = append(colls, collectionEntries(a)...)
			return nil
		})
		if err != nil {
			return ListOutput{}, err
		}
		pageCollections(pg, colls)
		out := pg.finish()
		if len(unread) > 0 {
			out.UnreadableHubs = unread
		}
		return out, nil
	}
	t, err := r.resolve(in.Collection)
	if err != nil {
		return ListOutput{}, err
	}
	a, err := r.open(ctx, t)
	if err != nil {
		return ListOutput{}, err
	}
	defer a.Close()
	if t.Collection == "" {
		if in.Path != "" {
			return ListOutput{}, &toolError{msg: "path nur zusammen mit einer Collection, nicht mit der Wurzel eines Hubs"}
		}
		pageCollections(pg, collectionEntries(a))
		return pg.finish(), nil
	}
	prefix, err := ident.DocDirPrefix(in.Path)
	if err != nil {
		return ListOutput{}, &toolError{msg: "path: " + err.Error()}
	}
	if err := a.wrap(ctx, listDocuments(ctx, a, t, prefix, pg)); err != nil {
		return ListOutput{}, err
	}
	return pg.finish(), nil
}

// collectionEntries sind die lesbaren Collections eines Hubs als Einträge.
func collectionEntries(a *hubAccess) []ListEntry {
	var out []ListEntry
	for _, c := range a.collections() {
		out = append(out, ListEntry{Kind: KindCollection, Name: c, Address: ident.Address(a.hub, c)})
	}
	return out
}

// pageCollections blättert in Collections nach Adresse; sort und order
// gelten hier nicht.
func pageCollections(pg *listPage, colls []ListEntry) {
	sort.Slice(colls, func(i, j int) bool { return colls[i].Address < colls[j].Address })
	for _, c := range colls {
		if pg.p.after != nil && c.Address <= pg.p.after.Name {
			continue
		}
		if !pg.add(c, listCursor{Dir: true, Name: c.Address}) {
			return
		}
	}
}

// listDocuments füllt die Seite aus einer Collection: ohne recursive zuerst
// die Verzeichnisse der nächsten Ebene nach Name, dann die Dokumente in der
// verlangten Ordnung; mask filtert nur Dokumente, in Go.
func listDocuments(ctx context.Context, a *hubAccess, t target, prefix string, pg *listPage) error {
	after := pg.p.after
	if !pg.p.in.Recursive && (after == nil || after.Dir) {
		dirs, err := a.rep.ChildDirs(ctx, t.Collection, prefix)
		if err != nil {
			return err
		}
		for _, d := range dirs {
			name := prefix + d
			if after != nil && name <= after.Name {
				continue
			}
			if !pg.add(ListEntry{Kind: KindDirectory, Name: name}, listCursor{Dir: true, Name: name}) {
				return nil
			}
		}
		after = nil
	}
	q := replica.ListQuery{Collection: t.Collection, Prefix: prefix, Recursive: pg.p.in.Recursive, Sort: pg.p.sort,
		Desc: pg.p.desc}
	if after != nil {
		q.After = &replica.ListKey{At: after.At, Name: after.Name}
	}
	return a.rep.ListEntries(ctx, q, func(e replica.Entry) bool {
		if pg.p.in.Mask != "" {
			if ok, _ := path.Match(pg.p.in.Mask, lastSegment(e.Name)); !ok {
				return true
			}
		}
		at := int64(0)
		switch pg.p.sort {
		case replica.SortCreated:
			at = e.CreatedAt
		case replica.SortUpdated:
			at = e.UpdatedAt
		}
		return pg.add(documentEntry(e), listCursor{Name: e.Name, At: at})
	})
}

func lastSegment(name string) string { return name[strings.LastIndexByte(name, '/')+1:] }

func documentEntry(e replica.Entry) ListEntry {
	size := e.Size
	return ListEntry{Kind: KindDocument, Name: e.Name, ID: e.ID, Revision: e.Revision,
		Created: stamp(e.CreatedAt, e.CreatedBy), Updated: stamp(e.UpdatedAt, e.UpdatedBy), Size: &size}
}
