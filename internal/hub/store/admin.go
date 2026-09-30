package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/kephalaion/kephalaion/internal/contract"
	"github.com/kephalaion/kephalaion/internal/ident"
	"github.com/kephalaion/kephalaion/internal/sqlitedb"
)

// Admin ist der Account, als der die CLI am Hub handelt: in created_by und
// in actions.
const Admin = ident.AdminName

// Fehlerarten; die Meldungen nennen dazu, was betroffen ist.
var (
	ErrNotFound = errors.New("gibt es nicht")
	ErrExists   = errors.New("gibt es schon")
	ErrInUse    = errors.New("wird noch gebraucht")
)

// kindError trägt eine eigene Meldung und eine Fehlerart für errors.Is.
type kindError struct {
	kind error
	msg  string
}

func (e *kindError) Error() string { return e.msg }
func (e *kindError) Unwrap() error { return e.kind }

// Collection ist eine Zeile in collections.
type Collection struct {
	Name        string
	Description string
	CreatedAt   int64
	CreatedBy   string
}

// Node ist eine Zeile in nodes. Collections sind die erlaubten Collections
// aus node_collections; Tables führt sie getrennt.
type Node struct {
	Name        string
	Description string
	TokenHash   string
	Locked      bool
	CreatedAt   int64
	CreatedBy   string
	Collections []string
}

// Grant ist eine Zeile in node_collections: der Node darf die Collection
// abgleichen.
type Grant struct {
	Node       string
	Collection string
}

// Tables sind die lokalen Tabellen des Hubs, wie export und import sie
// behandeln. Node.Collections bleibt dabei leer; die Rechte stehen in Grants.
// Accounts tragen ihre Rechte je Collection mit — beim Import entstehen aus
// ihnen die SYSTEM:A:-Zeilen. KeepAccounts heißt: Der Import lässt die
// Accounts, wie sie sind (ein Export ohne Accounts-Teil, Format vor 4).
type Tables struct {
	Collections  []Collection
	Nodes        []Node
	Grants       []Grant
	Accounts     []Account
	KeepAccounts bool
}

// CheckTables prüft lokale Tabellen ohne Datenbank, wie die CLI es beim
// Anlegen tut: Namensregel, Eindeutigkeit (Nodes und Accounts gemeinsam),
// User je Account (CheckUser), Form des Hashes, Rechte nur auf vorhandene
// Nodes und Collections, die Scopes vendor/<name> nach der Namensregel, die
// Verzeichnis-Scopes nach contract.CheckDirScope. Was die Datenbank braucht
// (Dokumente in wegfallenden Collections), prüft Import in der Transaktion.
func CheckTables(t Tables) error {
	colls := map[string]bool{}
	for _, c := range t.Collections {
		if err := ident.CheckName("Collection", c.Name); err != nil {
			return err
		}
		if colls[c.Name] {
			return fmt.Errorf("Collection %s: %w", c.Name, ErrExists)
		}
		if c.CreatedBy == "" {
			return fmt.Errorf("Collection %s: created_by fehlt", c.Name)
		}
		colls[c.Name] = true
	}
	nodes := map[string]bool{}
	for _, n := range t.Nodes {
		if err := ident.CheckPrincipalName("Node", n.Name); err != nil {
			return err
		}
		if nodes[n.Name] {
			return fmt.Errorf("Node %s: %w", n.Name, ErrExists)
		}
		if !contract.IsTokenHash(n.TokenHash) {
			return fmt.Errorf("Node %s: token_hash ist kein sha256 in Hex", n.Name)
		}
		if n.CreatedBy == "" {
			return fmt.Errorf("Node %s: created_by fehlt", n.Name)
		}
		nodes[n.Name] = true
	}
	grants := map[Grant]bool{}
	for _, g := range t.Grants {
		if !nodes[g.Node] {
			return fmt.Errorf("Recht %s: Node %s %w", ident.Address(g.Node, g.Collection), g.Node, ErrNotFound)
		}
		if !colls[g.Collection] {
			return fmt.Errorf("Recht %s: Collection %s %w", ident.Address(g.Node, g.Collection), g.Collection, ErrNotFound)
		}
		if grants[g] {
			return fmt.Errorf("Recht %s: %w", ident.Address(g.Node, g.Collection), ErrExists)
		}
		grants[g] = true
	}
	if t.KeepAccounts {
		return nil
	}
	accounts := map[string]bool{}
	for _, a := range t.Accounts {
		if err := ident.CheckPrincipalName("Account", a.Name); err != nil {
			return err
		}
		if accounts[a.Name] {
			return fmt.Errorf("Account %s: %w", a.Name, ErrExists)
		}
		if nodes[a.Name] {
			return fmt.Errorf("Name %s steht als Node und als Account im Export; Node- und Account-Namen sind gemeinsam eindeutig", a.Name)
		}
		if !contract.IsTokenHash(a.TokenHash) {
			return fmt.Errorf("Account %s: token_hash ist kein sha256 in Hex", a.Name)
		}
		if a.CreatedBy == "" {
			return fmt.Errorf("Account %s: created_by fehlt", a.Name)
		}
		if err := CheckUser(a.User); err != nil {
			return fmt.Errorf("Account %s: %w", a.Name, err)
		}
		accounts[a.Name] = true
		seen := map[string]bool{}
		for _, r := range a.Rights {
			if !colls[r.Collection] {
				return fmt.Errorf("Account %s: Collection %s %w", a.Name, r.Collection, ErrNotFound)
			}
			if seen[r.Collection] {
				return fmt.Errorf("Account %s: Rechte in %s stehen zweimal", a.Name, r.Collection)
			}
			seen[r.Collection] = true
			if _, err := contract.NormalizeRights(r.Rights); err != nil {
				return fmt.Errorf("Account %s in %s: %w", a.Name, r.Collection, err)
			}
		}
	}
	return nil
}

