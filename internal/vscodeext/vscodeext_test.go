package vscodeext

import (
	"archive/zip"
	"bytes"
	"testing"
	"testing/fstest"
)

// testVSIX baut eine .vsix mit den angegebenen Dateien.
func testVSIX(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// Mit und ohne eingebettete Datei — über ein eigenes Dateisystem, nie über
// den tatsächlichen Inhalt des Embeds.
func TestFromFS(t *testing.T) {
	if _, ok := fromFS(fstest.MapFS{"vsix/README.md": {Data: []byte("Platzhalter")}}); ok {
		t.Error("ohne .vsix: ok")
	}
	if _, ok := fromFS(fstest.MapFS{vsixPath: {Data: nil}}); ok {
		t.Error("leere .vsix: ok")
	}
	data, ok := fromFS(fstest.MapFS{vsixPath: {Data: []byte("PK")}})
	if !ok || string(data) != "PK" {
		t.Errorf("mit .vsix: %q, %v", data, ok)
	}
}

func TestVersion(t *testing.T) {
	v, err := Version(testVSIX(t, map[string]string{
		"extension.vsixmanifest": "<x/>",
		"extension/package.json": `{"name":"kephalaion","version":"0.3.0"}`,
	}))
	if err != nil || v != "0.3.0" {
		t.Errorf("Version = %q, %v", v, err)
	}
	for name, data := range map[string][]byte{
		"kein zip":         []byte("nein"),
		"ohne package":     testVSIX(t, map[string]string{"extension/x": "y"}),
		"ohne version":     testVSIX(t, map[string]string{"extension/package.json": `{"name":"k"}`}),
		"kaputtes package": testVSIX(t, map[string]string{"extension/package.json": `{`}),
	} {
		if v, err := Version(data); err == nil {
			t.Errorf("%s: Version = %q ohne Fehler", name, v)
		}
	}
}
