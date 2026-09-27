# Projektregeln

Diese Datei gilt nur für dieses Projekt. Sie wird nach der mitgelieferten
Ebene gelesen und kann deren Aussagen ergänzen oder überstimmen.

Was hier hineingehört: Aufbau und Besonderheiten des Projekts, Konventionen,
wiederkehrende Abläufe, alles was ein Assistent in jeder Sitzung wissen sollte.

Was nicht: allgemeine k-playbook-Regeln — die stehen in der mitgelieferten
Ebene und werden bei jedem Update aktualisiert.

## Aufbau

Ein Go-Modul `github.com/kephalaion/kephalaion`, ein Binary `kephalaion`
(`cmd/kephalaion`). Unterkommandos per erstem Argument, Standardbibliothek `flag`,
keine CLI-Bibliothek. Meldungen des Binarys deutsch, Bezeichner englisch. Begriffe stehen
in `docs/begriffe.md` und werden dort eingetragen, bevor sie benutzt werden.

Pakete unter `internal/`:

- `config` — die config (`config.yaml`): Ort suchen (`Locate`: `--config` >
  `KEPHALAION_CONFIG` > die des Users, wenn es sie gibt > `/etc/kephalaion/config.yaml`, wenn es
  sie gibt > Ort des Users; mit Quelle und Erkennung zweier Arten), strikt lesen, atomar
  schreiben, Rolle eintragen, db-Adressen zerlegen; die festen Orte der globalen Installation
  (`SystemConfig`, `SystemUser`, `SystemDataDir`, `SystemBinary`). `SystemPath` lenken Tests um
  — in `cmd/kephalaion` für alle Tests über `TestMain`.
- `service` — neutral, der Dienst: erzeugt Benutzer-Unit, System-Unit und LaunchAgent (golden
  getestet unter `testdata/`, `go test ./internal/service -update` schreibt neu) und richtet
  den Dienst pro User ein (`Manager`: `systemctl --user` bzw. `launchctl` hinter einem
  `Runner`, alle Orte überschreibbar). Kein Test ruft `systemctl` oder `launchctl`; in
  `cmd/kephalaion` ersetzt `TestMain` den Manager (`newServiceManager`).
- `upgrade` — neutral, das Binary aus einem Release ersetzen (`Upgrader`), dazu `Report` (was
  `upgrade --check --json` und das Feld `update` in `whoami` melden: neueste Version,
  Schreibrecht per Probedatei, Weg) und `Watcher` (die Frage im Hintergrund von `serve`,
  höchstens einmal am Tag, nach Fehler nach einer Stunde, nur im Speicher). Kein Test fragt
  GitHub: `httptest`, in `cmd/kephalaion` über `newUpgrader` in `TestMain`.
- `sqlitedb` — gemeinsamer Unterbau beider Datenbanken, kennt weder Hub noch Node: SQLite
  öffnen (`foreign_keys`, `busy_timeout`; eine fehlende Datei wird nie angelegt, nur
  `Create` legt an), `db_info` prüfen (Schemafassung, Rolle), `settings` lesen und schreiben.
  WAL setzt nur `Create`; `Open` ändert die Datei nicht, auch nicht ihren `journal_mode`.
  Transaktionen beginnen IMMEDIATE (DSN-Parameter `_txlock=immediate`, kein PRAGMA): Sie
  nehmen die Schreibsperre sofort, auch wenn sie nur lesen — reine Lesezugriffe laufen
  deshalb ohne Transaktion.
- `ident` — neutral, für Hub und Node: Namensregel, Adresse `<hub>:<collection>`, Token
  erzeugen, hashen, Format prüfen, gekürzt anzeigen; Pfadregeln für Dokumentnamen
  (`CheckDocName`, `SYSTEM:` abgelehnt); `CheckPrincipalName` für Accounts und Nodes (`admin`
  reserviert), `LogName` für Namen im Log.
- `sqlq` — Hilfe für PostgreSQL-taugliche Abfragen: Platzhalter `$n`, `Bind` je Dialekt,
  `Check` auf verbotene Konstrukte.
