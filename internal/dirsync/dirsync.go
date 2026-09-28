// Package dirsync gleicht einen lokalen Ordner mit einem Verzeichnis einer
// Collection ab: Push ersetzt den Inhalt des Verzeichnisses durch den des
// Ordners, Pull holt ihn in den Ordner (docs/konzept.md, „Einen Ordner
// abgleichen: push und pull“). Das Paket ist neutral — es kennt weder Hub
// noch Node noch MCP — und arbeitet gegen die kleine Schnittstelle Target:
// ein Verzeichnis lesen, ein Dokument lesen, anlegen, schreiben, löschen.
// Die Umsetzung über die Werkzeuge des Nodes und die Kommandos stehen in
// cmd/kephalaion.
//
// Verglichen wird der Inhalt, Byte für Byte, nie das Datum und kein Hash.
// Der Abgleich besteht aus Einzelvorgängen, je Ebene in fester Reihenfolge
// (nach Name): zuerst im Ziel löschen, was in der Quelle fehlt oder dort die
// andere Art hat, dann Dateien anlegen oder schreiben, deren Inhalt abweicht,
// dann in die Ordner absteigen. Ein zweiter Lauf ändert nur, was noch
// abweicht, und nach einem vollständigen Lauf nichts.
//
// Abbrechen und Höchstzeit wirken zwischen zwei Vorgängen: Ist ctx beendet,
// geht der laufende Vorgang zu Ende, dann ist Schluss (Report.Stopped). Ein
// Vorgang, der am Ziel scheitert, weil jemand dazwischen schrieb
// (stale_revision, name_taken, path_conflict, not_found) oder dessen Ausgang
// unklar ist, wird gemeldet (Report.Problems), nicht wiederholt; der Lauf
// geht weiter und ist am Ende unvollständig. Jeder andere Fehler des Ziels
// — forbidden, not_readable, unreachable, Transport — bricht ab.
package dirsync

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"time"
)

// Entry ist ein Eintrag eines Verzeichnisses im Ziel: ein Dokument mit
// seiner Revision oder ein Verzeichnis.
type Entry struct {
	// Name ist der volle Name in der Collection.
	Name string
	Dir  bool
	// Revision ist die Revision eines Dokuments; bei einem Verzeichnis 0.
	Revision int64
}

// Document ist ein gelesenes Dokument des Ziels.
type Document struct {
	Content  string
	Revision int64
}

// Target ist das Verzeichnis einer Collection, wie push und pull es brauchen.
// Jeder Fehler eines Vorgangs, den das Ziel mit einem Code ablehnt, ist ein
// *OpError; alles andere gilt als nicht erreicht und bricht ab.
type Target interface {
	// List liefert die Einträge direkt in dir ("" ist die Wurzel der
	// Collection), Dokumente und Verzeichnisse, vollständig (alle Seiten);
	// die Reihenfolge ist gleich. Ein Verzeichnis, das es nicht gibt, ist
	// leer.
	List(ctx context.Context, dir string) ([]Entry, error)
	// Read liest ein Dokument; found ist false, wenn es keines gibt.
	Read(ctx context.Context, name string) (doc Document, found bool, err error)
	// Create legt ein Dokument an.
	Create(ctx context.Context, name, content string) error
	// Write ersetzt den Inhalt eines Dokuments, nur wenn es noch die
	// Revision base hat.
	Write(ctx context.Context, name, content string, base int64) error
	// Delete löscht ein Dokument, nur wenn es noch die Revision base hat.
	Delete(ctx context.Context, name string, base int64) error
	// DeleteDir löscht ein Verzeichnis mit allen Dokumenten darunter.
	DeleteDir(ctx context.Context, dir string) error
}

// OpError ist ein Fehler, den das Ziel mit einem Code abgelehnt hat — die
// Codes des Vertrags und der Werkzeuge des Nodes (docs/begriffe.md, „write
// error codes“).
type OpError struct {
	Code    string
	Message string
}

func (e *OpError) Error() string {
	if e.Code == "" {
		return e.Message
	}
	return e.Code + ": " + e.Message
}

