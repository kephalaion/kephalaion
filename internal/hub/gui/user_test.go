package gui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/kephalaion/kephalaion/internal/config"
	"github.com/kephalaion/kephalaion/internal/contract"
	"github.com/kephalaion/kephalaion/internal/hub/store"
	"github.com/kephalaion/kephalaion/internal/ident"
	"github.com/kephalaion/kephalaion/internal/reqlog"
)

// fixture ist ein Hub-Store mit den Collections test, vorlagen und leer und
// zwei Usern: kleist mit den Accounts laptop (in test write, der Scope
// vendor/k-playbook und die Verzeichnis-Scopes docs und a/b, in vorlagen nur
// read) und vm (gesperrt, gemerkt supersede in test), carol mit dem Account
// carol ohne Collection.
type fixture struct {
	st     store.Store
	tokens map[string]string
	log    *bytes.Buffer
	srv    http.Handler
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	st, err := store.Create(ctx, config.SQLiteDB(filepath.Join(t.TempDir(), "hub.db")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	for name, desc := range map[string]string{"test": "Zum Probieren", "vorlagen": "Vorlagen <b>für alle</b>", "leer": ""} {
		if err := st.AddCollection(ctx, name, desc); err != nil {
			t.Fatal(err)
		}
	}
	f := &fixture{st: st, tokens: map[string]string{}, log: &bytes.Buffer{}}
	// Absichtlich nicht nach Name angelegt und gewährt: Die Antwort sortiert.
	for _, a := range []struct{ name, user, desc string }{
		{"vm", "kleist", "Auf der VM"},
		{"laptop", "kleist", "Mein Laptop"},
		{"carol", "carol", ""},
	} {
		tok, err := st.AddAccount(ctx, a.name, a.user, a.desc)
		if err != nil {
			t.Fatal(err)
		}
		f.tokens[a.name] = tok
	}
	f.grant(t, "laptop", "vorlagen", contract.Rights{})
	f.grant(t, "laptop", "test", contract.Rights{Write: true, Vendor: []string{"k-playbook"}, Dirs: []string{"docs", "a/b"}})
	f.grant(t, "vm", "test", contract.Rights{Supersede: true})
	if err := st.SetAccountLocked(ctx, "vm", true); err != nil {
		t.Fatal(err)
	}
	f.serve(HeaderViewer)
	return f
}

// serve setzt den Eingang mit viewer hinter das Log.
func (f *fixture) serve(viewer Viewer) {
	f.srv = reqlog.New(f.log).Middleware("hub", NewUser(f.st, viewer))
}

func (f *fixture) grant(t *testing.T, account, collection string, r contract.Rights) {
	t.Helper()
	if _, err := f.st.GrantAccount(context.Background(), account, collection, r); err != nil {
		t.Fatal(err)
	}
}

// do schickt eine Anfrage an den Eingang, mit je einem Header X-User für
// jeden Wert in users, und liefert Antwort und Body.
func (f *fixture) do(t *testing.T, method, target string, users ...string) (*http.Response, string) {
	t.Helper()
	h := http.Header{}
	for _, u := range users {
		h.Add(HeaderUser, u)
	}
	return f.send(t, method, target, h)
}

// admin schickt eine Anfrage als user mit der Rolle des Admins der
// Weboberfläche (X-User-Roles wie von authproxy, authproxy.admin zuerst).
func (f *fixture) admin(t *testing.T, method, target, user string) (*http.Response, string) {
	t.Helper()
	return f.send(t, method, target, http.Header{HeaderUser: {user}, HeaderRoles: {"authproxy.admin," + RoleAdmin}})
}

// send schickt eine Anfrage mit den Headern h an den Eingang und liefert
// Antwort und Body.
func (f *fixture) send(t *testing.T, method, target string, h http.Header) (*http.Response, string) {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	for k, vals := range h {
		req.Header[k] = append([]string(nil), vals...)
	}
	rec := httptest.NewRecorder()
	f.srv.ServeHTTP(rec, req)
	resp := rec.Result()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, string(raw)
}

// wantHeaders prüft, was jede Antwort des Eingangs trägt.
func wantHeaders(t *testing.T, what string, resp *http.Response) {
	t.Helper()
	for k, want := range map[string]string{
		"Content-Type":           "application/json; charset=utf-8",
		"Cache-Control":          "no-store",
		"X-Content-Type-Options": "nosniff",
	} {
		if got := resp.Header.Get(k); got != want {
			t.Errorf("%s: %s %q, erwartet %q", what, k, got, want)
		}
	}
}

// wantError prüft Status und Code einer Fehlerantwort und liefert die
// Meldung.
func wantError(t *testing.T, what string, resp *http.Response, body string, status int, code string) string {
	t.Helper()
	wantHeaders(t, what, resp)
	var e errorBody
	if err := json.Unmarshal([]byte(body), &e); err != nil {
		t.Fatalf("%s: Body %q ist kein JSON: %v", what, body, err)
	}
	if resp.StatusCode != status || e.Code != code || e.Message == "" {
		t.Errorf("%s: HTTP %d %+v, erwartet %d %s", what, resp.StatusCode, e, status, code)
	}
	return e.Message
}

// Der eigene User: 200 mit viewer, user und allen Accounts nach Name — der
// gesperrte mit seinen gemerkten Rechten —, je Account die Collections nach
// Name samt Beschreibung und Rechten; vendor und dirs sind immer eine Liste.
// Weder Token noch Hash stehen in der Antwort.
func TestUser(t *testing.T) {
	f := newFixture(t)
	want := UserResponse{Viewer: "kleist", User: "kleist", Accounts: []UserAccount{
		{Name: "laptop", Description: "Mein Laptop", Collections: []UserCollection{
			{Name: "test", Description: "Zum Probieren", Rights: UserRights{Write: true, Vendor: []string{"k-playbook"},
				Dirs: []string{"a/b", "docs"}}},
			{Name: "vorlagen", Description: "Vorlagen <b>für alle</b>", Rights: UserRights{Vendor: []string{}, Dirs: []string{}}},
		}},
		{Name: "vm", Description: "Auf der VM", Locked: true, Collections: []UserCollection{
			{Name: "test", Description: "Zum Probieren", Rights: UserRights{Supersede: true, Vendor: []string{}, Dirs: []string{}}},
		}},
	}}
	// Ohne name und mit dem eigenen: dieselbe Antwort.
	for _, target := range []string{"/gui/api/user", "/gui/api/user?name=kleist", "/gui/api/user?anderes=x&name=kleist"} {
		resp, body := f.do(t, http.MethodGet, target, "kleist")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: HTTP %d: %s", target, resp.StatusCode, body)
		}
		wantHeaders(t, target, resp)
		var got UserResponse
		if err := json.Unmarshal([]byte(body), &got); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: Antwort\n%+v\nerwartet\n%+v", target, got, want)
		}
		// Die Form auf der Leitung: vendor und dirs als Liste, auch leer; read
		// steht nicht eigens da, und weder Token noch Hash.
		for _, w := range []string{`{"viewer":"kleist","admin":false,"user":"kleist","accounts":[{"name":"laptop","description":"Mein Laptop","locked":false,`,
			`"rights":{"write":true,"supersede":false,"vendor":["k-playbook"],"dirs":["a/b","docs"]}`,
			`"rights":{"write":false,"supersede":false,"vendor":[],"dirs":[]}`,
			`{"name":"vm","description":"Auf der VM","locked":true,"collections":[{"name":"test",`} {
			if !strings.Contains(body, w) {
				t.Errorf("%s: Body ohne %s:\n%s", target, w, body)
			}
		}
		for _, w := range []string{"read", "keph_", "hash", "token", ident.HashToken(f.tokens["laptop"]), ident.HashToken(f.tokens["vm"])} {
			if strings.Contains(strings.ToLower(body), w) {
				t.Errorf("%s: Body mit %q:\n%s", target, w, body)
			}
		}
	}

	// HEAD: dieselben Header, kein Body.
	resp, body := f.do(t, http.MethodHead, "/gui/api/user", "kleist")
	if resp.StatusCode != http.StatusOK || body != "" {
		t.Errorf("HEAD: HTTP %d, Body %q", resp.StatusCode, body)
	}
	wantHeaders(t, "HEAD", resp)

	// Zurückgenommen: Die Collection erscheint nicht mehr.
	if err := f.st.RevokeAccount(context.Background(), "laptop", "vorlagen"); err != nil {
		t.Fatal(err)
	}
	_, body = f.do(t, http.MethodGet, "/gui/api/user", "kleist")
	var got UserResponse
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Accounts) != 2 || len(got.Accounts[0].Collections) != 1 || got.Accounts[0].Collections[0].Name != "test" {
		t.Errorf("nach revoke: %+v", got.Accounts)
	}

	// Ein Account ohne Collection: eine leere Liste, nicht null.
	resp, body = f.do(t, http.MethodGet, "/gui/api/user", "carol")
	if resp.StatusCode != http.StatusOK || strings.TrimSpace(body) !=
		`{"viewer":"carol","admin":false,"user":"carol","accounts":[{"name":"carol","description":"","locked":false,"collections":[]}]}` {
		t.Errorf("ohne Collection: HTTP %d: %s", resp.StatusCode, body)
	}
	// Ein User ohne Account: eine leere Liste, 200.
	resp, body = f.do(t, http.MethodGet, "/gui/api/user", "dora")
	if resp.StatusCode != http.StatusOK || strings.TrimSpace(body) != `{"viewer":"dora","admin":false,"user":"dora","accounts":[]}` {
		t.Errorf("ohne Account: HTTP %d: %s", resp.StatusCode, body)
	}
}

