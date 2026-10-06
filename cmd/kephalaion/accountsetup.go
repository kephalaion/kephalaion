package main

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kephalaion/kephalaion/internal/assistant"
	"github.com/kephalaion/kephalaion/internal/config"
	"github.com/kephalaion/kephalaion/internal/contract"
	"github.com/kephalaion/kephalaion/internal/contract/httpapi"
	"github.com/kephalaion/kephalaion/internal/ident"
	"github.com/kephalaion/kephalaion/internal/node/mcpnode"
)

// node account setup (Task 028): Ein User richtet seinen Account mit dem
// Einrichtungstoken selbst ein — über einen Node, den er nur erreicht, als
// Client der Routen für Accounts (/account/rotate, /account/check), nicht
// über MCP und ohne node.db. Das neue Token entsteht hier; zum Node und zum
// Hub geht nur sein Hash.

// accountCallTimeout begrenzt einen Aufruf einer Route für Accounts; der
// Node selbst wartet höchstens 45 s auf den Hub.
const accountCallTimeout = 90 * time.Second

// exitRetry ist der Exit-Code von setup, wenn eine liegende .pending geklärt
// und gelöscht ist: Das Einrichtungstoken gilt noch, setup erneut aufrufen.
const exitRetry = 3

// accountClient ist der Client der Routen für Accounts eines Nodes.
type accountClient struct {
	addr   nodeAddress
	caFile string
	http   *http.Client
}

func newAccountClient(addr nodeAddress, rootCAs *x509.CertPool, caFile string) *accountClient {
	return &accountClient{addr: addr, caFile: caFile, http: nodeClient(rootCAs, nil)}
}

// accountOutcome ist, wie ein Aufruf einer Route für Accounts ausging.
type accountOutcome int

const (
	// answerOK: Erfolg, result gilt.
	answerOK accountOutcome = iota
	// answerNode: Der Node antwortete mit einem seiner Codes (nodeErr).
	answerNode
	// answerNotReached: Die Anfrage hat den Node nachweislich nicht erreicht
	// — Fehler vor dem Abschicken (Verbindung, TLS) oder 404 ohne Code des
	// Nodes.
	answerNotReached
	// answerUnclear: alles andere nach dem Abschicken — Status eines
	// Proxys, Zeitüberschreitung, abgebrochene Verbindung, eine Antwort ohne
	// Code des Nodes.
	answerUnclear
)

// accountAnswer ist die Antwort einer Route für Accounts.
type accountAnswer struct {
	outcome accountOutcome
	result  mcpnode.AccountResult
	nodeErr mcpnode.AccountError
	// detail erklärt answerNotReached und answerUnclear.
	detail error
}

// call schickt eine Anfrage an eine Route für Accounts, genau einmal. Das
// Token steht nur im Header; keine Meldung nennt es.
func (c *accountClient) call(path, hub, account, token string, body []byte) accountAnswer {
	ctx, cancel := context.WithTimeout(context.Background(), accountCallTimeout)
	defer cancel()
	where := "Node unter " + c.addr.Base + path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.addr.Base+path, bytes.NewReader(body))
	if err != nil {
		return accountAnswer{outcome: answerNotReached, detail: err}
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set(assistant.AccountHeaderPrefix+hub, account)
	req.Header.Set(assistant.TokenHeaderPrefix+hub, token)
	resp, err := c.http.Do(req)
	if err != nil {
		if httpapi.NotSent(err) {
			return accountAnswer{outcome: answerNotReached, detail: explainNodeError(c.addr, c.caFile, err)}
		}
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return accountAnswer{outcome: answerUnclear, detail: fmt.Errorf("%s: abgeschickt, aber keine Antwort: %w", where, err)}
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return accountAnswer{outcome: answerUnclear, detail: fmt.Errorf("%s: Antwort abgebrochen: %w", where, err)}
	}
	if resp.StatusCode == http.StatusOK {
		var res mcpnode.AccountResult
		if json.Unmarshal(data, &res) == nil && res.Hub == hub && res.Account == account {
			return accountAnswer{outcome: answerOK, result: res}
		}
		return accountAnswer{outcome: answerUnclear, detail: fmt.Errorf("%s: HTTP 200, aber keine Antwort des Nodes", where)}
	}
	var ae mcpnode.AccountError
	if json.Unmarshal(data, &ae) == nil && mcpnode.AccountStatusFits(ae.Code, resp.StatusCode) {
		return accountAnswer{outcome: answerNode, nodeErr: ae}
	}
	status := fmt.Sprintf("HTTP %d", resp.StatusCode)
	if text := shortBody(resp, data); text != "" {
		status += ": " + text
	}
	if resp.StatusCode == http.StatusNotFound {
		return accountAnswer{outcome: answerNotReached, detail: fmt.Errorf("%s: dort antwortet kein Node (%s) — Präfix "+
			"falsch, oder ein Node ohne Routen für Accounts (älter als Task 028), oder der Proxy reicht sie nicht durch",
			where, status)}
	}
	return accountAnswer{outcome: answerUnclear, detail: fmt.Errorf("%s: keine Antwort des Nodes (%s)", where, status)}
}

