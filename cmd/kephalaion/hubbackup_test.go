package main

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/kephalaion/kephalaion/internal/config"
	hubstore "github.com/kephalaion/kephalaion/internal/hub/store"
	"github.com/kephalaion/kephalaion/internal/sqlitedb"
)

// backupSetup richtet Hub und Node in einer config ein: Collection wissen am
// Hub, Node laptop mit Recht darauf, am Node der Hub-Eintrag eigen über
// local, der sie will. Anders als syncSetup lässt es keine Datenbank offen —
// die Tests ersetzen die hub.db.
func backupSetup(t *testing.T, dir string) (cfg, c string) {
	t.Helper()
	cfg = setup(t, dir)
	c = "--config=" + cfg
	runT(t, "hub", "collection", "add", "wissen", c).want(t, 0)
	r := runT(t, "hub", "node", "add", "laptop", c)
	r.want(t, 0)
	runT(t, "hub", "node", "grant", "laptop", "wissen", c).want(t, 0)
	runIn(t, tokenFrom(t, r.out)+"\n", "node", "hub", "add", "eigen", "--node", "laptop", "--transport", "local",
		"--token-stdin", c).want(t, 0)
	runT(t, "node", "collection", "add", "eigen:wissen", c).want(t, 0)
	return cfg, c
}

// hubInfoOf liest db_info einer Hub-Datenbank und schließt sie wieder.
func hubInfoOf(t *testing.T, path string) hubstore.Info {
	t.Helper()
	ctx := context.Background()
	s, err := hubstore.Open(ctx, config.SQLiteDB(path))
	if err != nil {
		t.Fatalf("%s öffnet sich nicht als Hub-Datenbank: %v", path, err)
	}
	defer s.Close()
	info, err := s.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return info
}

// docsOf liest die Dokumente der Collection wissen einer Hub-Datenbank.
func docsOf(t *testing.T, path string) map[string]string {
	t.Helper()
	ctx := context.Background()
	s, err := hubstore.Open(ctx, config.SQLiteDB(path))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	docs, err := s.Documents(ctx, "wissen", "")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, d := range docs {
		out[d.Name] = d.Content
	}
	return out
}

// backupAt stellt die Uhr für den Namen der Sicherung.
func backupAt(t *testing.T, at time.Time) {
	t.Helper()
	old := backupNow
	backupNow = func() time.Time { return at }
	t.Cleanup(func() { backupNow = old })
}

