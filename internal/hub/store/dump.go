package store

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	"github.com/kephalaion/kephalaion/internal/sqlitedb"
)

// Der Abzug (Task 039) ist der vollständige Inhalt der Hub-Datenbank, Tabelle
// für Tabelle, für hub export und hub restore: alle Tabellen außer requests
// (Idempotenzschlüssel, laufen ab) und db_info, aus der nur Schemafassung,
// Revision und hub_id mitgehen. Das Format der Datei kennt der Store nicht;
// er liefert Zeilen und nimmt Zeilen.
//
// Die Abfragen entstehen aus DumpTables (dumpSelect, dumpInsert, dumpCount),
// damit Lesen und Schreiben dieselben Spalten in derselben Reihenfolge
// benutzen; ein Test prüft sie mit sqlq.Check wie queries.

// DumpColumn ist eine Spalte im Abzug. Int: eine Ganzzahl (int64), sonst Text
// (string); Null: NULL (nil) ist erlaubt.
type DumpColumn struct {
	Name string
	Int  bool
	Null bool
}

// DumpTable ist eine Tabelle im Abzug: Name, Spalten in der Reihenfolge des
// Schemas und die feste Ordnung der Zeilen beim Lesen.
type DumpTable struct {
	Name    string
	Columns []DumpColumn
	// order ist die ORDER BY-Klausel: der Primärschlüssel, bei actions (ohne
	// einen) alle Spalten mit NULLS FIRST — PostgreSQL sortiert NULL sonst
	// ans Ende, SQLite an den Anfang. Nichts hängt an der Reihenfolge; sie
	// macht nur zwei Abzüge desselben Stands gleich.
	order string
}

func text(name string) DumpColumn     { return DumpColumn{Name: name} }
func textNull(name string) DumpColumn { return DumpColumn{Name: name, Null: true} }
func num(name string) DumpColumn      { return DumpColumn{Name: name, Int: true} }
func numNull(name string) DumpColumn  { return DumpColumn{Name: name, Int: true, Null: true} }

// dumpTables sind die Tabellen des Abzugs in der Reihenfolge, in der Restore
// sie schreibt: erst collections und nodes, dann node_collections, das auf
// beide verweist.
var dumpTables = []DumpTable{
	{Name: "collections", Columns: []DumpColumn{text("name"), textNull("description"), num("public"),
		textNull("fetched_tag"), numNull("fetched_at"), num("created_at"), text("created_by")}, order: "name"},
	{Name: "nodes", Columns: []DumpColumn{text("name"), textNull("description"), text("token_hash"), num("locked"),
		num("created_at"), text("created_by")}, order: "name"},
	{Name: "node_collections", Columns: []DumpColumn{text("node"), text("collection")}, order: "node, collection"},
	{Name: "accounts", Columns: []DumpColumn{text("name"), text("user"), textNull("description"), text("token_hash"),
		num("locked"), textNull("locked_rights"), num("created_at"), text("created_by")}, order: "name"},
	{Name: "principal_names", Columns: []DumpColumn{text("name"), text("kind")}, order: "name"},
	{Name: "documents", Columns: []DumpColumn{text("id"), text("collection"), text("name"), textNull("content"),
		textNull("meta"), num("deleted"), num("revision"), num("created_at"), text("created_by"), num("updated_at"),
		text("updated_by")}, order: "id"},
	{Name: "actions", Columns: []DumpColumn{num("at"), text("account"), textNull("carrier"), text("action"),
		textNull("document_id"), textNull("subject"), numNull("revision")},
		order: "at, account, carrier NULLS FIRST, action, document_id NULLS FIRST, subject NULLS FIRST, revision NULLS FIRST"},
	{Name: "settings", Columns: []DumpColumn{text("key"), text("value")}, order: "key"},
}

// DumpTables liefert die Tabellen des Abzugs in der Reihenfolge, in der
// Restore sie schreibt.
func DumpTables() []DumpTable {
	out := make([]DumpTable, len(dumpTables))
	for i, t := range dumpTables {
		out[i] = t
		out[i].Columns = append([]DumpColumn(nil), t.Columns...)
	}
	return out
}

// columnList nennt die Spalten, "user" in Anführungszeichen (in PostgreSQL
// reserviert).
func (t DumpTable) columnList() string {
	names := make([]string, len(t.Columns))
	for i, c := range t.Columns {
		names[i] = c.Name
		if c.Name == "user" {
			names[i] = `"user"`
		}
	}
	return strings.Join(names, ", ")
}

