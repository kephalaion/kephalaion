# Task 014 — Schreiben über MCP: create, write, delete, rename

Clients schreiben über den Node beim Hub — Dokumente anlegen, ersetzen, löschen, umbenennen,
Verzeichnisse als Ganzes —, und die Erweiterung für VS Code speichert damit.

## Intent

Ein Client schreibt über MCP in jede Collection, in der sein Account schreiben darf; der Hub
bleibt der einzige Schreiber und prüft Anmeldung, Recht, Form und Vorbedingung, und in VS Code
gehen Speichern, neue Dateien, Löschen, Umbenennen und Drag & Drop.
- Der Hub prüft jeden Schreibvorgang: `write` für Neues und Eigenes, `supersede` für Fremdes;
  `created_by`/`updated_by` ist der User, `actions` nennt Account und Node.
- Nichts wird still überschrieben: Name vergeben und veraltete Revision sind eigene,
  endgültige Fehler.
- Die eigene Änderung steht in der Replica, bevor der Client die Antwort bekommt —
  zweimal hintereinander speichern geht ohne Konflikt.
- Kein Schreibvorgang wird nach dem Abschicken wiederholt; „nicht erreichbar“, „Ausgang
  unklar“ und „abgelehnt“ sind drei unterscheidbare Meldungen. Lesen läuft offline weiter.
- `delete` und `rename` behandeln ein Verzeichnis als Ganzes: alles oder nichts, eine Revision.

## Referenzen

- `docs/konzept.md` — „Die tragende Entscheidung“, „Der Weg eines Eintrags“, „Zwei Arten von
  Eingaben“ (Name vergeben, Revision als Vorbedingung), „Löschen“, „Collections, Accounts,
  Rechte“ (Rechte, Urheber), „Transport, Token und Fehlschläge“, „Authentifizierung“ (Wer wann
  prüft), „Werkzeuge“ → „Allgemein — schreiben“ (Festlegungen dieses Tasks), „Stufen“.
- `docs/vertrag.md` — Aufbau eines Vorgangs, Anmeldung von Node und Account, Fehler, HTTP;
  `rotate` als Vorbild für „nie wiederholen“ und „Ausgang unklar“.
- `docs/vscode.md` — Tabelle „Die Vorgänge und ihre Entsprechung“, Konflikte, Offline, Rechte.
- `docs/begriffe.md` — die Begriffe dieses Tasks stehen dort schon (als geplant markiert);
  weitere vor Benutzung eintragen.
- `k-playbook-local/k-playbook.md` — Regeln: Hub und Node getrennt, Vertrag zweimal gleich,
  Umsetzung nur in `cmd/kephalaion`, Hub-SQL PostgreSQL-tauglich, Account-Zeile zuerst
  sperren, kein Token in MCP.
- Code:
  - `internal/hub/store/documents.go` — `docTx`, `put`/`create`/`replace`, `DeleteDocument`,
    `checkPathFree`, `dirRange`, `CheckContent`, `MaxDocumentBytes`; Urheber heute fest `Admin`;
  - `internal/hub/store/accounts.go` — Account prüfen, Sperre der Account-Zeile;
  - `internal/hub/replication/replication.go` — `Whoami`/`Rotate` als Vorbild;
  - `internal/contract/contract.go` — `Hub`, `Codes`, `ErrOutcomeUnknown`;
  - `internal/contract/httpapi/` — `MaxBodyBytes`, Wiederholung im Client;
  - `internal/node/replica/accounts.go` — `WriteAccountRows` (Zeilen aus einer Antwort in die
    Replica);
  - `internal/node/mcpnode/` — `access.go`, `read.go`, `NewHandler`;
  - `cmd/kephalaion/synccmd.go` (`connector`, `localHub`), `bgsync.go`, `serve.go`;
  - `vscode/extension.js`, `vscode/package.json` (Stand 0.0.4: Account je Hub wählbar).

## Ziel

Stufe 2 des Konzepts, dazu `rename` (vorgezogen aus Stufe 3): der Vertrag Node → Hub um
`create`, `write`, `delete` und `rename`, dieselben vier als MCP-Werkzeuge am Node, und die
Erweiterung schreibt über sie.

## Kontext

- **Voraussetzung:** Tasks 001–013 in `done/`. Neue langsame Tests (serve mit Hub und zwei
  Nodes, HTTP gestoppt, Zeitüberschreitung) je Test mit dem Helfer `slow` aus 013 markieren;
  vor Abschluss läuft `make check` vollständig.
