package docsindex

import (
	"flag"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kephalaion/kephalaion/internal/contract"
)

var update = flag.Bool("update", false, "die erwarteten Dateien unter testdata/*/want neu schreiben")

// at ist der Zeitpunkt neuer Mahnungen in den Tests.
var at = time.Date(2026, 11, 10, 16, 53, 43, 500, time.UTC)

// readTree liest die Dateien unter dir als Dokumente mit dem Urheber by.
func readTree(t *testing.T, dir, by string) []Doc {
	t.Helper()
	var out []Doc
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		out = append(out, Doc{Name: filepath.ToSlash(rel), Content: string(b), UpdatedBy: by})
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return out
}

// Golden-Tests: testdata/<fall>/in sind Dokumente von Usern, system die des
// Systems (bestehende Mahnungen und index.md), want der Soll-Zustand. Mit
// -update schreibt der Test want neu.
func TestBuildGolden(t *testing.T) {
	cases, err := os.ReadDir("testdata")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		t.Run(c.Name(), func(t *testing.T) {
			base := filepath.Join("testdata", c.Name())
			docs := append(readTree(t, filepath.Join(base, "in"), "kamran"),
				readTree(t, filepath.Join(base, "system"), contract.SystemAuthor)...)
			got := Build(docs, at)
			wantDir := filepath.Join(base, "want")
			if *update {
				if err := os.RemoveAll(wantDir); err != nil {
					t.Fatal(err)
				}
				for _, f := range got {
					p := filepath.Join(wantDir, filepath.FromSlash(f.Name))
					if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(p, []byte(f.Content), 0o644); err != nil {
						t.Fatal(err)
					}
				}
			}
			want := readTree(t, wantDir, "")
			gotMap := map[string]string{}
			for _, f := range got {
				gotMap[f.Name] = f.Content
			}
			if len(want) != len(got) {
				var names []string
				for _, f := range got {
					names = append(names, f.Name)
				}
				t.Errorf("%d Dateien, erwartet %d: %v", len(got), len(want), names)
			}
			for _, w := range want {
				if g, ok := gotMap[w.Name]; !ok {
					t.Errorf("%s fehlt", w.Name)
				} else if g != w.Content {
					t.Errorf("%s:\n%s\nerwartet:\n%s", w.Name, g, w.Content)
				}
			}
			// Ein zweiter Neubau über dem Ergebnis ändert nichts, auch nicht
			// den Zeitpunkt der Mahnungen.
			var again []Doc
			for _, d := range readTree(t, filepath.Join(base, "in"), "kamran") {
				if !contract.IsGenerated(d.Name) {
					again = append(again, d)
				}
			}
			for _, f := range got {
				again = append(again, Doc{Name: f.Name, Content: f.Content, UpdatedBy: contract.SystemAuthor})
			}
			if second := Build(again, at.Add(time.Hour)); !reflect.DeepEqual(second, got) {
				t.Errorf("zweiter Neubau weicht ab")
			}
		})
	}
}

// Die Liste der festen Verzeichnisse: manual und generated, streng gelesen.
func TestLayout(t *testing.T) {
	l := Layout()
	if len(l) != 2 || l[0].Name != "generated" || l[1].Name != "manual" {
		t.Errorf("Layout: %+v", l)
	}
	for _, bad := range []string{
		"x:\n  title: X\n  description: D\n  extra: 1\n",
		"a/b:\n  title: X\n  description: D\n",
		"index.md:\n  title: X\n  description: D\n",
		"x:\n  title: X\n",
	} {
		if _, err := parseLayout([]byte(bad)); err == nil {
			t.Errorf("angenommen: %q", bad)
		}
	}
}

// Die Überschrift außerhalb von Codeblöcken, hinter dem Frontmatter.
func TestFirstHeading(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"# A\n", "A"},
		{"---\ntitle: x\n---\n# B #\n", "B"},
		{"```\n# C\n```\n# D\n", "D"},
		{"~~~~\n# C\n~~~\n# E\n~~~~\n# F\n", "F"},
		{"    # code\n## G\n# H\n", "H"},
		{"#kein\n", ""},
		{"", ""},
	} {
		if got := firstHeading(c.in); got != c.want {
			t.Errorf("firstHeading(%q) = %q, erwartet %q", c.in, got, c.want)
		}
	}
}

// Ein Name mit Leerzeichen oder Klammern steht in <…>; ein Titel mit
// eckigen Klammern ist maskiert.
func TestLink(t *testing.T) {
	if got := link("a b (x)/"); got != "<a b (x)/>" {
		t.Errorf("link: %s", got)
	}
	if got := link("x<y>.md"); got != `<x\<y\>.md>` {
		t.Errorf("link: %s", got)
	}
	if got := escapeText(`a [b] \c`); got != `a \[b\] \\c` {
		t.Errorf("escapeText: %s", got)
	}
	if !strings.Contains(readme(), "`manual/` — Documentation written by hand.") {
		t.Error("README ohne feste Verzeichnisse")
	}
}
