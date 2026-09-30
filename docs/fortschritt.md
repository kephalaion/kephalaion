# Fortschritt

Stand: 2026-09-29 (Tasks 001–016 abgeschlossen; 018 Etappen 1–3 ausgeführt, Etappe 4 ist
Nacharbeit des Nutzers)

## So wird diese Datei aktualisiert

- **Wann:** nach jeder abgeschlossenen Etappe, nach jedem Task, der nach `done/` wandert, und
  wenn im Gespräch etwas entschieden oder neu als offen erkannt wird.
- **Quellen, in dieser Reihenfolge:**
  1. `k-playbook-local/tasks/*.md` — Tabelle „Fortschritt“ je Task (offene Etappen);
  2. `k-playbook-local/tasks/done/*.md` — Abschnitte „Offen (nicht gefixt)“, „Offen:“ und
     „Intent-Alignment“ der Ausführung (was nicht oder nur teilweise geprüft wurde);
  3. `git log --oneline` und `git status` — was committet ist, was gerade in Arbeit ist;
  4. `docs/konzept.md` — Abschnitte „Offene Punkte“, „Stufen“, „Werkzeuge“ und alles mit
     „offen“, „zurückgestellt“, „vorgemerkt“;
  5. `k-playbook-local/material/befunde/` — Befunde mit Status ungleich `geklaert`.
- **Wie:** nur Stichpunkte, jeder mit Verweis auf seine Quelle (Task/Etappe, Abschnitt).
  Erledigtes aus „In Arbeit“ und „Zu tun“ nach „Erledigt“ verschieben, dort knapp halten.
  Entschiedenes aus „Zu besprechen“ streichen — die Entscheidung gehört in `konzept.md`,
  nicht hierher. Datum oben anpassen.
- **Auftrag an die KI:** „Aktualisiere `docs/fortschritt.md` nach der Anleitung am Anfang der
  Datei.“

## Erledigt

- **Task 001 — Gerüst:** Modul, `version`, Makefile, CI, Release per Tag (Entwurf → Assets →
  veröffentlichen), `install.sh`, `upgrade` mit `SHA256SUMS`; v0.1.0 und v0.1.1 veröffentlicht.
- **Task 002 — Datenbank, config, status:** `hub init`, `node init`, `status`,
  `config show|export|import`; `sqlq`, `sqlitedb`, Hub-/Node-Store, Trenntest Hub ↔ Node.
- **Task 003 — Collections, Nodes, Hubs:** Schema 2, `hub_id`, `ident`, CLI
  `hub collection|node …`, `node hub|collection …`, Token nur über stdin, Export Format 2.
- **Task 004 — Dokumente und Abgleich über local** (2026-09-26, Etappen 1–6):
  - `listen` in der config, `hubs.node_name` (Node-Schema 3, Export Format 3);
  - Dokumente am Hub: `hub doc put|get|list|rm`, `hub import` (eine Revision je Import);
  - Vertrag Fassung 1 (`docs/vertrag.md`, `internal/contract`), Hub-Seite des `sync`;
  - Replica je Hub unter `replicas/`, Abgleich am Node (`internal/node/replica`), Reset bei
    `hub_id`-Wechsel bzw. `since` > Hub-Revision;
  - `node sync`, `node doc list|get`, `status` je Hub und Collection; Durchlauf über `local`
    im README.
- **Task 005 — Kommunikation** (2026-09-26, Etappen 1–6): Accounts am Hub (`hub account …`,
  `SYSTEM:A:`-Zeilen, `admin` reserviert, Export Format 4); Vertrag um `whoami`/`rotate`, Hub
  über HTTP `/v1/`; Node mit Transport `http`, `node hub check`, `--create`,
  `node account rotate|check`; `serve` nur auf Loopback mit Sperrdatei; Node als MCP-Server
  `/mcp` mit `whoami`; Durchlauf mit Testaccounts über `local` und `http`.
- **Task 007 — Review-Befunde aus Task 005** (2026-09-26): `rotate` stellt die Antwort vor dem
  Commit zusammen, unklarer Ausgang auch über `local`; Account-Zeile zuerst gesperrt,
  `principal_names` (Hub-Schema 4); HTTP-Client ohne Weiterleitungen, Hub prüft `Host`
  (`internal/loopback`).
- **Task 006 — User je Account** (2026-09-26): `hub account add|set --user` (ohne Angabe der
  Name des Accounts), `list --user`, `show`; `accounts."user"` mit Index, Hub-Schema 5; User in
  jeder `SYSTEM:A:`-Zeile, `set --user` schreibt alle lebenden Zeilen unter einer Revision;
  `rotate` schreibt `updated_by` = User; `whoami` (`/v1/`, `local`, MCP) nennt ihn nur bei
  gültiger Anmeldung; Export Format 5 mit `user` (Pflicht), Format 4 → User = Name
  (`konzept.md`, „Account und User“).