// rotate lässt den Node das Token rotieren: altes Token im Header, Hash des
// neuen im Body.
func (c *accountClient) rotate(hub, account, oldToken, newHash string) accountAnswer {
	body, _ := json.Marshal(mcpnode.AccountRotateRequest{NewHash: newHash})
	return c.call(mcpnode.AccountRotatePath, hub, account, oldToken, body)
}

// tokenState ist der Zustand eines Tokens, wie der Node ihn beim Hub erfragt.
type tokenState string

const (
	stateValid tokenState = "valid"
	// stateInvalid: Der Hub lehnt das Token ab (lokal unterscheidbar).
	stateInvalid tokenState = "invalid"
	// stateUnknownHub: Der Node kennt den Hub nicht (lokal unterscheidbar).
	stateUnknownHub tokenState = "unknown_hub"
	// stateInvalidOrUnknown: die verdeckte Antwort über einen Proxy.
	stateInvalidOrUnknown tokenState = "invalid_or_unknown"
	// stateUnchecked: Node oder Hub nicht erreicht, oder der Hub nimmt den
	// Node nicht an.
	stateUnchecked tokenState = "unchecked"
	// stateUnreadable: Die Datei hält kein gültiges Token; nicht gefragt.
	stateUnreadable tokenState = "unreadable"
)

func (s tokenState) text() string {
	switch s {
	case stateValid:
		return "gültig"
	case stateInvalid:
		return "ungültig"
	case stateUnknownHub:
		return "unbekannt"
	case stateInvalidOrUnknown:
		return "ungültig oder unbekannt"
	case stateUnchecked:
		return "nicht geprüft"
	case stateUnreadable:
		return "unlesbar"
	}
	return string(s)
}

// tokenCheck ist das Ergebnis der Route zum Prüfen.
type tokenCheck struct {
	state  tokenState
	result mcpnode.AccountResult
	// reason sagt bei allem außer gültig, warum.
	reason string
}

// check fragt den Node, ob ein Token am Hub gilt — genau eine Anfrage.
func (c *accountClient) check(hub, account, token string) tokenCheck {
	a := c.call(mcpnode.AccountCheckPath, hub, account, token, nil)
	switch a.outcome {
	case answerOK:
		return tokenCheck{state: stateValid, result: a.result}
	case answerNode:
		switch {
		case a.nodeErr.Code == string(contract.CodeAccountUnauthenticated) && a.nodeErr.Hidden:
			return tokenCheck{state: stateInvalidOrUnknown, reason: a.nodeErr.Message}
		case a.nodeErr.Code == string(contract.CodeAccountUnauthenticated):
			return tokenCheck{state: stateInvalid, reason: a.nodeErr.Message}
		case a.nodeErr.Code == mcpnode.CodeUnknownHub:
			return tokenCheck{state: stateUnknownHub, reason: a.nodeErr.Message}
		}
		return tokenCheck{state: stateUnchecked, reason: a.nodeErr.Message}
	}
	return tokenCheck{state: stateUnchecked, reason: a.detail.Error()}
}

