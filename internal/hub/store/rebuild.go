package store

import (
	"context"
	"fmt"
	"time"

	"github.com/kephalaion/kephalaion/internal/contract"
	"github.com/kephalaion/kephalaion/internal/hub/docsindex"
	"github.com/kephalaion/kephalaion/internal/hub/source"
)

// Neubau von docs/ (Task 033, docs/vertrag.md, „rebuild_docs“): Den
// Soll-Zustand bildet docsindex.Build aus den lebenden Dokumenten unter
// docs/; der Store vergleicht ihn mit dem Ist und schreibt den Unterschied
// als system.

// RebuildResult ist das Ergebnis eines Neubaus von docs/. NoDocs: die
// Collection hat kein docs/ auf oberster Ebene, nichts geschrieben. Sonst
// die Revision des Vorgangs — 0, wenn sich nichts geändert hat — und die
// Zahl der angelegten, ersetzten und gelöschten Dokumente.
type RebuildResult struct {
	NoDocs                    bool
	Revision                  int64
	Created, Updated, Deleted int
}

// RebuildDocs baut docs/ einer Collection neu, in einer Transaktion:
//
//  1. Die erste Anweisung sperrt die Zeile der Collection (CollectionLock,
//     wie fetch); gibt es sie nicht, ErrNotFound. Zwei Neubauten laufen so
//     nacheinander, der zweite sieht den Stand des ersten.
//  2. Eine Collection mit Quelle: ErrForbidden — dort schreibt allein
//     fetch.
//  3. Ohne lebendes Dokument unter docs/: NoDocs, nichts geschrieben.
//  4. Sonst der Unterschied zwischen docsindex.Build und den Dokumenten
//     des Systems unter docs/ (contract.IsSystemDoc): Erst löschen, was nicht
//     mehr sein soll (eine verwaiste index.md oder Mahnung), dann ersetzen,
//     was abweicht, zuletzt anlegen, was fehlt — alles unter einer Revision,
//     Urheber contract.SystemAuthor, je Dokument eine Zeile in actions mit
//     by.Account und by.Carrier. Gleiches bleibt unberührt.
//  5. Hat sich etwas geändert, eine Zeile collection.rebuild_docs in
//     actions für den ganzen Vorgang.
//
// Eine README.md des Users ersetzt der Neubau nie: Build legt eine Mahnung
// nur an, wo keine liegt. Auf einen Namen, der nach dem Löschen ein
// Verzeichnis bleibt (ein Bestand wie docs/index.md/x.md,
// contract.ReservedDirOf), schlägt Build nichts vor; das Anlegen scheitert
// so nicht an ErrPathConflict.
func (s *sqliteStore) RebuildDocs(ctx context.Context, collection string, by source.Actor) (RebuildResult, error) {
	if by.Account == "" {
		by.Account = Admin
	}
	sqlTx, tx, err := s.begin(ctx)
	if err != nil {
		return RebuildResult{}, err
	}
	defer func() { _ = sqlTx.Rollback() }()
	res, err := tx.ExecContext(ctx, q(queries.CollectionLock), collection)
	if err := mustAffect(res, err, "Collection "+collection); err != nil {
		return RebuildResult{}, err
	}
	if _, ok := source.Lookup(collection); ok {
		return RebuildResult{}, sourceLocked(collection, "docs/ neu bauen")
	}
	cur, err := liveDocuments(ctx, tx, collection, contract.DocsDir+"/")
	if err != nil {
		return RebuildResult{}, err
	}
	if len(cur) == 0 {
		return RebuildResult{NoDocs: true}, nil
	}
	w := newDocTx(tx, contract.SystemAuthor, by.Account, by.Carrier)
	in := make([]docsindex.Doc, len(cur))
	live := make(map[string]Document, len(cur))
	for i, d := range cur {
		in[i] = docsindex.Doc{Name: d.Name, Content: d.Content, UpdatedBy: d.UpdatedBy}
		live[d.Name] = d
	}
	want := map[string]string{}
	var order []string
	for _, f := range docsindex.Build(in, time.UnixMilli(w.now)) {
		want[f.Name] = f.Content
		order = append(order, f.Name)
	}
	var out RebuildResult
	for _, d := range cur {
		if _, keep := want[d.Name]; keep || !contract.IsSystemDoc(d.Name, d.UpdatedBy) {
			continue
		}
		if _, err := w.delete(ctx, d); err != nil {
			return RebuildResult{}, err
		}
		out.Deleted++
	}
	for _, name := range order {
		d, ok := live[name]
		if !ok || d.Content == want[name] {
			continue
		}
		if !contract.IsSystemDoc(d.Name, d.UpdatedBy) {
			// Build schlägt für ein Dokument des Users nichts vor; träte es
			// doch ein, bliebe das Dokument stehen.
			return RebuildResult{}, fmt.Errorf("Neubau von docs/ in %s: %s gehört nicht dem System", collection, name)
		}
		if _, err := w.replace(ctx, d, want[name]); err != nil {
			return RebuildResult{}, err
		}
		out.Updated++
	}
	for _, name := range order {
		if _, ok := live[name]; ok {
			continue
		}
		if _, err := w.create(ctx, collection, DocumentInput{Name: name, Content: want[name]}); err != nil {
			return RebuildResult{}, err
		}
		out.Created++
	}
	out.Revision = w.rev.rev
	if out.Revision > 0 {
		if err := logActionFull(ctx, tx, w.now, by.Account, by.Carrier, "collection.rebuild_docs", collection,
			out.Revision); err != nil {
			return RebuildResult{}, err
		}
	}
	if err := sqlTx.Commit(); err != nil {
		return RebuildResult{}, err
	}
	return out, nil
}
