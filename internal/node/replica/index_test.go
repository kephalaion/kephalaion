package replica

import (
	"context"
	"reflect"
	"strconv"
	"testing"

	"github.com/kephalaion/kephalaion/internal/contract"
	"github.com/kephalaion/kephalaion/internal/sections"
)

// indexed liest die Abschnitte im Index als „Collection Name#Anker“, nach
// Collection, Name und Reihenfolge.
func indexed(t *testing.T, r *Replica) []string {
	t.Helper()
	rows, err := r.db.Query(`SELECT collection, name, anchor FROM sections ORDER BY collection, name, ord`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var c, n, a string
		if err := rows.Scan(&c, &n, &a); err != nil {
			t.Fatal(err)
		}
		out = append(out, c+" "+n+"#"+a)
	}
	var fts int
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM sections_fts`).Scan(&fts); err != nil {
		t.Fatal(err)
	}
	if fts != len(out) {
		t.Errorf("sections_fts hat %d Zeilen, sections %d", fts, len(out))
	}
	return out
}

// matches zählt die Abschnitte, die eine FTS5-Anfrage findet.
func matches(t *testing.T, r *Replica, query string) int {
	t.Helper()
	var n int
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM sections_fts WHERE sections_fts MATCH ?`, query).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func sysRow(id, coll, name string, rev int64, content string) contract.Row {
	r := row(id, coll, name, rev, 1, content)
	r.CreatedBy, r.UpdatedBy = contract.SystemAuthor, contract.SystemAuthor
	return r
}

func applyRows(t *testing.T, r *Replica, rows ...contract.Row) {
	t.Helper()
	if _, err := r.apply(context.Background(), page{rows: rows, advance: map[string]int64{}}); err != nil {
		t.Fatal(err)
	}
}

// Der Index folgt den Zeilen, die apply schreibt: nur Markdown unter docs/,
// ohne index.md, docs/README.md und Mahnungen; Änderung, Umbenennen,
// Löschmarke und das Entfernen einer Collection ziehen ihn nach.
func TestIndexFollowsRows(t *testing.T) {
	doc := "---\ntitle: Guide\nquestions:\n  - How do I install it?\n---\n# Setup\nInstall the binary.\n## Usage\nRun serve.\n"
	r := readReplica(t, 10,
		row("d1", "w", "docs/guide.md", 1, 1, doc),
		row("d2", "w", "docs/a/README.md", 2, 1, "# A\nAbout a.\n"),
		row("d3", "w", "notes/x.md", 3, 1, "# X\noutside\n"),
		row("d4", "w", "docs/pic.svg", 4, 1, "<svg/>"),
		sysRow("s1", "w", "docs/index.md", 5, "# Documents\n"),
		sysRow("s2", "w", "docs/README.md", 6, "# docs\n"),
		sysRow("s3", "w", "docs/b/README.md", 7, "# b\nnotice\n"),
		sysRow("s4", "w", "docs/b/index.md", 8, "# Documents\n"),
		row("d5", "v", "docs/other.md", 9, 1, "# Other\ntext\n"),
	)
	want := []string{"v docs/other.md#other", "w docs/a/README.md#a", "w docs/guide.md#setup", "w docs/guide.md#usage"}
	if got := indexed(t, r); !reflect.DeepEqual(got, want) {
		t.Fatalf("Index:\n%v\nerwartet\n%v", got, want)
	}
	// Felder des Dokuments an jedem Abschnitt, porter: installing → install.
	if n := matches(t, r, "questions:installing"); n != 2 {
		t.Errorf("questions an jedem Abschnitt: %d", n)
	}
	if n := matches(t, r, "title:guide"); n != 2 {
		t.Errorf("title an jedem Abschnitt: %d", n)
	}
	if n := matches(t, r, "body:serve"); n != 1 {
		t.Errorf("body: %d", n)
	}

	// Eine ältere Revision übergeht der Upsert, und der Index bleibt.
	applyRows(t, r, row("d1", "w", "docs/guide.md", 20, 1, "# Neu\nfresh text\n"))
	applyRows(t, r, row("d1", "w", "docs/guide.md", 15, 1, "# Alt\nstale words\n"))
	if n, m := matches(t, r, "fresh"), matches(t, r, "stale"); n != 1 || m != 0 {
		t.Errorf("ältere Revision: fresh %d, stale %d", n, m)
	}
	// Umbenennen innerhalb, dann aus docs/ hinaus; die Mahnung übernommen
	// (updated_by eines Users): jetzt im Index.
	applyRows(t, r, row("d1", "w", "docs/manual/guide.md", 21, 1, "# Neu\nfresh text\n"),
		row("s3", "w", "docs/b/README.md", 22, 1, "# B\nreal readme\n"))
	want = []string{"v docs/other.md#other", "w docs/a/README.md#a", "w docs/b/README.md#b", "w docs/manual/guide.md#neu"}
	if got := indexed(t, r); !reflect.DeepEqual(got, want) {
		t.Errorf("nach rename:\n%v", got)
	}
	applyRows(t, r, row("d1", "w", "alt/guide.md", 23, 1, "# Neu\nfresh text\n"), tomb("d2", "w", "docs/a/README.md", 24))
	want = []string{"v docs/other.md#other", "w docs/b/README.md#b"}
	if got := indexed(t, r); !reflect.DeepEqual(got, want) {
		t.Errorf("nach rename hinaus und Löschmarke:\n%v", got)
	}
	// Collection entfernen: ihre Abschnitte gehen mit.
	if _, err := r.apply(context.Background(), page{drop: []string{"w"}}); err != nil {
		t.Fatal(err)
	}
	if got := indexed(t, r); !reflect.DeepEqual(got, []string{"v docs/other.md#other"}) {
		t.Errorf("nach dropCollection: %v", got)
	}
	// reset leert ihn ganz.
	if err := r.reset(context.Background(), r.HubID()); err != nil {
		t.Fatal(err)
	}
	if got := indexed(t, r); len(got) != 0 {
		t.Errorf("nach reset: %v", got)
	}
}

// Passt die Fassung der Zerlegung nicht, baut apply den Index neu.
func TestIndexVersion(t *testing.T) {
	r := readReplica(t, 10, row("d1", "w", "docs/guide.md", 1, 1, "# A\na\n# B\nb\n"))
	if _, err := r.db.Exec(`DELETE FROM sections; DELETE FROM sections_fts; UPDATE db_info SET value = '0' WHERE key = ?`,
		KeySectionsVersion); err != nil {
		t.Fatal(err)
	}
	applyRows(t, r)
	if got := indexed(t, r); !reflect.DeepEqual(got, []string{"w docs/guide.md#a", "w docs/guide.md#b"}) {
		t.Errorf("neu gebaut: %v", got)
	}
	var v string
	if err := r.db.QueryRow(`SELECT value FROM db_info WHERE key = ?`, KeySectionsVersion).Scan(&v); err != nil ||
		v != strconv.Itoa(sections.Version) {
		t.Errorf("Fassung %q, %v", v, err)
	}
}