// accountNode ist die Adresse des Nodes für node account setup und list:
// --node, sonst listen im Abschnitt node: der gefundenen config (des Users,
// sonst der globalen). fromConfig sagt, dass sie aus der config kommt; usage
// heißt falscher Aufruf (Exit 2) — auch eine config ohne Node.
func accountNode(cfgFlag, nodeFlag, caFile string) (addr nodeAddress, fromConfig, usage bool, err error) {
	if nodeFlag != "" {
		addr, err = parseNodeAddress(nodeFlag)
		return addr, false, err != nil, err
	}
	if caFile != "" {
		return nodeAddress{}, false, true, errors.New("--ca-file nur zusammen mit --node https://…")
	}
	loc, err := config.Locate(cfgFlag)
	if err != nil {
		return nodeAddress{}, false, false, err
	}
	cfg, _, err := config.Load(loc.Path)
	if err != nil {
		return nodeAddress{}, false, false, err
	}
	listen := cfg.Listen(config.Node)
	if listen == "" {
		return nodeAddress{}, false, true, fmt.Errorf("kein Node in der config %s: --node <url> nennt den Node — "+
			"über einen Proxy https://<name>/<präfix> (etwa https://<name>/kephalaion), auf diesem Rechner "+
			"http://127.0.0.1:<port>", loc.Path)
	}
	return localNodeAddress(listen), true, false, nil
}

// accountNodeFlags sind --node und --ca-file von setup und list.
type accountNodeFlags struct {
	node, caFile *string
}

func newAccountNodeFlags(c *command) accountNodeFlags {
	return accountNodeFlags{node: c.fs.String("node", "", ""), caFile: c.fs.String("ca-file", "", "")}
}

// resolve liefert Adresse und CA; code ist der Exit-Code bei einem Fehler
// (gemeldet), sonst 0.
func (f accountNodeFlags) resolve(c *command) (addr nodeAddress, rootCAs *x509.CertPool, fromConfig bool, code int) {
	addr, fromConfig, usage, err := accountNode(*c.cfg, *f.node, *f.caFile)
	if err != nil {
		fmt.Fprintf(c.stderr, "%s: %v\n", c.name, err)
		if usage {
			return addr, nil, false, 2
		}
		return addr, nil, false, 1
	}
	rootCAs, err = readNodeCA(addr, *f.caFile)
	if err != nil {
		fmt.Fprintf(c.stderr, "%s: %v\n", c.name, err)
		if !addr.HTTPS {
			return addr, nil, false, 2
		}
		return addr, nil, false, 1
	}
	return addr, rootCAs, fromConfig, 0
}

// setupCmd ist ein Aufruf von node account setup.
type setupCmd struct {
	c            *command
	stdin        io.Reader
	hub, account string
	// argToken ist das Einrichtungstoken als Argument; leer mit
	// --token-stdin.
	argToken   string
	fromStdin  bool
	flags      accountNodeFlags
	addr       nodeAddress
	fromConfig bool
	client     *accountClient
	file       string
}

func runNodeAccountSetup(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	c := newCommand("node account setup", nodeAccountUsage, stdout, stderr, "<hub>", "<account>")
	c.optional = 1
	fromStdin := c.fs.Bool("token-stdin", false, "")
	s := &setupCmd{c: c, stdin: stdin, flags: newAccountNodeFlags(c)}
	pos, code, ok := c.parse(args)
	if !ok {
		return code
	}
	s.hub, s.account, s.fromStdin = pos[0], pos[1], *fromStdin
	if len(pos) == 3 {
		s.argToken = pos[2]
	}
	return s.run()
}

func (s *setupCmd) logf(format string, a ...any) { fmt.Fprintf(s.c.stdout, format+"\n", a...) }

func (s *setupCmd) usage(format string, a ...any) int {
	fmt.Fprintf(s.c.stderr, "%s: "+format+"\n", append([]any{s.c.name}, a...)...)
	return 2
}

