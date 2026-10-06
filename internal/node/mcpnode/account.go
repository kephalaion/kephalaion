package mcpnode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"time"

	"github.com/kephalaion/kephalaion/internal/contract"
	"github.com/kephalaion/kephalaion/internal/ident"
	"github.com/kephalaion/kephalaion/internal/loopback"
	"github.com/kephalaion/kephalaion/internal/node/replica"
	"github.com/kephalaion/kephalaion/internal/node/store"
	"github.com/kephalaion/kephalaion/internal/reqlog"
)

// Die Routen für Accounts (account routes, Task 028): Neben /mcp lässt ein
// Client über den Node das Token eines Accounts rotieren und prüfen, ohne
// Zugriff auf die Datenbank des Nodes — für node account setup und list.
// Kein MCP: je Vorgang genau eine HTTP-Anfrage und eine Logzeile.
//
// Beide Routen nehmen Account und Token im Header-Paar genau eines Hubs.
// Es entscheidet der Hub, ohne Vorprüfung gegen die Replica: Ein frisch
// angelegter Account steht dort erst nach dem nächsten Abgleich. Lehnt der
// Hub das Token ab, ist das ein Fehlversuch wie am MCP-Eingang
// (login=invalid als erster Vermerk). Über einen Proxy ohne gültige
// Anmeldung sind „abgelehnt“, „Hub unbekannt“ und „der Hub nimmt den Node
// nicht an“ eine Antwort ohne Namen; „Hub nicht erreicht“, „Ausgang unklar“
// und Fehler des Nodes bleiben eigene Codes, ebenfalls ohne Namen. Kein
// Token steht in einer Antwort oder im Log.

// Die Pfade der Routen für Accounts am Node-Listener.
const (
	AccountRotatePath = "/account/rotate"
	AccountCheckPath  = "/account/check"
)

// Die Codes der Routen für Accounts über die des Vertrags hinaus
// (invalid, account_unauthenticated, no_shared_collection) und die der
// Werkzeuge, die schreiben (unreachable, outcome_unknown, internal).
const (
	// CodeUnknownHub: Der Node hat keinen Hub-Eintrag mit dem Alias des
	// Header-Paars; nichts abgeschickt.
	CodeUnknownHub = "unknown_hub"
	// CodeHubRefused: Der Hub nimmt den Node nicht an oder lehnt die Anfrage
	// mit einem anderen Code des Vertrags ab; nichts geändert.
	CodeHubRefused = "hub_refused"
)

// accountStatus ist der HTTP-Status je Code der Routen für Accounts. Ein
// Client nimmt einen Code nur mit diesem Status als Antwort des Nodes.
var accountStatus = map[string]int{
	string(contract.CodeInvalid):                http.StatusBadRequest,
	string(contract.CodeAccountUnauthenticated): http.StatusForbidden,
	CodeUnknownHub:                              http.StatusForbidden,
	CodeHubRefused:                              http.StatusBadGateway,
	string(contract.CodeNoSharedCollection):     http.StatusConflict,
	CodeUnreachable:                             http.StatusServiceUnavailable,
	CodeOutcomeUnknown:                          http.StatusGatewayTimeout,
	CodeInternal:                                http.StatusInternalServerError,
}

// AccountStatusFits sagt, ob eine Antwort mit Status und Code von den
// Routen für Accounts stammen kann: der Code ist einer von ihnen, und der
// Status passt dazu (invalid auch 405, für eine andere Methode als POST).
func AccountStatusFits(code string, status int) bool {
	want, ok := accountStatus[code]
	if !ok {
		return false
	}
	return status == want || (code == string(contract.CodeInvalid) && status == http.StatusMethodNotAllowed)
}

// MaxAccountBodyBytes begrenzt den Body einer Anfrage an die Routen für
// Accounts.
const MaxAccountBodyBytes = 4 << 10

// accountHubTimeout begrenzt den Weg zum Hub je Anfrage; er läuft ohne den
// Abbruch des Clients, damit ein gelungenes rotate noch in die Replica
// kommt.
const accountHubTimeout = 45 * time.Second

// AccountRotateRequest ist der Body von /account/rotate.
type AccountRotateRequest struct {
	// NewHash ist sha256 des neuen Tokens, 64 Zeichen hex. Das Token selbst
	// erzeugt und behält der Client.
	NewHash string `json:"new_hash"`
}