- **Task 008 — Abgleich im Hintergrund, whoami, node whoami** (2026-09-26, Etappen 1–5,
  in `done/`; Intent-Alignment „Teilweise“, siehe „Zu tun“):
  - `serve` gleicht als Node selbst ab: beim Start, dann je `sync_interval` (Standard 30 s,
    `0` aus), je Eintrag eine Goroutine, `https`/`ssh` übergangen; Log nur bei Zeilen, beim
    ersten Fehler, bei Wechsel der Art des Fehlers und bei Erholung;
  - `config set|unset <rolle> <schlüssel> [<wert>]`, erster Schlüssel `sync_interval`;
  - Stand je Hub in `hub_sync` (Node-Schema 4), geschrieben von `serve` und `node sync`,
    gezeigt von `status` und `whoami`; nicht im Export;
  - Nebenläufigkeit: `hubs.entry_id` (ULID, nie wiederkehrend) auch in `db_info` der Replica
    (Replica-Schema 3); jede schreibende Transaktion prüft Eintrag, `hub_id` und Stand, Zeilen
    und Stände nur vorwärts (Task 008, Etappe 2);
  - MCP `whoami` nach Konzept: `version`, alle Hubs mit `login`, `node`, `sync`,
    `unknown_hubs`; Anmeldung über alle Hubs als `mcpnode.Authenticate` (Grundlage für Task 009);
  - `kephalaion node whoami [<account>] [--hub] [--json]` aus derselben Funktion.
- **Task 010 — Arbeitsbranch `dev`** (2026-09-26): gearbeitet und gesichert wird auf `dev`,
  `main` rückt nur über `make -C k-playbook-local release` per Fast-Forward vor (von `dev`,
  gepusht, CI grün; alle Prüfungen vor dem ersten Push, wiederholbar); `sichern` nur auf
  `dev`; CI-Push nur für `main`/`dev`; Dependabot gegen `dev`; Ruleset „main und dev“ sperrt
  Force-Push und Löschen (`k-playbook-local/k-playbook.md`, „Branches“, „Release“).
- **Task 012 — Nachbesserung Task 008** (2026-09-26, Etappen 1–3):
  - `whoami` und `node whoami` bleiben bei einer unlesbaren Replica benutzbar: nur ihr Hub
    ist betroffen (`login` `missing`, „Replica nicht lesbar“, keine Revision); die volle
    Meldung ins Log bzw. nach stderr; `DescribeSync` ohne nil-Dereferenz (Review-Vorschlag 4);
  - der Abgleich verwirft auch eine eindeutig beschädigte Replica und legt sie neu an;
    vorübergehende Fehler verwerfen nichts;
  - `replica.Create` prüft nach `Open` die `entry_id`, sonst `ErrChanged` (Vorschlag 1);
  - `serve` wartet beim Beenden höchstens `shutdownGrace` auf den Abgleich (Vorschlag 2)
    (`konzept.md`, „`whoami`“, „Im Hintergrund“).
- **Task 009 — Lesen über MCP** (2026-09-26, Etappen 1–5):
  - MCP-Werkzeuge `list`, `read`, `changes` am Node, aus der Replica, auf `Authenticate`
    aufgesetzt; eine Meldung „nicht lesbar“, „noch nie abgeglichen“, unlesbare Replica nur für
    ihren Hub;
  - `list` mit Verzeichnissen, `sort`/`order`, `mask`, `limit` (100, höchstens 1000), Cursor;
    `read` per Name oder `id`, `content: false`, `writable`; `changes` mit Cursor je Hub
    (`generation`) und Collection, `reset`, `dropped`, `since`;
  - `generation` in `db_info` der Replica (Replica-Schema 4); Durchlauf über `serve`
    (`konzept.md`, „Allgemein — lesen“).
- **VS-Code-Erweiterung, Lesen** (2026-09-26, ohne Task, Version 0.0.4): `vscode/`, reines
  JavaScript; Statusleiste und Menü aus `whoami`, Adresse aus `listen`, Tokens aus
  `tokens/<hub>/<account>.token`, Account je Hub wählbar (`kephalaion.accounts`); Collections
  als Ordner über `list`/`read`, Änderungen über `changes`. Im echten VS Code geprüft:
  „Collection einbinden“, Dokument öffnen; Account-Wahl nur mit Ersatz für `vscode`
  (`docs/vscode.md`, „Umsetzung“; README „VS Code“). Schreiben seit 0.0.5 (Task 014).