- `hub/store`, `node/store` — die gekapselten Datenbanken von Hub und Node, je eine
  Schnittstelle `Store` mit SQLite-Umsetzung und DDL. Der Hub-Store schreibt Dokumente mit
  Urheber (`docTx`: User, Account, Träger); die Vorgänge im Namen eines Accounts stehen in
  `hub/store/write.go` — `CreateDocumentAs`, `WriteDocumentAs`, `DeleteDocumentAs` (mit
  `recursive`), `RenameDocumentAs` mit `WriteAuth`: Account-Zeile zuerst sperren, dann Account,
  Lesbarkeit und Recht in der Transaktion; Fehlerarten `ErrNotReadable`, `ErrForbidden`,
  `ErrNameTaken`, `ErrPathConflict`, `ErrStaleRevision`, `ErrNotFound`, `ErrInvalid`.
- `contract` — neutral, der Vertrag zwischen Node und Hub als Go-Typen: Anfragen, Antworten,
  Fehlercodes, Schnittstelle `Hub` (`Whoami`, `Rotate`, `Sync` und die Schreibvorgänge
  `Create`, `Write`, `Delete`, `Rename` mit `WriteResponse`: Revision und Zeilen in der Form von
  `sync`), Fassung (`contract.Version`), Form der Account-Zeilen (`AccountContent`),
  `MaxDocumentBytes`; `ErrOutcomeUnknown` (Ausgang unklar) und `ErrUnknownOperation` (der Hub
  kennt den Vorgang nicht).
- `contract/httpapi` — neutral, der Vertrag über HTTP: `NewHandler` bedient jede Umsetzung von
  `contract.Hub`, `Client` setzt sie über HTTP um (Wiederholung nur für `whoami` und `sync`,
  `rotate` und die Schreibvorgänge nie; unklarer Ausgang als `contract.ErrOutcomeUnknown`; 404
  mit `invalid` als `contract.ErrUnknownOperation`; folgt keiner Weiterleitung, 3xx ist ein
  eindeutiger Fehler). Body höchstens 1 MiB (`MaxBodyBytes`), bei Schreibvorgängen 7 MiB
  (`MaxWriteBodyBytes`: jedes Dokument auch als `\u00XX`); `content` ist Pflicht; ungültiges
  UTF-8 schickt der Client nicht ab, der Handler nimmt es als ungültiges JSON.
- `reqlog` — neutral, eine Logzeile je HTTP-Anfrage; Namen nur über `ident.LogName`, nie ein
  Token.
- `loopback` — neutral, die Prüfung, dass eine HTTP-Anfrage diesen Rechner meint: `Host`
  Loopback mit dem Port, auf dem sie ankam, sonst 403. Hub-Listener (`serve`) und
  `node/mcpnode` benutzen dieselbe Prüfung.
- `hub/replication` — die Seite des Hubs im Vertrag: setzt `contract.Hub` über dem Hub-Store
  um (Anmeldung von Node und Account, erlaubte Collections, Seitenschnitt); der Store liefert
  nur Zeilen und schreibt `rotate` in einer Transaktion. Die Schreibvorgänge (`write.go`)
  prüfen Fassung, Form (Name, Inhalt, `base_revision` ≥ 1, neuer Name) und die Anmeldung des
  Nodes, dann ruft der Store `…DocumentAs` mit dem Node als Träger; seine Fehlerarten werden
  Codes, alles andere bleibt ein Fehler, der kein Fehler des Vertrags ist.
- `node/replica` — die Replica des Nodes (je Hub-Eintrag eine SQLite-Datei) und der Abgleich
  (`Syncer`), der sie über `contract.Hub` füllt; dazu die Account-Zeilen (`AccountRows` über
  den Teilindex `documents_system`, `WriteAccountRows` nach `rotate`) und `WriteRows`, das die
  Zeilen der Antwort eines Schreibvorgangs übernimmt — gebunden an `entry_id`/`hub_id`, nur in
  Collections mit Stand in `sync_state`, per `id` nur vorwärts, `sync_state` bleibt.
- `node/mcpnode` — der MCP-Eingang des Nodes (`/mcp`, go-sdk, zustandslos): Host/Origin,
  Header-Paare je Hub, Anmeldung über alle Hubs (`Authenticate`), Werkzeug `whoami`
  (`Whoami`, auch für `node whoami`); Werkzeuge `list`, `read`, `changes` (`list.go`,
  `read.go`, `changes.go`) über dem gemeinsamen Schritt in `access.go` (Adresse, Anmeldung,
  Recht, Replica je Hub) und den Abfragen in `node/replica/read.go`; Werkzeuge `create`,
  `write`, `delete`, `rename` (`write.go`) auf demselben Schritt, den Weg zum Hub und den
  Anstoß des Abgleichs bekommt es als `HubLink` von `cmd/kephalaion`. Fehler der Werkzeuge,
  die schreiben, tragen einen Code (Codes des Vertrags, dazu `unreachable`,
  `outcome_unknown`, `unsupported`, `internal`); `/mcp` nimmt Bodys bis 7 MiB
  (`MaxRequestBytes`).

