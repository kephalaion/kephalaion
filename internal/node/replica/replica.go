// Package replica ist die Replica des Nodes: je Hub-Eintrag eine eigene
// SQLite-Datei unter replicas/<alias>.db neben node.db, dazu der Abgleich,
// der sie über den Vertrag (internal/contract) füllt.
//
// Die Replica ist abgeleitet: Sie enthält genau die Zeilen, die der Hub
// geliefert hat, und lässt sich jederzeit neu abgleichen. Deshalb ist sie die
// einzige Datenbank, die nicht init anlegt, sondern der erste Abgleich. Auf
// dem Node zählt die id eines Dokuments, nicht sein Name: documents hat
// keinen eindeutigen Index auf den Namen, so dass Umbenennungen in jeder
// Reihenfolge ankommen dürfen.
//
// Wie der Node-Store darf die Replica SQLite-Eigenes benutzen. Sie kennt den
// Hub nur über internal/contract.
package replica

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/oklog/ulid/v2"

	"github.com/kephalaion/kephalaion/internal/contract"
	"github.com/kephalaion/kephalaion/internal/ident"
	"github.com/kephalaion/kephalaion/internal/sqlitedb"
)

// Role ist die Rolle, die in db_info einer Replica steht. Keine Rolle der
// config: Eine Replica gehört zum Node.
const Role = "replica"

// SchemaVersion ist die Schemafassung der Replica. Passt sie nicht, verwirft
// der Abgleich die Replica und legt sie neu an; sie ist abgeleitet. Fassung 4
// bringt die generation in db_info.
const SchemaVersion = 4

// KeyHubID ist der Schlüssel der hub_id in db_info. Sie ist maßgeblich; die
// Spalte hubs.hub_id in node.db ist nur Kopie.
const KeyHubID = "hub_id"

// KeyEntryID ist der Schlüssel der entry_id in db_info: der Hub-Eintrag, für
// den die Replica angelegt wurde. Jede Transaktion, die schreibt, prüft ihn
// zuerst — ein Abgleich für einen entfernten oder neu angelegten Eintrag
// schreibt so nie in die Replica des neuen.
const KeyEntryID = "entry_id"

// KeyGeneration ist der Schlüssel der generation in db_info: eine ULID, die
// Create vergibt und reset in derselben Transaktion ersetzt, in der es die
// Replica leert. Sie wechselt also bei jeder Neuanlage und jedem Leeren, auch
// bei gleicher hub_id (Hub aus einer Sicherung). changes trägt sie je Hub im
// Cursor und erkennt daran, dass der Stand eines Aufrufers nicht mehr gilt.
const KeyGeneration = "generation"

// ErrNotFound meldet eine Collection oder ein Dokument, das es in der
// Replica nicht gibt.
var ErrNotFound = errors.New("gibt es in der Replica nicht")

// errNoIDs meldet eine Replica, deren db_info keine hub_id, entry_id oder
// generation nennt.
var errNoIDs = errors.New("db_info ohne hub_id, entry_id oder generation")

// afterLink läuft in Create zwischen dem Linken an den Ort und dem Öffnen;
// nur Tests setzen es, um die Datei dazwischen zu ersetzen.
var afterLink func(path string)

// ErrChanged meldet, dass sich die Replica während eines Abgleichs unter ihm
// geändert hat: ein anderer Abgleich hat sie geleert, node hub rm oder
// config import hat sie entfernt, oder sie gehört inzwischen zu einem neuen
// Eintrag. Geschrieben ist dann nichts von der Seite; der nächste Abgleich
// setzt neu auf.
var ErrChanged = errors.New("die Replica hat sich während des Abgleichs geändert")

// schema ist das DDL der Replica über den Unterbau hinaus: documents wie am
// Hub, aber ohne eindeutigen Index auf den Namen — auf dem Node zählt die
// id —, und der Stand des Abgleichs je Collection. documents_system findet
// die Zeilen eines Accounts quer über die Collections; der Node fragt ihn
// bei jeder Anfrage eines Clients (siehe AccountRows).
const schema = `
CREATE TABLE documents (
  id          TEXT PRIMARY KEY,
  collection  TEXT NOT NULL,
  name        TEXT NOT NULL,
  content     TEXT,
  meta        TEXT,
  deleted     INTEGER NOT NULL DEFAULT 0,
  revision    INTEGER NOT NULL,
  created_at  INTEGER NOT NULL,
  created_by  TEXT NOT NULL,
  updated_at  INTEGER NOT NULL,
  updated_by  TEXT NOT NULL
);
CREATE INDEX documents_name ON documents(collection, name);
CREATE INDEX documents_revision ON documents(collection, revision);
CREATE INDEX documents_system ON documents(name) WHERE name LIKE 'SYSTEM:%';

CREATE TABLE sync_state (
  collection  TEXT PRIMARY KEY,
  revision    INTEGER NOT NULL,
  synced_at   INTEGER NOT NULL
);
`

