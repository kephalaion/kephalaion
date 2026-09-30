package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/kephalaion/kephalaion/internal/contract"
	hubstore "github.com/kephalaion/kephalaion/internal/hub/store"
)

// hub account grant --dir: wiederholbar, ein '/' am Ende erlaubt, geprüft, in
// show und list als „dir <pfad>/“ sichtbar, ohne --dir entzogen; ein
// gesperrter Account merkt den Scope.
func TestHubAccountGrantDir(t *testing.T) {
	dir := isolate(t)
	cfg := setup(t, dir)
	c := "--config=" + cfg
	runT(t, "hub", "collection", "add", "team-x", c).want(t, 0)
	runT(t, "hub", "account", "add", "kp", c).want(t, 0)

	runT(t, "hub", "account", "grant", "kp", "team-x", "--dir", "test-docs", c).want(t, 0,
		"team-x erlaubt (read, dir test-docs/)")
	runT(t, "hub", "account", "grant", "kp", "team-x", "--dir", "test-docs/", c).want(t, 0, "unverändert")
	runT(t, "hub", "account", "grant", "kp", "team-x", "--write", "--vendor", "test-docs", "--dir", "test-docs",
		"--dir", "a/b", c).want(t, 0, "team-x erlaubt (read, write, vendor/test-docs, dir a/b/, dir test-docs/)")
	for _, bad := range []struct{ dir, want string }{
		{"", "Verzeichnis-Scope: Verzeichnis fehlt"},
		{"/", "die Wurzel einer Collection ist kein Scope"},
		{"vendor", "Verzeichnis-Scope \"vendor\": unter vendor/ gilt allein der Scope vendor/<name> (--vendor)"},
		{"vendor/x", "unter vendor/ gilt allein"},
		{"..", "'.' und '..'"},
		{"a//", "endet mit '/'"},
	} {
		runT(t, "hub", "account", "grant", "kp", "team-x", "--dir", bad.dir, c).want(t, 1, bad.want)
	}
	runT(t, "hub", "account", "show", "kp", c).want(t, 0, "team-x: read, write, vendor/test-docs, dir a/b/, dir test-docs/")
	runT(t, "hub", "account", "list", c).want(t, 0, "team-x (read, write, vendor/test-docs, dir a/b/, dir test-docs/)")
	got := hubTables(t, cfg).Accounts
	if len(got) != 1 || !reflect.DeepEqual(got[0].Rights, []hubstore.AccountRight{{Collection: "team-x",
		Rights: contract.Rights{Write: true, Vendor: []string{"test-docs"}, Dirs: []string{"a/b", "test-docs"}}}}) {
		t.Errorf("Rechte: %+v", got)
	}
	// Ohne --dir ist jeder Verzeichnis-Scope entzogen.
	runT(t, "hub", "account", "grant", "kp", "team-x", "--write", c).want(t, 0, "team-x erlaubt (read, write)")
	runT(t, "hub", "account", "grant", "kp", "team-x", "--dir", "test-docs", c).want(t, 0, "team-x erlaubt (read, dir test-docs/)")
	runT(t, "hub", "account", "lock", "kp", c).want(t, 0)
	runT(t, "hub", "account", "show", "kp", c).want(t, 0, "gemerkt", "team-x: read, dir test-docs/")
	runT(t, "hub", "account", "unlock", "kp", c).want(t, 0)
	runT(t, "hub", "account", "show", "kp", c).want(t, 0, "Rechte:\n    team-x: read, dir test-docs/")
	runT(t, "hub", "account", "--help").want(t, 0, "--dir pfad", "Verzeichnis-Scope")
}

