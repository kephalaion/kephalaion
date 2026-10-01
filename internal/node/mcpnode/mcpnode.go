// Package mcpnode ist der MCP-Eingang des Nodes für Clients: Streamable HTTP
// unter /mcp über das go-sdk, zustandslos. Clients melden sich je Hub mit
// einem Header-Paar an (X-Keph-Account-<alias>, X-Keph-Token-<alias>); der
// Node prüft es bei jeder Anfrage gegen die Account-Zeilen (SYSTEM:A:) der
// Replica dieses Hubs — ohne Cache, die Datenbank ist die einzige Wahrheit.
//
// Werkzeuge: whoami (Version, alle Hubs mit Anmeldung, Node-Name und Stand
// des Abgleichs; bei gültiger Anmeldung Account, User, Collections), zum
// Lesen aus der Replica list, read und changes (access.go: gemeinsamer
// Schritt aus Anmeldung, Adresse und Recht) und zum Schreiben über den Hub
// create, write, delete und rename (write.go, auf demselben Schritt).
// Transport und initialize gehen ohne Anmeldung; list, read und changes
// liefern Inhalte nur aus Collections, in denen der gültig angemeldete
// Account read hat, und nur dort reichen create, write, delete und rename an
// den Hub weiter. Die Anmeldung über alle Hubs prüft Authenticate, einmal je
// Anfrage. Kein Token und kein Hash steht je in einer Antwort, auch nicht
// Adresse, Transport oder hub_id eines Hubs.
//
// Über einen Proxy (die Anfrage trägt X-Forwarded-For) ohne gültige
// Anmeldung an einem Hub ist die Antwort verdeckt (hidden): keine Version,
// kein update, keine Namen von Node und Hubs — der Node antwortet, als hätte
// er keinen Hub-Eintrag. Eine Anfrage mit mindestens einem ungültigen
// Header-Paar vermerkt im Log login=invalid, direkt hinter via; das zählt
// eine Jail auf dem Rechner des Proxys (docs/konzept.md, „Kommunikation“).
//
// Wie jedes Paket unter internal/node kennt es den Hub nicht: Den Weg zu ihm
// und den Anstoß des Abgleichs bekommt es als HubLink.
package mcpnode

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kephalaion/kephalaion/internal/ident"
	"github.com/kephalaion/kephalaion/internal/loopback"
	"github.com/kephalaion/kephalaion/internal/node/store"
	"github.com/kephalaion/kephalaion/internal/reqlog"
	"github.com/kephalaion/kephalaion/internal/upgrade"
)

// Path ist der Pfad des MCP-Eingangs.
const Path = "/mcp"

// Die Präfixe der Header je Hub, klein geschrieben. Der Rest des Namens ist
// der Alias des Hub-Eintrags; siehe HubHeaders.
const (
	accountHeaderPrefix = "x-keph-account-"
	tokenHeaderPrefix   = "x-keph-token-"
)

// Node ist der MCP-Eingang über der Datenbank des Nodes.
type Node struct {
	nodes   store.Store
	version string
	update  func() upgrade.Report
	link    HubLink
}

// NewHandler liefert den Handler des Nodes: /mcp mit Prüfung von Host und
// Origin, alles andere 404. version steht in der Antwort auf initialize —
// außer verdeckt (über einen Proxy ohne gültige Anmeldung, guard); update
// liefert für whoami die letzte Antwort auf die Frage nach einer neuen
// Version — ohne selbst GitHub zu fragen. link ist der Weg zum Hub für
// create, write, delete und rename. Der Body einer Anfrage darf
// MaxRequestBytes groß sein.
func NewHandler(nodes store.Store, version string, update func() upgrade.Report, link HubLink) http.Handler {
	n := &Node{nodes: nodes, version: version, update: update, link: link}
	full, hidden := n.server(version), n.server("")
	h := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		if isHidden(r.Context()) {
			return hidden
		}
		return full
	}, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true, MaxRequestBodyBytes: MaxRequestBytes})
	mux := http.NewServeMux()
	mux.Handle(Path, n.guard(h))
	return mux
}

// server ist der MCP-Server mit allen Werkzeugen; version steht in
// serverInfo, verdeckt leer.
func (n *Node) server(version string) *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: "kephalaion", Version: version}, nil)
	mcp.AddTool(srv, &mcp.Tool{
		Name: "whoami",
		Description: "Zeigt die Version des Nodes, ob es eine neuere gibt und wie das Upgrade geht (update), und je " +
			"Hub die Anmeldung (ok, invalid, missing) und den Stand des Abgleichs; bei ok Account, User und " +
			"Collections mit Adresse und Rechten (read, write, supersede, Scopes vendor/<name> und dir <pfad>/; " +
			"die Verzeichnis-Scopes auch als Liste dirs). Ohne Argumente.",
	}, n.whoami)
	mcp.AddTool(srv, &mcp.Tool{Name: "list", Description: listDescription}, n.list)
	mcp.AddTool(srv, &mcp.Tool{Name: "read", Description: readDescription}, n.read)
	mcp.AddTool(srv, &mcp.Tool{Name: "changes", Description: changesDescription}, n.changes)
	mcp.AddTool(srv, &mcp.Tool{Name: "create", Description: createDescription}, n.create)
	mcp.AddTool(srv, &mcp.Tool{Name: "write", Description: writeDescription}, n.replace)
	mcp.AddTool(srv, &mcp.Tool{Name: "delete", Description: deleteDescription}, n.remove)
	mcp.AddTool(srv, &mcp.Tool{Name: "rename", Description: renameDescription}, n.rename)
	return srv
}

