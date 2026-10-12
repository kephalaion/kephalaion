// Package source ist die Quelle einer Collection (Task 031): die Liste der
// Quellen, die das Binary trägt, und das Holen des neuesten Releases aus
// einem öffentlichen GitHub-Repo. Es gehört zum Hub und kennt weder den
// Hub-Store noch den Node; der Hub-Store setzt Target um.
package source

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"net/url"
	"path"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/kephalaion/kephalaion/internal/ident"
)

// DefaultsDir ist das Verzeichnis an der Wurzel einer Collection mit Quelle,
// in dem das geholte Release steht — ohne / am Ende.
const DefaultsDir = "defaults"

// Author ist der Urheber der Dokumente, die fetch schreibt
// (created_by/updated_by). Kein User kann so heißen: Die Namensregel
// verbietet den Präfix system.
const Author = "system"

// MaxBytesLimit ist die höchste Obergrenze, die ein Eintrag der Liste nennen
// darf: Der Hub hält Archiv und Inhalt im Speicher.
const MaxBytesLimit = 64 << 20

//go:embed sources.yaml
var embedded []byte

// Entry ist ein Eintrag der Liste: die festen Vorgaben einer Collection mit
// Quelle.
type Entry struct {
	// Name ist der Name der Collection.
	Name string
	// Public ist immer true: lesen darf jeder Account des Hubs.
	Public bool
	// Git ist die URL des Repos, https://github.com/<owner>/<repo>.
	Git string
	// Repo ist <owner>/<repo> aus Git.
	Repo string
	// Asset ist der Name des Archivs im Release.
	Asset string
	// MaxBytes ist die Obergrenze für das Archiv und für den entpackten
	// Inhalt.
	MaxBytes int64
}

// rawEntry ist ein Eintrag, wie er in der YAML steht.
type rawEntry struct {
	Public *bool `yaml:"public"`
	Source *struct {
		Git   string `yaml:"git"`
		Asset string `yaml:"asset"`
	} `yaml:"source"`
	MaxBytes int64 `yaml:"max_bytes"`
}

// list ist die Liste aus sources.yaml, gelesen beim Start. Ein Fehler darin
// ist ein Fehler des Baus; der Test des Pakets fängt ihn.
var list = mustParse(embedded)

func mustParse(data []byte) map[string]Entry {
	m, err := parse(data)
	if err != nil {
		panic("internal/hub/source/sources.yaml: " + err.Error())
	}
	return m
}

// parse liest die Liste strikt: unbekannte Schlüssel, doppelte Namen, ein
// Name gegen die Namensregel, eine Quelle, die kein GitHub-Repo ist, ein
// Asset mit Pfad und eine Obergrenze außerhalb von 1 bis MaxBytesLimit sind
// Fehler.
func parse(data []byte) (map[string]Entry, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	raw := map[string]rawEntry{}
	if err := dec.Decode(&raw); err != nil {
		return nil, err
	}
	out := make(map[string]Entry, len(raw))
	for name, r := range raw {
		if err := ident.CheckName("Collection", name); err != nil {
			return nil, err
		}
		if r.Public == nil || !*r.Public {
			return nil, fmt.Errorf("%s: public muss true sein — eine Collection mit Quelle liest jeder Account des Hubs", name)
		}
		if r.Source == nil {
			return nil, fmt.Errorf("%s: source fehlt", name)
		}
		repo, err := githubRepo(r.Source.Git)
		if err != nil {
			return nil, fmt.Errorf("%s: source.git: %w", name, err)
		}
		a := r.Source.Asset
		if a == "" || a != path.Base(a) || strings.ContainsAny(a, "/\\?#%") || a == "." || a == ".." {
			return nil, fmt.Errorf("%s: source.asset %q ist kein Dateiname", name, a)
		}
		if r.MaxBytes < 1 || r.MaxBytes > MaxBytesLimit {
			return nil, fmt.Errorf("%s: max_bytes %d, erwartet 1 bis %d", name, r.MaxBytes, MaxBytesLimit)
		}
		out[name] = Entry{Name: name, Public: true, Git: r.Source.Git, Repo: repo, Asset: a, MaxBytes: r.MaxBytes}
	}
	return out, nil
}

// githubRepo prüft eine URL der Form https://github.com/<owner>/<repo> und
// liefert <owner>/<repo>.
func githubRepo(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if u.Scheme != "https" || u.Host != "github.com" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("%q: erwartet https://github.com/<owner>/<repo>", raw)
	}
	parts := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
	if len(parts) != 2 {
		return "", fmt.Errorf("%q: erwartet https://github.com/<owner>/<repo>", raw)
	}
	for _, p := range parts {
		if p == "" || p == "." || p == ".." || strings.ContainsAny(p, "%?#") {
			return "", errors.New("ungültiger Teil des Pfads: " + raw)
		}
	}
	return parts[0] + "/" + parts[1], nil
}

// Lookup liefert den Eintrag der Liste zu einer Collection; ok ist false,
// wenn sie nicht in der Liste steht — dann hat sie keine Quelle.
func Lookup(name string) (Entry, bool) {
	e, ok := list[name]
	return e, ok
}

// Names sind die Namen aller Einträge, sortiert.
func Names() []string {
	out := make([]string, 0, len(list))
	for n := range list {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
