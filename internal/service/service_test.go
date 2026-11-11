package service

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "schreibt die Dateien unter testdata/ neu")

// golden vergleicht got mit testdata/<name>; mit -update schreibt es sie.
func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("%s weicht ab:\n--- erhalten\n%s\n--- erwartet\n%s", name, got, want)
	}
}

func TestUserUnitGolden(t *testing.T) {
	unit, err := UserUnit([]string{"/home/anna/.local/bin/kephalaion", "serve"})
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "user.service", unit)

	unit, err = UserUnit([]string{"/home/anna/.local/bin/kephalaion", "serve", "--config", "/home/anna/Meine Daten/100%/k$.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "user-config.service", unit)
}

func TestSystemUnitGolden(t *testing.T) {
	golden(t, "system.service", SystemUnit())
}

func TestLaunchAgentGolden(t *testing.T) {
	plist, err := LaunchAgent([]string{"/Users/anna/.local/bin/kephalaion", "serve", "--config", "/Users/anna/a&b <c>.yaml"},
		"/Users/anna/.local/state/kephalaion/serve.log")
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "launchagent.plist", plist)
}

func TestUnitRejects(t *testing.T) {
	for _, args := range [][]string{nil, {"kephalaion", "serve"}, {"/bin/kephalaion", "serve\n[Service]"}} {
		if _, err := UserUnit(args); err == nil {
			t.Errorf("UserUnit(%q) angenommen", args)
		}
		if _, err := LaunchAgent(args, "/tmp/log"); err == nil {
			t.Errorf("LaunchAgent(%q) angenommen", args)
		}
	}
}

// fakeRun spielt systemctl bzw. launchctl nach: Es merkt sich die Aufrufe und
// antwortet aus answers (Aufruf ohne Programmnamen → Ausgabe); ein Aufruf
// in fail scheitert.
type fakeRun struct {
	calls   []string
	answers map[string]string
	fail    map[string]bool
}

func (f *fakeRun) run(_ context.Context, name string, args ...string) (string, error) {
	call := strings.Join(append([]string{name}, args...), " ")
	f.calls = append(f.calls, call)
	if f.fail[call] {
		return f.answers[call], fmt.Errorf("%s: exit status 1", call)
	}
	return f.answers[call], nil
}

const showUser = "systemctl --user show kephalaion.service --property=LoadState,ActiveState,FragmentPath"

