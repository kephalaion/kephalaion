package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/kephalaion/kephalaion/internal/config"
)

// Runner führt ein Programm aus und liefert seine Standardausgabe. Endet es
// nicht mit 0, ist err gesetzt und nennt die Fehlerausgabe; stdout gilt
// trotzdem (systemctl is-active etwa). Tests ersetzen ihn.
type Runner func(ctx context.Context, name string, args ...string) (stdout string, err error)

// ExecRunner führt Programme wirklich aus.
func ExecRunner(ctx context.Context, name string, args ...string) (string, error) {
	return runCmd(exec.CommandContext(ctx, name, args...), name, args)
}

// OwnGroupRunner führt Programme aus wie ExecRunner, aber jedes in einer
// eigenen Prozessgruppe: Ein Signal an die Gruppe des Aufrufers, etwa Strg-C
// im Terminal, trifft es nicht. Endet ctx, beendet er die ganze Gruppe des
// Kindes. upgrade --auto nimmt ihn nach dem Ersetzen, damit Neustart und
// Rückweg zu Ende laufen; alle anderen Aufrufe bleiben bei ExecRunner, und
// Strg-C trifft ihre Kinder weiter.
func OwnGroupRunner(ctx context.Context, name string, args ...string) (string, error) {
	return runCmd(OwnGroup(exec.CommandContext(ctx, name, args...)), name, args)
}

// OwnGroup lässt cmd in einer eigenen Prozessgruppe starten und beim Ende
// seines Kontexts die ganze Gruppe beenden, nicht nur das Kind selbst.
// WaitDelay begrenzt das Warten auf Enkel, die seine Ausgabe noch offen
// halten.
func OwnGroup(cmd *exec.Cmd) *exec.Cmd {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		// Die Gruppe trägt die PID des Kindes (Setpgid ohne Pgid).
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = ownGroupWaitDelay
	return cmd
}

// ownGroupWaitDelay ist die Frist nach dem Beenden der Gruppe, bis Wait die
// Ausgabe aufgibt.
const ownGroupWaitDelay = 2 * time.Second

// runCmd führt cmd aus und liefert seine Standardausgabe; der Fehler nennt
// den Aufruf name args und die Fehlerausgabe.
func runCmd(cmd *exec.Cmd, name string, args []string) (string, error) {
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		call := strings.TrimSpace(name + " " + strings.Join(args, " "))
		if msg := strings.TrimSpace(errOut.String()); msg != "" {
			return out.String(), fmt.Errorf("%s: %w: %s", call, err, msg)
		}
		return out.String(), fmt.Errorf("%s: %w", call, err)
	}
	return out.String(), nil
}

// ErrNoSystemd meldet ein Linux ohne laufendes systemd: Alpine mit OpenRC,
// ein schlanker Container, WSL ohne systemd=true.
var ErrNoSystemd = errors.New("ohne systemd")

// ErrUnsupported meldet ein Betriebssystem, für das es keinen Dienst gibt.
var ErrUnsupported = errors.New("für dieses Betriebssystem gibt es keinen Dienst (nur Linux mit systemd und macOS)")

// Manager richtet den Dienst pro User ein und fragt ihn ab. Die Felder sind
// für Tests überschreibbar; New setzt die echten Werte.
type Manager struct {
	// OS ist runtime.GOOS: linux oder darwin.
	OS string
	// Home ist das Heimatverzeichnis.
	Home string
	// ConfigHome ist $XDG_CONFIG_HOME bzw. ~/.config: Dort liegt die
	// Benutzer-Unit unter systemd/user/.
	ConfigHome string
	// StateHome ist $XDG_STATE_HOME bzw. ~/.local/state: Dort schreibt der
	// LaunchAgent sein Log (kephalaion/serve.log).
	StateHome string
	// User und UID sind der aufrufende User.
	User string
	UID  int
	// SystemdDir gibt es, wenn systemd läuft (/run/systemd/system).
	SystemdDir string
	// LingerDir hält je User mit Linger eine Datei (/var/lib/systemd/linger).
	LingerDir string
	// WSL sagt, ob das System eine WSL ist.
	WSL bool
	// Settle ist die Pause nach dem Start, bevor install nachsieht, ob der
	// Dienst läuft.
	Settle time.Duration
	Run    Runner
}