- **Task 011 — Installation pro User und global** (2026-09-26, Etappen 1–7):
  - config-Suche `--config` > `KEPHALAION_CONFIG` > User > `/etc/kephalaion/config.yaml` > Ort
    des Users; `status` zeigt Quelle und Dienst, meldet zwei Arten (Exit 1), global ohne
    Leserecht nur Hinweis (Exit 0); `init` bricht neben der globalen config ab;
  - `service install|uninstall|status|unit [--system]` (`internal/service`): Benutzer-Unit
    bzw. LaunchAgent `io.github.kephalaion`, System-Unit gehärtet mit `StateDirectoryMode=0700`;
    Abbruch bei `serve` von Hand, ohne systemd, neben globaler config; Hinweise Linger, WSL;
  - `upgrade --check [--json]` mit Schreibrecht und Weg (`self`, `explicit`, `admin`,
    `manual`), Abbruch vor dem Download, Neustart des Dienstes; `make dev-install` ebenso;
  - `serve` fragt höchstens einmal am Tag, `whoami` zeigt `update`;
  - CI-Job auf macOS (LaunchAgent mit `plutil`, `install.sh`), derzeit abgeschaltet (siehe
    „Zu tun“); `docs/installation.md` mit Ansible-Beispiel (`konzept.md`, „Installation und
    Betrieb“);
  - Durchlauf: pro User hier echt (Abbruch bei `serve` von Hand, Dienst eingerichtet, Neustart
    über `make dev-install`, `uninstall` und wieder `install`; der Dienst läuft), global im
    Container nach der Doku, Ansible gegen einen zweiten Container (echter Download von v0.1.1
    mit Prüfsumme, danach das lokale Binary; zweiter Lauf ohne Änderung).

- **Task 013 — Tests schnell und vollständig** (2026-09-26, Etappen 1–5):
  - `TestBackgroundSync` wackelt nicht mehr: Er wartet vor `hub doc put` auf die erste Runde;
    `eventuallyLog` gibt beim Fehlschlag den Log von `serve` aus;
  - langsame Tests (> ~0,5 s oder Warten auf Runden, Timer, Fristen) beginnen mit `slow` und
    fallen unter `-short` weg; `make check-quick`, `make check` bleibt vollständig;
  - CI: Push auf `dev` quick, `main` und Pull Requests full, von Hand `suite` (Vorgabe full)
    und `kennung`, der Name des Laufs nennt beides;
  - `release` verlangt den neuesten vollständigen Lauf auf `dev` für `HEAD`, stößt ihn bei
    Bedarf an, wartet darauf und prüft danach `origin` erneut.

- **Task 014 — Schreiben über MCP** (2026-09-27, Etappen 1–6):
  - Hub-Store schreibt mit Urheber — User in `created_by`/`updated_by`, `actions` je Dokument
    mit Account und Node; die CLI bleibt `admin` ohne Träger — und Rechten: `write` für Neues
    und Eigenes, `supersede` für Fremdes; Account-Zeile zuerst gesperrt;
  - Vertrag um `create`, `write`, `delete`, `rename` (Fassung 1, `/v1/…`), Codes
    `not_readable`, `forbidden`, `not_found`, `name_taken`, `path_conflict`,
    `stale_revision`; nie wiederholt, unklarer Ausgang über HTTP und `local`; Body der
    Schreibvorgänge 7 MiB, ungültiges UTF-8 abgelehnt (`vertrag.md`, „Schreibvorgänge“);
  - Werkzeuge am Node mit Code (dazu `unreachable`, `outcome_unknown`, `unsupported`,
    `internal`); die Zeilen der Antwort sofort in der Replica (`replica.WriteRows`), Anstoß des
    Abgleichs auch bei `sync_interval` `0`; `delete` mit `recursive` und `rename` nehmen
    Verzeichnisse als Ganzes, Ziel nach der Art (`name_taken` bzw. `path_conflict`);
  - Erweiterung für VS Code 0.0.5 schreibt (Tabelle `id` → Name, Fehler nach Code), geprüft
    mit Ersatz für `vscode`; Durchlauf mit zwei Nodes als `TestMCPWriteTwoNodes`
    (`konzept.md`, „Allgemein — schreiben“; `vscode.md`, „Umsetzung: Schreiben“).
- **Task 015 — Frontmatter in list und read** (2026-09-27, Etappen 1–3): Parameter
  `frontmatter` bei `list` und `read` — je `.md`-Dokument das Frontmatter (Block am Anfang
  zwischen zwei Zeilen `---`, YAML) als JSON-Objekt `frontmatter` oder `frontmatter_error`,
  Verzeichnisse und Collections über ihre `README.md`; nur die ersten 64 KiB, nur für die
  Einträge der Seite, nicht im Cursor; neutrales Paket `internal/frontmatter` (yaml.v3 über den
  Knotenbaum, Zeitangaben bleiben Text); Durchlauf gegen den echten Node (`home:eins`,
  `test/frontmatter/`). Nur Node, kein Vertrag, kein Schema, Erweiterung unberührt
  (`konzept.md`, „Datenmodell“, „Allgemein — lesen“; Befund `frontmatter-yaml.md`).
