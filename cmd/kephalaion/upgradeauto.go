package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/kephalaion/kephalaion/internal/buildinfo"
	"github.com/kephalaion/kephalaion/internal/config"
	"github.com/kephalaion/kephalaion/internal/contract"
	nodestore "github.com/kephalaion/kephalaion/internal/node/store"
	"github.com/kephalaion/kephalaion/internal/service"
	"github.com/kephalaion/kephalaion/internal/upgrade"
)

// kephalaion upgrade --auto (Task 036): prüfen, ob ein Upgrade ansteht und ob
// es ohne Menschen gehen darf, und wenn ja, es ausführen — in einem Aufruf.
// Die erste zutreffende Stufe entscheidet; bis einschließlich der Probe
// (Stufe 11) bleiben Binary, Dienst und Datenbanken unverändert:
//
//  1. GitHub fragen (failed/check)
//  2. dev build (skipped/dev_build)
//  3. nicht neuer (current)
//  4. globale config (skipped/admin)
//  5. config pro User mit hub: (skipped/hub)
//  6. kein Schreibrecht beim Binary (skipped/manual)
//  7. der Node läuft nicht als Dienst pro User mit diesem Binary
//     (skipped/not_here)
//  8. ein Hub zu alt oder unbekannt (skipped/hub_older, hub_unknown)
//  9. Sperre (skipped/busy; current, wenn ein anderer Lauf ersetzt hat)
//  10. Download und Prüfsumme (failed/download)
//  11. Probe mit dem neuen Binary (skipped/schema, release; failed/preflight)
//  12. Ersetzen mit Rückweg (failed/replace)
//  13. Neustart und Prüfung (failed/restart mit Rückweg), dann upgraded
//
// Die Teile, die nach außen greifen — Hubs, Probe, Dienst, Node —, sind für
// Tests eingespeist wie newUpgrader und newServiceManager.

// Die Ergebnisse von upgrade --auto (autoResult.Outcome).
const (
	autoUpgraded = "upgraded"
	autoCurrent  = "current"
	autoSkipped  = "skipped"
	autoFailed   = "failed"
)

// Die Gründe (autoResult.Reason), festgelegt in Task 036: die ersten zehn bei
// skipped, die übrigen bei failed.
const (
	reasonDevBuild   = "dev_build"
	reasonAdmin      = "admin"
	reasonHub        = "hub"
	reasonManual     = "manual"
	reasonNotHere    = "not_here"
	reasonHubOlder   = "hub_older"
	reasonHubUnknown = "hub_unknown"
	reasonBusy       = "busy"
	reasonSchema     = "schema"
	reasonRelease    = "release"

	reasonCheck     = "check"
	reasonDownload  = "download"
	reasonPreflight = "preflight"
	reasonReplace   = "replace"
	reasonRestart   = "restart"
)

// Die Fristen von upgrade --auto; Tests setzen sie kürzer.
var (
	// autoHubTimeout begrenzt die Frage an einen Hub (Stufe 8).
	autoHubTimeout = 5 * time.Second
	// autoProbeTimeout begrenzt eine Probe des Nodes ohne Token (Stufe 7
	// und je Versuch nach dem Neustart).
	autoProbeTimeout = 3 * time.Second
	// autoPreflightTimeout begrenzt die Probe mit dem neuen Binary.
	autoPreflightTimeout = 30 * time.Second
	// autoRestartWithin ist die Frist nach dem Neustart, in der der Dienst
	// laufen und der Node mit der Zielversion antworten muss; autoRestartPoll
	// die Pause zwischen zwei Versuchen.
	autoRestartWithin = 10 * time.Second
	autoRestartPoll   = 250 * time.Millisecond
)

// Die eingespeisten Teile.
var (
	// autoAskHub fragt einen Hub-Eintrag des Nodes nach seinem Release.
	autoAskHub = askHubRelease
	// autoPreflight ruft die Probe des neuen Binarys bin mit der config
	// cfgPath auf und liefert ihre Standardausgabe.
	autoPreflight = execPreflight
	// autoProbeNode fragt den Node unter listen ohne Token.
	autoProbeNode = func(ctx context.Context, listen string, timeout time.Duration) (nodeInfo, error) {
		return probeNodeWithin(ctx, localNodeAddress(listen), nil, "", timeout)
	}
)

