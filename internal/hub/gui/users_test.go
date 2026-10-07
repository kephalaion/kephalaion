package gui

import (
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/kephalaion/kephalaion/internal/contract"
	"github.com/kephalaion/kephalaion/internal/ident"
	"github.com/kephalaion/kephalaion/internal/reqlog"
)

// Ein Admin der Weboberfläche darf in gui/api/user jeden User abfragen: Die
// Antwort nennt ihn als viewer mit admin true und den angefragten als user,
// samt Accounts wie für diesen selbst. Ein gültiger User ohne Account ist
// eine leere Liste; ein name, der kein gültiger User-Name ist — auch ein
// leeres name= —, ist 400 invalid, nie still der eigene User.
func TestUserAdmin(t *testing.T) {
	f := newFixture(t)
	resp, body := f.admin(t, http.MethodGet, "/gui/api/user?name=kleist", "carol")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Admin, fremder User: HTTP %d %s", resp.StatusCode, body)
	}
	wantHeaders(t, "Admin, fremder User", resp)
	var got UserResponse
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}
	// Dieselben Accounts wie für kleist selbst, nur viewer und admin
	// unterscheiden sich.
	_, own := f.do(t, http.MethodGet, "/gui/api/user", "kleist")
	var want UserResponse
	if err := json.Unmarshal([]byte(own), &want); err != nil {
		t.Fatal(err)
	}
	want.Viewer, want.Admin = "carol", true
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Admin, fremder User:\n%+v\nerwartet\n%+v", got, want)
	}
	if !strings.HasPrefix(body, `{"viewer":"carol","admin":true,"user":"kleist","accounts":[{"name":"laptop",`) {
		t.Errorf("Admin, fremder User: Body %s", body)
	}
	for _, w := range []string{"keph_", "hash", "token", ident.HashToken(f.tokens["laptop"]), ident.HashToken(f.tokens["vm"])} {
		if strings.Contains(strings.ToLower(body), w) {
			t.Errorf("Admin, fremder User: Body mit %q", w)
		}
	}
	for _, c := range []struct{ target, body string }{
		{"/gui/api/user", `{"viewer":"kleist","admin":true,"user":"kleist","accounts":[{"name":"laptop",`},
		{"/gui/api/user?name=kleist", `{"viewer":"kleist","admin":true,"user":"kleist","accounts":[{"name":"laptop",`},
		{"/gui/api/user?name=carol", `{"viewer":"kleist","admin":true,"user":"carol","accounts":[{"name":"carol",`},
		{"/gui/api/user?name=dora", `{"viewer":"kleist","admin":true,"user":"dora","accounts":[]}`},
	} {
		resp, body := f.admin(t, http.MethodGet, c.target, "kleist")
		if resp.StatusCode != http.StatusOK || !strings.HasPrefix(body, c.body) {
			t.Errorf("Admin %s: HTTP %d %s", c.target, resp.StatusCode, body)
		}
	}
	for _, c := range []struct {
		what, query string
		status      int
		code        string
	}{
		{"leer", "name=", 400, "invalid"},
		{"ohne =", "name", 400, "invalid"},
		{"groß geschrieben", "name=Kleist", 400, "invalid"},
		{"admin", "name=admin", 400, "invalid"},
		{"system", "name=system-x", 400, "invalid"},
		{"mit Doppelpunkt", "name=hub:kleist", 400, "invalid"},
		{"64 Zeichen", "name=" + strings.Repeat("a", 64), 400, "invalid"},
		{"zwei in einem", "name=kleist,carol", 400, "invalid"},
		{"zweimal", "name=kleist&name=carol", 400, "invalid"},
		{"nicht lesbar", "name=%zz", 400, "invalid"},
	} {
		resp, body := f.admin(t, http.MethodGet, "/gui/api/user?"+c.query, "kleist")
		msg := wantError(t, "Admin, "+c.what, resp, body, c.status, c.code)
		if c.what != "zweimal" && c.what != "nicht lesbar" && msg != msgInvalidName {
			t.Errorf("Admin, %s: Meldung %q", c.what, msg)
		}
		if strings.Contains(body, "accounts") || strings.Contains(body, "carol") {
			t.Errorf("Admin, %s: Body %s", c.what, body)
		}
	}
	// Nur die Rolle macht den Unterschied: Mit authproxy.admin allein oder
	// einem Teiltreffer bleibt es bei 403 forbidden.
	for _, roles := range []string{"authproxy.admin", "kephalaion.admins", "Kephalaion.admin"} {
		resp, body := f.send(t, http.MethodGet, "/gui/api/user?name=carol", http.Header{HeaderUser: {"kleist"}, HeaderRoles: {roles}})
		if msg := wantError(t, "Rolle "+roles, resp, body, http.StatusForbidden, "forbidden"); msg != msgForbidden {
			t.Errorf("Rolle %s: Meldung %q", roles, msg)
		}
	}
	// Ohne gültigen User zählt die Rolle nicht.
	resp, body = f.admin(t, http.MethodGet, "/gui/api/user?name=kleist", "admin")
	wantError(t, "Rolle mit User admin", resp, body, http.StatusForbidden, "invalid_user")
	resp, body = f.send(t, http.MethodGet, "/gui/api/user?name=kleist", http.Header{HeaderRoles: {RoleAdmin}})
	wantError(t, "Rolle ohne X-User", resp, body, http.StatusForbidden, "unauthenticated")
}

