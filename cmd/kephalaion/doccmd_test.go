package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	hubstore "github.com/kephalaion/kephalaion/internal/hub/store"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestHubDocFlow(t *testing.T) {
	dir := isolate(t)
	c := "--config=" + setup(t, dir)
	runT(t, "hub", "collection", "add", "team-x", c).want(t, 0)

	runIn(t, "Inhalt\n", "hub", "doc", "put", "team-x", "tasks/001-a.md", c).
		want(t, 0, "Dokument tasks/001-a.md in team-x angelegt (Revision 1")
	runIn(t, "Inhalt\n", "hub", "doc", "put", "team-x", "tasks/001-a.md", c).
		want(t, 0, "unverändert (Revision 1")
	f := filepath.Join(dir, "b.md")
	writeFile(t, f, "---\ntitel: b\n---\nB\n")
	runT(t, "hub", "doc", "put", "team-x", "tasks/done/002-b.md", "--file", f, c).want(t, 0, "angelegt (Revision 2")
	runT(t, "hub", "doc", "put", "team-x", "readme.md", "--file", f, c).want(t, 0, "angelegt (Revision 3")
	runIn(t, "neu", "hub", "doc", "put", "team-x", "readme.md", c).want(t, 0, "ersetzt (Revision 4")

	r := runT(t, "hub", "doc", "get", "team-x", "tasks/done/002-b.md", c)
	r.want(t, 0)
	if r.out != "---\ntitel: b\n---\nB\n" {
		t.Errorf("get = %q", r.out)
	}

	r = runT(t, "hub", "doc", "list", "team-x", c)
	r.want(t, 0, "NAME", "readme.md", "tasks/", "von admin")
	if strings.Index(r.out, "readme.md") > strings.Index(r.out, "tasks/") || strings.Contains(r.out, "001-a.md") {
		t.Errorf("list:\n%s", r.out)
	}
	r = runT(t, "hub", "doc", "list", "team-x", "tasks", c)
	r.want(t, 0, "001-a.md", "done/")
	if strings.Count(r.out, "done/") != 1 || strings.Contains(r.out, "002-b.md") {
		t.Errorf("list tasks:\n%s", r.out)
	}
	runT(t, "hub", "doc", "list", "team-x", "leer", c).want(t, 0, "Keine Dokumente unter leer/ in team-x.")

	runIn(t, "x", "hub", "doc", "put", "team-x", "tasks", c).want(t, 1, "zugleich")
	runIn(t, "x", "hub", "doc", "put", "team-x", "SYSTEM:A:kleist", c).want(t, 1, "dem Hub vorbehalten")
	runIn(t, "x", "hub", "doc", "put", "team-x", "../a", c).want(t, 1, "'..'")
	runIn(t, "a\xffb", "hub", "doc", "put", "team-x", "bin", c).want(t, 1, "kein UTF-8-Text")
	runIn(t, strings.Repeat("x", hubstore.MaxDocumentBytes+1), "hub", "doc", "put", "team-x", "gross", c).
		want(t, 1, "zu groß")
	runIn(t, "x", "hub", "doc", "put", "fehlt", "a.md", c).want(t, 1, "Collection fehlt gibt es nicht")
	runT(t, "hub", "doc", "put", "team-x", "a.md", "--file", filepath.Join(dir, "fehlt"), c).want(t, 1)
	runT(t, "hub", "doc", "put", "team-x", c).want(t, 2, "Es fehlt: <name>")
	runT(t, "hub", "doc", "list", "team-x", "a", "b", c).want(t, 2, "Unerwartetes Argument")

	runT(t, "hub", "doc", "rm", "team-x", "readme.md", c).want(t, 0, "gelöscht (Löschmarke, Revision 5)")
	runT(t, "hub", "doc", "get", "team-x", "readme.md", c).want(t, 1, "gibt es in team-x nicht")
	runT(t, "hub", "doc", "rm", "team-x", "readme.md", c).want(t, 1, "gibt es in team-x nicht")
	runIn(t, "wieder", "hub", "doc", "put", "team-x", "readme.md", c).want(t, 0, "angelegt (Revision 6")

	runT(t, "status", c).want(t, 0)
	runT(t, "hub", "doc", "--help").want(t, 0, "Löschmarke", "1 MiB")
	runT(t, "hub", "--help").want(t, 0, "hub doc put", "hub import")
}