// write führt fn in einer Transaktion aus und schreibt danach die Zeile in
// actions.
func (s *sqliteStore) write(ctx context.Context, action, subject string, fn func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(tx); err != nil {
		return err
	}
	if err := logAction(ctx, tx, action, subject); err != nil {
		return err
	}
	return tx.Commit()
}

func logAction(ctx context.Context, tx sqlitedb.Querier, action, subject string) error {
	var subj any
	if subject != "" {
		subj = subject
	}
	if _, err := tx.ExecContext(ctx, q(queries.ActionInsert), sqlitedb.NowMillis(), Admin, action, subj); err != nil {
		return fmt.Errorf("actions schreiben: %w", err)
	}
	return nil
}

// nullable liefert NULL für einen leeren Text.
func nullable(v string) any {
	if v == "" {
		return nil
	}
	return v
}

func count(ctx context.Context, db sqlitedb.Querier, query string, args ...any) (int64, error) {
	var n int64
	err := db.QueryRowContext(ctx, q(query), args...).Scan(&n)
	return n, err
}

// mustAffect meldet ErrNotFound, wenn eine Änderung keine Zeile traf.
func mustAffect(res sql.Result, err error, what string) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("%s %w", what, ErrNotFound)
	}
	return nil
}

// Collections

func (s *sqliteStore) Collections(ctx context.Context) ([]Collection, error) {
	return readCollections(ctx, s.db)
}