func (s *setupCmd) fail(format string, a ...any) int {
	fmt.Fprintf(s.c.stderr, "%s: "+format+"\n", append([]any{s.c.name}, a...)...)
	return 1
}

func (s *setupCmd) pending() string { return s.file + ".pending" }

func (s *setupCmd) run() int {
	// Zuerst alles, was kein Token braucht: Aufruf, Adresse, vorhandene
	// Datei, Node.
	if (s.argToken != "") == s.fromStdin {
		return s.usage("erwartet das Einrichtungstoken als Argument oder --token-stdin, genau eines")
	}
	if err := ident.CheckName("Hub", s.hub); err != nil {
		return s.usage("%v", err)
	}
	if err := ident.CheckPrincipalName("Account", s.account); err != nil {
		return s.usage("%v", err)
	}
	addr, rootCAs, fromConfig, code := s.flags.resolve(s.c)
	if code != 0 {
		return code
	}
	s.addr, s.fromConfig = addr, fromConfig
	dir, err := assistant.TokensDir()
	if err != nil {
		return s.fail("%v", err)
	}
	s.file = assistant.TokenFile(dir, s.hub, s.account)
	if ok, err := fileExists(s.file); err != nil {
		return s.fail("%v", err)
	} else if ok {
		return s.fail("%s gibt es schon — der Account ist auf diesem Rechner eingerichtet; das Einrichtungstoken "+
			"bleibt ungenutzt. Was gilt, zeigt: kephalaion node account list (mit eigenem Node auch kephalaion node "+
			"account check|rotate %s %s --token-file %s)", s.file, s.hub, s.account, s.file)
	}
	s.logf("Node: %s", addr.Base)
	if _, err := probeNode(context.Background(), addr, rootCAs, *s.flags.caFile); err != nil {
		return s.fail("%v\nNichts geändert; das Einrichtungstoken gilt weiter.", err)
	}
	s.logf("Node erreichbar.")
	s.client = newAccountClient(addr, rootCAs, *s.flags.caFile)

	if ok, err := fileExists(s.pending()); err != nil {
		return s.fail("%v", err)
	} else if ok {
		return s.clarify()
	}

	setupToken, code := s.readSetupToken()
	if code != 0 {
		return code
	}
	s.logf("Einrichtungstoken für %s/%s: %s", s.hub, s.account, ident.MaskToken(setupToken))
	newToken, err := ident.NewToken()
	if err != nil {
		return s.fail("%v", err)
	}
	if err := os.MkdirAll(filepath.Dir(s.file), 0o700); err != nil {
		return s.fail("%v", err)
	}
	// Das neue Token liegt, bevor irgendetwas abgeschickt wird.
	if err := writePending(s.pending(), newToken); err != nil {
		return s.fail("%v", err)
	}
	s.logf("Neues Token erzeugt (%s), vorgemerkt in %s (0600)", ident.MaskToken(newToken), s.pending())
	s.logf("Rotiere das Token über den Node …")
	a := s.client.rotate(s.hub, s.account, setupToken, ident.HashToken(newToken))
	switch {
	case a.outcome == answerOK:
		s.logf("Token rotiert: Account %s (User %s), Collections %s", s.account, orDash(a.result.User),
			joinOrNone(a.result.Collections))
		if a.result.Note != "" {
			s.logf("Hinweis des Nodes: %s", a.result.Note)
		}
		return s.store(newToken)
	case a.outcome == answerUnclear || (a.outcome == answerNode && a.nodeErr.Code == mcpnode.CodeOutcomeUnknown):
		reason := a.nodeErr.Message
		if a.detail != nil {
			reason = a.detail.Error()
		}
		return s.fail("Ausgang unklar: %s\nDas neue Token gilt vielleicht schon, vielleicht noch das "+
			"Einrichtungstoken; %s bleibt liegen. Nicht von Hand wiederholen — derselbe Aufruf klärt es, ohne zu "+
			"rotieren:\n  kephalaion node account setup %s %s …", reason, s.pending(), s.hub, s.account)
	}
	// Eindeutig ohne Wirkung: Der Hub hat nichts geändert.
	if err := os.Remove(s.pending()); err != nil {
		s.fail("%s ließ sich nicht löschen: %v", s.pending(), err)
	}
	if a.outcome == answerNotReached {
		return s.fail("%v\nNichts geändert; das Einrichtungstoken gilt weiter, %s ist gelöscht.", a.detail, s.pending())
	}
	return s.fail("%s\nNichts geändert, %s ist gelöscht.%s", a.nodeErr.Message, s.pending(), s.rotateHint(a.nodeErr))
}

