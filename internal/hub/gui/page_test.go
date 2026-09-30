package gui

import (
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// file liest eine eingebettete Datei der Seite.
func file(t *testing.T, name string) string {
	t.Helper()
	data, err := static.ReadFile("static/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// AcceptsHTML: nur text/html zählt, ohne Groß- und Kleinschreibung und mit
// q über 0 — nicht */* (curl) und nicht text/*.
func TestAcceptsHTML(t *testing.T) {
	for _, c := range []struct {
		accept []string
		want   bool
	}{
		{nil, false},
		{[]string{""}, false},
		{[]string{"*/*"}, false},
		{[]string{"text/*"}, false},
		{[]string{"text/plain"}, false},
		{[]string{"application/json"}, false},
		{[]string{"application/xhtml+xml"}, false},
		{[]string{"text/html"}, true},
		{[]string{"Text/HTML"}, true},
		{[]string{"text/html; charset=utf-8"}, true},
		{[]string{"text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"}, true},
		{[]string{"application/json, text/html;q=0.1"}, true},
		{[]string{"text/html;q=0"}, false},
		{[]string{"text/html;q=0.0, */*"}, false},
		{[]string{"*/*", "text/html"}, true},
		{[]string{"text/htmlx"}, false},
		{[]string{";;;,text/html"}, true},
	} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		for _, v := range c.accept {
			r.Header.Add("Accept", v)
		}
		if got := AcceptsHTML(r); got != c.want {
			t.Errorf("Accept %q: %v, erwartet %v", c.accept, got, c.want)
		}
	}
}

// Die Seite trägt die Version als Text, nie als HTML; GET und HEAD, sonst
// 405; die Header der Weboberfläche.
func TestPage(t *testing.T) {
	page := NewPage(`v1.2.3<script>alert("x")</script>`)
	rec := httptest.NewRecorder()
	page.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	resp := rec.Result()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatalf("HTTP %d, Content-Type %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if !strings.Contains(string(body), `v1.2.3&lt;script&gt;alert(&#34;x&#34;)&lt;/script&gt;`) || strings.Contains(string(body), "<script>alert") {
		t.Errorf("Version nicht als Text:\n%s", body)
	}
	for k, want := range map[string]string{
		"Content-Security-Policy": ContentSecurityPolicy,
		"X-Content-Type-Options":  "nosniff",
		"Referrer-Policy":         "no-referrer",
		"Cache-Control":           "no-store",
	} {
		if got := resp.Header.Get(k); got != want {
			t.Errorf("%s %q, erwartet %q", k, got, want)
		}
	}
	handlers := []http.Handler{page}
	for _, name := range Files {
		handlers = append(handlers, NewFile(name))
	}
	for _, h := range handlers {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodHead, "/", nil))
		if rec.Code != 200 || rec.Body.Len() != 0 || rec.Header().Get("Content-Length") == "0" {
			t.Errorf("HEAD: HTTP %d, Body %d Bytes, Content-Length %q", rec.Code, rec.Body.Len(), rec.Header().Get("Content-Length"))
		}
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader("x")))
		if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "GET, HEAD" ||
			rec.Header().Get("Cache-Control") != "no-store" || strings.TrimSpace(rec.Body.String()) != "nur GET" {
			t.Errorf("POST: HTTP %d, Allow %q, Body %q", rec.Code, rec.Header().Get("Allow"), rec.Body.String())
		}
	}
}

// Was die Content-Security-Policy verbietet, steht gar nicht erst in der
// Seite: kein Inline-Script, kein Inline-Style, kein style-Attribut, kein
// Handler als Attribut; Skript und Stylesheet kommen relativ aus gui/.
func TestIndexFollowsPolicy(t *testing.T) {
	index := file(t, "index.html")
	for _, bad := range []string{"<style", " style=", "javascript:", "<iframe", "<base", "<img", "http://", "https://"} {
		if strings.Contains(index, bad) {
			t.Errorf("index.html enthält %q", bad)
		}
	}
	if m := regexp.MustCompile(`(?i)\son[a-z]+\s*=`).FindString(index); m != "" {
		t.Errorf("index.html mit Handler als Attribut: %q", m)
	}
	scripts := regexp.MustCompile(`(?is)<script[^>]*>`).FindAllString(index, -1)
	if len(scripts) != 1 || scripts[0] != `<script src="gui/app.js" defer>` {
		t.Errorf("index.html: Skripte %q, erwartet nur gui/app.js", scripts)
	}
	for _, want := range []string{`<link rel="stylesheet" href="gui/style.css">`,
		`<link rel="icon" type="image/svg+xml" href="gui/icon.svg">`} {
		if !strings.Contains(index, want) {
			t.Errorf("index.html ohne %s", want)
		}
	}
	// Das Symbol ist ein Bild aus dem Binary: ohne Skript, ohne Inline-Style
	// und ohne Verweis nach außen (der Namensraum ist keine Quelle).
	icon := strings.Replace(file(t, "icon.svg"), `xmlns="http://www.w3.org/2000/svg"`, "", 1)
	for _, bad := range []string{"<script", "style=", "<style", "href", "http://", "https://", "<image", "<foreignObject"} {
		if strings.Contains(icon, bad) {
			t.Errorf("icon.svg enthält %q", bad)
		}
	}
}

