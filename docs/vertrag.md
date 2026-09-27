# Vertrag zwischen Node und Hub

Fassung 1. Dieser Text ist verbindlich; der Code folgt ihm. Im Code steht der Vertrag im
neutralen Paket `internal/contract` (Typen, Fehler, Schnittstelle `Hub`). Der Hub setzt die
Schnittstelle um (`internal/hub/replication`), der Node benutzt sie und kennt nur sie. Welche
Umsetzung er bekommt, entscheidet `cmd/kephalaion`: `local` ist ein Funktionsaufruf im selben
Prozess, `http` ist HTTP mit JSON (`internal/contract/httpapi`, siehe „HTTP“). Beide prüfen
dasselbe; die Tests des Vertrags laufen gegen beide.

Die Vorgänge: `whoami` (wer bin ich, gilt dieser Account und wem gehört er), `rotate` (Token
eines Accounts ersetzen), `sync` (Abgleich) und die Schreibvorgänge `create`, `write`, `delete`
und `rename` (ein Dokument anlegen, ersetzen, löschen, umbenennen — `delete` und `rename` auch
ein Verzeichnis als Ganzes —, im Namen eines Accounts). Begriffe:
[`begriffe.md`](begriffe.md); Hintergrund: [`konzept.md`](konzept.md), „Abgleich“,
„Authentifizierung“ und „Transport, Token und Fehlschläge“.

## Fassung

Jede Anfrage nennt die Fassung des Nodes, jede Antwort die des Hubs. Fassung 1 ist die
einzige. In Go ist sie ein Feld jeder Anfrage (`Version`), über HTTP steht sie im
Pfad (`/v1/…`) und nicht im Body. Eine Fassung, die der Hub nicht kennt oder nicht bedient,
beantwortet er mit `unsupported_version`, noch vor der Anmeldung.

`user` in `whoami` und in den Account-Zeilen kam mit Task 006 ohne neue Fassung dazu: Es gibt
keinen ausgelieferten Node mit Vertrag (v0.1.0 und v0.1.1 liegen vor Task 005). Ebenso kamen die
Schreibvorgänge mit Task 014 hinzu. Ein Hub, der älter ist als sie, kennt sie nicht und
antwortet wie auf jeden unbekannten Vorgang (über HTTP 404 mit `invalid`); ausgeführt hat er
nichts. Der Transport meldet das als eigenen Fall (`contract.ErrUnknownOperation`), nicht als
`invalid`, und der Node meldet, dass der Hub noch nicht schreiben kann.

## Anmeldung

Jede Anfrage trägt den Namen des Nodes am Hub und sein Token (`NodeAuth`). Über HTTP stehen
beide in Headern, nicht im Body; das Token steht nie in einem Log.

Der Hub schlägt den Node über seinen Namen nach, hasht das vorgelegte Token (sha256) und
vergleicht es in konstanter Zeit mit dem gespeicherten Hash. Gibt es den Node nicht, vergleicht
er gegen einen festen Ersatz-Hash, damit die Arbeit dieselbe ist. Unbekannter Node, falsches
Token und gesperrter Node ergeben dieselbe Antwort: `unauthenticated`. Der Hub prüft bei jedem
Aufruf, auch auf dem lokalen Weg.

## Anmeldung eines Accounts

`whoami`, `rotate` und die Schreibvorgänge tragen zusätzlich einen Account: seinen Namen und
sein Token, im Body (der Node ist der Träger, der Account sagt, in wessen Namen) — bei `whoami`
und den Schreibvorgängen als Objekt `account` (`{"account": <name>, "token": <token>}`), bei
`rotate` als eigene Felder. Der Hub prüft gegen seine Tabelle
`accounts` — dort steht der maßgebliche Hash, auch für einen gesperrten Account —, hasht das
Token und vergleicht in konstanter Zeit; einen unbekannten Account vergleicht er gegen einen
festen Ersatz-Hash. Unbekannt, falsches Token und gesperrt sind dieselbe Antwort.

## whoami

Bestätigt den Node und nennt, was er abgleichen darf. Mit Account-Teil prüft es zusätzlich den
Account — so prüft ein Node nach einem unklaren `rotate`, welches Token gilt.

| Feld | JSON | Bedeutung |
|---|---|---|
| Fassung, Node, Token | — (Pfad, Header) | wie bei `sync` |
| Account (wahlweise) | `account` | `{"account": <name>, "token": <token>}` |

Antwort:

