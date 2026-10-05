package store

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"

	"github.com/oklog/ulid/v2"

	"github.com/kephalaion/kephalaion/internal/contract"
	"github.com/kephalaion/kephalaion/internal/ident"
	"github.com/kephalaion/kephalaion/internal/sqlitedb"
)

// Accounts am Hub: Was sich abgleichen muss, steht in documents — je Account
// und Collection eine Zeile SYSTEM:A:<name> mit dem Hash des Tokens, dem User
// und den Rechten (contract.AccountContent). Was nur der Hub braucht, steht in
// der Tabelle accounts: Beschreibung, gesperrt, die gemerkten Rechte eines
// gesperrten Accounts, angelegt. Hash und User führt accounts maßgeblich und
// immer, auch gesperrt und ohne Collection; die Zeilen tragen eine Kopie.
//
// Jede Änderung an den Zeilen ist ein Schreibvorgang mit Revision; eine Zeile
// wird nie entfernt, sondern Löschmarke, und eine Löschmarke wird
// wiederbelebt, wenn der Account die Collection wieder bekommt.

// Fehlerarten der Accounts, für errors.Is.
var (
	// ErrAccountAuth: Account unbekannt, Token falsch oder Account gesperrt.
	ErrAccountAuth = errors.New("Account nicht angemeldet")
	// ErrNoSharedCollection: der Account hat keine der Collections des
	// Nodes.
	ErrNoSharedCollection = errors.New("keine gemeinsame Collection")
)

// errUnchanged bricht einen Schreibvorgang ab, der nichts ändern würde.
var errUnchanged = errors.New("unverändert")

// AccountRight ist das Recht eines Accounts in einer Collection.
type AccountRight struct {
	Collection string
	contract.Rights
}

// Account ist ein Account mit seinen Rechten.
type Account struct {
	Name string
	// User ist, wem der Account gehört; ohne Angabe beim Anlegen sein Name.
	User        string
	Description string
	TokenHash   string
	Locked      bool
	CreatedAt   int64
	CreatedBy   string
	// Rights sind die Rechte je Collection, nach Collection sortiert: ohne
	// Sperre aus den SYSTEM:A:-Zeilen, gesperrt die gemerkten.
	Rights []AccountRight
}

// accountRow ist eine Zeile der Tabelle accounts.
type accountRow struct {
	Account
	// lockedRights sind die gemerkten Rechte als JSON, leer ohne Sperre.
	lockedRights string
}

func scanAccount(sc interface{ Scan(...any) error }) (accountRow, error) {
	var a accountRow
	var locked int64
	if err := sc.Scan(&a.Name, &a.User, &a.Description, &a.TokenHash, &locked, &a.lockedRights, &a.CreatedAt, &a.CreatedBy); err != nil {
		return accountRow{}, err
	}
	a.Locked = locked != 0
	return a, nil
}

func accountNotFound(name string) error {
	return &kindError{ErrNotFound, fmt.Sprintf("Account %s gibt es nicht", name)}
}

func getAccount(ctx context.Context, db sqlitedb.Querier, name string) (accountRow, error) {
	a, err := scanAccount(db.QueryRowContext(ctx, q(queries.AccountGet), name))
	if errors.Is(err, sql.ErrNoRows) {
		return accountRow{}, accountNotFound(name)
	}
	if err != nil {
		return accountRow{}, fmt.Errorf("Account %s lesen: %w", name, err)
	}
	return a, nil
}

func readAccountRows(ctx context.Context, db sqlitedb.Querier) ([]accountRow, error) {
	return queryAccountRows(ctx, db, queries.AccountsAll)
}

func queryAccountRows(ctx context.Context, db sqlitedb.Querier, query string, args ...any) ([]accountRow, error) {
	rows, err := db.QueryContext(ctx, q(query), args...)
	if err != nil {
		return nil, fmt.Errorf("Accounts lesen: %w", err)
	}
	defer rows.Close()
	out := []accountRow{}
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, fmt.Errorf("Accounts lesen: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// rightsMap sind Rechte je Collection, so wie accounts sie als gemerkte
// Rechte speichert.
type rightsMap map[string]contract.Rights

func decodeRights(s string) (rightsMap, error) {
	out := rightsMap{}
	if s == "" {
		return out, nil
	}
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil, fmt.Errorf("accounts: gemerkte Rechte nicht lesbar: %w", err)
	}
	return out, nil
}

func encodeRights(m rightsMap) (string, error) {
	b, err := json.Marshal(m)
	return string(b), err
}

func (m rightsMap) list() []AccountRight {
	out := make([]AccountRight, 0, len(m))
	for c, r := range m {
		out = append(out, AccountRight{Collection: c, Rights: r})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Collection < out[j].Collection })
	return out
}

