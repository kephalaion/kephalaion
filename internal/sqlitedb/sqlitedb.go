// Package sqlitedb ist der gemeinsame Unterbau der Datenbanken von Hub und
// Node. Er kennt keine der beiden Rollen: SQLite öffnen samt Einstellungen der
// Verbindung, db_info prüfen (Schemafassung, Rolle), settings lesen und
// schreiben.
//
// SQLite-eigen sind nur das Öffnen und der Snapshot (Snapshot, für die
// Sicherung des Hubs). Die Abfragen auf db_info und settings folgen der Regel
// für den Hub: Sie laufen auch in PostgreSQL (siehe internal/sqlq), denn der
// Hub benutzt sie mit.
package sqlitedb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"modernc.org/sqlite" // Treiber "sqlite", reines Go
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/kephalaion/kephalaion/internal/sqlq"
)

// ErrNotFound meldet eine fehlende Datenbankdatei.
var ErrNotFound = errors.New("Datenbankdatei fehlt")

// ErrExists meldet, dass eine anzulegende Datenbankdatei schon existiert.
var ErrExists = errors.New("Datenbankdatei existiert schon")

// ErrNoInfo meldet eine Datei, die SQLite als Datenbank liest, die aber keine
// Kephalaion-Datenbank ist: db_info fehlt oder nennt keine Rolle.
var ErrNoInfo = errors.New("keine Kephalaion-Datenbank")

// IsCorrupt sagt, ob err meldet, dass SQLite die Datei nicht als Datenbank
// erkennt oder sie beschädigt ist (SQLITE_NOTADB, SQLITE_CORRUPT, auch mit
// erweitertem Code). Andere Fehler — belegt (BUSY, LOCKED), Zugriffsrechte,
// abgebrochener ctx — sind es nicht.
func IsCorrupt(err error) bool {
	var se *sqlite.Error
	if !errors.As(err, &se) {
		return false
	}
	switch se.Code() & 0xff {
	case sqlite3.SQLITE_NOTADB, sqlite3.SQLITE_CORRUPT:
		return true
	}
	return false
}

// noSuchTable sagt, ob err meldet, dass eine Tabelle fehlt. SQLite hat dafür
// keinen eigenen Code (SQLITE_ERROR), nur die Meldung.
func noSuchTable(err error) bool {
	var se *sqlite.Error
	return errors.As(err, &se) && se.Code()&0xff == sqlite3.SQLITE_ERROR && strings.Contains(se.Error(), "no such table")
}

// BaseSchema ist das DDL der Tabellen, die jede Datenbank hat.
const BaseSchema = `
CREATE TABLE db_info (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
CREATE TABLE settings (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
`

// Schlüssel in db_info.
const (
	KeySchemaVersion = "schema_version"
	KeyRole          = "role"
	KeyCreatedAt     = "created_at"
)

// busyTimeout ist die Wartezeit in ms, wenn ein anderer schreibt.
const busyTimeout = 5000

// Queries sind die zentral abgelegten Abfragetexte des Unterbaus, mit
// Platzhaltern $n. Ein Test prüft sie mit sqlq.Check.
var Queries = struct {
	InfoAll        string
	InfoGet        string
	InfoInsert     string
	InfoUpdate     string
	SettingsAll    string
	SettingsDelete string
	SettingsInsert string
	SettingsUpsert string
	SettingsUnset  string
}{
	InfoAll:        `SELECT key, value FROM db_info`,
	InfoGet:        `SELECT value FROM db_info WHERE key = $1`,
	InfoInsert:     `INSERT INTO db_info (key, value) VALUES ($1, $2)`,
	InfoUpdate:     `UPDATE db_info SET value = $2 WHERE key = $1`,
	SettingsAll:    `SELECT key, value FROM settings ORDER BY key`,
	SettingsDelete: `DELETE FROM settings`,
	SettingsInsert: `INSERT INTO settings (key, value) VALUES ($1, $2)`,
	SettingsUpsert: `INSERT INTO settings (key, value) VALUES ($1, $2)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value`,
	SettingsUnset: `DELETE FROM settings WHERE key = $1`,
}

// q übersetzt einen Abfragetext für SQLite.
func q(text string) string { return sqlq.Bind(sqlq.SQLite, text) }