// AccountResult ist die Antwort beider Routen bei Erfolg: bei rotate nach
// dem Wechsel, bei check, wenn das Token gilt. Nie ein Token.
type AccountResult struct {
	// Hub ist der Alias des Hub-Eintrags am Node.
	Hub     string `json:"hub"`
	Account string `json:"account"`
	// User ist, wem der Account gehört.
	User string `json:"user"`
	// Collections sind die Collections des Accounts, die dieser Node
	// abgleichen darf, sortiert.
	Collections []string `json:"collections"`
	// Replica nennt bei rotate die Collections, deren Account-Zeile der Node
	// in seine Replica geschrieben hat (die gewünschten).
	Replica []string `json:"replica,omitempty"`
	// Note ist ein Hinweis zu einem Erfolg von rotate: Die Replica ließ sich
	// nicht schreiben, der Abgleich holt nach.
	Note string `json:"note,omitempty"`
}

// AccountError ist die Antwort beider Routen bei einem Fehler. Hidden heißt:
// über einen Proxy ohne gültige Anmeldung — „abgelehnt“, „Hub unbekannt“ und
// „der Hub nimmt den Node nicht an“ sind dann dieselbe Antwort.
type AccountError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Hidden  bool   `json:"hidden,omitempty"`
}

// hiddenAccountMessage ist die Meldung der verdeckten Antwort.
const hiddenAccountMessage = "nicht angemeldet: Account oder Token ungültig, oder der Node kennt den Hub nicht"

var sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

// accountFailure ist ein Fehler einer Route für Accounts: Code, die Meldung
// für eine Anfrage ohne Proxy (mit Namen) und die ohne Namen.
type accountFailure struct {
	code   string
	status int
	msg    string
	plain  string
}

// accountCall sammelt, was eine Anfrage ins Log schreibt: den Fehlversuch
// zuerst, dann die Namen, den Code und Fehler — in dieser Reihenfolge, damit
// login=invalid direkt hinter via steht.
type accountCall struct {
	failed  bool
	hub     string
	node    string
	account string
	errs    []error
}

func (c *accountCall) note(ctx context.Context, code string) {
	if c.failed {
		reqlog.Note(ctx, "login", "invalid")
	}
	if c.hub != "" {
		reqlog.Note(ctx, "hub", c.hub)
	}
	if c.node != "" {
		reqlog.Note(ctx, "node", c.node)
	}
	if c.account != "" {
		reqlog.Note(ctx, "account", c.account)
	}
	if code != "" {
		reqlog.Note(ctx, "code", code)
	}
	for _, err := range c.errs {
		reqlog.NoteError(ctx, err)
	}
}

// accountHandler ist eine der beiden Routen: rotate oder check.
func (n *Node) accountHandler(rotate bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := loopback.CheckHost(r); err != nil {
			loopback.Forbid(w, err)
			return
		}
		if err := checkOrigin(r.Header.Values("Origin")); err != nil {
			loopback.Forbid(w, err)
			return
		}
		c := &accountCall{}
		res, fail := n.accountRun(w, r, c, rotate)
		if fail == nil {
			c.note(r.Context(), "")
			writeAccountJSON(w, http.StatusOK, res)
			return
		}
		c.note(r.Context(), fail.code)
		out := AccountError{Code: fail.code, Message: fail.msg}
		if Proxied(r.Header) {
			switch fail.code {
			case string(contract.CodeAccountUnauthenticated), CodeUnknownHub, CodeHubRefused:
				// Verdeckt: dieselbe Antwort, gleich ob der Hub ablehnte, der
				// Node ihn nicht kennt oder der Hub den Node nicht annimmt.
				out = AccountError{Code: string(contract.CodeAccountUnauthenticated), Message: hiddenAccountMessage,
					Hidden: true}
				fail.status = accountStatus[out.Code]
			case string(contract.CodeNoSharedCollection):
				// Das Token galt: wie lokal.
			default:
				out.Message = fail.plain
			}
		}
		if fail.status == http.StatusMethodNotAllowed {
			w.Header().Set("Allow", http.MethodPost)
		}
		writeAccountJSON(w, fail.status, out)
	})
}

