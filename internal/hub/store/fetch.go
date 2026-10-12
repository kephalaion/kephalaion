package store

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/kephalaion/kephalaion/internal/hub/source"
)

// Holen (Task 031): die Seite des Stores, source.Target. Das Netz bleibt
// draußen — source.Fetch liest den Tag und lädt das Archiv, bevor der Store
// eine Transaktion beginnt.

var _ source.Target = (*sqliteStore)(nil)

func (s *sqliteStore) FetchedTag(ctx context.Context, collection string) (string, error) {
	c, err := getCollection(ctx, s.db, collection)
	if err != nil {
		return "", err
	}
	return c.FetchedTag, nil
}

// ReplaceDefaults ersetzt defaults/ einer Collection mit Quelle durch files
// (Namen relativ zu defaults/), in einer Transaktion:
//
//  1. Die erste Anweisung sperrt die Zeile der Collection (CollectionLock);
//     gibt es sie nicht, ErrNotFound.
//  2. Der Tag wird erneut gelesen: Hat ihn inzwischen ein anderer Lauf
//     geholt, ist nichts zu tun (Result.Current).
//  3. Unter defaults/ wird gelöscht, was in files fehlt, ersetzt, was
//     abweicht, angelegt, was neu ist — alles unter einer Revision, Urheber
//     source.Author, je Dokument eine Zeile in actions mit by.Account und
//     by.Carrier. Gleiches bleibt unberührt.
//  4. Tag und Zeitpunkt stehen danach in collections, dazu eine Zeile
//     collection.fetch in actions.
//
// Nur für eine Collection aus der Liste der Quellen; die Namen und Inhalte
// prüft es wie PutDocument.
func (s *sqliteStore) ReplaceDefaults(ctx context.Context, collection, tag string, files []source.File, by source.Actor) (source.Result, error) {
	if _, ok := source.Lookup(collection); !ok {
		return source.Result{}, fmt.Errorf("Collection %s hat keine Quelle", collection)
	}
	if tag == "" {
		return source.Result{}, errors.New("fetch: Tag fehlt")
	}
	if by.Account == "" {
		by.Account = Admin
	}
	want := make(map[string]string, len(files))
	for _, f := range files {
		in := DocumentInput{Name: source.DefaultsDir + "/" + f.Name, Content: f.Content}
		if err := checkDocumentInput(in); err != nil {
			return source.Result{}, err
		}
		if _, dup := want[in.Name]; dup {
			return source.Result{}, fmt.Errorf("Dokument %s steht zweimal im Archiv", in.Name)
		}
		want[in.Name] = in.Content
	}
	sqlTx, tx, err := s.begin(ctx)
	if err != nil {
		return source.Result{}, err
	}
	defer func() { _ = sqlTx.Rollback() }()
	res, err := tx.ExecContext(ctx, q(queries.CollectionLock), collection)
	if err := mustAffect(res, err, "Collection "+collection); err != nil {
		return source.Result{}, err
	}
	c, err := getCollection(ctx, tx, collection)
	if err != nil {
		return source.Result{}, err
	}
	if c.FetchedTag == tag {
		return source.Result{Current: true, Tag: tag}, nil
	}
	w := newDocTx(tx, source.Author, by.Account, by.Carrier)
	out := source.Result{Tag: tag}
	cur, err := liveDocuments(ctx, tx, collection, source.DefaultsDir+"/")
	if err != nil {
		return source.Result{}, err
	}
	have := make(map[string]bool, len(cur))
	// Erst löschen, dann ersetzen, zuletzt anlegen: Ein Name, der vom
	// Verzeichnis zur Datei wird (oder umgekehrt), ist dann frei.
	for _, d := range cur {
		if _, keep := want[d.Name]; keep {
			have[d.Name] = true
			continue
		}
		if _, err := w.delete(ctx, d); err != nil {
			return source.Result{}, err
		}
		out.Deleted++
	}
	for _, d := range cur {
		if !have[d.Name] || want[d.Name] == d.Content {
			continue
		}
		if _, err := w.replace(ctx, d, want[d.Name]); err != nil {
			return source.Result{}, err
		}
		out.Updated++
	}
	var created []string
	for name := range want {
		if !have[name] {
			created = append(created, name)
		}
	}
	sort.Strings(created)
	for _, name := range created {
		if _, err := w.create(ctx, collection, DocumentInput{Name: name, Content: want[name]}); err != nil {
			return source.Result{}, err
		}
		out.Created++
	}
	out.Revision = w.rev.rev
	if _, err := tx.ExecContext(ctx, q(queries.CollectionSetFetched), collection, tag, w.now); err != nil {
		return source.Result{}, fmt.Errorf("geholten Stand von %s schreiben: %w", collection, err)
	}
	subject := collection + " " + strings.TrimSpace(tag)
	if err := logActionFull(ctx, tx, w.now, by.Account, by.Carrier, "collection.fetch", subject, w.rev.rev); err != nil {
		return source.Result{}, err
	}
	if err := sqlTx.Commit(); err != nil {
		return source.Result{}, err
	}
	return out, nil
}
