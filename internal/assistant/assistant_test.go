package assistant

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// ExecRunner mit einem harmlosen Programm (sh), nie mit einem Assistenten.
// Die Wartezeit auf die Ausgabe ist hier kurz, damit die Tests schnell
// bleiben; ExecRunner nimmt execWaitDelay.

// testWait ist die Wartezeit auf die Ausgabe in diesen Tests.
const testWait = 100 * time.Millisecond

// killLater beendet nach dem Test den Prozess, dessen PID in der ersten Zeile
// von out steht: den Kindprozess, den sh zurückgelassen hat.
func killLater(t *testing.T, out string) {
	t.Helper()
	line, _, _ := strings.Cut(out, "\n")
	pid, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		t.Errorf("keine PID in der Ausgabe: %q", out)
		return
	}
	t.Cleanup(func() {
		if p, err := os.FindProcess(pid); err == nil {
			_ = p.Kill()
		}
	})
}

// Die Frist: Ein Programm, das nicht endet, wird beendet, und der Fehler sagt
// „keine Antwort in der Frist“ — auch wenn ein Kindprozess, den es
// zurücklässt, Standard- und Fehlerausgabe offen hält. Der Aufruf kehrt an
// der Frist zurück, nicht erst, wenn der Kindprozess endet (30 s).
func TestExecRunnerDeadline(t *testing.T) {
	cases := []struct {
		name   string
		script string
	}{
		{"ohne Kindprozess", "exec sleep 30"},
		{"Kindprozess hält die Ausgabe offen", "sleep 30 & echo $!; echo läuft >&2; exec sleep 30"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			const frist = 200 * time.Millisecond
			ctx, cancel := context.WithTimeout(context.Background(), frist)
			defer cancel()
			start := time.Now()
			out, err := execRun(ctx, testWait, "sh", "-c", c.script)
			took := time.Since(start)
			if strings.Contains(c.script, "&") {
				killLater(t, out)
			}
			if err == nil || err.Error() != "sh -c "+c.script+": keine Antwort in der Frist" {
				t.Errorf("Fehler: %v", err)
			}
			// Die Frist ist die Obergrenze; der Zuschlag gilt nur der
			// Planung des Rechners, nicht dem Kindprozess.
			if took > frist+time.Second {
				t.Errorf("zurück nach %v, Frist %v", took, frist)
			}
		})
	}
}

// Endet das Programm von selbst mit 0 und hält nur ein zurückgelassener
// Kindprozess die Ausgabe offen, ist der Aufruf nach der Wartezeit
// erfolgreich, mit der Ausgabe des Programms.
func TestExecRunnerOrphanAfterSuccess(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	out, err := execRun(ctx, testWait, "sh", "-c", "sleep 30 & echo $!; echo fertig")
	took := time.Since(start)
	killLater(t, out)
	if err != nil || !strings.HasSuffix(out, "\nfertig\n") {
		t.Errorf("Ausgabe %q, Fehler %v", out, err)
	}
	if took > 2*time.Second {
		t.Errorf("zurück nach %v", took)
	}
}

// Die Standardeingabe ist geschlossen: Wer liest, bekommt sofort ihr Ende und
// wartet nicht auf eine Antwort. Ein Fehler nennt die letzte Zeile der
// Fehlerausgabe.
func TestExecRunner(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := ExecRunner(ctx, "sh", "-c", `if read -r line; then echo "gelesen: $line"; else echo Ende; fi`)
	if err != nil || out != "Ende\n" {
		t.Errorf("Standardeingabe: Ausgabe %q, Fehler %v", out, err)
	}
	out, err = ExecRunner(ctx, "sh", "-c", "echo teil; echo erste >&2; echo letzte >&2; exit 3")
	if out != "teil\n" || err == nil ||
		err.Error() != "sh -c echo teil; echo erste >&2; echo letzte >&2; exit 3: exit status 3: letzte" {
		t.Errorf("Fehlerausgabe: Ausgabe %q, Fehler %v", out, err)
	}
}
