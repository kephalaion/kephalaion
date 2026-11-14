package sqlitedb

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/kephalaion/kephalaion/internal/sqlq"
)

func TestQueriesPortable(t *testing.T) {
	for name, text := range sqlq.Texts(&Queries) {
		if err := sqlq.Check(text); err != nil {
			t.Errorf("Queries.%s: %v", name, err)
		}
	}
}

func TestOpenMissingCreatesNothing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fehlt.db")
	if _, err := Open(context.Background(), path); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Open: %v, erwartet ErrNotFound", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatalf("Open hat angelegt: %v", entries)
	}
}

func TestCreateOpenCheck(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "x.db")
	db, err := Create(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := CreateSchema(ctx, db, "CREATE TABLE extra (a TEXT);", "hub", 3, map[string]string{"revision": "0"}); err != nil {
		t.Fatal(err)
	}
	var mode string
	if err := db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
		t.Errorf("journal_mode = %q, %v", mode, err)
	}
	var fk, busy int
	if err := db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&fk); err != nil || fk != 1 {
		t.Errorf("foreign_keys = %d, %v", fk, err)
	}
	if err := db.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busy); err != nil || busy != busyTimeout {
		t.Errorf("busy_timeout = %d, %v", busy, err)
	}
	_ = db.Close()

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("Rechte %v, erwartet 0600", fi.Mode().Perm())
	}

	if _, err := Create(ctx, path); !errors.Is(err, ErrExists) {
		t.Fatalf("zweites Create: %v, erwartet ErrExists", err)
	}

	db, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// WAL steht seit Create in der Datei.
	if err := db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
		t.Errorf("journal_mode nach Open = %q, %v", mode, err)
	}
	info, err := CheckInfo(ctx, db, "hub", 3)
	if err != nil {
		t.Fatal(err)
	}
	if info[KeyRole] != "hub" || info["revision"] != "0" || info[KeyCreatedAt] == "" {
		t.Errorf("db_info = %v", info)
	}
	var wr *WrongRoleError
	if _, err := CheckInfo(ctx, db, "node", 3); !errors.As(err, &wr) {
		t.Errorf("falsche Rolle: %v", err)
	}
	var sv *SchemaVersionError
	if _, err := CheckInfo(ctx, db, "hub", 4); !errors.As(err, &sv) {
		t.Errorf("falsche Schemafassung: %v", err)
	}

	if err := SetInfo(ctx, db, "revision", "7"); err != nil {
		t.Fatal(err)
	}
	if v, err := GetInfo(ctx, db, "revision"); err != nil || v != "7" {
		t.Errorf("GetInfo = %q, %v", v, err)
	}
	if err := SetInfo(ctx, db, "gibtsnicht", "1"); err == nil {
		t.Error("SetInfo auf fehlende Zeile hätte scheitern sollen")
	}
}

func TestSettings(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "x.db")
	db, err := Create(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := CreateSchema(ctx, db, "", "node", 1, nil); err != nil {
		t.Fatal(err)
	}
	if err := ReplaceSettings(ctx, db, map[string]string{"a": "1", "b": "2"}); err != nil {
		t.Fatal(err)
	}
	if err := ReplaceSettings(ctx, db, map[string]string{"b": "3", "c": "4"}); err != nil {
		t.Fatal(err)
	}
	got, err := Settings(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got["b"] != "3" || got["c"] != "4" {
		t.Errorf("Settings = %v", got)
	}
}

func TestNotADatabase(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "leer.db")
	db, err := Create(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := CheckInfo(ctx, db, "hub", 1); err == nil {
		t.Fatal("leere Datenbank ohne db_info hätte scheitern sollen")
	}
}

func TestRemove(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.db")
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := Remove(path); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatalf("liegen geblieben: %v", entries)
	}
}

func TestPathWithSpecialChars(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "a b?c#d%e")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "x.db")
	db, err := Create(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := CreateSchema(ctx, db, "", "hub", 1, nil); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	entries, _ := os.ReadDir(dir)
	found := false
	for _, e := range entries {
		if e.Name() == "x.db" {
			found = true
		}
	}
	if !found {
		t.Fatalf("Datei nicht am erwarteten Ort: %v", entries)
	}
}

