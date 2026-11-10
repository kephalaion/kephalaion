---
title: Kephalaion — Indizierung von docs/
description: Entwurf vom 2026-10-16, entschieden bis 2026-11-10 und gebaut in Task 033 — Kephalaion indiziert das Verzeichnis docs/ einer Collection für den Zugriff der KI über MCP, Volltext mit FTS5 im Node und Navigation über eine index.md je Verzeichnis nach Google OKF v0.2, dazu eine erzeugte README.md als Anleitung, Mahnungen und Schreibschutz; was entschieden ist, was OKF festlegt und offen lässt, was zurückgestellt ist.
---

# Indizierung von `docs/`

**Stand: gebaut in Task 033, Etappen 1–7 am 2026-11-10, veröffentlicht in v0.4.0; Nachricht an
k-playbook offen** (2026-10-16,
`keph:/user/kamran/tasks/033-docs-index-und-suche.md`), nachgezogen am 2026-11-09 nach der
Übergabe aus k-playbook (siehe „Entschieden am 2026-11-09“) und am 2026-11-10 bei der Prüfung des
Tasks (siehe „Entschieden am 2026-11-10“). Was das Konzept betrifft, steht mit seinem Datum in
[`konzept.md`](konzept.md) („Indizierung“), der Vorgang `rebuild_docs` und der Code
`generated_file` in [`vertrag.md`](vertrag.md). Begriffe nach [`begriffe.md`](begriffe.md).

## Das Wichtigste

- **Maßstab ist der Zugriff der KI über MCP.** Alles hier dient dazu, dass eine KI über die
  Werkzeuge des Nodes schnell findet, was in `docs/` liegt.
- **Kephalaion indiziert, nicht k-playbook.** Gegenstand ist das Verzeichnis `docs/` einer
  Collection, der zentrale Ort für Doku. Was außerhalb liegt, indiziert Kephalaion nicht.
- **Zwei Arten Index:** Volltext (FTS5 im Node, Stufe 1 — entschieden) und Navigation
  (`index.md` je Verzeichnis).
- **An Google OKF v0.2 halten, wo es sinnvoll ist.** Was wir übernehmen und wo wir abweichen,
  ist zu besprechen.
- **`index.md` je Verzeichnis ist der Index**, erzeugt vom System (Urheber `system`), schreibgeschützt,
  bei jedem Neubau einfach überschrieben.
- **`docs/README.md` ist die Anleitung**, ebenfalls erzeugt und schreibgeschützt: wie `docs/`
  funktioniert, welche Indizes es gibt, dass `index.md` der Index ist und dass hier nichts von
  Hand geschrieben wird.
- **Erzeugte Dateien schreibt niemand außer dem System**, auch der Admin nicht: `docs/README.md`
  und jede `index.md` unter `docs/`. Eine Ablehnung trägt den Code `generated_file`. Unter `docs/`
  sind `index.md` und `README.md` nie ein Verzeichnis (siehe „Schreibschutz“).
- **In jedem Unterverzeichnis eine beschreibbare `README.md`** mit der Beschreibung des
  Verzeichnisses; k-playbook schreibt sie aus Vorlagen. Fehlt sie, legt das System eine
  **Mahnung** an, die das Fehlen anmahnt; jeder mit `write` ersetzt sie mit `write`.
- **Inhalte in `docs/` sind englisch**, gesucht wird mit `porter`. Übersetzen ist Sache von
  k-playbook, nicht von Kephalaion.
- **Ein Kopf für alle Dokumente** (`title`, `description`, `generated`, `hints`, `questions`,
  `sources`, `verified`); `questions` zählen in der Suche hoch. `type`, `tags`, `status` entfallen.
- **Eine feste Struktur unter `docs/`** legt Kephalaion fest und legt fehlende Verzeichnisse
  selbst an: vorerst `docs/manual/` und `docs/generated/`, aus einer Liste im Binary.
- **Finden über `search`**, Treffer je Abschnitt mit Name, Anker, Titel, Beschreibung und Auszug —
  das ist das zentrale Ergebnis, siehe „Suche über MCP“.