Regeln dazu:

- **Hub und Node bleiben getrennt.** Kein Paket unter `internal/hub` importiert eines unter
  `internal/node` und umgekehrt, auch nicht über Umwege oder in Tests; Gemeinsames gehört in
  neutrale Pakete. `internal/separation_test.go` prüft das, ebenso, dass `internal/contract`
  weder Hub noch Node importiert.
- **Der Vertrag steht zweimal, gleich.** `docs/vertrag.md` ist verbindlich, `internal/contract`
  folgt ihm; wer den einen ändert, zieht den anderen im selben Commit nach. Er trägt eine
  Fassung (`contract.Version`, derzeit 1), und der Hub soll auch ältere Nodes bedienen.
- **Welche Umsetzung des Vertrags ein Node bekommt, entscheidet nur `cmd/kephalaion`.** Dort
  ist sie verdrahtet (`connector` in `synccmd.go`): `local` ist der Hub der eigenen config mit
  `hub/replication` darüber, `http` der Client aus `contract/httpapi` (nur Loopback);
  `internal/node` kennt nur `contract.Hub`. Auch `local` prüft die Anmeldung wie jeder
  Transport; die Tests des Vertrags (`hub/replication`) laufen gegen `local` und HTTP. Ein
  Fehler von `rotate` oder eines Schreibvorgangs, der kein Fehler des Vertrags ist, gilt auf
  jedem Transport als unklar (`contract.ErrOutcomeUnknown`) — über `local` hüllt `localHub` in
  `synccmd.go` ihn ein. `localHub` hält den Hub in einem Feld und setzt jede Methode
  ausdrücklich um (keine Einbettung): Ein neuer Vorgang in `contract.Hub` bricht den Bau, bis
  er dort steht. Die Werkzeuge des Nodes, die schreiben, bekommen denselben `connector` als
  `mcpnode.HubLink` (`nodeHubLink` in `serve.go`; `https`/`ssh` als `mcpnode.ErrUnsupported`)
  und den Anstoß `backgroundSync.kick`.
- **Schreibvorgänge werden nie wiederholt** — `create`, `write`, `delete`, `rename` wie
  `rotate`: kein Transport, kein Node und keine Erweiterung schickt einen ein zweites Mal. Drei
  Ausgänge sind zu unterscheiden: abgelehnt (ein Code, endgültig), nicht erreicht (nichts
  gespeichert), unklar (kann gespeichert sein — der Node stößt den Abgleich an, der Aufrufer
  sieht nach). Einen Schlüssel für Wiederholungen gibt es nicht.
- **Die Replica ist abgeleitet.** Sie enthält nur, was der Hub geliefert hat, und darf wie
  der Node-Store SQLite-Eigenes benutzen. Angelegt wird sie nur vom Abgleich, nie von `init`;
  `node hub rm` und `config import` (für weggefallene Aliase) entfernen sie mit, innerhalb der
  Transaktion, die den Eintrag entfernt. Passt ihre Schemafassung nicht, gehört sie zu einem
  anderen Eintrag oder ist sie eindeutig beschädigt (`NOTADB`/`CORRUPT`, `db_info`, die IDs oder
  die `generation` fehlen), verwirft der Abgleich sie; vorübergehende Fehler verwerfen nichts.
  Eine unlesbare Replica betrifft in `whoami`, `list`, `read` und `changes` nur ihren Hub.
- **Hub-SQL bleibt PostgreSQL-tauglich.** Die Abfragen (DML) des Hubs und des Unterbaus
  stehen zentral in einer Struktur (`queries` bzw. `sqlitedb.Queries`), mit Platzhaltern
  `$n` über `sqlq.Bind` — kein `INSERT OR`, kein `PRAGMA`, kein `AUTOINCREMENT`, kein rohes
  `?`, `user` nur in Anführungszeichen (`"user"`, in PostgreSQL reserviert; auch im DDL). Ein
  Test je Paket prüft sie mit `sqlq.Check`. `PRAGMA` gibt es nur beim Öffnen der
  Verbindung. Das DDL steht je Dialekt. Zähler wie die Revision werden im Code
  hochgezählt, nicht per Umwandlung in SQL. Der Node darf SQLite-Eigenes benutzen.
