package source_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kephalaion/kephalaion/internal/hub/source"
	"github.com/kephalaion/kephalaion/internal/hub/source/sourcetest"
)

func entry() source.Entry {
	e, _ := source.Lookup("k-playbook")
	return e
}

func client(base string) *source.Client {
	c := source.NewClient("test")
	c.Base = base
	return c
}

// Den Tag liest Latest aus der Weiterleitung; ohne Release, bei 404 und
// ohne Weiterleitung scheitert es mit der passenden Art.
func TestLatest(t *testing.T) {
	gh := sourcetest.New("kascada/k-playbook", "k-playbook-defaults.tar.gz")
	defer gh.Close()
	c := client(gh.URL)
	ctx := context.Background()
	if _, err := c.Latest(ctx, entry()); !errors.Is(err, source.ErrMissing) {
		t.Errorf("ohne Release: %v", err)
	}
	gh.Release("v1.2.0", nil)
	if tag, err := c.Latest(ctx, entry()); err != nil || tag != "v1.2.0" {
		t.Errorf("Tag %q, %v", tag, err)
	}
	cases := map[string]struct {
		h    http.HandlerFunc
		kind error
	}{
		"404": {func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }, source.ErrMissing},
		"200": {func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("hallo")) }, source.ErrUnreachable},
		"500": {func(w http.ResponseWriter, r *http.Request) { http.Error(w, "x", 500) }, source.ErrUnreachable},
		"Tag leer": {func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/kascada/k-playbook/releases/tag/", http.StatusFound)
		}, source.ErrUnreachable},
		"Tag mit Leerzeichen": {func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/kascada/k-playbook/releases/tag/v1%20x", http.StatusFound)
		}, source.ErrUnreachable},
	}
	for name, cs := range cases {
		srv := httptest.NewServer(cs.h)
		if _, err := client(srv.URL).Latest(ctx, entry()); !errors.Is(err, cs.kind) {
			t.Errorf("%s: %v", name, err)
		}
		srv.Close()
	}
	dead := httptest.NewServer(http.NotFoundHandler())
	dead.Close()
	if _, err := client(dead.URL).Latest(ctx, entry()); !errors.Is(err, source.ErrUnreachable) {
		t.Errorf("nicht erreichbar: %v", err)
	}
}

// Download folgt der Weiterleitung auf den Speicher; ein fehlendes Asset ist
// ErrMissing, ein zu großes ErrInvalid.
func TestDownload(t *testing.T) {
	gh := sourcetest.New("kascada/k-playbook", "k-playbook-defaults.tar.gz")
	defer gh.Close()
	c := client(gh.URL)
	ctx := context.Background()
	gh.Release("v1", sourcetest.Archive(map[string]string{"README.md": "# k\n", "rules/a.md": "a"}))
	files, err := c.Download(ctx, entry(), "v1")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || files[0] != (source.File{Name: "README.md", Content: "# k\n"}) || files[1].Name != "rules/a.md" {
		t.Errorf("Dateien: %+v", files)
	}
	gh.Release("v2", nil)
	if _, err := c.Download(ctx, entry(), "v2"); !errors.Is(err, source.ErrMissing) {
		t.Errorf("ohne Asset: %v", err)
	}
	e := entry()
	e.MaxBytes = 10
	gh.Release("v3", sourcetest.Archive(map[string]string{"a.md": strings.Repeat("x", 100)}))
	if _, err := c.Download(ctx, e, "v3"); !errors.Is(err, source.ErrInvalid) || !strings.Contains(err.Error(), "größer als 10") {
		t.Errorf("zu groß: %v", err)
	}
}

