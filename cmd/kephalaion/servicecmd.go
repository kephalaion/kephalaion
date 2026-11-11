package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/kephalaion/kephalaion/internal/config"
	"github.com/kephalaion/kephalaion/internal/service"
)

// newServiceManager liefert den Manager des Dienstes; Tests ersetzen ihn, damit
// kein Test systemctl oder launchctl aufruft.
var newServiceManager = service.New

// serviceExecutable liefert das Binary, das der Dienst startet; Tests setzen
// es.
var serviceExecutable = os.Executable

const serviceUsage = `Aufruf:
  kephalaion service install [--config pfad]
  kephalaion service uninstall
  kephalaion service status [--config pfad]
  kephalaion service unit [--system] [--config pfad]

Der Dienst startet kephalaion serve und hält es am Laufen: pro User unter
Linux eine Benutzer-Unit für systemd --user
(~/.config/systemd/user/kephalaion.service), auf macOS ein LaunchAgent
(~/Library/LaunchAgents/io.github.kephalaion.plist). Für die globale
Installation (nur Linux) gibt service unit --system die System-Unit aus; sie
legt der Verwalter bzw. Ansible ab.

Kommandos:
  install     schreibt die Unit bzw. den LaunchAgent für dieses Binary, lädt
              und startet den Dienst; läuft er schon, mit der neuen Unit neu
  uninstall   hält den Dienst pro User an und entfernt ihn
  status      eingerichtet ja/nein, Ort der Unit, läuft ja/nein; gilt die
              globale config, die System-Unit
  unit        gibt die Unit bzw. den LaunchAgent aus, den install schreiben
              würde; mit --system die System-Unit der globalen Installation

Hilfe: kephalaion service install --help usw.
`

const serviceInstallUsage = `Aufruf:
  kephalaion service install [--config pfad]

Richtet den Dienst pro User ein: unter Linux die Benutzer-Unit
~/.config/systemd/user/kephalaion.service (bzw. unter $XDG_CONFIG_HOME) mit
ExecStart=<dieses Binary> serve, dazu --config, wenn die config nicht unter
~/.config/kephalaion/config.yaml liegt; danach systemctl --user daemon-reload
und enable --now. Auf macOS den LaunchAgent io.github.kephalaion mit Log nach
~/.local/state/kephalaion/serve.log, geladen mit launchctl bootstrap. Läuft der
Dienst schon, schreibt install die Unit neu und startet ihn neu.

Bricht ab,
  - wenn Kephalaion auf diesem Rechner global eingerichtet ist
    (/etc/kephalaion/config.yaml) — der Dienst ist dann die System-Unit;
  - ohne systemd (Alpine, Container, WSL ohne systemd=true): dann startet ein
    eigener Supervisor kephalaion serve;
  - wenn keine Rolle eingerichtet ist;
  - wenn kephalaion serve schon läuft, nicht als Dienst (Sperre <db>.lock) —
    sonst startete der Dienst immer wieder und scheiterte an der Sperre.

Linger schaltet install nicht ein: Ohne Linger laufen die Dienste eines Users
nur, solange er angemeldet ist. Für den Node reicht das; ist ein Hub
eingerichtet, den andere Rechner erreichen sollen, nennt install
loginctl enable-linger.

Log: journalctl --user -u kephalaion, auf macOS die Datei oben.

Optionen:
  --config pfad   Ort der config (siehe kephalaion hub init --help)
`

const serviceUninstallUsage = `Aufruf:
  kephalaion service uninstall

Hält den Dienst pro User an, schaltet ihn ab und entfernt die Unit bzw. den
LaunchAgent (systemctl --user disable --now, daemon-reload; auf macOS
launchctl bootout). Die config und die Datenbanken bleiben. Gibt es keinen
Dienst, ist das kein Fehler.
`

