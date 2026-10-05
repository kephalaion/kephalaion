package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/kephalaion/kephalaion/internal/config"
	"github.com/kephalaion/kephalaion/internal/upgrade"
)

// newUpgrader liefert den Upgrader für dieses Binary; Tests lenken ihn auf
// einen nachgespielten GitHub.
var newUpgrader = upgrade.New

const upgradeUsage = `Aufruf:
  kephalaion upgrade [--version vX.Y.Z]
  kephalaion upgrade --check [--json | --version vX.Y.Z]

Ersetzt dieses Binary durch das neueste Release — nach Prüfung gegen
SHA256SUMS und atomar. Stuft nie von selbst zurück. Darf der Aufrufer im
Verzeichnis des Binarys nicht schreiben, bricht upgrade vor dem Download ab
und nennt den Weg: bei der globalen Installation der Verwalter (Ansible oder
sudo kephalaion upgrade && sudo systemctl restart kephalaion).

Nach dem Ersetzen startet upgrade einen laufenden Dienst pro User neu
(systemd --user bzw. LaunchAgent), damit er das neue Binary benutzt; läuft
der Dienst global, nennt es sudo systemctl restart kephalaion.

Danach installiert das neue Binary seine Erweiterung für VS Code neu, in
jedem Editor, in dem sie installiert ist (kephalaion vscode install --code
<cli>, je CLI einmal). Scheitert das, bleibt es bei einer Warnung mit dem
Befehl von Hand; der Exit-Code ändert sich dadurch nie. Bei der globalen
Installation und unter sudo nur der Hinweis, das als eigener User zu tun.

Optionen:
  --check            nur melden: installierte und neueste Version, ob sich
                     dieses Binary selbst ersetzen kann und wie das Upgrade
                     geht
  --json             mit --check: dieselben Angaben als JSON (state,
                     checked_at, version, dev_build, latest,
                     update_available, self_upgrade, method, command, hint,
                     error); siehe docs/installation.md
  --version vX.Y.Z   genau diese Version installieren, auch eine ältere oder
                     eine Vorabversion; nötig, um einen dev build zu ersetzen

Exit-Code:
  0   erledigt; mit --check: GitHub hat geantwortet, gleich ob es eine neue
      Version gibt
  1   gescheitert — kein Netz, Rate-Limit, kein Release, kein Schreibrecht,
      Prüfsumme; das Binary bleibt unverändert. Mit --check --json steht der
      Grund auch im JSON (state failed, error)
  2   falscher Aufruf
`

func runUpgrade(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("upgrade", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, upgradeUsage) }
	var opts upgrade.Options
	fs.BoolVar(&opts.Check, "check", false, "")
	fs.StringVar(&opts.Version, "version", "", "")
	asJSON := fs.Bool("json", false, "")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "Unerwartetes Argument: %s\n\n", fs.Arg(0))
		fmt.Fprint(stderr, upgradeUsage)
		return 2
	}
	if *asJSON && (!opts.Check || opts.Version != "") {
		fmt.Fprintf(stderr, "upgrade: --json gibt es nur mit --check und ohne --version\n\n")
		fmt.Fprint(stderr, upgradeUsage)
		return 2
	}

	// Ein Abbruch mit Strg-C oder SIGTERM beendet den Download, und upgrade
	// räumt die temporäre Datei weg, bevor das Programm endet.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	u := newUpgrader(stdout)
	u.System = systemConfigApplies()
	if *asJSON {
		r := u.Report(ctx)
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		enc.SetEscapeHTML(false) // command und hint enthalten &&
		if err := enc.Encode(r); err != nil {
			fmt.Fprintf(stderr, "upgrade: %v\n", err)
			return 1
		}
		if r.State != upgrade.StateOK {
			fmt.Fprintf(stderr, "upgrade: %s\n", r.Error)
			return 1
		}
		return 0
	}
	res, err := u.Run(ctx, opts)
	if err != nil {
		fmt.Fprintf(stderr, "upgrade: %v\n", err)
		return 1
	}
	if res.Replaced {
		code := restartAfterUpgrade(ctx, stdout, stderr)
		upgradeExtension(ctx, res.Exe, stdout, stderr)
		return code
	}
	return 0
}

// systemConfigApplies sagt, ob für diesen Aufruf die globale config gilt —
// über die Suche oder KEPHALAION_CONFIG. Lässt sich das nicht feststellen,
// gilt sie nicht.
func systemConfigApplies() bool {
	loc, err := config.Locate("")
	return err == nil && loc.System()
}

// restartAfterUpgrade startet einen laufenden Dienst pro User neu, damit er
// das neue Binary benutzt; läuft die System-Unit, nennt es nur den Befehl
// für den Verwalter.
func restartAfterUpgrade(ctx context.Context, stdout, stderr io.Writer) int {
	m := newServiceManager()
	if st, err := m.UserState(ctx); err == nil && st.Running {
		if err := m.Restart(ctx); err != nil {
			fmt.Fprintf(stderr, "upgrade: Das neue Binary ist installiert, der Dienst ließ sich aber nicht neu starten: %v\n"+
				"Von Hand: %s\n", err, m.RestartCommand())
			return 1
		}
		fmt.Fprintf(stdout, "Dienst neu gestartet (%s).\n", m.RestartCommand())
		return 0
	}
	if m.OS == "linux" && m.HasSystemd() {
		if st, err := m.SystemState(ctx); err == nil && st.Running {
			fmt.Fprintln(stdout, "Der Dienst läuft global (System-Unit) und benutzt das neue Binary erst nach: "+
				"sudo systemctl restart kephalaion")
		}
	}
	return 0
}
