package replica

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
)

// Suche im Volltextindex über docs/ (Task 033, index.go): FTS5 mit bm25(),
// Gewichte nach docs/indizierung.md, Entscheidung 12 — Titel, Überschriften
// und questions hoch, description und hints mittel, der Text normal.

// Gewichte der Spalten von sections_fts in bm25(), in ihrer Reihenfolge:
// title, headings, questions, description, hints, body. Nachzustellen nach
// Messung.
const (
	weightHigh   = 8.0
	weightMedium = 3.0
	weightNormal = 1.0
)

// MaxQueryWords begrenzt die Wörter einer Anfrage; der Rest fällt weg.
const MaxQueryWords = 32

// MaxHitsPerDocument ist, wie viele Abschnitte eines Dokuments eine Liste
// höchstens trägt (Entscheidung 21).
const MaxHitsPerDocument = 3

var qSearch = fmt.Sprintf(`SELECT s.name, s.anchor, s.heading, s.headings, s.title, s.description,
		snippet(sections_fts, 5, '', '', '…', 24)
	FROM sections_fts JOIN sections s ON s.id = sections_fts.rowid
	WHERE sections_fts MATCH ? AND s.collection = ? AND s.name >= ? AND s.name < ?
	ORDER BY bm25(sections_fts, %[1]g, %[1]g, %[1]g, %[2]g, %[2]g, %[3]g), s.name, s.ord`,
	weightHigh, weightMedium, weightNormal)

// Hit ist ein Treffer: ein Abschnitt mit den Feldern seines Dokuments.
type Hit struct {
	Name        string
	Anchor      string
	Heading     string
	Headings    []string
	Title       string
	Description string
	Excerpt     string
}

// MatchQuery bildet aus einfachen Wörtern die Anfrage an FTS5 (Entscheidung
// 22): je Wort aus Buchstaben und Ziffern ein String in Anführungszeichen,
// mit OR verbunden; alle Sonderzeichen fallen so aus der Syntax heraus.
// Ohne Wort leer.
func MatchQuery(query string) string {
	words := strings.FieldsFunc(query, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	if len(words) > MaxQueryWords {
		words = words[:MaxQueryWords]
	}
	for i, w := range words {
		words[i] = `"` + w + `"`
	}
	return strings.Join(words, " OR ")
}

// Search sucht in den Abschnitten einer Collection unter prefix (ein
// Verzeichnis mit '/' am Ende, mindestens docs/), nach Rang: höchstens limit
// Treffer, von einem Dokument höchstens MaxHitsPerDocument. match ist eine
// Anfrage aus MatchQuery.
func (r *Replica) Search(ctx context.Context, collection, prefix, match string, limit int) ([]Hit, error) {
	if match == "" || limit <= 0 {
		return []Hit{}, nil
	}
	hi := prefix[:len(prefix)-1] + "0"
	rows, err := r.db.QueryContext(ctx, qSearch, match, collection, prefix, hi)
	if err != nil {
		return nil, fmt.Errorf("Suche: %w", err)
	}
	defer rows.Close()
	out := []Hit{}
	perDoc := map[string]int{}
	seen := map[string]bool{}
	for rows.Next() && len(out) < limit {
		var h Hit
		var headings string
		if err := rows.Scan(&h.Name, &h.Anchor, &h.Heading, &headings, &h.Title, &h.Description, &h.Excerpt); err != nil {
			return nil, fmt.Errorf("Suche: %w", err)
		}
		key := h.Name + "#" + h.Anchor
		if seen[key] || perDoc[h.Name] >= MaxHitsPerDocument {
			continue
		}
		seen[key] = true
		perDoc[h.Name]++
		if err := json.Unmarshal([]byte(headings), &h.Headings); err != nil {
			return nil, fmt.Errorf("Suche: Überschriften von %s: %w", h.Name, err)
		}
		h.Excerpt = strings.Join(strings.Fields(h.Excerpt), " ")
		out = append(out, h)
	}
	return out, rows.Err()
}