// names listet ein Verzeichnis, sortiert.
func names(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

// putDoc schreibt ein Dokument am Hub.
func putDoc(t *testing.T, c, name, content string) {
	t.Helper()
	runIn(t, content, "hub", "doc", "put", "wissen", name, c).want(t, 0)
}

// restoreHub spielt eine Sicherung zurück, wie docs/installation.md es
// beschreibt: hub.db samt -wal und -shm weg, die Sicherung an ihre Stelle,
// 0600. Der Dienst läuft im Test nicht.
func restoreHub(t *testing.T, backup, hubDB string) {
	t.Helper()
	if err := sqlitedb.Remove(hubDB); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(backup)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hubDB, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// hub backup schreibt neben die hub.db nach backups/: eine Datei 0600 in
// einem Verzeichnis 0700, benannt nach Zeit und Revision, auf WAL, ohne -wal
// und -shm, als Hub-Datenbank zu öffnen, mit den Dokumenten und der Revision
// des Originals. Die Revision im Namen ist die der Datei. Die Ausgabe ist
// eine Zeile.
func TestHubBackup(t *testing.T) {
	dir := isolate(t)
	cfg, c := backupSetup(t, dir)
	hubDB := filepath.Join(dir, "hub.db")
	putDoc(t, c, "a.md", "A\n")
	putDoc(t, c, "sub/b.md", "B\n")
	backupAt(t, time.Date(2026, 11, 14, 3, 15, 0, 0, time.FixedZone("CET", 3600)))

	backups := filepath.Join(dir, "backups")
	file := filepath.Join(backups, "hub-20261114T021500Z-r2.db")
	r := runT(t, "hub", "backup", c)
	r.want(t, 0, "Sicherung: "+file+" (", " Bytes, Revision 2); 1 Stand behalten, 0 gelöscht.\n")
	if strings.Count(r.out, "\n") != 1 || r.errOut != "" {
		t.Errorf("Ausgabe:\n%s\nstderr:\n%s", r.out, r.errOut)
	}
	if got := names(t, backups); !slices.Equal(got, []string{filepath.Base(file)}) {
		t.Fatalf("in backups/: %v", got)
	}
	for path, want := range map[string]os.FileMode{backups: 0o700, file: 0o600} {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != want {
			t.Errorf("%s: Modus %v, erwartet %v", path, fi.Mode().Perm(), want)
		}
	}
	fi, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.out, "("+strconv.FormatInt(fi.Size(), 10)+" Bytes,") {
		t.Errorf("Größe %d fehlt in der Ausgabe: %s", fi.Size(), r.out)
	}

	// WAL, und nach dem Schließen nichts daneben.
	ctx := context.Background()
	db, err := sqlitedb.Open(ctx, file)
	if err != nil {
		t.Fatal(err)
	}
	var mode string
	if err := db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
		t.Errorf("journal_mode der Sicherung = %q, %v", mode, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if got := names(t, backups); !slices.Equal(got, []string{filepath.Base(file)}) {
		t.Errorf("neben der Sicherung liegt: %v", got)
	}

	// Dieselben Dokumente, dieselbe Revision, dieselbe hub_id wie das Original.
	orig, copied := hubInfoOf(t, hubDB), hubInfoOf(t, file)
	if copied != orig || copied.Revision != 2 {
		t.Errorf("Sicherung %+v, Original %+v", copied, orig)
	}
	want := map[string]string{"a.md": "A\n", "sub/b.md": "B\n"}
	if got := docsOf(t, file); len(got) != len(want) || got["a.md"] != want["a.md"] || got["sub/b.md"] != want["sub/b.md"] {
		t.Errorf("Dokumente der Sicherung: %v", got)
	}

	// Was danach am Hub geschieht, steht nicht darin; das Original bleibt
	// benutzbar, die nächste Sicherung trägt die neue Revision.
	putDoc(t, c, "c.md", "C\n")
	backupAt(t, time.Date(2026, 11, 15, 2, 15, 0, 0, time.UTC))
	runT(t, "hub", "backup", "--config", cfg).want(t, 0, "hub-20261115T021500Z-r3.db", "Revision 3); 2 Stände behalten, 0 gelöscht.")
	if got := docsOf(t, file); len(got) != 2 {
		t.Errorf("die erste Sicherung hat sich geändert: %v", got)
	}

	// --dir: ein anderes Verzeichnis, angelegt mit 0700.
	other := filepath.Join(dir, "anders", "tief")
	runT(t, "hub", "backup", "--dir", other, c).want(t, 0, "Sicherung: "+filepath.Join(other, "hub-20261115T021500Z-r3.db"))
	if fi, err := os.Stat(other); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("--dir: %v, %v", fi, err)
	}

	runT(t, "hub", "backup", "--keep", "-1", c).want(t, 2, "--keep -1")
	runT(t, "hub", "backup", "--dir", "", c).want(t, 2, "--dir ist leer")
	runT(t, "hub", "backup", "zuviel", c).want(t, 2, "Unerwartetes Argument: zuviel")
	runT(t, "hub", "backup", "--help").want(t, 0, "hub-<UTC JJJJMMTTThhmmssZ>-r<revision>.db", "--keep n")
	runT(t, "hub", "--help").want(t, 0, "kephalaion hub backup [--dir verzeichnis] [--keep n]", "kephalaion hub reset-id",
		"kephalaion hub backup --help", "kephalaion hub reset-id --help")

	// Ohne Hub in der config: nichts angelegt.
	nodeOnly := filepath.Join(dir, "nur-node.yaml")
	runT(t, "node", "init", "--db", "sqlite://"+filepath.Join(dir, "n", "node.db"), "--config", nodeOnly).want(t, 0)
	runT(t, "hub", "backup", "--config", nodeOnly).want(t, 1, "der Hub ist nicht eingerichtet")
	runT(t, "hub", "reset-id", "--config", nodeOnly).want(t, 1, "der Hub ist nicht eingerichtet")
}

// hub backup neben Schreibern, wie neben einem laufenden serve: Jede
// Sicherung trägt im Namen die Revision, die in ihrer db_info steht — die des
// Snapshots, nicht die des Hubs danach —, und die liegt nie über der Revision
// des Hubs nach dem Aufruf. Kein Schreiber scheitert daran.
func TestHubBackupBesideWriters(t *testing.T) {
	dir := isolate(t)
	cfg, c := backupSetup(t, dir)
	hubDB := filepath.Join(dir, "hub.db")
	at := time.Date(2026, 11, 14, 2, 0, 0, 0, time.UTC)
	old := backupNow
	backupNow = func() time.Time { at = at.Add(time.Second); return at }
	t.Cleanup(func() { backupNow = old })

	ctx := context.Background()
	w := hubStore(t, cfg)
	const rounds = 40
	done := make(chan error, 1)
	go func() {
		for i := range rounds {
			if _, err := w.PutDocument(ctx, "wissen", "d"+strconv.Itoa(i%5)+".md", "Runde "+strconv.Itoa(i)+"\n"); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	name := regexp.MustCompile(`Sicherung: (\S+/hub-[0-9TZ]+-r([0-9]+)\.db) `)
	var files []string
	for range 5 {
		r := runT(t, "hub", "backup", c)
		r.want(t, 0)
		m := name.FindStringSubmatch(r.out)
		if m == nil {
			t.Fatalf("Ausgabe ohne Pfad: %s", r.out)
		}
		after := hubInfoOf(t, hubDB).Revision
		info := hubInfoOf(t, m[1])
		if strconv.FormatInt(info.Revision, 10) != m[2] {
			t.Errorf("%s: db_info nennt Revision %d", m[1], info.Revision)
		}
		if info.Revision > after {
			t.Errorf("%s: Revision %d über der des Hubs danach (%d)", m[1], info.Revision, after)
		}
		files = append(files, m[1])
	}
	if err := <-done; err != nil {
		t.Fatalf("Schreiber neben hub backup: %v", err)
	}
	if got := hubInfoOf(t, hubDB).Revision; got != rounds {
		t.Errorf("Revision des Hubs %d, erwartet %d", got, rounds)
	}
	if got := names(t, filepath.Join(dir, "backups")); len(got) != len(files) {
		t.Errorf("in backups/: %v", got)
	}
}

// Die Rotation löscht die ältesten Sicherungen über --keep hinaus und fasst
// nur Dateien an, die wie eine Sicherung heißen: Alles andere im Verzeichnis
// bleibt, auch was ähnlich aussieht. --keep 0 löscht nichts.
func TestHubBackupRotation(t *testing.T) {
	dir := isolate(t)
	_, c := backupSetup(t, dir)
	putDoc(t, c, "a.md", "A\n")
	backups := filepath.Join(dir, "backups")
	old := []string{
		"hub-20260101T000000Z-r1.db",
		"hub-20260102T000000Z-r1.db",
		"hub-20260103T000000Z-r1.db",
		// Dieselbe Sekunde: die höhere Revision ist die jüngere, 10 nach 9.
		"hub-20260104T000000Z-r9.db",
		"hub-20260104T000000Z-r10.db",
	}
	// Kein Name einer Sicherung, also nie gelöscht — auch nicht das
	// Verzeichnis, das wie eine heißt.
	others := []string{
		"notizen.txt",
		"hub.db",
		"hub-20260101T000000Z-r1.db.bak",
		"hub-20260101T000000Z-r1.db-wal",
		"hub-alt-r1.db",
		"hub-20260101T000000Z-rx.db",
		"xhub-20260101T000000Z-r1.db",
		".hub-backup-01JABCDEFGHJKMNPQRSTVWXYZ0.tmp",
	}
	for _, name := range slices.Concat(old, others) {
		writeFile(t, filepath.Join(backups, name), "x")
	}
	asDir := "hub-20250101T000000Z-r1.db"
	if err := os.Mkdir(filepath.Join(backups, asDir), 0o700); err != nil {
		t.Fatal(err)
	}
	others = append(others, asDir)
	backupAt(t, time.Date(2026, 11, 14, 2, 15, 0, 0, time.UTC))
	fresh := "hub-20261114T021500Z-r1.db"

	// --keep 0: nichts gelöscht.
	runT(t, "hub", "backup", "--keep", "0", c).want(t, 0, "6 Stände behalten, 0 gelöscht.")
	want := slices.Concat(old, others, []string{fresh})
	slices.Sort(want)
	if got := names(t, backups); !slices.Equal(got, want) {
		t.Fatalf("nach --keep 0:\n  %v\nerwartet\n  %v", got, want)
	}

	// --keep 3: die neue und die zwei jüngsten alten bleiben.
	backupAt(t, time.Date(2026, 11, 14, 2, 15, 1, 0, time.UTC))
	fresh2 := "hub-20261114T021501Z-r1.db"
	runT(t, "hub", "backup", "--keep", "3", c).want(t, 0, fresh2, "3 Stände behalten, 4 gelöscht.")
	want = slices.Concat(others, []string{fresh, fresh2, "hub-20260104T000000Z-r10.db"})
	slices.Sort(want)
	if got := names(t, backups); !slices.Equal(got, want) {
		t.Fatalf("nach --keep 3:\n  %v\nerwartet\n  %v", got, want)
	}

	// Eine zurückgestellte Uhr: Die eben geschriebene Sicherung bleibt, auch
	// wenn ihr Zeitstempel der älteste ist.
	backupAt(t, time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC))
	past := "hub-20250601T000000Z-r1.db"
	runT(t, "hub", "backup", "--keep", "1", c).want(t, 0, past, "1 Stand behalten, 3 gelöscht.")
	want = slices.Concat(others, []string{past})
	slices.Sort(want)
	if got := names(t, backups); !slices.Equal(got, want) {
		t.Fatalf("nach --keep 1:\n  %v\nerwartet\n  %v", got, want)
	}
}

// Besteht die geschriebene Datei die Prüfung nicht — hier: Die Schemafassung
// passt nicht zu diesem Binary —, wird sie gelöscht: Exit 1, nichts bleibt
// liegen, auch keine verdeckte Datei, und die Rotation läuft nicht.
func TestHubBackupFailedCheck(t *testing.T) {
	dir := isolate(t)
	_, c := backupSetup(t, dir)
	hubDB := filepath.Join(dir, "hub.db")
	backups := filepath.Join(dir, "backups")
	keep := "hub-20260101T000000Z-r1.db"
	writeFile(t, filepath.Join(backups, keep), "x")

	ctx := context.Background()
	db, err := sqlitedb.Open(ctx, hubDB)
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlitedb.SetInfo(ctx, db, sqlitedb.KeySchemaVersion, strconv.Itoa(hubstore.SchemaVersion+1)); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	r := runT(t, "hub", "backup", "--keep", "1", c)
	r.want(t, 1, "hub backup: Snapshot verworfen", "Schemafassung")
	if r.out != "" {
		t.Errorf("Ausgabe trotz Fehler: %s", r.out)
	}
	if got := names(t, backups); !slices.Equal(got, []string{keep}) {
		t.Errorf("in backups/ nach dem Fehler: %v", got)
	}

	// Ebenso eine Datei, die gar keine Hub-Datenbank ist: die des Nodes.
	if err := sqlitedb.Remove(hubDB); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "node.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hubDB, data, 0o600); err != nil {
		t.Fatal(err)
	}
	runT(t, "hub", "backup", c).want(t, 1, "Snapshot verworfen", "Rolle node")
	if got := names(t, backups); !slices.Equal(got, []string{keep}) {
		t.Errorf("in backups/ nach dem Fehler: %v", got)
	}

	// Ohne Datenbank: Fehler, kein Rest, kein neues Verzeichnis.
	if err := sqlitedb.Remove(hubDB); err != nil {
		t.Fatal(err)
	}
	runT(t, "hub", "backup", c).want(t, 1, "Datenbankdatei fehlt")
	if got := names(t, backups); !slices.Equal(got, []string{keep}) {
		t.Errorf("in backups/ ohne Datenbank: %v", got)
	}
	other := filepath.Join(dir, "anders")
	runT(t, "hub", "backup", "--dir", other, c).want(t, 1, "Datenbankdatei fehlt")
	if exists(other) {
		t.Errorf("%s ohne Datenbank angelegt", other)
	}
	runT(t, "hub", "reset-id", c).want(t, 1, "Datenbankdatei fehlt")
	if exists(lockPath(hubDB)) {
		t.Error("reset-id ohne Datenbank hat eine Sperrdatei angelegt")
	}
}

var resetIDLine = regexp.MustCompile(`(?m)^hub_id: ([0-9A-Z]{26}) → ([0-9A-Z]{26})$`)

// hub reset-id vergibt eine neue hub_id und nennt alt → neu. Hält serve die
// Sperre des Hubs, scheitert es, ohne etwas zu ändern.
func TestHubResetID(t *testing.T) {
	dir := isolate(t)
	_, c := backupSetup(t, dir)
	hubDB := filepath.Join(dir, "hub.db")
	putDoc(t, c, "a.md", "A\n")
	before := hubInfoOf(t, hubDB)

	// serve hält die Sperre.
	lock, err := takeLock(lockPath(hubDB))
	if err != nil {
		t.Fatal(err)
	}
	r := runT(t, "hub", "reset-id", c)
	r.want(t, 1, "hub reset-id: serve läuft für den Hub (Sperre "+lockPath(hubDB)+")", "nichts geändert")
	if r.out != "" {
		t.Errorf("Ausgabe trotz Sperre: %s", r.out)
	}
	if got := hubInfoOf(t, hubDB); got != before {
		t.Errorf("trotz Sperre geändert: %+v, davor %+v", got, before)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}

	r = runT(t, "hub", "reset-id", c)
	r.want(t, 0, "Jeder Node leert beim nächsten Abgleich seine Replica")
	m := resetIDLine.FindStringSubmatch(r.out)
	if m == nil {
		t.Fatalf("keine Zeile alt → neu:\n%s", r.out)
	}
	after := hubInfoOf(t, hubDB)
	if m[1] != before.HubID || m[2] != after.HubID || after.HubID == before.HubID {
		t.Errorf("Ausgabe %s → %s; davor %s, danach %s", m[1], m[2], before.HubID, after.HubID)
	}
	if _, err := ulid.ParseStrict(after.HubID); err != nil {
		t.Errorf("neue hub_id %q ist keine ULID: %v", after.HubID, err)
	}
	if after.Revision != before.Revision {
		t.Errorf("Revision %d, davor %d", after.Revision, before.Revision)
	}
	// Die Sperre ist wieder frei, status zeigt die neue hub_id.
	if running, err := serveRunning(hubDB); err != nil || running {
		t.Errorf("nach reset-id: Sperre gehalten %v, %v", running, err)
	}
	runT(t, "status", c).want(t, 0, after.HubID)

	runT(t, "hub", "reset-id", "zuviel", c).want(t, 2, "Unerwartetes Argument: zuviel")
	runT(t, "hub", "reset-id", "--help").want(t, 0, "Nur bei angehaltenem Dienst", "sudo -u kephalaion kephalaion hub reset-id")
}

// Wiederherstellen: Eine ältere Sicherung wird als hub.db eingesetzt, hub
// reset-id vergibt eine neue hub_id, und der Node — dessen Replica schon
// weiter war — leert sie beim nächsten Abgleich und gleicht von vorn ab. Der
// Hub hat bis dahin so weit weitergeschrieben, dass seine Revision den Stand
// der Replica erreicht: Am Stand allein erkennte der Node nichts, nur an der
// hub_id.
func TestHubRestoreResetsReplica(t *testing.T) {
	dir := isolate(t)
	_, c := backupSetup(t, dir)
	hubDB := filepath.Join(dir, "hub.db")
	putDoc(t, c, "a.md", "A\n")
	runT(t, "node", "sync", c).want(t, 0, "wissen: abgeglichen, 1 Zeile, Revision 1")
	backupAt(t, time.Date(2026, 11, 14, 2, 15, 0, 0, time.UTC))
	runT(t, "hub", "backup", c).want(t, 0, "Revision 1)")
	backup := filepath.Join(dir, "backups", "hub-20261114T021500Z-r1.db")

	// Nach der Sicherung geht es am Hub weiter, und der Node gleicht ab.
	putDoc(t, c, "b.md", "B\n")
	putDoc(t, c, "a.md", "A, später\n")
	runT(t, "node", "sync", c).want(t, 0, "wissen: abgeglichen, 2 Zeilen, Revision 3")
	old := hubInfoOf(t, hubDB)

	restoreHub(t, backup, hubDB)
	if got := hubInfoOf(t, hubDB); got.Revision != 1 || got.HubID != old.HubID {
		t.Fatalf("zurückgespielt: %+v", got)
	}
	r := runT(t, "hub", "reset-id", c)
	r.want(t, 0)
	fresh := hubInfoOf(t, hubDB)
	if m := resetIDLine.FindStringSubmatch(r.out); m == nil || m[1] != old.HubID || m[2] != fresh.HubID {
		t.Fatalf("reset-id: %s; alt %s, neu %s", r.out, old.HubID, fresh.HubID)
	}
	// Der Hub schreibt weiter, bis zur Revision, die die Replica schon hat.
	putDoc(t, c, "x.md", "X\n")
	putDoc(t, c, "y.md", "Y\n")
	if got := hubInfoOf(t, hubDB); got.Revision != 3 {
		t.Fatalf("Revision des Hubs %d, erwartet 3", got.Revision)
	}

	runT(t, "node", "sync", c).want(t, 0, "Hub eigen (hub_id "+fresh.HubID+")",
		"hub_id gewechselt ("+old.HubID+" → "+fresh.HubID+"); Replica geleert und von vorn abgeglichen",
		"wissen: abgeglichen, 3 Zeilen, Revision 3")
	// Die Replica trägt den Stand des Hubs, nichts aus der Zeit nach der
	// Sicherung.
	if r := runT(t, "node", "doc", "get", "eigen:wissen", "a.md", c); r.code != 0 || r.out != "A\n" {
		t.Errorf("a.md in der Replica: %d %q %s", r.code, r.out, r.errOut)
	}
	runT(t, "node", "doc", "get", "eigen:wissen", "b.md", c).want(t, 1, "gibt es in der Replica nicht")
	runT(t, "node", "doc", "get", "eigen:wissen", "x.md", c).want(t, 0, "X")
	runT(t, "status", c).want(t, 0, "      hub_id:      "+fresh.HubID, "wissen: Revision 3")

	// Danach wie immer.
	putDoc(t, c, "z.md", "Z\n")
	r = runT(t, "node", "sync", c)
	r.want(t, 0, "wissen: abgeglichen, 1 Zeile, Revision 4")
	if strings.Contains(r.out, "geleert") {
		t.Errorf("zweiter Abgleich leert erneut:\n%s", r.out)
	}
}

// Was nach der Sicherung geschah, kennt der zurückgespielte Hub nicht — auch
// kein Token, das danach rotiert wurde: Er hält wieder den Hash von davor. Der
// Node kommt mit seinem Token nicht mehr hinein und kann es auch nicht selbst
// rotieren (dazu müsste es gelten); erst ein neues vom Verwalter (hub node
// token) hilft.
func TestHubRestoreForgetsRotatedNodeToken(t *testing.T) {
	dir := isolate(t)
	_, c := backupSetup(t, dir)
	hubDB := filepath.Join(dir, "hub.db")
	putDoc(t, c, "a.md", "A\n")
	backupAt(t, time.Date(2026, 11, 14, 2, 15, 0, 0, time.UTC))
	runT(t, "hub", "backup", c).want(t, 0)
	backup := filepath.Join(dir, "backups", "hub-20261114T021500Z-r1.db")

	// Nach der Sicherung rotiert der Node sein Token.
	runT(t, "node", "hub", "rotate", "eigen", c).want(t, 0)
	runT(t, "node", "sync", c).want(t, 0, "wissen: abgeglichen")

	restoreHub(t, backup, hubDB)
	runT(t, "hub", "reset-id", c).want(t, 0)

	runT(t, "node", "sync", c).want(t, 1, "nicht angemeldet")
	runT(t, "node", "hub", "check", "eigen", c).want(t, 1, "nicht angemeldet")
	r := runT(t, "node", "hub", "rotate", "eigen", c)
	r.want(t, 1, "der Hub lehnt ab", "hub node token laptop")
	// Nichts gelernt, nichts geleert: Die Replica bleibt, bis der Node wieder
	// hineinkommt.
	runT(t, "node", "doc", "get", "eigen:wissen", "a.md", c).want(t, 0, "A")

	// Der Verwalter gibt ein neues Token aus, der Node trägt es ein und
	// rotiert es wie bei der Einrichtung.
	r = runT(t, "hub", "node", "token", "laptop", c)
	r.want(t, 0, "das alte gilt nicht mehr")
	runIn(t, tokenFrom(t, r.out)+"\n", "node", "hub", "token", "eigen", "--token-stdin", c).want(t, 0, "Token ersetzt")
	runT(t, "node", "hub", "rotate", "eigen", c).want(t, 0)
	runT(t, "node", "sync", c).want(t, 0, "hub_id gewechselt", "wissen: abgeglichen, 1 Zeile, Revision 1")
}