// tarGz baut ein Archiv aus beliebigen Köpfen.
func tarGz(t *testing.T, entries ...tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		e.hdr.Size = int64(len(e.body))
		if err := tw.WriteHeader(&e.hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

type tarEntry struct {
	hdr  tar.Header
	body string
}

func file(name, body string) tarEntry {
	return tarEntry{hdr: tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o755}, body: body}
}

// Unpack nimmt nur Dateien und Verzeichnisse, prüft Namen und Inhalte und
// lässt bei jedem Fehler das ganze Archiv scheitern.
func TestUnpack(t *testing.T) {
	ok := tarGz(t,
		tarEntry{hdr: tar.Header{Name: "./", Typeflag: tar.TypeDir}},
		tarEntry{hdr: tar.Header{Name: "./leer/", Typeflag: tar.TypeDir}},
		tarEntry{hdr: tar.Header{Typeflag: tar.TypeXGlobalHeader, PAXRecords: map[string]string{"comment": "x"}}},
		file("./checks/run.sh", "#!/bin/sh\n"),
		file("README.md", "ä"),
	)
	files, err := source.Unpack(ok, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || files[0].Name != "README.md" || files[1].Name != "checks/run.sh" {
		t.Errorf("Dateien: %+v", files)
	}
	big := strings.Repeat("x", 1<<20+1)
	cases := map[string][]byte{
		"Symlink":               tarGz(t, file("a.md", "a"), tarEntry{hdr: tar.Header{Name: "l", Typeflag: tar.TypeSymlink, Linkname: "a.md"}}),
		"Hardlink":              tarGz(t, file("a.md", "a"), tarEntry{hdr: tar.Header{Name: "h", Typeflag: tar.TypeLink, Linkname: "a.md"}}),
		"Gerät":                 tarGz(t, tarEntry{hdr: tar.Header{Name: "dev", Typeflag: tar.TypeChar}}),
		"FIFO":                  tarGz(t, tarEntry{hdr: tar.Header{Name: "fifo", Typeflag: tar.TypeFifo}}),
		"kein UTF-8":            tarGz(t, file("a.md", "\xff")),
		"NUL":                   tarGz(t, file("a.md", "a\x00b")),
		"über 1 MiB":            tarGz(t, file("a.md", big)),
		"Name ..":               tarGz(t, file("../a.md", "a")),
		"absolut":               tarGz(t, file("/a.md", "a")),
		"doppelt":               tarGz(t, file("a.md", "a"), file("./a.md", "b")),
		"Datei und Verzeichnis": tarGz(t, file("a", "a"), file("a/b.md", "b")),
		"kein gzip":             []byte("kein archiv"),
	}
	for name, data := range cases {
		if _, err := source.Unpack(data, 8<<20); !errors.Is(err, source.ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := source.Unpack(tarGz(t, file("a.md", "123456"), file("b.md", "123456")), 10); !errors.Is(err, source.ErrInvalid) {
		t.Errorf("Summe über max: %v", err)
	}
}

// target ist eine Attrappe von source.Target.
type target struct {
	tag      string
	replaced []string
	err      error
}

func (f *target) FetchedTag(context.Context, string) (string, error) { return f.tag, f.err }
func (f *target) ReplaceDefaults(_ context.Context, _ string, tag string, files []source.File, _ source.Actor) (source.Result, error) {
	f.replaced = append(f.replaced, tag)
	f.tag = tag
	return source.Result{Tag: tag, Created: len(files)}, nil
}

// Fetch: keine Quelle, schon aktuell (ohne Laden), geholt; die Zeitgrenze
// gilt für Tag und Laden zusammen.
func TestFetch(t *testing.T) {
	gh := sourcetest.New("kascada/k-playbook", "k-playbook-defaults.tar.gz")
	defer gh.Close()
	c := client(gh.URL)
	ctx := context.Background()
	tg := &target{}
	if _, err := source.Fetch(ctx, c, tg, "team-x", source.Actor{}); !errors.Is(err, source.ErrNoSource) {
		t.Errorf("ohne Quelle: %v", err)
	}
	gh.Release("v1", sourcetest.Archive(map[string]string{"a.md": "a"}))
	res, err := source.Fetch(ctx, c, tg, "k-playbook", source.Actor{Account: "bob"})
	if err != nil || res.Current || res.Tag != "v1" || res.Created != 1 {
		t.Fatalf("geholt: %+v %v", res, err)
	}
	res, err = source.Fetch(ctx, c, tg, "k-playbook", source.Actor{})
	if err != nil || !res.Current || res.Tag != "v1" {
		t.Errorf("aktuell: %+v %v", res, err)
	}
	if latest, loads := gh.Calls(); latest != 2 || loads != 1 {
		t.Errorf("Anfragen: latest %d, Archiv %d", latest, loads)
	}
	gh.Release("v2", sourcetest.Archive(map[string]string{"a.md": "a"}))
	gh.Delay(300 * time.Millisecond)
	c.Timeout = 100 * time.Millisecond
	if _, err := source.Fetch(ctx, c, tg, "k-playbook", source.Actor{}); !errors.Is(err, source.ErrUnreachable) ||
		!strings.Contains(err.Error(), "Zeitgrenze") {
		t.Errorf("Zeitgrenze: %v", err)
	}
	if len(tg.replaced) != 1 {
		t.Errorf("geschrieben: %v", tg.replaced)
	}
}
