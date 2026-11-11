package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/kephalaion/kephalaion/internal/service"
	"github.com/kephalaion/kephalaion/internal/upgrade"
)

// withSignals speist Signale in upgrade --auto ein. send liefert eines und
// wartet, bis der Lauf es verarbeitet hat; es darf nur während eines Laufs
// gerufen werden.
func withSignals(t *testing.T) (send func(os.Signal)) {
	t.Helper()
	ch := make(chan os.Signal)
	seen := make(chan os.Signal)
	oldNotify, oldSeen := autoNotify, autoSignalSeen
	autoNotify = func() (<-chan os.Signal, func()) { return ch, func() {} }
	autoSignalSeen = func(sig os.Signal) { seen <- sig }
	t.Cleanup(func() { autoNotify, autoSignalSeen = oldNotify, oldSeen })
	return func(sig os.Signal) {
		select {
		case ch <- sig:
		case <-time.After(5 * time.Second):
			t.Errorf("%v nicht angenommen — läuft kein upgrade --auto?", sig)
			return
		}
		<-seen
	}
}

// autoRun ruft upgrade --auto --json auf wie auto, liefert aber auch die
// Ausgabe.
func (e *autoEnv) autoRun(t *testing.T, code int) (autoResult, result) {
	t.Helper()
	r := runT(t, "upgrade", "--auto", "--json")
	if r.code != code {
		t.Fatalf("Exit-Code %d, erwartet %d\nstdout:\n%s\nstderr:\n%s", r.code, code, r.out, r.errOut)
	}
	var res autoResult
	if err := json.Unmarshal([]byte(r.out), &res); err != nil {
		t.Fatalf("%v:\n%s", err, r.out)
	}
	if res.Warnings == nil {
		t.Errorf("warnings ist nicht [] :\n%s", r.out)
	}
	return res, r
}

