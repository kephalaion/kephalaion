package dirsync

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/kephalaion/kephalaion/internal/contract"
	"github.com/kephalaion/kephalaion/internal/ident"
)

// Der lokale Ordner als Quelle von push: Vorab, bevor irgendetwas
// geschrieben wird, liest scan ihn ganz ein und prüft jede Datei — sonst
// bricht push mit allen Treffern ab, ohne zu schreiben. Nur reguläre Dateien
// zählen; Symlinks und andere werden übergangen und gemeldet, .git und
// Treffer von Exclude ausgelassen.

// localDir ist ein eingelesener Ordner: Dateien und Unterordner, je nach
// Name sortiert.
type localDir struct {
	files []localFile
	dirs  []localSub
}

// localFile ist eine reguläre Datei: ihr Name in der Ebene und ihr Pfad.
type localFile struct {
	name string
	path string
}

type localSub struct {
	name string
	dir  *localDir
}

// Hit ist ein Treffer der Vorabprüfung: eine Datei, die nicht in den Store
// kann, mit dem Grund.
type Hit struct {
	// Path ist der Pfad relativ zum lokalen Ordner, mit '/'.
	Path string
	Err  error
}

// PrecheckError ist das Ergebnis einer Vorabprüfung mit Treffern: alle, in
// der Reihenfolge des Ordners. Nichts wurde geschrieben.
type PrecheckError struct {
	Hits []Hit
}

func (e *PrecheckError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Vorabprüfung: %d Treffer, nichts geschrieben — mit --exclude ausnehmen oder beheben:", len(e.Hits))
	for _, h := range e.Hits {
		fmt.Fprintf(&b, "\n  %s: %v", h.Path, h.Err)
	}
	return b.String()
}

// Fehlerarten der Vorabprüfung, für errors.Is.
var (
	// ErrNotText: kein UTF-8 oder ein NUL-Byte.
	ErrNotText = errors.New("kein UTF-8-Text (oder NUL-Byte)")
	// ErrTooLarge: größer als contract.MaxDocumentBytes.
	ErrTooLarge = errors.New("größer als 1 MiB")
)

// scanner liest den lokalen Ordner ein.
type scanner struct {
	root string
	// target ist das Zielverzeichnis, unter dem die Namen geprüft werden.
	target string
	opts   Options
	hits   []Hit
	// skipped sind die übergangenen Einträge (relativer Pfad und Grund).
	skipped []Hit
	last    *localFile
}

// scan liest root ein: den Baum, die Treffer der Vorabprüfung, die
// übergangenen Einträge und die Datei aus Options.Last, die nicht im Baum
// steht.
func scan(root, target string, opts Options) (*scanner, *localDir, error) {
	s := &scanner{root: root, target: target, opts: opts}
	info, err := os.Lstat(root)
	if err != nil {
		return nil, nil, fmt.Errorf("lokaler Ordner: %w", err)
	}
	if !info.IsDir() {
		return nil, nil, fmt.Errorf("lokaler Ordner %s ist kein Ordner", root)
	}
	tree, err := s.dir(root, "")
	if err != nil {
		return nil, nil, err
	}
	if opts.Last != "" && s.last == nil {
		return nil, nil, fmt.Errorf("--last %s: keine reguläre Datei im lokalen Ordner (oder ausgelassen)", opts.Last)
	}
	return s, tree, nil
}

// dir liest einen Ordner; rel ist sein Pfad relativ zur Wurzel ("" für
// sie).
func (s *scanner) dir(dir, rel string) (*localDir, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("lokaler Ordner: %w", err)
	}
	out := &localDir{}
	for _, e := range entries {
		name := e.Name()
		if s.opts.excluded(name) {
			continue
		}
		relPath := join(rel, name)
		full := filepath.Join(dir, name)
		switch {
		case e.Type()&fs.ModeSymlink != 0:
			s.skipped = append(s.skipped, Hit{Path: relPath, Err: errors.New("Symlink übergangen")})
		case e.IsDir():
			sub, err := s.dir(full, relPath)
			if err != nil {
				return nil, err
			}
			out.dirs = append(out.dirs, localSub{name: name, dir: sub})
		case e.Type().IsRegular():
			f := localFile{name: name, path: full}
			s.check(relPath, full)
			if relPath == s.opts.Last {
				s.last = &f
				continue
			}
			out.files = append(out.files, f)
		default:
			s.skipped = append(s.skipped, Hit{Path: relPath, Err: fmt.Errorf("keine reguläre Datei (%s), übergangen", e.Type())})
		}
	}
	sort.Slice(out.files, func(i, j int) bool { return out.files[i].name < out.files[j].name })
	sort.Slice(out.dirs, func(i, j int) bool { return out.dirs[i].name < out.dirs[j].name })
	return out, nil
}

// check prüft eine Datei für den Store: ihren Namen unter dem Ziel
// (ident.CheckDocName) und ihren Inhalt (readFile).
func (s *scanner) check(relPath, full string) {
	if err := ident.CheckDocName(join(s.target, relPath)); err != nil {
		s.hits = append(s.hits, Hit{Path: relPath, Err: err})
	}
	if _, err := readFile(full); err != nil {
		s.hits = append(s.hits, Hit{Path: relPath, Err: err})
	}
}

// readFile liest eine Datei und prüft, dass sie in den Store kann: höchstens
// contract.MaxDocumentBytes, UTF-8 ohne NUL. Geprüft wird vor dem Lesen an
// der Größe und noch einmal beim Schreiben — die Datei kann sich seit der
// Vorabprüfung geändert haben.
func readFile(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("keine reguläre Datei mehr (%s)", info.Mode().Type())
	}
	if info.Size() > contract.MaxDocumentBytes {
		return "", fmt.Errorf("%w: %d Bytes", ErrTooLarge, info.Size())
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if len(b) > contract.MaxDocumentBytes {
		return "", fmt.Errorf("%w: %d Bytes", ErrTooLarge, len(b))
	}
	if !utf8.Valid(b) || strings.IndexByte(string(b), 0) >= 0 {
		return "", ErrNotText
	}
	return string(b), nil
}
