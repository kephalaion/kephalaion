package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/oklog/ulid/v2"

	"github.com/kephalaion/kephalaion/internal/contract"
	"github.com/kephalaion/kephalaion/internal/ident"
	"github.com/kephalaion/kephalaion/internal/sqlitedb"
)

// MaxDocumentBytes ist die Obergrenze für den Inhalt eines Dokuments: 1 MiB,
// festgelegt im Vertrag.
const MaxDocumentBytes = contract.MaxDocumentBytes

// Fehlerarten beim Schreiben von Dokumenten.
var (
	// ErrTooLarge: der Inhalt ist größer als MaxDocumentBytes.
	ErrTooLarge = errors.New("ist zu groß")
	// ErrNotText: der Inhalt ist kein UTF-8-Text.
	ErrNotText = errors.New("ist kein UTF-8-Text")
	// ErrPathConflict: ein Name wäre zugleich Datei und Verzeichnis.
	ErrPathConflict = errors.New("Name wäre zugleich Datei und Verzeichnis")
)

// documentColumns sind die Spalten von documents in der Reihenfolge, die
// scanDocument erwartet.
const documentColumns = `id, collection, name, content, meta, deleted, revision,
	created_at, created_by, updated_at, updated_by`

// Document ist eine Zeile in documents, mit allen Spalten.
type Document struct {
	ID         string
	Collection string
	Name       string
	// Content ist leer bei einer Löschmarke (in der Datenbank NULL).
	Content string
	// Meta ist leer, wenn die Spalte NULL ist.
	Meta      string
	Deleted   bool
	Revision  int64
	CreatedAt int64
	CreatedBy string
	UpdatedAt int64
	UpdatedBy string
}

// DocumentInput ist ein Dokument, wie es geschrieben werden soll.
type DocumentInput struct {
	Name    string
	Content string
}

// Outcome sagt, was ein Schreibvorgang mit einem Dokument getan hat.
type Outcome int

// Mögliche Ergebnisse.
const (
	Unchanged Outcome = iota
	Created
	Replaced
)

func (o Outcome) String() string {
	switch o {
	case Created:
		return "angelegt"
	case Replaced:
		return "ersetzt"
	}
	return "unverändert"
}

// PutResult ist das Ergebnis für ein Dokument: seine id, seine Revision
// (bei Unchanged die bisherige) und was geschah.
type PutResult struct {
	Name     string
	ID       string
	Revision int64
	Outcome  Outcome
}

// ImportResult ist das Ergebnis eines Imports: je Eingabe ein PutResult in
// derselben Reihenfolge und die eine Revision des Vorgangs — 0, wenn sich
// nichts geändert hat.
type ImportResult struct {
	Revision int64
	Results  []PutResult
}

// CheckContent prüft den Inhalt eines Dokuments: UTF-8-Text ohne NUL-Byte
// (PostgreSQL speichert keines in TEXT), höchstens MaxDocumentBytes.
func CheckContent(content string) error {
	if len(content) > MaxDocumentBytes {
		return fmt.Errorf("Inhalt %w: %d Bytes, erlaubt sind höchstens %d (1 MiB)", ErrTooLarge, len(content), MaxDocumentBytes)
	}
	if !utf8.ValidString(content) || strings.IndexByte(content, 0) >= 0 {
		return fmt.Errorf("Inhalt %w", ErrNotText)
	}
	return nil
}

func checkDocumentInput(d DocumentInput) error {
	if err := ident.CheckDocName(d.Name); err != nil {
		return err
	}
	if err := CheckContent(d.Content); err != nil {
		return fmt.Errorf("Dokument %s: %w", d.Name, err)
	}
	return nil
}

func scanDocument(sc interface{ Scan(...any) error }) (Document, error) {
	var d Document
	var content, meta sql.NullString
	var deleted int64
	if err := sc.Scan(&d.ID, &d.Collection, &d.Name, &content, &meta, &deleted, &d.Revision,
		&d.CreatedAt, &d.CreatedBy, &d.UpdatedAt, &d.UpdatedBy); err != nil {
		return Document{}, err
	}
	d.Content, d.Meta, d.Deleted = content.String, meta.String, deleted != 0
	return d, nil
}