- **Ein Befehl stößt den Neubau an:** `rebuild_docs`, als MCP-Werkzeug für jeden Account mit
  Lesezugriff, als CLI am Hub für den Admin (`kephalaion hub collection rebuild-docs`); nur wo
  `docs/` schon existiert. Der automatische Neubau bei jeder Änderung ist zurückgestellt.
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
  Zerlegung für die Suche) und wo sie abweichend eingestellt wird, ist offen. *Am 2026-11-09
  entschieden: Englisch, ohne Einstellung — siehe unten.*
- **Neubau per Befehl**, CLI und MCP, vorerst für jeden Account. Wann das System von selbst neu
  baut, ist zurückgestellt; bis dahin genügt der Befehl. Er nimmt die Collection als Argument
  und gilt **nur für `docs/`** — ein Neubau von `docs/`; andere Neubauten kommen später als
  eigene. *Am 2026-11-10 präzisiert: MCP für jeden Account mit Lesezugriff,
  CLI nur am Hub für den Admin, nur wo `docs/` existiert — siehe unten.*
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

## Entschieden am 2026-11-09

Übergabe aus k-playbook (Sitzung k-playbook-6f; dort Workbench `vm:k-playbook-dev` →
`user/kamran/workbench/004-docs-struktur.md`, Entscheidungen 26, 30, 33–35) und Gespräch mit dem
Nutzer am selben Tag.

- **Sprache.** Inhalte in `docs/` einer Collection sind englisch. Das stellt k-playbook sicher
  (Erzeuger und Handdoku englisch; der Spiegel bleibt eine reine Kopie, übersetzt werden die
  Quellen selbst, entschieden dort am 2026-11-10); Kephalaion übersetzt nicht. Die Suche
  bleibt bei `porter unicode61`, ohne Sprache je Collection und ohne eigenen Stemmer. Die
  Beschreibung von `search` sagt, dass auf Englisch angefragt wird. Kephalaion geht davon aus,
  dass `docs/` englisch ist; was es nicht ist, wird schlechter gefunden. Die Doku dieses Repos
  wird später übersetzt (Nutzer, 2026-11-10); das Spiegeln ist bei k-playbook zurückgestellt.
- **Ein Kopf für alle Dokumente unter `docs/`:** `title` (Pflicht), `description` (Pflicht, ein
  Satz), `generated: {by, at, stats?}` (nur erzeugte), `hints`, `questions`,
  `sources: [{resource, item?, version?}]`, `verified`; unter `docs/generated/sources/` zusätzlich
  `mirror`.
  `type`, `tags` und `status` entfallen, damit auch der Filter `type`/`tags` in `search`.
- **Suchfelder und Gewichte:** Titel, Überschriften und `questions` hoch; `description` und
  `hints` mittel; der Text normal. `questions` sind Fragen, die genau dieses Dokument beantwortet;
  eine Fragen-Tabelle im Briefing von k-playbook gibt es nicht, die Suche ist der einzige Weg, auf
  dem sie wirken. `mirror`, `generated.stats`, `sources` und `verified` sind keine Suchfelder.
- **`index.md` ist kein Suchtreffer** (bestätigt).
- **Spiegel:** Quellen außerhalb von `docs/` spiegelt k-playbook nach
  `docs/generated/sources/<mirror>/`. Für Kephalaion ist das gewöhnliches `docs/`, indiziert wie
  alles andere, samt `index.md` je Verzeichnis; nichts Eigenes für externe Quellen.
- **`README.md` unterhalb von `docs/`** schreibt k-playbook aus Vorlagen, mit `title` und
  `description`, ohne `generated`. `docs/README.md` ganz oben bleibt Kephalaions. Die Mahnung
  braucht es nur dort, wo niemand schreibt.
- **`changes` bekommt `frontmatter`** (Nutzer, 2026-11-10), wie `list` und `read`: Die KI sieht
  bei „Was ist neu?“ gleich Titel, Beschreibung und Fragen der geänderten Dokumente.
- **FTS5 ist im Treiber:** `modernc.org/sqlite` mit SQLite 3.53.4; `porter unicode61`,
  `unicode61` und `trigram` getestet.

