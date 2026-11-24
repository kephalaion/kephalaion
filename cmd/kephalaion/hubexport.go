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

	"github.com/oklog/ulid/v2"

	"github.com/kephalaion/kephalaion/internal/buildinfo"
	"github.com/kephalaion/kephalaion/internal/config"
	"github.com/kephalaion/kephalaion/internal/hub/archive"
	hubstore "github.com/kephalaion/kephalaion/internal/hub/store"
	"github.com/kephalaion/kephalaion/internal/sqlitedb"
)

// hub export und hub restore (Task 039): der vollständige Inhalt der
// Hub-Datenbank als Archiv, unabhängig von Datenbank und Schemafassung
// (internal/hub/archive), und der Weg zurück in einen frisch angelegten Hub.
// Der Ablauf von Umzug und Wiederherstellen steht in docs/installation.md.

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

const hubRestoreUsage = `Aufruf:
  kephalaion hub restore [--keep-hub-id] [--config pfad] <datei>

Spielt ein Archiv von kephalaion hub export in einen frisch angelegten, leeren
Hub ein: Dokumente samt Versionsgeschichte und Löschmarken, Protokoll
(actions), Collections, Nodes, Accounts mit Rechten, settings und die
Revision des Exports. In einer Transaktion — alles oder nichts. Darüber hinaus
schreibt restore nichts, auch keine Zeile ins Protokoll; files/ im Archiv
übergeht es.

Nur bei angehaltenem Dienst: restore nimmt dieselbe Sperre wie serve
(<db>.lock). Und nur in einen leeren Hub — keine Tabelle des Exports hat eine
Zeile; ein frisches hub init erfüllt das, sonst nennt die Meldung die belegte
Tabelle. Weil hub init abbricht, wenn die config schon einen Hub nennt,
bekommt der Ziel-Hub eine eigene config:

  kephalaion hub init --config <neue config> [--db sqlite:///pfad/hub.db]
  kephalaion hub restore --config <neue config> <datei>

Danach in der config des Dienstes im Abschnitt hub: nur db auf den Wert der
neuen config setzen; listen und ein Abschnitt node: bleiben. Ablauf:
docs/installation.md, „Umzug und Wiederherstellen über einen Export“.

hub_id: Ohne Option behält der Hub die neue hub_id aus hub init; jeder Node
leert beim nächsten Abgleich seine Replica und gleicht von vorn ab. Mit
--keep-hub-id übernimmt er die hub_id des Exports, und die Nodes gleichen ohne
Neuaufbau weiter ab — nur für einen Export bei angehaltenem serve
(serve_stopped: true), sonst lehnt restore ab. Zwischen diesem Export und dem
Umstellen der config darf gegen die alte Datenbank nichts mehr schreiben,
weder serve noch ein hub-Kommando.

Abgelehnt werden eine unbekannte Format-Fassung, eine Schemafassung, für die
dieses Binary keine Abbildung kennt, und Teile, deren Zeilen nicht zum
Manifest passen. Was nach dem Export geschah, kennt der Hub nicht — auch kein
Token, das danach rotiert wurde; der Verwalter gibt es neu aus.

In der globalen Installation als Systembenutzer aufrufen
(sudo -u kephalaion kephalaion hub restore …) — als root angelegt, gehörte die
Sperrdatei root, und serve startete nicht mehr.

Optionen:
  --keep-hub-id   die hub_id des Exports übernehmen (nur bei serve_stopped:
                  true)
  --config pfad   Ort der config (siehe kephalaion hub init --help)
`

