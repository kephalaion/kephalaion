package store

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"path"

	"github.com/kephalaion/kephalaion/internal/contract"
	"github.com/kephalaion/kephalaion/internal/hub/source"
	"github.com/kephalaion/kephalaion/internal/ident"
	"github.com/kephalaion/kephalaion/internal/sqlitedb"
)

// Schreiben über einen Node (Task 014): create, write, delete und rename im
// Namen eines Accounts, getragen von einem Node. Anders als die CLI am Hub
// (PutDocument, DeleteDocument, ImportDocuments — admin, ohne Träger) prüft
// jeder Vorgang in seiner Transaktion, wer schreibt:
//
//  1. Die erste Anweisung sperrt die Zeile des Accounts in accounts
//     (lockAccount); erst danach wird gelesen.
//  2. Hash, gesperrt und User aus accounts, dort maßgeblich: unbekannt,
//     falsches Token oder gesperrt ist ErrAccountAuth.
//  3. Lesbarkeit: Die Collection gibt es, der Node darf sie abgleichen
//     (node_collections, oder sie steht in der Liste der Quellen), der
//     Account hat eine lebende SYSTEM:A:-Zeile in ihr oder sie ist
//     öffentlich — sonst ErrNotReadable, dieselbe Antwort für alle drei.
//  4. In einer Collection mit Quelle schreibt niemand: ErrForbidden.
//  5. Mit WriteAuth.RequestID der Schlüssel (request.go, Task 032): Ist er
//     beim Account gemerkt, mit demselben Fingerabdruck, ist der Vorgang
//     eine Wiederholung — nichts wird geschrieben, auch nicht in actions, das
//     Ergebnis trägt Replayed; mit einem anderen ist er ErrInvalid. Abgelaufene
//     Schlüssel entfernt jeder Vorgang gleich nach der Sperre.
//  6. Das Recht aus dieser Zeile (ohne Zeile keines), je Dokument nach der einen Regel in
//     contract.Rights.MayWrite: write für Neues und Eigenes (created_by ist
//     der User des Accounts, gleich über welchen seiner Accounts angelegt),
//     supersede für Fremdes; unter vendor/<name>/ allein der Scope
//     vendor/<name>, direkt in vendor/ niemand; unter einem Verzeichnis-Scope
//     <pfad>/ auch ohne write und unabhängig vom Urheber. Sonst ErrForbidden
//     mit dem Grund.
//
// Unter docs/ (Task 033, docs/vertrag.md, „Erzeugte Dateien unter docs/“):
// Eine erzeugte Datei (contract.IsGenerated) lehnt jeder Einzelvorgang ab
// (ErrGenerated, über MayWrite); eine Mahnung (contract.IsNotice) ersetzt
// und löscht jeder mit write oder supersede (MayTakeOver), umbenennen lässt
// sie sich einzeln nicht. delete mit recursive nimmt beide mit, rename eines
// Verzeichnisses setzt auf sie eine Löschmarke, statt sie umzubenennen; ein
// rename nach docs/ hinein, das eine erzeugte Datei brächte, ist
// ErrGenerated. Ebenso ein neuer Name (create, rename), der unter docs/
// index.md oder README.md als Verzeichnis nutzte (contract.ReservedDirOf,
// reservedDir) — er nähme dem Neubau den Platz.
//
// delete und rename nehmen auch ein Verzeichnis — alle lebenden Dokumente
// darunter, als Ganzes: eine Revision, das Recht je Dokument (bei rename mit
// altem und neuem Namen), alles oder nichts. Reihenfolge nach der Lesbarkeit: gibt es den Namen (ErrNotFound),
// gilt der Vorgang für diese Art (ErrInvalid: Verzeichnis ohne recursive,
// base_revision bei einem Verzeichnis, ein neuer Name ungültig), das Recht,
// die Revision eines Dokuments, zuletzt das Ziel von rename.
//
// created_by/updated_by ist der User, actions nennt je Dokument Account und
// Node. Die Antwort — Revision und Zeilen in der Form von SyncRows — liest
// der Vorgang vor dem Commit; mit Schlüssel trägt er ihn ebenfalls vor dem
// Commit ein.

