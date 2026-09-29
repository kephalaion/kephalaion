// Package reqlog schreibt je HTTP-Anfrage eine Zeile ins Log: Zeit, Rolle,
// Methode, Pfad, Status, Dauer, hinter einem Proxy die Adresse des Aufrufers
// (via, aus X-Forwarded-For) und die Namen, die der Handler dazu vermerkt
// (Node, Account). Namen kommen nur ins Log, wenn sie der Namensregel folgen,
// sonst maskiert (ident.LogName); ein Token steht nie darin — Header und
// Bodys schreibt reqlog sonst nicht.
//
// Das Paket ist neutral und kennt weder Hub noch Node.
package reqlog

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/kephalaion/kephalaion/internal/ident"
)

type ctxKey struct{}

// entry sammelt, was ein Handler zu seiner Anfrage vermerkt.
type entry struct {
	mu    sync.Mutex
	notes []string
}

func (e *entry) add(s string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.notes = append(e.notes, s)
}

// Note vermerkt einen Namen zur laufenden Anfrage, etwa Note(ctx, "node",
// name). Der Name wird maskiert, wenn er nicht der Namensregel folgt. Ohne
// Middleware tut Note nichts.
func Note(ctx context.Context, label, name string) {
	if e, ok := ctx.Value(ctxKey{}).(*entry); ok {
		e.add(label + "=" + ident.LogName(name))
	}
}

// NoteError vermerkt einen Fehler, der keine Antwort an den Aufrufer ist
// (etwa ein Fehler der Datenbank), in Anführungszeichen. Er darf kein
// Geheimnis enthalten; Fehler der Stores nennen nie ein Token.
func NoteError(ctx context.Context, err error) {
	if e, ok := ctx.Value(ctxKey{}).(*entry); ok && err != nil {
		e.add(fmt.Sprintf("error=%q", err.Error()))
	}
}

// Logger schreibt die Zeilen; mehrere Listener teilen sich einen.
type Logger struct {
	mu  sync.Mutex
	w   io.Writer
	now func() time.Time
}

// New liefert einen Logger, der nach w schreibt.
func New(w io.Writer) *Logger { return &Logger{w: w, now: time.Now} }

// Printf schreibt eine freie Zeile, mit Zeit davor.
func (l *Logger) Printf(format string, a ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	fmt.Fprintf(l.w, "%s %s\n", l.now().Format(time.RFC3339), fmt.Sprintf(format, a...))
}

// Middleware schreibt nach next eine Zeile je Anfrage; role steht vorn
// (hub, node). Trägt die Anfrage X-Forwarded-For, steht die erste Adresse
// daraus als via dabei: Hinter einem Reverse-Proxy ist der Aufrufer sonst
// immer Loopback. Der Dienst nimmt nur Verbindungen von Loopback an; ein
// gefälschter Header eines lokalen Prozesses ist nur eine falsche Logzeile.
func (l *Logger) Middleware(role string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := l.now()
		e := &entry{}
		rec := &recorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r.WithContext(context.WithValue(r.Context(), ctxKey{}, e)))
		e.mu.Lock()
		notes := strings.Join(e.notes, " ")
		e.mu.Unlock()
		if notes != "" {
			notes = " " + notes
		}
		l.Printf("%s %s %s %d %s%s%s", role, r.Method, r.URL.EscapedPath(), rec.status,
			l.now().Sub(start).Round(time.Microsecond), via(r), notes)
	})
}

// via liefert " via=<adresse>" aus X-Forwarded-For (die erste Adresse),
// leer ohne den Header; was keine IP-Adresse ist, wird maskiert.
func via(r *http.Request) string {
	xff := r.Header.Get("X-Forwarded-For")
	if xff == "" {
		return ""
	}
	first, _, _ := strings.Cut(xff, ",")
	if ip := net.ParseIP(strings.TrimSpace(first)); ip != nil {
		return " via=" + ip.String()
	}
	return " via=(ungültig)"
}

// recorder merkt sich den Status und reicht Flush weiter, damit gestreamte
// Antworten (MCP) gehen.
type recorder struct {
	http.ResponseWriter
	status  int
	written bool
}

func (r *recorder) WriteHeader(code int) {
	if !r.written {
		r.status, r.written = code, true
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *recorder) Write(b []byte) (int, error) {
	r.written = true
	return r.ResponseWriter.Write(b)
}

func (r *recorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap gibt http.ResponseController den eigentlichen Writer.
func (r *recorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }
