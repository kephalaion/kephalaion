package frontmatter

import (
	"encoding/json"
	"strings"
	"testing"
)

// parse liefert das Ergebnis als JSON-Text (Schlüssel sortiert), "" ohne
// Frontmatter, und den Fehlertext.
func parse(t *testing.T, head string) (js string, ok bool, errText string) {
	t.Helper()
	fm, ok, err := Parse([]byte(head))
	if err != nil {
		if !ok || fm != nil {
			t.Errorf("%q: Fehler %v, aber ok %v, fm %v", head, err, ok, fm)
		}
		return "", ok, err.Error()
	}
	if !ok {
		if fm != nil {
			t.Errorf("%q: ohne Frontmatter, aber fm %v", head, fm)
		}
		return "", false, ""
	}
	b, err := json.Marshal(fm)
	if err != nil {
		t.Fatalf("%q: %v", head, err)
	}
	return string(b), true, ""
}

func TestParse(t *testing.T) {
	cases := []struct {
		name string
		head string
		want string
		// none: der Inhalt hat kein Frontmatter; errPart: der Fehler enthält
		// diesen Text.
		none    bool
		errPart string
	}{
		{name: "einfach", head: "---\ntitle: Hallo\n---\nText\n", want: `{"title":"Hallo"}`},
		{name: "ohne", head: "# Titel\n\nText\n", none: true},
		{name: "leerer Inhalt", head: "", none: true},
		{name: "nicht am Anfang", head: "\n---\ntitle: x\n---\n", none: true},
		{name: "Leerzeichen davor", head: " ---\ntitle: x\n---\n", none: true},
		{name: "kein genaues ---", head: "----\ntitle: x\n---\n", none: true},
		{name: "--- mit Rest", head: "--- \ntitle: x\n---\n", none: true},
		{name: "TOML", head: "+++\ntitle = \"x\"\n+++\n", none: true},
		{name: "BOM", head: "\xef\xbb\xbf---\ntitle: x\n---\n", want: `{"title":"x"}`},
		{name: "CRLF", head: "---\r\ntitle: x\r\nn: 1\r\n---\r\nText\r\n", want: `{"n":1,"title":"x"}`},
		{name: "Schluss ohne Zeilenende", head: "---\ntitle: x\n---", want: `{"title":"x"}`},
		{name: "nicht geschlossen", head: "---\ntitle: x\nText\n", errPart: "nicht geschlossen"},
		{name: "nur Anfang", head: "---", errPart: "nicht geschlossen"},
		{name: "nur Anfang mit Zeilenende", head: "---\n", errPart: "nicht geschlossen"},
		{name: "leerer Block", head: "---\n---\nText\n", want: `{}`},
		{name: "nur Kommentar", head: "---\n# nichts\n\n---\n", want: `{}`},
		{name: "ungültiges YAML", head: "---\ntitle: [x\n---\n", errPart: "YAML ungültig"},
		{name: "doppelter Schlüssel", head: "---\na: 1\na: 2\n---\n", errPart: "Schlüssel a steht doppelt"},
		{name: "Schlüssel in Anführungszeichen", head: "---\n\"1\": eins\n---\n", want: `{"1":"eins"}`},
		{name: "Alias", head: "---\na: &x [1, 2]\nb: *x\n---\n", want: `{"a":[1,2],"b":[1,2]}`},
		{name: "null oben", head: "---\n~\n---\n", want: `{}`},
		// yaml.v3 erkennt im Knotenbaum keinen Alias auf sich selbst; die Tiefe begrenzt es.
		{name: "Alias auf sich selbst", head: "---\na: &x [*x]\n---\n", errPart: "zu tief"},
		{name: "Zahlen", head: "---\nbig: 18446744073709551615\nhex: 0x10\noct: 0o17\nexp: 1e3\n---\n", want: `{"big":18446744073709551615,"exp":1000,"hex":16,"oct":15}`},
		{name: "fremder Tag", head: "---\nx: !custom wert\n---\n", want: `{"x":"wert"}`},
		{name: "Binary", head: "---\nx: !!binary aGk=\n---\n", want: `{"x":"aGk="}`},
		{name: "Liste oben", head: "---\n- a\n- b\n---\n", errPart: "kein Objekt, sondern eine Liste"},
		{name: "Wert oben", head: "---\nHallo\n---\n", errPart: "kein Objekt, sondern ein einzelner Wert"},
		{name: "Zahl oben", head: "---\n42\n---\n", errPart: "kein Objekt"},
		{name: "Schlüssel Zahl", head: "---\n1: eins\n---\n", errPart: "Schlüssel 1 ist kein Text"},
		{name: "Schlüssel Wahrheitswert", head: "---\ntrue: ja\n---\n", errPart: "Schlüssel true ist kein Text"},
		{name: "Schlüssel tief", head: "---\na:\n  b:\n    2: x\n---\n", errPart: "Schlüssel 2 ist kein Text"},
		{name: "Schlüssel in Liste", head: "---\na:\n  - 3: x\n---\n", errPart: "Schlüssel 3 ist kein Text"},
		{name: "verschachtelt",
			head: "---\nname: k\nlist:\n  - a\n  - 2\n  - true\n  - x: y\nobj:\n  n: 1.5\n  neg: -3\n  off: false\n  nil: ~\n---\n",
			want: `{"list":["a",2,true,{"x":"y"}],"name":"k","obj":{"n":1.5,"neg":-3,"nil":null,"off":false}}`},
		{name: "Text mit Anführungszeichen", head: "---\ndescription: \"Use when: a, b\"\n---\n",
			want: `{"description":"Use when: a, b"}`},
		{name: "Zeitangaben", head: "---\nd: 2026-09-27\nt: 2026-09-27T10:00:00Z\nx: !!timestamp 2026-09-27T10:00:00Z\n---\n",
			want: `{"d":"2026-09-27","t":"2026-09-27T10:00:00Z","x":"2026-09-27T10:00:00Z"}`},
		{name: "Zeit mit Zone", head: "---\nt: 2026-09-27 12:00:00 +02:00\n---\n", want: `{"t":"2026-09-27 12:00:00 +02:00"}`},
		{name: "nicht JSON", head: "---\nn: .inf\n---\n", errPart: "nicht als JSON"},
		{name: "NaN", head: "---\nn: .nan\n---\n", errPart: "nicht als JSON"},
		{name: "NaN tief", head: "---\nn:\n  - .NaN\n---\n", errPart: "nicht als JSON"},
		{name: "--- im Text", head: "---\ntitle: x\n---\nText\n---\nmehr: nein\n---\n", want: `{"title":"x"}`},
		{name: "--- als Wert", head: "---\ntitle: x\ntext: |\n  a\n  ---\n  b\n---\n", want: `{"text":"a\n---\nb\n","title":"x"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			js, ok, errText := parse(t, c.head)
			switch {
			case c.none:
				if ok || errText != "" {
					t.Errorf("erwartet keins: %q, %q", js, errText)
				}
			case c.errPart != "":
				if !ok || !strings.Contains(errText, c.errPart) {
					t.Errorf("erwartet Fehler mit %q: ok %v, %q, %q", c.errPart, ok, js, errText)
				}
			default:
				if !ok || errText != "" || js != c.want {
					t.Errorf("erwartet %s: ok %v, %q, %q", c.want, ok, js, errText)
				}
			}
		})
	}
}

// Die Grenze: Parse sieht nur MaxBytes an; ein Block, der darin nicht endet,
// ist zu lang.
func TestParseLimit(t *testing.T) {
	long := "---\ntitle: x\n" + strings.Repeat("# Kommentar, der den Block streckt\n", 3000)
	if len(long) <= MaxBytes {
		t.Fatal("Testdaten zu kurz")
	}
	// Geschlossen jenseits der Grenze: zu lang.
	_, ok, err := Parse([]byte(long + "---\nText\n"))
	if !ok || err == nil || !strings.Contains(err.Error(), "länger als 64 KiB") {
		t.Errorf("jenseits der Grenze: ok %v, %v", ok, err)
	}
	// Genau MaxBytes ohne Schluss: zu lang, nicht "nicht geschlossen" — der
	// Rest ist unbekannt.
	_, ok, err = Parse([]byte(long[:MaxBytes]))
	if !ok || err == nil || !strings.Contains(err.Error(), "länger als 64 KiB") {
		t.Errorf("genau MaxBytes: ok %v, %v", ok, err)
	}
	// Ein --- ganz am Ende von genau MaxBytes ohne Zeilenende könnte
	// abgeschnitten sein: zu lang.
	cut := long[:MaxBytes-3] + "---"
	if _, ok, err = Parse([]byte(cut)); !ok || err == nil || !strings.Contains(err.Error(), "länger als 64 KiB") {
		t.Errorf("--- am Ende: ok %v, %v", ok, err)
	}
	// Schluss innerhalb der Grenze: gelesen, auch wenn dahinter viel mehr
	// kommt; nur der Anfang wird angesehen.
	short := "---\ntitle: x\n" + strings.Repeat("# k\n", 100) + "---\n"
	fm, ok, err := Parse([]byte(short + strings.Repeat("Text\n", 30000)))
	if !ok || err != nil || fm["title"] != "x" {
		t.Errorf("innerhalb der Grenze: ok %v, %v, %v", ok, err, fm)
	}
	// Schluss genau an der Grenze, mit Zeilenende: gelesen.
	pad := MaxBytes - len("---\ntitle: x\n") - len("---\n")
	exact := "---\ntitle: x\n" + strings.Repeat("#", pad-1) + "\n---\n"
	if len(exact) != MaxBytes {
		t.Fatalf("Testdaten: %d", len(exact))
	}
	if fm, ok, err = Parse([]byte(exact + "Text\n")); !ok || err != nil || fm["title"] != "x" {
		t.Errorf("genau an der Grenze: ok %v, %v, %v", ok, err, fm)
	}
}

func TestIsMarkdown(t *testing.T) {
	for name, want := range map[string]bool{"a.md": true, "A.MD": true, "dir/README.Md": true, ".md": true, "md": false,
		"a.markdown": false, "a.md.txt": false, "a.txt": false, "": false} {
		if got := IsMarkdown(name); got != want {
			t.Errorf("%q: %v", name, got)
		}
	}
}
