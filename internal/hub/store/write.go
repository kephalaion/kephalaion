package store

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"

	"github.com/kephalaion/kephalaion/internal/contract"
	"github.com/kephalaion/kephalaion/internal/ident"
	"github.com/kephalaion/kephalaion/internal/sqlitedb"
)

// Schreiben über einen Node (Task 014): create, write und delete im Namen
// eines Accounts, getragen von einem Node. Anders als die CLI am Hub
// (PutDocument, DeleteDocument, ImportDocuments — admin, ohne Träger) prüft
// jeder Vorgang in seiner Transaktion, wer schreibt:
//
//  1. Die erste Anweisung sperrt die Zeile des Accounts in accounts
//     (lockAccount); erst danach wird gelesen.
//  2. Hash, gesperrt und User aus accounts, dort maßgeblich: unbekannt,
//     falsches Token oder gesperrt ist ErrAccountAuth.
//  3. Lesbarkeit: Die Collection gibt es, der Node darf sie abgleichen
//     (node_collections), der Account hat eine lebende SYSTEM:A:-Zeile in
//     ihr — sonst ErrNotReadable, dieselbe Antwort für alle drei.
//  4. Das Recht aus dieser Zeile: write für Neues und Eigenes (created_by ist
//     der User des Accounts, gleich über welchen seiner Accounts angelegt),
//     supersede für Fremdes; write ist dafür nicht nötig. Sonst
//     ErrForbidden mit dem Grund.
//
// created_by/updated_by ist der User, actions nennt je Dokument Account und
// Node. Die Antwort — Revision und Zeilen in der Form von SyncRows — liest
// der Vorgang vor dem Commit.

// Fehlerarten beim Schreiben über einen Node, für errors.Is. Dazu kommen
// ErrAccountAuth, ErrNotFound und ErrPathConflict.
var (
	// ErrInvalid: Name oder Inhalt ist ungültig (ident.CheckDocName,
	// CheckContent); die Meldung nennt den Grund.
	ErrInvalid = errors.New("ungültig")
	// ErrNotReadable: die Collection gibt es nicht, der Node darf sie nicht
	// abgleichen, oder der Account hat keine lebende Zeile in ihr.
	ErrNotReadable = errors.New("nicht lesbar")
	// ErrForbidden: dem Account fehlt das Recht — write für Neues und
	// Eigenes, supersede für Fremdes.
	ErrForbidden = errors.New("Recht fehlt")
	// ErrNameTaken: ein lebendes Dokument trägt den Namen schon.
	ErrNameTaken = errors.New("Name vergeben")
	// ErrStaleRevision: das Dokument hat nicht die Revision, auf der der
	// Vorgang beruht.
	ErrStaleRevision = errors.New("Revision veraltet")
)

// invalidError ist ein ungültiger Name oder Inhalt: die Meldung der Prüfung,
// für errors.Is zugleich ErrInvalid und die Fehlerart der Prüfung (etwa
// ErrTooLarge).
type invalidError struct{ err error }

func (e invalidError) Error() string   { return e.err.Error() }
func (e invalidError) Unwrap() []error { return []error{ErrInvalid, e.err} }

// dummyAccountHash wird verglichen, wenn es den Account nicht gibt: So
// kostet ein unbekannter Name denselben Vergleich wie ein falsches Token.
var dummyAccountHash = ident.HashToken("keph_unbekannter-account")

// WriteAuth ist die Anmeldung eines Schreibvorgangs über einen Node: der
// Account, der Hash des Tokens, das er vorgelegt hat, und der Node, der die
// Anfrage trägt (carrier). Den Node selbst hat der Aufrufer schon angemeldet.
type WriteAuth struct {
	Account   string
	TokenHash string
	Carrier   string
}

// WriteResult ist das Ergebnis eines Schreibvorgangs über einen Node: seine
// Revision — bei unverändertem Inhalt die bestehende — und die geschriebenen
// Zeilen in der Form von SyncRows, bei delete die Löschmarken.
type WriteResult struct {
	Revision int64
	Rows     []contract.Row
}