// Die Liste aller Accounts für einen Admin: User nach Name, je User seine
// Accounts nach Name mit Sperre und den Namen der Collections — bei einem
// gesperrten Account die gemerkten. Ein Account ohne Collection ist eine
// leere Liste. Weder Token noch Hash, auch keine Beschreibung oder Rechte.
func TestUsers(t *testing.T) {
	f := newFixture(t)
	f.srv = reqlog.New(f.log).Middleware("hub", NewUsers(f.st, HeaderViewer))
	const want = `{"viewer":"kleist","admin":true,"users":[` +
		`{"name":"carol","accounts":[{"name":"carol","locked":false,"collections":[]}]},` +
		`{"name":"kleist","accounts":[{"name":"laptop","locked":false,"collections":["test","vorlagen"]},` +
		`{"name":"vm","locked":true,"collections":["test"]}]}]}`
	for _, target := range []string{"/gui/api/users", "/gui/api/users?name=carol"} {
		resp, body := f.admin(t, http.MethodGet, target, "kleist")
		if resp.StatusCode != http.StatusOK || strings.TrimSpace(body) != want {
			t.Errorf("%s: HTTP %d\n%s\nerwartet\n%s", target, resp.StatusCode, body, want)
		}
		wantHeaders(t, target, resp)
		for _, w := range []string{"keph_", "hash", "token", "description", "rights", "Mein Laptop",
			ident.HashToken(f.tokens["laptop"]), ident.HashToken(f.tokens["vm"]), ident.HashToken(f.tokens["carol"])} {
			if strings.Contains(strings.ToLower(body), strings.ToLower(w)) {
				t.Errorf("%s: Body mit %q", target, w)
			}
		}
	}
	// HEAD: dieselben Header, kein Body.
	resp, body := f.admin(t, http.MethodHead, "/gui/api/users", "kleist")
	if resp.StatusCode != http.StatusOK || body != "" {
		t.Errorf("HEAD: HTTP %d, Body %q", resp.StatusCode, body)
	}
	wantHeaders(t, "HEAD", resp)

	// Ein weiterer User mit Accounts, nicht nach Name angelegt: Die Liste
	// sortiert User und Accounts; ein entsperrter Account nennt seine
	// Collections wieder aus den Zeilen.
	ctx := t.Context()
	for _, a := range []struct{ name, user string }{{"zz-mac", "berta"}, {"aa-pc", "berta"}} {
		if _, err := f.st.AddAccount(ctx, a.name, a.user, ""); err != nil {
			t.Fatal(err)
		}
	}
	f.grant(t, "aa-pc", "leer", contract.Rights{})
	if err := f.st.SetAccountLocked(ctx, "vm", false); err != nil {
		t.Fatal(err)
	}
	_, body = f.admin(t, http.MethodGet, "/gui/api/users", "carol")
	var got UsersResponse
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}
	wantUsers := UsersResponse{Viewer: "carol", Admin: true, Users: []UsersUser{
		{Name: "berta", Accounts: []UsersAccount{{Name: "aa-pc", Collections: []string{"leer"}}, {Name: "zz-mac", Collections: []string{}}}},
		{Name: "carol", Accounts: []UsersAccount{{Name: "carol", Collections: []string{}}}},
		{Name: "kleist", Accounts: []UsersAccount{{Name: "laptop", Collections: []string{"test", "vorlagen"}},
			{Name: "vm", Collections: []string{"test"}}}},
	}}
	if !reflect.DeepEqual(got, wantUsers) {
		t.Errorf("Liste:\n%+v\nerwartet\n%+v", got, wantUsers)
	}
}

