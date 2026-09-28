package dirsync

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

// fake ist ein Ziel im Speicher: Dokumente mit Revision, ein Zähler je
// Schreibvorgang, dazu Fehler, die ein Vorgang einmal liefert, und ein Haken
// vor jedem Vorgang (Abbruch, Änderungen zwischendurch). List liefert die
// Einträge in der Reihenfolge der Map — der Abgleich muss selbst sortieren.
type fake struct {
	docs map[string]Document
	rev  int64
	// ops sind die Vorgänge, wie sie ankamen: „create a“, „write a@3“,
	// „delete a@3“, „deletedir d“, „list d“, „read a“.
	ops []string
	// fail liefert für „op name“ einmal diesen Fehler, statt zu schreiben.
	fail map[string]error
	// before läuft vor jedem Vorgang.
	before func(op, name string)
}

func newFake(docs map[string]string) *fake {
	f := &fake{docs: map[string]Document{}, fail: map[string]error{}}
	names := make([]string, 0, len(docs))
	for n := range docs {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		f.rev++
		f.docs[n] = Document{Content: docs[n], Revision: f.rev}
	}
	return f
}

func (f *fake) start(op, name string) error {
	if f.before != nil {
		f.before(op, name)
	}
	key := op + " " + name
	if err, ok := f.fail[key]; ok {
		delete(f.fail, key)
		f.ops = append(f.ops, key+" (Fehler)")
		return err
	}
	return nil
}

func (f *fake) List(_ context.Context, dir string) ([]Entry, error) {
	if err := f.start("list", dir); err != nil {
		return nil, err
	}
	f.ops = append(f.ops, "list "+dir)
	prefix := dir
	if prefix != "" {
		prefix += "/"
	}
	var out []Entry
	seen := map[string]bool{}
	for name, d := range f.docs {
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		rest := name[len(prefix):]
		if i := strings.IndexByte(rest, '/'); i >= 0 {
			sub := prefix + rest[:i]
			if !seen[sub] {
				seen[sub] = true
				out = append(out, Entry{Name: sub, Dir: true})
			}
			continue
		}
		out = append(out, Entry{Name: name, Revision: d.Revision})
	}
	return out, nil
}

func (f *fake) Read(_ context.Context, name string) (Document, bool, error) {
	if err := f.start("read", name); err != nil {
		return Document{}, false, err
	}
	f.ops = append(f.ops, "read "+name)
	d, ok := f.docs[name]
	return d, ok, nil
}

// conflict prüft wie der Hub, dass ein Name nicht zugleich Datei und
// Verzeichnis wäre.
func (f *fake) conflict(name string) error {
	for other := range f.docs {
		if strings.HasPrefix(other, name+"/") || strings.HasPrefix(name, other+"/") {
			return &OpError{Code: "path_conflict", Message: name + " wäre zugleich Datei und Verzeichnis"}
		}
	}
	return nil
}

func (f *fake) Create(_ context.Context, name, content string) error {
	if err := f.start("create", name); err != nil {
		return err
	}
	f.ops = append(f.ops, "create "+name)
	if _, ok := f.docs[name]; ok {
		return &OpError{Code: "name_taken", Message: name + " gibt es schon"}
	}
	if err := f.conflict(name); err != nil {
		return err
	}
	f.rev++
	f.docs[name] = Document{Content: content, Revision: f.rev}
	return nil
}

func (f *fake) Write(_ context.Context, name, content string, base int64) error {
	if err := f.start("write", name); err != nil {
		return err
	}
	f.ops = append(f.ops, fmt.Sprintf("write %s@%d", name, base))
	d, ok := f.docs[name]
	if !ok {
		return &OpError{Code: "not_found", Message: name + " gibt es nicht"}
	}
	if d.Revision != base {
		return &OpError{Code: "stale_revision", Message: fmt.Sprintf("%s hat Revision %d, nicht %d", name, d.Revision, base)}
	}
	f.rev++
	f.docs[name] = Document{Content: content, Revision: f.rev}
	return nil
}

func (f *fake) Delete(_ context.Context, name string, base int64) error {
	if err := f.start("delete", name); err != nil {
		return err
	}
	f.ops = append(f.ops, fmt.Sprintf("delete %s@%d", name, base))
	d, ok := f.docs[name]
	if !ok {
		return &OpError{Code: "not_found", Message: name + " gibt es nicht"}
	}
	if d.Revision != base {
		return &OpError{Code: "stale_revision", Message: fmt.Sprintf("%s hat Revision %d, nicht %d", name, d.Revision, base)}
	}
	f.rev++
	delete(f.docs, name)
	return nil
}

func (f *fake) DeleteDir(_ context.Context, dir string) error {
	if err := f.start("deletedir", dir); err != nil {
		return err
	}
	f.ops = append(f.ops, "deletedir "+dir)
	n := 0
	for name := range f.docs {
		if strings.HasPrefix(name, dir+"/") {
			delete(f.docs, name)
			n++
		}
	}
	if n == 0 {
		return &OpError{Code: "not_found", Message: dir + " gibt es nicht"}
	}
	f.rev++
	return nil
}

// contents liefert die Dokumente als Name → Inhalt.
func (f *fake) contents() map[string]string {
	out := map[string]string{}
	for n, d := range f.docs {
		out[n] = d.Content
	}
	return out
}

// writes sind die Schreibvorgänge unter den aufgezeichneten, ohne Revision.
func (f *fake) writes() []string {
	var out []string
	for _, op := range f.ops {
		if strings.HasPrefix(op, "list ") || strings.HasPrefix(op, "read ") {
			continue
		}
		if i := strings.IndexByte(op, '@'); i >= 0 {
			op = op[:i]
		}
		out = append(out, op)
	}
	return out
}

