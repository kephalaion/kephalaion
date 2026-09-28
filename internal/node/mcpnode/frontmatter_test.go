package mcpnode

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fillSkills legt Dokumente mit und ohne Frontmatter an: in keph:wissen
// Skills, Notizen und READMEs, in keph:privat eine README.md mit kaputtem
// Frontmatter, in team:notizen eine mit gutem. Liefert die ids nach Name in
// wissen.
func (e *docEnv) fillSkills(t *testing.T) map[string]string {
	t.Helper()
	f := e.hubs["keph"]
	ids := map[string]string{}
	for name, content := range map[string]string{
		"README.md":          "---\ntitle: Wissen\ntags: [a, b]\n---\n# Wissen\n",
		"skills/a/SKILL.md":  "---\nname: a\ndescription: Skill A\n---\n# A\n",
		"skills/b/SKILL.md":  "---\nname: b\ndescription: Skill B\nversion: 2\n---\n",
		"skills/b/README.md": "# B ohne Frontmatter\n",
		"skills/c/SKILL.md":  "---\nname: [c\n---\n",
		"notes/Upper.MD":     "---\nx: 1\n---\n",
		"notes/plain.md":     "# ohne\n",
		"notes/empty.md":     "---\n---\n",
		"notes/data.txt":     "---\nx: 1\n---\n",
		"notes/README.md":    "---\ndesc: Notizen\n---\n",
		"broken/README.md":   "---\nbad: [\n---\n",
		"zdir/e.md":          "text",
	} {
		ids[name] = f.put("wissen", name, content)
	}
	f.put("privat", "README.md", "---\n- list\n---\n")
	e.hubs["team"].put("notizen", "README.md", "---\nteam: true\n---\n")
	e.sync(t)
	return ids
}

// fmOf liefert je Name (wie names) das Frontmatter als JSON-Text, "" ohne
// Feld, sonst "!" und der Fehler.
func fmOf(entries []ListEntry) map[string]string {
	out := map[string]string{}
	for i, n := range names(entries) {
		out[n] = fmText(entries[i].Frontmatter, entries[i].FrontmatterError)
	}
	return out
}

func fmText(fm any, errText string) string {
	if errText != "" {
		return "!" + errText
	}
	if fm == nil {
		return ""
	}
	b, _ := json.Marshal(fm)
	return string(b)
}

// rawOf ist die strukturierte Antwort als Text.
func rawOf(res *mcp.CallToolResult) string {
	b, _ := json.Marshal(res.StructuredContent)
	return string(b)
}

