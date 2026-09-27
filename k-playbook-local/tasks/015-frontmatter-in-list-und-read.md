# Task 015 — Frontmatter in list und read

`list` und `read` liefern auf Wunsch das Frontmatter von Markdown-Dateien, Verzeichnissen und
Collections als JSON — die KI bekommt eine Übersicht in einem Aufruf.

## Intent

Ein Aufruf von `list` gibt der KI die Frontmatter aller Markdown-Dateien, Verzeichnisse und
Collections einer Ebene oder eines Baums, ohne dass sie jede Datei lesen muss.
- Das Frontmatter bleibt im Inhalt der Datei; Hub, Vertrag, Schemata und Erweiterung bleiben
  unverändert.
- Nur `.md`-Dateien; ein Block ganz am Anfang zwischen `---`-Zeilen, darin YAML; in der
  Antwort ein JSON-Objekt.
- Verzeichnisse und Collections bekommen das Frontmatter ihrer `README.md`.
- Ein fehlerhaftes Frontmatter macht nichts unlesbar; ohne den Parameter bleiben die Antworten
  wie heute.

## Referenzen

- `docs/konzept.md` — „Datenmodell“ (Metadaten, Frontmatter im Text) und „Werkzeuge“ →
  „Allgemein — lesen“ (`list`, `read`, Abschnitt „Frontmatter“); Festlegung vom 2026-09-27.
- `docs/begriffe.md` — `list`, `read`, `directory`, `mask`, frontmatter (als geplant
  markiert); neue Begriffe vor Benutzung eintragen.
- `internal/node/mcpnode/list.go` — `ListInput`, `ListEntry`, `listDocuments`,
  `collectionEntries`, Fingerabdruck des Cursors.
- `internal/node/mcpnode/read.go` — `ReadInput`, `ReadOutput`.
- `internal/node/replica/read.go` — `Entry`, `EntryByName`, `ListEntries`, `ChildDirs`.
- `go.mod` — `go.yaml.in/yaml/v3` ist schon da.
- `k-playbook-local/tasks/done/014-schreiben-ueber-mcp.md` — Stand, auf dem dieser Task
  aufbaut.

## Ziel

Beide Werkzeuge bekommen einen Parameter `frontmatter` (bool, Standard aus). Mit ihm trägt
jeder passende Eintrag ein Feld `frontmatter` (JSON-Objekt) oder, wenn es nicht lesbar ist,
`frontmatter_error` (kurzer Grund).

## Kontext

- **Voraussetzung: Task 014 abgeschlossen und in `done/`.** Dieser Task baut auf dessen Stand
  auf — `mcpnode`, `replica` und die Doku, wie 014 sie hinterlassen hat (Schreibwerkzeuge,
  `writable`, Beschreibungen in README, Konzept, Begriffe). Liegt 014 noch nicht in `done/`,
  nicht beginnen, sondern melden.
- **Was als Frontmatter gilt:** Der Inhalt beginnt mit einer Zeile, die genau `---` lautet
  (davor darf ein UTF-8-BOM stehen, `\r\n` als Zeilenende erlaubt); der Block endet an der
  nächsten Zeile, die genau `---` lautet. Dazwischen steht YAML. Steht der Block nicht ganz am
  Anfang, hat die Datei kein Frontmatter — das Feld fehlt. Nur diese Schreibweise, kein TOML
  (`+++`), kein JSON.
- **Ausgabe:** das YAML gelesen, als JSON-Objekt. Oben muss ein Objekt stehen (eine Liste oder
  ein einzelner Wert ist ein Fehler), Schlüssel sind Text (sonst Fehler), Zeitangaben werden
  Text. Ein leerer Block (`---`, `---`) ergibt `{}`. Nicht geschlossen, ungültiges YAML oder zu
  lang: der Eintrag erscheint trotzdem, mit `frontmatter_error` statt `frontmatter`.
- **Grenze:** gelesen werden nur die ersten 64 KiB des Inhalts; endet der Block nicht darin,
  `frontmatter_error`. Die Grenze gilt je Eintrag; ein Gesamtmaß für eine Seite von `list` gibt
  es nicht — die Größe der Antwort steuert die KI über `limit`.
- **Welche Einträge:**
  - Dokumente, deren Name auf `.md` endet, ohne Unterscheidung von Groß- und Kleinschreibung;
  - Verzeichnisse (Art `directory`): das Frontmatter von `<verzeichnis>/README.md` — genau so
    geschrieben, nur aus dem Verzeichnis selbst;
  - Collections (Art `collection`, bei `list` ohne `collection` oder mit `<hub>:`): das
    Frontmatter von `README.md` auf ihrer obersten Ebene.
  - Die `README.md` selbst erscheint weiter als gewöhnliches Dokument mit ihrem Frontmatter.
    Fehlt die Datei oder ihr Frontmatter, fehlt das Feld; ist ihr Frontmatter fehlerhaft, tragen
    Verzeichnis bzw. Collection `frontmatter_error` — wie das Dokument `README.md` selbst.
