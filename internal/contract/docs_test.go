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
