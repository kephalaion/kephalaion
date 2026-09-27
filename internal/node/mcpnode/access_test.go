package mcpnode

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kephalaion/kephalaion/internal/node/store"
)

// request baut den gemeinsamen Schritt einer Anfrage mit diesen Headern.
func (e *env) request(t *testing.T, header http.Header) *request {
	t.Helper()
	n := &Node{nodes: e.nodes, version: "test"}
	r, err := n.begin(context.Background(), callWith(header))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// access löst addr auf und öffnet das Ziel; die Meldung eines Fehlers ist
// die, die der Client sähe.
func (e *env) access(t *testing.T, header http.Header, addr string) (target, error) {
	t.Helper()
	ctx := context.Background()
	r := e.request(t, header)
	tg, err := r.resolve(addr)
	if err != nil {
		return target{}, toolFailure(ctx, err)
	}
	a, err := r.open(ctx, tg)
	if err != nil {
		return tg, toolFailure(ctx, err)
	}
	a.Close()
	return tg, nil
}

func TestResolveAndAccess(t *testing.T) {
	e := newEnv(t)
	bob, bob2, alice := e.tokens["keph/bob"], e.tokens["team.x_y/bob"], e.tokens["keph/alice"]
	both := merge(pair("keph", "bob", bob), pair("team.x_y", "bob", bob2))

	cases := []struct {
		name   string
		header http.Header
		addr   string
		want   target
		err    string
	}{
		{"mit Hub-Teil", pair("keph", "bob", bob), "keph:privat", target{"keph", "privat"}, ""},
		{"ohne Hub-Teil, ein Hub", pair("keph", "bob", bob), "privat", target{"keph", "privat"}, ""},
		{"ohne Hub-Teil, anderer Hub", pair("team.x_y", "bob", bob2), "notizen", target{"team.x_y", "notizen"}, ""},
		{"mehrere Hubs mit Hub-Teil", both, "team.x_y:notizen", target{"team.x_y", "notizen"}, ""},
		{"mehrere Hubs ohne Hub-Teil", both, "notizen", target{},
			`Adresse "notizen" ohne Hub: angemeldet an keph, team.x_y; erwartet <hub>:<collection>`},
		{"ohne Anmeldung ohne Hub-Teil", nil, "notizen", target{},
			`Adresse "notizen" ohne Hub: an keinem Hub gültig angemeldet; erwartet <hub>:<collection>`},
		{"Wurzel eines Hubs", pair("keph", "bob", bob), "keph:", target{"keph", ""}, ""},
		{"ohne Recht", pair("keph", "alice", alice), "keph:privat", target{},
			"keph:privat nicht lesbar: unbekannt, nicht auf diesem Node oder ohne gültige Anmeldung mit read"},
		{"unbekannte Collection", pair("keph", "alice", alice), "keph:gibt-es-nicht", target{},
			"keph:gibt-es-nicht nicht lesbar: unbekannt, nicht auf diesem Node oder ohne gültige Anmeldung mit read"},
		// Recht am Hub, aber der Node führt die Collection nicht: Die Replica
		// hat keine Zeile des Accounts dafür.
		{"Collection nicht am Node", pair("keph", "bob", bob), "keph:woanders", target{},
			"keph:woanders nicht lesbar: unbekannt, nicht auf diesem Node oder ohne gültige Anmeldung mit read"},
		{"ungültige Anmeldung", pair("keph", "bob", alice), "keph:team-x", target{},
			"keph:team-x nicht lesbar: unbekannt, nicht auf diesem Node oder ohne gültige Anmeldung mit read"},
		{"ohne Anmeldung", nil, "keph:team-x", target{},
			"keph:team-x nicht lesbar: unbekannt, nicht auf diesem Node oder ohne gültige Anmeldung mit read"},
		{"Wurzel ohne Anmeldung", nil, "keph:", target{},
			"keph: nicht lesbar: unbekannt, nicht auf diesem Node oder ohne gültige Anmeldung mit read"},
		{"unbekannter Hub", pair("keph", "bob", bob), "fremd:team-x", target{},
			"fremd:team-x nicht lesbar: unbekannt, nicht auf diesem Node oder ohne gültige Anmeldung mit read"},
		{"SYSTEM als Collection", pair("keph", "bob", bob), "keph:SYSTEM", target{}, `Adresse "keph:SYSTEM": Collection`},
	}
	for _, c := range cases {
		got, err := e.access(t, c.header, c.addr)
		switch {
		case c.err == "" && err != nil:
			t.Errorf("%s: %v", c.name, err)
		case c.err == "" && got != c.want:
			t.Errorf("%s: %+v", c.name, got)
		case c.err != "" && (err == nil || !strings.HasPrefix(err.Error(), c.err)):
			t.Errorf("%s: Fehler %v, erwartet %q", c.name, err, c.err)
		}
	}
}

// Ohne Replica „noch nie abgeglichen“ — mit und ohne Anmeldung, nicht
// „nicht lesbar“ und nicht leer. Hat der Node nur diesen einen Eintrag, darf
// auch der Hub-Teil fehlen.
func TestAccessNeverSynced(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if err := e.nodes.AddHub(ctx, store.Hub{Name: "neu", NodeName: "laptop", Transport: store.TransportHTTPS,
		Address: "https://hub.example.org", Token: token(t)}, false); err != nil {
		t.Fatal(err)
	}
	want := "Hub neu: noch nie abgeglichen, der Node hat keine Replica; zuerst: kephalaion node sync neu"
	for _, h := range []http.Header{nil, pair("neu", "bob", e.tokens["keph/bob"])} {
		for _, addr := range []string{"neu:team-x", "neu:"} {
			if _, err := e.access(t, h, addr); err == nil || err.Error() != want {
				t.Errorf("%s mit %v: %v", addr, h, err)
			}
		}
	}

	// Nur ein Eintrag: ohne Hub-Teil dieser.
	for _, alias := range []string{"keph", "team.x_y"} {
		if err := e.nodes.RemoveHub(ctx, alias); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.access(t, nil, "team-x"); err == nil || err.Error() != want {
		t.Errorf("ohne Hub-Teil: %v", err)
	}
}

// Eine Replica, die sich nicht lesen lässt, betrifft nur ihren Hub: feste
// Meldung ohne Pfad; die anderen Hubs bleiben lesbar. Ein abgebrochener ctx
// ist ein Fehler der Anfrage.
func TestAccessUnreadableReplica(t *testing.T) {
	e := newEnv(t)
	e.breakReplicas(t)
	bob := e.tokens["keph/bob"]
	h := merge(pair("keph", "bob", bob), pair("alt", "bob", bob), pair("kaputt", "bob", bob))
	for _, hub := range []string{"alt", "kaputt"} {
		_, err := e.access(t, h, hub+":team-x")
		if err == nil || err.Error() != "Hub "+hub+": Replica nicht lesbar" {
			t.Errorf("%s: %v", hub, err)
		}
	}
	if _, err := e.access(t, h, "keph:privat"); err != nil {
		t.Errorf("gesunder Hub: %v", err)
	}
	unread, err := e.request(t, h).eachValid(context.Background(), func(a *hubAccess) error {
		if a.hub != "keph" {
			t.Errorf("eachValid öffnet %s", a.hub)
		}
		return nil
	})
	if err != nil || strings.Join(unread, ",") != "alt,kaputt" {
		t.Errorf("eachValid: %v, %v", unread, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := toolFailure(ctx, &UnreadableError{Hub: "alt", Err: context.Canceled}); !errors.Is(err, context.Canceled) {
		t.Errorf("abgebrochen: %v", err)
	}
	r := e.request(t, h)
	if _, err := r.open(ctx, target{"keph", "privat"}); err == nil || asUnreadable(err) != nil {
		t.Errorf("open mit abgebrochenem ctx: %v", err)
	}
}

// callWith ist ein Aufruf eines Werkzeugs mit diesen Headern, ohne Transport.
func callWith(header http.Header) *mcp.CallToolRequest {
	return &mcp.CallToolRequest{Extra: &mcp.RequestExtra{Header: header}}
}
