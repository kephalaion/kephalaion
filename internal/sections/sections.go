// Package sections zerlegt ein Markdown-Dokument in Abschnitte (Task 033,
// docs/indizierung.md, „Der Index: FTS5 im Node“): ein Abschnitt ist eine
// Überschrift samt Text bis zur nächsten Überschrift, außerhalb von
// Codeblöcken. Der Volltextindex der Replica und read mit section benutzen
// dieselbe Zerlegung. Das Paket ist neutral — ohne Hub, Node und
// Datenbank.
package sections

import (
	"fmt"
	"path"
	"strings"
	"unicode"

	"github.com/kephalaion/kephalaion/internal/frontmatter"
)

// Version ist die Fassung der Zerlegung und der Felder, die der Index aus
// einem Dokument nimmt. Ändert sich etwas an Split oder Parse, steigt sie;
// die Replica baut ihren Index dann neu.
const Version = 1

// Section ist ein Abschnitt eines Dokuments.
type Section struct {
	// Heading ist der Text der Überschrift; leer beim Text vor der ersten
	// Überschrift.
	Heading string
	// Level ist die Ebene der Überschrift, 1 bis 6; 0 vor der ersten.
	Level int
	// Headings ist der Pfad der Überschriften bis hierher, die eigene
	// zuletzt: je höherer Ebene die letzte davor.
	Headings []string
	// Anchor ist die Kennung im Dokument, eindeutig je Dokument; leer beim
	// Text vor der ersten Überschrift.
	Anchor string
	// Content ist der Abschnitt, wie er im Dokument steht, mit der Zeile der
	// Überschrift; Body ohne sie.
	Content string
	Body    string
}

// Doc ist ein Dokument, wie der Index es sieht: die Felder aus dem Kopf und
// die Abschnitte.
type Doc struct {
	// Title ist title aus dem Frontmatter, sonst die erste Überschrift
	// erster Ebene, sonst der Dateiname.
	Title       string
	Description string
	// Questions und Hints sind die Werte aus dem Frontmatter, je ein Text
	// oder eine Liste von Texten.
	Questions []string
	Hints     []string
	Sections  []Section
}

// Parse liest ein Dokument mit dem Namen name: Frontmatter und Abschnitte.
// Ein Frontmatter, das sich nicht lesen lässt, zählt als keines.
func Parse(name, content string) Doc {
	d := Doc{Sections: Split(content)}
	if fm, ok, err := frontmatter.Parse([]byte(content)); ok && err == nil {
		d.Title = text(fm["title"])
		d.Description = text(fm["description"])
		d.Questions = texts(fm["questions"])
		d.Hints = texts(fm["hints"])
	}
	if d.Title == "" {
		for _, s := range d.Sections {
			if s.Level == 1 {
				d.Title = s.Heading
				break
			}
		}
	}
	if d.Title == "" {
		d.Title = path.Base(name)
	}
	return d
}

// text ist ein Wert als eine Zeile Text: Text, Zahl oder Wahrheitswert.
func text(v any) string {
	switch v.(type) {
	case string, int, int64, uint64, float64, bool:
		return strings.Join(strings.Fields(fmt.Sprint(v)), " ")
	}
	return ""
}

// texts ist ein Wert als Liste von Texten: ein Text oder eine Liste davon.
func texts(v any) []string {
	if l, ok := v.([]any); ok {
		var out []string
		for _, e := range l {
			if t := text(e); t != "" {
				out = append(out, t)
			}
		}
		return out
	}
	if t := text(v); t != "" {
		return []string{t}
	}
	return nil
}