- **`list`:** Inhalt nur für die Einträge der aktuellen Seite lesen, ohne den Parameter wie
  bisher gar nicht. Mit `recursive: true` gibt es keine Einträge für Verzeichnisse, also kein
  Frontmatter von Verzeichnissen; die README-Dateien erscheinen als Dokumente. `frontmatter`
  gehört nicht zum Fingerabdruck des Cursors — es ändert nur, was ein Eintrag enthält, nicht
  welche Einträge kommen. Die Meldung `errCursorMismatch` (`cursor.go`, „die übrigen
  Angaben“) nennt `limit` und `frontmatter` als Ausnahmen.
- **`read`:** mit `frontmatter: true` bei einem Dokument (`.md`) sein Frontmatter — per `name`
  wie per `id` —, bei einem Verzeichnis das seiner `README.md`, bei der Wurzel einer Collection
  (leerer `name`) das ihrer `README.md`; die Wurzel eines Hubs (`<hub>:`) keines. Der Inhalt
  bleibt der volle Text, einschließlich Frontmatter; mit `content: false` nur das Frontmatter
  ohne Text.
- **Lesen in einer eigenen Funktion** ohne Abhängigkeit von MCP und Replica, gründlich
  getestet — etwa ein kleines neutrales Paket `internal/frontmatter`; die Suche wird sie später
  mitnutzen.
- **Nicht in diesem Task:** Frontmatter schreiben oder prüfen, Frontmatter in `changes`,
  andere Schreibweisen, Frontmatter für Dateien ohne Markdown.
- Beschreibungen der Werkzeuge: den Parameter in wenigen Worten nennen, deutsch — jedes Wort
  geht in den Kontext der KI.

## Zu bauen

### Etappe 1 — Frontmatter lesen

- Die Funktion: Eingabe der Anfang eines Inhalts, Ausgabe JSON-Objekt, „keins“ oder Fehler
  mit kurzem Grund.
- Tests: mit und ohne Frontmatter; Block nicht am Anfang; BOM, `\r\n`; nicht geschlossen;
  leerer Block; ungültiges YAML; Liste bzw. einzelner Wert oben; Schlüssel kein Text;
  verschachtelte Objekte und Listen, Zahlen, Wahrheitswerte, Zeitangaben; Grenze von 64 KiB;
  `---` im Text nach dem Block bleibt Text.

### Etappe 2 — list und read

- Parameter `frontmatter` in beiden Werkzeugen; in der Replica nur die ersten 64 KiB lesen,
  nur für die Einträge der Seite; `README.md` für Verzeichnisse und Collections.
- Tests mit dem MCP-Client des go-sdk: ohne Parameter unverändert; `.md` und `.MD`, andere
  Endungen ohne Feld; Verzeichnis mit und ohne `README.md`, `README.md` ohne Frontmatter;
  Collections; `recursive: true` mit `mask: "SKILL.md"`; `frontmatter_error` bei kaputtem
  YAML, der Eintrag kommt trotzdem — beim Dokument wie beim Verzeichnis und der Collection mit
  kaputter `README.md`; Blättern mit Cursor, auch beim Wechsel des Parameters;
  `read` bei Dokument (per `name` und per `id`), Verzeichnis, Wurzel der Collection und des
  Hubs, mit `content: false` und `true`.

### Etappe 3 — Durchlauf und Doku

- Durchlauf mit `serve` in `home:eins`, nur unter `test/`: einige `SKILL.md` mit Frontmatter,
  ein Verzeichnis mit `README.md`; `list` mit `recursive`, `mask` und `frontmatter` über MCP.
- Doku auf dem Stand nach Task 014 nachziehen, nicht neu schreiben: `docs/begriffe.md`
  (frontmatter — Markierung „geplant“ entfernen, `README.md` als Beschreibung eines
  Verzeichnisses), `docs/konzept.md` („Stand“, „Werkzeuge“: gebaut; in „Allgemein — lesen“ der
  Cursor-Satz auf „alle Angaben außer `limit` und `frontmatter`“, im Abschnitt „Frontmatter“
  der Fehlerfall der `README.md` bei Verzeichnis und Collection), `README.md`
  (MCP-Werkzeuge, Parameter `frontmatter`), `k-playbook-local/k-playbook.md` (das neue Paket,
  `mcpnode`), `docs/vscode.md` nur, falls die Erweiterung betroffen ist (nicht vorgesehen),
  `docs/fortschritt.md`.

---
## Review-Log (2026-09-27)

**Pfad:** k-playbook-local/tasks/015-frontmatter-in-list-und-read.md
**Intent:** inline (`## Intent`)
**Runden:** 1

### Diskussion

Keine strittigen Punkte: Der Critic fand keinen FEHLER, der Editor hat alle vier gerouteten
Issues umgesetzt, der Moderator hat die Vorschläge ohne Widerspruch übernommen (Fast Path).
Der Critic hatte zuvor den Bestand geprüft: Task 014 in `done/`, Konzept-Festlegung vom
2026-09-27, `begriffe.md`, `list.go`, `read.go`, `replica/read.go`, `go.mod` und der
Trennungstest passen zu den Annahmen des Tasks.

