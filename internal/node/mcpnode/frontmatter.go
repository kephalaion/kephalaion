package mcpnode

import (
	"context"
	"strings"

	"github.com/kephalaion/kephalaion/internal/frontmatter"
	"github.com/kephalaion/kephalaion/internal/node/replica"
)

// Frontmatter in list und read: Mit dem Parameter frontmatter trägt jeder
// passende Eintrag das Feld frontmatter (JSON-Objekt) oder frontmatter_error
// (kurzer Grund). Passend sind Dokumente, deren Name auf .md endet,
// Verzeichnisse über <verzeichnis>/README.md und Collections über README.md
// auf ihrer obersten Ebene. Gelesen werden nur die ersten frontmatter.MaxBytes
// des Inhalts, nur für die Einträge der Seite; ein fehlerhaftes Frontmatter
// macht nichts unlesbar.

// readmeName ist die Datei, deren Frontmatter ein Verzeichnis oder eine
// Collection beschreibt.
const readmeName = "README.md"

// frontmatterFields liest das Frontmatter aus dem Anfang eines Inhalts: das
// Objekt oder der Grund, warum es sich nicht lesen ließ; beides leer ohne
// Frontmatter.
func frontmatterFields(head []byte) (fm any, errText string) {
	m, ok, err := frontmatter.Parse(head)
	switch {
	case err != nil:
		return nil, err.Error()
	case !ok:
		return nil, ""
	}
	return m, ""
}

// documentFrontmatter liest das Frontmatter eines Dokuments per id; ein
// Dokument, dessen Name nicht auf .md endet oder das inzwischen fehlt, hat
// keines. content ist der volle Inhalt, wenn er schon gelesen ist — dann
// fragt es die Replica nicht noch einmal.
func documentFrontmatter(ctx context.Context, rep *replica.Replica, name, id string, content *string) (fm any, errText string, err error) {
	if !frontmatter.IsMarkdown(name) {
		return nil, "", nil
	}
	if content != nil {
		fm, errText = frontmatterFields([]byte(*content))
		return fm, errText, nil
	}
	head, ok, err := rep.HeadByID(ctx, id, frontmatter.MaxBytes)
	if err != nil || !ok {
		return nil, "", err
	}
	fm, errText = frontmatterFields(head)
	return fm, errText, nil
}

// readmeFrontmatter liest das Frontmatter der README.md eines Verzeichnisses
// (prefix mit '/' am Ende) oder der Wurzel einer Collection (prefix leer);
// ohne README.md keines.
func readmeFrontmatter(ctx context.Context, rep *replica.Replica, collection, prefix string) (fm any, errText string, err error) {
	head, ok, err := rep.HeadByName(ctx, collection, prefix+readmeName, frontmatter.MaxBytes)
	if err != nil || !ok {
		return nil, "", err
	}
	fm, errText = frontmatterFields(head)
	return fm, errText, nil
}

// fillFrontmatter trägt das Frontmatter in die Einträge einer Seite ein, die
// zum Hub von a gehören: Dokumente per id, Verzeichnisse über die README.md
// in der Collection t, Collections über ihre README.md. Einträge anderer Hubs
// (Adresse) bleiben unberührt.
func fillFrontmatter(ctx context.Context, a *hubAccess, t target, entries []ListEntry) error {
	for i := range entries {
		e := &entries[i]
		var err error
		switch e.Kind {
		case KindDocument:
			e.Frontmatter, e.FrontmatterError, err = documentFrontmatter(ctx, a.rep, e.Name, e.ID, nil)
		case KindDirectory:
			e.Frontmatter, e.FrontmatterError, err = readmeFrontmatter(ctx, a.rep, t.Collection, e.Name+"/")
		case KindCollection:
			if hub, _, _ := strings.Cut(e.Address, ":"); hub != a.hub {
				continue
			}
			e.Frontmatter, e.FrontmatterError, err = readmeFrontmatter(ctx, a.rep, e.Name, "")
		}
		if err != nil {
			return err
		}
	}
	return nil
}
