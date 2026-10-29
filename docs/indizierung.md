---
title: Kephalaion — Indizierung von docs/
description: Grundidee vom 2026-10-16, in Besprechung — Kephalaion indiziert das Verzeichnis docs/ einer Collection für den Zugriff der KI über MCP, Volltext mit FTS5 im Node und Navigation über eine index.md je Verzeichnis nach Google OKF v0.2, dazu eine erzeugte README.md als Anleitung; was entschieden ist, was OKF festlegt und offen lässt, offene Fragen.
---

# Indizierung von `docs/`

**Stand: entschieden bis auf Einzelheiten, Task 033 angelegt** (2026-10-16,
`k-playbook-local/tasks/033-docs-index-und-suche.md`); gebaut ist nichts. Was das Konzept betrifft, wandert mit seinem Datum nach
[`konzept.md`](konzept.md) („Indizierung“). Begriffe nach [`begriffe.md`](begriffe.md).

## Das Wichtigste

- **Maßstab ist der Zugriff der KI über MCP.** Alles hier dient dazu, dass eine KI über die
  Werkzeuge des Nodes schnell findet, was in `docs/` liegt.
- **Kephalaion indiziert, nicht k-playbook.** Gegenstand ist das Verzeichnis `docs/` einer
  Collection, der zentrale Ort für Doku. Was außerhalb liegt, indiziert Kephalaion nicht.
- **Zwei Arten Index:** Volltext (FTS5 im Node, Stufe 1 — entschieden) und Navigation
  (`index.md` je Verzeichnis).
- **An Google OKF v0.2 halten, wo es sinnvoll ist.** Was wir übernehmen und wo wir abweichen,
  ist zu besprechen.
- **`index.md` je Verzeichnis ist der Index**, erzeugt vom System (Autor `system`), schreibgeschützt,
  bei jedem Neubau einfach überschrieben.
- **`docs/README.md` ist die Anleitung**, ebenfalls erzeugt und schreibgeschützt: wie `docs/`
  funktioniert, welche Indizes es gibt, dass `index.md` der Index ist und dass hier nichts von
  Hand geschrieben wird.
- **Erzeugte Dateien schreibt niemand außer dem System**, auch der Admin nicht.
- **In jedem Unterverzeichnis eine beschreibbare `README.md`** mit der Beschreibung des
  Verzeichnisses. Fehlt sie, legt das System eine an, die das Fehlen anmahnt.
- **Eine feste Struktur unter `docs/`** legt Kephalaion fest und legt fehlende Verzeichnisse
  selbst an; die Liste ist noch zu klären.
- **Finden über `search`**, Treffer je Abschnitt mit Name, Anker, Titel, Beschreibung und Auszug —
  das ist das zentrale Ergebnis, siehe „Suche über MCP“.
- **Ein Befehl stößt den Neubau an**, in der CLI und als MCP-Werkzeug; vorerst darf das jeder.
  Der automatische Neubau bei jeder Änderung ist zurückgestellt.
- **`/k-docs-index` entfällt** in k-playbook samt Stichwort-Index A–Z und „Frage → Datei“.

## Entschieden am 2026-10-16

- **Zugriff der KI über MCP ist das Kriterium** für Aufbau und Inhalt beider Dateien.
- **`docs/` ist der zentrale Ort.** Wie k-playbook Doku außerhalb von `docs/` findet oder
  einbezieht, ist eine Frage von k-playbook und gehört nicht hierher.
- **`index.md`:** je Verzeichnis unter `docs/`, vom System erzeugt, schreibgeschützt, beim
  Neubau überschrieben — eine von Hand geschriebene gibt es nicht.
- **`docs/README.md`:** auf oberster Ebene von `docs/`, vom System erzeugt, schreibgeschützt;
  Anleitung, kein Index. Inhalt: wie `docs/` funktioniert, welche Indizes es gibt (Volltext
  über `search`, Navigation über `index.md`), dass `index.md` der Index ist und dass in den
  erzeugten Dateien nichts von Hand geschrieben wird. Kephalaion schreibt sie selbst aus einer
  Vorlage im Binary — für alle Installationen gleich.
- **Sprache: Englisch als Standard**, nicht fest. Was „Sprache“ alles umfasst (erzeugte Texte,
  Zerlegung für die Suche) und wo sie abweichend eingestellt wird, ist offen.