// New liefert einen Manager für diesen Rechner und diesen User.
func New() *Manager {
	m := &Manager{
		OS:         runtime.GOOS,
		UID:        os.Getuid(),
		SystemdDir: "/run/systemd/system",
		LingerDir:  "/var/lib/systemd/linger",
		Settle:     time.Second,
		Run:        ExecRunner,
	}
	m.Home, _ = os.UserHomeDir()
	m.ConfigHome = xdgDir("XDG_CONFIG_HOME", m.Home, ".config")
	m.StateHome = xdgDir("XDG_STATE_HOME", m.Home, ".local", "state")
	if u, err := user.Current(); err == nil {
		m.User = u.Username
	} else {
		m.User = os.Getenv("USER")
	}
	m.WSL = detectWSL()
	return m
}

// xdgDir liefert das XDG-Verzeichnis aus env, wenn es absolut ist, sonst den
// Standard unter home; ohne home leer.
func xdgDir(env, home string, rel ...string) string {
	if v := os.Getenv(env); v != "" && filepath.IsAbs(v) {
		return v
	}
	if home == "" {
		return ""
	}
	return filepath.Join(append([]string{home}, rel...)...)
}

// detectWSL erkennt eine WSL an der Kernel-Version bzw. an WSL_DISTRO_NAME.
func detectWSL() bool {
	if os.Getenv("WSL_DISTRO_NAME") != "" {
		return true
	}
	b, err := os.ReadFile("/proc/sys/kernel/osrelease")
	return err == nil && strings.Contains(strings.ToLower(string(b)), "microsoft")
}

// HasSystemd sagt, ob systemd läuft.
func (m *Manager) HasSystemd() bool {
	fi, err := os.Stat(m.SystemdDir)
	return err == nil && fi.IsDir()
}

// Check sagt, ob sich auf diesem System ein Dienst pro User einrichten lässt:
// macOS immer, Linux nur mit systemd (sonst ErrNoSystemd).
func (m *Manager) Check() error {
	switch m.OS {
	case "darwin":
		return nil
	case "linux":
		if !m.HasSystemd() {
			return ErrNoSystemd
		}
		return nil
	}
	return ErrUnsupported
}

// Kind beschreibt den Dienst pro User für Meldungen.
func (m *Manager) Kind() string {
	if m.OS == "darwin" {
		return "LaunchAgent " + Label
	}
	return "systemd --user"
}

// UnitPath ist der Ort der Unit bzw. plist pro User.
func (m *Manager) UnitPath() string {
	if m.OS == "darwin" {
		return filepath.Join(m.Home, "Library", "LaunchAgents", Label+".plist")
	}
	return filepath.Join(m.ConfigHome, "systemd", "user", UnitName)
}

// LogPath ist das Log des LaunchAgent; unter Linux geht es ins Journal.
func (m *Manager) LogPath() string {
	return filepath.Join(m.StateHome, "kephalaion", "serve.log")
}

// LogHint sagt, wo das Log des Dienstes pro User steht.
func (m *Manager) LogHint() string {
	if m.OS == "darwin" {
		return m.LogPath()
	}
	return "journalctl --user -u kephalaion"
}

// Unit liefert die Unit bzw. plist pro User für den Aufruf args.
func (m *Manager) Unit(args []string) (string, error) {
	switch m.OS {
	case "darwin":
		return LaunchAgent(args, m.LogPath())
	case "linux":
		return UserUnit(args)
	}
	return "", ErrUnsupported
}