- **Token nie als Argument.** Ein Token kommt über `--token-stdin` (eine Zeile) oder
  `--token-file` herein, nie über ein Argument — Shell-Verlauf und Prozessliste. Angezeigt wird
  es nur gekürzt (`ident.MaskToken`); ein am Hub erzeugtes Token genau einmal, gespeichert nur
  als Hash. Kein Token in einem MCP-Werkzeug, einer MCP-Antwort oder einem Log; `rotate` ist
  deshalb ein CLI-Kommando, kein Werkzeug.
- **Namensregeln.** Collections, Nodes, Accounts und Hub-Aliase:
  `[a-z0-9][a-z0-9._-]{0,62}`, kein `:`, kein Präfix `system` in beliebiger Schreibweise —
  geprüft mit `ident.CheckName` bzw. `ident.ParseAddress`; Accounts und Nodes zusätzlich mit
  `ident.CheckPrincipalName` (`admin` reserviert, er steht im Protokoll für den Verwalter).
  Die Pfadregeln für Dokumentnamen stehen nur in `ident` (`CheckDocName`, `DocDirPrefix`);
  Hub-Store und Replica benutzen sie. Node- und Account-Namen sind gemeinsam eindeutig,
  geprüft über die Tabellen `accounts` ↔ `nodes` in beide Richtungen (nicht mehr über
  `SYSTEM:A:`-Zeilen) und abgesichert in der Datenbank über `principal_names` (Primärschlüssel,
  `INSERT … ON CONFLICT DO NOTHING` in derselben Transaktion wie Anlegen, Löschen beim
  Entfernen; eine Verletzung ergibt `ErrExists` mit derselben Meldung wie die Vorprüfung). Die
  Tabelle ist abgeleitet: nicht im Export, `config import` baut sie neu auf. Nach `hub account
  rm` ist der Name frei. **Users** gehören nicht dazu: Namensregel wie Accounts, `admin`
  reserviert, geprüft mit `store.CheckUser` (CLI und Import); ein User darf wie ein Node oder
  ein anderer Account heißen.
- **Accounts am Hub.** Die Tabelle `accounts` führt Beschreibung, gesperrt, die gemerkten
  Rechte eines gesperrten Accounts und **maßgeblich Hash und User** (`"user"`, Index
  `accounts_user`); die `SYSTEM:A:`-Zeilen je Account und Collection tragen Rechte und eine
  Kopie von Hash und User. **Urheber:** `created_by`/`updated_by` ist der User des schreibenden
  Accounts, die CLI am Hub schreibt `admin`; `actions.account` bleibt der Account. Ein
  Schreibvorgang über einen Node trägt User, Account und Node (`docTx`) und schreibt je
  Dokument eine Zeile in `actions` mit `account`, `carrier` = Node und `action` (`create`,
  `update`, `delete`, `rename`) unter seiner Revision; die CLI schreibt `admin` ohne Träger.
  Recht: `write` für Neues und Eigenes (`created_by` = User des Accounts), `supersede` für
  Fremdes, `write` dafür nicht nötig; bei einem Verzeichnis je Dokument. `set --user` schreibt
  alle lebenden Zeilen des Accounts unter einer Revision neu, Löschmarken und Dokumente
  bleiben. Jede Änderung an den Zeilen
  ist ein Schreibvorgang mit Revision und genau einer Zeile in `actions` (`admin`, bei `rotate`
  der Account mit dem Node als `carrier`) und schreibt Hash in `accounts` und Zeilen in
  derselben Transaktion. **Die Zeile in `accounts` wird zuerst gesperrt**: Die erste Anweisung
  jedes Schreibvorgangs an einem Account (`writeAccount`) ist `UPDATE accounts SET name = name
  WHERE name = $1`, erst danach wird gelesen — kein `SELECT … FOR UPDATE` (SQLite). `rotate`
  beginnt stattdessen mit dem bedingten Schreiben (`… AND token_hash = $3 AND locked = 0`) und
  prüft die Zahl der Zeilen, liest danach den User; der Import sperrt vorher alle Zeilen von
  `accounts`. Die Schreibvorgänge über einen Node (`writeAs` in `hub/store/write.go`) sperren
  ebenso zuerst die Zeile ihres Accounts (`lockAccount`) und lesen erst danach Hash, Sperre und
  User. Zeilen werden
  nie entfernt: Löschmarke, und bei erneutem `grant` wiederbelebt — auch wenn die Collection
  entfernt wird: Löschmarken von `SYSTEM:A:`-Zeilen blockieren das Entfernen nicht (CLI und
  Import) und bleiben stehen; lebende Zeilen und gemerkte Rechte eines gesperrten Accounts
  blockieren weiter.
