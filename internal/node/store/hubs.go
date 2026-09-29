package store

import (
	"context"
	"crypto/x509"
	"database/sql"
	"encoding/pem"
	"errors"
	"fmt"
	"net/url"

	"github.com/oklog/ulid/v2"

	"github.com/kephalaion/kephalaion/internal/ident"
	"github.com/kephalaion/kephalaion/internal/sqlitedb"
)

// Fehlerarten; die Meldungen nennen dazu, was betroffen ist.
var (
	ErrNotFound = errors.New("gibt es nicht")
	ErrExists   = errors.New("gibt es schon")
	// ErrEntryGone meldet, dass der Hub-Eintrag, für den geschrieben werden
	// sollte, nicht mehr besteht: entfernt, oder unter demselben Alias neu
	// angelegt (andere entry_id).
	ErrEntryGone = errors.New("wurde entfernt oder neu angelegt")
)

// Die Transporte, über die ein Node einen Hub erreicht.
const (
	TransportLocal = "local"
	TransportHTTP  = "http"
	TransportHTTPS = "https"
	TransportSSH   = "ssh"
)

// Transports sind alle Transporte in fester Reihenfolge.
var Transports = []string{TransportLocal, TransportHTTP, TransportHTTPS, TransportSSH}

// Hub ist ein Hub-Eintrag: eine Zeile in hubs. Collections sind die
// gewünschten Collections aus hub_collections; Tables führt sie getrennt.
type Hub struct {
	Name string
	// EntryID ist die Kennung des Eintrags: beim Anlegen vergeben, nie
	// wiederkehrend, nicht im Export. Ein Import behält sie für Aliase, die
	// bleiben. An sie ist das Schreiben des Abgleichs gebunden.
	EntryID string
	// NodeName ist der Name, unter dem der Hub diesen Node kennt; mit ihm
	// und dem Token meldet sich der Node beim Hub an.
	NodeName  string
	Transport string
	Address   string
	Token     string
	SSHKey    string
	// CA ist bei https die Zertifizierungsstelle, gegen die der Node das
	// Zertifikat des Hubs prüft: ein oder mehrere Zertifikate als PEM, der
	// Text selbst, nicht ein Pfad — der Node läuft global unter einem anderen
	// User als der Verwalter. Leer heißt: die System-Roots.
	CA string
	// HubID ist leer bis zum ersten Kontakt.
	HubID       string
	Collections []string
}

// HubUpdate nennt die Felder, die node hub set ändert; nil bleibt, wie es
// ist.
type HubUpdate struct {
	NodeName  *string
	Transport *string
	Address   *string
	SSHKey    *string
	CA        *string
}

// Wanted ist eine Zeile in hub_collections: der Node will die Collection
// von diesem Hub haben.
type Wanted struct {
	Hub        string
	Collection string
}

// Tables sind die lokalen Tabellen des Nodes, wie export und import sie
// behandeln. Hub.Collections bleibt dabei leer; die Wünsche stehen in Wanted.
type Tables struct {
	Hubs   []Hub
	Wanted []Wanted
}

// localHosts sind die Hosts, die http erlaubt: nur dieser Rechner.
var localHosts = map[string]bool{"localhost": true, "127.0.0.1": true, "::1": true}