## Entschieden am 2026-11-10

Bei der Prüfung von Task 033 (`/k-task-refine`) mit dem Nutzer; im Task Entscheidungen 16–22.

- **`read` mit Abschnitt (16):** `read` bekommt `section` (Anker) in Task 033, aus derselben
  Zerlegung wie die Suche. Ein Anker, den es nicht gibt: Fehler `section_not_found` — ein Fehler
  der Anfrage, als Code neben der Meldung, kein interner Fehler.
- **Verzeichnisvorgänge (17):** `delete` mit `recursive` nimmt erzeugte Dateien mit; die Sperre
  gilt für Einzelvorgänge. Für jedes `rename` eines Verzeichnisses gilt dieselbe Regel: Erzeugte
  Dateien und Mahnungen darunter wandern nie unter einen neuen Namen mit — der Hub entfernt sie
  im selben Vorgang, ob das Verzeichnis aus `docs/` hinaus oder innerhalb von `docs/` umzieht
  (`docs/a/` → `docs/b/`), und der nächste Neubau stellt her, was unter `docs/` fehlt; bis dahin
  hat das verschobene Verzeichnis keine `index.md`. Grund: `rename` setzt `updated_by` auf den
  User, eine mitgenommene Mahnung wäre nicht mehr zu erkennen. Abgelehnt, mit klarer Meldung,
  wird nur ein `rename` von außerhalb nach `docs/` hinein, das eine `index.md` mitbringt oder eine
  `README.md`, die dabei zu `docs/README.md` würde.
- **Mahnung (18):** eine `README.md` unterhalb von `docs/` (nicht `docs/README.md`), die zuletzt
  `system` geschrieben hat. Jeder mit `write` darf sie ersetzen. Erkannt wird sie an Name und
  Urheber `system`, nicht am Frontmatter; das Frontmatter `generated` trägt sie trotzdem, für
  Leser. „Erzeugte Dateien“ im gesperrten Sinn sind nur `docs/README.md` und jede `index.md`
  unter `docs/`; die Mahnung wird eigens benannt.
- **Neubau (19)** nur, wo `docs/` auf oberster Ebene schon existiert; sonst „nichts zu tun“.
  Anstoßen darf ihn jeder Account mit Lesezugriff.
- **Namen (20):** Vorgang und MCP-Werkzeug `rebuild_docs`; CLI nur am Hub, für den Admin:
  `kephalaion hub collection rebuild-docs <collection>` (neben `hub collection fetch`). Accounts
  stoßen ihn über MCP an, wie `fetch_release`.
- **Felder von `search` (21):** Eingabe `query`, `collection`, `path`, `limit` (Standard 10,
  höchstens 50); Antwort je Collection eine Liste `hits` mit `rank`, `name`, `title`,
  `description`, `section` (`heading`, `headings` als Pfad, `anchor`), `excerpt`; ohne Treffer ein
  Hinweis auf `docs/index.md`. Von einem Dokument erscheinen in einer Antwort höchstens drei
  Abschnitte; die Felder des Dokuments (Titel, `description`, `questions`, `hints`) stehen an
  jedem Abschnitt.
- **Anfrage aus einer Frage (22):** Der Node verbindet die Wörter mit `OR`, `bm25()` ordnet.

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
`pitfalls/`. `queue/` und `user/<name>/` sind keine Doku und bleiben außerhalb.

**Die Liste** steht wie die Liste der Quellen als YAML im Binary
(`internal/hub/docsindex/layout.yaml`, `go:embed`; Task 033, Etappe 3): je festem Verzeichnis
`title` und `description` (englisch). Die Mahnung eines festen Verzeichnisses trägt beides, und
der Index zeigt so auch vor der ersten `README.md` eine Beschreibung. Die festen Verzeichnisse
bleiben, auch wenn nichts darin liegt; legt jemand eine `README.md` hinein, gilt deren
Beschreibung. Was Kephalaion später selbst ergänzt, kommt in dieselbe Liste.

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
- **Beschreibung:** Frontmatter `description`, bei einem Verzeichnis die seiner `README.md` —
  auch einer Mahnung: Die eines festen Verzeichnisses trägt die Beschreibung aus der Liste, jede
  andere keine. Fehlt sie, steht `*no description*`. Kein Ersatz aus dem ersten Satz.
