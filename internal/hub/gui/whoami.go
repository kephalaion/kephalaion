// Package gui ist die Weboberfläche des Hubs: die Seite, die der Hub-Listener
// einem Browser an seiner Wurzel liefert, ihre Dateien unter /gui/ und ihr
// Eingang POST /gui/api/whoami. Die Seite fragt Account und Account-Token ab
// und zeigt, worauf der Account am Hub Zugriff hat; verwalten kann sie
// nichts.
//
// Der Eingang ist kein Teil des Vertrags (docs/vertrag.md) und trägt keine
// Fassung: Seite und Eingang kommen aus demselben Binary. Er prüft den
// Account allein, ohne Node — whoami des Vertrags verlangt immer einen.
//
// Das Paket gehört zur Hub-Seite und kennt den Node nicht. Wo die Teile am
// Listener liegen, ordnet cmd/kephalaion (newHubHandler).
package gui

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"unicode/utf8"

	"github.com/kephalaion/kephalaion/internal/contract"
	"github.com/kephalaion/kephalaion/internal/hub/store"
	"github.com/kephalaion/kephalaion/internal/ident"
	"github.com/kephalaion/kephalaion/internal/reqlog"
)

// MaxWhoamiBytes ist die Grenze für den Body von POST /gui/api/whoami: ein
// Name und ein Token, mehr steht nicht darin.
const MaxWhoamiBytes = 4 << 10

// Codes der Fehlerantworten, dieselben Wörter wie im Vertrag.
const (
	codeInvalid         = string(contract.CodeInvalid)
	codeUnauthenticated = string(contract.CodeUnauthenticated)
	codeInternal        = "internal"
)

// msgUnauthenticated ist die eine Meldung für unbekannten Account, falsches
// Token und gesperrten Account: Wer fragt, erfährt nicht, woran es lag.
const msgUnauthenticated = "Account oder Token stimmt nicht."

// whoamiRequest ist der Body der Anfrage.
type whoamiRequest struct {
	Account string `json:"account"`
	Token   string `json:"token"`
}

// WhoamiResponse ist die Antwort auf eine gelungene Prüfung: der Account,
// sein User, seine Beschreibung und seine Collections nach Name. read gilt
// für jede aufgeführte Collection und steht nicht eigens da.
type WhoamiResponse struct {
	Account     string             `json:"account"`
	User        string             `json:"user"`
	Description string             `json:"description"`
	Collections []WhoamiCollection `json:"collections"`
}

// WhoamiCollection ist eine Collection des Accounts mit ihrer Beschreibung
// und seinen Rechten darin.
type WhoamiCollection struct {
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Rights      WhoamiRights `json:"rights"`
}

// WhoamiRights sind die Rechte über read hinaus, wie contract.Rights — nur
// sind vendor und dirs (die Verzeichnis-Scopes) hier immer eine Liste, auch
// leer.
type WhoamiRights struct {
	Write     bool     `json:"write"`
	Supersede bool     `json:"supersede"`
	Vendor    []string `json:"vendor"`
	Dirs      []string `json:"dirs"`
}

// errorBody ist der Body einer Fehlerantwort, in der Form des Vertrags.
type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// NewWhoami liefert den Eingang der Seite, POST /gui/api/whoami: Er prüft
// Account und Token gegen st (store.CheckAccount) und nennt User,
// Beschreibung und die Collections des Accounts mit ihren Rechten — aus den
// lebenden SYSTEM:A:-Zeilen, eine zurückgenommene Collection erscheint
// nicht.
//
//   - nur POST, sonst 405 mit Allow: POST;
//   - Content-Type application/json, sonst 415 — ein einfacher Formular-Post
//     einer fremden Seite kommt so nicht durch;
//   - Body höchstens MaxWhoamiBytes, sonst 413;
//   - falsche Form (kein JSON, Feld fehlt oder leer, unbekanntes Feld, Name
//     oder Token nicht nach ident) 400 invalid mit dem Grund;
//   - unbekannter Account, falsches Token, gesperrt: 401 unauthenticated,
//     immer dieselbe Antwort;
//   - ein Fehler des Stores 500 internal, die Einzelheit nur im Log.
//
// Ins Log kommt der Name des Accounts (reqlog.Note, maskiert, wenn er der
// Namensregel nicht folgt), nie das Token und nie sein Hash.
func NewWhoami(st store.Store) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeError(w, r, http.StatusMethodNotAllowed, codeInvalid, "nur POST")
			return
		}
		if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
			writeError(w, r, http.StatusUnsupportedMediaType, codeInvalid, "Content-Type application/json erwartet")
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxWhoamiBytes))
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, r, http.StatusRequestEntityTooLarge, codeInvalid,
				fmt.Sprintf("Anfrage größer als %d Bytes", MaxWhoamiBytes))
			return
		}
		if err != nil {
			writeError(w, r, http.StatusBadRequest, codeInvalid, "Anfrage nicht lesbar")
			return
		}
		req, reason := decodeWhoami(body)
		if reason == "" {
			reqlog.Note(ctx, "account", req.Account)
			reason = checkWhoami(req)
		}
		if reason != "" {
			writeError(w, r, http.StatusBadRequest, codeInvalid, reason)
			return
		}
		acc, ok, err := store.CheckAccount(ctx, st, req.Account, req.Token)
		if err != nil {
			internalError(w, r, err)
			return
		}
		if !ok {
			writeError(w, r, http.StatusUnauthorized, codeUnauthenticated, msgUnauthenticated)
			return
		}
		colls, err := st.Collections(ctx)
		if err != nil {
			internalError(w, r, err)
			return
		}
		writeJSON(w, r, http.StatusOK, whoamiResponse(acc, colls))
	})
}

