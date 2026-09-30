package mcpnode

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kephalaion/kephalaion/internal/ident"
	"github.com/kephalaion/kephalaion/internal/node/replica"
	"github.com/kephalaion/kephalaion/internal/node/store"
	"github.com/kephalaion/kephalaion/internal/reqlog"
	"github.com/kephalaion/kephalaion/internal/upgrade"
)

// WhoamiOutput ist die Antwort des Werkzeugs whoami (docs/konzept.md,
// „whoami — festgelegt am 2026-09-26“). Nie darin: Token, Hash, Adresse und
// Transport eines Hubs, hub_id.
type WhoamiOutput struct {
	// Version ist die Version des Nodes.
	Version string `json:"version"`
	// Update sagt, ob es eine neuere Version gibt, ob sich das Binary selbst
	// ersetzen kann und wie das Upgrade geht — aus der Sicht von serve, mit
	// der zwischengespeicherten Antwort von GitHub (höchstens eine Frage am
	// Tag). Dieselbe Struktur wie kephalaion upgrade --check --json.
	Update upgrade.Report `json:"update"`
	// Hubs nennt alle Hub-Einträge des Nodes, nach Alias.
	Hubs []HubInfo `json:"hubs"`
	// UnknownHubs sind die Aliase aus Headern, zu denen der Node keinen
	// Eintrag hat — nur der Alias; eine Hilfe bei falsch eingerichteten
	// Clients.
	UnknownHubs []string `json:"unknown_hubs"`
}

// HubInfo ist ein Hub-Eintrag, wie whoami ihn zeigt.
type HubInfo struct {
	Hub string `json:"hub"`
	// Login ist ok, invalid oder missing.
	Login string `json:"login"`
	// Node ist der Name dieses Nodes am Hub.
	Node string   `json:"node"`
	Sync SyncInfo `json:"sync"`
	// Account, User und Collections nur bei login ok.
	Account     string             `json:"account,omitempty"`
	User        string             `json:"user,omitempty"`
	Collections []CollectionRights `json:"collections,omitempty"`
}

// SyncInfo ist der Stand des Abgleichs eines Hub-Eintrags. Zeiten in RFC
// 3339, UTC.
type SyncInfo struct {
	// NeverSynced: Der Node hat noch keine Replica dieses Hubs; dann fehlt
	// revision, und eine Anmeldung ist invalid, auch mit richtigen
	// Zugangsdaten. Bei einer Replica, die sich nicht lesen lässt, nur, wenn
	// hub_sync keinen gelungenen Abgleich kennt.
	NeverSynced bool `json:"never_synced,omitempty"`
	// LastSuccess ist der letzte gelungene Abgleich.
	LastSuccess string `json:"last_success,omitempty"`
	// Revision ist der Stand, bis zu dem alle Collections der Replica
	// abgeglichen sind; fehlt ohne lesbare Replica.
	Revision *int64 `json:"revision,omitempty"`
	// LastError ist die Art des letzten Fehlers als kurzer Satz, ohne
	// Adresse; leer, wenn der letzte Versuch gelang. Lässt sich die Replica
	// nicht lesen, steht hier ReplicaUnreadable statt eines Fehlers aus
	// hub_sync, und LastErrorAt bleibt leer.
	LastError   string `json:"last_error,omitempty"`
	LastErrorAt string `json:"last_error_at,omitempty"`
}

// CollectionRights ist eine Collection mit Adresse und Rechten. Rights sind
// die Rechte als Text, je Angabe genau ein Eintrag (contract.Rights.List:
// „read“, „write“, „vendor/k-playbook“, „dir docs/“) — aus den Rechten
// gebildet, nicht aus ihrem Text zerlegt: Ein Verzeichnis-Scope darf Komma
// und Leerzeichen tragen. Dirs sind die
// Verzeichnis-Scopes als Namen ohne '/' am Ende, immer eine Liste — die
// strukturierte Angabe, nach der node dir push sein Ziel prüft; ein Node vor
// Task 021 lässt das Feld weg.
type CollectionRights struct {
	Collection string   `json:"collection"`
	Address    string   `json:"address"`
	Rights     []string `json:"rights"`
	Dirs       []string `json:"dirs"`
}