// control legt die Steuerdatei name für das neue Binary an (hang-…, kill-…).
func (e *autoEnv) control(t *testing.T, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(e.script, name), nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

// waitPID wartet, bis das neue Binary für cmd hängt, und liefert seine PID.
func (e *autoEnv) waitPID(t *testing.T, cmd string) int {
	path := filepath.Join(e.script, cmd+".pid")
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil && strings.HasSuffix(string(b), "\n") {
			pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
			if err == nil {
				return pid
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Errorf("%s hängt nicht", cmd)
	return 0
}

// withExtension richtet die Erweiterung für VS Code mit dem CLI code ein:
// Nach einem Upgrade ruft --auto dann das neue Binary mit vscode install.
// Der PATH behält die üblichen Programme für das Skript des neuen Binarys.
func (e *autoEnv) withExtension(t *testing.T) {
	t.Helper()
	path := os.Getenv("PATH")
	fe := fakeEditors(t, false, "code")
	t.Setenv("PATH", fe.dir+string(os.PathListSeparator)+path)
	installExt(t, e.dir, ".vscode")
}

// extensionCalls sind die Aufrufe des neuen Binarys mit vscode install.
func (e *autoEnv) extensionCalls(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, c := range e.preflightCalls(t) {
		if strings.HasPrefix(c, "vscode install") {
			out = append(out, c)
		}
	}
	return out
}

// lockFree prüft, dass die Sperre frei ist.
func (e *autoEnv) lockFree(t *testing.T) {
	t.Helper()
	lock, err := upgrade.Lock(filepath.Dir(e.exe))
	if err != nil {
		t.Fatalf("Sperre nicht frei: %v", err)
	}
	_ = lock.Close()
}

// gone prüft, dass der Prozess pid nicht mehr läuft.
func gone(t *testing.T, pid int) {
	t.Helper()
	if pid == 0 {
		return
	}
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Errorf("Prozess %d läuft noch (%v)", pid, err)
	}
}

// interrupted prüft failed/interrupted mit dem Signal im Fehler.
func (res autoResult) interrupted(t *testing.T, sig string) {
	t.Helper()
	res.want(t, autoFailed, reasonInterrupted, "nichts verändert")
	if !strings.Contains(res.Error, sig) || res.Restarted != nil || res.RolledBack != nil {
		t.Errorf("interrupted: %+v", res)
	}
}

// Ein Signal in Stufe 1 bis 9 bricht ab: failed/interrupted, Exit 1, Binary
// unverändert, nichts angelegt — auch keine Sperrdatei.
func TestUpgradeAutoSignalEarly(t *testing.T) {
	for name, tc := range map[string]struct {
		sig  os.Signal
		hook func(e *autoEnv, send func(os.Signal))
	}{
		"Stufe 1": {syscall.SIGTERM, func(e *autoEnv, send func(os.Signal)) {
			e.onLatest = func() { send(syscall.SIGTERM) }
		}},
		"Stufe 8": {syscall.SIGINT, func(e *autoEnv, send func(os.Signal)) {
			e.onAsk = func() { send(syscall.SIGINT) }
		}},
		"Stufe 8 SIGHUP": {syscall.SIGHUP, func(e *autoEnv, send func(os.Signal)) {
			e.onAsk = func() { send(syscall.SIGHUP) }
		}},
	} {
		t.Run(name, func(t *testing.T) {
			send := withSignals(t)
			e := newAutoEnv(t, "v0.1.0")
			e.initNode(t)
			before := fileState(t, e.nodeDB)
			tc.hook(e, send)
			res, _ := e.autoRun(t, 1)
			res.interrupted(t, signalName(tc.sig))
			e.unchanged(t, before)
			if exists(filepath.Join(filepath.Dir(e.exe), upgrade.LockName)) {
				t.Error("Sperrdatei angelegt")
			}
			if len(e.preflightCalls(t)) != 0 {
				t.Errorf("Probe aufgerufen: %v", e.preflightCalls(t))
			}
		})
	}
}

// Ein Signal während des Downloads (Stufe 10): keine Datei nach
// TempPattern, Sperre frei, keine Probe.
func TestUpgradeAutoSignalDownload(t *testing.T) {
	send := withSignals(t)
	e := newAutoEnv(t, "v0.1.0")
	e.initNode(t)
	e.onDownload = func() { send(syscall.SIGTERM) }
	res, _ := e.autoRun(t, 1)
	res.interrupted(t, "SIGTERM")
	e.unchanged(t, "")
	e.lockFree(t)
	if len(e.preflightCalls(t)) != 0 {
		t.Errorf("Probe aufgerufen: %v", e.preflightCalls(t))
	}
}

// Ein Signal während der Probe (Stufe 11): Die Probe endet, keine Datei nach
// TempPattern, Sperre frei, failed/interrupted.
func TestUpgradeAutoSignalPreflight(t *testing.T) {
	send := withSignals(t)
	e := newAutoEnv(t, "v0.1.0")
	e.initNode(t)
	e.control(t, "hang-upgrade")
	pid := make(chan int, 1)
	go func() {
		p := e.waitPID(t, "upgrade")
		send(syscall.SIGTERM)
		pid <- p
	}()
	start := time.Now()
	res, _ := e.autoRun(t, 1)
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("Lauf dauerte %v", d)
	}
	res.interrupted(t, "SIGTERM")
	e.unchanged(t, "")
	e.lockFree(t)
	gone(t, <-pid)
}

// Stirbt die Probe selbst am Signal an die Gruppe, ist es interrupted, nicht
// preflight — auch wenn der Lauf das Signal noch nicht gesehen hat.
func TestUpgradeAutoSignalPreflightGroup(t *testing.T) {
	withSignals(t)
	e := newAutoEnv(t, "v0.1.0")
	e.initNode(t)
	e.control(t, "kill-upgrade")
	res, _ := e.autoRun(t, 1)
	res.interrupted(t, "SIGTERM")
	e.unchanged(t, "")
	e.lockFree(t)
}

// Genau an der Grenze — nach der Probe, vor dem Ersetzen: interrupted,
// Binary unverändert.
func TestUpgradeAutoSignalAtBoundary(t *testing.T) {
	send := withSignals(t)
	e := newAutoEnv(t, "v0.1.0")
	e.initNode(t)
	autoPreflight = func(ctx context.Context, bin, cfgPath string) ([]byte, error) {
		out, err := execPreflight(ctx, bin, cfgPath)
		send(syscall.SIGTERM)
		return out, err
	}
	res, _ := e.autoRun(t, 1)
	res.interrupted(t, "SIGTERM")
	e.unchanged(t, "")
	e.lockFree(t)
	if len(e.preflightCalls(t)) != 1 {
		t.Errorf("Aufrufe des neuen Binarys: %v", e.preflightCalls(t))
	}
}

// afterSignal prüft ein Ergebnis nach einem Signal ab Stufe 12: die Warnung
// im JSON, die Zeile auf stderr und die übersprungene Erweiterung.
func (e *autoEnv) afterSignal(t *testing.T, res autoResult, r result, sig, what string) {
	t.Helper()
	if !strings.Contains(r.errOut, "upgrade --auto: "+sig+" empfangen, "+what+", endet spätestens in ") {
		t.Errorf("keine Zeile auf stderr:\n%s", r.errOut)
	}
	joined := strings.Join(res.Warnings, "\n")
	if !strings.Contains(joined, sig+" kam nach dem Ersetzen und wurde nicht befolgt") {
		t.Errorf("Warnungen ohne das Signal: %q", res.Warnings)
	}
	if !strings.Contains(joined, "Erweiterung für VS Code nach "+sig+" nicht erneuert — von Hand: kephalaion vscode "+
		"install") {
		t.Errorf("Warnungen ohne die übersprungene Erweiterung: %q", res.Warnings)
	}
	if got := e.extensionCalls(t); len(got) != 0 {
		t.Errorf("Erweiterung erneuert: %q", got)
	}
}

// restarts zählt die Neustarts des Dienstes; grouped die über den Runner mit
// eigener Prozessgruppe.
func (e *autoEnv) restarts() (all, grouped int) {
	const call = "systemctl --user restart kephalaion.service"
	return strings.Count(strings.Join(e.sd.calls, "\n"), call), strings.Count(strings.Join(e.grouped, "\n"), call)
}

// Ein Signal in Stufe 13 mit laufendem Dienst wird vermerkt, nicht befolgt:
// Während des Neustarts oder der Prüfung ergibt ein gesundes Upgrade
// upgraded, ein zweites Signal ändert nichts; scheitert die Prüfung, läuft
// der Rückweg samt zweitem Neustart. Jeder Neustart ab der Grenze geht über
// den Runner mit eigener Prozessgruppe.
func TestUpgradeAutoSignalAfterSwap(t *testing.T) {
	t.Run("während des Neustarts", func(t *testing.T) {
		send := withSignals(t)
		e := newAutoEnv(t, "v0.1.0")
		e.initNode(t)
		e.withExtension(t)
		e.sd.onRestart = func() { send(syscall.SIGTERM) }
		e.sd.calls = nil
		res, r := e.autoRun(t, 0)
		res.want(t, autoUpgraded, "")
		e.afterSignal(t, res, r, "SIGTERM", "Neustart läuft")
		if all, grouped := e.restarts(); all != 1 || grouped != 1 {
			t.Errorf("%d Neustarts, %d mit eigener Gruppe: %q", all, grouped, e.sd.calls)
		}
		if b, _ := os.ReadFile(e.exe); string(b) != string(e.newBin) {
			t.Errorf("Binary nicht ersetzt: %q", b)
		}
		assertNoUpgradeTemp(t, filepath.Dir(e.exe))
	})
	t.Run("zwei Signale", func(t *testing.T) {
		send := withSignals(t)
		e := newAutoEnv(t, "v0.1.0")
		e.initNode(t)
		e.sd.onRestart = func() {
			send(syscall.SIGTERM)
			send(syscall.SIGINT)
		}
		res, r := e.autoRun(t, 0)
		res.want(t, autoUpgraded, "")
		if strings.Count(r.errOut, "endet spätestens in") != 2 {
			t.Errorf("stderr:\n%s", r.errOut)
		}
		if strings.Count(strings.Join(res.Warnings, "\n"), "nicht befolgt") != 1 {
			t.Errorf("Warnungen: %q", res.Warnings)
		}
	})
	t.Run("während der Prüfung", func(t *testing.T) {
		send := withSignals(t)
		e := newAutoEnv(t, "v0.1.0")
		e.initNode(t)
		e.withExtension(t)
		sent := false
		e.probe = func(string) (nodeInfo, error) {
			if !sent {
				sent = true
				send(syscall.SIGHUP)
			}
			return nodeInfo{Version: "v0.2.0"}, nil
		}
		res, r := e.autoRun(t, 0)
		res.want(t, autoUpgraded, "")
		e.afterSignal(t, res, r, "SIGHUP", "Neustart läuft")
	})
	t.Run("Prüfung scheitert", func(t *testing.T) {
		send := withSignals(t)
		e := newAutoEnv(t, "v0.1.0")
		e.initNode(t)
		e.withExtension(t)
		sent := false
		e.probe = func(string) (nodeInfo, error) {
			if !sent {
				sent = true
				send(syscall.SIGTERM)
			}
			return nodeInfo{Version: "v0.1.0"}, nil
		}
		e.sd.calls = nil
		res, r := e.autoRun(t, 1)
		res.want(t, autoFailed, reasonRestart, "das alte Binary ist zurück")
		if res.RolledBack == nil || !*res.RolledBack || res.Restarted == nil || !*res.Restarted {
			t.Errorf("Rückweg: %+v", res)
		}
		if all, grouped := e.restarts(); all != 2 || grouped != 2 {
			t.Errorf("%d Neustarts, %d mit eigener Gruppe: %q", all, grouped, e.sd.calls)
		}
		if b, _ := os.ReadFile(e.exe); string(b) != "alt" {
			t.Errorf("Binary nicht zurück: %q", b)
		}
		if !strings.Contains(r.errOut, "SIGTERM empfangen, Neustart läuft") ||
			!strings.Contains(strings.Join(res.Warnings, "\n"), "nicht befolgt") {
			t.Errorf("Signal nicht vermerkt: %q\n%s", res.Warnings, r.errOut)
		}
	})
}

// Ohne laufenden Dienst bleibt nach der Grenze nur die Erweiterung: Kam das
// Signal schon beim Ersetzen, entfällt sie; kommt es, während sie läuft,
// bricht sie ab. Beides ist upgraded mit Warnung.
func TestUpgradeAutoSignalWithoutService(t *testing.T) {
	t.Run("beim Ersetzen", func(t *testing.T) {
		send := withSignals(t)
		e := newAutoEnv(t, "v0.1.0")
		e.withExtension(t)
		e.rename = func(_, newpath string) error {
			if newpath == e.exe {
				send(syscall.SIGTERM)
			}
			return nil
		}
		res, r := e.autoRun(t, 0)
		res.want(t, autoUpgraded, "", "kein Dienst lief")
		e.afterSignal(t, res, r, "SIGTERM", "Ersetzen läuft")
		if b, _ := os.ReadFile(e.exe); string(b) != string(e.newBin) {
			t.Errorf("Binary nicht ersetzt: %q", b)
		}
	})
	t.Run("während der Erweiterung", func(t *testing.T) {
		send := withSignals(t)
		e := newAutoEnv(t, "v0.1.0")
		e.withExtension(t)
		e.control(t, "hang-vscode")
		pid := make(chan int, 1)
		go func() {
			p := e.waitPID(t, "vscode")
			send(syscall.SIGINT)
			pid <- p
		}()
		start := time.Now()
		res, r := e.autoRun(t, 0)
		if d := time.Since(start); d > autoMaxWithoutService()+5*time.Second {
			t.Errorf("Lauf dauerte %v", d)
		}
		res.want(t, autoUpgraded, "")
		if !strings.Contains(r.errOut, "SIGINT empfangen, Ersetzen läuft") ||
			!strings.Contains(strings.Join(res.Warnings, "\n"), "Erweiterung für VS Code durch SIGINT abgebrochen") {
			t.Errorf("Abbruch nicht vermerkt: %q\n%s", res.Warnings, r.errOut)
		}
		gone(t, <-pid)
	})
}

// pgids liest die Prozessgruppen aus pgid.log des neuen Binarys, je Befehl.
func (e *autoEnv) pgids(t *testing.T) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	for _, line := range readLines(t, filepath.Join(e.script, "pgid.log")) {
		if cmd, pg, ok := strings.Cut(line, "|"); ok {
			out[cmd] = append(out[cmd], pg)
		}
	}
	return out
}

