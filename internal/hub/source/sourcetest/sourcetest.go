// Package sourcetest spielt GitHub für das Holen nach (Task 031): nur für
// Tests. Kein Test fragt das echte GitHub.
package sourcetest

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"time"
)

// Archive baut ein tar.gz aus files (Name → Inhalt), mit ./ vor jedem Namen
// und den Verzeichnissen davor, wie tar -C defaults -cf - . es schreibt.
func Archive(files map[string]string) []byte {
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: "./", Typeflag: tar.TypeDir, Mode: 0o755})
	dirs := map[string]bool{}
	for _, n := range names {
		for i := range n {
			if n[i] == '/' && !dirs[n[:i]] {
				dirs[n[:i]] = true
				_ = tw.WriteHeader(&tar.Header{Name: "./" + n[:i] + "/", Typeflag: tar.TypeDir, Mode: 0o755})
			}
		}
		_ = tw.WriteHeader(&tar.Header{Name: "./" + n, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(files[n]))})
		_, _ = tw.Write([]byte(files[n]))
	}
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

// GitHub ist ein nachgespieltes GitHub für ein Repo: releases/latest leitet
// auf den Tag, releases/download/<tag>/<asset> leitet auf einen Speicher
// weiter, der das Archiv liefert. Ohne Tag gibt es kein Release (Weiterleitung
// auf die Liste); ohne Archiv fehlt das Asset (404).
type GitHub struct {
	*httptest.Server
	mu      sync.Mutex
	repo    string
	asset   string
	tag     string
	archive []byte
	delay   time.Duration
	latest  int
	loads   int
}

// New startet ein GitHub für repo (<owner>/<repo>) und das Asset asset.
func New(repo, asset string) *GitHub {
	g := &GitHub{repo: repo, asset: asset}
	g.Server = httptest.NewServer(http.HandlerFunc(g.serve))
	return g
}

// Release setzt das neueste Release: Tag und Archiv (nil: Asset fehlt).
func (g *GitHub) Release(tag string, archive []byte) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.tag, g.archive = tag, archive
}

// Delay lässt jede Antwort so lange warten.
func (g *GitHub) Delay(d time.Duration) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.delay = d
}

// Calls zählt die Anfragen an releases/latest und an das Archiv.
func (g *GitHub) Calls() (latest, loads int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.latest, g.loads
}

func (g *GitHub) serve(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	tag, archive, delay := g.tag, g.archive, g.delay
	g.mu.Unlock()
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			return
		}
	}
	base := "/" + g.repo
	switch {
	case r.URL.Path == base+"/releases/latest":
		g.mu.Lock()
		g.latest++
		g.mu.Unlock()
		if tag == "" {
			http.Redirect(w, r, base+"/releases", http.StatusFound)
			return
		}
		http.Redirect(w, r, base+"/releases/tag/"+tag, http.StatusFound)
	case tag != "" && r.URL.Path == base+"/releases/download/"+tag+"/"+g.asset:
		g.mu.Lock()
		g.loads++
		g.mu.Unlock()
		if archive == nil {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, "/storage/"+strings.ReplaceAll(g.asset, "/", "_"), http.StatusFound)
	case strings.HasPrefix(r.URL.Path, "/storage/"):
		_, _ = w.Write(archive)
	default:
		http.NotFound(w, r)
	}
}
