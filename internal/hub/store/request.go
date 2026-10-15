package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/kephalaion/kephalaion/internal/contract"
	"github.com/kephalaion/kephalaion/internal/sqlitedb"
)

// Der Idempotenzschlüssel der Schreibvorgänge über einen Node (Task 032,
// docs/vertrag.md, „Wiederholung mit Schlüssel“). Die Tabelle requests merkt
// sich je Account und Schlüssel einen gelungenen Vorgang: welcher es war
// (op), sein Fingerabdruck, seine Revision und die ids seiner Zeilen.
// Eingetragen wird in der Transaktion des Vorgangs, vor ihrem Commit — der
// Schlüssel steht also genau dann am Hub, wenn geschrieben wurde. Eine
// Ablehnung rollt zurück und hinterlässt nichts. Nachgeschlagen wird unter
// der Sperre des Accounts, nach Account, Lesbarkeit und Quelle, vor Recht
// und Name; zwei Aufrufe mit demselben Schlüssel laufen so nacheinander.
// Die Tabelle ist abgeleitet und steht nicht im Export.

// writeKey beschreibt einen Schreibvorgang für den Schlüssel: den Vorgang
// und den Fingerabdruck seiner Felder. Der Schlüssel selbst steht in
// WriteAuth.RequestID.
type writeKey struct {
	op          string
	fingerprint string
}

// fingerprintInput sind die Felder, über die der Fingerabdruck geht. JSON
// trennt sie eindeutig; nil (das Feld gehört nicht zum Vorgang) und leer
// bleiben verschieden.
type fingerprintInput struct {
	Op           string  `json:"op"`
	Collection   string  `json:"collection"`
	Name         string  `json:"name"`
	NewName      string  `json:"new_name"`
	Content      *string `json:"content"`
	BaseRevision *int64  `json:"base_revision"`
	Recursive    bool    `json:"recursive"`
}

// newWriteKey bildet den Schlüssel eines Vorgangs: SHA-256 über Vorgang,
// Collection, Name, neuen Namen, Inhalt, base_revision und recursive, hex.
func newWriteKey(in fingerprintInput) writeKey {
	b, err := json.Marshal(in)
	if err != nil {
		// Strings, ein Zeiger auf int64 und ein bool lassen sich immer
		// schreiben.
		panic(err)
	}
	sum := sha256.Sum256(b)
	return writeKey{op: in.Op, fingerprint: hex.EncodeToString(sum[:])}
}

// checkRequestID prüft die Form eines angegebenen Schlüssels; leer heißt
// ohne.
func checkRequestID(id string) error {
	if id == "" {
		return nil
	}
	if err := contract.CheckRequestID(id); err != nil {
		return invalidError{err}
	}
	return nil
}

// storedRequest ist ein gemerkter Vorgang.
type storedRequest struct {
	op, fingerprint string
	revision        int64
	ids             []string
}

// expireRequests entfernt alle Einträge, deren Frist vor now abgelaufen
// ist (contract.RequestIDTTL) — bei jedem Schreibvorgang über einen Node, für
// alle Accounts.
func expireRequests(ctx context.Context, tx sqlitedb.Querier, now int64) error {
	cutoff := now - contract.RequestIDTTL.Milliseconds()
	if _, err := tx.ExecContext(ctx, q(queries.RequestsExpire), cutoff); err != nil {
		return fmt.Errorf("abgelaufene Schlüssel entfernen: %w", err)
	}
	return nil
}

// lookupRequest liest den gemerkten Vorgang eines Schlüssels; found false,
// wenn es keinen gibt.
func lookupRequest(ctx context.Context, tx sqlitedb.Querier, account, requestID string) (storedRequest, bool, error) {
	var r storedRequest
	var ids string
	err := tx.QueryRowContext(ctx, q(queries.RequestGet), account, requestID).Scan(&r.op, &r.fingerprint, &r.revision, &ids)
	if errors.Is(err, sql.ErrNoRows) {
		return storedRequest{}, false, nil
	}
	if err != nil {
		return storedRequest{}, false, fmt.Errorf("Schlüssel %s nachschlagen: %w", requestID, err)
	}
	if err := json.Unmarshal([]byte(ids), &r.ids); err != nil {
		return storedRequest{}, false, fmt.Errorf("Schlüssel %s: ids %q: %w", requestID, ids, err)
	}
	return r, true, nil
}

// replay ist die Antwort auf einen Schlüssel, der schon gemerkt ist: mit
// demselben Fingerabdruck eine Wiederholung ohne Zeilen, sonst ungültig.
func replay(prev storedRequest, key writeKey, requestID string) (WriteResult, error) {
	if prev.fingerprint != key.fingerprint {
		return WriteResult{}, invalidError{fmt.Errorf(
			"request_id %s gehört schon zu einem anderen Vorgang (%s mit anderen Angaben); je Aufruf ein eigener Schlüssel",
			requestID, prev.op)}
	}
	ids := prev.ids
	if ids == nil {
		ids = []string{}
	}
	return WriteResult{Revision: prev.revision, Rows: []contract.Row{}, Replayed: true, IDs: ids}, nil
}

// rememberRequest trägt einen gelungenen Vorgang unter seinem Schlüssel ein.
func rememberRequest(ctx context.Context, tx sqlitedb.Querier, account, requestID string, key writeKey, res WriteResult, now int64) error {
	ids := make([]string, len(res.Rows))
	for i, r := range res.Rows {
		ids[i] = r.ID
	}
	b, err := json.Marshal(ids)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, q(queries.RequestInsert),
		account, requestID, key.op, key.fingerprint, res.Revision, string(b), now); err != nil {
		return fmt.Errorf("Schlüssel %s eintragen: %w", requestID, err)
	}
	return nil
}
