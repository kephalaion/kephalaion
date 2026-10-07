package gui

import (
	"net/http"
	"sort"

	"github.com/kephalaion/kephalaion/internal/hub/store"
)

// UsersResponse ist die Antwort des Eingangs für die Liste: wer angemeldet
// ist (Viewer), dass er Admin der Weboberfläche ist (Admin, für eine Antwort
// mit 200 immer true) und alle User mit Accounts nach Name.
type UsersResponse struct {
	Viewer string      `json:"viewer"`
	Admin  bool        `json:"admin"`
	Users  []UsersUser `json:"users"`
}

// UsersUser ist ein User der Liste mit seinen Accounts nach Name. Einen User
// gibt es am Hub nur über seine Accounts: In der Liste steht nur, wer
// mindestens einen hat.
type UsersUser struct {
	Name     string         `json:"name"`
	Accounts []UsersAccount `json:"accounts"`
}

// UsersAccount ist ein Account der Liste: Name, gesperrt und die Namen
// seiner Collections nach Name — bei einem gesperrten Account die gemerkten,
// wie bei UserAccount. Rechte und Beschreibungen nennt erst GET
// /gui/api/user?name=<user>.
type UsersAccount struct {
	Name        string   `json:"name"`
	Locked      bool     `json:"locked"`
	Collections []string `json:"collections"`
}

// NewUsers liefert den Eingang der Seite für die Liste aller Accounts, GET
// /gui/api/users, nur für einen Admin der Weboberfläche (gui admin): alle
// Accounts des Hubs (store.Accounts), nach User gruppiert, User und Accounts
// je nach Name — ohne Hash und ohne Token.
//
//   - Nur GET und HEAD, sonst 405 mit Allow: GET, HEAD.
//   - Der angemeldete User und seine Rolle kommen allein von viewer, wie bei
//     NewUser: ohne User 403 unauthenticated, mit ungültigem 403
//     invalid_user. Ohne die Rolle 403 forbidden mit fester Meldung. Nie 401.
//   - Eine Query zählt nicht.
//   - Ein Fehler des Stores 500 internal, die Einzelheit nur im Log.
//
// Ins Log kommen der angemeldete User (viewer) und ob er Admin ist (admin).
func NewUsers(st store.Store, viewer Viewer) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !readMethod(w, r) {
			return
		}
		id, ok := identify(w, r, viewer)
		if !ok {
			return
		}
		if !id.Admin {
			writeError(w, r, http.StatusForbidden, codeForbidden, msgUsersForbidden)
			return
		}
		accounts, err := st.Accounts(r.Context())
		if err != nil {
			internalError(w, r, err)
			return
		}
		writeJSON(w, r, http.StatusOK, usersResponse(id, accounts))
	})
}

// usersResponse baut die Liste: je User seine Accounts, beide nach Name.
// Vom Account kommen nur Name, Sperre und die Namen seiner Collections — nie
// sein Hash.
func usersResponse(viewer Identity, accounts []store.Account) UsersResponse {
	byUser := map[string][]UsersAccount{}
	for _, acc := range accounts {
		a := UsersAccount{Name: acc.Name, Locked: acc.Locked, Collections: make([]string, 0, len(acc.Rights))}
		for _, r := range acc.Rights {
			a.Collections = append(a.Collections, r.Collection)
		}
		sort.Strings(a.Collections)
		byUser[acc.User] = append(byUser[acc.User], a)
	}
	out := UsersResponse{Viewer: viewer.User, Admin: viewer.Admin, Users: make([]UsersUser, 0, len(byUser))}
	for name, accs := range byUser {
		sort.Slice(accs, func(i, j int) bool { return accs[i].Name < accs[j].Name })
		out.Users = append(out.Users, UsersUser{Name: name, Accounts: accs})
	}
	sort.Slice(out.Users, func(i, j int) bool { return out.Users[i].Name < out.Users[j].Name })
	return out
}
