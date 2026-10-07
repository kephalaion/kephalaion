package gui

import (
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
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
// sie dich aus der Anmeldung am Proxy kennt. Für einen Admin der
// Weboberfläche hat sie die Liste aller Accounts mit dem Hinweis auf die
// Kommandozeile und den Knopf „Alle Accounts“ zurück zur Liste.
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
		`<section id="list" aria-labelledby="list-title" hidden>`,
		`<h2 id="list-title" tabindex="-1">Alle Accounts</h2>`,
		`<p id="list-count" class="meta count"></p>`,
		`<p id="list-empty" class="message info" hidden></p>`,
		`<div id="users"></div>`,
		`Du siehst die Liste als Admin der Weboberfläche (Rolle <code>kephalaion.admin</code> am Proxy).`,
		`Accounts und Rechte richtest du derzeit per Kommandozeile am Hub ein: <code>kephalaion hub account add|grant|revoke|lock|unlock …</code>. Verwalten kann diese Seite nichts.`,
		`<div id="result-nav" class="actions nav" hidden> <button type="button" id="back" class="quiet">Alle Accounts</button> </div>`,
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
// nichts aus der Adresse und ändert sie nicht, setzt Daten des Hubs nie als
// HTML, fragt nur seine beiden Eingänge per GET ohne Body und folgt keiner
// Umleitung; die Texte für keine Anmeldung, ungültigen User, abgelaufene
// Anmeldung und „nicht erreichbar“ sind verschieden. Die Liste aller
// Accounts holt es nur, wenn der Hub den Angemeldeten als Admin meldet.
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
		`var USERS_API = "gui/api/users";`,
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
		// Die Liste nur für einen Admin, und erst, wenn der Hub ihn so meldet:
		// So sieht ein anderer keine 403 in der Konsole.
		`if (out.kind === "ok" && out.data.admin === true) {`,
		`typeof data.viewer === "string" && data.admin === true && Array.isArray(data.users)`,
		`document.getElementById("result-nav").hidden = data.admin !== true;`,
		"Die Liste aller Accounts zeigt dir der Hub nicht",
		`TEXT.you`, `"(du)"`, `names.join(", ")`, `TEXT.remembered + names.join(", ")`,
		`open.addEventListener("click", function () { openUser(name); });`,
		`back.addEventListener("click", function () { openList(true); });`,
		// Nach dem Wechsel bekommt die Überschrift den Fokus.
		`document.getElementById(view === "list" ? "list-title" : "result-title").focus();`,
	} {
		if !strings.Contains(js, want) {
			t.Errorf("app.js ohne %s", want)
		}
	}
	wantOneFetch(t, js)
}

