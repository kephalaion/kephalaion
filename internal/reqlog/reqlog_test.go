package reqlog

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMiddleware(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf)
	l.now = func() time.Time { return time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC) }
	h := l.Middleware("node", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		Note(r.Context(), "account", "alice")
		Note(r.Context(), "account", "keph_geheim\nzeile")
		NoteError(r.Context(), errors.New("kaputt"))
		w.WriteHeader(http.StatusForbidden)
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/mcp?x=%0A", nil))
	line := buf.String()
	want := "2026-09-26T12:00:00Z node POST /mcp 403 0s account=alice account=(ungültig) error=\"kaputt\"\n"
	if line != want {
		t.Errorf("Zeile\n%q\nerwartet\n%q", line, want)
	}
	if strings.Contains(line, "geheim") {
		t.Error("Token im Log")
	}
}

// Hinter einem Proxy nennt die Zeile die erste Adresse aus X-Forwarded-For
// als via; ohne den Header fehlt das Feld, und was keine Adresse ist, wird
// maskiert.
func TestMiddlewareVia(t *testing.T) {
	for xff, want := range map[string]string{
		"":                        "node POST /mcp 200 0s\n",
		"9.141.8.157":             "node POST /mcp 200 0s via=9.141.8.157\n",
		"9.141.8.157, 10.0.0.1":   "node POST /mcp 200 0s via=9.141.8.157\n",
		" 2001:db8::1 , 10.0.0.1": "node POST /mcp 200 0s via=2001:db8::1\n",
		"keph_geheim":             "node POST /mcp 200 0s via=(ungültig)\n",
		"<script>, 1.2.3.4":       "node POST /mcp 200 0s via=(ungültig)\n",
	} {
		var buf bytes.Buffer
		l := New(&buf)
		l.now = func() time.Time { return time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC) }
		h := l.Middleware("node", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
		if xff != "" {
			req.Header.Set("X-Forwarded-For", xff)
		}
		h.ServeHTTP(httptest.NewRecorder(), req)
		if got := buf.String(); got != "2026-09-29T12:00:00Z "+want {
			t.Errorf("X-Forwarded-For %q: %q, erwartet %q", xff, got, want)
		}
	}
}