- **Nicht in diesem Task:** `create_numbered`, `append`, `replace_section`, `supersede`
  (Ablösen), `replace_directory`, `meta`; Überschreiben beim `rename`, Verschieben über
  Collections oder Hubs; Idempotenzschlüssel und Wiederholung; Ereignisstrom; `https`/`ssh`;
  persönliche Verzeichnisse; die Erweiterung im Release. `hub doc put|rm` und `hub import`
  bleiben Admin-Vorgänge wie bisher.
- **Fassung bleibt 1** — die vier Vorgänge kommen nur hinzu, es gibt keinen ausgelieferten
  Node mit Vertrag (wie bei `user` in Task 006). Kennt ein Hub den Vorgang nicht (404,
  `invalid`), meldet der Node „Hub kann noch nicht schreiben“.
- **Anmeldung:** der Node wie bisher (Header); der Account im Body `{"account", "token"}` wie
  bei `whoami`, geprüft gegen `accounts` (maßgeblich, gesperrt = ungültig):
  `account_unauthenticated`. Der Node prüft den Client vorher wie beim Lesen gegen die Replica
  (`Authenticate`) — nicht angemeldet oder Collection nicht lesbar: dieselbe Meldung „nicht
  lesbar“, ohne den Hub zu fragen. Bewusste Grenze: Schreiben setzt voraus, dass die Replica
  die Collection und die `SYSTEM:A:`-Zeile des Accounts schon trägt — nach `grant` erst nach
  dem nächsten Abgleich, „noch nie abgeglichen“ blockiert; die Meldung nennt dann `node sync`
  als Ausweg. Ob geschrieben werden darf, entscheidet allein der Hub; so wirkt eine Sperre
  beim Schreiben sofort.
- **Prüfung am Hub**, in einer Transaktion: zuerst die Zeile des Accounts in `accounts`
  sperren (Regel in `k-playbook.md`), dann Hash, gesperrt und User lesen. Die Collection muss
  es geben, der Node muss sie abgleichen dürfen (`node_collections`), der Account muss eine
  lebende `SYSTEM:A:`-Zeile in ihr haben — sonst eine Antwort: `not_readable`. Aus der Zeile:
  `write` für `create` und Eigenes (`created_by` = User des Accounts, auch wenn ein anderer
  Account desselben Users es angelegt hat); `supersede` für Fremdes, `write` ist dafür nicht
  nötig — bei Verzeichnissen je Dokument darunter. Sonst `forbidden` mit dem Grund in der
  Meldung („gehört admin, `supersede` fehlt“).
- **Urheber:** Der Schreibvorgang am Hub trägt User, Account und Node. `created_by`/
  `updated_by` = User; `actions` je Dokument eine Zeile mit `account`, `carrier` = Node und
  `action` (`create`, `update`, `delete`, `rename`) unter der Revision des Vorgangs. Die CLI am
  Hub bleibt `admin` ohne Träger.
- **Vorgänge** — je eine Transaktion, höchstens eine Revision:
  - `create` (`collection`, `name`, `content`): lebendes Dokument mit dem Namen →
    `name_taken` (endgültig); eine Löschmarke unter dem Namen hindert nicht, neue `id`. Datei
    und Verzeichnis zugleich → `path_conflict`.
  - `write` (`collection`, `name`, `content`, wahlweise `base_revision`): kein lebendes
    Dokument → `not_found`. `base_revision` weicht von der Revision des lebenden Dokuments ab
    → `stale_revision` (Meldung nennt die aktuelle), geprüft vor dem Vergleich des Inhalts.
    Unveränderter Inhalt: keine neue Revision, Antwort mit der bestehenden Zeile.
  - `delete` (`collection`, `name`, wahlweise `base_revision`, `recursive`): `name` ist ein
    lebendes Dokument → Löschmarke. Ist es ein Verzeichnis (lebende Dokumente darunter), nur
    mit `recursive: true`, sonst `invalid` („ist ein Verzeichnis“); dann alle darunter
    Löschmarken unter einer Revision. `base_revision` nur für Dokumente. Weder noch →
    `not_found`. Die Wurzel einer Collection wird nie gelöscht.
  - `rename` (`collection`, `name`, `new_name`, wahlweise `base_revision`): `id` bleibt. Ein
    Verzeichnis: alle Dokumente darunter bekommen den neuen Präfix, alles oder nichts, eine
    Revision; jeder neue Name wird geprüft (`CheckDocName`). Ziel lebend belegt →
    `name_taken` (kein Überschreiben); `path_conflict`; Ziel in der Quelle (`x` → `x/y`) oder
    gleicher Name → `invalid`. `base_revision` nur für Dokumente. Nur innerhalb einer
    Collection.
  - Name und Inhalt prüft der Hub wie bisher (`CheckDocName`, kein `SYSTEM:`; `CheckContent`:
    UTF-8, keine NUL, höchstens 1 MiB). Leere Dokumente sind erlaubt — „Neue Datei“ in
    VS Code schreibt zuerst leer.
