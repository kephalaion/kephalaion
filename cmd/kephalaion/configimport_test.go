package main

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/kephalaion/kephalaion/internal/config"
	hubstore "github.com/kephalaion/kephalaion/internal/hub/store"
	"github.com/kephalaion/kephalaion/internal/ident"
	nodestore "github.com/kephalaion/kephalaion/internal/node/store"
	"github.com/kephalaion/kephalaion/internal/sqlitedb"
)

// fill richtet unter cfg Collections, Nodes, Rechte, Accounts (einer
// gesperrt), Hubs und gewünschte Collections ein und liefert das Token des
// Hub-Eintrags.
func fill(t *testing.T, cfg string) string {
	t.Helper()
	tok := fillBase(t, cfg)
	c := "--config=" + cfg
	runT(t, "hub", "account", "add", "alice", c).want(t, 0)
	runT(t, "hub", "account", "add", "bob", "--user", "kleist", "--description", "Bob", c).want(t, 0)
	runT(t, "hub", "account", "grant", "alice", "team-x", c).want(t, 0)
	runT(t, "hub", "account", "grant", "bob", "team-x", "--write", c).want(t, 0)
	runT(t, "hub", "account", "grant", "bob", "privat", "--supersede", c).want(t, 0)
	runT(t, "hub", "account", "lock", "bob", c).want(t, 0)
	return tok
}

// fillBase ist fill ohne Accounts: Deren Zeilen blieben als Löschmarken in
// den Collections und sperrten sie gegen Entfernen.
func fillBase(t *testing.T, cfg string) string {
	t.Helper()
	c := "--config=" + cfg
	runT(t, "hub", "collection", "add", "team-x", "--description", "Team X", c).want(t, 0)
	runT(t, "hub", "collection", "add", "privat", c).want(t, 0)
	r := runT(t, "hub", "node", "add", "laptop", "--description", "Notebook", c)
	r.want(t, 0)
	tok := tokenFrom(t, r.out)
	runT(t, "hub", "node", "add", "desktop", c).want(t, 0)
	runT(t, "hub", "node", "grant", "laptop", "team-x", c).want(t, 0)
	runT(t, "hub", "node", "lock", "desktop", c).want(t, 0)
	runIn(t, tok, "node", "hub", "add", "lokal", "--node", "laptop", "--transport", "local", "--token-stdin", c).want(t, 0)
	runIn(t, tok, "node", "hub", "add", "fern", "--node", "rechner-fern", "--transport", "ssh", "--address", "keph@hub:22",
		"--ssh-key", "/k/id", "--token-stdin", c).want(t, 0)
	runT(t, "node", "collection", "add", "lokal:team-x", c).want(t, 0)
	runT(t, "node", "collection", "add", "fern:notizen", c).want(t, 0)
	return tok
}

func hubStore(t *testing.T, cfgPath string) hubstore.Store {
	t.Helper()
	cfg, _, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	s, err := openSection(context.Background(), config.Hub, cfg.Section(config.Hub))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s.(hubstore.Store)
}

func nodeStore(t *testing.T, cfgPath string) nodestore.Store {
	t.Helper()
	cfg, _, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	s, err := openSection(context.Background(), config.Node, cfg.Section(config.Node))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s.(nodestore.Store)
}

