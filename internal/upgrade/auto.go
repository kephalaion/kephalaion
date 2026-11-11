package upgrade

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// Die Bausteine für kephalaion upgrade --auto (Task 036). Die Regel, wann ein
// Upgrade ohne Menschen gehen darf, steht in cmd/kephalaion; hier stehen die
// Schritte am Binary: die Frage an GitHub samt Report, der Download neben
// das Binary, das Ersetzen mit Rückweg, das Zurücksetzen und die Sperre.

// LockName ist die Sperrdatei von upgrade --auto neben dem Binary. Sie ist
// nicht nach TempPattern benannt: Sie bleibt liegen und ist kein Rest eines
// abgebrochenen Laufs.
const LockName = ".kephalaion-upgrade.lock"

// ErrBusy meldet, dass ein anderer Lauf die Sperre hält.
var ErrBusy = errors.New("ein anderer upgrade --auto läuft gerade")

// Latest ist die Antwort von GitHub für upgrade --auto: der Report wie bei
// upgrade --check --json, dazu Ziel und installierte Version.
type Latest struct {
	// Report ist der Report wie bei upgrade --check --json; scheitert die
	// Frage, ist State failed.
	Report Report
	// Target ist das neueste Release ohne Suffix; leer, wenn die Frage
	// gescheitert ist.
	Target Version
	// Current ist die installierte Version; IsDev sagt, ob es ein dev build
	// ist (dann gilt Current nicht).
	Current Version
	IsDev   bool
	rel     release
}

// Latest fragt GitHub nach dem neuesten Release. Der Report steht in jedem
// Fall darin, auch wenn err gesetzt ist.
func (u *Upgrader) Latest(ctx context.Context) (Latest, error) {
	current, isDev := u.current()
	self, dir, _ := u.access()
	l := Latest{Current: current, IsDev: isDev}
	r := Report{State: StateOK, CheckedAt: u.now().UTC().Format(time.RFC3339), Version: u.Current.Version,
		DevBuild: isDev, SelfUpgrade: self}
	rel, target, err := u.release(ctx, "")
	if err != nil {
		r.State, r.Error = StateFailed, err.Error()
	} else {
		r.Latest = target.String()
		r.UpdateAvailable = !isDev && target.Compare(current) > 0
		l.Target, l.rel = target, rel
	}
	r.Method, r.Command, r.Hint = u.way(self, dir, r.Latest, isDev)
	l.Report = r
	return l, err
}

// ExecutablePath liefert den Pfad des Binarys, das upgrade ersetzt, mit
// aufgelösten Links.
func (u *Upgrader) ExecutablePath() (string, error) {
	return u.executablePath()
}

// WriteAccess prüft, ob sich dieses Binary selbst ersetzen lässt (wie
// self_upgrade im Report); why sagt sonst, warum nicht, hint nennt den Weg.
func (u *Upgrader) WriteAccess() (ok bool, why, hint string) {
	ok, dir, why := u.access()
	_, isDev := u.current()
	_, _, hint = u.way(ok, dir, "", isDev)
	return ok, why, hint
}

// Stage lädt das Binary des Releases aus l in eine temporäre Datei neben exe
// (TempPattern), prüft es gegen SHA256SUMS und macht es ausführbar. exe
// bleibt unverändert. Scheitert ein Schritt, liegt keine temporäre Datei mehr
// da.
func (u *Upgrader) Stage(ctx context.Context, l Latest, exe string) (string, error) {
	bin, want, err := u.assetAndSum(ctx, l.rel, l.Target)
	if err != nil {
		return "", err
	}
	return u.download(ctx, filepath.Dir(exe), bin, want)
}

// Swap ersetzt exe atomar durch staged und behält das alte Binary als
// Rückweg: eine Datei nach TempPattern neben exe — ein harter Link auf das
// alte Binary, wo das Dateisystem keinen erlaubt, eine Kopie. Scheitert es,
// sind staged und der Rückweg entfernt, und exe ist unverändert.
func (u *Upgrader) Swap(staged, exe string) (backup string, err error) {
	defer func() {
		if err != nil {
			_ = os.Remove(staged)
			if backup != "" {
				_ = os.Remove(backup)
			}
			backup = ""
		}
	}()
	backup, err = keepBackup(exe)
	if err != nil {
		return backup, fmt.Errorf("Rückweg für %s nicht anlegbar: %w", exe, err)
	}
	if err = u.rename(staged, exe); err != nil {
		return backup, replaceError(exe, err)
	}
	return backup, nil
}

// keepBackup legt neben exe eine Datei nach TempPattern an, die das alte
// Binary hält.
func keepBackup(exe string) (string, error) {
	f, err := os.CreateTemp(filepath.Dir(exe), TempPattern)
	if err != nil {
		return "", err
	}
	name := f.Name()
	_ = f.Close()
	// Der Name ist reserviert; der Link braucht ihn frei. Die Sperre von
	// upgrade --auto hält andere Läufe fern.
	if err := os.Remove(name); err != nil {
		return "", err
	}
	if err := os.Link(exe, name); err == nil {
		return name, nil
	}
	if err := copyFile(exe, name); err != nil {
		_ = os.Remove(name)
		return "", err
	}
	return name, nil
}

// copyFile kopiert src nach dst (neu angelegt) mit den Rechten von src.
func copyFile(src, dst string) (err error) {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	fi, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fi.Mode().Perm())
	if err != nil {
		return err
	}
	defer func() {
		if cerr := out.Close(); err == nil {
			err = cerr
		}
	}()
	if _, err = io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

// Restore setzt den Rückweg atomar wieder an die Stelle von exe.
func (u *Upgrader) Restore(backup, exe string) error {
	if err := u.rename(backup, exe); err != nil {
		return fmt.Errorf("altes Binary nicht zurückgesetzt (%s → %s): %w", backup, exe, err)
	}
	return nil
}

// Lock nimmt die Sperre von upgrade --auto im Verzeichnis dir, ohne zu
// warten: flock exklusiv auf LockName, die dafür angelegt wird und liegen
// bleibt — wie die Sperre von serve. Sie hält, bis die Datei geschlossen
// wird, und fällt mit dem Prozess. Hält ein anderer Lauf sie, ist der Fehler
// ErrBusy.
func Lock(dir string) (*os.File, error) {
	path := filepath.Join(dir, LockName)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("Sperrdatei %s: %w", path, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("%w (Sperre %s)", ErrBusy, path)
		}
		return nil, fmt.Errorf("Sperrdatei %s: %w", path, err)
	}
	return f, nil
}
