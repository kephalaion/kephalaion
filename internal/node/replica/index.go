package replica

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/kephalaion/kephalaion/internal/contract"
	"github.com/kephalaion/kephalaion/internal/frontmatter"
	"github.com/kephalaion/kephalaion/internal/sections"
)

// Der Volltextindex über docs/ (Task 033, docs/indizierung.md, „Der Index:
// FTS5 im Node“): je Abschnitt eines Markdown-Dokuments unter docs/ eine
// Zeile in sections und eine in sections_fts (FTS5, porter unicode61) mit
// derselben rowid. Er wird in apply nachgezogen, in derselben Transaktion,
// in der die Zeilen geschrieben werden — so passt er immer zur Replica.
// Nicht im Index sind index.md, docs/README.md und Mahnungen
// (contract.IsSystemDoc), Löschmarken und Dokumente außerhalb von docs/.

// KeySectionsVersion ist der Schlüssel der Fassung der Zerlegung in db_info
// (sections.Version). Weicht sie ab, baut apply den Index neu.
const KeySectionsVersion = "sections_version"

// indexSchema ist das DDL des Index, Teil von schema.
const indexSchema = `
CREATE TABLE sections (
  id          INTEGER PRIMARY KEY,
  doc_id      TEXT NOT NULL,
  collection  TEXT NOT NULL,
  name        TEXT NOT NULL,
  ord         INTEGER NOT NULL,
  anchor      TEXT NOT NULL,
  heading     TEXT NOT NULL,
  headings    TEXT NOT NULL,
  title       TEXT NOT NULL,
  description TEXT NOT NULL
);
CREATE INDEX sections_doc ON sections(doc_id);
CREATE INDEX sections_name ON sections(collection, name);
CREATE VIRTUAL TABLE sections_fts USING fts5(title, headings, questions, description, hints, body,
  tokenize = 'porter unicode61');
`

const (
	qSecInsert = `INSERT INTO sections (doc_id, collection, name, ord, anchor, heading, headings, title, description)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`
	qFTSInsert = `INSERT INTO sections_fts (rowid, title, headings, questions, description, hints, body)
		VALUES (?, ?, ?, ?, ?, ?, ?)`
	qFTSDeleteDoc  = `DELETE FROM sections_fts WHERE rowid IN (SELECT id FROM sections WHERE doc_id = ?)`
	qSecDeleteDoc  = `DELETE FROM sections WHERE doc_id = ?`
	qFTSDeleteOf   = `DELETE FROM sections_fts WHERE rowid IN (SELECT id FROM sections WHERE collection = ?)`
	qSecDeleteOf   = `DELETE FROM sections WHERE collection = ?`
	qFTSDeleteAll  = `DELETE FROM sections_fts`
	qSecDeleteAll  = `DELETE FROM sections`
	qInfoSet       = `INSERT INTO db_info (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`
	qInfoGet       = `SELECT COALESCE((SELECT value FROM db_info WHERE key = ?), '')`
	qDocsIndexable = `SELECT ` + documentColumns + ` FROM documents
		WHERE deleted = 0 AND name >= 'docs/' AND name < 'docs0'`
)

// indexable sagt, ob eine Zeile in den Index gehört: ein lebendes
// Markdown-Dokument unter docs/, das nicht dem System gehört.
func indexable(row contract.Row) bool {
	return !row.Deleted && row.Content != nil && contract.InDocs(row.Name) && frontmatter.IsMarkdown(row.Name) &&
		!contract.IsSystemDoc(row.Name, row.UpdatedBy)
}

// reindexRow zieht den Index für eine geschriebene Zeile nach: Die
// Abschnitte der id gehen, und ist die Zeile indizierbar, kommen ihre neuen
// — so folgt der Index Änderung, Löschmarke und Umbenennen.
func reindexRow(ctx context.Context, tx *sql.Tx, row contract.Row) error {
	if _, err := tx.ExecContext(ctx, qFTSDeleteDoc, row.ID); err != nil {
		return fmt.Errorf("Index für %s leeren: %w", row.Name, err)
	}
	if _, err := tx.ExecContext(ctx, qSecDeleteDoc, row.ID); err != nil {
		return fmt.Errorf("Index für %s leeren: %w", row.Name, err)
	}
	if !indexable(row) {
		return nil
	}
	d := sections.Parse(row.Name, *row.Content)
	questions, hints := strings.Join(d.Questions, "\n"), strings.Join(d.Hints, "\n")
	for i, s := range d.Sections {
		path := s.Headings
		if path == nil {
			path = []string{}
		}
		hs, err := json.Marshal(path)
		if err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, qSecInsert, row.ID, row.Collection, row.Name, i, s.Anchor, s.Heading, string(hs),
			d.Title, d.Description)
		if err != nil {
			return fmt.Errorf("Index für %s schreiben: %w", row.Name, err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, qFTSInsert, id, d.Title, strings.Join(s.Headings, "\n"), questions,
			d.Description, hints, s.Body); err != nil {
			return fmt.Errorf("Index für %s schreiben: %w", row.Name, err)
		}
	}
	return nil
}

// dropIndexOf entfernt die Abschnitte einer Collection.
func dropIndexOf(ctx context.Context, tx *sql.Tx, collection string) error {
	for _, q := range []string{qFTSDeleteOf, qSecDeleteOf} {
		if _, err := tx.ExecContext(ctx, q, collection); err != nil {
			return fmt.Errorf("Index von %s entfernen: %w", collection, err)
		}
	}
	return nil
}

// dropIndex leert den Index.
func dropIndex(ctx context.Context, tx *sql.Tx) error {
	for _, q := range []string{qFTSDeleteAll, qSecDeleteAll} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("Index leeren: %w", err)
		}
	}
	return nil
}

// ensureIndex baut den Index aus den Zeilen der Replica neu, wenn die
// Fassung der Zerlegung in db_info nicht die dieses Binarys ist, und schreibt
// die Fassung.
func ensureIndex(ctx context.Context, tx *sql.Tx) error {
	var have string
	if err := tx.QueryRowContext(ctx, qInfoGet, KeySectionsVersion).Scan(&have); err != nil {
		return fmt.Errorf("db_info lesen: %w", err)
	}
	want := strconv.Itoa(sections.Version)
	if have == want {
		return nil
	}
	if err := dropIndex(ctx, tx); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, qDocsIndexable)
	if err != nil {
		return fmt.Errorf("Dokumente lesen: %w", err)
	}
	var docs []contract.Row
	for rows.Next() {
		d, err := scanDocument(rows)
		if err != nil {
			rows.Close()
			return err
		}
		docs = append(docs, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, d := range docs {
		if err := reindexRow(ctx, tx, d); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, qInfoSet, KeySectionsVersion, want); err != nil {
		return fmt.Errorf("db_info schreiben: %w", err)
	}
	return nil
}