| Feld | JSON | Bedeutung |
|---|---|---|
| Hub-Kennung | `hub_id` | die `hub_id` des Hubs |
| Fassung | `version` | 1 |
| Node | `node` | der Name des Nodes am Hub |
| Erlaubte Collections | `allowed` | alle Collections, die der Node abgleichen darf, sortiert |
| Account | `account` | nur mit Account-Teil: `{"account", "valid", "user", "collections"}`. `valid` falsch heißt unbekannt, falsches Token oder gesperrt — dieselbe Antwort, kein Fehler. `user` ist der User des Accounts (aus `accounts`), nur wenn `valid` wahr ist, sonst leer (`""`). `collections` sind die Collections des Accounts, die dieser Node abgleichen darf, sortiert; leer, wenn `valid` falsch ist. |

## rotate

Ersetzt das Token eines Accounts. Der Node erzeugt das neue Token, schickt nur seinen Hash und
behält das Token selbst; zur Anmeldung dient das alte.

| Feld | JSON | Bedeutung |
|---|---|---|
| Fassung, Node, Token | — (Pfad, Header) | wie bei `sync`; der Node ist der Träger |
| Account | `account` | Name des Accounts |
| Altes Token | `token` | das bisherige Token des Accounts |
| Neuer Hash | `new_hash` | sha256 des neuen Tokens, 64 Zeichen hex |

Der Hub prüft in dieser Reihenfolge: Fassung, Form von `new_hash` (sonst `invalid`),
Anmeldung des Nodes, altes Token gegen `accounts` (gesperrt gilt nicht:
`account_unauthenticated`). Hat der Account keine der Collections, die der Node abgleichen
darf, antwortet er `no_shared_collection` — **vor** jeder Änderung. Sonst ersetzt er den Hash in
`accounts` und in allen Zeilen des Accounts in **einer** Transaktion. Sie beginnt mit dem
bedingten Schreiben in `accounts` (nur wenn das alte Token noch gilt und der Account nicht
gesperrt ist) — so gelingt von zwei gleichzeitigen `rotate` mit demselben alten Token nur einer.
Es ist ein Schreibvorgang mit
einer Revision und genau einer Zeile in `actions` (`account` = der Account, `carrier` = der
Node, `action` = `rotate`, `subject` = der Account). Der User bleibt, wie er ist: Der Hub
liest ihn nach dem bedingten Schreiben in derselben Transaktion aus `accounts`; er steht im
Inhalt der Zeilen und in ihrem `updated_by`. Fehlversuche stehen nicht in `actions`, nur im Log
des Hubs (ohne Token).

Antwort: `hub_id`, `version` und `rows` — die Account-Zeilen mit dem neuen Hash, beschränkt auf
die Collections, die der Node abgleichen darf, nach Collection; Form wie bei `sync`. Den User
nennt der Inhalt jeder Zeile (`user`, siehe „Account-Zeilen“); der Node übernimmt die Zeilen
samt User in seine Replica. Alles, was die Antwort braucht, liest der Hub **vor** dem Commit;
danach stellt er sie nur noch zusammen.

**Nicht wiederholbar.** Nach einem erfolgreichen `rotate` gilt das alte Token nicht mehr; ein
zweiter Versuch mit ihm scheitert. Ein Transport wiederholt `rotate` deshalb nie (siehe
„Ausgang und Wiederholung“). Ist der Ausgang unklar (`contract.ErrOutcomeUnknown`), prüft der
Node mit `whoami`, welches Token gilt.

## sync

### Anfrage

| Feld | JSON | Bedeutung |
|---|---|---|
| Fassung | — (Pfad) | Fassung des Nodes, 1 |
| Node, Token | — (Header) | Anmeldung, siehe oben |
| Collections | `collections` | Liste von Paaren (`collection`, `since`): alles aus dieser Collection mit `revision > since`. Jede Collection höchstens einmal, `since ≥ 0`; eine neue Collection fragt ab 0. Die Liste darf leer sein. |
| Seitengröße | `page_size` | gewünschte Zeilen je Seite, > 0. Standard des Nodes: 500 (`contract.DefaultPageSize`). |

### Antwort

