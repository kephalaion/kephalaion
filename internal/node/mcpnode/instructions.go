package mcpnode

import _ "embed"

// instructions ist die Anleitung für Clients (docs/begriffe.md,
// „instructions“; docs/konzept.md, „Anleitung für Clients“): der feste Text,
// den der Node in der Antwort auf initialize mitgibt — voll wie verdeckt
// derselbe, ohne Version und ohne Namen von Node, Hubs oder Accounts.
//
//go:embed instructions.md
var instructions string

// InstructionsLimit ist die Obergrenze der Anleitung in Bytes (UTF-8).
// Claude Code kürzt instructions ab v2.1.296 nach 4096 Zeichen, davor nach
// 2048; Bytes sind nie weniger als Zeichen, so kommt der Text auch bei älteren
// Fassungen ungekürzt an.
const InstructionsLimit = 2048

// Instructions liefert die Anleitung für Clients.
func Instructions() string { return instructions }