// Ohne die Rolle ist die Liste 403 forbidden mit fester Meldung — auch mit
// authproxy.admin, einem Teiltreffer oder zwei Headern; ohne Anmeldung 403
// unauthenticated, mit ungültigem User 403 invalid_user, auch mit der Rolle.
// Nie 401, nie ein Name eines anderen Users in der Antwort.
func TestUsersNotAllowed(t *testing.T) {
	f := newFixture(t)
	f.srv = reqlog.New(f.log).Middleware("hub", NewUsers(f.st, HeaderViewer))
	for _, c := range []struct {
		what   string
		header http.Header
		status int
		code   string
		msg    string
	}{
		{"ohne Rolle", http.Header{HeaderUser: {"kleist"}}, 403, "forbidden", msgUsersForbidden},
		{"authproxy.admin", http.Header{HeaderUser: {"kleist"}, HeaderRoles: {"authproxy.admin"}}, 403, "forbidden", msgUsersForbidden},
		{"Teiltreffer", http.Header{HeaderUser: {"kleist"}, HeaderRoles: {"authproxy.admin,kephalaion.admins"}}, 403, "forbidden", msgUsersForbidden},
		{"zwei Header", http.Header{HeaderUser: {"kleist"}, HeaderRoles: {"authproxy.admin", RoleAdmin}}, 403, "forbidden", msgUsersForbidden},
		{"mit _", http.Header{HeaderUser: {"kleist"}, "X_user_roles": {RoleAdmin}}, 403, "forbidden", msgUsersForbidden},
		{"ohne Anmeldung", http.Header{}, 403, "unauthenticated", msgUnauthenticated},
		{"nur die Rolle", http.Header{HeaderRoles: {RoleAdmin}}, 403, "unauthenticated", msgUnauthenticated},
		{"zwei User mit Rolle", http.Header{HeaderUser: {"kleist", "carol"}, HeaderRoles: {RoleAdmin}}, 403, "unauthenticated", msgUnauthenticated},
		{"admin mit Rolle", http.Header{HeaderUser: {"admin"}, HeaderRoles: {RoleAdmin}}, 403, "invalid_user", msgInvalidUser},
		{"groß mit Rolle", http.Header{HeaderUser: {"Kleist"}, HeaderRoles: {RoleAdmin}}, 403, "invalid_user", msgInvalidUser},
	} {
		resp, body := f.send(t, http.MethodGet, "/gui/api/users", c.header)
		if msg := wantError(t, c.what, resp, body, c.status, c.code); msg != c.msg {
			t.Errorf("%s: Meldung %q, erwartet %q", c.what, msg, c.msg)
		}
		if strings.Contains(body, "carol") || strings.Contains(body, "laptop") || strings.Contains(body, "users") {
			t.Errorf("%s: Body %s", c.what, body)
		}
	}
	if msgUsersForbidden == msgForbidden {
		t.Error("dieselbe Meldung für fremden User und Liste")
	}
}