// CheckHub prüft einen Hub-Eintrag für sich: Name, Token-Format und die
// Regeln seines Transports. hubInConfig sagt, ob in derselben config ein Hub
// eingerichtet ist — nur dann ist local erlaubt. Dass es höchstens einen
// local-Eintrag gibt, prüft checkHubs über alle Einträge.
func CheckHub(h Hub, hubInConfig bool) error {
	if err := ident.CheckName("Hub", h.Name); err != nil {
		return err
	}
	if h.NodeName == "" {
		return fmt.Errorf("Hub %s: es fehlt der Name, unter dem der Hub diesen Node kennt (--node)", h.Name)
	}
	if err := ident.CheckPrincipalName("Node", h.NodeName); err != nil {
		return fmt.Errorf("Hub %s: %w", h.Name, err)
	}
	if err := ident.CheckToken(h.Token); err != nil {
		return fmt.Errorf("Hub %s: %w", h.Name, err)
	}
	if h.SSHKey != "" && h.Transport != TransportSSH {
		return fmt.Errorf("Hub %s: --ssh-key gibt es nur bei Transport ssh", h.Name)
	}
	if h.CA != "" {
		if h.Transport != TransportHTTPS {
			return fmt.Errorf("Hub %s: --ca-file gibt es nur bei Transport https", h.Name)
		}
		if _, err := ParseCA(h.CA); err != nil {
			return fmt.Errorf("Hub %s: %w", h.Name, err)
		}
	}
	if h.HubID != "" {
		if _, err := ulid.ParseStrict(h.HubID); err != nil {
			return fmt.Errorf("Hub %s: hub_id %q ist keine ULID", h.Name, h.HubID)
		}
	}
	switch h.Transport {
	case TransportLocal:
		if !hubInConfig {
			return fmt.Errorf("Hub %s: Transport local verlangt einen Hub in derselben config (kephalaion hub init)", h.Name)
		}
		if h.Address != "" {
			return fmt.Errorf("Hub %s: Transport local hat keine Adresse", h.Name)
		}
	case TransportHTTP:
		u, err := url.Parse(h.Address)
		if err != nil || u.Scheme != "http" || u.Host == "" || !localHosts[u.Hostname()] || !plainURL(u) {
			return fmt.Errorf("Hub %s: Transport http verlangt eine Adresse http:// auf localhost, "+
				"127.0.0.1 oder ::1, etwa http://localhost:8080, ohne Query (sonst https)", h.Name)
		}
	case TransportHTTPS:
		u, err := url.Parse(h.Address)
		if err != nil || u.Scheme != "https" || u.Host == "" || !plainURL(u) {
			return fmt.Errorf("Hub %s: Transport https verlangt eine Adresse https://host[:port][/pfad], "+
				"ohne Query und ohne User", h.Name)
		}
	case TransportSSH:
		if h.Address == "" {
			return fmt.Errorf("Hub %s: Transport ssh verlangt eine Adresse [user@]host[:port]", h.Name)
		}
	case "":
		return fmt.Errorf("Hub %s: Transport fehlt", h.Name)
	default:
		return fmt.Errorf("Hub %s: unbekannter Transport %q; erlaubt sind local, http, https, ssh", h.Name, h.Transport)
	}
	return nil
}

// plainURL sagt, ob eine Hub-Adresse nur aus Schema, Host und wahlweise
// einem Pfad besteht — der Pfad ist ein Präfix, hinter dem der Client
// /v1/<vorgang> anhängt (ein Proxy nimmt ihn weg). Query, Fragment und User
// haben in einer Hub-Adresse nichts verloren.
func plainURL(u *url.URL) bool {
	return u.RawQuery == "" && u.Fragment == "" && u.User == nil
}

// ParseCA liest die Zertifikate einer CA aus PEM-Text: mindestens ein Block
// CERTIFICATE, jeder parsebar; andere Blöcke (ein Schlüssel etwa) sind ein
// Fehler. Dieselbe Prüfung für node hub add|set --ca-file und den Import.
func ParseCA(text string) ([]*x509.Certificate, error) {
	var certs []*x509.Certificate
	rest := []byte(text)
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			return nil, fmt.Errorf("--ca-file: erwartet nur Zertifikate (CERTIFICATE), nicht %s", block.Type)
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("--ca-file: Zertifikat %d nicht lesbar: %v", len(certs)+1, err)
		}
		certs = append(certs, cert)
	}
	if len(certs) == 0 {
		return nil, errors.New("--ca-file: kein Zertifikat gefunden (erwartet PEM mit -----BEGIN CERTIFICATE-----)")
	}
	return certs, nil
}

// checkHubs prüft alle Hub-Einträge zusammen: jeder für sich, Namen
// eindeutig, höchstens ein local.
func checkHubs(hubs []Hub, hubInConfig bool) error {
	names := map[string]bool{}
	local := ""
	for _, h := range hubs {
		if err := CheckHub(h, hubInConfig); err != nil {
			return err
		}
		if names[h.Name] {
			return fmt.Errorf("Hub %s %w", h.Name, ErrExists)
		}
		names[h.Name] = true
		if h.Transport == TransportLocal {
			if local != "" {
				return fmt.Errorf("Hub %s: es gibt schon einen Eintrag mit Transport local (%s); höchstens einer je Node", h.Name, local)
			}
			local = h.Name
		}
	}
	return nil
}

// CheckTables prüft lokale Tabellen ohne Datenbank, wie die CLI es beim
// Anlegen tut.
func CheckTables(t Tables, hubInConfig bool) error {
	if err := checkHubs(t.Hubs, hubInConfig); err != nil {
		return err
	}
	hubs := map[string]bool{}
	for _, h := range t.Hubs {
		hubs[h.Name] = true
	}
	seen := map[Wanted]bool{}
	for _, w := range t.Wanted {
		addr := ident.Address(w.Hub, w.Collection)
		if _, _, err := ident.ParseAddress(addr); err != nil {
			return err
		}
		if !hubs[w.Hub] {
			return fmt.Errorf("Collection %s: Hub %s %w", addr, w.Hub, ErrNotFound)
		}
		if seen[w] {
			return fmt.Errorf("Collection %s %w", addr, ErrExists)
		}
		seen[w] = true
	}
	return nil
}

