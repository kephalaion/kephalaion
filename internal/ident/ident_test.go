package ident

import (
	"strings"
	"testing"
)

func TestCheckName(t *testing.T) {
	good := []string{"a", "team-x", "laptop.1", "0_a", strings.Repeat("a", 63), "sys", "mysystem"}
	for _, n := range good {
		if err := CheckName("Collection", n); err != nil {
			t.Errorf("%q: %v", n, err)
		}
	}
	bad := []string{"", "A", "Team", "-a", ".a", "_a", "a:b", "a b", "ä", strings.Repeat("a", 64),
		"system", "systemx", "SYSTEM", "System-a", "sYsTeM.1"}
	for _, n := range bad {
		if err := CheckName("Collection", n); err == nil {
			t.Errorf("%q hätte abgelehnt werden sollen", n)
		}
	}
	if err := CheckName("Node", "SYSTEM"); err == nil || !strings.Contains(err.Error(), "reserviert") {
		t.Errorf("SYSTEM: %v", err)
	}
}

func TestParseAddress(t *testing.T) {
	h, c, err := ParseAddress("privat:team-x")
	if err != nil || h != "privat" || c != "team-x" {
		t.Fatalf("ParseAddress = %q, %q, %v", h, c, err)
	}
	for _, a := range []string{"privat", "privat:", ":team", "Privat:team", "privat:Team", "privat:system", "a:b:c"} {
		if _, _, err := ParseAddress(a); err == nil {
			t.Errorf("%q hätte abgelehnt werden sollen", a)
		}
	}
	if Address("privat", "team-x") != "privat:team-x" {
		t.Error("Address")
	}
}

func TestToken(t *testing.T) {
	a, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := NewToken()
	if a == b {
		t.Fatal("zwei gleiche Token")
	}
	if !strings.HasPrefix(a, "keph_") || len(a) != len("keph_")+43 {
		t.Errorf("Token %q: Länge %d", a, len(a))
	}
	if err := CheckToken(a); err != nil {
		t.Errorf("CheckToken(neu): %v", err)
	}
	for _, bad := range []string{"", "keph_", "abc", "keph_abc", a + "A", "KEPH_" + a[5:], a[:len(a)-1] + "=", a[:len(a)-1] + "+"} {
		if err := CheckToken(bad); err == nil {
			t.Errorf("CheckToken(%q) hätte scheitern sollen", bad)
		} else if len(bad) > 10 && strings.Contains(err.Error(), bad) {
			t.Errorf("Fehler nennt das Token: %v", err)
		}
	}
	h := HashToken(a)
	if len(h) != 64 || h == a || HashToken(a) != h || HashToken(b) == h {
		t.Errorf("HashToken = %q", h)
	}
	m := MaskToken(a)
	if m != "keph_…"+a[len(a)-4:] || strings.Contains(m, a[5:len(a)-4]) {
		t.Errorf("MaskToken = %q", m)
	}
	if MaskToken("") != "" {
		t.Error("MaskToken(leer)")
	}
}

func TestCheckDocName(t *testing.T) {
	good := []string{"a", "a.md", "tasks/001-a.md", "Groß/Übersicht.md", "a b/c d.md", "a:b",
		"system:x", "Système/x", ".hidden", "a/.b", "...", "a..b",
		strings.Repeat("x", MaxDocSegmentBytes),
		strings.TrimSuffix(strings.Repeat(strings.Repeat("x", 99)+"/", 10), "/") + "/" + strings.Repeat("y", 23)}
	for _, n := range good {
		if err := CheckDocName(n); err != nil {
			t.Errorf("%q: %v", n, err)
		}
	}
	bad := map[string]string{
		"":                "fehlt",
		"/a":              "beginnt oder endet",
		"a/":              "beginnt oder endet",
		"a//b":            "leeres Segment",
		"./a":             "'.' und '..'",
		"a/../b":          "'.' und '..'",
		"a/..":            "'.' und '..'",
		`a\b`:             `'\'`,
		"a\tb":            "Steuerzeichen",
		"a\nb":            "Steuerzeichen",
		"a\x7fb":          "Steuerzeichen",
		"a\u0085b":        "Steuerzeichen",
		"a\xffb":          "UTF-8",
		"SYSTEM:A:kleist": "vorbehalten",
		"SYSTEM:":         "vorbehalten",
		strings.Repeat("x", MaxDocSegmentBytes+1):     "Segment ist länger",
		strings.Repeat("x/", MaxDocNameBytes/2) + "x": "länger als 1024",
	}
	for n, want := range bad {
		err := CheckDocName(n)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, erwartet %q", n, err, want)
		}
	}
	if !IsSystemName("SYSTEM:A:x") || IsSystemName("system:a") {
		t.Error("IsSystemName")
	}
}