- **Import prüft wie die CLI.** `config import` benutzt dieselben Prüffunktionen
  (`CheckTables` je Store) und prüft alles, bevor geschrieben wird; erst der Hub, dann der
  Node. Exportformat 5 trägt die Accounts samt User und Rechten; der Import gleicht die
  `SYSTEM:A:`-Zeilen unter einer Revision an. Ein Export vor Format 4 lässt die Accounts und
  darf keinen Accounts-Teil tragen, in keiner Form (geprüft am YAML-Knoten); ab Format 4 ist
  null ein Fehler, nur `accounts: []` leert. `user` je Account ist ab Format 5 Pflicht
  (fehlend oder null am YAML-Knoten geprüft, leer oder ungültig mit `store.CheckUser`); Format 4
  darf ihn nicht tragen und setzt ihn auf den Namen des Accounts.
- **`serve` lauscht nur auf Loopback**, beide Rollen, bis `https`/`ssh` kommen, und beide
  prüfen `Host` (`loopback`); ein Tunnel geht nur mit gleichem Port. Er nimmt je
  Rolle eine Sperre (`flock` auf `<db>.lock` neben der Datenbank); CLI-Kommandos laufen daneben
  über SQLite, `status` prüft die Sperre ohne zu warten. Ein Log je Anfrage auf stderr über
  `reqlog`. Als Node gleicht `serve` im Hintergrund ab (`cmd/kephalaion/bgsync.go`): beim Start
  und je `sync_interval` (`settings`, je Runde gelesen, `0` aus), je Hub-Eintrag eine
  Goroutine, verdrahtet über `connector` wie `node sync` — `local` bekommt den Hub-Store
  desselben `serve`. `backgroundSync.kick` stößt einen Eintrag außer der Reihe an — nach einem
  Schreibvorgang über MCP (Erfolg oder unklarer Ausgang), auch bei `sync_interval` `0`; läuft
  der Abgleich des Eintrags schon, folgt genau einer. Beim Beenden bricht er ab, bevor die
  Stores schließen; `serve` wartet darauf höchstens `shutdownGrace`. Log nur bei Zeilen, erstem
  Fehler, Wechsel der Fehlerart (`replica.ErrorKind`) und Erholung.
- **Nebenläufigkeit am Node:** `node sync`, `node hub rm|add`, `config import` und `rotate`
  laufen als eigene Prozesse neben `serve`. Keine Sperre über Prozesse: Jedes Schreiben des
  Abgleichs ist an `hubs.entry_id` gebunden (ULID, beim Anlegen vergeben, nie wiederkehrend,
  `config import` behält sie für bleibende Aliase), die auch in `db_info` der Replica steht.
  Jede schreibende Transaktion der Replica prüft zuerst `entry_id`, `hub_id` und dass kein
  Stand unter dem `since` der Seite liegt (`ErrChanged`); `SetHubID` und `RecordSync` in
  `node.db` schreiben nur für die `entry_id` (`ErrEntryGone`). Zeilen per id und Stände gehen
  nur vorwärts. `replica.Create` baut unter eigenem Namen und linkt fertig an den Ort.
- **Stand des Abgleichs** je Hub in `hub_sync` (`node.db`): abgeleitet, nicht im Export,
  `node hub rm` und `config import` räumen mit ab; `Syncer.SyncEntry` schreibt ihn (für
  `serve` und `node sync`), außer bei Abbruch oder entferntem Eintrag.
