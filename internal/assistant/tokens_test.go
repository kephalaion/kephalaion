package assistant

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Dummy-Tokens im Format keph_<32 Bytes base64url>; sie gelten nirgends.
var (
	tokenA = "keph_" + strings.Repeat("A", 43)
	tokenB = "keph_" + strings.Repeat("B", 42) + "A"
)

func writeToken(t *testing.T, tokensDir, hub, file, content string) string {
	t.Helper()
	dir := filepath.Join(tokensDir, hub)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, file)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAccountsAndHubs(t *testing.T) {
	dir := t.TempDir()
	if hubs, err := Hubs(filepath.Join(dir, "fehlt")); err != nil || hubs != nil {
		t.Fatalf("fehlendes Verzeichnis: %v, %v", hubs, err)
	}
	writeToken(t, dir, "vm", "bob.token", tokenB+"\n")
	writeToken(t, dir, "vm", "alice.token", tokenA+"\n")
	writeToken(t, dir, "vm", "alice.token.pending", tokenB+"\n")
	writeToken(t, dir, "vm", "notiz.txt", "x")
	writeToken(t, dir, "eigen", "kp.token", tokenA+"\n")
	// Kein gültiger Alias: zählt nicht als Hub.
	writeToken(t, dir, "Groß", "x.token", tokenA+"\n")
	if err := os.WriteFile(filepath.Join(dir, "datei"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	hubs, err := Hubs(dir)
	if err != nil || !reflect.DeepEqual(hubs, []string{"eigen", "vm"}) {
		t.Fatalf("Hubs = %v, %v", hubs, err)
	}
	accounts, err := Accounts(filepath.Join(dir, "vm"))
	if err != nil || !reflect.DeepEqual(accounts, []string{"alice", "bob"}) {
		t.Fatalf("Accounts = %v, %v", accounts, err)
	}
	if accounts, err := Accounts(filepath.Join(dir, "fern")); err != nil || accounts != nil {
		t.Fatalf("fehlender Hub: %v, %v", accounts, err)
	}
}

func TestParseChoice(t *testing.T) {
	c, err := ParseChoice([]string{"vm=alice", "eigen=kp", "vm=alice"})
	if err != nil || !c.Equal(Choice{"vm": "alice", "eigen": "kp"}) {
		t.Fatalf("ParseChoice = %v, %v", c, err)
	}
	if !reflect.DeepEqual(c.Hubs(), []string{"eigen", "vm"}) {
		t.Fatalf("Hubs = %v", c.Hubs())
	}
	for _, bad := range [][]string{{"vm"}, {"=alice"}, {"vm="}, {"VM=alice"}, {"vm=admin"}, {"vm=a b"}, {"vm=alice", "vm=bob"}} {
		if _, err := ParseChoice(bad); err == nil {
			t.Errorf("ParseChoice(%v): kein Fehler", bad)
		}
	}
	if (Choice{"vm": "a"}).Equal(Choice{"vm": "b"}) || (Choice{"vm": "a"}).Equal(Choice{}) {
		t.Error("Equal: verschiedene Wahlen gelten als gleich")
	}
}

func TestLoginsAndHeaders(t *testing.T) {
	dir := t.TempDir()
	one := writeToken(t, dir, "eigen", "kp.token", tokenA+"\n")
	writeToken(t, dir, "eigen", "kp.token.pending", tokenB+"\n")
	writeToken(t, dir, "vm", "alice.token", tokenA+"\n")
	bob := writeToken(t, dir, "vm", "bob.token", tokenB+"\n")
	if err := os.MkdirAll(filepath.Join(dir, "leer"), 0o700); err != nil {
		t.Fatal(err)
	}

	// Ohne Wahl: der Hub mit einer Datei kommt, der mit zweien wird genannt,
	// der leere fehlt still.
	logins, skipped, err := Logins(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(logins, []Login{{Hub: "eigen", Account: "kp", File: one}}) {
		t.Fatalf("logins = %+v", logins)
	}
	if len(skipped) != 1 || skipped[0].Hub != "vm" || !reflect.DeepEqual(skipped[0].Accounts, []string{"alice", "bob"}) {
		t.Fatalf("skipped = %+v", skipped)
	}

	// Mit Wahl: beide Hubs.
	logins, skipped, err = Logins(dir, Choice{"vm": "bob"})
	if err != nil || len(skipped) != 0 {
		t.Fatalf("skipped = %+v, %v", skipped, err)
	}
	want := []Login{{Hub: "eigen", Account: "kp", File: one}, {Hub: "vm", Account: "bob", File: bob}}
	if !reflect.DeepEqual(logins, want) {
		t.Fatalf("logins = %+v", logins)
	}
	headers, unread := Headers(logins)
	if len(unread) != 0 {
		t.Fatalf("unread = %+v", unread)
	}
	wantHeaders := map[string]string{
		"X-Keph-Account-eigen": "kp", "X-Keph-Token-eigen": tokenA,
		"X-Keph-Account-vm": "bob", "X-Keph-Token-vm": tokenB,
	}
	if !reflect.DeepEqual(headers, wantHeaders) {
		t.Fatalf("headers = %v", headers)
	}

	// Die gewählte Datei fehlt: Der Hub wird genannt, auch wenn eine andere da
	// ist; ein gewählter Hub ohne Verzeichnis ebenso.
	_, skipped, err = Logins(dir, Choice{"eigen": "niemand", "fern": "x"})
	if err != nil {
		t.Fatal(err)
	}
	var hubs []string
	for _, s := range skipped {
		hubs = append(hubs, s.Hub)
		if strings.Contains(s.Reason, "keph_") {
			t.Errorf("Token im Grund: %s", s.Reason)
		}
	}
	if !reflect.DeepEqual(hubs, []string{"eigen", "fern", "vm"}) {
		t.Fatalf("skipped = %+v", skipped)
	}

	// Eine Datei ohne Token: Headers lässt den Hub weg und nennt ihn ohne
	// Inhalt.
	bad := writeToken(t, dir, "kaputt", "x.token", "kein token geheim\n")
	headers, unread = Headers([]Login{{Hub: "kaputt", Account: "x", File: bad}})
	if len(headers) != 0 || len(unread) != 1 || unread[0].Hub != "kaputt" {
		t.Fatalf("headers = %v, unread = %+v", headers, unread)
	}
	if strings.Contains(unread[0].Reason, "geheim") {
		t.Errorf("Inhalt der Datei im Grund: %s", unread[0].Reason)
	}
}

func TestNodeURL(t *testing.T) {
	for listen, want := range map[string]string{
		"127.0.0.1:7433": "http://127.0.0.1:7433/mcp",
		":7500":          "http://127.0.0.1:7500/mcp",
		"localhost:1":    "http://localhost:1/mcp",
	} {
		if got := NodeURL(listen, "/mcp"); got != want {
			t.Errorf("NodeURL(%q) = %q, erwartet %q", listen, got, want)
		}
	}
}

func TestHelperArgs(t *testing.T) {
	choice := Choice{"vm": "bob", "eigen": "kp"}
	args := HelperArgs("/opt/k/kephalaion", "/home/anna/.config/kephalaion/tokens", choice)
	want := []string{"/opt/k/kephalaion", "node", "mcp", "headers", "--tokens-dir", "/home/anna/.config/kephalaion/tokens",
		"--account", "eigen=kp", "--account", "vm=bob"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("HelperArgs = %v", args)
	}
	bin, dir, got, ok := ParseHelperArgs(args)
	if !ok || bin != "/opt/k/kephalaion" || dir != "/home/anna/.config/kephalaion/tokens" || !got.Equal(choice) {
		t.Fatalf("ParseHelperArgs = %q, %q, %v, %v", bin, dir, got, ok)
	}
	for _, bad := range [][]string{
		nil,
		{"/x/kephalaion", "node", "mcp", "headers"},
		{"/x/kephalaion", "node", "dir", "headers", "--tokens-dir", "/t"},
		{"/x/kephalaion", "node", "mcp", "headers", "--tokens-dir", "/t", "--account"},
		{"/x/kephalaion", "node", "mcp", "headers", "--tokens-dir", "/t", "--fremd", "x"},
		{"/x/kephalaion", "node", "mcp", "headers", "--tokens-dir", "/t", "--account", "ohne-gleich"},
	} {
		if _, _, _, ok := ParseHelperArgs(bad); ok {
			t.Errorf("ParseHelperArgs(%v): gilt als Helfer", bad)
		}
	}
}

func TestShellJoinSplit(t *testing.T) {
	cases := [][]string{
		{"/usr/local/bin/kephalaion", "node", "mcp", "headers", "--tokens-dir", "/home/a/.config/kephalaion/tokens"},
		{"/home/anna b/bin/kephalaion", "--account", "vm=bob"},
		{"/home/o'brien/k", "", "ä ö"},
	}
	for _, args := range cases {
		line := ShellJoin(args)
		got, ok := ShellSplit(line)
		if !ok || !reflect.DeepEqual(got, args) {
			t.Errorf("ShellSplit(%q) = %q, %v; erwartet %q", line, got, ok, args)
		}
	}
	if got := ShellJoin(cases[0]); got != strings.Join(cases[0], " ") {
		t.Errorf("ShellJoin setzt unnötig Anführungszeichen: %s", got)
	}
	if got := ShellQuote("a b"); got != "'a b'" {
		t.Errorf("ShellQuote = %s", got)
	}
	for _, foreign := range []string{`echo "x"`, "cat $HOME/x", "a | b", "a 'b", `a \n`, "a;b"} {
		if _, ok := ShellSplit(foreign); ok {
			t.Errorf("ShellSplit(%q): gilt als eigene Zeile", foreign)
		}
	}
}
