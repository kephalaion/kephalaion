package main

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kephalaion/kephalaion/internal/hub/source"
	"github.com/kephalaion/kephalaion/internal/hub/source/sourcetest"
	"github.com/kephalaion/kephalaion/internal/node/mcpnode"
	"github.com/kephalaion/kephalaion/internal/upgrade"
)

// Task 031, Etappe 2: Eine Collection aus der Liste der Quellen (k-playbook)
// bekommt ihre Vorgaben ohne Flag, ist gesperrt, für jeden Node erlaubt und
// fest abonniert; eine, die nur öffentlich ist, liest jeder Account, der am
// Node schon eine Collection desselben Hubs hat.
func TestPublicAndSourceCollections(t *testing.T) {
	e := newCommEnv(t)
	e.run(t, "hub", "collection", "add", "k-playbook").want(t, 0, "Collection k-playbook angelegt",
		"Aus der Liste der Quellen", "https://github.com/kascada/k-playbook", "defaults/")
	e.run(t, "hub", "collection", "add", "offen", "--public").want(t, 0)
	e.run(t, "hub", "collection", "list").want(t, 0, "k-playbook  ja", "kascada/k-playbook", "offen       ja",
		"team-x      nein")
	e.run(t, "hub", "collection", "show", "k-playbook").want(t, 0, "öffentlich:   ja",
		"Quelle:       https://github.com/kascada/k-playbook, Asset k-playbook-defaults.tar.gz",
		"abonniert:    fest", "geholt:       noch nichts (kephalaion hub collection fetch k-playbook)")
	e.run(t, "hub", "collection", "show", "team-x").want(t, 0, "öffentlich:   nein", "Quelle:       keine")
	e.run(t, "hub", "collection", "set", "k-playbook", "--public=false").want(t, 1, "bleibt öffentlich")
	e.run(t, "hub", "collection", "set", "team-x").want(t, 1, "nichts zu ändern")
	e.run(t, "hub", "account", "grant", "bob", "k-playbook").want(t, 1, "Rechte zum Schreiben gibt es darin nicht")
	e.runIn(t, "x", "hub", "doc", "put", "k-playbook", "defaults/x.md").want(t, 1,
		"ändert allein kephalaion hub collection fetch k-playbook")

	e.runIn(t, "# offen\n", "hub", "doc", "put", "offen", "README.md").want(t, 0)
	e.run(t, "hub", "node", "grant", "laptop", "offen").want(t, 0)
	e.run(t, "node", "collection", "add", "eigen:offen").want(t, 0)
	e.run(t, "node", "sync", "eigen").want(t, 0)
	e.run(t, "node", "collection", "list").want(t, 0, "eigen:k-playbook  (fest abonniert)", "eigen:offen")
	e.run(t, "node", "collection", "rm", "eigen:k-playbook").want(t, 1, "fest abonniert")
	e.run(t, "status").want(t, 0, "k-playbook: fest abonniert")

	// alice hat eine Zeile in team-x: Sie liest offen und k-playbook über
	// MCP, ohne Zeile dort; carol ebenso. Schreiben in offen ist forbidden.
	ns := nodeStore(t, e.cfg)
	srv := httptest.NewServer(mcpnode.NewHandler(ns, "test", func() upgrade.Report { return upgrade.Report{} },
		mcpnode.HubLink{}))
	t.Cleanup(srv.Close)
	endpoint := srv.URL + mcpnode.Path
	alice := map[string][2]string{"eigen": {"alice", e.tokens["alice"]}}
	var list mcpnode.ListOutput
	mcpTool(t, endpoint, alice, "list", mcpnode.ListInput{}, &list)
	var names []string
	for _, en := range list.Entries {
		names = append(names, en.Address)
	}
	if strings.Join(names, ",") != "eigen:k-playbook,eigen:offen,eigen:team-x" {
		t.Errorf("lesbare Collections: %v", names)
	}
	var doc mcpnode.ReadOutput
	mcpTool(t, endpoint, alice, "read", mcpnode.ReadInput{Collection: "eigen:offen", Name: "README.md"}, &doc)
	if contentOfRead(doc) != "# offen\n" || doc.Writable == nil || *doc.Writable {
		t.Errorf("read offen: %+v", doc)
	}
	var who mcpnode.WhoamiOutput
	mcpTool(t, endpoint, alice, "whoami", nil, &who)
	h := who.Hubs[0]
	if len(h.Collections) != 3 || !h.Collections[0].Public || h.Collections[0].Collection != "k-playbook" ||
		strings.Join(h.Collections[0].Rights, ",") != "read" || h.Collections[2].Public {
		t.Errorf("whoami: %+v", h.Collections)
	}
	e.run(t, "node", "whoami", "alice", "--hub", "eigen").want(t, 0, "eigen:offen (read, öffentlich)")

	// Ein Account ohne jede Zeile in dieser Replica liest nichts (Grenze aus
	// Entscheidung 4).
	r := e.run(t, "hub", "account", "add", "dave")
	r.want(t, 0)
	e.run(t, "node", "sync", "eigen").want(t, 0)
	var none mcpnode.ReadOutput
	_, text := mcpTool(t, endpoint, map[string][2]string{"eigen": {"dave", tokenFrom(t, r.out)}}, "read",
		mcpnode.ReadInput{Collection: "eigen:offen", Name: "README.md"}, &none)
	if !strings.Contains(text, "nicht lesbar") {
		t.Errorf("dave liest offen: %s", text)
	}

	// Export Format 9 trägt public, nicht den geholten Stand.
	exp := filepath.Join(e.dir, "export.yaml")
	e.run(t, "config", "export", "--output", exp).want(t, 0)
	raw, err := os.ReadFile(exp)
	if err != nil {
		t.Fatal(err)
	}
	data := string(raw)
	for _, want := range []string{"format: 9", "name: k-playbook\n        description: \"\"\n        public: true",
		"name: offen\n        description: \"\"\n        public: true", "name: team-x\n        description: \"\"\n        public: false"} {
		if !strings.Contains(data, want) {
			t.Errorf("Export ohne %q:\n%s", want, data)
		}
	}
	if strings.Contains(data, "fetched") {
		t.Errorf("Export mit geholtem Stand:\n%s", data)
	}
	// Format 8 mit public lehnt der Import ab.
	f8 := strings.Replace(data, "format: 9", "format: 8", 1)
	if err := os.WriteFile(exp, []byte(f8), 0o600); err != nil {
		t.Fatal(err)
	}
	e.run(t, "config", "import", exp).want(t, 1, "Format 8 kennt kein public (tables.hub.collections[0].public)")
	// Ein Import legt k-playbook leer an und nennt fetch.
	if err := os.WriteFile(exp, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	e.run(t, "config", "import", exp).want(t, 0,
		"hub: Collection k-playbook hat eine Quelle; holen mit: kephalaion hub collection fetch k-playbook")
}

// withGitHub lenkt das Holen für einen Test auf ein nachgespieltes GitHub.
func withGitHub(t *testing.T) *sourcetest.GitHub {
	t.Helper()
	gh := sourcetest.New("kascada/k-playbook", "k-playbook-defaults.tar.gz")
	t.Cleanup(gh.Close)
	old := newSourceClient
	newSourceClient = func() *source.Client {
		c := source.NewClient("test")
		c.Base = gh.URL
		return c
	}
	t.Cleanup(func() { newSourceClient = old })
	return gh
}

// Task 031, Etappe 3: Holen beim Anlegen und von Hand; schon aktuell;
// ohne Release bleibt die Collection leer und die Meldung nennt fetch.
func TestHubCollectionFetch(t *testing.T) {
	dir := isolate(t)
	cfg := setup(t, dir)
	gh := withGitHub(t)
	runT(t, "hub", "collection", "add", "k-playbook", "--config", cfg).want(t, 0, "Collection k-playbook angelegt",
		"Noch nichts geholt: https://github.com/kascada/k-playbook hat kein veröffentlichtes Release",
		"Holen mit: kephalaion hub collection fetch k-playbook")
	runT(t, "hub", "collection", "fetch", "k-playbook", "--config", cfg).want(t, 1, "kein veröffentlichtes Release")
	gh.Release("v1.0.0", nil)
	runT(t, "hub", "collection", "fetch", "k-playbook", "--config", cfg).want(t, 1,
		"dem Release v1.0.0 von https://github.com/kascada/k-playbook fehlt das Asset k-playbook-defaults.tar.gz")
	gh.Release("v1.0.0", sourcetest.Archive(map[string]string{"README.md": "# k-playbook\n", "rules/a.md": "a"}))
	runT(t, "hub", "collection", "fetch", "k-playbook", "--config", cfg).want(t, 0,
		"Collection k-playbook: v1.0.0 geholt — 2 angelegt, 0 ersetzt, 0 gelöscht (Revision")
	runT(t, "hub", "collection", "fetch", "k-playbook", "--config", cfg).want(t, 0,
		"Collection k-playbook: schon aktuell (v1.0.0).")
	runT(t, "hub", "doc", "get", "k-playbook", "defaults/README.md", "--config", cfg).want(t, 0, "# k-playbook")
	runT(t, "hub", "collection", "show", "k-playbook", "--config", cfg).want(t, 0, "geholt:       v1.0.0 am ")
	runT(t, "hub", "collection", "add", "team", "--config", cfg).want(t, 0)
	runT(t, "hub", "collection", "fetch", "team", "--config", cfg).want(t, 1, "Collection team hat keine Quelle")
	runT(t, "hub", "collection", "fetch", "fehlt", "--config", cfg).want(t, 1, "Collection fehlt gibt es nicht")

	// Eine zweite Collection gleich beim Anlegen: geholt.
	runT(t, "hub", "collection", "rm", "k-playbook", "--config", cfg).want(t, 0)
	runT(t, "hub", "collection", "add", "k-playbook", "--config", cfg).want(t, 0,
		"Collection k-playbook: v1.0.0 geholt — 2 angelegt")
	if _, loads := gh.Calls(); loads != 3 {
		t.Errorf("Archiv %d-mal geladen", loads)
	}
}
