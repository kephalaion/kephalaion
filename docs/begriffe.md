# Begriffe

Verbindliche Namen. Begriffe sind englisch — in Code, Konfiguration, Befehlen und Werkzeugen;
die Dokumentation ist vorerst deutsch. Wer einen neuen Begriff braucht, trägt ihn hier ein,
bevor er ihn benutzt.

Ausführlich: [`konzept.md`](konzept.md).

## Programm und Rollen

- **kephalaion** — Produkt, Paket, Binary. Kurzform im Gespräch: Keph.
- **serve** — `kephalaion serve`, der Dienst. Trägt die Rollen, die in der Konfiguration
  stehen: `hub:`, `node:` oder beide in einem Prozess. Keine eigene Rolle und kein eigener
  Eintrag in der config, sondern der eine Aufruf, der nicht endet: Er lauscht je Rolle auf
  ihrem `listen` (MCP für Clients unter `/mcp`, der Vertrag für Nodes unter `/v1/`), bisher
  nur auf Loopback. Als Node gleicht er im Hintergrund ab (`sync_interval`); später hält er
  den Index warm. Alle anderen Kommandos sind kurze Aufrufe und arbeiten neben ihm direkt auf
  der Datenbank. Beendet durch SIGINT/SIGTERM, mit Frist; ein laufender Abgleich bricht ab.
- **lock file** (Sperrdatei) — `<db>.lock` neben der Datenbank einer Rolle. `serve` hält darauf
  eine exklusive Sperre (`flock`), solange es läuft; ein zweiter `serve` auf derselben Rolle
  scheitert daran. `status` prüft sie, ohne zu warten, und zeigt, ob `serve` läuft. CLI-Kommandos
  kümmern sich nicht darum.
- **config** — `~/.config/kephalaion/config.yaml` pro User, `/etc/kephalaion/config.yaml`
  global. Sagt nur, welche Rollen eingerichtet sind, wo ihre Datenbank liegt (`db:`) und wo
  ihr Dienst lauscht (`listen:`). Alles andere steht in der Datenbank der Rolle. Gefunden wird
  sie ohne Angabe: `--config` > `KEPHALAION_CONFIG` > die des Users, wenn es sie gibt > die
  globale, wenn es sie gibt > der Ort des Users (dort legt `init` an); `status` nennt Ort und
  **Quelle** (`--config`, `KEPHALAION_CONFIG`, pro User, global).
  `kephalaion config show` zeigt sie samt den `settings` je Rolle.
  - **config set** / **config unset** — `kephalaion config set <rolle> <schlüssel> <wert>`
    setzt einen bekannten Schlüssel der `settings` einer Rolle, nach Prüfung des Werts;
    `config unset <rolle> <schlüssel>` entfernt ihn, dann gilt sein Standard. Unbekannte
    Schlüssel werden abgewiesen. Bisher kennt nur der Node einen: `sync_interval`.
  - **export** — `kephalaion config export`: sichert config, `settings` und lokale Tabellen
    je Rolle als YAML mit einer Fassung des Formats (`format: 4`, darin `tables:`; am Hub
    auch die Accounts samt Rechten), getrennt von den Inhalten; ohne `db_info` und `actions`.
  - **import** — `kephalaion config import <datei>`: schreibt einen Export in bereits
    eingerichtete Rollen und ersetzt dort je Rolle `settings` und lokale Tabellen; ein
    Export im Format 1 ersetzt nur die `settings`, einer vor Format 4 lässt die Accounts. Am
    Hub gleicht er die `SYSTEM:A:`-Zeilen an die Accounts des Exports an, unter einer
    Revision. Die config bleibt unverändert.
- **db address** (db-Adresse) — der Wert von `db:` in der config: `sqlite:///<absoluter
  Pfad>`; `postgres://…` ist vorgesehen.
- **settings** (Einstellungen) — Tabelle `settings (key, value)` in der Datenbank jeder Rolle:
  alles, was nicht in der config steht. Wird mit `config export` gesichert, mit `config set`
  gesetzt.