// Codes, die gemeldet werden und den Lauf nicht abbrechen: jemand schrieb
// dazwischen, oder der Ausgang ist unklar — der nächste Lauf gleicht an.
var reportedCodes = map[string]bool{
	"stale_revision":  true,
	"name_taken":      true,
	"path_conflict":   true,
	"not_found":       true,
	"outcome_unknown": true,
}

// reportable sagt, ob ein Fehler des Ziels gemeldet wird, statt den Lauf
// abzubrechen.
func reportable(err error) bool {
	var oe *OpError
	return errors.As(err, &oe) && reportedCodes[oe.Code]
}

// GitDir wird in Quelle und Ziel immer ausgelassen: weder angelegt noch
// geändert noch gelöscht.
const GitDir = ".git"

// Options steuern einen Lauf.
type Options struct {
	// Exclude sind Globs (path.Match) auf den Namen jeder Ebene, Datei wie
	// Ordner; ein Treffer bleibt in Quelle und Ziel unberührt.
	Exclude []string
	// Last ist bei push eine Datei, relativ zum lokalen Ordner mit '/', die
	// als letzter Vorgang geschrieben wird — nach dem Abstieg in alle Ordner
	// und nur, wenn bis dahin nichts gemeldet wurde und nichts den Lauf
	// beendet hat.
	Last string
	// DryRun liest nur und meldet, was angelegt, geändert und gelöscht
	// würde.
	DryRun bool
	// Delete lässt pull lokal löschen, was im Store fehlt; ohne es bleibt
	// Überzähliges liegen.
	Delete bool
	// Out bekommt eine Zeile je Vorgang: „+ name“ angelegt, „~ name“
	// geändert, „- name“ gelöscht, „! name: grund“ gemeldet oder übergangen.
	// nil schweigt.
	Out io.Writer
}

// checkExclude prüft die Globs.
func (o Options) checkExclude() error {
	for _, g := range o.Exclude {
		if _, err := path.Match(g, ""); err != nil || g == "" || strings.Contains(g, "/") {
			return fmt.Errorf("--exclude %q: kein gültiger Glob für einen Namen (ohne '/')", g)
		}
	}
	return nil
}

// excluded sagt, ob ein Name einer Ebene ausgelassen wird: .git immer, sonst
// ein Treffer von Exclude.
func (o Options) excluded(base string) bool {
	if base == GitDir {
		return true
	}
	for _, g := range o.Exclude {
		if ok, _ := path.Match(g, base); ok {
			return true
		}
	}
	return false
}

// Problem ist ein gemeldeter Fehler eines Vorgangs: Op nennt den Vorgang,
// Name das Dokument oder Verzeichnis.
type Problem struct {
	Op   string
	Name string
	Err  error
}

func (p Problem) String() string { return fmt.Sprintf("%s %s: %v", p.Op, p.Name, p.Err) }

// Report ist das Ergebnis eines Laufs.
type Report struct {
	// Created, Changed, Deleted, Unchanged zählen Dokumente bzw. Dateien;
	// ein gelöschtes Verzeichnis zählt einmal. Skipped zählt Übergangenes
	// der Quelle: Symlinks und andere Nicht-Dateien.
	Created, Changed, Deleted, Unchanged, Skipped int
	// Problems sind die gemeldeten Fehler, in der Reihenfolge des Laufs.
	Problems []Problem
	// Stopped ist gesetzt, wenn ctx den Lauf zwischen zwei Vorgängen beendet
	// hat: context.Canceled (Abbruch) oder context.DeadlineExceeded
	// (Höchstzeit).
	Stopped error
	// LastWritten sagt bei push, dass die Datei aus Options.Last am Ende
	// geschrieben (oder als unverändert bestätigt) wurde.
	LastWritten bool
	// Restarts zählt bei pull, wie oft der Lauf von vorn begann, weil sich
	// der Store während des Lesens änderte.
	Restarts int
	Duration time.Duration
}

// Complete sagt, ob der Lauf vollständig war: nichts gemeldet, nicht
// beendet.
func (r *Report) Complete() bool { return r.Stopped == nil && len(r.Problems) == 0 }

