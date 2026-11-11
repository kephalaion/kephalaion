package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/kephalaion/kephalaion/internal/config"
	hubstore "github.com/kephalaion/kephalaion/internal/hub/store"
	nodestore "github.com/kephalaion/kephalaion/internal/node/store"
	"github.com/kephalaion/kephalaion/internal/sqlitedb"
)

// runPreflight ruft upgrade --preflight --json auf und liest die Antwort.
func runPreflight(t *testing.T) preflightReport {
	t.Helper()
	r := runT(t, "upgrade", "--preflight", "--json")
	r.want(t, 0)
	var rep preflightReport
	if err := json.Unmarshal([]byte(r.out), &rep); err != nil {
		t.Fatalf("%v:\n%s", err, r.out)
	}
	return rep
}

// setSchemaVersion trägt in die Datenbank unter dbPath eine andere
// Schemafassung ein.
func setSchemaVersion(t *testing.T, dbPath string, v int) {
	t.Helper()
	ctx := context.Background()
	db, err := sqlitedb.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := sqlitedb.SetInfo(ctx, db, sqlitedb.KeySchemaVersion, strconv.Itoa(v)); err != nil {
		t.Fatal(err)
	}
}

// fileState ist Inhalt und Änderungszeit einer Datei: So prüft ein Test, dass
// die Probe nichts geändert hat.
func fileState(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.ModTime().String() + "\n" + string(b)
}

// Ohne config gibt es keine Rolle: ok.
func TestPreflightWithoutConfig(t *testing.T) {
	dir := isolate(t)
	rep := runPreflight(t)
	if rep.Result != preflightOK || rep.Reason != "" || len(rep.Roles) != 0 ||
		rep.Config != filepath.Join(dir, "config", "kephalaion", "config.yaml") || rep.Version == "" {
		t.Errorf("Probe ohne config: %+v", rep)
	}
	if exists(rep.Config) {
		t.Error("die Probe hat eine config angelegt")
	}
}

// Nur Node, dann Node und Hub, je mit passender Fassung: ok, je Rolle ok —
// auch neben einem laufenden serve, das die Sperre hält. Die Datenbanken
// bleiben unverändert.
func TestPreflightMatchingSchema(t *testing.T) {
	dir := isolate(t)
	runT(t, "node", "init").want(t, 0)
	nodeDB := filepath.Join(dir, "data", "kephalaion", "node.db")
	before := fileState(t, nodeDB)

	rep := runPreflight(t)
	if rep.Result != preflightOK || len(rep.Roles) != 1 || rep.Roles["node"].State != roleCheckOK {
		t.Errorf("nur Node: %+v", rep)
	}

	runT(t, "hub", "init").want(t, 0)
	hubDB := filepath.Join(dir, "data", "kephalaion", "hub.db")
	hubBefore := fileState(t, hubDB)
	// serve hält die Sperre beider Rollen.
	for _, db := range []string{nodeDB, hubDB} {
		lock, err := takeLock(lockPath(db))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = lock.Close() })
	}
	rep = runPreflight(t)
	if rep.Result != preflightOK || rep.Reason != "" || rep.Hint != "" || len(rep.Roles) != 2 ||
		rep.Roles["node"].State != roleCheckOK || rep.Roles["hub"].State != roleCheckOK {
		t.Errorf("Node und Hub neben serve: %+v", rep)
	}
	if fileState(t, nodeDB) != before || fileState(t, hubDB) != hubBefore {
		t.Error("die Probe hat eine Datenbank geändert")
	}
}

// Eine zu alte Fassung (SchemaVersionError): veto, schema, mit want/got je
// Rolle und dem Hinweis auf den Weg von Hand.
func TestPreflightSchemaVeto(t *testing.T) {
	dir := isolate(t)
	runT(t, "hub", "init").want(t, 0)
	runT(t, "node", "init").want(t, 0)
	nodeDB := filepath.Join(dir, "data", "kephalaion", "node.db")
	setSchemaVersion(t, nodeDB, nodestore.SchemaVersion-1)
	before := fileState(t, nodeDB)

	rep := runPreflight(t)
	node := rep.Roles["node"]
	if rep.Result != preflightVeto || rep.Reason != vetoSchema || node.State != roleCheckSchema ||
		node.Want != strconv.Itoa(nodestore.SchemaVersion) || node.Got != strconv.Itoa(nodestore.SchemaVersion-1) ||
		rep.Roles["hub"].State != roleCheckOK {
		t.Errorf("zu alte Fassung: %+v", rep)
	}
	if !contains(rep.Hint, "Neue Schemafassung", "docs/installation.md") {
		t.Errorf("Hinweis: %s", rep.Hint)
	}
	if fileState(t, nodeDB) != before {
		t.Error("die Probe hat die Datenbank geändert")
	}

	// Auch der Hub: schema schlägt jeden anderen Grund.
	setSchemaVersion(t, filepath.Join(dir, "data", "kephalaion", "hub.db"), hubstore.SchemaVersion-1)
	if err := os.Remove(nodeDB); err != nil {
		t.Fatal(err)
	}
	rep = runPreflight(t)
	if rep.Result != preflightVeto || rep.Reason != vetoSchema || rep.Roles["hub"].State != roleCheckSchema ||
		rep.Roles["node"].State != roleCheckError {
		t.Errorf("Hub zu alt, Node fehlt: %+v", rep)
	}
}

// Eine fehlende Datenbank ist ein Veto mit eigenem Grund, keines wegen der
// Schemafassung; die Probe legt nichts an.
func TestPreflightMissingDatabase(t *testing.T) {
	dir := isolate(t)
	runT(t, "node", "init").want(t, 0)
	nodeDB := filepath.Join(dir, "data", "kephalaion", "node.db")
	if err := sqlitedb.Remove(nodeDB); err != nil {
		t.Fatal(err)
	}
	rep := runPreflight(t)
	if rep.Result != preflightVeto || rep.Reason != vetoDatabase || rep.Roles["node"].State != roleCheckError ||
		!contains(rep.Roles["node"].Error, nodeDB) || !contains(rep.Hint, "kephalaion status") {
		t.Errorf("fehlende Datenbank: %+v", rep)
	}
	if exists(nodeDB) {
		t.Error("die Probe hat die Datenbank angelegt")
	}

	// Eine config, die dieses Binary nicht lesen kann: veto, config.
	if err := os.WriteFile(filepath.Join(dir, "config", "kephalaion", "config.yaml"), []byte("neu: ja\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rep = runPreflight(t)
	if rep.Result != preflightVeto || rep.Reason != vetoConfig || len(rep.Roles) != 0 {
		t.Errorf("unlesbare config: %+v", rep)
	}
}

// Die Probe nimmt die config aus KEPHALAION_CONFIG — so ruft upgrade --auto
// sie mit derselben config auf.
func TestPreflightConfigFromEnv(t *testing.T) {
	dir := isolate(t)
	cfg := setup(t, dir)
	t.Setenv(config.EnvConfig, cfg)
	rep := runPreflight(t)
	if rep.Result != preflightOK || rep.Config != cfg || len(rep.Roles) != 2 {
		t.Errorf("config aus der Umgebung: %+v", rep)
	}
}

// --preflight gibt es nur mit --json und ohne --check und --version.
func TestPreflightUsage(t *testing.T) {
	isolate(t)
	for _, args := range [][]string{
		{"--preflight"},
		{"--preflight", "--json", "--check"},
		{"--preflight", "--json", "--version", "v0.2.0"},
	} {
		runT(t, append([]string{"upgrade"}, args...)...).want(t, 2, "--preflight gibt es nur mit --json")
	}
}