const serviceStatusUsage = `Aufruf:
  kephalaion service status [--config pfad]

Zeigt, ob der Dienst eingerichtet ist, wo seine Unit liegt und ob er läuft.
Gilt die globale config, fragt es die System-Unit ab (systemctl show
kephalaion.service, ohne root), sonst den Dienst pro User. Ohne systemd sagt
es das und nennt den Aufruf für einen eigenen Supervisor, Exit-Code 1.

Optionen:
  --config pfad   Ort der config (siehe kephalaion hub init --help)
`

const serviceUnitUsage = `Aufruf:
  kephalaion service unit [--config pfad]
  kephalaion service unit --system

Gibt aus, was install schreiben würde: unter Linux die Benutzer-Unit, auf
macOS den LaunchAgent (plist). Schreibt nichts.

Mit --system die System-Unit der globalen Installation (Linux):
User=kephalaion, KEPHALAION_CONFIG=/etc/kephalaion/config.yaml,
ExecStart=/usr/local/bin/kephalaion serve, StateDirectory=kephalaion (0700),
gehärtet. Abzulegen als /etc/systemd/system/kephalaion.service, siehe
docs/installation.md.

Optionen:
  --system        die System-Unit
  --config pfad   Ort der config (siehe kephalaion hub init --help)
`

func runService(args []string, stdout, stderr io.Writer) int {
	return dispatch("service", serviceUsage, args, stdout, stderr, map[string]func([]string) int{
		"install":   func(a []string) int { return runServiceInstall(a, stdout, stderr) },
		"uninstall": func(a []string) int { return runServiceUninstall(a, stdout, stderr) },
		"status":    func(a []string) int { return runServiceStatus(a, stdout, stderr) },
		"unit":      func(a []string) int { return runServiceUnit(a, stdout, stderr) },
	})
}

// serveCall ist der Aufruf, den der Dienst pro User startet: dieses Binary
// mit serve, dazu --config, wenn die config nicht am Standardort liegt — den
// findet serve ohne Angabe, auch wenn die Umgebung des Dienstes
// XDG_CONFIG_HOME nicht kennt.
func serveCall(loc config.Location) ([]string, error) {
	exe, err := serviceExecutable()
	if err != nil {
		return nil, fmt.Errorf("Pfad dieses Binarys nicht ermittelbar: %w", err)
	}
	return serveCallFor(exe, loc), nil
}

// serveCallFor ist der Aufruf aus serveCall für das Binary exe — der
// Maßstab, an dem upgrade --auto die installierte Unit misst (Task 036).
func serveCallFor(exe string, loc config.Location) []string {
	args := []string{exe, "serve"}
	if std, err := config.DefaultUserPath(); err != nil || loc.Path != std {
		args = append(args, "--config", loc.Path)
	}
	return args
}

// noSystemdHint ist der Weg ohne systemd: serve unter einem eigenen
// Supervisor.
func noSystemdHint(loc config.Location) string {
	call := "kephalaion serve"
	if loc.Explicit() || loc.System() {
		call += " --config " + loc.Path
	}
	return "Ohne systemd (Alpine, Container, WSL ohne systemd=true in /etc/wsl.conf) richtet Kephalaion keinen Dienst " +
		"ein. Ein eigener Supervisor startet: " + call
}