// Export ab Format 8 trägt die Verzeichnis-Scopes; der Import nimmt sie mit
// und prüft sie. Ein Export im Format 7 liest sich ohne; trägt er welche,
// bricht der Import ab.
func TestExportImportDirs(t *testing.T) {
	dir := isolate(t)
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	cfgA := setup(t, a)
	fill(t, cfgA)
	cA := "--config=" + cfgA
	runT(t, "hub", "account", "add", "kp", cA).want(t, 0)
	runT(t, "hub", "account", "grant", "kp", "team-x", "--dir", "docs", "--dir", "a/b", cA).want(t, 0)
	// bob ist gesperrt: die gemerkten Rechte tragen den Scope.
	runT(t, "hub", "account", "grant", "bob", "privat", "--supersede", "--dir", "zwei", cA).want(t, 0)
	exp := filepath.Join(dir, "export.yaml")
	exportTo(t, cfgA, exp)
	raw, _ := os.ReadFile(exp)
	data := string(raw)
	const kpDirs = "\n            dirs:\n              - a/b\n              - docs"
	for _, want := range []string{"format: 8", "dirs: []", kpDirs, "dirs:\n              - zwei"} {
		if !strings.Contains(data, want) {
			t.Errorf("Export ohne %q:\n%s", want, data)
		}
	}

	cfgB := setup(t, b)
	runT(t, "hub", "collection", "add", "team-x", "--config", cfgB).want(t, 0)
	runT(t, "hub", "account", "add", "carol", "--config", cfgB).want(t, 0)
	runT(t, "hub", "account", "grant", "carol", "team-x", "--dir", "alt", "--config", cfgB).want(t, 0)
	before := takeSnapshot(t, b, cfgB)
	file := filepath.Join(dir, "import.yaml")
	write := func(content string) {
		if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	format7 := toFormat7(t, data)
	for _, c := range []struct{ name, content, want string }{
		{"F7 mit dirs", strings.Replace(data, "format: 8", "format: 7", 1),
			"Format 7 kennt keine Verzeichnis-Scopes (tables.hub.accounts[0].rights[0].dirs); ab Format 8"},
		{"F7 mit dirs leer", strings.Replace(format7, "supersede: false", "supersede: false\n            dirs: []", 1),
			"Format 7 kennt keine Verzeichnis-Scopes"},
		{"F7 mit dirs null", strings.Replace(format7, "supersede: false", "supersede: false\n            dirs:", 1),
			"Format 7 kennt keine Verzeichnis-Scopes"},
		{"F6 mit dirs", strings.Replace(data, "format: 8", "format: 6", 1), "Format 6 kennt keine Verzeichnis-Scopes"},
		{"vendor", strings.Replace(data, kpDirs, "\n            dirs:\n              - vendor/x", 1),
			"Account kp in team-x: Verzeichnis-Scope \"vendor/x\": unter vendor/ gilt allein"},
		{"leer", strings.Replace(data, kpDirs, "\n            dirs:\n              - \"\"", 1),
			"Account kp in team-x: Verzeichnis-Scope: Verzeichnis fehlt"},
		{"mit / am Ende", strings.Replace(data, kpDirs, "\n            dirs:\n              - docs/", 1),
			"endet mit '/'"},
		{"kein Text", strings.Replace(data, kpDirs, "\n            dirs: docs", 1), "Export nicht lesbar"},
	} {
		write(c.content)
		runT(t, "config", "import", "--config", cfgB, file).want(t, 1, c.want, "Nichts geschrieben")
		if after := takeSnapshot(t, b, cfgB); !reflect.DeepEqual(after, before) {
			t.Errorf("%s: verändert", c.name)
		}
	}

	// Format 7 ohne dirs: die Verzeichnis-Scopes sind leer, auch die
	// gemerkten; vendor bleibt.
	write(format7)
	runT(t, "config", "import", "--config", cfgB, file).want(t, 0, "3 Accounts")
	for _, acc := range hubTables(t, cfgB).Accounts {
		for _, r := range acc.Rights {
			if r.Dirs != nil {
				t.Errorf("nach Format 7: %s hat Verzeichnis-Scopes %v", acc.Name, r.Dirs)
			}
		}
	}
	// Format 8: die Scopes kommen mit, in die Zeilen und die gemerkten
	// Rechte; fehlt dirs, ist es leer.
	runT(t, "config", "import", "--config", cfgB, exp).want(t, 0, "3 Accounts")
	got, want := hubTables(t, cfgB).Accounts, hubTables(t, cfgA).Accounts
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Accounts nach Format 8:\n%+v\nerwartet\n%+v", got, want)
	}
	var content string
	if err := rawHub(t, b).QueryRow(`SELECT content FROM documents WHERE name = 'SYSTEM:A:kp' AND deleted = 0`).Scan(&content); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content, `"dirs":["a/b","docs"]`) {
		t.Errorf("Zeile von kp: %s", content)
	}
	runT(t, "hub", "account", "show", "bob", "--config", cfgB).want(t, 0, "gemerkt", "privat: read, supersede, dir zwei/")
	write(strings.Replace(data, kpDirs, "", 1))
	runT(t, "config", "import", "--config", cfgB, file).want(t, 0, "3 Accounts")
	runT(t, "hub", "account", "show", "kp", "--config", cfgB).want(t, 0, "team-x: read\n")
}
