package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/oklog/ulid/v2"

	"github.com/kephalaion/kephalaion/internal/config"
	"github.com/kephalaion/kephalaion/internal/sqlitedb"
)

// Backup bei gleichzeitigen Schreibern: Jeder Snapshot ist ein Stand — seine
// Revision liegt zwischen der vor und der nach dem Aufruf, er öffnet sich mit
// Open, und seine Dokumente sind genau die der Schreibvorgänge bis zu dieser
// Revision, keines darüber, keines halb. Die Schreiber laufen über eine eigene
// Verbindung wie ein serve neben dem Kommando, und keiner scheitert.
func TestBackupUnderWriters(t *testing.T) {
	ctx := context.Background()
	addr := newDB(t)
	s, err := Create(ctx, addr)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.AddCollection(ctx, "wissen", "", false); err != nil {
		t.Fatal(err)
	}
	w, err := Open(ctx, addr)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	// op ist ein Schreibvorgang, wie er unter seiner Revision steht.
	type op struct {
		name, content string
		deleted       bool
	}
	var (
		mu  sync.Mutex
		ops = map[int64]op{}
		wg  sync.WaitGroup
	)
	const writers, rounds = 3, 20
	errs := make(chan error, writers)
	start := make(chan struct{})
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for j := range rounds {
				name := fmt.Sprintf("w%d/d%d.md", i, j%4)
				if j%7 == 6 {
					d, err := w.DeleteDocument(ctx, "wissen", name)
					if err != nil {
						errs <- fmt.Errorf("rm %s: %w", name, err)
						return
					}
					mu.Lock()
					ops[d.Revision] = op{name: name, deleted: true}
					mu.Unlock()
					continue
				}
				content := fmt.Sprintf("Schreiber %d, Runde %d\n", i, j)
				res, err := w.PutDocument(ctx, "wissen", name, content)
				if err != nil {
					errs <- fmt.Errorf("put %s: %w", name, err)
					return
				}
				mu.Lock()
				ops[res.Revision] = op{name: name, content: content}
				mu.Unlock()
			}
		}()
	}

	type snapshot struct {
		path          string
		info          Info
		before, after int64
	}
	revision := func() int64 {
		t.Helper()
		info, err := s.Info(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return info.Revision
	}
	dir := t.TempDir()
	var snaps []snapshot
	close(start)
	for i := range 6 {
		sn := snapshot{path: filepath.Join(dir, fmt.Sprintf("s%d.db", i)), before: revision()}
		if sn.info, err = Backup(ctx, addr, sn.path); err != nil {
			t.Fatalf("Backup %d: %v", i, err)
		}
		sn.after = revision()
		snaps = append(snaps, sn)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("Schreiber: %v", err)
	}
	// Einer danach: der Endstand.
	last := snapshot{path: filepath.Join(dir, "ende.db"), before: revision()}
	if last.info, err = Backup(ctx, addr, last.path); err != nil {
		t.Fatal(err)
	}
	last.after = revision()
	if last.info.Revision != int64(len(ops)) {
		t.Errorf("Endstand Revision %d, %d Schreibvorgänge", last.info.Revision, len(ops))
	}
	snaps = append(snaps, last)

	origin, err := s.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, sn := range snaps {
		if sn.info.Revision < sn.before || sn.info.Revision > sn.after {
			t.Errorf("%s: Revision %d, davor %d, danach %d", sn.path, sn.info.Revision, sn.before, sn.after)
		}
		if sn.info.HubID != origin.HubID || sn.info.SchemaVersion != SchemaVersion {
			t.Errorf("%s: Info %+v, Original %+v", sn.path, sn.info, origin)
		}
		// Der Stand der Revision: alle Schreibvorgänge bis dahin, in ihrer
		// Reihenfolge.
		want := map[string]string{}
		for rev := int64(1); rev <= sn.info.Revision; rev++ {
			o, ok := ops[rev]
			if !ok {
				t.Fatalf("Revision %d ohne Schreibvorgang", rev)
			}
			if o.deleted {
				delete(want, o.name)
			} else {
				want[o.name] = o.content
			}
		}
		cp, err := Open(ctx, config.SQLiteDB(sn.path))
		if err != nil {
			t.Fatalf("%s öffnet sich nicht: %v", sn.path, err)
		}
		info, err := cp.Info(ctx)
		if err != nil || info != sn.info {
			t.Errorf("%s: Info %+v, %v; Backup nannte %+v", sn.path, info, err, sn.info)
		}
		docs, err := cp.Documents(ctx, "wissen", "")
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]string{}
		for _, d := range docs {
			got[d.Name] = d.Content
			if d.Revision > sn.info.Revision {
				t.Errorf("%s: %s trägt Revision %d über der des Snapshots (%d)", sn.path, d.Name, d.Revision, sn.info.Revision)
			}
		}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%s (Revision %d):\n  Dokumente %v\n  erwartet  %v", sn.path, sn.info.Revision, got, want)
		}
		if err := cp.Close(); err != nil {
			t.Fatal(err)
		}
		for _, suffix := range []string{"-wal", "-shm"} {
			if _, err := os.Stat(sn.path + suffix); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("%s%s liegt neben dem Snapshot (%v)", sn.path, suffix, err)
			}
		}
	}
}