func TestListFrontmatter(t *testing.T) {
	e := newDocEnv(t)
	e.fillSkills(t)
	// Ohne den Parameter wie bisher: kein Feld, nirgends.
	for _, in := range []ListInput{{}, {Collection: "keph:wissen"}, {Collection: "keph:wissen", Recursive: true}} {
		var out ListOutput
		res, errText := e.call(t, e.anna(), "list", in, &out)
		if errText != "" || strings.Contains(rawOf(res), "frontmatter") {
			t.Errorf("%+v ohne Parameter: %s, %s", in, errText, rawOf(res))
		}
	}
	cases := []struct {
		in   ListInput
		want map[string]string
	}{
		// Verzeichnisse über ihre README.md: kaputt → Fehler, ohne README →
		// kein Feld; die README.md selbst als Dokument.
		{ListInput{Collection: "keph:wissen"}, map[string]string{
			"broken/": "!YAML ungültig: line 1: did not find expected node content", "notes/": `{"desc":"Notizen"}`,
			"skills/": "", "zdir/": "", "README.md": `{"tags":["a","b"],"title":"Wissen"}`}},
		{ListInput{Collection: "keph:wissen", Path: "skills"}, map[string]string{
			"skills/a/": "", "skills/b/": "", "skills/c/": ""}},
		// .MD zählt, .txt nicht; ein leerer Block ist {}.
		{ListInput{Collection: "keph:wissen", Path: "notes"}, map[string]string{
			"notes/README.md": `{"desc":"Notizen"}`, "notes/Upper.MD": `{"x":1}`, "notes/data.txt": "",
			"notes/empty.md": `{}`, "notes/plain.md": ""}},
		// Skills in einem Aufruf: rekursiv mit Maske, ohne Verzeichnisse.
		{ListInput{Collection: "keph:wissen", Recursive: true, Mask: "SKILL.md"}, map[string]string{
			"skills/a/SKILL.md": `{"description":"Skill A","name":"a"}`,
			"skills/b/SKILL.md": `{"description":"Skill B","name":"b","version":2}`,
			"skills/c/SKILL.md": "!YAML ungültig: line 1: did not find expected ',' or ']'"}},
		// Collections über README.md auf oberster Ebene, über alle Hubs und
		// für einen Hub.
		{ListInput{}, map[string]string{"keph:privat": "!oben steht kein Objekt, sondern eine Liste",
			"keph:wissen": `{"tags":["a","b"],"title":"Wissen"}`, "team:notizen": `{"team":true}`}},
		{ListInput{Collection: "keph:"}, map[string]string{"keph:privat": "!oben steht kein Objekt, sondern eine Liste",
			"keph:wissen": `{"tags":["a","b"],"title":"Wissen"}`}},
	}
	for _, c := range cases {
		c.in.Frontmatter = true
		out, errText := e.list(t, e.anna(), c.in)
		if errText != "" || !reflect.DeepEqual(fmOf(out.Entries), c.want) {
			t.Errorf("%+v: %s\n got %v\nwant %v", c.in, errText, fmOf(out.Entries), c.want)
		}
	}
	// {} steht wirklich in der Antwort, ein fehlendes Feld nicht.
	var out ListOutput
	res, _ := e.call(t, e.anna(), "list", ListInput{Collection: "keph:wissen", Path: "notes", Frontmatter: true}, &out)
	if raw := rawOf(res); !strings.Contains(raw, `"frontmatter":{},"id":`) || strings.Count(raw, "frontmatter") != 3 {
		t.Errorf("roh: %s", raw)
	}
	// otto sieht nur keph:wissen.
	if out, _ := e.list(t, e.otto(), ListInput{Frontmatter: true}); !reflect.DeepEqual(fmOf(out.Entries),
		map[string]string{"keph:wissen": `{"tags":["a","b"],"title":"Wissen"}`}) {
		t.Errorf("otto: %v", fmOf(out.Entries))
	}
	// Blättern: frontmatter gehört nicht zum Cursor, der Parameter darf
	// zwischen den Seiten wechseln; jede Seite trägt es nur, wenn verlangt.
	in := ListInput{Collection: "keph:wissen", Recursive: true, Mask: "SKILL.md", Limit: 1, Frontmatter: true}
	var got []string
	for i, fm := range []bool{true, false, true} {
		in.Frontmatter = fm
		out, errText := e.list(t, e.anna(), in)
		if errText != "" || len(out.Entries) != 1 || out.More != (i < 2) {
			t.Fatalf("Seite %d: %+v, %s", i, out, errText)
		}
		got = append(got, fmText(out.Entries[0].Frontmatter, out.Entries[0].FrontmatterError))
		in.Cursor = out.Cursor
	}
	if want := []string{`{"description":"Skill A","name":"a"}`, "", "!YAML ungültig: line 1: did not find expected ',' or ']'"}; !reflect.DeepEqual(got, want) {
		t.Errorf("geblättert: %v", got)
	}
	// Die Meldung nennt die Ausnahmen.
	first, _ := e.list(t, e.anna(), ListInput{Collection: "keph:wissen", Limit: 1})
	if _, errText := e.list(t, e.anna(), ListInput{Collection: "keph:wissen", Limit: 1, Cursor: first.Cursor, Recursive: true}); !strings.Contains(errText, "außer limit und frontmatter") {
		t.Errorf("cursor: %s", errText)
	}
	// Eine unlesbare Replica im zweiten Gang betrifft nur ihren Hub.
	e.breakReplica(t, "team")
	out, errText := e.list(t, e.anna(), ListInput{Frontmatter: true})
	if errText != "" || !reflect.DeepEqual(out.UnreadableHubs, []string{"team"}) || len(out.Entries) != 2 {
		t.Errorf("team kaputt: %+v, %s", out, errText)
	}
}