| Feld | JSON | Bedeutung |
|---|---|---|
| Hub-Kennung | `hub_id` | die `hub_id` aus `db_info` des Hubs |
| Fassung | `version` | Fassung der Antwort, 1 |
| Status je Collection | `collections` | jede angefragte Collection in der Reihenfolge der Anfrage, mit `allowed` wahr oder falsch. Eine unbekannte Collection und eine nicht erlaubte sind dieselbe Antwort (falsch). |
| Erlaubte Collections | `allowed` | alle Collections, die der Node abgleichen darf (`node_collections`), sortiert — auch nicht angefragte |
| Zeilen | `rows` | siehe unten |
| Hub-Revision | `hub_revision` | die Revision H des Hubs |
| bis | `until` | die Revision, bis zu der der Node nach dieser Seite alles hat |
| mehr | `more` | es folgen weitere Zeilen; der Node fragt ab `until` weiter |

**Zeilen.** Aus jeder angefragten und erlaubten Collection alle Zeilen von `documents` mit
`revision > since` dieser Collection und `revision ≤ H`, sortiert nach Revision, dann nach
`id`. Jede Zeile trägt alle Spalten, so wie sie am Hub steht:

| Spalte | JSON | Typ |
|---|---|---|
| `id` | `id` | Text |
| `collection` | `collection` | Text |
| `name` | `name` | Text |
| `content` | `content` | Text oder `null` (Löschmarke) |
| `meta` | `meta` | Text (JSON als Text, der Hub deutet es nicht) oder `null` |
| `deleted` | `deleted` | wahr oder falsch |
| `revision` | `revision` | Zahl |
| `created_at`, `updated_at` | `created_at`, `updated_at` | Zahl, ms seit Epoche |
| `created_by`, `updated_by` | `created_by`, `updated_by` | Text |

Auch Löschmarken und `SYSTEM:`-Zeilen kommen mit (Account-Zeilen siehe unten). `null` ist ausdrücklich: Der Node speichert
genau die Zeile des Hubs und leitet `content` nicht aus `deleted` ab. In Go sind die nullbaren
Spalten Zeiger (`*string`, `nil` = NULL).

**Lesestand ohne Transaktion.** Der Hub liest H zuerst und liefert danach nur Zeilen mit
`revision ≤ H`. Was während der Anfrage geschrieben wird, trägt eine Revision über H und kommt
mit der nächsten Seite. So passen Seite und H zusammen, ohne Lese-Transaktion.

### Seiten

- **Eine Seite endet an einer Revisionsgrenze.** Sie nimmt ganze Revisionen, bis die
  Seitengröße erreicht ist; die Revision, die über die Seitengröße hinausreichte, bleibt für
  die nächste Seite. Ein Schreibvorgang mit vielen Zeilen kommt deshalb ganz oder gar nicht an.
- **Eine einzelne Revision, die größer ist als die Seitengröße, kommt ganz**, als eigene
  Seite.
- **`until`:** Gibt es mehr (`more` wahr), ist `until` die Revision der letzten Zeile der
  Seite. Ist es die letzte Seite (`more` falsch), ist `until` = H — auch bei einer leeren
  Seite: Der Node hat dann alles bis H.
- **Seitengröße:** ≤ 0 ist `invalid`. Über der Obergrenze des Hubs (5000 Zeilen) begrenzt er
  sie still darauf.

### Regeln für den Node

- **Die Revision ist global**, über alle Collections des Hubs. Nach jeder Seite setzt der Node
  je angefragter Collection ihren Stand auf max(`since`, `until`) — nie zurück. Eine
  Collection, die schon weiter war als `until`, bleibt, wo sie war.
- Jede Seite wird in einer Transaktion angewendet (Zeilen per `id` einfügen oder ersetzen,
  Stände fortschreiben). Ein abgebrochener Abgleich setzt beim letzten Stand fort.
- **`hub_id`:** Beim ersten Kontakt merkt der Node sie sich. Weicht sie später ab, verwirft er
  die Replica und gleicht von vorn ab.
- **`since` über H:** Liegt ein `since` über `hub_revision`, wurde der Hub aus einer Sicherung
  mit gleicher `hub_id` zurückgespielt. Der Node behandelt das wie einen Wechsel der `hub_id`.
  Das greift nur, bis der Hub wieder über dieses `since` hinaus geschrieben hat. **Grenze:** Ein
  aus einer Sicherung zurückgespielter Hub braucht deshalb eine neue `hub_id`; bis es dafür ein
  Kommando gibt, verwirft der Node die Replica selbst (`node hub rm` und `node hub add`).
- Collections mit `allowed` falsch — nicht erlaubt, nicht mehr erlaubt oder unbekannt — entfernt
  der Node aus seiner Replica.

## Account-Zeilen