// dumpSelect liest alle Zeilen einer Tabelle in fester Ordnung.
func dumpSelect(t DumpTable) string {
	return `SELECT ` + t.columnList() + ` FROM ` + t.Name + ` ORDER BY ` + t.order
}

// dumpInsert schreibt eine Zeile mit allen Spalten.
func dumpInsert(t DumpTable) string {
	marks := make([]string, len(t.Columns))
	for i := range marks {
		marks[i] = "$" + strconv.Itoa(i+1)
	}
	return `INSERT INTO ` + t.Name + ` (` + t.columnList() + `) VALUES (` + strings.Join(marks, ", ") + `)`
}

// dumpCount zählt die Zeilen einer Tabelle.
func dumpCount(t DumpTable) string {
	return `SELECT COUNT(*) FROM ` + t.Name
}

// Dump liest den ganzen Abzug in einer einzigen Lesetransaktion und gibt
// jede Zeile an row, Tabelle für Tabelle in der Reihenfolge von DumpTables:
// values in der Reihenfolge der Spalten, string, int64 oder nil. Info ist
// db_info aus derselben Transaktion — Schemafassung, Revision und hub_id
// gehören zu genau diesen Zeilen.
//
// Die Transaktion ist ReadOnly: Unter SQLite beginnt sie damit als BEGIN
// statt BEGIN IMMEDIATE (_txlock=immediate im dsn gälte sonst auch hier) und
// hält keinen Schreiber auf; WAL liefert ihr einen festen Lesestand. Unter
// PostgreSQL ist sie REPEATABLE READ READ ONLY — ein Lesestand über alle
// Tabellen, ohne Sperre; SQLite übergeht die Isolationsstufe.
func (s *sqliteStore) Dump(ctx context.Context, row func(table string, values []any) error) (Info, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return Info{}, fmt.Errorf("Lesetransaktion: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	info, err := readInfo(ctx, tx)
	if err != nil {
		return Info{}, err
	}
	for _, t := range dumpTables {
		if err := dumpTable(ctx, tx, t, row); err != nil {
			return Info{}, fmt.Errorf("%s lesen: %w", t.Name, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return Info{}, fmt.Errorf("Lesetransaktion: %w", err)
	}
	return info, nil
}

// readInfo liest Schemafassung, Revision und hub_id aus db_info.
func readInfo(ctx context.Context, db sqlitedb.Querier) (Info, error) {
	info, err := sqlitedb.ReadInfo(ctx, db)
	if err != nil {
		return Info{}, err
	}
	v, err := strconv.Atoi(info[sqlitedb.KeySchemaVersion])
	if err != nil {
		return Info{}, fmt.Errorf("db_info: Schemafassung %q: %w", info[sqlitedb.KeySchemaVersion], err)
	}
	rev, err := parseRevision(info[keyRevision])
	if err != nil {
		return Info{}, err
	}
	return Info{SchemaVersion: v, Revision: rev, HubID: info[keyHubID]}, nil
}

// dumpTable liest eine Tabelle und gibt jede Zeile an row.
func dumpTable(ctx context.Context, tx *sql.Tx, t DumpTable, row func(table string, values []any) error) error {
	rows, err := tx.QueryContext(ctx, q(dumpSelect(t)))
	if err != nil {
		return err
	}
	defer rows.Close()
	dest := make([]any, len(t.Columns))
	for i, c := range t.Columns {
		if c.Int {
			dest[i] = new(sql.NullInt64)
		} else {
			dest[i] = new(sql.NullString)
		}
	}
	for rows.Next() {
		if err := rows.Scan(dest...); err != nil {
			return err
		}
		values := make([]any, len(t.Columns))
		for i, c := range t.Columns {
			switch d := dest[i].(type) {
			case *sql.NullInt64:
				if d.Valid {
					values[i] = d.Int64
				}
			case *sql.NullString:
				if d.Valid {
					values[i] = d.String
				}
			}
			if values[i] == nil && !c.Null {
				return fmt.Errorf("Spalte %s ist NULL", c.Name)
			}
		}
		if err := row(t.Name, values); err != nil {
			return err
		}
	}
	return rows.Err()
}
