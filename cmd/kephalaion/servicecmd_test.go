package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kephalaion/kephalaion/internal/config"
	"github.com/kephalaion/kephalaion/internal/service"
)

// noSystemd ist ein Manager für Linux ohne systemd; er ruft nie etwas auf.
func noSystemd(root string) *service.Manager {
	return &service.Manager{
		OS:         "linux",
		Home:       root,
		ConfigHome: filepath.Join(root, "config"),
		StateHome:  filepath.Join(root, "state"),
		User:       "anna",
		UID:        1000,
		SystemdDir: filepath.Join(root, "kein-systemd"),
		LingerDir:  filepath.Join(root, "linger"),
		Run: func(_ context.Context, name string, args ...string) (string, error) {
			return "", fmt.Errorf("im Test aufgerufen: %s %s", name, strings.Join(args, " "))
		},
	}
}

// fakeSystemd spielt systemd für den Dienst pro User und die System-Unit
// nach: enable --now und restart starten, disable --now hält an; show sagt
// loaded, wenn die Unit-Datei da ist.
type fakeSystemd struct {
	m          *service.Manager
	calls      []string
	active     bool
	system     string // ActiveState der System-Unit; leer: gibt es nicht
	failStart  bool
	restartErr error
	// onRestart läuft bei jedem restart, bevor er wirkt.
	onRestart func()
	// hangRestarts ist die Zahl der nächsten restarts, die hängen, bis ihr
	// Kontext endet; hangShow lässt jedes show --user so hängen.
	hangRestarts int
	hangShow     bool
}

func (f *fakeSystemd) run(ctx context.Context, name string, args ...string) (string, error) {
	call := strings.Join(append([]string{name}, args...), " ")
	f.calls = append(f.calls, call)
	switch {
	case strings.HasPrefix(call, "systemctl --user show ") && f.hangShow:
		<-ctx.Done()
		return "", ctx.Err()
	case strings.HasPrefix(call, "systemctl --user show "):
		load := "not-found"
		if _, err := os.Stat(f.m.UnitPath()); err == nil {
			load = "loaded"
		}
		state := "inactive"
		if f.active {
			state = "active"
		} else if f.failStart && load == "loaded" {
			state = "activating"
		}
		return fmt.Sprintf("LoadState=%s\nActiveState=%s\nFragmentPath=%s\n", load, state, f.m.UnitPath()), nil
	case strings.HasPrefix(call, "systemctl show "):
		if f.system == "" {
			return "LoadState=not-found\nActiveState=inactive\nFragmentPath=\n", nil
		}
		return "LoadState=loaded\nActiveState=" + f.system + "\nFragmentPath=/etc/systemd/system/kephalaion.service\n", nil
	case call == "systemctl --user enable --now kephalaion.service":
		f.active = !f.failStart
	case call == "systemctl --user restart kephalaion.service":
		if f.onRestart != nil {
			f.onRestart()
		}
		if f.hangRestarts > 0 {
			f.hangRestarts--
			<-ctx.Done()
			return "", ctx.Err()
		}
		if f.restartErr != nil {
			return "", f.restartErr
		}
		f.active = true
	case call == "systemctl --user disable --now kephalaion.service":
		f.active = false
	}
	return "", nil
}