// autoResult ist die Antwort von upgrade --auto, mit --json als JSON.
type autoResult struct {
	// Outcome ist upgraded, current, skipped oder failed.
	Outcome string `json:"outcome"`
	// Reason gibt es nur bei skipped und failed.
	Reason string `json:"reason,omitempty"`
	// From ist die installierte Version, To das Ziel, wenn bekannt.
	From string `json:"from"`
	To   string `json:"to,omitempty"`
	// Restarted ist nur gesetzt, wenn ein Dienst pro User lief: true, wenn
	// --auto ihn neu gestartet hat — bei upgraded und immer bei failed mit
	// restart, auch nach dem Rückweg.
	Restarted *bool `json:"restarted,omitempty"`
	// RolledBack ist nur bei failed mit restart gesetzt: ob das alte Binary
	// wieder da ist und der Dienst mit ihm neu gestartet.
	RolledBack *bool `json:"rolled_back,omitempty"`
	// Warnings sind Warnungen, die das Ergebnis nicht ändern, etwa von der
	// Erweiterung für VS Code; nie null.
	Warnings []string `json:"warnings"`
	// Error ist bei failed der Fehler.
	Error string `json:"error,omitempty"`
	// Hint sagt in einem Satz, was zu tun ist.
	Hint string `json:"hint,omitempty"`
	// Check ist der Report wie bei upgrade --check --json.
	Check *upgrade.Report `json:"check,omitempty"`
}

func (r autoResult) skip(reason, hint string) autoResult {
	r.Outcome, r.Reason, r.Hint = autoSkipped, reason, hint
	return r
}

func (r autoResult) fail(reason string, err error, hint string) autoResult {
	r.Outcome, r.Reason, r.Error, r.Hint = autoFailed, reason, err.Error(), hint
	return r
}

// runUpgradeAuto führt upgrade --auto aus und gibt das Ergebnis aus: Exit 0
// bei upgraded, current und skipped, 1 bei failed.
func runUpgradeAuto(ctx context.Context, asJSON bool, stdout, stderr io.Writer) int {
	res := upgradeAuto(ctx)
	if asJSON {
		if code := printJSON(res, stdout, stderr); code != 0 {
			return code
		}
	} else {
		printAutoResult(stdout, res)
	}
	if res.Outcome == autoFailed {
		fmt.Fprintf(stderr, "upgrade: %s\n", res.Error)
		return 1
	}
	return 0
}

// printAutoResult zeigt das Ergebnis ohne --json in Zeilen.
func printAutoResult(w io.Writer, r autoResult) {
	head := "upgrade --auto: " + r.Outcome
	if r.Reason != "" {
		head += " (" + r.Reason + ")"
	}
	fmt.Fprintln(w, head)
	fmt.Fprintf(w, "  von:            %s\n", r.From)
	if r.To != "" {
		fmt.Fprintf(w, "  Ziel:           %s\n", r.To)
	}
	if r.Restarted != nil {
		fmt.Fprintf(w, "  neu gestartet:  %s\n", yesNo(*r.Restarted))
	}
	if r.RolledBack != nil {
		fmt.Fprintf(w, "  zurückgesetzt:  %s\n", yesNo(*r.RolledBack))
	}
	if r.Error != "" {
		fmt.Fprintf(w, "  Fehler:         %s\n", r.Error)
	}
	for _, warn := range r.Warnings {
		fmt.Fprintf(w, "  Warnung:        %s\n", warn)
	}
	if r.Hint != "" {
		fmt.Fprintf(w, "  Hinweis:        %s\n", r.Hint)
	}
}