// Fehlerarten beim Schreiben über einen Node, für errors.Is. Dazu kommen
// ErrAccountAuth, ErrNotFound und ErrPathConflict.
var (
	// ErrInvalid: Name oder Inhalt ist ungültig (ident.CheckDocName,
	// ident.CheckRename, CheckContent), oder der Vorgang gilt nicht für ein
	// Verzeichnis; die Meldung nennt den Grund.
	ErrInvalid = errors.New("ungültig")
	// ErrNotReadable: die Collection gibt es nicht, der Node darf sie nicht
	// abgleichen, oder der Account hat keine lebende Zeile in ihr.
	ErrNotReadable = errors.New("nicht lesbar")
	// ErrForbidden: dem Account fehlt das Recht — write für Neues und
	// Eigenes, supersede für Fremdes (beides nicht nötig unter einem
	// Verzeichnis-Scope), unter vendor/<name>/ der Scope; direkt in vendor/
	// schreibt niemand.
	ErrForbidden = errors.New("Recht fehlt")
	// ErrNameTaken: ein lebendes Dokument trägt den Namen schon.
	ErrNameTaken = errors.New("Name vergeben")
	// ErrStaleRevision: das Dokument hat nicht die Revision, auf der der
	// Vorgang beruht.
	ErrStaleRevision = errors.New("Revision veraltet")
	// ErrGenerated: eine erzeugte Datei unter docs/ schreibt nur das System
	// — ein Einzelvorgang darauf, ein rename nach docs/ hinein, das eine
	// mitbrächte, ein rename einer einzelnen Mahnung, oder ein neuer Name,
	// der unter docs/ index.md oder README.md als Verzeichnis nutzte (Task
	// 033). Auch der Admin schreibt sie nicht (PutDocument, DeleteDocument).
	ErrGenerated = errors.New("erzeugte Datei")
)

// invalidError ist ein ungültiger Name oder Inhalt: die Meldung der Prüfung,
// für errors.Is zugleich ErrInvalid und die Fehlerart der Prüfung (etwa
// ErrTooLarge).
type invalidError struct{ err error }

func (e invalidError) Error() string   { return e.err.Error() }
func (e invalidError) Unwrap() []error { return []error{ErrInvalid, e.err} }

// WriteAuth ist die Anmeldung eines Schreibvorgangs über einen Node: der
// Account, der Hash des Tokens, das er vorgelegt hat, und der Node, der die
// Anfrage trägt (carrier). Den Node selbst hat der Aufrufer schon angemeldet.
// RequestID ist wahlweise der Idempotenzschlüssel des Aufrufers
// (contract.CheckRequestID), gültig je Account; leer heißt ohne.
type WriteAuth struct {
	Account   string
	TokenHash string
	Carrier   string
	RequestID string
}

// WriteResult ist das Ergebnis eines Schreibvorgangs über einen Node: seine
// Revision — bei unverändertem Inhalt die bestehende — und die geschriebenen
// Zeilen in der Form von SyncRows, bei delete die Löschmarken. Replayed
// heißt: Der Schlüssel war schon gemerkt, nichts ist geschrieben; Revision
// und IDs sind die des ursprünglichen Vorgangs, Rows ist leer.
type WriteResult struct {
	Revision int64
	Rows     []contract.Row
	Replayed bool
	// IDs sind nur bei Replayed gesetzt.
	IDs []string
}

