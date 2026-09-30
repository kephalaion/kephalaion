package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Dummy-Tokens im Format keph_<32 Bytes base64url>; sie gelten nirgends.
var (
	dummyTokenA = "keph_" + strings.Repeat("A", 43)
	dummyTokenB = "keph_" + strings.Repeat("B", 42) + "A"
	dummyTokenC = "keph_" + strings.Repeat("C", 42) + "A"
)

// leaksToken sagt, ob ein Text eines der Dummy-Tokens trägt. Das Präfix keph_
// allein ist keines: Meldungen nennen es.
func leaksToken(s string) bool {
	for _, tok := range []string{dummyTokenA, dummyTokenB, dummyTokenC} {
		if strings.Contains(s, strings.TrimPrefix(tok, "keph_")) {
			return true
		}
	}
	return false
}

// tokensEnv ist ein temporäres HOME mit dem Verzeichnis der Token-Dateien.
type tokensEnv struct {
	home   string
	tokens string
}

func newTokensEnv(t *testing.T) *tokensEnv {
	t.Helper()
	home := isolate(t)
	return &tokensEnv{home: home, tokens: filepath.Join(home, "config", "kephalaion", "tokens")}
}

// put schreibt tokens/<hub>/<file> und liefert den Pfad.
func (e *tokensEnv) put(t *testing.T, hub, file, content string) string {
	t.Helper()
	dir := filepath.Join(e.tokens, hub)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, file)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// headers ruft node mcp headers auf und prüft die Zusage: Exit 0, gültiges
// JSON aus Texten auf stdout, kein Token auf stderr.
func (e *tokensEnv) headers(t *testing.T, args ...string) (map[string]string, string) {
	t.Helper()
	r := runT(t, append([]string{"node", "mcp", "headers"}, args...)...)
	if r.code != 0 {
		t.Fatalf("Exit-Code %d, erwartet 0\nstderr:\n%s", r.code, r.errOut)
	}
	var got map[string]string
	if err := json.Unmarshal([]byte(r.out), &got); err != nil {
		t.Fatalf("kein gültiges JSON: %v\n%s", err, r.out)
	}
	if !strings.HasSuffix(r.out, "}\n") || strings.Count(r.out, "\n") != 1 {
		t.Fatalf("erwartet ein JSON-Objekt in einer Zeile:\n%q", r.out)
	}
	if leaksToken(r.errOut) {
		t.Fatalf("Token auf stderr:\n%s", r.errOut)
	}
	return got, r.errOut
}

func wantHeaders(t *testing.T, got map[string]string, want map[string]string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Header-Paare:\n%v\nerwartet:\n%v", got, want)
	}
}