// upgradeAuto ist der Ablauf von upgrade --auto, Stufe für Stufe.
func upgradeAuto(ctx context.Context) autoResult {
	u := newUpgrader(io.Discard)
	u.System = systemConfigApplies()
	res := autoResult{From: u.Current.Version, Warnings: []string{}}

	// Das Binary, mit dem der Lauf beginnt: Nach der Sperre zeigt es, ob ein
	// anderer Lauf es inzwischen ersetzt hat.
	exe, exeErr := u.ExecutablePath()
	var startFile os.FileInfo
	if exeErr == nil {
		startFile, _ = os.Stat(exe)
	}

	// 1. GitHub — vor allen lokalen Gründen: current ist auch auf Rechnern
	// des Verwalters die nützlichste Antwort.
	latest, err := u.Latest(ctx)
	res.Check = &latest.Report
	if err != nil {
		return res.fail(reasonCheck, err, "GitHub nicht erreichbar oder ohne Release; später erneut versuchen")
	}
	target := latest.Target
	res.To = target.String()

	// 2. und 3.
	if latest.IsDev {
		return res.skip(reasonDevBuild, "ein dev build wird nie automatisch ersetzt; von Hand: kephalaion upgrade "+
			"--version "+res.To)
	}
	if target.Compare(latest.Current) <= 0 {
		res.Outcome = autoCurrent
		res.Hint = fmt.Sprintf("%s ist aktuell (neuestes Release %s); nichts zu tun", res.From, res.To)
		return res
	}

	// 4. Die globale config: immer der Verwalter, gleich mit welchen Rechten
	// und welches Binary aufgerufen wurde.
	if u.System {
		return res.skip(reasonAdmin, "globale Installation — das Upgrade macht der Verwalter: über Ansible (Version "+
			"anheben) oder sudo kephalaion upgrade && sudo systemctl restart kephalaion")
	}
	loc, cfgErr := config.Locate("")
	var cfg config.Config
	if cfgErr == nil {
		cfg, _, cfgErr = config.Load(loc.Path)
	}

	// 5. Ein Hub wird nie automatisch aktualisiert, auch nicht pro User.
	if cfgErr == nil && cfg.Hub != nil {
		return res.skip(reasonHub, "auf diesem Rechner ist ein Hub eingerichtet (config "+loc.Path+"); ihn "+
			"aktualisiert ein Mensch: kephalaion upgrade, danach die Nodes")
	}

	// 6.
	if exeErr != nil {
		return res.skip(reasonManual, exeErr.Error())
	}
	if ok, _, hint := u.WriteAccess(); !ok {
		return res.skip(reasonManual, hint)
	}

	// 7. und 8. Eine config, die sich nicht lesen lässt, lässt beide offen:
	// Wo der Node und seine Hub-Einträge sind, ist dann unbekannt.
	if cfgErr != nil {
		return res.skip(reasonHubUnknown, fmt.Sprintf("die config ist nicht lesbar (%v), die Hub-Einträge des Nodes "+
			"damit auch nicht; die Einrichtung dieses Rechners reparieren (kephalaion status)", cfgErr))
	}
	svc, running, hint := autoWhere(ctx, exe, loc, cfg)
	if hint != "" {
		return res.skip(reasonNotHere, hint)
	}
	if cfg.Node != nil {
		if reason, hint := autoHubs(ctx, cfg, target); reason != "" {
			return res.skip(reason, hint)
		}
	}

	// 9. Die Sperre hält bis nach dem Neustart; die Datei bleibt liegen.
	lock, err := upgrade.Lock(filepath.Dir(exe))
	if errors.Is(err, upgrade.ErrBusy) {
		return res.skip(reasonBusy, "ein anderer upgrade --auto läuft gerade auf diesem Binary; danach erneut")
	}
	if err != nil {
		return res.skip(reasonManual, fmt.Sprintf("die Sperre im Verzeichnis des Binarys lässt sich nicht nehmen (%v)",
			err))
	}
	defer lock.Close()
	if now, err := os.Stat(exe); startFile != nil && (err != nil || !os.SameFile(startFile, now)) {
		res.Outcome = autoCurrent
		res.Hint = "ein anderer Lauf hat das Binary inzwischen ersetzt; nichts zu tun"
		return res
	}

	// 10.
	staged, err := u.Stage(ctx, latest, exe)
	if err != nil {
		return res.fail(reasonDownload, err, "Download oder Prüfsumme gescheitert, das Binary bleibt unverändert; "+
			"später erneut versuchen")
	}

	// 11. Die Probe mit dem neuen Binary, vor dem Ersetzen.
	if r := autoProbe(ctx, res, staged, loc.Path); r.Outcome != "" {
		_ = os.Remove(staged)
		return r
	}

	// 12.
	backup, err := u.Swap(staged, exe)
	if err != nil {
		return res.fail(reasonReplace, err, "Ersetzen gescheitert, das Binary bleibt unverändert; Rechte im "+
			"Verzeichnis prüfen, dann erneut")
	}

	// 13. Neustart und Prüfung; scheitert sie, der Rückweg.
	if running {
		restarted := true
		res.Restarted = &restarted
		if err := restartAndCheck(ctx, svc, cfg.Listen(config.Node), res.To); err != nil {
			return autoRollBack(ctx, u, svc, res, backup, exe, err)
		}
	}
	_ = os.Remove(backup)
	res.Warnings = append(res.Warnings, autoExtension(ctx, exe)...)
	res.Outcome = autoUpgraded
	if running {
		res.Hint = fmt.Sprintf("aktualisiert auf %s; der Dienst läuft mit dem neuen Binary", res.To)
	} else {
		res.Hint = fmt.Sprintf("aktualisiert auf %s; kein Dienst lief — serve benutzt das neue Binary beim nächsten "+
			"Start", res.To)
	}
	return res
}

