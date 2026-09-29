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

// hub account grant --vendor: wiederholbar, geprüft, in show und list
// sichtbar, ohne --vendor entzogen; ein gesperrter Account merkt den Scope.
func TestHubAccountGrantVendor(t *testing.T) {
	dir := isolate(t)
	cfg := setup(t, dir)
	c := "--config=" + cfg
	runT(t, "hub", "collection", "add", "team-x", c).want(t, 0)
	runT(t, "hub", "account", "add", "kp", c).want(t, 0)

	runT(t, "hub", "account", "grant", "kp", "team-x", "--vendor", "k-playbook", c).want(t, 0,
		"team-x erlaubt (read, vendor/k-playbook)")
	runT(t, "hub", "account", "grant", "kp", "team-x", "--vendor", "k-playbook", c).want(t, 0, "unverändert")
	runT(t, "hub", "account", "grant", "kp", "team-x", "--vendor", "zwei", "--vendor", "k-playbook", "--write", c).want(t, 0,
		"team-x erlaubt (read, write, vendor/k-playbook, vendor/zwei)")
	runT(t, "hub", "account", "grant", "kp", "team-x", "--vendor", "K-Playbook", c).want(t, 1,
		"Scope vendor/<name> \"K-Playbook\": ungültiger Name")
	runT(t, "hub", "account", "grant", "kp", "team-x", "--vendor", "", c).want(t, 1, "Scope vendor/<name>: Name fehlt")
	runT(t, "hub", "account", "show", "kp", c).want(t, 0, "team-x: read, write, vendor/k-playbook, vendor/zwei")
	runT(t, "hub", "account", "list", c).want(t, 0, "team-x (read, write, vendor/k-playbook, vendor/zwei)")
	got := hubTables(t, cfg).Accounts
	if len(got) != 1 || !reflect.DeepEqual(got[0].Rights, []hubstore.AccountRight{{Collection: "team-x",
		Rights: contract.Rights{Write: true, Vendor: []string{"k-playbook", "zwei"}}}}) {
		t.Errorf("Rechte: %+v", got)
	}
	// Ohne --vendor ist jeder Scope entzogen.
	runT(t, "hub", "account", "grant", "kp", "team-x", "--write", c).want(t, 0, "team-x erlaubt (read, write)")
	runT(t, "hub", "account", "grant", "kp", "team-x", "--vendor", "k-playbook", c).want(t, 0,
		"team-x erlaubt (read, vendor/k-playbook)")
	runT(t, "hub", "account", "lock", "kp", c).want(t, 0)
	runT(t, "hub", "account", "show", "kp", c).want(t, 0, "gemerkt", "team-x: read, vendor/k-playbook")
	runT(t, "hub", "account", "unlock", "kp", c).want(t, 0)
	runT(t, "hub", "account", "show", "kp", c).want(t, 0, "Rechte:\n    team-x: read, vendor/k-playbook")
	runT(t, "hub", "account", "--help").want(t, 0, "--vendor")
}