- **Neubau per Befehl**, CLI und MCP, vorerst für jeden Account. Wann das System von selbst neu
  baut, ist zurückgestellt; bis dahin genügt der Befehl. Er nimmt die Collection als Argument
  und gilt **nur für `docs/`** — ein Neubau von `docs/`; andere Neubauten kommen später als
  eigene.
- **`search` kommt mit**, nicht in einem eigenen Schritt danach. Aufbau der `index.md` und Namen
  der Felder von `search` werden vorher festgelegt.
- **`/k-docs-index` fällt weg.** k-playbook schreibt keinen Index mehr in die Collection.
- **Schreibschutz der erzeugten Dateien** (`docs/README.md`, jede `index.md` unter `docs/`):
  vorerst einfach, später schöner. Niemand schreibt sie außer dem System — kein Account über
  einen Node und auch nicht der Admin. Siehe „Schreibschutz — was es schon gibt“.
- **`README.md` nur oben fest.** Allein `docs/README.md` ist erzeugt und gesperrt. In jedem
  Verzeichnis darunter ist `README.md` beschreibbar und trägt die Beschreibung des Verzeichnisses
  — im Frontmatter (`description`), woraus sie heute schon `list` liefert (Task 015) und künftig
  der Eintrag in der übergeordneten `index.md`.
- **Fehlt eine `README.md`** in einem Verzeichnis unter `docs/`, legt das System eine an, die das
  Fehlen anmahnt.
- **Feste Verzeichnisse:** Kephalaion legt eine Struktur unter `docs/` fest und fehlende
  Verzeichnisse selbst an — Kephalaion ergänzt dort später einiges. Vorerst nur `manual/` und
  `generated/`, nur diese erste Ebene; was darunter liegt, überlässt Kephalaion den Nutzern wie
  k-playbook. Siehe „Struktur unter `docs/`“.
- **Drei Fragen, drei Wege:** Übersicht über `index.md`, Finden über `search` mit einem
  maschinellen, schnellen Index, Neues über `changes` — siehe „Suche über MCP“.
- **Schreibweise wie OKF:** `index.md` klein.

## Struktur unter `docs/`

**Entschieden am 2026-10-16: vorerst nur `manual/` und `generated/`**, alles Weitere später.

```
docs/
  README.md        Anleitung                      erzeugt, gesperrt
  index.md         Index der Ebene, okf_version   erzeugt, gesperrt
  manual/          von Hand geschrieben
    README.md      Beschreibung                   beschreibbar, sonst Mahnung
    index.md                                      erzeugt, gesperrt
  generated/       von Werkzeugen erzeugt (bei k-playbook code/, libs/, versions/)
    README.md      Beschreibung                   beschreibbar, sonst Mahnung
    index.md                                      erzeugt, gesperrt
```

Zurückgestellt aus dem Layout-Entwurf von k-playbook vom 2026-10-09: `extracted/`, `findings/`,
`pitfalls/`. `queue/` und `user/<name>/` sind keine Doku und bleiben außerhalb. Zu klären:

- wo die Liste steht — naheliegend wie die Liste der Quellen als YAML im Binary
  (`internal/hub/source/sources.yaml`), mit Beschreibung je Verzeichnis, aus der die Mahnung und
  `docs/README.md` ihren Text nehmen;
- was Kephalaion später selbst ergänzt und wo es liegt.

## Aufbau der `index.md` (entschieden am 2026-10-16)

So knapp wie möglich und ausreichend genau. Beispiel `docs/generated/index.md`:

```markdown
# Directories

* [Code](code/) - Documentation generated from the source code, one document per package.
* [libs](libs/) - *no description*

# Documents

* [Version inventory](inventory.md) - Versions of all tools and libraries, from declarative sources.
* [notes.md](notes.md) - *no description*
```

- **Eine Ebene.** Ein Unterverzeichnis ist ein Eintrag mit Link auf `sub/`; tiefer geht es über
  dessen `index.md`.
- **Links relativ, Wurzel des OKF-Bundles ist `docs/`.** Bei einer Ebene heißt ein Link immer
  `x.md` oder `sub/`, nie `../` — der Name für `read` ist das Verzeichnis der `index.md` plus
  Link. Relative Links gehen auch in der Markdown-Vorschau von VS Code (ein `/`-Link zeigte dort
  auf `keph://<hub>/docs/…`, ohne Collection). `docs/index.md` trägt als einzige Frontmatter,
  nur `okf_version: "0.2"`.
