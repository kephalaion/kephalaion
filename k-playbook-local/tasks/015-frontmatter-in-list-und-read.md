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
  `frontmatter_error`.
- **Welche Einträge:**
  - Dokumente, deren Name auf `.md` endet, ohne Unterscheidung von Groß- und Kleinschreibung;
  - Verzeichnisse (Art `directory`): das Frontmatter von `<verzeichnis>/README.md` — genau so
    geschrieben, nur aus dem Verzeichnis selbst;
  - Collections (Art `collection`, bei `list` ohne `collection` oder mit `<hub>:`): das
    Frontmatter von `README.md` auf ihrer obersten Ebene.
  - Die `README.md` selbst erscheint weiter als gewöhnliches Dokument mit ihrem Frontmatter.
    Fehlt die Datei oder ihr Frontmatter, fehlt das Feld.
- **`list`:** Inhalt nur für die Einträge der aktuellen Seite lesen, ohne den Parameter wie
  bisher gar nicht. Mit `recursive: true` gibt es keine Einträge für Verzeichnisse, also kein
  Frontmatter von Verzeichnissen; die README-Dateien erscheinen als Dokumente. `frontmatter`
  gehört nicht zum Fingerabdruck des Cursors — es ändert nur, was ein Eintrag enthält, nicht
  welche Einträge kommen.
- **`read`:** mit `frontmatter: true` bei einem Dokument (`.md`) sein Frontmatter, bei einem
  Verzeichnis das seiner `README.md`, bei der Wurzel einer Collection (leerer `name`) das ihrer
  `README.md`; die Wurzel eines Hubs (`<hub>:`) keines. Der Inhalt bleibt der volle Text,
  einschließlich Frontmatter; mit `content: false` nur das Frontmatter ohne Text.
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
  YAML, der Eintrag kommt trotzdem; Blättern mit Cursor, auch beim Wechsel des Parameters;
  `read` bei Dokument, Verzeichnis, Wurzel der Collection und des Hubs, mit `content: false`
  und `true`.

### Etappe 3 — Durchlauf und Doku

- Durchlauf mit `serve` in `home:eins`, nur unter `test/`: einige `SKILL.md` mit Frontmatter,
  ein Verzeichnis mit `README.md`; `list` mit `recursive`, `mask` und `frontmatter` über MCP.
- Doku auf dem Stand nach Task 014 nachziehen, nicht neu schreiben: `docs/begriffe.md`
  (frontmatter — Markierung „geplant“ entfernen, `README.md` als Beschreibung eines
  Verzeichnisses), `docs/konzept.md` („Stand“, „Werkzeuge“: gebaut), `README.md`
  (MCP-Werkzeuge, Parameter `frontmatter`), `k-playbook-local/k-playbook.md` (das neue Paket,
  `mcpnode`), `docs/vscode.md` nur, falls die Erweiterung betroffen ist (nicht vorgesehen),
  `docs/fortschritt.md`.
