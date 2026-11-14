package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"syscall"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/kephalaion/kephalaion/internal/config"
	hubstore "github.com/kephalaion/kephalaion/internal/hub/store"
	"github.com/kephalaion/kephalaion/internal/sqlitedb"
)

// hub backup und hub reset-id (Task 038): die Sicherung der Hub-Datenbank im
// laufenden Betrieb und der eine Schritt, den das Zurückspielen am Hub
// braucht. Der Ablauf des Wiederherstellens steht in docs/installation.md,
// „Sicherung und Wiederherstellung“.

const hubBackupUsage = `Aufruf:
  kephalaion hub backup [--dir verzeichnis] [--keep n] [--config pfad]

Schreibt eine Sicherung der Hub-Datenbank — Inhalte, Versionsgeschichte,
Collections, Nodes, Accounts und Rechte — und räumt alte Stände ab. Läuft
neben serve: Der Dienst bleibt an, kein Schreiber wartet. Die Sicherung ist
ein Stand (eine Revision), nie die Kopie einer halb geschriebenen Datei; die
Datenbankdatei von Hand zu kopieren, leistet das nicht.

Ablauf: Snapshot in eine verdeckte Datei im Zielverzeichnis, auf WAL
umstellen, prüfen, erst dann umbenennen in

  hub-<UTC JJJJMMTTThhmmssZ>-r<revision>.db    (Datei 0600)

Geprüft wird die geschriebene Datei: integrity_check, journal_mode wal, Rolle
hub, Schemafassung dieses Binarys. Die Revision im Namen ist die der Datei.
Besteht sie die Prüfung nicht, wird sie gelöscht, und das Kommando endet mit
Exit-Code 1 — eine fehlerhafte Sicherung bleibt nie liegen.

Danach löscht es die ältesten Sicherungen über --keep hinaus: nur Dateien mit
genau diesem Namensmuster, nie etwas anderes im Verzeichnis. Die Ausgabe ist
eine Zeile: Pfad, Größe, Revision, behaltene und gelöschte Stände.

Die Sicherung enthält alle Inhalte und die Hashes der Tokens: Sie gehört nur
dem Benutzer, unter dem der Hub läuft. In der globalen Installation deshalb
als Systembenutzer aufrufen (sudo -u kephalaion kephalaion hub backup) — als
root angelegt, gehörten Verzeichnis und Dateien root, und der Dienst käme
nicht mehr daran.

Zurückspielen (docs/installation.md, „Sicherung und Wiederherstellung“):
Dienst stoppen, hub.db samt -wal und -shm beiseitelegen, die Sicherung als
hub.db einsetzen, kephalaion hub reset-id, Dienst starten. Die Sicherung passt
nur zu einem Binary mit derselben Schemafassung.

Gesichert wird nur eine SQLite-Datenbank.

Optionen:
  --dir verzeichnis  Zielverzeichnis; ohne Angabe backups/ neben der
                     Datenbank. Fehlt es, wird es mit 0700 angelegt
  --keep n           so viele Sicherungen bleiben, die neue mitgezählt
                     (Standard 14); 0 löscht nichts
  --config pfad      Ort der config (siehe kephalaion hub init --help)
`

const hubResetIDUsage = `Aufruf:
  kephalaion hub reset-id [--config pfad]

Vergibt dem Hub eine neue hub_id und gibt die bisherige und die neue aus.
Dokumente, Revision, Collections, Nodes und Accounts bleiben, wie sie sind.

Gedacht für den Schritt nach dem Zurückspielen einer Sicherung (kephalaion
hub backup --help): Eine Sicherung entsteht im laufenden Betrieb und ist nie
sicher der letzte Stand — Nodes können danach schon spätere Revisionen
abgeglichen haben. Mit derselben hub_id bemerkte ein Node das nur, solange
sein Stand über der Revision des Hubs liegt; schreibt der Hub weiter, liefen
die Replicas still auseinander. An der neuen hub_id erkennt jeder Node den
Wechsel: Er leert beim nächsten Abgleich seine Replica und gleicht von vorn
ab.

Nur bei angehaltenem Dienst: reset-id nimmt dieselbe Sperre wie serve
(<db>.lock). Läuft serve für den Hub, endet es mit Exit-Code 1 und ändert
nichts. In der globalen Installation als Systembenutzer aufrufen
(sudo -u kephalaion kephalaion hub reset-id) — als root angelegt, gehörte die
Sperrdatei root, und der Dienst startete nicht mehr.

Optionen:
  --config pfad   Ort der config (siehe kephalaion hub init --help)
`

// defaultBackupKeep ist die Vorgabe für --keep.
const defaultBackupKeep = 14

