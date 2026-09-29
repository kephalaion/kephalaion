package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/kephalaion/kephalaion/internal/ident"
	"github.com/kephalaion/kephalaion/internal/testcert"
)

// caFile schreibt das Zertifikat einer Test-CA in eine Datei unter dir.
func caFile(t *testing.T, dir, name, pemText string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(pemText), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// --ca-file bei add und set: gespeichert wird der Inhalt, show nennt Subject,
// Gültigkeit und Fingerabdruck, list eine Spalte CA; nur bei https, und ein
// Wechsel des Transports verwirft sie.
func TestNodeHubCA(t *testing.T) {
	dir := isolate(t)
	cfg := setup(t, dir)
	c := "--config=" + cfg
	ca, ca2 := testcert.NewCA(t, "Test-CA"), testcert.NewCA(t, "Zweite CA")
	file, file2 := caFile(t, dir, "ca.pem", ca.PEM), caFile(t, dir, "ca2.pem", ca2.PEM)
	tok, err := ident.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	add := func(alias string, args ...string) result {
		t.Helper()
		all := append([]string{"node", "hub", "add", alias, "--node", "laptop", "--token-stdin"}, args...)
		return runIn(t, tok+"\n", append(all, c)...)
	}
	// Die Regeln greifen vor dem Lesen des Tokens: nichts von stdin gelesen.
	add("kaputt", "--transport", "https", "--address", "https://h", "--ca-file", filepath.Join(dir, "fehlt.pem")).
		want(t, 1, "--ca-file: open")
	add("kaputt", "--transport", "https", "--address", "https://h", "--ca-file", caFile(t, dir, "muell.pem", "kein PEM")).
		want(t, 1, "muell.pem: --ca-file: kein Zertifikat gefunden")
	add("kaputt", "--transport", "http", "--address", "http://localhost:1", "--ca-file", file).
		want(t, 1, "--ca-file gibt es nur bei Transport https")
	add("kaputt", "--transport", "ssh", "--address", "h", "--ca-file", file).want(t, 1, "nur bei Transport https")
	runT(t, "node", "hub", "list", c).want(t, 0, "Keine Hubs.")

	add("vm", "--transport", "https", "--address", "https://9.141.8.157", "--ca-file", file).
		want(t, 0, "Hub vm eingetragen (https https://9.141.8.157, als Node laptop)")
	add("ohne", "--transport", "https", "--address", "https://hub.example.org").want(t, 0)
	r := runT(t, "node", "hub", "list", c)
	r.want(t, 0, "CA", "vm", "ja", "ohne", "–")
	if strings.Contains(r.out, "BEGIN CERTIFICATE") {
		t.Errorf("list zeigt die CA selbst:\n%s", r.out)
	}
	r = runT(t, "node", "hub", "show", "vm", c)
	r.want(t, 0, "  CA:           CN=Test-CA, gültig ", " – ", ", SHA-256 ")
	if !strings.Contains(r.out, ca.Cert.NotAfter.UTC().Format("2006-01-02")) || strings.Contains(r.out, "BEGIN") {
		t.Errorf("show:\n%s", r.out)
	}
	runT(t, "node", "hub", "show", "ohne", c).want(t, 0, "  CA:           – (System-Roots)")
	h, err := nodeStore(t, cfg).Hub(context.Background(), "vm")
	if err != nil || h.CA != ca.PEM {
		t.Fatalf("gespeichert: %q, %v", h.CA, err)
	}

	// set: andere CA, zwei CAs in einer Datei, entfernen, bei http abgewiesen.
	runT(t, "node", "hub", "set", "vm", "--ca-file", file2, c).want(t, 0, "Hub vm geändert")
	runT(t, "node", "hub", "show", "vm", c).want(t, 0, "CN=Zweite CA")
	both := caFile(t, dir, "beide.pem", ca.PEM+ca2.PEM)
	runT(t, "node", "hub", "set", "vm", "--ca-file", both, c).want(t, 0)
	r = runT(t, "node", "hub", "show", "vm", c)
	r.want(t, 0, "CN=Test-CA", "CN=Zweite CA")
	runT(t, "node", "hub", "set", "vm", "--ca-file", "", c).want(t, 0)
	runT(t, "node", "hub", "show", "vm", c).want(t, 0, "  CA:           – (System-Roots)")
	runT(t, "node", "hub", "set", "vm", "--ca-file", file, c).want(t, 0)
	runT(t, "node", "hub", "set", "vm", "--transport", "http", "--address", "http://localhost:7434", c).want(t, 0)
	if h, _ := nodeStore(t, cfg).Hub(context.Background(), "vm"); h.CA != "" || h.Transport != "http" {
		t.Errorf("nach Wechsel auf http: %+v", h)
	}
	runT(t, "node", "hub", "set", "vm", "--ca-file", file, c).want(t, 1, "nur bei Transport https")
	runT(t, "node", "hub", "set", "vm", "--transport", "https", "--address", "https://h", "--ca-file", file, c).want(t, 0)
	runT(t, "node", "hub", "show", "vm", c).want(t, 0, "CN=Test-CA")
	runT(t, "node", "hub", "--help").want(t, 0, "--ca-file pfad", "System-Roots")
}

// Export im Format 7 trägt die CA je Hub-Eintrag als Block; der Import
// nimmt sie mit und prüft sie wie die CLI. Ein Export vor Format 7 darf
// keine CA tragen und liest sich ohne.
func TestExportImportCA(t *testing.T) {
	dir := isolate(t)
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	cfgA := setup(t, a)
	fill(t, cfgA)
	ca := testcert.NewCA(t, "Test-CA")
	tok, _ := ident.NewToken()
	runIn(t, tok, "node", "hub", "add", "vm", "--node", "laptop", "--transport", "https", "--address", "https://9.141.8.157",
		"--ca-file", caFile(t, dir, "ca.pem", ca.PEM), "--token-stdin", "--config", cfgA).want(t, 0)
	exp := filepath.Join(dir, "export.yaml")
	exportTo(t, cfgA, exp)
	raw, _ := os.ReadFile(exp)
	data := string(raw)
	for _, want := range []string{"format: 7", "        ca: \"\"\n", "        ca: |\n          -----BEGIN CERTIFICATE-----\n"} {
		if !strings.Contains(data, want) {
			t.Errorf("Export ohne %q:\n%s", want, data)
		}
	}

	cfgB := setup(t, b)
	runT(t, "config", "import", "--config", cfgB, exp).want(t, 0, "3 Hubs")
	if got, want := nodeTables(t, cfgB), nodeTables(t, cfgA); !reflect.DeepEqual(got, want) {
		t.Errorf("Node-Tabellen\n%+v\nerwartet\n%+v", got, want)
	}
	runT(t, "node", "hub", "show", "vm", "--config", cfgB).want(t, 0, "CN=Test-CA")
	before := takeSnapshot(t, b, cfgB)

	file := filepath.Join(dir, "import.yaml")
	write := func(content string) {
		if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	const vmAddr = "address: https://9.141.8.157\n"
	if !strings.Contains(data, vmAddr) {
		t.Fatalf("Export ohne %q:\n%s", vmAddr, data)
	}
	for _, c := range []struct{ name, content, want string }{
		{"F6 mit CA", strings.Replace(data, "format: 7", "format: 6", 1), "Format 6 kennt keine CA (tables.node.hubs["},
		{"F6 mit CA leer", strings.Replace(toFormat6(t, data), "        hub_id: \"\"", "        ca: \"\"\n        hub_id: \"\"", 1),
			"Format 6 kennt keine CA"},
		{"F6 mit CA null", strings.Replace(toFormat6(t, data), "        hub_id: \"\"", "        ca:\n        hub_id: \"\"", 1),
			"Format 6 kennt keine CA"},
		{"CA bei http", strings.Replace(strings.Replace(data, vmAddr, "address: http://localhost:7434\n", 1),
			"transport: https", "transport: http", 1), "--ca-file gibt es nur bei Transport https"},
		{"kaputte CA", strings.Replace(data, "-----BEGIN CERTIFICATE-----", "-----BEGIN ZERTIFIKAT-----", 1), "kein Zertifikat gefunden"},
	} {
		write(c.content)
		runT(t, "config", "import", "--config", cfgB, file).want(t, 1, c.want, "Nichts geschrieben")
		if after := takeSnapshot(t, b, cfgB); !reflect.DeepEqual(after, before) {
			t.Errorf("%s: verändert", c.name)
		}
	}
	// Format 6 ohne CA: der Eintrag kommt ohne CA an (System-Roots).
	write(toFormat6(t, data))
	runT(t, "config", "import", "--config", cfgB, file).want(t, 0, "3 Hubs")
	if h, err := nodeStore(t, cfgB).Hub(context.Background(), "vm"); err != nil || h.CA != "" || h.Transport != "https" {
		t.Errorf("nach Format 6: %+v, %v", h, err)
	}
	runT(t, "config", "import", "--config", cfgB, exp).want(t, 0, "3 Hubs")
	if h, _ := nodeStore(t, cfgB).Hub(context.Background(), "vm"); h.CA != ca.PEM {
		t.Errorf("nach Format 7: CA %q", h.CA)
	}
}