// Abfragen des Nodes. Hier ist SQLite-Eigenes erlaubt; nötig ist es nicht.
const (
	qHubsAll = `SELECT name, entry_id, node_name, transport, COALESCE(address, ''), COALESCE(token, ''),
		COALESCE(ssh_key, ''), COALESCE(ca, ''), COALESCE(hub_id, '') FROM hubs ORDER BY name`
	qHubGet = `SELECT name, entry_id, node_name, transport, COALESCE(address, ''), COALESCE(token, ''),
		COALESCE(ssh_key, ''), COALESCE(ca, ''), COALESCE(hub_id, '') FROM hubs WHERE name = ?`
	qHubInsert = `INSERT INTO hubs (name, entry_id, node_name, transport, address, token, ssh_key, ca, hub_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`
	qHubUpdate = `UPDATE hubs SET node_name = ?, transport = ?, address = ?, ssh_key = ?, ca = ? WHERE name = ?`
	qHubToken  = `UPDATE hubs SET token = ? WHERE name = ?`
	qHubID     = `UPDATE hubs SET hub_id = ? WHERE name = ? AND entry_id = ?`
	qHubDelete = `DELETE FROM hubs WHERE name = ?`
	qHubsClear = `DELETE FROM hubs`

	qWantedAll    = `SELECT hub, collection FROM hub_collections ORDER BY hub, collection`
	qWantedOfHub  = `SELECT collection FROM hub_collections WHERE hub = ? ORDER BY collection`
	qWantedGet    = `SELECT COUNT(*) FROM hub_collections WHERE hub = ? AND collection = ?`
	qWantedInsert = `INSERT INTO hub_collections (hub, collection) VALUES (?, ?)`
	qWantedDelete = `DELETE FROM hub_collections WHERE hub = ? AND collection = ?`
	qWantedOfDel  = `DELETE FROM hub_collections WHERE hub = ?`
	qWantedClear  = `DELETE FROM hub_collections`
)

func nullable(v string) any {
	if v == "" {
		return nil
	}
	return v
}

func scanHub(sc interface{ Scan(...any) error }) (Hub, error) {
	var h Hub
	err := sc.Scan(&h.Name, &h.EntryID, &h.NodeName, &h.Transport, &h.Address, &h.Token, &h.SSHKey, &h.CA, &h.HubID)
	return h, err
}

