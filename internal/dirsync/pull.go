package dirsync

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/kephalaion/kephalaion/internal/ident"
)

// maxPullAttempts begrenzt, wie oft pull von vorn beginnt, wenn sich der
// Store während des Lesens ändert.
const maxPullAttempts = 3

// errChanged lässt pull von vorn beginnen: Die Revision eines Dokuments bei
// read weicht von der aus list ab.
var errChanged = errors.New("der Store hat sich während des Lesens geändert")

// Pull holt das Verzeichnis dir des Ziels in den lokalen Ordner local:
// Dateien schreiben, deren Inhalt abweicht oder die fehlen, Ordner anlegen;
// lokal gelöscht wird nur mit Options.Delete. .git und Treffer von
// Options.Exclude bleiben lokal unberührt, auch mit Delete. Geschrieben wird
// nie außerhalb von local, und keinem Symlink darin wird gefolgt: Ein Symlink
// an einer Stelle, die geschrieben werden müsste, ist ein gemeldeter Fehler
// dieser Datei. Ändert sich der Store während des Lesens, beginnt der Lauf
// von vorn, höchstens maxPullAttempts-mal.
func Pull(ctx context.Context, tgt Target, dir, local string, opts Options) (*Report, error) {
	start := time.Now()
	rep := &Report{}
	if err := opts.checkExclude(); err != nil {
		return rep, err
	}
	prefix, err := ident.DocDirPrefix(dir)
	if err != nil {
		return rep, err
	}
	dir = prefix[:max(len(prefix)-1, 0)]
	if opts.Last != "" {
		return rep, errors.New("--last gibt es nur bei push")
	}
	local = filepath.Clean(local)
	if info, err := os.Lstat(local); err == nil && !info.IsDir() {
		return rep, fmt.Errorf("lokaler Ordner %s ist kein Ordner", local)
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return rep, fmt.Errorf("lokaler Ordner: %w", err)
	}
	for attempt := 1; ; attempt++ {
		*rep = Report{Restarts: attempt - 1}
		r := &run{ctx: ctx, tgt: tgt, opts: opts, rep: rep}
		err = r.pullDir(dir, local)
		if !errors.Is(err, errChanged) {
			break
		}
		if attempt == maxPullAttempts {
			err = fmt.Errorf("%w — %d-mal von vorn begonnen; erneut ausführen", errChanged, attempt)
			break
		}
		r.note("! %s: %v; von vorn", dirName(dir), errChanged)
	}
	rep.Duration = time.Since(start)
	if errors.Is(err, errStopped) {
		return rep, nil
	}
	return rep, err
}

// localEntry ist ein Eintrag des lokalen Ordners, wie pull ihn sieht.
type localEntry struct {
	name string
	mode fs.FileMode
}

// pullDir gleicht eine Ebene ab: target ist das Verzeichnis im Ziel, local
// der Ordner dazu.
func (r *run) pullDir(target, local string) error {
	entries, err := r.list(target)
	if err != nil {
		return err
	}
	have := map[string]localEntry{}
	dirEntries, err := os.ReadDir(local)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("lokaler Ordner %s: %w", local, err)
	}
	for _, e := range dirEntries {
		have[e.Name()] = localEntry{name: e.Name(), mode: e.Type()}
	}
	want := map[string]Entry{}
	for _, e := range entries {
		name := base(e.Name)
		if r.opts.excluded(name) {
			continue
		}
		want[name] = e
	}
	// 1. Mit Delete lokal löschen, was im Store fehlt oder die andere Art
	// hat; .git und Ausgelassenes bleiben.
	if r.opts.Delete {
		names := make([]string, 0, len(have))
		for n := range have {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			if r.opts.excluded(n) {
				continue
			}
			le := have[n]
			// Ein Symlink, dessen Name im Store steht, bleibt: Er wird nicht
			// ersetzt, sondern beim Schreiben gemeldet.
			if w, ok := want[n]; ok && (w.Dir == le.mode.IsDir() || le.mode&fs.ModeSymlink != 0) {
				continue
			}
			if err := r.removeLocal(join(target, n), filepath.Join(local, n), le.mode); err != nil {
				return err
			}
			delete(have, n)
		}
	}
	// 2. Dokumente: schreiben, was abweicht oder fehlt.
	for _, e := range entries {
		name := base(e.Name)
		if e.Dir || r.opts.excluded(name) {
			continue
		}
		if err := r.pullFile(e, filepath.Join(local, name), have[name], local); err != nil {
			return err
		}
	}
	// 3. Ordner anlegen und absteigen.
	for _, e := range entries {
		name := base(e.Name)
		if !e.Dir || r.opts.excluded(name) {
			continue
		}
		sub := filepath.Join(local, name)
		if le, ok := have[name]; ok {
			switch {
			case le.mode&fs.ModeSymlink != 0:
				r.problem("ordner", e.Name, errors.New("ist lokal ein Symlink; nicht gefolgt"))
				continue
			case !le.mode.IsDir():
				r.problem("ordner", e.Name, errors.New("ist lokal eine Datei; mit --delete ersetzen"))
				continue
			}
		}
		if err := r.pullDir(e.Name, sub); err != nil {
			return err
		}
	}
	return nil
}