- **sync_interval** — Schlüssel der `settings` des Nodes: Abstand des Abgleichs im
  Hintergrund, eine Go-Dauer (`30s`, `2m`), mindestens `1s`; `0` schaltet ihn ab, auch den
  beim Start — nicht aber den Abgleich, den ein Schreibvorgang über MCP für seinen Hub
  anstößt. Standard 30 s. `serve` liest ihn je Runde, ein Neustart ist nicht nötig.
- **db_info** — Tabelle `db_info (key, value)` in der Datenbank jeder Rolle und in jeder
  Replica. Hält die Schemafassung (`schema_version`), die Rolle (`role`: `hub`, `node` oder
  `replica`), die Anlagezeit (`created_at`), am Hub auch die Revision (`revision`), in der
  Replica die `hub_id`, die `entry_id` des Hub-Eintrags und die `generation`. Passt Fassung oder
  Rolle nicht, wird die Datenbank nicht benutzt.
- **init** — `kephalaion hub init`, `kephalaion node init`: richtet eine Rolle ein — Datenbank,
  Schema, Abschnitt in der config. Nur `init` legt eine Datenbank an; einzige Ausnahme ist die
  Replica, die der erste `sync` anlegt. Ohne `--config` und `KEPHALAION_CONFIG` nur pro User:
  Gibt es die globale config, bricht es ab. Nennt am Ende den nächsten Schritt, den Dienst;
  einrichten tut es ihn nicht.
- **status** — `kephalaion status`: welche config gilt und woher, ob der Dienst eingerichtet
  ist und läuft (Zeile `Dienst:`), welche Rollen eingerichtet sind, wo ihre Datenbank liegt,
  welche Verbindungen bestehen; am Node je Hub der Stand des Abgleichs aus `hub_sync`. Exit 1,
  wenn eine Datenbank fehlt oder nicht passt oder es die config des Users und die globale
  nebeneinander gibt (zwei Arten auf einem Rechner); bei der globalen config ohne Leserecht
  auf die Datenbanken nur ein Hinweis, Exit 0.
- **client** (MCP-Client) — was per MCP mit dem Node redet: Claude Code, Cursor, OpenCode,
  k-playbook. Für ihn ist der Node der MCP-Server. Die KI im Client sieht Name und Token nicht.
- **header** (Header-Paar) — wie ein Client sich am Node anmeldet, je Hub ein Paar:
  `X-Keph-Account-<alias>` und `X-Keph-Token-<alias>`, eingetragen in seiner MCP-Konfiguration.
  Header-Namen zählen ohne Groß- und Kleinschreibung; der Alias ist der Rest des Namens nach
  dem Präfix, klein geschrieben. Der Node prüft das Paar bei jeder Anfrage gegen die
  `SYSTEM:A:`-Zeilen der Replica dieses Hubs, ohne Cache — über alle Hubs in einem Schritt
  (`mcpnode.Authenticate`), auf dem jedes Werkzeug aufsetzt. Zum Hub meldet sich der Node selbst
  mit `X-Keph-Node` und `Authorization: Bearer <token>`.
- **node** (Knoten) — Rolle, Abschnitt `node:`. Einmal je Rechner. MCP-Server über HTTP
  für Clients, Client eines oder mehrerer Hubs. Hält Replica, Index, Suche,
  Revisionen.
- **hub** (Zentrale) — Rolle, Abschnitt `hub:`. Einmal je Installation. Hält Store, Journal,
  Accounts, Token. Einziger Schreiber. Kein MCP, sucht nicht. Eigene Datenbank, getrennt von
  der Replica eines Nodes im selben Prozess.
- **transport** — wie ein Node einen Hub erreicht: `https`, `ssh` (dasselbe HTTP, getunnelt)
  oder `local` (Funktionsaufruf im selben Prozess, mit denselben Prüfungen). Dazu `http` ohne
  TLS, nur für `localhost` — zum Testen des HTTP-Wegs auf einem Rechner.
- **bridge** (Brücke) — *zurückgestellt.* Wäre der Prozess, den ein Client über stdio
  startet, und reichte an den Node weiter. Nur falls ein Client zwingend stdio braucht.

## Daten

- **store** (Bestand) — alle Collections eines Hubs.
- **address** (Adresse) — `<hub>:<collection>`, wie Node und Clients eine Collection
  ansprechen. Den Hub-Namen vergibt der Node.
