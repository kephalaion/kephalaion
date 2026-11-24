package archive

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/kephalaion/kephalaion/internal/hub/store"
)

// table liefert die Tabelle des Abzugs mit diesem Namen.
func table(t *testing.T, name string) store.DumpTable {
	t.Helper()
	for _, tb := range store.DumpTables() {
		if tb.Name == name {
			return tb
		}
	}
	t.Fatalf("keine Tabelle %s", name)
	return store.DumpTable{}
}

// sample sind Zeilen für einige Tabellen: ein lebendes Dokument, eine
// Löschmarke, eine SYSTEM:A:-Zeile, Text mit Zeichen, die JSON gern
// maskiert, und NULL.
func sample() map[string][][]any {
	return map[string][][]any{
		"collections": {{"wissen", nil, int64(0), nil, nil, int64(1700000000000), "admin"}},
		"documents": {
			{"01A", "wissen", "sub/a.md", "A <&> \"ä\"\n", nil, int64(0), int64(1), int64(1), "admin", int64(1), "admin"},
			{"01B", "wissen", "weg.md", nil, nil, int64(1), int64(3), int64(2), "admin", int64(3), "admin"},
			{"01C", "wissen", "SYSTEM:A:alice", "{}", nil, int64(0), int64(2), int64(2), "admin", int64(2), "admin"},
		},
		"actions":  {{int64(5), "admin", nil, "doc.put", "01A", nil, int64(1)}},
		"settings": {{"alias", "zentral"}},
	}
}

// writeArchive schreibt rows als Export nach dir/export.tar.gz.
func writeArchive(t *testing.T, dir string, rows map[string][][]any) (string, Manifest) {
	t.Helper()
	w, err := NewWriter(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	for _, tb := range store.DumpTables() {
		for _, r := range rows[tb.Name] {
			if err := w.Row(tb.Name, r); err != nil {
				t.Fatal(err)
			}
		}
	}
	path := filepath.Join(dir, "export.tar.gz")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	m, err := w.WriteTo(f, Manifest{SchemaVersion: store.SchemaVersion, Revision: 3, HubID: "01HUB",
		ServeStopped: true, CreatedAt: "2026-11-24T10:00:00Z", Version: "dev"})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path, m
}

// entries liest ein tar.gz in Name → Inhalt, in der Reihenfolge der Namen.
func entries(t *testing.T, path string) ([]string, map[string]string) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	var names []string
	out := map[string]string{}
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, h.Name)
		out[h.Name] = string(data)
	}
	return names, out
}

// Das Archiv ist ohne kephalaion lesbar: zuerst das Manifest, dann je
// Tabelle ein Teil in JSON Lines mit Spaltennamen, Zahlen als Zahlen, NULL
// als null, dann der Inhalt der lebenden Dokumente unter files/ — ohne
// Löschmarke und SYSTEM:-Zeile.
func TestWriterLayout(t *testing.T) {
	dir := t.TempDir()
	path, m := writeArchive(t, dir, sample())
	names, files := entries(t, path)

	want := []string{ManifestName}
	for _, tb := range store.DumpTables() {
		want = append(want, PartName(tb.Name))
	}
	want = append(want, "files/wissen/sub/a.md")
	if !slices.Equal(names, want) {
		t.Fatalf("Einträge %v, erwartet %v", names, want)
	}
	if got := files["files/wissen/sub/a.md"]; got != "A <&> \"ä\"\n" {
		t.Errorf("files/: %q", got)
	}

	var read Manifest
	if err := json.Unmarshal([]byte(files[ManifestName]), &read); err != nil {
		t.Fatal(err)
	}
	if read.Format != Format || read.Revision != 3 || read.HubID != "01HUB" || !read.ServeStopped ||
		read.SchemaVersion != store.SchemaVersion || read.CreatedAt != "2026-11-24T10:00:00Z" || read.Files != 1 {
		t.Errorf("Manifest %+v", read)
	}
	if read.Rows["documents"] != 3 || read.Rows["nodes"] != 0 || len(read.Rows) != len(store.DumpTables()) {
		t.Errorf("Zeilen im Manifest: %v", read.Rows)
	}
	if m.Rows["documents"] != 3 || m.Files != 1 || m.Format != Format {
		t.Errorf("geliefertes Manifest %+v", m)
	}

	wantLine := `{"id":"01A","collection":"wissen","name":"sub/a.md","content":"A <&> \"ä\"\n","meta":null,` +
		`"deleted":0,"revision":1,"created_at":1,"created_by":"admin","updated_at":1,"updated_by":"admin"}`
	if lines := strings.Split(files["documents.jsonl"], "\n"); len(lines) != 4 || lines[0] != wantLine || lines[3] != "" {
		t.Errorf("documents.jsonl:\n%s", files["documents.jsonl"])
	}
	if files["nodes.jsonl"] != "" {
		t.Errorf("nodes.jsonl: %q", files["nodes.jsonl"])
	}
	// Das Arbeitsverzeichnis ist nach Close weg; nur das Archiv bleibt.
	if got, _ := filepath.Glob(filepath.Join(dir, ".hub-export-*")); len(got) != 0 {
		t.Errorf("Arbeitsverzeichnis bleibt: %v", got)
	}
}

