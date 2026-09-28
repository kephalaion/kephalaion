package replica

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/kephalaion/kephalaion/internal/ident"
)

// Lesen für die Werkzeuge des Nodes (list, read, changes): ohne
// Transaktion, jede Abfrage für sich. SYSTEM:-Zeilen liefert keine dieser
// Abfragen; Löschmarken nur ChangedEntries und EntryByID.

// Entry ist ein Dokument der Replica ohne Inhalt, mit seiner Größe in Bytes
// (0 bei einer Löschmarke). Content ist nur gesetzt, wenn er verlangt war.
type Entry struct {
	ID         string
	Collection string
	Name       string
	Deleted    bool
	Revision   int64
	CreatedAt  int64
	CreatedBy  string
	UpdatedAt  int64
	UpdatedBy  string
	Size       int64
	Content    *string
}

// entryColumns sind die Spalten, die scanEntry erwartet; die Größe zählt
// Bytes, nicht Zeichen.
const entryColumns = `id, collection, name, deleted, revision, created_at, created_by, updated_at, updated_by,
	COALESCE(length(CAST(content AS BLOB)), 0)`

// notSystem schließt SYSTEM:-Zeilen aus.
const notSystem = `substr(name, 1, 7) <> 'SYSTEM:'`

// inRange grenzt auf ein Verzeichnis ein: name >= prefix und, wenn prefix
// nicht leer ist, name < hi (siehe prefixRange). Drei Platzhalter: prefix,
// hi, hi.
const inRange = `name >= ? AND (? = '' OR name < ?)`

// newest lässt von zwei lebenden Zeilen desselben Namens nur die jüngste
// gelten — auf dem Node gibt es keinen eindeutigen Index auf den Namen.
const newest = `NOT EXISTS (SELECT 1 FROM documents n WHERE n.collection = d.collection AND n.name = d.name
	AND n.deleted = 0 AND (n.revision > d.revision OR (n.revision = d.revision AND n.id > d.id)))`

const (
	qEntryByName = `SELECT ` + entryColumns + `, CASE WHEN ? THEN content END FROM documents
		WHERE collection = ? AND name = ? AND deleted = 0
		ORDER BY revision DESC, id DESC LIMIT 1`
	qEntryByID = `SELECT ` + entryColumns + `, CASE WHEN ? THEN content END FROM documents WHERE id = ?`
	qHasUnder  = `SELECT EXISTS (SELECT 1 FROM documents WHERE collection = ? AND deleted = 0 AND ` + notSystem +
		` AND ` + inRange + `)`
	qLiveNames = `SELECT DISTINCT name FROM documents WHERE collection = ? AND deleted = 0 AND ` + notSystem +
		` AND ` + inRange + ` ORDER BY name`
	qStateOf = `SELECT revision FROM sync_state WHERE collection = ?`
	// Der Anfang eines Inhalts in Bytes (substr auf TEXT zählte Zeichen):
	// Platzhalter Länge, dann die Zeile. Nur lebende Zeilen, keine SYSTEM:.
	qHeadByName = `SELECT substr(CAST(content AS BLOB), 1, ?) FROM documents
		WHERE collection = ? AND name = ? AND deleted = 0 AND ` + notSystem + `
		ORDER BY revision DESC, id DESC LIMIT 1`
	qHeadByID = `SELECT substr(CAST(content AS BLOB), 1, ?) FROM documents WHERE id = ? AND deleted = 0 AND ` + notSystem
)

// prefixRange liefert zu einem Verzeichnis-Präfix (wie von
// ident.DocDirPrefix, mit '/' am Ende oder leer) die obere Grenze des
// Bereichs: '0' folgt in Bytes auf '/'. Leer bei leerem Präfix.
func prefixRange(prefix string) string {
	if prefix == "" {
		return ""
	}
	return prefix[:len(prefix)-1] + "0"
}

func scanEntry(sc interface{ Scan(...any) error }, extra ...any) (Entry, error) {
	var e Entry
	var deleted int64
	dest := append([]any{&e.ID, &e.Collection, &e.Name, &deleted, &e.Revision, &e.CreatedAt, &e.CreatedBy,
		&e.UpdatedAt, &e.UpdatedBy, &e.Size}, extra...)
	if err := sc.Scan(dest...); err != nil {
		return Entry{}, err
	}
	e.Deleted = deleted != 0
	return e, nil
}

func scanEntryContent(sc interface{ Scan(...any) error }) (Entry, error) {
	var content sql.NullString
	e, err := scanEntry(sc, &content)
	if err == nil && content.Valid {
		e.Content = &content.String
	}
	return e, err
}

// EntryByName liest das lebende Dokument eines Namens, mit Inhalt, wenn
// content gilt; ok ist false, wenn es keines gibt. Ein Name, der gegen die
// Pfadregeln verstößt — auch ein SYSTEM:-Name —, ist ein Fehler.
func (r *Replica) EntryByName(ctx context.Context, collection, name string, content bool) (e Entry, ok bool, err error) {
	if err := ident.CheckDocName(name); err != nil {
		return Entry{}, false, err
	}
	e, err = scanEntryContent(r.db.QueryRowContext(ctx, qEntryByName, content, collection, name))
	if errors.Is(err, sql.ErrNoRows) {
		return Entry{}, false, nil
	}
	if err != nil {
		return Entry{}, false, fmt.Errorf("Dokument %s lesen: %w", name, err)
	}
	return e, true, nil
}