func readHubs(ctx context.Context, db sqlitedb.Querier) ([]Hub, error) {
	rows, err := db.QueryContext(ctx, qHubsAll)
	if err != nil {
		return nil, fmt.Errorf("Hubs lesen: %w", err)
	}
	defer rows.Close()
	out := []Hub{}
	for rows.Next() {
		h, err := scanHub(rows)
		if err != nil {
			return nil, fmt.Errorf("Hubs lesen: %w", err)
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

func readWanted(ctx context.Context, db sqlitedb.Querier) ([]Wanted, error) {
	rows, err := db.QueryContext(ctx, qWantedAll)
	if err != nil {
		return nil, fmt.Errorf("Collections lesen: %w", err)
	}
	defer rows.Close()
	out := []Wanted{}
	for rows.Next() {
		var w Wanted
		if err := rows.Scan(&w.Hub, &w.Collection); err != nil {
			return nil, fmt.Errorf("Collections lesen: %w", err)
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

func getHub(ctx context.Context, db sqlitedb.Querier, name string) (Hub, error) {
	h, err := scanHub(db.QueryRowContext(ctx, qHubGet, name))
	if errors.Is(err, sql.ErrNoRows) {
		return Hub{}, fmt.Errorf("Hub %s %w", name, ErrNotFound)
	}
	return h, err
}

// inTx führt fn in einer Transaktion aus.
func (s *sqliteStore) inTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *sqliteStore) Hubs(ctx context.Context) ([]Hub, error) {
	hubs, err := readHubs(ctx, s.db)
	if err != nil {
		return nil, err
	}
	wanted, err := readWanted(ctx, s.db)
	if err != nil {
		return nil, err
	}
	idx := map[string]int{}
	for i, h := range hubs {
		idx[h.Name] = i
		hubs[i].Collections = []string{}
	}
	for _, w := range wanted {
		if i, ok := idx[w.Hub]; ok {
			hubs[i].Collections = append(hubs[i].Collections, w.Collection)
		}
	}
	return hubs, nil
}

func (s *sqliteStore) Hub(ctx context.Context, name string) (Hub, error) {
	h, err := getHub(ctx, s.db, name)
	if err != nil {
		return Hub{}, err
	}
	rows, err := s.db.QueryContext(ctx, qWantedOfHub, name)
	if err != nil {
		return Hub{}, err
	}
	defer rows.Close()
	h.Collections = []string{}
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return Hub{}, err
		}
		h.Collections = append(h.Collections, c)
	}
	return h, rows.Err()
}

// checkWithOthers prüft einen Eintrag gegen die übrigen in der Datenbank:
// höchstens ein local. Der Eintrag selbst (gleicher Name) zählt nicht mit.
func checkWithOthers(ctx context.Context, tx sqlitedb.Querier, h Hub, hubInConfig bool) error {
	all, err := readHubs(ctx, tx)
	if err != nil {
		return err
	}
	others := []Hub{}
	for _, o := range all {
		if o.Name != h.Name {
			others = append(others, o)
		}
	}
	for _, o := range others {
		if o.Transport == TransportLocal && h.Transport == TransportLocal {
			return fmt.Errorf("Hub %s: es gibt schon einen Eintrag mit Transport local (%s); höchstens einer je Node", h.Name, o.Name)
		}
	}
	return CheckHub(h, hubInConfig)
}

func (s *sqliteStore) AddHub(ctx context.Context, h Hub, hubInConfig bool) error {
	h.HubID = ""
	h.EntryID = ulid.Make().String()
	if err := CheckHub(h, hubInConfig); err != nil {
		return err
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := getHub(ctx, tx, h.Name); err == nil {
			return fmt.Errorf("Hub %s %w", h.Name, ErrExists)
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
		if err := checkWithOthers(ctx, tx, h, hubInConfig); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, qHubInsert, h.Name, h.EntryID, h.NodeName, h.Transport, nullable(h.Address), h.Token,
			nullable(h.SSHKey), nullable(h.CA), nil)
		return err
	})
}

// ApplyUpdate liefert den Eintrag nach einer Änderung: Ein Wechsel des
// Transports verwirft, was zum neuen nicht passt (die Adresse bei local, den
// Schlüssel außer bei ssh, die CA außer bei https); danach gelten die
// angegebenen Felder. hub_id und Token bleiben.
func ApplyUpdate(h Hub, u HubUpdate) Hub {
	if u.Transport != nil && *u.Transport != h.Transport {
		h.Transport = *u.Transport
		if h.Transport == TransportLocal {
			h.Address = ""
		}
		if h.Transport != TransportSSH {
			h.SSHKey = ""
		}
		if h.Transport != TransportHTTPS {
			h.CA = ""
		}
	}
	if u.Address != nil {
		h.Address = *u.Address
	}
	if u.SSHKey != nil {
		h.SSHKey = *u.SSHKey
	}
	if u.CA != nil {
		h.CA = *u.CA
	}
	if u.NodeName != nil {
		h.NodeName = *u.NodeName
	}
	return h
}

func (s *sqliteStore) SetHub(ctx context.Context, name string, u HubUpdate, hubInConfig bool) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		h, err := getHub(ctx, tx, name)
		if err != nil {
			return err
		}
		h = ApplyUpdate(h, u)
		if err := checkWithOthers(ctx, tx, h, hubInConfig); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, qHubUpdate, h.NodeName, h.Transport, nullable(h.Address), nullable(h.SSHKey),
			nullable(h.CA), name)
		return err
	})
}

func (s *sqliteStore) SetHubToken(ctx context.Context, name, token string) error {
	if err := ident.CheckToken(token); err != nil {
		return fmt.Errorf("Hub %s: %w", name, err)
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, qHubToken, token, name)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			return fmt.Errorf("Hub %s %w", name, ErrNotFound)
		}
		return nil
	})
}

func (s *sqliteStore) SetHubID(ctx context.Context, name, entryID, hubID string) error {
	if _, err := ulid.ParseStrict(hubID); err != nil {
		return fmt.Errorf("Hub %s: hub_id %q ist keine ULID", name, hubID)
	}
	res, err := s.db.ExecContext(ctx, qHubID, hubID, name, entryID)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return fmt.Errorf("Hub-Eintrag %s %w", name, ErrEntryGone)
	}
	return nil
}