- **MCP am Node:** `internal/node/mcpnode`. Clients melden sich je Hub mit
  `X-Keph-Account-<alias>` und `X-Keph-Token-<alias>` an; der Node prüft sie je Anfrage gegen
  die Replica, ohne Cache und ohne die Replica offen zu halten — über alle Hub-Einträge in
  einem Schritt (`Authenticate`, `ok`/`invalid`/`missing`, dazu unbekannte Aliase), auf dem
  jedes Werkzeug aufsetzt. Die Antwort von `whoami` baut `mcpnode.Whoami`, dieselbe Funktion
  für das Werkzeug und für `kephalaion node whoami` (dort `AccountLogins` ohne Token).
  `list`, `read` und `changes` lesen nur aus der Replica: lesbar ist eine Collection mit
  gültiger Anmeldung und lebender `SYSTEM:A:`-Zeile des Accounts; alles andere ist dieselbe
  Meldung „nicht lesbar“, eine fehlende Replica „noch nie abgeglichen“. Eine unlesbare Replica
  betrifft nur ihren Hub (feste Meldung ohne Pfad, `unreadable_hubs`; die volle Meldung ins
  Log), ein abgebrochener ctx ist ein Fehler der Anfrage. Nie `SYSTEM:`-Namen, Löschmarken nur
  in `changes`. Gelesen wird ohne Transaktion; `changes` erkennt eine neu angelegte oder
  geleerte Replica an der `generation` in ihrem `db_info`, die es vor allem anderen liest.
  **Schreiben** (`create`, `write`, `delete`, `rename`, `write.go`), in dieser Folge: Adresse,
  Name und neuer Name prüfen; wie beim Lesen Anmeldung, Replica und Lesbarkeit — ohne den Hub
  zu fragen; dann `HubLink.Connect` und Account samt Token aus dem Header an den Hub. Nach
  Erfolg kommen die Zeilen der Antwort per `replica.WriteRows` in die Replica, bevor der Client
  antwortet (scheitert das: Erfolg mit `note`), danach `HubLink.Sync` — ebenso nach unklarem
  Ausgang. Ein Fehler ist `isError` mit der Meldung als Text und `error.code`/`error.message`
  in der Struktur; `account_unauthenticated` des Hubs wird `not_readable`, `unauthenticated`
  und `unsupported_version` gehen durch. Meldungen ohne Adresse und Transport des Hubs; das
  Log nennt Vorgang, Hub, Node, Account und Code, nie Token oder Inhalt. Grenze: Schreiben
  setzt voraus, dass die Replica Collection und Account-Zeile trägt; „noch nie abgeglichen“
  nennt `kephalaion node sync <hub>`.
- **Keine Migrationen, Schema neu anlegen** — befristet, solange es keine Daten gibt, die
  bleiben müssen. Ändert sich das Schema, wird `SchemaVersion` im Store-Paket erhöht;
  vorhandene Datenbanken werden dann abgelehnt und neu angelegt. Dokumente am Hub gelten
  vorerst als wiederherstellbar per `hub import`; die Replica gleicht sich neu ab.

## Bauen

- Über das `Makefile` im Wurzelverzeichnis, lokal und in CI gleich: `make build` bzw.
  `make dist-host` für diese Plattform, `make dist` für alle vier (linux/darwin ×
  amd64/arm64) plus `SHA256SUMS` nach `dist/` (gitignored), `make dev-install` ersetzt
  `~/.local/bin/kephalaion` und startet einen laufenden Dienst pro User neu.
- Flags stehen nur im Makefile: `CGO_ENABLED=0`, `-trimpath`, `-buildvcs=false`, Version
  und Commit per `-ldflags -X` in `internal/buildinfo`. Ohne `VERSION=` entsteht ein
  dev build.
- Die Toolchain ist die `toolchain`-Zeile in `go.mod`. CI baut mit `GOTOOLCHAIN=local` und
  prüft sie mit `make check-toolchain`. Wer sie anhebt, hebt sie nur dort an.
- Actions in `.github/workflows/` sind auf Commit-SHA gepinnt, mit Versionskommentar;
  Dependabot hält `gomod` und `github-actions` wöchentlich nach, mit PRs gegen `dev`.

## Testen

- Vor jedem Commit mit Code mindestens `make check-quick` (gofmt, `go vet`, `go test -short`,
  `sh -n install.sh`); für Zwischenstände genügt das. Vor dem Abschluss einer Task, auch unter
  `/k-task-run`, und bei Änderungen an `serve` oder dem Abgleich im Hintergrund (`bgsync`)
  vollständig `make check` (dasselbe mit allen Tests).