// EntryByID liest die Zeile einer id, auch eine Löschmarke; ok ist false,
// wenn es keine gibt oder sie eine SYSTEM:-Zeile ist.
func (r *Replica) EntryByID(ctx context.Context, id string, content bool) (e Entry, ok bool, err error) {
	e, err = scanEntryContent(r.db.QueryRowContext(ctx, qEntryByID, content, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Entry{}, false, nil
	}
	if err != nil {
		return Entry{}, false, fmt.Errorf("Dokument %s lesen: %w", id, err)
	}
	if ident.IsSystemName(e.Name) {
		return Entry{}, false, nil
	}
	return e, true, nil
}

// HeadByName liest die ersten n Bytes des Inhalts des lebenden Dokuments
// eines Namens — für das Frontmatter, ohne den ganzen Inhalt zu laden; ok ist
// false, wenn es keines gibt. Der Schnitt kann mitten in einem Zeichen liegen.
// Ein Name gegen die Pfadregeln ist ein Fehler.
func (r *Replica) HeadByName(ctx context.Context, collection, name string, n int) (head []byte, ok bool, err error) {
	if err := ident.CheckDocName(name); err != nil {
		return nil, false, err
	}
	return r.head(ctx, name, qHeadByName, n, collection, name)
}

// HeadByID liest die ersten n Bytes des Inhalts eines lebenden Dokuments per
// id; ok ist false ohne lebendes Dokument dieser id.
func (r *Replica) HeadByID(ctx context.Context, id string, n int) (head []byte, ok bool, err error) {
	return r.head(ctx, id, qHeadByID, n, id)
}

func (r *Replica) head(ctx context.Context, what, query string, n int, args ...any) ([]byte, bool, error) {
	var head []byte
	err := r.db.QueryRowContext(ctx, query, append([]any{n}, args...)...).Scan(&head)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("Anfang von Dokument %s lesen: %w", what, err)
	}
	return head, true, nil
}

// HasUnder sagt, ob unter einem Verzeichnis-Präfix (nicht leer, mit '/' am
// Ende) ein lebendes Dokument liegt — dann gibt es das Verzeichnis.
func (r *Replica) HasUnder(ctx context.Context, collection, prefix string) (bool, error) {
	hi := prefixRange(prefix)
	var ok bool
	if err := r.db.QueryRowContext(ctx, qHasUnder, collection, prefix, hi, hi).Scan(&ok); err != nil {
		return false, fmt.Errorf("Verzeichnis lesen: %w", err)
	}
	return ok, nil
}

// ChildDirs liefert die Verzeichnisse direkt unter einem Verzeichnis-Präfix
// ("" für die Wurzel), nach Name in Bytes: das nächste Segment jedes
// lebenden Namens darunter, der weitere Segmente hat, je einmal.
func (r *Replica) ChildDirs(ctx context.Context, collection, prefix string) ([]string, error) {
	hi := prefixRange(prefix)
	rows, err := r.db.QueryContext(ctx, qLiveNames, collection, prefix, hi, hi)
	if err != nil {
		return nil, fmt.Errorf("Verzeichnisse lesen: %w", err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("Verzeichnisse lesen: %w", err)
		}
		// Die Namen unter einem Verzeichnis folgen aufeinander: Es reicht,
		// mit dem letzten zu vergleichen.
		if child, isDir, ok := ident.DocChild(prefix, name); ok && isDir && (len(out) == 0 || out[len(out)-1] != child) {
			out = append(out, child)
		}
	}
	return out, rows.Err()
}

// StateOf liefert den Stand einer Collection aus sync_state; ok ist false,
// wenn die Replica keinen hat.
func (r *Replica) StateOf(ctx context.Context, collection string) (rev int64, ok bool, err error) {
	err = r.db.QueryRowContext(ctx, qStateOf, collection).Scan(&rev)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("sync_state lesen: %w", err)
	}
	return rev, true, nil
}

// Sortierungen von ListEntries.
const (
	SortName    = "name"
	SortCreated = "created"
	SortUpdated = "updated"
)

// sortColumn ist die Spalte je Sortierung; nur diese festen Werte kommen in
// den Text einer Abfrage.
var sortColumn = map[string]string{SortName: "name", SortCreated: "created_at", SortUpdated: "updated_at"}

// ListKey ist die Stelle, nach der ListEntries weiterliest: der Wert der
// Sortierung (bei SortName der Name selbst, sonst die Zeit in ms) und der
// Name — lebende Namen sind nach newest eindeutig, das Paar ordnet also
// vollständig.
type ListKey struct {
	At   int64
	Name string
}