// runHubRestore ist hub restore.
func runHubRestore(args []string, stdout, stderr io.Writer) int {
	c := newCommand("hub restore", hubRestoreUsage, stdout, stderr, "datei")
	keep := c.fs.Bool("keep-hub-id", false, "")
	pos, code, ok := c.parse(args)
	if !ok {
		return code
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
	if running, err := serveRunning(addr.Path); err != nil {
		return c.fail(err)
	} else if running {
		return c.fail(fmt.Errorf("serve läuft für den Hub (Sperre %s) — restore nur bei angehaltenem "+
			"Dienst; nichts geändert", lockPath(addr.Path)))
	}
	// Dieselbe Sperre wie serve, bis zum Ende: Ein serve, der jetzt startet,
	// scheitert daran, statt einen halb eingespielten Hub zu bedienen.
	lock, err := takeLock(lockPath(addr.Path))
	if err != nil {
		return c.fail(fmt.Errorf("%w; nichts geändert", err))
	}
	defer lock.Close()
	// Die Arbeitsdateien neben der Datenbank: Dort ist Platz für ihren
	// Inhalt, und das Verzeichnis gehört dem Benutzer des Hubs.
	a, err := archive.Open(pos[0], filepath.Dir(addr.Path))
	if err != nil {
		return c.fail(fmt.Errorf("%w; nichts geändert", err))
	}
	defer a.Close()
	m := a.Manifest
	if err := checkRestore(m, *keep); err != nil {
		return c.fail(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	s, err := hubstore.Open(ctx, addr)
	if err != nil {
		return c.fail(err)
	}
	defer s.Close()
	in := hubstore.RestoreInput{
		Revision: m.Revision,
		Rows: func(insert func(table string, values []any) error) error {
			for _, t := range hubstore.DumpTables() {
				if err := a.Rows(t, func(values []any) error { return insert(t.Name, values) }); err != nil {
					return err
				}
			}
			return nil
		},
	}
	if *keep {
		in.HubID = m.HubID
	}
	if err := s.Restore(ctx, in); err != nil {
		return c.fail(fmt.Errorf("%w; nichts geändert", err))
	}
	info, err := s.Info(ctx)
	if err != nil {
		return c.fail(err)
	}
	var rows int64
	for _, n := range m.Rows {
		rows += n
	}
	fmt.Fprintf(stdout, "Zurückgespielt aus %s: Revision %d, %d Zeilen (Export vom %s, hub_id %s).\n",
		pos[0], info.Revision, rows, m.CreatedAt, m.HubID)
	if *keep {
		fmt.Fprintf(stdout, "hub_id %s übernommen: Die Nodes gleichen ohne Neuaufbau weiter ab.\n", info.HubID)
	} else {
		fmt.Fprintf(stdout, "hub_id %s aus hub init: Jeder Node leert beim nächsten Abgleich seine Replica "+
			"und gleicht von vorn ab.\n", info.HubID)
	}
	return 0
}

// checkRestore prüft das Manifest eines Exports, bevor restore schreibt: die
// Schemafassung — Fassung 1 des Formats bildet nur die eigene ab; wer die
// Schemafassung erhöht, ergänzt hier die Abbildung von der Vorfassung — und
// --keep-hub-id nur für einen Export bei angehaltenem serve.
func checkRestore(m archive.Manifest, keep bool) error {
	if m.SchemaVersion != hubstore.SchemaVersion {
		return fmt.Errorf("Schemafassung %d des Exports: dieses Binary kennt keine Abbildung auf seine Fassung %d; "+
			"nichts geändert", m.SchemaVersion, hubstore.SchemaVersion)
	}
	if keep && !m.ServeStopped {
		return errors.New("--keep-hub-id nur für einen Export bei angehaltenem serve (serve_stopped: true); " +
			"dieser entstand im laufenden Betrieb und ist nicht sicher der letzte Stand — ohne --keep-hub-id " +
			"einspielen, dann leeren die Nodes ihre Replica; nichts geändert")
	}
	if keep {
		if _, err := ulid.ParseStrict(m.HubID); err != nil {
			return fmt.Errorf("hub_id %q des Exports ist keine ULID; nichts geändert", m.HubID)
		}
	}
	return nil
}
