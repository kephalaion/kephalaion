package upgrade

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kephalaion/kephalaion/internal/buildinfo"
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
	u := &Upgrader{Current: buildinfo.Info{Version: "v0.1.0"}}
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
	if ok, _ := filepath.Match(BackupPattern, filepath.Base(backup)); !ok {
		t.Errorf("Rückweg %s nicht nach %s", backup, BackupPattern)
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

// Swap nennt im Namen des Rückwegs die Version des alten Binarys und die
// Zeit; Backups liest beide zurück, auch mit Suffix, die jüngste zuerst.
// RemoveBackups entfernt alle.
func TestBackups(t *testing.T) {
	exe := installed(t)
	dir := filepath.Dir(exe)
	swap := func(version string, at time.Time) string {
		t.Helper()
		f, err := os.CreateTemp(dir, TempPattern)
		if err != nil {
			t.Fatal(err)
		}
		_ = f.Close()
		u := &Upgrader{Current: buildinfo.Info{Version: version}, Now: func() time.Time { return at }}
		backup, err := u.Swap(f.Name(), exe)
		if err != nil {
			t.Fatal(err)
		}
		return backup
	}
	day := time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)
	older := swap("v0.4.0", day)
	newer := swap("v0.5.0-rc1", day.Add(time.Hour))
	if !strings.HasPrefix(filepath.Base(newer), ".kephalaion-upgrade-backup-v0.5.0-rc1-20261111110000-") {
		t.Errorf("Name %s", filepath.Base(newer))
	}
	got, err := Backups(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != (Backup{Path: newer, Version: "v0.5.0-rc1", Stamp: "20261111110000"}) ||
		got[1] != (Backup{Path: older, Version: "v0.4.0", Stamp: "20261111100000"}) {
		t.Errorf("Backups: %+v", got)
	}
	if b := parseBackup(filepath.Join(dir, ".kephalaion-upgrade-backup-kaputt")); b.Version != "" {
		t.Errorf("unlesbarer Name: %+v", b)
	}
	if safeName("v1/../x*") != "v1_.._x_" || safeName("") != "unbekannt" {
		t.Errorf("safeName %q", safeName("v1/../x*"))
	}
	removed, err := RemoveBackups(dir)
	if err != nil || len(removed) != 2 {
		t.Errorf("entfernt %v, %v", removed, err)
	}
	assertNoTemp(t, dir)
}

// RemoveStale entfernt nur alte temporäre Dateien: eine frische bleibt
// (upgrade ohne --auto, ohne Sperre), eine alte Rückweg-Datei und die
// Sperrdatei auch.
func TestRemoveStale(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	old := now.Add(-2 * time.Hour)
	mk := func(name string, mtime time.Time) string {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mtime, mtime); err != nil {
			t.Fatal(err)
		}
		return p
	}
	stale := mk(".kephalaion-upgrade-123", old)
	fresh := mk(".kephalaion-upgrade-456", now.Add(-time.Minute))
	backup := mk(".kephalaion-upgrade-backup-v0.4.0-20261101000000-789", old.Add(-72*time.Hour))
	lock := mk(LockName, old)
	removed, err := RemoveStale(dir, time.Hour, now)
	if err != nil || len(removed) != 1 || removed[0] != stale {
		t.Errorf("entfernt %v, %v", removed, err)
	}
	for _, p := range []string{fresh, backup, lock} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s: %v", filepath.Base(p), err)
		}
	}
}
