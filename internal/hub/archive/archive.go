// Package archive ist das Dateiformat von hub export und hub restore (Task
// 039): ein tar.gz, das sich ohne kephalaion lesen lässt.
//
//	manifest.json            Format-Fassung, Schemafassung, Revision, hub_id,
//	                         serve_stopped, Zeitpunkt, Binary, Zeilen je Teil
//	<tabelle>.jsonl          je Tabelle des Abzugs (store.DumpTables) eine
//	                         Zeile JSON je Datenbankzeile: Spalten mit Namen,
//	                         Ganzzahlen als Zahlen, NULL als null
//	files/<collection>/<name> der lebende Inhalt jedes Dokuments außer
//	                         SYSTEM:, nur zum Lesen
//
// Die Reihenfolge der Zeilen ist fest (store.DumpTables), aber nichts hängt
// an ihr: Wer liest, behandelt jeden Teil als Multimenge. Restore liest nur
// Manifest und Tabellen; files/ übergeht es.
package archive

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kephalaion/kephalaion/internal/hub/store"
)

// Format ist die Fassung des Formats, die dieses Binary schreibt und liest.
const Format = 1

// Namen im Archiv.
const (
	ManifestName = "manifest.json"
	FilesDir     = "files/"
	partSuffix   = ".jsonl"
	// maxManifest begrenzt, was vom Manifest gelesen wird.
	maxManifest = 1 << 20
)

// Manifest beschreibt einen Export.
type Manifest struct {
	Format int `json:"format"`
	// SchemaVersion ist die Schemafassung des Hubs, aus dem der Export stammt.
	SchemaVersion int `json:"schema_version"`
	// Revision und HubID sind die aus db_info, gelesen mit den Zeilen.
	Revision int64  `json:"revision"`
	HubID    string `json:"hub_id"`
	// ServeStopped: Der Export hielt die Sperre von serve — er ist sicher
	// der letzte Stand dieses Hubs, solange danach nichts mehr schreibt.
	ServeStopped bool `json:"serve_stopped"`
	// CreatedAt ist der Zeitpunkt des Exports, RFC 3339 in UTC.
	CreatedAt string `json:"created_at"`
	// Version ist die Version des Binarys, das exportiert hat.
	Version string `json:"version"`
	// Rows zählt die Zeilen je Tabelle (Teil <tabelle>.jsonl).
	Rows map[string]int64 `json:"rows"`
	// Files zählt die Dateien unter files/.
	Files int64 `json:"files"`
}

// PartName ist der Name des Teils einer Tabelle.
func PartName(table string) string { return table + partSuffix }

// Writer sammelt die Zeilen eines Exports in Dateien eines eigenen
// Verzeichnisses und schreibt danach das Archiv. So steht nichts davon im
// Speicher, und die Zahlen fürs Manifest sind bekannt, bevor es geschrieben
// wird.
type Writer struct {
	dir     string
	tables  []store.DumpTable
	parts   map[string]*part
	content *os.File
	files   []fileRef
	line    bytes.Buffer
	enc     *json.Encoder
	sz      int64
}

type part struct {
	table store.DumpTable
	f     *os.File
	w     *bufio.Writer
	n     int64
	// Spalten, aus denen files/ entsteht (nur documents).
	collection, name, content, deleted int
}

type fileRef struct {
	name      string
	off, size int64
}

// NewWriter legt ein Arbeitsverzeichnis unter tmpDir an ("" für das
// temporäre Verzeichnis des Systems). Close räumt es ab.
func NewWriter(tmpDir string) (*Writer, error) {
	dir, err := os.MkdirTemp(tmpDir, ".hub-export-*")
	if err != nil {
		return nil, fmt.Errorf("Arbeitsverzeichnis: %w", err)
	}
	w := &Writer{dir: dir, tables: store.DumpTables(), parts: map[string]*part{}}
	w.enc = json.NewEncoder(&w.line)
	w.enc.SetEscapeHTML(false)
	ok := false
	defer func() {
		if !ok {
			_ = w.Close()
		}
	}()
	for _, t := range w.tables {
		f, err := os.Create(filepath.Join(dir, PartName(t.Name)))
		if err != nil {
			return nil, err
		}
		p := &part{table: t, f: f, w: bufio.NewWriter(f), collection: -1, name: -1, content: -1, deleted: -1}
		if t.Name == "documents" {
			for i, c := range t.Columns {
				switch c.Name {
				case "collection":
					p.collection = i
				case "name":
					p.name = i
				case "content":
					p.content = i
				case "deleted":
					p.deleted = i
				}
			}
		}
		w.parts[t.Name] = p
	}
	if w.content, err = os.Create(filepath.Join(dir, "content")); err != nil {
		return nil, err
	}
	ok = true
	return w, nil
}