// Ohne Anmeldung des Proxys — kein X-User, ein leerer, zwei — ist es 403
// unauthenticated; nennt sie einen Namen, der am Hub kein User sein kann,
// 403 invalid_user. Beide mit fester Meldung, Byte für Byte gleich, und nie
// 401: fail2ban zählt jede 401.
func TestUserNotLoggedIn(t *testing.T) {
	f := newFixture(t)
	for _, c := range []struct {
		what      string
		users     []string
		status    int
		code, msg string
	}{
		{"ohne X-User", nil, 403, "unauthenticated", msgUnauthenticated},
		{"leer", []string{""}, 403, "unauthenticated", msgUnauthenticated},
		{"zweimal", []string{"kleist", "carol"}, 403, "unauthenticated", msgUnauthenticated},
		{"zweimal derselbe", []string{"kleist", "kleist"}, 403, "unauthenticated", msgUnauthenticated},
		{"admin", []string{"admin"}, 403, "invalid_user", msgInvalidUser},
		{"groß", []string{"Kleist"}, 403, "invalid_user", msgInvalidUser},
		{"system", []string{"system-x"}, 403, "invalid_user", msgInvalidUser},
		{"mit Doppelpunkt", []string{"hub:kleist"}, 403, "invalid_user", msgInvalidUser},
		{"zwei in einem", []string{"kleist, carol"}, 403, "invalid_user", msgInvalidUser},
		{"64 Zeichen", []string{strings.Repeat("a", 64)}, 403, "invalid_user", msgInvalidUser},
	} {
		for _, target := range []string{"/gui/api/user", "/gui/api/user?name=kleist"} {
			what := c.what + " " + target
			resp, body := f.do(t, http.MethodGet, target, c.users...)
			if msg := wantError(t, what, resp, body, c.status, c.code); msg != c.msg {
				t.Errorf("%s: Meldung %q, erwartet %q", what, msg, c.msg)
			}
			if want, _ := json.Marshal(errorBody{Code: c.code, Message: c.msg}); strings.TrimSpace(body) != string(want) {
				t.Errorf("%s: Body %q, erwartet %s", what, body, want)
			}
			if strings.Contains(body, "kleist") || strings.Contains(body, "carol") {
				t.Errorf("%s: die Meldung nennt einen Namen: %s", what, body)
			}
		}
	}
	if msgUnauthenticated == msgInvalidUser {
		t.Error("dieselbe Meldung für keine Anmeldung und ungültigen User")
	}
	if !strings.Contains(msgUnauthenticated, "Keine Anmeldung des Proxys") || msgInvalidUser != "Dieser Name ist am Hub kein gültiger User." {
		t.Errorf("Meldungen %q, %q", msgUnauthenticated, msgInvalidUser)
	}
}