func newTestManager(t *testing.T, goos string) (*Manager, *fakeRun) {
	t.Helper()
	root := t.TempDir()
	f := &fakeRun{answers: map[string]string{}, fail: map[string]bool{}}
	m := &Manager{
		OS:         goos,
		Home:       filepath.Join(root, "home"),
		ConfigHome: filepath.Join(root, "home", ".config"),
		StateHome:  filepath.Join(root, "home", ".local", "state"),
		User:       "anna",
		UID:        1000,
		SystemdDir: filepath.Join(root, "run", "systemd", "system"),
		LingerDir:  filepath.Join(root, "var", "lib", "systemd", "linger"),
		Run:        f.run,
	}
	if err := os.MkdirAll(m.SystemdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	return m, f
}

func TestInstallLinux(t *testing.T) {
	m, f := newTestManager(t, "linux")
	f.answers[showUser] = "LoadState=not-found\nActiveState=inactive\nFragmentPath=\n"
	args := []string{"/home/anna/.local/bin/kephalaion", "serve"}
	st, err := m.Install(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{showUser, "systemctl --user daemon-reload", "systemctl --user enable --now kephalaion.service", showUser}
	if !reflect.DeepEqual(f.calls, want) {
		t.Errorf("Aufrufe\n%q\nerwartet\n%q", f.calls, want)
	}
	path := filepath.Join(m.ConfigHome, "systemd", "user", "kephalaion.service")
	if st.Path != path || !st.Installed {
		t.Errorf("Zustand %+v", st)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	unit, _ := UserUnit(args)
	if string(data) != unit {
		t.Errorf("Unit:\n%s", data)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o644 {
		t.Errorf("Rechte %v", fi.Mode().Perm())
	}

	// Läuft er schon: neu schreiben, enable, restart.
	f.calls = nil
	f.answers[showUser] = "LoadState=loaded\nActiveState=active\nFragmentPath=" + path + "\n"
	st, err = m.Install(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	want = []string{showUser, "systemctl --user daemon-reload", "systemctl --user enable kephalaion.service",
		"systemctl --user restart kephalaion.service", showUser}
	if !reflect.DeepEqual(f.calls, want) || !st.Running || st.Detail != "active" {
		t.Errorf("Neu einrichten: %q, %+v", f.calls, st)
	}

	// Ein Fehler von systemctl kommt an.
	f.fail["systemctl --user daemon-reload"] = true
	if _, err := m.Install(context.Background(), args); err == nil || !strings.Contains(err.Error(), "daemon-reload") {
		t.Errorf("Fehler %v", err)
	}
}

func TestUninstallLinux(t *testing.T) {
	m, f := newTestManager(t, "linux")
	f.answers[showUser] = "LoadState=not-found\nActiveState=inactive\nFragmentPath=\n"
	removed, err := m.Uninstall(context.Background())
	if err != nil || removed {
		t.Fatalf("nichts eingerichtet: %v %v", removed, err)
	}
	if !reflect.DeepEqual(f.calls, []string{showUser}) {
		t.Errorf("Aufrufe %q", f.calls)
	}

	if _, err := m.Install(context.Background(), []string{"/bin/kephalaion", "serve"}); err != nil {
		t.Fatal(err)
	}
	f.calls = nil
	f.answers[showUser] = "LoadState=loaded\nActiveState=active\nFragmentPath=" + m.UnitPath() + "\n"
	removed, err = m.Uninstall(context.Background())
	if err != nil || !removed {
		t.Fatalf("Entfernen: %v %v", removed, err)
	}
	want := []string{showUser, "systemctl --user disable --now kephalaion.service", "systemctl --user daemon-reload"}
	if !reflect.DeepEqual(f.calls, want) {
		t.Errorf("Aufrufe\n%q\nerwartet\n%q", f.calls, want)
	}
	if _, err := os.Stat(m.UnitPath()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Unit liegt noch da: %v", err)
	}
}

func TestStateLinux(t *testing.T) {
	m, f := newTestManager(t, "linux")
	f.answers[showUser] = "LoadState=loaded\nActiveState=failed\nFragmentPath=/x\n"
	st, err := m.UserState(context.Background())
	if err != nil || st.Installed || st.Running || !st.Loaded || st.Detail != "failed" {
		t.Errorf("pro User: %+v %v", st, err)
	}
	f.answers["systemctl show kephalaion.service --property=LoadState,ActiveState,FragmentPath"] =
		"LoadState=loaded\nActiveState=active\nFragmentPath=/etc/systemd/system/kephalaion.service\n"
	st, err = m.SystemState(context.Background())
	if err != nil || !st.Installed || !st.Running || st.Path != "/etc/systemd/system/kephalaion.service" {
		t.Errorf("global: %+v %v", st, err)
	}
	// Ohne systemd.
	if err := os.Remove(m.SystemdDir); err != nil {
		t.Fatal(err)
	}
	if _, err := m.UserState(context.Background()); !errors.Is(err, ErrNoSystemd) {
		t.Errorf("ohne systemd: %v", err)
	}
	if _, err := m.SystemState(context.Background()); !errors.Is(err, ErrNoSystemd) {
		t.Errorf("global ohne systemd: %v", err)
	}
	if _, err := m.Install(context.Background(), []string{"/bin/k", "serve"}); !errors.Is(err, ErrNoSystemd) {
		t.Errorf("install ohne systemd: %v", err)
	}
	if _, err := os.Stat(m.UnitPath()); !errors.Is(err, os.ErrNotExist) {
		t.Error("ohne systemd eine Unit geschrieben")
	}
}

func TestLinger(t *testing.T) {
	m, _ := newTestManager(t, "linux")
	if m.Linger() {
		t.Fatal("Linger ohne Datei")
	}
	if err := os.MkdirAll(m.LingerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(m.LingerDir, "anna"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if !m.Linger() {
		t.Fatal("Linger nicht erkannt")
	}
}

const printAgent = "launchctl print gui/1000/io.github.kephalaion"

func TestInstallDarwin(t *testing.T) {
	m, f := newTestManager(t, "darwin")
	f.fail[printAgent] = true // nicht geladen
	args := []string{"/Users/anna/.local/bin/kephalaion", "serve"}
	if _, err := m.Install(context.Background(), args); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(m.Home, "Library", "LaunchAgents", "io.github.kephalaion.plist")
	want := []string{printAgent, "launchctl enable gui/1000/io.github.kephalaion",
		"launchctl bootstrap gui/1000 " + path, printAgent}
	if !reflect.DeepEqual(f.calls, want) {
		t.Errorf("Aufrufe\n%q\nerwartet\n%q", f.calls, want)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	plist, _ := LaunchAgent(args, filepath.Join(m.StateHome, "kephalaion", "serve.log"))
	if string(data) != plist {
		t.Errorf("plist:\n%s", data)
	}
	if fi, err := os.Stat(filepath.Join(m.StateHome, "kephalaion")); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("Verzeichnis des Logs: %v %v", fi, err)
	}

	// Geladen und läuft: erst bootout, dann neu laden.
	f.calls = nil
	delete(f.fail, printAgent)
	f.answers[printAgent] = "gui/1000/io.github.kephalaion = {\n\tactive count = 1\n\tstate = running\n}\n"
	st, err := m.Install(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	want = []string{printAgent, "launchctl bootout gui/1000/io.github.kephalaion",
		"launchctl enable gui/1000/io.github.kephalaion", "launchctl bootstrap gui/1000 " + path, printAgent}
	if !reflect.DeepEqual(f.calls, want) || !st.Running || !st.Installed {
		t.Errorf("Neu einrichten: %q %+v", f.calls, st)
	}

	f.calls = nil
	if err := m.Restart(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.calls, []string{"launchctl kickstart -k gui/1000/io.github.kephalaion"}) {
		t.Errorf("Neustart: %q", f.calls)
	}

	f.calls = nil
	removed, err := m.Uninstall(context.Background())
	if err != nil || !removed {
		t.Fatalf("Entfernen: %v %v", removed, err)
	}
	if !reflect.DeepEqual(f.calls, []string{printAgent, "launchctl bootout gui/1000/io.github.kephalaion"}) {
		t.Errorf("Entfernen: %q", f.calls)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Error("plist liegt noch da")
	}
}

func TestUnsupportedOS(t *testing.T) {
	m, _ := newTestManager(t, "windows")
	if err := m.Check(); !errors.Is(err, ErrUnsupported) {
		t.Errorf("windows: %v", err)
	}
	if _, err := m.Unit([]string{"/k", "serve"}); !errors.Is(err, ErrUnsupported) {
		t.Errorf("Unit auf windows: %v", err)
	}
}

// InstalledCall liest den Aufruf zurück, den UserUnit bzw. LaunchAgent
// geschrieben hat — auch mit Leerraum, Anführungszeichen, Backslash, % und $
// bzw. XML-Zeichen im Pfad (Task 036).
func TestInstalledCall(t *testing.T) {
	calls := [][]string{
		{"/home/anna/.local/bin/kephalaion", "serve"},
		{"/home/anna/.local/bin/kephalaion", "serve", "--config", "/home/anna/Meine Daten/100%/k$.yaml"},
		{"/opt/a \"b\"\\c/kephalaion", "serve", "--config", "/x/a&b <c>.yaml"},
	}
	for _, osName := range []string{"linux", "darwin"} {
		m, _ := newTestManager(t, osName)
		if _, err := m.InstalledCall(); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s ohne Unit: %v", osName, err)
		}
		for _, call := range calls {
			text, err := m.Unit(call)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(m.UnitPath()), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(m.UnitPath(), []byte(text), 0o644); err != nil {
				t.Fatal(err)
			}
			got, err := m.InstalledCall()
			if err != nil || !reflect.DeepEqual(got, call) {
				t.Errorf("%s: %q, %v; erwartet %q", osName, got, err, call)
			}
		}
	}
	// Die Dateien unter testdata/ wie geschrieben.
	for name, want := range map[string][]string{
		"user-config.service": calls[1],
		"launchagent.plist":   {"/Users/anna/.local/bin/kephalaion", "serve", "--config", "/Users/anna/a&b <c>.yaml"},
	} {
		data, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		if strings.HasSuffix(name, ".plist") {
			got, err = plistArguments(data)
		} else {
			got, err = parseExecLine(strings.TrimPrefix(strings.Split(string(data), "\n")[6], "ExecStart="))
		}
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("%s: %q, %v", name, got, err)
		}
	}
	for _, bad := range []string{"", "   ", `/bin/k "offen`} {
		if got, err := parseExecLine(bad); err == nil {
			t.Errorf("ExecStart %q: %q ohne Fehler", bad, got)
		}
	}
	if _, err := plistArguments([]byte(`<plist><dict><key>Label</key><string>x</string></dict></plist>`)); err == nil {
		t.Error("plist ohne ProgramArguments ohne Fehler")
	}
}
