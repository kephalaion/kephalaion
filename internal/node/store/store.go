// Package store kapselt die Datenbank des Nodes (node.db): seine
// Einstellungen, Hub-Einträge und gewünschten Collections. Die Replicas liegen
// als eigene Dateien im Verzeichnis replicas/ daneben (internal/node/replica);
// hier steht nur, wo. Anders als beim Hub ist hier SQLite-Eigenes erlaubt —
// dort kommt später FTS5.
//
// Der Node kennt den Hub nicht über dessen Pakete: Kein Paket unter
// internal/node importiert eines unter internal/hub, und umgekehrt.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strconv"

	"github.com/kephalaion/kephalaion/internal/config"
	"github.com/kephalaion/kephalaion/internal/sqlitedb"
)

// Role ist die Rolle, die in db_info steht.
const Role = string(config.Node)

// SchemaVersion ist die Schemafassung, die dieses Binary erwartet. Es gibt
// noch keine Migrationen: Passt die Fassung nicht, ist die Datenbank neu
// anzulegen. Fassung 5 bringt hubs.ca (Task 018).
const SchemaVersion = 5

// Info beschreibt eine geöffnete Node-Datenbank.
type Info struct {
	SchemaVersion int
}

// Store ist der Zugriff des Nodes auf seine Datenbank. hubInConfig sagt bei
// den prüfenden Methoden, ob in derselben config ein Hub eingerichtet ist —
// nur dann ist Transport local erlaubt. Der Node liest das aus der config,
// nicht aus dem Hub.
type Store interface {
	Info(ctx context.Context) (Info, error)
	Settings(ctx context.Context) (map[string]string, error)
	// ReplaceSettings ersetzt alle settings in einer Transaktion.
	ReplaceSettings(ctx context.Context, settings map[string]string) error
	// SetSetting setzt einen bekannten Schlüssel nach Prüfung des Werts
	// (CheckSetting); UnsetSetting entfernt ihn und sagt, ob er gesetzt war.
	SetSetting(ctx context.Context, key, value string) error
	UnsetSetting(ctx context.Context, key string) (bool, error)

	Hubs(ctx context.Context) ([]Hub, error)
	Hub(ctx context.Context, name string) (Hub, error)
	AddHub(ctx context.Context, h Hub, hubInConfig bool) error
	// SetHub ändert die angegebenen Felder und prüft den Eintrag danach mit
	// denselben Regeln wie AddHub; siehe ApplyUpdate.
	SetHub(ctx context.Context, name string, u HubUpdate, hubInConfig bool) error
	SetHubToken(ctx context.Context, name, token string) error
	// SetHubID schreibt die Kopie der hub_id in den Hub-Eintrag. Maßgeblich
	// ist die hub_id in db_info der Replica; der Abgleich schreibt die Kopie
	// danach, für Anzeige und Export. Geschrieben wird nur, solange der
	// Eintrag noch der mit entryID ist, sonst ErrEntryGone.
	SetHubID(ctx context.Context, name, entryID, hubID string) error
	// RemoveHub entfernt einen Hub-Eintrag samt seinen gewünschten
	// Collections, seinem Stand des Abgleichs und seiner Replica.
	RemoveHub(ctx context.Context, name string) error

	// RecordSync hält das Ergebnis eines Abgleichs im Stand des Eintrags
	// fest (hub_sync) — nur, solange der Eintrag noch der mit entryID ist,
	// sonst ErrEntryGone.
	RecordSync(ctx context.Context, name, entryID string, rec SyncRecord) error
	// SyncStatus liest den Stand des Abgleichs aller Einträge, nach Alias.
	// Ein Eintrag ohne Zeile fehlt in der Map.
	SyncStatus(ctx context.Context) (map[string]SyncStatus, error)
	// ReplicaPath ist der Ort der Replica eines Hub-Eintrags:
	// replicas/<alias>.db neben node.db. Die Datei muss es nicht geben.
	ReplicaPath(name string) string

	Collections(ctx context.Context) ([]Wanted, error)
	// AddCollection prüft nur, dass es den Hub-Eintrag gibt; ob der Hub die
	// Collection erlaubt, zeigt sich erst beim Abgleich.
	AddCollection(ctx context.Context, hub, collection string) error
	RemoveCollection(ctx context.Context, hub, collection string) error

	// Tables liest die lokalen Tabellen für den Export.
	Tables(ctx context.Context) (Tables, error)
	// Import ersetzt in einer Transaktion die settings und, wenn tables nicht
	// nil ist, die lokalen Tabellen. Die Replicas der Aliase, die danach
	// fehlen, entfernt es mit, wie RemoveHub; der Stand des Abgleichs geht
	// ganz. Ein Alias, der bleibt, behält seine entry_id.
	Import(ctx context.Context, settings map[string]string, tables *Tables, hubInConfig bool) error

	Close() error
}

