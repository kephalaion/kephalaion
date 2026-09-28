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

## Fortschritt

| Etappe | Status | Datum | Notiz |
|---|---|---|---|
| 1 — Frontmatter lesen | erledigt | 2026-09-27 | Paket `internal/frontmatter` (neutral): `Parse(head)` → Objekt, „keins“ oder kurzer Fehler; `MaxBytes` 64 KiB (genau 64 KiB gelten als möglicherweise abgeschnitten), BOM, `\r\n`, Schluss ohne Zeilenende; über `yaml.Node`, damit Zeitangaben der geschriebene Text bleiben und Schlüssel am Tag geprüft werden (auch doppelte); `.inf`/`.nan` Fehler; `IsMarkdown`. Tests `frontmatter_test.go` grün; Befund `frontmatter-yaml.md` |
| 2 — list und read | erledigt | 2026-09-27 | Parameter `frontmatter` in `ListInput`/`ReadInput`, Felder `frontmatter` (`any`, `{}` bleibt) und `frontmatter_error` in `ListEntry`/`ReadOutput`; `replica.HeadByName`/`HeadByID` (erste n Bytes per `substr(CAST(content AS BLOB))`, nur lebende Zeilen ohne SYSTEM:); `mcpnode/frontmatter.go`: Dokumente per id (bei `read` mit Inhalt aus dem gelesenen Text), Verzeichnisse `<dir>/README.md`, Collections `README.md` — bei `list` erst nach dem Blättern, nur für die Seite, über alle Hubs in einem zweiten Gang (`eachValid`); `errCursorMismatch` nennt `limit` und `frontmatter`; Beschreibungen ergänzt; Begriff `README.md`. Tests `frontmatter_test.go` (mcpnode, MCP-Client des go-sdk) und `TestHead` (replica); `make check-quick` grün |
| 3 — Durchlauf und Doku | erledigt | 2026-09-27 | Durchlauf gegen den laufenden Node (`make dev-install`, Dienst neu gestartet), `home:eins` nur unter `test/frontmatter/` (README.md, drei `SKILL.md` — eine kaputt —, `notes.txt`, `plain.md`; liegen gelassen): `list` mit `recursive`, `mask: "SKILL.md"`, `frontmatter` über MCP per curl liefert die Frontmatter, `frontmatter_error` bei der kaputten, Verzeichnis über README.md, `.txt` und ohne Block kein Feld, ohne Parameter kein Feld, Blättern mit wechselndem Parameter; `read` Verzeichnis, `content: false`, voller Text. Doku: begriffe (frontmatter ohne „geplant“, README.md), konzept (Stand, Cursor-Satz, Frontmatter-Abschnitt gebaut samt README-Fehlerfall), README (list, read), k-playbook.md (`frontmatter`, `mcpnode`), fortschritt; vscode.md unberührt. Befund `mcp-durchlauf-curl.md`. `make check` grün |

<!-- k-task-run: snapshot 015-frontmatter-in-list-und-read.md 0449445e9b6f4af3f77cc904c4f68f5c0129fd29 -->

## Ausführung

<!-- k-task-run: ausgeführt 015-frontmatter-in-list-und-read.md -->
**Status:** Erfolgreich ausgeführt  
**Datum:** 2026-09-27  
**Zusammenfassung:** Alle drei Etappen umgesetzt. Neues neutrales Paket `internal/frontmatter` (`Parse`, `MaxBytes`, `IsMarkdown`; über den Knotenbaum von yaml.v3, damit Zeitangaben Text bleiben und Schlüssel am Tag geprüft werden), Replica-Abfragen `HeadByName`/`HeadByID` (erste 64 KiB), Parameter `frontmatter` mit Feldern `frontmatter`/`frontmatter_error` in `list` und `read` (`internal/node/mcpnode/frontmatter.go`; Dokumente `.md`, Verzeichnisse und Collections über `README.md`, nur für die Seite, nicht im Cursor; `errCursorMismatch` nennt `limit` und `frontmatter`). Tests mit dem MCP-Client des go-sdk, Durchlauf gegen den laufenden Node in `home:eins` unter `test/frontmatter/`. Doku nachgezogen: `docs/begriffe.md`, `docs/konzept.md`, `README.md`, `k-playbook-local/k-playbook.md`, `docs/fortschritt.md`. Befunde in `k-playbook-local/material/befunde/frontmatter-yaml.md` und `mcp-durchlauf-curl.md`. Nichts committet.