func docNotFound(collection, name string) error {
	return &kindError{ErrNotFound, fmt.Sprintf("Dokument %s gibt es in %s nicht", name, collection)}
}

// liveDocument liest das lebende Dokument eines Namens oder meldet
// ErrNotFound.
func liveDocument(ctx context.Context, db sqlitedb.Querier, collection, name string) (Document, error) {
	d, err := scanDocument(db.QueryRowContext(ctx, q(queries.DocumentLive), collection, name))
	if errors.Is(err, sql.ErrNoRows) {
		return Document{}, docNotFound(collection, name)
	}
	if err != nil {
		return Document{}, fmt.Errorf("Dokument %s lesen: %w", name, err)
	}
	return d, nil
}

func requireCollection(ctx context.Context, db sqlitedb.Querier, collection string) error {
	ok, err := collectionExists(ctx, db, collection)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("Collection %s %w", collection, ErrNotFound)
	}
	return nil
}

// lazyRevision holt die Revision eines Schreibvorgangs erst, wenn sich
// wirklich etwas ändert, und dann genau einmal: Alle Zeilen einer
// Transaktion tragen dieselbe Revision.
type lazyRevision struct {
	tx  sqlitedb.Querier
	rev int64
}

func (r *lazyRevision) get(ctx context.Context) (int64, error) {
	if r.rev == 0 {
		rev, err := nextRevision(ctx, r.tx)
		if err != nil {
			return 0, err
		}
		r.rev = rev
	}
	return r.rev, nil
}

// docTx ist ein Schreibvorgang an Dokumenten: eine Transaktion, höchstens
// eine Revision, ein Zeitpunkt für alle Zeilen, ein Urheber.
type docTx struct {
	tx  sqlitedb.Querier
	rev *lazyRevision
	now int64
	// by steht in created_by/updated_by der geschriebenen Zeilen: der User
	// des schreibenden Accounts, Admin für die CLI am Hub.
	by string
	// account und carrier stehen in actions: der Account (nicht der User)
	// und der Node, der die Anfrage trägt — leer (NULL) für die CLI am Hub.
	account, carrier string
}

// newDocTx beginnt einen Schreibvorgang in tx mit dem Urheber by, account
// und carrier.
func newDocTx(tx sqlitedb.Querier, by, account, carrier string) docTx {
	return docTx{tx: tx, rev: &lazyRevision{tx: tx}, now: sqlitedb.NowMillis(), by: by, account: account, carrier: carrier}
}