// autoWhere prüft Stufe 7: Läuft der Node hier, als Dienst pro User mit
// diesem Binary und dieser config? hint ist gesetzt, wenn nicht. running
// sagt, ob der Dienst pro User läuft; m ist sein Manager.
func autoWhere(ctx context.Context, exe string, loc config.Location, cfg config.Config) (
	m *service.Manager, running bool, hint string) {
	if cfg.Node == nil {
		// Ohne Node in der config: nur die Frage, ob an der Standardadresse
		// trotzdem einer antwortet — etwa der des Hosts im Devcontainer.
		listen := config.DefaultListenNode
		if info, err := autoProbeNode(ctx, listen, autoProbeTimeout); err == nil {
			return nil, false, fmt.Sprintf("hier ist kein Node eingerichtet, aber unter %s antwortet einer%s — etwa "+
				"ein Devcontainer auf einem Host mit globaler Installation; aktualisieren auf dem Rechner, auf dem der "+
				"Node läuft", listen, versionNote(info))
		}
		return nil, false, ""
	}
	if dir, ok := roleNotHere(cfg.Node); ok {
		return nil, false, fmt.Sprintf("Datenbank und Verzeichnis des Nodes fehlen hier (%s) — ein reiner Client, "+
			"etwa ein Devcontainer mit der config des Hosts; aktualisieren auf dem Rechner, auf dem der Node läuft", dir)
	}
	m = newServiceManager()
	st, err := m.UserState(ctx)
	if err != nil {
		// Ohne systemd, auf einem anderen System oder nicht abfragbar: kein
		// Dienst, den --auto neu starten könnte.
		st = service.State{}
	}
	switch {
	case st.Installed:
		call, err := m.InstalledCall()
		if err != nil {
			return m, false, fmt.Sprintf("die Unit des Dienstes pro User ist nicht lesbar (%v); neu einrichten: "+
				"kephalaion service install", err)
		}
		if !sameFile(call[0], exe) {
			return m, false, fmt.Sprintf("der Dienst pro User (%s) startet %s, nicht dieses Binary %s — jenes "+
				"aktualisieren oder den Dienst für dieses einrichten (kephalaion service install)", m.UnitPath(),
				call[0], exe)
		}
		if want := serveCallFor(exe, loc); !slices.Equal(call[1:], want[1:]) {
			return m, false, fmt.Sprintf("der Dienst pro User (%s) startet %q, die geltende config ist %s — den "+
				"Dienst neu einrichten (kephalaion service install) oder von Hand aktualisieren", m.UnitPath(),
				strings.Join(call, " "), loc.Path)
		}
	case st.Running:
		return m, false, fmt.Sprintf("der Dienst pro User läuft, aber seine Unit %s fehlt, sein Aufruf ist nicht "+
			"prüfbar; neu einrichten: kephalaion service install", m.UnitPath())
	}
	if !st.Running {
		listen := cfg.Listen(config.Node)
		if info, err := autoProbeNode(ctx, listen, autoProbeTimeout); err == nil {
			return m, false, fmt.Sprintf("unter %s antwortet ein Node%s, aber kein Dienst pro User läuft — serve läuft "+
				"von Hand oder unter eigener Aufsicht, und --auto kann es nicht neu starten; von Hand: kephalaion "+
				"upgrade, dann serve neu starten", listen, versionNote(info))
		}
	}
	return m, st.Running, ""
}

// versionNote nennt die Version aus einer Probe, wenn es eine gibt.
func versionNote(info nodeInfo) string {
	if info.Version == "" {
		return ""
	}
	return " (Version " + info.Version + ")"
}

