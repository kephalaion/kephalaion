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
  kephalaion upgrade --auto [--json]

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

Mit --auto prüft upgrade erst, ob ein Upgrade ansteht und ob es ohne
Menschen gehen darf, und führt es nur dann aus — für k-playbook und Skripte.
Übersprungen wird ohne Änderung: ein dev build, die globale config (das
macht der Verwalter), ein Hub in der config, kein Schreibrecht, ein Node, der
nicht als Dienst pro User mit diesem Binary läuft, ein Hub des Nodes, der
kein mindestens so neues Release nennt, ein anderer Lauf mit der Sperre und
ein Veto der Probe des neuen Binarys (etwa eine neue Schemafassung). Läuft
der Dienst nach dem Neustart nicht mit der neuen Version an, kommt das alte
Binary zurück. Das Ergebnis — outcome upgraded, current, skipped oder
failed, dazu reason und hint — steht in Zeilen oder mit --json als JSON;
siehe docs/installation.md, „Upgrade: was Kephalaion meldet“.

SIGTERM, SIGINT oder SIGHUP vor dem Ersetzen brechen --auto ab: failed mit
reason interrupted, nichts verändert. Danach wird das Signal nur vermerkt:
Neustart, Prüfung und Rückweg laufen zu Ende, mit laufendem Dienst höchstens
75 s, ohne höchstens 5 s; eine Zeile auf stderr sagt das sofort.

Optionen:
  --check            nur melden: installierte und neueste Version, ob sich
                     dieses Binary selbst ersetzen kann und wie das Upgrade
                     geht
  --json             mit --check: dieselben Angaben als JSON (state,
                     checked_at, version, dev_build, latest,
                     update_available, self_upgrade, method, command, hint,
                     error); mit --auto das Ergebnis als JSON (outcome,
                     reason, from, to, restarted, rolled_back, warnings,
                     error, hint, check); siehe docs/installation.md
  --version vX.Y.Z   genau diese Version installieren, auch eine ältere oder
                     eine Vorabversion; nötig, um einen dev build zu ersetzen
  --auto             nur aktualisieren, wenn es gefahrlos geht (oben); nicht
                     mit --check oder --version
  --preflight        mit --json: der Aufruf, mit dem --auto das
                     heruntergeladene Binary vor dem Ersetzen prüft (passt
                     es zu den Datenbanken der geltenden config?); kein
                     Werkzeug für Menschen

Exit-Code:
  0   erledigt; mit --check: GitHub hat geantwortet, gleich ob es eine neue
      Version gibt; mit --auto: upgraded, current oder skipped; mit
      --preflight: die Probe hat geantwortet, ok oder veto
  1   gescheitert — kein Netz, Rate-Limit, kein Release, kein Schreibrecht,
      Prüfsumme; das Binary bleibt unverändert. Mit --check --json steht der
      Grund auch im JSON (state failed, error). Mit --auto: failed, der Grund
      in reason und error; scheitert der Neustart, ist das alte Binary
      zurück, wenn rolled_back es sagt; interrupted: vor dem Ersetzen durch
      ein Signal abgebrochen, nichts verändert
  2   falscher Aufruf — auch für --auto mit einem Binary, das --auto noch
      nicht kennt
`

func runUpgrade(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("upgrade", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, upgradeUsage) }
	var opts upgrade.Options
	fs.BoolVar(&opts.Check, "check", false, "")
	fs.StringVar(&opts.Version, "version", "", "")
	asJSON := fs.Bool("json", false, "")
	preflightFlag := fs.Bool("preflight", false, "")
	auto := fs.Bool("auto", false, "")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	usageErr := func(msg string) int {
		fmt.Fprintf(stderr, "%s\n\n", msg)
		fmt.Fprint(stderr, upgradeUsage)
		return 2
	}
	if fs.NArg() > 0 {
		return usageErr("Unerwartetes Argument: " + fs.Arg(0))
	}
	if *preflightFlag {
		if !*asJSON || opts.Check || opts.Version != "" || *auto {
			return usageErr("upgrade: --preflight gibt es nur mit --json, ohne --check, --version und --auto")
		}
		return printJSON(preflight(context.Background()), stdout, stderr)
	}
	if *auto && (opts.Check || opts.Version != "") {
		return usageErr("upgrade: --auto gibt es nicht mit --check oder --version")
	}
	if *asJSON && !*auto && (!opts.Check || opts.Version != "") {
		return usageErr("upgrade: --json gibt es nur mit --check und ohne --version, mit --auto oder mit --preflight")
	}

	// --auto fängt seine Signale selbst ab (runUpgradeAuto): vor dem Ersetzen
	// bricht es ab, danach führt es den Neustart zu Ende.
	if *auto {
		return runUpgradeAuto(*asJSON, stdout, stderr)
	}

	// Ein Abbruch mit Strg-C oder SIGTERM beendet den Download, und upgrade
	// räumt die temporäre Datei weg, bevor das Programm endet.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	u := newUpgrader(stdout)
	u.System = systemConfigApplies()
	if *asJSON {
		r := u.Report(ctx)
		if code := printJSON(r, stdout, stderr); code != 0 {
			return code
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
		upgradeExtension(ctx, res.Exe, stdout, stderr, false)
		return code
	}
	return 0
}

// printJSON gibt v eingerückt als JSON aus, ohne HTML-Escapes (command und
// hint enthalten &&).
func printJSON(v any, stdout, stderr io.Writer) int {
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		fmt.Fprintf(stderr, "upgrade: %v\n", err)
		return 1
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