- **Task 016 — vendor/ und Ordner abgleichen** (2026-09-28, Etappen 1–5):
  - Scope `vendor/<name>` in `rights.vendor` der Account-Zeile (Liste, ohne neue Fassung des
    Vertrags); die eine Regel in `contract.Rights.MayWrite`: unter `vendor/<name>/` allein der
    Scope, direkt in `vendor/` niemand, sonst `write`/`supersede`; Hub-Store prüft damit
    `create`, `write`, `delete`, `rename` (alter und neuer Name, je Dokument), der Node meldet
    `writable` danach; `hub account grant --vendor <name>` (wiederholbar), `show`, `list`,
    `whoami`; Exportformat 6, Format 5 und älter lesen ohne Scopes; gesperrt gemerkt
    (`vertrag.md`, „Account-Zeilen“);
  - neutrales Paket `internal/dirsync`: `Push` und `Pull` gegen die Schnittstelle `Target`
    (list, read, create, write, delete), Vergleich über den Inhalt, je Ebene löschen,
    anlegen/schreiben, absteigen; Vorabprüfung mit allen Treffern, `.git` und `--exclude`
    beidseitig unberührt, `--last`, `--dry-run`, Abbruch und Höchstzeit zwischen zwei
    Vorgängen, gemeldete Konflikte lassen den Lauf weitergehen (unvollständig), `pull`
    mit `--delete`, ohne Symlinks zu folgen, von vorn bei geändertem Stand;
  - `kephalaion node dir push|pull` als MCP-Client des Nodes (go-sdk, Header-Paar), Adresse
    aus `listen` oder `--node`, Token aus `tokens/<hub>/<account>.token`, `--token-file` oder
    `--token-stdin`; Exit 0/1/2/3; `push` nur unter `vendor/<name>/`;
  - Durchlauf im vorübergehenden Aufbau: `k-playbook/` (115 Dateien, `--exclude installer`,
    84 Symlinks übergangen, keine Treffer der Vorabprüfung) in 1,7 s nach
    `vendor/k-playbook/`, zweiter Lauf 0,5 s ohne Änderung, SIGINT nach 13 von 49 Änderungen
    und Fortsetzen, `pull` mit `diff -r` ohne Unterschied, Account nur mit `write`
    abgewiesen (Befund `vendor-scope-und-dir-push-pull.md`).

