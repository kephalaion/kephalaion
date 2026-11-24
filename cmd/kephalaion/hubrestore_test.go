package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/kephalaion/kephalaion/internal/config"
	"github.com/kephalaion/kephalaion/internal/contract"
	"github.com/kephalaion/kephalaion/internal/hub/archive"
	hubstore "github.com/kephalaion/kephalaion/internal/hub/store"
	"github.com/kephalaion/kephalaion/internal/sqlitedb"
)

// newTargetHub richtet einen Ziel-Hub mit eigener config ein, wie
// installation.md es beschreibt: hub init --config <neu> --db <neu>. Es
// liefert die config und den Pfad der Datenbank.
func newTargetHub(t *testing.T, dir, name string) (cfg, db string) {
	t.Helper()
	cfg = filepath.Join(dir, name+".yaml")
	db = filepath.Join(dir, name+".db")
	runT(t, "hub", "init", "--config", cfg, "--db", "sqlite://"+db).want(t, 0)
	return cfg, db
}

// switchHubDB stellt in der config des Dienstes im Abschnitt hub: nur db um;
// listen und der Abschnitt node: bleiben.
func switchHubDB(t *testing.T, cfgPath, db string) {
	t.Helper()
	cfg, _, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Hub.DB = "sqlite://" + db
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
}

// hubExportTo exportiert den Hub der config nach path.
func hubExportTo(t *testing.T, cfg, path string) {
	t.Helper()
	runT(t, "hub", "export", "--output", path, "--config", cfg).want(t, 0)
}