// Was Open und Rows lesen, ist, was Row bekam: dieselben Werte, dieselben
// Typen, dieselbe Zahl je Tabelle.
func TestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	rows := sample()
	path, _ := writeArchive(t, dir, rows)
	a, err := Open(path, dir)
	if err != nil {
		t.Fatal(err)
	}
	if a.Manifest.HubID != "01HUB" || !a.Manifest.ServeStopped {
		t.Errorf("Manifest %+v", a.Manifest)
	}
	for _, tb := range store.DumpTables() {
		var got [][]any
		if err := a.Rows(tb, func(values []any) error { got = append(got, values); return nil }); err != nil {
			t.Fatal(err)
		}
		if !slices.EqualFunc(got, rows[tb.Name], slices.Equal) {
			t.Errorf("%s: %v, erwartet %v", tb.Name, got, rows[tb.Name])
		}
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if got, _ := filepath.Glob(filepath.Join(dir, ".hub-restore-*")); len(got) != 0 {
		t.Errorf("Arbeitsverzeichnis bleibt: %v", got)
	}
}

// Row nimmt nur, was zur Spalte passt — und kein Text, den JSON nicht
// unverändert trägt.
func TestWriterRejects(t *testing.T) {
	w, err := NewWriter(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	for name, c := range map[string]struct {
		table  string
		values []any
		want   string
	}{
		"Tabelle":   {"requests", []any{"x"}, "unbekannte Tabelle requests"},
		"Anzahl":    {"settings", []any{"a"}, "1 Werte für 2 Spalten"},
		"NULL":      {"settings", []any{"a", nil}, "settings.value: NULL"},
		"Zahl":      {"settings", []any{"a", int64(1)}, "Zahl statt Text"},
		"Text":      {"actions", []any{"1", "admin", nil, "x", nil, nil, nil}, "Text statt Zahl"},
		"UTF-8":     {"settings", []any{"a", "\xff"}, "kein gültiges UTF-8"},
		"Typ":       {"settings", []any{"a", 1.5}, "Wert vom Typ float64"},
		"int statt": {"collections", []any{"c", nil, 0, nil, nil, int64(1), "a"}, "Wert vom Typ int"},
	} {
		err := w.Row(c.table, c.values)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, erwartet %q", name, err, c.want)
		}
	}
}

// rewrite schreibt ein Archiv mit den Einträgen in dieser Reihenfolge.
func rewrite(t *testing.T, path string, names []string, files map[string]string) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, n := range names {
		if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: n, Size: int64(len(files[n])), Mode: 0o600}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(files[n])); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