func readCollections(ctx context.Context, db sqlitedb.Querier) ([]Collection, error) {
	rows, err := db.QueryContext(ctx, q(queries.CollectionsAll))
	if err != nil {
		return nil, fmt.Errorf("Collections lesen: %w", err)
	}
	defer rows.Close()
	out := []Collection{}
	for rows.Next() {
		var c Collection
		if err := rows.Scan(&c.Name, &c.Description, &c.CreatedAt, &c.CreatedBy); err != nil {
			return nil, fmt.Errorf("Collections lesen: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func collectionExists(ctx context.Context, db sqlitedb.Querier, name string) (bool, error) {
	var c Collection
	err := db.QueryRowContext(ctx, q(queries.CollectionGet), name).
		Scan(&c.Name, &c.Description, &c.CreatedAt, &c.CreatedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (s *sqliteStore) AddCollection(ctx context.Context, name, description string) error {
	if err := ident.CheckName("Collection", name); err != nil {
		return err
	}
	return s.write(ctx, "collection.add", name, func(tx *sql.Tx) error {
		ok, err := collectionExists(ctx, tx, name)
		if err != nil {
			return err
		}
		if ok {
			return fmt.Errorf("Collection %s %w", name, ErrExists)
		}
		_, err = tx.ExecContext(ctx, q(queries.CollectionInsert), name, nullable(description), sqlitedb.NowMillis(), Admin)
		return err
	})
}

func (s *sqliteStore) SetCollectionDescription(ctx context.Context, name, description string) error {
	return s.write(ctx, "collection.set", name, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, q(queries.CollectionSetDesc), name, nullable(description))
		return mustAffect(res, err, "Collection "+name)
	})
}

func (s *sqliteStore) RemoveCollection(ctx context.Context, name string) error {
	return s.write(ctx, "collection.rm", name, func(tx *sql.Tx) error {
		ok, err := collectionExists(ctx, tx, name)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("Collection %s %w", name, ErrNotFound)
		}
		if err := collectionRemovable(ctx, tx, name, true, true); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, q(queries.CollectionDelete), name)
		return err
	})
}

// collectionRemovable prüft, ob eine Collection entfernt werden darf: keine
// Dokumente in ihr (jede Zeile zählt, auch Löschmarken und lebende
// SYSTEM:-Zeilen; nur Löschmarken von Account-Zeilen nicht — sie bleiben),
// wenn checkGrants, kein Node, der sie abgleichen darf, und wenn
// checkAccounts, kein gesperrter Account, der sich Rechte in ihr gemerkt hat.
func collectionRemovable(ctx context.Context, tx sqlitedb.Querier, name string, checkGrants, checkAccounts bool) error {
	if checkAccounts {
		accounts, err := lockedAccountsUsing(ctx, tx, name)
		if err != nil {
			return err
		}
		if len(accounts) > 0 {
			return fmt.Errorf("Collection %s %w: der gesperrte Account %s hat sich Rechte in ihr gemerkt "+
				"(zuerst hub account revoke)", name, ErrInUse, strings.Join(accounts, ", "))
		}
	}
	if checkGrants {
		nodes, err := strings1(ctx, tx, queries.CollectionGrantedNode, name)
		if err != nil {
			return err
		}
		if len(nodes) > 0 {
			return fmt.Errorf("Collection %s %w: erlaubt für Node %s (zuerst hub node revoke)",
				name, ErrInUse, strings.Join(nodes, ", "))
		}
	}
	n, err := count(ctx, tx, queries.CollectionCountDocs, name)
	if err != nil {
		return err
	}
	if n > 0 {
		return fmt.Errorf("Collection %s %w: sie enthält %d Dokumentzeilen (samt Löschmarken und lebenden SYSTEM:-Zeilen)",
			name, ErrInUse, n)
	}
	return nil
}

// strings1 liest eine Spalte Text.
func strings1(ctx context.Context, db sqlitedb.Querier, query string, args ...any) ([]string, error) {
	rows, err := db.QueryContext(ctx, q(query), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// Nodes

func scanNode(sc interface{ Scan(...any) error }) (Node, error) {
	var n Node
	var locked int64
	if err := sc.Scan(&n.Name, &n.Description, &n.TokenHash, &locked, &n.CreatedAt, &n.CreatedBy); err != nil {
		return Node{}, err
	}
	n.Locked = locked != 0
	return n, nil
}

func (s *sqliteStore) Nodes(ctx context.Context) ([]Node, error) {
	nodes, err := readNodes(ctx, s.db)
	if err != nil {
		return nil, err
	}
	grants, err := readGrants(ctx, s.db)
	if err != nil {
		return nil, err
	}
	idx := map[string]int{}
	for i, n := range nodes {
		idx[n.Name] = i
		nodes[i].Collections = []string{}
	}
	for _, g := range grants {
		if i, ok := idx[g.Node]; ok {
			nodes[i].Collections = append(nodes[i].Collections, g.Collection)
		}
	}
	return nodes, nil
}

func readNodes(ctx context.Context, db sqlitedb.Querier) ([]Node, error) {
	rows, err := db.QueryContext(ctx, q(queries.NodesAll))
	if err != nil {
		return nil, fmt.Errorf("Nodes lesen: %w", err)
	}
	defer rows.Close()
	out := []Node{}
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, fmt.Errorf("Nodes lesen: %w", err)
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func readGrants(ctx context.Context, db sqlitedb.Querier) ([]Grant, error) {
	rows, err := db.QueryContext(ctx, q(queries.GrantsAll))
	if err != nil {
		return nil, fmt.Errorf("Rechte lesen: %w", err)
	}
	defer rows.Close()
	out := []Grant{}
	for rows.Next() {
		var g Grant
		if err := rows.Scan(&g.Node, &g.Collection); err != nil {
			return nil, fmt.Errorf("Rechte lesen: %w", err)
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func getNode(ctx context.Context, db sqlitedb.Querier, name string) (Node, error) {
	n, err := scanNode(db.QueryRowContext(ctx, q(queries.NodeGet), name))
	if errors.Is(err, sql.ErrNoRows) {
		return Node{}, fmt.Errorf("Node %s %w", name, ErrNotFound)
	}
	return n, err
}

func (s *sqliteStore) Node(ctx context.Context, name string) (Node, error) {
	n, err := getNode(ctx, s.db, name)
	if err != nil {
		return Node{}, err
	}
	n.Collections, err = strings1(ctx, s.db, queries.GrantsOfNode, name)
	if err != nil {
		return Node{}, err
	}
	return n, nil
}

func (s *sqliteStore) AddNode(ctx context.Context, name, description string) (string, error) {
	if err := ident.CheckPrincipalName("Node", name); err != nil {
		return "", err
	}
	token, err := ident.NewToken()
	if err != nil {
		return "", err
	}
	err = s.write(ctx, "node.add", name, func(tx *sql.Tx) error {
		if _, err := getNode(ctx, tx, name); err == nil {
			return nameTaken(name, kindNode, kindNode)
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
		if err := checkNodeNameFree(ctx, tx, name); err != nil {
			return err
		}
		if err := claimName(ctx, tx, name, kindNode); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, q(queries.NodeInsert),
			name, nullable(description), ident.HashToken(token), 0, sqlitedb.NowMillis(), Admin)
		return err
	})
	if err != nil {
		return "", err
	}
	return token, nil
}

func (s *sqliteStore) SetNodeDescription(ctx context.Context, name, description string) error {
	return s.write(ctx, "node.set", name, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, q(queries.NodeSetDesc), name, nullable(description))
		return mustAffect(res, err, "Node "+name)
	})
}

func (s *sqliteStore) SetNodeLocked(ctx context.Context, name string, locked bool) error {
	action, v := "node.unlock", 0
	if locked {
		action, v = "node.lock", 1
	}
	return s.write(ctx, action, name, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, q(queries.NodeSetLocked), name, v)
		return mustAffect(res, err, "Node "+name)
	})
}

func (s *sqliteStore) NewNodeToken(ctx context.Context, name string) (string, error) {
	token, err := ident.NewToken()
	if err != nil {
		return "", err
	}
	err = s.write(ctx, "node.token", name, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, q(queries.NodeSetToken), name, ident.HashToken(token))
		return mustAffect(res, err, "Node "+name)
	})
	if err != nil {
		return "", err
	}
	return token, nil
}

func (s *sqliteStore) RemoveNode(ctx context.Context, name string) error {
	return s.write(ctx, "node.rm", name, func(tx *sql.Tx) error {
		if _, err := getNode(ctx, tx, name); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, q(queries.GrantsDeleteOf), name); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, q(queries.NodeDelete), name); err != nil {
			return err
		}
		return releaseName(ctx, tx, name)
	})
}

