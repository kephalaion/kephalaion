package source

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/kephalaion/kephalaion/internal/contract"
	"github.com/kephalaion/kephalaion/internal/ident"
)

// Fehlerarten beim Holen, für errors.Is; die Meldungen nennen den Grund.
// Holen ändert in allen Fällen nichts.
var (
	// ErrNoSource: die Collection steht nicht in der Liste der Quellen.
	ErrNoSource = errors.New("die Collection hat keine Quelle")
	// ErrMissing: an der Quelle nicht vorhanden — kein Release, oder dem
	// neuesten fehlt das Asset.
	ErrMissing = errors.New("an der Quelle nicht vorhanden")
	// ErrUnreachable: die Quelle ist nicht erreichbar, antwortet
	// unbrauchbar oder nicht innerhalb der Zeitgrenze.
	ErrUnreachable = errors.New("Quelle nicht erreichbar")
	// ErrInvalid: das Archiv lässt sich nicht übernehmen.
	ErrInvalid = errors.New("Archiv nicht übernehmbar")
)

// kindError trägt eine eigene Meldung und eine Fehlerart.
type kindError struct {
	kind error
	msg  string
}

func (e *kindError) Error() string { return e.msg }
func (e *kindError) Unwrap() error { return e.kind }

func errorf(kind error, format string, args ...any) error {
	return &kindError{kind: kind, msg: fmt.Sprintf(format, args...)}
}

// DefaultBase ist die Adresse von GitHub, gegen die der Hub holt.
const DefaultBase = "https://github.com"

// DefaultTimeout ist die Zeitgrenze für Lesen des Tags, Laden und Entpacken
// zusammen — unter den 30 s, die der Client des Vertrags auf fetch wartet.
const DefaultTimeout = 20 * time.Second

// maxEntries begrenzt die Einträge eines Archivs; maxTagLen die Länge eines
// Tags.
const (
	maxEntries = 20000
	maxTagLen  = 200
)

// Client holt Releases von GitHub — ohne API, ohne Token: den Tag aus der
// Weiterleitung von releases/latest, das Archiv aus releases/download. Ein
// Proxy kommt aus der Umgebung (HTTPS_PROXY, NO_PROXY).
type Client struct {
	// Base ist die Adresse von GitHub; Tests setzen einen httptest-Server.
	// Keine Nutzeroption.
	Base string
	// HTTP ist der Client für beide Anfragen; für releases/latest folgt
	// Latest keiner Weiterleitung, das Archiv darf weitergeleitet werden
	// (GitHub leitet auf seinen Speicher).
	HTTP *http.Client
	// Timeout ist die Zeitgrenze für Lesen des Tags, Laden und Entpacken.
	Timeout time.Duration
	// UserAgent steht in jeder Anfrage.
	UserAgent string
}

// NewClient liefert einen Client gegen GitHub mit dem Proxy der Umgebung.
func NewClient(userAgent string) *Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = http.ProxyFromEnvironment
	return &Client{Base: DefaultBase, HTTP: &http.Client{Transport: tr}, Timeout: DefaultTimeout, UserAgent: userAgent}
}

func (c *Client) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return DefaultTimeout
}

func (c *Client) get(ctx context.Context, rawURL string, follow bool) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, errorf(ErrUnreachable, "Anfrage an %s: %v", rawURL, err)
	}
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	hc := *c.HTTP
	if !follow {
		hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, netError(ctx, req.URL, err)
	}
	return resp, nil
}

// netError übersetzt einen Fehler der Verbindung.
func netError(ctx context.Context, u *url.URL, err error) error {
	var ne net.Error
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded):
		return errorf(ErrUnreachable, "Quelle %s: Zeitgrenze überschritten", u.Host)
	case ctx.Err() != nil:
		return ctx.Err()
	case errors.As(err, &ne) && ne.Timeout():
		return errorf(ErrUnreachable, "Quelle %s: Zeitüberschreitung (%v)", u.Host, err)
	}
	return errorf(ErrUnreachable, "Quelle %s nicht erreichbar: %v", u.Host, err)
}

