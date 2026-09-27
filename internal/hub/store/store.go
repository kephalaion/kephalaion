// Package store kapselt die Datenbank des Hubs. Der Hub sieht nur die
// Schnittstelle Store; die erste Umsetzung ist SQLite, PostgreSQL soll folgen.
//
// Regel: Die Abfragen (DML) des Hubs stehen zentral in queries und laufen in
// SQLite und PostgreSQL — Platzhalter $n über sqlq.Bind, kein INSERT OR, kein
// PRAGMA, kein AUTOINCREMENT. Ein Test prüft das. Das DDL steht je Dialekt.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"

	"github.com/oklog/ulid/v2"

	"github.com/kephalaion/kephalaion/internal/config"
	"github.com/kephalaion/kephalaion/internal/contract"
	"github.com/kephalaion/kephalaion/internal/sqlitedb"
	"github.com/kephalaion/kephalaion/internal/sqlq"
)

// Role ist die Rolle, die in db_info steht.
const Role = string(config.Hub)

// SchemaVersion ist die Schemafassung, die dieses Binary erwartet. Es gibt
// noch keine Migrationen: Passt die Fassung nicht, ist die Datenbank neu
// anzulegen.
const SchemaVersion = 5

// Zeilen in db_info, die nur der Hub hat.
const (
	// keyRevision zählt die Revision des Hubs.
	keyRevision = "revision"
	// keyHubID ist die Kennung des Hubs, eine ULID, vergeben beim Anlegen.
	keyHubID = "hub_id"
)

// Info beschreibt eine geöffnete Hub-Datenbank.
type Info struct {
	SchemaVersion int
	Revision      int64
	HubID         string
}

// Stats sind die Kennzahlen für status.
type Stats struct {
	// Documents zählt Dokumente ohne Löschmarken und ohne SYSTEM:-Zeilen.
	Documents int64
}