func (s *sqliteStore) Grant(ctx context.Context, node, collection string) error {
	return s.write(ctx, "node.grant", ident.Address(node, collection), func(tx *sql.Tx) error {
		if _, err := getNode(ctx, tx, node); err != nil {
			return err
		}
		ok, err := collectionExists(ctx, tx, collection)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("Collection %s %w", collection, ErrNotFound)
		}
		n, err := count(ctx, tx, queries.GrantGet, node, collection)
		if err != nil {
			return err
		}
		if n > 0 {
			return &kindError{ErrExists, fmt.Sprintf("Node %s darf %s schon abgleichen", node, collection)}
		}
		_, err = tx.ExecContext(ctx, q(queries.GrantInsert), node, collection)
		return err
	})
}

func (s *sqliteStore) Revoke(ctx context.Context, node, collection string) error {
	return s.write(ctx, "node.revoke", ident.Address(node, collection), func(tx *sql.Tx) error {
		if _, err := getNode(ctx, tx, node); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, q(queries.GrantDelete), node, collection)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return &kindError{ErrNotFound, fmt.Sprintf("Node %s darf %s nicht abgleichen", node, collection)}
		}
		return nil
	})
}

// Export und Import

func (s *sqliteStore) Tables(ctx context.Context) (Tables, error) {
	var t Tables
	var err error
	if t.Collections, err = readCollections(ctx, s.db); err != nil {
		return Tables{}, err
	}
	if t.Nodes, err = readNodes(ctx, s.db); err != nil {
		return Tables{}, err
	}
	if t.Grants, err = readGrants(ctx, s.db); err != nil {
		return Tables{}, err
	}
	if t.Accounts, err = readAccounts(ctx, s.db); err != nil {
		return Tables{}, err
	}
	return t, nil
}