// Linger sagt, ob für den User Linger an ist: Dann laufen seine Dienste auch
// ohne Anmeldung.
func (m *Manager) Linger() bool {
	if m.User == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(m.LingerDir, m.User))
	return err == nil
}

// State ist der Zustand eines Dienstes.
type State struct {
	// Installed: Die Unit bzw. plist ist da (pro User: die Datei; global:
	// systemd kennt die Unit).
	Installed bool
	// Loaded: systemd bzw. launchd kennt den Dienst.
	Loaded bool
	// Running: Der Dienst läuft.
	Running bool
	// Path ist der Ort der Unit bzw. plist.
	Path string
	// Detail ist der Zustand in den Worten von systemd bzw. launchd, etwa
	// active, inactive, failed, activating oder running.
	Detail string
}

func (m *Manager) target() string {
	return fmt.Sprintf("gui/%d", m.UID)
}

func (m *Manager) service() string {
	return m.target() + "/" + Label
}

// UserState fragt den Dienst pro User ab.
func (m *Manager) UserState(ctx context.Context) (State, error) {
	if err := m.Check(); err != nil {
		return State{}, err
	}
	st := State{Path: m.UnitPath()}
	if _, err := os.Stat(st.Path); err == nil {
		st.Installed = true
	} else if !errors.Is(err, fs.ErrNotExist) {
		return State{}, err
	}
	if m.OS == "darwin" {
		out, err := m.Run(ctx, "launchctl", "print", m.service())
		if err != nil {
			// Nicht geladen: launchctl print scheitert.
			st.Detail = "nicht geladen"
			return st, nil
		}
		st.Loaded = true
		st.Detail = launchdState(out)
		st.Running = st.Detail == "running"
		return st, nil
	}
	props, err := m.show(ctx, "--user")
	if err != nil {
		return State{}, err
	}
	st.Loaded = props["LoadState"] == "loaded"
	st.Detail = props["ActiveState"]
	st.Running = st.Detail == "active"
	return st, nil
}

// SystemState fragt die System-Unit der globalen Installation ab — ohne root,
// nur unter Linux mit systemd.
func (m *Manager) SystemState(ctx context.Context) (State, error) {
	if m.OS != "linux" {
		return State{}, errors.New("eine globale Installation gibt es nur unter Linux")
	}
	if !m.HasSystemd() {
		return State{}, ErrNoSystemd
	}
	props, err := m.show(ctx)
	if err != nil {
		return State{}, err
	}
	st := State{
		Installed: props["LoadState"] == "loaded",
		Loaded:    props["LoadState"] == "loaded",
		Running:   props["ActiveState"] == "active",
		Path:      props["FragmentPath"],
		Detail:    props["ActiveState"],
	}
	return st, nil
}

// show liest LoadState, ActiveState und FragmentPath der Unit über
// systemctl show — auch für eine Unit, die es nicht gibt (LoadState
// not-found).
func (m *Manager) show(ctx context.Context, scope ...string) (map[string]string, error) {
	args := append(append([]string{}, scope...), "show", UnitName, "--property=LoadState,ActiveState,FragmentPath")
	out, err := m.Run(ctx, "systemctl", args...)
	if err != nil {
		return nil, err
	}
	props := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
			props[k] = v
		}
	}
	return props, nil
}

// launchdState liest den Zustand aus der Ausgabe von launchctl print: die
// Zeile „state = running“ auf oberster Ebene des Dienstes.
func launchdState(out string) string {
	for _, line := range strings.Split(out, "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok && strings.TrimSpace(k) == "state" {
			return strings.TrimSpace(v)
		}
	}
	return "unbekannt"
}

