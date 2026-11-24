package mcpnode

import (
	"encoding/json"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

// initializeOf liefert Version und instructions aus der Antwort auf
// initialize an base.
func initializeOf(t *testing.T, base string, header http.Header) (version, instructions string) {
	t.Helper()
	status, body := post(t, base, header, initializeBody)
	if status != http.StatusOK {
		t.Fatalf("initialize: HTTP %d %s", status, body)
	}
	var msg struct {
		Result struct {
			ServerInfo struct {
				Version string `json:"version"`
			} `json:"serverInfo"`
			Instructions string `json:"instructions"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(body), &msg); err != nil {
		t.Fatalf("initialize: %v %s", err, body)
	}
	return msg.Result.ServerInfo.Version, msg.Result.Instructions
}

// Die Anleitung für Clients: initialize liefert sie lokal, über den Proxy
// verdeckt und über den Proxy angemeldet gleich — den eingebetteten Text,
// unter der Grenze, ohne Version und ohne Namen von Node oder Hubs.
func TestInstructions(t *testing.T) {
	text := Instructions()
	if strings.TrimSpace(text) == "" || !utf8.ValidString(text) {
		t.Fatalf("Anleitung leer oder kein UTF-8: %q", text)
	}
	if len(text) > InstructionsLimit {
		t.Errorf("Anleitung hat %d Bytes, höchstens %d", len(text), InstructionsLimit)
	}
	aliases := []string{"lager", "werkstatt", "zentrale"}
	e := newProxyEnv(t, aliases...)
	cases := []struct {
		name, base  string
		header      http.Header
		wantVersion string
	}{
		{"lokal", e.direct, nil, proxyVersion},
		{"verdeckt", e.proxy, nil, ""},
		{"verdeckt, falsches Token", e.proxy, e.wrong(t, "zentrale"), ""},
		{"über den Proxy angemeldet", e.proxy, e.valid("zentrale"), proxyVersion},
	}
	for _, c := range cases {
		version, got := initializeOf(t, c.base, c.header)
		if version != c.wantVersion {
			t.Errorf("%s: Version %q, erwartet %q", c.name, version, c.wantVersion)
		}
		if got != text {
			t.Errorf("%s: instructions weichen ab:\n%s", c.name, got)
		}
	}
	checkHidden(t, "Anleitung", text, aliases)
	for _, s := range []string{"anna", "wissen", "privat", "notizen", "kisten"} {
		if regexp.MustCompile(`\b` + s + `\b`).MatchString(text) {
			t.Errorf("Anleitung nennt %q aus dem Test-Setup", s)
		}
	}
}

// codeSpan ist ein Abschnitt in Backticks.
var codeSpan = regexp.MustCompile("`([^`]+)`")

// instructionsNames liefert, was die Anleitung in Backticks nennt: Wörter
// (a–z, _) und Kommandos (kephalaion …), je ohne Doppelte.
func instructionsNames(text string) (words, commands []string) {
	word := regexp.MustCompile(`^[a-z_]+$`)
	for _, m := range codeSpan.FindAllStringSubmatch(text, -1) {
		s := m[1]
		switch {
		case strings.HasPrefix(s, "kephalaion "):
			if !slices.Contains(commands, s) {
				commands = append(commands, s)
			}
		case word.MatchString(s):
			if !slices.Contains(words, s) {
				words = append(words, s)
			}
		}
	}
	return words, commands
}

// Jedes Wort in Backticks ist ein registriertes Werkzeug (wantTools, die
// Liste aus tools/list) oder ein Feld bzw. Wert aus whoami; ein Werkzeug
// außerhalb von Backticks gilt nicht. Die Kommandos prüft cmd/kephalaion
// (TestInstructionsCommands).
func TestInstructionsNamesTools(t *testing.T) {
	words, commands := instructionsNames(Instructions())
	fields := []string{"login", LoginMissing, LoginInvalid, CodeNeverSynced}
	var tools []string
	for _, w := range words {
		switch {
		case slices.Contains(wantTools, w):
			tools = append(tools, w)
		case !slices.Contains(fields, w):
			t.Errorf("`%s` ist weder ein Werkzeug noch ein Feld aus whoami", w)
		}
	}
	// whoami, sync und die Werkzeuge, die lesen und schreiben, nennt sie.
	for _, w := range []string{"whoami", "sync", "list", "read", "search", "changes", "create", "write", "delete",
		"rename"} {
		if !slices.Contains(tools, w) {
			t.Errorf("Anleitung nennt `%s` nicht", w)
		}
	}
	// Ein Werkzeug ohne Backticks entginge der Prüfung oben.
	plain := codeSpan.ReplaceAllString(Instructions(), "")
	for _, w := range wantTools {
		if regexp.MustCompile(`\b` + w + `\b`).MatchString(plain) {
			t.Errorf("Werkzeug %s ohne Backticks", w)
		}
	}
	if len(commands) == 0 {
		t.Error("Anleitung nennt kein Kommando")
	}
}