Je Account und Collection steht in `documents` eine Zeile mit dem Namen `SYSTEM:A:<account>`
(`contract.AccountRowPrefix`). Sie gleicht sich ab wie jede andere Zeile; der Node prüft seine
Clients gegen sie. `content` ist JSON in genau dieser Form:

```json
{"hash":"<sha256 des Tokens, 64 Zeichen hex>","user":"<user>","rights":{"write":false,"supersede":false}}
```

- `hash` ist eine Kopie; maßgeblich führt der Hub den Hash in seiner Tabelle `accounts`.
- `user` ist der User des Accounts, ebenfalls eine Kopie aus `accounts`; alle lebenden Zeilen
  eines Accounts tragen denselben. Ändert der Admin ihn (`hub account set --user`), schreibt
  der Hub alle lebenden Zeilen des Accounts unter **einer** Revision neu; Löschmarken bleiben.
  Eine Zeile ohne `user` (oder mit leerem) ist ein Fehler dieser Zeile: Der Node zählt sie
  nicht — der Account ist mit ihr nicht angemeldet —, ohne abzubrechen. Solche Zeilen stammen
  von einem Hub vor Task 006; ein neu angelegter Hub hat eine neue `hub_id`, und der Node
  verwirft die Replica beim nächsten Kontakt.
- `created_by` ist `admin`; `updated_by` ist `admin`, nach `rotate` der User des Accounts.
- `rights` sind die Rechte in dieser Collection über `read` hinaus; `read` ergibt sich aus der
  Zeile selbst. `write` und `supersede` sind unabhängig.
- Sperren, Entziehen und Entfernen machen die Zeile zur Löschmarke (`content` NULL). Bekommt
  der Account die Collection wieder, wird die Löschmarke mit neuer Revision wiederbelebt; ihre
  `id` bleibt.
- `meta` ist immer NULL.

## Schreibvorgänge

`create`, `write`, `delete` und `rename` schreiben im Namen eines Accounts; der Node ist der
Träger. Ob geschrieben werden darf, entscheidet allein der Hub — so wirkt eine Sperre beim
Schreiben sofort. Jeder Vorgang ist eine Transaktion mit höchstens einer Revision. `delete` und
`rename` nehmen auch ein **Verzeichnis** — einen Namen, unter dem lebende Dokumente liegen
(`<name>/…`) —, als Ganzes: alle Dokumente darunter, alles oder nichts, eine Revision.

### Felder

| Feld | JSON | Bedeutung |
|---|---|---|
| Fassung, Node, Token | — (Pfad, Header) | wie bei `sync`; der Node ist der Träger |
| Account | `account` | `{"account": <name>, "token": <token>}`, wie bei `whoami` |
| Collection | `collection` | die Collection am Hub |
| Name | `name` | der Name des Dokuments, bei `delete` und `rename` auch eines Verzeichnisses |
| Inhalt | `content` | nur `create` und `write`, Pflicht: der ganze Inhalt, auch leer (`""`) |
| Vorbedingung | `base_revision` | nur `write`, `delete` und `rename`, wahlweise: die Revision, auf der der Vorgang beruht; nur für ein Dokument |
| Rekursiv | `recursive` | nur `delete`, wahlweise, Standard falsch: ein Verzeichnis mit allen Dokumenten darunter löschen |
| Neuer Name | `new_name` | nur `rename`, Pflicht: der neue Name in derselben Collection |

### Prüfung

Der Hub prüft in dieser Reihenfolge, wie bei `rotate`:

1. **Fassung** (`unsupported_version`).
2. **Form der Anfrage**, ohne Datenbank (`invalid`): der Name nach den Pfadregeln
   (`ident.CheckDocName`, kein `SYSTEM:`) — die Wurzel einer Collection (`""`) ist kein Name,
   `delete` löscht sie nie; der Inhalt UTF-8 ohne NUL-Byte, höchstens 1 MiB
   (`contract.MaxDocumentBytes`), leer ist erlaubt; `base_revision`, wenn angegeben, ≥ 1 — eine
   Revision 0 gibt es nicht; bei `rename` der neue Name nach denselben Regeln, weder gleich dem
   alten noch darunter (`x` nach `x/y`; `ident.CheckRename`). Die Collection gehört nicht dazu:
   Eine ungültige gibt es nicht, das ist `not_readable`.