// Install schreibt die Unit bzw. plist für den Aufruf args, lädt sie und
// startet den Dienst; lief er schon, startet er ihn mit der neuen Unit neu.
// Danach wartet es Settle und liefert den Zustand.
func (m *Manager) Install(ctx context.Context, args []string) (State, error) {
	if err := m.Check(); err != nil {
		return State{}, err
	}
	text, err := m.Unit(args)
	if err != nil {
		return State{}, err
	}
	before, err := m.UserState(ctx)
	if err != nil {
		return State{}, err
	}
	path := m.UnitPath()
	if m.OS == "darwin" {
		if err := os.MkdirAll(filepath.Dir(m.LogPath()), 0o700); err != nil {
			return State{}, fmt.Errorf("Verzeichnis für das Log nicht anlegbar: %w", err)
		}
	}
	if err := config.WriteFileAtomic(path, []byte(text), 0o644); err != nil {
		return State{}, err
	}
	if m.OS == "darwin" {
		if before.Loaded {
			if _, err := m.Run(ctx, "launchctl", "bootout", m.service()); err != nil {
				return State{}, err
			}
		}
		// Ein früher per launchctl disable abgeschalteter Dienst ließe sich
		// sonst nicht laden.
		if _, err := m.Run(ctx, "launchctl", "enable", m.service()); err != nil {
			return State{}, err
		}
		if _, err := m.Run(ctx, "launchctl", "bootstrap", m.target(), path); err != nil {
			return State{}, err
		}
	} else {
		if _, err := m.Run(ctx, "systemctl", "--user", "daemon-reload"); err != nil {
			return State{}, err
		}
		if before.Running {
			if _, err := m.Run(ctx, "systemctl", "--user", "enable", UnitName); err != nil {
				return State{}, err
			}
			if _, err := m.Run(ctx, "systemctl", "--user", "restart", UnitName); err != nil {
				return State{}, err
			}
		} else if _, err := m.Run(ctx, "systemctl", "--user", "enable", "--now", UnitName); err != nil {
			return State{}, err
		}
	}
	if m.Settle > 0 {
		t := time.NewTimer(m.Settle)
		select {
		case <-ctx.Done():
			t.Stop()
			return State{}, ctx.Err()
		case <-t.C:
		}
	}
	return m.UserState(ctx)
}

// Uninstall hält den Dienst pro User an, schaltet ihn ab und entfernt die
// Unit bzw. plist. removed ist false, wenn es nichts zu entfernen gab.
func (m *Manager) Uninstall(ctx context.Context) (removed bool, err error) {
	if err := m.Check(); err != nil {
		return false, err
	}
	st, err := m.UserState(ctx)
	if err != nil {
		return false, err
	}
	if m.OS == "darwin" {
		if st.Loaded {
			if _, err := m.Run(ctx, "launchctl", "bootout", m.service()); err != nil {
				return false, err
			}
		}
		if !st.Installed {
			return st.Loaded, nil
		}
		if err := os.Remove(st.Path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return false, err
		}
		return true, nil
	}
	if !st.Installed && !st.Loaded {
		return false, nil
	}
	if _, err := m.Run(ctx, "systemctl", "--user", "disable", "--now", UnitName); err != nil {
		return false, err
	}
	if err := os.Remove(st.Path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	if _, err := m.Run(ctx, "systemctl", "--user", "daemon-reload"); err != nil {
		return false, err
	}
	return true, nil
}

// Restart startet den Dienst pro User neu, etwa nach einem Upgrade.
func (m *Manager) Restart(ctx context.Context) error {
	if err := m.Check(); err != nil {
		return err
	}
	if m.OS == "darwin" {
		_, err := m.Run(ctx, "launchctl", "kickstart", "-k", m.service())
		return err
	}
	_, err := m.Run(ctx, "systemctl", "--user", "restart", UnitName)
	return err
}

// RestartCommand ist der Befehl, mit dem Restart den Dienst neu startet —
// für Meldungen.
func (m *Manager) RestartCommand() string {
	if m.OS == "darwin" {
		return "launchctl kickstart -k " + m.service()
	}
	return "systemctl --user restart " + UnitName
}
