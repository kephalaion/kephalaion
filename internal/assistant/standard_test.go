package assistant

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Der Standard-Account (Task 037): Datei tokens/<hub>/default mit dem Namen
// und einer Zeilenschaltung, 0600; fehlt sie, ist der Standard leer. Ein
// Inhalt, der kein Account-Name ist, ist ein Fehler, der ihn nicht nennt.
func TestDefaultFile(t *testing.T) {
	dir := t.TempDir()
	if got, err := ReadDefault(dir, "vm"); err != nil || got != "" {
		t.Fatalf("ohne Datei: %q, %v", got, err)
	}
	if err := WriteDefault(dir, "vm", "bob"); err != nil {
		t.Fatal(err)
	}
	path := DefaultFile(dir, "vm")
	if path != filepath.Join(dir, "vm", "default") {
		t.Fatalf("DefaultFile = %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "bob\n" {
		t.Fatalf("Inhalt %q, %v", data, err)
	}
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("Rechte %v, %v", fi.Mode().Perm(), err)
	}
	if got, err := ReadDefault(dir, "vm"); err != nil || got != "bob" {
		t.Fatalf("ReadDefault = %q, %v", got, err)
	}
	// Die Datei stört Accounts nicht.
	writeToken(t, dir, "vm", "bob.token", tokenB+"\n")
	if accounts, err := Accounts(filepath.Join(dir, "vm")); err != nil || !reflect.DeepEqual(accounts, []string{"bob"}) {
		t.Fatalf("Accounts = %v, %v", accounts, err)
	}
	for _, bad := range [][2]string{{"VM", "bob"}, {"vm", "admin"}, {"vm", "Bob"}, {"vm", ""}} {
		if err := WriteDefault(dir, bad[0], bad[1]); err == nil {
			t.Errorf("WriteDefault(%q, %q): kein Fehler", bad[0], bad[1])
		}
	}
	// Ein Token in der Datei: Fehler, ohne den Inhalt.
	if err := os.WriteFile(path, []byte(tokenA+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadDefault(dir, "vm"); err == nil || strings.Contains(err.Error(), "keph_") {
		t.Fatalf("ReadDefault mit Token: %v", err)
	}
	if removed, err := RemoveDefault(dir, "vm"); err != nil || !removed {
		t.Fatalf("RemoveDefault = %v, %v", removed, err)
	}
	if removed, err := RemoveDefault(dir, "vm"); err != nil || removed {
		t.Fatalf("RemoveDefault ohne Datei = %v, %v", removed, err)
	}
}

// Die Reihenfolge der Wahl: Überschreibung, Standard, einzige Token-Datei,
// sonst „Wahl fehlt“ mit den Kandidaten. Ein Standard ohne Token-Datei wird
// genannt, die nächste Regel greift; eine Überschreibung ohne Token-Datei
// bleibt ohne Rückfall.
func TestResolve(t *testing.T) {
	dir := t.TempDir()
	writeToken(t, dir, "zwei", "alice.token", tokenA+"\n")
	writeToken(t, dir, "zwei", "bob.token", tokenB+"\n")
	writeToken(t, dir, "eins", "kp.token", tokenA+"\n")

	// Eine Datei ohne Standard: die einzige.
	r := Resolve(dir, "eins", "")
	if r.How != ByOnly || r.Account != "kp" || r.File != TokenFile(dir, "eins", "kp") || len(r.Notes) != 0 {
		t.Fatalf("eine Datei: %+v", r)
	}
	// Zwei Dateien ohne Standard: Wahl fehlt, mit beiden Kandidaten.
	r = Resolve(dir, "zwei", "")
	if r.How != Missing || r.Account != "" || r.File != "" || !reflect.DeepEqual(r.Candidates, []string{"alice", "bob"}) ||
		!strings.Contains(r.Reason, "Wahl fehlt") {
		t.Fatalf("zwei Dateien: %+v", r)
	}
	// Standard.
	if err := WriteDefault(dir, "zwei", "bob"); err != nil {
		t.Fatal(err)
	}
	r = Resolve(dir, "zwei", "")
	if r.How != ByDefault || r.Account != "bob" || r.File != TokenFile(dir, "zwei", "bob") || r.Reason != "" {
		t.Fatalf("Standard: %+v", r)
	}
	// Überschreibung vor Standard.
	r = Resolve(dir, "zwei", "alice")
	if r.How != ByOverride || r.Account != "alice" || r.File != TokenFile(dir, "zwei", "alice") {
		t.Fatalf("Überschreibung: %+v", r)
	}
	// Überschreibung ohne Token-Datei: kein Rückfall auf den Standard.
	r = Resolve(dir, "zwei", "carol")
	if r.How != ByOverride || r.Account != "carol" || r.File != "" || !strings.Contains(r.Reason, "carol") {
		t.Fatalf("Überschreibung ohne Datei: %+v", r)
	}
	// Veralteter Standard: Hinweis, dann die nächste Regel — bei zwei Dateien
	// fehlt die Wahl, bei einer gilt sie.
	if err := WriteDefault(dir, "zwei", "carol"); err != nil {
		t.Fatal(err)
	}
	r = Resolve(dir, "zwei", "")
	if r.How != Missing || len(r.Notes) != 1 || !strings.Contains(r.Notes[0], "Standard carol hat keine Token-Datei") {
		t.Fatalf("veralteter Standard, zwei Dateien: %+v", r)
	}
	if err := WriteDefault(dir, "eins", "carol"); err != nil {
		t.Fatal(err)
	}
	r = Resolve(dir, "eins", "")
	if r.How != ByOnly || r.Account != "kp" || len(r.Notes) != 1 {
		t.Fatalf("veralteter Standard, eine Datei: %+v", r)
	}
	// .pending allein macht keinen Kandidaten; neben der .token steht der
	// Account in beiden Listen.
	writeToken(t, dir, "warte", "neu.token.pending", tokenA+"\n")
	r = Resolve(dir, "warte", "")
	if r.How != Missing || len(r.Candidates) != 0 || !reflect.DeepEqual(r.Pending, []string{"neu"}) || r.Reason != "" {
		t.Fatalf(".pending allein: %+v", r)
	}
	writeToken(t, dir, "eins", "kp.token.pending", tokenB+"\n")
	r = Resolve(dir, "eins", "")
	if !reflect.DeepEqual(r.Candidates, []string{"kp"}) || !reflect.DeepEqual(r.Pending, []string{"kp"}) {
		t.Fatalf(".pending neben .token: %+v", r)
	}
	// Ohne Verzeichnis: leer, ohne Grund.
	r = Resolve(dir, "fern", "")
	if r.How != Missing || r.Candidates == nil || r.Pending == nil || r.Reason != "" {
		t.Fatalf("ohne Verzeichnis: %+v", r)
	}
}

// Logins folgt derselben Reihenfolge: der Standard bei zwei Dateien, die
// Überschreibung davor; ohne Standard wird der Hub mit beiden Kandidaten
// übergangen; ein veralteter Standard steht in den Notes.
func TestLoginsDefault(t *testing.T) {
	dir := t.TempDir()
	writeToken(t, dir, "vm", "alice.token", tokenA+"\n")
	bob := writeToken(t, dir, "vm", "bob.token", tokenB+"\n")
	kp := writeToken(t, dir, "eigen", "kp.token", tokenA+"\n")

	logins, skipped, err := Logins(dir, nil)
	if err != nil || len(logins) != 1 || len(skipped) != 1 || !reflect.DeepEqual(skipped[0].Accounts, []string{"alice", "bob"}) ||
		!strings.Contains(skipped[0].Reason, "Wahl fehlt") {
		t.Fatalf("ohne Standard: %+v, %+v, %v", logins, skipped, err)
	}
	if err := WriteDefault(dir, "vm", "bob"); err != nil {
		t.Fatal(err)
	}
	logins, skipped, err = Logins(dir, nil)
	want := []Login{{Hub: "eigen", Account: "kp", File: kp, How: ByOnly}, {Hub: "vm", Account: "bob", File: bob, How: ByDefault}}
	if err != nil || len(skipped) != 0 || !reflect.DeepEqual(logins, want) {
		t.Fatalf("Standard: %+v, %+v, %v", logins, skipped, err)
	}
	logins, _, _ = Logins(dir, Choice{"vm": "alice"})
	if logins[1].Account != "alice" || logins[1].How != ByOverride {
		t.Fatalf("Überschreibung: %+v", logins)
	}
	if err := WriteDefault(dir, "vm", "carol"); err != nil {
		t.Fatal(err)
	}
	_, skipped, _ = Logins(dir, nil)
	if len(skipped) != 1 || len(skipped[0].Notes) != 1 || len(skipped[0].Accounts) != 2 {
		t.Fatalf("veralteter Standard: %+v", skipped)
	}
	if err := WriteDefault(dir, "eigen", "weg"); err != nil {
		t.Fatal(err)
	}
	logins, _, _ = Logins(dir, nil)
	if logins[0].Hub != "eigen" || logins[0].How != ByOnly || len(logins[0].Notes) != 1 {
		t.Fatalf("veralteter Standard, eine Datei: %+v", logins)
	}
}
