// Package upgrade ersetzt das laufende Binary durch das Binary eines Releases
// von GitHub: Version ermitteln, Asset und SHA256SUMS laden, Prüfsumme prüfen,
// atomar austauschen. Jeder Fehler lässt das alte Binary unberührt.
package upgrade

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kephalaion/kephalaion/internal/buildinfo"
)

// TempPattern ist das Namensmuster der temporären Datei neben dem Binary, an
// dem sich Reste eines abgebrochenen Laufs erkennen lassen.
const TempPattern = ".kephalaion-upgrade-*"

// Options sind die Angaben auf der Kommandozeile.
type Options struct {
	// Check meldet nur, ob es eine neuere Version gibt.
	Check bool
	// Version nennt die Zielversion ausdrücklich; nur so wird zurückgestuft
	// und nur so ein dev build ersetzt.
	Version string
}

// Upgrader trägt, woher Releases kommen und welches Binary ersetzt wird. Die
// Felder sind für Tests überschreibbar; New setzt die echten Werte.
type Upgrader struct {
	// APIBase ist die Basis-URL der GitHub-API, ohne Schrägstrich am Ende.
	APIBase string
	// Repo ist owner/name.
	Repo   string
	Client *http.Client
	// Current beschreibt das laufende Binary.
	Current buildinfo.Info
	// Executable liefert den Pfad des zu ersetzenden Binarys.
	Executable func() (string, error)
	// System sagt, ob die globale config gilt (/etc/kephalaion/config.yaml,
	// über die Suche oder KEPHALAION_CONFIG): Kann sich das Binary dann nicht
	// selbst ersetzen, ist der Weg der des Verwalters.
	System bool
	// Now liefert die Zeit für Report.CheckedAt.
	Now func() time.Time
	// Rename benennt eine Datei atomar um; nil heißt os.Rename. Tests lassen
	// das Ersetzen damit scheitern.
	Rename func(oldpath, newpath string) error
	Out    io.Writer
}

// New liefert einen Upgrader für dieses Binary und github.com/kephalaion/kephalaion.
func New(out io.Writer) *Upgrader {
	return &Upgrader{
		APIBase:    "https://api.github.com",
		Repo:       "kephalaion/kephalaion",
		Client:     &http.Client{Timeout: 10 * time.Minute},
		Current:    buildinfo.Get(),
		Executable: os.Executable,
		Now:        time.Now,
		Out:        out,
	}
}

var errAborted = errors.New("abgebrochen")

// unchanged hängt an jede Fehlermeldung, dass nichts passiert ist.
func unchanged(err error) error {
	return fmt.Errorf("%w\nDas installierte Binary bleibt unverändert.", err)
}

// Result sagt, was Run getan hat.
type Result struct {
	// Replaced: Das Binary ist ersetzt.
	Replaced bool
	// Exe ist das ersetzte Binary; From und To sind die Versionen.
	Exe, From, To string
}

// Run führt `kephalaion upgrade` aus. Scheitert es, bleibt das Binary, wie es
// war, und die Meldung sagt das.
func (u *Upgrader) Run(ctx context.Context, opts Options) (Result, error) {
	res, err := u.run(ctx, opts)
	if err != nil {
		return Result{}, unchanged(err)
	}
	return res, nil
}

// current liefert die installierte Version; isDev ist true für einen dev
// build und alles, was sich nicht als Version lesen lässt.
func (u *Upgrader) current() (v Version, isDev bool) {
	v, err := ParseVersion(u.Current.Version)
	return v, u.Current.IsDev() || err != nil
}