- **collection** (Sammlung) — unabhängige Einheit des Stores, gemeint ist der Inhalt, nicht
  der Speicherort. Keine Überschneidung mit anderen. Einheit für Rechte und Abgleich. Ein Hub
  hat mehrere. Ersetzt den Begriff „Bereich“ aus dem Konzept.
- **document** (Dokument) — eine Datei im Store. Stabile `id`; der Name ist sein Pfad und
  kann sich ändern.
- **name** (Name) — Pfad eines Dokuments in seiner Collection, Segmente durch `/` getrennt;
  eindeutig je Collection. Verzeichnisse gibt es nur als Präfix vorhandener Namen. Regeln in
  `konzept.md`, „Datenmodell“. Einzige Ausnahme: `SYSTEM:`-Zeilen.
- **deleted** (Löschmarke) — ein gelöschtes Dokument: Die Zeile bleibt mit `deleted = 1`, ohne
  Inhalt und mit neuer Revision, damit der Abgleich davon erfährt. Der Name ist danach wieder
  frei; eine Neuanlage bekommt eine neue `id`.
- **doc** — `kephalaion hub doc put|get|list|rm`: Dokumente am Hub. `put` legt an oder
  ersetzt (Admin-Upsert; unveränderter Inhalt zählt keine Revision), `rm` setzt eine
  Löschmarke. Am Node liest `kephalaion node doc list|get <hub>:<collection> …` aus der
  Replica, ohne Löschmarken und `SYSTEM:`-Zeilen.
- **hub import** — `kephalaion hub import <collection> <verzeichnis>`: spielt ein Verzeichnis
  als Dokumente ein, Name = relativer Pfad; ein Schreibvorgang, eine Revision. Nicht zu
  verwechseln mit `config import`.
- **personal** (persönliches Verzeichnis) — *vorgemerkt.* Eigenschaft eines Verzeichnisses:
  Auflisten, Lesen und Schreiben zeigen nur Dokumente des eigenen Users, der Schalter `all`
  alles; die Suche bleibt unberührt. Eine Ansicht, kein Recht — anders als eine private
  Collection, die eine Rechtegrenze ist.
- **numbered** (nummeriert) — *vorgemerkt.* Eigenschaft eines Verzeichnisses: Namen werden vom
  Hub fortlaufend nummeriert. Ersetzt den früheren Gedanken der „Reihen“ (`series_*`).
- **mask** (Maske) — Glob auf das letzte Segment eines Namens (`*.md`, `0*-*.md`), etwa bei
  `list`; kein regulärer Ausdruck.
- **directory** (Verzeichnis) — ein Präfix von Namen bis zu einem `/`; es gibt es, solange ein
  lebendes Dokument darunter liegt. `list` zeigt die Verzeichnisse der nächsten Ebene als
  eigene Einträge (Art `directory`), `read` erkennt eines an seinem Namen.
- **list** — Werkzeug des Nodes: Inhalt eines Verzeichnisses aus der Replica, ohne Löschmarken
  und `SYSTEM:`-Namen; ohne `collection` die lesbaren Collections. Sortiert nach `name`,
  `created` oder `updated`, geblättert mit `limit` und `cursor`, gefiltert mit `mask`.
- **read** — Werkzeug des Nodes: ein Dokument aus der Replica, per Name oder `id`; Art
  `document`, `directory` oder `none` (kein Fehler). Mit `content: false` nur die Angaben —
  so beantwortet die Erweiterung für VS Code `stat`. Löschmarken sind `none`.
- **writable** (schreibbar) — Angabe von `read`: Der Account hat `write` in der Collection.
- **changes** — Werkzeug des Nodes: je Dokument, das sich seit dem `cursor` (oder seit einem
  Zeitpunkt, `since`) geändert hat, einmal der aktuelle Stand, Löschmarken eingeschlossen, ohne
  alten Namen. Ohne beides nur der `cursor` für „ab jetzt“. Dazu **reset** — Hubs, deren
  Replica seit dem `cursor` neu angelegt oder geleert wurde (andere `generation`); der Aufrufer
  liest sie neu mit `list` — und **dropped** — Collections des `cursor`, die nicht mehr lesbar
  sind; ihre Dokumente verschwinden ohne Löschmarke.
