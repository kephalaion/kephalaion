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

// Die Seite fragt nichts ab: kein Formular, kein Feld, kein Token. Sie nennt
// den angemeldeten User im Kopf, erklärt die Rechte in Worten und sagt, dass
// sie dich aus der Anmeldung am Proxy kennt.
func TestIndexTexts(t *testing.T) {
	index := strings.Join(strings.Fields(file(t, "index.html")), " ")
	for _, want := range []string{
		`<p id="viewer-line" class="meta viewer" hidden>angemeldet als <strong id="viewer"></strong></p>`,
		`<h2 id="result-title" tabindex="-1">Deine Accounts</h2>`,
		`Die Seite kennt dich aus der Anmeldung am Proxy; sie fragt kein Token ab und zeigt keins.`,
		`<div id="accounts"></div>`,
		`<button type="button" id="reload" hidden>Seite neu laden</button>`,
		`Unter <code>vendor/&lt;name&gt;/</code> zählt allein der Scope`,
		`Direkt in <code>vendor/</code> schreibt niemand.`,
		`<strong>Verzeichnis-Scope</strong> (<code>dir &lt;pfad&gt;/</code>): Unter <code>&lt;pfad&gt;/</code> darf der Account anlegen, ändern, löschen und umbenennen, auch ohne <code>write</code>`,
		`Er nimmt niemandem etwas: <code>write</code> und <code>supersede</code> gelten dort wie überall.`,
		`<code>kephalaion node dir push</code> schreibt nur dorthin`,
		`<strong>Gesperrt</strong>: Ein gesperrter Account darf nichts, auch nicht lesen. Seine Rechte sind gemerkt und ruhen`,
		`Über einen Node siehst du davon nur die Collections, die dieser Node abgleicht (<code>kephalaion node whoami</code>).`,
	} {
		if !strings.Contains(index, want) {
			t.Errorf("index.html ohne %s", want)
		}
	}
	for _, bad := range []string{"<form", "<input", "<textarea", "password", "keph_", "Account-Token", `method="`, "action="} {
		if strings.Contains(index, bad) {
			t.Errorf("index.html enthält %s", bad)
		}
	}
}

// Das Skript hält sich an die Entscheidungen: Es legt nichts ab, liest
// nichts aus der Adresse, setzt Daten des Hubs nie als HTML, fragt den
// Eingang nur per GET ohne Body und folgt keiner Umleitung; die Texte für
// keine Anmeldung, ungültigen User, abgelaufene Anmeldung und „nicht
// erreichbar“ sind verschieden.
func TestScriptFollowsDecisions(t *testing.T) {
	js := file(t, "app.js")
	for _, bad := range []string{"localStorage", "sessionStorage", "indexedDB", "document.cookie", "innerHTML", "outerHTML",
		"insertAdjacentHTML", "document.write", "eval(", "new Function", "location.href", "location.search", "location.hash",
		"history.", "URLSearchParams", "console.", "XMLHttpRequest", "sendBeacon", "http://", "https://",
		`"POST"`, "body:", "keph_", "Authorization", "X-User"} {
		if strings.Contains(js, bad) {
			t.Errorf("app.js enthält %q", bad)
		}
	}
	for _, want := range []string{
		`var USER_API = "gui/api/user";`,
		`method: "GET"`,
		`redirect: "manual"`,
		`credentials: "same-origin"`,
		`cache: "no-store"`,
		`resp.type === "opaqueredirect"`,
		`data.code === "unauthenticated"`,
		`data.code === "invalid_user"`,
		`data.code === "forbidden"`,
		"Keine Anmeldung des Proxys: Der Hub hat zu dieser Anfrage keinen",
		"Der Name deiner Anmeldung am Proxy ist am Hub kein gültiger User",
		"Deine Anmeldung an dieser Seite ist abgelaufen — Seite neu laden und neu anmelden.",
		"Hub nicht erreichbar.",
		"Fehler am Hub.",
		"Du hast an diesem Hub noch keinen Account.",
		"Dieser Account hat noch keine Collection.",
		"gesperrt — die Rechte ruhen",
		`"~/.config/kephalaion/tokens/<hub>/" + String(a.name) + ".token"`,
		// Wen die Seite zeigt, sagt die Antwort; der angemeldete User ist
		// nicht vorausgesetzt.
		`var own = data.user === data.viewer;`,
		`TEXT.otherTitle + data.user`,
		// Die Scopes wie in der Kommandozeile, auch die Verzeichnis-Scopes.
		`Array.isArray(rights.dirs) ? rights.dirs : []`,
		`return "vendor/" + String(name);`,
		`return "dir " + String(dir) + "/";`,
	} {
		if !strings.Contains(js, want) {
			t.Errorf("app.js ohne %s", want)
		}
	}
	// Genau ein fetch, und der geht an den Eingang der Seite.
	if n := strings.Count(js, "fetch("); n != 1 || !strings.Contains(js, "fetch(USER_API, {") {
		t.Errorf("app.js: %d Aufrufe von fetch, erwartet einen an USER_API", n)
	}
}

// Das Stylesheet: hell und dunkel, schmal als Blöcke, die Tabelle scrollt
// nur in ihrem eigenen Kasten, nichts Fremdes. overflow-wrap: anywhere ließ
// die Spalten der Tabelle unter ihre Wörter schrumpfen („Les-en“, Befund
// gui.md) — break-word bricht nur, was sonst überliefe.
func TestStyle(t *testing.T) {
	css := file(t, "style.css")
	for _, want := range []string{"@media (prefers-color-scheme: dark)", "@media (max-width: 48rem)", "overflow-x: auto",
		"overflow-wrap: break-word", "content: attr(data-label)", "[hidden]", "system-ui", ".badge.locked", ".resting"} {
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
	for _, want := range []string{`setAttribute("data-label", label)`, `yesNo("Lesen", true, resting)`, `"Schreiben (write)"`,
		`"Fremdes (supersede)"`, `cell("Scopes")`} {
		if !strings.Contains(js, want) {
			t.Errorf("app.js ohne %s", want)
		}
	}
}