// decodeWhoami liest den Body als genau ein JSON-Objekt mit den Feldern
// account und token. reason ist leer, wenn das gelingt, sonst der Grund für
// die Antwort — ohne den Inhalt der Anfrage.
func decodeWhoami(body []byte) (req whoamiRequest, reason string) {
	// JSON ist UTF-8; encoding/json ersetzte ungültige Bytes sonst still.
	if !utf8.Valid(body) {
		return whoamiRequest{}, "Anfrage ist kein gültiges JSON (kein UTF-8)"
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return whoamiRequest{}, "Anfrage ist kein gültiges JSON mit genau den Feldern account und token"
	}
	if _, err := dec.Token(); err != io.EOF {
		return whoamiRequest{}, "Anfrage ist kein gültiges JSON: nach dem Objekt folgt noch etwas"
	}
	return req, ""
}

// checkWhoami prüft die Form der Felder, ohne Datenbank. Keine der Meldungen
// nennt das Token.
func checkWhoami(req whoamiRequest) (reason string) {
	if req.Account == "" {
		return "account fehlt"
	}
	if req.Token == "" {
		return "token fehlt"
	}
	if err := ident.CheckName("Account", req.Account); err != nil {
		return err.Error()
	}
	if err := ident.CheckToken(req.Token); err != nil {
		return err.Error()
	}
	return ""
}

// whoamiResponse baut die Antwort: die Collections aus den Rechten des
// Accounts (schon nach Name), die Beschreibungen aus colls.
func whoamiResponse(acc store.Account, colls []store.Collection) WhoamiResponse {
	desc := make(map[string]string, len(colls))
	for _, c := range colls {
		desc[c.Name] = c.Description
	}
	out := WhoamiResponse{Account: acc.Name, User: acc.User, Description: acc.Description,
		Collections: make([]WhoamiCollection, 0, len(acc.Rights))}
	for _, r := range acc.Rights {
		out.Collections = append(out.Collections, WhoamiCollection{
			Name:        r.Collection,
			Description: desc[r.Collection],
			Rights: WhoamiRights{Write: r.Write, Supersede: r.Supersede,
				Vendor: append([]string{}, r.Vendor...), Dirs: append([]string{}, r.Dirs...)},
		})
	}
	return out
}

// internalError antwortet auf einen Fehler des Stores: die Einzelheit nur
// ins Log, an den Aufrufer ein fester Text.
func internalError(w http.ResponseWriter, r *http.Request, err error) {
	reqlog.NoteError(r.Context(), err)
	writeError(w, r, http.StatusInternalServerError, codeInternal, "Fehler am Hub")
}

func writeError(w http.ResponseWriter, r *http.Request, status int, code, msg string) {
	writeJSON(w, r, status, errorBody{Code: code, Message: msg})
}

// writeJSON schreibt v als JSON; nichts davon gehört in einen Cache, denn
// die Antwort hängt am Token im Body der Anfrage.
func writeJSON(w http.ResponseWriter, r *http.Request, status int, v any) {
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	if r.Method == http.MethodHead {
		return
	}
	if err := json.NewEncoder(w).Encode(v); err != nil {
		reqlog.NoteError(r.Context(), fmt.Errorf("Antwort schreiben: %w", err))
	}
}