// backupDirName ist das Verzeichnis der Sicherungen neben der Datenbank,
// wenn --dir fehlt.
const backupDirName = "backups"

// backupNow liefert die Zeit für den Namen der Sicherung; Tests setzen es.
var backupNow = time.Now

// backupFile erkennt den Namen einer Sicherung: hub-<UTC>-r<revision>.db.
// Nur solche Dateien zählt und löscht die Rotation.
var backupFile = regexp.MustCompile(`^hub-([0-9]{8}T[0-9]{6}Z)-r([0-9]+)\.db$`)

// backupFileName ist der Name einer Sicherung.
func backupFileName(at time.Time, revision int64) string {
	return fmt.Sprintf("hub-%s-r%d.db", at.UTC().Format("20060102T150405Z"), revision)
}

// hubDBOf liefert die db-Adresse des Hubs aus einer geladenen config.
func hubDBOf(cfg config.Config) (config.DB, error) {
	sec := cfg.Section(config.Hub)
	if sec == nil {
		return config.DB{}, errors.New("der Hub ist nicht eingerichtet; zuerst: kephalaion hub init")
	}
	return config.ParseDB(sec.DB)
}

// runHubBackup ist hub backup.
func runHubBackup(args []string, stdout, stderr io.Writer) int {
	c := newCommand("hub backup", hubBackupUsage, stdout, stderr)
	dirFlag := c.fs.String("dir", "", "")
	keep := c.fs.Int("keep", defaultBackupKeep, "")
	if _, code, ok := c.parse(args); !ok {
		return code
	}
	if *keep < 0 {
		fmt.Fprintf(stderr, "hub backup: --keep %d: erwartet eine Zahl ≥ 0 (0 löscht nichts)\n\n", *keep)
		fmt.Fprint(stderr, hubBackupUsage)
		return 2
	}
	if c.isSet("dir") && *dirFlag == "" {
		fmt.Fprint(stderr, "hub backup: --dir ist leer\n\n")
		fmt.Fprint(stderr, hubBackupUsage)
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
	dir := *dirFlag
	if dir == "" {
		dir = filepath.Join(filepath.Dir(addr.Path), backupDirName)
	}
	if dir, err = filepath.Abs(dir); err != nil {
		return c.fail(err)
	}
	// Ein Abbruch (systemd beim Herunterfahren, Strg-C) bricht den Snapshot
	// ab; die verdeckte Datei geht dann wie nach jedem Fehler.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	res, err := backupHub(ctx, addr, dir, *keep)
	if res.path != "" {
		fmt.Fprintf(stdout, "Sicherung: %s (%d Bytes, Revision %d); %s behalten, %d gelöscht.\n",
			res.path, res.size, res.revision, statesText(res.kept), len(res.removed))
	}
	if err != nil {
		return c.fail(err)
	}
	return 0
}

// statesText zählt Stände: „1 Stand“, „3 Stände“.
func statesText(n int) string {
	if n == 1 {
		return "1 Stand"
	}
	return fmt.Sprintf("%d Stände", n)
}

// backupResult beschreibt eine geschriebene Sicherung und was die Rotation
// getan hat.
type backupResult struct {
	path     string
	size     int64
	revision int64
	kept     int
	removed  []string
}

// backupHub schreibt eine geprüfte Sicherung der Hub-Datenbank nach dir und
// räumt danach alte Stände ab. Ist path im Ergebnis gesetzt, liegt die
// Sicherung dort — auch wenn danach noch etwas scheiterte (err).
func backupHub(ctx context.Context, addr config.DB, dir string, keep int) (backupResult, error) {
	// Vor dem Verzeichnis: Ohne Datenbank entsteht auch kein backups/.
	if addr.Kind == config.SQLite {
		if _, err := os.Stat(addr.Path); errors.Is(err, fs.ErrNotExist) {
			return backupResult{}, fmt.Errorf("%w: %s", sqlitedb.ErrNotFound, addr.Path)
		}
	}
	if err := ensureBackupDir(dir); err != nil {
		return backupResult{}, err
	}
	// Verdeckt und unter eigenem Namen: Die Rotation sieht die Datei nicht,
	// und erst die geprüfte Sicherung bekommt per rename ihren Namen — im
	// selben Verzeichnis, also im selben Dateisystem.
	tmp := filepath.Join(dir, ".hub-backup-"+ulid.Make().String()+".tmp")
	info, err := hubstore.Backup(ctx, addr, tmp)
	if err != nil {
		return backupResult{}, err
	}
	fi, err := os.Stat(tmp)
	if err != nil {
		_ = sqlitedb.Remove(tmp)
		return backupResult{}, err
	}
	name := backupFileName(backupNow(), info.Revision)
	path := filepath.Join(dir, name)
	if err := os.Rename(tmp, path); err != nil {
		_ = sqlitedb.Remove(tmp)
		return backupResult{}, err
	}
	res := backupResult{path: path, size: fi.Size(), revision: info.Revision}
	// Der neue Name soll einen Stromausfall überstehen.
	syncErr := syncDir(dir)
	var rotErr error
	res.kept, res.removed, rotErr = rotateBackups(dir, keep, name)
	if rotErr != nil {
		rotErr = fmt.Errorf("die Sicherung liegt in %s, aber alte Stände ließen sich nicht abräumen: %w", path, rotErr)
	}
	return res, errors.Join(syncErr, rotErr)
}

// ensureBackupDir legt das Verzeichnis der Sicherungen mit 0700 an, wenn es
// fehlt. Ein vorhandenes bleibt, wie es ist.
func ensureBackupDir(dir string) error {
	fi, err := os.Stat(dir)
	switch {
	case err == nil && !fi.IsDir():
		return fmt.Errorf("%s ist kein Verzeichnis", dir)
	case err == nil:
		return nil
	case !errors.Is(err, fs.ErrNotExist):
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// MkdirAll rechnet die umask ab; die Sicherungen gehören nur dem
	// Benutzer des Hubs.
	return os.Chmod(dir, 0o700)
}

// syncDir bringt die Einträge eines Verzeichnisses auf die Platte.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	if err := d.Sync(); err != nil {
		_ = d.Close()
		return fmt.Errorf("%s sichern: %w", dir, err)
	}
	return d.Close()
}