// Die Query: höchstens ein name (sonst 400 invalid), ein anderer als der
// eigene 403 forbidden — auch ein leeres name=, das nicht als „ohne name“
// gilt, und ein Name, den es am Hub gar nicht geben kann.
func TestUserQuery(t *testing.T) {
	f := newFixture(t)
	for _, c := range []struct {
		what, query string
		status      int
		code        string
	}{
		{"fremder User", "name=carol", 403, "forbidden"},
		{"fremder User ohne Account", "name=dora", 403, "forbidden"},
		{"leer", "name=", 403, "forbidden"},
		{"ohne =", "name", 403, "forbidden"},
		{"groß geschrieben", "name=Kleist", 403, "forbidden"},
		{"admin", "name=admin", 403, "forbidden"},
		{"zweimal", "name=kleist&name=carol", 400, "invalid"},
		{"zweimal derselbe", "name=kleist&name=kleist", 400, "invalid"},
		{"zweimal leer", "name=&name=", 400, "invalid"},
		{"nicht lesbar", "name=%zz", 400, "invalid"},
		{"nicht lesbar, Semikolon", "name=kleist;x=y", 400, "invalid"},
	} {
		resp, body := f.do(t, http.MethodGet, "/gui/api/user?"+c.query, "kleist")
		msg := wantError(t, c.what, resp, body, c.status, c.code)
		if c.code == "forbidden" && msg != msgForbidden {
			t.Errorf("%s: Meldung %q", c.what, msg)
		}
		if strings.Contains(body, "carol") || strings.Contains(body, "dora") {
			t.Errorf("%s: die Antwort nennt den fremden User: %s", c.what, body)
		}
	}
}