// documentColumns sind die Spalten von documents in der Reihenfolge, die
// scanDocument erwartet.
const documentColumns = `id, collection, name, content, meta, deleted, revision,
	created_at, created_by, updated_at, updated_by`

// Abfragen der Replica.
const (
	// qDocUpsert fügt eine Zeile ein oder ersetzt sie per id — nie durch eine
	// ältere Revision: Laufen zwei Abgleiche derselben Replica nebeneinander
	// (serve und node sync), bleibt die jüngste.
	qDocUpsert = `INSERT INTO documents (` + documentColumns + `)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET collection = excluded.collection, name = excluded.name,
			content = excluded.content, meta = excluded.meta, deleted = excluded.deleted,
			revision = excluded.revision, created_at = excluded.created_at, created_by = excluded.created_by,
			updated_at = excluded.updated_at, updated_by = excluded.updated_by
		WHERE excluded.revision >= documents.revision`
	qDocsDeleteOf  = `DELETE FROM documents WHERE collection = ?`
	qDocsDeleteAll = `DELETE FROM documents`
	// qDocLive liest das lebende Dokument eines Namens. Ohne eindeutigen
	// Index könnten es kurzzeitig zwei sein; es gilt das jüngste.
	qDocLive = `SELECT ` + documentColumns + ` FROM documents
		WHERE collection = ? AND name = ? AND deleted = 0
		ORDER BY revision DESC, id DESC LIMIT 1`
	// qDocsLive liest die lebenden Dokumente einer Collection ohne
	// SYSTEM:-Zeilen, nach Name; ein Verzeichnis grenzt der Bereich
	// name >= 'a/' AND name < 'a0' ein ('0' folgt in Bytes auf '/').
	qDocsLive = `SELECT ` + documentColumns + ` FROM documents
		WHERE collection = ? AND deleted = 0 AND substr(name, 1, 7) <> 'SYSTEM:'
		AND name >= ? AND (? = '' OR name < ?)
		ORDER BY name, revision DESC, id DESC`
	qCollectionsAll = `SELECT collection FROM sync_state
		UNION SELECT DISTINCT collection FROM documents ORDER BY 1`

	qStateAll = `SELECT collection, revision, synced_at FROM sync_state ORDER BY collection`
	qStateGet = `SELECT COUNT(*) FROM sync_state WHERE collection = ?`
	// qStateRev liest den Stand einer Collection; ohne Zeile -1.
	qStateRev = `SELECT COALESCE((SELECT revision FROM sync_state WHERE collection = ?), -1)`
	// qStateUpsert schreibt den Stand fort, nie zurück — auch nicht, wenn ein
	// zweiter Abgleich schon weiter ist.
	qStateUpsert = `INSERT INTO sync_state (collection, revision, synced_at) VALUES (?, ?, ?)
		ON CONFLICT(collection) DO UPDATE SET revision = max(sync_state.revision, excluded.revision),
			synced_at = excluded.synced_at`
	// qStateMin ist die Revision der Replica: der Stand, bis zu dem alle ihre
	// Collections abgeglichen sind.
	qStateMin       = `SELECT COUNT(*), COALESCE(MIN(revision), 0) FROM sync_state`
	qStateDeleteOf  = `DELETE FROM sync_state WHERE collection = ?`
	qStateDeleteAll = `DELETE FROM sync_state`
)

// Replica ist eine geöffnete Replica.
type Replica struct {
	db         *sql.DB
	hubID      string
	entryID    string
	generation string
}

// State ist der Stand des Abgleichs einer Collection: bis zu welcher
// Revision die Replica alles hat und wann zuletzt eine Seite ankam (ms seit
// Epoche).
type State struct {
	Collection string
	Revision   int64
	SyncedAt   int64
}

