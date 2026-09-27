package httpapi

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/kephalaion/kephalaion/internal/contract"
	"github.com/kephalaion/kephalaion/internal/reqlog"
)

// NewHandler liefert den Handler, der hub über HTTP bedient. Er prüft nichts,
// was hub selbst prüft (Anmeldung, Rechte): Er zerlegt Pfad, Header und Body
// und übersetzt Antwort und Fehler.
func NewHandler(hub contract.Hub) http.Handler {
	return &handler{hub: hub}
}

type handler struct {
	hub contract.Hub
}

// errorBody ist der Body einer Fehlerantwort.
type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	version, op, ok := parsePath(r.URL.Path)
	if !ok {
		writeError(w, r, http.StatusNotFound, string(contract.CodeInvalid), "unbekannter Pfad; erwartet POST /v1/<vorgang>")
		return
	}
	// Die Fassung zuerst, noch vor der Anmeldung.
	if version != contract.Version {
		writeError(w, r, Status(contract.CodeUnsupportedVersion), string(contract.CodeUnsupportedVersion),
			fmt.Sprintf("Fassung %d nicht unterstützt, der Hub spricht Fassung %d", version, contract.Version))
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeError(w, r, http.StatusMethodNotAllowed, string(contract.CodeInvalid), "nur POST")
		return
	}
	auth := contract.NodeAuth{Node: r.Header.Get(HeaderNode), Token: bearer(r)}
	reqlog.Note(ctx, "node", auth.Node)
	// Den Vorgang vor dem Body: Von ihm hängt die Grenze ab.
	limit, known := bodyLimit(op)
	if !known {
		writeError(w, r, http.StatusNotFound, string(contract.CodeInvalid), fmt.Sprintf("unbekannter Vorgang %q", op))
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeError(w, r, http.StatusRequestEntityTooLarge, string(contract.CodeInvalid),
			fmt.Sprintf("Anfrage größer als %d Bytes", limit))
		return
	}
	if err != nil {
		writeError(w, r, http.StatusBadRequest, string(contract.CodeInvalid), "Anfrage nicht lesbar")
		return
	}

	var resp any
	switch op {
	case OpWhoami:
		var req contract.WhoamiRequest
		if !decode(w, r, body, &req) {
			return
		}
		req.Version, req.Auth = version, auth
		if req.Account != nil {
			reqlog.Note(ctx, "account", req.Account.Account)
		}
		resp, err = h.hub.Whoami(ctx, req)
	case OpRotate:
		var req contract.RotateRequest
		if !decode(w, r, body, &req) {
			return
		}
		req.Version, req.Auth = version, auth
		reqlog.Note(ctx, "account", req.Account)
		resp, err = h.hub.Rotate(ctx, req)
	case OpSync:
		var req contract.SyncRequest
		if !decode(w, r, body, &req) {
			return
		}
		req.Version, req.Auth = version, auth
		resp, err = h.hub.Sync(ctx, req)
	case OpCreate:
		// content ist Pflicht: Ein fehlendes Feld legte sonst still ein
		// leeres Dokument an.
		var in struct {
			contract.CreateRequest
			Content *string `json:"content"`
		}
		if !decode(w, r, body, &in) || !requireContent(w, r, in.Content) {
			return
		}
		req := in.CreateRequest
		req.Version, req.Auth, req.Content = version, auth, *in.Content
		reqlog.Note(ctx, "account", req.Account.Account)
		resp, err = h.hub.Create(ctx, req)
	case OpWrite:
		// content ist Pflicht: Ein fehlendes Feld leerte sonst still das
		// Dokument.
		var in struct {
			contract.WriteRequest
			Content *string `json:"content"`
		}
		if !decode(w, r, body, &in) || !requireContent(w, r, in.Content) {
			return
		}
		req := in.WriteRequest
		req.Version, req.Auth, req.Content = version, auth, *in.Content
		reqlog.Note(ctx, "account", req.Account.Account)
		resp, err = h.hub.Write(ctx, req)
	case OpDelete:
		var req contract.DeleteRequest
		if !decode(w, r, body, &req) {
			return
		}
		req.Version, req.Auth = version, auth
		reqlog.Note(ctx, "account", req.Account.Account)
		resp, err = h.hub.Delete(ctx, req)
	default:
		// bodyLimit kennt den Vorgang, der Handler nicht: ein Fehler hier.
		writeError(w, r, http.StatusNotFound, string(contract.CodeInvalid), fmt.Sprintf("unbekannter Vorgang %q", op))
		return
	}
	var cerr *contract.Error
	switch {
	case errors.As(err, &cerr):
		writeError(w, r, Status(cerr.Code), string(cerr.Code), cerr.Message)
	case errors.Is(err, context.Canceled):
		// Der Node hat aufgegeben; niemand liest die Antwort.
		writeError(w, r, http.StatusServiceUnavailable, CodeInternal, "abgebrochen")
	case err != nil:
		reqlog.NoteError(ctx, err)
		writeError(w, r, http.StatusInternalServerError, CodeInternal, "interner Fehler des Hubs")
	default:
		writeJSON(w, r, http.StatusOK, resp)
	}
}