### Diff: Geänderte Dateien

**Umfang:** ab Task-Beginn  
**Snapshot:** `0449445e9b6f4af3f77cc904c4f68f5c0129fd29`

```text
 README.md                                          |  10 +-
 docs/begriffe.md                                   |  15 +-
 docs/fortschritt.md                                |  14 +-
 docs/konzept.md                                    |  33 +++-
 internal/frontmatter/frontmatter.go                | 181 +++++++++++++++++
 internal/frontmatter/frontmatter_test.go           | 160 +++++++++++++++
 internal/node/mcpnode/cursor.go                    |   2 +-
 internal/node/mcpnode/frontmatter.go               |  93 +++++++++
 internal/node/mcpnode/frontmatter_test.go          | 220 +++++++++++++++++++++
 internal/node/mcpnode/list.go                      |  51 ++++-
 internal/node/mcpnode/read.go                      |  54 +++--
 internal/node/replica/read.go                      |  35 ++++
 internal/node/replica/read_test.go                 |  49 +++++
 k-playbook-local/k-playbook.md                     |  10 +-
 .../material/befunde/frontmatter-yaml.md           |  45 +++++
 .../material/befunde/mcp-durchlauf-curl.md         |  28 +++
 16 files changed, 961 insertions(+), 39 deletions(-)
```

### Diff: Code-Änderungen

Der Diff umfasst rund 1300 Zeilen. Nebensächlich und hier nur zusammengefasst: die Doku-Nachzüge in `README.md` (Parameter `frontmatter` bei `list` und `read`), `docs/begriffe.md` (Begriff `frontmatter` ohne „geplant“, neuer Begriff `README.md`), `docs/konzept.md` (Stand, Cursor-Satz „außer `limit` und `frontmatter`“, Abschnitt „Frontmatter“ als gebaut samt Fehlerfall der `README.md`), `docs/fortschritt.md` (Task 015 nach „abgeschlossen“), `k-playbook-local/k-playbook.md` (Paket `frontmatter`, `mcpnode`); die beiden neuen Befunde `frontmatter-yaml.md` und `mcp-durchlauf-curl.md`; die Tests `internal/frontmatter/frontmatter_test.go` (Tabelle mit rund 40 Fällen plus `TestParseLimit`, `TestIsMarkdown`), `internal/node/mcpnode/frontmatter_test.go` (`TestListFrontmatter`, `TestReadFrontmatter` über den MCP-Client des go-sdk) und `TestHead` in `internal/node/replica/read_test.go`; in `cursor.go` nur der Text von `errCursorMismatch` („Angaben außer limit und frontmatter“). In `read.go` bekommen `readByName` und `readByID` einen Parameter `withFM` und rufen an drei Stellen (Wurzel, Dokument, Verzeichnis) `readmeFrontmatter` bzw. `documentFrontmatter`; `ReadInput`/`ReadOutput` und `ListInput`/`ListEntry` tragen `Frontmatter`/`FrontmatterError`.

Kern des neuen Pakets — die Grenze und die Schleife über die Zeilen:

`````diff
+func Parse(head []byte) (fm map[string]any, ok bool, err error) {
+	// Genau MaxBytes können ein abgeschnittener Anfang sein: Ein `---` ganz
+	// am Ende ohne Zeilenende zählt dann nicht als Schluss.
+	truncated := len(head) >= MaxBytes
+	if len(head) > MaxBytes {
+		head = head[:MaxBytes]
+	}
+	head = bytes.TrimPrefix(head, bom)
+	line, rest, _ := nextLine(head)
+	if string(line) != marker {
+		return nil, false, nil
+	}
+	body := rest
+	for {
+		line, after, eol := nextLine(rest)
+		if string(line) == marker && (eol || !truncated) {
+			return decode(body[:len(body)-len(rest)])
+		}
+		if !eol {
+			if truncated {
+				return nil, true, fmt.Errorf("Frontmatter länger als %d KiB", MaxBytes/1024)
+			}
+			return nil, true, errors.New("Frontmatter nicht geschlossen: keine zweite Zeile ---")
+		}
+		rest = after
+	}
+}
`````

Die Umwandlung des Knotenbaums (Aliase werden beim Gang aufgelöst, die Tiefe ist begrenzt):

