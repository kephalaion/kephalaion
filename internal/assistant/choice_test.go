package assistant

import (
	"strings"
	"testing"
)

// Die Wahl je Hub für den Eintrag eines Assistenten: --account, dann der
// eigene Eintrag, dann die anderen; ein Account ohne Token-Datei gilt als
// keine Wahl, und widersprechen sich die anderen, gilt der Hub als ohne Wahl.
func TestResolveChoice(t *testing.T) {
	dir := t.TempDir()
	writeToken(t, dir, "vm", "alice.token", tokenA+"\n")
	writeToken(t, dir, "vm", "bob.token", tokenB+"\n")
	writeToken(t, dir, "eigen", "kp.token", tokenA+"\n")

	st := func(name string, choice Choice) *state {
		return &state{name: name, f: found{exists: choice != nil, choice: choice}}
	}
	claude := st(Claude, Choice{"vm": "alice"})
	codex := st(Codex, Choice{"vm": "bob"})
	third := st("dritter", nil)
	gone := st("vierter", Choice{"vm": "carol", "eigen": "niemand"})
	all := []*state{claude, codex, third, gone}

	// Der eigene Eintrag geht vor.
	if c, notes := resolveChoice(dir, claude, all, nil); !c.Equal(Choice{"vm": "alice"}) || len(notes) != 0 {
		t.Errorf("claude: %v, %v", c, notes)
	}
	if c, _ := resolveChoice(dir, codex, all, nil); !c.Equal(Choice{"vm": "bob"}) {
		t.Errorf("codex: %v", c)
	}
	// --account geht vor dem eigenen Eintrag, auch für einen Account ohne
	// Token-Datei (den meldet Logins).
	if c, _ := resolveChoice(dir, claude, all, Choice{"vm": "bob"}); !c.Equal(Choice{"vm": "bob"}) {
		t.Errorf("claude mit --account: %v", c)
	}
	if c, _ := resolveChoice(dir, claude, all, Choice{"vm": "carol"}); !c.Equal(Choice{"vm": "carol"}) {
		t.Errorf("claude mit --account ohne Datei: %v", c)
	}
	// Ohne eigenen Eintrag: Die anderen widersprechen sich — keine Wahl, und
	// die Meldung nennt sie.
	c, notes := resolveChoice(dir, third, all, nil)
	if len(c) != 0 || len(notes) != 1 || !strings.Contains(notes[0], "Hub vm") ||
		!strings.Contains(notes[0], "alice (claude)") || !strings.Contains(notes[0], "bob (codex)") {
		t.Errorf("dritter: %v, %v", c, notes)
	}
	// Sind sich die anderen einig, gilt ihre Wahl.
	agree := []*state{claude, st(Codex, Choice{"vm": "alice"}), third}
	if c, notes := resolveChoice(dir, third, agree, nil); !c.Equal(Choice{"vm": "alice"}) || len(notes) != 0 {
		t.Errorf("dritter, einig: %v, %v", c, notes)
	}
	// Die eigene Wahl ohne Token-Datei gilt nicht; dann zählen die anderen —
	// hier widersprüchlich für vm, niemand für eigen.
	if c, notes := resolveChoice(dir, gone, all, nil); len(c) != 0 || len(notes) != 1 {
		t.Errorf("vierter: %v, %v", c, notes)
	}
	// Logins nimmt danach für den Hub mit einer Token-Datei diese eine.
	logins, skipped, err := Logins(dir, Choice{})
	if err != nil || len(logins) != 1 || logins[0].Hub != "eigen" || len(skipped) != 1 || skipped[0].Hub != "vm" {
		t.Errorf("Logins: %+v, %+v, %v", logins, skipped, err)
	}
}