- **Welche Verzeichnisse:** Ein Verzeichnis zählt nur über Dokumente, die nicht erzeugt sind —
  keine `index.md`, keine Mahnung; die festen Verzeichnisse zählen immer. Ein Verzeichnis, das
  nur noch erzeugte Dateien und Mahnungen trägt, verliert sie beim nächsten Neubau. Ein Dokument,
  das `index.md` oder `README.md` als Verzeichnis nutzt (`docs/index.md/x.md`, aus der Zeit vor
  der Sperre), zählt nicht und steht in keinem Index; auf den belegten Namen legt der Neubau keine
  Datei (siehe „Schreibschutz“).
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

- **Einfache englische Wörter**, keine FTS5-Syntax; Fragen gehen. Der Node maskiert die Wörter
  und verbindet sie mit `OR`; `bm25()` ordnet (2026-11-10). Ein Wort, das nicht vorkommt (etwa
  „how“, „the“), lässt die Anfrage nicht scheitern.
- **Filter wahlweise:** `path` unter `docs/` und die Collection(s) wie bei den anderen
  Werkzeugen. Kein Filter `type`/`tags` (entfallen am 2026-11-09).
- **Die Beschreibung des Werkzeugs** sagt selbst, wie man anfragt: einfache Wörter auf Englisch,
  Fragen gehen, Treffer je Abschnitt, `name` und Anker an `read`.
- **Keine Treffer** sind `index.md`, `docs/README.md` und die Mahnung — sie verdrängten im
  Ranking die Dokumente, auf die sie zeigen.
- **Ohne Treffer** ein klares „nichts gefunden“ mit dem Hinweis auf `docs/index.md`, statt dünner
  Treffer.

Vorbild: `k_playbook_knowledge_search` liefert heute je Treffer `path`, `heading`, `excerpt`,
`rank`, `anchor` (k-playbook, `docs/mcp.md`). Die Felder von `search` sind entschieden
(„Entschieden am 2026-11-10“, 21): `rank`, `name`, `title`, `description`, `section` mit
`heading`, `headings` und `anchor`, `excerpt`; höchstens drei Abschnitte je Dokument.

### Der Index: FTS5 im Node (entschieden am 2026-10-16)

FTS5 ist die Volltextsuche, die SQLite mitbringt. Der Suchindex ist eine Tabelle in der Replica
des Nodes, neben den Dokumenten, und wird in derselben Transaktion nachgezogen, in der der
Abgleich die Zeilen schreibt — er passt immer zur Revision, ohne zweiten Speicher.

- **Zerlegung an den Überschriften:** Ein Abschnitt ist eine Überschrift samt Text bis zur
  nächsten. Indiziert wird der ganze Text, nicht nur die Überschriften.
- **Felder je Abschnitt**, gewichtet in `bm25()`: Titel des Dokuments, Überschriften darüber und
  `questions` hoch, `description` und `hints` mittel, der Text normal. Der Auszug kommt aus
  `snippet()`. Die Gewichte im Einzelnen nach Messung.
- **Fassung der Zerlegung** in der Replica; ändert sie sich, baut der Node neu.
- **Sprache:** Englisch, `porter unicode61` (entschieden am 2026-11-09). Die Inhalte sind
  englisch; eine zweite Sprache in der Suche ist nicht vorgesehen.