// pullFile schreibt ein Dokument des Ziels in die lokale Datei path, wenn
// der Inhalt abweicht oder die Datei fehlt. le ist der lokale Eintrag, wenn
// es einen gibt.
func (r *run) pullFile(e Entry, path string, le localEntry, local string) error {
	switch {
	case le.mode&fs.ModeSymlink != 0:
		r.problem("schreiben", e.Name, errors.New("ist lokal ein Symlink; nicht gefolgt"))
		return nil
	case le.name != "" && le.mode.IsDir():
		r.problem("schreiben", e.Name, errors.New("ist lokal ein Ordner; mit --delete ersetzen"))
		return nil
	case le.name != "" && !le.mode.IsRegular():
		r.problem("schreiben", e.Name, fmt.Errorf("ist lokal keine reguläre Datei (%s)", le.mode.Type()))
		return nil
	}
	doc, found, err := r.read(e.Name)
	if err != nil {
		return err
	}
	if !found || doc.Revision != e.Revision {
		return errChanged
	}
	exists := le.name != ""
	if exists {
		b, err := os.ReadFile(path)
		if err != nil {
			r.problem("lesen", e.Name, err)
			return nil
		}
		if string(b) == doc.Content {
			r.rep.Unchanged++
			return nil
		}
	}
	if err := r.stop(); err != nil {
		return err
	}
	if r.opts.DryRun {
		r.count(exists, e.Name)
		return nil
	}
	if err := os.MkdirAll(local, 0o755); err != nil {
		return fmt.Errorf("lokaler Ordner %s: %w", local, err)
	}
	if err := writeLocal(path, doc.Content, exists); err != nil {
		r.problem("schreiben", e.Name, err)
		return nil
	}
	r.count(exists, e.Name)
	return nil
}

// count zählt eine geschriebene Datei als angelegt oder geändert.
func (r *run) count(existed bool, name string) {
	if existed {
		r.rep.Changed++
		r.note("~ %s", name)
		return
	}
	r.rep.Created++
	r.note("+ %s", name)
}

// writeLocal schreibt eine Datei: eine vorhandene in place (ihre Rechte
// bleiben), eine neue mit 0644 (umask gilt). Keinem Symlink wird gefolgt;
// das hat der Aufrufer schon geprüft, hier gilt es noch einmal.
func writeLocal(path, content string, exists bool) error {
	flags := os.O_WRONLY | os.O_TRUNC
	if !exists {
		flags |= os.O_CREATE | os.O_EXCL
	}
	if info, err := os.Lstat(path); err == nil && info.Mode()&fs.ModeSymlink != 0 {
		return errors.New("ist lokal ein Symlink; nicht gefolgt")
	}
	f, err := os.OpenFile(path, flags, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(content); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// removeLocal löscht einen lokalen Eintrag, der im Store fehlt: eine Datei
// oder einen Symlink (den Link, nie sein Ziel), einen Ordner mit allem
// darunter — außer .git und Ausgelassenem, die bleiben, samt ihrem Ordner.
func (r *run) removeLocal(name, path string, mode fs.FileMode) error {
	if err := r.stop(); err != nil {
		return err
	}
	shown := name
	if mode.IsDir() {
		shown = dirName(name)
	}
	if r.opts.DryRun {
		r.rep.Deleted++
		r.note("- %s", shown)
		return nil
	}
	var err error
	removed := 1
	if mode.IsDir() {
		_, removed, err = r.removeTree(path)
	} else {
		err = os.Remove(path)
	}
	if err != nil {
		r.problem("löschen", name, err)
		return nil
	}
	// Ein Ordner, in dem nur Ausgelassenes lag, bleibt — und zählt nicht.
	if removed == 0 {
		return nil
	}
	r.rep.Deleted++
	r.note("- %s", shown)
	return nil
}

// removeTree löscht einen Ordner mit allem darunter, außer .git und
// Ausgelassenem; kept sagt, dass etwas geblieben ist (dann auch der
// Ordner), removed zählt die gelöschten Dateien.
func (r *run) removeTree(dir string) (kept bool, removed int, err error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, 0, err
	}
	for _, e := range entries {
		if r.opts.excluded(e.Name()) {
			kept = true
			continue
		}
		p := filepath.Join(dir, e.Name())
		if e.IsDir() {
			k, n, err := r.removeTree(p)
			removed += n
			if err != nil {
				return kept, removed, err
			}
			kept = kept || k
			continue
		}
		if err := os.Remove(p); err != nil {
			return kept, removed, err
		}
		removed++
	}
	if kept {
		return true, removed, nil
	}
	return false, removed, os.Remove(dir)
}
