package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// feed liefert Rows für RestoreInput aus einem gesammelten Abzug.
func feed(rows map[string][][]any) func(insert func(string, []any) error) error {
	return func(insert func(string, []any) error) error {
		for _, tb := range DumpTables() {
			for _, r := range rows[tb.Name] {
				if err := insert(tb.Name, r); err != nil {
					return err
				}
			}
		}
		return nil
	}
}

// multiset macht aus den Zeilen einer Tabelle eine sortierte Liste ihrer
// Textform: gleich als Multimenge, unabhängig von der Reihenfolge.
func multiset(rows [][]any) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = fmt.Sprintf("%#v", r)
	}
	slices.Sort(out)
	return out
}

// emptyHub sagt, ob keine Tabelle des Abzugs eine Zeile hat.
func emptyHub(t *testing.T, s *sqliteStore) bool {
	t.Helper()
	for _, tb := range dumpTables {
		n, err := count(context.Background(), s.db, dumpCount(tb))
		if err != nil {
			t.Fatal(err)
		}
		if n > 0 {
			return false
		}
	}
	return true
}

// Restore schreibt den Abzug Zeile für Zeile und die Revision; ein zweiter
// Abzug ist derselbe, je Tabelle als Multimenge. Eine eigene Zeile in
// actions gibt es nicht. Ohne HubID bleibt die hub_id des Ziels, mit HubID
// gilt die übergebene.
func TestRestoreRoundTrip(t *testing.T) {
	src := newStore(t)
	fillHub(t, src)
	info, rows := collect(t, src)
	ctx := context.Background()

	for _, keep := range []bool{false, true} {
		dst := newStore(t)
		own, err := dst.Info(ctx)
		if err != nil {
			t.Fatal(err)
		}
		in := RestoreInput{Revision: info.Revision, Rows: feed(rows)}
		if keep {
			in.HubID = info.HubID
		}
		if err := dst.Restore(ctx, in); err != nil {
			t.Fatal(err)
		}
		got, again := collect(t, dst)
		for _, tb := range DumpTables() {
			if !slices.Equal(multiset(again[tb.Name]), multiset(rows[tb.Name])) {
				t.Errorf("keep=%v, %s: %v, erwartet %v", keep, tb.Name, again[tb.Name], rows[tb.Name])
			}
		}
		want := Info{SchemaVersion: SchemaVersion, Revision: info.Revision, HubID: own.HubID}
		if keep {
			want.HubID = info.HubID
		}
		if got != want {
			t.Errorf("keep=%v: db_info %+v, erwartet %+v", keep, got, want)
		}
		// Der Hub arbeitet danach weiter: die nächste Revision folgt auf die
		// des Abzugs.
		res, err := dst.PutDocument(ctx, "wissen", "danach.md", "x\n")
		if err != nil || res.Revision != info.Revision+1 {
			t.Errorf("keep=%v: nach Restore schreiben: %+v, %v", keep, res, err)
		}
	}
}

// Restore schreibt nur in einen leeren Hub: Hat irgendeine Tabelle des
// Abzugs eine Zeile, nennt die Meldung sie, und nichts ändert sich. requests
// zählt nicht.
func TestRestoreNotEmpty(t *testing.T) {
	src := newStore(t)
	fillHub(t, src)
	info, rows := collect(t, src)
	ctx := context.Background()

	for table, fill := range map[string]func(s *sqliteStore) error{
		"collections": func(s *sqliteStore) error { return s.AddCollection(ctx, "x", "", false) },
		"settings":    func(s *sqliteStore) error { return s.SetSetting(ctx, SettingAlias, "x") },
		"actions": func(s *sqliteStore) error {
			return logAction(ctx, s.db, "probe", "")
		},
	} {
		dst := newStore(t)
		if err := fill(dst); err != nil {
			t.Fatal(err)
		}
		before, _ := collect(t, dst)
		err := dst.Restore(ctx, RestoreInput{Revision: info.Revision, Rows: feed(rows)})
		if !errors.Is(err, ErrNotEmpty) || !strings.Contains(err.Error(), "Tabelle "+table+" hat") {
			t.Errorf("%s: %v", table, err)
		}
		if after, _ := collect(t, dst); after != before {
			t.Errorf("%s: db_info geändert: %+v, davor %+v", table, after, before)
		}
	}

	// Ein Idempotenzschlüssel in requests hindert nicht.
	dst := newStore(t)
	if _, err := dst.db.ExecContext(ctx, `INSERT INTO requests (account, request_id, op, fingerprint, revision, ids, at)
		VALUES ('alice', 'r1', 'write', 'f', 1, '[]', 1)`); err != nil {
		t.Fatal(err)
	}
	if err := dst.Restore(ctx, RestoreInput{Revision: info.Revision, Rows: feed(rows)}); err != nil {
		t.Errorf("mit requests: %v", err)
	}
}

// Restore schreibt alles oder nichts: Scheitert das Einlesen mittendrin
// oder passt die Revision nicht zu den Zeilen, bleibt der Hub leer und auf
// Revision 0.
func TestRestoreAllOrNothing(t *testing.T) {
	src := newStore(t)
	fillHub(t, src)
	info, rows := collect(t, src)
	ctx := context.Background()

	stop := errors.New("abgebrochen")
	for name, in := range map[string]RestoreInput{
		"Abbruch": {Revision: info.Revision, Rows: func(insert func(string, []any) error) error {
			if err := feed(rows)(insert); err != nil {
				return err
			}
			return stop
		}},
		"Revision": {Revision: info.Revision - 1, Rows: feed(rows)},
		"Spalten": {Revision: info.Revision, Rows: func(insert func(string, []any) error) error {
			return insert("settings", []any{"a"})
		}},
		"Tabelle": {Revision: info.Revision, Rows: func(insert func(string, []any) error) error {
			return insert("requests", []any{"a"})
		}},
		"doppelt": {Revision: info.Revision, Rows: func(insert func(string, []any) error) error {
			if err := feed(rows)(insert); err != nil {
				return err
			}
			return insert("settings", rows["settings"][0])
		}},
	} {
		dst := newStore(t)
		before, err := dst.Info(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := dst.Restore(ctx, in); err == nil {
			t.Errorf("%s: kein Fehler", name)
		} else if name == "Abbruch" && !errors.Is(err, stop) {
			t.Errorf("%s: %v", name, err)
		} else if name == "Revision" && !strings.Contains(err.Error(), "nicht stimmig") {
			t.Errorf("%s: %v", name, err)
		}
		if !emptyHub(t, dst) {
			t.Errorf("%s: der Hub ist nicht mehr leer", name)
		}
		if after, _ := dst.Info(ctx); after != before {
			t.Errorf("%s: db_info %+v, davor %+v", name, after, before)
		}
	}
}
