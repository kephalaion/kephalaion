package main

import (
	"bytes"
	"regexp"
	"strings"
	"testing"

	"github.com/kephalaion/kephalaion/internal/node/mcpnode"
)

// Jedes Kommando, das die Anleitung für Clients in Backticks nennt
// (`kephalaion node …`), gibt es: Mit --help endet es mit 0, ohne
// „Unbekanntes Kommando“.
func TestInstructionsCommands(t *testing.T) {
	var commands []string
	for _, m := range regexp.MustCompile("`(kephalaion [^`]+)`").FindAllStringSubmatch(mcpnode.Instructions(), -1) {
		commands = append(commands, m[1])
	}
	if len(commands) == 0 {
		t.Fatal("die Anleitung nennt kein Kommando")
	}
	for _, c := range commands {
		args := append(strings.Fields(c)[1:], "--help")
		var out, errOut bytes.Buffer
		code := run(args, strings.NewReader(""), &out, &errOut)
		if code != 0 || strings.Contains(errOut.String(), "Unbekannt") {
			t.Errorf("%s: Exit %d\n%s%s", c, code, out.String(), errOut.String())
		}
	}
}
