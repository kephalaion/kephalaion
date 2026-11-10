package mcpnode

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kephalaion/kephalaion/internal/contract"
	"github.com/kephalaion/kephalaion/internal/ident"
	"github.com/kephalaion/kephalaion/internal/node/replica"
	"github.com/kephalaion/kephalaion/internal/reqlog"
)

// Das Werkzeug search (Task 033, docs/indizierung.md, „Suche über MCP“):
// Volltextsuche über docs/ der lesbaren Collections, aus dem Index der
// Replica (replica.Search). Lesbarkeit wie list; getrennte Listen je
// Collection, Rang ab 1, kein Punktwert.

// Grenzen von limit bei search (Entscheidung 21).
const (
	SearchDefaultLimit = 10
	SearchMaxLimit     = 50
)

// SearchInput sind die Argumente von search.
type SearchInput struct {
	Query      string `json:"query" jsonschema:"einfache Wörter auf Englisch, auch eine Frage; keine Suchsyntax"`
	Collection string `json:"collection,omitempty" jsonschema:"<hub>:<collection> oder <hub>:, Hub-Teil entbehrlich bei nur einem Hub; leer alle lesbaren Collections aller angemeldeten Hubs"`
	Path       string `json:"path,omitempty" jsonschema:"nur unter diesem Verzeichnis: docs oder darunter, etwa docs/manual"`
	Limit      int    `json:"limit,omitempty" jsonschema:"höchstens so viele Treffer je Collection, Standard 10, höchstens 50"`
}

// SearchOutput ist die Antwort von search: je Collection mit Treffern eine
// Liste, nach Adresse; ohne Treffer leer und ein Hinweis.
type SearchOutput struct {
	Results []SearchResult `json:"results"`
	// Hint steht nur, wenn nichts gefunden wurde.
	Hint string `json:"hint,omitempty"`
	// UnreadableHubs nennt Hubs, deren Replica sich nicht lesen ließ.
	UnreadableHubs []string `json:"unreadable_hubs,omitempty"`
}

// SearchResult sind die Treffer einer Collection, nach Rang.
type SearchResult struct {
	Address string      `json:"address"`
	Hits    []SearchHit `json:"hits"`
}

// SearchHit ist ein Treffer: ein Abschnitt eines Dokuments. name und
// section.anchor gehen unverändert an read (name, section).
type SearchHit struct {
	Rank        int        `json:"rank"`
	Name        string     `json:"name"`
	Title       string     `json:"title"`
	Description string     `json:"description,omitempty"`
	Section     HitSection `json:"section"`
	Excerpt     string     `json:"excerpt"`
}

// HitSection ist der Abschnitt eines Treffers: die Überschrift, der Pfad der
// Überschriften bis zu ihr und der Anker. Vor der ersten Überschrift sind
// heading und anchor leer — dann read ohne section.
type HitSection struct {
	Heading  string   `json:"heading"`
	Headings []string `json:"headings"`
	Anchor   string   `json:"anchor"`
}

const searchDescription = "Volltextsuche über docs/ der lesbaren Collections (Replica, FTS5). query: einfache Wörter " +
	"auf Englisch, Fragen gehen; keine Suchsyntax, die Wörter werden mit OR verbunden und nach Relevanz geordnet. " +
	"Treffer sind Abschnitte: je Collection eine Liste hits mit rank (ab 1), name, title, description, section " +
	"(heading, headings, anchor) und excerpt; höchstens drei Abschnitte je Dokument. Zum Weiterlesen name und " +
	"section.anchor an read geben (name, section). path grenzt auf ein Verzeichnis unter docs/ ein, limit (Standard " +
	"10, höchstens 50) gilt je Collection. Ohne Treffer: hint auf docs/index.md, die Übersicht. index.md, " +
	"docs/README.md und Mahnungen sind nie Treffer; neu Geschriebenes erst nach dem Abgleich."