// Open und Rows lehnen ab, was kein stimmiger Export dieser Fassung ist —
// mit einer Meldung, die den Teil nennt. Die Reihenfolge der Einträge spielt
// keine Rolle.
func TestOpenRejects(t *testing.T) {
	dir := t.TempDir()
	path, _ := writeArchive(t, dir, sample())
	names, files := entries(t, path)
	bad := filepath.Join(dir, "bad.tar.gz")

	manifest := func(fn func(m map[string]any)) string {
		var m map[string]any
		if err := json.Unmarshal([]byte(files[ManifestName]), &m); err != nil {
			t.Fatal(err)
		}
		fn(m)
		data, _ := json.Marshal(m)
		return string(data)
	}
	without := func(name string) []string {
		return slices.DeleteFunc(slices.Clone(names), func(n string) bool { return n == name })
	}
	with := func(name, content string) map[string]string {
		out := map[string]string{}
		for k, v := range files {
			out[k] = v
		}
		out[name] = content
		return out
	}

	// Umgekehrte Reihenfolge geht.
	reversed := slices.Clone(names)
	slices.Reverse(reversed)
	rewrite(t, bad, reversed, files)
	a, err := Open(bad, dir)
	if err != nil {
		t.Fatalf("umgekehrte Reihenfolge: %v", err)
	}
	_ = a.Close()

	for name, c := range map[string]struct {
		names []string
		files map[string]string
		want  string
	}{
		"ohne Manifest": {without(ManifestName), files, "manifest.json fehlt"},
		"Fassung": {names, with(ManifestName, manifest(func(m map[string]any) { m["format"] = 2 })),
			"Format-Fassung 2 des Exports ist unbekannt"},
		"ohne Teil": {without("nodes.jsonl"), files, "nodes.jsonl fehlt im Export"},
		"unbekannt": {append(slices.Clone(names), "extra.jsonl"), with("extra.jsonl", ""), "unbekannter Teil im Export: extra.jsonl"},
		"doppelt":   {append(slices.Clone(names), "nodes.jsonl"), files, "nodes.jsonl steht zweimal"},
		"ohne Zahl": {names, with(ManifestName, manifest(func(m map[string]any) { delete(m["rows"].(map[string]any), "nodes") })),
			"keine Zahl der Zeilen für nodes"},
		"Zahl zuviel": {names, with(ManifestName, manifest(func(m map[string]any) { m["rows"].(map[string]any)["requests"] = 1 })),
			"zählt Tabellen, die es im Export nicht gibt"},
	} {
		rewrite(t, bad, c.names, c.files)
		a, err := Open(bad, dir)
		if err == nil {
			_ = a.Close()
		}
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, erwartet %q", name, err, c.want)
		}
	}
	if err := os.WriteFile(bad, []byte("kein gzip"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(bad, dir); err == nil || !strings.Contains(err.Error(), "kein Export von kephalaion hub export") {
		t.Errorf("kein gzip: %v", err)
	}

	settings := table(t, "settings")
	for name, c := range map[string]struct {
		part, want string
	}{
		"Zahl":        {`{"key":"a","value":"b"}` + "\n" + `{"key":"c","value":"d"}`, "settings.jsonl: 2 Zeilen, das Manifest nennt 1"},
		"null":        {`{"key":"a","value":null}`, "Zeile 1: value ist null"},
		"Typ":         {`{"key":"a","value":1}`, "value: Zahl statt Text"},
		"fehlt":       {`{"key":"a","wert":"b"}`, "Spalte value fehlt"},
		"zuviel":      {`{"key":"a","value":"b","x":1}`, "3 Spalten, erwartet 2"},
		"kein JSON":   {`{"key":`, "settings.jsonl, Zeile 1"},
		"kein Objekt": {`null`, "kein Objekt"},
		"Liste":       {`{"key":"a","value":["b"]}`, "Wert vom Typ []interface {}"},
	} {
		rewrite(t, bad, names, with("settings.jsonl", c.part))
		a, err := Open(bad, dir)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		err = a.Rows(settings, func([]any) error { return nil })
		_ = a.Close()
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, erwartet %q", name, err, c.want)
		}
	}
	// Ganzzahlen: keine Kommazahl.
	actions := table(t, "actions")
	rewrite(t, bad, names, with("actions.jsonl",
		`{"at":1.5,"account":"admin","carrier":null,"action":"x","document_id":null,"subject":null,"revision":null}`))
	a, err = Open(bad, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if err := a.Rows(actions, func([]any) error { return nil }); err == nil || !strings.Contains(err.Error(), "at: 1.5 ist keine Ganzzahl") {
		t.Errorf("Kommazahl: %v", err)
	}
}