// dsn liefert die Adresse für den Treiber: URI-Form, damit mode=rw gilt und
// eine fehlende Datei nicht angelegt wird, dazu Fremdschlüssel und
// busy_timeout für jede Verbindung — beides ändert die Datei nicht.
// _txlock=immediate lässt jede Transaktion als BEGIN IMMEDIATE beginnen: Sie
// nimmt die Schreibsperre sofort, sodass ein späterer Wechsel vom Lesen zum
// Schreiben nicht an SQLITE_BUSY scheitert. Das gilt auch für Transaktionen,
// die nur lesen; reine Lesezugriffe laufen deshalb ohne Transaktion.
//
// withWAL setzt zusätzlich journal_mode(WAL). Das bleibt in der Datei stehen
// und gehört deshalb nur zu Create, nie zum Öffnen einer vorhandenen Datei.
func dsn(path string, withWAL bool) string {
	u := url.URL{Scheme: "file", Path: path}
	s := u.String() + "?mode=rw" +
		"&_txlock=immediate" +
		"&_pragma=foreign_keys(1)" +
		"&_pragma=busy_timeout(" + strconv.Itoa(busyTimeout) + ")"
	if withWAL {
		s += "&_pragma=journal_mode(WAL)"
	}
	return s
}

// Open öffnet eine vorhandene Datenbankdatei. Fehlt sie, liefert Open
// ErrNotFound und legt nichts an. Open ändert die Datei nicht, auch nicht
// ihren journal_mode.
func Open(ctx context.Context, path string) (*sql.DB, error) {
	return open(ctx, path, false)
}

func open(ctx context.Context, path string, withWAL bool) (*sql.DB, error) {
	fi, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, path)
	}
	if err != nil {
		return nil, fmt.Errorf("Datenbankdatei %s: %w", path, err)
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("Datenbankdatei %s ist keine Datei", path)
	}
	db, err := sql.Open("sqlite", dsn(path, withWAL))
	if err != nil {
		return nil, fmt.Errorf("Datenbank %s: %w", path, err)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("Datenbank %s lässt sich nicht öffnen: %w", path, err)
	}
	return db, nil
}

// Create legt eine neue, leere Datenbankdatei mit 0600 an, stellt sie auf WAL
// und öffnet sie. Existiert die Datei schon, liefert Create ErrExists und ändert nichts.
func Create(ctx context.Context, path string) (*sql.DB, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return nil, fmt.Errorf("%w: %s", ErrExists, path)
	}
	if err != nil {
		return nil, fmt.Errorf("Datenbankdatei %s nicht anlegbar: %w", path, err)
	}
	if err := f.Close(); err != nil {
		_ = Remove(path)
		return nil, fmt.Errorf("Datenbankdatei %s nicht anlegbar: %w", path, err)
	}
	db, err := open(ctx, path, true)
	if err != nil {
		_ = Remove(path)
		return nil, err
	}
	return db, nil
}