- **Zwei Abschnitte:** `# Directories`, dann `# Documents`; ein leerer entfällt. Keine Gruppierung
  nach `type`.
- **Reihenfolge nach Name** in jedem Abschnitt — stabil, nummerierte Namen bleiben in ihrer Folge.
- **Titel:** Frontmatter `title`, sonst die erste `#`-Überschrift, sonst der Dateiname. Bei einem
  Verzeichnis aus dem Frontmatter seiner `README.md`, sonst der Name des Verzeichnisses.
- **Beschreibung:** Frontmatter `description`; fehlt sie oder liegt beim Verzeichnis noch die
  Mahnung, steht `*no description*`. Kein Ersatz aus dem ersten Satz.
- **Keine Einträge** für `index.md` und `README.md` des Verzeichnisses selbst; die Beschreibung des
  Verzeichnisses steht im Eintrag darüber, die README findet `search`.
- **Andere Dateien als `.md`** erscheinen unter `Documents` mit dem Dateinamen, ohne Beschreibung.
- **Keine fremden Felder** (`type`, `tags`) im Eintrag; die liefert `list` mit `frontmatter`.
- **Größe:** Wird ein Verzeichnis so groß, dass seine `index.md` an 1 MiB stößt (grob ab 8000
  Einträgen), achtet der schreibende Prozess darauf; Kephalaion prüft das vorerst nicht (Todo #22).

## Suche über MCP

**Der Kern dessen, was hier herauskommen muss.** Eine KI hat drei verschiedene Fragen, und jede
bekommt ihren eigenen Weg (2026-10-16):

| Frage | Weg | Werkzeug |
|---|---|---|
| „Was gibt es hier?“ | kurze Übersicht genau einer Ebene, je Eintrag Titel und ein Satz | `read` auf `index.md` |
| „Wo steht X?“ | maschineller, schneller Volltextindex, Treffer je Abschnitt | `search` (neu) |
| „Was ist neu?“ | Änderungen seit einem Stand oder Zeitpunkt | `changes` (gebaut, Task 009) |

**Im Index sucht die KI nicht.** Dafür müsste sie ihn erst ganz lesen und dann raten; er trägt
nur Titel und Beschreibungen, nicht den Inhalt. Ein großer Gesamtindex auf einmal kostet Kontext,
und das meiste braucht sie nicht. So arbeitet eine KI heute auf der Platte: Index lesen, `grep -n`
auf Überschriften, gezielt Zeilen lesen — über MCP: `index.md`, `search`, `read` mit Abschnitt.

### Was ein Treffer von `search` trägt

- **`name` und Abschnitt** (Überschrift samt Pfad der Überschriften darüber, dazu der Anker),
  unverändert an `read` weiterzugeben;
- **`title` und `description` des Dokuments**, damit die KI ohne Lesen entscheidet, ob es passt;
- **ein kurzer Auszug**, zwei bis drei Zeilen um den Treffer;
- **Rang ab 1, kein Punktwert** (wie im Konzept, „Suche über mehrere Collections“);
- **wenige Treffer** als Standard, etwa 10, mit `limit` mehr.

### Wie die Anfrage aussieht

- **Einfache Wörter**, keine FTS5-Syntax; gebeugte deutsche Wörter sollen trotzdem treffen.
- **Filter wahlweise:** `path` unter `docs/`, `type` und `tags` aus dem Frontmatter, die
  Collection(s) wie bei den anderen Werkzeugen.
- **Keine Treffer** sind `index.md`, `docs/README.md` und die Mahnung — sie verdrängten im
  Ranking die Dokumente, auf die sie zeigen.
- **Ohne Treffer** ein klares „nichts gefunden“ mit dem Hinweis auf `docs/index.md`, statt dünner
  Treffer.

Vorbild: `k_playbook_knowledge_search` liefert heute je Treffer `path`, `heading`, `excerpt`,
`rank`, `anchor` (k-playbook, `docs/mcp.md`).

### Der Index: FTS5 im Node (entschieden am 2026-10-16)

FTS5 ist die Volltextsuche, die SQLite mitbringt. Der Suchindex ist eine Tabelle in der Replica
des Nodes, neben den Dokumenten, und wird in derselben Transaktion nachgezogen, in der der
Abgleich die Zeilen schreibt — er passt immer zur Revision, ohne zweiten Speicher.

- **Zerlegung an den Überschriften:** Ein Abschnitt ist eine Überschrift samt Text bis zur
  nächsten. Indiziert wird der ganze Text, nicht nur die Überschriften.
- **Felder je Abschnitt**, gewichtet in `bm25()`: Titel des Dokuments und Überschriften darüber
  hoch, `description` und `tags` mittel, der Text normal. Der Auszug kommt aus `snippet()`.
- **Fassung der Zerlegung** in der Replica; ändert sie sich, baut der Node neu.
- **Sprache:** Englisch als Standard — FTS5 bringt dafür `porter` mit. Deutsch (Stemmen in Go,
  zweite Spalte) erst, wenn die Messung es verlangt.
- **Treiber:** `modernc.org/sqlite` (`go.mod`); dass FTS5 darin enthalten ist, ist noch zu prüfen.

Verworfen oder zurückgestellt:

- **Indexdatei vom Hub im Verzeichnis** (etwa JSON): sprengt 1 MiB und UTF-8-Text je Dokument,
  jeder Schreibvorgang erneuert sie und schickt sie an alle Nodes, Ranking und Sprache bräuchte
  trotzdem jeder Client. Clients ohne Node gibt es nach dem Konzept nicht.
- **Zerlegung am Hub, Node indiziert nur:** widerspricht „Die Zerlegung gibt es nur im Node“.
- **Bleve** (Volltextbibliothek in reinem Go, eingebettet; Analyzer je Sprache, unscharfe Suche,
  Facetten): Rückfall, wenn FTS5 nicht reicht. Kosten: eigener Index neben der Replica, ohne
  gemeinsame Transaktion, größeres Binary.
- **Semantische Suche** (Stufe 5): Einbettungen rechnet der Hub mit einem Modell; auch die Frage
  braucht eine vom selben Modell. Zusatz, kein Ersatz; zurückgestellt.

### `changes` — reicht es?

Gebaut in Task 009 ([`konzept.md`](konzept.md), „Allgemein — lesen“): seit einem Cursor oder
Zeitpunkt (`since`), wahlweise `collection` und `path` als Präfix; je geändertem Dokument einmal
Name, `id`, Revision, gelöscht ja/nein, geändert (wann, von wem); aus der Replica, ohne Netz.
„Was ist neu in `docs/` seit gestern“ geht damit heute schon in einem Aufruf. Es fehlt für die
KI nur, was `list` und `read` schon können: der Parameter **`frontmatter`**
(`internal/node/mcpnode/changes.go` kennt ihn nicht) — ohne ihn sieht sie nur Namen und muss
jedes Dokument lesen, um zu wissen, worum es geht. Bekannte Grenze: Wer ein Dokument aus `path`
heraus umbenennt, erscheint dort nicht mehr.

## Herkunft

Übergabe aus einer k-playbook-Sitzung am 2026-10-16. Was dort schon galt:

- Die Suche liegt vollständig bei Kephalaion; k-playbook gibt seine Suche (BM25 im Go-Prozess
  über `k-playbook-local/knowledge/`) schrittweise ab ([`konzept.md`](konzept.md), „Ein
  Programm, zwei Rollen“). Indiziert wird auf dem Node über die Replica, zerlegt nur im Node
  („Indizierung“). Stufe 1 ist nicht gebaut ([`fortschritt.md`](fortschritt.md), „Zu tun“).
- Der Abgleich zieht einen Index je Dokument nach — beim Start, alle 30 s, nach jedem eigenen
  Schreibvorgang („Abgleich“). Der Volltextindex braucht deshalb keinen Anstoß.
- Verzeichnisse und Collections tragen in `list` und `read` das Frontmatter ihrer `README.md`
  (Task 015, „Allgemein — lesen“).
- In k-playbook liefert die `README.md` an der Wurzel keine Abschnitte für die Suche, weil ein
  Stichwort-Index im Ranking die Dokumente verdrängte, auf die er zeigt — dasselbe gilt für
  `index.md` und die erzeugte `README.md`.
- k-playbooks Generatoren schreiben Frontmatter nach OKF seit 2026-07-29; `index.md` wurde
  damals bewusst nicht übernommen, `README.md` blieb Einstieg. Das dreht sich jetzt.

## Arten von Index

| Art | heute | Ziel | wer |
|---|---|---|---|
| Volltext mit Ranking | BM25 in k-playbook | FTS5 im Node, beim Abgleich | Kephalaion, Stufe 1 |
| Navigation (was liegt wo) | eine `README.md` über alles aus `/k-docs-index` | `index.md` je Verzeichnis (OKF) | Kephalaion |
| Stichwort A–Z, Frage → Datei | Teil der `README.md` | entfällt | — |
| Facetten `type`, `tags`, `status` | im Frontmatter, ungenutzt | `list` mit `frontmatter` liefert sie schon | Kephalaion |
| Semantisch | nicht gebaut | Stufe 5, zurückgestellt | Kephalaion |

## Was OKF festlegt

Open Knowledge Format, Version 0.2,
`github.com/GoogleCloudPlatform/open-knowledge-format`, `SPEC.md`, gelesen am 2026-10-16.

**`index.md` (§8):**

- reservierter Name, klein geschrieben, in jedem Verzeichnis erlaubt, auch an der Wurzel des
  Bundles; optional; kein Concept;
- „enumerates the directory's contents to support progressive disclosure“;
- **kein Frontmatter**, einzige Ausnahme `okf_version` in der `index.md` an der Wurzel (§12);
- Inhalt: „one or more sections, each grouping concepts under a heading“;
- Einträge SOLLEN die `description` aus dem Frontmatter des Ziels tragen;
- Erzeuger dürfen sie generieren, Konsumenten dürfen sie bilden, wenn sie fehlt.

Beispiel der Spezifikation:

```markdown
# Section / Group Heading

* [Title 1](relative-url-1) - short description of item 1
* [Title 2](relative-url-2) - short description of item 2

# Another Section

* [Subdirectory](subdir/) - short description of the subdirectory
```

**`log.md` (§9):** reserviert, auf jeder Ebene erlaubt; Änderungen dieser Ebene, nach Datum
`YYYY-MM-DD` gruppiert, neueste zuerst; Einträge sind Prosa, ein fettes Leitwort
(`**Update**`, `**Creation**`) ist Konvention.

**Concepts (§4.1):** alle anderen `.md`-Dateien. Pflicht nur `type`; empfohlen `title` (sonst
aus dem Dateinamen), `description` (ein Satz), `resource`, `tags`. Fremde Schlüssel erlaubt.

**Links:** relativ wie in Markdown oder mit `/` ab der Wurzel des Bundles — empfohlen, weil
stabil beim Verschieben innerhalb eines Unterverzeichnisses. Konsumenten müssen kaputte Links
hinnehmen.

## Was OKF offen lässt

- **Gruppierung:** welche Überschriften — nach `type`, nach Thema, eine einzige.
- **Reihenfolge** der Einträge.
- **Tiefe:** ob eine `index.md` nur ihre Ebene zeigt. Das Beispiel legt es nahe
  (Unterverzeichnis als ein Eintrag), der Text sagt es nicht.
- **Prosa:** weder erlaubt noch verboten; das Beispiel zeigt nur Überschriften und Listen.
- **Linkform im Index:** das Beispiel relativ, allgemein empfohlen `/`.
- **Eintrag ohne `description`**, Dokument ohne Frontmatter.

Die Übergabe nannte zwei Punkte als Vorgabe, die so nicht in `SPEC.md` stehen: „Prosa unter den
Überschriften erlaubt“ und „eine Ebene je Datei“.

## Abweichung: `README.md`

OKF kennt keine `README.md`; für OKF wäre sie ein Concept und bräuchte `type`. Wir nehmen sie
bewusst hinzu: `docs/README.md` als Anleitung, weil ein Mensch wie eine KI zuerst dort
nachsieht, darunter je Verzeichnis als dessen Beschreibung. Ob sie `type` trägt (etwa
`type: guide` oben, `type: directory` darunter), damit ein OKF-Leser nicht stolpert, ist offen.

## Schreibschutz — was es schon gibt

Zwei Sperren, an zwei Stellen:

- **Nach dem Namen, nur über einen Node.** Die Regel `contract.Rights.MayWrite`
  (`internal/contract/account.go:241`) sperrt `vendor` und alles direkt in `vendor/`
  (`DenyReserved`, `VendorOf` `account.go:168`). Der Hub-Store prüft sie bei jedem Vorgang eines
  Accounts (`internal/hub/store/write.go:242`), bei `rename` alten und neuen Namen; der Node meldet
  danach `writable`, VS Code zeigt die Datei schreibgeschützt. **Der Admin ist davon nicht
  betroffen:** `hub doc put|rm` und `hub import` laufen über `writeDocs`
  (`internal/hub/store/documents.go:202`) ohne `MayWrite` — er schreibt auch direkt in `vendor/`.
- **Die ganze Collection, auch für den Admin.** Eine Collection mit Quelle lehnt `writeDocs` ab
  (`documents.go:211`, `sourceLocked`); allein `fetch` schreibt dort, als `system`.

Für die erzeugten Dateien unter `docs/` heißt das: Die Sperre über den Node ist ein weiterer Fall
in `MayWrite` (eine Funktion wie `VendorOf`, die `docs/README.md` und `docs/**/index.md`
erkennt); für den Admin kommt eine Prüfung in `writeDocs` dazu, wie bei der Quelle. „So wie
`vendor/`“ allein reichte nicht, weil der Admin dort schreiben darf. Offen: ein Import, der eine
solche Datei enthält — ablehnen, übergehen oder als Wiederherstellung übernehmen.

## Unterverzeichnisse und ihre `README.md`

- **Beschreibung im Frontmatter:** `description` (ein Satz, wie OKF), dazu wahlweise `title`. Der
  Text darunter erklärt das Verzeichnis ausführlicher.
- **Volltext:** Eine von Hand geschriebene `README.md` ist eine gewöhnliche Beschreibung und kommt
  in den Volltextindex. Nicht hinein kommen `index.md`, `docs/README.md` und die Mahnung — sie
  verdrängten im Ranking die Dokumente, auf die sie zeigen (Erfahrung aus k-playbook).
- **Die Mahnung** trägt OKF-gemäß `generated: {by: kephalaion, at: …}` im Frontmatter; daran
  erkennen Index, Suche und Mensch, dass sie noch nicht ersetzt wurde. Wer sie ersetzt, nimmt das
  Feld heraus.
- **Urheber der Mahnung:** Legt sie das System an (`system`), ist sie für jeden Account fremd, und
  sie zu ersetzen bräuchte `supersede`. Zu klären: ob Dokumente von `system` mit `write` wie
  eigene gelten, oder nur die Mahnung.

## Offene Fragen

Bevor ein Task entsteht:

- **Mahnung ersetzen:** Sie gehört `system`, also bräuchte ein Mensch `supersede`. Vorschlag: eine
  Mahnung (erkennbar an `generated`) darf jeder mit `write` überschreiben.
- **Neubau-Befehl:** Name und Vorgang im Vertrag ([`vertrag.md`](vertrag.md)); was er alles
  tut — vorgeschlagen: fehlende Verzeichnisse und Mahnungen anlegen, `docs/README.md` aus der
  Vorlage, jede `index.md`.
- **Welche Collections:** jede mit `docs/` auf oberster Ebene, oder nur gekennzeichnete. Und ob
  der Befehl `docs/` in einer Collection anlegt, die noch keines hat.
- **`search` im Einzelnen:** Name der Felder, Standard und Höchstwert von `limit`, ob Treffer
  eines Dokuments zusammengefasst werden; Gewichte erst nach Messung.
- **`frontmatter` für `changes`:** ob es mitkommt.

Kleiner, kann im Task entschieden werden:

- **Sprache:** wofür sie gilt — Vorlage von `docs/README.md`, Mahnung, Überschriften der
  `index.md`, Zerlegung der Suche — und wo eine andere als Englisch eingestellt wird.
- **Import erzeugter Dateien:** ablehnen, übergehen oder als Wiederherstellung übernehmen.
- **`type` in `README.md`** für OKF-Leser.
- **Struktur:** wo die Liste steht.

Zurückgestellt:

- **Automatischer Neubau:** wann und wer — im Schreibvorgang des Hubs, danach, oder nie von
  selbst.
- **`log.md`:** Vorschlag, keine. „Was ist neu“ beantwortet `changes` genauer; eine `log.md` je
  Verzeichnis wäre eine gröbere Fassung davon und änderte sich bei jedem Schreibvorgang mit.
- **Deutsch in der Suche, Bleve, semantische Suche.**