// writeTree legt Dateien unter root an; ein Pfad mit '/' am Ende ist ein
// leerer Ordner.
func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if strings.HasSuffix(rel, "/") {
			if err := os.MkdirAll(p, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// readTree liest alle regulären Dateien unter root als relativer Pfad →
// Inhalt; Symlinks stehen als „→“.
func readTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			out[rel] = "→"
		case d.Type().IsRegular():
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			out[rel] = string(b)
		}
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return out
	}
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func wantEqual[T any](t *testing.T, what string, got, want T) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s:\n%+v\nerwartet:\n%+v", what, got, want)
	}
}

// pushEnv ist ein Ziel mit Dokumenten unter vendor/x/ und daneben, und ein
// lokaler Ordner, der davon abweicht: geändert, gleich, neu, weg, Art
// gewechselt in beide Richtungen, tief, ein leerer Ordner.
func pushEnv(t *testing.T) (*fake, string) {
	t.Helper()
	f := newFake(map[string]string{
		"vendor/x/a.md":              "alt",
		"vendor/x/same.md":           "gleich",
		"vendor/x/gone.md":           "weg",
		"vendor/x/kind/inner.md":     "war ein Ordner",
		"vendor/x/other.md":          "war eine Datei",
		"vendor/x/deep/l1/l2/c.md":   "c",
		"vendor/x/deep/l1/l2/old.md": "alt",
		"vendor/y/z.md":              "bleibt",
		"top.md":                     "bleibt",
	})
	local := t.TempDir()
	writeTree(t, local, map[string]string{
		"a.md":             "neu",
		"same.md":          "gleich",
		"kind":             "jetzt eine Datei",
		"other/x.md":       "jetzt ein Ordner",
		"deep/l1/l2/c.md":  "c",
		"deep/l1/new.md":   "n",
		"new.md":           "new",
		"empty/":           "",
		"empty/deeper/":    "",
		"deep/l1/l2/leer/": "",
	})
	return f, local
}

// wantPushed ist der Stand des Ziels nach einem vollständigen push aus pushEnv.
var wantPushed = map[string]string{
	"vendor/x/a.md":            "neu",
	"vendor/x/same.md":         "gleich",
	"vendor/x/kind":            "jetzt eine Datei",
	"vendor/x/other/x.md":      "jetzt ein Ordner",
	"vendor/x/deep/l1/l2/c.md": "c",
	"vendor/x/deep/l1/new.md":  "n",
	"vendor/x/new.md":          "new",
	"vendor/y/z.md":            "bleibt",
	"top.md":                   "bleibt",
}

// wantPushOps ist die Reihenfolge der Schreibvorgänge: je Ebene löschen,
// anlegen und schreiben nach Name, dann die Ordner nach Name; leere Ordner
// ergeben nichts.
var wantPushOps = []string{
	"delete vendor/x/gone.md", "deletedir vendor/x/kind", "delete vendor/x/other.md",
	"write vendor/x/a.md", "create vendor/x/kind", "create vendor/x/new.md",
	"create vendor/x/deep/l1/new.md", "delete vendor/x/deep/l1/l2/old.md",
	"create vendor/x/other/x.md",
}

// push: anlegen, ändern, unverändert, löschen, Art gewechselt, tief, leere
// Ordner; die Reihenfolge; die Ausgabe; ein zweiter Lauf ändert nichts.
func TestPush(t *testing.T) {
	f, local := pushEnv(t)
	var out bytes.Buffer
	rep, err := Push(context.Background(), f, "vendor/x", local, Options{Out: &out})
	if err != nil {
		t.Fatal(err)
	}
	wantEqual(t, "Ziel", f.contents(), wantPushed)
	wantEqual(t, "Vorgänge", f.writes(), wantPushOps)
	if rep.Created != 4 || rep.Changed != 1 || rep.Deleted != 4 || rep.Unchanged != 2 || rep.Skipped != 0 ||
		!rep.Complete() || rep.Duration <= 0 {
		t.Errorf("Bericht: %+v", rep)
	}
	wantEqual(t, "Ausgabe", strings.Split(strings.TrimSpace(out.String()), "\n"), []string{
		"- vendor/x/gone.md", "- vendor/x/kind/", "- vendor/x/other.md", "~ vendor/x/a.md", "+ vendor/x/kind",
		"+ vendor/x/new.md", "+ vendor/x/deep/l1/new.md", "- vendor/x/deep/l1/l2/old.md", "+ vendor/x/other/x.md"})
	if !strings.HasPrefix(rep.Summary(), "4 angelegt, 1 geändert, 4 gelöscht, 2 unverändert, 0 übergangen, 0 gemeldet, ") ||
		strings.Contains(rep.Summary(), "unvollständig") {
		t.Errorf("Summary: %s", rep.Summary())
	}

	// Zweiter Lauf: nur lesen, nichts schreiben.
	f.ops = nil
	rep, err = Push(context.Background(), f, "vendor/x", local, Options{})
	if err != nil || !rep.Complete() || rep.Created+rep.Changed+rep.Deleted != 0 || rep.Unchanged != 7 {
		t.Errorf("zweiter Lauf: %+v, %v", rep, err)
	}
	if w := f.writes(); len(w) != 0 {
		t.Errorf("zweiter Lauf schreibt: %v", w)
	}
	wantEqual(t, "Ziel nach zweitem Lauf", f.contents(), wantPushed)
	// Ein '/' am Ende des Verzeichnisses ist erlaubt.
	if rep, err := Push(context.Background(), f, "vendor/x/", local, Options{}); err != nil || rep.Unchanged != 7 {
		t.Errorf("mit '/': %+v, %v", rep, err)
	}
}

