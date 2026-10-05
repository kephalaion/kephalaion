// Package gui ist die Weboberfläche des Hubs: die Seite, die der Hub-Listener
// einem Browser an seiner Wurzel liefert, ihre Dateien unter /gui/ und ihr
// Eingang GET /gui/api/user. Die Seite zeigt dem über die Anmeldung des
// Proxys angemeldeten User alle seine Accounts mit ihren Rechten. Ein Token
// fragt sie nicht ab: Sie glaubt dem User, den die Anmeldung des Proxys
// nennt (Viewer), und liest nur; verwalten kann sie nichts.
//
// Der Eingang ist kein Teil des Vertrags (docs/vertrag.md) und trägt keine
// Fassung: Seite und Eingang kommen aus demselben Binary. Keine Antwort des
// Eingangs ist eine 401 — fail2ban zählt jede 401 im Log des Proxys —, eine
// fehlende Anmeldung ist 403.
//
// Das Paket gehört zur Hub-Seite und kennt den Node nicht. Wo die Teile am
// Listener liegen und welcher Viewer gilt, ordnet cmd/kephalaion
// (newHubHandler, hubViewer).
package gui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/kephalaion/kephalaion/internal/contract"
	"github.com/kephalaion/kephalaion/internal/hub/store"
	"github.com/kephalaion/kephalaion/internal/reqlog"
)

// HeaderUser ist der Header, in dem die Anmeldung des Proxys den
// angemeldeten User nennt: Caddy entfernt ihn aus der Anfrage des Browsers
// und kopiert ihn per forward_auth aus der Antwort von authproxy.
const HeaderUser = "X-User"

// Viewer entscheidet, wem der Hub den angemeldeten User einer Anfrage glaubt:
// Er liefert den Namen, den die Anmeldung des Proxys nennt, und ok false,
// wenn die Anfrage keinen trägt — dann zeigt der Eingang nichts. Ob der Name
// ein gültiger User ist, prüft der Eingang (store.CheckUser), nicht der
// Viewer.
//
// Das ist die eine Stelle dieser Entscheidung: cmd/kephalaion baut den Wert
// genau einmal und gibt ihn dem Eingang; Tests geben ihren eigenen. Wer sie
// ändert (der geheime Header des Proxys, eine eigene Task), ändert den
// Viewer und die Stelle, an der er entsteht — nicht den Eingang und nicht
// die Seite.
type Viewer func(r *http.Request) (user string, ok bool)

// HeaderViewer ist der Viewer ohne weitere Prüfung: der Wert von genau einem
// X-User. Fehlt der Header, ist er leer oder steht er mehrfach da, ist
// niemand angemeldet (ok false) — mehrere Werte wählt er nicht aus.
//
// Grenze: Der Hub-Listener nimmt nur Anfragen an diesen Rechner an (serve
// lauscht auf Loopback, loopback.Guard), prüft aber nicht, dass X-User vom
// Proxy kommt. Jeder Prozess auf dem Rechner des Hubs kann ihn selbst setzen
// und sieht dann Accounts und Rechte eines beliebigen Users — keine Tokens,
// keine Hashes.
func HeaderViewer(r *http.Request) (string, bool) {
	vals := r.Header.Values(HeaderUser)
	if len(vals) != 1 || vals[0] == "" {
		return "", false
	}
	return vals[0], true
}

// Codes der Fehlerantworten; die Wörter des Vertrags, wo es sie gibt.
const (
	codeInvalid         = string(contract.CodeInvalid)
	codeUnauthenticated = string(contract.CodeUnauthenticated)
	codeForbidden       = string(contract.CodeForbidden)
	codeInvalidUser     = "invalid_user"
	codeInternal        = "internal"
)

// Die festen Meldungen des Eingangs. Keine nennt den Namen aus der Anfrage.
const (
	msgUnauthenticated = "Keine Anmeldung des Proxys: Die Anfrage nennt keinen angemeldeten User."
	msgInvalidUser     = "Dieser Name ist am Hub kein gültiger User."
	msgForbidden       = "Nur die eigenen Accounts: Einen anderen User zeigt der Hub nicht."
)

// UserResponse ist die Antwort des Eingangs: wer angemeldet ist (Viewer),
// wessen Accounts es sind (User) und die Accounts nach Name. Viewer und User
// sind vorerst immer gleich; die Seite verlässt sich nicht darauf.
type UserResponse struct {
	Viewer   string        `json:"viewer"`
	User     string        `json:"user"`
	Accounts []UserAccount `json:"accounts"`
}

// UserAccount ist ein Account des Users: Beschreibung, gesperrt und seine
// Collections nach Name. Ein gesperrter Account trägt seine gemerkten Rechte;
// sie ruhen, solange er gesperrt ist. read gilt für jede aufgeführte
// Collection und steht nicht eigens da.
type UserAccount struct {
	Name        string           `json:"name"`
	Description string           `json:"description"`
	Locked      bool             `json:"locked"`
	Collections []UserCollection `json:"collections"`
}

