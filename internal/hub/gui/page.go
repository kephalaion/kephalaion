package gui

import (
	"bytes"
	"embed"
	"html/template"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
)

// static sind die Dateien der Seite, eingebettet ins Binary: keine fremden
// Quellen, kein CDN, keine Webfonts.
//
//go:embed static/index.html static/app.js static/style.css static/icon.svg
var static embed.FS

// Die Dateien der Seite unter /gui/, wie index.html sie lädt (relativ:
// gui/app.js, gui/style.css, gui/icon.svg).
const (
	FileScript = "app.js"
	FileStyle  = "style.css"
	// FileIcon ist das Symbol der Seite. Ohne es fragt ein Browser
	// /favicon.ico an der Wurzel des Hosts — hinter einem Proxy außerhalb
	// seines Präfixes, lokal ein 404 des Hub-Listeners, das als Fehler in
	// der Konsole steht.
	FileIcon = "icon.svg"
)

// Files sind die Dateien der Seite, in fester Reihenfolge.
var Files = []string{FileScript, FileStyle, FileIcon}

// contentTypes nennt den Content-Type je Datei der Seite.
var contentTypes = map[string]string{
	FileScript: "text/javascript; charset=utf-8",
	FileStyle:  "text/css; charset=utf-8",
	FileIcon:   "image/svg+xml",
}

// ContentSecurityPolicy gilt für die Seite und ihre Dateien: nur Eigenes,
// kein Inline-Script, kein Inline-Style, kein style-Attribut; kein Formular
// schickt irgendwohin (das Absenden läuft per fetch), kein Rahmen bettet die
// Seite ein.
const ContentSecurityPolicy = "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; " +
	"img-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// AcceptsHTML sagt, ob die Anfrage eine Seite will: Accept nennt text/html
// (mit q über 0). So fragt ein Browser; curl schickt */* und bekommt an der
// Wurzel weiter die Begrüßung.
func AcceptsHTML(r *http.Request) bool {
	for _, v := range r.Header.Values("Accept") {
		for _, part := range strings.Split(v, ",") {
			mt, params, err := mime.ParseMediaType(part)
			if err != nil || mt != "text/html" {
				continue
			}
			if q, ok := params["q"]; ok {
				if f, err := strconv.ParseFloat(q, 64); err == nil && f <= 0 {
					continue
				}
			}
			return true
		}
	}
	return false
}

// NewPage liefert die Seite (index.html) mit der Version im Kopf. Sie lädt
// ihre Dateien und fragt ihren Eingang nur über relative Pfade; wo sie
// liegt, weiß sie nicht.
func NewPage(version string) http.Handler {
	tmpl := template.Must(template.ParseFS(static, "static/index.html"))
	var page bytes.Buffer
	if err := tmpl.Execute(&page, struct{ Version string }{version}); err != nil {
		panic("gui: index.html: " + err.Error())
	}
	return serveBytes("text/html; charset=utf-8", page.Bytes())
}

// NewFile liefert eine Datei der Seite, eine aus Files.
func NewFile(name string) http.Handler {
	ctype, ok := contentTypes[name]
	if !ok {
		panic("gui: unbekannte Datei " + name)
	}
	data, err := static.ReadFile("static/" + name)
	if err != nil {
		panic("gui: " + err.Error())
	}
	return serveBytes(ctype, data)
}

// serveBytes antwortet auf GET und HEAD mit data und den Headern der Seite,
// auf alles andere mit 405. Nichts davon gehört in einen Cache: Die Seite
// liegt hinter einem Proxy hinter dessen Anmeldung, und Seite, Skript und
// Eingang kommen immer aus demselben Binary.
func serveBytes(ctype string, data []byte) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Cache-Control", "no-store")
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			h.Set("Content-Type", "text/plain; charset=utf-8")
			h.Set("Allow", "GET, HEAD")
			w.WriteHeader(http.StatusMethodNotAllowed)
			_, _ = io.WriteString(w, "nur GET\n")
			return
		}
		h.Set("Content-Type", ctype)
		h.Set("Content-Security-Policy", ContentSecurityPolicy)
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Length", strconv.Itoa(len(data)))
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			_, _ = w.Write(data)
		}
	})
}
