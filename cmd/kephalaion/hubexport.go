package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/kephalaion/kephalaion/internal/buildinfo"
	"github.com/kephalaion/kephalaion/internal/config"
	"github.com/kephalaion/kephalaion/internal/hub/archive"
	hubstore "github.com/kephalaion/kephalaion/internal/hub/store"
	"github.com/kephalaion/kephalaion/internal/sqlitedb"
)

// hub export (Task 039): der vollständige Inhalt der Hub-Datenbank als
// Archiv, unabhängig von Datenbank und Schemafassung (internal/hub/archive).

const hubExportUsage = `Aufruf:
  kephalaion hub export --output datei [--config pfad]

Schreibt den vollständigen Inhalt der Hub-Datenbank in ein Archiv (tar.gz),
das nicht an der Datenbank hängt: Dokumente samt Versionsgeschichte und
Löschmarken, Protokoll (actions), Collections, Nodes, Accounts mit Rechten,
settings, dazu Revision und hub_id. Zurück in einen frisch angelegten Hub
bringt es kephalaion hub restore.

Im Archiv, ohne kephalaion lesbar:
  manifest.json              Format-Fassung, Schemafassung, Revision, hub_id,
                             serve_stopped, Zeitpunkt, Binary, Zeilen je Teil
  <tabelle>.jsonl            je Tabelle eine Zeile JSON je Datenbankzeile
  files/<collection>/<name>  der lebende Inhalt jedes Dokuments, nur zum Lesen

Gelesen wird in einer einzigen Lesetransaktion: ein Stand, eine Revision,
ohne dass ein Schreiber wartet. Zuerst versucht export, ohne zu warten, die
Sperre von serve zu nehmen (<db>.lock). Gelingt das, hält es sie bis zum Ende
und vermerkt serve_stopped: true — der Export ist dann der letzte Stand,
solange gegen diese Datenbank nichts mehr schreibt (Umzug, neue
Schemafassung; nur ein solcher Export erlaubt hub restore --keep-hub-id).
Sonst läuft er neben serve, mit serve_stopped: false.

Das Archiv enthält alle Inhalte und die Hashes der Tokens; es entsteht mit
0600, erst als verdeckte Datei neben dem Ziel, dann umbenannt. In der
globalen Installation als Systembenutzer aufrufen
(sudo -u kephalaion kephalaion hub export …) — als root angelegt, gehörte die
Sperrdatei root, und serve startete nicht mehr.

Nicht zu verwechseln mit kephalaion config export (config, settings und
lokale Tabellen ohne Inhalte, samt Tokens der Node-Einträge) und mit
kephalaion hub backup (Kopie der SQLite-Datei).

Optionen:
  --output datei  Ziel des Archivs (Pflicht); eine vorhandene Datei wird
                  ersetzt
  --config pfad   Ort der config (siehe kephalaion hub init --help)
`

// exportNow liefert den Zeitpunkt für das Manifest; Tests setzen es.
var exportNow = time.Now

// runHubExport ist hub export.
func runHubExport(args []string, stdout, stderr io.Writer) int {
	c := newCommand("hub export", hubExportUsage, stdout, stderr)
	output := c.fs.String("output", "", "")
	if _, code, ok := c.parse(args); !ok {
		return code
	}
	if *output == "" {
		fmt.Fprint(stderr, "hub export: --output fehlt\n\n")
		fmt.Fprint(stderr, hubExportUsage)
		return 2
	}
	cfg, err := c.loadConfig()
	if err != nil {
		return c.fail(err)
	}
	addr, err := hubDBOf(cfg)
	if err != nil {
		return c.fail(err)
	}
	if addr.Kind != config.SQLite {
		return c.fail(fmt.Errorf("Datenbankart %q: %w", addr.Kind, config.ErrUnsupported))
	}
	// Vor der Sperre: Ohne Datenbank bliebe sonst nur eine Sperrdatei zurück.
	if _, err := os.Stat(addr.Path); errors.Is(err, fs.ErrNotExist) {
		return c.fail(fmt.Errorf("%w: %s", sqlitedb.ErrNotFound, addr.Path))
	} else if err != nil {
		return c.fail(err)
	}
	out, err := filepath.Abs(*output)
	if err != nil {
		return c.fail(err)
	}
	// Die Sperre von serve, ohne zu warten. Gelingt sie nicht — serve läuft,
	// oder die Sperrdatei ist nicht zu öffnen —, ist der Export einer aus dem
	// laufenden Betrieb: serve_stopped false, die sichere Richtung.
	stopped := false
	if lock, err := takeLock(lockPath(addr.Path)); err == nil {
		defer lock.Close()
		stopped = true
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	s, err := hubstore.Open(ctx, addr)
	if err != nil {
		return c.fail(err)
	}
	defer s.Close()
	m, size, err := exportHub(ctx, s, out, stopped)
	if err != nil {
		return c.fail(err)
	}
	var rows int64
	for _, n := range m.Rows {
		rows += n
	}
	how := "neben laufendem serve (serve_stopped: false)"
	if stopped {
		how = "bei angehaltenem serve (serve_stopped: true)"
	}
	fmt.Fprintf(stdout, "Export: %s (%d Bytes, Revision %d, hub_id %s, %d Zeilen, %d Dateien), %s.\n",
		out, size, m.Revision, m.HubID, rows, m.Files, how)
	return 0
}

// exportHub liest den Abzug und schreibt das Archiv nach out: erst eine
// verdeckte Datei 0600 im selben Verzeichnis, dann rename. Scheitert etwas,
// bleibt out unverändert und nichts liegt herum.
func exportHub(ctx context.Context, s hubstore.Store, out string, stopped bool) (m archive.Manifest, size int64, err error) {
	dir := filepath.Dir(out)
	w, err := archive.NewWriter(dir)
	if err != nil {
		return archive.Manifest{}, 0, err
	}
	defer func() { _ = w.Close() }()
	info, err := s.Dump(ctx, w.Row)
	if err != nil {
		return archive.Manifest{}, 0, err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(out)+".tmp-*")
	if err != nil {
		return archive.Manifest{}, 0, err
	}
	tmpName := tmp.Name()
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
		}
	}()
	if err = tmp.Chmod(0o600); err != nil {
		return archive.Manifest{}, 0, err
	}
	m, err = w.WriteTo(tmp, archive.Manifest{
		SchemaVersion: info.SchemaVersion,
		Revision:      info.Revision,
		HubID:         info.HubID,
		ServeStopped:  stopped,
		CreatedAt:     exportNow().UTC().Format(time.RFC3339),
		Version:       buildinfo.Get().Version,
	})
	if err != nil {
		return archive.Manifest{}, 0, err
	}
	if err = tmp.Sync(); err != nil {
		return archive.Manifest{}, 0, err
	}
	fi, err := tmp.Stat()
	if err != nil {
		return archive.Manifest{}, 0, err
	}
	if err = tmp.Close(); err != nil {
		return archive.Manifest{}, 0, err
	}
	if err = os.Rename(tmpName, out); err != nil {
		return archive.Manifest{}, 0, err
	}
	// Der neue Name soll einen Stromausfall überstehen.
	if err := syncDir(dir); err != nil {
		return m, fi.Size(), err
	}
	return m, fi.Size(), nil
}