func runServiceInstall(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("service install", serviceInstallUsage, stderr)
	cfgFlag := fs.String("config", "", "")
	if _, code, ok := parseFlags(fs, args, serviceInstallUsage, 0, stderr); !ok {
		return code
	}
	fail := func(format string, a ...any) int {
		fmt.Fprintf(stderr, "service install: "+format+"\n", a...)
		return 1
	}
	loc, err := config.Locate(*cfgFlag)
	if err != nil {
		return fail("%v", err)
	}
	// Vor der Prüfung auf systemd: Neben einer globalen Installation gibt es
	// keine pro User, auch nicht auf einem Rechner ohne systemd.
	if loc.SystemExists {
		return fail("Kephalaion ist auf diesem Rechner global eingerichtet (%s); der Dienst ist die System-Unit, "+
			"eingerichtet vom Verwalter (docs/installation.md). service install gilt nur pro User.", config.SystemPath)
	}
	m := newServiceManager()
	if err := m.Check(); err != nil {
		if errors.Is(err, service.ErrNoSystemd) {
			return fail("%s", noSystemdHint(loc))
		}
		return fail("%v", err)
	}
	cfg, exists, err := config.Load(loc.Path)
	if err != nil {
		return fail("%v", err)
	}
	if !exists || cfg.Empty() {
		return fail("keine Rolle eingerichtet (config %s); zuerst kephalaion hub init bzw. kephalaion node init", loc.Path)
	}
	ctx := context.Background()
	before, err := m.UserState(ctx)
	if err != nil {
		return fail("%v", err)
	}
	if !before.Running {
		for _, r := range config.Roles {
			sec := cfg.Section(r)
			if sec == nil {
				continue
			}
			addr, err := config.ParseDB(sec.DB)
			if err != nil || addr.Kind != config.SQLite {
				continue
			}
			if running, _ := serveRunning(addr.Path); running {
				return fail("kephalaion serve läuft schon, nicht als Dienst (%s: Sperre %s). Erst dieses serve beenden, "+
					"dann service install — sonst startete der Dienst immer wieder und scheiterte an der Sperre.",
					r, lockPath(addr.Path))
			}
		}
	}
	call, err := serveCall(loc)
	if err != nil {
		return fail("%v", err)
	}
	st, err := m.Install(ctx, call)
	if err != nil {
		return fail("%v", err)
	}
	verb := "eingerichtet"
	if before.Running {
		verb = "neu eingerichtet und neu gestartet"
	}
	fmt.Fprintf(stdout, "Dienst %s (%s).\n", verb, m.Kind())
	fmt.Fprintf(stdout, "  Unit:   %s\n", st.Path)
	fmt.Fprintf(stdout, "  Aufruf: %s\n", strings.Join(call, " "))
	fmt.Fprintf(stdout, "  Log:    %s\n", m.LogHint())
	code := 0
	if st.Running {
		fmt.Fprintf(stdout, "  läuft:  ja\n")
	} else {
		fmt.Fprintf(stdout, "  läuft:  nein (%s) — siehe %s\n", orDash(st.Detail), m.LogHint())
		code = 1
	}
	if m.OS == "linux" && cfg.Hub != nil && !m.Linger() {
		fmt.Fprintf(stdout, "Hinweis: Linger ist aus — ohne Linger laufen die Dienste eines Users nur, solange er angemeldet "+
			"ist. Für den Node reicht das; für einen Hub, den andere Rechner erreichen sollen, nicht: loginctl "+
			"enable-linger %s\n", m.User)
	}
	if m.WSL {
		fmt.Fprintln(stdout, "Hinweis: Unter WSL fährt die VM ohne offenes Terminal bzw. VS Code herunter; ein laufender "+
			"Dienst hält sie nicht wach.")
	}
	return code
}

func runServiceUninstall(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("service uninstall", serviceUninstallUsage, stderr)
	if _, code, ok := parseFlags(fs, args, serviceUninstallUsage, 0, stderr); !ok {
		return code
	}
	m := newServiceManager()
	if err := m.Check(); err != nil {
		if errors.Is(err, service.ErrNoSystemd) {
			fmt.Fprintf(stderr, "service uninstall: ohne systemd gibt es keinen Dienst pro User\n")
			return 1
		}
		fmt.Fprintf(stderr, "service uninstall: %v\n", err)
		return 1
	}
	removed, err := m.Uninstall(context.Background())
	if err != nil {
		fmt.Fprintf(stderr, "service uninstall: %v\n", err)
		return 1
	}
	if !removed {
		fmt.Fprintf(stdout, "Kein Dienst pro User eingerichtet (%s fehlt).\n", m.UnitPath())
		return 0
	}
	fmt.Fprintf(stdout, "Dienst angehalten und entfernt: %s\n", m.UnitPath())
	fmt.Fprintln(stdout, "config und Datenbanken bleiben; kephalaion serve läuft jetzt nicht mehr.")
	return 0
}

