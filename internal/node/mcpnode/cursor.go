package mcpnode

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"
)

// Cursor von list und changes: JSON in base64url ohne Padding. Für den
// Client undurchsichtig; er gibt ihn unverändert zurück. Er trägt keine
// Geheimnisse — nur Namen, Revisionen und die generation einer Replica —,
// und der Node vertraut ihm nichts an, was er nicht bei jedem Aufruf neu
// prüft: Ein gefälschter Cursor öffnet keine Collection, die der Client nicht
// lesen darf.

// cursorVersion ist die Fassung des Formats; ein Cursor anderer Fassung ist
// ungültig.
const cursorVersion = 1

func encodeCursor(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		// Nur feste Typen dieses Pakets: kann nicht scheitern.
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// decodeCursor liest einen Cursor nach v; die Fassung prüft der Aufrufer.
func decodeCursor(s string, v any) error {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err == nil {
		err = json.Unmarshal(b, v)
	}
	if err != nil {
		return &toolError{msg: "cursor ungültig: nicht aus einer Antwort dieses Werkzeugs"}
	}
	return nil
}

// errCursorMismatch meldet einen Cursor, der zu einer anderen Anfrage gehört.
var errCursorMismatch = &toolError{msg: "cursor gehört zu einer anderen Anfrage: collection, path und die übrigen " +
	"Angaben müssen gleich bleiben"}

// fingerprint fasst die Angaben einer Anfrage zusammen, die ein Cursor
// festhält: 16 Hex-Zeichen von sha256.
func fingerprint(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:8])
}

// Stamp ist ein Zeitpunkt mit dem, der ihn gesetzt hat: wann (RFC 3339,
// UTC, auf Millisekunden) und von wem (User).
type Stamp struct {
	At string `json:"at"`
	By string `json:"by"`
}

// stamp macht aus Millisekunden seit Epoche und User einen Stamp.
func stamp(ms int64, by string) *Stamp { return &Stamp{At: formatMillis(ms), By: by} }

// formatMillis ist ein Zeitpunkt in RFC 3339, UTC, mit Millisekunden — so
// genau, wie der Hub ihn führt.
func formatMillis(ms int64) string {
	return time.UnixMilli(ms).UTC().Format("2006-01-02T15:04:05.000Z07:00")
}