func TestReadFrontmatter(t *testing.T) {
	e := newDocEnv(t)
	ids := e.fillSkills(t)
	skillA := "---\nname: a\ndescription: Skill A\n---\n# A\n"
	// Ohne den Parameter wie bisher.
	for _, in := range []ReadInput{{Collection: "keph:wissen", Name: "skills/a/SKILL.md"}, {Collection: "keph:wissen", Name: "notes"},
		{Collection: "keph:wissen"}, {ID: ids["skills/a/SKILL.md"], Collection: "keph:"}} {
		_, res, errText := e.read(t, e.anna(), in)
		if errText != "" || strings.Contains(rawOf(res), "frontmatter") {
			t.Errorf("%+v ohne Parameter: %s, %s", in, errText, rawOf(res))
		}
	}
	cases := []struct {
		in   ReadInput
		kind string
		fm   string
		text string
	}{
		// Dokument per Name und per id, mit und ohne Inhalt: der Inhalt
		// bleibt der volle Text, einschließlich Frontmatter.
		{ReadInput{Collection: "keph:wissen", Name: "skills/a/SKILL.md"}, KindDocument, `{"description":"Skill A","name":"a"}`, skillA},
		{ReadInput{Collection: "keph:", ID: ids["skills/a/SKILL.md"]}, KindDocument, `{"description":"Skill A","name":"a"}`, skillA},
		{ReadInput{Collection: "keph:wissen", Name: "skills/a/SKILL.md", Content: no()}, KindDocument, `{"description":"Skill A","name":"a"}`, ""},
		{ReadInput{Collection: "keph:", ID: ids["skills/a/SKILL.md"], Content: no()}, KindDocument, `{"description":"Skill A","name":"a"}`, ""},
		{ReadInput{Collection: "keph:wissen", Name: "skills/c/SKILL.md"}, KindDocument, "!YAML ungültig: line 1: did not find expected ',' or ']'", "---\nname: [c\n---\n"},
		{ReadInput{Collection: "keph:wissen", Name: "skills/c/SKILL.md", Content: no()}, KindDocument, "!YAML ungültig: line 1: did not find expected ',' or ']'", ""},
		{ReadInput{Collection: "keph:wissen", Name: "notes/Upper.MD"}, KindDocument, `{"x":1}`, "---\nx: 1\n---\n"},
		{ReadInput{Collection: "keph:wissen", Name: "notes/data.txt"}, KindDocument, "", "---\nx: 1\n---\n"},
		{ReadInput{Collection: "keph:wissen", Name: "notes/plain.md"}, KindDocument, "", "# ohne\n"},
		{ReadInput{Collection: "keph:wissen", Name: "notes/empty.md"}, KindDocument, `{}`, "---\n---\n"},
		// Verzeichnis: die README.md darin; ohne oder ohne Frontmatter kein Feld.
		{ReadInput{Collection: "keph:wissen", Name: "notes"}, KindDirectory, `{"desc":"Notizen"}`, ""},
		{ReadInput{Collection: "keph:wissen", Name: "notes/"}, KindDirectory, `{"desc":"Notizen"}`, ""},
		{ReadInput{Collection: "keph:wissen", Name: "broken"}, KindDirectory, "!YAML ungültig: line 1: did not find expected node content", ""},
		{ReadInput{Collection: "keph:wissen", Name: "skills"}, KindDirectory, "", ""},
		{ReadInput{Collection: "keph:wissen", Name: "skills/b"}, KindDirectory, "", ""},
		// Wurzel der Collection: README.md oben; Wurzel des Hubs: keines.
		{ReadInput{Collection: "keph:wissen"}, KindDirectory, `{"tags":["a","b"],"title":"Wissen"}`, ""},
		{ReadInput{Collection: "keph:privat", Name: ""}, KindDirectory, "!oben steht kein Objekt, sondern eine Liste", ""},
		{ReadInput{Collection: "team:notizen"}, KindDirectory, `{"team":true}`, ""},
		{ReadInput{Collection: "keph:"}, KindDirectory, "", ""},
		{ReadInput{Collection: "keph:wissen", Name: "fehlt.md"}, KindNone, "", ""},
	}
	for _, c := range cases {
		c.in.Frontmatter = true
		out, res, errText := e.read(t, e.anna(), c.in)
		if errText != "" || out.Kind != c.kind || fmText(out.Frontmatter, out.FrontmatterError) != c.fm {
			t.Errorf("%+v: %+v, %s", c.in, out, errText)
			continue
		}
		text := textOf(res)
		switch {
		case c.in.Content != nil && !*c.in.Content, out.Kind != KindDocument:
			// Ohne Inhalt steht die Struktur im Text, samt Frontmatter.
			if (c.fm != "") != strings.Contains(text, "frontmatter") || (c.fm != "" && !strings.Contains(rawOf(res), "frontmatter")) {
				t.Errorf("%+v: Text %q", c.in, text)
			}
		case text != c.text:
			t.Errorf("%+v: Inhalt %q", c.in, text)
		}
	}
	// {} steht in der Antwort.
	_, res, _ := e.read(t, e.anna(), ReadInput{Collection: "keph:wissen", Name: "notes/empty.md", Frontmatter: true, Content: no()})
	if raw := rawOf(res); !strings.Contains(raw, `"frontmatter":{}`) {
		t.Errorf("roh: %s", raw)
	}
}
