package main

import (
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

// Signale in upgrade --auto (Task 037). SIGTERM, SIGINT und SIGHUP fängt nur
// der Zweig --auto ab; die anderen Modi von upgrade bleiben beim
// NotifyContext in runUpgrade. Die Grenze liegt unmittelbar vor dem Ersetzen
// (Stufe 12), beim Nachholen eines Neustarts unmittelbar vor dem Neustart:
//
//   - Davor bricht ein Signal den Kontext der Stufen ab. Der Lauf räumt auf
//     und endet mit failed/interrupted, gleich welchen Fehler die Stufe
//     meldet; Binary, Dienst und Datenbanken bleiben unverändert.
//   - Danach wird es vermerkt, aber nicht befolgt: Ersetzen, Neustart,
//     Prüfung und Rückweg laufen auf einem Kontext ohne Abbruch mit eigenen
//     Fristen zu Ende, höchstens autoMaxWithService bzw.
//     autoMaxWithoutService. Sofort kommt eine Zeile auf stderr, ins Ergebnis
//     eine Warnung, und die Erweiterung für VS Code entfällt bzw. bricht ab.
//
// Ein zweites Signal ändert daran nichts; SIGKILL bleibt der harte Abbruch.

// autoNotify liefert den Kanal mit den Signalen für upgrade --auto und die
// Funktion, die den Empfang beendet. Tests speisen Signale über einen eigenen
// Kanal ein.
var autoNotify = func() (<-chan os.Signal, func()) {
	ch := make(chan os.Signal, 4)
	signal.Notify(ch, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	return ch, func() { signal.Stop(ch) }
}

// autoSignalSeen wird gerufen, wenn der Lauf ein Signal verarbeitet hat; nur
// Tests setzen es, um darauf zu warten.
var autoSignalSeen func(os.Signal)

// autoSlack ist der Spielraum in der Höchstdauer: Ersetzen, Zurücksetzen,
// Aufräumen und das Beenden der Erweiterung für VS Code.
var autoSlack = 5 * time.Second

// autoMaxWithService ist die Höchstdauer vom Signal bis zum Ende, wenn ein
// Dienst pro User läuft: zwei Neustarts (der des Upgrades und der des
// Rückwegs) mit je autoRestartTimeout, die Prüfung dazwischen und Spielraum.
// Mit den Standardwerten 75 s.
func autoMaxWithService() time.Duration {
	return 2*autoRestartTimeout + autoRestartWithin + autoSlack
}

// autoMaxWithoutService ist die Höchstdauer vom Signal bis zum Ende ohne
// laufenden Dienst: nur Ersetzen und Aufräumen. Mit den Standardwerten 5 s.
func autoMaxWithoutService() time.Duration {
	return autoSlack
}

// autoGuard verfolgt die Signale eines Laufs von upgrade --auto.
type autoGuard struct {
	// cancel bricht den Kontext der Stufen ab.
	cancel context.CancelFunc
	stderr io.Writer

	mu sync.Mutex
	// first ist das erste Signal, after das erste nach der Grenze.
	first, after os.Signal
	// crossed sagt, ob die Grenze überschritten ist; running, ob danach ein
	// Dienst neu gestartet wird.
	crossed, running bool

	done chan struct{}
	wg   sync.WaitGroup
}

// watchSignals beginnt, Signale für den Lauf abzufangen. g.stop beendet das
// Zuhören, bevor das Ergebnis ausgegeben wird; danach schreibt der Wächter
// nichts mehr auf stderr. Abgefangen bleiben die Signale, bis release sie
// freigibt — erst nach der Ausgabe, damit sie nicht abreißt.
func watchSignals(cancel context.CancelFunc, stderr io.Writer) (g *autoGuard, release func()) {
	ch, rel := autoNotify()
	g = &autoGuard{cancel: cancel, stderr: stderr, done: make(chan struct{})}
	g.wg.Add(1)
	go func() {
		defer g.wg.Done()
		for {
			select {
			case sig := <-ch:
				g.receive(sig)
			case <-g.done:
				return
			}
		}
	}()
	return g, rel
}

// stop beendet das Zuhören und wartet, bis der Wächter fertig ist.
func (g *autoGuard) stop() {
	close(g.done)
	g.wg.Wait()
}

// receive vermerkt ein Signal. Vor der Grenze bricht es die Stufen ab; danach
// meldet es sofort auf stderr, wie lange der Lauf noch dauert. Der Kontext
// der Stufen wird in jedem Fall abgebrochen — nach der Grenze trifft das nur
// noch die Erweiterung für VS Code.
func (g *autoGuard) receive(sig os.Signal) {
	g.mu.Lock()
	if g.first == nil {
		g.first = sig
	}
	crossed, running := g.crossed, g.running
	if crossed && g.after == nil {
		g.after = sig
	}
	g.mu.Unlock()
	g.cancel()
	if crossed {
		what, limit := "Ersetzen läuft", autoMaxWithoutService()
		if running {
			what, limit = "Neustart läuft", autoMaxWithService()
		}
		fmt.Fprintf(g.stderr, "upgrade --auto: %s empfangen, %s, endet spätestens in %d s\n", signalName(sig), what,
			seconds(limit))
	}
	if autoSignalSeen != nil {
		autoSignalSeen(sig)
	}
}

// cross ist die Grenze. Kam schon ein Signal, liefert sie es, und der Lauf
// bricht ab, ohne etwas zu verändern. Sonst gilt ab hier jedes Signal als
// nach der Grenze; running sagt, ob ein Dienst neu gestartet wird.
func (g *autoGuard) cross(running bool) os.Signal {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.first != nil {
		return g.first
	}
	g.crossed, g.running = true, running
	return nil
}

// before liefert das Signal, das vor der Grenze kam, sonst nil.
func (g *autoGuard) before() os.Signal {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.crossed {
		return nil
	}
	return g.first
}

// afterCross liefert das erste Signal nach der Grenze, sonst nil.
func (g *autoGuard) afterCross() os.Signal {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.after
}

// signalName nennt ein Signal mit seinem üblichen Namen.
func signalName(sig os.Signal) string {
	switch sig {
	case syscall.SIGTERM:
		return "SIGTERM"
	case syscall.SIGINT:
		return "SIGINT"
	case syscall.SIGHUP:
		return "SIGHUP"
	}
	return sig.String()
}

// seconds rundet d auf ganze Sekunden auf.
func seconds(d time.Duration) int {
	return int(math.Ceil(d.Seconds()))
}