// Ab der Grenze startet --auto seine Kinder in einer eigenen Prozessgruppe —
// die Erweiterung wirklich, den Neustart über den Runner mit eigener
// Gruppe —, die Probe davor in der des Laufs. upgrade ohne --auto startet
// seine Kinder ohne eigene Gruppe und nie über diesen Runner.
func TestUpgradeAutoOwnProcessGroup(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("liest /proc")
	}
	own := strconv.Itoa(syscall.Getpgrp())
	t.Run("--auto", func(t *testing.T) {
		e := newAutoEnv(t, "v0.1.0")
		e.initNode(t)
		e.withExtension(t)
		e.auto(t, 0).want(t, autoUpgraded, "")
		pg := e.pgids(t)
		if len(pg["upgrade"]) != 1 || pg["upgrade"][0] != own {
			t.Errorf("Probe in Gruppe %v, erwartet die des Laufs %s", pg["upgrade"], own)
		}
		if len(pg["vscode"]) != 1 || pg["vscode"][0] == own {
			t.Errorf("Erweiterung in Gruppe %v, erwartet eine eigene (nicht %s)", pg["vscode"], own)
		}
		if _, grouped := e.restarts(); grouped != 1 {
			t.Errorf("Neustart nicht über den Runner mit eigener Gruppe: %q", e.grouped)
		}
	})
	t.Run("ohne --auto", func(t *testing.T) {
		e := newAutoEnv(t, "v0.1.0")
		e.initNode(t)
		e.withExtension(t)
		autoOwnGroup = func(m *service.Manager) *service.Manager {
			t.Error("upgrade ohne --auto nimmt den Runner mit eigener Gruppe")
			return m
		}
		e.sd.calls = nil
		runT(t, "upgrade").want(t, 0, "Dienst neu gestartet")
		pg := e.pgids(t)
		if len(pg["vscode"]) != 1 || pg["vscode"][0] != own {
			t.Errorf("Erweiterung in Gruppe %v, erwartet die des Aufrufs %s", pg["vscode"], own)
		}
		if all, _ := e.restarts(); all != 1 {
			t.Errorf("%d Neustarts: %q", all, e.sd.calls)
		}
	})
}