// Export ab Format 6 trägt die Scopes; der Import nimmt sie mit und prüft
// ihre Namen. Ein Export im Format 5 liest sich ohne Scopes; trägt er
// welche, bricht der Import ab.
func TestExportImportVendor(t *testing.T) {
	dir := isolate(t)
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	cfgA := setup(t, a)
	fill(t, cfgA)
	cA := "--config=" + cfgA
	runT(t, "hub", "account", "add", "kp", cA).want(t, 0)
	runT(t, "hub", "account", "grant", "kp", "team-x", "--vendor", "k-playbook", cA).want(t, 0)
	// bob ist gesperrt: die gemerkten Rechte tragen den Scope.
	runT(t, "hub", "account", "grant", "bob", "privat", "--supersede", "--vendor", "zwei", cA).want(t, 0)
	exp := filepath.Join(dir, "export.yaml")
	exportTo(t, cfgA, exp)
	raw, _ := os.ReadFile(exp)
	data := string(raw)
	for _, want := range []string{"format: 7", "vendor: []", "vendor:\n              - k-playbook", "vendor:\n              - zwei"} {
		if !strings.Contains(data, want) {
			t.Errorf("Export ohne %q:\n%s", want, data)
		}
	}

	cfgB := setup(t, b)
	runT(t, "hub", "collection", "add", "team-x", "--config", cfgB).want(t, 0)
	runT(t, "hub", "account", "add", "carol", "--config", cfgB).want(t, 0)
	runT(t, "hub", "account", "grant", "carol", "team-x", "--vendor", "alt", "--config", cfgB).want(t, 0)
	before := takeSnapshot(t, b, cfgB)
	file := filepath.Join(dir, "import.yaml")
	write := func(content string) {
		if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	const kpVendor = "\n            vendor:\n              - k-playbook"
	if !strings.Contains(data, kpVendor) {
		t.Fatalf("Export ohne %q:\n%s", kpVendor, data)
	}
	for _, c := range []struct{ name, content, want string }{
		{"F5 mit vendor", strings.Replace(toFormat6(t, data), "format: 6", "format: 5", 1),
			"Format 5 kennt keinen Scope vendor (tables.hub.accounts[0].rights[0].vendor)"},
		{"F5 mit vendor leer", strings.Replace(toFormat5(t, data), "supersede: false", "supersede: false\n            vendor: []", 1),
			"Format 5 kennt keinen Scope vendor"},
		{"F5 mit vendor null", strings.Replace(toFormat5(t, data), "supersede: false", "supersede: false\n            vendor:", 1),
			"Format 5 kennt keinen Scope vendor"},
		{"F4 mit vendor", strings.Replace(dropLines(t, toFormat6(t, data), "        user: "), "format: 6", "format: 4", 1),
			"Format 4 kennt keinen Scope vendor"},
		{"ungültiger Name", strings.Replace(data, kpVendor, "\n            vendor:\n              - K-Playbook", 1),
			"Account kp in team-x: Scope vendor/<name> \"K-Playbook\": ungültiger Name"},
		{"kein Text", strings.Replace(data, kpVendor, "\n            vendor: k-playbook", 1), "Export nicht lesbar"},
	} {
		write(c.content)
		runT(t, "config", "import", "--config", cfgB, file).want(t, 1, c.want, "Nichts geschrieben")
		if after := takeSnapshot(t, b, cfgB); !reflect.DeepEqual(after, before) {
			t.Errorf("%s: verändert", c.name)
		}
	}

	// Format 5 ohne vendor: die Scopes sind leer, auch die gemerkten.
	write(toFormat5(t, data))
	runT(t, "config", "import", "--config", cfgB, file).want(t, 0, "3 Accounts")
	for _, acc := range hubTables(t, cfgB).Accounts {
		for _, r := range acc.Rights {
			if r.Vendor != nil {
				t.Errorf("nach Format 5: %s hat Scopes %v", acc.Name, r.Vendor)
			}
		}
	}
	// Format 7: die Scopes kommen mit, in die Zeilen und die gemerkten
	// Rechte; fehlt vendor, ist es leer.
	runT(t, "config", "import", "--config", cfgB, exp).want(t, 0, "3 Accounts")
	got, want := hubTables(t, cfgB).Accounts, hubTables(t, cfgA).Accounts
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Accounts nach Format 7:\n%+v\nerwartet\n%+v", got, want)
	}
	var content string
	if err := rawHub(t, b).QueryRow(`SELECT content FROM documents WHERE name = 'SYSTEM:A:kp' AND deleted = 0`).Scan(&content); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content, `"vendor":["k-playbook"]`) {
		t.Errorf("Zeile von kp: %s", content)
	}
	runT(t, "hub", "account", "show", "bob", "--config", cfgB).want(t, 0, "gemerkt", "privat: read, supersede, vendor/zwei")
	write(strings.Replace(data, kpVendor, "", 1))
	runT(t, "config", "import", "--config", cfgB, file).want(t, 0, "3 Accounts")
	runT(t, "hub", "account", "show", "kp", "--config", cfgB).want(t, 0, "team-x: read\n")
}