// Latest liest den Tag des neuesten Releases aus der Weiterleitung von
// https://github.com/<repo>/releases/latest auf …/releases/tag/<tag>. Ohne
// Release leitet GitHub auf die Liste der Releases oder antwortet 404:
// ErrMissing.
func (c *Client) Latest(ctx context.Context, e Entry) (string, error) {
	u := strings.TrimSuffix(c.Base, "/") + "/" + e.Repo + "/releases/latest"
	resp, err := c.get(ctx, u, false)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return "", errorf(ErrMissing, "%s hat kein Release", e.Git)
	case resp.StatusCode < 300 || resp.StatusCode > 399:
		return "", errorf(ErrUnreachable, "%s: releases/latest antwortet %s statt einer Weiterleitung", e.Git, resp.Status)
	}
	loc, err := resp.Location()
	if err != nil {
		return "", errorf(ErrUnreachable, "%s: Weiterleitung ohne Ziel", e.Git)
	}
	p := loc.EscapedPath()
	i := strings.LastIndex(p, "/releases/tag/")
	if i < 0 {
		return "", errorf(ErrMissing, "%s hat kein veröffentlichtes Release (Weiterleitung auf %s)", e.Git, loc.Path)
	}
	tag, err := url.PathUnescape(p[i+len("/releases/tag/"):])
	if err != nil || checkTag(tag) != nil {
		return "", errorf(ErrUnreachable, "%s: Weiterleitung auf %s nennt keinen gültigen Tag", e.Git, loc.Path)
	}
	return tag, nil
}

// checkTag prüft einen Tag: nicht leer, höchstens maxTagLen Zeichen, UTF-8,
// ohne Steuerzeichen, Leerraum und /.
func checkTag(tag string) error {
	if tag == "" || len(tag) > maxTagLen || !utf8.ValidString(tag) || strings.Contains(tag, "/") {
		return errors.New("ungültiger Tag")
	}
	for _, r := range tag {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return errors.New("ungültiger Tag")
		}
	}
	return nil
}

// Download lädt das Archiv des Releases tag
// (https://github.com/<repo>/releases/download/<tag>/<asset>) und entpackt
// es (Unpack). Größer als MaxBytes ist ErrInvalid, schon beim Laden.
func (c *Client) Download(ctx context.Context, e Entry, tag string) ([]File, error) {
	u := strings.TrimSuffix(c.Base, "/") + "/" + e.Repo + "/releases/download/" + url.PathEscape(tag) + "/" +
		url.PathEscape(e.Asset)
	resp, err := c.get(ctx, u, true)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, errorf(ErrMissing, "dem Release %s von %s fehlt das Asset %s", tag, e.Git, e.Asset)
	case resp.StatusCode != http.StatusOK:
		return nil, errorf(ErrUnreachable, "%s: Asset %s antwortet %s", e.Git, e.Asset, resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, e.MaxBytes+1))
	if err != nil {
		return nil, netError(ctx, resp.Request.URL, err)
	}
	if int64(len(data)) > e.MaxBytes {
		return nil, errorf(ErrInvalid, "Asset %s von %s ist größer als %d Bytes", e.Asset, tag, e.MaxBytes)
	}
	return Unpack(data, e.MaxBytes)
}

// File ist eine Datei aus dem Archiv: Name relativ zu defaults/, Inhalt.
type File struct {
	Name    string
	Content string
}

// Unpack entpackt ein tar.gz im Speicher. Nur Dateien und Verzeichnisse;
// jeder andere Eintrag (Link, Hardlink, Gerät, FIFO) lässt das ganze Archiv
// scheitern, ein globaler PAX-Kopf (nur Angaben, keine Datei) wird
// übergangen. Je Datei: Name nach ident.CheckDocName unter defaults/, kein
// Name doppelt, keiner zugleich Datei und Verzeichnis, Inhalt UTF-8 ohne
// NUL, höchstens contract.MaxDocumentBytes; alle Inhalte zusammen höchstens
// max. Modus-Bits, Zeiten und leere Verzeichnisse gehen nicht mit. Ein
// führendes ./ fällt weg. Das Ergebnis ist nach Name sortiert. Jeder Fehler
// ist ErrInvalid.
func Unpack(archive []byte, max int64) ([]File, error) {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, errorf(ErrInvalid, "kein gzip-Archiv: %v", err)
	}
	// Auch der entpackte Strom samt Köpfen bleibt begrenzt.
	stream := &io.LimitedReader{R: gz, N: 2*max + 1<<20}
	tr := tar.NewReader(stream)
	var files []File
	seen := map[string]bool{}
	var total int64
	for entries := 0; ; entries++ {
		if entries > maxEntries {
			return nil, errorf(ErrInvalid, "mehr als %d Einträge im Archiv", maxEntries)
		}
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			if stream.N <= 0 {
				return nil, errorf(ErrInvalid, "Archiv entpackt größer als erlaubt")
			}
			return nil, errorf(ErrInvalid, "kein gültiges tar-Archiv: %v", err)
		}
		name := hdr.Name
		for strings.HasPrefix(name, "./") {
			name = name[2:]
		}
		switch hdr.Typeflag {
		case tar.TypeXGlobalHeader:
			continue
		case tar.TypeDir:
			name = strings.TrimSuffix(name, "/")
			if name == "" || name == "." {
				continue
			}
			if err := ident.CheckDocName(DefaultsDir + "/" + name); err != nil {
				return nil, errorf(ErrInvalid, "Verzeichnis %q im Archiv: %v", hdr.Name, err)
			}
			continue
		case tar.TypeReg: // TypeRegA wandelt archive/tar selbst in TypeReg
		default:
			return nil, errorf(ErrInvalid, "Eintrag %q ist weder Datei noch Verzeichnis (Typ %q)", hdr.Name, hdr.Typeflag)
		}
		if err := ident.CheckDocName(DefaultsDir + "/" + name); err != nil {
			return nil, errorf(ErrInvalid, "Datei %q im Archiv: %v", hdr.Name, err)
		}
		if seen[name] {
			return nil, errorf(ErrInvalid, "Datei %s steht zweimal im Archiv", name)
		}
		seen[name] = true
		if hdr.Size > contract.MaxDocumentBytes {
			return nil, errorf(ErrInvalid, "Datei %s ist größer als 1 MiB (%d Bytes)", name, hdr.Size)
		}
		total += hdr.Size
		if total > max {
			return nil, errorf(ErrInvalid, "Inhalt des Archivs größer als %d Bytes", max)
		}
		data, err := io.ReadAll(io.LimitReader(tr, contract.MaxDocumentBytes+1))
		if err != nil {
			return nil, errorf(ErrInvalid, "Datei %s im Archiv: %v", name, err)
		}
		if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
			return nil, errorf(ErrInvalid, "Datei %s ist kein UTF-8-Text ohne NUL", name)
		}
		files = append(files, File{Name: name, Content: string(data)})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	for _, f := range files {
		for _, dir := range ident.DocAncestors(f.Name) {
			if seen[dir] {
				return nil, errorf(ErrInvalid, "%s ist im Archiv Datei und Verzeichnis zugleich", dir)
			}
		}
	}
	return files, nil
}