- **Treiber:** `modernc.org/sqlite` (`go.mod`), FTS5 enthalten (geprüft am 2026-11-09).
- **Gebaut (Task 033, Etappe 5):** Paket `internal/sections` (Zerlegung an ATX-Überschriften
  außerhalb von Codeblöcken, Anker wie GitHub, eindeutig je Dokument; Fassung
  `sections.Version`), in der Replica (Schemafassung 6) die Tabelle `sections` (Dokument-`id`,
  Collection, Name, Reihenfolge, Anker, Überschrift, Pfad der Überschriften, Titel, Beschreibung)
  und die FTS5-Tabelle `sections_fts` (Titel, Überschriften, `questions`, `description`, `hints`,
  Text) mit derselben `rowid`. Nachgezogen in `Replica.apply`, je Zeile, die der Upsert
  tatsächlich schreibt; beim Entfernen einer Collection und bei `reset` mit. Indiziert werden
  nur **Markdown-Dokumente** (`.md`) unter `docs/` — die Zerlegung gilt Markdown; ein Bild oder
  eine YAML-Datei brächte nur Rauschen. Die Fassung der Zerlegung steht in `db_info`
  (`sections_version`); weicht sie ab, baut der nächste Schreibvorgang der Replica den Index neu.

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
„Was ist neu in `docs/` seit gestern“ geht damit in einem Aufruf. Was für die KI fehlte, kam mit
Task 033: der Parameter **`frontmatter`** wie bei `list` — je geändertem `.md`-Dokument sein
Frontmatter, so dass die KI Titel, Beschreibung und Fragen sieht, ohne jedes Dokument zu lesen.
Bekannte Grenze: Wer ein Dokument aus `path` heraus umbenennt, erscheint dort nicht mehr.

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
| Facetten `type`, `tags`, `status` | im Frontmatter, ungenutzt | entfallen (2026-11-09) | — |
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
nachsieht, darunter je Verzeichnis als dessen Beschreibung. Einen `type` trägt sie nicht: Der
Kopf vom 2026-11-09 kennt `type` nicht mehr (siehe „Abweichung: kein `type`“); die Frage, ob
`README.md` für OKF-Leser einen trägt, ist damit erledigt.

## Abweichung: kein `type`

OKF verlangt `type` in jedem Concept (§4.1). Der Kopf vom 2026-11-09 kennt `type` nicht mehr;
die Dokumente unter `docs/` sind damit keine strengen OKF-Concepts. Wir nehmen das hin, weil
weder Index noch Suche `type` brauchen.

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
`vendor/`“ allein reichte nicht, weil der Admin dort schreiben darf. **Ein Import** (`hub
import`), der eine solche Datei enthält, übergeht sie und nennt sie in seiner Ausgabe; der Neubau
stellt sie her (entschieden in Task 033, Etappe 2). Ablehnen hätte jeden Import eines Verzeichnisses
mit einer alten `index.md` scheitern lassen; übernehmen hätte die Sperre umgangen.

**Kein Verzeichnis namens `index.md` oder `README.md` unter `docs/`** (Task 033, nach einem
Befund des Reviews). Wer `write` hat, konnte ein Dokument wie `docs/index.md/x.md` oder
`docs/a/README.md/b.md` anlegen. Damit war der Name, auf den der Neubau eine Datei legt
(`docs/README.md`, die `index.md` eines Verzeichnisses, die Mahnung), ein Verzeichnis. Der Neubau
der ganzen Collection scheiterte daran (`path_conflict` im Store, über HTTP ein 500), bis jemand
das Dokument entfernte. Seitdem gilt:

- **Ein solcher Name entsteht nicht neu.** `create`, ein `rename` auf ihn (auch jedes Dokument
  eines Verzeichnisses, das umzieht) und `hub doc put` eines neuen Dokuments sind
  `generated_file`, gleich mit welchen Rechten; `hub import` übergeht ihn und nennt ihn. Erkannt
  am Namen (`contract.ReservedDirOf`): ein Pfadteil `index.md` oder `README.md` unter `docs/`,
  der nicht der letzte ist. Der Code ist derselbe wie für die erzeugten Dateien, weil es um
  dieselben Namen geht: Nur das System legt dort etwas an.
- **Ein Bestand blockiert nicht.** Ein Dokument, das schon so heißt, bleibt änderbar und
  löschbar und lässt sich hinaus umbenennen. Der Neubau übergeht es und legt auf den belegten
  Namen keine Datei; dort fehlen `index.md`, Mahnung oder `docs/README.md`, bis es fort ist.
  Alles Übrige baut er wie sonst.

## Unterverzeichnisse und ihre `README.md`

