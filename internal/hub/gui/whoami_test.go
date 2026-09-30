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
// den Accounts bob (User kleist: in test write, der Scope vendor/k-playbook
// und die Verzeichnis-Scopes docs und a/b, in vorlagen nur read) und carol
// ohne Collection.
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
	for _, a := range []struct{ name, user, desc string }{
		{"bob", "kleist", "Bobs Sitzung"},
		{"carol", "carol", ""},
	} {
		tok, err := st.AddAccount(ctx, a.name, a.user, a.desc)
		if err != nil {
			t.Fatal(err)
		}
		f.tokens[a.name] = tok
	}
	// Absichtlich nicht nach Name gewährt: Die Antwort sortiert.
	f.grant(t, "bob", "vorlagen", contract.Rights{})
	f.grant(t, "bob", "test", contract.Rights{Write: true, Vendor: []string{"k-playbook"}, Dirs: []string{"docs", "a/b"}})
	f.srv = reqlog.New(f.log).Middleware("hub", NewWhoami(st))
	return f
}

func (f *fixture) grant(t *testing.T, account, collection string, r contract.Rights) {
	t.Helper()
	if _, err := f.st.GrantAccount(context.Background(), account, collection, r); err != nil {
		t.Fatal(err)
	}
}

// do schickt eine Anfrage an den Eingang und liefert Antwort und Body.
func (f *fixture) do(t *testing.T, method, ctype, body string) (*http.Response, string) {
	t.Helper()
	req := httptest.NewRequest(method, "/gui/api/whoami", strings.NewReader(body))
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
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

// post schickt Account und Token als JSON.
func (f *fixture) post(t *testing.T, account, token string) (*http.Response, string) {
	t.Helper()
	body, err := json.Marshal(map[string]string{"account": account, "token": token})
	if err != nil {
		t.Fatal(err)
	}
	return f.do(t, http.MethodPost, "application/json", string(body))
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

// Ein Account mit zwei Collections: 200 mit User, Beschreibung, den
// Collections nach Name samt Beschreibung und Rechten; vendor und dirs sind
// immer eine Liste. Nach RevokeAccount fehlt die Collection, ohne Collection
// ist die Liste leer, nicht null.
func TestWhoami(t *testing.T) {
	f := newFixture(t)
	resp, body := f.post(t, "bob", f.tokens["bob"])
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("HTTP %d: %s", resp.StatusCode, body)
	}
	wantHeaders(t, "200", resp)
	var got WhoamiResponse
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}
	want := WhoamiResponse{Account: "bob", User: "kleist", Description: "Bobs Sitzung", Collections: []WhoamiCollection{
		{Name: "test", Description: "Zum Probieren", Rights: WhoamiRights{Write: true, Vendor: []string{"k-playbook"},
			Dirs: []string{"a/b", "docs"}}},
		{Name: "vorlagen", Description: "Vorlagen <b>für alle</b>", Rights: WhoamiRights{Vendor: []string{}, Dirs: []string{}}},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Antwort\n%+v\nerwartet\n%+v", got, want)
	}
	// Die Form auf der Leitung: vendor und dirs als Liste, auch leer; read
	// steht nicht eigens da, und weder Token noch Hash.
	for _, w := range []string{`"rights":{"write":true,"supersede":false,"vendor":["k-playbook"],"dirs":["a/b","docs"]}`,
		`"rights":{"write":false,"supersede":false,"vendor":[],"dirs":[]}`} {
		if !strings.Contains(body, w) {
			t.Errorf("Body ohne %s:\n%s", w, body)
		}
	}
	for _, w := range []string{"read", "keph_", "hash", ident.HashToken(f.tokens["bob"])} {
		if strings.Contains(body, w) {
			t.Errorf("Body mit %q:\n%s", w, body)
		}
	}

	// Content-Type mit Parameter geht auch.
	raw, _ := json.Marshal(map[string]string{"account": "bob", "token": f.tokens["bob"]})
	if resp, body := f.do(t, http.MethodPost, "application/json; charset=utf-8", string(raw)); resp.StatusCode != http.StatusOK {
		t.Errorf("mit charset: HTTP %d: %s", resp.StatusCode, body)
	}

	// Zurückgenommen: Die Collection erscheint nicht mehr.
	if err := f.st.RevokeAccount(context.Background(), "bob", "vorlagen"); err != nil {
		t.Fatal(err)
	}
	_, body = f.post(t, "bob", f.tokens["bob"])
	got = WhoamiResponse{}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Collections) != 1 || got.Collections[0].Name != "test" {
		t.Errorf("nach revoke: %+v", got.Collections)
	}

	// Ohne Collection: eine leere Liste.
	resp, body = f.post(t, "carol", f.tokens["carol"])
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"collections":[]`) {
		t.Errorf("ohne Collection: HTTP %d: %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, `"account":"carol","user":"carol","description":""`) {
		t.Errorf("ohne Collection: %s", body)
	}
}

// Unbekannter Account, falsches Token und gesperrter Account sind dieselbe
// Antwort, Byte für Byte.
func TestWhoamiUnauthenticated(t *testing.T) {
	f := newFixture(t)
	other, err := ident.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	if err := f.st.SetAccountLocked(context.Background(), "carol", true); err != nil {
		t.Fatal(err)
	}
	var first string
	for _, c := range []struct{ what, account, token string }{
		{"unbekannter Account", "niemand", f.tokens["bob"]},
		{"falsches Token", "bob", other},
		{"Token eines anderen Accounts", "bob", f.tokens["carol"]},
		{"gesperrter Account", "carol", f.tokens["carol"]},
		// admin ist als Name reserviert, der Form nach aber ein Name.
		{"admin", "admin", other},
	} {
		resp, body := f.post(t, c.account, c.token)
		if msg := wantError(t, c.what, resp, body, http.StatusUnauthorized, "unauthenticated"); msg != "Account oder Token stimmt nicht." {
			t.Errorf("%s: Meldung %q", c.what, msg)
		}
		if first == "" {
			first = body
		}
		if body != first {
			t.Errorf("%s: Body %q weicht ab von %q", c.what, body, first)
		}
		if got := resp.Header; !reflect.DeepEqual(got, http.Header{"Content-Type": {"application/json; charset=utf-8"},
			"Cache-Control": {"no-store"}, "X-Content-Type-Options": {"nosniff"}}) {
			t.Errorf("%s: Header %v", c.what, got)
		}
	}
}

// Eine Anfrage in falscher Form ist 400 invalid mit einem Grund, der nichts
// aus dem Token wiedergibt; die Datenbank wird dafür nicht gefragt.
func TestWhoamiInvalid(t *testing.T) {
	f := newFixture(t)
	tok := f.tokens["bob"]
	obj := func(account, token string) string {
		b, _ := json.Marshal(map[string]string{"account": account, "token": token})
		return string(b)
	}
	for _, c := range []struct{ what, body, want string }{
		{"kein JSON", "account=bob&token=x", "kein gültiges JSON"},
		{"leer", "", "kein gültiges JSON"},
		{"kein Objekt", `["bob"]`, "kein gültiges JSON"},
		{"kein UTF-8", "{\"account\":\"bob\xff\",\"token\":\"" + tok + "\"}", "kein UTF-8"},
		{"zwei Objekte", obj("bob", tok) + obj("bob", tok), "nach dem Objekt"},
		{"Text danach", obj("bob", tok) + " x", "nach dem Objekt"},
		{"Account fehlt", `{"token":"` + tok + `"}`, "account fehlt"},
		{"Token fehlt", `{"account":"bob"}`, "token fehlt"},
		{"Account leer", obj("", tok), "account fehlt"},
		{"Token leer", obj("bob", ""), "token fehlt"},
		{"Felder null", `{"account":null,"token":null}`, "account fehlt"},
		{"null", `null`, "account fehlt"},
		{"unbekanntes Feld", `{"account":"bob","token":"` + tok + `","node":"laptop"}`, "genau den Feldern account und token"},
		{"Token ohne keph_", obj("bob", "hunter2-mein-Passwort"), "beginnt nicht mit keph_"},
		{"Token zu kurz", obj("bob", tok[:len(tok)-1]), "32 Bytes base64url"},
		{"Token zu lang", obj("bob", tok+"A"), "32 Bytes base64url"},
		{"Token mit Zeichen außerhalb", obj("bob", tok[:len(tok)-1]+"+"), "32 Bytes base64url"},
		{"Account groß", obj("Bob", tok), "ungültiger Name"},
		{"Account mit Doppelpunkt", obj("hub:bob", tok), "ungültiger Name"},
		{"Account system", obj("system-x", tok), "reserviert"},
		{"Token als Account", obj(tok, tok), "ungültiger Name"},
	} {
		resp, body := f.do(t, http.MethodPost, "application/json", c.body)
		msg := wantError(t, c.what, resp, body, http.StatusBadRequest, "invalid")
		if !strings.Contains(msg, c.want) {
			t.Errorf("%s: Meldung %q ohne %q", c.what, msg, c.want)
		}
		if strings.Contains(msg, "hunter2") {
			t.Errorf("%s: die Meldung gibt das Token wieder: %q", c.what, msg)
		}
	}
	// Das gültige Token steht in keiner Meldung — außer dort, wo es als Name
	// des Accounts kam; ins Log kommt auch das nicht.
	if log := f.log.String(); strings.Contains(log, "keph_") || strings.Contains(log, "hunter2") {
		t.Errorf("Token im Log:\n%s", log)
	}
	if !strings.Contains(f.log.String(), "account=(ungültig)") {
		t.Errorf("Log ohne maskierten Namen:\n%s", f.log.String())
	}
}

// Methode, Content-Type und Größe: 405 mit Allow, 415, 413 — vor jeder
// Prüfung des Inhalts.
func TestWhoamiTransport(t *testing.T) {
	f := newFixture(t)
	good, _ := json.Marshal(map[string]string{"account": "bob", "token": f.tokens["bob"]})
	for _, m := range []string{http.MethodGet, http.MethodHead, http.MethodPut, http.MethodDelete, http.MethodOptions} {
		resp, body := f.do(t, m, "application/json", string(good))
		if resp.StatusCode != http.StatusMethodNotAllowed || resp.Header.Get("Allow") != "POST" {
			t.Errorf("%s: HTTP %d, Allow %q", m, resp.StatusCode, resp.Header.Get("Allow"))
		}
		if m == http.MethodHead {
			if body != "" {
				t.Errorf("HEAD: Body %q", body)
			}
			wantHeaders(t, m, resp)
			continue
		}
		wantError(t, m, resp, body, http.StatusMethodNotAllowed, "invalid")
	}
	for _, ctype := range []string{"", "text/plain", "application/x-www-form-urlencoded", "multipart/form-data; boundary=x",
		"application/jsonx", "text/json", "application/json; charset"} {
		resp, body := f.do(t, http.MethodPost, ctype, string(good))
		wantError(t, "Content-Type "+ctype, resp, body, http.StatusUnsupportedMediaType, "invalid")
	}
	if log := f.log.String(); strings.Contains(log, "account=") {
		t.Errorf("Name im Log, bevor der Body gelesen ist:\n%s", log)
	}
	// Genau an der Grenze ist es noch die Form, darüber 413.
	pad := func(n int) string { return `{"account":"` + strings.Repeat("a", n-len(`{"account":""}`)) + `"}` }
	resp, body := f.do(t, http.MethodPost, "application/json", pad(MaxWhoamiBytes))
	wantError(t, "4 KiB", resp, body, http.StatusBadRequest, "invalid")
	resp, body = f.do(t, http.MethodPost, "application/json", pad(MaxWhoamiBytes+1))
	if msg := wantError(t, "über 4 KiB", resp, body, http.StatusRequestEntityTooLarge, "invalid"); !strings.Contains(msg, "4096 Bytes") {
		t.Errorf("413: Meldung %q", msg)
	}
}

// Das Log nennt je Anfrage den Account, nie das Token und nie seinen Hash —
// bei Erfolg, bei 401 und bei 400.
func TestWhoamiLog(t *testing.T) {
	f := newFixture(t)
	other, err := ident.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	f.post(t, "bob", f.tokens["bob"])
	f.post(t, "bob", other)
	f.post(t, "niemand", other)
	f.post(t, "Bob", other)
	log := f.log.String()
	for _, want := range []string{"hub POST /gui/api/whoami 200 ", "hub POST /gui/api/whoami 401 ",
		"hub POST /gui/api/whoami 400 ", " account=bob\n", " account=niemand\n", " account=(ungültig)\n"} {
		if !strings.Contains(log, want) {
			t.Errorf("Log ohne %q:\n%s", want, log)
		}
	}
	for _, secret := range []string{f.tokens["bob"], other, ident.HashToken(f.tokens["bob"]), ident.HashToken(other), "keph_"} {
		if strings.Contains(log, secret) {
			t.Errorf("Log mit %q:\n%s", secret, log)
		}
	}
	if n := strings.Count(log, "\n"); n != 4 {
		t.Errorf("%d Logzeilen, erwartet 4:\n%s", n, log)
	}
}

// failing ist ein Store, dessen Lesen scheitert.
type failing struct {
	store.Store
	account, collections error
}

func (s failing) Account(ctx context.Context, name string) (store.Account, error) {
	if s.account != nil {
		return store.Account{}, s.account
	}
	return s.Store.Account(ctx, name)
}

func (s failing) Collections(ctx context.Context) ([]store.Collection, error) {
	if s.collections != nil {
		return nil, s.collections
	}
	return s.Store.Collections(ctx)
}

// Ein Fehler des Stores ist 500 internal mit festem Text; die Einzelheit
// steht nur im Log.
func TestWhoamiStoreError(t *testing.T) {
	f := newFixture(t)
	boom := errors.New("database is locked (hub.db)")
	for _, st := range []failing{{Store: f.st, account: boom}, {Store: f.st, collections: boom}} {
		f.log.Reset()
		f.srv = reqlog.New(f.log).Middleware("hub", NewWhoami(st))
		resp, body := f.post(t, "bob", f.tokens["bob"])
		if msg := wantError(t, "Fehler des Stores", resp, body, http.StatusInternalServerError, "internal"); msg != "Fehler am Hub" {
			t.Errorf("Meldung %q", msg)
		}
		if strings.Contains(body, "locked") {
			t.Errorf("die Antwort nennt die Einzelheit: %s", body)
		}
		if log := f.log.String(); !strings.Contains(log, `error="database is locked (hub.db)"`) || !strings.Contains(log, "account=bob") ||
			strings.Contains(log, "keph_") {
			t.Errorf("Log:\n%s", log)
		}
	}
}