`````diff
+// maxDepth begrenzt die Verschachtelung; ein Alias, der auf sich selbst
+// zeigt, liefe sonst endlos.
+const maxDepth = 100
+
+func value(n *yaml.Node, depth int) (any, error) {
+	if depth > maxDepth {
+		return nil, errors.New("zu tief verschachtelt")
+	}
+	switch n.Kind {
+	case yaml.DocumentNode:
+		if len(n.Content) == 0 {
+			return nil, nil
+		}
+		return value(n.Content[0], depth+1)
+	case yaml.AliasNode:
+		return value(n.Alias, depth+1)
+	case yaml.MappingNode:
+		out := make(map[string]any, len(n.Content)/2)
+		for i := 0; i+1 < len(n.Content); i += 2 {
+			k := n.Content[i]
+			if k.Kind != yaml.ScalarNode || k.ShortTag() != "!!str" {
+				return nil, fmt.Errorf("Schlüssel %s ist kein Text", k.Value)
+			}
+			if _, dup := out[k.Value]; dup {
+				return nil, fmt.Errorf("Schlüssel %s steht doppelt", k.Value)
+			}
+			v, err := value(n.Content[i+1], depth+1)
+			if err != nil {
+				return nil, err
+			}
+			out[k.Value] = v
+		}
+		return out, nil
+	case yaml.SequenceNode:
+		out := make([]any, 0, len(n.Content))
+		for _, c := range n.Content {
+			v, err := value(c, depth+1)
+			if err != nil {
+				return nil, err
+			}
+			out = append(out, v)
+		}
+		return out, nil
+	}
+	return scalar(n)
+}
`````

Das Eintragen in die Seite von `list` (`internal/node/mcpnode/frontmatter.go`):

`````diff
+func fillFrontmatter(ctx context.Context, a *hubAccess, t target, entries []ListEntry) error {
+	for i := range entries {
+		e := &entries[i]
+		var err error
+		switch e.Kind {
+		case KindDocument:
+			e.Frontmatter, e.FrontmatterError, err = documentFrontmatter(ctx, a.rep, e.Name, e.ID, nil)
+		case KindDirectory:
+			e.Frontmatter, e.FrontmatterError, err = readmeFrontmatter(ctx, a.rep, t.Collection, e.Name+"/")
+		case KindCollection:
+			if hub, _, _ := strings.Cut(e.Address, ":"); hub != a.hub {
+				continue
+			}
+			e.Frontmatter, e.FrontmatterError, err = readmeFrontmatter(ctx, a.rep, e.Name, "")
+		}
+		if err != nil {
+			return err
+		}
+	}
+	return nil
+}
`````

Die drei Zweige von `doList` (`internal/node/mcpnode/list.go`), jeweils nach dem Blättern:

`````diff
@@ -206,6 +216,16 @@ func (n *Node) doList(ctx context.Context, req *mcp.CallToolRequest, in ListInpu
 		pageCollections(pg, colls)
 		out := pg.finish()
+		if in.Frontmatter && len(out.Entries) > 0 {
+			// Ein zweiter Gang über die Hubs, nur für die Einträge der Seite.
+			more, err := r.eachValid(ctx, func(a *hubAccess) error {
+				return fillFrontmatter(ctx, a, target{Hub: a.hub}, out.Entries)
+			})
+			if err != nil {
+				return ListOutput{}, err
+			}
+			unread = mergeSorted(unread, more)
+		}
 		if len(unread) > 0 {
 			out.UnreadableHubs = unread
 		}
@@ -225,7 +245,13 @@ func (n *Node) doList(ctx context.Context, req *mcp.CallToolRequest, in ListInpu
 		pageCollections(pg, collectionEntries(a))
-		return pg.finish(), nil
+		out := pg.finish()
+		if in.Frontmatter {
+			if err := a.wrap(ctx, fillFrontmatter(ctx, a, t, out.Entries)); err != nil {
+				return ListOutput{}, err
+			}
+		}
+		return out, nil
 	}
@@ -234,7 +260,26 @@ func (n *Node) doList(ctx context.Context, req *mcp.CallToolRequest, in ListInpu
 	if err := a.wrap(ctx, listDocuments(ctx, a, t, prefix, pg)); err != nil {
 		return ListOutput{}, err
 	}
-	return pg.finish(), nil
+	out := pg.finish()
+	if in.Frontmatter {
+		// Nach dem Blättern, nur für die Einträge der Seite.
+		if err := a.wrap(ctx, fillFrontmatter(ctx, a, t, out.Entries)); err != nil {
+			return ListOutput{}, err
+		}
+	}
+	return out, nil
+}
`````