- **cursor** — undurchsichtige Angabe in der Antwort von `list` und `changes`, mit der der
  nächste Aufruf weiterfragt; der Client gibt sie unverändert zurück. Bei `list` die Stelle
  nach dem letzten Eintrag, bei `changes` der Stand je Collection und die `generation` je Hub.
- **create** / **write** / **delete** / **rename** — *geplant, Task 014.* Werkzeuge des Nodes
  und Vorgänge des Vertrags, die schreiben: anlegen (scheitert an einem lebenden Namen),
  ersetzen, löschen (Löschmarke) und umbenennen (`id` bleibt). `delete` und `rename` nehmen
  auch ein Verzeichnis, als Ganzes. Der Node prüft Anmeldung und Lesbarkeit und reicht an den
  Hub; der Hub prüft `write`/`supersede` und die Vorbedingung. Nie wiederholt.
- **base_revision** — *geplant, Task 014.* Die Revision, auf der ein `write`, `delete` oder
  `rename` beruht. Weicht die des Dokuments am Hub ab, lehnt er ab (`stale_revision`).
- **recursive** — *geplant, Task 014.* Angabe bei `delete`: ein Verzeichnis mit allen
  Dokumenten darunter löschen; ohne sie ist ein Verzeichnis kein Ziel von `delete`.
- **write error codes** (Fehlercodes beim Schreiben) — *geplant, Task 014.* `name_taken`
  (Name vergeben), `stale_revision` (Revision veraltet), `path_conflict` (Name wäre zugleich
  Datei und Verzeichnis), `not_found`, `forbidden` (Recht fehlt), `not_readable` (Collection
  für diesen Account oder Node nicht lesbar). Am Node dazu `unreachable` (Hub nicht erreicht,
  nichts gespeichert) und `outcome_unknown` (**Ausgang unklar**: abgeschickt, keine brauchbare
  Antwort — kann gespeichert sein; wie bei `rotate`, `contract.ErrOutcomeUnknown`),
  `unsupported` (**noch nicht unterstützt**: der Hub kennt den Vorgang nicht, oder der Node
  erreicht ihn über einen Transport, den er noch nicht kann — nichts gespeichert) und
  `internal` (ein Fehler des Nodes selbst, etwa `node.db` oder eine Replica nicht lesbar —
  nichts abgeschickt). Die Werkzeuge melden den Code strukturiert neben der Meldung.
- **tool** (Werkzeug) — ein MCP-Werkzeug des Nodes für Clients. Gesammelt in `konzept.md`,
  „Werkzeuge“.
- **id** (Kennung) — stabile Kennung eines Dokuments, vom Hub vergeben.
- **journal** (Journal) — fortlaufende Folge der Änderungen eines Hubs.
- **revision** (Stand) — fortlaufende Nummer je Hub über alle Collections, vergeben beim
  Schreiben. Der Node merkt sich je Collection die letzte und fragt „alles seit Revision X“.
- **replica** (Kopie) — der Ausschnitt des Stores auf einem Node: je Hub-Eintrag eine eigene
  SQLite-Datei `replicas/<alias>.db` neben `node.db` (Verzeichnis `0700`), in `db_info` mit
  der Rolle `replica`, der `hub_id`, der `entry_id` und der `generation`. Darin `documents` wie am Hub, aber ohne eindeutigen
  Index auf den Namen — auf dem Node zählt die `id` —, und `sync_state`. Abgeleitet, nie
  selbst beschrieben: Sie enthält genau die Zeilen, die der Hub geliefert hat, und lässt sich
  jederzeit neu abgleichen. Der erste `sync` eines Hub-Eintrags legt sie an; `node hub rm`
  löscht sie mit, ebenso `config import` für Aliase, die im Export fehlen. Eine Replica mit
  fremder Schemafassung, fremder `entry_id` oder eindeutig beschädigt verwirft `sync` und legt
  sie neu an. Lässt sie sich nicht lesen, zeigt `whoami` für ihren Hub `login` `missing` und
  „Replica nicht lesbar“; die übrigen Hubs betrifft das nicht.