// Document ist ein Dokument der Replica, so wie der Hub es geliefert hat.
type Document = contract.Row

// Open öffnet eine vorhandene Replica und prüft Rolle und Schemafassung.
// Fehlt die Datei, liefert Open sqlitedb.ErrNotFound und legt nichts an.
func Open(ctx context.Context, path string) (*Replica, error) {
	db, err := sqlitedb.Open(ctx, path)
	if err != nil {
		return nil, err
	}
	info, err := sqlitedb.CheckInfo(ctx, db, Role, SchemaVersion)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("Replica %s: %w", path, err)
	}
	id, entry, gen := info[KeyHubID], info[KeyEntryID], info[KeyGeneration]
	if id == "" || entry == "" || gen == "" {
		_ = db.Close()
		return nil, fmt.Errorf("Replica %s: %w", path, errNoIDs)
	}
	return &Replica{db: db, hubID: id, entryID: entry, generation: gen}, nil
}

// Create legt eine neue Replica für den Hub hubID und den Hub-Eintrag
// entryID an, samt Verzeichnis (0700). Sie entsteht unter einem eigenen Namen
// daneben und wird erst fertig an ihren Ort gelinkt: Wer sie dort öffnet,
// findet nie eine halb angelegte. Existiert die Datei schon — etwa weil ein
// zweiter Abgleich schneller war —, bricht Create mit sqlitedb.ErrExists ab.
// Scheitert das Anlegen, bleibt keine Datei zurück. Zwischen Linken und
// Öffnen kann ein anderer Prozess die Datei ersetzen (node hub rm, add und
// der Abgleich des neuen Eintrags): Trägt die geöffnete Datei nicht entryID,
// liefert Create ErrChanged und schreibt nichts.
func Create(ctx context.Context, path, hubID, entryID string) (*Replica, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("Verzeichnis der Replicas: %w", err)
	}
	tmp := path + ".new-" + ulid.Make().String()
	db, err := sqlitedb.Create(ctx, tmp)
	if err != nil {
		return nil, err
	}
	err = sqlitedb.CreateSchema(ctx, db, schema, Role, SchemaVersion, map[string]string{KeyHubID: hubID, KeyEntryID: entryID,
		KeyGeneration: ulid.Make().String()})
	if cerr := db.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Link(tmp, path)
		if errors.Is(err, fs.ErrExist) {
			err = fmt.Errorf("%w: %s", sqlitedb.ErrExists, path)
		}
	}
	if rmErr := sqlitedb.Remove(tmp); err == nil && rmErr != nil {
		err = rmErr
	}
	if err != nil {
		return nil, err
	}
	if afterLink != nil {
		afterLink(path)
	}
	r, err := Open(ctx, path)
	if err != nil {
		return nil, err
	}
	if r.EntryID() != entryID {
		_ = r.Close()
		return nil, ErrChanged
	}
	return r, nil
}

// Remove entfernt die Replica-Datei samt -wal und -shm; eine fehlende Datei
// ist kein Fehler.
func Remove(path string) error { return sqlitedb.Remove(path) }

// Close schließt die Replica.
func (r *Replica) Close() error { return r.db.Close() }

// HubID ist die hub_id aus db_info — die maßgebliche.
func (r *Replica) HubID() string { return r.hubID }

// EntryID ist der Hub-Eintrag, für den die Replica angelegt wurde.
func (r *Replica) EntryID() string { return r.entryID }

// Generation ist die generation aus db_info, gelesen beim Öffnen — vor allem,
// was danach aus der Replica gelesen wird. Wer sie zusammen mit Gelesenem
// weitergibt, erfährt von einem reset dazwischen spätestens beim nächsten
// Öffnen.
func (r *Replica) Generation() string { return r.generation }

// checkOwner prüft in einer Transaktion, die schreiben will, dass die Datei
// noch die ist, die der Aufrufer meint: gleicher Eintrag, gleiche hub_id.
// Sonst ErrChanged, und die Transaktion schreibt nichts.
func (r *Replica) checkOwner(ctx context.Context, tx *sql.Tx) error {
	info, err := sqlitedb.ReadInfo(ctx, tx)
	if err != nil {
		return fmt.Errorf("db_info lesen: %w", err)
	}
	if info[KeyEntryID] != r.entryID || info[KeyHubID] != r.hubID {
		return ErrChanged
	}
	return nil
}

