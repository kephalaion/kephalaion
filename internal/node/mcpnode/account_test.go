package mcpnode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/oklog/ulid/v2"

	"github.com/kephalaion/kephalaion/internal/config"
	"github.com/kephalaion/kephalaion/internal/contract"
	"github.com/kephalaion/kephalaion/internal/ident"
	"github.com/kephalaion/kephalaion/internal/node/store"
	"github.com/kephalaion/kephalaion/internal/reqlog"
	"github.com/kephalaion/kephalaion/internal/upgrade"
)

// Die Routen für Accounts (Task 028): rotate und check über den Node, ohne
// Vorprüfung gegen die Replica; lokal unterscheidbar, über einen Proxy ohne
// gültige Anmeldung verdeckt — außer „Hub nicht erreicht“ und „Ausgang
// unklar“. Je Anfrage eine Logzeile, nie ein Token darin.

// acctHub ist eine Attrappe von contract.Hub für whoami und rotate: Hash,
// User und Collections je Account. refuse lässt den Hub den Node abweisen,
// lost führt rotate aus und verliert dann die Antwort.
type acctHub struct {
	mu          sync.Mutex
	id          string
	hashes      map[string]string
	users       map[string]string
	collections map[string][]string
	refuse      bool
	lost        bool
	rotates     int
	whoamis     int
}

func newAcctHub() *acctHub {
	return &acctHub{id: ulid.Make().String(), hashes: map[string]string{}, users: map[string]string{},
		collections: map[string][]string{}}
}

func (f *acctHub) add(account, user, tok string, collections ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hashes[account], f.users[account], f.collections[account] = ident.HashToken(tok), user, collections
}

func (f *acctHub) valid(account, tok string) bool {
	h, ok := f.hashes[account]
	return ok && h == ident.HashToken(tok)
}

func (f *acctHub) Whoami(_ context.Context, req contract.WhoamiRequest) (contract.WhoamiResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.whoamis++
	if f.refuse {
		return contract.WhoamiResponse{}, contract.ErrUnauthenticated
	}
	resp := contract.WhoamiResponse{HubID: f.id, Version: contract.Version, Node: req.Auth.Node}
	if a := req.Account; a != nil {
		st := &contract.AccountStatus{Account: a.Account, Collections: []string{}}
		if f.valid(a.Account, a.Token) {
			st.Valid, st.User, st.Collections = true, f.users[a.Account], f.collections[a.Account]
		}
		resp.Account = st
	}
	return resp, nil
}

func (f *acctHub) Rotate(_ context.Context, req contract.RotateRequest) (contract.RotateResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rotates++
	switch {
	case f.refuse:
		return contract.RotateResponse{}, contract.ErrUnauthenticated
	case !f.valid(req.Account, req.Token):
		return contract.RotateResponse{}, contract.ErrAccountUnauthenticated
	case len(f.collections[req.Account]) == 0:
		return contract.RotateResponse{}, contract.ErrNoSharedCollection
	}
	f.hashes[req.Account] = req.NewHash
	var rows []contract.Row
	for i, c := range f.collections[req.Account] {
		content, err := contract.EncodeAccountContent(contract.AccountContent{Hash: req.NewHash,
			User: f.users[req.Account], Rights: contract.Rights{Write: true}})
		if err != nil {
			panic(err)
		}
		rows = append(rows, contract.Row{ID: ulid.Make().String(), Collection: c,
			Name: contract.AccountRowName(req.Account), Content: &content, Revision: int64(10 + i),
			CreatedAt: 1, CreatedBy: "admin", UpdatedAt: 2, UpdatedBy: f.users[req.Account]})
	}
	if f.lost {
		return contract.RotateResponse{}, fmt.Errorf("%w: Verbindung abgebrochen", contract.ErrOutcomeUnknown)
	}
	return contract.RotateResponse{HubID: f.id, Version: contract.Version, Rows: rows}, nil
}