3. **Anmeldung des Nodes** (`unauthenticated`).
4. Der Rest in der Transaktion des Vorgangs. Ihre erste Anweisung sperrt die Zeile des Accounts
   in `accounts`; danach:
   - **Account** gegen `accounts` wie oben — unbekannt, falsches Token, gesperrt:
     `account_unauthenticated`.
   - **Lesbarkeit:** Die Collection gibt es, der Node darf sie abgleichen (`node_collections`),
     und der Account hat eine lebende `SYSTEM:A:`-Zeile in ihr. Sonst `not_readable`, dieselbe
     Antwort für alle drei.
   - **Recht** aus dieser Zeile: `write` für `create` und für Eigenes — `created_by` ist der
     User des Accounts, gleich über welchen seiner Accounts es angelegt wurde —, `supersede`
     für Fremdes; `write` ist dafür nicht nötig. Bei einem Verzeichnis gilt das je Dokument
     darunter; ein einziges verbotenes lässt den ganzen Vorgang scheitern. Sonst `forbidden`,
     die Meldung nennt den Grund („gehört admin, supersede fehlt“).
   - **Name und Vorbedingung** je Vorgang, siehe unten. `base_revision` weicht von der Revision
     des lebenden Dokuments ab: `stale_revision`, die Meldung nennt die aktuelle. Die Revision
     ist global und steigt nur, Gleichheit genügt. Ohne `base_revision` gilt keine
     Vorbedingung.

**Urheber:** `created_by`/`updated_by` ist der User des Accounts. `actions` bekommt je Dokument
eine Zeile: `account` = der Account, `carrier` = der Node, `action` = `create`, `update`,
`delete` oder `rename`, dazu `document_id` und die Revision des Vorgangs — bei einem
Verzeichnis je Dokument darunter eine, alle unter derselben Revision. Fehlversuche stehen nicht
in `actions`, nur im Log des Hubs (ohne Token, ohne Inhalt).

### create

Legt ein Dokument an. Trägt ein lebendes Dokument den Namen: `name_taken`. Eine Löschmarke
unter dem Namen hindert nicht; das Dokument bekommt eine neue `id`. Wäre der Name zugleich
Datei und Verzeichnis (`x` gibt es und `x/y` soll entstehen, oder umgekehrt):
`path_conflict`.

### write

Ersetzt den Inhalt eines lebenden Dokuments; gibt es keines: `not_found`. Dann das Recht, dann
`base_revision` — geprüft vor dem Vergleich des Inhalts. Ist der Inhalt unverändert, schreibt
der Hub nichts: keine neue Revision, die Antwort trägt die bestehende Zeile und ihre Revision.

### delete

Setzt eine Löschmarke auf ein lebendes Dokument (Inhalt `null`, `deleted` wahr, neue Revision).
Recht und `base_revision` wie bei `write`; `recursive` gilt für ein Dokument nicht — es wird
gelöscht wie ohne, kein Fehler.

Ist `name` ein **Verzeichnis**, nur mit `recursive` wahr, sonst `invalid` („ist ein
Verzeichnis“): Dann bekommen alle lebenden Dokumente darunter eine Löschmarke, unter einer
Revision, alles oder nichts. `base_revision` gibt es für ein Verzeichnis nicht (`invalid`). Ist
`name` weder Dokument noch Verzeichnis — auch wenn darunter nur Löschmarken liegen —:
`not_found`.

Reihenfolge nach der Lesbarkeit: `not_found`; bei einem Verzeichnis `recursive` und
`base_revision` (`invalid`); das Recht; bei einem Dokument `base_revision`.

### rename

Gibt einem lebenden Dokument den Namen `new_name`, in derselben Collection. Die `id` bleibt,
ebenso Inhalt, `meta` und `created_by`; der Name ist neu, dazu Revision, `updated_at` und
`updated_by`. Recht und `base_revision` wie bei `write`.

Ist `name` ein **Verzeichnis**, bekommen alle lebenden Dokumente darunter den neuen Präfix
(`name/a/b.md` → `new_name/a/b.md`), unter einer Revision, alles oder nichts; jeder neue Name
muss den Pfadregeln folgen (sonst `invalid`, etwa zu lang). `base_revision` gibt es für ein
Verzeichnis nicht (`invalid`). Weder Dokument noch Verzeichnis: `not_found`. Über Collections
oder Hubs hinweg geht `rename` nicht.

**Das Ziel** — geprüft im Stand vor dem Umbenennen; liegt die Quelle im Ziel (`x/y` nach `x`),
belegt sie es selbst:

