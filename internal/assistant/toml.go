package assistant

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Das Nötigste an TOML, um in einer fremden Datei genau eine Tabelle zu
// finden, zu ersetzen und zu entfernen — am Text, damit alles andere bleibt,
// auch Kommentare und Leerraum. Kein allgemeiner TOML-Leser: Werte außerhalb
// der eigenen Tabelle werden nur so weit verfolgt, dass eine Zeile in einem
// mehrzeiligen Text oder Feld nicht als Tabellenkopf gilt.

// tomlTable ist eine Tabelle im Text: der Kopf und die Zeilen bis vor den
// nächsten Kopf.
type tomlTable struct {
	// path sind die Segmente des Namens, etwa mcp_servers, kephalaion.
	path []string
	// array sagt, ob der Kopf [[…]] ist.
	array bool
	// header ist die Zeile des Kopfs, end die erste Zeile danach, die nicht
	// mehr zur Tabelle gehört (der nächste Kopf oder das Ende).
	header, end int
}

// tomlTables liefert die Zeilen des Texts (ohne Zeilenende) und seine
// Tabellen in der Reihenfolge der Datei.
func tomlTables(text string) (lines []string, tables []tomlTable) {
	lines = strings.Split(text, "\n")
	var st tomlState
	for i, line := range lines {
		if st.top() {
			if path, array, ok := tomlHeader(line); ok {
				if n := len(tables); n > 0 {
					tables[n-1].end = i
				}
				tables = append(tables, tomlTable{path: path, array: array, header: i, end: len(lines)})
				continue
			}
		}
		st.scan(line)
	}
	return lines, tables
}

// tomlState verfolgt über Zeilen hinweg, ob der Text in einem mehrzeiligen
// Text oder in einem offenen Feld steht.
type tomlState struct {
	// multi ist das Ende des offenen mehrzeiligen Texts (""" oder '''), sonst
	// leer.
	multi string
	// depth zählt offene [ und {.
	depth int
}

func (s *tomlState) top() bool { return s.multi == "" && s.depth == 0 }

// scan liest eine Zeile und führt den Zustand fort.
func (s *tomlState) scan(line string) {
	i := 0
	for i < len(line) {
		if s.multi != "" {
			if s.multi == `"""` && line[i] == '\\' {
				i += 2
				continue
			}
			if strings.HasPrefix(line[i:], s.multi) {
				i += 3
				s.multi = ""
				continue
			}
			i++
			continue
		}
		switch c := line[i]; {
		case c == '#':
			return
		case strings.HasPrefix(line[i:], `"""`) || strings.HasPrefix(line[i:], `'''`):
			s.multi = line[i : i+3]
			i += 3
		case c == '"':
			i++
			for i < len(line) && line[i] != '"' {
				if line[i] == '\\' {
					i++
				}
				i++
			}
			i++
		case c == '\'':
			i++
			for i < len(line) && line[i] != '\'' {
				i++
			}
			i++
		case c == '[' || c == '{':
			s.depth++
			i++
		case c == ']' || c == '}':
			if s.depth > 0 {
				s.depth--
			}
			i++
		default:
			i++
		}
	}
}

// tomlHeader liest einen Tabellenkopf: [a.b], [ a . "b" ], [[a.b]], dahinter
// höchstens ein Kommentar.
func tomlHeader(line string) (path []string, array, ok bool) {
	s := strings.TrimSpace(line)
	open, closing := "[", "]"
	if strings.HasPrefix(s, "[[") {
		open, closing, array = "[[", "]]", true
	}
	if !strings.HasPrefix(s, open) {
		return nil, false, false
	}
	path, rest, ok := tomlKey(s[len(open):])
	if !ok {
		return nil, false, false
	}
	rest, ok = strings.CutPrefix(strings.TrimLeft(rest, " \t"), closing)
	if !ok {
		return nil, false, false
	}
	if rest = strings.TrimSpace(rest); rest != "" && !strings.HasPrefix(rest, "#") {
		return nil, false, false
	}
	return path, array, true
}

// tomlKey liest einen Schlüssel mit Punkten (a.b."c d") vom Anfang von s und
// liefert seine Segmente und den Rest.
func tomlKey(s string) (path []string, rest string, ok bool) {
	for {
		s = strings.TrimLeft(s, " \t")
		var seg string
		switch {
		case strings.HasPrefix(s, `"`):
			end := closingQuote(s)
			if end < 0 {
				return nil, "", false
			}
			v, err := parseTOMLBasic(s[1:end])
			if err != nil {
				return nil, "", false
			}
			seg, s = v, s[end+1:]
		case strings.HasPrefix(s, "'"):
			end := strings.IndexByte(s[1:], '\'')
			if end < 0 {
				return nil, "", false
			}
			seg, s = s[1:1+end], s[end+2:]
		default:
			n := 0
			for n < len(s) && isBareKeyChar(s[n]) {
				n++
			}
			if n == 0 {
				return nil, "", false
			}
			seg, s = s[:n], s[n:]
		}
		path = append(path, seg)
		s = strings.TrimLeft(s, " \t")
		if !strings.HasPrefix(s, ".") {
			return path, s, true
		}
		s = s[1:]
	}
}