// writeAs führt fn als einen Schreibvorgang des Accounts auth.Account in
// collection aus, nach den Schritten oben. fn bekommt die Rechte des
// Accounts in der Collection; w.by ist sein User. key beschreibt den
// Vorgang für den Schlüssel; ohne auth.RequestID zählt er nicht.
func (s *sqliteStore) writeAs(ctx context.Context, auth WriteAuth, collection string, key writeKey,
	fn func(w *docTx, rights contract.Rights) (WriteResult, error)) (WriteResult, error) {
	if err := checkRequestID(auth.RequestID); err != nil {
		return WriteResult{}, err
	}
	sqlTx, tx, err := s.begin(ctx)
	if err != nil {
		return WriteResult{}, err
	}
	defer func() { _ = sqlTx.Rollback() }()
	if err := lockAccount(ctx, tx, auth.Account); err != nil {
		return WriteResult{}, err
	}
	w := newDocTx(tx, "", auth.Account, auth.Carrier)
	if err := expireRequests(ctx, tx, w.now); err != nil {
		return WriteResult{}, err
	}
	user, err := accountUser(ctx, tx, auth)
	if err != nil {
		return WriteResult{}, err
	}
	w.by = user
	rights, err := readableRights(ctx, tx, auth, collection)
	if err != nil {
		return WriteResult{}, err
	}
	if _, ok := source.Lookup(collection); ok {
		return WriteResult{}, sourceLocked(collection, "schreiben über einen Node")
	}
	if auth.RequestID != "" {
		prev, found, err := lookupRequest(ctx, tx, auth.Account, auth.RequestID)
		if err != nil {
			return WriteResult{}, err
		}
		if found {
			// Geschrieben ist nichts; der Commit hält nur das Aufräumen der
			// Frist fest.
			res, err := replay(prev, key, auth.RequestID)
			if err != nil {
				return WriteResult{}, err
			}
			return res, sqlTx.Commit()
		}
	}
	res, err := fn(&w, rights)
	if err != nil {
		return WriteResult{}, err
	}
	if auth.RequestID != "" {
		if err := rememberRequest(ctx, tx, auth.Account, auth.RequestID, key, res, w.now); err != nil {
			return WriteResult{}, err
		}
	}
	return res, sqlTx.Commit()
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
// lesbar ist, und liefert seine Rechte aus seiner Zeile in ihr — in einer
// öffentlichen Collection ohne Zeile keine.
func readableRights(ctx context.Context, tx sqlitedb.Querier, auth WriteAuth, collection string) (contract.Rights, error) {
	notReadable := &kindError{ErrNotReadable, fmt.Sprintf(
		"Collection %s ist nicht lesbar: unbekannt, für diesen Node nicht erlaubt oder für Account %s nicht eingetragen",
		collection, auth.Account)}
	c, err := getCollection(ctx, tx, collection)
	if errors.Is(err, ErrNotFound) {
		return contract.Rights{}, notReadable
	}
	if err != nil {
		return contract.Rights{}, err
	}
	if _, listed := source.Lookup(collection); !listed {
		n, err := count(ctx, tx, queries.GrantGet, auth.Carrier, collection)
		if err != nil {
			return contract.Rights{}, err
		}
		if n == 0 {
			return contract.Rights{}, notReadable
		}
	}
	row, err := liveDocument(ctx, tx, collection, contract.AccountRowName(auth.Account))
	if errors.Is(err, ErrNotFound) {
		if c.IsPublic() {
			return contract.Rights{}, nil
		}
		return contract.Rights{}, notReadable
	}
	if err != nil {
		return contract.Rights{}, err
	}
	content, err := contract.DecodeAccountContent(row.Content)
	if err != nil {
		return contract.Rights{}, fmt.Errorf("%s in %s: %w", row.Name, collection, err)
	}
	return content.Rights, nil
}

// mayChange prüft das Recht, ein lebendes Dokument zu ändern, zu löschen
// oder umzubenennen — nach contract.Rights.MayWrite, mit dem Namen des
// Dokuments: Eigenes braucht write, Fremdes supersede, unter vendor/<name>/
// nur der Scope, unter einem Verzeichnis-Scope keines von beiden.
func (w *docTx) mayChange(cur Document, rights contract.Rights, verb string) error {
	return w.mayWriteAs(cur.Name, cur, rights, verb)
}

// mayWriteAs prüft das Recht an cur unter dem Namen name — bei rename der
// neue —; own ist, ob der User des Accounts das Dokument angelegt hat. Eine
// Mahnung unter ihrem eigenen Namen ersetzt jeder mit write oder supersede
// (MayTakeOver).
func (w *docTx) mayWriteAs(name string, cur Document, rights contract.Rights, verb string) error {
	own := cur.CreatedBy == w.by
	var d *contract.WriteDenial
	if name == cur.Name && contract.IsNotice(cur.Name, cur.UpdatedBy) {
		d = rights.MayTakeOver(name)
	} else {
		d = rights.MayWrite(name, own)
	}
	if d == nil {
		return nil
	}
	reason := d.Error()
	if d.Kind == contract.DenySupersede {
		reason = "gehört " + cur.CreatedBy + ", " + reason
	}
	what := name
	if name != cur.Name {
		what = cur.Name + " nach " + name
	}
	return denial(d, fmt.Sprintf("Dokument %s in %s %s: %s", what, cur.Collection, verb, reason))
}

// denial ist der Fehler zu einer Ablehnung nach MayWrite: eine erzeugte
// Datei ErrGenerated, alles andere ErrForbidden.
func denial(d *contract.WriteDenial, msg string) error {
	if d.Kind == contract.DenyGenerated {
		return &kindError{ErrGenerated, msg}
	}
	return &kindError{ErrForbidden, msg}
}

// mayCreate prüft das Recht, name in collection anzulegen: Neues zählt wie
// Eigenes (write), unter vendor/<name>/ der Scope, unter einem
// Verzeichnis-Scope ohne write; eine erzeugte Datei unter docs/ legt niemand
// an, ebenso keinen Namen, der dort index.md oder README.md als Verzeichnis
// nutzte (ErrGenerated).
func mayCreate(collection, name string, rights contract.Rights) error {
	if err := reservedDir(name, fmt.Sprintf("Dokument %s in %s anlegen", name, collection)); err != nil {
		return err
	}
	if d := rights.MayWrite(name, true); d != nil {
		return denial(d, fmt.Sprintf("Dokument %s in %s anlegen: %v", name, collection, d))
	}
	return nil
}

// reservedDir lehnt einen neuen Namen ab, der unter docs/ index.md oder
// README.md als Verzeichnis nutzte (contract.ReservedDirOf): ErrGenerated,
// gleich mit welchem Recht, auch für den Admin. Auf diesen Namen legt der
// Neubau eine Datei; ein Verzeichnis dort ließe ihr keinen Platz. what
// beginnt die Meldung. Ein Dokument, das so schon heißt, bleibt änderbar,
// löschbar und lässt sich unter einen anderen Namen umbenennen.
func reservedDir(name, what string) error {
	dir := contract.ReservedDirOf(name)
	if dir == "" {
		return nil
	}
	return &kindError{ErrGenerated, fmt.Sprintf(
		"%s: %s wäre ein Verzeichnis; unter %s/ ist %s der Name einer Datei, die das System schreibt "+
			"(rebuild_docs), nie ein Verzeichnis", what, dir, contract.DocsDir, path.Base(dir))}
}

// mayTakeAlong prüft das Recht an einem Dokument des Systems unter docs/
// (contract.IsSystemDoc), das ein Verzeichnisvorgang mitnimmt: wie eine
// Mahnung ersetzen (MayTakeOver) — write oder supersede, unter einem
// Verzeichnis-Scope keines von beiden.
func (w *docTx) mayTakeAlong(cur Document, rights contract.Rights, verb string) error {
	if d := rights.MayTakeOver(cur.Name); d != nil {
		return &kindError{ErrForbidden, fmt.Sprintf("Dokument %s in %s %s: %v", cur.Name, cur.Collection, verb, d)}
	}
	return nil
}

// splitSystemDocs teilt die Dokumente unter einem Verzeichnis in die
// gewöhnlichen und die des Systems unter docs/ (erzeugte Dateien und
// Mahnungen), je in ihrer Reihenfolge.
func splitSystemDocs(docs []Document) (regular, system []Document) {
	for _, d := range docs {
		if contract.IsSystemDoc(d.Name, d.UpdatedBy) {
			system = append(system, d)
		} else {
			regular = append(regular, d)
		}
	}
	return regular, system
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
	return w.results(ctx, rev, []string{id})
}

// results liest die Zeilen ids nach dem Schreiben, in dieser Reihenfolge und
// in der Form von SyncRows.
func (w *docTx) results(ctx context.Context, rev int64, ids []string) (WriteResult, error) {
	out := WriteResult{Revision: rev, Rows: make([]contract.Row, 0, len(ids))}
	for _, id := range ids {
		r, err := scanRow(w.tx.QueryRowContext(ctx, q(queries.DocumentByID), id))
		if err != nil {
			return WriteResult{}, fmt.Errorf("Dokument %s lesen: %w", id, err)
		}
		out.Rows = append(out.Rows, r)
	}
	return out, nil
}

// source liest, was ein Name vor delete oder rename bezeichnet: ein lebendes
// Dokument (dir false, genau eines in docs) oder ein Verzeichnis (dir true)
// — dann alle lebenden Dokumente darunter, nach Name. Weder noch ist
// ErrNotFound.
func (w *docTx) source(ctx context.Context, collection, name string) (docs []Document, dir bool, err error) {
	cur, err := liveDocument(ctx, w.tx, collection, name)
	if err == nil {
		return []Document{cur}, false, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, false, err
	}
	docs, err = liveDocuments(ctx, w.tx, collection, name+"/")
	if err != nil {
		return nil, false, err
	}
	if len(docs) == 0 {
		return nil, false, &kindError{ErrNotFound, fmt.Sprintf(
			"%s gibt es in %s weder als Dokument noch als Verzeichnis", name, collection)}
	}
	return docs, true, nil
}

// checkDirBase lehnt eine Vorbedingung für ein Verzeichnis ab: base_revision
// gilt nur für Dokumente.
func checkDirBase(collection, name string, base *int64) error {
	if base != nil {
		return invalidError{fmt.Errorf("%s ist in %s ein Verzeichnis; base_revision gilt nur für Dokumente", name, collection)}
	}
	return nil
}

// mayChangeAll prüft das Recht an jedem Dokument (mayChange); das erste
// verbotene lässt den ganzen Vorgang scheitern.
func (w *docTx) mayChangeAll(docs []Document, rights contract.Rights, verb string) error {
	for _, d := range docs {
		if err := w.mayChange(d, rights, verb); err != nil {
			return err
		}
	}
	return nil
}

func ids(docs []Document) []string {
	out := make([]string, len(docs))
	for i, d := range docs {
		out[i] = d.ID
	}
	return out
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
	key := newWriteKey(fingerprintInput{Op: "create", Collection: collection, Name: name, Content: &content})
	return s.writeAs(ctx, auth, collection, key, func(w *docTx, rights contract.Rights) (WriteResult, error) {
		if err := mayCreate(collection, name, rights); err != nil {
			return WriteResult{}, err
		}
		_, err := liveDocument(ctx, w.tx, collection, name)
		if err == nil {
			return WriteResult{}, &kindError{ErrNameTaken, fmt.Sprintf("Dokument %s gibt es in %s schon", name, collection)}
		}
		if !errors.Is(err, ErrNotFound) {
			return WriteResult{}, err
		}
		res, err := w.create(ctx, collection, DocumentInput{Name: name, Content: content})
		if err != nil {
			return WriteResult{}, err
		}
		return w.result(ctx, res.Revision, res.ID)
	})
}

func (s *sqliteStore) WriteDocumentAs(ctx context.Context, auth WriteAuth, collection, name, content string, base *int64) (WriteResult, error) {
	if err := checkWriteInput(name, content); err != nil {
		return WriteResult{}, err
	}
	key := newWriteKey(fingerprintInput{Op: "write", Collection: collection, Name: name, Content: &content,
		BaseRevision: base})
	return s.writeAs(ctx, auth, collection, key, func(w *docTx, rights contract.Rights) (WriteResult, error) {
		cur, err := liveDocument(ctx, w.tx, collection, name)
		if err != nil {
			return WriteResult{}, err
		}
		if err := w.mayChange(cur, rights, "ändern"); err != nil {
			return WriteResult{}, err
		}
		if err := checkBase(cur, base); err != nil {
			return WriteResult{}, err
		}
		if cur.Content == content {
			return w.result(ctx, cur.Revision, cur.ID)
		}
		res, err := w.replace(ctx, cur, content)
		if err != nil {
			return WriteResult{}, err
		}
		return w.result(ctx, res.Revision, res.ID)
	})
}

func (s *sqliteStore) DeleteDocumentAs(ctx context.Context, auth WriteAuth, collection, name string, base *int64, recursive bool) (WriteResult, error) {
	if err := checkWriteName(name); err != nil {
		return WriteResult{}, err
	}
	key := newWriteKey(fingerprintInput{Op: "delete", Collection: collection, Name: name, BaseRevision: base,
		Recursive: recursive})
	return s.writeAs(ctx, auth, collection, key, func(w *docTx, rights contract.Rights) (WriteResult, error) {
		docs, dir, err := w.source(ctx, collection, name)
		if err != nil {
			return WriteResult{}, err
		}
		if dir {
			if !recursive {
				return WriteResult{}, invalidError{fmt.Errorf(
					"%s ist in %s ein Verzeichnis mit %d Dokumenten; löschen nur mit recursive", name, collection, len(docs))}
			}
			if err := checkDirBase(collection, name, base); err != nil {
				return WriteResult{}, err
			}
		}
		if dir {
			// Erzeugte Dateien und Mahnungen gehen mit (Task 033); die Sperre
			// gilt für Einzelvorgänge.
			regular, system := splitSystemDocs(docs)
			if err := w.mayChangeAll(regular, rights, "löschen"); err != nil {
				return WriteResult{}, err
			}
			for _, d := range system {
				if err := w.mayTakeAlong(d, rights, "löschen"); err != nil {
					return WriteResult{}, err
				}
			}
		} else {
			if err := w.mayChange(docs[0], rights, "löschen"); err != nil {
				return WriteResult{}, err
			}
			if err := checkBase(docs[0], base); err != nil {
				return WriteResult{}, err
			}
		}
		for _, d := range docs {
			if _, err := w.delete(ctx, d); err != nil {
				return WriteResult{}, err
			}
		}
		return w.results(ctx, w.rev.rev, ids(docs))
	})
}

func (s *sqliteStore) RenameDocumentAs(ctx context.Context, auth WriteAuth, collection, name, newName string, base *int64) (WriteResult, error) {
	if err := ident.CheckRename(name, newName); err != nil {
		return WriteResult{}, invalidError{err}
	}
	key := newWriteKey(fingerprintInput{Op: "rename", Collection: collection, Name: name, NewName: newName,
		BaseRevision: base})
	return s.writeAs(ctx, auth, collection, key, func(w *docTx, rights contract.Rights) (WriteResult, error) {
		all, dir, err := w.source(ctx, collection, name)
		if err != nil {
			return WriteResult{}, err
		}
		// Bei einem Verzeichnis wandern erzeugte Dateien und Mahnungen nicht
		// mit (Task 033): Sie bekommen eine Löschmarke, der nächste Neubau
		// stellt her, was unter docs/ fehlt.
		docs, system := all, []Document(nil)
		if dir {
			docs, system = splitSystemDocs(all)
		}
		renamed := make([]string, len(docs))
		for i, d := range docs {
			renamed[i], _ = ident.DocRenamed(d.Name, name, newName)
			if err := ident.CheckDocName(renamed[i]); err != nil {
				return WriteResult{}, invalidError{fmt.Errorf("%s umbenennen: %w", d.Name, err)}
			}
		}
		if dir {
			if err := checkDirBase(collection, name, base); err != nil {
				return WriteResult{}, err
			}
			for i, d := range docs {
				if contract.IsGenerated(renamed[i]) {
					return WriteResult{}, &kindError{ErrGenerated, fmt.Sprintf(
						"Verzeichnis %s in %s nach %s umbenennen: %s würde zu %s, einer erzeugten Datei unter %s/; "+
							"die schreibt nur das System — erst %s entfernen oder umbenennen",
						name, collection, newName, d.Name, renamed[i], contract.DocsDir, d.Name)}
				}
			}
		} else if contract.IsNotice(docs[0].Name, docs[0].UpdatedBy) {
			return WriteResult{}, &kindError{ErrGenerated, fmt.Sprintf(
				"Dokument %s in %s umbenennen: eine Mahnung des Systems wandert nicht unter einen neuen Namen; "+
					"mit write ersetzen oder löschen", docs[0].Name, collection)}
		}
		// Kein neuer Name nutzt unter docs/ index.md oder README.md als
		// Verzeichnis — auch nicht innerhalb eines solchen Verzeichnisses
		// aus der Zeit davor; heraus geht es immer.
		for i, d := range docs {
			if err := reservedDir(renamed[i], fmt.Sprintf("Dokument %s in %s nach %s umbenennen",
				d.Name, collection, renamed[i])); err != nil {
				return WriteResult{}, err
			}
		}
		// Das Recht am alten und am neuen Namen, je Dokument: hinein, heraus
		// und innerhalb von vendor/ ohne Sonderfall.
		if err := w.mayChangeAll(docs, rights, "umbenennen"); err != nil {
			return WriteResult{}, err
		}
		for i, d := range docs {
			if err := w.mayWriteAs(renamed[i], d, rights, "umbenennen"); err != nil {
				return WriteResult{}, err
			}
		}
		for _, d := range system {
			if err := w.mayTakeAlong(d, rights, "mit dem Verzeichnis entfernen"); err != nil {
				return WriteResult{}, err
			}
		}
		if !dir {
			if err := checkBase(docs[0], base); err != nil {
				return WriteResult{}, err
			}
		}
		if err := renameTargetFree(ctx, w.tx, collection, newName, dir); err != nil {
			return WriteResult{}, err
		}
		for i, d := range docs {
			if err := w.rename(ctx, d, renamed[i]); err != nil {
				return WriteResult{}, err
			}
		}
		for _, d := range system {
			if _, err := w.delete(ctx, d); err != nil {
				return WriteResult{}, err
			}
		}
		return w.results(ctx, w.rev.rev, append(ids(docs), ids(system)...))
	})
}

// renameTargetFree prüft das Ziel eines Umbenennens, im Stand davor — liegt
// die Quelle im Ziel (x/y nach x), belegt sie es selbst:
//
//   - Ein Dokument geht nicht auf ein lebendes Dokument, ein Verzeichnis
//     nicht auf ein Verzeichnis: ErrNameTaken. Nichts wird überschrieben oder
//     zusammengelegt.
//   - Ein Dokument auf ein Verzeichnis, ein Verzeichnis auf ein Dokument oder
//     ein Dokument über dem Ziel: ErrPathConflict — der Name wäre zugleich
//     Datei und Verzeichnis, wie bei create.
func renameTargetFree(ctx context.Context, db sqlitedb.Querier, collection, newName string, dir bool) error {
	if !dir {
		if _, err := liveDocument(ctx, db, collection, newName); err == nil {
			return &kindError{ErrNameTaken, fmt.Sprintf("Dokument %s gibt es in %s schon", newName, collection)}
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
		return checkPathFree(ctx, db, collection, newName)
	}
	what := "Verzeichnis " + newName
	n, err := count(ctx, db, queries.DocumentLiveCount, collection, newName)
	if err != nil {
		return err
	}
	if n > 0 {
		return &kindError{ErrPathConflict, fmt.Sprintf(
			"%s: %s ist in %s ein Dokument und kann nicht zugleich Verzeichnis sein", what, newName, collection)}
	}
	lo, hi := dirRange(newName + "/")
	if n, err = count(ctx, db, queries.DocumentsUnderLive, collection, lo, hi); err != nil {
		return err
	}
	if n > 0 {
		return &kindError{ErrNameTaken, fmt.Sprintf(
			"Verzeichnis %s gibt es in %s schon, mit %d Dokumenten; umbenennen legt nicht zusammen", newName, collection, n)}
	}
	return checkAncestorsFree(ctx, db, collection, what, newName)
}