// --dry-run liest nur und meldet, was geschähe; das Ziel bleibt.
func TestPushDryRun(t *testing.T) {
	f, local := pushEnv(t)
	before := f.contents()
	var out bytes.Buffer
	rep, err := Push(context.Background(), f, "vendor/x", local, Options{DryRun: true, Out: &out})
	if err != nil {
		t.Fatal(err)
	}
	wantEqual(t, "Ziel", f.contents(), before)
	if w := f.writes(); len(w) != 0 {
		t.Errorf("dry-run schreibt: %v", w)
	}
	if rep.Created != 4 || rep.Changed != 1 || rep.Deleted != 4 || rep.Unchanged != 2 || !rep.Complete() {
		t.Errorf("Bericht: %+v", rep)
	}
	for _, want := range []string{"+ vendor/x/new.md", "~ vendor/x/a.md", "- vendor/x/kind/", "- vendor/x/gone.md"} {
		if !strings.Contains(out.String(), want+"\n") {
			t.Errorf("Ausgabe ohne %q:\n%s", want, out.String())
		}
	}
}

// --last: die Datei ist der letzte Vorgang über alle Ebenen; nach einem
// gemeldeten Konflikt wird sie nicht geschrieben, im nächsten Lauf dann. Ist
// sie unverändert, gilt sie als bestätigt. Fehlt sie, bricht push vorher ab.
func TestPushLast(t *testing.T) {
	f, local := pushEnv(t)
	writeTree(t, local, map[string]string{"VERSION": "2", "deep/MARK": "m"})
	// Konflikt mitten im Lauf: a.md hat inzwischen eine andere Revision.
	f.fail["write vendor/x/a.md"] = &OpError{Code: "stale_revision", Message: "jemand schrieb dazwischen"}
	rep, err := Push(context.Background(), f, "vendor/x", local, Options{Last: "VERSION"})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Complete() || rep.LastWritten || len(rep.Problems) != 1 || rep.Problems[0].Op != "write" ||
		rep.Problems[0].Name != "vendor/x/a.md" || rep.Changed != 0 || rep.Created != 5 {
		t.Errorf("Bericht: %+v", rep)
	}
	if _, ok := f.docs["vendor/x/VERSION"]; ok {
		t.Error("VERSION trotz Konflikt geschrieben")
	}
	if _, ok := f.docs["vendor/x/deep/MARK"]; !ok {
		t.Error("deep/MARK nicht geschrieben")
	}
	if !strings.Contains(rep.Summary(), "1 gemeldet") || !strings.HasSuffix(rep.Summary(), "unvollständig — erneut ausführen") {
		t.Errorf("Summary: %s", rep.Summary())
	}

	// Der nächste Lauf gleicht an und schreibt VERSION zuletzt.
	f.ops = nil
	rep, err = Push(context.Background(), f, "vendor/x", local, Options{Last: "VERSION"})
	if err != nil || !rep.Complete() || !rep.LastWritten || rep.Changed != 1 || rep.Created != 1 {
		t.Fatalf("zweiter Lauf: %+v, %v", rep, err)
	}
	wantEqual(t, "Vorgänge", f.writes(), []string{"write vendor/x/a.md", "create vendor/x/VERSION"})
	if f.docs["vendor/x/VERSION"].Content != "2" {
		t.Error("VERSION fehlt")
	}
	// Unverändert: bestätigt, nicht geschrieben; VERSION wird nicht gelöscht.
	f.ops = nil
	rep, err = Push(context.Background(), f, "vendor/x", local, Options{Last: "VERSION"})
	if err != nil || !rep.Complete() || !rep.LastWritten || rep.Unchanged != 9 || len(f.writes()) != 0 {
		t.Errorf("dritter Lauf: %+v, %v, %v", rep, err, f.writes())
	}
	// Eine Ebene tiefer, und VERSION ist dort ebenso der letzte Vorgang.
	writeTree(t, local, map[string]string{"deep/MARK": "m2", "zz.md": "z"})
	f.ops = nil
	rep, err = Push(context.Background(), f, "vendor/x", local, Options{Last: "deep/MARK"})
	if err != nil || !rep.Complete() || !rep.LastWritten {
		t.Fatalf("mit deep/MARK: %+v, %v", rep, err)
	}
	wantEqual(t, "Vorgänge", f.writes(), []string{"create vendor/x/zz.md", "write vendor/x/deep/MARK"})
	// Ohne VERSION lokal: VERSION im Ziel gilt als weg.
	if err := os.Remove(filepath.Join(local, "VERSION")); err != nil {
		t.Fatal(err)
	}
	if _, err := Push(context.Background(), f, "vendor/x", local, Options{Last: "VERSION"}); err == nil ||
		!strings.Contains(err.Error(), "--last VERSION: keine reguläre Datei") {
		t.Errorf("--last ohne Datei: %v", err)
	}
	if _, ok := f.docs["vendor/x/VERSION"]; !ok {
		t.Error("nach dem Abbruch vor dem Lauf fehlt VERSION")
	}
	rep, err = Push(context.Background(), f, "vendor/x", local, Options{})
	if err != nil || rep.Deleted != 1 {
		t.Errorf("ohne --last: %+v, %v", rep, err)
	}
	if _, ok := f.docs["vendor/x/VERSION"]; ok {
		t.Error("VERSION nicht gelöscht")
	}
}

