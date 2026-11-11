package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/kephalaion/kephalaion/internal/upgrade"
)

// newPendingEnv ist ein Rechner, auf dem ein Neustart aussteht: Das Binary
// ist schon v0.2.0, das neueste Release, der Dienst pro User läuft aber noch
// mit node. Nach einem Neustart meldet der Node *after — anfangs v0.2.0.
func newPendingEnv(t *testing.T, node string) (*autoEnv, *string) {
	t.Helper()
	e := newAutoEnv(t, "v0.2.0")
	e.initNode(t)
	// Die Probe läuft mit dem installierten Binary.
	if err := os.WriteFile(e.exe, e.newBin, 0o755); err != nil {
		t.Fatal(err)
	}
	after := "v0.2.0"
	e.probe = func(string) (nodeInfo, error) {
		if !e.sd.active {
			return nodeInfo{}, errors.New("nicht erreichbar")
		}
		if all, _ := e.restarts(); all > 0 {
			return nodeInfo{Version: after}, nil
		}
		return nodeInfo{Version: node}, nil
	}
	e.sd.calls = nil
	return e, &after
}

// backupFile legt eine Rückweg-Datei mit der Version version an, wie Swap
// sie anlegt: als harten Link auf ein altes Binary mit einer Änderungszeit
// von vor Tagen.
func (e *autoEnv) backupFile(t *testing.T, version string) string {
	t.Helper()
	dir := filepath.Dir(e.exe)
	// Im selben Verzeichnis: Der harte Link braucht dasselbe Dateisystem.
	old := filepath.Join(dir, "kephalaion-"+version+".alt")
	if err := os.WriteFile(old, []byte("alt "+version), 0o755); err != nil {
		t.Fatal(err)
	}
	days := time.Now().Add(-72 * time.Hour)
	if err := os.Chtimes(old, days, days); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(dir, ".kephalaion-upgrade-backup-"+version+"-20261101000000-1")
	if err := os.Link(old, backup); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(old); err != nil {
		t.Fatal(err)
	}
	return backup
}