// accountRun führt eine Anfrage aus: Form prüfen, Hub-Eintrag nachschlagen,
// Hub fragen. Die Anfrage an den Hub läuft ohne den Abbruch des Clients.
func (n *Node) accountRun(w http.ResponseWriter, r *http.Request, c *accountCall, rotate bool) (*AccountResult,
	*accountFailure) {
	invalid := func(msg string) *accountFailure {
		return &accountFailure{code: string(contract.CodeInvalid), status: http.StatusBadRequest, msg: msg, plain: msg}
	}
	if r.Method != http.MethodPost {
		f := invalid("nur POST")
		f.status = http.StatusMethodNotAllowed
		return nil, f
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxAccountBodyBytes))
	if err != nil {
		return nil, invalid(fmt.Sprintf("Body nicht lesbar oder größer als %d Bytes", MaxAccountBodyBytes))
	}
	pairs := HubHeaders(r.Header)
	if len(pairs) != 1 || !pairs[0].Complete {
		return nil, invalid("erwartet genau ein Header-Paar X-Keph-Account-<hub> und X-Keph-Token-<hub>")
	}
	p := pairs[0]
	if err := ident.CheckPrincipalName("Account", p.Account); err != nil {
		return nil, invalid("X-Keph-Account-<hub>: kein gültiger Account-Name")
	}
	var newHash string
	if rotate {
		var req AccountRotateRequest
		if err := decodeStrict(body, &req); err != nil || !sha256Hex.MatchString(req.NewHash) {
			return nil, invalid(`Body: erwartet {"new_hash": "<sha256 des neuen Tokens, 64 Zeichen hex>"}`)
		}
		newHash = req.NewHash
	} else if len(bytes.TrimSpace(body)) > 0 {
		var none struct{}
		if err := decodeStrict(body, &none); err != nil {
			return nil, invalid("Body: erwartet keinen oder {}")
		}
	}
	c.hub, c.account = p.Alias, p.Account

	ctx := r.Context()
	h, err := n.nodes.Hub(ctx, p.Alias)
	if errors.Is(err, store.ErrNotFound) {
		return nil, &accountFailure{code: CodeUnknownHub, status: http.StatusForbidden,
			msg: fmt.Sprintf("der Node hat keinen Hub-Eintrag %s", p.Alias), plain: hiddenAccountMessage}
	}
	if err != nil {
		c.errs = append(c.errs, err)
		return nil, &accountFailure{code: CodeInternal, status: http.StatusInternalServerError,
			msg: "Fehler des Nodes (node.db); nichts abgeschickt", plain: "Fehler des Nodes; nichts abgeschickt"}
	}
	c.node = h.NodeName
	what := "nichts geändert"
	if !rotate {
		what = "nicht geprüft"
	}
	unreachable := func(err error) *accountFailure {
		if err != nil {
			c.errs = append(c.errs, err)
		}
		msg := fmt.Sprintf("Hub %s nicht erreichbar; %s", h.Name, what)
		if errors.Is(err, ErrUnsupported) {
			msg = fmt.Sprintf("Hub %s: Transport des Hub-Eintrags wird noch nicht unterstützt; %s", h.Name, what)
		}
		return &accountFailure{code: CodeUnreachable, status: http.StatusServiceUnavailable, msg: msg,
			plain: "Hub nicht erreichbar; " + what}
	}
	if n.link.Connect == nil {
		return nil, unreachable(errors.New("kein Weg zum Hub an diesem Eingang"))
	}
	hubCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), accountHubTimeout)
	defer cancel()
	hub, done, err := n.link.Connect(hubCtx, h)
	if err != nil {
		return nil, unreachable(err)
	}
	defer done()
	auth := contract.NodeAuth{Node: h.NodeName, Token: h.Token}
	if rotate {
		return n.accountRotate(hubCtx, c, h, hub, auth, p, newHash, unreachable)
	}
	return accountCheck(hubCtx, c, h, hub, auth, p, unreachable)
}