// autoHubs prüft Stufe 8: Jeder Hub-Eintrag des Nodes muss ein Release
// nennen, das mindestens so neu ist wie target. Gefragt wird live, mit der
// Anmeldung des Nodes; in node.db wird nichts geschrieben. reason ist leer,
// wenn alle Hubs passen; sonst der Grund des ersten, der nicht passt, und
// hint nennt jeden mit seinem Alias.
func autoHubs(ctx context.Context, cfg config.Config, target upgrade.Version) (reason, hint string) {
	st, err := openNodeOf(ctx, cfg)
	if err != nil {
		return reasonHubUnknown, fmt.Sprintf("die Hub-Einträge des Nodes sind nicht lesbar (%v) — das liegt am Node "+
			"selbst; seine Einrichtung reparieren (kephalaion status)", err)
	}
	hubs, err := st.Hubs(ctx)
	_ = st.Close()
	if err != nil {
		return reasonHubUnknown, fmt.Sprintf("die Hub-Einträge des Nodes sind nicht lesbar (%v) — das liegt am Node "+
			"selbst; seine Einrichtung reparieren (kephalaion status)", err)
	}
	var problems []string
	for _, h := range hubs {
		release, err := autoAskHub(ctx, cfg, h)
		r, why := judgeHubRelease(h.Name, release, err, target)
		if r == "" {
			continue
		}
		if reason == "" {
			reason = r
		}
		problems = append(problems, why)
	}
	if reason == "" {
		return "", ""
	}
	return reason, strings.Join(problems, "; ") + " — erst den Hub auf " + target.String() + " oder neuer " +
		"bringen, dann erneut kephalaion upgrade --auto"
}

// judgeHubRelease bewertet die Antwort eines Hubs: leer, wenn er mindestens
// target trägt, sonst hub_older oder hub_unknown mit einem Satz.
func judgeHubRelease(alias, release string, err error, target upgrade.Version) (reason, why string) {
	switch {
	case err != nil:
		return reasonHubUnknown, fmt.Sprintf("Hub %s antwortet nicht (%v)", alias, err)
	case release == "":
		return reasonHubUnknown, fmt.Sprintf("Hub %s nennt kein Release — er ist älter als upgrade --auto", alias)
	}
	v, perr := upgrade.ParseVersion(release)
	switch {
	case release == buildinfo.DevVersion || perr != nil:
		return reasonHubUnknown, fmt.Sprintf("Hub %s läuft als dev build (%s)", alias, release)
	case v.Compare(target) < 0:
		return reasonHubOlder, fmt.Sprintf("Hub %s hat %s, älter als das Ziel %s", alias, release, target)
	}
	return "", ""
}

// askHubRelease fragt einen Hub-Eintrag mit whoami und der Anmeldung des
// Nodes, wie hubCheck — aber ohne die hub_id zu merken: --auto schreibt
// nichts in node.db. Die Frage hat eine kurze Frist.
func askHubRelease(ctx context.Context, cfg config.Config, h nodestore.Hub) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, autoHubTimeout)
	defer cancel()
	conn := &connector{ctx: ctx, cfg: cfg}
	defer conn.close()
	hub, err := conn.connect(h)
	if err != nil {
		return "", err
	}
	resp, err := hub.Whoami(ctx, contract.WhoamiRequest{Version: contract.Version,
		Auth: contract.NodeAuth{Node: h.NodeName, Token: h.Token}})
	if err != nil {
		return "", explainHubError(h, err)
	}
	return resp.Release, nil
}

// execPreflight ruft bin upgrade --preflight --json auf, mit der config
// cfgPath über KEPHALAION_CONFIG.
func execPreflight(ctx context.Context, bin, cfgPath string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, autoPreflightTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "upgrade", "--preflight", "--json")
	cmd.Env = append(os.Environ(), config.EnvConfig+"="+cfgPath)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		if msg := firstLine(errOut.String()); msg != "" {
			return out.Bytes(), fmt.Errorf("%w: %s", err, msg)
		}
		return out.Bytes(), err
	}
	return out.Bytes(), nil
}

// firstLine ist die erste nicht leere Zeile von s.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}