- **Antwort** jedes Vorgangs: `hub_id`, `version`, `revision` (die neue; bei unverändertem
  Inhalt die bestehende) und `rows` — die geschriebenen Zeilen in der Form von `sync`, bei
  `delete` die Löschmarken. Alles, was die Antwort braucht, liest der Hub vor dem Commit. Die
  Antwort ist die Wahrheit, nicht die Anfrage.
- **Fehlercodes neu:** `name_taken` (409), `path_conflict` (409), `stale_revision` (409),
  `not_found` (404), `forbidden` (403), `not_readable` (403). Über HTTP unterscheidet der Client
  nach dem Code im Body, nicht nach dem Status.
- **Transport:** `create`, `write`, `delete`, `rename` werden nie wiederholt. Kam keine
  Verbindung zustande oder eine Weiterleitung: nichts geschehen („Hub nicht erreichbar, nichts
  gespeichert“). Jeder andere Fehler nach dem Abschicken (Zeitüberschreitung, abgebrochene
  Verbindung, unlesbare Antwort, 5xx) ist `contract.ErrOutcomeUnknown`; über `local` jeder
  Fehler, der kein Fehler des Vertrags ist (wie `localHub` bei `rotate`). Nach unklarem Ausgang
  stößt der Node den Abgleich dieses Hubs ebenfalls an, ohne zu warten; der Aufrufer sieht
  nach. Die Grenze des Bodys für Schreibvorgänge muss jedes Dokument tragen, das der Store
  annimmt, auch bei größter Aufblähung durch JSON (`\u00XX` = 6 Byte) — mindestens
  6 × `MaxDocumentBytes` plus Rand; ebenso am MCP-Eingang des Nodes. Log: Node- und
  Account-Name, nie Token, nie Inhalt.
- **Eigene Änderung sofort sichtbar:** Nach Erfolg schreibt der Node die `rows` der Antwort in
  die Replica des Hub-Eintrags, bevor er dem Client antwortet — `WriteAccountRows`
  verallgemeinert: gebunden an `entry_id`/`hub_id`, Zeilen nur vorwärts, nur gewünschte
  Collections, `sync_state` bleibt. Danach stößt er den Abgleich dieses Hubs an, ohne darauf zu
  warten (unter `serve` über `backgroundSync`). Scheitert das Schreiben in die Replica, bleibt
  es ein Erfolg mit Hinweis; der Abgleich holt nach. `changes` meldet die eigene Änderung erst
  nach dem nächsten Abgleich (liest bis `sync_state`) — gewollt.
- **MCP am Node:** Werkzeuge `create`, `write`, `delete`, `rename` mit der Adresse wie beim
  Lesen (`<hub>:<collection>`, Hub-Teil darf fehlen). Den Weg zum Hub bekommt `mcpnode` von
  `cmd/kephalaion` als Funktion über den `connector`, dazu den Anstoß des Abgleichs;
  `internal/node` kennt weiter nur `contract.Hub`. `https`/`ssh`: „noch nicht unterstützt“.
  Antwort: Adresse, Name, `id`, Revision, geändert (wann, von wem), Größe; bei Verzeichnissen
  Zahl der Dokumente und Revision. Fehler tragen neben der Meldung einen Code (`name_taken`,
  `stale_revision`, `forbidden`, `not_found`, `path_conflict`, `not_readable`, `invalid`,
  `unreachable`, `outcome_unknown`), den die Erweiterung auswertet, nicht die Meldung. Die
  Meldung bei `outcome_unknown` sagt, dass es gespeichert sein kann und wie man nachsieht.
  Beschreibungen kurz und deutsch.
