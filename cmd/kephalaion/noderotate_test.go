package main

import (
	"context"
	"crypto/x509"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kephalaion/kephalaion/internal/contract"
	"github.com/kephalaion/kephalaion/internal/contract/httpapi"
	"github.com/kephalaion/kephalaion/internal/ident"
	nodestore "github.com/kephalaion/kephalaion/internal/node/store"
	"github.com/kephalaion/kephalaion/internal/testcert"
)

// rotateHook spielt am Weg zum Hub einen Ausgang von node_rotate nach: lost
// führt aus und meldet dann unklar, unclear meldet unklar ohne auszuführen,
// old ist ein Hub vor Task 034; sonst geht es durch. calls zählt die
// Aufrufe.
type rotateHook struct {
	contract.Hub
	mode  *string
	calls *atomic.Int32
}

func (h rotateHook) RotateNode(ctx context.Context, req contract.NodeRotateRequest) (contract.NodeRotateResponse, error) {
	h.calls.Add(1)
	switch *h.mode {
	case "lost":
		if _, err := h.Hub.RotateNode(ctx, req); err != nil {
			return contract.NodeRotateResponse{}, err
		}
		return contract.NodeRotateResponse{}, contract.ErrOutcomeUnknown
	case "unclear":
		return contract.NodeRotateResponse{}, fmt.Errorf("%w: Zeitüberschreitung", contract.ErrOutcomeUnknown)
	case "old":
		return contract.NodeRotateResponse{}, fmt.Errorf("%w: unbekannter Vorgang", contract.ErrUnknownOperation)
	}
	return h.Hub.RotateNode(ctx, req)
}

// hookRotate lenkt den Weg über HTTP durch rotateHook; mode stellt der Test
// um.
func hookRotate(t *testing.T) (mode *string, calls *atomic.Int32) {
	t.Helper()
	mode, calls = new(string), new(atomic.Int32)
	hookHTTP(t, func(address string, rootCAs *x509.CertPool) (contract.Hub, error) {
		c, err := httpapi.NewClient(address, rootCAs)
		return rotateHook{Hub: c, mode: mode, calls: calls}, err
	})
	return mode, calls
}