// sqliteSchema ist das DDL des Nodes über den Unterbau hinaus, nach
// „Datenmodell“ im Konzept: seine Hubs und die Collections, die er von ihnen
// haben will. Beides gleicht sich nicht ab. entry_id ist die Kennung des
// Eintrags, beim Anlegen vergeben und nie wieder vergeben (ULID); an sie ist
// das Schreiben des Abgleichs gebunden. ca ist bei https die CA als PEM-Text,
// gegen die der Node das Zertifikat des Hubs prüft. hub_sync ist der Stand
// des Abgleichs je Eintrag — abgeleitet, nicht im Export.
const sqliteSchema = `
CREATE TABLE hubs (
  name        TEXT PRIMARY KEY,
  entry_id    TEXT NOT NULL UNIQUE,
  node_name   TEXT NOT NULL,
  transport   TEXT NOT NULL,
  address     TEXT,
  token       TEXT,
  ssh_key     TEXT,
  ca          TEXT,
  hub_id      TEXT
);
CREATE TABLE hub_collections (
  hub         TEXT NOT NULL REFERENCES hubs(name),
  collection  TEXT NOT NULL,
  PRIMARY KEY (hub, collection)
);
CREATE TABLE hub_sync (
  hub         TEXT PRIMARY KEY REFERENCES hubs(name),
  entry_id    TEXT NOT NULL,
  ok_at       INTEGER,
  error       TEXT,
  error_kind  TEXT,
  error_at    INTEGER
);
`

// Open öffnet eine vorhandene Node-Datenbank und prüft Rolle und
// Schemafassung. Eine fehlende Datei wird nicht angelegt.
func Open(ctx context.Context, addr config.DB) (Store, error) {
	if addr.Kind != config.SQLite {
		return nil, fmt.Errorf("Datenbankart %q: %w", addr.Kind, config.ErrUnsupported)
	}
	db, err := sqlitedb.Open(ctx, addr.Path)
	if err != nil {
		return nil, err
	}
	if _, err := sqlitedb.CheckInfo(ctx, db, Role, SchemaVersion); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("Datenbank %s: %w", addr.Path, err)
	}
	return &sqliteStore{db: db, path: addr.Path}, nil
}

// Create legt eine neue Node-Datenbank samt Schema an. Existiert die Datei
// schon, bricht Create ab. Scheitert das Anlegen, bleibt keine Datei zurück.
func Create(ctx context.Context, addr config.DB) (Store, error) {
	if addr.Kind != config.SQLite {
		return nil, fmt.Errorf("Datenbankart %q: %w", addr.Kind, config.ErrUnsupported)
	}
	db, err := sqlitedb.Create(ctx, addr.Path)
	if err != nil {
		return nil, err
	}
	if err := sqlitedb.CreateSchema(ctx, db, sqliteSchema, Role, SchemaVersion, nil); err != nil {
		_ = db.Close()
		_ = sqlitedb.Remove(addr.Path)
		return nil, err
	}
	return &sqliteStore{db: db, path: addr.Path}, nil
}

// ReplicaDir ist das Verzeichnis der Replicas neben node.db: replicas/.
func ReplicaDir(nodeDB string) string {
	return filepath.Join(filepath.Dir(nodeDB), "replicas")
}

// ReplicaPath ist der Ort der Replica eines Hub-Eintrags neben node.db:
// replicas/<alias>.db. Der Alias folgt der Namensregel und taugt deshalb als
// Dateiname.
func ReplicaPath(nodeDB, alias string) string {
	return filepath.Join(ReplicaDir(nodeDB), alias+".db")
}

type sqliteStore struct {
	db *sql.DB
	// path ist der Ort von node.db; neben ihr liegen die Replicas.
	path string
}

func (s *sqliteStore) ReplicaPath(name string) string { return ReplicaPath(s.path, name) }

func (s *sqliteStore) Info(ctx context.Context) (Info, error) {
	v, err := sqlitedb.GetInfo(ctx, s.db, sqlitedb.KeySchemaVersion)
	if err != nil {
		return Info{}, err
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return Info{}, fmt.Errorf("db_info: Schemafassung %q: %w", v, err)
	}
	return Info{SchemaVersion: n}, nil
}

func (s *sqliteStore) Settings(ctx context.Context) (map[string]string, error) {
	return sqlitedb.Settings(ctx, s.db)
}

func (s *sqliteStore) ReplaceSettings(ctx context.Context, settings map[string]string) error {
	return sqlitedb.ReplaceSettings(ctx, s.db, settings)
}

func (s *sqliteStore) Close() error { return s.db.Close() }
