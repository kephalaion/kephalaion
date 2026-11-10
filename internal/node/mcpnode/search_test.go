package mcpnode

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/kephalaion/kephalaion/internal/contract"
)

const guide = "---\ntitle: Node guide\ndescription: How a node syncs.\nquestions:\n  - How does the node sync?\n---\n" +
	"# Setup\nInstall the binary.\n## Sync\nThe node pulls pages from the hub.\n## Sync\nSecond sync section.\n"

// search über die lesbaren Collections: Treffer je Abschnitt, getrennt je
// Collection, Rang ab 1; nur unter docs/, ohne index.md und Mahnungen;
// path, limit, ohne Treffer der Hinweis. Ein Treffer geht an read mit
// section.
func TestSearch(t *testing.T) {
	e := newDocEnv(t)
	keph, team := e.hubs["keph"], e.hubs["team"]
	keph.put("wissen", "docs/manual/guide.md", guide)
	keph.put("wissen", "docs/other.md", "# Other\nNothing about pages.\n")
	keph.put("wissen", "notes/sync.md", "# Sync\nnode sync outside docs\n")
	keph.put("privat", "docs/p.md", "# Private\nnode sync private\n")
	id := keph.put("wissen", "docs/index.md", "# Documents\nnode sync index\n")
	keph.change(id, func(r *contract.Row) { r.CreatedBy, r.UpdatedBy = contract.SystemAuthor, contract.SystemAuthor })
	id = keph.put("wissen", "docs/m/README.md", "# m\nnode sync notice\n")
	keph.change(id, func(r *contract.Row) { r.CreatedBy, r.UpdatedBy = contract.SystemAuthor, contract.SystemAuthor })
	team.put("notizen", "docs/t.md", "# Team\nthe node syncs\n")
	e.sync(t)

	var out SearchOutput
	if _, errText := e.call(t, e.anna(), "search", SearchInput{Query: "How does the node sync?"}, &out); errText != "" {
		t.Fatal(errText)
	}
	var got []string
	for _, r := range out.Results {
		for _, h := range r.Hits {
			got = append(got, r.Address+" "+h.Name+"#"+h.Section.Anchor)
			if h.Rank < 1 {
				t.Errorf("Rang %d", h.Rank)
			}
		}
	}
	for _, want := range []string{"keph:wissen docs/manual/guide.md#sync", "keph:privat docs/p.md#private",
		"team:notizen docs/t.md#team"} {
		if !strings.Contains(strings.Join(got, "\n"), want) {
			t.Errorf("fehlt %s in\n%s", want, strings.Join(got, "\n"))
		}
	}
	for _, not := range []string{"notes/sync.md", "docs/index.md", "docs/m/README.md"} {
		if strings.Contains(strings.Join(got, "\n"), not) {
			t.Errorf("%s ist ein Treffer", not)
		}
	}
	if out.Hint != "" || out.Results[0].Address != "keph:privat" {
		t.Errorf("Reihenfolge oder Hinweis: %+v", out)
	}
	var first SearchHit
	for _, r := range out.Results {
		if r.Address == "keph:wissen" {
			first = r.Hits[0]
			if r.Hits[0].Rank != 1 {
				t.Errorf("Rang: %+v", r.Hits)
			}
		}
	}
	if first.Name != "docs/manual/guide.md" || first.Title != "Node guide" || first.Description != "How a node syncs." ||
		!reflect.DeepEqual(first.Section.Headings, []string{"Setup", "Sync"}) || first.Excerpt == "" {
		t.Errorf("erster Treffer: %+v", first)
	}

	// otto liest nur keph:wissen; path und limit.
	out = SearchOutput{}
	e.call(t, e.otto(), "search", SearchInput{Query: "sync", Path: "docs/manual", Limit: 1}, &out)
	if len(out.Results) != 1 || out.Results[0].Address != "keph:wissen" || len(out.Results[0].Hits) != 1 {
		t.Errorf("otto, path, limit: %+v", out)
	}
	// Ohne Treffer: Hinweis auf docs/index.md.
	out = SearchOutput{}
	e.call(t, e.otto(), "search", SearchInput{Query: "zebra", Collection: "keph:wissen"}, &out)
	if len(out.Results) != 0 || !strings.Contains(out.Hint, "docs/index.md") {
		t.Errorf("ohne Treffer: %+v", out)
	}
	for _, c := range []struct {
		in   SearchInput
		want string
	}{
		{SearchInput{Query: " ?! "}, "query fehlt"},
		{SearchInput{Query: "x", Path: "notes"}, "nur unter docs/"},
		{SearchInput{Query: "x", Collection: "keph:privat"}, "nicht lesbar"},
		{SearchInput{Query: "x", Limit: -1}, "limit"},
	} {
		if _, errText := e.call(t, e.otto(), "search", c.in, nil); !strings.Contains(errText, c.want) {
			t.Errorf("%+v: %q", c.in, errText)
		}
	}

	// Der Treffer an read mit section: nur dieser Abschnitt.
	var rd ReadOutput
	if _, errText := e.call(t, e.otto(), "read", ReadInput{Collection: "keph:wissen", Name: first.Name,
		Section: first.Section.Anchor}, &rd); errText != "" {
		t.Fatal(errText)
	}
	if rd.Content == nil || !strings.HasPrefix(*rd.Content, "## Sync\n") || rd.Section == nil ||
		rd.Section.Anchor != first.Section.Anchor || rd.Section.Heading != "Sync" {
		t.Errorf("read section: %+v", rd)
	}
	for anchor, want := range map[string]string{"sync": "## Sync\nThe node pulls pages from the hub.\n",
		"sync-1": "## Sync\nSecond sync section.\n", "setup": "# Setup\nInstall the binary.\n"} {
		rd = ReadOutput{}
		if _, errText := e.call(t, e.otto(), "read", ReadInput{Collection: "keph:wissen", Name: first.Name,
			Section: anchor}, &rd); errText != "" || rd.Content == nil || *rd.Content != want {
			t.Errorf("Anker %s: %q %+v", anchor, errText, rd)
		}
	}
}