func (n *Node) search(ctx context.Context, req *mcp.CallToolRequest, in SearchInput) (*mcp.CallToolResult, SearchOutput, error) {
	reqlog.Note(ctx, "op", "search")
	out, err := n.doSearch(ctx, req, in)
	if err != nil {
		return nil, SearchOutput{}, toolFailure(ctx, err)
	}
	return nil, out, nil
}

func (n *Node) doSearch(ctx context.Context, req *mcp.CallToolRequest, in SearchInput) (SearchOutput, error) {
	match := replica.MatchQuery(in.Query)
	if match == "" {
		return SearchOutput{}, &toolError{msg: "query fehlt: erwartet einfache Wörter auf Englisch"}
	}
	limit, err := searchLimit(in.Limit)
	if err != nil {
		return SearchOutput{}, err
	}
	prefix, err := searchPrefix(in.Path)
	if err != nil {
		return SearchOutput{}, err
	}
	r, err := n.begin(ctx, req)
	if err != nil {
		return SearchOutput{}, err
	}
	out := SearchOutput{Results: []SearchResult{}}
	collect := func(a *hubAccess, only string) error {
		for _, c := range a.collections() {
			if only != "" && c != only {
				continue
			}
			hits, err := a.rep.Search(ctx, c, prefix, match, limit)
			if err != nil {
				return err
			}
			if len(hits) == 0 {
				continue
			}
			res := SearchResult{Address: ident.Address(a.hub, c), Hits: make([]SearchHit, len(hits))}
			for i, h := range hits {
				headings := h.Headings
				if headings == nil {
					headings = []string{}
				}
				res.Hits[i] = SearchHit{Rank: i + 1, Name: h.Name, Title: h.Title, Description: h.Description,
					Section: HitSection{Heading: h.Heading, Headings: headings, Anchor: h.Anchor}, Excerpt: h.Excerpt}
			}
			out.Results = append(out.Results, res)
		}
		return nil
	}
	if in.Collection == "" {
		unread, err := r.eachValid(ctx, func(a *hubAccess) error { return collect(a, "") })
		if err != nil {
			return SearchOutput{}, err
		}
		if len(unread) > 0 {
			out.UnreadableHubs = unread
		}
	} else {
		t, err := r.resolve(in.Collection)
		if err != nil {
			return SearchOutput{}, err
		}
		a, err := r.open(ctx, t)
		if err != nil {
			return SearchOutput{}, err
		}
		err = a.wrap(ctx, collect(a, t.Collection))
		a.Close()
		if err != nil {
			return SearchOutput{}, err
		}
	}
	if len(out.Results) == 0 {
		out.Hint = "Nichts gefunden. Übersicht: read auf docs/index.md der Collection (je Verzeichnis eine index.md); " +
			"oder andere englische Wörter."
	}
	reqlog.Note(ctx, "hits", fmt.Sprint(len(out.Results)))
	return out, nil
}

// searchLimit prüft limit: Standard SearchDefaultLimit, mehr als
// SearchMaxLimit wird gekürzt.
func searchLimit(limit int) (int, error) {
	switch {
	case limit < 0:
		return 0, &toolError{msg: fmt.Sprintf("limit %d: erwartet eine Zahl ab 1", limit)}
	case limit == 0:
		return SearchDefaultLimit, nil
	case limit > SearchMaxLimit:
		return SearchMaxLimit, nil
	}
	return limit, nil
}

// searchPrefix liefert das Verzeichnis, unter dem gesucht wird, mit '/' am
// Ende: docs/ ohne path, sonst path — docs oder darunter.
func searchPrefix(path string) (string, error) {
	p, err := ident.DocDirPrefix(path)
	if err != nil {
		return "", &toolError{msg: "path: " + err.Error()}
	}
	switch {
	case p == "":
		return contract.DocsDir + "/", nil
	case !strings.HasPrefix(p, contract.DocsDir+"/"):
		return "", &toolError{msg: fmt.Sprintf("path %q: gesucht wird nur unter %s/; erwartet %s oder ein Verzeichnis "+
			"darunter", path, contract.DocsDir, contract.DocsDir)}
	}
	return p, nil
}
