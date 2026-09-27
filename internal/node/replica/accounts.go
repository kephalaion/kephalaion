package replica

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/kephalaion/kephalaion/internal/contract"
	"github.com/kephalaion/kephalaion/internal/ident"
	"github.com/kephalaion/kephalaion/internal/node/store"
	"github.com/kephalaion/kephalaion/internal/sqlitedb"
)

// qAccountRows liest die lebenden Zeilen eines Accounts, nach Collection. Die
// Bedingung name LIKE 'SYSTEM:%' steht wörtlich wie im Teilindex
// documents_system, damit SQLite ihn benutzt; genau grenzt name = ? ein.
const qAccountRows = `SELECT ` + documentColumns + ` FROM documents
	WHERE name = ? AND name LIKE 'SYSTEM:%' AND deleted = 0
	ORDER BY collection, revision DESC, id DESC`

// AccountRows liest die lebenden Zeilen SYSTEM:A:<account> der Replica, je
// Collection die jüngste, nach Collection — über den Index documents_system,
// ohne Transaktion und ohne Cache: Die Datenbank ist die einzige Wahrheit.
func (r *Replica) AccountRows(ctx context.Context, account string) ([]Document, error) {
	rows, err := r.db.QueryContext(ctx, qAccountRows, contract.AccountRowName(account))
	if err != nil {
		return nil, fmt.Errorf("Account-Zeilen lesen: %w", err)
	}
	defer rows.Close()
	out := []Document{}
	for rows.Next() {
		d, err := scanDocument(rows)
		if err != nil {
			return nil, fmt.Errorf("Account-Zeilen lesen: %w", err)
		}
		if n := len(out); n > 0 && out[n-1].Collection == d.Collection {
			continue
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// qAllAccountRows liest die lebenden Zeilen aller Accounts, nach Name und
// Collection; die Bedingungen stehen wie in qAccountRows, damit SQLite den
// Teilindex documents_system benutzt.
const qAllAccountRows = `SELECT ` + documentColumns + ` FROM documents
	WHERE name LIKE 'SYSTEM:%' AND substr(name, 1, 9) = 'SYSTEM:A:' AND deleted = 0
	ORDER BY name, collection, revision DESC, id DESC`

// AllAccountRows liest die lebenden Zeilen SYSTEM:A: aller Accounts, je
// Account und Collection die jüngste, nach Name und Collection.
func (r *Replica) AllAccountRows(ctx context.Context) ([]Document, error) {
	rows, err := r.db.QueryContext(ctx, qAllAccountRows)
	if err != nil {
		return nil, fmt.Errorf("Account-Zeilen lesen: %w", err)
	}
	defer rows.Close()
	out := []Document{}
	for rows.Next() {
		d, err := scanDocument(rows)
		if err != nil {
			return nil, fmt.Errorf("Account-Zeilen lesen: %w", err)
		}
		if n := len(out); n > 0 && out[n-1].Name == d.Name && out[n-1].Collection == d.Collection {
			continue
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// AdoptHubID übernimmt die hub_id, die ein Hub genannt hat (whoami): Weicht
// sie von der Replica ab, wird die Replica geleert (Regel aus docs/vertrag.md,
// „hub_id“) und reset sagt warum; danach folgt die Kopie in node.db. Fehlt
// die Replica, wird nur die Kopie geschrieben — anlegen tut sie der Abgleich.
func AdoptHubID(ctx context.Context, nodes store.Store, h store.Hub, hubID string) (reset string, err error) {
	rep, reason, err := openForSync(ctx, nodes, h)
	if err != nil {
		return "", err
	}
	reset = reason
	if rep != nil {
		defer rep.Close()
		if rep.HubID() != hubID {
			why := fmt.Sprintf("hub_id gewechselt (%s → %s); Replica geleert, der nächste Abgleich beginnt von vorn",
				rep.HubID(), hubID)
			if err := rep.reset(ctx, hubID); err != nil {
				return "", err
			}
			reset = why
		}
	}
	if h.HubID != hubID {
		if err := nodes.SetHubID(ctx, h.Name, h.EntryID, hubID); err != nil {
			return reset, err
		}
	}
	return reset, nil
}

// Zeilen aus der Antwort eines Vorgangs, der schreibt, übernimmt der Node in
// die Replica, bevor er antwortet: nach rotate die Account-Zeilen
// (WriteAccountRows), nach create, write und delete die Dokumente
// (WriteRows). Beide schreiben wie eine Seite des Abgleichs: nur Zeilen der
// gewünschten Collections, per id und nie durch eine ältere Revision, in einer
// Transaktion, die zuerst entry_id und hub_id prüft (apply). Der Stand des
// Abgleichs (sync_state) bleibt: Der nächste Abgleich liefert die Zeilen noch
// einmal, per id ersetzt — und changes, das bis sync_state liest, meldet sie
// erst danach.

// wantedRows sind die Zeilen aus Collections, die der Eintrag will, und ihre
// Collections in der Reihenfolge der Zeilen.
func wantedRows(h store.Hub, rows []contract.Row) (keep []contract.Row, collections []string) {
	for _, row := range rows {
		if slices.Contains(h.Collections, row.Collection) {
			keep = append(keep, row)
			if !slices.Contains(collections, row.Collection) {
				collections = append(collections, row.Collection)
			}
		}
	}
	return keep, collections
}

// putRows schreibt Zeilen aus einer Antwort des Hubs wie eine Seite ohne
// Stand; known siehe page.
func (r *Replica) putRows(ctx context.Context, rows []contract.Row, known bool) error {
	_, err := r.apply(ctx, page{rows: rows, advance: map[string]int64{}, known: known})
	return err
}

// WriteAccountRows schreibt die Zeilen, die ein rotate geliefert hat, in die
// Replica eines Hub-Eintrags — nur die der gewünschten Collections. Fehlt die
// Replica, legt es sie an; nennt sie eine andere hub_id, wird sie zuerst
// geleert (wie beim Abgleich). written sind die Collections, deren Zeile
// geschrieben wurde.
func WriteAccountRows(ctx context.Context, nodes store.Store, h store.Hub, hubID string, rows []contract.Row) (written []string, reset string, err error) {
	for _, row := range rows {
		if _, ok := contract.AccountOfRow(row.Name); !ok {
			return nil, "", fmt.Errorf("der Hub liefert %q, keine Account-Zeile", row.Name)
		}
	}
	keep, written := wantedRows(h, rows)
	if len(keep) == 0 {
		return nil, "", nil
	}
	path := nodes.ReplicaPath(h.Name)
	rep, reason, err := openForSync(ctx, nodes, h)
	if err != nil {
		return nil, "", err
	}
	reset = reason
	if rep == nil {
		if rep, err = Create(ctx, path, hubID, h.EntryID); err != nil {
			return nil, reset, err
		}
	} else if rep.HubID() != hubID {
		reset = fmt.Sprintf("hub_id gewechselt (%s → %s); Replica geleert, der nächste Abgleich beginnt von vorn",
			rep.HubID(), hubID)
		if err := rep.reset(ctx, hubID); err != nil {
			_ = rep.Close()
			return nil, "", err
		}
	}
	defer rep.Close()
	if err := rep.putRows(ctx, keep, false); err != nil {
		return nil, reset, err
	}
	if h.HubID != hubID {
		if err := nodes.SetHubID(ctx, h.Name, h.EntryID, hubID); err != nil {
			return written, reset, err
		}
	}
	return written, reset, nil
}

// WriteRows schreibt die Zeilen, die ein Schreibvorgang (create, write,
// delete) geliefert hat, in die Replica eines Hub-Eintrags — so liefert read
// die eigene Änderung sofort, und ein zweites Speichern beruht auf der neuen
// Revision. Anders als WriteAccountRows legt es keine Replica an und leert
// keine, und es schreibt nur in Collections, die die Replica schon führt
// (Stand in sync_state). Fehlt die Replica, gehört sie zu einem anderen
// Eintrag oder nennt sie eine andere hub_id, ist nichts geschrieben und der
// Fehler ErrChanged: Der Abgleich holt nach. Nur Dokumente — eine Zeile, deren
// Name kein gültiger Dokumentname ist (etwa SYSTEM:), lehnt es ab.
func WriteRows(ctx context.Context, nodes store.Store, h store.Hub, hubID string, rows []contract.Row) error {
	for _, row := range rows {
		if err := ident.CheckDocName(row.Name); err != nil {
			return fmt.Errorf("der Hub liefert %q, kein Dokument: %w", row.Name, err)
		}
	}
	keep, _ := wantedRows(h, rows)
	if len(keep) == 0 {
		return nil
	}
	rep, err := Open(ctx, nodes.ReplicaPath(h.Name))
	if errors.Is(err, sqlitedb.ErrNotFound) {
		return fmt.Errorf("Hub %s: keine Replica: %w", h.Name, ErrChanged)
	}
	if err != nil {
		return err
	}
	defer rep.Close()
	switch {
	case rep.EntryID() != h.EntryID:
		return fmt.Errorf("Hub %s: die Replica gehört zu einem anderen Eintrag: %w", h.Name, ErrChanged)
	case rep.HubID() != hubID:
		return fmt.Errorf("Hub %s: die Replica gehört zu hub_id %s, die Antwort zu %s: %w", h.Name, rep.HubID(), hubID,
			ErrChanged)
	}
	return rep.putRows(ctx, keep, true)
}