- **Erweiterung:** baut auf 0.0.4 auf (Account je Hub wählbar).
  - `stat` schreibgeschützt nur ohne `writable` — ein Recht je Collection (`write`); ein
    fremdes Dokument ohne `supersede` scheitert erst beim Speichern mit `NoPermissions`.
    Bewusste Grenze: `supersede` ohne `write` bleibt in der Erweiterung schreibgeschützt.
    `isReadonly` wird `false`.
  - `writeFile`: `read` mit `content: false`; `none` → `create` (ohne `options.create`:
    `FileNotFound`); `document` → `write` mit dessen Revision (ohne `options.overwrite`:
    `FileExists`); `directory` → `FileIsADirectory`. Inhalt streng als UTF-8; kein Text oder zu
    groß → klare Meldung vor dem Aufruf.
  - `delete` → `delete` mit `recursive` aus den Optionen.
  - `rename` innerhalb einer Collection → `rename`; über Collections oder Hubs, oder mit
    `overwrite` bei belegtem Ziel → Meldung.
  - `createDirectory` nur in der Erweiterung gemerkt, bis darin etwas angelegt oder es
    gelöscht wird; `stat` und `readDirectory` zeigen es.
  - Je Collection eine Tabelle `id` → Name, gespeist aus jeder Quelle: `list`, `read`,
    `changes` und der Antwort eigener Schreibvorgänge (sie trägt die `id`). Meldet `changes`
    eine bekannte `id` unter neuem Namen: `Deleted` für den alten Namen, für den neuen
    `Created` bzw. `Deleted` nach `deleted`, `Changed` für beide Elternpfade — so sieht ein
    zweites VS Code das Umbenennen. Bei `reset`/`dropped` wird die Tabelle des Hubs bzw. der
    Collection verworfen.
  - Nach Erfolg die Ereignisse selbst feuern. Codes auf `FileSystemError`: `name_taken` →
    `FileExists`, `not_found` → `FileNotFound`, `forbidden`/`not_readable` → `NoPermissions`,
    `unreachable`/`outcome_unknown` → `Unavailable` mit Meldung, sonst ein Fehler mit der
    Meldung des Nodes.
  - Nächste Version (0.0.5).
- **Testdaten im echten Store** nur unter `test/` in `home:eins`. Dokumente, die dort mit
  `hub doc put` angelegt wurden, gehören `admin` — ein Account ohne `supersede` darf sie nicht
  ändern; das ist zugleich der Test für `forbidden`.

## Zu bauen

Jede Etappe endet als sicherbarer Stand auf `dev`: `make check` grün, Vertrag und Code
derselben Etappe im selben Commit. Etappen 5–6 sind der zweite Teil und können getrennt folgen.

### Etappe 1 — Hub-Store: Urheber, Rechte, create, write, delete

- Wer schreibt (User, Account, Node) als Teil des Schreibvorgangs statt fest `Admin`; die CLI
  bleibt `admin`.
- `create`, `write` (mit `base_revision`), `delete` für Dokumente, mit Rechteprüfung und Sperre
  der Account-Zeile; Abfragen in `queries`, `sqlq.Check`.
- Tests: `created_by`/`updated_by` = User, `actions` mit Account und Node; Eigenes mit
  `write`, Eigenes eines anderen Accounts desselben Users; Fremdes ohne `write`: mit
  `supersede` erlaubt, ohne `supersede` `forbidden`; `create` ohne `write` → `forbidden`;
  gesperrter Account; `name_taken`, Löschmarke neu anlegen (neue `id`),
  `path_conflict`, `stale_revision`, unveränderter Inhalt ohne Revision; leeres Dokument;
  `SYSTEM:`-Name abgelehnt.

### Etappe 2 — Vertrag: create, write, delete über local und HTTP

- `docs/vertrag.md` und `internal/contract` im selben Commit: Vorgänge, Felder, Antwort, Codes
  mit HTTP-Status, „nie wiederholen“, Grenze des Bodys. Dabei verallgemeinern: Einleitung
  „Drei Vorgänge“, „Fehler des Transports … der Node versucht es später wieder“ (gilt nicht
  für Schreibvorgänge), „Wiederholung“, „Unklarer Ausgang bei `rotate`“.
- `hub/replication`: Anmeldung, `not_readable`; `httpapi`: Pfade, keine Wiederholung, unklarer
  Ausgang; `localHub`: Nicht-Vertragsfehler unklar.
- Tests gegen `local` und HTTP: jeder Code; Node ohne `replicate`; Account unbekannt oder
  gesperrt; Dokument an der Größengrenze aus Zeichen, die JSON aufbläht, über HTTP; kein
  Wiederholen nach 5xx und Zeitüberschreitung (unklar), Verbindung verweigert (nichts
  gespeichert); Hub ohne den Vorgang.

### Etappe 3 — Node: Werkzeuge create, write, delete

- Werkzeuge in `mcpnode` über `access.go`; Weg zum Hub und Anstoß des Abgleichs aus
  `cmd/kephalaion`; Antwortzeilen in die Replica; Fehlercodes.
- Tests mit dem MCP-Client des go-sdk: schreiben → sofort `read` mit neuer Revision, ohne
  Abgleich; zweimal hintereinander `write` mit der Revision aus `read`; `changes` nach dem
  Abgleich; Hub über `http` gestoppt → `unreachable`, Lesen geht weiter; `connectHTTP` mit
  `ErrOutcomeUnknown` → `outcome_unknown`, und hat der Hub geschrieben, steht das Dokument nach
  dem angestoßenen Abgleich in der Replica; „nicht lesbar“ ohne Anfrage an den Hub; kein
  Token in Antwort und Log.

