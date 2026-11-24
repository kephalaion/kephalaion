package main

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kephalaion/kephalaion/internal/hub/archive"
	hubstore "github.com/kephalaion/kephalaion/internal/hub/store"
)

// exportAt stellt die Uhr für das Manifest.
func exportAt(t *testing.T, at time.Time) {
	t.Helper()
	old := exportNow
	exportNow = func() time.Time { return at }
	t.Cleanup(func() { exportNow = old })
}

// readExport liest das Manifest eines Exports.
func readExport(t *testing.T, path string) archive.Manifest {
	t.Helper()
	a, err := archive.Open(path, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	return a.Manifest
}

var exportLine = regexp.MustCompile(`^Export: (\S+) \(([0-9]+) Bytes, Revision ([0-9]+), hub_id ([0-9A-Z]{26}), ` +
	`([0-9]+) Zeilen, ([0-9]+) Dateien\), (bei angehaltenem serve \(serve_stopped: true\)|neben laufendem serve \(serve_stopped: false\))\.\n$`)

// hub export schreibt das Archiv mit 0600 an --output, ohne Reste daneben,
// und meldet es in einer Zeile. Ist die Sperre von serve frei, hält export
// sie und vermerkt serve_stopped: true; hält serve sie, läuft der Export
// trotzdem, mit serve_stopped: false. Danach ist die Sperre wieder frei.
func TestHubExport(t *testing.T) {
	dir := isolate(t)
	_, c := backupSetup(t, dir)
	hubDB := filepath.Join(dir, "hub.db")
	putDoc(t, c, "a.md", "A\n")
	putDoc(t, c, "sub/b.md", "B\n")
	exportAt(t, time.Date(2026, 11, 24, 10, 0, 0, 0, time.FixedZone("CET", 3600)))
	info := hubInfoOf(t, hubDB)

	outDir := filepath.Join(dir, "exporte")
	if err := os.Mkdir(outDir, 0o700); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(outDir, "hub.tar.gz")
	r := runT(t, "hub", "export", "--output", out, c)
	r.want(t, 0)
	m := exportLine.FindStringSubmatch(r.out)
	if m == nil || r.errOut != "" {
		t.Fatalf("Ausgabe:\n%s\nstderr:\n%s", r.out, r.errOut)
	}
	if m[1] != out || m[3] != "2" || m[4] != info.HubID || m[6] != "2" || !strings.HasPrefix(m[7], "bei angehaltenem") {
		t.Errorf("Ausgabe: %s", r.out)
	}
	fi, err := os.Stat(out)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 || m[2] != strconv.FormatInt(fi.Size(), 10) {
		t.Errorf("Modus %v, Größe %d, Ausgabe %s", fi.Mode().Perm(), fi.Size(), m[2])
	}
	if got := names(t, outDir); !slices.Equal(got, []string{"hub.tar.gz"}) {
		t.Errorf("neben dem Export liegt: %v", got)
	}
	man := readExport(t, out)
	if man.Format != archive.Format || !man.ServeStopped || man.Revision != info.Revision || man.HubID != info.HubID ||
		man.SchemaVersion != hubstore.SchemaVersion || man.CreatedAt != "2026-11-24T09:00:00Z" || man.Version == "" ||
		man.Files != 2 || man.Rows["nodes"] != 1 || man.Rows["documents"] != 2 {
		t.Errorf("Manifest %+v", man)
	}
	if running, err := serveRunning(hubDB); err != nil || running {
		t.Errorf("nach export: Sperre gehalten %v, %v", running, err)
	}

	// serve hält die Sperre: Der Export läuft, serve_stopped ist false. Die
	// vorhandene Datei wird ersetzt.
	lock, err := takeLock(lockPath(hubDB))
	if err != nil {
		t.Fatal(err)
	}
	putDoc(t, c, "c.md", "C\n")
	r = runT(t, "hub", "export", "--output", out, c)
	r.want(t, 0, "Revision 3,", "3 Dateien", "neben laufendem serve (serve_stopped: false)")
	if man := readExport(t, out); man.ServeStopped || man.Revision != 3 {
		t.Errorf("Manifest bei laufendem serve: %+v", man)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	if got := names(t, outDir); !slices.Equal(got, []string{"hub.tar.gz"}) {
		t.Errorf("neben dem Export liegt: %v", got)
	}

	// Relativ zum Arbeitsverzeichnis.
	t.Chdir(outDir)
	runT(t, "hub", "export", "--output", "rel.tar.gz", c).want(t, 0, "Export: "+filepath.Join(outDir, "rel.tar.gz")+" (")

	runT(t, "hub", "export", c).want(t, 2, "--output fehlt")
	runT(t, "hub", "export", "--output", out, "zuviel", c).want(t, 2, "Unerwartetes Argument: zuviel")
	runT(t, "hub", "export", "--output", filepath.Join(dir, "fehlt", "x.tar.gz"), c).want(t, 1, "Arbeitsverzeichnis")
	runT(t, "hub", "export", "--help").want(t, 0, "serve_stopped", "sudo -u kephalaion kephalaion hub export")
	runT(t, "hub", "--help").want(t, 0, "kephalaion hub export --output datei", "kephalaion hub export --help")

	// Ohne Hub in der config, ohne Datenbank: nichts angelegt.
	nodeOnly := filepath.Join(dir, "nur-node.yaml")
	runT(t, "node", "init", "--db", "sqlite://"+filepath.Join(dir, "n", "node.db"), "--config", nodeOnly).want(t, 0)
	runT(t, "hub", "export", "--output", out, "--config", nodeOnly).want(t, 1, "der Hub ist nicht eingerichtet")
	if err := os.Rename(hubDB, hubDB+".weg"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(lockPath(hubDB)); err != nil {
		t.Fatal(err)
	}
	runT(t, "hub", "export", "--output", out, c).want(t, 1, "Datenbankdatei fehlt")
	if _, err := os.Stat(lockPath(hubDB)); !os.IsNotExist(err) {
		t.Error("export ohne Datenbank hat eine Sperrdatei angelegt")
	}
}