// entry liest einen Hub-Eintrag des Nodes.
func (e *commEnv) entry(t *testing.T, alias string) nodestore.Hub {
	t.Helper()
	h, err := nodeStore(t, e.cfg).Hub(context.Background(), alias)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// pendingOf ist der Ort der .pending eines Eintrags.
func (e *commEnv) pendingOf(t *testing.T, alias string) string {
	t.Helper()
	return nodeStore(t, e.cfg).PendingTokenPath(e.entry(t, alias).EntryID)
}

// writePendingFor merkt für einen Eintrag ein Token vor, wie es ein
// abgebrochenes node_rotate hinterließe.
func (e *commEnv) writePendingFor(t *testing.T, alias, token string) string {
	t.Helper()
	h := e.entry(t, alias)
	p := nodeStore(t, e.cfg).PendingTokenPath(h.EntryID)
	if err := nodestore.WritePendingToken(p, h.EntryID, token); err != nil {
		t.Fatal(err)
	}
	return p
}

func pendingToken(t *testing.T, e *commEnv, alias string) string {
	t.Helper()
	h := e.entry(t, alias)
	tok, err := nodestore.ReadPendingToken(nodeStore(t, e.cfg).PendingTokenPath(h.EntryID), h.EntryID)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

// noTokens prüft, dass eine Ausgabe keines der Tokens ungekürzt nennt.
func noTokens(t *testing.T, r result, tokens ...string) {
	t.Helper()
	for _, tok := range tokens {
		if tok != "" && strings.Contains(r.out+r.errOut, tok) {
			t.Fatalf("Ausgabe nennt ein Token ungekürzt:\n%s%s", r.out, r.errOut)
		}
	}
}

func newToken(t *testing.T) string {
	t.Helper()
	tok, err := ident.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

// node hub rotate über local und http: rotiert, das bisherige gilt nicht
// mehr, der Abgleich geht mit dem neuen; ssh abgelehnt, ohne .pending; ein
// inzwischen ersetztes Token lehnt der Hub ab.
func TestNodeHubRotate(t *testing.T) {
	e := newCommEnv(t)
	for alias, node := range map[string]string{"eigen": "laptop", "fern": "laptop-http"} {
		before := e.entry(t, alias).Token
		r := e.run(t, "node", "hub", "rotate", alias)
		r.want(t, 0, "Token rotiert und in node.db übernommen", ident.MaskToken(e.entry(t, alias).Token))
		after := e.entry(t, alias).Token
		noTokens(t, r, before, after)
		if after == before || !nodeHashOK(t, e.cfg, node, after) {
			t.Errorf("%s: Token nicht rotiert", alias)
		}
		if exists(e.pendingOf(t, alias)) {
			t.Errorf("%s: .pending nach Erfolg", alias)
		}
		e.run(t, "node", "sync", alias).want(t, 0)
	}

	// ssh: noch nicht unterstützt, keine .pending.
	e.runIn(t, newToken(t), "node", "hub", "add", "weit", "--node", "x", "--transport", "ssh", "--address", "h",
		"--token-stdin").want(t, 0)
	e.run(t, "node", "hub", "rotate", "weit").want(t, 1, "noch nicht unterstützt")
	if exists(e.pendingOf(t, "weit")) {
		t.Error("ssh: .pending angelegt")
	}

	// Abgelehnt: Der Admin hat ein neues Token erzeugt.
	e.run(t, "hub", "node", "token", "laptop-http").want(t, 0)
	before := e.entry(t, "fern").Token
	r := e.run(t, "node", "hub", "rotate", "fern")
	r.want(t, 1, "der Hub lehnt ab", "ist gelöscht", "hub node token laptop-http")
	noTokens(t, r, before)
	if exists(e.pendingOf(t, "fern")) || e.entry(t, "fern").Token != before {
		t.Error("nach Ablehnung: .pending liegt oder Token geändert")
	}
	e.run(t, "node", "hub", "rotate", "gibtsnicht").want(t, 1, "gibt es nicht")
	e.run(t, "node", "hub", "rotate").want(t, 2, "Es fehlt: <alias>")
}

// Unklarer Ausgang: die .pending bleibt, der nächste Aufruf klärt sie ohne
// zu rotieren — übernommen, wenn sie gilt; verworfen, wenn das gespeicherte
// gilt (Exit 3); liegen gelassen, wenn keins gilt.
func TestNodeHubRotatePending(t *testing.T) {
	e := newCommEnv(t)
	mode, calls := hookRotate(t)

	// Der Hub hat rotiert, die Antwort ging verloren.
	*mode = "lost"
	before := e.entry(t, "fern").Token
	r := e.run(t, "node", "hub", "rotate", "fern")
	r.want(t, 1, "Ausgang unklar", "bleibt liegen")
	pending := pendingToken(t, e, "fern")
	noTokens(t, r, before, pending)
	if e.entry(t, "fern").Token != before || !nodeHashOK(t, e.cfg, "laptop-http", pending) {
		t.Fatal("Stand nach verlorener Antwort falsch")
	}
	*mode = ""
	calls.Store(0)
	r = e.run(t, "node", "hub", "rotate", "fern")
	r.want(t, 0, "liegt von einem früheren node_rotate", "gilt", "übernommen")
	noTokens(t, r, before, pending)
	if calls.Load() != 0 {
		t.Errorf("beim Klären %d-mal rotiert", calls.Load())
	}
	if e.entry(t, "fern").Token != pending || exists(e.pendingOf(t, "fern")) {
		t.Error("die gültige .pending ist nicht übernommen")
	}

	// Unklar, der Hub hat nicht rotiert: verworfen, Exit 3, dann rotieren.
	*mode = "unclear"
	before = e.entry(t, "fern").Token
	e.run(t, "node", "hub", "rotate", "fern").want(t, 1, "Ausgang unklar")
	*mode = ""
	calls.Store(0)
	r = e.run(t, "node", "hub", "rotate", "fern")
	r.want(t, exitRetry, "gilt nicht", "verworfen", "erneut aufrufen")
	if calls.Load() != 0 || exists(e.pendingOf(t, "fern")) || e.entry(t, "fern").Token != before {
		t.Error("nach dem Verwerfen: rotiert, .pending liegt oder Token geändert")
	}
	e.run(t, "node", "hub", "rotate", "fern").want(t, 0, "Token rotiert")

	// Keins gilt: Die .pending bleibt, mit dem Hinweis auf hub node token.
	*mode = "unclear"
	e.run(t, "node", "hub", "rotate", "fern").want(t, 1, "Ausgang unklar")
	pending = pendingToken(t, e, "fern")
	*mode = ""
	e.run(t, "hub", "node", "token", "laptop-http").want(t, 0)
	r = e.run(t, "node", "hub", "rotate", "fern")
	r.want(t, 1, "weder das Token", "bleibt liegen", "hub node token laptop-http")
	if pendingToken(t, e, "fern") != pending {
		t.Error("die ungültige .pending ist weg, obwohl kein anderes Token gilt")
	}
}

// Ein Hub ohne node_rotate: Abbruch mit Hinweis, .pending gelöscht, der
// Eintrag unverändert und weiter gültig.
func TestNodeHubRotateOldHub(t *testing.T) {
	e := newCommEnv(t)
	mode, _ := hookRotate(t)
	*mode = "old"
	before := e.entry(t, "fern").Token
	e.run(t, "node", "hub", "rotate", "fern").want(t, 1, "kennt node_rotate noch nicht", "erst den Hub aktualisieren",
		"ist gelöscht")
	if exists(e.pendingOf(t, "fern")) || e.entry(t, "fern").Token != before || !nodeHashOK(t, e.cfg, "laptop-http", before) {
		t.Error("nach einem Hub ohne node_rotate: .pending liegt oder Token geändert")
	}
}

// Über https mit eigener CA.
func TestNodeHubRotateHTTPS(t *testing.T) {
	e := newCommEnv(t)
	ca := testcert.NewCA(t, "Test-CA")
	srv := proxyHub(t, e, serverCert(ca.ServerNow(t, "127.0.0.1")), "")
	e.addTLSHub(t, "sicher", srv.URL+"/hub", ca.PEM)
	before := e.entry(t, "sicher").Token
	r := e.run(t, "node", "hub", "rotate", "sicher")
	r.want(t, 0, "Token rotiert")
	after := e.entry(t, "sicher").Token
	noTokens(t, r, before, after)
	if after == before || !nodeHashOK(t, e.cfg, "laptop-tls", after) {
		t.Error("https: nicht rotiert")
	}
	e.run(t, "node", "hub", "check", "sicher").want(t, 0, "erreichbar")
}

// Je Eintrag eine eigene .pending; rm und import räumen sie ab, der Export
// nimmt sie nicht mit, und nach rm und add unter demselben Alias gilt eine
// alte nicht.
func TestNodeHubPendingPerEntry(t *testing.T) {
	e := newCommEnv(t)
	mode, _ := hookRotate(t)

	// fern: gültig vorgemerkt (Antwort verloren); eigen: ungültig vorgemerkt.
	*mode = "lost"
	e.run(t, "node", "hub", "rotate", "fern").want(t, 1, "Ausgang unklar")
	*mode = ""
	eigenPending := e.writePendingFor(t, "eigen", newToken(t))
	if e.pendingOf(t, "eigen") == e.pendingOf(t, "fern") {
		t.Fatal("zwei Einträge, eine .pending")
	}
	e.run(t, "node", "hub", "rotate", "fern").want(t, 0, "übernommen")
	if !exists(eigenPending) {
		t.Fatal("das Klären von fern hat die .pending von eigen entfernt")
	}
	e.run(t, "node", "hub", "rotate", "eigen").want(t, exitRetry, "verworfen")
	if exists(eigenPending) {
		t.Error(".pending von eigen liegt noch")
	}

	// Der Export nimmt keine .pending mit.
	p := e.writePendingFor(t, "fern", newToken(t))
	pending := pendingToken(t, e, "fern")
	exp := filepath.Join(e.dir, "export.yaml")
	e.run(t, "config", "export", "--output", exp).want(t, 0)
	data, err := os.ReadFile(exp)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), pending) || strings.Contains(string(data), ".pending") {
		t.Error("der Export nennt die .pending")
	}

	// import: Ein Alias, der bleibt, behält seine .pending; einer, der
	// wegfällt, verliert sie.
	e.runIn(t, newToken(t), "node", "hub", "add", "drei", "--node", "laptop-http", "--transport", "http", "--address",
		e.url, "--token-stdin").want(t, 0)
	drei := e.writePendingFor(t, "drei", newToken(t))
	e.run(t, "config", "import", exp).want(t, 0)
	if exists(drei) {
		t.Error("import: die .pending des wegfallenden Alias liegt noch")
	}
	if !exists(p) {
		t.Error("import: die .pending des bleibenden Alias ist weg")
	}

	// rm entfernt sie; nach rm und add gilt eine liegengebliebene nicht.
	old := e.entry(t, "fern")
	e.run(t, "node", "hub", "rm", "fern").want(t, 0)
	if exists(p) {
		t.Error("rm: .pending liegt noch")
	}
	stray := nodeStore(t, e.cfg).PendingTokenPath(old.EntryID)
	if err := nodestore.WritePendingToken(stray, old.EntryID, newToken(t)); err != nil {
		t.Fatal(err)
	}
	e.runIn(t, old.Token, "node", "hub", "add", "fern", "--node", "laptop-http", "--transport", "http", "--address", e.url,
		"--token-stdin").want(t, 0)
	r := e.run(t, "node", "hub", "rotate", "fern")
	r.want(t, 0, "Token rotiert")
	if strings.Contains(r.out, "liegt von einem früheren") {
		t.Error("die .pending des alten Eintrags gilt für den neuen")
	}
}