- **Langsam** ist, was die Messung so ausweist: mehr als etwa 0,5 s, oder der Test wartet auf
  `sync_interval`, Timer oder Runden. Dass er `serve` startet, reicht nicht. Ein langsamer Test
  beginnt mit `slow(t, "…")` (`cmd/kephalaion/main_test.go`) und wird unter `-short`
  übersprungen; kompiliert und von `go vet` gesehen wird er immer. Ohne `-v` ist das
  Überspringen nicht zu sehen; den vollständigen Lauf verlangt `release`.
- `shellcheck` über `install.sh` läuft nur in CI; lokal ist es nicht installiert (zur Not:
  `docker run --rm -v "$PWD:/mnt" -w /mnt koalaman/shellcheck:stable install.sh`). Keine
  typografischen Anführungszeichen in `install.sh` (SC1111).
- CI prüft in zwei Suiten: **quick** (`make check-quick`) für einen Push auf `dev`, **full**
  (`make check`) für einen Push auf `main` und jeden Pull Request; von Hand wählt der Input
  `suite` (Vorgabe `full`), dazu eine optionale `kennung`. Der Name des Laufs nennt beides
  (`CI full, workflow_dispatch dev, kennung …`); daran erkennt `release` den vollständigen Lauf.
- CI hat zwei Jobs: `check` auf Ubuntu (Suite, `make dist`, shellcheck) und `macos` auf
  `macos-latest` (Suite, `make dist-host`, der LaunchAgent des gebauten Binarys mit
  `plutil -lint`, `install.sh` mit dem neuesten Release in leerem `HOME`, der Tag aus der
  Weiterleitung von `releases/latest` — keine Anfrage an die GitHub-API ohne Token). **Der
  Job `macos` ist seit 2026-09-26 abgeschaltet** (`if: false`, auf Wunsch des Nutzers):
  `TestBackgroundSync` scheiterte dort fast immer an einer Race-Condition im Test. Task 013 hat
  sie behoben; der Job bleibt trotzdem vorerst aus. Die Definition bleibt, Dependabot pflegt
  ihre Actions; ein übersprungener Job lässt den Lauf grün. Wieder einschalten: `if: false`
  entfernen. Tests vergleichen Pfade des Binarys aufgelöst (`filepath.EvalSymlinks`): Auf
  macOS liegt das temporäre Verzeichnis hinter einem Link.
- Von Hand, weder in `make check` noch in CI: `make race` (Race-Detector, braucht cgo und
  einen C-Compiler), `make cover` (Abdeckung je Paket einschließlich der Tests anderer Pakete,
  Bericht nach `coverage/`) und `make mutate` (Mutationstests mit gremlins, per `go run` in
  fester Fassung, in einer Kopie von `go.mod`, `go.sum`, `cmd` und `internal`;
  `MUTATE=./internal/<paket>` für ein Paket).
- Tests gegen GitHub laufen über `httptest`; die Basis-URL der API ist dafür im
  `upgrade.Upgrader` überschreibbar. Sie ist keine Nutzeroption und wird nicht dokumentiert.

## Branches

- **`dev`** ist der Arbeitsbranch: dort wird gearbeitet und gesichert. Auf `dev` darf jeder
  ohne Prüfung und ohne Rückfrage sichern und pushen, auch die KI und `/k-task-run` — `dev`
  ist nur die Sicherung des Arbeitsstands. CI prüft einen Push auf `dev` nur mit den schnellen
  Tests; vollständig geprüft wird vor dem Release.
- **`main`** trägt nur veröffentlichte Stände und rückt allein über `release` per
  Fast-Forward auf `dev` vor, nach einem grünen vollständigen CI-Lauf auf `dev`; der Push auf
  `main` prüft noch einmal vollständig. Nach `main` wird nie direkt committet oder gepusht.
  Das ist Regel, keine Sperre: Das Ruleset „main und dev“ auf GitHub sperrt für beide
  Branches nur Force-Push und Löschen. Weicht `main` doch ab (etwa ein auf GitHub gemergter
  PR), erkennt `release` das in Schritt 6 und bricht ab.
- `main` bleibt Standard-Branch auf GitHub: Ein Clone bekommt den Release-Stand; wer arbeiten
  will, wechselt nach `dev` (`git switch dev`).