func hubTables(t *testing.T, cfg string) hubstore.Tables {
	t.Helper()
	tables, err := hubStore(t, cfg).Tables(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return tables
}

// nodeTables liest die lokalen Tabellen des Nodes, ohne entry_id: Sie ist
// je Datenbank vergeben und nicht im Export.
func nodeTables(t *testing.T, cfg string) nodestore.Tables {
	t.Helper()
	tables, err := nodeStore(t, cfg).Tables(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for i := range tables.Hubs {
		tables.Hubs[i].EntryID = ""
	}
	return tables
}

// rawHub öffnet die Datenbank des Hubs ohne Store, für Prüfungen und
// Handgriffe, die der Store nicht anbietet.
func rawHub(t *testing.T, dir string) *sql.DB {
	t.Helper()
	db, err := sqlitedb.Open(context.Background(), filepath.Join(dir, "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func countImports(t *testing.T, dir string) int {
	t.Helper()
	var n int
	if err := rawHub(t, dir).QueryRow(`SELECT COUNT(*) FROM actions WHERE action = 'config.import' AND account = 'admin'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func exportTo(t *testing.T, cfg, file string) {
	t.Helper()
	runT(t, "config", "export", "--config", cfg, "--output", file).want(t, 0)
}

func TestImportRoundTripTables(t *testing.T) {
	dir := isolate(t)
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	cfgA := setup(t, a)
	tok := fill(t, cfgA)
	setSettings(t, cfgA, config.Hub, map[string]string{"gruss": ":8443"})
	exp := filepath.Join(dir, "export.yaml")
	exportTo(t, cfgA, exp)
	data, _ := os.ReadFile(exp)
	for _, want := range []string{"token_hash: " + ident.HashToken(tok), "token: " + tok, "node_collections:", "hub_collections:",
		"format: 7", "node_name: laptop", "node_name: rechner-fern", "accounts:", "name: alice", "supersede: true",
		"user: alice", "user: kleist", "vendor: []", "ca: \"\""} {
		if !strings.Contains(string(data), want) {
			t.Errorf("Export ohne %q", want)
		}
	}
	if strings.Contains(string(data), "hub_id: 0") || strings.Contains(string(data), "revision") {
		t.Errorf("Export enthält db_info:\n%s", data)
	}

	cfgB := setup(t, b)
	runT(t, "config", "import", "--config", cfgB, exp).want(t, 0,
		"hub: settings ersetzt (1 Einträge), 2 Collections, 2 Nodes, 1 Rechte, 2 Accounts; config.import im Protokoll",
		"node: settings ersetzt (0 Einträge), 2 Hubs, 2 Collections")
	if got, want := hubTables(t, cfgB), hubTables(t, cfgA); !reflect.DeepEqual(got, want) {
		t.Errorf("Hub-Tabellen\n%+v\nerwartet\n%+v", got, want)
	}
	if got, want := nodeTables(t, cfgB), nodeTables(t, cfgA); !reflect.DeepEqual(got, want) {
		t.Errorf("Node-Tabellen\n%+v\nerwartet\n%+v", got, want)
	}
	infoA, _ := hubStore(t, cfgA).Info(context.Background())
	infoB, _ := hubStore(t, cfgB).Info(context.Background())
	if infoA.HubID == infoB.HubID || infoB.HubID == "" {
		t.Errorf("hub_id: %q und %q", infoA.HubID, infoB.HubID)
	}
	if n := countImports(t, b); n != 1 {
		t.Errorf("%d Zeilen config.import", n)
	}
	// Der importierte Node gilt mit seinem alten Token.
	n, err := hubStore(t, cfgB).Node(context.Background(), "laptop")
	if err != nil || n.TokenHash != ident.HashToken(tok) {
		t.Errorf("Node = %+v, %v", n, err)
	}

	runT(t, "status", "--config", cfgB).want(t, 0,
		"hub_id:        "+infoB.HubID, "Collections:   privat, team-x",
		"desktop: gesperrt, erlaubt: keine", "laptop: aktiv, erlaubt: team-x",
		"fern: ssh keph@hub:22, als Node rechner-fern\n      hub_id:      noch kein Abgleich\n      Collections: notizen",
		"lokal: local, als Node laptop\n      hub_id:      noch kein Abgleich\n      Collections: team-x")
}

// Ein Export im Format 2 kennt hubs.node_name nicht; ein Hub-Eintrag ohne ihn
// scheitert an derselben Prüfung wie node hub add ohne --node.
func TestImportFormat2WithoutNodeName(t *testing.T) {
	dir := isolate(t)
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	fill(t, setup(t, a))
	exp := filepath.Join(dir, "export.yaml")
	exportTo(t, filepath.Join(a, "config.yaml"), exp)
	data, _ := os.ReadFile(exp)
	var lines []string
	for _, l := range strings.Split(string(data), "\n") {
		if !strings.Contains(l, "node_name:") {
			lines = append(lines, l)
		}
	}
	old := strings.Replace(dropAccounts(t, toFormat6(t, strings.Join(lines, "\n"))), "format: 6", "format: 2", 1)
	if err := os.WriteFile(exp, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	cfgB := setup(t, b)
	before := takeSnapshot(t, b, cfgB)
	runT(t, "config", "import", "--config", cfgB, exp).want(t, 1, "Rolle node", "--node", "Nichts geschrieben")
	if after := takeSnapshot(t, b, cfgB); !reflect.DeepEqual(before, after) {
		t.Errorf("trotz Abbruch geschrieben:\n%+v\n%+v", before, after)
	}
}

func TestImportEmptyRoundTrip(t *testing.T) {
	dir := isolate(t)
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	cfgA := setup(t, a)
	exp := filepath.Join(dir, "export.yaml")
	exportTo(t, cfgA, exp)
	data, _ := os.ReadFile(exp)
	for _, want := range []string{"hub: {}", "node: {}", "collections: []", "nodes: []", "node_collections: []",
		"hubs: []", "hub_collections: []", "accounts: []"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("Export ohne %q:\n%s", want, data)
		}
	}
	cfgB := setup(t, b)
	fillBase(t, cfgB)
	setSettings(t, cfgB, config.Node, map[string]string{"alt": "weg"})
	runT(t, "config", "import", "--config", cfgB, exp).want(t, 0)
	if h := hubTables(t, cfgB); len(h.Collections)+len(h.Nodes)+len(h.Grants)+len(h.Accounts) != 0 {
		t.Errorf("Hub nicht geleert: %+v", h)
	}
	if n := nodeTables(t, cfgB); len(n.Hubs)+len(n.Wanted) != 0 {
		t.Errorf("Node nicht geleert: %+v", n)
	}
	if got := getSettings(t, cfgB, config.Node); len(got) != 0 {
		t.Errorf("settings nicht geleert: %v", got)
	}
}

// snapshot hält den Stand beider Rollen fest, um „nichts geschrieben“ zu
// prüfen.
type snapshot struct {
	hub          hubstore.Tables
	node         nodestore.Tables
	hubSettings  map[string]string
	nodeSettings map[string]string
	imports      int
}

func takeSnapshot(t *testing.T, dir, cfg string) snapshot {
	t.Helper()
	return snapshot{hubTables(t, cfg), nodeTables(t, cfg), getSettings(t, cfg, config.Hub),
		getSettings(t, cfg, config.Node), countImports(t, dir)}
}

func TestImportInvalidWritesNothing(t *testing.T) {
	dir := isolate(t)
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	cfgA := setup(t, a)
	fill(t, cfgA)
	exp := filepath.Join(dir, "export.yaml")
	exportTo(t, cfgA, exp)
	orig, _ := os.ReadFile(exp)

	cfgB := setup(t, b)
	runT(t, "hub", "collection", "add", "bleibt", "--config", cfgB).want(t, 0)
	setSettings(t, cfgB, config.Hub, map[string]string{"alt": "1"})
	setSettings(t, cfgB, config.Node, map[string]string{"alt": "2"})
	before := takeSnapshot(t, b, cfgB)

	cases := []struct {
		name, old, new, want string
	}{
		{"Node-Token", "token: keph_", "token: kaputt_", "ungültiges Token"},
		{"Transport", "transport: local", "transport: ftp", "unbekannter Transport"},
		{"Collection-Name", "name: team-x", "name: Team-X", "ungültiger Name"},
		{"Hash", "token_hash: ", "token_hash: x", "token_hash"},
		{"Adresse", "collection: notizen", "collection: system", "reserviert"},
		{"settings fehlt", "settings:\n", "einstellungen:\n", ""},
	}
	for _, c := range cases {
		if !strings.Contains(string(orig), c.old) {
			t.Fatalf("%s: %q nicht im Export", c.name, c.old)
		}
		mod := strings.Replace(string(orig), c.old, c.new, 1)
		if err := os.WriteFile(exp, []byte(mod), 0o600); err != nil {
			t.Fatal(err)
		}
		r := runT(t, "config", "import", "--config", cfgB, exp)
		r.want(t, 1, c.want, "Nichts geschrieben")
		if after := takeSnapshot(t, b, cfgB); !reflect.DeepEqual(after, before) {
			t.Errorf("%s: verändert\n%+v\n%+v", c.name, before, after)
		}
	}
}

// Ein Account-Name belegt den Node-Namen auch beim Import — hier im Export
// selbst (Node und Account gleichen Namens) und bei einem Export ohne
// Accounts gegen die vorhandenen.
func TestImportNodeNameTakenByAccount(t *testing.T) {
	dir := isolate(t)
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	cfgA := setup(t, a)
	fill(t, cfgA)
	exp := filepath.Join(dir, "export.yaml")
	exportTo(t, cfgA, exp)
	data, _ := os.ReadFile(exp)
	both := filepath.Join(dir, "both.yaml")
	if err := os.WriteFile(both, []byte(strings.Replace(string(data), "name: alice", "name: laptop", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	cfgB := setup(t, b)
	runT(t, "config", "import", "--config", cfgB, both).want(t, 1, "gemeinsam eindeutig", "Nichts geschrieben")

	old := filepath.Join(dir, "old.yaml")
	if err := os.WriteFile(old, []byte(strings.Replace(dropAccounts(t, toFormat6(t, string(data))), "format: 6", "format: 3", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	runT(t, "hub", "account", "add", "laptop", "--config", cfgB).want(t, 0)
	runT(t, "config", "import", "--config", cfgB, old).want(t, 1, "Account", "Nichts geschrieben")
	if n := countImports(t, b); n != 0 {
		t.Errorf("%d Zeilen config.import", n)
	}
}

// dropAccounts entfernt den Teil tables.hub.accounts aus einem Export, wie
// ihn ein Export vor Format 4 nicht hat.
func dropAccounts(t *testing.T, data string) string {
	t.Helper()
	var out []string
	skip := false
	found := false
	for _, l := range strings.Split(data, "\n") {
		if l == "    accounts:" || l == "    accounts: []" {
			skip, found = l == "    accounts:", true
			continue
		}
		if skip && (strings.HasPrefix(l, "      ") || strings.HasPrefix(l, "    - ")) {
			continue
		}
		skip = false
		out = append(out, l)
	}
	if !found {
		t.Fatalf("kein accounts-Teil im Export:\n%s", data)
	}
	return strings.Join(out, "\n")
}

// Ein Export vor Format 4 lässt die Accounts, wie sie sind; einer im Format 4
// ohne Accounts-Teil bricht ab; ein älterer mit Accounts-Teil ebenso.
func TestImportAccountsPart(t *testing.T) {
	dir := isolate(t)
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	cfgA := setup(t, a)
	fill(t, cfgA)
	exp := filepath.Join(dir, "export.yaml")
	exportTo(t, cfgA, exp)
	data, _ := os.ReadFile(exp)
	cfgB := setup(t, b)
	runT(t, "hub", "collection", "add", "team-x", "--config", cfgB).want(t, 0)
	runT(t, "hub", "account", "add", "carol", "--config", cfgB).want(t, 0)
	runT(t, "hub", "account", "grant", "carol", "team-x", "--config", cfgB).want(t, 0)
	before := hubTables(t, cfgB).Accounts

	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	missing := write("missing.yaml", dropAccounts(t, string(data)))
	runT(t, "config", "import", "--config", cfgB, missing).want(t, 1, "tables.hub.accounts fehlt", "Nichts geschrieben")
	oldWith := write("oldwith.yaml", strings.Replace(toFormat6(t, string(data)), "format: 6", "format: 3", 1))
	runT(t, "config", "import", "--config", cfgB, oldWith).want(t, 1, "kennt keine Accounts", "Nichts geschrieben")
	if got := hubTables(t, cfgB).Accounts; !reflect.DeepEqual(got, before) {
		t.Errorf("Accounts verändert: %+v", got)
	}

	old := write("old.yaml", strings.Replace(dropAccounts(t, toFormat6(t, string(data))), "format: 6", "format: 3", 1))
	// team-x bleibt im Export, carol behält ihre Zeile.
	runT(t, "config", "import", "--config", cfgB, old).want(t, 0, "Accounts unberührt (Format 3)")
	if got := hubTables(t, cfgB).Accounts; !reflect.DeepEqual(got, before) {
		t.Errorf("Accounts nach Format 3: %+v", got)
	}

	runT(t, "config", "import", "--config", cfgB, exp).want(t, 0, "2 Accounts")
	got := hubTables(t, cfgB).Accounts
	if len(got) != 2 || got[0].Name != "alice" || got[1].Name != "bob" || !got[1].Locked {
		t.Errorf("Accounts nach Format 7: %+v", got)
	}
	// carol ist weg, ihre Zeile eine Löschmarke; alices Zeile lebt.
	var live, dead int
	if err := rawHub(t, b).QueryRow(`SELECT
		(SELECT COUNT(*) FROM documents WHERE name = 'SYSTEM:A:alice' AND deleted = 0),
		(SELECT COUNT(*) FROM documents WHERE name = 'SYSTEM:A:carol' AND deleted = 1)`).Scan(&live, &dead); err != nil {
		t.Fatal(err)
	}
	if live != 1 || dead != 1 {
		t.Errorf("Zeilen: alice lebend %d, carol Löschmarke %d", live, dead)
	}
}

func TestImportRemovingCollectionWithDocuments(t *testing.T) {
	dir := isolate(t)
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	cfgA := setup(t, a)
	exp := filepath.Join(dir, "export.yaml")
	exportTo(t, cfgA, exp)

	cfgB := setup(t, b)
	runT(t, "hub", "collection", "add", "voll", "--config", cfgB).want(t, 0)
	if _, err := rawHub(t, b).Exec(`INSERT INTO documents
		(id, collection, name, deleted, revision, created_at, created_by, updated_at, updated_by)
		VALUES ('1', 'voll', 'weg.md', 1, 1, 0, 'x', 0, 'x')`); err != nil {
		t.Fatal(err)
	}
	before := takeSnapshot(t, b, cfgB)
	runT(t, "config", "import", "--config", cfgB, exp).want(t, 1, "Collection voll", "Dokument", "Nichts geschrieben")
	if after := takeSnapshot(t, b, cfgB); !reflect.DeepEqual(after, before) {
		t.Errorf("verändert\n%+v\n%+v", before, after)
	}
}

// Ein Export im Format 1 (Task 002) ersetzt nur die settings.
func TestImportFormat1(t *testing.T) {
	dir := isolate(t)
	cfg := setup(t, dir)
	fill(t, cfg)
	before := takeSnapshot(t, dir, cfg)
	exp := filepath.Join(dir, "export1.yaml")
	content := "format: 1\nconfig:\n  hub:\n    db: sqlite:///x/hub.db\n  node:\n    db: sqlite:///x/node.db\n" +
		"settings:\n  hub:\n    gruss: :8443\n  node: {}\n"
	if err := os.WriteFile(exp, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	runT(t, "config", "import", "--config", cfg, exp).want(t, 0,
		"hub: settings ersetzt (1 Einträge); config.import im Protokoll; Tabellen unberührt (Format 1)",
		"node: settings ersetzt (0 Einträge); Tabellen unberührt (Format 1)")
	if got := getSettings(t, cfg, config.Hub); !reflect.DeepEqual(got, map[string]string{"gruss": ":8443"}) {
		t.Errorf("hub settings = %v", got)
	}
	if !reflect.DeepEqual(hubTables(t, cfg), before.hub) || !reflect.DeepEqual(nodeTables(t, cfg), before.node) {
		t.Error("Format 1 hat Tabellen verändert")
	}
	if n := countImports(t, dir); n != 1 {
		t.Errorf("%d Zeilen config.import", n)
	}

	// Format 1 mit tables: ist kein Export dieses Programms.
	if err := os.WriteFile(exp, []byte(content+"tables: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runT(t, "config", "import", "--config", cfg, exp).want(t, 1, "Format 1 kennt keine Tabellen")
}

// config.import steht nur dann im Protokoll des Hubs, wenn der Export einen
// Hub-Teil hat.
func TestImportNodeOnlyNoHubAction(t *testing.T) {
	dir := isolate(t)
	cfg := setup(t, dir)
	exp := filepath.Join(dir, "export.yaml")
	content := "format: 2\nconfig:\n  node:\n    db: sqlite:///x/node.db\nsettings:\n  node:\n    a: b\n" +
		"tables:\n  node:\n    hubs: []\n    hub_collections: []\n"
	if err := os.WriteFile(exp, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	r := runT(t, "config", "import", "--config", cfg, exp)
	r.want(t, 0, "node: settings ersetzt (1 Einträge)")
	if strings.Contains(r.out, "hub:") {
		t.Errorf("Hub erwähnt:\n%s", r.out)
	}
	if n := countImports(t, dir); n != 0 {
		t.Errorf("%d Zeilen config.import ohne Hub-Teil", n)
	}
}

// Fehlt der Teil einer Rolle oder ist er null, bricht der Import ab; nur ein
// ausdrücklich leerer Teil leert.
func TestImportMissingOrNullPart(t *testing.T) {
	dir := isolate(t)
	cfg := setup(t, dir)
	setSettings(t, cfg, config.Hub, map[string]string{"bleibt": "1"})
	fill(t, cfg)
	before := takeSnapshot(t, dir, cfg)
	head := "config:\n  hub:\n    db: sqlite:///x/hub.db\n"
	hubTablesYAML := "tables:\n  hub:\n    collections: []\n    nodes: []\n    node_collections: []\n"
	cases := []struct{ name, content, want string }{
		{"F1 settings fehlt", "format: 1\n" + head, "settings.hub fehlt"},
		{"F1 settings null", "format: 1\n" + head + "settings: null\n", "settings.hub fehlt"},
		{"F1 Rolle null", "format: 1\n" + head + "settings:\n  hub: null\n", "settings.hub ist null"},
		{"F1 Rolle ~", "format: 1\n" + head + "settings:\n  hub: ~\n", "settings.hub ist null"},
		{"F2 settings fehlt", "format: 2\n" + head + hubTablesYAML, "settings.hub fehlt"},
		{"F2 Rolle null", "format: 2\n" + head + "settings:\n  hub:\n" + hubTablesYAML, "settings.hub ist null"},
		{"F2 tables fehlt", "format: 2\n" + head + "settings:\n  hub: {}\n", "tables.hub fehlt"},
		{"F2 Tabelle fehlt", "format: 2\n" + head + "settings:\n  hub: {}\ntables:\n  hub:\n    collections: []\n    nodes: []\n",
			"tables.hub.node_collections fehlt"},
		{"F2 Tabelle null", "format: 2\n" + head + "settings:\n  hub: {}\ntables:\n  hub:\n    collections:\n    nodes: []\n    node_collections: []\n",
			"tables.hub.collections ist null"},
	}
	exp := filepath.Join(dir, "export.yaml")
	for _, c := range cases {
		if err := os.WriteFile(exp, []byte(c.content), 0o600); err != nil {
			t.Fatal(err)
		}
		runT(t, "config", "import", "--config", cfg, exp).want(t, 1, c.want, "Nichts geschrieben")
		if after := takeSnapshot(t, dir, cfg); !reflect.DeepEqual(after, before) {
			t.Errorf("%s: verändert", c.name)
		}
	}
	// settings: {} leert, Format 1 lässt die Tabellen stehen.
	if err := os.WriteFile(exp, []byte("format: 1\n"+head+"settings:\n  hub: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runT(t, "config", "import", "--config", cfg, exp).want(t, 0, "hub: settings ersetzt (0 Einträge)")
	if got := getSettings(t, cfg, config.Hub); len(got) != 0 {
		t.Errorf("settings = %v", got)
	}
	if !reflect.DeepEqual(hubTables(t, cfg), before.hub) {
		t.Error("Tabellen verändert")
	}
}

// Scheitert das Schreiben des Nodes, nachdem der Hub committet ist, sagt die
// Meldung, was geschrieben ist.
func TestImportNodeFailsAfterHub(t *testing.T) {
	dir := isolate(t)
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	cfgA := setup(t, a)
	fill(t, cfgA)
	exp := filepath.Join(dir, "export.yaml")
	exportTo(t, cfgA, exp)
	cfgB := setup(t, b)
	nodeBefore := nodeTables(t, cfgB)

	orig := nodeImport
	t.Cleanup(func() { nodeImport = orig })
	nodeImport = func(context.Context, nodestore.Store, map[string]string, *nodestore.Tables, bool) error {
		return errors.New("Platte voll")
	}
	r := runT(t, "config", "import", "--config", cfgB, exp)
	r.want(t, 1, "Rolle node: Platte voll", "hub: ersetzt", "config.import im Protokoll", "node: nicht geschrieben")
	if strings.Contains(r.errOut, "Nichts geschrieben") {
		t.Error("meldet „Nichts geschrieben“, obwohl der Hub ersetzt ist")
	}
	if got, want := hubTables(t, cfgB), hubTables(t, cfgA); !reflect.DeepEqual(got, want) {
		t.Error("Hub nicht ersetzt")
	}
	if n := countImports(t, b); n != 1 {
		t.Errorf("%d Zeilen config.import", n)
	}
	if got := nodeTables(t, cfgB); !reflect.DeepEqual(got, nodeBefore) {
		t.Error("Node verändert")
	}
}

// accounts: ohne Wert (null) leert nie: Format 4 und 5 brechen ab,
// Format 3 lehnt jeden Accounts-Teil ab — null wie [].
func TestImportAccountsNull(t *testing.T) {
	dir := isolate(t)
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	cfgA := setup(t, a)
	fill(t, cfgA)
	exp := filepath.Join(dir, "export.yaml")
	exportTo(t, cfgA, exp)
	data, _ := os.ReadFile(exp)
	cfgB := setup(t, b)
	runT(t, "hub", "collection", "add", "team-x", "--config", cfgB).want(t, 0)
	runT(t, "hub", "account", "add", "carol", "--config", cfgB).want(t, 0)
	runT(t, "hub", "account", "grant", "carol", "team-x", "--config", cfgB).want(t, 0)
	before := takeSnapshot(t, b, cfgB)

	dropped := dropAccounts(t, toFormat6(t, string(data)))
	if !strings.Contains(dropped, "    node_collections:") {
		t.Fatalf("kein node_collections im Export:\n%s", dropped)
	}
	with := func(part string) string {
		return strings.Replace(dropped, "    node_collections:", part+"\n    node_collections:", 1)
	}
	format3 := func(s string) string { return strings.Replace(s, "format: 6", "format: 3", 1) }
	cases := []struct{ name, content, want string }{
		{"F5 ohne Wert", with("    accounts:"), "tables.hub.accounts ist null"},
		{"F5 null", with("    accounts: null"), "tables.hub.accounts ist null"},
		{"F5 ~", with("    accounts: ~"), "tables.hub.accounts ist null"},
		{"F3 ohne Wert", format3(with("    accounts:")), "kennt keine Accounts"},
		{"F3 null", format3(with("    accounts: null")), "kennt keine Accounts"},
		{"F3 leer", format3(with("    accounts: []")), "kennt keine Accounts"},
	}
	file := filepath.Join(dir, "null.yaml")
	for _, c := range cases {
		if err := os.WriteFile(file, []byte(c.content), 0o600); err != nil {
			t.Fatal(err)
		}
		runT(t, "config", "import", "--config", cfgB, file).want(t, 1, c.want, "Nichts geschrieben")
		if after := takeSnapshot(t, b, cfgB); !reflect.DeepEqual(after, before) {
			t.Errorf("%s: verändert", c.name)
		}
	}
}

// toFormat6 macht aus einem Export im Format 7 einen im Format 6: ohne die
// CA je Hub-Eintrag — die Zeile ca: und, bei einem Block, die Zeilen
// darunter.
func toFormat6(t *testing.T, data string) string {
	t.Helper()
	var out []string
	dropped, inBlock := 0, false
	for _, l := range strings.Split(data, "\n") {
		switch {
		case strings.HasPrefix(l, "        ca:"):
			dropped++
			inBlock = true
			continue
		case inBlock && strings.HasPrefix(l, "          "):
			continue
		}
		inBlock = false
		out = append(out, l)
	}
	if dropped == 0 {
		t.Fatalf("keine CA im Export:\n%s", data)
	}
	return strings.Replace(strings.Join(out, "\n"), "format: 7", "format: 6", 1)
}

// toFormat5 macht aus einem Export im Format 7 einen im Format 5: ohne CA und
// ohne die Scopes vendor je Recht — die Zeile vendor: und die Einträge der
// Liste darunter.
func toFormat5(t *testing.T, data string) string {
	t.Helper()
	data = toFormat6(t, data)
	var out []string
	dropped, inList := 0, false
	for _, l := range strings.Split(data, "\n") {
		switch {
		case strings.HasPrefix(l, "            vendor:"):
			dropped++
			inList = true
			continue
		case inList && strings.HasPrefix(l, "              - "):
			continue
		}
		inList = false
		out = append(out, l)
	}
	if dropped == 0 {
		t.Fatalf("kein vendor im Export:\n%s", data)
	}
	return strings.Replace(strings.Join(out, "\n"), "format: 6", "format: 5", 1)
}

// toFormat4 macht aus einem Export im Format 7 einen im Format 4: ohne CA,
// ohne user und ohne vendor.
func toFormat4(t *testing.T, data string) string {
	t.Helper()
	return strings.Replace(dropLines(t, toFormat5(t, data), "        user: "), "format: 5", "format: 4", 1)
}

// dropLines entfernt die Zeilen, die mit prefix beginnen; keine ist ein
// Fehler des Tests.
func dropLines(t *testing.T, data, prefix string) string {
	t.Helper()
	var out []string
	dropped := 0
	for _, l := range strings.Split(data, "\n") {
		if strings.HasPrefix(l, prefix) {
			dropped++
			continue
		}
		out = append(out, l)
	}
	if dropped == 0 {
		t.Fatalf("keine Zeile %q im Export:\n%s", prefix, data)
	}
	return strings.Join(out, "\n")
}

// Der User je Account: Format 5 trägt ihn und verlangt ihn — fehlend, null,
// leer, ungültig oder admin bricht ohne Änderung ab. Format 4 kennt ihn nicht;
// sein Import setzt ihn auf den Namen des Accounts.
func TestImportAccountUser(t *testing.T) {
	dir := isolate(t)
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	cfgA := setup(t, a)
	fill(t, cfgA)
	exp := filepath.Join(dir, "export.yaml")
	exportTo(t, cfgA, exp)
	raw, _ := os.ReadFile(exp)
	data := string(raw)
	cfgB := setup(t, b)
	runT(t, "hub", "collection", "add", "team-x", "--config", cfgB).want(t, 0)
	runT(t, "hub", "account", "add", "carol", "--user", "kleist", "--config", cfgB).want(t, 0)
	before := takeSnapshot(t, b, cfgB)

	file := filepath.Join(dir, "user.yaml")
	write := func(content string) {
		if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	const bobUser = "\n        user: kleist"
	if !strings.Contains(data, bobUser+"\n") {
		t.Fatalf("Export ohne %q:\n%s", bobUser, data)
	}
	cases := []struct{ name, content, want string }{
		{"fehlt", strings.Replace(data, bobUser, "", 1), "tables.hub.accounts[1]: user fehlt"},
		{"ohne Wert", strings.Replace(data, bobUser, "\n        user:", 1), "tables.hub.accounts[1]: user ist null"},
		{"null", strings.Replace(data, bobUser, "\n        user: null", 1), "user ist null"},
		{"~", strings.Replace(data, bobUser, "\n        user: ~", 1), "user ist null"},
		{"leer", strings.Replace(data, bobUser, "\n        user: \"\"", 1), "Account bob: User: Name fehlt"},
		{"ungültig", strings.Replace(data, bobUser, "\n        user: Kleist", 1), "Account bob: User \"Kleist\": ungültiger Name"},
		{"admin", strings.Replace(data, bobUser, "\n        user: admin", 1), "reserviert"},
		{"F4 mit user", strings.Replace(toFormat5(t, data), "format: 5", "format: 4", 1), "Format 4 kennt keinen User"},
	}
	for _, c := range cases {
		write(c.content)
		runT(t, "config", "import", "--config", cfgB, file).want(t, 1, c.want, "Nichts geschrieben")
		if after := takeSnapshot(t, b, cfgB); !reflect.DeepEqual(after, before) {
			t.Errorf("%s: verändert", c.name)
		}
	}

	// Format 4: der User ist der Name des Accounts.
	write(toFormat4(t, data))
	runT(t, "config", "import", "--config", cfgB, file).want(t, 0, "2 Accounts")
	got := hubTables(t, cfgB).Accounts
	if len(got) != 2 || got[0].Name != "alice" || got[0].User != "alice" || got[1].Name != "bob" || got[1].User != "bob" {
		t.Errorf("Accounts nach Format 4: %+v", got)
	}
	// Format 7: der User kommt mit, auch in die Zeilen.
	runT(t, "config", "import", "--config", cfgB, exp).want(t, 0, "2 Accounts")
	if got, want := hubTables(t, cfgB).Accounts, hubTables(t, cfgA).Accounts; !reflect.DeepEqual(got, want) || got[1].User != "kleist" {
		t.Errorf("Accounts nach Format 7:\n%+v\nerwartet\n%+v", got, want)
	}
	var content string
	if err := rawHub(t, b).QueryRow(`SELECT content FROM documents WHERE name = 'SYSTEM:A:alice' AND deleted = 0`).Scan(&content); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content, `"user":"alice"`) {
		t.Errorf("Zeile von alice: %s", content)
	}
}
