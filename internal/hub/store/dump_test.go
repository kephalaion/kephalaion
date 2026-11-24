package store

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kephalaion/kephalaion/internal/contract"
	"github.com/kephalaion/kephalaion/internal/sqlq"
)

// Die Abfragen des Abzugs folgen derselben Regel wie queries.
func TestDumpQueriesPortable(t *testing.T) {
	for _, tb := range dumpTables {
		for _, text := range []string{dumpSelect(tb), dumpInsert(tb), dumpCount(tb)} {
			if err := sqlq.Check(text); err != nil {
				t.Errorf("%s: %v", tb.Name, err)
			}
		}
	}
	for _, m := range dumpMaxRevision {
		if err := sqlq.Check(m.query); err != nil {
			t.Errorf("%s: %v", m.table, err)
		}
	}
	if got := dumpInsert(dumpTables[3]); !strings.Contains(got, `"user"`) || !strings.Contains(got, "$8") {
		t.Errorf("accounts: %s", got)
	}
}

// Der Abzug trägt jede Tabelle des Schemas außer db_info und requests, jede
// mit allen Spalten in der Reihenfolge des Schemas, mit Typ und NULL wie
// dort. Eine neue Spalte oder Tabelle bricht diesen Test, bis sie im Abzug
// steht.
func TestDumpCoversSchema(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	rows, err := s.db.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		if name != "db_info" && name != "requests" && !strings.HasPrefix(name, "sqlite_") {
			tables = append(tables, name)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	var dumped []string
	for _, tb := range DumpTables() {
		dumped = append(dumped, tb.Name)
	}
	sorted := slices.Clone(dumped)
	slices.Sort(sorted)
	if !slices.Equal(sorted, tables) {
		t.Fatalf("Tabellen im Abzug %v, im Schema %v", sorted, tables)
	}
	for _, tb := range DumpTables() {
		cols, err := s.db.QueryContext(ctx, `SELECT name, type, "notnull", pk FROM pragma_table_info(?) ORDER BY cid`, tb.Name)
		if err != nil {
			t.Fatal(err)
		}
		var got []DumpColumn
		for cols.Next() {
			var name, typ string
			var notNull, pk int
			if err := cols.Scan(&name, &typ, &notNull, &pk); err != nil {
				t.Fatal(err)
			}
			// Ein Primärschlüssel aus Text ist in SQLite auch ohne NOT NULL
			// gemeint als NULL-frei, in PostgreSQL ist er es.
			got = append(got, DumpColumn{Name: name, Int: typ == "INTEGER", Null: notNull == 0 && pk == 0})
		}
		if err := cols.Err(); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(got, tb.Columns) {
			t.Errorf("%s: Schema %v, Abzug %v", tb.Name, got, tb.Columns)
		}
	}
}

// fillHub legt in s von jeder Tabelle des Abzugs Zeilen an, auch mit NULL:
// Collection, Node mit Recht, Account mit Rechten, Dokumente samt Löschmarke,
// settings, actions mit und ohne Revision.
func fillHub(t *testing.T, s Store) {
	t.Helper()
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.AddCollection(ctx, "wissen", "Notizen", false))
	must(s.AddCollection(ctx, "leer", "", true))
	_, err := s.AddNode(ctx, "laptop", "")
	must(err)
	must(s.Grant(ctx, "laptop", "wissen"))
	_, err = s.AddAccount(ctx, "alice", "alice", "die Erste")
	must(err)
	_, err = s.GrantAccount(ctx, "alice", "wissen", contract.Rights{Write: true, Dirs: []string{"docs"}})
	must(err)
	_, err = s.PutDocument(ctx, "wissen", "a.md", "A <&> \"ä\"\n")
	must(err)
	_, err = s.PutDocument(ctx, "wissen", "weg.md", "weg\n")
	must(err)
	_, err = s.DeleteDocument(ctx, "wissen", "weg.md")
	must(err)
	must(s.SetSetting(ctx, SettingAlias, "zentral"))
}