- **generation** (Generation der Replica) — Kennung in `db_info` der Replica, eine ULID. Sie
  wechselt bei jeder Neuanlage und jedem Leeren der Replica (andere `hub_id`, Hub aus einer
  Sicherung), sonst nie — auch bei gleicher `hub_id`. `changes` trägt sie je Hub im Cursor und
  meldet `reset`, wenn sie nicht mehr passt.
- **sync_state** — Tabelle der Replica: je Collection der Stand des Abgleichs (`revision`) und
  der Zeitpunkt der letzten Seite (`synced_at`).
- **contract** (Vertrag) — die Schnittstelle zwischen Node und Hub, beschrieben in
  [`vertrag.md`](vertrag.md), im Code das neutrale Paket `internal/contract`. Trägt eine
  **Fassung** (`version`, derzeit 1); der Hub nennt sie in jeder Antwort.
- **sync** (Abgleich) — der Vorgang des Vertrags, mit dem ein Node je Collection alles seit
  einer Revision holt, in Seiten; jede Seite ist eine Transaktion in der Replica. Collections,
  die der Hub nicht erlaubt oder der Node nicht mehr will, entfernt er aus der Replica; bei
  anderer `hub_id` oder einem Stand über der Revision des Hubs gleicht er von vorn ab.
  Kommando: `kephalaion node sync [<alias>]`, über `transport local` oder `http`; scheitert ein
  Hub-Eintrag, laufen die übrigen weiter, der Exit-Code ist 1. **Im Hintergrund** gleicht
  `serve` selbst ab: beim Start je Hub-Eintrag, danach je `sync_interval`, jeder Eintrag für
  sich, `https`/`ssh` übergangen. Beide halten das Ergebnis in `hub_sync` fest.
- **hub_sync** (Stand des Abgleichs) — Tabelle in `node.db`: je Hub-Eintrag letzter Erfolg und
  letzter Fehler mit Zeit und Art (`error_kind`: `connect`, `unreachable`,
  `unauthenticated`, `unsupported_version`, `hub`, `protocol`, `replica`); ein Erfolg leert
  den Fehler. Geschrieben von `serve` und `node sync`, gezeigt von `status` und `whoami`.
  Abgeleitet: nicht im Export; `node hub rm` und `config import` räumen mit ab.
- **page** (Seite) — eine Antwort des Abgleichs: ganze Revisionen, bis die **page size**
  (Seitengröße, Zeilen je Seite, Standard 500, am Hub höchstens 5000) erreicht ist; eine
  einzelne größere Revision kommt ganz. **until** (`bis`) ist die Revision, bis zu der der
  Node danach alles hat — auf der letzten Seite, auch einer leeren, die Revision des Hubs
  (**hub_revision**); **more** (`mehr`) sagt, dass eine weitere Seite folgen kann. Der Node
  setzt den Stand jeder angefragten Collection auf max(`since`, `until`), nie zurück.

## Zugriff

- **account** (Konto) — ein Zugang: eine Zugriffsart auf einem Rechner (KI-Sitzung,
  Automatisierung, Leseprozess). Name, Token, Rechte je Collection; gehört einem **user**.
  Liegt auf genau einem Rechner — eine Regel, die nicht geprüft wird; ein zweiter Rechner mit
  demselben Token verliert den Zugang beim ersten `rotate`. Am Hub
  `kephalaion hub account add|list|show|set|rm|lock|unlock|grant|revoke|token`: User,
  Beschreibung, gesperrt und den maßgeblichen Hash führt die lokale Tabelle **accounts**; die
  Rechte je Collection stehen in den `SYSTEM:A:`-Zeilen, bei einem gesperrten Account gemerkt
  in `accounts` (`locked_rights`). Name gemeinsam mit den Nodes eindeutig.
- **user** (Nutzer) — wem ein Account gehört; ein Merkmal am Account wie ein Tag, ohne Token,
  ohne Rechte, ohne Anmeldung. Setzt nur der Admin (`hub account add|set … --user`); ohne Angabe
  der Name des Accounts. Steht in `accounts` (mit Index, `hub account list --user`), in den
  `SYSTEM:A:`-Zeilen und in `created_by`/`updated_by` der Dokumente; `whoami` nennt ihn. Ein
  User hat meist mehrere Accounts. Namensregel wie bei Accounts, nicht `admin`; er gehört nicht
  zu den gemeinsamen Namen von Nodes und Accounts (`principal_names`) und darf wie ein Node
  heißen.