// Row nimmt eine Zeile einer Tabelle, values wie store.Dump sie liefert.
func (w *Writer) Row(table string, values []any) error {
	p, ok := w.parts[table]
	if !ok {
		return fmt.Errorf("unbekannte Tabelle %s", table)
	}
	if len(values) != len(p.table.Columns) {
		return fmt.Errorf("%s: %d Werte für %d Spalten", table, len(values), len(p.table.Columns))
	}
	w.line.Reset()
	w.line.WriteByte('{')
	for i, c := range p.table.Columns {
		if i > 0 {
			w.line.WriteByte(',')
		}
		if err := w.encode(c.Name); err != nil {
			return err
		}
		w.line.WriteByte(':')
		switch v := values[i].(type) {
		case nil:
			if !c.Null {
				return fmt.Errorf("%s.%s: NULL, wo keines sein darf", table, c.Name)
			}
			w.line.WriteString("null")
		case int64:
			if !c.Int {
				return fmt.Errorf("%s.%s: Zahl statt Text", table, c.Name)
			}
			w.line.WriteString(strconv.FormatInt(v, 10))
		case string:
			if c.Int {
				return fmt.Errorf("%s.%s: Text statt Zahl", table, c.Name)
			}
			// JSON ersetzte ungültige Bytes still; der Export wäre nicht mehr
			// derselbe Inhalt.
			if !utf8.ValidString(v) {
				return fmt.Errorf("%s.%s: kein gültiges UTF-8", table, c.Name)
			}
			if err := w.encode(v); err != nil {
				return err
			}
		default:
			return fmt.Errorf("%s.%s: Wert vom Typ %T", table, c.Name, v)
		}
	}
	w.line.WriteString("}\n")
	if _, err := p.w.Write(w.line.Bytes()); err != nil {
		return err
	}
	p.n++
	if p.content >= 0 {
		return w.file(p, values)
	}
	return nil
}

// encode hängt v als JSON an die Zeile, ohne Zeilenende.
func (w *Writer) encode(v string) error {
	if err := w.enc.Encode(v); err != nil {
		return err
	}
	w.line.Truncate(w.line.Len() - 1)
	return nil
}

// file merkt sich den Inhalt eines lebenden Dokuments für files/.
func (w *Writer) file(p *part, values []any) error {
	content, ok := values[p.content].(string)
	name, _ := values[p.name].(string)
	collection, _ := values[p.collection].(string)
	deleted, _ := values[p.deleted].(int64)
	if !ok || deleted != 0 || strings.HasPrefix(name, "SYSTEM:") {
		return nil
	}
	n, err := io.WriteString(w.content, content)
	if err != nil {
		return err
	}
	w.files = append(w.files, fileRef{name: FilesDir + collection + "/" + name, off: w.sz, size: int64(n)})
	w.sz += int64(n)
	return nil
}