func isBareKeyChar(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-'
}

// closingQuote liefert die Stelle des schließenden " eines Texts, der an s[0]
// beginnt, oder -1.
func closingQuote(s string) int {
	for i := 1; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '"':
			return i
		}
	}
	return -1
}

// tomlPair ist eine Zeile schlüssel = wert einer Tabelle.
type tomlPair struct {
	key []string
	// text ist der Wert, wenn er ein einzeiliger Text ist ("…" oder '…');
	// isText sagt, ob er es ist.
	text   string
	isText bool
}

// tomlPairs liest die Zeilen schlüssel = wert aus den Zeilen einer Tabelle.
// Kommentare und Leerzeilen zählen nicht; ok ist false, wenn eine Zeile
// weder das eine noch das andere ist (etwa die Fortsetzung eines mehrzeiligen
// Werts) — dann ist der Inhalt der Tabelle fremd.
func tomlPairs(lines []string) (pairs []tomlPair, ok bool) {
	ok = true
	var st tomlState
	for _, line := range lines {
		if !st.top() {
			st.scan(line)
			ok = false
			continue
		}
		s := strings.TrimSpace(line)
		if s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		key, rest, keyOK := tomlKey(s)
		rest, eq := strings.CutPrefix(strings.TrimLeft(rest, " \t"), "=")
		if !keyOK || !eq {
			ok = false
			st.scan(line)
			continue
		}
		p := tomlPair{key: key}
		p.text, p.isText = tomlTextValue(strings.TrimSpace(rest))
		pairs = append(pairs, p)
		st.scan(line)
	}
	return pairs, ok
}

// tomlTextValue liest einen einzeiligen Text ("…" mit Escapes oder '…'),
// hinter dem höchstens ein Kommentar steht.
func tomlTextValue(s string) (string, bool) {
	var v, rest string
	switch {
	case strings.HasPrefix(s, `"""`) || strings.HasPrefix(s, `'''`):
		return "", false
	case strings.HasPrefix(s, `"`):
		end := closingQuote(s)
		if end < 0 {
			return "", false
		}
		parsed, err := parseTOMLBasic(s[1:end])
		if err != nil {
			return "", false
		}
		v, rest = parsed, s[end+1:]
	case strings.HasPrefix(s, "'"):
		end := strings.IndexByte(s[1:], '\'')
		if end < 0 {
			return "", false
		}
		v, rest = s[1:1+end], s[end+2:]
	default:
		return "", false
	}
	if rest = strings.TrimSpace(rest); rest != "" && !strings.HasPrefix(rest, "#") {
		return "", false
	}
	return v, true
}

// parseTOMLBasic löst die Escapes eines einzeiligen Texts in "…" auf.
func parseTOMLBasic(s string) (string, error) {
	if !strings.Contains(s, `\`) {
		return s, nil
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' {
			b.WriteByte(s[i])
			continue
		}
		i++
		if i >= len(s) {
			return "", fmt.Errorf("Escape am Ende")
		}
		switch s[i] {
		case 'b':
			b.WriteByte('\b')
		case 't':
			b.WriteByte('\t')
		case 'n':
			b.WriteByte('\n')
		case 'f':
			b.WriteByte('\f')
		case 'r':
			b.WriteByte('\r')
		case '"':
			b.WriteByte('"')
		case '\\':
			b.WriteByte('\\')
		case 'u', 'U':
			n := 4
			if s[i] == 'U' {
				n = 8
			}
			if i+n >= len(s) {
				return "", fmt.Errorf("unvollständiges \\%c", s[i])
			}
			r, err := strconv.ParseUint(s[i+1:i+1+n], 16, 32)
			if err != nil || !utf8.ValidRune(rune(r)) {
				return "", fmt.Errorf("ungültiges \\%c", s[i])
			}
			b.WriteRune(rune(r))
			i += n
		default:
			return "", fmt.Errorf("unbekanntes Escape \\%c", s[i])
		}
	}
	return b.String(), nil
}

// tomlString schreibt einen Text als einzeiligen TOML-Text in "…".
func tomlString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"':
			b.WriteString(`\"`)
		case r == '\\':
			b.WriteString(`\\`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\t':
			b.WriteString(`\t`)
		case r == '\r':
			b.WriteString(`\r`)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\u%04X`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// pathEqual sagt, ob zwei Namen dieselben Segmente haben; hasPrefix, ob path
// mit prefix beginnt und länger ist (eine Tabelle darunter).
func pathEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func pathBelow(path, prefix []string) bool {
	return len(path) > len(prefix) && pathEqual(path[:len(prefix)], prefix)
}

// contentEnd liefert die erste Zeile nach der letzten Zeile mit Inhalt in
// lines[from:to]: Leerzeilen und Kommentare am Ende einer Tabelle gehören zur
// nächsten, nicht zu ihr.
func contentEnd(lines []string, from, to int) int {
	end := from
	for i := from; i < to; i++ {
		if s := strings.TrimSpace(lines[i]); s != "" && !strings.HasPrefix(s, "#") {
			end = i + 1
		}
	}
	return end
}