- **Dependabot:** Versions-Updates kommen als PR gegen `dev` (`target-branch` in
  `.github/dependabot.yml`; wirkt erst, wenn `main` die Datei enthält). Security-Updates gehen
  nach GitHub-Vorgabe immer gegen `main`. Solche PRs nicht auf GitHub mergen, sondern lokal
  nach `dev` holen (`git fetch origin`, `git merge origin/<branch>`, sichern); enthält `main`
  die Commits nach dem nächsten Release, markiert GitHub den PR als gemergt.

## Release

- Ein Release ist ein Tag `vX.Y.Z` (mit Suffix `-…` eine Vorabversion). Es gibt keine
  `VERSION`-Datei und keine versionierte `SHA256SUMS`.
- Anlegen nur über `make -C k-playbook-local release VERSION=vX.Y.Z`, von `dev` aus. Alle
  Prüfungen laufen vor dem ersten Push: gültige Version, Branch `dev`, sauberer Arbeitsbaum,
  `HEAD == origin/dev` (nach `git fetch`), `origin/main` Vorfahre von `HEAD`, Tag weder auf
  `origin` noch lokal auf einem anderen Commit, zuletzt die CI mit `gh`:
  - Es zählt der neueste vollständige Lauf von `ci.yml` auf `dev` für `HEAD` (Name
    `CI full, …`); ein schneller zählt nicht.
  - Gibt es keinen, stößt `release` ihn an (`gh workflow run ci.yml --ref dev -f suite=full`,
    mit einer Kennung) und findet ihn über die URL, die `gh workflow run` zurückgibt, sonst
    über die Kennung im Namen des Laufs — nicht einfach den neuesten.
  - Er zählt nur, wenn sein `headSha` gleich `HEAD` ist, sonst Abbruch: `--ref dev` nimmt die
    Spitze von `dev` beim Anstoß.
  - Läuft er noch, wartet `release` darauf (`gh run watch --exit-status`), statt einen zweiten
    anzustoßen. Ist er rot, bricht es mit der URL ab. Einen neuen Versuch gibt es nur
    ausdrücklich (`gh run rerun <id>`, danach `release` erneut); `release` wiederholt nie
    selbst.
  - Nach der CI holt es `origin` erneut und wiederholt die Prüfungen dort: `HEAD` gleich
    `origin/dev`, `origin/main` Vorfahre, Tag nicht auf `origin`.

  Danach schiebt es `main` per Fast-Forward auf `HEAD` (`git push origin
  HEAD:refs/heads/main`, ohne Force), legt den Tag annotiert an, pusht ihn und zieht den
  lokalen `main` nach.
- Bricht es nach dem Push von `main` ab, wird es einfach wiederholt: `main` steht dann schon
  auf `HEAD`, ein lokal schon angelegter Tag auf `HEAD` wird nur noch gepusht.
- Den Rest macht `.github/workflows/release.yml` (Workflow „Release“): `make check`, `make
  dist`, Release als Entwurf, Assets (vier Binaries, `SHA256SUMS`, `install.sh`), dann
  veröffentlichen. `latest` bekommt nur die höchste Version ohne Suffix.
- Scheitert ein Lauf, wird er über `workflow_dispatch` mit dem Tag wiederholt
  (`gh workflow run release.yml -f tag=vX.Y.Z`); vorhandene Assets werden ersetzt.
- Ein Release wirkt öffentlich: Tag, Push und Veröffentlichung nur nach Rückfrage beim
  Nutzer. Das gilt für `release` (und damit für jeden Push nach `main`), nicht für Pushes auf
  `dev`. Ein v0.x-Release gilt nicht als Veröffentlichung im Sinne der Namensprüfung
  (`docs/konzept.md`); die ist erst vor v1.0 fällig.

## Sichern

- `make -C k-playbook-local sichern [MSG=…]` committet alles und pusht nach `origin dev`,
  ohne Prüfung — für Zwischenstände, nicht für Releases. Es läuft nur auf `dev`; auf einem
  anderen Branch bricht es ab. Ist `dev` auf `origin` weiter, lehnt der Push ab: dann
  `git pull --no-rebase origin dev` und erneut sichern. Da `git add -A` alles nimmt, vorher
  sehen, was im Arbeitsbaum liegt.