// rotateHint nennt zu einem Code des Nodes den nächsten Schritt.
func (s *setupCmd) rotateHint(e mcpnode.AccountError) string {
	switch e.Code {
	case string(contract.CodeAccountUnauthenticated):
		alias := ""
		if e.Hidden {
			alias = fmt.Sprintf(" oder kennt der Node unter %s keinen Hub %s (der Alias des Hub-Eintrags am Node)?",
				s.addr.Base, s.hub)
		}
		return fmt.Sprintf("\nDas Einrichtungstoken gilt nicht — vertippt, schon benutzt oder ersetzt?%s Ein neues "+
			"erzeugt der Admin: kephalaion hub account token %s", alias, s.account)
	case mcpnode.CodeUnknownHub:
		return "\n<hub> ist der Alias des Hub-Eintrags am Node (dort: kephalaion node hub list); das " +
			"Einrichtungstoken gilt weiter."

	case string(contract.CodeNoSharedCollection):
		return fmt.Sprintf("\nDer Admin gibt dem Account eine Collection, die der Node abgleicht (kephalaion hub "+
			"account grant %s <collection>, kephalaion hub node grant <node> <collection>); das Einrichtungstoken "+
			"gilt weiter.", s.account)
	case mcpnode.CodeUnreachable:
		return "\nDas Einrichtungstoken gilt weiter; später erneut."
	}
	return "\nDas Einrichtungstoken gilt weiter."
}

// readSetupToken liest das Einrichtungstoken: das Argument oder eine Zeile
// von stdin, im Format geprüft.
func (s *setupCmd) readSetupToken() (string, int) {
	if !s.fromStdin {
		if err := ident.CheckToken(s.argToken); err != nil {
			return "", s.usage("Einrichtungstoken: %v", err)
		}
		return s.argToken, 0
	}
	tok, err := readToken(s.stdin)
	if err != nil {
		return "", s.fail("%v", err)
	}
	return tok, 0
}

// store schreibt das Token in die Token-Datei, löscht .pending und trägt den
// Node bei den Assistenten ein.
func (s *setupCmd) store(token string) int {
	if err := config.WriteFileAtomic(s.file, []byte(token+"\n"), 0o600); err != nil {
		return s.fail("Das neue Token gilt, aber %s ließ sich nicht schreiben: %v\nEs steht in %s; von Hand nach %s "+
			"verschieben.", s.file, err, s.pending(), s.file)
	}
	if err := os.Remove(s.pending()); err != nil {
		fmt.Fprintf(s.c.stderr, "%s: %s ließ sich nicht löschen: %v\n", s.c.name, s.pending(), err)
	}
	s.logf("Token in %s gespeichert (0600).", s.file)
	s.register()
	return 0
}