// Nur GET und HEAD: alles andere 405 mit Allow, bevor die Anmeldung gilt.
func TestUsersMethod(t *testing.T) {
	f := newFixture(t)
	f.srv = reqlog.New(f.log).Middleware("hub", NewUsers(f.st, HeaderViewer))
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch, http.MethodOptions} {
		for _, h := range []http.Header{{}, {HeaderUser: {"kleist"}, HeaderRoles: {RoleAdmin}}} {
			resp, body := f.send(t, m, "/gui/api/users", h)
			if msg := wantError(t, m, resp, body, http.StatusMethodNotAllowed, "invalid"); msg != "nur GET" {
				t.Errorf("%s: Meldung %q", m, msg)
			}
			if resp.Header.Get("Allow") != "GET, HEAD" {
				t.Errorf("%s: Allow %q", m, resp.Header.Get("Allow"))
			}
		}
	}
	resp, body := f.send(t, http.MethodHead, "/gui/api/users", http.Header{HeaderUser: {"kleist"}})
	if resp.StatusCode != http.StatusForbidden || body != "" {
		t.Errorf("HEAD ohne Rolle: HTTP %d, Body %q", resp.StatusCode, body)
	}
}

// Das Log nennt je Anfrage an die Liste den angemeldeten User und ob er
// Admin ist; ohne gültigen User steht admin nicht da. Bei gui/api/user steht
// admin zwischen viewer und user.
func TestUsersLog(t *testing.T) {
	f := newFixture(t)
	f.srv = reqlog.New(f.log).Middleware("hub", NewUsers(f.st, HeaderViewer))
	f.admin(t, http.MethodGet, "/gui/api/users", "kleist")
	f.do(t, http.MethodGet, "/gui/api/users", "kleist")
	f.do(t, http.MethodGet, "/gui/api/users")
	f.admin(t, http.MethodGet, "/gui/api/users", "admin")
	f.serve(HeaderViewer)
	f.admin(t, http.MethodGet, "/gui/api/user?name=carol", "kleist")
	log := f.log.String()
	for _, want := range []string{
		"hub GET /gui/api/users 200 ", " viewer=kleist admin=ja\n",
		"hub GET /gui/api/users 403 ", " viewer=kleist admin=nein\n",
		" viewer=-\n", " viewer=admin\n",
		"hub GET /gui/api/user 200 ", " viewer=kleist admin=ja user=carol\n",
	} {
		if !strings.Contains(log, want) {
			t.Errorf("Log ohne %q:\n%s", want, log)
		}
	}
	for _, secret := range []string{"keph_", ident.HashToken(f.tokens["laptop"]), "laptop"} {
		if strings.Contains(log, secret) {
			t.Errorf("Log mit %q:\n%s", secret, log)
		}
	}
	if n := strings.Count(log, "\n"); n != 5 {
		t.Errorf("%d Logzeilen, erwartet 5:\n%s", n, log)
	}
}

// Ein Fehler des Stores ist 500 internal mit festem Text; die Einzelheit
// steht nur im Log.
func TestUsersStoreError(t *testing.T) {
	f := newFixture(t)
	boom := errors.New("database is locked (hub.db)")
	f.srv = reqlog.New(f.log).Middleware("hub", NewUsers(failing{Store: f.st, all: boom}, HeaderViewer))
	resp, body := f.admin(t, http.MethodGet, "/gui/api/users", "kleist")
	if msg := wantError(t, "Fehler des Stores", resp, body, http.StatusInternalServerError, "internal"); msg != "Fehler am Hub" {
		t.Errorf("Meldung %q", msg)
	}
	if strings.Contains(body, "locked") {
		t.Errorf("die Antwort nennt die Einzelheit: %s", body)
	}
	if log := f.log.String(); !strings.Contains(log, `error="database is locked (hub.db)"`) ||
		!strings.Contains(log, "viewer=kleist admin=ja") {
		t.Errorf("Log:\n%s", log)
	}
}