// read mit einem Anker, den es nicht gibt: isError mit error.code
// section_not_found; Verzeichnis und content: false sind ungültig, none
// bleibt none.
func TestReadSectionNotFound(t *testing.T) {
	e := newDocEnv(t)
	e.hubs["keph"].put("wissen", "docs/a.md", "# A\ntext\n")
	e.sync(t)
	res, errText := e.call(t, e.otto(), "read", ReadInput{Collection: "keph:wissen", Name: "docs/a.md", Section: "fehlt"}, nil)
	var out ReadOutput
	b, _ := json.Marshal(res.StructuredContent)
	_ = json.Unmarshal(b, &out)
	if !res.IsError || out.Error == nil || out.Error.Code != CodeSectionNotFound || !strings.Contains(errText, "fehlt") ||
		out.Name != "docs/a.md" || out.Kind != KindDocument {
		t.Errorf("section_not_found: %+v, %q", out, errText)
	}
	if _, errText := e.call(t, e.otto(), "read", ReadInput{Collection: "keph:wissen", Name: "docs", Section: "a"},
		nil); !strings.Contains(errText, "Verzeichnis") {
		t.Errorf("Verzeichnis: %q", errText)
	}
	no := false
	if _, errText := e.call(t, e.otto(), "read", ReadInput{Collection: "keph:wissen", Name: "docs/a.md", Section: "a",
		Content: &no}, nil); !strings.Contains(errText, "content: false") {
		t.Errorf("content false: %q", errText)
	}
	var none ReadOutput
	if _, errText := e.call(t, e.otto(), "read", ReadInput{Collection: "keph:wissen", Name: "docs/b.md", Section: "a"},
		&none); errText != "" || none.Kind != KindNone {
		t.Errorf("none: %+v %q", none, errText)
	}
}

// changes mit frontmatter: je geändertem .md-Dokument sein Frontmatter,
// nicht bei Löschmarken und anderen Dateien; der cursor bleibt gültig.
func TestChangesFrontmatter(t *testing.T) {
	e := newDocEnv(t)
	keph := e.hubs["keph"]
	var start ChangesOutput
	e.call(t, e.otto(), "changes", ChangesInput{Collection: "keph:wissen"}, &start)
	keph.put("wissen", "docs/a.md", "---\ntitle: A\ndescription: About a.\n---\n")
	keph.put("wissen", "docs/kaputt.md", "---\ntitle: [\n---\n")
	keph.put("wissen", "docs/x.txt", "---\ntitle: X\n---\n")
	gone := keph.put("wissen", "docs/weg.md", "---\ntitle: W\n---\n")
	keph.rm(gone)
	e.sync(t)
	var out ChangesOutput
	if _, errText := e.call(t, e.otto(), "changes", ChangesInput{Collection: "keph:wissen", Cursor: start.Cursor,
		Frontmatter: true}, &out); errText != "" {
		t.Fatal(errText)
	}
	byName := map[string]Change{}
	for _, c := range out.Changes {
		byName[c.Name] = c
	}
	if fm, _ := byName["docs/a.md"].Frontmatter.(map[string]any); fm["title"] != "A" || fm["description"] != "About a." {
		t.Errorf("a.md: %+v", byName["docs/a.md"])
	}
	if byName["docs/kaputt.md"].FrontmatterError == "" || byName["docs/x.txt"].Frontmatter != nil ||
		byName["docs/weg.md"].Frontmatter != nil || !byName["docs/weg.md"].Deleted {
		t.Errorf("Sonderfälle: %+v", out.Changes)
	}
	// Ohne frontmatter wie bisher; derselbe cursor gilt.
	var plain ChangesOutput
	if _, errText := e.call(t, e.otto(), "changes", ChangesInput{Collection: "keph:wissen", Cursor: start.Cursor},
		&plain); errText != "" || len(plain.Changes) != len(out.Changes) || plain.Changes[0].Frontmatter != nil {
		t.Errorf("ohne frontmatter: %+v %q", plain, errText)
	}
}