// release holt das neueste Release ohne Suffix (tag leer) oder das zu tag und
// prüft seinen Tag.
func (u *Upgrader) release(ctx context.Context, tag string) (release, Version, error) {
	rel, err := u.fetchRelease(ctx, tag)
	if errors.Is(err, errNotFound) {
		if tag != "" {
			return release{}, Version{}, fmt.Errorf("ein Release %s gibt es nicht", tag)
		}
		return release{}, Version{}, errors.New("es gibt noch kein veröffentlichtes Release")
	}
	if err != nil {
		return release{}, Version{}, err
	}
	target, err := ParseVersion(rel.TagName)
	if err != nil {
		return release{}, Version{}, fmt.Errorf("das Release trägt keinen gültigen Versions-Tag: %w", err)
	}
	// releases/latest liefert nie eine Vorabversion; darauf verlassen wir
	// uns nicht, denn latest ist hier als „ohne Suffix“ festgelegt.
	if tag == "" && (target.IsPrerelease() || rel.Prerelease) {
		return release{}, Version{}, fmt.Errorf("GitHub nennt %s als neuestes Release, das ist aber eine Vorabversion", target)
	}
	return rel, target, nil
}

func (u *Upgrader) run(ctx context.Context, opts Options) (Result, error) {
	current, isDev := u.current()

	var explicit Version
	if opts.Version != "" {
		v, err := ParseVersion(opts.Version)
		if err != nil {
			return Result{}, err
		}
		explicit = v
	}

	if isDev && opts.Version == "" && !opts.Check {
		return Result{}, fmt.Errorf("dieses Binary ist ein dev build (%s) und wird nur mit ausdrücklicher --version ersetzt, z. B.:\n  kephalaion upgrade --version vX.Y.Z", u.Current.Version)
	}

	rel, target, err := u.release(ctx, opts.Version)
	if err != nil {
		return Result{}, err
	}
	if opts.Version != "" && target.Compare(explicit) != 0 {
		return Result{}, fmt.Errorf("verlangt war %s, GitHub liefert %s", explicit, target)
	}

	if opts.Check {
		u.printCheck(current, isDev, target, opts.Version != "")
		return Result{}, nil
	}
	switch {
	case isDev:
	case target.Compare(current) == 0:
		fmt.Fprintf(u.Out, "%s ist bereits installiert.\n", current)
		return Result{}, nil
	case target.Compare(current) < 0 && opts.Version == "":
		fmt.Fprintf(u.Out, "Installiert ist %s, neuer als das neueste Release %s. Nichts zu tun.\n", current, target)
		fmt.Fprintf(u.Out, "Zurückstufen nur ausdrücklich: kephalaion upgrade --version %s\n", target)
		return Result{}, nil
	}

	// Vor dem Download: Lässt sich das Binary nicht ersetzen, sagt die
	// Meldung, wie es sonst geht.
	exe, err := u.executablePath()
	if err != nil {
		return Result{}, err
	}
	if ok, why := probeWrite(filepath.Dir(exe)); !ok {
		msg := fmt.Sprintf("%s — das Binary %s lässt sich von hier nicht ersetzen.", why, exe)
		if method, _, hint := u.way(false, filepath.Dir(exe), target.String(), isDev); method != MethodManual {
			msg += "\nWeg: " + hint
		}
		return Result{}, errors.New(msg)
	}

	bin, want, err := u.assetAndSum(ctx, rel, target)
	if err != nil {
		return Result{}, err
	}
	if err := u.replace(ctx, exe, bin, want); err != nil {
		return Result{}, err
	}

	from := u.Current.Version
	if !isDev && target.Compare(current) < 0 {
		fmt.Fprintf(u.Out, "Zurückgestuft: %s → %s (%s)\n", from, target, exe)
	} else {
		fmt.Fprintf(u.Out, "Aktualisiert: %s → %s (%s)\n", from, target, exe)
	}
	return Result{Replaced: true, Exe: exe, From: from, To: target.String()}, nil
}

