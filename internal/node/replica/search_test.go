package replica

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestMatchQuery(t *testing.T) {
	for in, want := range map[string]string{
		"How do I install it?":     `"How" OR "do" OR "I" OR "install" OR "it"`,
		`NEAR("a" b) OR -x* col:y`: `"NEAR" OR "a" OR "b" OR "OR" OR "x" OR "col" OR "y"`,
		"Größe der index.md":       `"Größe" OR "der" OR "index" OR "md"`,
		" ?! ":                     "",
	} {
		if got := MatchQuery(in); got != want {
			t.Errorf("MatchQuery(%q) = %s", in, got)
		}
	}
	if got := MatchQuery(strings.Repeat("w ", 100)); strings.Count(got, `"w"`) != MaxQueryWords {
		t.Errorf("Wörter nicht begrenzt: %d", strings.Count(got, `"w"`))
	}
}

// Rang nach bm25 mit Gewichten, höchstens drei Abschnitte je Dokument,
// path grenzt ein, Felder des Dokuments an jedem Treffer.
func TestSearch(t *testing.T) {
	many := "---\ntitle: Many\n---\n"
	for _, h := range []string{"One", "Two", "Three", "Four", "Five"} {
		many += "# " + h + "\nreplica replica sync text\n"
	}
	r := readReplica(t, 10,
		row("d1", "w", "docs/manual/sync.md", 1, 1, "---\ntitle: Sync\ndescription: How the replica syncs.\n"+
			"questions:\n  - How does the node sync its replica?\n---\n# Intro\nThe node pulls pages.\n## Pages\nA page ends at a revision.\n"),
		row("d2", "w", "docs/generated/many.md", 2, 1, many),
		row("d3", "w", "docs/other.md", 3, 1, "# Other\nNothing relevant here, only a replica mention.\n"),
		row("d4", "v", "docs/x.md", 4, 1, "# X\nreplica sync\n"),
	)
	ctx := context.Background()
	hits, err := r.Search(ctx, "w", "docs/", MatchQuery("how does the replica sync?"), 10)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	perDoc := map[string]int{}
	for _, h := range hits {
		got = append(got, h.Name+"#"+h.Anchor)
		perDoc[h.Name]++
	}
	if len(hits) == 0 || hits[0].Name != "docs/manual/sync.md" {
		t.Fatalf("erster Treffer: %v", got)
	}
	if perDoc["docs/generated/many.md"] != MaxHitsPerDocument || perDoc["docs/other.md"] != 1 {
		t.Errorf("je Dokument: %v", got)
	}
	h := hits[0]
	if h.Title != "Sync" || h.Description != "How the replica syncs." || h.Excerpt == "" {
		t.Errorf("Felder: %+v", h)
	}
	for _, x := range hits {
		if x.Name == "docs/manual/sync.md" && x.Anchor == "pages" && !reflect.DeepEqual(x.Headings, []string{"Intro", "Pages"}) {
			t.Errorf("Pfad: %+v", x)
		}
	}
	// path und limit.
	hits, err = r.Search(ctx, "w", "docs/generated/", MatchQuery("replica"), 2)
	if err != nil || len(hits) != 2 || hits[0].Name != "docs/generated/many.md" {
		t.Errorf("path/limit: %+v %v", hits, err)
	}
	if hits, _ := r.Search(ctx, "w", "docs/", MatchQuery("zebra"), 10); len(hits) != 0 {
		t.Errorf("zebra: %+v", hits)
	}
	if hits, _ := r.Search(ctx, "w", "docs/", "", 10); len(hits) != 0 {
		t.Errorf("leer: %+v", hits)
	}
}