// Gemeldete Fehler eines Vorgangs — stale_revision, name_taken,
// path_conflict, not_found, outcome_unknown — lassen den Lauf weitergehen;
// er endet unvollständig, der nächste gleicht an. forbidden, not_readable,
// unreachable und Transportfehler brechen ab, mit Bericht bis dahin.
func TestPushErrors(t *testing.T) {
	f, local := pushEnv(t)
	f.fail["delete vendor/x/gone.md"] = &OpError{Code: "stale_revision", Message: "geändert"}
	f.fail["create vendor/x/new.md"] = &OpError{Code: "name_taken", Message: "vergeben"}
	f.fail["create vendor/x/other/x.md"] = &OpError{Code: "outcome_unknown", Message: "unklar"}
	f.fail["deletedir vendor/x/kind"] = &OpError{Code: "not_found", Message: "weg"}
	rep, err := Push(context.Background(), f, "vendor/x", local, Options{})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, p := range rep.Problems {
		got = append(got, p.Op+" "+p.Name)
	}
	// Das Verzeichnis kind blieb (not_found gemeldet), also ist die Datei
	// kind ein path_conflict — ebenso gemeldet.
	wantEqual(t, "gemeldet", got, []string{"delete vendor/x/gone.md", "delete vendor/x/kind/", "create vendor/x/kind",
		"create vendor/x/new.md", "create vendor/x/other/x.md"})
	if rep.Complete() || rep.Created != 1 || rep.Changed != 1 || rep.Deleted != 2 {
		t.Errorf("Bericht: %+v", rep)
	}
	// Der nächste Lauf holt nach (name_taken: das Dokument gibt es jetzt in
	// der Fälschung nicht — also anlegen).
	rep, err = Push(context.Background(), f, "vendor/x", local, Options{})
	if err != nil || !rep.Complete() {
		t.Fatalf("zweiter Lauf: %+v, %v", rep, err)
	}
	wantEqual(t, "Ziel", f.contents(), wantPushed)

	for _, c := range []struct {
		key  string
		err  error
		text string
	}{
		{"write vendor/x/a.md", &OpError{Code: "forbidden", Message: "Scope vendor/x fehlt"}, "write vendor/x/a.md: forbidden: Scope vendor/x fehlt"},
		{"list vendor/x", &OpError{Code: "not_readable", Message: "nicht lesbar"}, "list vendor/x/: not_readable"},
		{"read vendor/x/a.md", errors.New("Verbindung abgelehnt"), "read vendor/x/a.md: Verbindung abgelehnt"},
		{"create vendor/x/neu.md", &OpError{Code: "unreachable", Message: "Hub nicht erreichbar"}, "create vendor/x/neu.md: unreachable"},
	} {
		f, local := pushEnv(t)
		writeTree(t, local, map[string]string{"neu.md": "x"})
		f.fail[c.key] = c.err
		rep, err := Push(context.Background(), f, "vendor/x", local, Options{})
		if err == nil || !strings.Contains(err.Error(), c.text) || !errors.Is(err, c.err) {
			t.Errorf("%s: %v", c.key, err)
		}
		if rep == nil || rep.Duration <= 0 {
			t.Errorf("%s: kein Bericht", c.key)
		}
	}
}

