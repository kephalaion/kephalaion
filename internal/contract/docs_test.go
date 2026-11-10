package contract

import "testing"

// Erzeugte Dateien und Mahnungen unter docs/: allein docs/ auf oberster
// Ebene, Namen genau so geschrieben; die Mahnung erkannt an Name und
// Urheber.
func TestDocsNames(t *testing.T) {
	for _, c := range []struct {
		name              string
		generated, notice bool
	}{
		{"docs/README.md", true, false},
		{"docs/index.md", true, false},
		{"docs/manual/index.md", true, false},
		{"docs/a/b/index.md", true, false},
		{"docs/manual/README.md", false, true},
		{"docs/a/b/README.md", false, true},
		{"docs", false, false},
		{"docs/x.md", false, false},
		{"docs/a/Index.md", false, false},
		{"docs/a/readme.md", false, false},
		{"docs/a/xindex.md", false, false},
		{"docs/a/xREADME.md", false, false},
		{"index.md", false, false},
		{"README.md", false, false},
		{"a/docs/index.md", false, false},
		{"a/docs/README.md", false, false},
		{"docs2/index.md", false, false},
		{"docs2/a/README.md", false, false},
	} {
		if got := IsGenerated(c.name); got != c.generated {
			t.Errorf("IsGenerated(%q) = %v", c.name, got)
		}
		if got := IsNoticeName(c.name); got != c.notice {
			t.Errorf("IsNoticeName(%q) = %v", c.name, got)
		}
		if got := IsNotice(c.name, SystemAuthor); got != c.notice {
			t.Errorf("IsNotice(%q, system) = %v", c.name, got)
		}
		if IsNotice(c.name, "kamran") {
			t.Errorf("IsNotice(%q, kamran) gilt", c.name)
		}
		if got := IsSystemDoc(c.name, SystemAuthor); got != (c.generated || c.notice) {
			t.Errorf("IsSystemDoc(%q, system) = %v", c.name, got)
		}
	}
	if !InDocs("docs/x") || InDocs("docs") || InDocs("docsx/y") {
		t.Error("InDocs")
	}
}

// Ein Name unter docs/, der index.md oder README.md als Verzeichnis nutzt,
// belegt die Stelle einer Datei des Systems (Review-Befund zu Task 033).
func TestReservedDirOf(t *testing.T) {
	for name, want := range map[string]string{
		"docs/index.md/x.md":        "docs/index.md",
		"docs/README.md/x.md":       "docs/README.md",
		"docs/a/index.md/b.md":      "docs/a/index.md",
		"docs/a/README.md/b/c.md":   "docs/a/README.md",
		"docs/a/index.md/README.md": "docs/a/index.md",
		"docs/a/README.md/index.md": "docs/a/README.md",
		"docs/index.md":             "",
		"docs/README.md":            "",
		"docs/a/index.md":           "",
		"docs/a/README.md":          "",
		"docs/a/b.md":               "",
		"docs/a/Index.md/b.md":      "",
		"docs/a/readme.md/b.md":     "",
		"index.md/x.md":             "",
		"a/docs/index.md/x.md":      "",
		"docs2/index.md/x.md":       "",
		"docs":                      "",
	} {
		if got := ReservedDirOf(name); got != want {
			t.Errorf("ReservedDirOf(%q) = %q, erwartet %q", name, got, want)
		}
	}
}

// Erzeugte Dateien schreibt über einen Node niemand, auch nicht mit einem
// Verzeichnis-Scope; eine Mahnung und die Dokumente des Systems in einem
// Verzeichnisvorgang nimmt mit, wer write oder supersede hat.
func TestMayWriteGenerated(t *testing.T) {
	all := Rights{Write: true, Supersede: true, Dirs: []string{"docs"}}
	for _, name := range []string{"docs/README.md", "docs/index.md", "docs/a/index.md"} {
		for _, own := range []bool{true, false} {
			d := all.MayWrite(name, own)
			if d == nil || d.Kind != DenyGenerated || d.Error() == "" {
				t.Errorf("MayWrite(%q, %v) = %v", name, own, d)
			}
		}
		if all.Writable(name) {
			t.Errorf("Writable(%q)", name)
		}
	}
	if !all.WritableUnder("docs") || !all.Writable("docs/a/README.md") {
		t.Error("unter docs/ sonst schreibbar")
	}
	for _, c := range []struct {
		r    Rights
		name string
		ok   bool
	}{
		{Rights{Write: true}, "docs/a/README.md", true},
		{Rights{Supersede: true}, "docs/a/README.md", true},
		{Rights{Dirs: []string{"docs/a"}}, "docs/a/README.md", true},
		{Rights{}, "docs/a/README.md", false},
		{Rights{Write: true}, "docs/a/index.md", true},
		{Rights{Supersede: true}, "docs/index.md", true},
		{Rights{}, "docs/index.md", false},
		{Rights{Dirs: []string{"docs2"}}, "docs/index.md", false},
	} {
		d := c.r.MayTakeOver(c.name)
		if (d == nil) != c.ok {
			t.Errorf("%+v MayTakeOver(%q) = %v", c.r, c.name, d)
		}
		if d != nil && d.Kind != DenyWrite {
			t.Errorf("%+v MayTakeOver(%q): Grund %v", c.r, c.name, d)
		}
	}
}
