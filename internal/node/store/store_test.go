package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kephalaion/kephalaion/internal/config"
	"github.com/kephalaion/kephalaion/internal/sqlitedb"
)

func newDB(t *testing.T) config.DB {
	t.Helper()
	return config.SQLiteDB(filepath.Join(t.TempDir(), "node.db"))
}

func TestCreateAndReopen(t *testing.T) {
	ctx := context.Background()
	addr := newDB(t)
	s, err := Create(ctx, addr)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceSettings(ctx, map[string]string{"gruss": "127.0.0.1:7070"}); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()

	if _, err := Create(ctx, addr); !errors.Is(err, sqlitedb.ErrExists) {
		t.Fatalf("zweites Create: %v", err)
	}

	s, err = Open(ctx, addr)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	info, err := s.Info(ctx)
	if err != nil || info.SchemaVersion != SchemaVersion {
		t.Errorf("Info = %+v, %v", info, err)
	}
	settings, err := s.Settings(ctx)
	if err != nil || settings["gruss"] != "127.0.0.1:7070" {
		t.Errorf("Settings = %v, %v", settings, err)
	}
}

func TestOpenMissing(t *testing.T) {
	addr := newDB(t)
	if _, err := Open(context.Background(), addr); !errors.Is(err, sqlitedb.ErrNotFound) {
		t.Fatalf("Open: %v", err)
	}
	if _, err := os.Stat(addr.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Open hat die Datei angelegt: %v", err)
	}
}

func TestWrongSchemaVersion(t *testing.T) {
	ctx := context.Background()
	addr := newDB(t)
	s, err := Create(ctx, addr)
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlitedb.SetInfo(ctx, s.(*sqliteStore).db, sqlitedb.KeySchemaVersion, "99"); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	var sv *sqlitedb.SchemaVersionError
	if _, err := Open(ctx, addr); !errors.As(err, &sv) {
		t.Fatalf("Open: %v, erwartet SchemaVersionError", err)
	}
}

func TestWrongRole(t *testing.T) {
	ctx := context.Background()
	addr := newDB(t)
	db, err := sqlitedb.Create(ctx, addr.Path)
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlitedb.CreateSchema(ctx, db, "", "hub", SchemaVersion, nil); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	var wr *sqlitedb.WrongRoleError
	if _, err := Open(ctx, addr); !errors.As(err, &wr) {
		t.Fatalf("Open: %v, erwartet WrongRoleError", err)
	}
}

func TestSchemaVersion1Rejected(t *testing.T) {
	ctx := context.Background()
	addr := newDB(t)
	s, err := Create(ctx, addr)
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlitedb.SetInfo(ctx, s.(*sqliteStore).db, sqlitedb.KeySchemaVersion, "1"); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	_, err = Open(ctx, addr)
	var sv *sqlitedb.SchemaVersionError
	if !errors.As(err, &sv) {
		t.Fatalf("Open: %v, erwartet SchemaVersionError", err)
	}
	for _, want := range []string{"Schemafassung 1", "erwartet 5", "node init", "config export"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Meldung ohne %q: %v", want, err)
		}
	}
}

// Zwei gleichzeitige Transaktionen, die erst lesen und dann schreiben,
// scheitern nicht an SQLITE_BUSY.
func TestReadThenWriteConcurrent(t *testing.T) {
	ctx := context.Background()
	addr := newDB(t)
	s, err := Create(ctx, addr)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	other, err := Open(ctx, addr)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()

	const rounds = 5
	var wg sync.WaitGroup
	errs := make(chan error, 2*rounds)
	for w, st := range []Store{s, other} {
		wg.Add(1)
		go func(w int, db *sqliteStore) {
			defer wg.Done()
			for i := 0; i < rounds; i++ {
				if err := readThenWrite(ctx, db, fmt.Sprintf("k%d-%d", w, i)); err != nil {
					errs <- err
					return
				}
			}
		}(w, st.(*sqliteStore))
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	got, err := s.Settings(ctx)
	if err != nil || len(got) != 2*rounds {
		t.Errorf("settings = %v, %v", got, err)
	}
}

func readThenWrite(ctx context.Context, s *sqliteStore, key string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM settings`).Scan(&n); err != nil {
		return err
	}
	time.Sleep(5 * time.Millisecond)
	if _, err := tx.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES (?, ?)`, key, "v"); err != nil {
		return err
	}
	return tx.Commit()
}