- **setup token** (Einrichtungstoken) — das Token, das `hub account add` und `hub account
  token` einmal anzeigen: das erste Token des Accounts. Sein erster Vorgang tauscht es per
  `rotate` gegen ein eigenes, danach ist es wertlos.
- **token** — Geheimnis eines Accounts, Format `keph_<geheimnis>`. Jede Anfrage trägt
  Account-Name und Token. Gespeichert wird nur der Hash. Unabhängig vom Transport.
- **scope** — ein Recht eines Accounts auf einer Collection, geschrieben
  `<collection>:<recht>`. Ein Account hat mehrere. Gespeichert je Collection in der
  Account-Zeile. Rechte:
  - `read` — hat jeder in der Collection eingetragene Account.
  - `write` — anlegen; Eigenes (`created_by` = eigener User) ändern und löschen; gelöschte
    Namen neu anlegen.
  - `supersede` — Fremdes ändern, ablösen, löschen.
  - `replicate` — kein Scope eines Accounts, sondern das Recht eines Nodes: Inhalt und
    Account-Zeilen der Collection abgleichen. Steht am Hub in `node_collections`.
- **node entry** (Node-Eintrag) — ein Node am Hub: Zeile in `nodes`, Name vom Admin, Token
  (nur der Hash), gesperrt ja/nein. Name gemeinsam mit den Accounts eindeutig.
- **principal_names** (belegte Namen) — Tabelle des Hubs: je Node und Account eine Zeile
  (`name` als Primärschlüssel, `kind` `node` oder `account`). Sichert die gemeinsame
  Eindeutigkeit der Node- und Account-Namen in der Datenbank ab; geschrieben in derselben
  Transaktion wie Anlegen und Entfernen. Abgeleitet aus `nodes` und `accounts`: nicht im
  Export, `config import` baut sie neu auf.
- **hub entry** (Hub-Eintrag) — ein Hub am Node: Zeile in `hubs` in `node.db`, Alias vom
  Node, Name des Nodes am Hub (`node_name`, `--node`), Transport, Adresse, Token, `hub_id`
  (Kopie aus der Replica), `entry_id`.
- **entry_id** — Kennung eines Hub-Eintrags, eine ULID, beim Anlegen vergeben und nie wieder
  vergeben: `node hub rm` und `add` unter demselben Alias ergeben eine neue. Steht in `hubs`
  und in `db_info` der Replica, nicht im Export; `config import` behält sie für Aliase, die
  bleiben. An sie ist jedes Schreiben des Abgleichs gebunden — Seiten der Replica, `hub_id`,
  `hub_sync` —, damit ein Abgleich, der neben `node hub rm|add` oder `config import` läuft,
  nie in einen neuen Eintrag schreibt.
- **hub_id** — Kennung des Hubs, eine ULID, von `hub init` vergeben und in `db_info`
  gehalten; `status` zeigt sie. Am Node ist `db_info.hub_id` der Replica maßgeblich,
  `hubs.hub_id` in `node.db` nur Kopie für Anzeige und Export. Weicht sie ab, gleicht der Node
  von vorn ab.
- **grant** / **revoke** — `kephalaion hub node grant <node> <collection>`: gibt einem Node
  `replicate` auf eine Collection; `revoke` nimmt es zurück. `kephalaion hub account grant
  <name> <collection> [--write] [--supersede]` setzt die Rechte eines Accounts in einer
  Collection vollständig (ohne `--write` wird `write` entzogen); `revoke` macht seine Zeile
  zur Löschmarke.
- **lock** / **unlock** — `kephalaion hub node lock <name>`: sperrt einen Node; `unlock` hebt
  die Sperre auf. `hub account lock` macht alle Zeilen eines Accounts zu Löschmarken und merkt
  seine Rechte; `unlock` legt sie wieder an.
- **admin** — Account und User, als die die CLI am Hub handelt; steht in `created_by` und in
  `actions`. Als Account- und Node-Name reserviert (`ident.CheckPrincipalName`).