// ListQuery beschreibt, was ListEntries liest.
type ListQuery struct {
	Collection string
	// Prefix ist das Verzeichnis wie von ident.DocDirPrefix.
	Prefix string
	// Recursive: auch Dokumente in Unterverzeichnissen; sonst nur die direkt
	// im Verzeichnis.
	Recursive bool
	// Sort ist SortName, SortCreated oder SortUpdated; Desc kehrt um.
	Sort string
	Desc bool
	// After ist die Stelle, nach der es weitergeht; nil von vorn.
	After *ListKey
}

// ListEntries liest die lebenden Dokumente eines Verzeichnisses in der
// verlangten Ordnung, ohne SYSTEM:-Zeilen, und gibt sie nacheinander an fn,
// bis fn false liefert oder keine mehr kommen. Die Abfrage läuft über den
// Index (collection, name).
func (r *Replica) ListEntries(ctx context.Context, q ListQuery, fn func(Entry) bool) error {
	col, ok := sortColumn[q.Sort]
	if !ok {
		return fmt.Errorf("Sortierung %q: erwartet name, created oder updated", q.Sort)
	}
	hi := prefixRange(q.Prefix)
	var b strings.Builder
	args := []any{q.Collection, q.Prefix, hi, hi}
	b.WriteString(`SELECT ` + entryColumns + ` FROM documents d WHERE collection = ? AND deleted = 0 AND ` +
		notSystem + ` AND ` + inRange + ` AND ` + newest)
	if !q.Recursive {
		b.WriteString(` AND instr(substr(name, length(?) + 1), '/') = 0`)
		args = append(args, q.Prefix)
	}
	cmp, dir := ">", "ASC"
	if q.Desc {
		cmp, dir = "<", "DESC"
	}
	if q.After != nil {
		if q.Sort == SortName {
			b.WriteString(` AND name ` + cmp + ` ?`)
			args = append(args, q.After.Name)
		} else {
			b.WriteString(` AND (` + col + ` ` + cmp + ` ? OR (` + col + ` = ? AND name ` + cmp + ` ?))`)
			args = append(args, q.After.At, q.After.At, q.After.Name)
		}
	}
	if q.Sort == SortName {
		b.WriteString(` ORDER BY name ` + dir)
	} else {
		b.WriteString(` ORDER BY ` + col + ` ` + dir + `, name ` + dir)
	}
	rows, err := r.db.QueryContext(ctx, b.String(), args...)
	if err != nil {
		return fmt.Errorf("Dokumente lesen: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return fmt.Errorf("Dokumente lesen: %w", err)
		}
		if !fn(e) {
			return nil
		}
	}
	return rows.Err()
}

const (
	// qChanged liest die Zeilen einer Collection nach (revision, id), auch
	// Löschmarken, ohne SYSTEM:-Zeilen, über den Index (collection,
	// revision).
	qChanged = `SELECT ` + entryColumns + ` FROM documents
		WHERE collection = ? AND ` + notSystem + ` AND ` + inRange + `
		AND (revision > ? OR (revision = ? AND ? <> '' AND id > ?)) AND revision <= ?
		ORDER BY revision, id LIMIT ?`
	qFirstSince = `SELECT MIN(revision) FROM documents
		WHERE collection = ? AND ` + notSystem + ` AND ` + inRange + ` AND updated_at >= ?`
)

// ChangeKey ist die Stelle, nach der ChangedEntries weiterliest: alle Zeilen
// mit kleinerer Revision und in Revision Rev die bis einschließlich ID sind
// geliefert. Leere ID: Revision Rev ganz.
type ChangeKey struct {
	Rev int64
	ID  string
}

// ChangedEntries liest die Zeilen einer Collection unter einem
// Verzeichnis-Präfix ("" für alle) nach der Stelle after bis einschließlich
// Revision upto, nach Revision und id, höchstens limit: je id die jüngste —
// die Replica hält je id nur eine Zeile —, Löschmarken eingeschlossen,
// SYSTEM:-Zeilen nicht.
func (r *Replica) ChangedEntries(ctx context.Context, collection, prefix string, after ChangeKey, upto int64,
	limit int) ([]Entry, error) {
	hi := prefixRange(prefix)
	rows, err := r.db.QueryContext(ctx, qChanged, collection, prefix, hi, hi, after.Rev, after.Rev, after.ID, after.ID,
		upto, limit)
	if err != nil {
		return nil, fmt.Errorf("Änderungen lesen: %w", err)
	}
	defer rows.Close()
	out := []Entry{}
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, fmt.Errorf("Änderungen lesen: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// FirstRevisionSince liefert die kleinste Revision einer Zeile unter einem
// Verzeichnis-Präfix, die der Hub zu since (ms) oder später geschrieben hat,
// Löschmarken eingeschlossen; ok ist false, wenn es keine gibt.
func (r *Replica) FirstRevisionSince(ctx context.Context, collection, prefix string, since int64) (rev int64, ok bool, err error) {
	hi := prefixRange(prefix)
	var n sql.NullInt64
	if err := r.db.QueryRowContext(ctx, qFirstSince, collection, prefix, hi, hi, since).Scan(&n); err != nil {
		return 0, false, fmt.Errorf("Änderungen lesen: %w", err)
	}
	return n.Int64, n.Valid, nil
}