// Store ist der Zugriff des Hubs auf seine Datenbank. Jede Änderung läuft in
// einer Transaktion und schreibt in actions, als Account admin ohne Träger —
// außer RotateAccount und den Schreibvorgängen …As, die ein Account über
// einen Node auslöst: Dort steht der Account in actions, der Node als
// carrier, und sein User in created_by/updated_by.
type Store interface {
	Info(ctx context.Context) (Info, error)
	Stats(ctx context.Context) (Stats, error)
	Settings(ctx context.Context) (map[string]string, error)
	// ReplaceSettings ersetzt alle settings in einer Transaktion.
	ReplaceSettings(ctx context.Context, settings map[string]string) error

	Collections(ctx context.Context) ([]Collection, error)
	AddCollection(ctx context.Context, name, description string) error
	SetCollectionDescription(ctx context.Context, name, description string) error
	// RemoveCollection entfernt eine Collection, die kein Node erlaubt hat, in
	// der kein gesperrter Account Rechte gemerkt hat und in der keine
	// Dokumente stehen. Löschmarken von Account-Zeilen zählen nicht; sie
	// bleiben stehen.
	RemoveCollection(ctx context.Context, name string) error

	Nodes(ctx context.Context) ([]Node, error)
	Node(ctx context.Context, name string) (Node, error)
	// AddNode legt einen Node an und liefert sein Token. Gespeichert wird nur
	// der Hash; das Token gibt es danach nicht wieder.
	AddNode(ctx context.Context, name, description string) (token string, err error)
	SetNodeDescription(ctx context.Context, name, description string) error
	SetNodeLocked(ctx context.Context, name string, locked bool) error
	// NewNodeToken ersetzt das Token eines Nodes und liefert das neue.
	NewNodeToken(ctx context.Context, name string) (token string, err error)
	// RemoveNode entfernt einen Node samt seinen erlaubten Collections.
	RemoveNode(ctx context.Context, name string) error
	Grant(ctx context.Context, node, collection string) error
	Revoke(ctx context.Context, node, collection string) error

	// Accounts liest alle Accounts samt ihren Rechten, nach Name.
	Accounts(ctx context.Context) ([]Account, error)
	// AccountsOfUser liest die Accounts eines Users samt Rechten, nach Name —
	// über den Index auf accounts."user".
	AccountsOfUser(ctx context.Context, user string) ([]Account, error)
	Account(ctx context.Context, name string) (Account, error)
	// AddAccount legt einen Account ohne Collections an und liefert sein
	// Einrichtungstoken. Gespeichert wird nur der Hash. user ist der User des
	// Accounts (Namensregel wie Accounts, nicht admin); den Vorgabewert — den
	// Namen des Accounts — setzt der Aufrufer.
	AddAccount(ctx context.Context, name, user, description string) (token string, err error)
	// SetAccount ändert Beschreibung und/oder User eines Accounts in einem
	// Schreibvorgang. Ein neuer User steht danach in accounts und in allen
	// lebenden Zeilen des Accounts, unter einer Revision; Löschmarken und
	// Dokumente bleiben unberührt.
	SetAccount(ctx context.Context, name string, change AccountChange) error
	// SetAccountLocked sperrt einen Account — seine Zeilen werden
	// Löschmarken, die Rechte merkt sich accounts — oder hebt die Sperre auf
	// und legt die Zeilen neu an.
	SetAccountLocked(ctx context.Context, name string, locked bool) error
	// NewAccountToken ersetzt das Token eines Accounts in accounts und in
	// allen seinen Zeilen und liefert das neue Einrichtungstoken.
	NewAccountToken(ctx context.Context, name string) (token string, err error)
	// RemoveAccount entfernt einen Account; seine Zeilen werden Löschmarken.
	RemoveAccount(ctx context.Context, name string) error
	// GrantAccount setzt die Rechte eines Accounts in einer Collection
	// vollständig: legt die Zeile an oder ändert sie. changed ist false, wenn
	// schon genau diese Rechte galten; dann ist nichts geschrieben.
	GrantAccount(ctx context.Context, name, collection string, rights contract.Rights) (changed bool, err error)
	// RevokeAccount nimmt einem Account eine Collection: Die Zeile wird eine
	// Löschmarke.
	RevokeAccount(ctx context.Context, name, collection string) error
	// RotateAccount ersetzt den Hash des Tokens eines Accounts in accounts
	// und allen seinen Zeilen, in einer Transaktion und unter einer
	// Revision, mit einer Zeile rotate in actions (account ist der Account,
	// carrier der Node); updated_by der Zeilen ist der User des Accounts. Es
	// prüft in der Transaktion noch einmal, dass oldHash gilt und der Account
	// nicht gesperrt ist (sonst ErrAccountAuth), und dass er mindestens eine
	// der Collections in shared hat (sonst ErrNoSharedCollection, ohne
	// Änderung). Es liefert die Zeilen des Accounts in diesen Collections.
	RotateAccount(ctx context.Context, name, oldHash, newHash, carrier string, shared []string) ([]contract.Row, error)

	// Tables liest die lokalen Tabellen für den Export.
	Tables(ctx context.Context) (Tables, error)
	// Import ersetzt in einer Transaktion die settings und, wenn tables nicht
	// nil ist, die lokalen Tabellen, und schreibt config.import in actions.
	// Die Accounts gleicht es nur an, wenn tables.KeepAccounts nicht gilt:
	// accounts ersetzen und die SYSTEM:A:-Zeilen angleichen, unter einer
	// Revision.
	Import(ctx context.Context, settings map[string]string, tables *Tables) error

	// PutDocument legt ein Dokument an oder ersetzt seinen Inhalt — der
	// Admin-Upsert der CLI. Unveränderter Inhalt schreibt nichts und zählt
	// keine Revision.
	PutDocument(ctx context.Context, collection, name, content string) (PutResult, error)
	// Document liest ein lebendes Dokument; eine Löschmarke gilt als nicht
	// vorhanden.
	Document(ctx context.Context, collection, name string) (Document, error)
	// Documents liest die lebenden Dokumente unter einem Verzeichnis (samt
	// Unterverzeichnissen, "" für alle), nach Name sortiert, ohne
	// SYSTEM:-Zeilen.
	Documents(ctx context.Context, collection, dir string) ([]Document, error)
	// DeleteDocument setzt eine Löschmarke: Inhalt NULL, deleted = 1, neue
	// Revision.
	DeleteDocument(ctx context.Context, collection, name string) (Document, error)
	// ImportDocuments legt an oder ersetzt wie PutDocument, alle Dokumente in
	// einer Transaktion und unter einer Revision. Was fehlt, bleibt.
	ImportDocuments(ctx context.Context, collection string, docs []DocumentInput) (ImportResult, error)

	// Schreiben über einen Node (write.go): im Namen von auth.Account, mit
	// auth.Carrier als Träger, je Vorgang eine Transaktion und höchstens eine
	// Revision. Ihre erste Anweisung sperrt die Zeile des Accounts in
	// accounts; geprüft wird darin die Anmeldung (ErrAccountAuth), die
	// Lesbarkeit (ErrNotReadable) und das Recht (ErrForbidden): write für
	// Neues und Eigenes, supersede für Fremdes. Name und Inhalt prüfen sie
	// wie PutDocument, ungültig ist ErrInvalid. Das Ergebnis trägt die
	// Zeilen in der Form von SyncRows.

	// CreateDocumentAs legt ein Dokument an. Trägt ein lebendes Dokument den
	// Namen: ErrNameTaken; eine Löschmarke hindert nicht, das Dokument
	// bekommt eine neue id. Datei und Verzeichnis zugleich: ErrPathConflict.
	CreateDocumentAs(ctx context.Context, auth WriteAuth, collection, name, content string) (WriteResult, error)
	// WriteDocumentAs ersetzt den Inhalt eines lebenden Dokuments (sonst
	// ErrNotFound). Nennt base eine andere Revision als die des Dokuments:
	// ErrStaleRevision, geprüft vor dem Vergleich des Inhalts; nil heißt ohne
	// Vorbedingung. Unveränderter Inhalt schreibt nichts, das Ergebnis trägt
	// dann die bestehende Zeile und ihre Revision.
	WriteDocumentAs(ctx context.Context, auth WriteAuth, collection, name, content string, base *int64) (WriteResult, error)
	// DeleteDocumentAs setzt eine Löschmarke auf ein lebendes Dokument; base
	// wie bei WriteDocumentAs, recursive gilt für ein Dokument nicht. Ist name
	// ein Verzeichnis (lebende Dokumente darunter), nur mit recursive, sonst
	// ErrInvalid: dann alle Dokumente darunter, unter einer Revision, alles
	// oder nichts — das Recht je Dokument; base ist bei einem Verzeichnis
	// ErrInvalid. Weder Dokument noch Verzeichnis: ErrNotFound. Das Ergebnis
	// trägt die Löschmarken, nach Name.
	DeleteDocumentAs(ctx context.Context, auth WriteAuth, collection, name string, base *int64, recursive bool) (WriteResult, error)
	// RenameDocumentAs gibt einem Dokument oder allen Dokumenten unter einem
	// Verzeichnis einen neuen Namen (newName bzw. newName/…); id, Inhalt und
	// created_by bleiben. Eine Revision, alles oder nichts, das Recht je
	// Dokument wie bei WriteDocumentAs. Name und neuer Name nach
	// ident.CheckRename, jeder neue Name nach ident.CheckDocName (ErrInvalid).
	// Ziel belegt — ein Dokument auf ein lebendes Dokument, ein Verzeichnis auf
	// ein Verzeichnis —: ErrNameTaken, nichts wird überschrieben oder
	// zusammengelegt. Wäre ein neuer Name zugleich Datei und Verzeichnis:
	// ErrPathConflict. base wie bei DeleteDocumentAs. Das Ergebnis trägt die
	// Zeilen unter den neuen Namen, nach Name.
	RenameDocumentAs(ctx context.Context, auth WriteAuth, collection, name, newName string, base *int64) (WriteResult, error)

	// SyncRows liest für den Abgleich die Zeilen der Collections in since
	// mit revision > Since der jeweiligen Collection und revision ≤ upTo,
	// sortiert nach Revision, dann id, höchstens limit (0: ohne Grenze). Alle
	// Spalten, auch Löschmarken und SYSTEM:-Zeilen; NULL bleibt nil. Ohne
	// Transaktion. Rechte prüft der Aufrufer.
	SyncRows(ctx context.Context, since []contract.Since, upTo int64, limit int) ([]contract.Row, error)

	Close() error
}

