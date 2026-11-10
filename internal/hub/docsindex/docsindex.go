// Package docsindex bildet den Soll-Zustand der Dokumente, die Kephalaion
// unter docs/ einer Collection selbst schreibt (Task 033,
// docs/indizierung.md): docs/README.md aus einer Vorlage, je Verzeichnis eine
// index.md nach Google OKF v0.2 und eine Mahnung, wo einem Verzeichnis die
// README.md fehlt. Es ist eine reine Funktion über den lebenden Dokumenten
// unter docs/ — ohne Datenbank, ohne Hub und Node; den Unterschied zum Ist
// bildet und schreibt der Hub-Store (rebuild_docs).
package docsindex

import (
	"bytes"
	_ "embed"
	"fmt"
	"path"
	"sort"
	"strings"
	"text/template"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/kephalaion/kephalaion/internal/contract"
	"github.com/kephalaion/kephalaion/internal/frontmatter"
)

//go:embed layout.yaml
var layoutYAML []byte

//go:embed readme.md.tmpl
var readmeTmpl string

// Author steht im Frontmatter generated.by der Dokumente, die der Neubau
// schreibt (OKF); der Urheber in created_by/updated_by ist
// contract.SystemAuthor.
const Author = "kephalaion"

// NoDescription steht im Index, wo ein Eintrag keine Beschreibung hat.
const NoDescription = "*no description*"

// OKFVersion steht im Frontmatter von docs/index.md, der einzigen index.md
// mit Frontmatter (OKF §12).
const OKFVersion = "0.2"

// Dir ist ein festes Verzeichnis aus layout.yaml: Name direkt unter docs/,
// Titel und Beschreibung.
type Dir struct {
	Name        string
	Title       string
	Description string
}

// layout sind die festen Verzeichnisse nach Name, gelesen beim Start. Ein
// Fehler darin ist ein Fehler des Baus; der Test des Pakets fängt ihn.
var layout = mustLayout(layoutYAML)

func mustLayout(data []byte) []Dir {
	l, err := parseLayout(data)
	if err != nil {
		panic("internal/hub/docsindex/layout.yaml: " + err.Error())
	}
	return l
}