// tempFile legt eine temporäre Datei nach TempPattern mit der Änderungszeit
// mtime neben das Binary.
func (e *autoEnv) tempFile(t *testing.T, name string, mtime time.Time) string {
	t.Helper()
	p := filepath.Join(filepath.Dir(e.exe), name)
	if err := os.WriteFile(p, []byte("rest"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	return p
}

// Der Node meldet ein älteres Release als das Binary: --auto holt den
// Neustart nach — ohne Download und ohne Ersetzen, die Probe mit dem
// installierten Binary, die Hubs gegen die Version des Binarys —, upgraded
// mit from gleich der Version des Nodes. Eine Rückweg-Datei mit dieser
// Version ist danach weg.
func TestUpgradeAutoPendingRestart(t *testing.T) {
	e, _ := newPendingEnv(t, "v0.1.0")
	backup := e.backupFile(t, "v0.1.0")
	res := e.auto(t, 0)
	res.want(t, autoUpgraded, "", "Neustart nachgeholt", "v0.1.0")
	if res.From != "v0.1.0" || res.To != "v0.2.0" || res.Restarted == nil || !*res.Restarted ||
		res.RolledBack != nil || res.Error != "" {
		t.Errorf("nachgeholt: %+v", res)
	}
	if all, grouped := e.restarts(); all != 1 || grouped != 1 {
		t.Errorf("%d Neustarts, %d mit eigener Gruppe: %q", all, grouped, e.sd.calls)
	}
	if len(e.renames) != 0 {
		t.Errorf("ersetzt: %v", e.renames)
	}
	if got := e.preflightCalls(t); len(got) != 1 || got[0] != "upgrade --preflight --json|"+e.cfgPath {
		t.Errorf("Aufrufe des installierten Binarys: %q", got)
	}
	if strings.Join(e.asked, ",") != "vm" {
		t.Errorf("gefragt: %v", e.asked)
	}
	if b, _ := os.ReadFile(e.exe); string(b) != string(e.newBin) {
		t.Errorf("Binary verändert: %q", b)
	}
	if exists(backup) {
		t.Error("Rückweg-Datei liegt noch da")
	}
	if !strings.Contains(strings.Join(res.Warnings, "\n"), "Rückweg-Dateien früherer Läufe entfernt: "+backup) {
		t.Errorf("Warnungen: %q", res.Warnings)
	}
	assertNoUpgradeTemp(t, filepath.Dir(e.exe))
}

// Scheitert die Prüfung beim Nachholen, kommt die Rückweg-Datei mit der
// Version des Nodes zurück — auch wenn sie als harter Link alt aussieht: Das
// Aufräumen nach Stufe 9 nimmt sie aus, eine alte temporäre Datei nicht.
// Ohne passende Rückweg-Datei bleibt es bei failed/restart ohne Rückweg.
func TestUpgradeAutoPendingRestartFailed(t *testing.T) {
	t.Run("mit Rückweg", func(t *testing.T) {
		e, after := newPendingEnv(t, "v0.1.0")
		*after = "v0.1.0"
		other := e.backupFile(t, "v0.0.9")
		backup := e.backupFile(t, "v0.1.0")
		stale := e.tempFile(t, ".kephalaion-upgrade-123", time.Now().Add(-2*time.Hour))
		res := e.auto(t, 1)
		res.want(t, autoFailed, reasonRestart, "das alte Binary ist zurück")
		if res.From != "v0.1.0" || res.RolledBack == nil || !*res.RolledBack || res.Restarted == nil || !*res.Restarted {
			t.Errorf("Rückweg: %+v", res)
		}
		if b, _ := os.ReadFile(e.exe); string(b) != "alt v0.1.0" {
			t.Errorf("Binary nicht zurück: %q", b)
		}
		if all, _ := e.restarts(); all != 2 {
			t.Errorf("%d Neustarts: %q", all, e.sd.calls)
		}
		if exists(backup) || exists(stale) || !exists(other) {
			t.Errorf("Rückweg %v, alte Datei %v, anderer Rückweg %v", exists(backup), exists(stale), exists(other))
		}
		joined := strings.Join(res.Warnings, "\n")
		if !strings.Contains(joined, "alte temporäre Dateien neben dem Binary entfernt: "+stale) ||
			!strings.Contains(joined, other+" (Version v0.0.9)") {
			t.Errorf("Warnungen: %q", res.Warnings)
		}
	})
	t.Run("ohne Rückweg", func(t *testing.T) {
		e, after := newPendingEnv(t, "v0.1.0")
		*after = "v0.1.0"
		e.backupFile(t, "v0.0.9")
		res := e.auto(t, 1)
		res.want(t, autoFailed, reasonRestart, "kein Rückweg mit v0.1.0", "kephalaion status",
			"kephalaion upgrade --version v0.1.0")
		if res.RolledBack == nil || *res.RolledBack || res.Restarted == nil || !*res.Restarted {
			t.Errorf("ohne Rückweg: %+v", res)
		}
		if b, _ := os.ReadFile(e.exe); string(b) != string(e.newBin) {
			t.Errorf("Binary verändert: %q", b)
		}
		if all, _ := e.restarts(); all != 1 {
			t.Errorf("%d Neustarts: %q", all, e.sd.calls)
		}
	})
}

// Kein Neustart auf Verdacht: Der Node antwortet nicht, meldet dev, nichts,
// eine neuere Version, oder es läuft kein Dienst — current, Exit 0. Liegt
// eine Rückweg-Datei da, bleibt sie, und eine Warnung nennt sie samt Weg von
// Hand.
func TestUpgradeAutoPendingNot(t *testing.T) {
	for name, tc := range map[string]struct {
		node    string
		silent  bool
		stopped bool
		lead    string
	}{
		"stumm":       {silent: true, lead: "der Node antwortet nicht"},
		"dev":         {node: "dev"},
		"leer":        {node: ""},
		"Suffix":      {node: "v0.1.0-rc1"},
		"neuer":       {node: "v0.3.0"},
		"kein Dienst": {node: "v0.1.0", stopped: true},
	} {
		t.Run(name, func(t *testing.T) {
			e, _ := newPendingEnv(t, tc.node)
			if tc.silent {
				e.probe = func(string) (nodeInfo, error) { return nodeInfo{}, errors.New("Zeitüberschreitung") }
			}
			if tc.stopped {
				e.sd.active = false
				e.probe = func(string) (nodeInfo, error) { return nodeInfo{}, errors.New("nicht erreichbar") }
			}
			backup := e.backupFile(t, "v0.1.0")
			res := e.auto(t, 0)
			res.want(t, autoCurrent, "", "nichts zu tun")
			if res.Restarted != nil || len(e.asked) != 0 {
				t.Errorf("current: %+v, gefragt %v", res, e.asked)
			}
			if all, _ := e.restarts(); all != 0 {
				t.Errorf("Neustart: %q", e.sd.calls)
			}
			if !exists(backup) {
				t.Error("Rückweg-Datei entfernt")
			}
			joined := strings.Join(res.Warnings, "\n")
			if !strings.Contains(joined, backup+" (Version v0.1.0)") || !strings.Contains(joined, "kephalaion status") ||
				!strings.Contains(joined, tc.lead) {
				t.Errorf("Warnungen: %q", res.Warnings)
			}
			if exists(filepath.Join(filepath.Dir(e.exe), upgrade.LockName)) {
				t.Error("Sperrdatei angelegt")
			}
		})
	}
}

// Meldet der Node die Version des Binarys, ist es current, und eine
// liegengebliebene Rückweg-Datei ist danach weg — es sei denn, die Sperre
// ist belegt.
func TestUpgradeAutoCurrentRemovesBackups(t *testing.T) {
	e, _ := newPendingEnv(t, "v0.2.0")
	backup := e.backupFile(t, "v0.1.0")
	lock, err := upgrade.Lock(filepath.Dir(e.exe))
	if err != nil {
		t.Fatal(err)
	}
	res := e.auto(t, 0)
	res.want(t, autoCurrent, "", "der Node läuft damit")
	if !exists(backup) || !strings.Contains(strings.Join(res.Warnings, "\n"), backup) {
		t.Errorf("bei belegter Sperre: Rückweg %v, Warnungen %q", exists(backup), res.Warnings)
	}
	_ = lock.Close()

	res = e.auto(t, 0)
	res.want(t, autoCurrent, "", "der Node läuft damit")
	if exists(backup) {
		t.Error("Rückweg-Datei liegt noch da")
	}
	if !strings.Contains(strings.Join(res.Warnings, "\n"), "Rückweg-Dateien entfernt, der Node läuft mit der "+
		"Version des Binarys: "+backup) {
		t.Errorf("Warnungen: %q", res.Warnings)
	}
	if all, _ := e.restarts(); all != 0 {
		t.Errorf("Neustart: %q", e.sd.calls)
	}
}

// Hält der Dienst zwischen der Frage an Stufe 3 und Stufe 7 an, wird nichts
// nachgeholt: current, kein Neustart, und die Rückweg-Datei bleibt, von
// einer Warnung genannt — der Lauf meldet nicht upgraded, ohne neu gestartet
// zu haben.
func TestUpgradeAutoPendingServiceStopped(t *testing.T) {
	e, _ := newPendingEnv(t, "v0.1.0")
	backup := e.backupFile(t, "v0.1.0")
	probe := e.probe
	e.probe = func(listen string) (nodeInfo, error) {
		info, err := probe(listen)
		// Nach der Frage an Stufe 3 hält der Dienst an.
		e.sd.active = false
		return info, err
	}
	res := e.auto(t, 0)
	res.want(t, autoCurrent, "", "läuft inzwischen nicht mehr", "v0.1.0")
	if res.Restarted != nil {
		t.Errorf("restarted gesetzt: %+v", res)
	}
	if all, _ := e.restarts(); all != 0 {
		t.Errorf("Neustart: %q", e.sd.calls)
	}
	if !exists(backup) {
		t.Error("Rückweg-Datei entfernt")
	}
	if !strings.Contains(strings.Join(res.Warnings, "\n"), backup) {
		t.Errorf("Warnungen: %q", res.Warnings)
	}
}

// Beim Nachholen liegt die Grenze vor dem Neustart: Ein Signal davor ist
// failed/interrupted, Binary und Dienst bleiben. Hubs älter als die Version
// des Binarys: skipped/hub_older wie beim Upgrade.
func TestUpgradeAutoPendingStops(t *testing.T) {
	t.Run("Signal vor dem Neustart", func(t *testing.T) {
		send := withSignals(t)
		e, _ := newPendingEnv(t, "v0.1.0")
		autoPreflight = func(ctx context.Context, bin, cfgPath string) ([]byte, error) {
			out, err := execPreflight(ctx, bin, cfgPath)
			send(syscall.SIGTERM)
			return out, err
		}
		res, _ := e.autoRun(t, 1)
		res.interrupted(t, "SIGTERM")
		if all, _ := e.restarts(); all != 0 {
			t.Errorf("Neustart: %q", e.sd.calls)
		}
		if b, _ := os.ReadFile(e.exe); string(b) != string(e.newBin) {
			t.Errorf("Binary verändert: %q", b)
		}
	})
	t.Run("Hub älter", func(t *testing.T) {
		e, _ := newPendingEnv(t, "v0.1.0")
		e.hubs["vm"] = hubAnswer{release: "v0.1.5"}
		e.auto(t, 0).want(t, autoSkipped, reasonHubOlder, "Hub vm hat v0.1.5", "v0.2.0")
		if all, _ := e.restarts(); all != 0 {
			t.Errorf("Neustart: %q", e.sd.calls)
		}
	})
}

// Nach der Sperre räumt das Upgrade alte temporäre Dateien weg; eine frische
// (upgrade ohne --auto ohne Sperre) und die Sperrdatei bleiben.
func TestUpgradeAutoRemovesStaleTemp(t *testing.T) {
	e := newAutoEnv(t, "v0.1.0")
	e.initNode(t)
	stale := e.tempFile(t, ".kephalaion-upgrade-111", time.Now().Add(-2*time.Hour))
	fresh := e.tempFile(t, ".kephalaion-upgrade-222", time.Now())
	lockPath := filepath.Join(filepath.Dir(e.exe), upgrade.LockName)
	res := e.auto(t, 0)
	res.want(t, autoUpgraded, "")
	if exists(stale) || !exists(fresh) || !exists(lockPath) {
		t.Errorf("alt %v, frisch %v, Sperrdatei %v", exists(stale), exists(fresh), exists(lockPath))
	}
	if !strings.Contains(strings.Join(res.Warnings, "\n"), "alte temporäre Dateien neben dem Binary entfernt: "+stale) {
		t.Errorf("Warnungen: %q", res.Warnings)
	}
}