// shortDeadlines setzt die Fristen nach dem Ersetzen kurz; die Höchstdauer
// mit Dienst ist dann 2×400 ms + 300 ms + 1 s.
func shortDeadlines() {
	autoRestartTimeout, autoRestartWithin, autoSlack = 400*time.Millisecond, 300*time.Millisecond, time.Second
	autoRestartPoll = 20 * time.Millisecond
}

// Hängt der Neustart, die Abfrage des Dienstes oder die Probe, endet der
// Lauf trotzdem innerhalb der Höchstdauer — auch vom Signal an gerechnet —,
// und der Rückweg läuft: failed/restart, rolled_back true.
func TestUpgradeAutoDeadlines(t *testing.T) {
	check := func(t *testing.T, e *autoEnv, start time.Time, wantErr string) {
		t.Helper()
		res := e.auto(t, 1)
		if d, limit := time.Since(start), autoMaxWithService(); d > limit {
			t.Errorf("Lauf dauerte %v, Höchstdauer %v", d, limit)
		}
		res.want(t, autoFailed, reasonRestart, "das alte Binary ist zurück")
		if res.RolledBack == nil || !*res.RolledBack || !strings.Contains(res.Error, wantErr) {
			t.Errorf("Rückweg: %+v", res)
		}
		if b, _ := os.ReadFile(e.exe); string(b) != "alt" {
			t.Errorf("Binary nicht zurück: %q", b)
		}
		if all, _ := e.restarts(); all != 2 {
			t.Errorf("%d Neustarts: %q", all, e.sd.calls)
		}
	}
	t.Run("Neustart hängt, Signal", func(t *testing.T) {
		send := withSignals(t)
		e := newAutoEnv(t, "v0.1.0")
		e.initNode(t)
		shortDeadlines()
		e.sd.hangRestarts = 1
		var signalled time.Time
		e.sd.onRestart = func() {
			if signalled.IsZero() {
				signalled = time.Now()
				send(syscall.SIGTERM)
			}
		}
		e.sd.calls = nil
		start := time.Now()
		check(t, e, start, "Neustart nach 400ms abgebrochen")
		if d := time.Since(signalled); d > autoMaxWithService() {
			t.Errorf("vom Signal bis zum Ende %v", d)
		}
	})
	t.Run("Abfrage hängt", func(t *testing.T) {
		e := newAutoEnv(t, "v0.1.0")
		e.initNode(t)
		shortDeadlines()
		e.sd.onRestart = func() { e.sd.hangShow = true }
		e.sd.calls = nil
		check(t, e, time.Now(), "Frist 300ms")
	})
	t.Run("Probe hängt", func(t *testing.T) {
		e := newAutoEnv(t, "v0.1.0")
		e.initNode(t)
		shortDeadlines()
		oldProbe := autoProbeTimeout
		autoProbeTimeout = time.Hour
		t.Cleanup(func() { autoProbeTimeout = oldProbe })
		autoProbeNode = func(ctx context.Context, _ string, timeout time.Duration) (nodeInfo, error) {
			if timeout > autoRestartWithin {
				t.Errorf("Probe mit %v, Frist %v", timeout, autoRestartWithin)
			}
			<-ctx.Done()
			return nodeInfo{}, ctx.Err()
		}
		e.sd.calls = nil
		start := time.Now()
		check(t, e, start, "Frist 300ms")
		if d := time.Since(start); d > autoRestartWithin+500*time.Millisecond {
			t.Errorf("Prüfung dauerte %v, Frist %v", d, autoRestartWithin)
		}
	})
}

// Hängt die Erweiterung für VS Code, bricht --auto sie nach ihrer Frist ab:
// upgraded mit Warnung, kein Prozess bleibt.
func TestUpgradeAutoExtensionDeadline(t *testing.T) {
	e := newAutoEnv(t, "v0.1.0")
	e.initNode(t)
	e.withExtension(t)
	autoExtensionTimeout = 200 * time.Millisecond
	e.control(t, "hang-vscode")
	start := time.Now()
	res := e.auto(t, 0)
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("Lauf dauerte %v", d)
	}
	res.want(t, autoUpgraded, "")
	if !strings.Contains(strings.Join(res.Warnings, "\n"), "Erweiterung für VS Code nach 200ms abgebrochen — von Hand") {
		t.Errorf("Warnungen: %q", res.Warnings)
	}
	b, err := os.ReadFile(filepath.Join(e.script, "vscode.pid"))
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	gone(t, pid)
}