// UserCollection ist eine Collection eines Accounts mit ihrer Beschreibung
// und seinen Rechten darin.
type UserCollection struct {
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Rights      UserRights `json:"rights"`
}

// UserRights sind die Rechte über read hinaus, wie contract.Rights — nur
// sind vendor und dirs (die Verzeichnis-Scopes) hier immer eine Liste, auch
// leer.
type UserRights struct {
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

// NewUser liefert den Eingang der Seite, GET /gui/api/user: die Accounts
// eines Users mit Beschreibung, Sperre und ihren Collections samt Rechten —
// ohne Account-Token, das der Eingang weder braucht noch kennt.
//
//   - Nur GET und HEAD, sonst 405 mit Allow: GET, HEAD.
//   - Der angemeldete User kommt allein von viewer. Nennt die Anfrage keinen:
//     403 unauthenticated; nennt sie einen, der am Hub kein gültiger User ist
//     (store.CheckUser, etwa admin): 403 invalid_user — beide mit fester
//     Meldung. Nie 401.
//   - Die Query trägt höchstens einen name, sonst 400 invalid (ebenso eine
//     Query, die sich nicht lesen lässt). Ohne name gilt der angemeldete
//     User; ein anderer name 403 forbidden — vorerst sieht jeder nur sich
//     selbst. Ein leeres name= ist nicht „ohne name“: Ein angegebener name
//     gilt wörtlich, und leer ist nicht der eigene, also 403. So zeigt der
//     Eingang nie still den eigenen User, wenn ein Aufrufer einen anderen
//     meinte und der Wert unterwegs verloren ging.
//   - Ein User ohne Account: accounts ist eine leere Liste, 200.
//   - Ein Fehler des Stores 500 internal, die Einzelheit nur im Log.
//
// Ins Log kommen der angemeldete User (viewer) und der angefragte (user),
// über reqlog.Note (maskiert, wenn ein Name der Namensregel nicht folgt) —
// nie ein Token und nie ein Hash.
func NewUser(st store.Store, viewer Viewer) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			writeError(w, r, http.StatusMethodNotAllowed, codeInvalid, "nur GET")
			return
		}
		me, ok := viewer(r)
		reqlog.Note(ctx, "viewer", me)
		if !ok {
			writeError(w, r, http.StatusForbidden, codeUnauthenticated, msgUnauthenticated)
			return
		}
		if store.CheckUser(me) != nil {
			writeError(w, r, http.StatusForbidden, codeInvalidUser, msgInvalidUser)
			return
		}
		query, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, codeInvalid, "Query nicht lesbar")
			return
		}
		user := me
		switch names := query["name"]; len(names) {
		case 0:
		case 1:
			user = names[0]
		default:
			writeError(w, r, http.StatusBadRequest, codeInvalid, "name höchstens einmal")
			return
		}
		reqlog.Note(ctx, "user", user)
		if user != me {
			writeError(w, r, http.StatusForbidden, codeForbidden, msgForbidden)
			return
		}
		accounts, err := st.AccountsOfUser(ctx, user)
		if err != nil {
			internalError(w, r, err)
			return
		}
		colls, err := st.Collections(ctx)
		if err != nil {
			internalError(w, r, err)
			return
		}
		writeJSON(w, r, http.StatusOK, userResponse(me, user, accounts, colls))
	})
}

// userResponse baut die Antwort: die Accounts (vom Store nach Name) mit ihren
// Rechten (nach Collection), die Beschreibungen der Collections aus colls.
// Vom Account kommen nur Name, Beschreibung, Sperre und Rechte — nie sein
// Hash.
func userResponse(viewer, user string, accounts []store.Account, colls []store.Collection) UserResponse {
	desc := make(map[string]string, len(colls))
	for _, c := range colls {
		desc[c.Name] = c.Description
	}
	out := UserResponse{Viewer: viewer, User: user, Accounts: make([]UserAccount, 0, len(accounts))}
	for _, acc := range accounts {
		a := UserAccount{Name: acc.Name, Description: acc.Description, Locked: acc.Locked,
			Collections: make([]UserCollection, 0, len(acc.Rights))}
		for _, r := range acc.Rights {
			a.Collections = append(a.Collections, UserCollection{
				Name:        r.Collection,
				Description: desc[r.Collection],
				Rights: UserRights{Write: r.Write, Supersede: r.Supersede,
					Vendor: append([]string{}, r.Vendor...), Dirs: append([]string{}, r.Dirs...)},
			})
		}
		out.Accounts = append(out.Accounts, a)
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
// die Antwort hängt am angemeldeten User.
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
