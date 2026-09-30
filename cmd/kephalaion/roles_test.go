package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/kephalaion/kephalaion/internal/config"
	hubstore "github.com/kephalaion/kephalaion/internal/hub/store"
)

// isolate lenkt HOME und die XDG-Verzeichnisse in ein temporäres Verzeichnis,
// damit kein Test die echte config oder Datenbank berührt.
func isolate(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
	t.Setenv("KEPHALAION_CONFIG", "")
	old := config.SystemPath
	config.SystemPath = filepath.Join(dir, "etc", "kephalaion", "config.yaml")
	t.Cleanup(func() { config.SystemPath = old })
	return dir
}

type result struct {
	code        int
	out, errOut string
}

func runT(t *testing.T, args ...string) result {
	t.Helper()
	return runIn(t, "", args...)
}

// runIn ruft run mit stdin auf.
func runIn(t *testing.T, stdin string, args ...string) result {
	t.Helper()
	var out, errOut bytes.Buffer
	code := run(args, strings.NewReader(stdin), &out, &errOut)
	return result{code, out.String(), errOut.String()}
}

func (r result) want(t *testing.T, code int, contains ...string) {
	t.Helper()
	if r.code != code {
		t.Fatalf("Exit-Code %d, erwartet %d\nstdout:\n%s\nstderr:\n%s", r.code, code, r.out, r.errOut)
	}
	all := r.out + r.errOut
	for _, c := range contains {
		if !strings.Contains(all, c) {
			t.Fatalf("Ausgabe ohne %q\nstdout:\n%s\nstderr:\n%s", c, r.out, r.errOut)
		}
	}
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return !errors.Is(err, os.ErrNotExist)
}

func TestStatusNothing(t *testing.T) {
	dir := isolate(t)
	cfg := filepath.Join(dir, "config", "kephalaion", "config.yaml")
	runT(t, "status").want(t, 0, "config: "+cfg+" (fehlt)", "hub: nicht eingerichtet",
		"node: nicht eingerichtet", "Keine Rolle eingerichtet", "kephalaion hub init", "kephalaion node init")
	if exists(cfg) {
		t.Fatal("status hat eine config angelegt")
	}
}

func TestInitHubOnly(t *testing.T) {
	dir := isolate(t)
	cfg := filepath.Join(dir, "k.yaml")
	db := filepath.Join(dir, "sub", "hub.db")
	runT(t, "hub", "init", "--db", "sqlite://"+db, "--config", cfg).
		want(t, 0, "Hub eingerichtet.", db, cfg)
	runT(t, "status", "--config", cfg).want(t, 0,
		"config: "+cfg+" (vorhanden)",
		"hub: eingerichtet", "sqlite://"+db, "Schemafassung: "+strconv.Itoa(hubstore.SchemaVersion), "Revision:      0",
		"hub_id:        ", "Dokumente:     0", "Collections:   keine", "Nodes:         keine", "node: nicht eingerichtet")
}