// printCheck meldet für upgrade --check, ob es eine andere Version gibt, ob
// sich dieses Binary selbst ersetzen kann und wie das Upgrade geht.
func (u *Upgrader) printCheck(current Version, isDev bool, target Version, explicit bool) {
	switch {
	case isDev && !explicit:
		fmt.Fprintf(u.Out, "Installiert: %s (dev build). Neuestes Release: %s.\n", u.Current.Version, target)
		fmt.Fprintf(u.Out, "Ersetzen nur ausdrücklich: kephalaion upgrade --version %s\n", target)
	case !isDev && target.Compare(current) == 0:
		fmt.Fprintf(u.Out, "%s ist bereits installiert.\n", current)
	case !isDev && target.Compare(current) < 0 && !explicit:
		fmt.Fprintf(u.Out, "Installiert ist %s, neuer als das neueste Release %s. Nichts zu tun.\n", current, target)
		fmt.Fprintf(u.Out, "Zurückstufen nur ausdrücklich: kephalaion upgrade --version %s\n", target)
	default:
		verb := "Neue Version verfügbar"
		if !isDev && target.Compare(current) < 0 {
			verb = "Zurückstufen möglich"
		}
		fmt.Fprintf(u.Out, "%s: %s → %s\n", verb, u.Current.Version, target)
	}
	self, dir, why := u.access()
	if self {
		fmt.Fprintf(u.Out, "Selbst ersetzen: ja (Schreibrecht in %s)\n", dir)
	} else {
		fmt.Fprintf(u.Out, "Selbst ersetzen: nein (%s)\n", why)
	}
	_, _, hint := u.way(self, dir, target.String(), isDev)
	fmt.Fprintf(u.Out, "Weg: %s\n", hint)
}

// assetAndSum sucht im Release das Binary dieser Plattform und lädt seine
// Summe aus SHA256SUMS.
func (u *Upgrader) assetAndSum(ctx context.Context, rel release, target Version) (asset, string, error) {
	name := AssetName(u.Current.OS, u.Current.Arch)
	bin, ok := rel.findAsset(name)
	if !ok {
		return asset{}, "", fmt.Errorf("das Release %s hat kein Binary für %s (%s fehlt)", target, u.Current.Platform(), name)
	}
	sumsAsset, ok := rel.findAsset(SumsAsset)
	if !ok {
		return asset{}, "", fmt.Errorf("das Release %s hat kein %s", target, SumsAsset)
	}
	want, err := u.expectedSum(ctx, sumsAsset, name)
	if err != nil {
		return asset{}, "", err
	}
	return bin, want, nil
}

// expectedSum lädt SHA256SUMS und liefert die Summe für name.
func (u *Upgrader) expectedSum(ctx context.Context, sums asset, name string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sums.URL, nil)
	if err != nil {
		return "", err
	}
	resp, err := u.do(req)
	if err != nil {
		return "", fmt.Errorf("%s nicht ladbar: %w", SumsAsset, err)
	}
	defer resp.Body.Close()
	sum, err := findSum(io.LimitReader(resp.Body, 64<<10), name)
	if err != nil {
		return "", err
	}
	return sum, nil
}

// findSum liest eine Datei im Format von sha256sum und liefert die Summe für
// name. Ein * vor dem Namen (Binärmodus) wird angenommen.
func findSum(r io.Reader, name string) (string, error) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) != 2 {
			continue
		}
		if strings.TrimPrefix(fields[1], "*") != name {
			continue
		}
		sum := strings.ToLower(fields[0])
		if b, err := hex.DecodeString(sum); err != nil || len(b) != sha256.Size {
			return "", fmt.Errorf("%s enthält für %s keine gültige SHA-256-Summe", SumsAsset, name)
		}
		return sum, nil
	}
	if err := sc.Err(); err != nil {
		return "", fmt.Errorf("%s nicht lesbar: %w", SumsAsset, err)
	}
	return "", fmt.Errorf("%s enthält keine Summe für %s", SumsAsset, name)
}