// Nur GET und HEAD: alles andere 405 mit Allow, bevor die Anmeldung gilt.
func TestUserMethod(t *testing.T) {
	f := newFixture(t)
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch, http.MethodOptions} {
		for _, users := range [][]string{nil, {"kleist"}} {
			resp, body := f.do(t, m, "/gui/api/user", users...)
			if msg := wantError(t, m, resp, body, http.StatusMethodNotAllowed, "invalid"); msg != "nur GET" {
				t.Errorf("%s: Meldung %q", m, msg)
			}
			if resp.Header.Get("Allow") != "GET, HEAD" {
				t.Errorf("%s: Allow %q", m, resp.Header.Get("Allow"))
			}
		}
	}
	// HEAD ohne Anmeldung: 403 ohne Body.
	resp, body := f.do(t, http.MethodHead, "/gui/api/user")
	if resp.StatusCode != http.StatusForbidden || body != "" {
		t.Errorf("HEAD ohne Anmeldung: HTTP %d, Body %q", resp.StatusCode, body)
	}
}

// HeaderViewer glaubt genau einem, nicht leeren X-User — nicht X_User, nicht
// X-User-Email.
func TestHeaderViewer(t *testing.T) {
	for _, c := range []struct {
		what   string
		header http.Header
		user   string
		ok     bool
	}{
		{"ohne", http.Header{}, "", false},
		{"einer", http.Header{"X-User": {"kleist"}}, "kleist", true},
		{"wörtlich", http.Header{"X-User": {"Kleist, carol"}}, "Kleist, carol", true},
		{"leer", http.Header{"X-User": {""}}, "", false},
		{"zwei", http.Header{"X-User": {"kleist", "carol"}}, "", false},
		{"mit Unterstrich", http.Header{"X_user": {"kleist"}}, "", false},
		{"nur E-Mail", http.Header{"X-User-Email": {"kleist@example.org"}}, "", false},
		{"nur Rollen", http.Header{"X-User-Roles": {"kephalaion.admin"}}, "", false},
	} {
		r := httptest.NewRequest(http.MethodGet, "/gui/api/user", nil)
		r.Header = c.header
		if id, ok := HeaderViewer(r); id.User != c.user || ok != c.ok || id.Admin {
			t.Errorf("%s: %+v %v, erwartet %q %v ohne Admin", c.what, id, ok, c.user, c.ok)
		}
	}
}