// WriteTo schreibt das Archiv nach out: zuerst das Manifest — m mit den
// gezählten Zeilen und Dateien und dieser Format-Fassung —, dann die
// Tabellen in der Reihenfolge von store.DumpTables, dann files/ nach Namen.
// Es liefert das geschriebene Manifest.
func (w *Writer) WriteTo(out io.Writer, m Manifest) (Manifest, error) {
	m.Format = Format
	m.Rows = map[string]int64{}
	for _, t := range w.tables {
		p := w.parts[t.Name]
		if err := p.w.Flush(); err != nil {
			return Manifest{}, err
		}
		m.Rows[t.Name] = p.n
	}
	m.Files = int64(len(w.files))
	at := time.Now().UTC()
	if t, err := time.Parse(time.RFC3339, m.CreatedAt); err == nil {
		at = t
	}
	manifest, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return Manifest{}, err
	}
	manifest = append(manifest, '\n')

	gz := gzip.NewWriter(out)
	tw := tar.NewWriter(gz)
	header := func(name string, size int64) error {
		return tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: name, Size: size, Mode: 0o600, ModTime: at})
	}
	if err := header(ManifestName, int64(len(manifest))); err != nil {
		return Manifest{}, err
	}
	if _, err := tw.Write(manifest); err != nil {
		return Manifest{}, err
	}
	for _, t := range w.tables {
		f := w.parts[t.Name].f
		fi, err := f.Stat()
		if err != nil {
			return Manifest{}, err
		}
		if err := header(PartName(t.Name), fi.Size()); err != nil {
			return Manifest{}, err
		}
		if _, err := io.Copy(tw, io.NewSectionReader(f, 0, fi.Size())); err != nil {
			return Manifest{}, err
		}
	}
	files := append([]fileRef(nil), w.files...)
	sort.Slice(files, func(i, j int) bool { return files[i].name < files[j].name })
	for _, ref := range files {
		if err := header(ref.name, ref.size); err != nil {
			return Manifest{}, err
		}
		if _, err := io.Copy(tw, io.NewSectionReader(w.content, ref.off, ref.size)); err != nil {
			return Manifest{}, err
		}
	}
	if err := tw.Close(); err != nil {
		return Manifest{}, err
	}
	if err := gz.Close(); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

// Close schließt die Arbeitsdateien und räumt das Arbeitsverzeichnis ab.
func (w *Writer) Close() error {
	for _, p := range w.parts {
		_ = p.f.Close()
	}
	if w.content != nil {
		_ = w.content.Close()
	}
	return os.RemoveAll(w.dir)
}

// Archive ist ein gelesener Export: das Manifest und je Tabelle ihr Teil in
// einer Arbeitsdatei.
type Archive struct {
	Manifest Manifest
	dir      string
	parts    map[string]string
}

// Open liest einen Export: das Manifest und die Teile der Tabellen, die es in
// Arbeitsdateien unter tmpDir ablegt ("" für das temporäre Verzeichnis des
// Systems); files/ übergeht es. Es prüft die Form: Format-Fassung, jeder Teil
// genau einmal, kein unbekannter, das Manifest zählt genau diese Tabellen.
// Die Zeilen selbst prüft erst Rows. Close räumt ab.
func Open(path, tmpDir string) (_ *Archive, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(bufio.NewReader(f))
	if err != nil {
		return nil, fmt.Errorf("%s ist kein Export von kephalaion hub export (gzip: %w)", path, err)
	}
	dir, err := os.MkdirTemp(tmpDir, ".hub-restore-*")
	if err != nil {
		return nil, fmt.Errorf("Arbeitsverzeichnis: %w", err)
	}
	a := &Archive{dir: dir, parts: map[string]string{}}
	defer func() {
		if err != nil {
			_ = a.Close()
		}
	}()
	known := map[string]bool{}
	for _, t := range store.DumpTables() {
		known[PartName(t.Name)] = true
	}
	var manifest []byte
	var unknown []string
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%s ist kein lesbarer Export: %w", path, err)
		}
		name := h.Name
		switch {
		case name == FilesDir || strings.HasPrefix(name, FilesDir):
			continue
		case h.Typeflag != tar.TypeReg:
			unknown = append(unknown, name)
		case name == ManifestName:
			if manifest != nil {
				return nil, fmt.Errorf("%s steht zweimal im Export", name)
			}
			if manifest, err = io.ReadAll(io.LimitReader(tr, maxManifest+1)); err != nil {
				return nil, err
			}
			if len(manifest) > maxManifest {
				return nil, fmt.Errorf("%s ist größer als %d Bytes", name, maxManifest)
			}
		case known[name]:
			table := strings.TrimSuffix(name, partSuffix)
			if _, dup := a.parts[table]; dup {
				return nil, fmt.Errorf("%s steht zweimal im Export", name)
			}
			p := filepath.Join(dir, name)
			if err := spool(p, tr); err != nil {
				return nil, err
			}
			a.parts[table] = p
		default:
			unknown = append(unknown, name)
		}
	}
	if manifest == nil {
		return nil, fmt.Errorf("%s: %s fehlt — kein Export von kephalaion hub export", path, ManifestName)
	}
	if err := json.Unmarshal(manifest, &a.Manifest); err != nil {
		return nil, fmt.Errorf("%s: %w", ManifestName, err)
	}
	if a.Manifest.Format != Format {
		return nil, fmt.Errorf("Format-Fassung %d des Exports ist unbekannt; dieses Binary liest Fassung %d",
			a.Manifest.Format, Format)
	}
	if len(unknown) > 0 {
		return nil, fmt.Errorf("unbekannter Teil im Export: %s", strings.Join(unknown, ", "))
	}
	for _, t := range store.DumpTables() {
		if _, ok := a.parts[t.Name]; !ok {
			return nil, fmt.Errorf("%s fehlt im Export", PartName(t.Name))
		}
		if _, ok := a.Manifest.Rows[t.Name]; !ok {
			return nil, fmt.Errorf("%s: keine Zahl der Zeilen für %s", ManifestName, t.Name)
		}
	}
	if len(a.Manifest.Rows) != len(a.parts) {
		return nil, fmt.Errorf("%s zählt Tabellen, die es im Export nicht gibt", ManifestName)
	}
	return a, nil
}