- **actions** (Protokoll) — Tabelle des Hubs: wer (Account, nicht User) wann was getan hat. `subject` nennt das
  Ziel einer Handlung ohne Dokument — Collection, Node oder `<node>:<collection>`.
- **--token-stdin** — liest ein Token als eine Zeile von der Standardeingabe. Ein Token wird
  nie als Argument übergeben und nur gekürzt angezeigt (`keph_…` und die letzten vier
  Zeichen).
- **--token-file** — eine Datei, die das Token eines Accounts als eine Zeile hält (`0600`), bei
  `node account rotate|check`; üblicher Ort `~/.config/kephalaion/tokens/<hub>/<account>.token`,
  `<hub>` der Alias des Hub-Eintrags. Daneben **pending** (`<datei>.pending`): das neue Token eines
  laufenden `rotate`, geschrieben vor dem Aufruf; nach Erfolg ersetzt es die Datei, bei
  unklarem Ausgang bleibt es, bis `check` es klärt.
- **check** — `kephalaion node hub check <alias>`: `whoami` am Hub, zeigt Erreichbarkeit,
  Node-Namen und erlaubte Collections und merkt beim ersten Kontakt die `hub_id`.
  `kephalaion node account check <hub> <account>`: `whoami` mit Account-Teil, ob ein Token
  gilt; löst ein liegengebliebenes `pending` auf.
- **--create** — `kephalaion node hub add <alias> --transport local --create`: legt den Node
  am Hub derselben config an und trägt sein Token direkt in `node.db` ein, ohne es anzuzeigen.
- **name rule** (Namensregel) — Namen von Collections, Nodes, Accounts und Hub-Aliasen:
  `[a-z0-9][a-z0-9._-]{0,62}`, kein `:`, kein Präfix `system` in beliebiger Schreibweise;
  Accounts und Nodes dürfen nicht `admin` heißen.
- **SYSTEM:** — reservierter Präfix im Namen eines Dokuments, nur der Hub schreibt ihn.
  `SYSTEM:A:<account>` ist die Zeile eines Accounts in einer Collection.
- **carrier** (Träger) — der Node, der eine Anfrage an den Hub trägt; meldet sich mit seinem
  eigenen Token an. Das Token des Accounts steht in der Anfrage.
- **rotate** — ersetzt das Token eines Accounts: altes Token zur Anmeldung, Hash des neuen.
  Erster Vorgang jedes Accounts; eine eigene Begrüßung gibt es nicht. Vorgang des Vertrags,
  nie wiederholt; ein Schreibvorgang mit einer Zeile `rotate` in `actions`. Am Node das
  Kommando `kephalaion node account rotate <hub> <account> (--token-file pfad |
  --token-stdin)` — ein CLI-Kommando, kein MCP-Werkzeug.
- **whoami** — Vorgang des Vertrags: bestätigt den Node, nennt die `hub_id` und seine
  erlaubten Collections und prüft wahlweise einen Account (`valid`). Am Node auch ein
  MCP-Werkzeug für Clients: Version, `update` (neueste Version und Weg, aus der Antwort, die
  `serve` höchstens einmal am Tag bei GitHub holt), je Hub-Eintrag `login` (`ok`, `invalid`, `missing`),
  Node-Name und Stand des Abgleichs (`sync`), bei `ok` Account, User und Collections; dazu
  `unknown_hubs`, die Aliase aus Headern ohne Eintrag. Nie Token, Hash, Adresse, Transport
  oder `hub_id`.
- **node whoami** — `kephalaion node whoami [<account>] [--hub <alias>] [--json]`: ohne
  Account Version, je Hub Node-Name und Stand und die Accounts, die der Node aus seinen
  Replicas kennt; mit Account die Antwort des Werkzeugs `whoami` für ihn (`login: ok`, wo er
  lebende `SYSTEM:A:`-Zeilen hat, sonst `missing`), aus derselben Funktion. Ohne Token.

## Auslieferung

- **release** — eine veröffentlichte Version auf GitHub, erzeugt aus einem Git-Tag `v*`. Die
  Version ist der Tag; eine `VERSION`-Datei gibt es nicht. Trägt die Assets.
- **asset** — eine Datei an einem Release: je Plattform ein nacktes Binary
  `kephalaion-<os>-<arch>`, dazu `SHA256SUMS` und `install.sh`.