### Critic-Issues
| ID | Kategorie | Datei | Stelle | Problem | Empfehlung |
|---|---|---|---|---|---|
| FEHLEND-01 | FEHLEND | 015 | Kontext → `list`, Etappe 3 | `frontmatter` soll nicht zum Fingerabdruck des Cursors gehören; `docs/konzept.md` sagt bis heute „alle Angaben außer `limit`“. Etappe 3 nennt diesen Satz nicht — nach dem Task widersprechen sich Konzept und Code. | Cursor-Satz im Konzept in Etappe 3 aufnehmen; Meldung `errCursorMismatch` prüfen. |
| FEHLEND-02 | FEHLEND | 015 | Kontext → `read` | `read` kennt `name` und `id`; der Task beschreibt Frontmatter nur für „ein Dokument (`.md`)“, ohne den Weg per `id`. | Satz ergänzen: gilt gleichermaßen per `id`; Testfall. |
| WARNUNG-01 | WARNUNG | 015 | Kontext → Grenze / `list` | 64 KiB gelten je Eintrag, `list` liefert bis zu 1000 Einträge je Seite; kein Gesamtmaß genannt. | Entscheidung im Task festhalten. |
| WARNUNG-02 | WARNUNG | 015 | Kontext → Welche Einträge | Ob ein Verzeichnis mit kaputtem README-Frontmatter `frontmatter_error` trägt oder das Feld weglässt, stand nicht da. | Halbsatz plus Testfall. |

### Moderator-Routing
| ID | Route | Begründung | Ergebnis |
|---|---|---|---|
| FEHLEND-01 | pass | Doku-Widerspruch nach dem Task, den der Ausführende aus der Etappe-3-Liste nicht ableiten kann; Regel docs-sync. | gefixt |
| FEHLEND-02 | pass | Echte Mehrdeutigkeit mit geringem Aufwand. | gefixt |
| WARNUNG-01 | decide → pass | Moderator-Entscheidung: kein Gesamtlimit in diesem Task, die KI steuert über `limit`; im Task festhalten, keine neue Mechanik. | gefixt |
| WARNUNG-02 | decide → pass | Moderator-Entscheidung: Fehler im README-Frontmatter erscheinen bei Verzeichnis und Collection als `frontmatter_error`, passend zum Intent „nichts wird unlesbar“. | gefixt |

### Editor-Entscheidungen
| ID | Aktion | Begründung |
|---|---|---|
| FEHLEND-01 | Behoben | Cursor-Satz im Konzept in Etappe 3 benannt; in „Kontext → `list`“ festgelegt, dass `errCursorMismatch` `limit` und `frontmatter` als Ausnahmen nennt — die Meldung geht an die KI, sie soll den Parameter frei wechseln dürfen. |
| FEHLEND-02 | Behoben | `readByID` und `readByName` liefern denselben Eintragstyp; Frontmatter hängt am Dokument, nicht am Zugriffsweg. Testfall „per `name` und per `id`“. |
| WARNUNG-01 | Behoben | Ein Satz im Abschnitt „Grenze“ gemäß Moderator-Entscheidung. |
| WARNUNG-02 | Behoben | Halbsatz unter „Welche Einträge“, Testfall in Etappe 2 (Verzeichnis und Collection mit kaputter `README.md`). |

### Moderator-Entscheidungen
- WARNUNG-01: kein Gesamtmaß für eine Seite von `list`; die KI steuert über `limit`.
- WARNUNG-02: kaputtes Frontmatter der `README.md` ergibt `frontmatter_error` auch bei Verzeichnis und Collection.
- Editor-Hinweis ohne Vorschlag: Der Konzept-Abschnitt „Frontmatter“ deckt den Fehlerfall der `README.md` nicht ausdrücklich. Als Konsistenz-Ergänzung zu WARNUNG-02 in Etappe 3 aufgenommen (Konzept nachziehen).
- Redaktionell ohne inhaltliche Änderung: typografische Anführungszeichen wie im Rest der Datei, Einschub zu `read` per `id` gekürzt, Zeilenumbrüche geglättet.

### Intent-Alignment
Ja — alle Punkte des Intents sind abgedeckt, die Referenzen stimmen mit dem Repo überein (Task 014 in `done/`, `errCursorMismatch` in `cursor.go`, `go.yaml.in/yaml/v3` in `go.mod`, Konzept und Begriffe zu Frontmatter vorhanden). Frontmatter bleibt im Inhalt; Hub, Vertrag, Schemata und Erweiterung unberührt; fehlerhaftes Frontmatter führt zu `frontmatter_error` bei Dokument, Verzeichnis und Collection; ohne Parameter unverändert.

### Geänderte Dateien
- 015-frontmatter-in-list-und-read.md: Grenze je Eintrag ohne Gesamtmaß (WARNUNG-01); `frontmatter_error` bei Verzeichnis und Collection (WARNUNG-02); `errCursorMismatch` nennt Ausnahmen (FEHLEND-01); `read` per `name` wie per `id` (FEHLEND-02); Testfälle in Etappe 2 (FEHLEND-02, WARNUNG-02); Konzept-Nachzug in Etappe 3 (FEHLEND-01, WARNUNG-02)

### Offen (nicht gefixt)
- —
