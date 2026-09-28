// Package frontmatter liest das Frontmatter einer Markdown-Datei: ein Block
// ganz am Anfang des Inhalts zwischen zwei Zeilen, die genau `---` lauten,
// darin YAML. Das Paket ist neutral — es kennt weder Hub noch Node noch MCP;
// die Werkzeuge des Nodes (list, read) nutzen es, später die Suche.
//
// Nur diese Schreibweise gilt: kein TOML (`+++`), kein JSON. Steht der Block
// nicht ganz am Anfang, hat der Inhalt kein Frontmatter. Das Frontmatter
// bleibt Teil des Inhalts; hier wird es nur gelesen.
package frontmatter

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"strings"

	"go.yaml.in/yaml/v3"
)

// MaxBytes ist, wie viel vom Anfang eines Inhalts Parse ansieht: 64 KiB.
// Endet der Block nicht darin, ist das ein Fehler.
const MaxBytes = 64 * 1024

// marker ist die Zeile, die den Block öffnet und schließt.
const marker = "---"

// bom ist die Byte-Order-Mark von UTF-8; sie darf vor dem Block stehen.
var bom = []byte("\xef\xbb\xbf")

// Parse liest das Frontmatter am Anfang von head. head ist der Anfang eines
// Inhalts; mehr als MaxBytes sieht Parse nicht an. ok ist false, wenn der
// Inhalt keines hat: Er beginnt nicht (nach einer BOM) mit einer Zeile, die
// genau `---` lautet. Ein Fehler nennt kurz den Grund — nicht geschlossen,
// länger als MaxBytes, ungültiges YAML, oben kein Objekt, ein Schlüssel, der
// kein Text ist, ein Wert, der kein JSON werden kann; ok ist dann true.
//
// Das Ergebnis ist als JSON darstellbar: Objekte als map[string]any, Listen
// als []any, Zeitangaben als Text, Zahlen und Wahrheitswerte wie in YAML. Ein
// leerer Block ergibt ein leeres Objekt. Zeilen enden mit \n oder \r\n.
func Parse(head []byte) (fm map[string]any, ok bool, err error) {
	// Genau MaxBytes können ein abgeschnittener Anfang sein: Ein `---` ganz
	// am Ende ohne Zeilenende zählt dann nicht als Schluss.
	truncated := len(head) >= MaxBytes
	if len(head) > MaxBytes {
		head = head[:MaxBytes]
	}
	head = bytes.TrimPrefix(head, bom)
	line, rest, _ := nextLine(head)
	if string(line) != marker {
		return nil, false, nil
	}
	body := rest
	for {
		line, after, eol := nextLine(rest)
		if string(line) == marker && (eol || !truncated) {
			return decode(body[:len(body)-len(rest)])
		}
		if !eol {
			if truncated {
				return nil, true, fmt.Errorf("Frontmatter länger als %d KiB", MaxBytes/1024)
			}
			return nil, true, errors.New("Frontmatter nicht geschlossen: keine zweite Zeile ---")
		}
		rest = after
	}
}

// nextLine trennt die erste Zeile von b ab: line ohne Zeilenende (\n oder
// \r\n), rest dahinter, eol false, wenn b ohne Zeilenende endet — line ist
// dann der ganze Rest.
func nextLine(b []byte) (line, rest []byte, eol bool) {
	i := bytes.IndexByte(b, '\n')
	if i < 0 {
		return b, nil, false
	}
	return bytes.TrimSuffix(b[:i], []byte("\r")), b[i+1:], true
}

// decode liest das YAML des Blocks und macht daraus ein Objekt, das JSON
// werden kann. Es geht über den Knotenbaum statt direkt nach any: So bleibt
// eine Zeitangabe der Text, wie er geschrieben steht (nach any würde
// `2026-09-27` ein time.Time und daraus `2026-09-27T00:00:00Z`), und
// Schlüssel lassen sich an ihrem Tag prüfen.
func decode(body []byte) (map[string]any, bool, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(body, &doc); err != nil {
		return nil, true, errors.New("YAML ungültig: " + strings.TrimPrefix(err.Error(), "yaml: "))
	}
	if doc.Kind == 0 || len(doc.Content) == 0 {
		return map[string]any{}, true, nil
	}
	v, err := value(doc.Content[0], 0)
	if err != nil {
		return nil, true, err
	}
	switch x := v.(type) {
	case map[string]any:
		return x, true, nil
	case nil:
		return map[string]any{}, true, nil
	case []any:
		return nil, true, errors.New("oben steht kein Objekt, sondern eine Liste")
	}
	return nil, true, errors.New("oben steht kein Objekt, sondern ein einzelner Wert")
}

// maxDepth begrenzt die Verschachtelung; ein Alias, der auf sich selbst
// zeigt, liefe sonst endlos.
const maxDepth = 100

// value macht aus einem Knoten einen JSON-tauglichen Wert: Objekte mit
// Text-Schlüsseln (map[string]any), Listen ([]any), Zeitangaben und alles mit
// fremdem Tag als Text, Zahlen, Wahrheitswerte und null wie in YAML.
func value(n *yaml.Node, depth int) (any, error) {
	if depth > maxDepth {
		return nil, errors.New("zu tief verschachtelt")
	}
	switch n.Kind {
	case yaml.DocumentNode:
		if len(n.Content) == 0 {
			return nil, nil
		}
		return value(n.Content[0], depth+1)
	case yaml.AliasNode:
		return value(n.Alias, depth+1)
	case yaml.MappingNode:
		out := make(map[string]any, len(n.Content)/2)
		for i := 0; i+1 < len(n.Content); i += 2 {
			k := n.Content[i]
			if k.Kind != yaml.ScalarNode || k.ShortTag() != "!!str" {
				return nil, fmt.Errorf("Schlüssel %s ist kein Text", k.Value)
			}
			if _, dup := out[k.Value]; dup {
				return nil, fmt.Errorf("Schlüssel %s steht doppelt", k.Value)
			}
			v, err := value(n.Content[i+1], depth+1)
			if err != nil {
				return nil, err
			}
			out[k.Value] = v
		}
		return out, nil
	case yaml.SequenceNode:
		out := make([]any, 0, len(n.Content))
		for _, c := range n.Content {
			v, err := value(c, depth+1)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, nil
	}
	return scalar(n)
}

// scalar ist der Wert eines Skalars: null, Wahrheitswert, Zahl oder Text.
// Zeitangaben und fremde Tags bleiben der geschriebene Text.
func scalar(n *yaml.Node) (any, error) {
	switch n.ShortTag() {
	case "!!null":
		return nil, nil
	case "!!bool", "!!int", "!!float":
		var v any
		if err := n.Decode(&v); err != nil {
			return nil, errors.New("YAML ungültig: " + strings.TrimPrefix(err.Error(), "yaml: "))
		}
		if f, isFloat := v.(float64); isFloat && (math.IsInf(f, 0) || math.IsNaN(f)) {
			return nil, fmt.Errorf("Wert %s lässt sich nicht als JSON darstellen", n.Value)
		}
		return v, nil
	}
	return n.Value, nil
}

// IsMarkdown sagt, ob ein Name auf .md endet, ohne Unterscheidung von Groß-
// und Kleinschreibung — nur solche Dokumente haben ein Frontmatter.
func IsMarkdown(name string) bool {
	return len(name) >= 3 && strings.EqualFold(name[len(name)-3:], ".md")
}