// inTx führt fn in einer Transaktion aus.
func (r *Replica) inTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// Lesen: ohne Transaktion — sie wäre IMMEDIATE und nähme die Schreibsperre.

// States liest den Stand aller Collections, nach Name.
func (r *Replica) States(ctx context.Context) ([]State, error) {
	rows, err := r.db.QueryContext(ctx, qStateAll)
	if err != nil {
		return nil, fmt.Errorf("sync_state lesen: %w", err)
	}
	defer rows.Close()
	out := []State{}
	for rows.Next() {
		var s State
		if err := rows.Scan(&s.Collection, &s.Revision, &s.SyncedAt); err != nil {
			return nil, fmt.Errorf("sync_state lesen: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// Revision ist die Revision der Replica: der Stand, bis zu dem alle ihre
// Collections abgeglichen sind. Ohne Collection 0.
func (r *Replica) Revision(ctx context.Context) (int64, error) {
	var n, rev int64
	if err := r.db.QueryRowContext(ctx, qStateMin).Scan(&n, &rev); err != nil {
		return 0, fmt.Errorf("sync_state lesen: %w", err)
	}
	return rev, nil
}

// Collections liest alle Collections, von denen die Replica etwas hat: einen
// Stand oder Zeilen.
func (r *Replica) Collections(ctx context.Context) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, qCollectionsAll)
	if err != nil {
		return nil, fmt.Errorf("Collections der Replica lesen: %w", err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (r *Replica) requireCollection(ctx context.Context, collection string) error {
	var n int
	if err := r.db.QueryRowContext(ctx, qStateGet, collection).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("Collection %s %w", collection, ErrNotFound)
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
	if content.Valid {
		d.Content = &content.String
	}
	if meta.Valid {
		d.Meta = &meta.String
	}
	d.Deleted = deleted != 0
	return d, nil
}

// Document liest das lebende Dokument eines Namens: nie eine Löschmarke,
// nie eine SYSTEM:-Zeile. Eine Collection ohne Stand in der Replica ist
// ErrNotFound, ebenso ein fehlendes Dokument.
func (r *Replica) Document(ctx context.Context, collection, name string) (Document, error) {
	if err := ident.CheckDocName(name); err != nil {
		return Document{}, err
	}
	if err := r.requireCollection(ctx, collection); err != nil {
		return Document{}, err
	}
	d, err := scanDocument(r.db.QueryRowContext(ctx, qDocLive, collection, name))
	if errors.Is(err, sql.ErrNoRows) {
		return Document{}, fmt.Errorf("Dokument %s in %s %w", name, collection, ErrNotFound)
	}
	if err != nil {
		return Document{}, fmt.Errorf("Dokument %s lesen: %w", name, err)
	}
	return d, nil
}

// Documents liest die lebenden Dokumente einer Collection unter einem
// Verzeichnis ("" für alle), nach Name: nie Löschmarken, nie SYSTEM:-Zeilen.
// Tragen zwei lebende Zeilen denselben Namen, gilt die jüngste. Eine
// Collection ohne Stand in der Replica ist ErrNotFound.
func (r *Replica) Documents(ctx context.Context, collection, dir string) ([]Document, error) {
	prefix, err := ident.DocDirPrefix(dir)
	if err != nil {
		return nil, err
	}
	if err := r.requireCollection(ctx, collection); err != nil {
		return nil, err
	}
	hi := ""
	if prefix != "" {
		hi = prefix[:len(prefix)-1] + "0"
	}
	rows, err := r.db.QueryContext(ctx, qDocsLive, collection, prefix, hi, hi)
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
		if n := len(out); n > 0 && out[n-1].Name == d.Name {
			continue
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Schreiben.

// page ist eine Seite, wie der Abgleich sie anwendet.
type page struct {
	// rows sind die Zeilen der Seite.
	rows []contract.Row
	// advance sind die Collections, deren Stand auf max(bisher, until)
	// rückt, mit ihrem bisherigen Stand.
	advance map[string]int64
	until   int64
	// drop sind Collections, die ganz aus der Replica verschwinden.
	drop []string
	now  int64
	// known lässt nur Zeilen aus Collections zu, die die Replica schon führt
	// (Stand in sync_state); die übrigen übergeht apply.
	known bool
}

// dropped ist das Ergebnis des Entfernens einer Collection: wie viele
// Zeilen gingen.
type dropped struct {
	rows int64
}

// apply wendet eine Seite in einer Transaktion an: Collections entfernen,
// Zeilen per id einfügen oder ersetzen, Stände fortschreiben — nie zurück.
// Mit known nur Zeilen aus Collections, die einen Stand haben.
//
// Zuerst prüft sie, dass die Seite noch passt: Die Replica gehört noch zum
// selben Eintrag und Hub (checkOwner), und keine Collection steht unter dem
// Stand, ab dem der Aufrufer gefragt hat — sonst hat sie ein anderer
// inzwischen geleert oder entfernt, und die Seite ließe eine Lücke. Dann
// ErrChanged; geschrieben ist nichts.
func (r *Replica) apply(ctx context.Context, p page) (map[string]dropped, error) {
	out := map[string]dropped{}
	err := r.inTx(ctx, func(tx *sql.Tx) error {
		if err := r.checkOwner(ctx, tx); err != nil {
			return err
		}
		for c, since := range p.advance {
			if since == 0 {
				continue
			}
			var cur int64
			if err := tx.QueryRowContext(ctx, qStateRev, c).Scan(&cur); err != nil {
				return fmt.Errorf("sync_state lesen: %w", err)
			}
			if cur < since {
				return ErrChanged
			}
		}
		for _, c := range p.drop {
			d, err := dropCollection(ctx, tx, c)
			if err != nil {
				return err
			}
			out[c] = d
		}
		carried := map[string]bool{}
		for _, row := range p.rows {
			if p.known {
				ok, seen := carried[row.Collection]
				if !seen {
					var n int
					if err := tx.QueryRowContext(ctx, qStateGet, row.Collection).Scan(&n); err != nil {
						return fmt.Errorf("sync_state lesen: %w", err)
					}
					ok = n > 0
					carried[row.Collection] = ok
				}
				if !ok {
					continue
				}
			}
			deleted := 0
			if row.Deleted {
				deleted = 1
			}
			if _, err := tx.ExecContext(ctx, qDocUpsert, row.ID, row.Collection, row.Name, row.Content, row.Meta,
				deleted, row.Revision, row.CreatedAt, row.CreatedBy, row.UpdatedAt, row.UpdatedBy); err != nil {
				return fmt.Errorf("Dokument %s in %s schreiben: %w", row.Name, row.Collection, err)
			}
		}
		for c, since := range p.advance {
			if _, err := tx.ExecContext(ctx, qStateUpsert, c, max(since, p.until), p.now); err != nil {
				return fmt.Errorf("sync_state schreiben: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func dropCollection(ctx context.Context, tx *sql.Tx, collection string) (dropped, error) {
	res, err := tx.ExecContext(ctx, qDocsDeleteOf, collection)
	if err != nil {
		return dropped{}, fmt.Errorf("Collection %s entfernen: %w", collection, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return dropped{}, err
	}
	if _, err := tx.ExecContext(ctx, qStateDeleteOf, collection); err != nil {
		return dropped{}, fmt.Errorf("Collection %s entfernen: %w", collection, err)
	}
	return dropped{rows: n}, nil
}

// reset leert die Replica und schreibt die neue hub_id und eine neue
// generation, in einer Transaktion.
func (r *Replica) reset(ctx context.Context, hubID string) error {
	gen := ulid.Make().String()
	err := r.inTx(ctx, func(tx *sql.Tx) error {
		info, err := sqlitedb.ReadInfo(ctx, tx)
		if err != nil {
			return fmt.Errorf("db_info lesen: %w", err)
		}
		if info[KeyEntryID] != r.entryID {
			return ErrChanged
		}
		for _, del := range []string{qDocsDeleteAll, qStateDeleteAll} {
			if _, err := tx.ExecContext(ctx, del); err != nil {
				return err
			}
		}
		if err := sqlitedb.SetInfo(ctx, tx, KeyGeneration, gen); err != nil {
			return err
		}
		return sqlitedb.SetInfo(ctx, tx, KeyHubID, hubID)
	})
	if errors.Is(err, ErrChanged) {
		return err
	}
	if err != nil {
		return fmt.Errorf("Replica leeren: %w", err)
	}
	r.hubID, r.generation = hubID, gen
	return nil
}