// register trägt den Node bei den KI-Assistenten ein: mit dem Node aus der
// config wie der automatische Anstoß (node mcp add --auto), mit --node wie
// node mcp add --node <url> --hub <hub>. Scheitert das, bleibt es bei einer
// Warnung: Das Token ist gespeichert.
func (s *setupCmd) register() {
	s.logf("Trage den Node bei den KI-Assistenten ein …")
	if s.fromConfig {
		s.c.autoRegister(s.file)
		return
	}
	exe, err := assistantExecutable()
	if err == nil {
		exe, err = filepath.Abs(exe)
	}
	if err != nil {
		fmt.Fprintf(s.c.stderr, "%s: Warnung: bei den KI-Assistenten nicht eingetragen: Pfad dieses Binarys: %v\n",
			s.c.name, err)
		return
	}
	target := assistant.Target{URL: s.addr.Endpoint(), Binary: exe, TokensDir: filepath.Dir(filepath.Dir(s.file))}
	if s.addr.Remote() {
		target.Hubs = []string{s.hub}
	}
	if registerAssistants(context.Background(), s.c.name, target, assistant.AddOptions{}, s.c.stdout, s.c.stderr) != 0 {
		fmt.Fprintf(s.c.stderr, "%s: Warnung: Die Anmeldung bei den KI-Assistenten ist nicht vollständig "+
			"(kephalaion node mcp status --node %s); das Token ist gespeichert.\n", s.c.name, s.addr.Base)
	}
	if own := ownNode(*s.c.cfg); own != "" {
		s.logf("Hinweis: Dieser Rechner hat einen eigenen Node (%s). Der nächste automatische Anstoß (install.sh, "+
			"kephalaion node account rotate, check) trägt wieder dessen Adresse ein.", own)
	}
}

// clarify klärt eine liegende .pending über die Route zum Prüfen, ohne zu
// rotieren: Gilt ihr Token, wird es die Token-Datei. Gilt es nicht, prüft es
// das Einrichtungstoken — nur wenn das gilt, wird .pending gelöscht (der
// rotate kam nicht an), und setup lässt sich wiederholen. Sonst bleibt sie.
func (s *setupCmd) clarify() int {
	s.logf("%s liegt von einem früheren Aufruf mit unklarem Ausgang; prüfe, welches Token gilt (kein rotate) …",
		s.pending())
	pendingToken, err := readTokenFile(s.pending())
	if err != nil {
		return s.fail("%v; %s bleibt liegen", err, s.pending())
	}
	st := s.client.check(s.hub, s.account, pendingToken)
	s.logf("Neues Token aus %s (%s): %s", s.pending(), ident.MaskToken(pendingToken), st.state.text())
	switch st.state {
	case stateValid:
		s.logf("Der rotate kam an: Account %s (User %s), Collections %s", s.account, orDash(st.result.User),
			joinOrNone(st.result.Collections))
		return s.store(pendingToken)
	case stateUnchecked:
		return s.fail("nicht zu entscheiden: %s\n%s bleibt liegen; später erneut aufrufen.", st.reason, s.pending())
	case stateUnknownHub:
		return s.fail("%s\n%s bleibt liegen; <hub> ist der Alias des Hub-Eintrags am Node.", st.reason, s.pending())
	}
	setupToken, code := s.readSetupToken()
	if code != 0 {
		fmt.Fprintf(s.c.stderr, "%s: %s bleibt liegen.\n", s.c.name, s.pending())
		return code
	}
	st2 := s.client.check(s.hub, s.account, setupToken)
	s.logf("Einrichtungstoken (%s): %s", ident.MaskToken(setupToken), st2.state.text())
	switch st2.state {
	case stateValid:
		if err := os.Remove(s.pending()); err != nil {
			return s.fail("%s ließ sich nicht löschen: %v", s.pending(), err)
		}
		s.logf("Der rotate kam nicht an; %s gelöscht. Das Einrichtungstoken gilt weiter — erneut aufrufen:\n"+
			"  kephalaion node account setup %s %s …", s.pending(), s.hub, s.account)
		return exitRetry
	case stateUnchecked:
		return s.fail("nicht zu entscheiden: %s\n%s bleibt liegen; später erneut aufrufen.", st2.reason, s.pending())
	}
	return s.fail("Weder das Token in %s noch das Einrichtungstoken gilt (%s). %s bleibt liegen — sonst ginge ein "+
		"gültiges Token verloren. Ein neues Einrichtungstoken erzeugt der Admin: kephalaion hub account token %s",
		s.pending(), strings.TrimSuffix(st2.reason, "."), s.pending(), s.account)
}
