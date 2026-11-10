package sections

import (
	"reflect"
	"strings"
	"testing"
)

const doc = `---
title: Guide
description: How it works.
questions:
  - How do I start?
  - Where is the config?
hints: Read setup first.
---
Intro text.

# Setup

Install it.

` + "```sh\n# not a heading\n```" + `

## Config file

Edit ~/.config.

## Config file

Again.

#Not a heading
# Usage #

### Deep  ` + "`code`" + `
~~~~
# inside
~~~
still inside
~~~~
end
`

// Zerlegung: Text vor der ersten Überschrift, Codeblöcke, Pfad, eindeutige
// Anker, Inhalt mit und ohne Überschrift.
func TestSplit(t *testing.T) {
	got := Split(doc)
	type short struct {
		heading  string
		level    int
		headings string
		anchor   string
	}
	var s []short
	for _, x := range got {
		s = append(s, short{x.Heading, x.Level, strings.Join(x.Headings, " > "), x.Anchor})
	}
	want := []short{
		{"", 0, "", ""},
		{"Setup", 1, "Setup", "setup"},
		{"Config file", 2, "Setup > Config file", "config-file"},
		{"Config file", 2, "Setup > Config file", "config-file-1"},
		{"Usage", 1, "Usage", "usage"},
		{"Deep `code`", 3, "Usage > Deep `code`", "deep-code"},
	}
	if !reflect.DeepEqual(s, want) {
		t.Errorf("Abschnitte:\n%v\nerwartet\n%v", s, want)
	}
	if got[0].Body != "Intro text.\n\n" {
		t.Errorf("Vorspann: %q", got[0].Body)
	}
	if !strings.HasPrefix(got[1].Content, "# Setup\n") || !strings.Contains(got[1].Body, "# not a heading") ||
		strings.Contains(got[1].Body, "# Setup") {
		t.Errorf("Setup: %q / %q", got[1].Content, got[1].Body)
	}
	if !strings.Contains(got[5].Body, "# inside") || !strings.Contains(got[5].Body, "end") {
		t.Errorf("Deep: %q", got[5].Body)
	}
	// Die Teile ergeben zusammen den Inhalt hinter dem Frontmatter.
	var all strings.Builder
	for _, x := range got {
		all.WriteString(x.Content)
	}
	if all.String() != doc[strings.Index(doc, "Intro"):] {
		t.Errorf("Zusammengesetzt:\n%s", all.String())
	}
}

func TestParse(t *testing.T) {
	d := Parse("docs/guide.md", doc)
	if d.Title != "Guide" || d.Description != "How it works." ||
		!reflect.DeepEqual(d.Questions, []string{"How do I start?", "Where is the config?"}) ||
		!reflect.DeepEqual(d.Hints, []string{"Read setup first."}) || len(d.Sections) != 6 {
		t.Errorf("%+v", d)
	}
	if d := Parse("docs/x.md", "# First\n## Second\n"); d.Title != "First" {
		t.Errorf("Titel aus Überschrift: %q", d.Title)
	}
	if d := Parse("docs/x.md", "## Second\ntext\n"); d.Title != "x.md" || len(d.Sections) != 1 {
		t.Errorf("Titel aus Name: %+v", d)
	}
	if d := Parse("docs/x.md", "---\ntitle: [\n---\n# T\n"); d.Title != "T" {
		t.Errorf("kaputtes Frontmatter: %+v", d)
	}
	if got := Split("\n\n"); len(got) != 0 {
		t.Errorf("leer: %+v", got)
	}
}

func TestAnchor(t *testing.T) {
	for in, want := range map[string]string{
		"Hello World":         "hello-world",
		"Über die Größe":      "über-die-größe",
		"`read` mit section!": "read-mit-section",
		"a_b-c":               "a_b-c",
		"???":                 "section",
	} {
		if got := Anchor(in); got != want {
			t.Errorf("Anchor(%q) = %q, erwartet %q", in, got, want)
		}
	}
	used := map[string]bool{"a-1": true}
	if a, b := unique("a", used), unique("a", used); a != "a" || b != "a-2" {
		t.Errorf("unique: %s %s", a, b)
	}
	if s, ok := Find(doc, "config-file-1"); !ok || !strings.Contains(s.Content, "Again.") {
		t.Errorf("Find: %+v %v", s, ok)
	}
	if _, ok := Find(doc, "fehlt"); ok {
		t.Error("Find fehlt")
	}
	if _, ok := Find(doc, ""); ok {
		t.Error("Find leer")
	}
}
