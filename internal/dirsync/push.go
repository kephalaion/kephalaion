package dirsync

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/kephalaion/kephalaion/internal/contract"
	"github.com/kephalaion/kephalaion/internal/ident"
)

// Push ersetzt den Inhalt des Verzeichnisses dir im Ziel durch den des
// lokalen Ordners local. Vorab liest es den Ordner ganz ein und prüft jede
// Datei; Treffer sind ein *PrecheckError, ohne dass etwas geschrieben wurde.
// Dann je Ebene, rekursiv: im Ziel löschen, was lokal fehlt oder die andere
// Art hat; Dateien anlegen oder mit base_revision schreiben, deren Inhalt
// abweicht; in die Ordner absteigen. Leere Ordner entstehen im Ziel nicht.
// Zuletzt, nach allem, die Datei aus Options.Last — nur nach einem Lauf ohne
// Meldung und ohne Abbruch.
//
// Der Bericht kommt auch mit einem Fehler, soweit der Lauf kam. Ein Fehler
// ist ein Abbruch (Ziel nicht erreichbar, verboten, nicht lesbar); ein
// beendeter oder gemeldeter Lauf ist kein Fehler, sondern
// Report.Complete() false.
func Push(ctx context.Context, tgt Target, dir, local string, opts Options) (*Report, error) {
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
	s, tree, err := scan(local, dir, opts)
	if err != nil {
		return rep, err
	}
	if len(s.hits) > 0 {
		return rep, &PrecheckError{Hits: s.hits}
	}
	r := &run{ctx: ctx, tgt: tgt, opts: opts, rep: rep}
	if s.last != nil {
		r.lastName = join(dir, opts.Last)
	}
	for _, sk := range s.skipped {
		rep.Skipped++
		r.note("! %s: %v", sk.Path, sk.Err)
	}
	err = r.pushDir(dir, tree)
	if err == nil && s.last != nil && rep.Complete() {
		err = r.pushLast(*s.last)
	}
	rep.Duration = time.Since(start)
	if errors.Is(err, errStopped) {
		return rep, nil
	}
	return rep, err
}

// pushDir gleicht eine Ebene ab: target ist das Verzeichnis im Ziel, d der
// lokale Ordner.
func (r *run) pushDir(target string, d *localDir) error {
	entries, err := r.list(target)
	if err != nil {
		return err
	}
	files := map[string]bool{}
	for _, f := range d.files {
		files[f.name] = true
	}
	subs := map[string]bool{}
	for _, s := range d.dirs {
		subs[s.name] = true
	}
	// Die Datei aus Options.Last gehört zur Quelle: Sie wird nicht gelöscht,
	// nur zuletzt geschrieben.
	if r.lastName != "" && parent(r.lastName) == target {
		files[base(r.lastName)] = true
	}
	docs := map[string]Entry{}
	// 1. Im Ziel löschen, was in der Quelle fehlt oder die andere Art hat;
	// Ausgelassenes bleibt.
	for _, e := range entries {
		name := base(e.Name)
		if r.opts.excluded(name) {
			continue
		}
		if e.Dir {
			if !subs[name] {
				if err := r.deleteDir(e.Name); err != nil {
					return err
				}
			}
			continue
		}
		if files[name] {
			docs[name] = e
			continue
		}
		if err := r.deleteDoc(e); err != nil {
			return err
		}
	}
	// 2. Dateien anlegen oder schreiben.
	for _, f := range d.files {
		name := join(target, f.name)
		_, exists := docs[f.name]
		if err := r.pushFile(name, f, exists); err != nil {
			return err
		}
	}
	// 3. In die Ordner absteigen.
	for _, s := range d.dirs {
		if err := r.pushDir(join(target, s.name), s.dir); err != nil {
			return err
		}
	}
	return nil
}

// pushFile bringt eine Datei ins Ziel: anlegen, wenn es das Dokument nicht
// gibt (exists); schreiben, wenn der Inhalt abweicht — mit der Revision aus
// read. Die Datei wird jetzt gelesen und noch einmal geprüft: Sie kann sich
// seit der Vorabprüfung geändert haben.
func (r *run) pushFile(name string, f localFile, exists bool) error {
	content, err := readFile(f.path)
	if err != nil {
		r.problem("lesen", name, err)
		return nil
	}
	if !exists {
		return r.create(name, content)
	}
	doc, found, err := r.read(name)
	if err != nil {
		return err
	}
	if !found {
		r.problem("read", name, &OpError{Code: "not_found", Message: "das Dokument ist inzwischen weg"})
		return nil
	}
	if doc.Content == content {
		r.rep.Unchanged++
		return nil
	}
	return r.write(name, content, doc.Revision)
}