// Remove entfernt eine Datenbankdatei samt -wal, -shm und -journal. Fehlende
// Dateien sind kein Fehler.
func Remove(path string) error {
	var errs []error
	for _, p := range []string{path, path + "-wal", path + "-shm", path + "-journal"} {
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Snapshot schreibt eine vollständige, in sich stimmige Kopie der Datenbank
// src nach dst, auch während andere Prozesse in src schreiben: ein Stand, nie
// eine halb geschriebene Datei. dst darf es noch nicht geben; die Kopie
// entsteht mit 0600, steht wie jede angelegte Datenbank auf WAL, ist mit
// integrity_check geprüft und geschlossen — neben ihr liegen danach weder -wal
// noch -shm. Scheitert etwas, bleibt von dst nichts zurück.
//
// VACUUM INTO läuft als einzelne Anweisung, nie in einer mit BeginTx
// begonnenen Transaktion: _txlock=immediate im dsn machte daraus BEGIN
// IMMEDIATE und nähme die Schreibsperre. So liest es in einer Lesetransaktion
// und hält keinen Schreiber auf. Seine Kopie steht auf journal_mode=delete,
// und Open stellt nie um — ohne das Umstellen hier liefe eine zurückgespielte
// Datenbank mit Rollback-Journal.
func Snapshot(ctx context.Context, src, dst string) (err error) {
	db, err := Open(ctx, src)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	// Vorab leer mit 0600 anlegen: VACUUM INTO nimmt eine leere Datei an, und
	// der Modus hängt so nicht an der umask.
	f, err := os.OpenFile(dst, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("%w: %s", ErrExists, dst)
	}
	if err != nil {
		return fmt.Errorf("Snapshot %s nicht anlegbar: %w", dst, err)
	}
	defer func() {
		if err != nil {
			_ = Remove(dst)
		}
	}()
	if err := f.Close(); err != nil {
		return fmt.Errorf("Snapshot %s nicht anlegbar: %w", dst, err)
	}
	if _, err := db.ExecContext(ctx, `VACUUM INTO ?`, dst); err != nil {
		return fmt.Errorf("Snapshot von %s nach %s: %w", src, dst, err)
	}
	if err := db.Close(); err != nil {
		return fmt.Errorf("Datenbank %s schließen: %w", src, err)
	}
	if err := checkSnapshot(ctx, dst); err != nil {
		return err
	}
	return syncFile(dst)
}

// checkSnapshot stellt die Kopie auf WAL und prüft sie: journal_mode und
// integrity_check. Danach ist sie geschlossen.
func checkSnapshot(ctx context.Context, path string) error {
	db, err := open(ctx, path, true)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	// Eine Verbindung: Beim Schließen der letzten räumt SQLite -wal und -shm
	// weg.
	db.SetMaxOpenConns(1)
	var mode string
	if err := db.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&mode); err != nil {
		return fmt.Errorf("Snapshot %s: journal_mode lesen: %w", path, err)
	}
	if mode != "wal" {
		return fmt.Errorf("Snapshot %s: journal_mode ist %s, nicht wal", path, mode)
	}
	rows, err := db.QueryContext(ctx, `PRAGMA integrity_check`)
	if err != nil {
		return fmt.Errorf("Snapshot %s: integrity_check: %w", path, err)
	}
	var found []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			_ = rows.Close()
			return fmt.Errorf("Snapshot %s: integrity_check: %w", path, err)
		}
		found = append(found, line)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return fmt.Errorf("Snapshot %s: integrity_check: %w", path, err)
	}
	if len(found) != 1 || found[0] != "ok" {
		return fmt.Errorf("Snapshot %s: integrity_check meldet: %s", path, strings.Join(found, "; "))
	}
	if err := db.Close(); err != nil {
		return fmt.Errorf("Snapshot %s schließen: %w", path, err)
	}
	return nil
}

// syncFile bringt eine geschriebene Datei auf die Platte.
func syncFile(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("%s sichern: %w", path, err)
	}
	return f.Close()
}

// Querier ist, was *sql.DB und *sql.Tx gemeinsam haben.
type Querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// NowMillis liefert die aktuelle Zeit in ms seit Epoche, wie sie in der
// Datenbank steht.
func NowMillis() int64 { return time.Now().UnixMilli() }

// InitInfo schreibt die Zeilen von db_info einer frisch angelegten Datenbank:
// Schemafassung, Rolle, Anlagezeit und was extra dazukommt.
func InitInfo(ctx context.Context, tx Querier, role string, schemaVersion int, extra map[string]string) error {
	rows := map[string]string{
		KeySchemaVersion: strconv.Itoa(schemaVersion),
		KeyRole:          role,
		KeyCreatedAt:     strconv.FormatInt(NowMillis(), 10),
	}
	for k, v := range extra {
		rows[k] = v
	}
	for _, k := range sortedKeys(rows) {
		if _, err := tx.ExecContext(ctx, q(Queries.InfoInsert), k, rows[k]); err != nil {
			return fmt.Errorf("db_info schreiben: %w", err)
		}
	}
	return nil
}

// ReadInfo liest alle Zeilen von db_info.
func ReadInfo(ctx context.Context, db Querier) (map[string]string, error) {
	rows, err := db.QueryContext(ctx, q(Queries.InfoAll))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// GetInfo liest eine Zeile von db_info.
func GetInfo(ctx context.Context, db Querier, key string) (string, error) {
	var v string
	err := db.QueryRowContext(ctx, q(Queries.InfoGet), key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("db_info: %s fehlt", key)
	}
	return v, err
}

// SetInfo ändert eine vorhandene Zeile von db_info.
func SetInfo(ctx context.Context, tx Querier, key, value string) error {
	res, err := tx.ExecContext(ctx, q(Queries.InfoUpdate), key, value)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("db_info: %s fehlt", key)
	}
	return nil
}

// WrongRoleError meldet eine Datenbank der anderen Rolle.
type WrongRoleError struct {
	Want, Got string
}

func (e *WrongRoleError) Error() string {
	return fmt.Sprintf("die Datenbank gehört zur Rolle %s, nicht zu %s", e.Got, e.Want)
}

// SchemaVersionError meldet eine Schemafassung, die nicht zu diesem Binary
// passt. Migrationen gibt es noch nicht: Die Datenbank ist neu anzulegen.
type SchemaVersionError struct {
	Role      string
	Want, Got string
}