- **Belegt** ist es für ein Dokument, wenn ein lebendes Dokument `new_name` heißt, für ein
  Verzeichnis, wenn es das Verzeichnis `new_name` schon gibt: `name_taken`. Nichts wird
  überschrieben, zwei Verzeichnisse werden nicht zusammengelegt. Eine Löschmarke unter dem
  Namen hindert nicht.
- **Datei und Verzeichnis zugleich** wie bei `create`: ein Dokument auf ein Verzeichnis, ein
  Verzeichnis auf ein Dokument, oder ein Dokument über `new_name`: `path_conflict`.

Reihenfolge nach der Lesbarkeit: `not_found`; bei einem Verzeichnis die neuen Namen und
`base_revision` (`invalid`); das Recht; bei einem Dokument `base_revision`; zuletzt das Ziel.

### Antwort

| Feld | JSON | Bedeutung |
|---|---|---|
| Hub-Kennung | `hub_id` | die `hub_id` des Hubs |
| Fassung | `version` | 1 |
| Revision | `revision` | die Revision des Vorgangs; bei `write` mit unverändertem Inhalt die bestehende |
| Zeilen | `rows` | die geschriebenen Zeilen in der Form von `sync` — das Dokument, bei `delete` die Löschmarke, bei `rename` die Zeile unter dem neuen Namen; bei einem Verzeichnis alle Dokumente darunter, nach Name |

Ob ein Vorgang ein Dokument oder ein Verzeichnis traf, zeigen die Zeilen: ein Dokument ist genau
eine Zeile unter `name` (bei `rename` unter `new_name`), ein Verzeichnis sind Zeilen, die alle
darunter liegen. Nach `rename` ersetzt der Node die Zeilen per `id` — der alte Name ist damit
aus seiner Replica verschwunden.

Die Antwort ist die Wahrheit, nicht die Anfrage: Der Node übernimmt `rows` in seine Replica.
Alles, was sie braucht, liest der Hub vor dem Commit — die `hub_id` vor der Transaktion, die
Zeilen in ihr; danach stellt er sie nur noch zusammen.

## Ausgang und Wiederholung

`whoami` und `sync` ändern nichts; ein Transport darf sie wiederholen (über HTTP siehe
„Wiederholung“). `rotate` und die Schreibvorgänge schickt ein Transport **genau einmal**, nie
ein zweites Mal: Nach `rotate` gilt das alte Token nicht mehr, und ein zweiter Schreibversuch
ergäbe `name_taken`, `not_found` oder einen Konflikt mit sich selbst. Einen Schlüssel, an dem
der Hub eine Wiederholung erkennt, gibt es nicht. Ihr Ausgang ist einer von vier:

- **Erfolg** — die Antwort.
- **Abgelehnt** — ein Fehler des Vertrags (ein Code unten), endgültig. Der Hub hat nichts
  geändert.
- **Nicht erreicht** — die Anfrage hat den Hub nachweislich nicht erreicht: Die Verbindung kam
  nicht zustande, oder er antwortete mit einer Weiterleitung. Nichts ist geschehen. Ebenso
  **unbekannter Vorgang**: Der Hub kennt ihn nicht (siehe „Fassung“) und hat nichts ausgeführt.
- **Unklar** — jeder andere Fehler nach dem Abschicken: Zeitüberschreitung, abgebrochene
  Verbindung, unlesbare Antwort, 5xx; über `local` jeder Fehler, der kein Fehler des Vertrags
  ist, auch einer der Datenbank nach dem Commit. Der Hub kann ausgeführt haben. Nach `rotate`
  prüft der Node mit `whoami`; nach einem Schreibvorgang stößt er den Abgleich an, und der
  Aufrufer sieht nach.

In Go: ein `*contract.Error` ist abgelehnt, `contract.ErrOutcomeUnknown` unklar,
`contract.ErrUnknownOperation` der unbekannte Vorgang; jeder andere Fehler heißt nicht
erreicht. Das sichert jeder Transport zu — über `local` hüllt `cmd/kephalaion` (`localHub`)
jeden Fehler, der kein Fehler des Vertrags ist, in `contract.ErrOutcomeUnknown`.

## Fehler

Ein Fehler hat einen Code und eine Meldung (`contract.Error`, über HTTP als JSON
`{"code": …, "message": …}`). Nach einem Fehler gibt es keine Antwortdaten, und der Hub hat
nichts geändert.