### Etappe 4 — rename und Verzeichnisse

- `rename` durch alle Schichten (Store, Vertrag, Werkzeug); `delete` mit `recursive` für
  Verzeichnisse.
- Tests: `id` bleibt; Verzeichnis umbenennen und löschen: eine Revision, alles oder nichts,
  auch mit einem fremden Dokument ohne `supersede` mittendrin; `name_taken` am Ziel,
  `path_conflict`, `x` → `x/y`, gleicher Name; Verzeichnis ohne `recursive`; Replica nach
  `rename` sofort richtig (alter Name weg, neuer da).

### Etappe 5 — Erweiterung

- `vscode/extension.js` nach „Kontext“, Version 0.0.5, `.vsix` bauen.
- Geprüft mit dem Ersatz für `vscode` gegen den laufenden Node (`home:eins`, nur unter
  `test/`): neue Datei (leer, dann Inhalt), zweimal speichern, Konflikt nach `hub doc put` auf
  dasselbe Dokument, Löschen von Datei und Verzeichnis, Umbenennen von Datei und Verzeichnis
  (ein zweiter Client sieht über `changes` den alten Namen verschwinden und den neuen kommen;
  auch umbenennen, dann löschen, ein Abgleich),
  leeres Verzeichnis anlegen und befüllen, Binärdatei abgelehnt, fremdes Dokument nicht
  änderbar (`NoPermissions` erst beim Speichern), Node bzw. Hub nicht erreichbar.

### Etappe 6 — Durchlauf und Doku

- `serve` mit Hub und Node, ein zweiter Node über `http`: Schreiben von beiden, Konflikt
  zwischen beiden, Hub gestoppt.
- Liste der Handgriffe für den echten VS Code, die der Nutzer durchgeht: Speichern, neue
  Datei, Ordner per Drag & Drop, Umbenennen und Löschen im Explorer, „Datei ist neuer“.
- Doku: `README.md` (Schreiben über MCP), `docs/begriffe.md` (Markierung „geplant“ entfernen,
  nachziehen), `docs/konzept.md` (Stand, Werkzeuge gebaut, Stufen; Tabelle „Wer wann prüft“:
  Schreiben — der Node prüft vorher Anmeldung und Lesbarkeit gegen die Replica; die Grenze
  „Replica muss Collection und Account-Zeile tragen“; „Allgemein — schreiben“: „`supersede`
  zusätzlich für Fremdes“ → „`supersede` für Fremdes, `write` dafür nicht nötig“),
  `docs/vscode.md` (Stand, Umsetzung Schreiben; Tabelle „watch“ und Absatz 0.0.3 zur `id` —
  sie wird jetzt ausgewertet; Schreibschutz je Collection, `NoPermissions` erst beim
  Speichern, `supersede` ohne `write` schreibgeschützt),
  `k-playbook-local/k-playbook.md` (`contract.Hub`, `mcpnode`, Urheber), `docs/fortschritt.md`.

## Fortschritt