// spool schreibt r in eine neue Datei.
func spool(path string, r io.Reader) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// Rows liest den Teil einer Tabelle und gibt jede Zeile an fn, values in der
// Reihenfolge der Spalten: string, int64 oder nil. Jede Zeile muss genau die
// Spalten der Tabelle tragen, mit dem Typ der Spalte; NULL nur, wo die
// Spalte es erlaubt. Am Ende muss die Zahl der Zeilen die des Manifests sein.
func (a *Archive) Rows(t store.DumpTable, fn func(values []any) error) error {
	path, ok := a.parts[t.Name]
	if !ok {
		return fmt.Errorf("%s fehlt im Export", PartName(t.Name))
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	part := PartName(t.Name)
	dec := json.NewDecoder(bufio.NewReader(f))
	dec.UseNumber()
	var n int64
	for {
		var row map[string]any
		if err := dec.Decode(&row); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return fmt.Errorf("%s, Zeile %d: %w", part, n+1, err)
		}
		n++
		values, err := convert(t, row)
		if err != nil {
			return fmt.Errorf("%s, Zeile %d: %w", part, n, err)
		}
		if err := fn(values); err != nil {
			return err
		}
	}
	if want := a.Manifest.Rows[t.Name]; n != want {
		return fmt.Errorf("%s: %d Zeilen, das Manifest nennt %d", part, n, want)
	}
	return nil
}

// convert macht aus einer gelesenen Zeile die Werte in der Reihenfolge der
// Spalten.
func convert(t store.DumpTable, row map[string]any) ([]any, error) {
	if row == nil {
		return nil, errors.New("kein Objekt")
	}
	if len(row) != len(t.Columns) {
		return nil, fmt.Errorf("%d Spalten, erwartet %d", len(row), len(t.Columns))
	}
	values := make([]any, len(t.Columns))
	for i, c := range t.Columns {
		v, ok := row[c.Name]
		if !ok {
			return nil, fmt.Errorf("Spalte %s fehlt", c.Name)
		}
		switch v := v.(type) {
		case nil:
			if !c.Null {
				return nil, fmt.Errorf("%s ist null", c.Name)
			}
		case json.Number:
			if !c.Int {
				return nil, fmt.Errorf("%s: Zahl statt Text", c.Name)
			}
			x, err := strconv.ParseInt(v.String(), 10, 64)
			if err != nil {
				return nil, fmt.Errorf("%s: %s ist keine Ganzzahl", c.Name, v)
			}
			values[i] = x
		case string:
			if c.Int {
				return nil, fmt.Errorf("%s: Text statt Zahl", c.Name)
			}
			values[i] = v
		default:
			return nil, fmt.Errorf("%s: Wert vom Typ %T", c.Name, v)
		}
	}
	return values, nil
}

// Close räumt die Arbeitsdateien ab.
func (a *Archive) Close() error { return os.RemoveAll(a.dir) }
