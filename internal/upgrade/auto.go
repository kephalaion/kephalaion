package upgrade

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
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

// BackupPattern ist das Namensmuster der Rückweg-Datei, die Swap anlegt:
// .kephalaion-upgrade-backup-<version>-<zeit>-<zufall> mit der Version des
// alten Binarys und der Zeit des Anlegens (UTC, JJJJMMTThhmmss). Es passt
// auch auf TempPattern; das Aufräumen alter temporärer Dateien nimmt die
// Rückweg-Dateien aus, denn als harter Link tragen sie die Änderungszeit des
// alten Binarys (Task 037).
const BackupPattern = backupPrefix + "*"

const (
	backupPrefix = ".kephalaion-upgrade-backup-"
	backupStamp  = "20060102150405"
)

// Backup ist eine Rückweg-Datei neben dem Binary.
type Backup struct {
	// Path ist der Pfad der Datei.
	Path string
	// Version ist die Version des alten Binarys, Stamp die Zeit des Anlegens
	// wie im Namen; beide leer, wenn der Name sie nicht hergibt.
	Version, Stamp string
}

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
// Rückweg: eine Datei nach BackupPattern neben exe, mit der Version dieses
// Binarys (u.Current) im Namen — ein harter Link auf das alte Binary, wo das
// Dateisystem keinen erlaubt, eine Kopie. Scheitert es, sind staged und der
// Rückweg entfernt, und exe ist unverändert.
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
	backup, err = keepBackup(exe, u.Current.Version, u.now())
	if err != nil {
		return backup, fmt.Errorf("Rückweg für %s nicht anlegbar: %w", exe, err)
	}
	if err = u.rename(staged, exe); err != nil {
		return backup, replaceError(exe, err)
	}
	return backup, nil
}

// keepBackup legt neben exe eine Datei nach BackupPattern an, die das alte
// Binary mit der Version version hält.
func keepBackup(exe, version string, now time.Time) (string, error) {
	prefix := backupPrefix + safeName(version) + "-" + now.UTC().Format(backupStamp) + "-"
	f, err := os.CreateTemp(filepath.Dir(exe), prefix+"*")
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

// safeName macht aus einer Version einen Teil eines Dateinamens: Zeichen
// außer Buchstaben, Ziffern, Punkt und Bindestrich werden zu _.
func safeName(version string) string {
	if version == "" {
		return "unbekannt"
	}
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-':
			return r
		}
		return '_'
	}, version)
}

// parseBackup liest Version und Zeit aus dem Namen einer Rückweg-Datei, von
// hinten: Die Version selbst kann Bindestriche tragen.
func parseBackup(path string) Backup {
	b := Backup{Path: path}
	rest, ok := strings.CutPrefix(filepath.Base(path), backupPrefix)
	if !ok {
		return b
	}
	i := strings.LastIndex(rest, "-")
	if i < 0 {
		return b
	}
	rest = rest[:i]
	j := strings.LastIndex(rest, "-")
	if j < 0 {
		return b
	}
	b.Version, b.Stamp = rest[:j], rest[j+1:]
	return b
}

// Backups liefert die Rückweg-Dateien in dir, die jüngste zuerst.
func Backups(dir string) ([]Backup, error) {
	paths, err := filepath.Glob(filepath.Join(dir, BackupPattern))
	if err != nil {
		return nil, err
	}
	out := make([]Backup, 0, len(paths))
	for _, p := range paths {
		out = append(out, parseBackup(p))
	}
	slices.SortStableFunc(out, func(a, b Backup) int { return strings.Compare(b.Stamp, a.Stamp) })
	return out, nil
}

// RemoveBackups entfernt alle Rückweg-Dateien in dir und liefert die
// entfernten.
func RemoveBackups(dir string) ([]string, error) {
	backups, err := Backups(dir)
	if err != nil {
		return nil, err
	}
	var removed []string
	for _, b := range backups {
		if err := os.Remove(b.Path); err != nil {
			return removed, err
		}
		removed = append(removed, b.Path)
	}
	return removed, nil
}

// RemoveStale entfernt in dir die Dateien nach TempPattern, die keine
// Rückweg-Dateien sind und zuletzt vor mehr als maxAge geändert wurden —
// Reste abgebrochener Läufe. Die Altersgrenze schützt die temporäre Datei
// eines gleichzeitigen upgrade ohne --auto, der die Sperre nicht nimmt. Es
// liefert die entfernten.
func RemoveStale(dir string, maxAge time.Duration, now time.Time) ([]string, error) {
	paths, err := filepath.Glob(filepath.Join(dir, TempPattern))
	if err != nil {
		return nil, err
	}
	var removed []string
	for _, p := range paths {
		if backup, _ := filepath.Match(BackupPattern, filepath.Base(p)); backup {
			continue
		}
		fi, err := os.Lstat(p)
		if err != nil || !fi.Mode().IsRegular() || now.Sub(fi.ModTime()) <= maxAge {
			continue
		}
		if err := os.Remove(p); err != nil {
			return removed, err
		}
		removed = append(removed, p)
	}
	return removed, nil
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