// autoProbe ist Stufe 11: Die Probe mit dem neuen Binary staged. Ein
// Ergebnis mit Outcome heißt: nicht ersetzen.
func autoProbe(ctx context.Context, res autoResult, staged, cfgPath string) autoResult {
	out, err := autoPreflight(ctx, staged, cfgPath)
	failHint := "das neue Binary ließ sich nicht prüfen, das installierte bleibt; von Hand: kephalaion upgrade, " +
		"danach kephalaion status"
	if err != nil {
		return res.fail(reasonPreflight, fmt.Errorf("Probe mit %s gescheitert: %w", res.To, err), failHint)
	}
	var rep preflightReport
	if err := json.Unmarshal(out, &rep); err != nil {
		return res.fail(reasonPreflight, fmt.Errorf("Antwort der Probe mit %s nicht lesbar: %w", res.To, err), failHint)
	}
	switch rep.Result {
	case preflightOK:
		return autoResult{}
	case preflightVeto:
		hint := rep.Hint
		if rep.Reason == vetoSchema {
			if hint == "" {
				hint = res.To + " erwartet eine andere Schemafassung; das Upgrade geht nur von Hand: " +
					"docs/installation.md, „Neue Schemafassung“"
			}
			return res.skip(reasonSchema, hint)
		}
		if hint == "" {
			hint = fmt.Sprintf("%s verlangt einen Schritt von Hand (%s); siehe die Release-Notizen", res.To, rep.Reason)
		}
		return res.skip(reasonRelease, hint)
	}
	return res.fail(reasonPreflight, fmt.Errorf("Antwort der Probe mit %s ohne ok oder veto (result %q)", res.To,
		rep.Result), failHint)
}

// restartAndCheck startet den Dienst pro User neu und wartet, bis er läuft
// und der Node unter listen mit der Version want antwortet — beides
// zusammen, innerhalb von autoRestartWithin.
func restartAndCheck(ctx context.Context, m *service.Manager, listen, want string) error {
	if err := m.Restart(ctx); err != nil {
		return err
	}
	deadline := time.Now().Add(autoRestartWithin)
	for {
		var last error
		st, err := m.UserState(ctx)
		switch {
		case err != nil:
			last = err
		case !st.Running:
			last = fmt.Errorf("der Dienst läuft nicht (%s)", orDash(st.Detail))
		default:
			info, err := autoProbeNode(ctx, listen, autoProbeTimeout)
			switch {
			case err != nil:
				last = err
			case info.Version != want:
				last = fmt.Errorf("der Node meldet Version %s, erwartet %s", orDash(info.Version), want)
			default:
				return nil
			}
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("nach dem Neustart: %w", last)
		}
		t := time.NewTimer(autoRestartPoll)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
	}
}

// autoRollBack setzt nach einem gescheiterten Neustart das alte Binary
// zurück und startet den Dienst erneut: failed, restart, mit rolled_back.
func autoRollBack(ctx context.Context, u *upgrade.Upgrader, m *service.Manager, res autoResult, backup, exe string,
	cause error) autoResult {
	rolledBack := false
	res.RolledBack = &rolledBack
	if err := u.Restore(backup, exe); err != nil {
		return res.fail(reasonRestart, fmt.Errorf("%v; Rückweg gescheitert: %w", cause, err),
			fmt.Sprintf("das alte Binary liegt noch als %s — von Hand zurück: mv %s %s && %s", backup, backup, exe,
				m.RestartCommand()))
	}
	if err := m.Restart(ctx); err != nil {
		return res.fail(reasonRestart, fmt.Errorf("%v; altes Binary zurück, aber der Dienst startet nicht neu: %w",
			cause, err), "das alte Binary ist zurück; den Dienst von Hand starten: "+m.RestartCommand()+
			" (Log: "+m.LogHint()+")")
	}
	rolledBack = true
	return res.fail(reasonRestart, cause, "der Dienst lief mit "+res.To+" nicht an; das alte Binary ist zurück und "+
		"der Dienst neu gestartet — Log: "+m.LogHint())
}

// autoExtension erneuert die Erweiterung für VS Code wie upgrade und liefert,
// was dabei scheiterte, als Warnungen; die Ausgabe des neuen Binarys selbst
// bleibt draußen, damit --json nur JSON schreibt.
func autoExtension(ctx context.Context, exe string) []string {
	var out, errOut bytes.Buffer
	upgradeExtension(ctx, exe, &out, &errOut)
	var warnings []string
	for _, line := range strings.Split(errOut.String(), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			warnings = append(warnings, strings.TrimPrefix(line, "upgrade: Warnung: "))
		}
	}
	for _, line := range strings.Split(out.String(), "\n") {
		if line = strings.TrimSpace(line); strings.Contains(line, "nicht erneuert") {
			warnings = append(warnings, line)
		}
	}
	return warnings
}