// wantOneFetch hält fest, wohin die Seite fragt: genau ein fetch, in der
// Hilfsfunktion apiGet, mit deren Parameter als Adresse und festen Angaben —
// GET, keiner Umleitung folgen, nur dieselbe Herkunft, kein Cache, kein Body.
// apiGet wird nur mit den beiden festen, relativen Konstanten gerufen,
// USER_API (ohne Query oder mit ?name= und encodeURIComponent) und USERS_API,
// und die Konstanten dienen zu nichts anderem. So fragt die Seite keine
// beliebige Adresse, folgt keiner Umleitung und schickt nichts außer GET.
func wantOneFetch(t *testing.T, js string) {
	t.Helper()
	// Ohne Kommentarzeilen: Ein Kommentar darf die Namen nennen.
	code := regexp.MustCompile(`(?m)^\s*//.*$`).ReplaceAllString(js, "")

	if n := strings.Count(code, "fetch"); n != 1 || strings.Count(code, "fetch(") != 1 {
		t.Fatalf("app.js: fetch %d-mal, erwartet genau einen Aufruf", n)
	}
	def := strings.Index(code, "function apiGet(path, valid) {")
	at := strings.Index(code, "return fetch(path, {")
	if def < 0 || at < def || strings.Contains(code[def+len("function apiGet(path, valid) {"):at], "path") ||
		regexp.MustCompile(`(?m)^\s*function `).MatchString(code[def+1:at]) {
		t.Fatalf("app.js: der fetch steht nicht als return fetch(path, { direkt in apiGet(path, valid)")
	}
	if m := regexp.MustCompile(`\bpath\s*(=[^=]|\+=)`).FindString(code); m != "" {
		t.Errorf("app.js weist path neu zu: %q", m)
	}
	opts, ok := argsOf(code, at+len("return fetch"))
	if !ok {
		t.Fatal("app.js: Ende des fetch nicht gefunden")
	}
	for _, want := range []string{`path, {`, `method: "GET"`, `redirect: "manual"`, `credentials: "same-origin"`,
		`cache: "no-store"`, `referrerPolicy: "no-referrer"`, `signal: abort.signal`} {
		if !strings.Contains(opts, want) {
			t.Errorf("fetch ohne %s:\n%s", want, opts)
		}
	}
	for _, bad := range []string{"body", "mode:", "keepalive", "integrity", "..."} {
		if strings.Contains(opts, bad) {
			t.Errorf("fetch mit %s", bad)
		}
	}
	for _, key := range []string{"method:", "redirect:", "credentials:", "cache:"} {
		if n := strings.Count(code, key); n != 1 {
			t.Errorf("app.js: %s %d-mal, erwartet nur im fetch", key, n)
		}
	}

	// apiGet: die Definition und genau diese drei Aufrufe, je einmal; nie als
	// Wert weitergereicht.
	want := map[string]int{
		"path, valid":      1,
		"USER_API, isUser": 1,
		`USER_API + "?name=" + encodeURIComponent(name), isUser`: 1,
		"USERS_API, isUsers": 1,
	}
	got := map[string]int{}
	for i := strings.Index(code, "apiGet("); i >= 0; {
		args, ok := argsOf(code, i+len("apiGet"))
		if !ok {
			t.Fatal("app.js: Ende eines Aufrufs von apiGet nicht gefunden")
		}
		got[args]++
		next := strings.Index(code[i+1:], "apiGet(")
		if next < 0 {
			break
		}
		i += 1 + next
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("app.js: apiGet mit %v, erwartet %v", got, want)
	}
	if strings.Count(code, "apiGet") != strings.Count(code, "apiGet(") {
		t.Error("app.js nennt apiGet ohne Aufruf")
	}

	// Die beiden Eingänge: feste, relative Konstanten, je einmal zugewiesen,
	// außer in apiGet nirgends benutzt; kein anderer Pfad unter gui/.
	for name, def := range map[string]string{"USER_API": `var USER_API = "gui/api/user";`, "USERS_API": `var USERS_API = "gui/api/users";`} {
		if strings.Count(code, def) != 1 {
			t.Errorf("app.js ohne %s", def)
		}
		uses := regexp.MustCompile(`\b`+name+`\b`).FindAllStringIndex(code, -1)
		assigns := regexp.MustCompile(`\b`+name+`\s*(=[^=]|\+=)`).FindAllString(code, -1)
		if wantUses := map[string]int{"USER_API": 3, "USERS_API": 2}[name]; len(uses) != wantUses || len(assigns) != 1 {
			t.Errorf("app.js: %s %d-mal genannt, %d-mal zugewiesen", name, len(uses), len(assigns))
		}
	}
	if paths := regexp.MustCompile(`"gui/[^"]*"`).FindAllString(code, -1); !reflect.DeepEqual(paths, []string{`"gui/api/user"`, `"gui/api/users"`}) {
		t.Errorf("app.js: Pfade %q, erwartet nur die beiden Eingänge", paths)
	}
	if n := strings.Count(code, `"?name="`); n != 1 {
		t.Errorf("app.js: ?name= %d-mal", n)
	}
}

// argsOf liefert den Text zwischen der Klammer an code[open] und der
// passenden schließenden, ohne beide; Klammern in Zeichenketten zählen nicht.
func argsOf(code string, open int) (string, bool) {
	if open >= len(code) || code[open] != '(' {
		return "", false
	}
	depth, quote := 0, byte(0)
	for i := open; i < len(code); i++ {
		c := code[i]
		switch {
		case quote != 0:
			if c == '\\' {
				i++
			} else if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '(':
			depth++
		case c == ')':
			depth--
			if depth == 0 {
				return code[open+1 : i], true
			}
		}
	}
	return "", false
}

// Das Stylesheet: hell und dunkel, schmal als Blöcke, die Tabelle scrollt
// nur in ihrem eigenen Kasten, nichts Fremdes. overflow-wrap: anywhere ließ
// die Spalten der Tabelle unter ihre Wörter schrumpfen („Les-en“, Befund
// gui.md) — break-word bricht nur, was sonst überliefe.
func TestStyle(t *testing.T) {
	css := file(t, "style.css")
	for _, want := range []string{"@media (prefers-color-scheme: dark)", "@media (max-width: 48rem)", "overflow-x: auto",
		"overflow-wrap: break-word", "content: attr(data-label)", "[hidden]", "system-ui", ".badge.locked", ".resting",
		".user-group", "button.user-name", ".account-list li", ".actions.nav"} {
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