// collect liest einen Abzug in Zeilen je Tabelle.
func collect(t *testing.T, s Store) (Info, map[string][][]any) {
	t.Helper()
	got := map[string][][]any{}
	var order []string
	info, err := s.Dump(context.Background(), func(table string, values []any) error {
		if len(order) == 0 || order[len(order)-1] != table {
			order = append(order, table)
		}
		got[table] = append(got[table], values)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Tabelle für Tabelle in der Reihenfolge von DumpTables, jede einmal.
	var want []string
	for _, tb := range DumpTables() {
		if len(got[tb.Name]) > 0 {
			want = append(want, tb.Name)
		}
	}
	if !slices.Equal(order, want) {
		t.Errorf("Reihenfolge %v, erwartet %v", order, want)
	}
	return info, got
}

// Dump liefert jede Zeile mit den Typen der Spalten, NULL als nil, dazu
// db_info aus derselben Transaktion.
func TestDump(t *testing.T) {
	s := newStore(t)
	fillHub(t, s)
	ctx := context.Background()
	want, err := s.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	info, got := collect(t, s)
	if info != want {
		t.Errorf("Info %+v, erwartet %+v", info, want)
	}
	for _, tb := range DumpTables() {
		if len(got[tb.Name]) == 0 {
			t.Errorf("%s: keine Zeile", tb.Name)
		}
		for _, row := range got[tb.Name] {
			for i, c := range tb.Columns {
				switch row[i].(type) {
				case nil:
					if !c.Null {
						t.Errorf("%s.%s: nil", tb.Name, c.Name)
					}
				case int64:
					if !c.Int {
						t.Errorf("%s.%s: int64", tb.Name, c.Name)
					}
				case string:
					if c.Int {
						t.Errorf("%s.%s: string", tb.Name, c.Name)
					}
				default:
					t.Errorf("%s.%s: %T", tb.Name, c.Name, row[i])
				}
			}
		}
	}
	// collections nach Name, mit NULL in description und fetched_*.
	if c := got["collections"]; len(c) != 2 || c[0][0] != "leer" || c[0][1] != nil || c[0][3] != nil || c[1][1] != "Notizen" {
		t.Errorf("collections: %v", c)
	}
	// Die Löschmarke steht mit Inhalt NULL darin; eine SYSTEM:A:-Zeile auch.
	var tomb, system bool
	for _, d := range got["documents"] {
		if d[2] == "weg.md" && d[3] == nil && d[5] == int64(1) {
			tomb = true
		}
		if strings.HasPrefix(d[2].(string), "SYSTEM:A:") {
			system = true
		}
	}
	if !tomb || !system {
		t.Errorf("documents ohne Löschmarke (%v) oder SYSTEM:A:-Zeile (%v): %v", tomb, system, got["documents"])
	}
	// actions ohne Revision (collection.add) tragen NULL.
	var nullRev bool
	for _, a := range got["actions"] {
		if a[6] == nil {
			nullRev = true
		}
	}
	if !nullRev {
		t.Errorf("actions ohne Zeile mit revision NULL: %v", got["actions"])
	}
	if p := got["principal_names"]; len(p) != 2 {
		t.Errorf("principal_names: %v", p)
	}

	// Zweimal derselbe Stand: derselbe Abzug, Zeile für Zeile.
	_, again := collect(t, s)
	for _, tb := range DumpTables() {
		if !slices.EqualFunc(got[tb.Name], again[tb.Name], slices.Equal) {
			t.Errorf("%s: zweiter Abzug weicht ab", tb.Name)
		}
	}
}

// Dump liest in einer Lesetransaktion, die keinen Schreiber aufhält:
// Während der Abzug läuft, schreibt ein anderer — ohne zu warten, bis die
// Frist abliefe —, und der Abzug zeigt den Stand von seinem Beginn, mit der
// Revision davor. Mit BEGIN IMMEDIATE hinge der Schreiber bis zum Ende des
// Abzugs und scheiterte an der Frist.
func TestDumpBesideWriter(t *testing.T) {
	s := newStore(t)
	fillHub(t, s)
	ctx := context.Background()
	before, err := s.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	wrote := false
	var docs []string
	info, err := s.Dump(ctx, func(table string, values []any) error {
		if !wrote {
			wrote = true
			wctx, cancel := context.WithTimeout(ctx, 2*time.Second)
			defer cancel()
			start := time.Now()
			if _, err := s.PutDocument(wctx, "wissen", "neu.md", "neu\n"); err != nil {
				t.Errorf("Schreiber neben dem Abzug: %v", err)
			}
			if d := time.Since(start); d > time.Second {
				t.Errorf("Schreiber hat %v gewartet", d)
			}
		}
		if table == "documents" {
			docs = append(docs, values[2].(string))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if info.Revision != before.Revision {
		t.Errorf("Revision des Abzugs %d, vor dem Schreiber %d", info.Revision, before.Revision)
	}
	if slices.Contains(docs, "neu.md") {
		t.Errorf("der Abzug sieht, was nach seinem Beginn geschrieben wurde: %v", docs)
	}
	after, err := s.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if after.Revision != before.Revision+1 {
		t.Errorf("Revision nach dem Schreiber %d, davor %d", after.Revision, before.Revision)
	}
}
