package vscodeext

import (
	"os"
	"path/filepath"
	"testing"
)

func mkdirs(t *testing.T, dirs ...string) {
	t.Helper()
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func TestFindNothing(t *testing.T) {
	home := t.TempDir()
	mkdirs(t, filepath.Join(home, ".vscode", "extensions", "other.ext-1.0.0"))
	if got := Find(home); len(got) != 0 {
		t.Errorf("Find = %+v", got)
	}
}

// Ohne extensions.json zählt das Verzeichnis mit der höchsten Fassung; die
// Editoren kommen in der Reihenfolge von Editors, Remote-Server bei ihrem
// Editor.
func TestFindByDirs(t *testing.T) {
	home := t.TempDir()
	ext := func(rel, name string) string { return filepath.Join(home, rel, "extensions", name) }
	mkdirs(t,
		ext(".vscode-server", "kascada.kephalaion-0.0.9"),
		ext(".vscode-server", "kascada.kephalaion-0.0.10"),
		ext(".vscodium-server", "kascada.kephalaion-0.3.0"),
		ext(".cursor-server", "kascada.kephalaion-0.2.0"),
		ext(".cursor", "kascada.kephalaion-0.2.1"),
		ext(".vscode-oss", "other.ext-0.1.0"),
	)
	got := Find(home)
	want := []Installation{
		{Editor: "VS Code", CLI: "code", Dir: ext(".vscode-server", "kascada.kephalaion-0.0.10"), Version: "0.0.10"},
		{Editor: "Cursor", CLI: "cursor", Dir: ext(".cursor", "kascada.kephalaion-0.2.1"), Version: "0.2.1"},
		{Editor: "Cursor", CLI: "cursor", Dir: ext(".cursor-server", "kascada.kephalaion-0.2.0"), Version: "0.2.0"},
		{Editor: "VSCodium", CLI: "codium", Dir: ext(".vscodium-server", "kascada.kephalaion-0.3.0"), Version: "0.3.0"},
	}
	if len(got) != len(want) {
		t.Fatalf("Find = %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] = %+v, erwartet %+v", i, got[i], want[i])
		}
	}
}

// Nach einem Downgrade mit --force liegt das Verzeichnis der höheren Fassung
// noch daneben; extensions.json entscheidet. Steht die Erweiterung dort
// nicht, ist sie nicht installiert, auch wenn ein Verzeichnis liegt.
func TestFindByExtensionsJSON(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".vscode-server", "extensions")
	mkdirs(t, filepath.Join(dir, "kascada.kephalaion-0.3.0"), filepath.Join(dir, "kascada.kephalaion-0.0.0"))
	manifest := `[{"identifier":{"id":"other.ext"},"version":"9.9.9","relativeLocation":"other.ext-9.9.9"},` +
		`{"identifier":{"id":"Kascada.Kephalaion"},"version":"0.0.0","relativeLocation":"kascada.kephalaion-0.0.0"}]`
	if err := os.WriteFile(filepath.Join(dir, "extensions.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	got := Find(home)
	if len(got) != 1 || got[0].Version != "0.0.0" || got[0].Dir != filepath.Join(dir, "kascada.kephalaion-0.0.0") {
		t.Errorf("Find = %+v", got)
	}

	if err := os.WriteFile(filepath.Join(dir, "extensions.json"), []byte(`[]`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := Find(home); len(got) != 0 {
		t.Errorf("nicht in extensions.json: %+v", got)
	}

	// Unlesbar: wie ohne die Datei.
	if err := os.WriteFile(filepath.Join(dir, "extensions.json"), []byte(`{`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := Find(home); len(got) != 1 || got[0].Version != "0.3.0" {
		t.Errorf("unlesbare extensions.json: %+v", got)
	}
}

func TestEditorsAndVersions(t *testing.T) {
	if got := CLIs(); len(got) != 4 || got[0] != "code" {
		t.Errorf("CLIs = %v", got)
	}
	if e, ok := EditorOf("cursor"); !ok || e.Name != "Cursor" {
		t.Errorf("EditorOf(cursor) = %+v, %v", e, ok)
	}
	if _, ok := EditorOf("vim"); ok {
		t.Error("EditorOf(vim)")
	}
	if v, ok := ParseVersion("1.20.3"); !ok || v != [3]int{1, 20, 3} {
		t.Errorf("ParseVersion = %v, %v", v, ok)
	}
	for _, s := range []string{"1.2", "1.2.x", "1.2.-3", "1.2.3-rc1"} {
		if _, ok := ParseVersion(s); ok {
			t.Errorf("ParseVersion(%q) ok", s)
		}
	}
	if !Less([3]int{0, 0, 9}, [3]int{0, 0, 10}) || Less([3]int{1, 0, 0}, [3]int{0, 9, 9}) || Less([3]int{1, 2, 3}, [3]int{1, 2, 3}) {
		t.Error("Less")
	}
}