// rotateBackups löscht in dir die ältesten Sicherungen über keep hinaus und
// liefert, wie viele bleiben und welche gingen. Es fasst nur gewöhnliche
// Dateien an, deren Name der einer Sicherung ist (backupFile); alles andere im
// Verzeichnis bleibt. fresh, die eben geschriebene, bleibt immer — auch wenn
// ihr Zeitstempel nach einer zurückgestellten Uhr nicht der jüngste ist. keep
// 0 löscht nichts.
func rotateBackups(dir string, keep int, fresh string) (kept int, removed []string, err error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, nil, err
	}
	type backup struct {
		name     string
		stamp    string
		revision int64
	}
	var all []backup
	for _, e := range entries {
		m := backupFile.FindStringSubmatch(e.Name())
		if m == nil || !e.Type().IsRegular() {
			continue
		}
		// Eine Revision jenseits von int64 sortiert nach dem Zeitstempel als 0.
		rev, _ := strconv.ParseInt(m[2], 10, 64)
		all = append(all, backup{name: e.Name(), stamp: m[1], revision: rev})
	}
	// Jüngste zuerst: Der Zeitstempel hat feste Breite, also nach Text; in
	// derselben Sekunde die höhere Revision.
	slices.SortFunc(all, func(a, b backup) int {
		switch {
		case a.name == fresh:
			return -1
		case b.name == fresh:
			return 1
		}
		return cmp.Or(cmp.Compare(b.stamp, a.stamp), cmp.Compare(b.revision, a.revision), cmp.Compare(b.name, a.name))
	})
	if keep == 0 || len(all) <= keep {
		return len(all), nil, nil
	}
	var errs []error
	kept = keep
	for _, b := range all[keep:] {
		if err := os.Remove(filepath.Join(dir, b.name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
			kept++
			continue
		}
		removed = append(removed, b.name)
	}
	return kept, removed, errors.Join(errs...)
}

// runHubResetID ist hub reset-id.
func runHubResetID(args []string, stdout, stderr io.Writer) int {
	c := newCommand("hub reset-id", hubResetIDUsage, stdout, stderr)
	if _, code, ok := c.parse(args); !ok {
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
		return c.fail(fmt.Errorf("serve läuft für den Hub (Sperre %s) — eine neue hub_id gibt es nur bei "+
			"angehaltenem Dienst; nichts geändert", lockPath(addr.Path)))
	}
	// Dieselbe Sperre wie serve, bis zum Ende: Ein serve, der jetzt startet,
	// scheitert daran, statt mitten im Wechsel zu antworten.
	lock, err := takeLock(lockPath(addr.Path))
	if err != nil {
		return c.fail(fmt.Errorf("%w; nichts geändert", err))
	}
	defer lock.Close()
	ctx := context.Background()
	s, err := hubstore.Open(ctx, addr)
	if err != nil {
		return c.fail(err)
	}
	defer s.Close()
	old, fresh, err := s.ResetHubID(ctx)
	if err != nil {
		return c.fail(err)
	}
	fmt.Fprintf(stdout, "hub_id: %s → %s\n", old, fresh)
	fmt.Fprintln(stdout, "Jeder Node leert beim nächsten Abgleich seine Replica und gleicht von vorn ab.")
	return 0
}