// liveAccountRows liest die lebenden Zeilen eines Accounts, nach Collection.
func liveAccountRows(ctx context.Context, db sqlitedb.Querier, name string) ([]Document, error) {
	return queryDocuments(ctx, db, queries.AccountRowsLive, contract.AccountRowName(name))
}

func queryDocuments(ctx context.Context, db sqlitedb.Querier, query string, args ...any) ([]Document, error) {
	rows, err := db.QueryContext(ctx, q(query), args...)
	if err != nil {
		return nil, fmt.Errorf("Zeilen lesen: %w", err)
	}
	defer rows.Close()
	out := []Document{}
	for rows.Next() {
		d, err := scanDocument(rows)
		if err != nil {
			return nil, fmt.Errorf("Zeilen lesen: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// rowRights liest die Rechte aus den lebenden Zeilen eines Accounts.
func rowRights(rows []Document) (rightsMap, error) {
	out := rightsMap{}
	for _, d := range rows {
		c, err := contract.DecodeAccountContent(d.Content)
		if err != nil {
			return nil, fmt.Errorf("%s in %s: %w", d.Name, d.Collection, err)
		}
		out[d.Collection] = c.Rights
	}
	return out, nil
}

// withRights ergänzt einen Account um seine Rechte.
func withRights(ctx context.Context, db sqlitedb.Querier, a accountRow) (Account, error) {
	var m rightsMap
	var err error
	if a.Locked {
		m, err = decodeRights(a.lockedRights)
	} else {
		var rows []Document
		if rows, err = liveAccountRows(ctx, db, a.Name); err == nil {
			m, err = rowRights(rows)
		}
	}
	if err != nil {
		return Account{}, err
	}
	out := a.Account
	out.Rights = m.list()
	return out, nil
}

func (s *sqliteStore) Account(ctx context.Context, name string) (Account, error) {
	a, err := getAccount(ctx, s.db, name)
	if err != nil {
		return Account{}, err
	}
	return withRights(ctx, s.db, a)
}

// dummyAccountHash wird verglichen, wenn es den Account nicht gibt: So
// kostet ein unbekannter Name denselben Vergleich wie ein falsches Token.
var dummyAccountHash = ident.HashToken("keph_unbekannter-account")

// CheckAccount ist die Anmeldung eines Accounts am Hub außerhalb einer
// Transaktion: Name, Token und Sperre gegen accounts — dort steht der
// maßgebliche Hash. Unbekannt, falsches Token und gesperrt ergeben ok false
// und keinen Account; der Hash wird in jedem Fall in konstanter Zeit
// verglichen, für einen unbekannten Namen gegen dummyAccountHash. Bei ok
// trägt der Account seine Rechte aus den lebenden SYSTEM:A:-Zeilen. Der
// Vertrag (whoami, rotate) benutzt sie; die Schreibvorgänge über einen Node
// prüfen in ihrer Transaktion (accountUser in write.go). Die Weboberfläche
// prüft kein Token: Sie zeigt die Accounts des Users, den die Anmeldung des
// Proxys nennt (AccountsOfUser).
func CheckAccount(ctx context.Context, st Store, name, token string) (acc Account, ok bool, err error) {
	acc, err = st.Account(ctx, name)
	known := err == nil
	if err != nil && !errors.Is(err, ErrNotFound) {
		return Account{}, false, err
	}
	want := dummyAccountHash
	if known {
		want = acc.TokenHash
	}
	match := subtle.ConstantTimeCompare([]byte(ident.HashToken(token)), []byte(want)) == 1
	if !known || !match || acc.Locked {
		return Account{}, false, nil
	}
	return acc, true, nil
}

func (s *sqliteStore) Accounts(ctx context.Context) ([]Account, error) {
	return readAccounts(ctx, s.db)
}

func (s *sqliteStore) AccountsOfUser(ctx context.Context, user string) ([]Account, error) {
	accounts, err := queryAccountRows(ctx, s.db, queries.AccountsOfUser, user)
	if err != nil {
		return nil, err
	}
	out := make([]Account, 0, len(accounts))
	for _, a := range accounts {
		acc, err := withRights(ctx, s.db, a)
		if err != nil {
			return nil, fmt.Errorf("Account %s: %w", a.Name, err)
		}
		out = append(out, acc)
	}
	return out, nil
}

// readAccounts liest alle Accounts samt Rechten: die lebenden Zeilen einmal
// für alle.
func readAccounts(ctx context.Context, db sqlitedb.Querier) ([]Account, error) {
	accounts, err := readAccountRows(ctx, db)
	if err != nil {
		return nil, err
	}
	rows, err := queryDocuments(ctx, db, queries.AccountRowsAllLive)
	if err != nil {
		return nil, err
	}
	byAccount := map[string][]Document{}
	for _, d := range rows {
		name, _ := contract.AccountOfRow(d.Name)
		byAccount[name] = append(byAccount[name], d)
	}
	out := make([]Account, 0, len(accounts))
	for _, a := range accounts {
		m, err := decodeRights(a.lockedRights)
		if !a.Locked {
			m, err = rowRights(byAccount[a.Name])
		}
		if err != nil {
			return nil, fmt.Errorf("Account %s: %w", a.Name, err)
		}
		acc := a.Account
		acc.Rights = m.list()
		out = append(out, acc)
	}
	return out, nil
}

// Arten eines Namens in principal_names.
const (
	kindNode    = "node"
	kindAccount = "account"
)

// nameTaken ist der Fehler, wenn der Name für want schon an holder vergeben
// ist — dieselbe Meldung aus der Vorprüfung wie aus der Datenbank.
func nameTaken(name, holder, want string) error {
	var msg string
	switch {
	case holder == want && want == kindNode:
		msg = fmt.Sprintf("Node %s gibt es schon", name)
	case holder == want:
		msg = fmt.Sprintf("Account %s gibt es schon", name)
	case holder == kindAccount:
		msg = fmt.Sprintf("Name %s ist schon an einen Account vergeben; Node- und Account-Namen sind gemeinsam eindeutig", name)
	default:
		msg = fmt.Sprintf("Name %s ist schon an einen Node vergeben; Node- und Account-Namen sind gemeinsam eindeutig", name)
	}
	return &kindError{ErrExists, msg}
}

// checkNodeNameFree prüft, dass kein Account den Namen trägt: Node- und
// Account-Namen sind gemeinsam eindeutig. Die Vorprüfung über die Tabellen
// accounts und nodes liefert die lesbare Meldung; die letzte Wache ist
// principal_names (claimName). Ein entfernter Account gibt seinen Namen
// frei, auch wenn seine Zeilen als Löschmarken bleiben.
func checkNodeNameFree(ctx context.Context, db sqlitedb.Querier, name string) error {
	n, err := count(ctx, db, queries.AccountCount, name)
	if err != nil {
		return err
	}
	if n > 0 {
		return nameTaken(name, kindAccount, kindNode)
	}
	return nil
}

// checkAccountNameFree prüft, dass kein Node den Namen trägt.
func checkAccountNameFree(ctx context.Context, db sqlitedb.Querier, name string) error {
	n, err := count(ctx, db, queries.NodeCount, name)
	if err != nil {
		return err
	}
	if n > 0 {
		return nameTaken(name, kindNode, kindAccount)
	}
	return nil
}

// claimName belegt einen Namen in principal_names, in der Transaktion, die
// den Node oder Account anlegt. Ist er schon belegt — auch von einer
// gleichzeitigen Transaktion, an der die Vorprüfung vorbeisah —, kommt
// derselbe Fehler wie aus der Vorprüfung (ErrExists).
func claimName(ctx context.Context, tx sqlitedb.Querier, name, kind string) error {
	res, err := tx.ExecContext(ctx, q(queries.NameClaim), name, kind)
	if err != nil {
		return fmt.Errorf("Name %s belegen: %w", name, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	var holder string
	if err := tx.QueryRowContext(ctx, q(queries.NameKind), name).Scan(&holder); err != nil {
		return &kindError{ErrExists, fmt.Sprintf("Name %s ist schon vergeben", name)}
	}
	return nameTaken(name, holder, kind)
}

// releaseName gibt einen Namen in principal_names frei.
func releaseName(ctx context.Context, tx sqlitedb.Querier, name string) error {
	if _, err := tx.ExecContext(ctx, q(queries.NameRelease), name); err != nil {
		return fmt.Errorf("Name %s freigeben: %w", name, err)
	}
	return nil
}

// rebuildNames baut principal_names beim Import aus nodes und accounts neu
// auf; ein Name in beiden bricht mit ErrExists ab.
func rebuildNames(ctx context.Context, tx sqlitedb.Querier) error {
	if _, err := tx.ExecContext(ctx, q(queries.NamesDeleteAll)); err != nil {
		return err
	}
	nodes, err := readNodes(ctx, tx)
	if err != nil {
		return err
	}
	for _, n := range nodes {
		if err := claimName(ctx, tx, n.Name, kindNode); err != nil {
			return err
		}
	}
	accounts, err := readAccountRows(ctx, tx)
	if err != nil {
		return err
	}
	for _, a := range accounts {
		if err := claimName(ctx, tx, a.Name, kindAccount); err != nil {
			return err
		}
	}
	return nil
}

// lockedAccountsUsing nennt die gesperrten Accounts, die sich Rechte auf
// collection gemerkt haben. Solche Rechte stehen in keiner Zeile, wenn sie
// während der Sperre vergeben wurden.
func lockedAccountsUsing(ctx context.Context, db sqlitedb.Querier, collection string) ([]string, error) {
	accounts, err := readAccountRows(ctx, db)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, a := range accounts {
		if !a.Locked {
			continue
		}
		m, err := decodeRights(a.lockedRights)
		if err != nil {
			return nil, fmt.Errorf("Account %s: %w", a.Name, err)
		}
		if _, ok := m[collection]; ok {
			out = append(out, a.Name)
		}
	}
	return out, nil
}

// accountTx ist ein Schreibvorgang an Accounts: eine Transaktion, höchstens
// eine Revision, ein Zeitpunkt, eine Zeile in actions. by (aus docTx) ist
// Admin für die CLI am Hub, bei rotate der User des Accounts.
type accountTx struct {
	docTx
}

// begin beginnt eine schreibende Transaktion. tx ist sie selbst oder, wenn
// ein Test traceTx gesetzt hat, ihre Aufzeichnung; Commit und Rollback gehen
// an sqlTx.
func (s *sqliteStore) begin(ctx context.Context) (sqlTx *sql.Tx, tx sqlitedb.Querier, err error) {
	sqlTx, err = s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	tx = sqlTx
	if s.traceTx != nil {
		tx = s.traceTx(tx)
	}
	return sqlTx, tx, nil
}

// lockAccount sperrt die Zeile eines Accounts in accounts schreibend — die
// erste Anweisung jedes Schreibvorgangs an oder durch einen Account.
func lockAccount(ctx context.Context, tx sqlitedb.Querier, name string) error {
	if _, err := tx.ExecContext(ctx, q(queries.AccountLock), name); err != nil {
		return fmt.Errorf("Account %s sperren: %w", name, err)
	}
	return nil
}

// writeAccount führt fn als einen Schreibvorgang am Account target aus und
// schreibt danach die Zeile in actions — mit der Revision, wenn fn Zeilen
// geändert hat. Liefert fn errUnchanged, wird nichts geschrieben, auch nicht
// in actions.
//
// Die erste Anweisung sperrt die Zeile von target in accounts (AccountLock);
// erst danach liest fn. So schreibt kein Vorgang die Zeilen mit einem Hash,
// den ein gleichzeitiger rotate schon ersetzt hat — auch unter PostgreSQL
// (READ COMMITTED), wo sonst erst die Revision sperrte. Ist target leer,
// sperrt fn selbst mit seiner ersten Anweisung (rotate: das bedingte
// Schreiben).
func (s *sqliteStore) writeAccount(ctx context.Context, target, account, carrier, action, subject string, fn func(w *accountTx) error) error {
	sqlTx, tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = sqlTx.Rollback() }()
	if target != "" {
		if err := lockAccount(ctx, tx, target); err != nil {
			return err
		}
	}
	w := &accountTx{docTx: newDocTx(tx, Admin, account, carrier)}
	if err := fn(w); err != nil {
		return err
	}
	if err := logActionFull(ctx, tx, w.now, account, carrier, action, subject, w.rev.rev); err != nil {
		return err
	}
	return sqlTx.Commit()
}

// logActionFull schreibt eine Zeile in actions; carrier leer und rev 0 werden
// NULL.
func logActionFull(ctx context.Context, tx sqlitedb.Querier, at int64, account, carrier, action, subject string, rev int64) error {
	var r any
	if rev > 0 {
		r = rev
	}
	if _, err := tx.ExecContext(ctx, q(queries.ActionInsertFull), at, account, nullable(carrier), action, nullable(subject), r); err != nil {
		return fmt.Errorf("actions schreiben: %w", err)
	}
	return nil
}

// setRow schreibt die Zeile eines Accounts in einer Collection: legt sie an,
// belebt eine Löschmarke wieder oder ändert den Inhalt. changed ist false,
// wenn die lebende Zeile schon genau diesen Inhalt trägt.
func (w *accountTx) setRow(ctx context.Context, collection, account string, c contract.AccountContent) (bool, error) {
	content, err := contract.EncodeAccountContent(c)
	if err != nil {
		return false, err
	}
	name := contract.AccountRowName(account)
	cur, err := scanDocument(w.tx.QueryRowContext(ctx, q(queries.AccountRowLatest), collection, name))
	found := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("%s in %s lesen: %w", name, collection, err)
	}
	if found && !cur.Deleted && cur.Content == content {
		return false, nil
	}
	rev, err := w.rev.get(ctx)
	if err != nil {
		return false, err
	}
	switch {
	case !found:
		_, err = w.tx.ExecContext(ctx, q(queries.DocumentInsert),
			ulid.Make().String(), collection, name, content, rev, w.now, w.by, w.now, w.by)
	case cur.Deleted:
		_, err = w.tx.ExecContext(ctx, q(queries.AccountRowRevive), cur.ID, content, rev, w.now, w.by)
	default:
		_, err = w.tx.ExecContext(ctx, q(queries.DocumentReplace), cur.ID, content, rev, w.now, w.by)
	}
	if err != nil {
		return false, fmt.Errorf("%s in %s schreiben: %w", name, collection, err)
	}
	return true, nil
}

// deleteRow macht eine lebende Zeile zur Löschmarke.
func (w *accountTx) deleteRow(ctx context.Context, d Document) error {
	rev, err := w.rev.get(ctx)
	if err != nil {
		return err
	}
	res, err := w.tx.ExecContext(ctx, q(queries.DocumentDelete), d.ID, rev, w.now, w.by)
	return mustAffect(res, err, d.Name+" in "+d.Collection)
}

// CheckUser prüft den User eines Accounts — dieselbe Prüfung für CLI und
// Import: Namensregel wie Accounts, admin reserviert (Account und User der
// CLI am Hub). Ein User darf wie ein Node oder ein anderer Account heißen.
func CheckUser(user string) error {
	return ident.CheckPrincipalName("User", user)
}

func (s *sqliteStore) AddAccount(ctx context.Context, name, user, description string) (string, error) {
	if err := ident.CheckPrincipalName("Account", name); err != nil {
		return "", err
	}
	if err := CheckUser(user); err != nil {
		return "", err
	}
	token, err := ident.NewToken()
	if err != nil {
		return "", err
	}
	err = s.writeAccount(ctx, name, Admin, "", "account.add", name, func(w *accountTx) error {
		n, err := count(ctx, w.tx, queries.AccountCount, name)
		if err != nil {
			return err
		}
		if n > 0 {
			return nameTaken(name, kindAccount, kindAccount)
		}
		if err := checkAccountNameFree(ctx, w.tx, name); err != nil {
			return err
		}
		if err := claimName(ctx, w.tx, name, kindAccount); err != nil {
			return err
		}
		_, err = w.tx.ExecContext(ctx, q(queries.AccountInsert),
			name, user, nullable(description), ident.HashToken(token), 0, nil, w.now, Admin)
		return err
	})
	if err != nil {
		return "", err
	}
	return token, nil
}

// AccountChange sind die Änderungen von SetAccount; nil lässt das Feld.
type AccountChange struct {
	Description *string
	User        *string
}

func (s *sqliteStore) SetAccount(ctx context.Context, name string, ch AccountChange) error {
	if ch.Description == nil && ch.User == nil {
		return errors.New("nichts zu ändern")
	}
	if ch.User != nil {
		if err := CheckUser(*ch.User); err != nil {
			return err
		}
	}
	return s.writeAccount(ctx, name, Admin, "", "account.set", name, func(w *accountTx) error {
		a, err := getAccount(ctx, w.tx, name)
		if err != nil {
			return err
		}
		if ch.Description != nil {
			if _, err := w.tx.ExecContext(ctx, q(queries.AccountSetDesc), name, nullable(*ch.Description)); err != nil {
				return err
			}
		}
		if ch.User == nil || *ch.User == a.User {
			return nil
		}
		if _, err := w.tx.ExecContext(ctx, q(queries.AccountSetUser), name, *ch.User); err != nil {
			return err
		}
		// Gesperrt sind alle Zeilen Löschmarken und bleiben es; unlock legt
		// sie mit dem User aus accounts an. Ohne Sperre ändern sich alle
		// lebenden Zeilen, Löschmarken bleiben — auch die in entfernten
		// Collections.
		rows, err := liveAccountRows(ctx, w.tx, name)
		if err != nil {
			return err
		}
		_, err = w.rewriteRows(ctx, name, a.TokenHash, *ch.User, rows)
		return err
	})
}

func (s *sqliteStore) SetAccountLocked(ctx context.Context, name string, locked bool) error {
	action := "account.unlock"
	if locked {
		action = "account.lock"
	}
	return s.writeAccount(ctx, name, Admin, "", action, name, func(w *accountTx) error {
		a, err := getAccount(ctx, w.tx, name)
		if err != nil {
			return err
		}
		if a.Locked == locked {
			if locked {
				return &kindError{ErrExists, fmt.Sprintf("Account %s ist schon gesperrt", name)}
			}
			return &kindError{ErrNotFound, fmt.Sprintf("Account %s ist nicht gesperrt", name)}
		}
		if locked {
			return w.lock(ctx, a)
		}
		return w.unlock(ctx, a)
	})
}

// lock macht alle lebenden Zeilen zu Löschmarken und merkt ihre Rechte.
func (w *accountTx) lock(ctx context.Context, a accountRow) error {
	rows, err := liveAccountRows(ctx, w.tx, a.Name)
	if err != nil {
		return err
	}
	m, err := rowRights(rows)
	if err != nil {
		return err
	}
	for _, d := range rows {
		if err := w.deleteRow(ctx, d); err != nil {
			return err
		}
	}
	saved, err := encodeRights(m)
	if err != nil {
		return err
	}
	_, err = w.tx.ExecContext(ctx, q(queries.AccountSetLocked), a.Name, 1, saved)
	return err
}

// unlock legt die Zeilen aus den gemerkten Rechten neu an, mit dem Hash aus
// accounts.
func (w *accountTx) unlock(ctx context.Context, a accountRow) error {
	m, err := decodeRights(a.lockedRights)
	if err != nil {
		return err
	}
	for _, r := range m.list() {
		if err := requireCollection(ctx, w.tx, r.Collection); err != nil {
			return fmt.Errorf("Account %s: %w", a.Name, err)
		}
		if _, err := w.setRow(ctx, r.Collection, a.Name, contract.AccountContent{Hash: a.TokenHash, User: a.User, Rights: r.Rights}); err != nil {
			return err
		}
	}
	_, err = w.tx.ExecContext(ctx, q(queries.AccountSetLocked), a.Name, 0, nil)
	return err
}

// setHash schreibt einen neuen Hash in accounts und alle lebenden Zeilen des
// Accounts a und liefert die Zeilen danach.
func (w *accountTx) setHash(ctx context.Context, a accountRow, hash string) ([]Document, error) {
	if _, err := w.tx.ExecContext(ctx, q(queries.AccountSetToken), a.Name, hash); err != nil {
		return nil, err
	}
	rows, err := liveAccountRows(ctx, w.tx, a.Name)
	if err != nil {
		return nil, err
	}
	return w.rewriteRows(ctx, a.Name, hash, a.User, rows)
}

// rewriteRows schreibt Hash und User in die lebenden Zeilen rows eines
// Accounts — die Rechte bleiben — und liefert die Zeilen danach. Eine Zeile,
// die schon genau so lautet, bleibt unberührt.
func (w *accountTx) rewriteRows(ctx context.Context, name, hash, user string, rows []Document) ([]Document, error) {
	m, err := rowRights(rows)
	if err != nil {
		return nil, err
	}
	for _, d := range rows {
		if _, err := w.setRow(ctx, d.Collection, name, contract.AccountContent{Hash: hash, User: user, Rights: m[d.Collection]}); err != nil {
			return nil, err
		}
	}
	return liveAccountRows(ctx, w.tx, name)
}

func (s *sqliteStore) NewAccountToken(ctx context.Context, name string) (string, error) {
	token, err := ident.NewToken()
	if err != nil {
		return "", err
	}
	err = s.writeAccount(ctx, name, Admin, "", "account.token", name, func(w *accountTx) error {
		a, err := getAccount(ctx, w.tx, name)
		if err != nil {
			return err
		}
		_, err = w.setHash(ctx, a, ident.HashToken(token))
		return err
	})
	if err != nil {
		return "", err
	}
	return token, nil
}

func (s *sqliteStore) RemoveAccount(ctx context.Context, name string) error {
	return s.writeAccount(ctx, name, Admin, "", "account.rm", name, func(w *accountTx) error {
		if _, err := getAccount(ctx, w.tx, name); err != nil {
			return err
		}
		rows, err := liveAccountRows(ctx, w.tx, name)
		if err != nil {
			return err
		}
		for _, d := range rows {
			if err := w.deleteRow(ctx, d); err != nil {
				return err
			}
		}
		if _, err := w.tx.ExecContext(ctx, q(queries.AccountDelete), name); err != nil {
			return err
		}
		return releaseName(ctx, w.tx, name)
	})
}

func (s *sqliteStore) GrantAccount(ctx context.Context, name, collection string, rights contract.Rights) (bool, error) {
	rights, err := contract.NormalizeRights(rights)
	if err != nil {
		return false, err
	}
	err = s.writeAccount(ctx, name, Admin, "", "account.grant", ident.Address(name, collection), func(w *accountTx) error {
		a, err := getAccount(ctx, w.tx, name)
		if err != nil {
			return err
		}
		if err := requireCollection(ctx, w.tx, collection); err != nil {
			return err
		}
		if a.Locked {
			// Gesperrt: nur die gemerkten Rechte ändern; die Zeile entsteht mit
			// unlock.
			m, err := decodeRights(a.lockedRights)
			if err != nil {
				return err
			}
			if cur, ok := m[collection]; ok && cur.Equal(rights) {
				return errUnchanged
			}
			m[collection] = rights
			saved, err := encodeRights(m)
			if err != nil {
				return err
			}
			_, err = w.tx.ExecContext(ctx, q(queries.AccountSetLocked), name, 1, saved)
			return err
		}
		changed, err := w.setRow(ctx, collection, name, contract.AccountContent{Hash: a.TokenHash, User: a.User, Rights: rights})
		if err != nil {
			return err
		}
		if !changed {
			return errUnchanged
		}
		return nil
	})
	if errors.Is(err, errUnchanged) {
		return false, nil
	}
	return err == nil, err
}

func (s *sqliteStore) RevokeAccount(ctx context.Context, name, collection string) error {
	return s.writeAccount(ctx, name, Admin, "", "account.revoke", ident.Address(name, collection), func(w *accountTx) error {
		a, err := getAccount(ctx, w.tx, name)
		if err != nil {
			return err
		}
		none := &kindError{ErrNotFound, fmt.Sprintf("Account %s hat keine Rechte in %s", name, collection)}
		if a.Locked {
			m, err := decodeRights(a.lockedRights)
			if err != nil {
				return err
			}
			if _, ok := m[collection]; !ok {
				return none
			}
			delete(m, collection)
			saved, err := encodeRights(m)
			if err != nil {
				return err
			}
			_, err = w.tx.ExecContext(ctx, q(queries.AccountSetLocked), name, 1, saved)
			return err
		}
		rows, err := liveAccountRows(ctx, w.tx, name)
		if err != nil {
			return err
		}
		i := slices.IndexFunc(rows, func(d Document) bool { return d.Collection == collection })
		if i < 0 {
			return none
		}
		return w.deleteRow(ctx, rows[i])
	})
}

func (s *sqliteStore) RotateAccount(ctx context.Context, name, oldHash, newHash, carrier string, shared []string) ([]contract.Row, error) {
	if !contract.IsTokenHash(newHash) {
		return nil, errors.New("neuer Hash ist kein sha256 in Hex")
	}
	var out []contract.Row
	// Kein target: Die erste Anweisung ist das bedingte Schreiben, es sperrt
	// die Zeile zugleich.
	err := s.writeAccount(ctx, "", name, carrier, "rotate", name, func(w *accountTx) error {
		res, err := w.tx.ExecContext(ctx, q(queries.AccountRotate), name, newHash, oldHash)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			// Unbekannt, falsches Token oder gesperrt; der Rollback nimmt
			// nichts zurück, weil nichts geschrieben ist.
			return ErrAccountAuth
		}
		// Erst nach der Sperre lesen: der User bleibt, wie er ist, und steht
		// in updated_by der Zeilen; actions.account bleibt der Account.
		a, err := getAccount(ctx, w.tx, name)
		if err != nil {
			return err
		}
		w.by = a.User
		rows, err := liveAccountRows(ctx, w.tx, name)
		if err != nil {
			return err
		}
		if !slices.ContainsFunc(rows, func(d Document) bool { return slices.Contains(shared, d.Collection) }) {
			return ErrNoSharedCollection
		}
		rows, err = w.rewriteRows(ctx, name, newHash, a.User, rows)
		if err != nil {
			return err
		}
		for _, d := range rows {
			if slices.Contains(shared, d.Collection) {
				out = append(out, documentRow(d))
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// documentRow liefert eine lebende Dokumentzeile als Zeile des Vertrags; meta
// ist bei Account-Zeilen immer NULL.
func documentRow(d Document) contract.Row {
	content := d.Content
	r := contract.Row{ID: d.ID, Collection: d.Collection, Name: d.Name, Content: &content, Deleted: d.Deleted,
		Revision: d.Revision, CreatedAt: d.CreatedAt, CreatedBy: d.CreatedBy, UpdatedAt: d.UpdatedAt, UpdatedBy: d.UpdatedBy}
	if d.Meta != "" {
		meta := d.Meta
		r.Meta = &meta
	}
	return r
}

// replaceAccounts ersetzt beim Import die Tabelle accounts und gleicht die
// SYSTEM:A:-Zeilen an: vorhandene ändern, fehlende werden Löschmarken, neue
// entstehen — alles unter der einen Revision des Imports. Die Rechte kommen
// in die gespeicherte Form (CheckTables hat sie geprüft).
func (w *accountTx) replaceAccounts(ctx context.Context, accounts []Account) error {
	if _, err := w.tx.ExecContext(ctx, q(queries.AccountsDeleteAll)); err != nil {
		return err
	}
	type key struct{ collection, account string }
	want := map[key]contract.AccountContent{}
	for _, a := range accounts {
		locked, saved := 0, any(nil)
		m := rightsMap{}
		for _, r := range a.Rights {
			rights, err := contract.NormalizeRights(r.Rights)
			if err != nil {
				return fmt.Errorf("Account %s: %w", a.Name, err)
			}
			m[r.Collection] = rights
		}
		if a.Locked {
			enc, err := encodeRights(m)
			if err != nil {
				return err
			}
			locked, saved = 1, enc
		} else {
			for c, r := range m {
				want[key{c, a.Name}] = contract.AccountContent{Hash: a.TokenHash, User: a.User, Rights: r}
			}
		}
		if _, err := w.tx.ExecContext(ctx, q(queries.AccountInsert),
			a.Name, a.User, nullable(a.Description), a.TokenHash, locked, saved, a.CreatedAt, a.CreatedBy); err != nil {
			return fmt.Errorf("Account %s: %w", a.Name, err)
		}
	}
	current, err := queryDocuments(ctx, w.tx, queries.AccountRowsAllLive)
	if err != nil {
		return err
	}
	for _, d := range current {
		name, _ := contract.AccountOfRow(d.Name)
		if _, ok := want[key{d.Collection, name}]; !ok {
			if err := w.deleteRow(ctx, d); err != nil {
				return err
			}
		}
	}
	keys := make([]key, 0, len(want))
	for k := range want {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].account != keys[j].account {
			return keys[i].account < keys[j].account
		}
		return keys[i].collection < keys[j].collection
	})
	for _, k := range keys {
		if _, err := w.setRow(ctx, k.collection, k.account, want[k]); err != nil {
			return err
		}
	}
	return nil
}