// RemoveHub entfernt die Replica innerhalb der Transaktion, vor dem Eintrag:
// Scheitert danach das Commit, fehlt nur die Replica, und die ist abgeleitet
// — der nächste Abgleich legt sie neu an. Andersherum bliebe eine Replica
// ohne Eintrag liegen, die ein späteres add unter demselben Alias erbte.
func (s *sqliteStore) RemoveHub(ctx context.Context, name string) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := getHub(ctx, tx, name); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, qWantedOfDel, name); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, qSyncOfDel, name); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, qHubDelete, name); err != nil {
			return err
		}
		if err := sqlitedb.Remove(s.ReplicaPath(name)); err != nil {
			return fmt.Errorf("Hub %s: Replica entfernen: %w", name, err)
		}
		return nil
	})
}

func (s *sqliteStore) Collections(ctx context.Context) ([]Wanted, error) {
	return readWanted(ctx, s.db)
}

func (s *sqliteStore) AddCollection(ctx context.Context, hub, collection string) error {
	addr := ident.Address(hub, collection)
	if _, _, err := ident.ParseAddress(addr); err != nil {
		return err
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := getHub(ctx, tx, hub); err != nil {
			return err
		}
		var n int
		if err := tx.QueryRowContext(ctx, qWantedGet, hub, collection).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return fmt.Errorf("Collection %s %w", addr, ErrExists)
		}
		_, err := tx.ExecContext(ctx, qWantedInsert, hub, collection)
		return err
	})
}

func (s *sqliteStore) RemoveCollection(ctx context.Context, hub, collection string) error {
	addr := ident.Address(hub, collection)
	return s.inTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, qWantedDelete, hub, collection)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			return fmt.Errorf("Collection %s %w", addr, ErrNotFound)
		}
		return nil
	})
}

func (s *sqliteStore) Tables(ctx context.Context) (Tables, error) {
	hubs, err := readHubs(ctx, s.db)
	if err != nil {
		return Tables{}, err
	}
	wanted, err := readWanted(ctx, s.db)
	if err != nil {
		return Tables{}, err
	}
	return Tables{Hubs: hubs, Wanted: wanted}, nil
}

func (s *sqliteStore) Import(ctx context.Context, settings map[string]string, tables *Tables, hubInConfig bool) error {
	if tables != nil {
		if err := CheckTables(*tables, hubInConfig); err != nil {
			return err
		}
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if err := sqlitedb.ReplaceSettingsTx(ctx, tx, settings); err != nil {
			return err
		}
		if tables == nil {
			return nil
		}
		before, err := readHubs(ctx, tx)
		if err != nil {
			return err
		}
		entryIDs := make(map[string]string, len(before))
		for _, h := range before {
			entryIDs[h.Name] = h.EntryID
		}
		for _, del := range []string{qWantedClear, qSyncClear, qHubsClear} {
			if _, err := tx.ExecContext(ctx, del); err != nil {
				return err
			}
		}
		for _, h := range tables.Hubs {
			// Ein Alias, der bleibt, behält seine Kennung und damit seine
			// Replica; ein neuer bekommt eine neue.
			id, ok := entryIDs[h.Name]
			if !ok {
				id = ulid.Make().String()
			}
			if _, err := tx.ExecContext(ctx, qHubInsert, h.Name, id, h.NodeName, h.Transport, nullable(h.Address), h.Token,
				nullable(h.SSHKey), nullable(h.CA), nullable(h.HubID)); err != nil {
				return fmt.Errorf("Hub %s: %w", h.Name, err)
			}
		}
		for _, w := range tables.Wanted {
			if _, err := tx.ExecContext(ctx, qWantedInsert, w.Hub, w.Collection); err != nil {
				return fmt.Errorf("Collection %s: %w", ident.Address(w.Hub, w.Collection), err)
			}
		}
		return removeStaleReplicas(s, before, tables.Hubs)
	})
}

// removeStaleReplicas entfernt die Replicas der Einträge, die ein Import
// nicht mehr enthält — wie RemoveHub innerhalb der Transaktion: Scheitert
// danach das Commit, fehlt nur Abgeleitetes. Bleibt ein Alias, bleibt seine
// Replica; zeigt er auf einen anderen Hub, merkt das der Abgleich an der
// hub_id.
func removeStaleReplicas(s *sqliteStore, before, after []Hub) error {
	keep := make(map[string]bool, len(after))
	for _, h := range after {
		keep[h.Name] = true
	}
	for _, h := range before {
		if keep[h.Name] {
			continue
		}
		if err := sqlitedb.Remove(s.ReplicaPath(h.Name)); err != nil {
			return fmt.Errorf("Hub %s: Replica entfernen: %w", h.Name, err)
		}
	}
	return nil
}