// Admin der Weboberfläche ist nur, wessen Anfrage einen gültigen X-User und
// genau einen X-User-Roles trägt, der unter seinen durch Komma getrennten
// Einträgen genau kephalaion.admin enthält; Leerraum um einen Eintrag zählt
// nicht, Groß- und Kleinschreibung zählt. Andere Rollen, Teiltreffer, ein
// zweiter Header und die Varianten mit _ zählen nicht.
func TestHeaderViewerRoles(t *testing.T) {
	for _, c := range []struct {
		what  string
		roles []string // Werte von X-User-Roles; nil: kein Header
		admin bool
	}{
		{"ohne", nil, false},
		{"allein", []string{"kephalaion.admin"}, true},
		{"wie authproxy", []string{"authproxy.admin,kephalaion.admin"}, true},
		{"vorn", []string{"kephalaion.admin,authproxy.admin"}, true},
		{"mit Leerraum", []string{" authproxy.admin , 	kephalaion.admin "}, true},
		{"leere Einträge", []string{",,kephalaion.admin,"}, true},
		{"nur authproxy.admin", []string{"authproxy.admin"}, false},
		{"andere Rolle", []string{"kephalaion.leser"}, false},
		{"leer", []string{""}, false},
		{"Teiltreffer hinten", []string{"kephalaion.admins"}, false},
		{"Teiltreffer vorn", []string{"xkephalaion.admin"}, false},
		{"ohne Präfix", []string{"admin"}, false},
		{"groß", []string{"Kephalaion.Admin"}, false},
		{"mit Leerraum innen", []string{"kephalaion. admin"}, false},
		{"Semikolon", []string{"authproxy.admin;kephalaion.admin"}, false},
		{"zwei Header", []string{"authproxy.admin", "kephalaion.admin"}, false},
		{"zweimal derselbe", []string{"kephalaion.admin", "kephalaion.admin"}, false},
	} {
		r := httptest.NewRequest(http.MethodGet, "/gui/api/users", nil)
		r.Header = http.Header{"X-User": {"kleist"}}
		if c.roles != nil {
			r.Header[HeaderRoles] = c.roles
		}
		if id, ok := HeaderViewer(r); !ok || id.User != "kleist" || id.Admin != c.admin {
			t.Errorf("%s: %+v %v, erwartet kleist mit Admin %v", c.what, id, ok, c.admin)
		}
	}
	// Die Rolle unter einem anderen Namen zählt nicht, ohne X-User auch nicht.
	for _, h := range []http.Header{
		{"X-User": {"kleist"}, "X_user_roles": {"kephalaion.admin"}},
		{"X-User": {"kleist"}, "X-User-Role": {"kephalaion.admin"}},
		{"X-User": {"kleist"}, "X-Roles": {"kephalaion.admin"}},
		{"X-User": {"kleist, kephalaion.admin"}},
		{"X-User-Roles": {"kephalaion.admin"}},
		{"X-User": {""}, "X-User-Roles": {"kephalaion.admin"}},
		{"X-User": {"kleist", "carol"}, "X-User-Roles": {"kephalaion.admin"}},
	} {
		r := httptest.NewRequest(http.MethodGet, "/gui/api/users", nil)
		r.Header = h
		if id, _ := HeaderViewer(r); id.Admin {
			t.Errorf("%v: Admin", h)
		}
	}
}

// Wem der Eingang glaubt, entscheidet allein der übergebene Viewer: Mit
// einem eigenen zählen X-User und X-User-Roles nicht.
func TestUserViewer(t *testing.T) {
	f := newFixture(t)
	f.serve(func(*http.Request) (Identity, bool) { return Identity{User: "carol"}, true })
	resp, body := f.admin(t, http.MethodGet, "/gui/api/user", "kleist")
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(body, `{"viewer":"carol","admin":false,"user":"carol",`) {
		t.Errorf("eigener Viewer: HTTP %d %s", resp.StatusCode, body)
	}
	resp, body = f.admin(t, http.MethodGet, "/gui/api/user?name=kleist", "kleist")
	wantError(t, "eigener Viewer, name des Headers", resp, body, http.StatusForbidden, "forbidden")
	f.serve(func(*http.Request) (Identity, bool) { return Identity{User: "carol", Admin: true}, true })
	resp, body = f.do(t, http.MethodGet, "/gui/api/user?name=kleist")
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(body, `{"viewer":"carol","admin":true,"user":"kleist",`) {
		t.Errorf("eigener Viewer mit Admin: HTTP %d %s", resp.StatusCode, body)
	}
	// Ohne gültigen User zählt die Rolle nicht, auch wenn der Viewer sie
	// meldet.
	f.serve(func(*http.Request) (Identity, bool) { return Identity{User: "admin", Admin: true}, true })
	resp, body = f.do(t, http.MethodGet, "/gui/api/user?name=kleist")
	wantError(t, "Viewer mit ungültigem User und Admin", resp, body, http.StatusForbidden, "invalid_user")
	f.serve(func(*http.Request) (Identity, bool) { return Identity{Admin: true}, false })
	resp, body = f.do(t, http.MethodGet, "/gui/api/user", "kleist")
	wantError(t, "Viewer ohne Anmeldung", resp, body, http.StatusForbidden, "unauthenticated")
}