// writeDocs führt fn als einen Schreibvorgang der CLI am Hub (admin, ohne
// Träger) an den Dokumenten einer vorhandenen Collection aus.
func (s *sqliteStore) writeDocs(ctx context.Context, collection string, fn func(w *docTx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := requireCollection(ctx, tx, collection); err != nil {
		return err
	}
	w := newDocTx(tx, Admin, Admin, "")
	if err := fn(&w); err != nil {
		return err
	}
	return tx.Commit()
}

// logAction schreibt die Zeile eines Dokuments in actions: Account und
// Träger des Vorgangs, die Revision.
func (w *docTx) logAction(ctx context.Context, action, documentID string, rev int64) error {
	if _, err := w.tx.ExecContext(ctx, q(queries.ActionInsertDocument),
		w.now, w.account, nullable(w.carrier), action, documentID, rev); err != nil {
		return fmt.Errorf("actions schreiben: %w", err)
	}
	return nil
}

// put legt ein Dokument an oder ersetzt seinen Inhalt; unveränderter Inhalt
// schreibt nichts.
func (w *docTx) put(ctx context.Context, collection string, d DocumentInput) (PutResult, error) {
	cur, err := liveDocument(ctx, w.tx, collection, d.Name)
	if errors.Is(err, ErrNotFound) {
		return w.create(ctx, collection, d)
	}
	if err != nil {
		return PutResult{}, err
	}
	if cur.Content == d.Content {
		return PutResult{Name: d.Name, ID: cur.ID, Revision: cur.Revision, Outcome: Unchanged}, nil
	}
	return w.replace(ctx, cur, d.Content)
}

// create legt ein Dokument neu an, mit neuer id. Den Namen darf kein
// lebendes Dokument tragen; das prüft der Aufrufer. Hier geprüft wird, dass
// der Name nicht zugleich Datei und Verzeichnis wäre.
func (w *docTx) create(ctx context.Context, collection string, d DocumentInput) (PutResult, error) {
	if err := checkPathFree(ctx, w.tx, collection, d.Name); err != nil {
		return PutResult{}, err
	}
	rev, err := w.rev.get(ctx)
	if err != nil {
		return PutResult{}, err
	}
	id := ulid.Make().String()
	if _, err := w.tx.ExecContext(ctx, q(queries.DocumentInsert),
		id, collection, d.Name, d.Content, rev, w.now, w.by, w.now, w.by); err != nil {
		return PutResult{}, fmt.Errorf("Dokument %s anlegen: %w", d.Name, err)
	}
	if err := w.logAction(ctx, "create", id, rev); err != nil {
		return PutResult{}, err
	}
	return PutResult{Name: d.Name, ID: id, Revision: rev, Outcome: Created}, nil
}

// replace ersetzt den Inhalt eines lebenden Dokuments.
func (w *docTx) replace(ctx context.Context, cur Document, content string) (PutResult, error) {
	rev, err := w.rev.get(ctx)
	if err != nil {
		return PutResult{}, err
	}
	res, err := w.tx.ExecContext(ctx, q(queries.DocumentReplace), cur.ID, content, rev, w.now, w.by)
	if err := mustAffect(res, err, "Dokument "+cur.Name); err != nil {
		return PutResult{}, err
	}
	if err := w.logAction(ctx, "update", cur.ID, rev); err != nil {
		return PutResult{}, err
	}
	return PutResult{Name: cur.Name, ID: cur.ID, Revision: rev, Outcome: Replaced}, nil
}

// delete macht ein lebendes Dokument zur Löschmarke und liefert die
// Revision.
func (w *docTx) delete(ctx context.Context, cur Document) (int64, error) {
	rev, err := w.rev.get(ctx)
	if err != nil {
		return 0, err
	}
	res, err := w.tx.ExecContext(ctx, q(queries.DocumentDelete), cur.ID, rev, w.now, w.by)
	if err := mustAffect(res, err, "Dokument "+cur.Name); err != nil {
		return 0, err
	}
	if err := w.logAction(ctx, "delete", cur.ID, rev); err != nil {
		return 0, err
	}
	return rev, nil
}

// checkPathFree prüft, dass ein neuer Name nicht zugleich Datei und
// Verzeichnis wäre: Keines der Verzeichnisse über ihm ist ein lebendes
// Dokument, und unter ihm als Verzeichnis liegt keines. Löschmarken zählen
// nicht.
func checkPathFree(ctx context.Context, db sqlitedb.Querier, collection, name string) error {
	for _, dir := range ident.DocAncestors(name) {
		n, err := count(ctx, db, queries.DocumentLiveCount, collection, dir)
		if err != nil {
			return err
		}
		if n > 0 {
			return &kindError{ErrPathConflict, fmt.Sprintf(
				"Dokument %s: %s ist in %s ein Dokument und kann nicht zugleich Verzeichnis sein", name, dir, collection)}
		}
	}
	lo, hi := dirRange(name + "/")
	n, err := count(ctx, db, queries.DocumentsUnderLive, collection, lo, hi)
	if err != nil {
		return err
	}
	if n > 0 {
		return &kindError{ErrPathConflict, fmt.Sprintf(
			"Dokument %s: %s ist in %s ein Verzeichnis mit %d Dokumenten und kann nicht zugleich Dokument sein",
			name, name, collection, n)}
	}
	return nil
}

// dirRange liefert die Grenzen für alle Namen unter einem Verzeichnis
// prefix (mit '/' am Ende): name >= lo AND name < hi. '0' folgt in Bytes auf
// '/'.
func dirRange(prefix string) (lo, hi string) {
	return prefix, prefix[:len(prefix)-1] + "0"
}

func (s *sqliteStore) PutDocument(ctx context.Context, collection, name, content string) (PutResult, error) {
	in := DocumentInput{Name: name, Content: content}
	if err := checkDocumentInput(in); err != nil {
		return PutResult{}, err
	}
	var res PutResult
	err := s.writeDocs(ctx, collection, func(w *docTx) error {
		var err error
		res, err = w.put(ctx, collection, in)
		return err
	})
	if err != nil {
		return PutResult{}, err
	}
	return res, nil
}

func (s *sqliteStore) ImportDocuments(ctx context.Context, collection string, docs []DocumentInput) (ImportResult, error) {
	seen := map[string]bool{}
	for _, d := range docs {
		if err := checkDocumentInput(d); err != nil {
			return ImportResult{}, err
		}
		if seen[d.Name] {
			return ImportResult{}, fmt.Errorf("Dokument %s steht zweimal im Import", d.Name)
		}
		seen[d.Name] = true
	}
	out := ImportResult{Results: make([]PutResult, 0, len(docs))}
	err := s.writeDocs(ctx, collection, func(w *docTx) error {
		for _, d := range docs {
			res, err := w.put(ctx, collection, d)
			if err != nil {
				return err
			}
			out.Results = append(out.Results, res)
		}
		out.Revision = w.rev.rev
		return nil
	})
	if err != nil {
		return ImportResult{}, err
	}
	return out, nil
}

func (s *sqliteStore) DeleteDocument(ctx context.Context, collection, name string) (Document, error) {
	if err := ident.CheckDocName(name); err != nil {
		return Document{}, err
	}
	var out Document
	err := s.writeDocs(ctx, collection, func(w *docTx) error {
		cur, err := liveDocument(ctx, w.tx, collection, name)
		if err != nil {
			return err
		}
		rev, err := w.delete(ctx, cur)
		if err != nil {
			return err
		}
		out = cur
		out.Content, out.Meta, out.Deleted = "", "", true
		out.Revision, out.UpdatedAt, out.UpdatedBy = rev, w.now, w.by
		return nil
	})
	if err != nil {
		return Document{}, err
	}
	return out, nil
}

// Lesen: ohne Transaktion — sie wäre IMMEDIATE und nähme die Schreibsperre.

func (s *sqliteStore) Document(ctx context.Context, collection, name string) (Document, error) {
	if err := ident.CheckDocName(name); err != nil {
		return Document{}, err
	}
	if err := requireCollection(ctx, s.db, collection); err != nil {
		return Document{}, err
	}
	return liveDocument(ctx, s.db, collection, name)
}

func (s *sqliteStore) Documents(ctx context.Context, collection, dir string) ([]Document, error) {
	prefix, err := ident.DocDirPrefix(dir)
	if err != nil {
		return nil, err
	}
	if err := requireCollection(ctx, s.db, collection); err != nil {
		return nil, err
	}
	var rows *sql.Rows
	if prefix == "" {
		rows, err = s.db.QueryContext(ctx, q(queries.DocumentsAll), collection)
	} else {
		lo, hi := dirRange(prefix)
		rows, err = s.db.QueryContext(ctx, q(queries.DocumentsInDir), collection, lo, hi)
	}
	if err != nil {
		return nil, fmt.Errorf("Dokumente lesen: %w", err)
	}
	defer rows.Close()
	out := []Document{}
	for rows.Next() {
		d, err := scanDocument(rows)
		if err != nil {
			return nil, fmt.Errorf("Dokumente lesen: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