func (f *acctHub) Sync(context.Context, contract.SyncRequest) (contract.SyncResponse, error) {
	return contract.SyncResponse{}, errors.New("sync: nicht in der Attrappe")
}

func (f *acctHub) Create(context.Context, contract.CreateRequest) (contract.WriteResponse, error) {
	return contract.WriteResponse{}, errors.New("nicht in der Attrappe")
}

func (f *acctHub) Write(context.Context, contract.WriteRequest) (contract.WriteResponse, error) {
	return contract.WriteResponse{}, errors.New("nicht in der Attrappe")
}

func (f *acctHub) Delete(context.Context, contract.DeleteRequest) (contract.WriteResponse, error) {
	return contract.WriteResponse{}, errors.New("nicht in der Attrappe")
}

func (f *acctHub) Rename(context.Context, contract.RenameRequest) (contract.WriteResponse, error) {
	return contract.WriteResponse{}, errors.New("nicht in der Attrappe")
}

// set ändert refuse und lost unter der Sperre.
func (f *acctHub) set(refuse, lost bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refuse, f.lost = refuse, lost
}

func (f *acctHub) counts() (rotates, whoamis int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.rotates, f.whoamis
}

// acctEnv ist ein Node mit zwei Hub-Einträgen: zentrale (die Attrappe, nie
// abgeglichen — keine Replica) und funkloch (Connect scheitert, nichts
// abgeschickt). Davor ein Nachbau des Proxys wie in proxy_test.go.
type acctEnv struct {
	nodes  store.Store
	hub    *acctHub
	direct string
	proxy  string
	log    *lockedBuffer
	tokens []string

	mu    sync.Mutex
	kicks []string
}

func newAcctEnv(t *testing.T) *acctEnv {
	t.Helper()
	ctx := context.Background()
	nodes, err := store.Create(ctx, config.SQLiteDB(filepath.Join(t.TempDir(), "node.db")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = nodes.Close() })
	e := &acctEnv{nodes: nodes, hub: newAcctHub(), log: &lockedBuffer{}}
	for _, alias := range []string{"zentrale", "funkloch"} {
		nodeTok := token(t)
		e.tokens = append(e.tokens, nodeTok)
		if err := nodes.AddHub(ctx, store.Hub{Name: alias, NodeName: proxyNode, Transport: store.TransportHTTPS,
			Address: "https://hub.example.org", Token: nodeTok}, false); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []string{"wissen", "privat"} {
		if err := nodes.AddCollection(ctx, "zentrale", c); err != nil {
			t.Fatal(err)
		}
	}
	link := HubLink{
		Connect: func(_ context.Context, h store.Hub) (contract.Hub, func(), error) {
			if h.Name == "funkloch" {
				return nil, nil, errors.New("dial tcp 192.0.2.1:443: connect: connection refused")
			}
			return e.hub, func() {}, nil
		},
		Sync: func(h store.Hub) {
			e.mu.Lock()
			defer e.mu.Unlock()
			e.kicks = append(e.kicks, h.Name)
		},
	}
	node := httptest.NewServer(reqlog.New(e.log).Middleware("node",
		NewHandler(nodes, proxyVersion, func() upgrade.Report { return proxyUpdate }, link)))
	t.Cleanup(node.Close)
	e.direct = node.URL
	target, _ := url.Parse(node.URL)
	proxy := httptest.NewServer(&httputil.ReverseProxy{Rewrite: func(pr *httputil.ProxyRequest) {
		pr.SetURL(target)
		pr.SetXForwarded()
	}})
	t.Cleanup(proxy.Close)
	e.proxy = proxy.URL
	return e
}

// newToken legt ein Token an, das kein Log und keine Antwort nennen darf.
func (e *acctEnv) newToken(t *testing.T) string {
	tok := token(t)
	e.tokens = append(e.tokens, tok)
	return tok
}

func (e *acctEnv) takeKicks() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := e.kicks
	e.kicks = nil
	return out
}