// partsOf liest jeden Teil eines Exports als Multimenge seiner Zeilen.
func partsOf(t *testing.T, path string) (archive.Manifest, map[string][]string) {
	t.Helper()
	a, err := archive.Open(path, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	out := map[string][]string{}
	for _, tb := range hubstore.DumpTables() {
		if err := a.Rows(tb, func(values []any) error {
			out[tb.Name] = append(out[tb.Name], fmt.Sprintf("%#v", values))
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		slices.Sort(out[tb.Name])
	}
	return a.Manifest, out
}

// fillRich legt am Hub der config c von jeder Tabelle Zeilen an:
// Dokumente samt Löschmarke, Accounts mit Rechten, einen gesperrten, eine
// setting, den geholten Stand einer Collection mit Quelle, dazu einen
// Idempotenzschlüssel in requests, der nicht in den Export gehört.
func fillRich(t *testing.T, cfg, c, hubDB string) {
	t.Helper()
	putDoc(t, c, "a.md", "A\n")
	putDoc(t, c, "sub/b.md", "B <&>\n")
	putDoc(t, c, "weg.md", "weg\n")
	runT(t, "hub", "doc", "rm", "wissen", "weg.md", c).want(t, 0)
	runT(t, "hub", "account", "add", "alice", c).want(t, 0)
	runT(t, "hub", "account", "grant", "alice", "wissen", "--write", c).want(t, 0)
	runT(t, "hub", "account", "add", "bob", c).want(t, 0)
	runT(t, "hub", "account", "grant", "bob", "wissen", c).want(t, 0)
	runT(t, "hub", "account", "lock", "bob", c).want(t, 0)
	ctx := context.Background()
	s := hubStore(t, cfg)
	if err := s.SetSetting(ctx, hubstore.SettingAlias, "zentral"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GrantAccount(ctx, "alice", "wissen", contract.Rights{Write: true, Dirs: []string{"docs"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sqlitedb.Open(ctx, hubDB)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, stmt := range []string{
		`UPDATE collections SET fetched_tag = 'v1.2.3', fetched_at = 1700000000000 WHERE name = 'wissen'`,
		`INSERT INTO requests (account, request_id, op, fingerprint, revision, ids, at)
			VALUES ('alice', 'r1', 'write', 'f', 1, '[]', 1)`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
}

// Rundreise: Export → Restore → Export ergibt denselben Inhalt, jeder Teil
// als Multimenge, dazu dieselbe Revision; mit --keep-hub-id dieselbe hub_id,
// ohne die des neuen Hubs. Der Restore schreibt nichts Eigenes — auch keine
// Zeile in actions —, requests geht nicht mit.
func TestHubExportRestoreRoundTrip(t *testing.T) {
	dir := isolate(t)
	cfg, c := backupSetup(t, dir)
	hubDB := filepath.Join(dir, "hub.db")
	fillRich(t, cfg, c, hubDB)
	first := filepath.Join(dir, "eins.tar.gz")
	hubExportTo(t, cfg, first)
	m1, parts1 := partsOf(t, first)
	if !m1.ServeStopped || m1.Rows["principal_names"] != 3 || m1.Rows["settings"] != 1 {
		t.Fatalf("erster Export: %+v", m1)
	}
	if !strings.Contains(strings.Join(parts1["collections"], "\n"), `"v1.2.3"`) {
		t.Errorf("geholter Stand fehlt: %v", parts1["collections"])
	}

	for _, keep := range []bool{true, false} {
		name := fmt.Sprintf("ziel-%v", keep)
		cfg2, db2 := newTargetHub(t, dir, name)
		fresh := hubInfoOf(t, db2)
		args := []string{"hub", "restore", "--config", cfg2, first}
		if keep {
			args = append(args, "--keep-hub-id")
		}
		r := runT(t, args...)
		r.want(t, 0, "Zurückgespielt aus "+first+": Revision "+fmt.Sprint(m1.Revision)+",")
		if keep {
			r.want(t, 0, "hub_id "+m1.HubID+" übernommen")
		} else {
			r.want(t, 0, "hub_id "+fresh.HubID+" aus hub init", "leert beim nächsten Abgleich seine Replica")
		}
		if running, err := serveRunning(db2); err != nil || running {
			t.Errorf("nach restore: Sperre gehalten %v, %v", running, err)
		}

		second := filepath.Join(dir, name+".tar.gz")
		hubExportTo(t, cfg2, second)
		m2, parts2 := partsOf(t, second)
		for _, tb := range hubstore.DumpTables() {
			if !slices.Equal(parts2[tb.Name], parts1[tb.Name]) {
				t.Errorf("keep=%v, %s:\n%v\nerwartet\n%v", keep, tb.Name, parts2[tb.Name], parts1[tb.Name])
			}
		}
		info := hubInfoOf(t, db2)
		wantID := fresh.HubID
		if keep {
			wantID = m1.HubID
		}
		if m2.Revision != m1.Revision || info.Revision != m1.Revision || m2.HubID != wantID || info.HubID != wantID {
			t.Errorf("keep=%v: Manifest %+v, db_info %+v; erwartet Revision %d, hub_id %s", keep, m2, info, m1.Revision, wantID)
		}
		if m2.Files != m1.Files {
			t.Errorf("keep=%v: %d Dateien, erwartet %d", keep, m2.Files, m1.Files)
		}
	}
}

// rewriteExport schreibt einen Export neu, mit geändertem Manifest.
func rewriteExport(t *testing.T, src, dst string, change func(m map[string]any)) {
	t.Helper()
	f, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		if h.Name == archive.ManifestName {
			var m map[string]any
			if err := json.Unmarshal(data, &m); err != nil {
				t.Fatal(err)
			}
			change(m)
			if data, err = json.Marshal(m); err != nil {
				t.Fatal(err)
			}
		}
		h.Size = int64(len(data))
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

// emptyTarget prüft, dass der Ziel-Hub so leer ist wie nach hub init.
func emptyTarget(t *testing.T, cfg string, want hubstore.Info) {
	t.Helper()
	db := hubStore(t, cfg)
	ctx := context.Background()
	info, err := db.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	if _, err := db.Dump(ctx, func(string, []any) error { n++; return nil }); err != nil {
		t.Fatal(err)
	}
	if info != want || n != 0 {
		t.Errorf("Ziel-Hub geändert: %+v, %d Zeilen; erwartet %+v, leer", info, n, want)
	}
	_ = db.Close()
}

// hub restore lehnt ab, ohne etwas zu ändern: bei laufendem serve, in einen
// Hub mit Inhalt, --keep-hub-id nach einem Export im laufenden Betrieb, eine
// unbekannte Format- oder Schemafassung und Teile, deren Zeilen nicht zum
// Manifest passen — Letzteres erst mitten im Einspielen.
func TestHubRestoreRejects(t *testing.T) {
	dir := isolate(t)
	cfg, c := backupSetup(t, dir)
	hubDB := filepath.Join(dir, "hub.db")
	putDoc(t, c, "a.md", "A\n")
	putDoc(t, c, "b.md", "B\n")
	good := filepath.Join(dir, "gut.tar.gz")
	hubExportTo(t, cfg, good)
	cfg2, db2 := newTargetHub(t, dir, "ziel")
	fresh := hubInfoOf(t, db2)
	c2 := "--config=" + cfg2

	// serve läuft für den Ziel-Hub.
	lock, err := takeLock(lockPath(db2))
	if err != nil {
		t.Fatal(err)
	}
	runT(t, "hub", "restore", good, c2).want(t, 1, "serve läuft für den Hub (Sperre "+lockPath(db2)+")", "nichts geändert")
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	emptyTarget(t, cfg2, fresh)

	// In den Hub mit Inhalt: die belegte Tabelle steht in der Meldung.
	before := hubInfoOf(t, hubDB)
	runT(t, "hub", "restore", good, c).want(t, 1, "Hub nicht leer: Tabelle collections hat 1 Zeilen", "nichts geändert")
	if got := hubInfoOf(t, hubDB); got != before {
		t.Errorf("Quelle geändert: %+v, davor %+v", got, before)
	}

	// Ein Export neben laufendem serve: ohne Option geht er, --keep-hub-id nicht.
	lock, err = takeLock(lockPath(hubDB))
	if err != nil {
		t.Fatal(err)
	}
	running := filepath.Join(dir, "laufend.tar.gz")
	hubExportTo(t, cfg, running)
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	runT(t, "hub", "restore", "--keep-hub-id", running, c2).want(t, 1,
		"--keep-hub-id nur für einen Export bei angehaltenem serve (serve_stopped: true)", "nichts geändert")
	emptyTarget(t, cfg2, fresh)

	bad := filepath.Join(dir, "schlecht.tar.gz")
	for name, c := range map[string]struct {
		change func(m map[string]any)
		want   string
	}{
		"Format": {func(m map[string]any) { m["format"] = 2 }, "Format-Fassung 2 des Exports ist unbekannt"},
		"Schema": {func(m map[string]any) { m["schema_version"] = hubstore.SchemaVersion - 1 },
			fmt.Sprintf("Schemafassung %d des Exports: dieses Binary kennt keine Abbildung auf seine Fassung %d",
				hubstore.SchemaVersion-1, hubstore.SchemaVersion)},
		"Zeilen": {func(m map[string]any) { m["rows"].(map[string]any)["documents"] = 3 },
			"documents.jsonl: 2 Zeilen, das Manifest nennt 3"},
		"hub_id": {func(m map[string]any) { m["hub_id"] = "keine" }, `hub_id "keine" des Exports ist keine ULID`},
	} {
		rewriteExport(t, good, bad, c.change)
		args := []string{"hub", "restore", bad, c2}
		if name == "hub_id" {
			args = append(args, "--keep-hub-id")
		}
		runT(t, args...).want(t, 1, c.want, "nichts geändert")
		emptyTarget(t, cfg2, fresh)
	}
	// Keine Arbeitsdateien bleiben neben der Datenbank.
	if got, _ := filepath.Glob(filepath.Join(dir, ".hub-restore-*")); len(got) != 0 {
		t.Errorf("Arbeitsdateien bleiben: %v", got)
	}

	runT(t, "hub", "restore", filepath.Join(dir, "fehlt.tar.gz"), c2).want(t, 1, "fehlt.tar.gz")
	runT(t, "hub", "restore", c2).want(t, 2, "Es fehlt: datei")
	runT(t, "hub", "restore", good, "zuviel", c2).want(t, 2, "Unerwartetes Argument: zuviel")
	runT(t, "hub", "restore", "--help").want(t, 0, "--keep-hub-id", "sudo -u kephalaion kephalaion hub restore",
		"kephalaion hub init --config <neue config>")
	runT(t, "hub", "--help").want(t, 0, "kephalaion hub restore [--keep-hub-id] <datei>", "kephalaion hub restore --help")

	// Danach geht der gute Export.
	runT(t, "hub", "restore", good, c2).want(t, 0, "Revision 2,")
}

// Der Node nach einem Restore: Mit --keep-hub-id gleicht er ohne Leeren
// weiter ab; ohne die Option erkennt er die neue hub_id, leert seine Replica
// und gleicht von vorn ab. In beiden Fällen bleibt sein Token gültig — der
// Hash steht im Export.
func TestHubRestoreNodes(t *testing.T) {
	dir := isolate(t)
	cfg, c := backupSetup(t, dir)
	putDoc(t, c, "a.md", "A\n")
	runT(t, "node", "sync", c).want(t, 0, "wissen: abgeglichen, 1 Zeile, Revision 1")
	old := hubInfoOf(t, filepath.Join(dir, "hub.db"))

	// Umzug mit --keep-hub-id: Export bei angehaltenem serve, Ziel-Hub mit
	// eigener config, restore, in der config des Dienstes nur db umstellen.
	first := filepath.Join(dir, "eins.tar.gz")
	hubExportTo(t, cfg, first)
	cfg2, db2 := newTargetHub(t, dir, "neu")
	runT(t, "hub", "restore", "--keep-hub-id", "--config", cfg2, first).want(t, 0, "übernommen")
	switchHubDB(t, cfg, db2)
	putDoc(t, c, "b.md", "B\n")
	r := runT(t, "node", "sync", c)
	r.want(t, 0, "Hub eigen (hub_id "+old.HubID+")", "wissen: abgeglichen, 1 Zeile, Revision 2")
	if strings.Contains(r.out, "geleert") {
		t.Errorf("Abgleich nach Restore mit --keep-hub-id leert:\n%s", r.out)
	}
	runT(t, "node", "doc", "get", "eigen:wissen", "a.md", c).want(t, 0, "A")

	// Wiederherstellen ohne die Option: neue hub_id, der Node fängt von vorn an.
	second := filepath.Join(dir, "zwei.tar.gz")
	hubExportTo(t, cfg, second)
	cfg3, db3 := newTargetHub(t, dir, "wieder")
	fresh := hubInfoOf(t, db3)
	runT(t, "hub", "restore", "--config", cfg3, second).want(t, 0, "aus hub init")
	switchHubDB(t, cfg, db3)
	runT(t, "node", "sync", c).want(t, 0, "Hub eigen (hub_id "+fresh.HubID+")",
		"hub_id gewechselt ("+old.HubID+" → "+fresh.HubID+"); Replica geleert und von vorn abgeglichen",
		"wissen: abgeglichen, 2 Zeilen, Revision 2")
	runT(t, "node", "doc", "get", "eigen:wissen", "b.md", c).want(t, 0, "B")

	// Danach wie immer.
	putDoc(t, c, "c.md", "C\n")
	r = runT(t, "node", "sync", c)
	r.want(t, 0, "wissen: abgeglichen, 1 Zeile, Revision 3")
	if strings.Contains(r.out, "geleert") {
		t.Errorf("zweiter Abgleich leert erneut:\n%s", r.out)
	}
}