// Die Felder der Abfrage: eigene Namen (kein username, kein password), das
// Token verdeckt, nichts davon zum Vervollständigen angeboten. Der Knopf
// ist gesperrt, bis das Skript läuft, und das Formular schickt nie per GET.
func TestIndexFields(t *testing.T) {
	index := strings.Join(strings.Fields(file(t, "index.html")), " ")
	for _, want := range []string{
		`<label for="keph-account">Kephalaion-Account</label>`,
		`<input id="keph-account" name="keph-account" type="text" autocomplete="off"`,
		`<label for="keph-account-token">Account-Token (keph_…)</label>`,
		`<input id="keph-account-token" name="keph-account-token" type="password" autocomplete="off"`,
		`spellcheck="false"`,
		`<form id="ask-form" method="post" autocomplete="off" novalidate>`,
		`<button type="submit" id="ask-submit" disabled>`,
		`>anzeigen</button>`,
		`>Anderes Token prüfen</button>`,
		`Dieser Account hat noch keine Collection.`,
		`Über einen Node siehst du davon nur die Collections, die dieser Node abgleicht (<code>kephalaion node whoami</code>).`,
		`Unter <code>vendor/&lt;name&gt;/</code> zählt allein der Scope`,
		`Direkt in <code>vendor/</code> schreibt niemand.`,
	} {
		if !strings.Contains(index, want) {
			t.Errorf("index.html ohne %s", want)
		}
	}
	for _, bad := range []string{`name="username"`, `name="password"`, `id="username"`, `id="password"`, `method="get"`, `action=`} {
		if strings.Contains(index, bad) {
			t.Errorf("index.html enthält %s", bad)
		}
	}
}

// Das Skript hält sich an die Entscheidungen: Das Token wird nirgends
// abgelegt und steht in keiner Adresse, Daten des Hubs werden nie als HTML
// gesetzt, abgeschickt wird nur die Form eines Tokens, einer Umleitung folgt
// es nicht, und die Texte für 401, abgelaufene Anmeldung und „nicht
// erreichbar“ sind verschieden.
func TestScriptFollowsDecisions(t *testing.T) {
	js := file(t, "app.js")
	for _, bad := range []string{"localStorage", "sessionStorage", "indexedDB", "document.cookie", "innerHTML", "outerHTML",
		"insertAdjacentHTML", "document.write", "eval(", "new Function", "location.href", "location.search", "location.hash",
		"history.", "URLSearchParams", "console.", "XMLHttpRequest", "sendBeacon", "http://", "https://"} {
		if strings.Contains(js, bad) {
			t.Errorf("app.js enthält %q", bad)
		}
	}
	for _, want := range []string{
		`/^keph_[A-Za-z0-9_-]{43}$/`,
		`var WHOAMI = "gui/api/whoami";`,
		`method: "POST"`,
		`"Content-Type": "application/json"`,
		`redirect: "manual"`,
		`ev.preventDefault()`,
		"Das ist kein Kephalaion-Token (die beginnen mit keph_). Gemeint ist nicht das",
		"Account oder Token stimmt nicht. Wiederholte Fehlversuche können deinen",
		"Deine Anmeldung an dieser Seite ist abgelaufen — Seite neu laden und neu anmelden.",
		"Hub nicht erreichbar.",
		"Fehler am Hub.",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("app.js ohne %s", want)
		}
	}
	// Genau ein fetch, und der geht an den Eingang der Seite.
	if n := strings.Count(js, "fetch("); n != 1 || !strings.Contains(js, "fetch(WHOAMI, {") {
		t.Errorf("app.js: %d Aufrufe von fetch, erwartet einen an WHOAMI", n)
	}
	// Der Hinweis auf die Sperre steht nur bei der 401 des Hubs, nicht bei
	// der abgelaufenen Anmeldung.
	if strings.Count(js, "sperren") != 1 {
		t.Errorf("app.js nennt die Sperre %d-mal, erwartet einmal (nur bei 401 des Hubs)", strings.Count(js, "sperren"))
	}
}

// Das Stylesheet: hell und dunkel, schmal als Blöcke, die Tabelle scrollt
// nur in ihrem eigenen Kasten, nichts Fremdes. overflow-wrap: anywhere ließ
// die Spalten der Tabelle unter ihre Wörter schrumpfen („Les-en“, Befund
// gui.md) — break-word bricht nur, was sonst überliefe.
func TestStyle(t *testing.T) {
	css := file(t, "style.css")
	for _, want := range []string{"@media (prefers-color-scheme: dark)", "@media (max-width: 48rem)", "overflow-x: auto",
		"overflow-wrap: break-word", "content: attr(data-label)", "[hidden]", "system-ui"} {
		if !strings.Contains(css, want) {
			t.Errorf("style.css ohne %q", want)
		}
	}
	for _, bad := range []string{"overflow-wrap: anywhere;", "@import", "url(", "@font-face", "http://", "https://"} {
		if strings.Contains(css, bad) {
			t.Errorf("style.css enthält %q", bad)
		}
	}
	// Die Spaltennamen für die schmale Ansicht setzt app.js als data-label.
	js := file(t, "app.js")
	for _, want := range []string{`setAttribute("data-label", label)`, `yesNo("Lesen", true)`, `"Schreiben (write)"`, `"Fremdes (supersede)"`, `cell("Scopes")`} {
		if !strings.Contains(js, want) {
			t.Errorf("app.js ohne %s", want)
		}
	}
}