// Whoami baut die Antwort von whoami aus den Anmeldungen, samt Textteil —
// die Version, eine Zeile zum Update, eine Zeile je Hub. update ist die
// Antwort auf die Frage nach einer neuen Version, wie der Aufrufer sie hat. Dieselbe Funktion dient dem Werkzeug (über
// Authenticate) und der Kommandozeile (node whoami, über AccountLogins).
//
// Eine Replica, die sich nicht lesen lässt, betrifft nur ihren Hub: login
// missing, ohne Account, im Stand des Abgleichs ReplicaUnreadable. Die vollen
// Meldungen dazu stehen in unread, je Hub eine — fürs Log bzw. stderr, nie
// für die Antwort. Der Fehler ist nur einer von node.db oder ein
// abgebrochener ctx.
func Whoami(ctx context.Context, nodes store.Store, version string, update upgrade.Report, logins Logins) (
	out WhoamiOutput, text string, unread []*UnreadableError, err error) {
	status, err := nodes.SyncStatus(ctx)
	if err != nil {
		return WhoamiOutput{}, "", nil, err
	}
	hubs, err := nodes.Hubs(ctx)
	if err != nil {
		return WhoamiOutput{}, "", nil, err
	}
	entries := make(map[string]store.Hub, len(hubs))
	for _, h := range hubs {
		entries[h.Name] = h
	}
	out = WhoamiOutput{Version: version, Update: update, Hubs: make([]HubInfo, 0, len(logins.Hubs)),
		UnknownHubs: logins.Unknown}
	if out.UnknownHubs == nil {
		out.UnknownHubs = []string{}
	}
	lines := []string{"kephalaion " + version, update.Summary()}
	for _, l := range logins.Hubs {
		info := HubInfo{Hub: l.Hub, Login: l.State, Node: l.Node}
		sync, err := syncInfo(ctx, nodes, entries[l.Hub], status[l.Hub])
		ue := asUnreadable(err)
		if err != nil && ue == nil {
			return WhoamiOutput{}, "", nil, err
		}
		if ue == nil {
			ue = l.Unreadable
		}
		if ue != nil {
			unread = append(unread, ue)
			sync = unreadableSync(status[l.Hub])
			info.Login = LoginMissing
		}
		info.Sync = sync
		var who string
		switch {
		case ue != nil:
			who = "Anmeldung nicht prüfbar"
		case l.State == LoginOK:
			info.Account, info.User = l.Account, l.User
			parts := make([]string, 0, len(l.Rights))
			for _, r := range l.Rights {
				addr := ident.Address(l.Hub, r.Collection)
				info.Collections = append(info.Collections, CollectionRights{Collection: r.Collection, Address: addr,
					Rights: r.Rights.List(), Dirs: append([]string{}, r.Dirs...)})
				parts = append(parts, fmt.Sprintf("%s (%s)", addr, r.Rights))
			}
			who = fmt.Sprintf("angemeldet als %s (User %s): %s", l.Account, l.User, strings.Join(parts, ", "))
		case l.State == LoginInvalid:
			who = "Anmeldung ungültig"
		default:
			who = "keine Zugangsdaten"
		}
		lines = append(lines, fmt.Sprintf("%s (Node %s): %s; %s", l.Hub, l.Node, who, DescribeSync(sync)))
		out.Hubs = append(out.Hubs, info)
	}
	if len(logins.Hubs) == 0 {
		lines = append(lines, "Keine Hubs eingetragen.")
	}
	if len(out.UnknownHubs) > 0 {
		lines = append(lines, "Zugangsdaten für Hubs, die dieser Node nicht kennt: "+strings.Join(out.UnknownHubs, ", "))
	}
	return out, strings.Join(lines, "\n"), unread, nil
}

// syncInfo liest den Stand des Abgleichs eines Eintrags: hub_sync und die
// Revision seiner Replica. Lässt sich die Replica nicht lesen, ist der Fehler
// ein UnreadableError.
func syncInfo(ctx context.Context, nodes store.Store, h store.Hub, st store.SyncStatus) (SyncInfo, error) {
	var out SyncInfo
	if st.OKAt != 0 {
		out.LastSuccess = formatTime(st.OKAt)
	}
	if st.Err != "" {
		out.LastError, out.LastErrorAt = replica.ErrorKind(st.ErrKind).Text(), formatTime(st.ErrAt)
	}
	rep, err := openReplica(ctx, nodes, h)
	if err != nil {
		return SyncInfo{}, err
	}
	if rep == nil {
		out.NeverSynced = true
		return out, nil
	}
	defer rep.Close()
	rev, err := rep.Revision(ctx)
	if err != nil {
		return SyncInfo{}, unreadable(ctx, h.Name, err)
	}
	out.Revision = &rev
	return out, nil
}

// unreadableSync ist der Stand eines Eintrags, dessen Replica sich nicht
// lesen lässt: keine Revision, als letzter Fehler der feste Satz ohne Zeit —
// er ersetzt einen Fehler aus hub_sync —, der letzte Erfolg aus hub_sync;
// never_synced nur, wenn es keinen gab.
func unreadableSync(st store.SyncStatus) SyncInfo {
	out := SyncInfo{LastError: ReplicaUnreadable, NeverSynced: st.OKAt == 0}
	if st.OKAt != 0 {
		out.LastSuccess = formatTime(st.OKAt)
	}
	return out
}

func formatTime(ms int64) string { return time.UnixMilli(ms).UTC().Format(time.RFC3339) }

// DescribeSync ist der Stand des Abgleichs als Text, wie im Textteil von
// whoami.
func DescribeSync(s SyncInfo) string {
	var parts []string
	switch {
	case s.NeverSynced:
		parts = append(parts, "noch nie abgeglichen")
	case s.LastSuccess != "":
		parts = append(parts, "abgeglichen "+s.LastSuccess)
	}
	if s.Revision != nil {
		parts = append(parts, fmt.Sprintf("Revision %d", *s.Revision))
	}
	text := strings.Join(parts, ", ")
	if s.LastError != "" {
		last := "letzter Fehler"
		if s.LastErrorAt != "" {
			last += " " + s.LastErrorAt
		}
		last += ": " + s.LastError
		if text == "" {
			return last
		}
		text += "; " + last
	}
	return text
}

func (n *Node) whoami(ctx context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, WhoamiOutput, error) {
	var header http.Header
	if req != nil && req.Extra != nil {
		header = req.Extra.Header
	}
	logins, err := n.Authenticate(ctx, header)
	if err != nil {
		reqlog.NoteError(ctx, err)
		return nil, WhoamiOutput{}, fmt.Errorf("Datenbank des Nodes nicht lesbar")
	}
	out, text, unread, err := Whoami(ctx, n.nodes, n.version, n.update(), logins)
	if err != nil {
		reqlog.NoteError(ctx, err)
		return nil, WhoamiOutput{}, fmt.Errorf("Datenbank des Nodes nicht lesbar")
	}
	// Die vollen Meldungen nennen den Pfad: nur ins Log.
	for _, ue := range unread {
		reqlog.NoteError(ctx, ue)
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}, out, nil
}