func runServiceStatus(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("service status", serviceStatusUsage, stderr)
	cfgFlag := fs.String("config", "", "")
	if _, code, ok := parseFlags(fs, args, serviceStatusUsage, 0, stderr); !ok {
		return code
	}
	loc, err := config.Locate(*cfgFlag)
	if err != nil {
		fmt.Fprintf(stderr, "service status: %v\n", err)
		return 1
	}
	m := newServiceManager()
	ctx := context.Background()
	var st service.State
	if loc.System() {
		fmt.Fprintf(stdout, "Dienst: global (System-Unit %s)\n", service.UnitName)
		st, err = m.SystemState(ctx)
	} else {
		fmt.Fprintf(stdout, "Dienst: pro User (%s)\n", m.Kind())
		st, err = m.UserState(ctx)
	}
	if errors.Is(err, service.ErrNoSystemd) {
		fmt.Fprintf(stdout, "  ohne systemd\n")
		fmt.Fprintf(stderr, "service status: %s\n", noSystemdHint(loc))
		return 1
	}
	if err != nil {
		fmt.Fprintf(stderr, "service status: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "  Unit:         %s\n", orDash(st.Path))
	fmt.Fprintf(stdout, "  eingerichtet: %s\n", yesNo(st.Installed))
	running := yesNo(st.Running)
	if st.Detail != "" {
		running += " (" + st.Detail + ")"
	}
	fmt.Fprintf(stdout, "  läuft:        %s\n", running)
	if !st.Installed && !loc.System() {
		fmt.Fprintln(stdout, "Einrichten: kephalaion service install")
	}
	return 0
}

func runServiceUnit(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("service unit", serviceUnitUsage, stderr)
	cfgFlag := fs.String("config", "", "")
	system := fs.Bool("system", false, "")
	if _, code, ok := parseFlags(fs, args, serviceUnitUsage, 0, stderr); !ok {
		return code
	}
	if *system {
		if *cfgFlag != "" {
			fmt.Fprintf(stderr, "service unit: --config gibt es nicht mit --system; die System-Unit nennt immer %s\n",
				config.SystemConfig)
			return 2
		}
		fmt.Fprint(stdout, service.SystemUnit())
		return 0
	}
	loc, err := config.Locate(*cfgFlag)
	if err != nil {
		fmt.Fprintf(stderr, "service unit: %v\n", err)
		return 1
	}
	call, err := serveCall(loc)
	if err != nil {
		fmt.Fprintf(stderr, "service unit: %v\n", err)
		return 1
	}
	text, err := newServiceManager().Unit(call)
	if err != nil {
		fmt.Fprintf(stderr, "service unit: %v\n", err)
		return 1
	}
	fmt.Fprint(stdout, text)
	return 0
}

func yesNo(b bool) string {
	if b {
		return "ja"
	}
	return "nein"
}

// serviceLine ist die Zeile Dienst in status: pro User bzw. global, ob
// eingerichtet und ob er läuft. Sie ändert den Exit-Code von status nie.
func serviceLine(ctx context.Context, loc config.Location) string {
	m := newServiceManager()
	var st service.State
	var err error
	var kind string
	if loc.System() {
		kind = "global (System-Unit " + service.UnitName + ")"
		st, err = m.SystemState(ctx)
	} else {
		kind = "pro User (" + m.Kind() + ")"
		st, err = m.UserState(ctx)
	}
	switch {
	case errors.Is(err, service.ErrNoSystemd):
		return "ohne systemd"
	case errors.Is(err, service.ErrUnsupported):
		return "auf diesem Betriebssystem keiner"
	case err != nil:
		return fmt.Sprintf("%s, unbekannt (%v)", kind, err)
	case !st.Installed && !st.Running:
		if loc.System() {
			return kind + ", nicht eingerichtet"
		}
		return kind + ", nicht eingerichtet (kephalaion service install)"
	case st.Running:
		return kind + ", eingerichtet, läuft"
	}
	return fmt.Sprintf("%s, eingerichtet, läuft nicht (%s)", kind, orDash(st.Detail))
}