// accountRotate ruft rotate am Hub und übernimmt die Zeilen in die Replica.
func (n *Node) accountRotate(ctx context.Context, c *accountCall, h store.Hub, hub contract.Hub, auth contract.NodeAuth,
	p HubHeader, newHash string, unreachable func(error) *accountFailure) (*AccountResult, *accountFailure) {
	resp, err := hub.Rotate(ctx, contract.RotateRequest{Version: contract.Version, Auth: auth, Account: p.Account,
		Token: p.Token, NewHash: newHash})
	if err != nil {
		var ce *contract.Error
		switch {
		case errors.As(err, &ce) && ce.Code == contract.CodeAccountUnauthenticated:
			c.failed = true
			return nil, &accountFailure{code: string(ce.Code), status: http.StatusForbidden,
				msg: fmt.Sprintf("Hub %s: Account %s nicht angemeldet — unbekannt, Token falsch oder gesperrt; nichts "+
					"geändert", h.Name, p.Account), plain: hiddenAccountMessage}
		case errors.As(err, &ce) && ce.Code == contract.CodeNoSharedCollection:
			return nil, &accountFailure{code: string(ce.Code), status: http.StatusConflict,
				msg: fmt.Sprintf("Hub %s: Account %s hat keine der Collections, die der Node %s abgleichen darf; nichts "+
					"geändert", h.Name, p.Account, h.NodeName)}
		case errors.As(err, &ce):
			return nil, hubRefused(h, ce, "nichts geändert")
		case errors.Is(err, contract.ErrOutcomeUnknown):
			c.errs = append(c.errs, err)
			n.kick(h)
			return nil, &accountFailure{code: CodeOutcomeUnknown, status: http.StatusGatewayTimeout,
				msg: fmt.Sprintf("Hub %s: Ausgang unklar — abgeschickt, aber keine brauchbare Antwort; das neue Token gilt "+
					"vielleicht schon. Nicht wiederholen, sondern prüfen (%s)", h.Name, AccountCheckPath),
				plain: "Ausgang unklar — abgeschickt, aber keine brauchbare Antwort; nicht wiederholen, sondern prüfen (" +
					AccountCheckPath + ")"}
		case errors.Is(err, contract.ErrUnknownOperation):
			c.errs = append(c.errs, err)
			return nil, hubRefused(h, &contract.Error{Message: "der Hub kennt rotate nicht"}, "nichts geändert")
		}
		// Nicht abgeschickt (so sichert es jeder Transport zu): Der Hub hat
		// nichts geändert.
		return nil, unreachable(err)
	}
	out := &AccountResult{Hub: h.Name, Account: p.Account, User: userOfRows(resp.Rows), Collections: []string{}}
	for _, r := range resp.Rows {
		out.Collections = append(out.Collections, r.Collection)
	}
	sort.Strings(out.Collections)
	written, _, err := replica.WriteAccountRows(ctx, n.nodes, h, resp.HubID, resp.Rows)
	if err != nil {
		// Der Erfolg bleibt: Das Token gilt, der Abgleich holt die Zeilen
		// nach.
		c.errs = append(c.errs, err)
		out.Note = "Am Hub rotiert, aber nicht in die Replica übernommen; der Abgleich holt es nach."
		n.kick(h)
		return out, nil
	}
	sort.Strings(written)
	out.Replica = written
	return out, nil
}

// accountCheck fragt den Hub, ob das Token gilt (whoami mit Account-Teil).
func accountCheck(ctx context.Context, c *accountCall, h store.Hub, hub contract.Hub, auth contract.NodeAuth,
	p HubHeader, unreachable func(error) *accountFailure) (*AccountResult, *accountFailure) {
	resp, err := hub.Whoami(ctx, contract.WhoamiRequest{Version: contract.Version, Auth: auth,
		Account: &contract.AccountAuth{Account: p.Account, Token: p.Token}})
	var ce *contract.Error
	switch {
	case errors.As(err, &ce):
		return nil, hubRefused(h, ce, "nicht geprüft")
	case err != nil:
		return nil, unreachable(err)
	case resp.Account == nil:
		return nil, hubRefused(h, &contract.Error{Message: "der Hub antwortet ohne Auskunft zum Account"}, "nicht geprüft")
	case !resp.Account.Valid:
		c.failed = true
		return nil, &accountFailure{code: string(contract.CodeAccountUnauthenticated), status: http.StatusForbidden,
			msg: fmt.Sprintf("Hub %s: Account %s gilt nicht mit diesem Token (unbekannt, Token falsch oder gesperrt)",
				h.Name, p.Account), plain: hiddenAccountMessage}
	}
	cols := append([]string{}, resp.Account.Collections...)
	sort.Strings(cols)
	return &AccountResult{Hub: h.Name, Account: p.Account, User: resp.Account.User, Collections: cols}, nil
}

// hubRefused ist die Antwort, wenn der Hub den Node nicht annimmt oder die
// Anfrage mit einem anderen Code des Vertrags ablehnt.
func hubRefused(h store.Hub, ce *contract.Error, what string) *accountFailure {
	msg := fmt.Sprintf("Hub %s nimmt die Anfrage dieses Nodes (%s) nicht an: %s; %s", h.Name, h.NodeName, ce.Message,
		what)
	if ce.Code == contract.CodeUnauthenticated {
		msg = fmt.Sprintf("Hub %s nimmt diesen Node (%s) nicht an — Node unbekannt, Token des Nodes falsch oder "+
			"gesperrt; %s", h.Name, h.NodeName, what)
	}
	return &accountFailure{code: CodeHubRefused, status: http.StatusBadGateway, msg: msg, plain: hiddenAccountMessage}
}

// userOfRows nennt den User aus den Account-Zeilen einer Antwort von rotate.
func userOfRows(rows []contract.Row) string {
	for _, r := range rows {
		if r.Content == nil {
			continue
		}
		if c, err := contract.DecodeAccountContent(*r.Content); err == nil {
			return c.User
		}
	}
	return ""
}

// decodeStrict liest JSON ohne unbekannte Felder und ohne Rest.
func decodeStrict(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if dec.More() {
		return errors.New("mehr als ein JSON-Wert")
	}
	return nil
}

func writeAccountJSON(w http.ResponseWriter, status int, v any) {
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
