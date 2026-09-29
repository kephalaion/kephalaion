package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/kephalaion/kephalaion/internal/config"
)

// setup richtet Hub und Node unter dir ein und liefert den Pfad der config.
func setup(t *testing.T, dir string) string {
	t.Helper()
	cfg := filepath.Join(dir, "config.yaml")
	runT(t, "hub", "init", "--db", "sqlite://"+filepath.Join(dir, "hub.db"), "--config", cfg).want(t, 0)
	runT(t, "node", "init", "--db", "sqlite://"+filepath.Join(dir, "node.db"), "--config", cfg).want(t, 0)
	return cfg
}

func setSettings(t *testing.T, cfgPath string, r config.Role, settings map[string]string) {
	t.Helper()
	cfg, _, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	s, err := openSection(context.Background(), r, cfg.Section(r))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.ReplaceSettings(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
}

func getSettings(t *testing.T, cfgPath string, r config.Role) map[string]string {
	t.Helper()
	cfg, _, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	got, err := readSettings(context.Background(), r, cfg.Section(r))
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestConfigShow(t *testing.T) {
	dir := isolate(t)
	cfg := setup(t, filepath.Join(dir, "a"))
	setSettings(t, cfg, config.Hub, map[string]string{"gruss": ":8443"})
	runT(t, "config", "show", "--config", cfg).want(t, 0,
		"config: "+cfg+" (vorhanden)", "  hub:", "sqlite://", "settings hub:", "gruss = :8443",
		"settings node:", "(keine)")

	// Fehlt die Datenbank einer Rolle, zeigt show die config trotzdem.
	if err := os.Remove(filepath.Join(dir, "a", "node.db")); err != nil {
		t.Fatal(err)
	}
	runT(t, "config", "show", "--config", cfg).want(t, 1,
		"  node:", "gruss = :8443", "Fehler:", "Datenbankdatei fehlt")
	if _, err := os.Stat(filepath.Join(dir, "a", "node.db")); err == nil {
		t.Fatal("config show hat die Datenbankdatei angelegt")
	}
}

func TestConfigExportImportRoundTrip(t *testing.T) {
	dir := isolate(t)
	cfgA := setup(t, filepath.Join(dir, "a"))
	hubSettings := map[string]string{"gruss": ":8443", "name": "zentrale"}
	nodeSettings := map[string]string{"gruss": "127.0.0.1:7070"}
	setSettings(t, cfgA, config.Hub, hubSettings)
	setSettings(t, cfgA, config.Node, nodeSettings)

	exp := filepath.Join(dir, "export.yaml")
	runT(t, "config", "export", "--config", cfgA, "--output", exp).want(t, 0, "Exportiert nach "+exp)
	fi, err := os.Stat(exp)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("Exportdatei %v, erwartet 0600", fi.Mode().Perm())
	}
	data, _ := os.ReadFile(exp)
	for _, want := range []string{"format: 7", "config:", "settings:", "tables:", "zentrale"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("Export ohne %q:\n%s", want, data)
		}
	}

	// Ohne --output auf die Standardausgabe, derselbe Inhalt.
	r := runT(t, "config", "export", "--config", cfgA)
	r.want(t, 0)
	if r.out != string(data) {
		t.Errorf("Standardausgabe weicht ab:\n%s\n---\n%s", r.out, data)
	}

	// Neue Datenbanken, dann Import.
	cfgB := setup(t, filepath.Join(dir, "b"))
	setSettings(t, cfgB, config.Hub, map[string]string{"alt": "weg"})
	cfgBefore, _ := os.ReadFile(cfgB)
	runT(t, "config", "import", "--config", cfgB, exp).want(t, 0,
		"hub: settings ersetzt (2 Einträge)", "node: settings ersetzt (1 Einträge)")
	if got := getSettings(t, cfgB, config.Hub); !reflect.DeepEqual(got, hubSettings) {
		t.Errorf("hub settings = %v, erwartet %v", got, hubSettings)
	}
	if got := getSettings(t, cfgB, config.Node); !reflect.DeepEqual(got, nodeSettings) {
		t.Errorf("node settings = %v, erwartet %v", got, nodeSettings)
	}
	cfgAfter, _ := os.ReadFile(cfgB)
	if string(cfgBefore) != string(cfgAfter) {
		t.Errorf("import hat die config verändert")
	}
}

func TestConfigImportUnknownFormat(t *testing.T) {
	dir := isolate(t)
	cfg := setup(t, dir)
	setSettings(t, cfg, config.Hub, map[string]string{"bleibt": "ja"})
	for _, content := range []string{
		"format: 8\nconfig: {}\nneu: 1\n",
		"config: {}\nsettings: {}\n",
	} {
		exp := filepath.Join(dir, "export.yaml")
		if err := os.WriteFile(exp, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		runT(t, "config", "import", "--config", cfg, exp).want(t, 1, "Fassung des Formats")
	}
	if got := getSettings(t, cfg, config.Hub); got["bleibt"] != "ja" || len(got) != 1 {
		t.Errorf("settings verändert: %v", got)
	}
}

func TestConfigImportMissingRoleWritesNothing(t *testing.T) {
	dir := isolate(t)
	cfgA := setup(t, filepath.Join(dir, "a"))
	setSettings(t, cfgA, config.Hub, map[string]string{"neu": "1"})
	setSettings(t, cfgA, config.Node, map[string]string{"neu": "2"})
	exp := filepath.Join(dir, "export.yaml")
	runT(t, "config", "export", "--config", cfgA, "--output", exp).want(t, 0)

	// Nur der Hub ist eingerichtet.
	cfgB := filepath.Join(dir, "b", "config.yaml")
	runT(t, "hub", "init", "--db", "sqlite://"+filepath.Join(dir, "b", "hub.db"), "--config", cfgB).want(t, 0)
	setSettings(t, cfgB, config.Hub, map[string]string{"alt": "bleibt"})

	nodeDB := "sqlite://" + filepath.Join(dir, "a", "node.db")
	runT(t, "config", "import", "--config", cfgB, exp).want(t, 1,
		"die Rolle node ist nicht eingerichtet", "kephalaion node init --db "+nodeDB, "Nichts geschrieben")
	if got := getSettings(t, cfgB, config.Hub); !reflect.DeepEqual(got, map[string]string{"alt": "bleibt"}) {
		t.Errorf("hub settings verändert: %v", got)
	}
}

func TestConfigImportMissingDBWritesNothing(t *testing.T) {
	dir := isolate(t)
	cfg := setup(t, dir)
	setSettings(t, cfg, config.Hub, map[string]string{"alt": "bleibt"})
	exp := filepath.Join(dir, "export.yaml")
	runT(t, "config", "export", "--config", cfg, "--output", exp).want(t, 0)
	setSettings(t, cfg, config.Hub, map[string]string{"alt": "bleibt", "zwei": "2"})
	if err := os.Remove(filepath.Join(dir, "node.db")); err != nil {
		t.Fatal(err)
	}
	runT(t, "config", "import", "--config", cfg, exp).want(t, 1, "Rolle node", "Nichts geschrieben")
	if got := getSettings(t, cfg, config.Hub); len(got) != 2 {
		t.Errorf("hub settings verändert: %v", got)
	}
}

func TestConfigUsage(t *testing.T) {
	isolate(t)
	runT(t, "config").want(t, 2, "kephalaion config show")
	runT(t, "config", "import").want(t, 2, "Es fehlt die Exportdatei")
	runT(t, "config", "gibtsnicht").want(t, 2, "Unbekanntes Kommando: config gibtsnicht")
	runT(t, "config", "export", "--help").want(t, 0, "0600")
}