// executablePath liefert den Pfad des laufenden Binarys mit aufgelösten
// Links: ersetzt wird die Datei, nicht der Link, der auf sie zeigt.
func (u *Upgrader) executablePath() (string, error) {
	exe, err := u.Executable()
	if err != nil {
		return "", fmt.Errorf("Pfad des laufenden Binarys nicht ermittelbar: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return "", fmt.Errorf("Pfad des laufenden Binarys nicht auflösbar (%s): %w", exe, err)
	}
	return resolved, nil
}

// replace lädt das Binary in eine temporäre Datei neben exe, prüft die Summe
// und benennt sie über exe um. rename ist atomar, weil beide Dateien im selben
// Verzeichnis und damit auf demselben Dateisystem liegen. Scheitert irgendein
// Schritt oder wird ctx abgebrochen (SIGINT/SIGTERM), wird die temporäre Datei
// entfernt und exe bleibt, wie es war.
func (u *Upgrader) replace(ctx context.Context, exe string, bin asset, wantSum string) error {
	tmpName, err := u.download(ctx, filepath.Dir(exe), bin, wantSum)
	if err != nil {
		return err
	}
	// Letzte Gelegenheit für einen Abbruch; nach rename ist das neue Binary da.
	if ctx.Err() != nil {
		_ = os.Remove(tmpName)
		return errAborted
	}
	if err := u.rename(tmpName, exe); err != nil {
		_ = os.Remove(tmpName)
		return replaceError(exe, err)
	}
	return nil
}

// replaceError ist die Meldung, wenn sich exe nicht ersetzen lässt.
func replaceError(exe string, err error) error {
	if errors.Is(err, fs.ErrPermission) {
		return fmt.Errorf("kein Schreibrecht, um %s zu ersetzen", exe)
	}
	return fmt.Errorf("%s nicht ersetzbar: %w", exe, err)
}

func (u *Upgrader) rename(oldpath, newpath string) error {
	if u.Rename != nil {
		return u.Rename(oldpath, newpath)
	}
	return os.Rename(oldpath, newpath)
}

// download lädt das Binary in eine temporäre Datei in dir (TempPattern),
// prüft die Summe und macht die Datei ausführbar. Scheitert ein Schritt oder
// wird ctx abgebrochen, ist die temporäre Datei entfernt.
func (u *Upgrader) download(ctx context.Context, dir string, bin asset, wantSum string) (name string, err error) {
	tmp, err := os.CreateTemp(dir, TempPattern)
	if err != nil {
		if errors.Is(err, fs.ErrPermission) {
			return "", fmt.Errorf("kein Schreibrecht im Verzeichnis %s — das Binary dort lässt sich nicht ersetzen", dir)
		}
		return "", fmt.Errorf("temporäre Datei in %s nicht anlegbar: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
		}
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, bin.URL, nil)
	if err != nil {
		return "", err
	}
	resp, err := u.do(req)
	if err != nil {
		return "", fmt.Errorf("%s nicht ladbar: %w", bin.Name, err)
	}
	defer resp.Body.Close()

	h := sha256.New()
	if _, err = io.Copy(io.MultiWriter(tmp, h), resp.Body); err != nil {
		if ctx.Err() != nil {
			return "", errAborted
		}
		return "", fmt.Errorf("Download von %s abgebrochen: %w", bin.Name, err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != wantSum {
		return "", fmt.Errorf("Prüfsumme von %s stimmt nicht: erwartet %s, erhalten %s", bin.Name, wantSum, got)
	}
	if err = tmp.Chmod(0o755); err != nil {
		return "", fmt.Errorf("Rechte der temporären Datei nicht setzbar: %w", err)
	}
	if err = tmp.Sync(); err != nil {
		return "", fmt.Errorf("temporäre Datei nicht schreibbar: %w", err)
	}
	if err = tmp.Close(); err != nil {
		return "", fmt.Errorf("temporäre Datei nicht schreibbar: %w", err)
	}
	return tmpName, nil
}