- **Beschreibung im Frontmatter:** `description` (ein Satz, wie OKF), dazu wahlweise `title`. Der
  Text darunter erklärt das Verzeichnis ausführlicher.
- **Wer sie schreibt (2026-11-09):** k-playbook, aus Vorlagen (`defaults/docs/<pfad>/README.md`),
  mit `title` und `description`, ohne `generated`. Die Mahnung bleibt für Verzeichnisse, in die
  niemand eine `README.md` schreibt.
- **Volltext:** Eine von Hand geschriebene `README.md` ist eine gewöhnliche Beschreibung und kommt
  in den Volltextindex. Nicht hinein kommen `index.md`, `docs/README.md` und die Mahnung — sie
  verdrängten im Ranking die Dokumente, auf die sie zeigen (Erfahrung aus k-playbook).
- **Die Mahnung** trägt OKF-gemäß `generated: {by: kephalaion, at: …}` im Frontmatter — für
  Leser. **Erkannt** wird sie an Name und Urheber (2026-11-10): eine `README.md` unterhalb von
  `docs/` (nicht `docs/README.md`), die zuletzt `system` geschrieben hat (`updated_by`). Das
  Frontmatter ließe sich fälschen; den Urheber setzt allein der Hub. Index, Suche und Mensch
  erkennen so, dass sie noch nicht ersetzt wurde.
- **Ersetzen:** Jeder mit `write` ersetzt sie mit `write`, ohne `supersede` — obwohl sie `system`
  gehört (2026-11-10); `create` scheitert am belegten Namen. Danach ist sie eine gewöhnliche
  `README.md` des Users, gleich welches Frontmatter darin steht: Wer sie ersetzt, übernimmt sie
  (`created_by` wird er, auch beim Admin), damit er sie danach wie jedes eigene Dokument mit
  `write` ändert. Nur die Mahnung gilt so; andere Dokumente von `system` (etwa `defaults/` einer
  Collection mit Quelle) bleiben fremd. Wer `supersede` hat, ersetzt sie ebenso.
- **Eine einzelne Mahnung löschen oder umbenennen** (entschieden in Task 033, Etappe 2, im Sinn
  von Entscheidung 18): **Löschen** geht mit demselben Recht wie Ersetzen (`write` oder
  `supersede`); der nächste Neubau legt sie wieder an, wenn die `README.md` weiter fehlt — so
  bleibt Löschen harmlos, und niemand braucht ein Recht über das Ersetzen hinaus. **Umbenennen**
  einer einzelnen Mahnung lehnt der Hub ab (`generated_file`), aus dem Grund von Entscheidung 17:
  Unter einem neuen Namen trüge sie den User in `updated_by` und wäre nicht mehr als Mahnung zu
  erkennen; ein Text „hier fehlt die `README.md`“ an anderer Stelle hätte auch keinen Sinn. Wer
  sie loswerden will, ersetzt oder löscht sie. In einem Verzeichnisvorgang gilt Entscheidung 17:
  `delete` mit `recursive` nimmt sie mit, `rename` entfernt sie.

## Zurückgestellt

Die offenen Fragen sind entschieden (2026-11-09 und 2026-11-10, siehe oben; im Task 033 die
Entscheidungen 1–22). Was bleibt, ist bewusst zurückgestellt:

- **Automatischer Neubau:** wann und wer — im Schreibvorgang des Hubs, danach, oder nie von
  selbst. Bis dahin stößt k-playbook `rebuild_docs` an, nachdem es veröffentlicht hat.
- **`log.md`:** Vorschlag, keine. „Was ist neu“ beantwortet `changes` genauer; eine `log.md` je
  Verzeichnis wäre eine gröbere Fassung davon und änderte sich bei jedem Schreibvorgang mit.
- **Bleve, semantische Suche.** (Deutsch in der Suche entfällt: Die Inhalte sind englisch.)
- **Größe einer `index.md`** (Todo #22): Kephalaion prüft sie vorerst nicht.
- **Gewichte der Suchfelder** im Einzelnen: vorerst nach Entscheidung 12, nachzustellen nach
  Messung.