// parseLayout liest die Liste streng: unbekannte Schlüssel, ein Name mit '/'
// oder ein leerer Titel bzw. eine leere Beschreibung sind Fehler.
func parseLayout(data []byte) ([]Dir, error) {
	var raw map[string]struct {
		Title       string `yaml:"title"`
		Description string `yaml:"description"`
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&raw); err != nil {
		return nil, err
	}
	out := make([]Dir, 0, len(raw))
	for name, e := range raw {
		if name == "" || strings.Contains(name, "/") || name == contract.IndexName || name == contract.ReadmeName {
			return nil, fmt.Errorf("Verzeichnis %q: ein Name direkt unter docs/ erwartet", name)
		}
		if strings.TrimSpace(e.Title) == "" || strings.TrimSpace(e.Description) == "" {
			return nil, fmt.Errorf("Verzeichnis %q: title und description sind Pflicht", name)
		}
		out = append(out, Dir{Name: name, Title: e.Title, Description: e.Description})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Layout liefert die festen Verzeichnisse, nach Name.
func Layout() []Dir { return append([]Dir(nil), layout...) }

// Doc ist ein lebendes Dokument unter docs/, wie der Neubau es sieht.
// UpdatedBy unterscheidet eine Mahnung (contract.IsNotice) von einer
// README.md des Users.
type Doc struct {
	Name      string
	Content   string
	UpdatedBy string
}

// File ist ein Dokument des Soll-Zustands.
type File struct {
	Name    string
	Content string
}

// Build liefert den Soll-Zustand der Dokumente des Systems unter docs/, nach
// Name: docs/README.md, je Verzeichnis eine index.md und eine Mahnung in
// jedem Verzeichnis unterhalb von docs/ ohne README.md.
//
// Ein Verzeichnis zählt nur über Dokumente, die nicht dem System gehören
// (contract.IsSystemDoc: keine index.md, keine Mahnung), dazu docs/ selbst
// und die festen Verzeichnisse — außer eines, dessen Name ein Dokument trägt.
// Eine neue Mahnung trägt at (UTC, auf Sekunden) in generated.at; eine
// bestehende behält ihren Zeitpunkt, so dass ein Neubau ohne Änderung nichts
// schreibt. Dokumente außerhalb von docs/ übergeht Build.
func Build(docs []Doc, at time.Time) []File {
	stamp := at.UTC().Truncate(time.Second).Format(time.RFC3339)
	regular := map[string]Doc{}
	notices := map[string]Doc{}
	for _, d := range docs {
		switch {
		case !contract.InDocs(d.Name):
		case contract.IsNotice(d.Name, d.UpdatedBy):
			notices[path.Dir(d.Name)] = d
		case !contract.IsGenerated(d.Name):
			regular[d.Name] = d
		}
	}
	fixed := map[string]Dir{}
	dirs := map[string]bool{contract.DocsDir: true}
	for _, f := range layout {
		name := contract.DocsDir + "/" + f.Name
		if _, isDoc := regular[name]; isDoc {
			continue
		}
		fixed[name] = f
		dirs[name] = true
	}
	for name := range regular {
		for dir := path.Dir(name); dir != contract.DocsDir && dir != "."; dir = path.Dir(dir) {
			dirs[dir] = true
		}
	}

	out := []File{{Name: contract.DocsReadme, Content: readme()}}
	// readmes ist der Inhalt der README.md je Verzeichnis unterhalb von
	// docs/: die des Users oder die Mahnung des Soll-Zustands.
	readmes := map[string]string{}
	for dir := range dirs {
		if dir == contract.DocsDir {
			continue
		}
		name := dir + "/" + contract.ReadmeName
		if d, ok := regular[name]; ok {
			readmes[dir] = d.Content
			continue
		}
		when := stamp
		if old, ok := notices[dir]; ok {
			if prev := noticeStamp(old.Content); prev != "" {
				when = prev
			}
		}
		f, isFixed := fixed[dir]
		content := notice(dir, f, isFixed, when)
		readmes[dir] = content
		out = append(out, File{Name: name, Content: content})
	}
	for dir := range dirs {
		out = append(out, File{Name: dir + "/" + contract.IndexName, Content: index(dir, dirs, regular, readmes)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// readme ist docs/README.md aus der Vorlage, mit den festen Verzeichnissen.
func readme() string {
	var b strings.Builder
	if err := template.Must(template.New("readme").Parse(readmeTmpl)).Execute(&b, layout); err != nil {
		panic("internal/hub/docsindex/readme.md.tmpl: " + err.Error())
	}
	return b.String()
}

// noticeHead ist das Frontmatter einer Mahnung, in dieser Reihenfolge.
type noticeHead struct {
	Title       string        `yaml:"title,omitempty"`
	Description string        `yaml:"description,omitempty"`
	Generated   generatedHead `yaml:"generated"`
}

type generatedHead struct {
	By string `yaml:"by"`
	At string `yaml:"at"`
}

// notice ist die Mahnung eines Verzeichnisses ohne README.md. Die eines
// festen Verzeichnisses trägt Titel und Beschreibung aus der Liste, jede
// andere keine: Der Index zeigt dann den Namen und *no description*.
func notice(dir string, f Dir, isFixed bool, at string) string {
	head := noticeHead{Generated: generatedHead{By: Author, At: at}}
	title := path.Base(dir)
	if isFixed {
		head.Title, head.Description, title = f.Title, f.Description, f.Title
	}
	var y bytes.Buffer
	enc := yaml.NewEncoder(&y)
	enc.SetIndent(2)
	if err := enc.Encode(head); err != nil {
		panic("Mahnung: " + err.Error())
	}
	return "---\n" + y.String() + "---\n\n# " + title + "\n\n" +
		"This directory has no `README.md` yet; Kephalaion put this notice here when it rebuilt `docs/`.\n\n" +
		"Replace it with a `README.md` that describes the directory: front matter with `title` and\n" +
		"`description` (one sentence), then a few lines on what belongs here. Use `write`, not `create`;\n" +
		"anyone with `write` may replace it. Rebuilding `docs/` keeps your `README.md`.\n"
}

// noticeStamp liest generated.at einer bestehenden Mahnung, leer ohne.
func noticeStamp(content string) string {
	fm, ok, err := frontmatter.Parse([]byte(content))
	if !ok || err != nil {
		return ""
	}
	g, _ := fm["generated"].(map[string]any)
	at, _ := g["at"].(string)
	return at
}

// index ist die index.md eines Verzeichnisses: eine Ebene, erst
// # Directories, dann # Documents, ein leerer Abschnitt entfällt; je
// Abschnitt nach Name. Nur docs/index.md trägt Frontmatter.
func index(dir string, dirs map[string]bool, regular map[string]Doc, readmes map[string]string) string {
	var subdirs, documents []string
	for d := range dirs {
		if d != dir && path.Dir(d) == dir {
			subdirs = append(subdirs, d)
		}
	}
	for name := range regular {
		if path.Dir(name) == dir && path.Base(name) != contract.ReadmeName {
			documents = append(documents, name)
		}
	}
	sort.Strings(subdirs)
	sort.Strings(documents)

	var b strings.Builder
	if dir == contract.DocsDir {
		fmt.Fprintf(&b, "---\nokf_version: %q\n---\n\n", OKFVersion)
	}
	if len(subdirs) > 0 {
		b.WriteString("# Directories\n\n")
		for _, d := range subdirs {
			title, desc := describe(readmes[d], "", path.Base(d))
			entry(&b, title, path.Base(d)+"/", desc)
		}
	}
	if len(documents) > 0 {
		if len(subdirs) > 0 {
			b.WriteString("\n")
		}
		b.WriteString("# Documents\n\n")
		for _, name := range documents {
			base := path.Base(name)
			if !frontmatter.IsMarkdown(base) {
				fmt.Fprintf(&b, "* [%s](%s)\n", escapeText(base), link(base))
				continue
			}
			title, desc := describe(regular[name].Content, firstHeading(regular[name].Content), base)
			entry(&b, title, base, desc)
		}
	}
	return b.String()
}

// entry schreibt einen Eintrag: * [Titel](link) - Beschreibung.
func entry(b *strings.Builder, title, target, desc string) {
	fmt.Fprintf(b, "* [%s](%s) - %s\n", escapeText(title), link(target), desc)
}

// describe liefert Titel und Beschreibung aus dem Frontmatter eines
// Inhalts: title, sonst heading, sonst fallback; description, sonst
// NoDescription. Ein Frontmatter, das sich nicht lesen lässt, zählt als
// keines.
func describe(content, heading, fallback string) (title, desc string) {
	title, desc = heading, NoDescription
	if fm, ok, err := frontmatter.Parse([]byte(content)); ok && err == nil {
		if t := scalarText(fm["title"]); t != "" {
			title = t
		}
		if d := scalarText(fm["description"]); d != "" {
			desc = d
		}
	}
	if title == "" {
		title = fallback
	}
	return title, desc
}

// scalarText ist ein Wert des Frontmatters als eine Zeile Text: Text, Zahl
// oder Wahrheitswert; alles andere leer.
func scalarText(v any) string {
	switch v.(type) {
	case string, int, int64, float64, bool, uint64:
		return strings.Join(strings.Fields(fmt.Sprint(v)), " ")
	}
	return ""
}

// firstHeading liefert die erste Überschrift erster Ebene (# Titel) hinter
// dem Frontmatter, außerhalb von Codeblöcken; leer ohne.
func firstHeading(content string) string {
	fence := ""
	for _, line := range strings.Split(frontmatter.Body(content), "\n") {
		line = strings.TrimRight(line, "\r")
		trimmed := strings.TrimLeft(line, " ")
		if len(line)-len(trimmed) > 3 {
			continue
		}
		if f := fenceOf(trimmed); f != "" {
			switch {
			case fence == "":
				fence = f
			case strings.HasPrefix(trimmed, fence) && strings.TrimSpace(strings.TrimLeft(trimmed, fence[:1])) == "":
				fence = ""
			}
			continue
		}
		if fence != "" {
			continue
		}
		if rest, ok := strings.CutPrefix(trimmed, "# "); ok {
			rest = strings.TrimSpace(strings.TrimRight(strings.TrimSpace(rest), "#"))
			if t := strings.Join(strings.Fields(rest), " "); t != "" {
				return t
			}
		}
	}
	return ""
}

// fenceOf liefert den Zaun am Anfang einer Zeile — mindestens drei ` oder ~
// —, leer ohne.
func fenceOf(line string) string {
	for _, c := range []string{"`", "~"} {
		n := len(line) - len(strings.TrimLeft(line, c))
		if n >= 3 {
			return strings.Repeat(c, n)
		}
	}
	return ""
}

// escapeText maskiert, was im Text eines Links Markdown wäre: \, [ und ].
func escapeText(s string) string {
	return strings.NewReplacer(`\`, `\\`, `[`, `\[`, `]`, `\]`).Replace(s)
}

// link ist das Ziel eines Links: der Name selbst, relativ; trägt er
// Leerzeichen, Klammern oder spitze Klammern, in <…> mit maskierten < und >
// (CommonMark), damit der Name für read unverändert bleibt.
func link(target string) string {
	if !strings.ContainsAny(target, " \t()<>") {
		return target
	}
	return "<" + strings.NewReplacer(`<`, `\<`, `>`, `\>`).Replace(target) + ">"
}