func TestInitBoth(t *testing.T) {
	dir := isolate(t)
	runT(t, "hub", "init").want(t, 0, "Hub eingerichtet.")
	runT(t, "node", "init").want(t, 0, "Node eingerichtet.")

	dataDir := filepath.Join(dir, "data", "kephalaion")
	fi, err := os.Stat(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o700 {
		t.Errorf("Datenverzeichnis %v, erwartet 0700", fi.Mode().Perm())
	}
	for _, f := range []string{"hub.db", "node.db"} {
		if !exists(filepath.Join(dataDir, f)) {
			t.Errorf("%s fehlt", f)
		}
	}
	r := runT(t, "status")
	r.want(t, 0, "hub: eingerichtet", "node: eingerichtet", "Hubs:          keine",
		"sqlite://"+filepath.Join(dataDir, "node.db"))
	if strings.Contains(r.out, "Keine Rolle") {
		t.Fatal("Hinweis auf init, obwohl eingerichtet")
	}
}

func TestInitListen(t *testing.T) {
	dir := isolate(t)
	cfg := filepath.Join(dir, "k.yaml")
	c := "--config=" + cfg
	runT(t, "hub", "init", c, "--listen", "0.0.0.0:9000").want(t, 0, "listen:    0.0.0.0:9000")
	runT(t, "node", "init", c).want(t, 0, "listen:    127.0.0.1:7433")
	data, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"listen: 0.0.0.0:9000", "listen: 127.0.0.1:7433"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("config ohne %q:\n%s", want, data)
		}
	}
	runT(t, "status", c).want(t, 0, "listen:        0.0.0.0:9000", "listen:        127.0.0.1:7433")

	// Ungültiges --listen: nichts angelegt.
	cfg2 := filepath.Join(dir, "k2.yaml")
	db2 := filepath.Join(dir, "hub2.db")
	runT(t, "hub", "init", "--config", cfg2, "--db", "sqlite://"+db2, "--listen", ":7434").want(t, 1, "host:port")
	if exists(cfg2) || exists(db2) {
		t.Fatal("init trotz ungültigem --listen angelegt")
	}

	// Fehlt listen in einer bestehenden config, gilt der Standard.
	cfg3 := filepath.Join(dir, "k3.yaml")
	db3 := filepath.Join(dir, "hub3.db")
	runT(t, "hub", "init", "--config", cfg3, "--db", "sqlite://"+db3).want(t, 0, "listen:    127.0.0.1:7434",
		"Adresse:   http://127.0.0.1:7434/hub (für Nodes dieses Rechners")
	if err := os.WriteFile(cfg3, []byte("hub:\n  db: sqlite://"+db3+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runT(t, "status", "--config", cfg3).want(t, 0, "listen:        127.0.0.1:7434 (Standard, nicht in der config)",
		"Adresse:       http://127.0.0.1:7434/hub (für Nodes dieses Rechners, Transport http)")
}

func TestInitTwiceFails(t *testing.T) {
	dir := isolate(t)
	cfg := filepath.Join(dir, "k.yaml")
	db := filepath.Join(dir, "hub.db")
	runT(t, "hub", "init", "--db", "sqlite://"+db, "--config", cfg).want(t, 0)
	before, _ := os.ReadFile(cfg)

	// Die Rolle steht schon in der config.
	runT(t, "hub", "init", "--db", "sqlite://"+filepath.Join(dir, "anders.db"), "--config", cfg).
		want(t, 1, "schon eingerichtet")
	if exists(filepath.Join(dir, "anders.db")) {
		t.Fatal("zweites init hat eine Datenbank angelegt")
	}
	after, _ := os.ReadFile(cfg)
	if !bytes.Equal(before, after) {
		t.Fatalf("config verändert:\n%s\n→\n%s", before, after)
	}

	// Die Datenbankdatei gibt es schon (andere config).
	cfg2 := filepath.Join(dir, "k2.yaml")
	runT(t, "hub", "init", "--db", "sqlite://"+db, "--config", cfg2).
		want(t, 1, "existiert schon")
	if exists(cfg2) {
		t.Fatal("config trotz Abbruch geschrieben")
	}
	runT(t, "status", "--config", cfg).want(t, 0, "hub: eingerichtet")
}

func TestInitConfigNotWritableLeavesNoDB(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("als root greifen die Dateirechte nicht")
	}
	dir := isolate(t)
	cfgDir := filepath.Join(dir, "ro")
	if err := os.Mkdir(cfgDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(cfgDir, 0o700) })
	dbDir := filepath.Join(dir, "db")
	db := filepath.Join(dbDir, "hub.db")
	runT(t, "hub", "init", "--db", "sqlite://"+db, "--config", filepath.Join(cfgDir, "config.yaml")).
		want(t, 1, "wieder entfernt")
	entries, _ := os.ReadDir(dbDir)
	if len(entries) != 0 {
		t.Fatalf("liegen geblieben: %v", entries)
	}
}

func TestStatusMissingDBCreatesNothing(t *testing.T) {
	dir := isolate(t)
	cfg := filepath.Join(dir, "k.yaml")
	db := filepath.Join(dir, "hub.db")
	runT(t, "hub", "init", "--db", "sqlite://"+db, "--config", cfg).want(t, 0)
	runT(t, "node", "init", "--db", "sqlite://"+filepath.Join(dir, "node.db"), "--config", cfg).want(t, 0)
	for _, p := range []string{db, db + "-wal", db + "-shm"} {
		_ = os.Remove(p)
	}
	runT(t, "status", "--config", cfg).want(t, 1, "hub: eingerichtet", "Fehler:", "Datenbankdatei fehlt",
		"node: eingerichtet", "Hubs:          keine")
	if exists(db) {
		t.Fatal("status hat die Datenbankdatei angelegt")
	}
}

func TestStatusWrongRole(t *testing.T) {
	dir := isolate(t)
	cfg := filepath.Join(dir, "k.yaml")
	node := filepath.Join(dir, "node.db")
	runT(t, "node", "init", "--db", "sqlite://"+node, "--config", cfg).want(t, 0)
	// Die Hub-Rolle zeigt von Hand auf die Datenbank des Nodes.
	data := "hub:\n  db: sqlite://" + node + "\nnode:\n  db: sqlite://" + node + "\n"
	if err := os.WriteFile(cfg, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	runT(t, "status", "--config", cfg).want(t, 1, "gehört zur Rolle node")
}

func TestInitRejectsPostgres(t *testing.T) {
	dir := isolate(t)
	cfg := filepath.Join(dir, "k.yaml")
	runT(t, "hub", "init", "--db", "postgres://keph@db/kephalaion", "--config", cfg).
		want(t, 1, "noch nicht unterstützt")
	if exists(cfg) {
		t.Fatal("config trotz Abbruch geschrieben")
	}
}

func TestRoleUsage(t *testing.T) {
	isolate(t)
	runT(t, "hub").want(t, 2, "kephalaion hub init")
	runT(t, "node", "help").want(t, 0, "kephalaion node init")
	runT(t, "hub", "init", "--help").want(t, 0, "--db adresse")
	runT(t, "hub", "gibtsnicht").want(t, 2, "Unbekanntes Kommando: hub gibtsnicht")
	runT(t, "status", "zuviel").want(t, 2, "Unerwartetes Argument: zuviel")
}