| Etappe | Status | Datum | Notiz |
|---|---|---|---|
| 1 — Hub-Store: Urheber, Rechte, create, write, delete | erledigt | 2026-09-27 | `docTx` trägt Urheber (User, Account, Node), CLI bleibt admin ohne Träger; `CreateDocumentAs`/`WriteDocumentAs`/`DeleteDocumentAs` mit `WriteAuth`, Sperre der Account-Zeile zuerst, Fehler `ErrNotReadable`/`ErrForbidden`/`ErrNameTaken`/`ErrStaleRevision`/`ErrInvalid` (`write.go`); Tests `write_test.go`, `make check` grün; Commit folgt |
| 2 — Vertrag: create, write, delete über local und HTTP | erledigt | 2026-09-27 | `contract.Hub` um `Create`/`Write`/`Delete` (`WriteResponse`), Codes `not_readable`, `forbidden`, `not_found`, `name_taken`, `path_conflict`, `stale_revision`, `contract.MaxDocumentBytes`, `ErrUnknownOperation` (Hub ohne Vorgang: 404 `invalid`); `replication/write.go` (Fassung, Form, Node, dann Store); `httpapi`: Pfade, Body 7 MiB für Schreibvorgänge, nie wiederholt, `content` Pflicht, ungültiges UTF-8 abgelehnt (Befund `json-utf8.md`); `localHub` hüllt Nicht-Vertragsfehler in `ErrOutcomeUnknown`; `vertrag.md` („Schreibvorgänge“, „Ausgang und Wiederholung“); `make check` grün |
| 3 — Node: Werkzeuge create, write, delete | erledigt | 2026-09-27 | `mcpnode/write.go`: Werkzeuge über `access.go`, Weg zum Hub und Anstoß als `HubLink` aus `cmd/kephalaion` (`nodeHubLink`, `backgroundSync.kick`, auch bei `sync_interval` 0, folgt einem laufenden); Fehler mit `isError`, Text und `error.code` in der Struktur, Codes des Vertrags plus `unreachable`, `outcome_unknown`, `unsupported`, `internal`; Antwortzeilen per `replica.WriteRows` (gebunden an `entry_id`/`hub_id`, nur geführte Collections), sonst Erfolg mit `note`; Grenze `/mcp` 7 MiB; `make check` grün, `-race` über `internal/node` grün |
| 4 — rename und Verzeichnisse | erledigt | 2026-09-27 | `RenameDocumentAs` und `DeleteDocumentAs` mit `recursive` (Store, eine Revision, Recht je Dokument, alles oder nichts; `actions` `rename`); Ziel: gleiche Art belegt → `name_taken` (kein Zusammenlegen), Datei/Verzeichnis → `path_conflict`, `x`→`x/y` und gleicher Name `invalid` (`ident.CheckRename`), `base_revision` bei Verzeichnis `invalid`; Vertrag `/v1/rename`, `recursive` (`vertrag.md` + `contract`); `localHub` ohne Einbettung (Befund `rename-verzeichnisse.md`); Werkzeug `rename`, Antwort bei Verzeichnis `kind: directory` mit `count`; `make check` grün, `-race` über `internal/node`, `internal/hub` grün |
| 5 — Erweiterung | erledigt | 2026-09-27 | `extension.js` 0.0.5: `stat` mit `writable` je Collection, `isReadonly: false`; `writeFile` über `read` → `create`/`write` mit Revision, Inhalt vorher streng UTF-8 (BOM bleibt), ohne NUL, ≤ 1 MiB; `delete` mit `recursive`; `rename` nur in einer Collection, kein Überschreiben; `createDirectory` nur gemerkt; Tabelle `id` → Name je Collection aus allen Quellen, `reset`/`dropped` verwerfen; Ereignisse nach Erfolg; Codes auf `FileSystemError`, Weg zum Node: nicht erreichbar vs. Ausgang unklar, nie wiederholt; `.vsix` gebaut (gitignored). Ersatz für `vscode` gegen den echten Node (`home:eins`, `test/schreiben/…`, 118 Prüfungen): neue Datei, zweimal speichern, Konflikt nach `hub doc put`, Löschen, Umbenennen mit zweitem Client (auch umbenennen+löschen), leeres Verzeichnis, Binär/NUL/Größe, fremdes Dokument, Node nicht erreichbar. Isoliert (eigene configs, Node über http): Hub gestoppt → `Unavailable`, Lesen geht weiter; `outcome_unknown` und abgebrochene Verbindung über falschen Node. `make check-quick` grün; Befund `vscode-schreiben.md` |
| 6 — Durchlauf und Doku | erledigt | 2026-09-27 | `TestMCPWriteTwoNodes` (slow, ~0,35 s): serve mit Hub und Node, zweiter Node mit eigener config und eigenem serve über http, zwei Accounts eines Users — Schreiben von beiden, `stale_revision` und `name_taken` (create, rename) zwischen beiden, `actions` mit Account und Node, Hub gestoppt → `unreachable`, Lesen geht weiter; Hilfe von `serve` samt `TestServeHelp`. Doku: README, begriffe (ohne „geplant“), konzept (Stand, „Wer wann prüft“, Grenze der Replica, „Allgemein — schreiben“, Stufen), vscode.md („Umsetzung: Schreiben“ mit Handgriffen für den echten VS Code), vertrag.md geprüft (503 nachgetragen), k-playbook.md, fortschritt.md; `vscode/README.md` geprüft, unverändert. `make check` grün |

---
## Review-Log (2026-09-26)

**Pfad:** k-playbook-local/tasks/014-schreiben-ueber-mcp.md
**Intent:** inline (`## Intent` in 014)
**Runden:** 2

### Diskussion
- **Rechte für Fremdes (WARNUNG-01, NEU-01, NEU-03):** „`write` … zusätzlich `supersede`“
  ließ offen, ob Fremdes beide Rechte braucht. Der Moderator folgte dem Konzept (Rechte-
  Tabelle, „Löschen“, Werkzeug-Tabelle): `supersede` allein genügt. Folge: die Stelle
  „Allgemein — schreiben“ in `konzept.md` sagt noch „zusätzlich“ und wird in Etappe 6
  nachgezogen; `writable` in der Erweiterung bleibt an `write` gebunden, `supersede` ohne
  `write` erscheint dort schreibgeschützt — als bewusste Grenze festgehalten.