func TestHubImport(t *testing.T) {
	dir := isolate(t)
	c := "--config=" + setup(t, dir)
	runT(t, "hub", "collection", "add", "team-x", c).want(t, 0)
	runIn(t, "bleibt", "hub", "doc", "put", "team-x", "docs/nur-am-hub.md", c).want(t, 0)
	runIn(t, "alt", "hub", "doc", "put", "team-x", "docs/a.md", c).want(t, 0)
	runIn(t, "gleich", "hub", "doc", "put", "team-x", "docs/sub/c.md", c).want(t, 0)

	src := filepath.Join(dir, "quelle")
	writeFile(t, filepath.Join(src, "a.md"), "neu")
	writeFile(t, filepath.Join(src, "b.md"), "b")
	writeFile(t, filepath.Join(src, "sub", "c.md"), "gleich")
	writeFile(t, filepath.Join(src, ".versteckt"), "x")
	writeFile(t, filepath.Join(src, ".git", "config"), "x")
	writeFile(t, filepath.Join(src, "sub", ".obsidian", "x.md"), "x")
	writeFile(t, filepath.Join(src, "bild.png"), "\x89PNG\r\n\x1a\n\xff\xfe")
	writeFile(t, filepath.Join(src, "gross.md"), strings.Repeat("x", hubstore.MaxDocumentBytes+1))
	if err := os.Symlink(filepath.Join(src, "a.md"), filepath.Join(src, "link.md")); err != nil {
		t.Fatal(err)
	}

	r := runT(t, "hub", "import", "team-x", src, "--prefix", "docs", c)
	r.want(t, 0, "ersetzt:  docs/a.md", "angelegt: docs/b.md",
		"übersprungen: bild.png (kein UTF-8-Text)", "übersprungen: gross.md (größer als 1 MiB)",
		"übersprungen: link.md (keine gewöhnliche Datei)",
		"Import nach team-x: 1 angelegt, 1 ersetzt, 1 unverändert, 3 übersprungen — Revision 4.")
	if strings.Contains(r.out+r.errOut, "versteckt") || strings.Contains(r.out+r.errOut, ".git") ||
		strings.Contains(r.out+r.errOut, "obsidian") {
		t.Errorf("versteckte Dateien erwähnt:\n%s%s", r.out, r.errOut)
	}
	runT(t, "hub", "doc", "get", "team-x", "docs/a.md", c).want(t, 0, "neu")
	runT(t, "hub", "doc", "get", "team-x", "docs/nur-am-hub.md", c).want(t, 0, "bleibt")
	runT(t, "hub", "doc", "get", "team-x", "docs/.versteckt", c).want(t, 1, "gibt es")

	// Nochmals: nichts geändert, keine Revision.
	runT(t, "hub", "import", "team-x", src, "--prefix", "docs/", c).
		want(t, 0, "0 angelegt, 0 ersetzt, 3 unverändert, 3 übersprungen — keine neue Revision.")

	// Ohne Prefix landet a.md neben docs/; ein Konflikt bricht alles ab.
	writeFile(t, filepath.Join(src, "docs"), "Datei statt Verzeichnis")
	runT(t, "hub", "import", "team-x", src, c).want(t, 1, "zugleich")
	runT(t, "hub", "doc", "get", "team-x", "b.md", c).want(t, 1, "gibt es")

	runT(t, "hub", "import", "team-x", filepath.Join(src, "a.md"), c).want(t, 1, "kein Verzeichnis")
	runT(t, "hub", "import", "team-x", src, "--prefix", "../x", c).want(t, 1, "--prefix")
	runT(t, "hub", "import", "fehlt", src, "--prefix", "p", c).want(t, 1, "Collection fehlt gibt es nicht")
}

// Erzeugte Dateien unter docs/ (Task 033): put und rm lehnen sie ab, der
// Import übergeht sie und nennt sie.
func TestHubDocGenerated(t *testing.T) {
	dir := isolate(t)
	c := "--config=" + setup(t, dir)
	runT(t, "hub", "collection", "add", "team-x", c).want(t, 0)
	runIn(t, "x", "hub", "doc", "put", "team-x", "docs/index.md", c).want(t, 1, "erzeugte Datei", "rebuild-docs")
	runT(t, "hub", "doc", "rm", "team-x", "docs/README.md", c).want(t, 1, "erzeugte Datei")
	runIn(t, "x", "hub", "doc", "put", "team-x", "notizen/index.md", c).want(t, 0)

	src := filepath.Join(dir, "quelle")
	writeFile(t, filepath.Join(src, "index.md"), "alt")
	writeFile(t, filepath.Join(src, "README.md"), "alt")
	writeFile(t, filepath.Join(src, "sub", "index.md"), "alt")
	writeFile(t, filepath.Join(src, "sub", "x.md"), "x")
	runT(t, "hub", "import", "team-x", src, "--prefix", "docs", c).want(t, 0,
		"übersprungen: docs/index.md (erzeugte Datei", "übersprungen: docs/sub/index.md (erzeugte Datei",
		"übersprungen: docs/README.md (erzeugte Datei", "angelegt: docs/sub/x.md",
		"1 angelegt, 0 ersetzt, 0 unverändert, 3 übersprungen")
	runT(t, "hub", "doc", "get", "team-x", "docs/index.md", c).want(t, 1, "gibt es")
}