- **Task 018 — Transport `https`: Node zu einem Hub hinter einem Reverse-Proxy** (2026-09-29,
  Etappen 1–3):
  - Node-Seite: `connector`, Abgleich im Hintergrund, `kick` und `nodeHubLink` behandeln
    `https` wie `http`, nur über TLS; je Hub-Eintrag optional eine CA (`node hub add|set
    --ca-file`, gespeichert als Text in `hubs.ca`, Node-Schema 5; `show` mit Subject,
    Gültigkeit, Fingerabdruck, `list` mit Spalte CA), sonst die System-Roots;
    `httpapi.NewClient(address, rootCAs)` mit TLS ≥ 1.2 und HTTP/1.1, Zertifikatsfehler gelten
    als „nicht erreicht“ und werden nie wiederholt; `node hub check` erklärt Zertifikatsfehler,
    502/503 des Proxys und die Host-Prüfung (403); Exportformat 7 mit `ca`
    (`vertrag.md`, „HTTP“; `konzept.md`, „Kommunikation“); Nachtrag nach Entscheidung des
    Nutzers vom 2026-09-29: die Adresse darf einen Pfad als Präfix tragen
    (`https://<name>/kephhub`, Caddy `handle_path`; seit Task 019 `…/kephalaion/hub` mit
    `uri strip_prefix /kephalaion`), `check` erklärt dazu 404 und 401 des
    Proxys;
  - Hub unverändert, nur `reqlog` nennt hinter dem Proxy die Adresse des Aufrufers aus
    `X-Forwarded-For` (`via`); `serve` bleibt auf Loopback;
  - Doku: Caddyfile-Vorlage, zwei Zertifikatswege (Name mit Let's Encrypt; IP mit `tls
    internal` oder eigener CA), Freigabe der Ports, Ansible, Node-Seite, bekannte Grenze
    (Fehlversuche) und der Weg zum Neuanlegen von `node.db` (`installation.md`, „Hub für Nodes
    anderer Rechner“ und „Neue Schemafassung“; README, „Einrichten“);
  - Tests ohne Netz gegen „Proxy plus Hub“ (`internal/testcert`, `httptest` mit `StartTLS`):
    richtige, falsche und keine CA, falscher Name, abgelaufen, `serve` mit `https`-Eintrag
    (Abgleich, `create` über MCP, Anstoß, falsche CA). Abnahme auf der VM: Etappe 4, siehe
    „Zu testen“ (Befund `material/befunde/transport-entfernt.md`).

- **Task 019 — Das Binary hinter einem Präfix: Begrüßung an der Wurzel, der Hub unter
  `/hub/`** (2026-09-30, Etappen 1–3):
  - Hub-Listener von `serve` (`newHubHandler`): `GET /` eine Begrüßung mit Version
    (`text/plain`, `Cache-Control: no-store`), `/hub` und `/hub/` ein kurzer Text ohne
    Version, unter `/hub/` der Vertrag (Fassung 1 unverändert, `http.StripPrefix`), `/v1/…`
    an der Wurzel 404 `text/plain` mit dem Hinweis auf `/hub`, alles andere 404 `text/plain`;
    nie ein 3xx — `/hub` und `/v1` eigens registriert, unsaubere Pfade (`//`, `.`, `..`) vor
    dem Mux mit 404 statt der 301-Bereinigung von `ServeMux`; `loopback.Guard` davor;
  - Vertrag: der Pfad ist relativ zur Adresse des Hub-Eintrags, die eines `serve` endet auf
    `/hub`; ein 404 ohne Vertragsform gilt als „nicht erreicht“ — der Client setzt
    `sent: false`, `rotate` und die Schreibvorgänge melden den Fehler mit dem Text des Hubs
    statt „unklar“ (`vertrag.md`, „Ausgang und Wiederholung“, „HTTP“);
  - Adressen: `http://localhost:7434/hub`, hinter einem Proxy `https://<name>/kephalaion/hub`
    (der Proxy nimmt nur `/kephalaion` weg); Hilfe von `node hub add`, `hub init` und
    `status` nennen sie; `node hub check` sagt bei 404 „der Pfad ist nicht der Hub … fehlt
    /hub am Ende der Adresse, oder kennt der Proxy die Route nicht?“, bei 401 „… oder fehlt
    /hub am Ende der Adresse (dann landet die Anfrage in der Anmeldung vor dem Rest)?“;
  - Doku: `konzept.md` (config, „Kommunikation“: das Binary hinter einem Präfix, GUI später),
    `installation.md` (Caddyfile in der allgemeinen Form mit `handle /kephalaion/hub/*` und
    `uri strip_prefix /kephalaion`, die Anmeldung vor dem Rest als Option, Proben nach Ort),
    `begriffe.md` (`/hub`, `greeting`), README, Hilfetexte;
  - Tests: `TestServeHubListener` (22 Pfade und Methoden, nie `Location`),
    `TestNotFoundWithoutContract`, `TestAccountRotateOldAddress`, `TestNodeHubCheckHTTPS`
    gegen `proxyHubAt` mit `newHubHandler` (Präfix `/kephalaion`, Rest 401 bzw. 404).
    Abnahme auf der VM: Etappe 4, siehe „Zu testen“ (Befund
    `material/befunde/transport-entfernt.md`).

## In Arbeit

- **Task 018 und 019, Etappe 4 — Abnahme auf der VM:** Nacharbeit des Nutzers, Schritte in
  `~/dev/vm/kephalaion/README.md`, „Abnahme des HTTPS-Wegs“ (mit `/kephalaion/hub` und
  `uri strip_prefix /kephalaion`); siehe „Zu testen“.

## Zu tun

- **Nach Task 016:** `push` außerhalb von `vendor/` freigeben, sobald sich der Abgleich
  bewährt hat (`konzept.md`, „Einen Ordner abgleichen“); das Update von `vendor/k-playbook/`
  und das Überlagern macht k-playbook. Die Erweiterung für VS Code merkt sich `writable` je
  Collection aus dem letzten `read` (`extension.js`, `this.writable`) — unter `vendor/` kann
  die Anzeige eines Ordners deshalb vom Recht des zuletzt gelesenen Dokuments abhängen; `stat`
  je Dokument ist richtig (Befund `vendor-scope-und-dir-push-pull.md`).

- **macOS-Job in CI wieder einschalten:** seit 2026-09-26 auf Wunsch des Nutzers abgeschaltet
  (`if: false` in `.github/workflows/ci.yml`, Job `macos`); später `if: false` entfernen. Stand:
  Er lief, einzig `TestBackgroundSync` scheiterte (Läufe 36260080975, 36260519391; grün waren
  36259254294 im dritten Versuch und 36260980326) — an einer Race-Condition im Test, seit
  Task 013 behoben; der Job bleibt trotzdem vorerst aus (Task 011, Etappe 5; Befund
  `material/befunde/ci-macos.md`).

- **Update-Hinweis einstellbar oder zwischengespeichert** (vorgemerkt 2026-09-26, Todo):
  `node whoami` fragt GitHub bei jedem Aufruf, auch ohne Account und vor der Prüfung des
  Namens (ohne Netz bis zu 10 s, geteiltes Limit 60 Anfragen je Stunde und Adresse); `whoami`
  zeigt `update` immer. Nicht immer ausgeben: einstellbar machen oder die Antwort
  zwischenspeichern, auch für die Kommandozeile. Hängt mit „Tägliche Frage nach einem Update
  abschaltbar?“ unter „Zu besprechen“ zusammen (Task 011, Code-Review, Punkt 1).
- **Kleinere Punkte aus dem Review von Task 011** (`done/011-…`, „Code-Review“, Punkte 2–5):
  - `upgrade` endet mit Exit 1, wenn nach dem Ersetzen der Neustart des Dienstes scheitert;
    die Hilfe sagt zu Exit 1 „das Binary bleibt unverändert“ — eigener Exit-Code oder Hilfe
    und `docs/installation.md` ergänzen (2);
  - `serve` fragt bei jedem Start nach einem Update; „höchstens einmal am Tag“ gilt je Prozess,
    häufige Neustarts (`make dev-install`, `Restart=on-failure`) fragen jedes Mal (3);
  - `config.Location.System()` erkennt die globale config nur am bereinigten Pfad; über einen
    Symlink gilt sie nicht als global (Weg des Upgrades, Dienstzeile) (4);
  - `make dev-install` wiederholt Unit-Name und Label aus `internal/service` (5).
- **Kleinere Punkte aus dem Review von Task 013** (`done/013-…`, „Code-Review“, Punkte 1, 2, 5, 6):
  - der Ausdruck für die Suite steht in `ci.yml` zweimal (`run-name`, `env.SUITE`); laufen
    sie auseinander, zählt `release` womöglich einen schnellen Lauf als vollständig (1);
  - `release` liest die URL nur aus stdout von `gh workflow run`; steht sie woanders, greift
    der Rückfall über die Kennung, bis zu 60 s später (2);
  - `TestServe` ist nur langsam, weil das Beenden 5 s auf eine ungenutzte Verbindung wartet;
    mit `CloseIdleConnections` vor dem Beenden wieder schnell (5, Befund
    `material/befunde/test-laufzeiten.md`);
  - `make check` gibt `go test  ./...` mit doppeltem Leerzeichen aus (6).

- **VS-Code-Erweiterung in die Installation:** heute nur aus `vscode/` von Hand gebaut und
  mit `code --install-extension` installiert. Gehört ins gemeinsame Release und in die
  Installation — als Asset `.vsix` oder ins Binary eingebettet (`kephalaion vscode install`),
  dazu `upgrade`; Node.js und `vsce` in der CI (`docs/vscode.md`, „Ein Repository, ein
  Release“; Task 011).

- **Rotation des Node-Tokens** (vorgemerkt 2026-09-26): Ein Node rotiert sein Token bei einem
  Hub selbst, anders als ein Account — er hält es in `node.db` (`hubs.token`) und kann das neue
  dort ablegen, ohne fremde Konfiguration. Vorbild `rotate` der Accounts: neues Token vor dem
  Aufruf als ausstehend merken, altes zur Anmeldung, Hash des neuen, danach ersetzen.
  **Entschieden 2026-09-26: Auch beim Node ist der erste Vorgang ein `rotate`** — das Token aus
  `hub node add` taugt nur zur Einrichtung; das erste `rotate` prüft Verbindung und
  Zusammenspiel gleich bei der Einrichtung. Kommt erst, wenn `rotate` gebaut ist. Grundsatz: Nodes werden wie Accounts behandelt, außer wo es anders sinnvoll
  ist (`konzept.md`, „Offene Punkte“, Token-Rotation).
- **Danach (Konzept, „Stufen“):**
  - Stufe 1: Zerlegung in Abschnitte, FTS5-Index, MCP-Werkzeug `search`, Abschnitte in
    `read`; Ereignisstrom (SSE/Long-Polling, Todo #10); dabei die Tokenisierung für Deutsch
    entscheiden (`konzept.md`, „Indizierung“, „Deutsche Texte“);
  - Stufe 2, Rest nach Task 014: `create_numbered`;
  - Stufe 3: `append`, `replace_section`, `supersede`, `replace_directory`;
  - Transport `ssh` (Entwurf geparkt in `k-playbook-local/inbox/chat/`, derselbe Anschluss
    wie `https` im `connector`); der Hub bleibt auf Loopback, nach außen spricht der Proxy;
  - Lauschen auf der Docker-Bridge für Devcontainer — die globale Installation erreichen bis
    dahin nur User auf dem Rechner selbst (Task 011);
  - PostgreSQL-Umsetzung des Hub-Stores (DDL, `BIGINT`);
  - Migrationsrahmen, sobald Daten bleiben müssen;
  - Kommando für eine neue `hub_id` nach Wiederherstellung aus einer Sicherung;
  - Markdown-Export des Stores;
  - Begrenzung von Fehlversuchen bei der Anmeldung — dringlicher, seit der Hub hinter dem
    Proxy nach außen spricht (Task 018); Übergang fail2ban auf das Caddy-Log
    (`installation.md`, „Bekannte Grenze“).
- **Kleinere Punkte aus dem Review von Task 008** (`done/008-…`, „Code-Review“, Vorschläge 3–12):
  - `reset` prüft nur `entry_id`, nicht die alte `hub_id` — doppeltes Verwerfen bei zwei
    parallelen Resets (3);
  - neues `sync_interval` wirkt erst nach dem laufenden Timer, bei `0` bis zu 30 s
    (`syncIdle`); in der Hilfe nennen (5, Ausführung);
  - `outcome`/`skipped` in `bgsync.go` für entfernte Einträge nicht geräumt; https → http →
    https ohne zweite Logzeile (6);
  - verwaiste `.db.new-<ULID>`-Dateien nach einem Absturz; `os.Link` setzt Hardlinks voraus (7);
  - je Runde ein neuer `connector`, bei `http` womöglich offene Idle-Verbindungen (8);
  - `whoami` liest die Hubs zweimal und öffnet je Replica bis zu zweimal (9);
  - nach `Remove` in `openForSync` sehen lesende Pfade eines alten `*sql.DB` womöglich die neue
    Datei — nur schreibende Pfade geschützt; im Kommentar festhalten (10);
  - Unit-Test der Log-Zustandsmaschine mit gefälschtem Connector; die Langsamkeit der
    Log-Tests über `serve` ist mit Task 013 gelöst — sie fallen unter `-short` weg und laufen
    nur noch im vollständigen Lauf, auf `dev` bleibt die Zustandsmaschine ungeprüft (11);
  - geloggte „Revision“ ist das Minimum über die Collections; so benennen (12);
  - ungültiges `sync_interval` aus `config import` wird nicht abgewiesen, `serve` nimmt 30 s
    (Ausführung, Restrisiken);
  - `last_error` in `whoami` nur als fester Satz je Fehlerart (Adresse); bewusst, ggf.
    besprechen (Ausführung, Restrisiken).
- **Kleinere Punkte aus Reviews (Task 003):**
  - `node hub add` prüft Transportregeln erst nach der Token-Eingabe;
  - `parseFlags`: Flag-Wert `--` gilt als Ende der Optionen;
  - `status` auf eine schreibgeschützte Datenbank;
  - Index für `CollectionCountDocs`/`AccountRows` prüfen, jetzt wo es Dokumente gibt.

## Zu testen

- **Task 018 und 019, Etappe 4 — Abnahme auf der VM: abgenommen 2026-09-30.** Binary dev
  fa5d6a6 auf VM (global, `node.db` neu in Schema 5, Import) und WSL (pro User, frisch);
  Caddyfile mit `handle /kephalaion/hub/*` (ohne `forward_auth`, `uri strip_prefix
  /kephalaion`) und `handle /kephalaion/*` (mit); Node `wsl-kleist`, Account `kamran-wsl`;
  aus der WSL `check`, `sync` (Revision 3 → 4 nach `rotate`), `create` über MCP
  (`wsl-hallo.md`, Revision 6); Hub-Journal `POST /hub/v1/{whoami,sync,rotate,create} 200 …
  via=<WSL-IP> node=wsl-kleist [account=kamran-wsl]`, Caddy-Log 200 auf `/kephalaion/hub/v1/…`;
  von außen `…/kephalaion/hub/` → 200 Hub-Text, `…/hub/nix` → 404 `invalid`, `GET
  …/hub/v1/whoami` → 405 `invalid`; auf der VM `/` → Begrüßung mit Version, `POST /v1/whoami` →
  Hinweis auf `/hub`, `vmhttp` mit `/hub` erreichbar. Befunde bestätigt: „Proxy setzt Host, Hub
  unverändert“, „`strip_prefix /kephalaion` reicht `/hub/v1/…` durch“, `via` trägt die WSL-IP
  (kein LB/NAT vor `9.141.8.157`) — `material/befunde/transport-entfernt--2026-09-29.md`.
  **Bewusst nicht geprobt** (fail2ban, 10× 401 in 10 min): das 401 in Vertragsform von außen und
  `node hub check` mit alter Adresse `…/kephalaion` — beides ist im Repo getestet
  (`TestNodeHubCheckHTTPS`, `TestAccountRotateOldAddress`). Ablauf als Skripte:
  `~/dev/vm/kephalaion/deploy-vm.sh`, `bind-wsl.sh`; Hinweis des Nutzers: für eine Installation
  ohne entfernte Hubs reicht beim Schemawechsel `node init` + `node hub add … --create` statt
  Export/Import.
- **Task 008:** Abgleich im Hintergrund mit einem echten Client über längere Zeit; der
  Race-Detector lief über `cmd/kephalaion` und `internal/node/...` sauber. (Der Weg, `node.db`
  nach einem Sprung der Schemafassung neu anzulegen — Review-Punkt 9, `init` bricht bei
  eingetragener Rolle ab —, steht seit Task 018 im README, „Einrichten“, und in
  `installation.md`, „Neue Schemafassung“; geschlossen.)

- **Erster Security-PR von Dependabot** gegen `main`: lokal nach `dev` holen und prüfen, ob
  GitHub ihn nach dem Release als gemergt markiert — auch wenn Dependabot den Branch rebased
  (Task 010, Review-Punkt 5, vertagt).
- **macOS:** `install.sh` und der LaunchAgent (`plutil -lint`) liefen in CI (Task 011, Job
  derzeit abgeschaltet);
  `service install` mit `launchctl` und `upgrade` nie echt auf einem Mac getestet (Task 001,
  Intent-Alignment; Task 011).
- **Task 011, global mit systemd als PID 1:** Im Container lief `serve` von Hand; die
  System-Unit ist nur mit `systemd-analyze verify` geprüft, `enable --now`, der Handler und
  `service status`/`status` gegen eine laufende System-Unit nur mit ersetztem `systemctl`.
- **Task 011, Neustart nach echtem `upgrade`:** nur über Tests mit ersetzter Schnittstelle;
  echt geprüft ist der Neustart über `make dev-install` (kein Release mit den neuen
  Kommandos).
- **`upgrade`-Abbruch:** nur per httptest belegt, nicht durch einen echten Abbruch.
- **PostgreSQL:** Tauglichkeit der Hub-Abfragen nur per Check auf verbotene Konstrukte;
  Eindeutigkeit bei gleichzeitigen Schreibern liefert dort rohe Treiberfehler (Task 003).
- **Großer Import:** eine Revision = eine unbegrenzte Seite; Verhalten über HTTP prüfen. Ebenso
  `delete` und `rename` eines großen Verzeichnisses: eine Revision, die Antwort trägt jede Zeile
  (`vertrag.md`, „Bekannte Grenzen“; Task 014).
- **Task 014, im echten VS Code:** die Handgriffe aus `docs/vscode.md`, „Im echten VS Code noch
  zu prüfen“ — speichern, neue Datei und Ordner, Drag & Drop (auch mit Binärdatei), umbenennen,
  verschieben mit „Ersetzen“ und in eine andere Collection, löschen, „Datei ist neuer“,
  fremdes Dokument; dabei klären, wie der `FileService` den Provider ruft (Befund
  `material/befunde/vscode-schreiben.md`, offen).
- **Task 014, PostgreSQL:** `rename` eines Verzeichnisses kommt zeilenweise ohne Zwischennamen
  aus — nur mit SQLite geprüft, für PostgreSQL aus der Überlegung (Befund
  `material/befunde/rename-verzeichnisse.md`, unbestätigt).
- **Ranking mit FTS5:** Korrektur aus k-playbook Task 056 (Zeiger vor Zielen) neu nachweisen,
  sobald die Suche steht.

## Zu besprechen

- **Tägliche Frage nach einem Update abschaltbar?** (ohne Netz, Datenschutz; `konzept.md`,
  „Offene Punkte“, Node als Dienst). Richtung (2026-09-26): einstellbar oder zwischengespeichert,
  siehe „Zu tun“, Update-Hinweis.
- **Name:** TMview-Recherche (griechische nationale Marken, wegen Kefalaio). Marke erst bei Entscheidung zur Vermarktung (siehe Konzept, „Der Name“).
- **Release-Signatur** statt nur `SHA256SUMS` (cosign/minisign/Attestations) — wann?
- **Obergrenze je Schreibvorgang** oder Datenstrom für große `sync`-Seiten über HTTP.
- **Verwaltung über MCP:** eigenes Recht (`admin` je Hub?), wer am Node verwalten darf, ob
  Werkzeuge nur mit Recht erscheinen, Token-Ausgabe ohne KI-Kontext.
- **k-playbook ↔ Kephalaion:** welche k-playbook-Werkzeuge (Eingang, Warteschlange, Todos,
  Tasks, `publish`, Status) Kephalaion trägt; welche die KI nicht sehen soll; in welcher
  Collection die Tasks eines Projekts liegen; wie Werkzeuge zuschaltbar werden.
- **Token-Dateien:** Ort `~/.config/kephalaion/tokens/<hub>/<account>.token` ist bisher nur
  Konvention; ob `node account rotate|check` ihn ohne `--token-file` selbst nimmt
  (`konzept.md`, „Orte nach XDG“).
- **Token-Rotation mit Frist** für Menschen/KIs; wie ein neues Token zu k-playbook gelangt.
- **Persönliche Verzeichnisse** (`personal`, `numbered` als Eigenschaften eines Verzeichnisses)
  — vorgemerkt; ob es sie braucht, wer sie setzt, Übergabe an einen anderen User
  (`konzept.md`, „Persönliche Verzeichnisse“).
- **Collection „nur nach Bestätigung“** schreiben — ja/nein?
- **Was eine Collection im Betrieb ist** (Team, Produkt, Thema).
- **Ausgangskorb** bei nicht erreichbarem Hub — derzeit nein.
- **`append`:** gemeinsamer Vertrag mit k-playbook Task 078.
- **Semantische Suche (Stufe 5):** Einbettung der Frage lokal oder BM25 zuerst.
- **Zurückgestellt, bei Bedarf:** Schnipsel und Einordnen durch den Hub, KI im Hub (Kosten,
  Anbieterbindung, Protokoll, Warteschlange), Dopplungen, History (`document_versions`),
  stdio-Bridge, `write` als Namenspräfixe.