// Abbruch und Höchstzeit wirken zwischen zwei Vorgängen: Der laufende geht
// zu Ende, der nächste beginnt nicht; der Bericht sagt, dass der Lauf
// unvollständig ist. Ein neuer Lauf setzt fort.
func TestPushStopped(t *testing.T) {
	f, local := pushEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	f.before = func(op, name string) {
		if op == "create" && name == "vendor/x/kind" {
			cancel()
		}
	}
	rep, err := Push(ctx, f, "vendor/x", local, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(rep.Stopped, context.Canceled) || rep.Complete() || rep.Created != 1 || rep.Changed != 1 || rep.Deleted != 3 {
		t.Errorf("Bericht: %+v", rep)
	}
	if f.docs["vendor/x/kind"].Content != "jetzt eine Datei" {
		t.Error("der laufende Vorgang ging nicht zu Ende")
	}
	if _, ok := f.docs["vendor/x/new.md"]; ok {
		t.Error("nach dem Abbruch noch geschrieben")
	}
	if !strings.HasSuffix(rep.Summary(), "abgebrochen, unvollständig — erneut ausführen") {
		t.Errorf("Summary: %s", rep.Summary())
	}
	// Fortsetzen.
	f.before = nil
	rep, err = Push(context.Background(), f, "vendor/x", local, Options{})
	if err != nil || !rep.Complete() || rep.Created != 3 {
		t.Errorf("Fortsetzen: %+v, %v", rep, err)
	}
	wantEqual(t, "Ziel", f.contents(), wantPushed)

	// Höchstzeit: abgelaufen, während ein Vorgang läuft.
	f, local = pushEnv(t)
	ctx, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	f.before = func(op, name string) {
		if op == "delete" && name == "vendor/x/gone.md" {
			time.Sleep(40 * time.Millisecond)
		}
	}
	rep, err = Push(ctx, f, "vendor/x", local, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(rep.Stopped, context.DeadlineExceeded) || rep.Deleted != 1 || rep.Created+rep.Changed != 0 {
		t.Errorf("Höchstzeit: %+v", rep)
	}
	if _, ok := f.docs["vendor/x/gone.md"]; ok {
		t.Error("der laufende Vorgang ging nicht zu Ende")
	}
	if !strings.Contains(rep.Summary(), "Höchstzeit erreicht") {
		t.Errorf("Summary: %s", rep.Summary())
	}
	// Schon vor dem ersten Vorgang beendet: nichts geschieht.
	f.before = nil
	done, cancelDone := context.WithCancel(context.Background())
	cancelDone()
	rep, err = Push(done, f, "vendor/x", local, Options{})
	if err != nil || rep.Stopped == nil || len(f.writes()) != 1 {
		t.Errorf("schon beendet: %+v, %v, %v", rep, err, f.writes())
	}
}

// Die Vorabprüfung: kein UTF-8, NUL, zu groß, ungültiger Name — alle Treffer
// auf einmal, nichts geschrieben. Symlinks und Nicht-Dateien werden
// übergangen und gezählt, .git und --exclude ausgelassen.
func TestPushPrecheck(t *testing.T) {
	f, local := pushEnv(t)
	writeTree(t, local, map[string]string{
		"bin.md":           "a\xffb",
		"nul.md":           "a\x00b",
		"gross.md":         strings.Repeat("x", 1<<20+1),
		"genau.md":         strings.Repeat("y", 1<<20),
		"deep/back\\.md":   "x",
		".git/HEAD":        "ref",
		".git/schlecht.md": "a\xff",
		"out.tmp":          "a\xff",
	})
	if err := os.Symlink(filepath.Join(local, "a.md"), filepath.Join(local, "link.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(local, filepath.Join(local, "deep", "loop")); err != nil {
		t.Fatal(err)
	}
	before := f.contents()
	var out bytes.Buffer
	rep, err := Push(context.Background(), f, "vendor/x", local, Options{Exclude: []string{"*.tmp"}, Out: &out})
	var pe *PrecheckError
	if !errors.As(err, &pe) {
		t.Fatalf("kein PrecheckError: %v", err)
	}
	var hits []string
	for _, h := range pe.Hits {
		hits = append(hits, h.Path)
	}
	wantEqual(t, "Treffer", hits, []string{"bin.md", "deep/back\\.md", "gross.md", "nul.md"})
	for _, want := range []string{"Vorabprüfung: 4 Treffer, nichts geschrieben", "bin.md: kein UTF-8-Text",
		"nul.md: kein UTF-8-Text", "gross.md: größer als 1 MiB", `deep/back\.md: Dokument "vendor/x/deep/back\\.md": der Name enthält '\'`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Meldung ohne %q:\n%v", want, err)
		}
	}
	if !errors.Is(pe.Hits[0].Err, ErrNotText) || !errors.Is(pe.Hits[2].Err, ErrTooLarge) {
		t.Errorf("Fehlerarten: %v, %v", pe.Hits[0].Err, pe.Hits[2].Err)
	}
	wantEqual(t, "Ziel", f.contents(), before)
	if len(f.ops) != 0 || rep.Created+rep.Changed+rep.Deleted != 0 {
		t.Errorf("trotz Treffern gearbeitet: %v, %+v", f.ops, rep)
	}

	// Ohne die Treffer: Symlinks übergangen und gemeldet, genau 1 MiB geht.
	for _, p := range []string{"bin.md", "nul.md", "gross.md", "deep/back\\.md"} {
		if err := os.Remove(filepath.Join(local, filepath.FromSlash(p))); err != nil {
			t.Fatal(err)
		}
	}
	out.Reset()
	rep, err = Push(context.Background(), f, "vendor/x", local, Options{Exclude: []string{"*.tmp"}, Out: &out})
	if err != nil || !rep.Complete() || rep.Skipped != 2 {
		t.Fatalf("Bericht: %+v, %v", rep, err)
	}
	for _, want := range []string{"! deep/loop: Symlink übergangen", "! link.md: Symlink übergangen"} {
		if !strings.Contains(out.String(), want+"\n") {
			t.Errorf("Ausgabe ohne %q:\n%s", want, out.String())
		}
	}
	got := f.contents()
	for _, name := range []string{"vendor/x/link.md", "vendor/x/deep/loop", "vendor/x/.git/HEAD", "vendor/x/.git/schlecht.md",
		"vendor/x/out.tmp"} {
		if _, ok := got[name]; ok {
			t.Errorf("%s im Ziel", name)
		}
	}
	if len(got["vendor/x/genau.md"]) != 1<<20 {
		t.Error("genau 1 MiB fehlt")
	}
	if !strings.Contains(rep.Summary(), "2 übergangen") {
		t.Errorf("Summary: %s", rep.Summary())
	}
}

// .git und Treffer von --exclude bleiben im Ziel unberührt: weder angelegt
// noch geändert noch gelöscht — auf jeder Ebene.
func TestPushExclude(t *testing.T) {
	f := newFake(map[string]string{
		"vendor/x/.git/config":    "bleibt",
		"vendor/x/keep.tmp":       "bleibt",
		"vendor/x/build/out.md":   "bleibt",
		"vendor/x/sub/build/o.md": "bleibt",
		"vendor/x/sub/x.tmp":      "bleibt",
		"vendor/x/sub/.git/HEAD":  "bleibt",
		"vendor/x/a.md":           "alt",
		"vendor/x/sub/gone.md":    "weg",
		"vendor/x/build":          "ein Dokument, ausgelassen",
	})
	local := t.TempDir()
	writeTree(t, local, map[string]string{
		"a.md": "neu", ".git/HEAD": "x", "foo.tmp": "x", "build/new.md": "x", "sub/a.md": "x", "sub/y.tmp": "x",
		"sub/build/n.md": "x", "keep.tmp": "anders",
	})
	rep, err := Push(context.Background(), f, "vendor/x", local, Options{Exclude: []string{"*.tmp", "build"}})
	if err != nil || !rep.Complete() {
		t.Fatalf("%+v, %v", rep, err)
	}
	wantEqual(t, "Ziel", f.contents(), map[string]string{
		"vendor/x/.git/config": "bleibt", "vendor/x/keep.tmp": "bleibt", "vendor/x/build/out.md": "bleibt",
		"vendor/x/sub/build/o.md": "bleibt", "vendor/x/sub/x.tmp": "bleibt", "vendor/x/sub/.git/HEAD": "bleibt",
		"vendor/x/a.md": "neu", "vendor/x/sub/a.md": "x", "vendor/x/build": "ein Dokument, ausgelassen",
	})
	wantEqual(t, "Vorgänge", f.writes(), []string{"write vendor/x/a.md", "delete vendor/x/sub/gone.md", "create vendor/x/sub/a.md"})
	for _, bad := range []string{"[", "a/b", ""} {
		if _, err := Push(context.Background(), f, "vendor/x", local, Options{Exclude: []string{bad}}); err == nil ||
			!strings.Contains(err.Error(), "kein gültiger Glob") {
			t.Errorf("--exclude %q: %v", bad, err)
		}
	}
}

// push nimmt vorerst nur vendor/<name> oder darunter; ein ungültiges
// Verzeichnis lehnt es ab.
func TestCheckPushDir(t *testing.T) {
	for _, ok := range []string{"vendor/k-playbook", "vendor/k-playbook/", "vendor/k-playbook/rules", "vendor/x/a/b/"} {
		if err := CheckPushDir(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "/", "vendor", "vendor/", "docs", "Vendor/x", "vendors/x", "vendor//x", "/vendor/x"} {
		if err := CheckPushDir(bad); err == nil {
			t.Errorf("%q angenommen", bad)
		}
	}
	if err := CheckPushDir("docs"); !strings.Contains(err.Error(), "nur unter vendor/<name>/, nicht nach docs/") {
		t.Errorf("Meldung: %v", err)
	}
	f, local := pushEnv(t)
	if _, err := Push(context.Background(), f, "..", local, Options{}); err == nil {
		t.Error("'..' angenommen")
	}
	if _, err := Push(context.Background(), f, "vendor/x", filepath.Join(local, "a.md"), Options{}); err == nil ||
		!strings.Contains(err.Error(), "kein Ordner") {
		t.Errorf("Datei als Ordner: %v", err)
	}
	if _, err := Push(context.Background(), f, "vendor/x", filepath.Join(local, "fehlt"), Options{}); err == nil {
		t.Error("fehlender Ordner angenommen")
	}
}

// pullEnv ist ein Ziel mit einem Verzeichnis, das pull holt.
func pullEnv(t *testing.T) (*fake, string) {
	t.Helper()
	f := newFake(map[string]string{
		"docs/a.md":           "a",
		"docs/sub/b.md":       "b",
		"docs/sub/deep/c.md":  "c",
		"docs/kind":           "im Store eine Datei",
		"docs/other/x.md":     "im Store ein Ordner",
		"docs/.git/config":    "ausgelassen",
		"docs/x.tmp":          "ausgelassen",
		"docs/sub/build/o.md": "ausgelassen",
		"top.md":              "nicht dabei",
	})
	return f, filepath.Join(t.TempDir(), "ziel")
}

var wantPulled = map[string]string{"a.md": "a", "sub/b.md": "b", "sub/deep/c.md": "c", "kind": "im Store eine Datei",
	"other/x.md": "im Store ein Ordner"}

// pull: in einen leeren Ordner (0644, 0755), dann nur Abweichendes; ohne
// --delete bleibt Überzähliges, mit --delete geht es — außer .git und
// --exclude, in beide Richtungen. Art gewechselt nur mit --delete.
func TestPull(t *testing.T) {
	f, local := pullEnv(t)
	var out bytes.Buffer
	opts := Options{Exclude: []string{"*.tmp", "build"}, Out: &out}
	rep, err := Pull(context.Background(), f, "docs", local, opts)
	if err != nil || !rep.Complete() || rep.Created != 5 || rep.Changed+rep.Deleted+rep.Unchanged != 0 {
		t.Fatalf("erster Lauf: %+v, %v", rep, err)
	}
	wantEqual(t, "lokal", readTree(t, local), wantPulled)
	for p, want := range map[string]fs.FileMode{"a.md": 0o644, "sub": 0o755 | fs.ModeDir} {
		info, err := os.Stat(filepath.Join(local, p))
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode() &^ umaskBits(t); got != want&^umaskBits(t) {
			t.Errorf("%s: %v, erwartet %v", p, got, want)
		}
	}
	wantEqual(t, "Ausgabe", strings.Split(strings.TrimSpace(out.String()), "\n"),
		[]string{"+ docs/a.md", "+ docs/kind", "+ docs/other/x.md", "+ docs/sub/b.md", "+ docs/sub/deep/c.md"})

	// Zweiter Lauf: unverändert. Dann lokal geändert und gelöscht.
	rep, err = Pull(context.Background(), f, "docs", local, opts)
	if err != nil || !rep.Complete() || rep.Unchanged != 5 || rep.Created+rep.Changed != 0 {
		t.Errorf("zweiter Lauf: %+v, %v", rep, err)
	}
	writeTree(t, local, map[string]string{"a.md": "geändert", "extra.md": "lokal", "sub/extra/e.md": "lokal",
		"sub/extra/.git/x": "git", "sub/extra/keep.tmp": "tmp", ".git/HEAD": "git", "mine.tmp": "tmp", "build/b.md": "b",
		"sub/deep/": ""})
	if err := os.Remove(filepath.Join(local, "sub", "b.md")); err != nil {
		t.Fatal(err)
	}
	// Ohne --delete: Überzähliges bleibt, a.md und b.md kommen zurück.
	out.Reset()
	rep, err = Pull(context.Background(), f, "docs", local, opts)
	if err != nil || !rep.Complete() || rep.Changed != 1 || rep.Created != 1 || rep.Deleted != 0 || rep.Unchanged != 3 {
		t.Errorf("ohne --delete: %+v, %v", rep, err)
	}
	got := readTree(t, local)
	if got["a.md"] != "a" || got["sub/b.md"] != "b" || got["extra.md"] != "lokal" || got["sub/extra/e.md"] != "lokal" {
		t.Errorf("lokal: %v", got)
	}
	// Mit --delete: Überzähliges geht, .git und --exclude bleiben — auch in
	// einem Ordner, der sonst ganz ginge; der Ordner bleibt dann.
	opts.Delete = true
	rep, err = Pull(context.Background(), f, "docs", local, opts)
	if err != nil || !rep.Complete() || rep.Deleted != 2 || rep.Unchanged != 5 {
		t.Errorf("mit --delete: %+v, %v", rep, err)
	}
	wantEqual(t, "lokal mit --delete", readTree(t, local), map[string]string{"a.md": "a", "sub/b.md": "b",
		"sub/deep/c.md": "c", "kind": "im Store eine Datei", "other/x.md": "im Store ein Ordner", "sub/extra/.git/x": "git",
		"sub/extra/keep.tmp": "tmp", ".git/HEAD": "git", "mine.tmp": "tmp", "build/b.md": "b"})
	if _, err := os.Stat(filepath.Join(local, "sub", "extra", "e.md")); !errors.Is(err, fs.ErrNotExist) {
		t.Error("sub/extra/e.md nicht gelöscht")
	}

	// Art gewechselt: lokal ein Ordner, wo der Store eine Datei hat, und
	// umgekehrt — ohne --delete gemeldet, mit --delete ersetzt.
	if err := os.RemoveAll(filepath.Join(local, "kind")); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(local, "other")); err != nil {
		t.Fatal(err)
	}
	writeTree(t, local, map[string]string{"kind/inner.md": "lokal ein Ordner", "other": "lokal eine Datei"})
	opts.Delete = false
	rep, err = Pull(context.Background(), f, "docs", local, opts)
	if err != nil || rep.Complete() || len(rep.Problems) != 2 {
		t.Fatalf("Art gewechselt ohne --delete: %+v, %v", rep, err)
	}
	if p := rep.Problems; p[0].Name != "docs/kind" || !strings.Contains(p[0].Err.Error(), "lokal ein Ordner") ||
		p[1].Name != "docs/other" || !strings.Contains(p[1].Err.Error(), "lokal eine Datei") {
		t.Errorf("gemeldet: %v", p)
	}
	if got := readTree(t, local); got["kind/inner.md"] != "lokal ein Ordner" || got["other"] != "lokal eine Datei" {
		t.Errorf("lokal verändert: %v", got)
	}
	opts.Delete = true
	rep, err = Pull(context.Background(), f, "docs", local, opts)
	if err != nil || !rep.Complete() || rep.Deleted != 2 || rep.Created != 2 {
		t.Errorf("Art gewechselt mit --delete: %+v, %v", rep, err)
	}
	if got := readTree(t, local); got["kind"] != "im Store eine Datei" || got["other/x.md"] != "im Store ein Ordner" {
		t.Errorf("lokal: %v", got)
	}
	// Die Wurzel der Collection, leer oder als '/': alles außer .git.
	for _, dir := range []string{"", "/"} {
		rep, err := Pull(context.Background(), f, dir, filepath.Join(local, "..", "wurzel"), Options{DryRun: true})
		if err != nil || rep.Created != 8 {
			t.Errorf("Wurzel %q: %+v, %v", dir, rep, err)
		}
	}
}

// umaskBits liefert die Bits, die die umask wegnimmt — für den Vergleich
// der Rechte neuer Dateien und Ordner.
func umaskBits(t *testing.T) fs.FileMode {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "probe")
	if err := os.WriteFile(p, nil, 0o666); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return 0o666 &^ info.Mode().Perm()
}

// pull folgt keinem Symlink im Zielordner: an einer Stelle, die geschrieben
// werden müsste, ist er ein gemeldeter Fehler dieser Datei bzw. dieses
// Ordners, sein Ziel bleibt unberührt; ein Symlink, den der Store nicht
// kennt, geht mit --delete als Link.
func TestPullSymlink(t *testing.T) {
	f, local := pullEnv(t)
	outside := t.TempDir()
	writeTree(t, outside, map[string]string{"a.md": "draußen", "sub/x.md": "draußen"})
	if err := os.MkdirAll(local, 0o755); err != nil {
		t.Fatal(err)
	}
	for link, target := range map[string]string{"a.md": filepath.Join(outside, "a.md"), "sub": filepath.Join(outside, "sub"),
		"fremd": filepath.Join(outside, "a.md")} {
		if err := os.Symlink(target, filepath.Join(local, link)); err != nil {
			t.Fatal(err)
		}
	}
	rep, err := Pull(context.Background(), f, "docs", local, Options{Exclude: []string{"*.tmp", "build"}, Delete: true})
	if err != nil || rep.Complete() || len(rep.Problems) != 2 || rep.Created != 2 || rep.Deleted != 1 {
		t.Fatalf("%+v, %v", rep, err)
	}
	if p := rep.Problems; p[0].Name != "docs/a.md" || !strings.Contains(p[0].Err.Error(), "Symlink") ||
		p[1].Name != "docs/sub" || !strings.Contains(p[1].Err.Error(), "Symlink") {
		t.Errorf("gemeldet: %v", p)
	}
	wantEqual(t, "draußen unberührt", readTree(t, outside), map[string]string{"a.md": "draußen", "sub/x.md": "draußen"})
	got := readTree(t, local)
	if got["a.md"] != "→" || got["sub"] != "→" || got["kind"] != "im Store eine Datei" || got["other/x.md"] == "" {
		t.Errorf("lokal: %v", got)
	}
	if _, ok := got["fremd"]; ok {
		t.Error("fremder Symlink nicht gelöscht")
	}
	if _, err := os.Stat(filepath.Join(outside, "a.md")); err != nil {
		t.Error("das Ziel des fremden Symlinks ist weg")
	}
}

// Ändert sich der Store während des Lesens — die Revision bei read weicht
// von der aus list ab —, beginnt pull von vorn, höchstens dreimal.
func TestPullChanged(t *testing.T) {
	f, local := pullEnv(t)
	changes := 0
	f.before = func(op, name string) {
		if op == "read" && name == "docs/sub/b.md" && changes < 1 {
			changes++
			f.rev++
			f.docs[name] = Document{Content: "b2", Revision: f.rev}
		}
	}
	var out bytes.Buffer
	rep, err := Pull(context.Background(), f, "docs", local, Options{Exclude: []string{"*.tmp", "build"}, Out: &out})
	if err != nil || !rep.Complete() || rep.Restarts != 1 || rep.Created != 2 || rep.Unchanged != 3 {
		t.Fatalf("%+v, %v", rep, err)
	}
	if got := readTree(t, local); got["sub/b.md"] != "b2" || len(got) != 5 {
		t.Errorf("lokal: %v", got)
	}
	if !strings.Contains(out.String(), "von vorn") {
		t.Errorf("Ausgabe ohne Hinweis:\n%s", out.String())
	}
	// Ändert es sich immer wieder: Abbruch mit Hinweis.
	f.before = func(op, name string) {
		if op == "read" && name == "docs/a.md" {
			f.rev++
			f.docs[name] = Document{Content: fmt.Sprint(f.rev), Revision: f.rev}
		}
	}
	rep, err = Pull(context.Background(), f, "docs", local, Options{Exclude: []string{"*.tmp", "build"}})
	if err == nil || !errors.Is(err, errChanged) || !strings.Contains(err.Error(), "3-mal von vorn") || rep.Restarts != 2 {
		t.Errorf("dreimal: %+v, %v", rep, err)
	}
}

// pull mit --dry-run, --timeout und Abbruch: nichts geschrieben bzw. der
// laufende Vorgang zu Ende, dann Schluss; Fehler des Ziels brechen ab.
func TestPullDryRunAndStopped(t *testing.T) {
	f, local := pullEnv(t)
	var out bytes.Buffer
	rep, err := Pull(context.Background(), f, "docs", local, Options{DryRun: true, Out: &out})
	if err != nil || !rep.Complete() || rep.Created != 7 {
		t.Fatalf("dry-run: %+v, %v", rep, err)
	}
	if _, err := os.Stat(local); !errors.Is(err, fs.ErrNotExist) {
		t.Error("dry-run legt den Ordner an")
	}
	writeTree(t, local, map[string]string{"extra.md": "x", "a.md": "alt"})
	out.Reset()
	rep, err = Pull(context.Background(), f, "docs", local, Options{DryRun: true, Delete: true, Out: &out})
	if err != nil || rep.Created != 6 || rep.Changed != 1 || rep.Deleted != 1 {
		t.Errorf("dry-run mit --delete: %+v, %v", rep, err)
	}
	wantEqual(t, "lokal", readTree(t, local), map[string]string{"extra.md": "x", "a.md": "alt"})
	if !strings.Contains(out.String(), "- docs/extra.md\n") || !strings.Contains(out.String(), "~ docs/a.md\n") {
		t.Errorf("Ausgabe:\n%s", out.String())
	}

	ctx, cancel := context.WithCancel(context.Background())
	f.before = func(op, name string) {
		if op == "read" && name == "docs/kind" {
			cancel()
		}
	}
	rep, err = Pull(ctx, f, "docs", local, Options{Delete: true})
	if err != nil || !errors.Is(rep.Stopped, context.Canceled) || rep.Changed != 1 {
		t.Errorf("Abbruch: %+v, %v", rep, err)
	}
	if got := readTree(t, local); got["a.md"] != "a" || got["kind"] != "" || got["extra.md"] != "" {
		t.Errorf("lokal nach Abbruch: %v", got)
	}
	f.before = nil
	f.fail["read docs/sub/b.md"] = &OpError{Code: "not_readable", Message: "nicht lesbar"}
	if rep, err := Pull(context.Background(), f, "docs", local, Options{}); err == nil ||
		!strings.Contains(err.Error(), "read docs/sub/b.md: not_readable") || rep == nil {
		t.Errorf("Fehler des Ziels: %+v, %v", rep, err)
	}
	if _, err := Pull(context.Background(), f, "docs", local, Options{Last: "x"}); err == nil {
		t.Error("--last bei pull angenommen")
	}
	if _, err := Pull(context.Background(), f, "docs", filepath.Join(local, "a.md"), Options{}); err == nil {
		t.Error("Datei als Ordner angenommen")
	}
}