func TestNodeMCPHeaders(t *testing.T) {
	e := newTokensEnv(t)

	// Keine Tokens: {} — das Verzeichnis fehlt, dann ist es leer.
	got, errOut := e.headers(t)
	wantHeaders(t, got, map[string]string{})
	if errOut != "" {
		t.Errorf("ohne Tokens eine Meldung:\n%s", errOut)
	}
	if err := os.MkdirAll(e.tokens, 0o700); err != nil {
		t.Fatal(err)
	}
	got, _ = e.headers(t)
	wantHeaders(t, got, map[string]string{})

	// Ein Hub, eine Token-Datei; .pending zählt nicht.
	e.put(t, "vm", "alice.token", dummyTokenA+"\n")
	e.put(t, "vm", "alice.token.pending", dummyTokenC+"\n")
	got, errOut = e.headers(t)
	wantHeaders(t, got, map[string]string{"X-Keph-Account-vm": "alice", "X-Keph-Token-vm": dummyTokenA})
	if errOut != "" {
		t.Errorf("ein Hub, eine Datei: Meldung auf stderr:\n%s", errOut)
	}

	// Mehrere Hubs.
	e.put(t, "eigen", "kp.token", dummyTokenB+"\n")
	both := map[string]string{
		"X-Keph-Account-eigen": "kp", "X-Keph-Token-eigen": dummyTokenB,
		"X-Keph-Account-vm": "alice", "X-Keph-Token-vm": dummyTokenA,
	}
	got, _ = e.headers(t)
	wantHeaders(t, got, both)

	// Mehrere Accounts ohne Wahl: Der Hub fehlt, die übrigen bleiben; stderr
	// nennt ihn und die Accounts.
	e.put(t, "vm", "bob.token", dummyTokenC+"\n")
	got, errOut = e.headers(t)
	wantHeaders(t, got, map[string]string{"X-Keph-Account-eigen": "kp", "X-Keph-Token-eigen": dummyTokenB})
	for _, w := range []string{"Hub vm", "alice, bob", "keiner gewählt"} {
		if !strings.Contains(errOut, w) {
			t.Errorf("stderr ohne %q:\n%s", w, errOut)
		}
	}

	// Mit Wahl.
	got, errOut = e.headers(t, "--account", "vm=alice")
	wantHeaders(t, got, both)
	if errOut != "" {
		t.Errorf("mit Wahl eine Meldung:\n%s", errOut)
	}
	got, _ = e.headers(t, "--account", "vm=bob", "--account", "eigen=kp")
	wantHeaders(t, got, map[string]string{
		"X-Keph-Account-eigen": "kp", "X-Keph-Token-eigen": dummyTokenB,
		"X-Keph-Account-vm": "bob", "X-Keph-Token-vm": dummyTokenC,
	})

	// Die gewählte Datei fehlt: Der Hub fehlt — auch wenn eine andere Datei da
	// ist, und auch ein Hub ganz ohne Verzeichnis.
	got, errOut = e.headers(t, "--account", "vm=alice", "--account", "eigen=niemand", "--account", "fern=x")
	wantHeaders(t, got, map[string]string{"X-Keph-Account-vm": "alice", "X-Keph-Token-vm": dummyTokenA})
	for _, w := range []string{"Hub eigen", "niemand", "Hub fern"} {
		if !strings.Contains(errOut, w) {
			t.Errorf("stderr ohne %q:\n%s", w, errOut)
		}
	}

	// Eine Token-Datei ohne Token: Der Hub fehlt, der Inhalt steht nirgends.
	e.put(t, "eigen", "kp.token", "nicht geheim genug\n")
	got, errOut = e.headers(t, "--account", "vm=alice")
	wantHeaders(t, got, map[string]string{"X-Keph-Account-vm": "alice", "X-Keph-Token-vm": dummyTokenA})
	if !strings.Contains(errOut, "Hub eigen") || strings.Contains(errOut, "geheim") {
		t.Errorf("stderr:\n%s", errOut)
	}
	e.put(t, "eigen", "kp.token", dummyTokenB+"\n")

	// --tokens-dir nennt ein anderes Verzeichnis, auch relativ.
	other := filepath.Join(e.home, "andere")
	if err := os.MkdirAll(filepath.Join(other, "fern"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, "fern", "carol.token"), []byte(dummyTokenC+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, _ = e.headers(t, "--tokens-dir", other)
	wantHeaders(t, got, map[string]string{"X-Keph-Account-fern": "carol", "X-Keph-Token-fern": dummyTokenC})
	got, _ = e.headers(t, "--tokens-dir", filepath.Join(e.home, "gibt-es-nicht"))
	wantHeaders(t, got, map[string]string{})
}

// Eine unlesbare Token-Datei und ein unlesbares Verzeichnis: Exit 0, gültiges
// JSON, der Hub fehlt.
func TestNodeMCPHeadersUnreadable(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root liest jede Datei")
	}
	e := newTokensEnv(t)
	e.put(t, "vm", "alice.token", dummyTokenA+"\n")
	locked := e.put(t, "eigen", "kp.token", dummyTokenB+"\n")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	got, errOut := e.headers(t)
	wantHeaders(t, got, map[string]string{"X-Keph-Account-vm": "alice", "X-Keph-Token-vm": dummyTokenA})
	if !strings.Contains(errOut, "Hub eigen") {
		t.Errorf("stderr nennt den Hub nicht:\n%s", errOut)
	}
	// Das Verzeichnis eines Hubs ist unlesbar.
	if err := os.Chmod(locked, 0o600); err != nil {
		t.Fatal(err)
	}
	hubDir := filepath.Dir(locked)
	if err := os.Chmod(hubDir, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(hubDir, 0o700) })
	got, errOut = e.headers(t)
	wantHeaders(t, got, map[string]string{"X-Keph-Account-vm": "alice", "X-Keph-Token-vm": dummyTokenA})
	if !strings.Contains(errOut, "Hub eigen") {
		t.Errorf("stderr nennt den Hub nicht:\n%s", errOut)
	}
	// tokens/ selbst ist unlesbar: {}.
	if err := os.Chmod(e.tokens, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(e.tokens, 0o700) })
	got, errOut = e.headers(t)
	wantHeaders(t, got, map[string]string{})
	if errOut == "" {
		t.Error("unlesbares tokens/ ohne Meldung")
	}
}