// withSystemd ersetzt den Manager durch einen mit nachgespieltem systemd und
// das Binary durch einen festen Pfad.
func withSystemd(t *testing.T, dir string) *fakeSystemd {
	t.Helper()
	m := noSystemd(dir)
	m.SystemdDir = filepath.Join(dir, "run-systemd")
	if err := os.MkdirAll(m.SystemdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	f := &fakeSystemd{m: m}
	m.Run = f.run
	oldM, oldExe := newServiceManager, serviceExecutable
	newServiceManager = func() *service.Manager { return m }
	serviceExecutable = func() (string, error) { return "/opt/keph/bin/kephalaion", nil }
	t.Cleanup(func() { newServiceManager, serviceExecutable = oldM, oldExe })
	return f
}

func readText(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestServiceInstallLinux(t *testing.T) {
	dir := isolate(t)
	f := withSystemd(t, dir)
	runT(t, "service", "install").want(t, 1, "keine Rolle eingerichtet")
	runT(t, "node", "init").want(t, 0)

	r := runT(t, "service", "install")
	unit := filepath.Join(dir, "config", "systemd", "user", "kephalaion.service")
	cfgPath := filepath.Join(dir, "config", "kephalaion", "config.yaml")
	// XDG_CONFIG_HOME zeigt nicht auf ~/.config: Die Unit nennt die config.
	r.want(t, 0, "Dienst eingerichtet (systemd --user).", "Unit:   "+unit,
		"Aufruf: /opt/keph/bin/kephalaion serve --config "+cfgPath, "journalctl --user -u kephalaion", "läuft:  ja")
	if strings.Contains(r.out, "Linger") {
		t.Errorf("Hinweis auf Linger ohne Hub:\n%s", r.out)
	}
	want, _ := service.UserUnit([]string{"/opt/keph/bin/kephalaion", "serve", "--config", cfgPath})
	if got := readText(t, unit); got != want {
		t.Errorf("Unit:\n%s", got)
	}
	runT(t, "service", "status").want(t, 0, "Dienst: pro User (systemd --user)", "Unit:         "+unit,
		"eingerichtet: ja", "läuft:        ja (active)")
	runT(t, "status").want(t, 0, "Dienst: pro User (systemd --user), eingerichtet, läuft\n")

	// Der Dienst hält die Sperre: install schreibt neu und startet neu, statt
	// abzubrechen.
	lock, err := takeLock(filepath.Join(dir, "data", "kephalaion", "node.db.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	f.calls = nil
	runT(t, "service", "install").want(t, 0, "Dienst neu eingerichtet und neu gestartet")
	if !contains(strings.Join(f.calls, "\n"), "systemctl --user restart kephalaion.service") {
		t.Errorf("kein Neustart: %q", f.calls)
	}

	// Mit Hub: Hinweis auf Linger; unter WSL der Hinweis auf die VM.
	runT(t, "hub", "init").want(t, 0)
	f.m.WSL = true
	runT(t, "service", "install").want(t, 0, "loginctl enable-linger anna", "Unter WSL fährt die VM")
	if err := os.MkdirAll(f.m.LingerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.m.LingerDir, "anna"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if r := runT(t, "service", "install"); strings.Contains(r.out, "Linger") {
		t.Errorf("Hinweis auf Linger, obwohl an:\n%s", r.out)
	}

	lock.Close()
	f.calls = nil
	runT(t, "service", "uninstall").want(t, 0, "Dienst angehalten und entfernt: "+unit)
	if exists(unit) || f.active {
		t.Fatalf("Unit %v, aktiv %v", exists(unit), f.active)
	}
	runT(t, "service", "uninstall").want(t, 0, "Kein Dienst pro User eingerichtet")
	runT(t, "service", "status").want(t, 0, "eingerichtet: nein", "läuft:        nein (inactive)",
		"Einrichten: kephalaion service install")
	runT(t, "status").want(t, 0, "Dienst: pro User (systemd --user), nicht eingerichtet (kephalaion service install)")
}

// Liegt die config am Standardort ~/.config/kephalaion/config.yaml, nennt die
// Unit sie nicht: serve findet sie dort ohne Angabe.
func TestServiceInstallStandardConfig(t *testing.T) {
	dir := isolate(t)
	t.Setenv("XDG_CONFIG_HOME", "")
	withSystemd(t, dir)
	runT(t, "node", "init").want(t, 0)
	runT(t, "service", "install").want(t, 0, "Aufruf: /opt/keph/bin/kephalaion serve\n")
	runT(t, "service", "unit").want(t, 0, "ExecStart=/opt/keph/bin/kephalaion serve\n")
}

// Läuft serve von Hand, bricht install ab: Der Dienst startete sonst immer
// wieder und scheiterte an der Sperre.
func TestServiceInstallManualServe(t *testing.T) {
	dir := isolate(t)
	f := withSystemd(t, dir)
	runT(t, "node", "init").want(t, 0)
	lock, err := takeLock(filepath.Join(dir, "data", "kephalaion", "node.db.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	runT(t, "service", "install").want(t, 1, "kephalaion serve läuft schon, nicht als Dienst", "node.db.lock",
		"Erst dieses serve beenden")
	if exists(f.m.UnitPath()) {
		t.Fatal("Unit trotz laufendem serve geschrieben")
	}
	for _, c := range f.calls {
		if !strings.Contains(c, " show ") {
			t.Errorf("Aufruf trotz Abbruch: %s", c)
		}
	}
}

// Startet der Dienst nicht, sagt install das und endet mit 1.
func TestServiceInstallNotRunning(t *testing.T) {
	dir := isolate(t)
	f := withSystemd(t, dir)
	f.failStart = true
	runT(t, "node", "init").want(t, 0)
	runT(t, "service", "install").want(t, 1, "läuft:  nein (activating) — siehe journalctl --user -u kephalaion")
}

// Neben der globalen config bricht install ab, und zwar vor der Prüfung auf
// systemd; ohne systemd nennt es den Aufruf für einen eigenen Supervisor.
func TestServiceInstallRefuses(t *testing.T) {
	dir := isolate(t)
	runT(t, "node", "init").want(t, 0)
	r := runT(t, "service", "install")
	r.want(t, 1, "Ohne systemd", "Ein eigener Supervisor startet: kephalaion serve")
	runT(t, "service", "status").want(t, 1, "ohne systemd", "kephalaion serve")
	runT(t, "status").want(t, 0, "Dienst: ohne systemd\n")

	writeSystemConfig(t, filepath.Join(dir, "var"))
	runT(t, "service", "install").want(t, 1, "global eingerichtet ("+config.SystemPath+")", "service install gilt nur pro User")
	runT(t, "service", "install", "--config", filepath.Join(dir, "config", "kephalaion", "config.yaml")).
		want(t, 1, "global eingerichtet")
}

// Gilt die globale config, fragen status und service status die System-Unit
// ab, nicht systemctl --user.
func TestServiceStatusSystem(t *testing.T) {
	dir := isolate(t)
	f := withSystemd(t, dir)
	if err := config.Save(config.SystemPath, config.Config{}); err != nil {
		t.Fatal(err)
	}
	runT(t, "service", "status").want(t, 0, "Dienst: global (System-Unit kephalaion.service)", "eingerichtet: nein")
	runT(t, "status").want(t, 0, "Dienst: global (System-Unit kephalaion.service), nicht eingerichtet\n")
	f.system = "active"
	runT(t, "service", "status").want(t, 0, "Unit:         /etc/systemd/system/kephalaion.service", "eingerichtet: ja",
		"läuft:        ja (active)")
	runT(t, "status").want(t, 0, "Dienst: global (System-Unit kephalaion.service), eingerichtet, läuft\n")
	f.system = "failed"
	runT(t, "status").want(t, 0, "eingerichtet, läuft nicht (failed)")
	for _, c := range f.calls {
		if strings.Contains(c, "--user") {
			t.Errorf("systemctl --user bei globaler config: %s", c)
		}
	}
}

func TestServiceUnit(t *testing.T) {
	isolate(t)
	runT(t, "service", "unit", "--system").want(t, 0, service.SystemUnit())
	r := runT(t, "service", "unit", "--system")
	if r.out != service.SystemUnit() {
		t.Errorf("unit --system:\n%s", r.out)
	}
	runT(t, "service", "unit", "--system", "--config", "/x").want(t, 2, "--config gibt es nicht mit --system")
	serviceExecutable = func() (string, error) { return "/opt/k", nil }
	t.Cleanup(func() { serviceExecutable = os.Executable })
	runT(t, "service", "unit", "--config", "/srv/k.yaml").want(t, 0, "[Service]",
		"ExecStart=/opt/k serve --config /srv/k.yaml", "WantedBy=default.target")
	runT(t, "service").want(t, 2, "kephalaion service install")
	runT(t, "service", "gibtsnicht").want(t, 2, "Unbekanntes Kommando: service gibtsnicht")
	runT(t, "service", "install", "--help").want(t, 0, "loginctl enable-linger")
}

// Der Fehler von systemctl kommt bei install an.
func TestServiceInstallError(t *testing.T) {
	dir := isolate(t)
	f := withSystemd(t, dir)
	runT(t, "node", "init").want(t, 0)
	f.active = true
	f.restartErr = errors.New("systemctl --user restart kephalaion.service: exit status 1: Job failed")
	runT(t, "service", "install").want(t, 1, "Job failed")
}