| Code | HTTP | Bedeutung |
|---|---|---|
| `unauthenticated` | 401 | nicht angemeldet: Node unbekannt, Token falsch oder Node gesperrt — dieselbe Meldung für alle drei |
| `account_unauthenticated` | 403 | der Node ist angemeldet, der Account nicht: unbekannt, Token falsch oder gesperrt (`rotate`, Schreibvorgänge) |
| `no_shared_collection` | 409 | der Account hat keine der Collections, die der Node abgleichen darf (`rotate`) |
| `not_readable` | 403 | die Collection gibt es nicht, der Node darf sie nicht abgleichen, oder der Account hat keine lebende Zeile in ihr — dieselbe Meldung für alle drei (Schreibvorgänge) |
| `forbidden` | 403 | dem Account fehlt das Recht: `write` für Neues und Eigenes, `supersede` für Fremdes; die Meldung nennt den Grund |
| `not_found` | 404 | kein lebendes Dokument mit dem Namen (`write`), bei `delete` und `rename` auch kein Verzeichnis |
| `name_taken` | 409 | ein lebendes Dokument trägt den Namen schon (`create`, `rename`), oder das Verzeichnis gibt es schon (`rename` eines Verzeichnisses) |
| `path_conflict` | 409 | der Name wäre zugleich Datei und Verzeichnis (`create`, `rename`) |
| `stale_revision` | 409 | das Dokument hat nicht die Revision `base_revision`; die Meldung nennt die aktuelle |
| `invalid` | 400 | ungültige Anfrage: Seitengröße ≤ 0, Collection doppelt, `since` negativ, `new_hash` kein sha256, Name oder Inhalt ungültig, `content` fehlt, `base_revision` < 1, kein gültiges JSON; ein Verzeichnis ohne `recursive` (`delete`), `base_revision` bei einem Verzeichnis, `new_name` gleich `name` oder darunter; über HTTP auch 404 (unbekannter Vorgang), 405 (nicht POST), 413 (Body zu groß) |
| `unsupported_version` | 404 | Fassung nicht unterstützt |

Fehler des Transports oder der Datenbank sind keine Fehler des Vertrags. Nach `whoami` und
`sync` versucht der Node es später wieder; `rotate` und die Schreibvorgänge wiederholt niemand,
ihr Ausgang ist dann unklar (siehe „Ausgang und Wiederholung“). Über HTTP antwortet der Hub mit
500 und dem Code `internal`, der kein Code des Vertrags ist.

## HTTP

- **Pfad:** `POST /v<Fassung>/<Vorgang>`, also `/v1/whoami`, `/v1/rotate`, `/v1/sync`,
  `/v1/create`, `/v1/write`, `/v1/delete`, `/v1/rename`. Die Fassung im Pfad ist die Fassung
  des Vertrags. Eine fremde Fassung (`/v2/…`, auch `/v0/…`) beantwortet der Hub mit 404 und
  `unsupported_version`, vor der Anmeldung; ein unbekannter Vorgang ist 404 mit `invalid`, noch
  vor dem Lesen des Bodys; eine andere Methode als POST 405.
- **Anmeldung des Nodes** in Headern: `X-Keph-Node: <name>` und `Authorization: Bearer
  <token>`. Der Body ist JSON (die Felder oben); ein leerer Body gilt als `{}`. `content` ist
  bei `create` und `write` Pflicht; fehlt es oder ist es `null`, ist die Anfrage `invalid` —
  sonst legte ein vergessenes Feld still ein leeres Dokument an oder leerte eines.
- **UTF-8:** JSON ist UTF-8; ein Body mit ungültigem UTF-8 ist kein gültiges JSON (`invalid`).
  `encoding/json` ersetzte solche Bytes still durch U+FFFD, der Hub schriebe einen anderen Namen
  oder Inhalt als gemeint. Der Client schickt deshalb einen Schreibvorgang, dessen Name, neuer
  Name oder Inhalt kein gültiges UTF-8 ist, nicht ab und meldet `invalid` — dieselbe Antwort
  wie über `local`.
- **Antwort:** 200 mit JSON, gzip-komprimiert, wenn die Anfrage `Accept-Encoding: gzip` trägt
  (der Client des Nodes bittet immer darum). Fehler: Status nach der Tabelle oben, Body
  `{"code": …, "message": …}`. Der Client unterscheidet Fehler am Code im Body, nicht am
  Status: 404 ist `not_found`, `unsupported_version` oder mit `invalid` ein unbekannter
  Vorgang. Diesen meldet er als `contract.ErrUnknownOperation`, nicht als `invalid`.