- **`rename` für andere Clients (WARNUNG-02, NEU-02):** `changes` nennt keinen alten Namen
  (Konzeptentscheidung vom 2026-09-26, Erkennung über die `id`), die Erweiterung wertet die
  `id` bisher nicht aus — ein zweites VS Code hätte den alten Namen nie verloren. Entscheidung:
  Tabelle `id` → Name je Collection in der Erweiterung, gespeist aus allen Quellen; Ereignis für
  den neuen Namen folgt `deleted`; `reset`/`dropped` verwirft sie. Konzept bleibt unverändert.
- **Parallelität mit 013 (WARNUNG-03):** 013 läuft und teilt die Tests in schnell/langsam. Der
  Nutzer entschied: 013 zuerst; 014 markiert seine langsamen Tests mit dem Helfer `slow`.

### Critic-Issues
| ID | Kategorie | Datei | Stelle | Problem | Empfehlung |
|---|---|---|---|---|---|
| WARNUNG-01 | WARNUNG | 014 | Prüfung am Hub, Etappe 1 Tests | „zusätzlich `supersede`“ doppeldeutig: braucht Fremdes `write` und `supersede`? Test „ohne `write`“ ohne Erwartung | Festlegen: Fremdes nur `supersede`; Erwartung im Test nennen |
| WARNUNG-02 | WARNUNG | 014, docs/vscode.md | Etappe 4/5, „watch“ | `rename` behält die `id`, `changes` ohne alten Namen; die Erweiterung wertet die `id` nicht aus — zweiter Client sieht den alten Namen nie verschwinden | `id` in der Erweiterung auswerten oder `changes` erweitern; vscode.md korrigieren |
| WARNUNG-03 | WARNUNG | 014 | Voraussetzung | 013 läuft parallel, ändert Test-Aufteilung und `k-playbook.md` „Testen“ | Reihenfolge festlegen; langsame Tests nach 013 markieren |
| WARNUNG-04 | WARNUNG | 014 | „Zu bauen“ | Task zieht durch alle Schichten; kein Wort zu sicherbaren Zwischenständen | Etappen als abgeschlossene Commits; 5–6 abtrennbar |
| WARNUNG-05 | WARNUNG | 014 | Erweiterung, `stat` | `writable` je Collection: fremdes Dokument scheint schreibbar, scheitert erst beim Speichern | bewusst so nennen oder `read` erweitern |
| FEHLEND-01 | FEHLEND | 014 | Transport, Eigene Änderung | Abgleich nur nach Erfolg angestoßen; Konzept verlangt ihn auch nach unklarem Ausgang | Abgleich nach `outcome_unknown`; Test |
| FEHLEND-02 | FEHLEND | 014 | Etappe 2, Etappe 6 | Widersprüche in `konzept.md` („Wer wann prüft“) und `vertrag.md` (Einleitung, Fehler, Wiederholung, unklarer Ausgang) nicht als Doku-Stellen genannt | Stellen aufführen |
| FEHLEND-03 | FEHLEND | 014 | Anmeldung | Vorprüfung am Node setzt voraus, dass die Replica Collection und Account-Zeile trägt; nicht ausgesprochen | als bewusste Grenze nennen; Meldung mit Ausweg |
| NEU-01 | WARNUNG | 014 | Etappe 6, konzept.md | „`supersede` zusätzlich“ in „Allgemein — schreiben“ widerspricht nach WARNUNG-01 dem Rest des Konzepts | in Etappe 6 nachziehen |
| NEU-02 | FEHLEND | 014 | Erweiterung, `id`-Tabelle | Regel deckt Löschmarke unter neuem Namen, eigene Schreibvorgänge und `reset`/`dropped` nicht | Regel allgemeiner fassen; Prüfschritt „umbenennen, dann löschen“ |
| NEU-03 | WARNUNG | 014 | Erweiterung, `stat` | `supersede` ohne `write` darf am Hub schreiben, in VS Code aber nicht | bewusste Grenze festhalten oder `writable` erweitern |
| A-1 | Randnotiz (Alignment) | 014 | Vorgänge, `rename` | `base_revision` bei Verzeichnissen nicht geregelt (bei `delete` „nur für Dokumente“) | gleich wie `delete` |