- **SHA256SUMS** — Asset mit den SHA-256-Prüfsummen der Binaries eines Releases, nicht
  mehr. Prüft die Unversehrtheit, nicht die Herkunft.
- **latest** — das neueste veröffentlichte Release ohne Suffix; so, wie die GitHub-API es
  unter `releases/latest` nennt.
- **prerelease** (Vorabversion) — ein Release, dessen Tag ein Suffix trägt
  (`v0.2.0-rc1`). Nie `latest`; nur ausdrücklich per Version zu erreichen.
- **dev build** (Entwicklungs-Build) — ein Binary, das nicht aus einem Release stammt. Trägt
  die Version `dev`, dazu den Commit aus `git describe`.
- **version** — `kephalaion version`: zeigt Version, Commit, Go-Version und Plattform.
- **upgrade** — `kephalaion upgrade`: ersetzt das laufende Binary durch das Binary eines
  Releases, nach Prüfung gegen `SHA256SUMS`, atomar. Stuft nie von selbst zurück. Ohne
  Schreibrecht bricht es vor dem Download mit dem Weg ab; nach Erfolg startet es einen
  laufenden Dienst pro User neu. `--check` sagt, ob es eine neuere Version gibt, ob sich dieses
  Binary selbst ersetzen kann (**self upgrade**, Schreibrecht in seinem Verzeichnis) und den
  Weg (**method**: `self`, `explicit` für einen dev build, `admin` global, `manual`);
  `--check --json` dasselbe als JSON, dieselbe Struktur wie das Feld `update` in `whoami`.
- **install.sh** — Installationsskript für die Erstinstallation pro User nach
  `~/.local/bin/kephalaion`; liegt im Repo und hängt an jedem Release. Nennt am Ende die
  nächsten Schritte (Rollen, `service install`).
- **user installation** (Installation pro User) — Binary, config, Daten und Dienst gehören
  einem User: `~/.local/bin`, `~/.config/kephalaion/`, `~/.local/share/kephalaion/`, systemd
  `--user` bzw. LaunchAgent (`service install`). Linux und macOS.
- **linger** — systemd-Einstellung je User (`loginctl enable-linger`): Seine Dienste laufen auch
  ohne Anmeldung. `service install` schaltet es nicht ein; ist ein Hub eingerichtet, nennt es
  den Befehl.
- **system installation** (globale Installation) — ein Dienst für alle User eines Rechners,
  nur Linux mit systemd: `/usr/local/bin/kephalaion`, Systembenutzer `kephalaion`,
  `/etc/kephalaion/config.yaml` (Verzeichnis gehört `kephalaion`, `0755`; config `0644`),
  `/var/lib/kephalaion/` (`0700`), System-Unit aus `service unit --system`; eingerichtet per
  Ansible oder von Hand nach [`installation.md`](installation.md). Die User sind nur Clients.
  Gebaut für User auf dem Rechner selbst, über Loopback; für Devcontainer fehlt das Lauschen
  auf der Docker-Bridge. Je Rechner gibt es genau eine der beiden Arten (`konzept.md`,
  „Installation und Betrieb“).
- **service** (Dienst) — `kephalaion service install|uninstall|status`: richtet den Dienst pro
  User ein, der `serve` startet, entfernt und zeigt ihn — unter Linux die Benutzer-Unit
  `~/.config/systemd/user/kephalaion.service` (systemd `--user`), auf macOS den LaunchAgent
  `io.github.kephalaion`. `kephalaion service unit --system` gibt die System-Unit der globalen
  Installation aus, die Ansible oder der Verwalter ablegt; `service unit` ohne `--system` die
  Unit bzw. den LaunchAgent pro User. Festgelegt am 2026-09-26; `init` richtet keinen Dienst
  ein, es nennt nur den nächsten Schritt.
- **unit** (Unit) — die Datei, mit der systemd einen Dienst startet; hier `kephalaion.service`,
  pro User oder global (System-Unit). Erzeugt vom Binary, damit sie zur Version passt.
- **LaunchAgent** — das Gegenstück auf macOS: eine plist unter `~/Library/LaunchAgents/`,
  geladen von launchd für den angemeldeten User. Label `io.github.kephalaion`.