// Im Terminal gibt headers nichts aus: Es ist die einzige Ausgabe mit Token.
func TestNodeMCPHeadersTerminal(t *testing.T) {
	e := newTokensEnv(t)
	e.put(t, "vm", "alice.token", dummyTokenA+"\n")
	old := stdoutIsTerminal
	stdoutIsTerminal = func(io.Writer) bool { return true }
	t.Cleanup(func() { stdoutIsTerminal = old })
	r := runT(t, "node", "mcp", "headers")
	r.want(t, 1, "Terminal", "jq -r 'keys[]'")
	if r.out != "" || leaksToken(r.errOut) {
		t.Fatalf("Ausgabe im Terminal:\nstdout:\n%s\nstderr:\n%s", r.out, r.errOut)
	}
	// Ein falscher Aufruf bleibt ein falscher Aufruf.
	runT(t, "node", "mcp", "headers", "--account", "vm").want(t, 2, "<hub>=<account>")
}

func TestNodeMCPHeadersUsage(t *testing.T) {
	e := newTokensEnv(t)
	e.put(t, "vm", "alice.token", dummyTokenA+"\n")
	for _, args := range [][]string{
		{"--account", "vm"},
		{"--account", "vm=alice", "--account", "vm=bob"},
		{"--account", "VM=alice"},
		{"zuviel"},
		{"--gibt-es-nicht"},
	} {
		r := runT(t, append([]string{"node", "mcp", "headers"}, args...)...)
		if r.code != 2 || r.out != "" {
			t.Errorf("headers %v: Exit %d, stdout %q; erwartet 2 ohne Ausgabe", args, r.code, r.out)
		}
		if leaksToken(r.errOut) {
			t.Errorf("headers %v: Token auf stderr", args)
		}
	}
	runT(t, "node", "mcp", "headers", "--help").want(t, 0, "Ein Agent ruft", "--tokens-dir")
	runT(t, "node", "mcp").want(t, 2, "kephalaion node mcp headers")
	runT(t, "node", "mcp", "gibtsnicht").want(t, 2, "Unbekanntes Kommando: node mcp gibtsnicht")
}

// Ein Writer, der kein Terminal ist, gilt nicht als eines: ein Puffer, eine
// Pipe, /dev/null.
func TestStdoutIsTerminal(t *testing.T) {
	if stdoutIsTerminal(io.Discard) {
		t.Error("io.Discard gilt als Terminal")
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	if stdoutIsTerminal(w) {
		t.Error("eine Pipe gilt als Terminal")
	}
	null, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer null.Close()
	if stdoutIsTerminal(null) {
		t.Error("/dev/null gilt als Terminal")
	}
}
