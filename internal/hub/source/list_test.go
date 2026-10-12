package source

import (
	"strings"
	"testing"
)

// Die eingebettete Liste ist gültig und trägt k-playbook mit den Vorgaben
// aus Task 031.
func TestEmbeddedList(t *testing.T) {
	m, err := parse(embedded)
	if err != nil {
		t.Fatal(err)
	}
	e, ok := m["k-playbook"]
	if !ok || len(m) != 1 {
		t.Fatalf("Liste: %v", m)
	}
	want := Entry{Name: "k-playbook", Public: true, Git: "https://github.com/kascada/k-playbook",
		Repo: "kascada/k-playbook", Asset: "k-playbook-defaults.tar.gz", MaxBytes: 8 << 20}
	if e != want {
		t.Errorf("Eintrag %+v, erwartet %+v", e, want)
	}
	if got, ok := Lookup("k-playbook"); !ok || got != want {
		t.Errorf("Lookup: %+v %v", got, ok)
	}
	if _, ok := Lookup("k-playbook-dev"); ok {
		t.Error("k-playbook-dev steht nicht in der Liste")
	}
	if n := Names(); len(n) != 1 || n[0] != "k-playbook" {
		t.Errorf("Names: %v", n)
	}
}

// parse liest strikt.
func TestParseStrict(t *testing.T) {
	entry := func(body string) string { return "x:\n" + body }
	good := "  public: true\n  source:\n    git: https://github.com/o/r\n    asset: a.tar.gz\n  max_bytes: 10\n"
	if _, err := parse([]byte(entry(good))); err != nil {
		t.Fatalf("gültiger Eintrag: %v", err)
	}
	cases := map[string]string{
		"unbekannter Schlüssel": entry(good + "  extra: 1\n"),
		"doppelter Name":        entry(good) + entry(good),
		"Namensregel":           strings.Replace(entry(good), "x:", "System-x:", 1),
		"public fehlt":          entry(strings.Replace(good, "  public: true\n", "", 1)),
		"public false":          entry(strings.Replace(good, "public: true", "public: false", 1)),
		"source fehlt":          entry("  public: true\n  max_bytes: 10\n"),
		"kein GitHub":           entry(strings.Replace(good, "github.com", "example.com", 1)),
		"http":                  entry(strings.Replace(good, "https://", "http://", 1)),
		"Pfad zu lang":          entry(strings.Replace(good, "o/r", "o/r/x", 1)),
		"Query":                 entry(strings.Replace(good, "o/r", "o/r?x=1", 1)),
		"Asset mit Pfad":        entry(strings.Replace(good, "a.tar.gz", "d/a.tar.gz", 1)),
		"Asset leer":            entry(strings.Replace(good, "a.tar.gz", `""`, 1)),
		"max_bytes 0":           entry(strings.Replace(good, "max_bytes: 10", "max_bytes: 0", 1)),
		"max_bytes zu groß":     entry(strings.Replace(good, "max_bytes: 10", "max_bytes: 999999999999", 1)),
	}
	for name, data := range cases {
		if _, err := parse([]byte(data)); err == nil {
			t.Errorf("%s: kein Fehler", name)
		}
	}
}