// Open auf eine fremde SQLite-Datei stellt sie nicht auf WAL um, bevor
// CheckInfo sie ablehnt.
func TestOpenForeignKeepsJournalMode(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "fremd.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(ctx, "CREATE TABLE fremd (a TEXT)"); err != nil {
		t.Fatal(err)
	}
	journalMode := func(db *sql.DB) string {
		t.Helper()
		var mode string
		if err := db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil {
			t.Fatal(err)
		}
		return mode
	}
	if m := journalMode(raw); m != "delete" {
		t.Fatalf("Ausgangslage journal_mode = %q", m)
	}
	_ = raw.Close()

	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CheckInfo(ctx, db, "hub", 1); err == nil {
		t.Error("fremde Datei hätte abgelehnt werden sollen")
	}
	_ = db.Close()

	raw, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if m := journalMode(raw); m != "delete" {
		t.Errorf("journal_mode nach Open = %q, erwartet delete", m)
	}
}

// Snapshot schreibt eine eigenständige Kopie: 0600, auf WAL, ohne -wal und
// -shm daneben, mit dem Inhalt der Quelle — auch wenn die Quelle offen ist
// und ihr letzter Stand noch im WAL liegt. Die Quelle bleibt, wie sie ist.
func TestSnapshot(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	src := filepath.Join(dir, "x.db")
	db, err := Create(ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := CreateSchema(ctx, db, "CREATE TABLE extra (a TEXT);", "hub", 3, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO extra (a) VALUES ('eins')`); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(dir, "kopie.db")
	if err := Snapshot(ctx, src, dst); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("Modus %v, erwartet 0600", fi.Mode().Perm())
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Stat(dst + suffix); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s liegt neben dem Snapshot (%v)", suffix, err)
		}
	}

	// Was danach in der Quelle geschieht, steht nicht in der Kopie.
	if _, err := db.ExecContext(ctx, `INSERT INTO extra (a) VALUES ('zwei')`); err != nil {
		t.Fatal(err)
	}
	cp, err := Open(ctx, dst)
	if err != nil {
		t.Fatal(err)
	}
	defer cp.Close()
	var mode string
	if err := cp.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
		t.Errorf("journal_mode der Kopie = %q, %v", mode, err)
	}
	if _, err := CheckInfo(ctx, cp, "hub", 3); err != nil {
		t.Errorf("CheckInfo der Kopie: %v", err)
	}
	var n int
	if err := cp.QueryRowContext(ctx, `SELECT COUNT(*) FROM extra`).Scan(&n); err != nil || n != 1 {
		t.Errorf("Zeilen in der Kopie = %d, %v; erwartet 1", n, err)
	}

	// Ein vorhandenes Ziel wird nie überschrieben.
	if err := Snapshot(ctx, src, dst); !errors.Is(err, ErrExists) {
		t.Errorf("Snapshot auf vorhandenes Ziel: %v, erwartet ErrExists", err)
	}
	if _, err := os.Stat(dst); err != nil {
		t.Errorf("das vorhandene Ziel ist weg: %v", err)
	}
}

// Scheitert der Snapshot, bleibt vom Ziel nichts zurück.
func TestSnapshotFailureLeavesNothing(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	if err := Snapshot(ctx, filepath.Join(dir, "fehlt.db"), filepath.Join(dir, "kopie.db")); !errors.Is(err, ErrNotFound) {
		t.Errorf("fehlende Quelle: %v, erwartet ErrNotFound", err)
	}
	// Keine Datenbank: VACUUM INTO scheitert, die vorab angelegte Datei geht.
	src := filepath.Join(dir, "kaputt.db")
	if err := os.WriteFile(src, []byte("das ist keine Datenbank, nur Text, lang genug für einen Kopf …………"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Snapshot(ctx, src, filepath.Join(dir, "kopie.db")); err == nil {
		t.Error("Snapshot einer Datei, die keine Datenbank ist, gelingt")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 || entries[0].Name() != "kaputt.db" {
		t.Errorf("zurückgeblieben: %v", entries)
	}
}