func TestDocDirPrefix(t *testing.T) {
	for in, want := range map[string]string{"": "", "/": "", "tasks": "tasks/", "tasks/": "tasks/", "a/b": "a/b/"} {
		got, err := DocDirPrefix(in)
		if err != nil || got != want {
			t.Errorf("DocDirPrefix(%q) = %q, %v; erwartet %q", in, got, err, want)
		}
	}
	for _, in := range []string{"/a", "a//", "..", "SYSTEM:"} {
		if _, err := DocDirPrefix(in); err == nil {
			t.Errorf("DocDirPrefix(%q) hätte abgelehnt werden sollen", in)
		}
	}
}

func TestDocAncestorsAndChild(t *testing.T) {
	if got := DocAncestors("a/b/c.md"); strings.Join(got, "|") != "a|a/b" {
		t.Errorf("DocAncestors = %v", got)
	}
	if got := DocAncestors("c.md"); len(got) != 0 {
		t.Errorf("DocAncestors ohne Verzeichnis = %v", got)
	}
	cases := []struct {
		prefix, name, child string
		isDir, ok           bool
	}{
		{"", "a.md", "a.md", false, true},
		{"", "a/b.md", "a", true, true},
		{"a/", "a/b.md", "b.md", false, true},
		{"a/", "a/b/c.md", "b", true, true},
		{"a/", "ab.md", "", false, false},
		{"a/", "a", "", false, false},
	}
	for _, c := range cases {
		child, isDir, ok := DocChild(c.prefix, c.name)
		if child != c.child || isDir != c.isDir || ok != c.ok {
			t.Errorf("DocChild(%q, %q) = %q, %v, %v", c.prefix, c.name, child, isDir, ok)
		}
	}
}

// Umbenennen: beide Namen gültig, das Ziel weder gleich noch in der Quelle;
// in die Gegenrichtung (x/y nach x) entscheidet erst die Datenbank.
func TestCheckRename(t *testing.T) {
	for _, ok := range [][2]string{{"a.md", "b.md"}, {"x", "xy"}, {"x/y", "x"}, {"x", "y/x"}, {"a/b", "a/c/b"}} {
		if err := CheckRename(ok[0], ok[1]); err != nil {
			t.Errorf("CheckRename(%q, %q): %v", ok[0], ok[1], err)
		}
	}
	for _, c := range []struct{ name, newName, want string }{
		{"x", "x", "der neue Name ist der alte"},
		{"x", "x/y", "liegt darunter"},
		{"a/b", "a/b/c/d", "liegt darunter"},
		{"x", "", "neuer Name"},
		{"x", "SYSTEM:A:bob", "neuer Name"},
		{"x", "y/", "neuer Name"},
		{"", "y", "Name fehlt"},
		{"SYSTEM:A:bob", "y", "vorbehalten"},
	} {
		if err := CheckRename(c.name, c.newName); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("CheckRename(%q, %q) = %v, erwartet %q", c.name, c.newName, err, c.want)
		}
	}
	for _, c := range []struct {
		doc, name, newName, want string
		ok                       bool
	}{
		{"a.md", "a.md", "b.md", "b.md", true},
		{"dir/a.md", "dir", "neu", "neu/a.md", true},
		{"dir/sub/b.md", "dir", "x/y", "x/y/sub/b.md", true},
		{"dirx/a.md", "dir", "neu", "", false},
		{"dir", "dir/a.md", "neu", "", false},
	} {
		got, ok := DocRenamed(c.doc, c.name, c.newName)
		if got != c.want || ok != c.ok {
			t.Errorf("DocRenamed(%q, %q, %q) = %q, %v", c.doc, c.name, c.newName, got, ok)
		}
	}
}

func TestCheckPrincipalName(t *testing.T) {
	for _, ok := range []string{"alice", "laptop", "admin2", "x-admin"} {
		if err := CheckPrincipalName("Account", ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for n, want := range map[string]string{"admin": "reserviert", "Admin": "ungültiger Name", "system-x": "reserviert", "": "fehlt"} {
		err := CheckPrincipalName("Account", n)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, erwartet %q", n, err, want)
		}
	}
	// Collections und Hub-Aliase dürfen admin heißen.
	if err := CheckName("Collection", "admin"); err != nil {
		t.Errorf("CheckName(admin): %v", err)
	}
}

func TestLogName(t *testing.T) {
	for in, want := range map[string]string{"laptop": "laptop", "": "-", "keph_abc\n": "(ungültig)", "Laptop": "(ungültig)"} {
		if got := LogName(in); got != want {
			t.Errorf("LogName(%q) = %q, erwartet %q", in, got, want)
		}
	}
}
