package assistant

import (
	"testing"
)

// Die ausdrückliche Wahl je Hub für den Eintrag eines Assistenten (Task
// 037): --account, dann der eigene Eintrag; ein Account ohne Token-Datei
// gilt als keine Wahl, und aus anderen Einträgen wird nichts übernommen. Bei
// OpenCode gilt der bisherige Standard aus follow nicht als Wahl.
func TestResolveChoice(t *testing.T) {
	dir := t.TempDir()
	writeToken(t, dir, "vm", "alice.token", tokenA+"\n")
	writeToken(t, dir, "vm", "bob.token", tokenB+"\n")
	writeToken(t, dir, "eigen", "kp.token", tokenA+"\n")

	st := func(name string, choice Choice) *state {
		return &state{name: name, f: found{exists: choice != nil, choice: choice}}
	}
	claude := st(Claude, Choice{"vm": "alice"})
	third := st(Codex, nil)
	gone := st(Codex, Choice{"vm": "carol", "eigen": "niemand"})

	// Der eigene Eintrag gilt.
	if c := resolveChoice(dir, claude, nil, nil); !c.Equal(Choice{"vm": "alice"}) {
		t.Errorf("claude: %v", c)
	}
	// --account geht vor dem eigenen Eintrag, auch für einen Account ohne
	// Token-Datei (den meldet Logins).
	if c := resolveChoice(dir, claude, Choice{"vm": "bob"}, nil); !c.Equal(Choice{"vm": "bob"}) {
		t.Errorf("claude mit --account: %v", c)
	}
	if c := resolveChoice(dir, claude, Choice{"vm": "carol"}, nil); !c.Equal(Choice{"vm": "carol"}) {
		t.Errorf("claude mit --account ohne Datei: %v", c)
	}
	// Ohne eigenen Eintrag keine Wahl — auch wenn andere eine tragen.
	if c := resolveChoice(dir, third, nil, nil); len(c) != 0 {
		t.Errorf("ohne Eintrag: %v", c)
	}
	// Die eigene Wahl ohne Token-Datei gilt nicht.
	if c := resolveChoice(dir, gone, nil, nil); len(c) != 0 {
		t.Errorf("ohne Token-Datei: %v", c)
	}
	// follow wirkt nur bei OpenCode: Trägt der Eintrag den bisherigen
	// Standard, ist es keine Wahl; ein anderer Account bleibt.
	if c := resolveChoice(dir, claude, nil, Choice{"vm": "alice"}); !c.Equal(Choice{"vm": "alice"}) {
		t.Errorf("claude mit follow: %v", c)
	}
	oc := st(OpenCode, Choice{"vm": "alice", "eigen": "kp"})
	if c := resolveChoice(dir, oc, nil, Choice{"vm": "alice"}); !c.Equal(Choice{"eigen": "kp"}) {
		t.Errorf("opencode, folgt: %v", c)
	}
	if c := resolveChoice(dir, oc, nil, Choice{"vm": "bob"}); !c.Equal(Choice{"vm": "alice", "eigen": "kp"}) {
		t.Errorf("opencode, ausdrücklich: %v", c)
	}
}

// Der Helfer trägt nur die ausdrückliche Wahl; alles Übrige löst er bei jeder
// Verbindung auf.
func TestEntryChoiceExplicitOnly(t *testing.T) {
	e := entry{Target: Target{Binary: "/b/kephalaion", TokensDir: "/t"}, logins: []Login{
		{Hub: "eigen", Account: "kp", How: ByOnly},
		{Hub: "vm", Account: "bob", How: ByDefault},
		{Hub: "zwei", Account: "x", How: ByOverride},
	}}
	if c := e.choice(); !c.Equal(Choice{"zwei": "x"}) {
		t.Fatalf("choice = %v", c)
	}
	if got := e.helper(); got != "/b/kephalaion node mcp headers --tokens-dir /t --account zwei=x" {
		t.Fatalf("helper = %s", got)
	}
}
