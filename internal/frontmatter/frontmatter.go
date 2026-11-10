// Package frontmatter liest das Frontmatter einer Markdown-Datei: ein Block
// ganz am Anfang des Inhalts zwischen zwei Zeilen, die genau `---` lauten,
// darin YAML. Das Paket ist neutral — es kennt weder Hub noch Node noch MCP;
// die Werkzeuge des Nodes (list, read; create und write für ihre Hinweise)
// nutzen es, später die Suche.
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
	body, ok, err := block(head)
	if !ok || err != nil {
		return nil, ok, err
	}
	doc, err := unmarshal(body)
	if err != nil {
		return nil, true, err
	}
	fm, err = object(doc)
	if err != nil {
		return nil, true, err
	}
	return fm, true, nil
}

// block schneidet den YAML-Text des Blocks aus head, ohne die beiden Zeilen
// `---`; ok und der Fehler wie bei Parse. Parse und Hints teilen ihn, damit
// beide denselben Ausschnitt sehen.
func block(head []byte) (body []byte, ok bool, err error) {
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
	body = rest
	for {
		line, after, eol := nextLine(rest)
		if string(line) == marker && (eol || !truncated) {
			return body[:len(body)-len(rest)], true, nil
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

// unmarshal liest das YAML des Blocks in den Knotenbaum. Parse geht über den
// Knotenbaum statt direkt nach any: So bleibt eine Zeitangabe der Text, wie
// er geschrieben steht (nach any würde `2026-09-27` ein time.Time und daraus
// `2026-09-27T00:00:00Z`), und Schlüssel lassen sich an ihrem Tag prüfen.
func unmarshal(body []byte) (*yaml.Node, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(body, &doc); err != nil {
		return nil, errors.New("YAML ungültig: " + strings.TrimPrefix(err.Error(), "yaml: "))
	}
	return &doc, nil
}

// object macht aus dem Knotenbaum des Blocks ein Objekt, das JSON werden
// kann; ein leerer Block ergibt ein leeres Objekt.
func object(doc *yaml.Node) (map[string]any, error) {
	if doc.Kind == 0 || len(doc.Content) == 0 {
		return map[string]any{}, nil
	}
	v, err := value(doc.Content[0], 0)
	if err != nil {
		return nil, err
	}
	switch x := v.(type) {
	case map[string]any:
		return x, nil
	case nil:
		return map[string]any{}, nil
	case []any:
		return nil, errors.New("oben steht kein Objekt, sondern eine Liste")
	}
	return nil, errors.New("oben steht kein Objekt, sondern ein einzelner Wert")
}

// Hints nennt die Werte im Frontmatter am Anfang von head, die an einem
// Kommentar enden: je Skalar, der als Wert eines Mappings im Plain-Stil
// (ohne Anführungszeichen) steht und auf seiner Zeile einen Kommentar trägt,
// einen Satz mit Zeile, Schlüssel und Kommentar. YAML schneidet einen
// unquotierten Wert an „ #“ ab; ein Titel wie `a (Todo #1)` wird so still zu
// `a (Todo`. Gewollte Kommentare bekommen den Hinweis ebenso.
//
// Die Zeile ist die Zeile in der Datei, ab 1 gezählt: Das öffnende `---` ist
// Zeile 1, eine BOM ändert daran nichts. Quotierte Werte, Kommentare auf
// eigener Zeile und Listeneinträge geben keinen Hinweis. Hat head kein
// Frontmatter oder lässt es sich nicht lesen (wie bei Parse), gibt es keine
// Hinweise.
func Hints(head []byte) []string {
	body, ok, err := block(head)
	if !ok || err != nil {
		return nil
	}
	doc, err := unmarshal(body)
	if err != nil {
		return nil
	}
	if _, err := object(doc); err != nil {
		return nil
	}
	var out []string
	for _, c := range doc.Content {
		out = commentHints(c, out)
	}
	return out
}

// commentHints hängt die Hinweise aus dem Knoten n und allem darunter an
// out an. Aliase verfolgt es nicht: Ihr Ziel steht anderswo im Block und
// wird dort gezählt. Die Tiefe ist begrenzt, weil object den Baum schon
// geprüft hat.
func commentHints(n *yaml.Node, out []string) []string {
	switch n.Kind {
	case yaml.SequenceNode:
		for _, c := range n.Content {
			out = commentHints(c, out)
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			if v.Kind == yaml.ScalarNode && v.Style == 0 && v.LineComment != "" {
				// Node.Line zählt ab der Zeile nach dem öffnenden ---.
				out = append(out, fmt.Sprintf("Zeile %d, %s: ‚%s‘ ist ein YAML-Kommentar, nicht Teil des Werts; "+
					"gehört er dazu, den Wert in doppelte Anführungszeichen setzen", v.Line+1, k.Value,
					strings.TrimSpace(v.LineComment)))
			}
			out = commentHints(v, out)
		}
	}
	return out
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

// Body liefert den Inhalt hinter dem Frontmatter: alles nach der Zeile `---`,
// die den Block schließt. Hat der Inhalt kein Frontmatter oder ist der Block
// nicht geschlossen (wie bei Parse, innerhalb von MaxBytes), ist es der ganze
// Inhalt ohne BOM. Für die Zerlegung in Abschnitte und die erste Überschrift
// eines Dokuments (Task 033).
func Body(content string) string {
	b := bytes.TrimPrefix([]byte(content), bom)
	line, rest, eol := nextLine(b)
	if string(line) != marker || !eol {
		return string(b)
	}
	read := len(b) - len(rest)
	for read <= MaxBytes {
		line, after, eol := nextLine(rest)
		if string(line) == marker {
			return string(after)
		}
		if !eol {
			break
		}
		read += len(rest) - len(after)
		rest = after
	}
	return string(b)
}
