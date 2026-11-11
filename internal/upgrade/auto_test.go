package upgrade

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// Latest liefert Ziel, installierte Version und den Report wie --check
// --json aus einer Frage; Report ruft dasselbe.
func TestLatest(t *testing.T) {
	f := newFakeGitHub(t)
	f.addRelease("v0.2.0", []byte("neu"))
	f.latest = "v0.2.0"
	exe := installed(t)
	u, _ := f.upgrader("v0.1.0", exe)
	l, err := u.Latest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if l.Target.String() != "v0.2.0" || l.Current.String() != "v0.1.0" || l.IsDev || l.Report.State != StateOK ||
		!l.Report.UpdateAvailable || l.Report.Method != MethodSelf {
		t.Errorf("Latest: %+v", l)
	}
	if n := f.requests.Load(); n != 1 {
		t.Errorf("%d Anfragen", n)
	}
	assertUnchanged(t, exe)

	f.srv.Close()
	l, err = u.Latest(context.Background())
	if err == nil || l.Report.State != StateFailed || l.Report.Error == "" || l.Report.Method == "" {
		t.Errorf("ohne Netz: %+v, %v", l, err)
	}
}

// Stage lädt neben das Binary, ohne es anzufassen; scheitert die Prüfsumme,
// bleibt nichts liegen.
func TestStage(t *testing.T) {
	f := newFakeGitHub(t)
	f.addRelease("v0.2.0", []byte("neu"))
	f.latest = "v0.2.0"
	exe := installed(t)
	u, _ := f.upgrader("v0.1.0", exe)
	l, err := u.Latest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	staged, err := u.Stage(context.Background(), l, exe)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(staged) != filepath.Dir(exe) || readFile(t, staged) != "neu" || readFile(t, exe) != "alt" {
		t.Errorf("staged %s", staged)
	}
	if fi, _ := os.Stat(staged); fi.Mode().Perm() != 0o755 {
		t.Errorf("Rechte %v", fi.Mode().Perm())
	}
	_ = os.Remove(staged)

	f.releases["v0.2.0"][SumsAsset] = []byte("0000000000000000000000000000000000000000000000000000000000000000  " +
		AssetName(testOS, testArch) + "\n")
	if _, err := u.Stage(context.Background(), l, exe); err == nil {
		t.Fatal("falsche Prüfsumme angenommen")
	}
	assertUnchanged(t, exe)
}

// Swap ersetzt und behält das alte Binary als Rückweg nach TempPattern;
// Restore setzt es zurück. Scheitert das Ersetzen, liegt weder die neue
// Datei noch der Rückweg da.
func TestSwapAndRestore(t *testing.T) {
	exe := installed(t)
	dir := filepath.Dir(exe)
	stage := func() string {
		t.Helper()
		f, err := os.CreateTemp(dir, TempPattern)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = f.WriteString("neu")
		_ = f.Close()
		return f.Name()
	}
	u := &Upgrader{}
	backup, err := u.Swap(stage(), exe)
	if err != nil {
		t.Fatal(err)
	}
	if readFile(t, exe) != "neu" || readFile(t, backup) != "alt" {
		t.Errorf("nach Swap: exe %q, Rückweg %q", readFile(t, exe), readFile(t, backup))
	}
	if ok, _ := filepath.Match(TempPattern, filepath.Base(backup)); !ok || filepath.Dir(backup) != dir {
		t.Errorf("Rückweg %s", backup)
	}
	if err := u.Restore(backup, exe); err != nil {
		t.Fatal(err)
	}
	assertUnchanged(t, exe)

	u.Rename = func(string, string) error { return errors.New("Gerät beschäftigt") }
	if _, err := u.Swap(stage(), exe); err == nil {
		t.Fatal("Swap ohne Fehler")
	}
	assertUnchanged(t, exe)
	if err := u.Restore(filepath.Join(dir, "weg"), exe); err == nil {
		t.Error("Restore ohne Fehler")
	}
}

// Lock nimmt die Sperre ohne zu warten; ein zweiter Lauf bekommt ErrBusy. Die
// Datei bleibt liegen und ist keine temporäre.
func TestLock(t *testing.T) {
	dir := t.TempDir()
	first, err := Lock(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Lock(dir); !errors.Is(err, ErrBusy) {
		t.Errorf("zweite Sperre: %v", err)
	}
	_ = first.Close()
	second, err := Lock(dir)
	if err != nil {
		t.Fatalf("nach dem Freigeben: %v", err)
	}
	_ = second.Close()
	if _, err := os.Stat(filepath.Join(dir, LockName)); err != nil {
		t.Error(err)
	}
	assertNoTemp(t, dir)
}