// writeAs führt fn als einen Schreibvorgang des Accounts auth.Account in
// collection aus, nach den Schritten oben. fn bekommt die Rechte des
// Accounts in der Collection; w.by ist sein User.
func (s *sqliteStore) writeAs(ctx context.Context, auth WriteAuth, collection string, fn func(w *docTx, rights contract.Rights) error) error {
	sqlTx, tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = sqlTx.Rollback() }()
	if err := lockAccount(ctx, tx, auth.Account); err != nil {
		return err
	}
	user, err := accountUser(ctx, tx, auth)
	if err != nil {
		return err
	}
	rights, err := readableRights(ctx, tx, auth, collection)
	if err != nil {
		return err
	}
	w := newDocTx(tx, user, auth.Account, auth.Carrier)
	if err := fn(&w, rights); err != nil {
		return err
	}
	return sqlTx.Commit()
}

// accountUser prüft nach der Sperre Hash und Sperre des Accounts gegen
// accounts und liefert seinen User. Unbekannt, falsches Token und gesperrt
// sind ErrAccountAuth; der Hash wird in jedem Fall in konstanter Zeit
// verglichen.
func accountUser(ctx context.Context, tx sqlitedb.Querier, auth WriteAuth) (string, error) {
	a, err := getAccount(ctx, tx, auth.Account)
	known := err == nil
	if err != nil && !errors.Is(err, ErrNotFound) {
		return "", err
	}
	want := dummyAccountHash
	if known {
		want = a.TokenHash
	}
	match := subtle.ConstantTimeCompare([]byte(auth.TokenHash), []byte(want)) == 1
	if !known || !match || a.Locked {
		return "", ErrAccountAuth
	}
	return a.User, nil
}

// readableRights prüft, dass collection für den Account über diesen Node
// lesbar ist, und liefert seine Rechte aus seiner Zeile in ihr.
func readableRights(ctx context.Context, tx sqlitedb.Querier, auth WriteAuth, collection string) (contract.Rights, error) {
	notReadable := &kindError{ErrNotReadable, fmt.Sprintf(
		"Collection %s ist nicht lesbar: unbekannt, für diesen Node nicht erlaubt oder für Account %s nicht eingetragen",
		collection, auth.Account)}
	ok, err := collectionExists(ctx, tx, collection)
	if err != nil {
		return contract.Rights{}, err
	}
	if !ok {
		return contract.Rights{}, notReadable
	}
	n, err := count(ctx, tx, queries.GrantGet, auth.Carrier, collection)
	if err != nil {
		return contract.Rights{}, err
	}
	if n == 0 {
		return contract.Rights{}, notReadable
	}
	row, err := liveDocument(ctx, tx, collection, contract.AccountRowName(auth.Account))
	if errors.Is(err, ErrNotFound) {
		return contract.Rights{}, notReadable
	}
	if err != nil {
		return contract.Rights{}, err
	}
	c, err := contract.DecodeAccountContent(row.Content)
	if err != nil {
		return contract.Rights{}, fmt.Errorf("%s in %s: %w", row.Name, collection, err)
	}
	return c.Rights, nil
}

// mayChange prüft das Recht, ein lebendes Dokument zu ändern oder zu
// löschen: Eigenes braucht write, Fremdes supersede.
func (w *docTx) mayChange(cur Document, rights contract.Rights, verb string) error {
	if cur.CreatedBy == w.by {
		if rights.Write {
			return nil
		}
		return &kindError{ErrForbidden, fmt.Sprintf("Dokument %s in %s %s: write fehlt", cur.Name, cur.Collection, verb)}
	}
	if rights.Supersede {
		return nil
	}
	return &kindError{ErrForbidden, fmt.Sprintf("Dokument %s in %s %s: gehört %s, supersede fehlt",
		cur.Name, cur.Collection, verb, cur.CreatedBy)}
}