// Split zerlegt den Inhalt hinter dem Frontmatter an den Überschriften
// (ATX, # bis ######, höchstens drei Leerzeichen davor), außerhalb von
// Codeblöcken (``` und ~~~). Der Text vor der ersten Überschrift ist ein
// eigener Abschnitt ohne Überschrift und Anker, nur wenn er etwas außer
// Leerraum trägt. Anker nach Anchor, eindeutig je Dokument: Kommt einer
// wieder, bekommt er -1, -2 ….
func Split(content string) []Section {
	lines := strings.SplitAfter(frontmatter.Body(content), "\n")
	var out []Section
	var cur *Section
	var buf strings.Builder
	stack := make([]string, 7)
	used := map[string]bool{}
	flush := func() {
		if cur == nil {
			if strings.TrimSpace(buf.String()) != "" {
				out = append(out, Section{Content: buf.String(), Body: buf.String()})
			}
		} else {
			cur.Content += buf.String()
			cur.Body = buf.String()
			out = append(out, *cur)
		}
		buf.Reset()
	}
	fence := ""
	for _, line := range lines {
		if line == "" {
			continue
		}
		trimmed := strings.TrimRight(line, "\r\n")
		if f, ok := fenceLine(trimmed, fence); ok {
			fence = f
			buf.WriteString(line)
			continue
		}
		level, heading, ok := "", "", false
		if fence == "" {
			level, heading, ok = atx(trimmed)
		}
		if !ok {
			buf.WriteString(line)
			continue
		}
		flush()
		n := len(level)
		stack[n] = heading
		for i := n + 1; i < len(stack); i++ {
			stack[i] = ""
		}
		var hs []string
		for i := 1; i <= n; i++ {
			if stack[i] != "" {
				hs = append(hs, stack[i])
			}
		}
		cur = &Section{Heading: heading, Level: n, Headings: hs, Anchor: unique(Anchor(heading), used), Content: line}
	}
	flush()
	return out
}

// fenceLine erkennt eine Zeile, die einen Codeblock öffnet oder schließt:
// außerhalb (open leer) jeder Zaun aus mindestens drei ` oder ~, innerhalb
// nur derselbe Zeichen, mindestens so lang, ohne Text dahinter. Es liefert
// den Zaun danach.
func fenceLine(line, open string) (string, bool) {
	t := strings.TrimLeft(line, " ")
	if len(line)-len(t) > 3 {
		return open, false
	}
	for _, c := range []byte{'`', '~'} {
		n := 0
		for n < len(t) && t[n] == c {
			n++
		}
		if n < 3 {
			continue
		}
		if open == "" {
			return t[:n], true
		}
		if open[0] == c && n >= len(open) && strings.TrimSpace(t[n:]) == "" {
			return "", true
		}
	}
	return open, false
}

// atx erkennt eine Überschrift: höchstens drei Leerzeichen, 1 bis 6 #, dann
// ein Leerzeichen oder das Zeilenende; schließende # fallen weg.
func atx(line string) (marks, heading string, ok bool) {
	t := strings.TrimLeft(line, " ")
	if len(line)-len(t) > 3 {
		return "", "", false
	}
	n := 0
	for n < len(t) && t[n] == '#' {
		n++
	}
	if n == 0 || n > 6 || (n < len(t) && t[n] != ' ' && t[n] != '\t') {
		return "", "", false
	}
	h := strings.TrimSpace(t[n:])
	if s := strings.TrimRight(h, "#"); s == "" || strings.HasSuffix(s, " ") || strings.HasSuffix(s, "\t") {
		h = strings.TrimSpace(s)
	}
	return t[:n], strings.Join(strings.Fields(h), " "), true
}

// Anchor bildet den Anker einer Überschrift wie GitHub: klein, Buchstaben,
// Ziffern, - und _ bleiben, Leerzeichen werden -, alles andere fällt weg.
// Bleibt nichts, ist er section.
func Anchor(heading string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(heading) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_':
			b.WriteRune(r)
		case r == ' ':
			b.WriteByte('-')
		}
	}
	if b.Len() == 0 {
		return "section"
	}
	return b.String()
}

// unique macht einen Anker im Dokument eindeutig: beim zweiten Mal -1, dann
// -2 …, auch an einem Anker vorbei, den es so schon gibt.
func unique(a string, used map[string]bool) string {
	out := a
	for i := 1; used[out]; i++ {
		out = fmt.Sprintf("%s-%d", a, i)
	}
	used[out] = true
	return out
}

// Find liefert den Abschnitt mit dem Anker anchor; ok ist false, wenn es ihn
// nicht gibt.
func Find(content, anchor string) (Section, bool) {
	if anchor == "" {
		return Section{}, false
	}
	for _, s := range Split(content) {
		if s.Anchor == anchor {
			return s, true
		}
	}
	return Section{}, false
}