// Actor ist, wer ein Holen anstößt: der Account (bzw. admin an der CLI) und
// der Node, der die Anfrage trägt (leer an der CLI). Geschrieben wird als
// Author; Actor steht in actions.
type Actor struct {
	Account string
	Carrier string
}

// Result ist das Ergebnis eines Holens.
type Result struct {
	// Current: das neueste Release war schon geholt; nichts geändert.
	Current bool
	// Tag ist der Tag des Releases, das jetzt in defaults/ steht.
	Tag string
	// Revision ist die Revision des Schreibvorgangs; 0, wenn sich kein
	// Dokument geändert hat.
	Revision int64
	// Created, Updated und Deleted zählen die Dokumente.
	Created, Updated, Deleted int
}

// Target ist die Seite des Hubs, in die geholt wird — der Hub-Store.
type Target interface {
	// FetchedTag liefert den zuletzt geholten Tag einer Collection, leer
	// ohne; gibt es die Collection nicht, einen Fehler.
	FetchedTag(ctx context.Context, collection string) (string, error)
	// ReplaceDefaults ersetzt defaults/ der Collection durch files, in einer
	// Transaktion: zuerst die Zeile der Collection sperren, den Tag erneut
	// prüfen (gleich: Result.Current), dann löschen, ersetzen, anlegen, nur
	// was sich unterscheidet, als Author unter einer Revision; Tag und
	// Zeitpunkt merken.
	ReplaceDefaults(ctx context.Context, collection, tag string, files []File, by Actor) (Result, error)
}

// Fetch holt das neueste Release der Quelle einer Collection nach
// defaults/: Tag lesen, gegen den geholten vergleichen (gleich: Current),
// Archiv laden und entpacken, dann t.ReplaceDefaults. Lesen des Tags, Laden
// und Entpacken dürfen zusammen höchstens c.Timeout dauern; geschrieben
// wird unter ctx. Nur eine Collection aus der Liste hat eine Quelle
// (ErrNoSource).
func Fetch(ctx context.Context, c *Client, t Target, collection string, by Actor) (Result, error) {
	e, ok := Lookup(collection)
	if !ok {
		return Result{}, errorf(ErrNoSource, "Collection %s hat keine Quelle: sie steht nicht in der Liste der Quellen", collection)
	}
	fetched, err := t.FetchedTag(ctx, collection)
	if err != nil {
		return Result{}, err
	}
	nctx, cancel := context.WithTimeout(ctx, c.timeout())
	defer cancel()
	tag, err := c.Latest(nctx, e)
	if err != nil {
		return Result{}, err
	}
	if tag == fetched {
		return Result{Current: true, Tag: tag}, nil
	}
	files, err := c.Download(nctx, e, tag)
	if err != nil {
		return Result{}, err
	}
	cancel()
	return t.ReplaceDefaults(ctx, collection, tag, files, by)
}