// bearer liest das Token aus Authorization: Bearer <token>.
func bearer(r *http.Request) string {
	v := r.Header.Get("Authorization")
	if len(v) > 7 && strings.EqualFold(v[:7], "bearer ") {
		return strings.TrimSpace(v[7:])
	}
	return ""
}

// decode liest den Body als JSON; ein leerer Body ist {}. JSON ist UTF-8:
// Ein Body mit ungültigem UTF-8 ist kein gültiges JSON — encoding/json
// ersetzte die Bytes sonst still durch U+FFFD. Bei einem Fehler ist die
// Antwort geschrieben und ok false.
func decode(w http.ResponseWriter, r *http.Request, body []byte, v any) bool {
	if len(bytes.TrimSpace(body)) == 0 {
		return true
	}
	if !utf8.Valid(body) {
		writeError(w, r, http.StatusBadRequest, string(contract.CodeInvalid), "Anfrage ist kein gültiges JSON (kein UTF-8)")
		return false
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	if err := dec.Decode(v); err != nil || dec.More() {
		writeError(w, r, http.StatusBadRequest, string(contract.CodeInvalid), "Anfrage ist kein gültiges JSON")
		return false
	}
	return true
}

// requireContent prüft, dass die Anfrage content trägt (auch leer, nicht
// null). Sonst ist die Antwort geschrieben und ok false.
func requireContent(w http.ResponseWriter, r *http.Request, content *string) bool {
	if content == nil {
		writeError(w, r, http.StatusBadRequest, string(contract.CodeInvalid), "ungültige Anfrage: content fehlt")
		return false
	}
	return true
}

func writeError(w http.ResponseWriter, r *http.Request, status int, code, msg string) {
	writeJSON(w, r, status, errorBody{Code: code, Message: msg})
}

// writeJSON schreibt v als JSON, gzip-komprimiert, wenn der Aufrufer es
// erbittet (Accept-Encoding: gzip).
func writeJSON(w http.ResponseWriter, r *http.Request, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Add("Vary", "Accept-Encoding")
	var out io.Writer = w
	var gz *gzip.Writer
	if acceptsGzip(r) {
		w.Header().Set("Content-Encoding", "gzip")
		gz = gzip.NewWriter(w)
		out = gz
	}
	w.WriteHeader(status)
	if err := json.NewEncoder(out).Encode(v); err != nil {
		reqlog.NoteError(r.Context(), fmt.Errorf("Antwort schreiben: %w", err))
	}
	if gz != nil {
		_ = gz.Close()
	}
}

func acceptsGzip(r *http.Request) bool {
	for _, part := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		name, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		if strings.EqualFold(strings.TrimSpace(name), "gzip") && strings.ReplaceAll(params, " ", "") != "q=0" {
			return true
		}
	}
	return false
}