// Das Log nennt je Anfrage den angemeldeten und den angefragten User,
// maskiert, was kein Name ist — nie Token oder Hash.
func TestUserLog(t *testing.T) {
	f := newFixture(t)
	f.do(t, http.MethodGet, "/gui/api/user", "kleist")
	f.do(t, http.MethodGet, "/gui/api/user?name=carol", "kleist")
	f.do(t, http.MethodGet, "/gui/api/user")
	f.do(t, http.MethodGet, "/gui/api/user", "Kleist")
	f.do(t, http.MethodGet, "/gui/api/user?name=kleist&name=x", "kleist")
	f.do(t, http.MethodGet, "/gui/api/user?name="+f.tokens["laptop"], "kleist")
	log := f.log.String()
	for _, want := range []string{
		"hub GET /gui/api/user 200 ", " viewer=kleist admin=nein user=kleist\n",
		"hub GET /gui/api/user 403 ", " viewer=kleist admin=nein user=carol\n",
		" viewer=-\n", " viewer=(ungültig)\n",
		"hub GET /gui/api/user 400 ", " viewer=kleist admin=nein user=(ungültig)\n",
	} {
		if !strings.Contains(log, want) {
			t.Errorf("Log ohne %q:\n%s", want, log)
		}
	}
	for _, secret := range []string{"keph_", ident.HashToken(f.tokens["laptop"])} {
		if strings.Contains(log, secret) {
			t.Errorf("Log mit %q:\n%s", secret, log)
		}
	}
	if n := strings.Count(log, "\n"); n != 6 {
		t.Errorf("%d Logzeilen, erwartet 6:\n%s", n, log)
	}
}

// failing ist ein Store, dessen Lesen scheitert.
type failing struct {
	store.Store
	accounts, collections, all error
}

func (s failing) Accounts(ctx context.Context) ([]store.Account, error) {
	if s.all != nil {
		return nil, s.all
	}
	return s.Store.Accounts(ctx)
}

func (s failing) AccountsOfUser(ctx context.Context, user string) ([]store.Account, error) {
	if s.accounts != nil {
		return nil, s.accounts
	}
	return s.Store.AccountsOfUser(ctx, user)
}

func (s failing) Collections(ctx context.Context) ([]store.Collection, error) {
	if s.collections != nil {
		return nil, s.collections
	}
	return s.Store.Collections(ctx)
}

// Ein Fehler des Stores ist 500 internal mit festem Text; die Einzelheit
// steht nur im Log.
func TestUserStoreError(t *testing.T) {
	f := newFixture(t)
	boom := errors.New("database is locked (hub.db)")
	for _, st := range []failing{{Store: f.st, accounts: boom}, {Store: f.st, collections: boom}} {
		f.log.Reset()
		f.srv = reqlog.New(f.log).Middleware("hub", NewUser(st, HeaderViewer))
		resp, body := f.do(t, http.MethodGet, "/gui/api/user", "kleist")
		if msg := wantError(t, "Fehler des Stores", resp, body, http.StatusInternalServerError, "internal"); msg != "Fehler am Hub" {
			t.Errorf("Meldung %q", msg)
		}
		if strings.Contains(body, "locked") {
			t.Errorf("die Antwort nennt die Einzelheit: %s", body)
		}
		if log := f.log.String(); !strings.Contains(log, `error="database is locked (hub.db)"`) ||
			!strings.Contains(log, "viewer=kleist admin=nein user=kleist") {
			t.Errorf("Log:\n%s", log)
		}
	}
}
