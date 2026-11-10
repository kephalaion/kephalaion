package contract

import "strings"

// Die Namen unter docs/ (Task 033, docs/indizierung.md): Kephalaion
// indiziert das Verzeichnis docs/ auf oberster Ebene einer Collection und
// schreibt dort selbst — beim Neubau (rebuild_docs) als SystemAuthor. Hub
// und Node erkennen die Dokumente des Systems an denselben Funktionen.

// DocsDir ist das Verzeichnis, das Kephalaion indiziert: docs/ auf oberster
// Ebene einer Collection, ohne / am Ende. Ein docs tiefer in der Collection
// ist ein gewöhnliches Verzeichnis.
const DocsDir = "docs"

// IndexName ist der Name der index.md je Verzeichnis unter docs/ (Google OKF
// v0.2, §8), genau so geschrieben.
const IndexName = "index.md"

// ReadmeName ist der Name der README.md, die ein Verzeichnis beschreibt.
const ReadmeName = "README.md"

// SystemAuthor ist der Urheber (created_by/updated_by) der Dokumente, die der
// Hub selbst schreibt: fetch unter defaults/ und rebuild_docs unter docs/.
// Kein User kann so heißen — die Namensregel verbietet den Präfix system.
const SystemAuthor = "system"

// DocsReadme ist die Anleitung oben in docs/: eine erzeugte Datei.
const DocsReadme = DocsDir + "/" + ReadmeName

// InDocs sagt, ob ein Dokumentname unter docs/ liegt (docs/x.md,
// docs/a/b.md) — nicht docs selbst.
func InDocs(name string) bool { return strings.HasPrefix(name, DocsDir+"/") }

// IsGenerated sagt, ob ein Dokumentname eine erzeugte Datei bezeichnet:
// docs/README.md oder eine index.md in docs/ oder darunter. Nur das System
// schreibt sie (Code generated_file); erkannt wird allein am Namen, auch an
// einer index.md, die es vor Task 033 schon gab.
func IsGenerated(name string) bool {
	if name == DocsReadme {
		return true
	}
	return InDocs(name) && (name == DocsDir+"/"+IndexName || strings.HasSuffix(name, "/"+IndexName))
}

// IsNoticeName sagt, ob ein Dokumentname der einer Mahnung sein kann: eine
// README.md unterhalb von docs/, nicht docs/README.md.
func IsNoticeName(name string) bool {
	return InDocs(name) && name != DocsReadme && strings.HasSuffix(name, "/"+ReadmeName)
}

// IsNotice sagt, ob ein lebendes Dokument eine Mahnung ist: eine README.md
// unterhalb von docs/ (IsNoticeName), die zuletzt SystemAuthor geschrieben
// hat (updatedBy). Erkannt an Name und Urheber, nicht am Frontmatter — das
// ließe sich fälschen, den Urheber setzt allein der Hub. Eine Mahnung
// ersetzt jeder mit write (Rights.MayTakeOver); danach trägt sie den User
// und ist eine gewöhnliche README.md.
func IsNotice(name, updatedBy string) bool {
	return updatedBy == SystemAuthor && IsNoticeName(name)
}

// IsSystemDoc sagt, ob ein lebendes Dokument dem System unter docs/ gehört:
// eine erzeugte Datei oder eine Mahnung. Ein Verzeichnisvorgang nimmt solche
// Dokumente mit, statt an ihnen zu scheitern, und rename trägt sie nicht
// unter einen neuen Namen.
func IsSystemDoc(name, updatedBy string) bool {
	return IsGenerated(name) || IsNotice(name, updatedBy)
}