// Summary ist der Bericht in einer Zeile.
func (r *Report) Summary() string {
	s := fmt.Sprintf("%d angelegt, %d geändert, %d gelöscht, %d unverändert, %d übergangen, %d gemeldet, %s",
		r.Created, r.Changed, r.Deleted, r.Unchanged, r.Skipped, len(r.Problems), r.Duration.Round(time.Millisecond))
	switch {
	case errors.Is(r.Stopped, context.DeadlineExceeded):
		s += "; Höchstzeit erreicht, unvollständig — erneut ausführen"
	case r.Stopped != nil:
		s += "; abgebrochen, unvollständig — erneut ausführen"
	case len(r.Problems) > 0:
		s += "; unvollständig — erneut ausführen"
	}
	return s
}

// errStopped beendet den Lauf, nachdem ctx beendet wurde; Report.Stopped
// trägt den Grund.
var errStopped = errors.New("beendet")

// run ist der gemeinsame Stand eines Laufs.
type run struct {
	ctx  context.Context
	tgt  Target
	opts Options
	rep  *Report
	// lastName ist bei push der volle Name der Datei aus Options.Last im
	// Ziel; leer ohne sie.
	lastName string
}

// stop prüft zwischen zwei Vorgängen, ob ctx den Lauf beendet hat.
func (r *run) stop() error {
	if err := r.ctx.Err(); err != nil {
		if r.rep.Stopped == nil {
			r.rep.Stopped = err
		}
		return errStopped
	}
	return nil
}

// opCtx ist der ctx eines laufenden Vorgangs: Er geht zu Ende, auch wenn ctx
// inzwischen beendet ist.
func (r *run) opCtx() context.Context { return context.WithoutCancel(r.ctx) }

// note schreibt eine Zeile je Vorgang.
func (r *run) note(format string, a ...any) {
	if r.opts.Out != nil {
		fmt.Fprintf(r.opts.Out, format+"\n", a...)
	}
}

// problem meldet einen Fehler eines Vorgangs und lässt den Lauf weitergehen.
func (r *run) problem(op, name string, err error) {
	r.rep.Problems = append(r.rep.Problems, Problem{Op: op, Name: name, Err: err})
	r.note("! %s: %s: %v", name, op, err)
}

// op führt einen Vorgang am Ziel aus: vorher die Prüfung auf Abbruch, ein
// gemeldeter Fehler geht in den Bericht, jeder andere bricht ab. ok sagt, ob
// der Vorgang gelungen ist.
func (r *run) op(op, name string, fn func(ctx context.Context) error) (ok bool, err error) {
	if err := r.stop(); err != nil {
		return false, err
	}
	err = fn(r.opCtx())
	switch {
	case err == nil:
		return true, nil
	case reportable(err):
		r.problem(op, name, err)
		return false, nil
	}
	return false, fmt.Errorf("%s %s: %w", op, name, err)
}

// list liest ein Verzeichnis des Ziels, nach Name des Eintrags sortiert.
func (r *run) list(dir string) ([]Entry, error) {
	if err := r.stop(); err != nil {
		return nil, err
	}
	entries, err := r.tgt.List(r.opCtx(), dir)
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", dirName(dir), err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries, nil
}

// read liest ein Dokument des Ziels.
func (r *run) read(name string) (Document, bool, error) {
	if err := r.stop(); err != nil {
		return Document{}, false, err
	}
	doc, found, err := r.tgt.Read(r.opCtx(), name)
	if err != nil {
		return Document{}, false, fmt.Errorf("read %s: %w", name, err)
	}
	return doc, found, nil
}

// join hängt einen Namen an ein Verzeichnis ("" ist die Wurzel).
func join(dir, name string) string {
	if dir == "" {
		return name
	}
	return dir + "/" + name
}

// base ist das letzte Segment eines Namens.
func base(name string) string { return name[strings.LastIndexByte(name, '/')+1:] }

// parent ist das Verzeichnis eines Namens; "" für die Wurzel.
func parent(name string) string {
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		return name[:i]
	}
	return ""
}

// dirName nennt ein Verzeichnis in Meldungen; die Wurzel als „/“.
func dirName(dir string) string {
	if dir == "" {
		return "/"
	}
	return dir + "/"
}