Die Replica liest den Anfang in Bytes (`internal/node/replica/read.go`):

`````diff
+	qHeadByName = `SELECT substr(CAST(content AS BLOB), 1, ?) FROM documents
+		WHERE collection = ? AND name = ? AND deleted = 0 AND ` + notSystem + `
+		ORDER BY revision DESC, id DESC LIMIT 1`
+	qHeadByID = `SELECT substr(CAST(content AS BLOB), 1, ?) FROM documents WHERE id = ? AND deleted = 0 AND ` + notSystem
...
+func (r *Replica) HeadByName(ctx context.Context, collection, name string, n int) (head []byte, ok bool, err error) {
+	if err := ident.CheckDocName(name); err != nil {
+		return nil, false, err
+	}
+	return r.head(ctx, name, qHeadByName, n, collection, name)
+}
+
+func (r *Replica) HeadByID(ctx context.Context, id string, n int) (head []byte, ok bool, err error) {
+	return r.head(ctx, id, qHeadByID, n, id)
+}
+
+func (r *Replica) head(ctx context.Context, what, query string, n int, args ...any) ([]byte, bool, error) {
+	var head []byte
+	err := r.db.QueryRowContext(ctx, query, append([]any{n}, args...)...).Scan(&head)
+	if errors.Is(err, sql.ErrNoRows) {
+		return nil, false, nil
+	}
+	if err != nil {
+		return nil, false, fmt.Errorf("Anfang von Dokument %s lesen: %w", what, err)
+	}
+	return head, true, nil
+}
`````

### Review: Befunde

**Umfang:** ab Task-Beginn  
**Rezept:** `/home/kleist/dev/kephalaion/k-playbook/reviews/review-code.md`  
**Beleg für Build und Tests:** übergeben (`go test ./internal/frontmatter`, `go test ./internal/node/replica -run TestHead`, `go test -short ./internal/node/mcpnode`, `make check-quick`, `make check` → grün; MCP-Durchlauf per curl → wie erwartet)  
**Ergebnis:** 0 kritisch, 1 wichtig, 0 Hinweis

| Einstufung | Ort | Grundlage | Befund |
|---|---|---|---|
| wichtig | `internal/frontmatter/frontmatter.go:126` | `code-unbounded-work` | `value` löst jeden `AliasNode` erneut vollständig auf, und `maxDepth` (100) begrenzt nur die Tiefe, nicht den Fächer: Ein Frontmatter nach dem Muster „billion laughs“ (`l0: &l0 [x,…]`, `l1: &l1 [*l0 ×9]`, …) wächst je Ebene um den Faktor 9 — in einer Probe mit einer Kopie des Pakets kosteten 422 Bytes (7 Ebenen) 1,7 GiB und 1,4 s, 8 Ebenen wären ~15 GiB; die 64-KiB-Grenze lässt hunderte Ebenen zu, so dass ein einzelnes vom Hub repliziertes Dokument `serve` bei `list`/`read` mit `frontmatter: true` in den Speicher laufen lässt. yaml.v3 fängt dieselbe Eingabe beim direkten Decodieren nach `any` ab („document contains excessive aliasing“, `decode.go:489`); der Gang über den Knotenbaum umgeht diesen Schutz. Ein Budget für aufgelöste Knoten (oder Aliase gar nicht auflösen) fehlt; der Test „Alias auf sich selbst“ (`frontmatter_test.go:65`) deckt nur den Zyklus. |

**Intent-Alignment:** Ja - `list` und `read` liefern per Parameter `frontmatter` das YAML-Frontmatter als JSON-Objekt für `.md`-Dokumente sowie für Verzeichnisse und Collections über deren `README.md`; das Frontmatter bleibt im Inhalt, Hub, Vertrag, Schemata und Erweiterung sind unverändert, fehlerhaftes YAML landet in `frontmatter_error`, ohne den Parameter bleiben die Antworten unverändert. Ein wichtiger Review-Befund bleibt dokumentiert.