- **Grenzen:** Der Body einer Anfrage an `whoami`, `rotate` und `sync` darf höchstens 1 MiB
  groß sein, der eines Schreibvorgangs 7 MiB (`httpapi.MaxWriteBodyBytes`); sonst 413. Die
  größere Grenze trägt jedes Dokument, das der Hub annimmt (1 MiB), auch wenn JSON jedes Byte
  als `\u00XX` schreibt (sechs Byte je Byte), dazu 1 MiB für alles andere. Einen zu großen
  Inhalt lehnt der Hub selbst ab (`invalid`). Antworten sind nicht begrenzt — eine Seite des
  Abgleichs kann eine große Revision ganz tragen. Der Server liest Kopf und Body innerhalb von
  10 s bzw. 60 s und darf eine Antwort bis zu 10 Minuten lang schreiben. Der Client wartet auf
  `whoami`, `rotate` und die Schreibvorgänge 30 s, auf eine Seite von `sync` 10 Minuten.
- **Wiederholung:** `whoami` und `sync` wiederholt der Client bei Fehlern des Transports und
  bei 5xx bis zu dreimal, mit wachsendem Abstand (0,5 s, 1 s, 2 s). `rotate` und die
  Schreibvorgänge nie.
- **Weiterleitungen:** Der Client folgt keiner Weiterleitung. Eine 3xx-Antwort ist ein Fehler,
  ohne Wiederholung; bei `rotate` und den Schreibvorgängen ein eindeutiger — der Hub hat nicht
  ausgeführt, und Body und Token gehen an kein anderes Ziel.
- **Unklarer Ausgang bei `rotate` und den Schreibvorgängen:** Kam die Verbindung nicht zustande
  oder antwortet der Hub mit einer Weiterleitung, ist nichts geschehen; ebenso bei einem Code
  des Vertrags und bei 404 mit `invalid` (unbekannter Vorgang). Jeder andere Fehler nach dem
  Abschicken — Zeitüberschreitung, abgebrochene Verbindung, unlesbare Antwort, 5xx — ist unklar
  (`contract.ErrOutcomeUnknown`).
- **Host:** Der Hub beantwortet nur Anfragen, deren `Host` dieser Rechner (`localhost`,
  `127.0.0.1`, `[::1]`) mit dem Port ist, auf dem die Anfrage ankam — dieselbe Prüfung wie am
  Node vor `/mcp`. Sonst antwortet er 403 ohne Vertragsform, noch vor Pfad und Anmeldung. Ein
  Tunnel geht damit nur mit gleichem Port (`ssh -L 7434:localhost:7434`), bis `ssh` und `https`
  als Transport kommen.
- **Log:** eine Zeile je Anfrage mit Methode, Pfad, Status, Dauer, Node- und Account-Namen;
  Namen, die der Namensregel nicht folgen, erscheinen maskiert. Nie ein Token, nie ein Body —
  also auch nie der Inhalt eines Dokuments.

## Bekannte Grenzen

- **Ein großer Import ist eine unbegrenzte Seite.** `hub import` schreibt alle Dokumente unter
  einer Revision, und eine Revision kommt immer ganz. Über HTTP wird das später ein Datenstrom
  oder eine Obergrenze je Schreibvorgang; in Fassung 1 gibt es kein Limit. Dasselbe gilt für
  `delete` und `rename` eines großen Verzeichnisses: eine Revision, und die Antwort trägt jede
  Zeile — bei `rename` samt Inhalt.
- **Wiederherstellung aus einer Sicherung** braucht eine neue `hub_id` (siehe oben).
- **Kein Schlüssel für Wiederholungen.** Nach unklarem Ausgang eines Schreibvorgangs sieht der
  Aufrufer nach, statt zu wiederholen. Ein Schlüssel, an dem der Hub eine Wiederholung erkennt,
  kommt, wenn überhaupt, mit `https` und `ssh`.
- **Ein Hub vor Task 014 und große Schreibvorgänge.** Ein solcher Hub liest den Body vor dem
  Vorgang und begrenzt ihn auf 1 MiB; einen größeren Schreibvorgang beantwortet er mit 413
  (`invalid`) statt als unbekannten Vorgang.
- **Fehlversuche werden nicht begrenzt.** Ein Node kann beliebig viele Account-Tokens
  probieren; er muss dafür aber selbst angemeldet sein. Eine Begrenzung kommt später.