// acctReply ist eine Antwort einer Route für Accounts.
type acctReply struct {
	status int
	header http.Header
	body   string
	res    AccountResult
	err    AccountError
}

// call schickt eine Anfrage an base+path und prüft: genau eine neue
// Logzeile, kein Token in Antwort und Log.
func (e *acctEnv) call(t *testing.T, base, method, path string, h http.Header, body string) acctReply {
	t.Helper()
	before := strings.Count(e.log.String(), "\n")
	req, err := http.NewRequest(method, base+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range h {
		req.Header[k] = v
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	out := acctReply{status: resp.StatusCode, header: resp.Header, body: string(data)}
	if resp.StatusCode == http.StatusOK {
		_ = json.Unmarshal(data, &out.res)
	} else {
		_ = json.Unmarshal(data, &out.err)
	}
	if n := strings.Count(e.log.String(), "\n") - before; n != 1 {
		t.Errorf("%s %s: %d Logzeilen, erwartet 1:\n%s", method, path, n, e.log.String())
	}
	for _, tok := range e.tokens {
		if strings.Contains(out.body, tok) || strings.Contains(e.log.String(), tok) {
			t.Errorf("%s %s: Token in Antwort oder Log", method, path)
		}
	}
	return out
}

func (e *acctEnv) rotate(t *testing.T, base string, h http.Header, newHash string) acctReply {
	t.Helper()
	return e.call(t, base, http.MethodPost, AccountRotatePath, h, `{"new_hash":"`+newHash+`"}`)
}

func (e *acctEnv) check(t *testing.T, base string, h http.Header) acctReply {
	t.Helper()
	return e.call(t, base, http.MethodPost, AccountCheckPath, h, "")
}

// lastLine ist die letzte Logzeile ohne Zeitstempel.
func (e *acctEnv) lastLine() string {
	lines := strings.Split(strings.TrimSpace(e.log.String()), "\n")
	_, rest, _ := strings.Cut(lines[len(lines)-1], " ")
	return rest
}

func wantAccountError(t *testing.T, what string, r acctReply, status int, code string, contains ...string) {
	t.Helper()
	if r.status != status || r.err.Code != code {
		t.Fatalf("%s: HTTP %d %+v, erwartet %d %s\n%s", what, r.status, r.err, status, code, r.body)
	}
	for _, c := range contains {
		if !strings.Contains(r.err.Message, c) {
			t.Errorf("%s: Meldung ohne %q: %q", what, c, r.err.Message)
		}
	}
	if !AccountStatusFits(r.err.Code, r.status) {
		t.Errorf("%s: Status %d passt nicht zu %s", what, r.status, r.err.Code)
	}
}

// Rotieren über den Node: Der Hub entscheidet, auch für einen Account, den
// der Node nie abgeglichen hat (keine Replica). Danach stehen die Zeilen in
// der Replica, das neue Token gilt am Hub, das alte nicht mehr; check sagt
// dasselbe. Die Antwort nennt Hub, Account, User und Collections, nie ein
// Token; das Log Hub, Node und Account.
func TestAccountRotateAndCheck(t *testing.T) {
	e := newAcctEnv(t)
	setup, next := e.newToken(t), e.newToken(t)
	e.hub.add("anna", "kleist", setup, "wissen", "privat", "fremd")

	// check vor rotate: das Einrichtungstoken gilt, ohne Replica.
	r := e.check(t, e.direct, pair("zentrale", "anna", setup))
	if r.status != http.StatusOK || r.res.Hub != "zentrale" || r.res.Account != "anna" || r.res.User != "kleist" ||
		!reflect.DeepEqual(r.res.Collections, []string{"fremd", "privat", "wissen"}) {
		t.Fatalf("check: HTTP %d %+v\n%s", r.status, r.res, r.body)
	}
	if got := e.lastLine(); got != "node POST /account/check 200 "+durationOf(got)+" hub=zentrale node="+proxyNode+
		" account=anna" {
		t.Errorf("Logzeile check: %q", got)
	}

	r = e.rotate(t, e.direct, pair("zentrale", "anna", setup), ident.HashToken(next))
	if r.status != http.StatusOK || r.res.User != "kleist" || r.res.Note != "" ||
		!reflect.DeepEqual(r.res.Collections, []string{"fremd", "privat", "wissen"}) ||
		!reflect.DeepEqual(r.res.Replica, []string{"privat", "wissen"}) {
		t.Fatalf("rotate: HTTP %d %+v\n%s", r.status, r.res, r.body)
	}
	if ct := r.header.Get("Content-Type"); ct != "application/json" || r.header.Get("Cache-Control") != "no-store" {
		t.Errorf("Header: %v", r.header)
	}
	if got := e.lastLine(); !strings.HasPrefix(got, "node POST /account/rotate 200 ") ||
		!strings.HasSuffix(got, " hub=zentrale node="+proxyNode+" account=anna") {
		t.Errorf("Logzeile rotate: %q", got)
	}
	if strings.Contains(e.log.String(), ident.HashToken(next)) {
		t.Error("Hash im Log")
	}
	// Die Replica kennt den Account mit dem neuen Hash: MCP nimmt das neue
	// Token sofort an.
	logins, err := (&Node{nodes: e.nodes}).Authenticate(context.Background(), pair("zentrale", "anna", next))
	if err != nil || len(logins.Valid()) != 1 {
		t.Fatalf("Anmeldung mit dem neuen Token: %+v, %v", logins, err)
	}
	if k := e.takeKicks(); len(k) != 0 {
		t.Errorf("Anstoß nach Erfolg: %v", k)
	}
	if r := e.check(t, e.direct, pair("zentrale", "anna", next)); r.status != http.StatusOK {
		t.Errorf("check mit dem neuen Token: HTTP %d %s", r.status, r.body)
	}
	// Das Einrichtungstoken gilt nicht mehr: ein Fehlversuch, direkt hinter
	// der Dauer.
	r = e.check(t, e.direct, pair("zentrale", "anna", setup))
	wantAccountError(t, "check mit altem Token", r, http.StatusForbidden, "account_unauthenticated", "zentrale", "anna")
	if r.err.Hidden {
		t.Error("lokal verdeckt")
	}
	if got := e.lastLine(); got != "node POST /account/check 403 "+durationOf(got)+" login=invalid hub=zentrale node="+
		proxyNode+" account=anna code=account_unauthenticated" {
		t.Errorf("Logzeile Fehlversuch: %q", got)
	}
	r = e.rotate(t, e.direct, pair("zentrale", "anna", setup), ident.HashToken(e.newToken(t)))
	wantAccountError(t, "rotate mit altem Token", r, http.StatusForbidden, "account_unauthenticated", "nichts geändert")
	if !strings.Contains(e.lastLine(), " login=invalid hub=zentrale") {
		t.Errorf("Logzeile: %q", e.lastLine())
	}
	if rot, _ := e.hub.counts(); rot != 2 {
		t.Errorf("rotate am Hub: %d, erwartet 2 (nie wiederholt)", rot)
	}
}

// durationOf ist die Dauer aus einer Logzeile ohne Zeitstempel (Rolle,
// Methode, Pfad, Status, Dauer).
func durationOf(line string) string {
	f := strings.Fields(line)
	if len(f) < 5 {
		return ""
	}
	return f[4]
}

// Die Ausgänge außer Erfolg, je lokal und über den Proxy: abgelehnt,
// unbekannter Hub und ein Hub, der den Node nicht annimmt, sind über den
// Proxy eine Antwort — Byte für Byte —, ohne Namen und Version; keine
// gemeinsame Collection heißt, das Token galt: wie lokal. Hub nicht erreicht
// und Ausgang unklar bleiben eigene Codes, über den Proxy ohne Namen.
func TestAccountOutcomes(t *testing.T) {
	e := newAcctEnv(t)
	tok, ottoTok := e.newToken(t), e.newToken(t)
	e.hub.add("anna", "anna", tok, "wissen")
	e.hub.add("otto", "otto", ottoTok)
	secrets := proxySecrets([]string{"zentrale", "funkloch", "anna", "otto"})
	hash := ident.HashToken(e.newToken(t))
	wrong := pair("zentrale", "anna", e.newToken(t))

	type send func(base string, h http.Header) acctReply
	routes := map[string]send{
		"rotate": func(base string, h http.Header) acctReply { return e.rotate(t, base, h, hash) },
		"check":  func(base string, h http.Header) acctReply { return e.check(t, base, h) },
	}
	for name, do := range routes {
		// Lokal unterscheidbar.
		wantAccountError(t, name+" falsches Token", do(e.direct, wrong), http.StatusForbidden,
			"account_unauthenticated", "zentrale")
		wantAccountError(t, name+" unbekannter Hub", do(e.direct, pair("nirgends", "anna", tok)),
			http.StatusForbidden, "unknown_hub", "nirgends")
		if strings.Contains(e.lastLine(), "login=invalid") {
			t.Errorf("%s: unbekannter Hub als Fehlversuch: %q", name, e.lastLine())
		}
		wantAccountError(t, name+" nicht erreicht", do(e.direct, pair("funkloch", "anna", tok)),
			http.StatusServiceUnavailable, "unreachable", "funkloch")
		if l := e.lastLine(); strings.Contains(l, "login=invalid") || !strings.Contains(l, `error="dial tcp`) {
			t.Errorf("%s: Logzeile nicht erreicht: %q", name, l)
		}

		// Über den Proxy: dieselbe Antwort für abgelehnt und unbekannt.
		hiddenWrong := do(e.proxy, wrong)
		wantAccountError(t, name+" falsches Token, Proxy", hiddenWrong, http.StatusForbidden, "account_unauthenticated")
		if !hiddenWrong.err.Hidden {
			t.Errorf("%s: über den Proxy nicht verdeckt: %s", name, hiddenWrong.body)
		}
		if l := e.lastLine(); !strings.Contains(l, " via=127.0.0.1 login=invalid hub=zentrale") {
			t.Errorf("%s: Logzeile Fehlversuch über den Proxy: %q", name, l)
		}
		hiddenUnknown := do(e.proxy, pair("nirgends", "anna", tok))
		if hiddenUnknown.status != hiddenWrong.status || hiddenUnknown.body != hiddenWrong.body {
			t.Errorf("%s: unbekannter Hub über den Proxy %d %s, falsches Token %d %s", name, hiddenUnknown.status,
				hiddenUnknown.body, hiddenWrong.status, hiddenWrong.body)
		}
		r := do(e.proxy, pair("funkloch", "anna", tok))
		wantAccountError(t, name+" nicht erreicht, Proxy", r, http.StatusServiceUnavailable, "unreachable")
		for _, r := range []acctReply{hiddenWrong, hiddenUnknown, r} {
			for _, s := range secrets {
				if strings.Contains(r.body, s) {
					t.Errorf("%s über den Proxy nennt %q: %s", name, s, r.body)
				}
			}
		}

		// Der Hub nimmt den Node nicht an: lokal eigener Code, über den
		// Proxy verdeckt; kein Fehlversuch.
		e.hub.set(true, false)
		wantAccountError(t, name+" Node abgewiesen", do(e.direct, pair("zentrale", "anna", tok)), http.StatusBadGateway,
			"hub_refused", "nimmt diesen Node ("+proxyNode+") nicht an")
		r = do(e.proxy, pair("zentrale", "anna", tok))
		if r.status != hiddenWrong.status || r.body != hiddenWrong.body {
			t.Errorf("%s: Node abgewiesen über den Proxy: %d %s", name, r.status, r.body)
		}
		if strings.Contains(e.lastLine(), "login=invalid") {
			t.Errorf("%s: abgewiesener Node als Fehlversuch: %q", name, e.lastLine())
		}
		e.hub.set(false, false)
	}

	// Keine gemeinsame Collection: Das Token galt — über den Proxy wie lokal,
	// kein Fehlversuch.
	for _, base := range []string{e.direct, e.proxy} {
		r := e.rotate(t, base, pair("zentrale", "otto", ottoTok), hash)
		wantAccountError(t, "keine gemeinsame Collection", r, http.StatusConflict, "no_shared_collection", "otto",
			proxyNode)
		if strings.Contains(e.lastLine(), "login=invalid") {
			t.Errorf("keine gemeinsame Collection als Fehlversuch: %q", e.lastLine())
		}
	}

	// Ausgang unklar: rotate ist am Hub geschehen, die Antwort verloren —
	// eigener Code, Anstoß des Abgleichs, nie wiederholt.
	e.hub.set(false, true)
	rotBefore, _ := e.hub.counts()
	r := e.rotate(t, e.direct, pair("zentrale", "anna", tok), hash)
	wantAccountError(t, "Ausgang unklar", r, http.StatusGatewayTimeout, "outcome_unknown", "zentrale", "prüfen")
	if k := e.takeKicks(); !reflect.DeepEqual(k, []string{"zentrale"}) {
		t.Errorf("Anstoß nach unklarem Ausgang: %v", k)
	}
	e.hub.add("anna", "anna", tok, "wissen")
	r = e.rotate(t, e.proxy, pair("zentrale", "anna", tok), hash)
	wantAccountError(t, "Ausgang unklar, Proxy", r, http.StatusGatewayTimeout, "outcome_unknown", "prüfen")
	for _, s := range secrets {
		if strings.Contains(r.body, s) {
			t.Errorf("Ausgang unklar über den Proxy nennt %q: %s", s, r.body)
		}
	}
	if rot, _ := e.hub.counts(); rot != rotBefore+2 {
		t.Errorf("rotate am Hub: %d, erwartet %d", rot, rotBefore+2)
	}
}

// Falsche Anfragen kommen nie zum Hub und zählen nicht: andere Methode,
// kein, ein halbes oder zwei Header-Paare, ein ungültiger Hash, ein Body bei
// check. Host und Origin wie am MCP-Eingang. Andere Pfade sind 404 — auch
// einer, den ServeMux mit 301 bereinigt hätte.
func TestAccountInvalid(t *testing.T) {
	e := newAcctEnv(t)
	tok := e.newToken(t)
	e.hub.add("anna", "anna", tok, "wissen")
	valid := pair("zentrale", "anna", tok)
	hash := `{"new_hash":"` + ident.HashToken(tok) + `"}`

	r := e.call(t, e.direct, http.MethodGet, AccountRotatePath, valid, "")
	wantAccountError(t, "GET", r, http.StatusMethodNotAllowed, "invalid", "POST")
	if r.header.Get("Allow") != http.MethodPost {
		t.Errorf("Allow: %q", r.header.Get("Allow"))
	}
	half := http.Header{}
	half.Set("X-Keph-Account-zentrale", "anna")
	for name, c := range map[string]struct {
		path string
		h    http.Header
		body string
	}{
		"ohne Paar":         {AccountCheckPath, nil, ""},
		"halbes Paar":       {AccountCheckPath, half, ""},
		"zwei Paare":        {AccountCheckPath, merge(valid, pair("funkloch", "anna", tok)), ""},
		"Account ungültig":  {AccountCheckPath, pair("zentrale", "ANNA", tok), ""},
		"ohne Body":         {AccountRotatePath, valid, ""},
		"Hash kurz":         {AccountRotatePath, valid, `{"new_hash":"abc"}`},
		"Hash groß":         {AccountRotatePath, valid, `{"new_hash":"` + strings.ToUpper(ident.HashToken(tok)) + `"}`},
		"unbekanntes Feld":  {AccountRotatePath, valid, `{"new_hash":"` + ident.HashToken(tok) + `","token":"x"}`},
		"zwei Werte":        {AccountRotatePath, valid, hash + hash},
		"check mit Feld":    {AccountCheckPath, valid, `{"x":1}`},
		"Body zu groß":      {AccountCheckPath, valid, `{"x":"` + strings.Repeat("a", MaxAccountBodyBytes) + `"}`},
		"check kein Objekt": {AccountCheckPath, valid, `[]`},
	} {
		r := e.call(t, e.direct, http.MethodPost, c.path, c.h, c.body)
		wantAccountError(t, name, r, http.StatusBadRequest, "invalid")
		if strings.Contains(e.lastLine(), "login=invalid") {
			t.Errorf("%s als Fehlversuch: %q", name, e.lastLine())
		}
	}
	if rot, who := e.hub.counts(); rot != 0 || who != 0 {
		t.Errorf("falsche Anfragen am Hub: rotate %d, whoami %d", rot, who)
	}
	// check mit {} ist richtig.
	if r := e.call(t, e.direct, http.MethodPost, AccountCheckPath, valid, "{}"); r.status != http.StatusOK {
		t.Errorf("check mit {}: HTTP %d %s", r.status, r.body)
	}

	// Host und Origin.
	for name, set := range map[string]func(*http.Request){
		"Host":   func(r *http.Request) { r.Host = "evil.example:80" },
		"Origin": func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") },
	} {
		req, _ := http.NewRequest(http.MethodPost, e.direct+AccountCheckPath, nil)
		for k, v := range valid {
			req.Header[k] = v
		}
		set(req)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s fremd: HTTP %d", name, resp.StatusCode)
		}
	}

	// Andere Pfade: 404, nie eine Weiterleitung.
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for _, p := range []string{"/account", "/account/", "/account/rotate/", "//account/rotate", "/account/./check",
		"/x/../account/check", "/mcp/", "/"} {
		req, _ := http.NewRequest(http.MethodPost, e.direct+p, nil)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: HTTP %d, erwartet 404", p, resp.StatusCode)
		}
	}
	if rot, who := e.hub.counts(); rot != 0 || who != 1 {
		t.Errorf("am Hub: rotate %d, whoami %d", rot, who)
	}
}

// Die Replica lässt sich nach rotate nicht schreiben: Der Erfolg bleibt —
// das Token gilt —, mit Hinweis und Anstoß des Abgleichs.
func TestAccountRotateReplicaFails(t *testing.T) {
	e := newAcctEnv(t)
	tok, next := e.newToken(t), e.newToken(t)
	e.hub.add("anna", "anna", tok, "wissen")
	// An ihrem Ort liegt ein Verzeichnis mit Inhalt: Die Replica lässt sich
	// weder öffnen noch verwerfen.
	if err := os.MkdirAll(filepath.Join(e.nodes.ReplicaPath("zentrale"), "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	r := e.rotate(t, e.direct, pair("zentrale", "anna", tok), ident.HashToken(next))
	if r.status != http.StatusOK || r.res.Note == "" || len(r.res.Replica) != 0 {
		t.Fatalf("rotate: HTTP %d %+v\n%s", r.status, r.res, r.body)
	}
	if k := e.takeKicks(); !reflect.DeepEqual(k, []string{"zentrale"}) {
		t.Errorf("Anstoß: %v", k)
	}
	if !strings.Contains(e.lastLine(), "error=") {
		t.Errorf("Logzeile ohne Fehler: %q", e.lastLine())
	}
}