// pushLast schreibt die Datei aus Options.Last als letzten Vorgang: Gibt es
// das Dokument, wird es bei abweichendem Inhalt geschrieben, sonst angelegt.
func (r *run) pushLast(f localFile) error {
	name := r.lastName
	entries, err := r.list(parent(name))
	if err != nil {
		return err
	}
	exists := false
	for _, e := range entries {
		if e.Name == name && !e.Dir {
			exists = true
		}
	}
	if err := r.pushFile(name, f, exists); err != nil {
		return err
	}
	r.rep.LastWritten = r.rep.Complete()
	return nil
}

func (r *run) create(name, content string) error {
	if r.opts.DryRun {
		r.rep.Created++
		r.note("+ %s", name)
		return nil
	}
	ok, err := r.op("create", name, func(ctx context.Context) error { return r.tgt.Create(ctx, name, content) })
	if ok {
		r.rep.Created++
		r.note("+ %s", name)
	}
	return err
}

func (r *run) write(name, content string, base int64) error {
	if r.opts.DryRun {
		r.rep.Changed++
		r.note("~ %s", name)
		return nil
	}
	ok, err := r.op("write", name, func(ctx context.Context) error { return r.tgt.Write(ctx, name, content, base) })
	if ok {
		r.rep.Changed++
		r.note("~ %s", name)
	}
	return err
}

func (r *run) deleteDoc(e Entry) error {
	if r.opts.DryRun {
		r.rep.Deleted++
		r.note("- %s", e.Name)
		return nil
	}
	ok, err := r.op("delete", e.Name, func(ctx context.Context) error { return r.tgt.Delete(ctx, e.Name, e.Revision) })
	if ok {
		r.rep.Deleted++
		r.note("- %s", e.Name)
	}
	return err
}

func (r *run) deleteDir(dir string) error {
	if r.opts.DryRun {
		r.rep.Deleted++
		r.note("- %s", dirName(dir))
		return nil
	}
	ok, err := r.op("delete", dirName(dir), func(ctx context.Context) error { return r.tgt.DeleteDir(ctx, dir) })
	if ok {
		r.rep.Deleted++
		r.note("- %s", dirName(dir))
	}
	return err
}

// CheckPushDir prüft das Ziel von push, soweit es ohne Verbindung geht — ein
// Schutz vor Versehen in der Kommandozeile, keine Grenze am Hub: vendor/<name>
// und darunter ist erlaubt (vendor wahr), die Wurzel einer Collection und
// vendor selbst nie. Jedes andere Ziel braucht einen Verzeichnis-Scope des
// Accounts in der Collection (vendor falsch, kein Fehler); ob er ihn hat,
// zeigen erst seine Rechte am Node (CoveredByDirScope).
func CheckPushDir(dir string) (vendor bool, err error) {
	prefix, err := ident.DocDirPrefix(dir)
	if err != nil {
		return false, err
	}
	// Bewertet an einem Namen darunter: vendor/<name> selbst ist erlaubt.
	name, reserved := contract.VendorOf(prefix + "x")
	switch {
	case prefix == "":
		return false, fmt.Errorf("push schreibt nie an die Wurzel einer Collection; Ziel ist %s/<name>/ oder ein Verzeichnis, "+
			"für das der Account einen Verzeichnis-Scope hat", contract.VendorDir)
	case reserved:
		return false, fmt.Errorf("push schreibt nicht nach %s/ selbst, nur nach %s/<name>/", contract.VendorDir, contract.VendorDir)
	}
	return name != "", nil
}

// CoveredByDirScope sagt, ob push nach dir schreiben darf, weil dir gleich
// einem der Verzeichnis-Scopes dirs ist oder darunter liegt — bewertet nach
// der Regel des Hubs (contract.Rights.DirScopeOf) an einem Namen unter dir.
// Die Wurzel deckt nie ein Scope.
func CoveredByDirScope(dir string, dirs []string) bool {
	prefix, err := ident.DocDirPrefix(dir)
	if err != nil || prefix == "" {
		return false
	}
	_, ok := contract.Rights{Dirs: dirs}.DirScopeOf(prefix + "x")
	return ok
}