func (e *SchemaVersionError) Error() string {
	return fmt.Sprintf("Schemafassung %s der Datenbank passt nicht zu diesem Binary (erwartet %s). "+
		"Die Datenbank muss neu angelegt werden (kephalaion %s init); die Inhalte gehen dabei verloren. "+
		"Die Einstellungen (settings und config) lassen sich vorher mit dem bisherigen Binary über "+
		"`kephalaion config export` sichern und danach mit `kephalaion config import` zurückholen.",
		e.Got, e.Want, e.Role)
}

// CheckInfo prüft Rolle und Schemafassung einer geöffneten Datenbank und
// liefert db_info. Fehlt db_info oder nennt sie keine Rolle, ist der Fehler
// ErrNoInfo.
func CheckInfo(ctx context.Context, db Querier, role string, schemaVersion int) (map[string]string, error) {
	info, err := ReadInfo(ctx, db)
	if err != nil {
		if noSuchTable(err) {
			return nil, fmt.Errorf("%w (db_info fehlt)", ErrNoInfo)
		}
		return nil, fmt.Errorf("keine Kephalaion-Datenbank (db_info nicht lesbar: %w)", err)
	}
	gotRole, ok := info[KeyRole]
	if !ok {
		return nil, fmt.Errorf("%w (db_info ohne Rolle)", ErrNoInfo)
	}
	if gotRole != role {
		return nil, &WrongRoleError{Want: role, Got: gotRole}
	}
	want := strconv.Itoa(schemaVersion)
	if got := info[KeySchemaVersion]; got != want {
		return nil, &SchemaVersionError{Role: role, Want: want, Got: got}
	}
	return info, nil
}

// Settings liest alle settings.
func Settings(ctx context.Context, db Querier) (map[string]string, error) {
	rows, err := db.QueryContext(ctx, q(Queries.SettingsAll))
	if err != nil {
		return nil, fmt.Errorf("settings lesen: %w", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, fmt.Errorf("settings lesen: %w", err)
		}
		out[k] = v
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("settings lesen: %w", err)
	}
	return out, nil
}

// SetSetting setzt einen Schlüssel der settings, die übrigen bleiben. Eine
// Anweisung, also ohne eigene Transaktion atomar.
func SetSetting(ctx context.Context, db Querier, key, value string) error {
	if _, err := db.ExecContext(ctx, q(Queries.SettingsUpsert), key, value); err != nil {
		return fmt.Errorf("settings schreiben: %w", err)
	}
	return nil
}

// UnsetSetting entfernt einen Schlüssel der settings; removed sagt, ob er
// gesetzt war.
func UnsetSetting(ctx context.Context, db Querier, key string) (removed bool, err error) {
	res, err := db.ExecContext(ctx, q(Queries.SettingsUnset), key)
	if err != nil {
		return false, fmt.Errorf("settings schreiben: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("settings schreiben: %w", err)
	}
	return n > 0, nil
}

// ReplaceSettings ersetzt alle settings in einer Transaktion.
func ReplaceSettings(ctx context.Context, db *sql.DB, settings map[string]string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("settings schreiben: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := ReplaceSettingsTx(ctx, tx, settings); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("settings schreiben: %w", err)
	}
	return nil
}

// ReplaceSettingsTx ersetzt alle settings innerhalb einer laufenden
// Transaktion.
func ReplaceSettingsTx(ctx context.Context, tx Querier, settings map[string]string) error {
	if _, err := tx.ExecContext(ctx, q(Queries.SettingsDelete)); err != nil {
		return fmt.Errorf("settings schreiben: %w", err)
	}
	for _, k := range sortedKeys(settings) {
		if _, err := tx.ExecContext(ctx, q(Queries.SettingsInsert), k, settings[k]); err != nil {
			return fmt.Errorf("settings schreiben: %w", err)
		}
	}
	return nil
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// CreateSchema legt in einer frisch angelegten Datenbank in einer Transaktion
// BaseSchema, das DDL der Rolle und die Zeilen von db_info an.
func CreateSchema(ctx context.Context, db *sql.DB, roleDDL, role string, schemaVersion int, extra map[string]string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("Schema anlegen: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, BaseSchema+roleDDL); err != nil {
		return fmt.Errorf("Schema anlegen: %w", err)
	}
	if err := InitInfo(ctx, tx, role, schemaVersion, extra); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("Schema anlegen: %w", err)
	}
	return nil
}