// queries sind die Abfragetexte des Hubs, Platzhalter $n.
var queries = struct {
	CountDocuments string
	LockRevision   string

	ActionInsert         string
	ActionInsertDocument string
	ActionInsertFull     string

	DocumentLive       string
	DocumentByID       string
	DocumentsAll       string
	DocumentsInDir     string
	DocumentLiveCount  string
	DocumentsUnderLive string
	DocumentInsert     string
	DocumentReplace    string
	DocumentDelete     string
	DocumentRename     string

	SyncRowsHead   string
	SyncRowsClause string
	SyncRowsOr     string
	SyncRowsOrder  string
	SyncRowsLimit  string

	CollectionsAll        string
	CollectionGet         string
	CollectionInsert      string
	CollectionSetDesc     string
	CollectionDelete      string
	CollectionsDeleteAll  string
	CollectionCountDocs   string
	CollectionGrantedNode string

	NodesAll       string
	NodeGet        string
	NodeInsert     string
	NodeSetDesc    string
	NodeSetLocked  string
	NodeSetToken   string
	NodeDelete     string
	NodesDeleteAll string
	NodeCount      string

	AccountsAll        string
	AccountsOfUser     string
	AccountGet         string
	AccountLock        string
	AccountsLockAll    string
	AccountRotate      string
	AccountCount       string
	AccountInsert      string
	AccountSetDesc     string
	AccountSetUser     string
	AccountSetLocked   string
	AccountSetToken    string
	AccountDelete      string
	AccountsDeleteAll  string
	AccountRowsLive    string
	AccountRowsAllLive string
	AccountRowLatest   string
	AccountRowRevive   string

	NameClaim      string
	NameKind       string
	NameRelease    string
	NamesDeleteAll string

	GrantsAll       string
	GrantsOfNode    string
	GrantGet        string
	GrantInsert     string
	GrantDelete     string
	GrantsDeleteOf  string
	GrantsDeleteAll string
}{
	CountDocuments: `SELECT COUNT(*) FROM documents
		WHERE deleted = 0 AND substr(name, 1, 7) <> 'SYSTEM:'`,
	// LockRevision sperrt die Zeile der Revision schreibend, bevor sie gelesen
	// wird — für PostgreSQL, wo sonst zwei Schreiber dieselbe Revision
	// vergäben. Unter SQLite sperrt schon BEGIN IMMEDIATE.
	LockRevision: `UPDATE db_info SET value = value WHERE key = $1`,

	ActionInsert: `INSERT INTO actions (at, account, action, subject) VALUES ($1, $2, $3, $4)`,
	ActionInsertDocument: `INSERT INTO actions (at, account, carrier, action, document_id, revision)
		VALUES ($1, $2, $3, $4, $5, $6)`,
	ActionInsertFull: `INSERT INTO actions (at, account, carrier, action, subject, revision)
		VALUES ($1, $2, $3, $4, $5, $6)`,

	// Dokumente: documentColumns in dieser Reihenfolge, gelesen mit scanDocument.
	DocumentLive: `SELECT ` + documentColumns + ` FROM documents
		WHERE collection = $1 AND name = $2 AND deleted = 0`,
	DocumentByID: `SELECT ` + documentColumns + ` FROM documents WHERE id = $1`,
	// DocumentsAll und DocumentsInDir lesen die lebenden Dokumente einer
	// Collection bzw. unter einem Verzeichnis, ohne SYSTEM:-Zeilen, nach Name.
	// Das Verzeichnis grenzt ein Bereich ein: name >= 'tasks/' AND name <
	// 'tasks0' ('0' folgt auf '/') — nutzt den Index auf (collection, name).
	DocumentsAll: `SELECT ` + documentColumns + ` FROM documents
		WHERE collection = $1 AND deleted = 0 AND substr(name, 1, 7) <> 'SYSTEM:'
		ORDER BY name`,
	DocumentsInDir: `SELECT ` + documentColumns + ` FROM documents
		WHERE collection = $1 AND deleted = 0 AND name >= $2 AND name < $3
		AND substr(name, 1, 7) <> 'SYSTEM:'
		ORDER BY name`,
	DocumentLiveCount: `SELECT COUNT(*) FROM documents
		WHERE collection = $1 AND name = $2 AND deleted = 0`,
	DocumentsUnderLive: `SELECT COUNT(*) FROM documents
		WHERE collection = $1 AND deleted = 0 AND name >= $2 AND name < $3`,
	// meta bleibt NULL; der Hub deutet es nicht, und die CLI setzt es nicht.
	DocumentInsert: `INSERT INTO documents
		(id, collection, name, content, meta, deleted, revision, created_at, created_by, updated_at, updated_by)
		VALUES ($1, $2, $3, $4, NULL, 0, $5, $6, $7, $8, $9)`,
	DocumentReplace: `UPDATE documents SET content = $2, revision = $3, updated_at = $4, updated_by = $5
		WHERE id = $1`,
	// Die Löschmarke behält id, Collection und Name, verliert Inhalt und meta.
	DocumentDelete: `UPDATE documents SET content = NULL, meta = NULL, deleted = 1,
		revision = $2, updated_at = $3, updated_by = $4
		WHERE id = $1`,
	// Umbenennen behält id, Inhalt und created_by.
	DocumentRename: `UPDATE documents SET name = $2, revision = $3, updated_at = $4, updated_by = $5
		WHERE id = $1`,

	// SyncRows… setzen die Abfrage des Abgleichs zusammen (syncRowsQuery):
	// Kopf, je Collection eine Klausel, durch OR verbunden, Ordnung,
	// wahlweise Grenze. Die Nummern der Platzhalter vergibt syncRowsQuery;
	// %d steht für sie. Jede Klausel nutzt den Index (collection, revision).
	SyncRowsHead:   `SELECT ` + documentColumns + ` FROM documents WHERE revision <= $1 AND (`,
	SyncRowsClause: `(collection = $%d AND revision > $%d)`,
	SyncRowsOr:     ` OR `,
	SyncRowsOrder:  `) ORDER BY revision, id`,
	SyncRowsLimit:  ` LIMIT $%d`,

	CollectionsAll: `SELECT name, COALESCE(description, ''), created_at, created_by
		FROM collections ORDER BY name`,
	CollectionGet: `SELECT name, COALESCE(description, ''), created_at, created_by
		FROM collections WHERE name = $1`,
	CollectionInsert: `INSERT INTO collections (name, description, created_at, created_by)
		VALUES ($1, $2, $3, $4)`,
	CollectionSetDesc:    `UPDATE collections SET description = $2 WHERE name = $1`,
	CollectionDelete:     `DELETE FROM collections WHERE name = $1`,
	CollectionsDeleteAll: `DELETE FROM collections`,
	// Jede Zeile zählt — auch Löschmarken von Dokumenten und lebende
	// SYSTEM:-Zeilen —, nur Löschmarken von Account-Zeilen nicht: Sie bleiben
	// stehen, wenn die Collection entfällt (documents hat keinen
	// Fremdschlüssel), und ein grant belebt sie in einer gleichnamigen neuen
	// Collection wieder. Ein Node, der offline war, bekommt so beim nächsten
	// Abgleich die Löschmarke bzw. die wiederbelebte Zeile.
	CollectionCountDocs: `SELECT COUNT(*) FROM documents WHERE collection = $1
		AND NOT (deleted = 1 AND substr(name, 1, 9) = 'SYSTEM:A:')`,
	CollectionGrantedNode: `SELECT node FROM node_collections WHERE collection = $1 ORDER BY node`,

	NodesAll: `SELECT name, COALESCE(description, ''), token_hash, locked, created_at, created_by
		FROM nodes ORDER BY name`,
	NodeGet: `SELECT name, COALESCE(description, ''), token_hash, locked, created_at, created_by
		FROM nodes WHERE name = $1`,
	NodeInsert: `INSERT INTO nodes (name, description, token_hash, locked, created_at, created_by)
		VALUES ($1, $2, $3, $4, $5, $6)`,
	NodeSetDesc:    `UPDATE nodes SET description = $2 WHERE name = $1`,
	NodeSetLocked:  `UPDATE nodes SET locked = $2 WHERE name = $1`,
	NodeSetToken:   `UPDATE nodes SET token_hash = $2 WHERE name = $1`,
	NodeDelete:     `DELETE FROM nodes WHERE name = $1`,
	NodesDeleteAll: `DELETE FROM nodes`,
	NodeCount:      `SELECT COUNT(*) FROM nodes WHERE name = $1`,

	// Die Spalte "user" steht immer in Anführungszeichen: user ist in
	// PostgreSQL ein reserviertes Wort (sqlq.Check prüft das).
	AccountsAll: `SELECT name, "user", COALESCE(description, ''), token_hash, locked, COALESCE(locked_rights, ''),
		created_at, created_by FROM accounts ORDER BY name`,
	// AccountsOfUser nutzt den Index accounts_user.
	AccountsOfUser: `SELECT name, "user", COALESCE(description, ''), token_hash, locked, COALESCE(locked_rights, ''),
		created_at, created_by FROM accounts WHERE "user" = $1 ORDER BY name`,
	AccountGet: `SELECT name, "user", COALESCE(description, ''), token_hash, locked, COALESCE(locked_rights, ''),
		created_at, created_by FROM accounts WHERE name = $1`,
	AccountCount: `SELECT COUNT(*) FROM accounts WHERE name = $1`,
	// AccountLock sperrt die Zeile eines Accounts schreibend, bevor sie
	// gelesen wird — die erste Anweisung jedes Schreibvorgangs an einem
	// Account (wie LockRevision; SELECT … FOR UPDATE versteht SQLite nicht).
	// Unter PostgreSQL (READ COMMITTED) liest so kein Schreibvorgang einen
	// Hash, den ein gleichzeitiger rotate gerade ersetzt. AccountsLockAll
	// sperrt beim Import alle Zeilen.
	AccountLock:     `UPDATE accounts SET name = name WHERE name = $1`,
	AccountsLockAll: `UPDATE accounts SET name = name`,
	// AccountRotate ist das bedingte Schreiben von rotate und zugleich seine
	// Sperre: Es trifft die Zeile nur, wenn das alte Token gilt und der
	// Account nicht gesperrt ist. Von zwei rotate mit demselben alten Token
	// trifft so nur eines.
	AccountRotate: `UPDATE accounts SET token_hash = $2 WHERE name = $1 AND token_hash = $3 AND locked = 0`,
	AccountInsert: `INSERT INTO accounts (name, "user", description, token_hash, locked, locked_rights, created_at, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
	AccountSetDesc:    `UPDATE accounts SET description = $2 WHERE name = $1`,
	AccountSetUser:    `UPDATE accounts SET "user" = $2 WHERE name = $1`,
	AccountSetLocked:  `UPDATE accounts SET locked = $2, locked_rights = $3 WHERE name = $1`,
	AccountSetToken:   `UPDATE accounts SET token_hash = $2 WHERE name = $1`,
	AccountDelete:     `DELETE FROM accounts WHERE name = $1`,
	AccountsDeleteAll: `DELETE FROM accounts`,
	// Account-Zeilen: Die Bedingung name LIKE 'SYSTEM:%' steht wörtlich wie im
	// Teilindex documents_system, damit SQLite und PostgreSQL ihn benutzen.
	// Sie allein grenzt nicht genau ein — LIKE unterscheidet in SQLite nicht
	// zwischen Groß- und Kleinschreibung —, genau grenzt name = $1 bzw.
	// substr ein.
	AccountRowsLive: `SELECT ` + documentColumns + ` FROM documents
		WHERE name = $1 AND name LIKE 'SYSTEM:%' AND deleted = 0
		ORDER BY collection`,
	AccountRowsAllLive: `SELECT ` + documentColumns + ` FROM documents
		WHERE name LIKE 'SYSTEM:%' AND substr(name, 1, 9) = 'SYSTEM:A:' AND deleted = 0
		ORDER BY name, collection`,
	// AccountRowLatest liest die Zeile eines Accounts in einer Collection, die
	// lebende vor einer Löschmarke: Eine Löschmarke wird wiederbelebt, statt
	// eine zweite Zeile anzulegen.
	AccountRowLatest: `SELECT ` + documentColumns + ` FROM documents
		WHERE collection = $1 AND name = $2
		ORDER BY deleted, revision DESC LIMIT 1`,
	AccountRowRevive: `UPDATE documents SET content = $2, meta = NULL, deleted = 0, revision = $3,
		created_at = $4, created_by = $5, updated_at = $4, updated_by = $5
		WHERE id = $1`,

	// principal_names: je Node und Account eine Zeile, der Primärschlüssel
	// sichert die gemeinsame Eindeutigkeit. NameClaim trifft keine Zeile, wenn
	// der Name schon belegt ist — ON CONFLICT DO NOTHING statt eines Fehlers,
	// der unter PostgreSQL die Transaktion abbräche.
	NameClaim:      `INSERT INTO principal_names (name, kind) VALUES ($1, $2) ON CONFLICT (name) DO NOTHING`,
	NameKind:       `SELECT kind FROM principal_names WHERE name = $1`,
	NameRelease:    `DELETE FROM principal_names WHERE name = $1`,
	NamesDeleteAll: `DELETE FROM principal_names`,

	GrantsAll:       `SELECT node, collection FROM node_collections ORDER BY node, collection`,
	GrantsOfNode:    `SELECT collection FROM node_collections WHERE node = $1 ORDER BY collection`,
	GrantGet:        `SELECT COUNT(*) FROM node_collections WHERE node = $1 AND collection = $2`,
	GrantInsert:     `INSERT INTO node_collections (node, collection) VALUES ($1, $2)`,
	GrantDelete:     `DELETE FROM node_collections WHERE node = $1 AND collection = $2`,
	GrantsDeleteOf:  `DELETE FROM node_collections WHERE node = $1`,
	GrantsDeleteAll: `DELETE FROM node_collections`,
}

// sqliteSchema ist das DDL des Hubs für SQLite, nach „Datenmodell“ im
// Konzept. Das PostgreSQL-DDL entsteht mit der PostgreSQL-Umsetzung.
const sqliteSchema = `
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
CREATE UNIQUE INDEX documents_name ON documents(collection, name) WHERE deleted = 0;
CREATE INDEX documents_revision ON documents(collection, revision);
CREATE INDEX documents_system ON documents(name) WHERE name LIKE 'SYSTEM:%';

CREATE TABLE actions (
  at          INTEGER NOT NULL,
  account     TEXT NOT NULL,
  carrier     TEXT,
  action      TEXT NOT NULL,
  document_id TEXT,
  subject     TEXT,
  revision    INTEGER
);

CREATE TABLE collections (
  name        TEXT PRIMARY KEY,
  description TEXT,
  created_at  INTEGER NOT NULL,
  created_by  TEXT NOT NULL
);
CREATE TABLE nodes (
  name        TEXT PRIMARY KEY,
  description TEXT,
  token_hash  TEXT NOT NULL,
  locked      INTEGER NOT NULL DEFAULT 0,
  created_at  INTEGER NOT NULL,
  created_by  TEXT NOT NULL
);
CREATE TABLE node_collections (
  node        TEXT NOT NULL REFERENCES nodes(name),
  collection  TEXT NOT NULL REFERENCES collections(name),
  PRIMARY KEY (node, collection)
);
CREATE TABLE accounts (
  name          TEXT PRIMARY KEY,
  "user"        TEXT NOT NULL,
  description   TEXT,
  token_hash    TEXT NOT NULL,
  locked        INTEGER NOT NULL DEFAULT 0,
  locked_rights TEXT,
  created_at    INTEGER NOT NULL,
  created_by    TEXT NOT NULL
);
CREATE INDEX accounts_user ON accounts("user");
CREATE TABLE principal_names (
  name        TEXT PRIMARY KEY,
  kind        TEXT NOT NULL
);
`

// Open öffnet eine vorhandene Hub-Datenbank und prüft Rolle und
// Schemafassung. Eine fehlende Datei wird nicht angelegt.
func Open(ctx context.Context, addr config.DB) (Store, error) {
	switch addr.Kind {
	case config.SQLite:
		db, err := sqlitedb.Open(ctx, addr.Path)
		if err != nil {
			return nil, err
		}
		if _, err := sqlitedb.CheckInfo(ctx, db, Role, SchemaVersion); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("Datenbank %s: %w", addr.Path, err)
		}
		return &sqliteStore{db: db}, nil
	default:
		return nil, fmt.Errorf("Datenbankart %q: %w", addr.Kind, config.ErrUnsupported)
	}
}

// Create legt eine neue Hub-Datenbank samt Schema an. Existiert die Datei
// schon, bricht Create ab. Scheitert das Anlegen, bleibt keine Datei zurück.
func Create(ctx context.Context, addr config.DB) (Store, error) {
	switch addr.Kind {
	case config.SQLite:
		db, err := sqlitedb.Create(ctx, addr.Path)
		if err != nil {
			return nil, err
		}
		extra := map[string]string{keyRevision: "0", keyHubID: ulid.Make().String()}
		if err := sqlitedb.CreateSchema(ctx, db, sqliteSchema, Role, SchemaVersion, extra); err != nil {
			_ = db.Close()
			_ = sqlitedb.Remove(addr.Path)
			return nil, err
		}
		return &sqliteStore{db: db}, nil
	default:
		return nil, fmt.Errorf("Datenbankart %q: %w", addr.Kind, config.ErrUnsupported)
	}
}

// sqliteStore ist die Umsetzung für SQLite.
type sqliteStore struct {
	db *sql.DB
	// traceTx umhüllt die Transaktion eines Schreibvorgangs an Accounts oder
	// über einen Node (begin); nur Tests setzen es, um die Reihenfolge der
	// Anweisungen zu sehen.
	traceTx func(sqlitedb.Querier) sqlitedb.Querier
}

func q(text string) string { return sqlq.Bind(sqlq.SQLite, text) }

func (s *sqliteStore) Info(ctx context.Context) (Info, error) {
	info, err := sqlitedb.ReadInfo(ctx, s.db)
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

func (s *sqliteStore) Stats(ctx context.Context) (Stats, error) {
	var st Stats
	if err := s.db.QueryRowContext(ctx, q(queries.CountDocuments)).Scan(&st.Documents); err != nil {
		return Stats{}, fmt.Errorf("Dokumente zählen: %w", err)
	}
	return st, nil
}

func (s *sqliteStore) Settings(ctx context.Context) (map[string]string, error) {
	return sqlitedb.Settings(ctx, s.db)
}

func (s *sqliteStore) ReplaceSettings(ctx context.Context, settings map[string]string) error {
	return sqlitedb.ReplaceSettings(ctx, s.db, settings)
}

func (s *sqliteStore) Close() error { return s.db.Close() }

// nextRevision zählt die Revision innerhalb der schreibenden Transaktion
// hoch und liefert die neue. Gesperrt, gelesen und geschrieben wird in
// derselben Transaktion, gerechnet im Code — keine SEQUENCE, kein
// Autoincrement, keine Umwandlung in SQL. Die Zeile wird zuerst schreibend
// gesperrt (für PostgreSQL; unter SQLite hält die Transaktion die Sperre seit
// BEGIN IMMEDIATE). Schreibvorgänge an Dokumenten holen sie über
// lazyRevision, höchstens einmal je Transaktion.
func nextRevision(ctx context.Context, tx sqlitedb.Querier) (int64, error) {
	if _, err := tx.ExecContext(ctx, q(queries.LockRevision), keyRevision); err != nil {
		return 0, fmt.Errorf("Revision sperren: %w", err)
	}
	v, err := sqlitedb.GetInfo(ctx, tx, keyRevision)
	if err != nil {
		return 0, err
	}
	rev, err := parseRevision(v)
	if err != nil {
		return 0, err
	}
	rev++
	if err := sqlitedb.SetInfo(ctx, tx, keyRevision, strconv.FormatInt(rev, 10)); err != nil {
		return 0, err
	}
	return rev, nil
}

func parseRevision(v string) (int64, error) {
	rev, err := strconv.ParseInt(v, 10, 64)
	if err != nil || rev < 0 {
		return 0, fmt.Errorf("db_info: Revision %q ist keine Zahl ≥ 0", v)
	}
	return rev, nil
}