func (s *sqliteStore) Import(ctx context.Context, settings map[string]string, tables *Tables) error {
	if tables != nil {
		if err := CheckTables(*tables); err != nil {
			return err
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	// Erste Anweisung: alle Zeilen in accounts sperren, bevor irgendetwas
	// gelesen wird — wie writeAccount für einen Account.
	if _, err := tx.ExecContext(ctx, q(queries.AccountsLockAll)); err != nil {
		return fmt.Errorf("Accounts sperren: %w", err)
	}
	w := &accountTx{docTx: newDocTx(tx, Admin, Admin, "")}
	if tables != nil {
		if err := checkImport(ctx, tx, *tables); err != nil {
			return err
		}
	}
	if err := sqlitedb.ReplaceSettingsTx(ctx, tx, settings); err != nil {
		return err
	}
	if tables != nil {
		if err := replaceTables(ctx, tx, *tables); err != nil {
			return err
		}
		if !tables.KeepAccounts {
			if err := w.replaceAccounts(ctx, tables.Accounts); err != nil {
				return err
			}
		}
		// principal_names folgt aus nodes und accounts.
		if err := rebuildNames(ctx, tx); err != nil {
			return err
		}
	}
	if err := logActionFull(ctx, tx, w.now, Admin, "", "config.import", "", w.rev.rev); err != nil {
		return err
	}
	return tx.Commit()
}

// checkImport prüft vor dem Schreiben, was die Datenbank braucht: Keine
// Collection mit Dokumenten fiele weg, kein Node trägt den Namen eines
// Accounts. Bleiben die Accounts, zählen die vorhandenen; sonst hat
// CheckTables die Namen schon gegen die Accounts des Exports geprüft.
func checkImport(ctx context.Context, tx sqlitedb.Querier, t Tables) error {
	keep := map[string]bool{}
	for _, c := range t.Collections {
		keep[c.Name] = true
	}
	current, err := readCollections(ctx, tx)
	if err != nil {
		return err
	}
	for _, c := range current {
		if keep[c.Name] {
			continue
		}
		if err := collectionRemovable(ctx, tx, c.Name, false, t.KeepAccounts); err != nil {
			return fmt.Errorf("%w — der Import würde sie entfernen", err)
		}
	}
	if !t.KeepAccounts {
		return nil
	}
	for _, n := range t.Nodes {
		if err := checkNodeNameFree(ctx, tx, n.Name); err != nil {
			return err
		}
	}
	return nil
}

func replaceTables(ctx context.Context, tx sqlitedb.Querier, t Tables) error {
	for _, del := range []string{queries.GrantsDeleteAll, queries.NodesDeleteAll, queries.CollectionsDeleteAll} {
		if _, err := tx.ExecContext(ctx, q(del)); err != nil {
			return err
		}
	}
	for _, c := range t.Collections {
		if _, err := tx.ExecContext(ctx, q(queries.CollectionInsert),
			c.Name, nullable(c.Description), c.CreatedAt, c.CreatedBy); err != nil {
			return fmt.Errorf("Collection %s: %w", c.Name, err)
		}
	}
	for _, n := range t.Nodes {
		locked := 0
		if n.Locked {
			locked = 1
		}
		if _, err := tx.ExecContext(ctx, q(queries.NodeInsert),
			n.Name, nullable(n.Description), n.TokenHash, locked, n.CreatedAt, n.CreatedBy); err != nil {
			return fmt.Errorf("Node %s: %w", n.Name, err)
		}
	}
	for _, g := range t.Grants {
		if _, err := tx.ExecContext(ctx, q(queries.GrantInsert), g.Node, g.Collection); err != nil {
			return fmt.Errorf("Recht %s: %w", ident.Address(g.Node, g.Collection), err)
		}
	}
	return nil
}