// checkBase prüft die Revision, auf der ein Vorgang beruht; nil heißt ohne
// Vorbedingung. Die Revision ist global und steigt nur, Gleichheit genügt.
func checkBase(cur Document, base *int64) error {
	if base == nil || *base == cur.Revision {
		return nil
	}
	return &kindError{ErrStaleRevision, fmt.Sprintf("Dokument %s in %s hat Revision %d, der Vorgang beruht auf %d",
		cur.Name, cur.Collection, cur.Revision, *base)}
}

// result liest die Zeile id nach dem Schreiben, in der Form von SyncRows.
func (w *docTx) result(ctx context.Context, rev int64, id string) (WriteResult, error) {
	r, err := scanRow(w.tx.QueryRowContext(ctx, q(queries.DocumentByID), id))
	if err != nil {
		return WriteResult{}, fmt.Errorf("Dokument %s lesen: %w", id, err)
	}
	return WriteResult{Revision: rev, Rows: []contract.Row{r}}, nil
}

func checkWriteName(name string) error {
	if err := ident.CheckDocName(name); err != nil {
		return invalidError{err}
	}
	return nil
}

func checkWriteInput(name, content string) error {
	if err := checkDocumentInput(DocumentInput{Name: name, Content: content}); err != nil {
		return invalidError{err}
	}
	return nil
}

func (s *sqliteStore) CreateDocumentAs(ctx context.Context, auth WriteAuth, collection, name, content string) (WriteResult, error) {
	if err := checkWriteInput(name, content); err != nil {
		return WriteResult{}, err
	}
	var out WriteResult
	err := s.writeAs(ctx, auth, collection, func(w *docTx, rights contract.Rights) error {
		if !rights.Write {
			return &kindError{ErrForbidden, fmt.Sprintf("Dokument %s in %s anlegen: write fehlt", name, collection)}
		}
		_, err := liveDocument(ctx, w.tx, collection, name)
		if err == nil {
			return &kindError{ErrNameTaken, fmt.Sprintf("Dokument %s gibt es in %s schon", name, collection)}
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		res, err := w.create(ctx, collection, DocumentInput{Name: name, Content: content})
		if err != nil {
			return err
		}
		out, err = w.result(ctx, res.Revision, res.ID)
		return err
	})
	if err != nil {
		return WriteResult{}, err
	}
	return out, nil
}

func (s *sqliteStore) WriteDocumentAs(ctx context.Context, auth WriteAuth, collection, name, content string, base *int64) (WriteResult, error) {
	if err := checkWriteInput(name, content); err != nil {
		return WriteResult{}, err
	}
	var out WriteResult
	err := s.writeAs(ctx, auth, collection, func(w *docTx, rights contract.Rights) error {
		cur, err := liveDocument(ctx, w.tx, collection, name)
		if err != nil {
			return err
		}
		if err := w.mayChange(cur, rights, "ändern"); err != nil {
			return err
		}
		if err := checkBase(cur, base); err != nil {
			return err
		}
		if cur.Content == content {
			out, err = w.result(ctx, cur.Revision, cur.ID)
			return err
		}
		res, err := w.replace(ctx, cur, content)
		if err != nil {
			return err
		}
		out, err = w.result(ctx, res.Revision, res.ID)
		return err
	})
	if err != nil {
		return WriteResult{}, err
	}
	return out, nil
}

func (s *sqliteStore) DeleteDocumentAs(ctx context.Context, auth WriteAuth, collection, name string, base *int64) (WriteResult, error) {
	if err := checkWriteName(name); err != nil {
		return WriteResult{}, err
	}
	var out WriteResult
	err := s.writeAs(ctx, auth, collection, func(w *docTx, rights contract.Rights) error {
		cur, err := liveDocument(ctx, w.tx, collection, name)
		if err != nil {
			return err
		}
		if err := w.mayChange(cur, rights, "löschen"); err != nil {
			return err
		}
		if err := checkBase(cur, base); err != nil {
			return err
		}
		rev, err := w.delete(ctx, cur)
		if err != nil {
			return err
		}
		out, err = w.result(ctx, rev, cur.ID)
		return err
	})
	if err != nil {
		return WriteResult{}, err
	}
	return out, nil
}