// Ein Snapshot, der sich nicht als Hub-Datenbank dieses Binarys öffnet, wird
// verworfen: Fehler, keine Datei. Eine andere Datenbankart als SQLite sichert
// Backup nicht.
func TestBackupRejects(t *testing.T) {
	ctx := context.Background()
	addr := newDB(t)
	s, err := Create(ctx, addr)
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlitedb.SetInfo(ctx, s.(*sqliteStore).db, sqlitedb.KeySchemaVersion, "1"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	dst := filepath.Join(dir, "kopie.db")
	var sv *sqlitedb.SchemaVersionError
	if _, err := Backup(ctx, addr, dst); !errors.As(err, &sv) {
		t.Errorf("Backup mit fremder Schemafassung: %v, erwartet SchemaVersionError", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("zurückgeblieben: %v", entries)
	}

	if _, err := Backup(ctx, config.DB{Kind: "postgres"}, dst); !errors.Is(err, config.ErrUnsupported) {
		t.Errorf("Backup einer anderen Datenbankart: %v, erwartet ErrUnsupported", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("zurückgeblieben: %v", entries)
	}
}

// ResetHubID vergibt eine neue ULID, lässt Revision und Dokumente stehen und
// nennt die bisherige hub_id in actions.
func TestResetHubID(t *testing.T) {
	ctx := context.Background()
	s, err := Create(ctx, newDB(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.AddCollection(ctx, "wissen", "", false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutDocument(ctx, "wissen", "a.md", "A\n"); err != nil {
		t.Fatal(err)
	}
	before, err := s.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	old, fresh, err := s.ResetHubID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	after, err := s.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if old != before.HubID || fresh != after.HubID || fresh == old {
		t.Errorf("ResetHubID = %s → %s; davor %s, danach %s", old, fresh, before.HubID, after.HubID)
	}
	if _, err := ulid.ParseStrict(fresh); err != nil {
		t.Errorf("neue hub_id %q ist keine ULID: %v", fresh, err)
	}
	if after.Revision != before.Revision || after.SchemaVersion != before.SchemaVersion {
		t.Errorf("Info danach %+v, davor %+v", after, before)
	}
	if d, err := s.Document(ctx, "wissen", "a.md"); err != nil || d.Content != "A\n" {
		t.Errorf("Dokument danach: %+v, %v", d, err)
	}
	all := actions(t, s.(*sqliteStore))
	if last := all[len(all)-1]; last.account != Admin || last.action != "hub.reset_id" || last.subject.String != old {
		t.Errorf("letzte Zeile in actions: %+v", last)
	}
}
