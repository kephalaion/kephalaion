package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kephalaion/kephalaion/internal/config"
)

// writeSystemConfig legt eine globale config an, deren Datenbanken unter
// dataDir liegen, und liefert ihren Ort.
func writeSystemConfig(t *testing.T, dataDir string) string {
	t.Helper()
	cfg := config.Config{
		Hub:  &config.Section{DB: "sqlite://" + filepath.Join(dataDir, "hub.db"), Listen: config.DefaultListenHub},
		Node: &config.Section{DB: "sqlite://" + filepath.Join(dataDir, "node.db"), Listen: config.DefaultListenNode},
	}
	if err := config.Save(config.SystemPath, cfg); err != nil {
		t.Fatal(err)
	}
	return config.SystemPath
}

// init ohne --config und ohne KEPHALAION_CONFIG richtet nur pro User ein: Gibt
// es die globale config, bricht es ab und nennt den Weg als Systembenutzer.
// Mit ausdrücklichem Ort bleibt init, wie es ist.
func TestInitRefusesBesideSystemInstallation(t *testing.T) {
	dir := isolate(t)
	system := writeSystemConfig(t, filepath.Join(dir, "var"))
	before, err := os.ReadFile(system)
	if err != nil {
		t.Fatal(err)
	}

	r := runT(t, "node", "init")
	r.want(t, 1, "global eingerichtet ("+system+")",
		"sudo -u kephalaion kephalaion node init --config "+system+" --db sqlite:///var/lib/kephalaion/node.db")
	userCfg := filepath.Join(dir, "config", "kephalaion", "config.yaml")
	if exists(userCfg) || exists(filepath.Join(dir, "data", "kephalaion", "node.db")) {
		t.Fatal("init hat trotz globaler config pro User angelegt")
	}
	if after, _ := os.ReadFile(system); string(after) != string(before) {
		t.Fatalf("globale config verändert:\n%s", after)
	}

	// Auch neben einer config des Users: die globale gibt es, also nein.
	if err := config.Save(userCfg, config.Config{}); err != nil {
		t.Fatal(err)
	}
	runT(t, "hub", "init").want(t, 1, "global eingerichtet")

	// Ausdrücklich per --config oder KEPHALAION_CONFIG: wie bisher.
	own := filepath.Join(dir, "eigen.yaml")
	runT(t, "node", "init", "--config", own, "--db", "sqlite://"+filepath.Join(dir, "eigen", "node.db")).
		want(t, 0, "Node eingerichtet.", "Dienst einrichten: kephalaion service install --config "+own)
	t.Setenv(config.EnvConfig, filepath.Join(dir, "env.yaml"))
	runT(t, "hub", "init", "--db", "sqlite://"+filepath.Join(dir, "env", "hub.db")).
		want(t, 0, "Hub eingerichtet.", "Dienst einrichten: kephalaion service install\n")
}

// init nennt am Ende den Dienst als nächsten Schritt; bei der globalen config
// die System-Unit.
func TestInitNextStep(t *testing.T) {
	dir := isolate(t)
	runT(t, "node", "init").want(t, 0, "Nächster Schritt: Dienst einrichten: kephalaion service install\n")

	data := filepath.Join(dir, "var")
	runT(t, "hub", "init", "--config", config.SystemPath, "--db", "sqlite://"+filepath.Join(data, "hub.db")).
		want(t, 0, "Nächster Schritt: System-Unit ablegen: kephalaion service unit --system")
}

// status nennt Ort und Quelle der config für jede Stufe der Suche.
func TestStatusConfigSource(t *testing.T) {
	dir := isolate(t)
	user := filepath.Join(dir, "config", "kephalaion", "config.yaml")
	runT(t, "status").want(t, 0, "config: "+user+" (fehlt)\nQuelle: pro User\n")

	system := config.SystemPath
	if err := config.Save(system, config.Config{}); err != nil {
		t.Fatal(err)
	}
	runT(t, "status").want(t, 0, "config: "+system+" (vorhanden)\nQuelle: global (Installation für alle User")

	flagPath := filepath.Join(dir, "flag.yaml")
	runT(t, "status", "--config", flagPath).want(t, 0, "config: "+flagPath+" (fehlt)\nQuelle: --config\n")
	t.Setenv(config.EnvConfig, system)
	runT(t, "status").want(t, 0, "Quelle: KEPHALAION_CONFIG, die globale config\n")
	runT(t, "config", "show").want(t, 0, "Quelle: KEPHALAION_CONFIG, die globale config\n")
}

// Gibt es die config des Users und die globale, meldet status zwei Arten auf
// einem Rechner als Fehler, mit dem Weg; die Suche bleibt dabei: Es gilt die
// des Users.
func TestStatusBothKinds(t *testing.T) {
	dir := isolate(t)
	writeSystemConfig(t, filepath.Join(dir, "var"))
	runT(t, "node", "init", "--config", filepath.Join(dir, "config", "kephalaion", "config.yaml")).want(t, 0)
	user := filepath.Join(dir, "config", "kephalaion", "config.yaml")

	r := runT(t, "status")
	r.want(t, 1, "config: "+user+" (vorhanden)\nQuelle: pro User\n",
		"Fehler: zwei Arten der Installation auf diesem Rechner", user, config.SystemPath,
		"Installation pro User entfernen", "node: eingerichtet", "Schemafassung")
	// Ohne die globale ist alles gut.
	if err := os.Remove(config.SystemPath); err != nil {
		t.Fatal(err)
	}
	r = runT(t, "status")
	r.want(t, 0)
	if strings.Contains(r.out, "zwei Arten") {
		t.Fatalf("zwei Arten ohne globale config:\n%s", r.out)
	}
}

// Gilt die globale config und darf der Aufrufer die Datenbanken nicht lesen,
// nennt status die globale Installation und den Weg, Exit 0.
func TestStatusSystemUnreadable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("als root greifen Dateirechte nicht")
	}
	dir := isolate(t)
	data := filepath.Join(dir, "var", "lib", "kephalaion")
	writeSystemConfig(t, data)
	// Die Datenbanken anlegen wie der Systembenutzer: init mit eigener config,
	// danach die globale darauf zeigen lassen.
	tmpCfg := filepath.Join(dir, "admin.yaml")
	runT(t, "hub", "init", "--config", tmpCfg, "--db", "sqlite://"+filepath.Join(data, "hub.db")).want(t, 0)
	runT(t, "node", "init", "--config", tmpCfg, "--db", "sqlite://"+filepath.Join(data, "node.db")).want(t, 0)
	// Lesbar: status prüft wie immer.
	runT(t, "status").want(t, 0, "Quelle: global", "hub_id:", "Hubs:          keine")

	if err := os.Chmod(data, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(data, 0o700) })
	r := runT(t, "status")
	// Am Hub bleibt „serve: nicht prüfbar“; am Node steht statt dessen die
	// Probe (Task 035, statusnode_test.go).
	r.want(t, 0, "Quelle: global", "hub: eingerichtet", "node: eingerichtet",
		"serve:         nicht prüfbar", "Antwort:", "Anmeldung:",
		"globale Installation; die Datenbank gehört dem Systembenutzer kephalaion",
		"sudo -u kephalaion kephalaion status")
	if strings.Contains(r.out, "Fehler") {
		t.Fatalf("Fehler statt Hinweis:\n%s", r.out)
	}

	// Eine config pro User, deren Datenbank nicht lesbar ist, bleibt ein
	// Fehler.
	t.Setenv(config.EnvConfig, tmpCfg)
	runT(t, "status").want(t, 1, "Fehler:")
}