### Moderator-Routing
| ID | Route | Begründung | Ergebnis |
|---|---|---|---|
| WARNUNG-01 | decide + pass | Konzept ist eindeutig (Rechte-Tabelle, „Löschen“, Werkzeug-Tabelle) | behoben |
| WARNUNG-02 | decide + pass | Konzeptentscheidung „ohne alten Namen“ bleibt; Auswertung der `id` gehört in die Erweiterung | behoben |
| WARNUNG-03 | ask-user + pass | Ablauffrage; Nutzer: 013 zuerst | behoben |
| WARNUNG-04 | decide + pass | ein Satz vor den Etappen, kein Umbau | behoben |
| WARNUNG-05 | decide + pass | bewusst so, kein neues Feld in `read` | behoben |
| FEHLEND-01 | pass | Widerspruch zum Konzept | behoben |
| FEHLEND-02 | pass | Docs-Sync | behoben |
| FEHLEND-03 | pass | Folge der gewählten Reihenfolge, nicht ableitbar | behoben |
| NEU-01 | decide (Moderator wendet an) | Folge von WARNUNG-01 | behoben |
| NEU-02 | decide (Moderator wendet an) | Critic-Wortlaut übernommen | behoben |
| NEU-03 | decide (Moderator wendet an) | Randfall; bewusste Grenze statt Änderung an `read`/Konzept | behoben |
| A-1 | decide (Moderator wendet an) | gleich wie `delete` | behoben |

### Editor-Entscheidungen
| ID | Aktion | Begründung |
|---|---|---|
| WARNUNG-01 | behoben | Kontext: „`supersede` für Fremdes, `write` ist dafür nicht nötig“; Tests nennen je Fall das Ergebnis |
| WARNUNG-02 | behoben | Punkt „Tabelle `id` → Name“ in Erweiterung; Prüfschritt zweiter Client; Etappe 6 vscode.md „watch“ und Absatz 0.0.3 |
| WARNUNG-03 | behoben | Voraussetzung 001–013 in `done/`; Helfer `slow`; `make check` vor Abschluss |
| WARNUNG-04 | behoben | Satz vor Etappe 1 |
| WARNUNG-05 | behoben | Halbsatz bei `stat`; Prüfschritt; Doku |
| FEHLEND-01 | behoben | Transport: Abgleich nach unklarem Ausgang; Test in Etappe 3 |
| FEHLEND-02 | behoben | Etappe 2 nennt vier Stellen in `vertrag.md`; Etappe 6 „Wer wann prüft“ |
| FEHLEND-03 | behoben | Absatz „Anmeldung“: bewusste Grenze, Meldung nennt `node sync`; Etappe 6 konzept.md |

### Moderator-Entscheidungen
- WARNUNG-01: Fremdes braucht nur `supersede` — dem Konzept gefolgt, nicht dem Wortlaut
  „zusätzlich“ in „Allgemein — schreiben“; letzterer wird in Etappe 6 korrigiert (NEU-01).
- WARNUNG-02: `changes` bleibt ohne alten Namen (Konzept); die Erweiterung wertet die `id` aus.
- WARNUNG-04: Etappen als sicherbare Stände, Etappen 5–6 als zweiter Teil.
- WARNUNG-05 / NEU-03: `writable` bleibt `write` je Collection; `supersede` ohne `write` ist in
  der Erweiterung schreibgeschützt (bewusste Grenze, in vscode.md nachzuziehen).
- NEU-01, NEU-02, NEU-03, A-1: vom Moderator direkt angewandt, weil Wortlaut und Ort vom Critic
  bzw. Alignment-Check vorlagen; keine dritte Editor-Runde.
- Nutzerentscheidung (2026-09-26): 013 zuerst abschließen; Voraussetzung 001–013 (WARNUNG-03).
- Keine Punkte übersprungen, keine Editor-Edits abgelehnt.

### Intent-Alignment
Ja (zweimal geprüft, zuletzt auf dem Endstand). Jeder Intent-Punkt ist in Kontext und Etappe
mit Tests verankert. `supersede` ohne `write` schreibgeschützt in VS Code hebt keinen
Intent-Punkt auf: über MCP erlaubt der Hub das Ablösen; die Grenze betrifft nur die Erweiterung
und ist benannt.

### Geänderte Dateien
- 014-schreiben-ueber-mcp.md: Voraussetzung 001–013 und Helfer `slow` (WARNUNG-03); Grenze der
  Vorprüfung am Node (FEHLEND-03); Rechte für Fremdes eindeutig, Tests mit Erwartung
  (WARNUNG-01); Abgleich nach unklarem Ausgang, Test (FEHLEND-01); `stat` je Collection,
  `supersede` ohne `write` (WARNUNG-05, NEU-03); `id`-Tabelle der Erweiterung, alle Quellen,
  `reset`/`dropped`, Prüfschritte (WARNUNG-02, NEU-02); Satz zu sicherbaren Ständen
  (WARNUNG-04); Vertragsstellen in Etappe 2, Doku-Stellen in Etappe 6 (FEHLEND-02, NEU-01);
  `base_revision` bei `rename` nur für Dokumente (A-1)

### Offen (nicht gefixt)
- —