// hiddenKey markiert im ctx einer Anfrage, dass ihre Antwort verdeckt ist.
type hiddenKey struct{}

func isHidden(ctx context.Context) bool {
	v, _ := ctx.Value(hiddenKey{}).(bool)
	return v
}

// Proxied sagt, ob eine Anfrage über einen Proxy kam: Sie trägt
// X-Forwarded-For. Caddy setzt den Header selbst und verwirft einen, den der
// Client mitschickt; hinter einem Proxy ohne ihn sähe jede Anfrage lokal
// aus. Ein lokaler Prozess, der ihn selbst setzt, verdeckt nur sich selbst.
func Proxied(h http.Header) bool {
	_, ok := h["X-Forwarded-For"]
	return ok
}

// hides sagt, ob die Antwort auf eine Anfrage verdeckt ist: über einen Proxy
// und an keinem Hub gültig angemeldet. Mit gültiger Anmeldung an einem Hub
// antwortet der Node wie lokal, auch zu den übrigen.
func hides(h http.Header, l Logins) bool { return Proxied(h) && len(l.Valid()) == 0 }

// guard lässt nur Anfragen durch, deren Host dieser Rechner mit dem eigenen
// Port ist und deren Origin fehlt oder lokal ist — sonst 403. So erreicht
// eine Webseite im Browser den Node nicht über DNS-Rebinding. Danach prüft es
// die Header-Paare: Ist eines ungültig, vermerkt es zuerst login=invalid im
// Log (ein Fehlversuch je Anfrage; die Zeile setzt via davor), dann die
// Accounts (nur Namen). Über einen Proxy ohne gültige Anmeldung markiert es
// die Anfrage als verdeckt — initialize nennt dann keine Version.
func (n *Node) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := loopback.CheckHost(r); err != nil {
			loopback.Forbid(w, err)
			return
		}
		if err := checkOrigin(r.Header.Values("Origin")); err != nil {
			loopback.Forbid(w, err)
			return
		}
		ctx := r.Context()
		pairs := HubHeaders(r.Header)
		valid := false
		if len(pairs) > 0 {
			// Ein Fehler von node.db zählt nicht als Fehlversuch; das
			// Werkzeug meldet ihn selbst.
			if logins, err := n.Authenticate(ctx, r.Header); err == nil {
				if logins.Failed() {
					reqlog.Note(ctx, "login", "invalid")
				}
				valid = len(logins.Valid()) > 0
			}
		}
		for _, p := range pairs {
			reqlog.Note(ctx, "account", p.Account)
		}
		if Proxied(r.Header) && !valid {
			r = r.WithContext(context.WithValue(ctx, hiddenKey{}, true))
		}
		next.ServeHTTP(w, r)
	})
}

// checkOrigin lässt eine fehlende Origin zu, sonst nur http://localhost… und
// http://127.0.0.1… (auch [::1]).
func checkOrigin(values []string) error {
	switch len(values) {
	case 0:
		return nil
	case 1:
	default:
		return errors.New("Origin mehrfach")
	}
	u, err := url.Parse(values[0])
	if err != nil || u.Scheme != "http" || !loopback.IsHost(u.Hostname()) || u.Path != "" || u.User != nil {
		return fmt.Errorf("Origin %q ist nicht dieser Rechner", values[0])
	}
	return nil
}

// HubHeader ist ein Header-Paar für einen Hub: Alias des Hub-Eintrags,
// Account und Token. Complete ist false, wenn einer der beiden Header fehlt
// oder mehrfach steht.
type HubHeader struct {
	Alias    string
	Account  string
	Token    string
	Complete bool
}

// HubHeaders liest die Header-Paare einer Anfrage, nach Alias sortiert.
//
// Zuordnung: Header-Namen sind in HTTP unabhängig von Groß- und
// Kleinschreibung, und Go schreibt sie kanonisch (X-Keph-Account-Team.x_y).
// Der Alias ist deshalb der Rest des Namens nach dem Präfix
// x-keph-account- bzw. x-keph-token-, klein geschrieben. Aliase folgen der
// Namensregel (klein, a–z, 0–9, '.', '_', '-'), das Kleinschreiben ist also
// eindeutig; ein '-' im Alias bleibt, der Präfix ist fest. Namen, die danach
// keinen gültigen Alias ergeben, übergeht der Node.
func HubHeaders(h http.Header) []HubHeader {
	accounts := map[string][]string{}
	tokens := map[string][]string{}
	for key, values := range h {
		lower := strings.ToLower(key)
		if alias, ok := strings.CutPrefix(lower, accountHeaderPrefix); ok {
			accounts[alias] = append(accounts[alias], values...)
		} else if alias, ok := strings.CutPrefix(lower, tokenHeaderPrefix); ok {
			tokens[alias] = append(tokens[alias], values...)
		}
	}
	seen := map[string]bool{}
	var out []HubHeader
	for _, m := range []map[string][]string{accounts, tokens} {
		for alias := range m {
			if seen[alias] || ident.CheckName("Hub", alias) != nil {
				continue
			}
			seen[alias] = true
			p := HubHeader{Alias: alias}
			a, t := accounts[alias], tokens[alias]
			if len(a) == 1 {
				p.Account = strings.TrimSpace(a[0])
			}
			if len(t) == 1 {
				p.Token = strings.TrimSpace(t[0])
			}
			p.Complete = len(a) == 1 && len(t) == 1 && p.Account != "" && p.Token != ""
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Alias < out[j].Alias })
	return out
}
