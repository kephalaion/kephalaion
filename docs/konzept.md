---
title: Kephalaion — Konzept
description: Entwurf für eine geteilte Wissensdatenbank mehrerer Nutzer und Projekte — ein Binary mit zwei Rollen, dem Hub für Store, Journal und Accounts und dem Node als lokalem MCP-Server mit Replica —, mit zentralem Schreiben, Collections und Rechten. Entstanden in k-playbook.
---

# Kephalaion — Konzept

**Stand: Entwurf.** Gebaut sind das Gerüst — Build, Release, Installation und
`kephalaion upgrade`, siehe [`README.md`](../README.md) — und das Einrichten der Rollen
(`hub init`, `node init`, `status`, `config show|export|import`) samt den lokalen Tabellen —
Collections und Nodes am Hub, Hubs und gewünschte Collections am Node, alles über die
Kommandozeile. Dazu Dokumente am Hub (`hub doc`, `hub import`), der Vertrag
([`vertrag.md`](vertrag.md): `whoami`, `rotate`, `sync`) und die Replica am Node, abgeglichen
über `transport local` oder `http` auf diesem Rechner (`node sync`, `node doc`). Accounts mit
Rechten je Collection (`hub account …`), die ihr Token am Node tauschen (`node account
rotate`), und `kephalaion serve`: der Hub für Nodes, der Node als MCP-Server mit dem Werkzeug
`whoami` (Task 005), dazu der User je Account (Task 006). Seit Task 008 gleicht `serve` die
Replicas im Hintergrund selbst ab (`sync_interval`, `config set`), `whoami` zeigt Version,
alle Hubs, Anmeldung und Stand des Abgleichs, `kephalaion node whoami` dasselbe auf der
Kommandozeile. Task 009 brachte das Lesen über MCP (`list`, `read`, `changes`), Task 014 das
Schreiben: `create`, `write`, `delete` und `rename` als Vorgänge des Vertrags und Werkzeuge des
Nodes, mit Rechten je Collection, Urheber und Revision als Vorbedingung, Verzeichnisse als
Ganzes; die Erweiterung für VS Code speichert damit (Stufe 2 bis auf `create_numbered`). Seit
Task 015 liefern `list` und `read` mit `frontmatter` das Frontmatter der `.md`-Dokumente als
JSON-Objekt, Verzeichnisse und Collections das ihrer `README.md`. Task 016 brachte den Scope
`vendor/<name>` — unter `vendor/<name>/` zählt allein er — und `kephalaion node dir push|pull`,
den Abgleich eines lokalen Ordners mit einem Verzeichnis einer Collection als Client des
Nodes über MCP (`push` vorerst nur unter `vendor/`). Seit
Task 011 gibt es beide Arten der Installation ([`installation.md`](installation.md)): pro User mit
Dienst (`kephalaion service install`, systemd `--user` bzw. LaunchAgent) und global für alle
User eines Linux-Rechners (System-Unit aus `service unit --system`, von Hand oder per
Ansible) — global bisher nur für User auf dem Rechner selbst, ohne Devcontainer. Die config
wird ohne Angabe gefunden, `upgrade --check [--json]` und `whoami` sagen, ob es eine neue
Version gibt und wie das Upgrade geht; ein CI-Job für macOS ist gebaut, derzeit aber
abgeschaltet. Noch nicht gebaut: `https`
und `ssh`, Suche, die übrigen Werkzeuge zum Schreiben (`create_numbered`, Stufe 3) und das
Lauschen auf der Docker-Bridge. Die Überlegungen
entstanden in k-playbook und sind am 2026-09-25 hierher umgezogen.
Begriffe nach [`begriffe.md`](begriffe.md): Sie sind englisch, die Dokumentation ist deutsch.

Ausgangspunkt ist die lokale Wissensablage von k-playbook (`k-playbook-local/knowledge/`,
beschrieben in `k-playbook/docs/knowledge-gate.md` und `knowledge-layout.md`). Sie bleibt
vorerst, wie sie ist; k-playbook gibt seine Suche schrittweise an Kephalaion ab.

## Der Name

**Kephalaion** (κεφάλαιον), von κεφαλή, Kopf: die Hauptsache, der Kernpunkt, die Summe einer
Sache. Im Geldwesen der Grundbetrag im Gegensatz zu den Zinsen — daraus wurde über das
lateinische *capitale* unser „Kapital“; im heutigen Griechisch heißt κεφάλαιο Kapitel und
Kapital zugleich.

Ausschlaggebend war die Gattung: **Kephalaia** sind gesammelte, durchnummerierte kurze
Lehrsätze, jeder für sich lesbar — die manichäischen „Kephalaia des Lehrers“, die „Kephalaia
Gnostika“ des Euagrios. Genau das ist dieser Store: verdichtete Stücke, einzeln
auffindbar.

Recherche am 2026-09-24: kein Softwareprojekt und keine Marke dieses Namens gefunden, auch
nicht in der Nische der KI-Gedächtnisse — anders als bei Mneme, Episteme, Pinakes, Scrinium,
Hypomnema, Anamnesis und Armarium, die dort alle mehrfach belegt sind. Belegt ist dagegen die
neugriechische Form **Kefalaio**: ein verbreitetes griechisches ERP und eine Zeitung. Andere
Schreibweise, anderer Markt, aber wer Griechisch spricht, hört zuerst „Kapital“.

**Benennung:** Produkt, Paket und Binary heißen `kephalaion`. Der Dienst ist
`kephalaion serve`; welche Rollen er trägt — hub, node oder beide —, steht in der
Konfiguration. Im Gespräch: Keph. Im Code und in der Konfiguration steht der volle Name —
er wird gelesen, nicht getippt.

**Repository:** `kephalaion/kephalaion` auf GitHub, öffentlich, in einer eigenen
Organisation; Modulpfad `github.com/kephalaion/kephalaion` — entschieden am 2026-09-26
(vorher `kascada/kephalaion`).

**Markenrecherche am 2026-09-26** (DPMAregister, nationale, Unions- und internationale
Marken): keine Marke Kephalaion, auch nicht als Kefalaion, Cephalaion oder griechisch; auch
Kefalaio ist dort nicht eingetragen. Am nächsten liegt die Unionsmarke **KEPHALIOS**
(017564981, Kephalios SAS, Paris) für Medizinprodukte (Klasse 10) und deren Forschung
(Klasse 42) — kein Software- oder IT-Bereich. Die übrigen Kephal-Marken sind Arzneimittel.
Offen: TMview, weil nur dort griechische nationale Marken stehen.

**Untertitel** (englisch, auch vor der Umstellung der Doku), entschieden am 2026-09-26:

- **„Knowledge, distilled.“** — der Claim, er erklärt den Namen. Steht am Logo.
- **„Shared memory for humans and agents“** — die Beschreibung: was es konkret ist. Steht, wo
  Platz ist — README, GitHub-Beschreibung, Website, Banner.

Im kleinen Logo (Favicon, Avatar) steht keiner, im großen der Claim, in Headern und Bannern
beide untereinander, der Claim oben.

**Verworfene Alternative: K-Wissen.** Passt zur `k-`-Familie, taugt aber nicht als Marke —
das DPMA hat „KI Wissen“ 2023 mangels Unterscheidungskraft und als beschreibende Angabe
zurückgewiesen (3020232353446), und der Name geht in der Suche unter.

**Marke und Domains:** Kephalaion ist vorerst für den eigenen Gebrauch. Eine Anmeldung bei
DPMA oder EUIPO folgt erst, wenn es vermarktet werden soll — unabhängig von v1.0. Domain
ist `kephalaion.com` (2026-09-26); sie leitet vorerst mit 302 auf das GitHub-Repo weiter, eine
eigene Seite folgt mit Logo und englischer Doku. Als Veröffentlichung gilt erst das erste Release ab v1.0; ein
v0.x-Release gilt nicht als solche (entschieden am 2026-09-25).

## Wozu

Wissen, das nur in einem Projektverzeichnis liegt, stirbt mit dem Projekt und hilft keinem
zweiten Menschen. Gebraucht wird ein Store, den mehrere Nutzer und mehrere Projekte
gemeinsam benutzen — mit Rechten, ohne Clone, ohne Merge-Konflikte.

Was dabei nicht verhandelbar ist: **Fragen muss schneller sein als Suchen.** Das ist der
einzige Grund, warum ein Assistent die Ablage überhaupt benutzt, statt den Code zu greppen.
Gemessen sind heute rund 20 ms bei 506 Abschnitten; ein Netzweg kostet das Fünf- bis
Fünfundzwanzigfache.

## Die tragende Entscheidung: einer schreibt, viele lesen lokal

| Vorgang | Wo | Geschwindigkeit |
|---|---|---|
| Suchen, Lesen, Auflisten | Replica auf dem Node | wie heute, ohne Netz |
| Schreiben, Ablösen, Anhängen | Hub | darf langsam sein |
| Abgleich der Replica | Hub → Node | im Hintergrund |

Daraus folgt alles Weitere:

- **Es gibt genau einen Schreiber**, den Hub. Gleichzeitiges Schreiben mehrerer Accounts gibt
  es damit nicht, und ein Zusammenführen von Konflikten braucht es nicht.
- **Die Replica ist abgeleitet.** Sie wird nie selbst beschrieben. Ein Aufruf geht zum Hub,
  dessen Antwort sagt, was gespeichert wurde und wo, und der Node schreibt genau das. Zwei
  Wahrheiten wären der Anfang vom Auseinanderlaufen.
- **Offline wird gelesen, nicht geschrieben.** Ist der Hub nicht erreichbar, scheitert ein
  Schreibvorgang mit einer klaren Meldung. Lesen läuft weiter.

## Ein Programm, zwei Rollen

**Entschieden am 2026-09-25:** Kephalaion ist in Go geschrieben und ein einziges Binary.
Ein Dienst `kephalaion serve` trägt die Rollen, die in der Konfiguration stehen; der frühere
Gedanke, Hub und Node als zwei Programme in womöglich zwei Sprachen zu bauen, ist verworfen —
die Zerlegung in Abschnitte und der Vertrag gäbe es sonst zweimal.

```text
Client (lokal)    ──MCP über HTTP──────► node ──HTTPS / SSH / local──► hub
Client (entfernt) ──MCP über HTTPS─────► node
k-playbook        ──MCP über HTTP──────► node
```

| | node | hub |
|---|---|---|
| Gestartet durch | `kephalaion serve`, Abschnitt `node:` | `kephalaion serve`, Abschnitt `hub:` |
| Läuft | einmal je Rechner, als Dienst | einmal je Installation, als Dienst |
| Datenbank | eigene: die Replica, eine je Hub | eigene: der Store |
| Spricht mit | Clients (MCP über HTTP), Hubs | Nodes |
| Besitzt | Replica, Index, Suche, Revisionen | Store, Journal, ids, Accounts, Protokoll |
| Schreibt | nur die Replica, und nur was der Hub geantwortet hat | den Store |

**Der Hub ist kein MCP-Server.** Er spricht mit Nodes, nicht mit Clients, und er sucht
nicht.

**Hub und Node im selben Prozess — entschieden am 2026-09-25.** Das wird der häufige Fall
sein. Stehen in der Konfiguration beide Abschnitte, trägt ein Prozess beide Rollen.

**Konfiguration — entschieden am 2026-09-25: eine kleine Datei für „was und wo“, alles
andere in der Datenbank.** Die Datei sagt, welche Rollen auf diesem Rechner eingerichtet sind,
wo ihre Datenbank liegt und wo ihr Dienst lauscht — mehr nicht:

```yaml
# ~/.config/kephalaion/config.yaml   (abweichend: --config, KEPHALAION_CONFIG, XDG_CONFIG_HOME;
#                                     global /etc/kephalaion/config.yaml, siehe „Installation und Betrieb“)
hub:                          # nur auf dem Rechner des Hubs
  db: sqlite:///home/kleist/.local/share/kephalaion/hub.db
  # später: postgres://keph@db.intern/kephalaion
  listen: 127.0.0.1:7434      # für Nodes anderer Rechner später 0.0.0.0:7434 (mit https)
node:
  db: sqlite:///home/kleist/.local/share/kephalaion/node.db
  listen: 127.0.0.1:7433      # MCP für Clients
```

- **Fehlt ein Abschnitt, fehlt die Rolle.** `status` und `serve` lesen das direkt ab.
- **`listen` — entschieden am 2026-09-25, zuvor in `settings` vorgesehen:** wo `serve` für
  diese Rolle lauscht. Es gehört zum „wo“ wie der Ort der Datenbank, und `serve` braucht es
  beim Start. Standard Node `127.0.0.1:7433`, Hub `127.0.0.1:7434`; `init` schreibt den Wert
  sichtbar in die Datei, `--listen` weicht ab. Nach außen lauscht nur, wer es ausdrücklich
  einträgt. **Bis `https` und `ssh` gebaut sind, lauscht `serve` für beide Rollen nur auf
  Loopback** (`127.0.0.1`, `::1`, `localhost`) und bricht sonst beim Start ab — Klartext-HTTP
  verlässt den Rechner nicht (Task 005). Ein Node-Eintrag mit `http` für den Test auf einem
  Rechner nennt die Adresse des Hubs: `http://localhost:7434`.
- **Alles andere steht in der Datenbank der Rolle** und wird nur über die CLI geändert:
  die Hubs eines Nodes mit Transport und Token, die Collections, die ein Node haben will, am
  Hub Collections, Nodes und Accounts. Eine Quelle, eine Prüfung.
- **Die Datei enthält kein Geheimnis** und wird einfach mitgesichert. Die Einstellungen in
  der Datenbank sichert `kephalaion config export` getrennt von den Inhalten. Ein Passwort für
  PostgreSQL steht nicht in der Datei, sondern kommt aus `~/.pgpass` oder der Umgebung.
- **Eingerichtet wird je Rolle mit einem Aufruf:** `kephalaion hub init [--db …]` und
  `kephalaion node init [--db …]`. Ohne Angabe gilt der Standard unter
  `~/.local/share/kephalaion/` (bzw. `$XDG_DATA_HOME/kephalaion/`). `init` legt Datenbank und Schema an und trägt den Abschnitt
  in die Datei ein, die es bei Bedarf anlegt. Keine Rückfragen, nur Parameter — das läuft
  auch in Skripten und Devcontainern. Gibt es die Rolle schon, bricht `init` ab, statt zu
  überschreiben, ebenso, wenn die Datenbankdatei schon existiert. Erst entsteht die
  Datenbank, dann der Eintrag in der Datei; scheitert der, wird die Datenbank wieder
  entfernt. Nur `init` legt eine Datenbank an, alle anderen Kommandos öffnen nur vorhandene.
  **Einzige Ausnahme, umgesetzt am 2026-09-26:** die Replica. Sie ist abgeleitet, und der
  erste Abgleich eines Hub-Eintrags legt sie an.
  Bei PostgreSQL legt `init` nur das Schema an; Datenbank und Benutzer richtet der Betrieb
  ein.
- **Die Replicas eines Nodes** liegen als eine Datenbank je Hub in `replicas/` neben `node.db`
  (siehe „Speicherung“); `node.db`
  selbst hält die Einstellungen des Nodes und in einer eigenen Tabelle `hubs` seine Hubs:
  Name (den der Node als Alias vergibt), den Namen, unter dem der Hub den Node kennt
  (`node_name`), Transport, Adresse, Token, SSH-Schlüssel, `hub_id` (eine Kopie, maßgeblich
  ist die in der Replica). `node hub rm` löscht die Replica mit, `config import` die Replicas
  der Aliase, die im Export fehlen.

- **Zwei Umsetzungen des Vertrags:** über HTTP — direkt per TLS oder durch SSH getunnelt, das
  ist derselbe Client mit anderem Verbindungsaufbau — und lokal als Funktionsaufruf. Der Node
  kennt nur die Schnittstelle, nicht die Umsetzung. `transport: local` ist ein Eintrag in der
  Liste der Hubs wie jeder andere; daneben kann derselbe Node entfernte Hubs bedienen.
- **Der lokale Weg prüft genauso.** Auch beim Funktionsaufruf trägt der Node sein eigenes
  Token und das des Accounts, und der Hub prüft beide. Sonst gäbe es einen Weg ohne Prüfung,
  den kein Test der HTTP-Seite findet. Die Tests des Vertrags laufen gegen beide Umsetzungen.
- **Getestet wird auf einem Rechner — entschieden am 2026-09-25.** Hub und Node im selben
  Prozess; trägt der Hub-Eintrag des Nodes `http` mit `localhost` und dem Port des Hubs
  statt `local`, verhält er sich wie eine getrennte Installation. Mehrere Installationen auf
  einem Rechner braucht es dafür nicht; getrennte Rechner kommen, wenn es so weit ist.
- **Getrennte Datenbanken.** Der Node behält seine eigene Replica, auch neben dem Hub, statt
  aus dessen Datenbank zu lesen: ein einziger Lesepfad im Node, kein Suchindex im Hub, keine
  Leser in der Datei des einzigen Schreibers. Die doppelten Daten sind wenige Megabyte. Der
  Abgleich ist lokal sofort da; statt eines Ereignisstroms genügt ein Signal im Prozess.
- **Der Hub bleibt extern erreichbar**, wenn eingestellt — für Nodes anderer Rechner —, und
  wird zugleich lokal direkt aufgerufen. Extern erst mit `https` oder `ssh`; bis dahin nur
  Loopback.
- **Die Kopplung ist gewollt.** Ein Absturz oder Update betrifft beide Rollen; im Code bleiben
  sie getrennt: Der Hub kennt den Node nicht, der Node kennt den Hub nur über die
  Schnittstelle.

**Die Suche liegt vollständig bei Kephalaion.** Während des Übergangs gibt k-playbook seine
Suche schrittweise ab; der Endzustand ist ein einziger Ort, an dem zerlegt, indiziert und
gesucht wird.

**Der Vertrag zwischen Node und Hub bleibt das wichtigste Stück Arbeit**, auch in einem
Binary. Er ist eine harte Schnittstelle mit zwei Umsetzungen — entfernt und im Prozess — und
wird zuerst geschrieben, nicht nebenbei: Vorgänge, Felder, Fehlercodes, die Bedeutung der
Revision, die ids, das Ersetzen eines ganzen Verzeichnisses. Er trägt eine Fassung, und der
Hub bedient auch ältere Nodes — sonst zwingt jedes Update des Hubs alle Rechner zum
Mitziehen.

## Kommunikation

**Entschieden am 2026-09-25: Der Node ist ein MCP-Server über HTTP (Streamable HTTP), lokal
wie entfernt, mit einem einzigen Eingang.** Er läuft einmal je Rechner als Dienst, pro User
oder global für alle User (siehe „Installation und Betrieb“); jede Anfrage bedient eine
eigene Goroutine. HTTP auf `127.0.0.1` braucht
keinen Webserver auf dem Rechner. Zusätzlich prüft k-playbook beim Briefing, ob der Node
läuft, und startet ihn sonst. Nur der Node hält den Index warm, später ein
Einbettungsmodell, und nur er führt die Abgleichschleife und die Verbindungen zu den Hubs.

**Eine Bridge für stdio ist zurückgestellt.** Bei stdio startet der Client den MCP-Server als
eigenen Kindprozess und kann sich nicht an einen laufenden hängen; jede Sitzung bekäme ihren
eigenen Prozess. Eine Bridge wäre der Prozess, den der Client startet, und reichte stdio an den
Node weiter. Sie wird erst gebaut, wenn ein Client zwingend stdio braucht; am Node ändert sie
nichts.

**Zustandslos.** Jede Anfrage trägt Account-Name und Token als HTTP-Header, je Hub ein Paar:
`X-Keph-Account-<alias>` und `X-Keph-Token-<alias>`, der Alias ist der des Hub-Eintrags am
Node. Header-Namen zählen ohne Groß- und Kleinschreibung; der Alias ist der Rest des Namens
nach dem Präfix, klein geschrieben — eindeutig, weil Aliase klein sind. Der Client trägt die
Header aus seiner MCP-Konfiguration ein, die KI sieht sie nicht. Eine Sitzung
(`Mcp-Session-Id`) wird nicht geführt (go-sdk, zustandsloser Modus); `initialize` geht ohne
Anmeldung. Header zu einem Alias, den der Node nicht kennt, melden an keinem Hub an; `whoami`
nennt den Alias in einem eigenen Feld (`unknown_hubs`), nur den Alias — eine Hilfe bei falsch
eingerichteten Clients, die nichts verrät, was der Client nicht schon weiß. Geprüft wird über
alle Hubs in einem Schritt je Anfrage, auf dem jedes Werkzeug aufsetzt.
Die Prüfung je Anfrage kostet Mikrosekunden: ein Nachschlagen der
Account-Zeilen in der Replica über einen Index, ein SHA-256 über das Token und ein Vergleich in
konstanter Zeit. Einen Cache im Speicher gibt es bewusst nicht — er müsste nach jedem
Abgleich, `rotate` und jeder Sperre nachgezogen werden, und eine vergessene Stelle ließe ein
gesperrtes Token still weiter gelten. Die Datenbank ist die einzige Wahrheit. Was sonst über
Aufrufe hinweg reichen müsste, etwa das Weiterblättern in Treffern, steht als Cursor in der
Antwort.

**Lokales HTTP absichern.** Ein lokaler Node bedient nicht nach außen. Er lauscht auf
`127.0.0.1` und prüft die Header `Host` und `Origin`, wie die MCP-Spezifikation es für lokale
Server verlangt — sonst könnte eine Webseite im Browser über DNS-Rebinding Anfragen an den
Node schicken. Umgesetzt: `Host` muss `localhost`, `127.0.0.1` oder `[::1]` mit dem eigenen
Port sein, `Origin` fehlt oder ist `http://localhost…`, `http://127.0.0.1…` (oder
`http://[::1]…`); sonst 403.

**Devcontainer nutzen den Node des Hosts.** Entschieden am 2026-09-25: kein eigener Node je
Container. Weil `127.0.0.1` im Container der Container selbst ist, lauscht der Node zusätzlich
auf der Schnittstelle, über die Container den Host erreichen (Docker-Bridge,
`host.docker.internal`) — und nur dort, nicht im übrigen Netz.

**Anfragen werden beantwortet, während der Abgleich läuft.** Die Suche liest den zuletzt
bestätigten Stand der Replica (SQLite im WAL-Modus trennt Leser und Schreiber). Ausnahme ist
der eigene Schreibvorgang: Dessen Ergebnis steht in der Replica, bevor die Antwort an den
Client geht.

**Entfernt: MCP über HTTPS** mit Token, später OAuth, für Clients ohne eigenen Node. Es ist
derselbe Eingang.

**Node ↔ Hub: ein Protokoll, zwei Transportwege** — dazu der Funktionsaufruf im selben
Prozess (`local`, siehe oben). Das Protokoll ist HTTP mit JSON und der Fassung im Pfad, kein
MCP; es ist zustandslos, die Revision trägt der Node. Gebaut ist es als `POST /v1/whoami`,
`/v1/rotate`, `/v1/sync` und — seit Task 014 — `/v1/create`, `/v1/write`, `/v1/delete`,
`/v1/rename` (Einzelheiten in [`vertrag.md`](vertrag.md), „HTTP“), der Node meldet
sich mit `X-Keph-Node` und `Authorization: Bearer <token>` an. Benutzbar ist es bisher nur als
Transport `http` ohne TLS auf Loopback, zum Testen des HTTP-Wegs auf einem Rechner.

- **Direkt über TLS.** Neue Revisionen meldet der Hub über einen Ereignisstrom (Server-Sent
  Events) oder Long-Polling; das Delta holt der Node danach selbst.
- **Über SSH.** Der Node hält eine stehende SSH-Verbindung (Go-Bibliothek, kein externes
  `ssh`), mit Keepalive und Neuaufbau, und tunnelt dasselbe HTTP zum Hub, der dann nur auf
  `localhost` lauscht.
- **Die Identität ist immer das Token.** Der Transport verschlüsselt und bringt durch die
  Firewall, mehr nicht. Ein SSH-Schlüssel ist kein zweites Rechtemodell.

**k-playbook ↔ Kephalaion.** Zusammenlegen ist nicht sinnvoll: k-playbook wird je Projekt
installiert, Kephalaion je Rechner, und Kephalaion ist auch ohne k-playbook nützlich.
k-playbook steuert selbst Abläufe, startet Sitzungen und Werkzeuge und greift deshalb selbst
auf Kephalaion zu, **ebenfalls über MCP**, ist also selbst ein Client. Die KI-Sitzungen,
auch die von k-playbook gestarteten, sprechen für die Suche direkt mit Kephalaion. Welche MCP-Server eine KI hat und mit welchen
Zugangsdaten, sagt ihr das Briefing von k-playbook; das ist Sache der Gegenseite.

## Collections, Accounts, Rechte

**Collection** ist die Einheit, über die Rechte vergeben werden, und zugleich die Einheit, die
abgeglichen wird. Collections eines Hubs sind unabhängig und überschneiden sich nicht. Eine
Collection hat einen Namen. Was eine Collection ist — Team, Produkt, Thema — entscheidet der
Betrieb, nicht das Werkzeug.

**Wer in einer Collection eingetragen ist, liest alles darin — ohne Einschränkung.** Was ein
anderer nicht lesen soll, kommt entweder nicht in den Store oder in eine eigene Collection.
„Privat“ ist kein Sonderfall, sondern eine Collection, in der nur die Accounts eines Users
eingetragen sind. Eine Ansicht, die Fremdes ausblendet, gibt es als Vormerkung
(„Persönliche Verzeichnisse“ unten) — sie ist Bequemlichkeit, keine Grenze.

**Account und User — entschieden am 2026-09-26.**

**Account** ist ein Zugang: eine Zugriffsart auf einem Rechner — die KI-Sitzung am Desktop,
eine Automatisierung auf der VM, ein Prozess, der nur lesen soll. Ein Name, ein Token und die
Rechte je Collection. Mehrere Accounts auf einem Rechner haben verschiedene Namen; wo das Token
liegt, liegt auch der Name des Accounts. Das Token liegt nie im Repository, sondern beim Nutzer
auf dem Rechner.

**Ein Account liegt auf genau einem Rechner.** Das ist eine Regel, die niemand prüft. Wer sich
nicht daran hält, sperrt sich selbst aus: Das erste `rotate` auf einem Rechner macht das Token
auf dem anderen ungültig.

**User** ist, wem ein Account gehört. Er ist kein eigenes Objekt, sondern ein Merkmal am
Account, wie ein Tag: kein Token, keine Rechte, keine Anmeldung. Die Zuordnung Account → User
setzt allein der Admin. Wer drei Rechner hat und auf jedem eine KI-Sitzung und eine
Automatisierung, hat sechs Accounts und einen User.

- **Rechte hängen am Account, nicht am User.** Das ist gewollt: Ein User kann einen Account
  haben, der nur liest, und einen, der verwaltet.
- **Ohne Angabe ist der User der Name des Accounts** — für Accounts, die niemandem gehören,
  etwa eine Automatisierung. Ein Account eines Menschen nennt seinen User beim Anlegen.
- **Warum nicht mehrere Tokens je Account:** Dann stünde am Hub je Account eine Liste, und
  `rotate` müsste sagen, welches Token es ersetzt. Ein Token je Account hält Anmeldung,
  `rotate`, Sperren und Rückruf so, wie sie sind; jedes betrifft genau einen Rechner und einen
  Zweck. Synchronisiert werden muss dabei nichts.
- **Was nach der Anmeldung feststeht:** Hub und Node kennen zum Account seinen User und seine
  Rechte je Collection.

**Rechte** je Account und Collection — entschieden am 2026-09-25:

| Recht | Bedeutung |
|---|---|
| `read` | Suchen, Lesen. Hat jeder Account, der in der Collection eingetragen ist. |
| `write` | Neues anlegen; **Eigenes** ändern und löschen (`created_by` ist der eigene User — auch was ein anderer Account desselben Users angelegt hat). Einen gelöschten Namen neu anlegen darf jeder mit `write`. |
| `supersede` | **Fremdes** ändern, ablösen und löschen. |
| `vendor/<name>` | Unter `vendor/<name>/` schreiben — allein dieser Scope zählt dort, ohne `write` und unabhängig vom Urheber; siehe „vendor/“ unten (entschieden am 2026-09-27/28, gebaut in Task 016). |
| `replicate` | Kein Recht eines Accounts, sondern eines Nodes: Inhalt und Account-Zeilen der Collection abgleichen. Steht am Hub in `node_collections` (siehe „Datenmodell“). |

Später, falls gebraucht: `write` als Liste von Namenspräfixen statt `true` (etwa `["eins/",
"zwei/"]`) — Präfixe, keine Regex; bei Rechten ist „passt versehentlich mehr“ die gefährliche
Richtung. Faustregel: Unterscheidet sich, wer *lesen* darf, gehört es in eine eigene
Collection; unterscheidet sich nur, wer *schreiben* darf, genügt ein Präfix. Schnipsel bekommen,
wenn sie gebaut werden, ein eigenes Recht (`submit`).

**Lesen ist grob, Schreiben feiner.** Ein Store, der beim Lesen filtert, zwingt jede Suche zu
einer Rechteprüfung je Treffer und macht den Abgleich je Account verschieden. Wer eine
Collection nicht sehen soll, bekommt sie nicht — das ist die Grenze, und sie verläuft an der
Collection.

Fremdes zu ändern, abzulösen oder zu löschen ist ausdrücklich ein eigenes Recht: Es nimmt
etwas aus der Suche, und wer schreiben darf, darf deshalb nicht automatisch löschen, was ein
anderer beigetragen hat.

**Urheber.** Jedes Dokument trägt, wer es angelegt und zuletzt geändert hat: den **User** des
Accounts (`created_by`, `updated_by`). Welcher Account es war und über welchen Node, steht im
Protokoll (`actions`: `account`, `carrier`); das genügt. Die Herkunft (`origin`) bleibt davon
unberührt — sie sagt, woher der Inhalt stammt, der Urheber sagt, wer ihn abgelegt hat.
Umgesetzt in Task 014: Ein Schreibvorgang über einen Node schreibt je Dokument eine Zeile in
`actions` mit Account, Node als `carrier` und `action` (`create`, `update`, `delete`,
`rename`); die CLI am Hub schreibt `admin` ohne Träger.

### vendor/ — Vorlagen mit eigenem Scope (entschieden am 2026-09-27, überarbeitet am 2026-09-28)

Gebaut in Task 016 (2026-09-28): Die eine Regel, welches Recht ein Name braucht, steht in
`contract.Rights.MayWrite`; der Hub-Store prüft damit jeden Schreibvorgang (bei `rename` den
alten und den neuen Namen), der Node meldet `writable` danach. Der Scope kommt per
`hub account grant --vendor <name>` (wiederholbar) in `rights.vendor` der Account-Zeile,
Exportformat 6 trägt ihn.

**Der Fall:** k-playbook liefert Regeln, Reviews, Commands und Ähnliches mit, die ein Projekt
mit einer gleichnamigen eigenen Datei überschreibt. Die mitgelieferten ändert niemand von
Hand; ein Update ersetzt sie. Liegen sie im Store, sollen sie dort ebenso fest sein — sonst
verschwände eine Änderung beim nächsten Update still, ohne dass es jemand merkt.

- **Lesen** darf unter `vendor/` jeder, der die Collection lesen darf.
- **Schreiben unter `vendor/<name>/`** — `create`, `write`, `delete`, `rename`, auch hinein und
  heraus — **nur mit dem Scope `vendor/<name>`** des Accounts in dieser Collection (geschrieben
  `<collection>:vendor/<name>`; `<name>` ist ein Segment, etwa `k-playbook`). **Dort zählt nur
  der Scope:** `write` ist weder nötig noch genügt es, und wer ein Dokument angelegt hat, spielt
  keine Rolle (kein `supersede`) — so scheitert ein Update nicht an Vorlagen, die ein anderer
  Account oder admin eingespielt hat.
- **Ein Token nur für die Vorlagen:** Weil der Scope nicht an `write` hängt, bekommt der Account
  von k-playbook nur `vendor/k-playbook`. Er pflegt seine Vorlagen und kann sonst nichts
  schreiben (entschieden am 2026-09-28).
- **Direkt in `vendor/`**, ohne Unterverzeichnis, schreibt über einen Node niemand.
- **`rename`** wird mit dem alten und dem neuen Namen geprüft, bei einem Verzeichnis für jedes
  Dokument darunter — hinein, heraus und innerhalb von `vendor/` ohne Sonderfall.
- **Am Node** folgt `writable` derselben Regel; VS Code zeigt `vendor/` ohne Scope
  schreibgeschützt. Die CLI am Hub (`hub doc`, `hub import`) darf wie überall.
- **Verworfen am 2026-09-28:** unter `vendor/` nur ein Ersetzen am Stück (`replace_directory`)
  zu erlauben. Das Update besteht aus gewöhnlichen Einzelvorgängen (siehe „Einen Ordner
  abgleichen“ unten); der Hub könnte ein „Ersetzen am Stück“ von ihnen nicht unterscheiden.
- **Das Update macht k-playbook**, nicht der Hub: in der Collection seines Projekts — jedes
  Projekt hat seine eigene —, mit seinem eigenen Account, wenn es bei seiner gelegentlichen
  Prüfung eine neue Version findet, über `kephalaion node dir push`. Ein Hub, der selbst
  aktualisiert, müsste je Collection wissen, was und wie; das ist zu speziell für ihn. Ein
  Zeitplan am Hub kann später für anderes kommen. Eine Datei `VERSION`, als letzte
  geschrieben, zeigt, ob ein Update vollständig durchlief.
- **Das Überlagern ist nicht Sache von Kephalaion.** k-playbook stellt die Regeln der KI über
  ein eigenes Werkzeug (Briefing) bereit: Es holt die mitgelieferten und die des Projekts —
  aus Kephalaion oder anderswoher —, baut sie mit Frontmatter in seinem Cache zusammen und
  liefert das Ergebnis. Kephalaion liefert nur die Daten.
- **Faustregel erfüllt:** Wer lesen darf, ist derselbe; nur wer schreiben darf, unterscheidet
  sich — deshalb ein Präfix, keine eigene Collection (siehe „Rechte“ oben).

### Einen Ordner abgleichen: push und pull (entschieden am 2026-09-28)

Gebaut in Task 016 (2026-09-28) als neutrales Paket `internal/dirsync` gegen eine kleine
Schnittstelle (list, read, create, write, delete) und als `kephalaion node dir push|pull`;
gemessen im Durchlauf: 115 Dokumente in 1,7 s angelegt (rund 15 ms je Vorgang), ein Lauf ohne
Änderung 0,5 s, ein Abbruch per SIGINT setzt beim nächsten Lauf fort (Befund
`vendor-scope-und-dir-push-pull.md`). Ein ganzer Ordner wird geschrieben, indem der alte
Inhalt durch den eines lokalen Ordners ersetzt wird — und ebenso zurückgelesen.

- **Die Dateien liefert, wer sie sieht.** Den Namen eines lokalen Ordners an den Node zu geben,
  damit er ihn selbst liest oder schreibt, geht nicht: Global läuft der Node als `kephalaion`
  mit `ProtectHome`, im Devcontainer ist der Pfad ein anderer, und ein Dienst, der im Namen
  eines Clients Dateien liest oder schreibt, wäre ein Weg an den Rechten vorbei. Deshalb macht es
  die **Kommandozeile**, dort aufgerufen, wo der Ordner liegt: `kephalaion node dir push` und
  `node dir pull`. Sie ist dabei ein Client des Nodes über MCP wie die Erweiterung für
  VS Code; den Account und sein Token bringt der Aufrufer mit (Token-Datei oder
  `--token-stdin`, nie als Argument).
- **Einzelvorgänge statt eines neuen Vorgangs im Vertrag.** `push` geht Quelle und Ziel rekursiv
  durch und benutzt die vorhandenen Werkzeuge `list`, `read`, `create`, `write` und `delete`.
  Das ist nicht atomar, braucht aber keine Größengrenze, kein neues Protokoll und lässt sich
  jederzeit abbrechen und **wiederholen**: Ein zweiter Lauf ändert nur, was noch abweicht. Dass
  es länger dauert als ein Aufruf am Stück, schadet nicht — es kommt selten vor und meist mit
  wenigen Änderungen (gemessen: ein Schreibvorgang rund 12 ms). Verworfen: löschen und neu
  anlegen (neue `id` für alles, dazwischen leer) und ein `replace_directory` am Stück.
- **Verglichen wird der Inhalt**, nicht das Änderungsdatum: Archive und Git setzen alte Zeiten,
  und was im Store geändert wurde, sähe jünger aus und käme nie zurück. Ein Hash spart nichts —
  für ihn müsste man den Inhalt ohnehin lesen. Die Dateien sind klein, und der Node ist lokal.
- **Reihenfolge je Ebene:** erst im Ziel löschen, was in der Quelle fehlt oder dort die andere
  Art hat (Datei ↔ Ordner; Ordner mit `recursive`), dann Dateien anlegen oder mit
  `base_revision` schreiben, deren Inhalt abweicht, dann in die Ordner absteigen. Leere Ordner
  der Quelle entstehen im Store nicht — dort gibt es keine leeren Ordner (siehe „Datenmodell“,
  wie bei Git; wer einen braucht, legt eine `README.md` hinein).
- **Abbrechen und Höchstzeit** wirken zwischen zwei Vorgängen: Der laufende geht zu Ende, dann
  meldet die CLI, wie weit sie kam. `stale_revision` und ein unklarer Ausgang werden gemeldet,
  nicht wiederholt; der nächste Lauf gleicht an.
- **`--dry-run`** zeigt, was angelegt, geändert und gelöscht würde.
- **Vorerst nur unter `vendor/`:** `push` lehnt andere Ziele ab. Das ist ein Schutz vor
  Versehen in der CLI, keine Grenze am Hub — der kann nicht erkennen, dass Einzelvorgänge zu
  einem Abgleich gehören; ein Account könnte dasselbe von Hand. Die Sperre fällt, wenn sich der
  Abgleich bewährt hat.
- **`pull`** gleicht in der anderen Richtung ab: Store → lokaler Ordner, ebenso über den
  Inhalt. Lokal gelöscht wird nur mit `--delete`. Ändert sich während des Lesens etwas (Revision
  bei `read` anders als bei `list`), liest `pull` neu, damit es keinen halb alten Stand holt.

### Persönliche Verzeichnisse (vorgemerkt am 2026-09-26)

Nicht gebaut; festgehalten, damit der Weg klar ist. Gebaut wird es, wenn es sich als nötig
erweist.

**Die Mischform:** Alle dürfen lesen, aber man will bei der Arbeit nur das Eigene sehen.
Beispiel Tasks: Ein Entwickler legt sie an, bearbeitet und führt sie aus. Sie sind für alle
sichtbar, versioniert und gelten in begrenztem Umfang als Nachweis — die Tasks der anderen
stören aber bei der eigenen Arbeit. Das Beispiel gilt für alles Gleichartige, das in den
Store kommt.

- **Ein Verzeichnis einer Collection bekommt die Eigenschaft `personal`.** Dann zeigen
  Auflisten, Lesen und Schreiben dort nur Dokumente, deren `created_by` der User des Accounts
  ist. Ein Schalter `all` zeigt alles.
- **Die Suche bleibt unberührt.** Sie findet auch Fremdes.
- **Kein Recht, sondern eine Ansicht.** Der Hub prüft dafür nichts Neues; wer lesen darf, darf
  alles lesen. Gefiltert wird im Node, über ein Feld, das jede Zeile schon hat. Weder der
  Abgleich noch die Suche werden dadurch je Account verschieden.
- **Fremdes ohne `all`:** Lesen per Name oder Nummer meldet „gehört X, mit `all` lesbar“ statt
  „nicht gefunden“. Schreiben lehnt der Node ab — Schutz vor Versehen; ob fremdes Schreiben
  überhaupt erlaubt ist, regelt weiter `supersede`.
- **Nummern bleiben gemeinsam.** Der Hub vergibt fortlaufende Nummern über alle Dokumente des
  Verzeichnisses, gleich wem sie gehören. „Task 012“ bleibt eindeutig; in der eigenen Liste
  entstehen Lücken.
- **Die Eigenschaft muss sich abgleichen**, weil der Node filtert. Sie steht deshalb in
  `documents` als `SYSTEM:`-Zeile des Verzeichnisses, etwa `SYSTEM:D:tasks/` mit
  `{"personal": true}`, und nur der Hub schreibt sie.
- **Zweite Eigenschaft `numbered`** — fortlaufend nummerierte Namen, siehe „Zwei Arten von
  Eingaben“. Beide sind unabhängig: Tasks sind `numbered` und `personal`, Todos nur
  `personal`, gemeinsame Entscheidungen nur `numbered`. Das ersetzt den Gedanken der „Reihen“
  (siehe „Werkzeuge“).
- **Offen:** wer die Eigenschaft setzt (der Admin am Hub, später vielleicht über MCP), und wie
  eine Übergabe an einen anderen User geht — `created_by` ändert sich nie.

## Der Weg eines Eintrags

```text
Client ──► Node ──► Hub ──► Store
             ▲              │
             └── Antwort: id, Collection, Name, Revision
                    │
             Replica schreibt genau das
```

**Der Hub darf umsortieren.** Er kann ein Dokument anders einordnen, als der Aufrufer es
vorgeschlagen hat — anfangs nach Regeln, später selbsttätig. Deshalb ist seine Antwort die
Wahrheit und nicht der Wunsch des Aufrufers.

**Daraus folgt eine `id` je Dokument.** Ein Dokument hat eine stabile `id`, der Name ist nur
ein Merkmal. Sonst wäre jedes Umsortieren für die Replica „gelöscht und neu angelegt“, und
Verweise darauf würden brechen.

## Abgleich

Der Node merkt sich eine Revision je Collection und fragt „was hat sich seit dieser Revision
geändert“. Er bekommt geänderte Dokumente, Umzüge und Löschmarken, und zieht seinen Index
nach — genau der Vorgang, den der lokale Index heute beim Dateiwechsel schon macht.

Angestoßen wird das beim Start und danach regelmäßig, sowie unmittelbar nach einem eigenen
Schreibvorgang — gebaut in Task 014: nach Erfolg und nach unklarem Ausgang, ohne dass der
Aufrufer darauf wartet, auch bei `sync_interval` `0`; läuft der Abgleich des Hubs schon, folgt
genau einer.

**Im Hintergrund — entschieden am 2026-09-26.** `serve` gleicht jeden Hub-Eintrag beim Start
ab und danach in festem Abstand, Standard 30 s, änderbar in den `settings` des Nodes. Der
Abstand bestimmt, wie schnell eine Änderung von woanders — anderer Rechner, CLI am Hub —
lokal sichtbar wird, in `read`, `list`, `changes` und damit im Explorer von VS Code. Eigene
Schreibvorgänge warten nicht darauf. Eine Anfrage ohne Änderungen ist klein. Scheitert ein
Abgleich, steht das im Log, und der nächste folgt nach dem Abstand. Letzter Erfolg und
letzter Fehler je Hub liegen in der Datenbank des Nodes, damit `whoami` und `status` dasselbe
zeigen. Später meldet der Hub Änderungen über einen Ereignisstrom (SSE oder Long-Polling);
der Abstand bleibt dann als Rückfallebene.

Gebaut in Task 008: Der Abstand ist `sync_interval` in den `settings` des Nodes (Go-Dauer,
mindestens `1s`, `0` schaltet ab), gesetzt mit `kephalaion config set node sync_interval 1m`
und je Runde gelesen. Die Hub-Einträge liest jede Runde neu; jeder Eintrag gleicht für sich
ab, ein hängender Hub hält die anderen nicht auf; `https` und `ssh` werden übergangen. `local`
nimmt den Hub desselben `serve`. Das Log nennt einen Abgleich nur, wenn Zeilen kamen, einen
Fehler beim ersten Mal nach dem Start, beim Übergang von Erfolg zu Fehler und wenn sich seine
Art ändert, und die Erholung. Der Stand je Hub steht in `hub_sync` (letzter Erfolg, letzter
Fehler mit Zeit und Art), geschrieben auch von `node sync`; abgeleitet, nicht im Export.

Ergänzt in Task 012: Vor dem Verbinden mit dem Hub verwirft der Abgleich eine Replica alter
Schemafassung, fremder `entry_id` oder eindeutig beschädigt und legt sie neu an; das Ergebnis
nennt den Grund. Eindeutig beschädigt heißt: SQLite meldet `NOTADB` oder `CORRUPT` (beim
Öffnen oder beim Lesen von `db_info`), `db_info` fehlt oder hat keine Rolle, oder `hub_id`,
`entry_id` bzw. (seit Task 009) `generation` fehlen. Vorübergehende Fehler verwerfen
nichts — eine belegte Datei (`BUSY`, `LOCKED`), ein abgebrochener Abgleich, ein Fehler von `stat` oder der Zugriffsrechte —, ebenso
wenig eine Datei fremder Rolle; sie bleiben ein Fehler des Abgleichs. Beim Beenden wartet
`serve` höchstens die Frist (10 s) auf den Abgleich; ein Hub, der den Abbruch nicht beachtet,
hält es nicht auf. Ein danach noch laufender Abgleich hält nichts mehr in `hub_sync` fest.

**Nebenläufigkeit — entschieden am 2026-09-26.** `node sync`, `node hub rm|add` und `config
import` laufen als eigene Prozesse neben `serve`. Statt einer Sperre über Prozesse ist das
Schreiben des Abgleichs an die `entry_id` des Hub-Eintrags gebunden — eine ULID, beim Anlegen
vergeben, nie wiederkehrend, auch in `db_info` der Replica. Jede Transaktion, die in die
Replica schreibt, prüft zuerst `entry_id`, `hub_id` und dass kein Stand unter dem liegt, von
dem die Seite ausging; `hub_id` und `hub_sync` in `node.db` schreibt sie nur für diese
`entry_id`. Zeilen und Stände gehen nur vorwärts. Passt etwas nicht, schreibt die Seite
nichts; der Abgleich setzt neu auf oder bricht still ab, wenn sein Eintrag nicht mehr besteht.
Eine Replica entsteht unter eigenem Namen und wird fertig an ihren Ort gelinkt. So wartet
`node hub rm` nie auf einen hängenden Hub, und zwei Abgleiche derselben Replica vertragen
sich.

**Entschieden am 2026-09-25: Abgleich über die Revision, nicht über die Uhrzeit.** Der Hub
ist der einzige Schreiber und vergibt je Schreibvorgang innerhalb der Transaktion eine
fortlaufende Nummer. Ein Zeitstempel taugt nicht als Revision: Uhren springen zurück (NTP), und
zwei Änderungen in derselben Millisekunde machen „alles nach X“ mehrdeutig — beides führt zu
still verpassten Änderungen. Die Zeitstempel bleiben für Menschen. Einzelheiten unter
„Datenmodell“.

**Warum nicht rsync oder Git.** Beide kennen keine Rechte: Sie übertragen alles oder nichts.
Sobald ein Node nur einen Teil der Collections sehen darf, muss der Hub beim Ausliefern
filtern — und dann ist der Änderungsstrom ohnehin der kürzere Weg.

## Zwei Arten von Eingaben

**Reihenfolge, entschieden am 2026-09-25:** Im Kern ist Kephalaion eine Datenbank, die
Dateien deterministisch schreibt und ändert. Das kommt zuerst. Schnipsel, die erst
verarbeitet und dann eingepflegt werden, sind **zurückgestellt**; was unten über sie steht,
bleibt als Richtung stehen, wird aber vorerst nicht gebaut.

**Wissensschnipsel.** Ein Text mit so viel Zusammenhang wie möglich: wofür er gilt, woher er
stammt, was er bedeutet. Der Aufrufer schlägt nichts vor, er liefert. Was daraus wird —
welches Dokument, welche Art, ob mehrere Dokumente — entscheidet der Hub.

**Dateien.** Ganze Dokumente mit eigener Struktur, etwa Tasks. Sie werden nicht verdichtet,
sondern geführt: anlegen, ergänzen, einen Abschnitt ändern, abschließen. Dafür braucht es
keine KI, sondern verlässliche Vorgänge. Weil nur der Hub schreibt und seine Schreibvorgänge
nacheinander ausführt, braucht es keine eigene Sperre.

Der Unterschied ist nicht die Größe, sondern wer über die Form bestimmt. Beim Schnipsel der
Hub, bei der Datei der Aufrufer. Beide liegen im selben Store und werden gleich
indiziert; getrennt sind die **Werkzeuge**.

**Es sind zwei Familien, nicht ein Werkzeug mit zwei Antworten.** Ein Aufrufer, der einen
Schnipsel einliefert, tut etwas anderes als einer, der eine Datei schreibt, und bekommt etwas
anderes zurück.

| | Schnipsel einliefern | Datei schreiben |
|---|---|---|
| Eingabe | JSON mit festen Feldern: Text, wofür, Quelle, Bedeutung, Collection | Name, Inhalt oder Abschnitt |
| Der Hub | nimmt entgegen, ordnet später ein | speichert sofort, deterministisch |
| Antwort | „angenommen“, mit Vorgangsnummer | id, Collection, Name, Revision |
| Danach lokal | **noch nicht verfügbar** | nach dem Abgleich vorhanden |

**Die Einlieferung ist keine Speicherung.** Wer einen Schnipsel abgibt, weiß danach nur, dass
er angekommen ist. Ob daraus ein Dokument wird, mehrere, oder eine Ergänzung an einem
bestehenden, entscheidet der Hub, und sichtbar wird es beim nächsten Abgleich. Die
Vorgangsnummer bleibt auffindbar, damit „was wurde aus meinem Schnipsel“ beantwortbar ist.

**Ein Arbeitsprozess auf dem Hub** nimmt die Einlieferungen der Reihe nach vor. Was er
nicht entscheiden kann, legt er in eine eigene Warteschlange, die ein Mensch bearbeitet. Der
Reihe nach heißt: nachvollziehbar, und ohne zwei gleichzeitige Einordnungen am selben Thema.

**Der Schnipsel bleibt erhalten.** Was hereinkommt, wird archiviert, auch nachdem daraus ein
Dokument wurde — so wie der Eingang der lokalen Ablage. Sonst ist eine maschinelle
Einordnung nicht nachprüfbar und nicht wiederholbar.

**Dateien brauchen keine KI**, sondern verlässliche Vorgänge: anlegen, ergänzen, einen
Abschnitt ersetzen, abschließen — und ein ganzes Verzeichnis in einem Schritt ersetzen, wie es
ein Generator braucht (in k-playbook heute `publish`). Das ist der Teil, der sofort nutzbar
ist.

**Anlegen scheitert an einem vorhandenen Namen, und der Hub entscheidet das.** Ein Client
kann vorher nachsehen, aber das genügt nicht: Seine Replica kann hinter dem Hub zurückliegen,
und zwischen Nachsehen und Anlegen kann ein anderer Node denselben Namen belegen. Der Hub
lehnt deshalb mit einem eigenen Fehlercode ab (Name vergeben — endgültig, kein erneuter
Versuch); der Client wählt einen anderen Namen oder nimmt den nummerierten Weg unten. Für
das Ändern gilt dasselbe in anderer Form: Ein Schreibvorgang kann die Revision nennen, auf
der er beruht, und der Hub lehnt ab, wenn das Dokument inzwischen eine neuere hat — sonst
überschreiben zwei Nodes einander still.

**Fortlaufend nummerierte Namen (vorgemerkt).** Manche Namen werden hochgezählt, etwa Tasks
(`tasks/004-kurzname.md`). Schreiben mehrere Nodes, kann das nur der Hub, weil nur er den
ganzen Stand kennt und seine Schreibvorgänge nacheinander ausführt. Ein eigener Vorgang:

- Der Aufrufer übergibt Collection, Verzeichnis (`tasks/`) und den Rest des Namens
  (`kurzname.md`), wahlweise schon einen Inhalt; ohne Inhalt wird das Dokument leer angelegt.
- Der Hub sucht unter dem Verzeichnis — auch in Unterverzeichnissen wie `tasks/done/` — die
  höchste Nummer im **letzten Pfadsegment** nach dem festen Muster `<Ziffern>-<Rest>`. Namen,
  die dem Muster nicht folgen, zählen nicht. Löschmarken zählen mit: Eine Nummer wird nie
  zweimal vergeben.
- Er legt `<Verzeichnis><Nummer+1>-<Rest>` an, die Nummer mit führenden Nullen auf die Breite
  der bisher höchsten (mindestens drei Stellen), und antwortet mit id, Name und Revision.
- **Suche und Anlegen stehen in derselben Transaktion**, der Zähler der Revision reiht sie
  hinter alle anderen Schreiber — zwei Nodes bekommen nie dieselbe Nummer.
- **Das Muster prüft Go, nicht SQL.** Die Abfrage grenzt nur über den Präfix des Verzeichnisses
  ein (`name >= 'tasks/' AND name < 'tasks0'`, nutzt den Index auf `(collection, name)`);
  reguläre Ausdrücke sind in SQLite und PostgreSQL verschieden, und ein Verzeichnis hat
  selten mehr als einige hundert Einträge.

## Löschen

Drei Stufen:

- **Ablösen** ist der Normalfall: Überholtes bekommt einen Nachfolger und fällt aus der Suche,
  bleibt aber lesbar.
- **Löschen** über einen Node (Eigenes mit `write`, Fremdes mit `supersede`) setzt eine
  Löschmarke: Der Inhalt verschwindet aus Store und Replicas, die Zeile bleibt als Marke. Den
  Namen darf danach jeder mit `write` neu anlegen. Ein Verzeichnis wird als Ganzes gelöscht,
  nur ausdrücklich (`recursive`): alle Dokumente darunter unter einer Revision, alles oder
  nichts (Task 014).
- **Endgültiges Löschen gehört allein dem Admin.** Es läuft direkt am Hub, nicht über einen
  Node, und ist der Weg für etwas Vertrauliches, das versehentlich eingeliefert wurde. Entfernt
  wird auch, was sonst bliebe — Einlieferung, ältere Fassungen, sobald es sie gibt. Im
  Protokoll bleibt, dass gelöscht wurde, von wem und warum — nicht aber der Inhalt.

**Was ein Löschen nicht kann:** Es holt nichts zurück, was schon abgeglichen wurde. Die
Replicas erfahren beim nächsten Abgleich davon und entfernen es; was jemand daraus kopiert oder
gesichert hat, bleibt. Das ist keine Schwäche dieses Entwurfs, sondern die Eigenschaft
verteilter Replicas — die Antwort darauf ist, beim Einliefern vorsichtig zu sein, nicht beim
Löschen gründlich.

## Indizierung

**Indiziert wird auf dem Node, über genau den Ausschnitt, den er replizieren darf.** Das ist
die Replica, und die enthält nur die Collections, die der Hub ihm erlaubt (`replicate`). Welche
davon eine einzelne Anfrage sehen darf, entscheidet der Node über die Rechte des Accounts je
Collection — grob, vor der Suche, nicht je Treffer.

Daraus folgen drei Dinge:

- **Die Zerlegung in Abschnitte gibt es nur im Node** und trägt eine Fassung. Ändert sie
  sich, wird der Index neu gebaut; die lokale Ablage von k-playbook macht das schon so.
- **Der Abgleich zieht den Index nach**, Dokument für Dokument: geänderte neu zerlegen,
  gelöschte entfernen. Ein vollständiger Neubau ist nur nötig, wenn sich die Fassung der
  Zerlegung ändert.
- **Einbettungen liefert der Hub mit**, wenn es sie gibt. Sie gehören zu einem Modell, und
  die Replica hält fest, zu welchem — ein Index, der mit einem Modell gebaut und mit einem
  anderen befragt wird, antwortet Unsinn, und das muss sichtbar sein statt still.

**Ein Client ohne eigenen Node** wird nicht vom Hub bedient, sondern von einem Node, der
neben ihm läuft und eine Replica hält wie jeder andere. Sonst gäbe es die Zerlegung in
Abschnitte zweimal. Der Hub sucht nicht.

## Wenn der Hub selbst einordnet (zurückgestellt)

Der Hub soll Schnipsel einordnen, zusammenführen und Überholtes erkennen. Das heißt, dass
im Hub eine KI arbeitet. Zwei Leitplanken, ohne die daraus ein Store wird, dem
niemand mehr traut:

- **Jede maschinelle Entscheidung wird protokolliert:** Eingabe, betroffene Dokumente,
  Begründung, Zeitpunkt, Modell. Das Protokoll ist selbst Teil des Stores und wird von Zeit
  zu Zeit durchgesehen.
- **Unklares wandert in eine Warteschlange**, statt geraten zu werden. Ein Mensch entscheidet
  dort. Das ist dieselbe Zone, die die lokale Ablage schon kennt: Was offen ist, ist sichtbar
  offen.

**Folge für die Antwort.** Eine Einordnung durch eine KI dauert und kann mehrere Dokumente
berühren. Der Schreibvorgang kann dann nicht mehr sagen „liegt unter diesem Pfad“. Zwei
Antwortarten:

| Fall | Antwort |
|---|---|
| deterministisch gespeichert (Datei, einfacher Schnipsel) | id, Collection, Name, Revision |
| zur Einordnung angenommen | Vorgangsnummer und die Aufforderung, abzugleichen |

Nach dem Abgleich sieht der Node, was daraus geworden ist. Die Vorgangsnummer bleibt
auffindbar, damit „was wurde aus meinem Schnipsel“ beantwortbar ist.

Ob der Hub wirklich eine KI enthalten soll, wird **getrennt verfolgt**: Es bringt
Kosten, Abhängigkeit von einem Anbieter und eine neue Fehlerquelle in einen Dienst, der sonst
nur verwaltet. Die erste Stufe kommt ohne aus.

## Transport, Token und Fehlschläge

Zwischen Node und Hub gibt es nur wenige Vorgänge: einliefern, schreiben, abgleichen,
verwalten. Die Wege dafür stehen unter „Kommunikation“.

**Token.** Jeder Zugriff trägt Account-Name und Token (siehe „Authentifizierung“). Der Hub
verwaltet sie: ausstellen, Rechte je Collection hinterlegen, zurückziehen. Lokal liegt das
Token in der Nutzerkonfiguration mit engen Rechten, nie im Repository.

**Ein Schreibvorgang scheitert auf drei Arten, und jede wird gemeldet — entschieden am
2026-09-26, umgesetzt in Task 014:**

- **Abgelehnt** — fehlendes Recht, unbrauchbare Eingabe, unbekannte Collection, Name
  vergeben, veraltete Revision. Das ist endgültig; der Aufrufer erfährt den Grund und kann
  korrigieren. Kein erneuter Versuch.
- **Nicht erreichbar** — die Anfrage hat den Hub nachweislich nicht erreicht (keine
  Verbindung, eine Weiterleitung). Dann ist *nichts gespeichert*, und so heißt die Meldung.
- **Ausgang unklar** — die Anfrage war abgeschickt, dann kam keine brauchbare Antwort
  (Zeitüberschreitung, abgebrochene Verbindung, 5xx). Der Hub kann gespeichert haben. Ein
  Schreibvorgang wird deshalb **nie wiederholt**, wie `rotate`: Ein zweiter Versuch ergäbe
  „Name vergeben“ oder einen Konflikt mit sich selbst. Die Meldung sagt, dass gespeichert sein
  kann; der Node gleicht danach ab, und der Aufrufer sieht nach. Früher stand hier, der Node
  wiederhole mit wachsendem Abstand und melde danach „nichts gespeichert“ — das lässt sich ohne
  einen Schlüssel, an dem der Hub eine Wiederholung erkennt, nicht halten. Ein solcher
  Schlüssel kommt, wenn überhaupt, mit `https` und `ssh`.

## Authentifizierung

**Account-Name und Token, wie Benutzername und Passwort.** Jede Anfrage trägt beide. Hub und
Node speichern nur `sha256(token)`; geprüft wird, indem der Account über seinen Namen
nachgeschlagen, das vorgelegte Token gehasht und in konstanter Zeit verglichen wird. Ein Hash
wird nirgends als Ausweis angenommen, und kein Vorgang antwortet auf einen Hash allein. Der
Hash schützt, was liegt — eine kompromittierte Datenbank enthält keine brauchbaren
Zugangsdaten. Was unterwegs ist, schützt der Transport. Token sind Zufallswerte mit 256 Bit;
ein einfacher SHA-256 genügt, langsame Verfahren braucht es nur für Passwörter. Format:
`keph_<geheimnis>` — der Präfix lässt Scanner wie gitleaks ein eingechecktes Token erkennen.

Der Name bringt kryptographisch nichts dazu, praktisch aber: Fehlversuche lassen sich einem
Account zuordnen und begrenzen, ein vertauschtes Token fällt auf, und Logs nennen Namen statt
Geheimnisse. Unbekannter Name und falsches Token bekommen dieselbe Antwort.

**Wo das Token unterwegs ist:**

| Strecke | Wie oft | Schutz |
|---|---|---|
| MCP-Konfiguration des Clients → Node | je Anfrage, als Header | nur `127.0.0.1`, Datei mit `0600`; die KI sieht es nicht |
| Node → Hub | je Vorgang in fremdem Namen | TLS oder SSH; bisher nur `http` auf Loopback |

Das Token steht nie in einem Tool-Call.

**Zwei Identitäten je Anfrage an den Hub.** Der Node meldet sich immer mit seinem eigenen
Token an — er ist der Träger. Das Token des Accounts steht in der einzelnen Anfrage und sagt,
in wessen Namen. Ein Node, der im eigenen Namen für andere schreiben dürfte, wäre ein
Generalschlüssel.

**Wer wann prüft:**

| Vorgang | Node | Hub |
|---|---|---|
| Lesen | prüft gegen die Account-Zeilen seiner Replica | — |
| Schreiben | prüft vorher Anmeldung und Lesbarkeit gegen die Account-Zeilen seiner Replica, reicht dann durch | prüft Anmeldung, Lesbarkeit, Recht, Form und Vorbedingung — maßgeblich |
| `rotate` | reicht durch, muss den Account nicht kennen | prüft, liefert den Account-Eintrag |

**Grenze beim Schreiben (Task 014):** Weil der Node vorher gegen seine Replica prüft, setzt
Schreiben voraus, dass sie die Collection und die `SYSTEM:A:`-Zeile des Accounts schon trägt —
nach `grant` also erst nach dem nächsten Abgleich; hat der Node den Hub noch nie abgeglichen,
geht nichts, und die Meldung nennt `kephalaion node sync <hub>` als Ausweg. Nicht angemeldet
oder nicht lesbar ist dieselbe Meldung „nicht lesbar“, ohne den Hub zu fragen. Ob geschrieben
werden darf, entscheidet trotzdem allein der Hub, gegen `accounts` — so wirkt eine Sperre beim
Schreiben sofort, auch wenn die Replica sie noch nicht kennt.

**Einrichtung.** Collections und Accounts legt der Admin am Hub per Kommandozeile an; sie
liegen in der Datenbank des Hubs, die Konfigurationsdatei enthält nur, was der Dienst zum
Starten braucht. Ein Account — ein Zugang auf einem Rechner — bekommt Name, User,
Kurzbeschreibung (`kephalaion hub account add <name> [--user …] [--description …]`) und seine
Rechte je Collection, gesetzt mit `kephalaion hub account grant <name> <collection> [--write]
[--supersede] [--vendor <name>]…` — `grant` setzt die Rechte der Collection vollständig, ohne
`--write` wird `write` entzogen, ohne `--vendor` jeder Scope (Task 016); `revoke` nimmt die Collection (umgesetzt in Task 005 statt `account add
--scope`). Der User (`--user`, ohne Angabe der Name des Accounts; ändern mit `hub account set
--user`) ist mit Task 006 gebaut. Ein Node bekommt einen Eintrag mit Name und
Kurzbeschreibung (`kephalaion hub node add <name>`)
und die Collections, die er abgleichen darf (`kephalaion hub node grant <node>
<collection>`). In beiden Fällen erzeugt der Hub ein Token, zeigt es einmal an und speichert
nur den Hash. Das Token wird vorerst von Hand übergeben. Ein Node trägt sein Token in seine
Datenbank ein (`kephalaion node hub add …`); steht der Hub in derselben config, legt `node hub
add … --transport local --create` den Node dort selbst an und trägt das Token direkt ein, ohne
es anzuzeigen.

**Auch der erste Vorgang eines Nodes ist ein `rotate` — entschieden am 2026-09-26**, gebaut
wird es nach dem `rotate` der Accounts. Das angezeigte Token taugt dann nur zur Einrichtung,
und das erste `rotate` prüft Verbindung und Zusammenspiel gleich beim Einrichten statt später.
Anders als ein Account legt der Node das neue Token selbst ab, in `node.db`; eine fremde
Konfiguration muss dafür niemand ändern. Grundsatz: Ein Node wird behandelt wie ein Account,
außer wo es anders sinnvoll ist.

**Der erste Vorgang jedes Accounts ist ein `rotate`.** Einrichtung und Rotation sind derselbe
Vorgang:

1. Der Account erzeugt ein neues Token und speichert es als „ausstehend“ neben dem alten,
   bevor er etwas abschickt.
2. Er sendet das **alte Token** zur Anmeldung und den **Hash des neuen**.
3. Der Hub prüft das alte Token, ersetzt den Hash und liefert dem Node den Eintrag des
   Accounts — beschränkt auf die Collections, die dieser Node repliziert. Hat der Account
   keine davon, ist das ein Fehler.
4. Der Node schreibt den Eintrag in seine Replica und antwortet mit „ok“. Der Account
   weiß damit: Der Hub ist erreichbar, das neue Token gilt, und der Node kennt ihn.
5. Erst dann löscht der Account das alte Token.

Das Einrichtungs-Token ist danach wertlos. Nebeneffekt: Ein neuer Account ist sofort auf dem
Node bekannt, ohne auf einen Abgleich zu warten.

**Umgesetzt (Task 005): `rotate` ist ein Kommando der Kommandozeile des Nodes, kein
MCP-Werkzeug** — das Token stünde sonst im Kontext der KI: `kephalaion node account rotate
<hub> <account> (--token-file pfad | --token-stdin)`. „Ausstehend“ ist die Datei
`<pfad>.pending` (`0600`), geschrieben vor dem Aufruf; nach Erfolg ersetzt sie die Token-Datei,
danach schreibt der Node die gelieferten Zeilen in die Replica — nur die der gewünschten
Collections; ein Fehler dabei kippt den Erfolg nicht, `node sync` holt nach. Der Hub ersetzt
den Hash in `accounts` und allen Zeilen in einer Transaktion, eine Revision, eine Zeile `rotate`
in `actions` (`carrier` ist der Node); Fehlversuche stehen nur im Log. Der Transport wiederholt
`rotate` nie; bei unklarem Ausgang bleiben beide Dateien, und `kephalaion node account check`
(`whoami` mit Account-Teil) klärt, welches Token gilt, und räumt auf. **Unklar ist jeder Fehler,
der kein Fehler des Vertrags ist (Task 007)** — über HTTP ein 5xx, über `local` ebenso ein
Fehler der Datenbank: Er kann nach dem Commit entstanden sein, und wer ihn als Scheitern
nähme, löschte das neue Token, obwohl nur noch dieses gilt. Der Hub liest deshalb alles, was
die Antwort braucht (`hub_id`), vor dem Commit; danach stellt er sie nur noch zusammen. Eine
Weiterleitung (3xx) dagegen ist ein eindeutiges Scheitern: Der Client folgt ihr nicht, der
Hub hat nicht ausgeführt.

**Wenn das „ok“ ausbleibt,** meldet sich der Account mit dem neuen Token an (`whoami`).
Gelingt das, war die Rotation erfolgreich; sonst wiederholt er sie mit dem alten. Der Hub
kennt keinen Wiederholungsfall, `rotate` verlangt immer ein gültiges altes Token. Der
Mechanismus wird so definiert; ein Account implementiert die Wiederholung, wenn er will.

**Sperren** wirkt beim Schreiben sofort, denn geschrieben wird nur über den Hub. Beim Lesen
wirkt es auf einem Node erst mit dem nächsten Abgleich; der Hub meldet Sperren deshalb sofort
über den Ereignisstrom (noch nicht gebaut). `hub account lock` macht alle Zeilen des Accounts
zu Löschmarken und **merkt seine Rechte in `accounts`**; `unlock` legt die Zeilen daraus neu an,
mit dem Hash aus `accounts`. Ein gesperrter Account kann nicht rotieren.

**Die Grenze beim Lesen ist der Rechner.** Die Replica liegt unverschlüsselt beim Node. Auf
dem Rechner haben nur root und der Node Zugriff auf sie; das genügt. Die Token-Prüfung im
Node ordnet Anfragen ihren Collections zu. Die harte Grenze ist, was der Hub auf den Rechner
lässt — das bestimmt, welche Collections der Hub dem Node erlaubt (`replicate`).

**Was der Hub über einen Node weiß:** nur seinen Eintrag in `nodes` und die erlaubten
Collections in `node_collections`. `replicate` umfasst den Inhalt der Collection und ihre
Account-Zeilen (`SYSTEM:A:<account>`, siehe „Datenmodell“). `read` allein bekommt die
Account-Zeilen nicht. Welche Collections ein Node tatsächlich
hält, verfolgt der Hub nicht.

## Mehrere Hubs

Ein Node bedient mehrere Hubs — etwa den eines Arbeitgebers, dessen Daten dessen Netz nicht
verlassen dürfen, und einen privaten. Mehrere Nodes auf einem Rechner wären schlechter: mehrere
Dienste, mehrere Ports, mehrere MCP-Einträge je Client.

- **Hubs wissen nichts voneinander.** Jeder hat eigene Collections, Accounts, User und Token.
  Sie treffen sich nur im Node. Derselbe Mensch kann an zwei Hubs verschiedene User heißen.
- **Adressen sind zweistufig.** Auf dem Hub bleibt alles hub-lokal (`team-x:write`). Im Node
  und für Clients gilt `<hub>:<collection>`; den Hub-Namen vergibt der Node als Alias in
  seiner Datenbank.
- **Der Node ist auf jedem Hub ein eigener Eintrag** in `nodes`, mit eigenem Token und
  eigener Rotation.
  Seine Datenbank führt eine Liste von Hubs: Name, sein eigener Name am Hub, Adresse, Transport
  (`https`, `http`, `ssh`, `local`), SSH-Schlüssel, Token.
- **Abgleich je Hub.** Ist ein Hub nicht erreichbar, laufen die anderen weiter.
- **Ein Client trägt mehrere Paare aus Name und Token**, je Hub höchstens eins. Die Suche
  geht über alle Collections, die diese Accounts lesen dürfen.

**Kein Schutz gegen Abfluss zwischen Hubs.** Ein Client mit Token für zwei Hubs kann aus
dem einen lesen und in den anderen schreiben. Das zu verhindern ist nicht Aufgabe von
Kephalaion; wer Daten übertragen will, kann das auch anders. Geschützt wird, dass die
Datenbank eines Hubs nicht anderswo läuft und kein Fremder auf sie zugreift.

## Speicherung

**Entschieden am 2026-09-25: Das Journal ist eine Datenbank.** Der Abgleich fragt „was hat
sich seit Revision X geändert“; dafür braucht es eine fortlaufende Folge von Änderungen,
Umzügen und Löschmarken. Git als interner Speicher scheidet aus, weil das echte
Löschen durch den Admin dann ein Umschreiben der Historie wäre.

**Entschieden am 2026-09-25: Auch die Inhalte liegen in der Datenbank**, nicht in
Markdown-Dateien daneben; Markdown gibt es als Export. Die Entscheidung steht: Ist die Suche
zu langsam, wird am Index oder an der Suche gearbeitet, nicht an der Datenbank. Die Gründe:

- **Index nachziehen.** Der lokale Index von k-playbook lädt heute bei jedem Schreiben den
  ganzen Index und schreibt ihn zurück; gemessen 48 ms je Dokument bei 640 Dokumenten, ein
  Import wächst quadratisch (`k-playbook/docs/knowledge-gate.md`, Abschnitt zu den
  Messungen). Ein Node zieht aber laufend Deltas nach. Ein Volltextindex in der Datenbank
  (SQLite FTS5) ändert je Dokument nur dessen Zeilen.
- **Lesen während des Abgleichs.** Der Node beantwortet viele Anfragen parallel, während er
  abgleicht. SQLite im WAL-Modus erlaubt viele Leser und einen Schreiber, ohne selbst gebaute
  Sperren.
- **Delta atomar anwenden.** Replica, Index und Revision bewegen sich in einer Transaktion —
  oder gar nicht. Mit Dateien entsteht ein Fenster, in dem sie auseinanderlaufen. Im Hub gilt
  dasselbe für Journaleintrag und Inhalt.
- **Die Replica soll nicht editiert werden.** Dateien laden dazu ein; eine Datenbank nicht.

Lesen eines einzelnen Dokuments ist dagegen kein Unterscheidungsmerkmal: aus Datei und aus
SQLite gleichermaßen unter einer Millisekunde.

Auf dem Node ist das SQLite über einen reinen Go-Treiber (`modernc.org/sqlite`), weil ohne C
gebaut wird. Die Replica bleibt lokal: Sie ist abgeleitet, jederzeit neu abzugleichen, und
lokal am schnellsten.

**Der Hub soll eine richtige Datenbank bekommen können** — PostgreSQL, betrieben und
regelmäßig gesichert wie jede andere. Anfangs ist es SQLite, einstellbar bei der
Einrichtung. Damit der Weg offen bleibt, ist der Datenbankzugriff des Hubs von Anfang an
hinter einer eigenen Schnittstelle gekapselt, und seine Abfragen bleiben in beiden
Datenbanken ausführbar. Der Hub sucht nicht, braucht also weder FTS5 noch ein
Gegenstück. Der Wechsel ändert den Vertrag nicht.

So ist es umgesetzt: Hub und Node haben je eine Schnittstelle zu ihrer Datenbank
(`internal/hub/store`, `internal/node/store`), darunter ein gemeinsamer SQLite-Unterbau, der
keine der beiden Rollen kennt. Die Abfragen des Hubs und des Unterbaus stehen zentral, mit
Platzhaltern `$n`, ohne SQLite-Eigenes wie `INSERT OR`, `PRAGMA` oder `AUTOINCREMENT`; ein Test
prüft das. Das DDL steht je Dialekt; das für PostgreSQL entsteht mit dessen Umsetzung (dort
`BIGINT` für Zeitstempel und Revisionen). Der volle Nachweis kommt erst mit PostgreSQL selbst.
Die Verbindung zu SQLite läuft im WAL-Modus, mit `foreign_keys` und `busy_timeout`.

- **Ein Ausweg nach Markdown bleibt — als Export.** Ein Store, den man nur über einen
  laufenden Dienst lesen kann, ist ein Store, den man verlieren kann. Markdown mit
  Frontmatter wird bei Bedarf exportiert, ist aber nicht der Speicher.

**Was dabei zu beachten ist:** FTS5 rechnet BM25 anders als der Index von k-playbook. Die
Ranking-Korrektur aus Task 056 (Zeiger-Dokumente vor ihren Zielen) ist neu nachzuweisen. Die
Zerlegung in Abschnitte bleibt eigener Code; FTS5 indiziert nur, was sie liefert.

**Wo die Replica liegt: nicht im Projekt.** Fremdes Wissen hat in der Versionierung eines
Projekts nichts verloren. Ein Ort je Rechner, eine Datenbank je Hub, gemeinsam für alle
Projekte dieses Rechners.

**Orte nach XDG — entschieden am 2026-09-25**, auf Linux und macOS gleich:

| Verzeichnis | Zweck | Kephalaion |
|---|---|---|
| `~/.config/kephalaion/` (`XDG_CONFIG_HOME`) | Konfiguration, klein, lesbar | `config.yaml`, `tokens/<hub>/<account>.token` je Account (`0600`) |
| `~/.local/share/kephalaion/` (`XDG_DATA_HOME`) | Daten, die bleiben müssen | `hub.db`, `node.db` |
| `~/.local/share/kephalaion/replicas/` | wiederherstellbar durch Abgleich | `<alias>.db` je Hub-Eintrag |
| `~/.local/state/kephalaion/` (`XDG_STATE_HOME`) | Zustand, Logs | `serve.log` des LaunchAgent (macOS); unter Linux geht das Log ins Journal |

- **Token-Dateien — festgehalten am 2026-09-26:** Die Tokens der Accounts liegen neben der
  config, **eine Datei je Account und Hub**: `~/.config/kephalaion/tokens/<hub>/<account>.token`,
  Rechte `0600`, die Verzeichnisse `0700`, das Token in der ersten Zeile. `<hub>` ist der
  Alias des Hub-Eintrags am Node — derselbe Name wie in den Headern (`X-Keph-Account-<hub>`)
  und in den Adressen (`<hub>:<collection>`); die Datei liegt auf dem Rechner des Nodes.
  **Der Hub steht im Pfad, nicht im Dateinamen** (entschieden am 2026-09-26): Aliase und
  Accounts dürfen beide `-`, `.` und `_` enthalten, ein Name wie `home-kamran-desktop.token`
  ließe sich nicht eindeutig zerlegen. Ein User hat meist mehrere Accounts (siehe
  [`begriffe.md`](begriffe.md), „user“), also auch mehrere Dateien — etwa einen je Rechner
  oder je Hub. Während `rotate` liegt daneben `<account>.token.pending`. Bisher ist das eine
  Konvention (README, `node account rotate --token-file`); der Code legt den Ort nicht fest.
  Wer ihn kennt: die Erweiterung für VS Code liest die Tokens von dort
  ([`vscode.md`](vscode.md)) und ordnet sie über das Verzeichnis den Hubs zu. Später kann
  `node account rotate|check <hub> <account>` die Datei ohne `--token-file` selbst finden.
- **Die Replicas liegen in einem eigenen Unterverzeichnis** `replicas/` im Verzeichnis von
  `node.db` (liegt `node.db` per `--db` anderswo, dann dort), nicht direkt daneben: So ist
  sichtbar, was sich neu abgleichen lässt und was nicht. Eine Sicherung kann `replicas/`
  auslassen. Nicht unter `~/.cache`: Aufräumprogramme leeren es bedenkenlos, und jedes Leeren
  hieße einen vollständigen Abgleich.
- **macOS folgt ebenfalls XDG**, nicht `~/Library/Application Support`: dieselbe Doku,
  dieselben Tests, derselbe Pfad im Devcontainer.
- **Eine globale Installation** — Node und Hub unter dem Systembenutzer `kephalaion` —
  gehört nach `/var/lib/kephalaion/` (systemd `StateDirectory=`). Das ist kein Standard von
  `init`, sondern kommt mit der Einrichtung: `--db` beim `init`, `KEPHALAION_CONFIG` in der
  Unit (siehe „Installation und Betrieb“).
- **Nie auf einem synchronisierten oder Netzlaufwerk.** SQLite im WAL-Modus braucht ein
  lokales Dateisystem mit funktionierenden Sperren. Ein Sync-Ordner (HiDrive, OneDrive,
  Dropbox, iCloud) oder unter WSL `/mnt/<laufwerk>/` kann die Datenbank beschädigen oder sehr
  langsam machen. `init` warnt bei solchen Pfaden: `/mnt/` unter WSL sicher, Sync-Ordner
  über bekannte Namen im Pfad.

## Installation und Betrieb

**Entschieden am 2026-09-26: zwei Arten der Installation, je Rechner genau eine.**

| | pro User (user installation) | global (system installation) |
|---|---|---|
| Für | einen Menschen auf seinem Rechner | alle User eines Rechners, auch aus Devcontainern |
| Plattform | Linux, macOS | nur Linux |
| Binary | `~/.local/bin/kephalaion`, gehört dem User | `/usr/local/bin/kephalaion`, root, `0755` |
| Installiert durch | `install.sh`, direkt oder über k-playbook | Ansible oder von Hand, nach [`installation.md`](installation.md) |
| Läuft als | der User | Systembenutzer `kephalaion` |
| config | `~/.config/kephalaion/config.yaml` | `/etc/kephalaion/config.yaml` |
| Daten | `~/.local/share/kephalaion/` | `/var/lib/kephalaion/` |
| Dienst | `service install`: systemd `--user`, auf macOS LaunchAgent `io.github.kephalaion` | System-Unit aus `service unit --system` |
| Upgrade | der User: `kephalaion upgrade` | der Verwalter: Ansible oder `sudo kephalaion upgrade` |

- **Ohne Clone.** Keine der beiden Arten braucht Git oder Go: Binary und `SHA256SUMS` kommen
  aus dem Release — über `install.sh` oder über Ansible (`get_url` mit
  `checksum: sha256:<URL von SHA256SUMS>`, das Modul sucht die Zeile zum Asset selbst heraus).
  Namen und URLs der Assets sind damit eine Schnittstelle und ändern sich nicht ohne Hinweis
  im Release.
- **Nie beide auf einem Rechner.** Sie wollten dieselben Ports, und ein Client wüsste nicht,
  welchen Node er meint. `init` ohne `--config` und ohne `KEPHALAION_CONFIG` richtet nur pro
  User ein und bricht ab, wenn es die globale config findet; `service install` bricht dann
  ebenso ab, noch vor der Prüfung auf systemd. Die umgekehrte Reihenfolge — erst pro User,
  dann global — verhindert Kephalaion nicht, es erkennt sie: Gibt es die config des Users und
  die globale, meldet `status` zwei Arten auf einem Rechner als Fehler (Exit 1) mit dem Weg,
  die Installation pro User zu entfernen. Die Doku verlangt das vor der globalen Einrichtung.
- **Global ist ein Dienst für alle.** Node und, falls eingerichtet, Hub laufen in einem
  `serve` unter dem Systembenutzer. Die User betreiben nichts, sie sind Clients: je User ein
  Account mit Token und der Eintrag in seinen MCP-Clients (Sache von k-playbook). Das
  Anmeldemodell trägt das schon — jede Anfrage bringt Account und Token mit, und der Node
  prüft gegen die Rechte dieses Accounts. Loopback teilen alle User eines Rechners;
  Devcontainer erreichen den Node über die Docker-Bridge (siehe „Kommunikation“). **Stand nach
  Task 011:** Die globale Installation ist gebaut für User auf dem Rechner selbst, über
  Loopback, ohne Devcontainer — die Docker-Bridge fehlt und bleibt für Devcontainer
  Voraussetzung (eigene Task).
- **Rechte im Dateisystem, global:** Die config ist für alle lesbar (`0644`) — sie enthält
  kein Geheimnis und sagt, wo der Node lauscht. Die Datenbanken gehören `kephalaion`
  (`/var/lib/kephalaion`, `0700`). Verwaltet wird als Systembenutzer, etwa
  `sudo -u kephalaion kephalaion hub account add …`. Ruft ein anderer User `status` auf, sagt
  es, dass die Installation global ist und wie man sie verwaltet, statt an der Datenbank zu
  scheitern (Exit 0).
- **`/etc/kephalaion/` gehört `kephalaion`, `0755` — festgelegt am 2026-09-26 (Task 011).**
  So schreibt `init` als Systembenutzer die config (`sudo -u kephalaion kephalaion node init
  --config /etc/kephalaion/config.yaml --db sqlite:///var/lib/kephalaion/node.db`), ohne dass
  `init` als root laufen müsste. Der laufende Dienst schreibt dort trotzdem nie: Die
  System-Unit hat `ProtectSystem=strict`, schreibbar ist für ihn nur `/var/lib/kephalaion`.
  Die config ändert nur, wer als Systembenutzer ein Kommando aufruft. Verworfen: das
  Verzeichnis root zu geben — dann müsste `init` als root laufen und die config danach
  umgehängt werden.
- **Kein Token in Ansible.** Ansible legt Binary, Systembenutzer, Verzeichnisse, config und
  Dienst an und richtet die Rollen ein (`init` mit ausdrücklichem `--config` und `--db`). Die
  Hub-Einträge des Nodes (`node hub add`, erstes `rotate`) richtet der Verwalter von Hand ein;
  die Tokens der User gehören den Usern bzw. k-playbook.

**Die config wird gefunden — entschieden am 2026-09-26.** Reihenfolge:

1. `--config`,
2. `KEPHALAION_CONFIG`,
3. `$XDG_CONFIG_HOME/kephalaion/config.yaml` bzw. `~/.config/kephalaion/config.yaml`, wenn
   die Datei existiert,
4. `/etc/kephalaion/config.yaml`, wenn die Datei existiert,
5. sonst der Ort aus 3 — dort legt `init` pro User an.

Die System-Unit setzt `KEPHALAION_CONFIG` ausdrücklich; der Dienst hängt nicht an der Suche.
`status` nennt, welche config gilt und woher (Flag, Umgebung, User, global). Gebaut in Task
011 (`config.Locate`). Eine config des Users, auf die der Aufrufer nicht zugreifen darf, zählt
als nicht vorhanden — unter `sudo -u kephalaion` mit fremdem `HOME` gilt so die globale.
Exit-Code von `status`: 1, wenn eine Datenbank fehlt oder nicht passt oder zwei Arten
nebeneinander liegen; 0 sonst, auch bei der globalen config ohne Leserecht auf die
Datenbanken.

**Der Dienst — entschieden am 2026-09-26: systemd auf Linux, launchd auf macOS.**

- **Das hängt nicht an der Distribution.** systemd läuft auf praktisch allen verbreiteten —
  Debian, Ubuntu, Fedora, RHEL und Nachbauten, openSUSE, Arch —, und die Unit ist überall
  dieselbe, `useradd` ebenso. Ohne systemd (Alpine mit OpenRC, schlanke Container, WSL ohne
  `systemd=true` in `/etc/wsl.conf`) nennt die Doku nur den Aufruf `kephalaion serve` für
  einen eigenen Supervisor; eigene Dateien dafür gibt es nicht.
- **Die Units erzeugt das Binary** — `kephalaion service install|uninstall|status`, dazu
  `service unit [--system]`, festgelegt am 2026-09-26: Pro User schreibt es die Benutzer-Unit
  `~/.config/systemd/user/kephalaion.service` bzw. den LaunchAgent, aktiviert und startet ihn;
  global gibt es die System-Unit aus, und Ansible oder der Verwalter legt sie ab. Eine Quelle,
  passend zur Version des Binarys. Nicht in `init`: `init` gilt je Rolle, der Dienst gehört zu
  `serve`, und `init` nennt nur den nächsten Schritt.
- **Gebaut in Task 011 (2026-09-26):** Die Benutzer-Unit startet das eigene Binary mit
  absolutem Pfad und `serve`, dazu `--config`, wenn die config nicht unter
  `~/.config/kephalaion/config.yaml` liegt; `Restart=on-failure`, `RestartSec=5s`,
  `WantedBy=default.target`. `service install` bricht ab neben der globalen config, ohne
  systemd (mit dem Aufruf für einen eigenen Supervisor), ohne Rolle und solange `serve` von
  Hand läuft (Sperre `<db>.lock`); läuft der Dienst schon, schreibt es neu und startet ihn neu.
  Linger schaltet es nicht ein, nennt aber `loginctl enable-linger`, wenn ein Hub eingerichtet
  ist; unter WSL den Hinweis, dass die VM ohne offenes Terminal herunterfährt. Die System-Unit:
  `User=`/`Group=kephalaion`, `Environment=KEPHALAION_CONFIG=/etc/kephalaion/config.yaml`,
  `ExecStart=/usr/local/bin/kephalaion serve`, `StateDirectory=kephalaion`,
  `StateDirectoryMode=0700`, `Restart=on-failure`, `NoNewPrivileges`, `ProtectSystem=strict`,
  `ProtectHome`, `PrivateTmp`, `WantedBy=multi-user.target`. `status` zeigt den Dienst in einer
  Zeile — bei der globalen config die System-Unit (`systemctl show`, ohne root), ohne systemd
  „ohne systemd“, das den Exit-Code nicht ändert.
- **Label des LaunchAgent: `io.github.kephalaion`** — festgelegt am 2026-09-26 (Task 011).
  Kephalaion hat keine eigene Domain; das Label folgt dem Ort des Repositorys. Die plist liegt
  unter `~/Library/LaunchAgents/io.github.kephalaion.plist`, mit `RunAtLoad`, `KeepAlive` nur
  bei erfolglosem Ende (wie `Restart=on-failure`) und dem Log in
  `~/.local/state/kephalaion/serve.log`; geladen mit `launchctl bootstrap gui/<uid>`.
- **Logs:** unter Linux ins Journal (`journalctl --user -u kephalaion`, global
  `journalctl -u kephalaion`), keine eigene Logdatei; auf macOS die Datei oben.
- **Nach `upgrade`** startet ein laufender Dienst pro User neu, damit er das neue Binary
  benutzt; global gehört der Neustart zum Upgrade des Verwalters (`upgrade` nennt ihn nur).
  `make dev-install` startet den Dienst pro User ebenso neu.
- **macOS nur pro User.** Systembenutzer und LaunchDaemon sind dort umständlich; hier ist
  macOS nur selten zu testen. Die CI prüft es auf `macos-latest` — für ein öffentliches
  Repo kostenlos. Gebaut in Task 011 (Tests, `plutil -lint` des LaunchAgent, `install.sh`);
  seit 2026-09-26 auf Wunsch des Nutzers abgeschaltet (`if: false`), weil
  `TestBackgroundSync` dort fast immer an der Race-Condition scheitert (Task 013); später
  wieder einschalten.

**Upgrade — entschieden am 2026-09-26.**

- **Pro User** wie bisher `kephalaion upgrade`: atomar per Umbenennen. Das ist auf macOS
  nötig: Ein signiertes Binary, das an Ort und Stelle überschrieben wird, beendet macOS beim
  nächsten Start sofort.
- **Global kann ein User nicht upgraden, und er soll es nicht** — sonst tauschte jeder das
  Binary aller aus. Der Verwalter hebt die Version in Ansible an (Playbook, Neustart) oder
  ruft `sudo kephalaion upgrade && sudo systemctl restart kephalaion`.
- **Ob es ein Update gibt und wie es geht, sagt Kephalaion selbst** — auf der
  Kommandozeile (`kephalaion upgrade --check`, mit `--json` für k-playbook) und über MCP
  (in `whoami`): installierte und neueste Version, ob dieses Binary sich selbst ersetzen kann
  (Schreibrecht in seinem Verzeichnis) und der Weg — pro User `kephalaion upgrade`, global
  der Befehl für den Verwalter. Das Werkzeug meldet nur; ersetzen kann das Binary über MCP
  niemand.
- **Über MCP ohne Anfrage an GitHub je Aufruf.** Ohne Anmeldung erlaubt die GitHub-API 60
  Anfragen je Stunde und Adresse, geteilt von allen Usern eines Rechners. `serve` fragt
  höchstens einmal am Tag und merkt sich die Antwort; `upgrade --check` fragt direkt.
- **Die JSON-Felder — festgelegt am 2026-09-26 (Task 011):** `upgrade --check --json` und das
  Feld `update` in `whoami` haben dieselbe Struktur (`upgrade.Report`): `state` (`ok`,
  `unchecked`, `failed`), `error`, `checked_at`, `version`, `dev_build`, `latest`,
  `update_available`, `self_upgrade`, `method` (`self`, `explicit`, `admin`, `manual`),
  `command`, `hint`; Einzelheiten unter „Werkzeuge“, `whoami`, und in
  [`installation.md`](installation.md). Ob sich das Binary selbst ersetzen kann, prüft eine
  Probedatei im Verzeichnis, wie beim Ersetzen. Kann es das nicht und gilt die globale config
  (über die Suche oder `KEPHALAION_CONFIG`), ist der Weg der des Verwalters, sonst der
  allgemeine Hinweis. Die Exit-Codes von `--check` bleiben: 0, wenn GitHub geantwortet hat,
  1, wenn die Frage scheitert, 2 bei falschem Aufruf. Ohne Schreibrecht bricht `upgrade` vor
  dem Download ab und nennt den Weg.
- **k-playbook** bietet das Update in seiner Oberfläche an, wenn das Binary sich selbst
  ersetzen kann; sonst weist es nur auf die neue Version und den Weg hin.

**Doku für Menschen, Ansible und KI — entschieden am 2026-09-26, geschrieben in Task 011.**
[`installation.md`](installation.md) beschreibt beide Arten, Dienst und Upgrade, dazu einen Abschnitt „Für Automatisierung“ mit
den festen Angaben: URL-Schema der Assets, `SHA256SUMS`, Architekturen (`x86_64` → `amd64`,
`aarch64` → `arm64`), Pfade, Systembenutzer, die Schritte in ihrer Reihenfolge, das Upgrade.
Das README bringt beide Arten kurz und verweist dorthin. Weil `main` der Release-Stand ist,
beschreibt `https://raw.githubusercontent.com/kephalaion/kephalaion/main/docs/installation.md`
immer das neueste Release, mit dem Tag statt `main` eine feste Version. Diese URL nennt das
Ansible-Repo, das anderswo liegt, in seiner Anleitung — für Menschen wie für eine KI.

**Später: ein deb-Paket.** Gebaut im Release-Workflow aus denselben Binaries (etwa mit
nfpm), bringt es Systembenutzer, Unit und Neustart mit. Erst, wenn jemand ohne Ansible
global installieren will; der eigentliche Aufwand ist ein signiertes apt-Repository, ohne
das `apt upgrade` es nicht findet. Für macOS entspräche dem ein Homebrew-Tap.

## Datenmodell

**Entschieden am 2026-09-25: eine Datenbank je Hub**, alle Collections in denselben Tabellen,
die Collection ist eine Spalte. Collections werden alle gleich behandelt; unterschieden wird
nur beim Zugriff über Token und Scope. Mehrere Tabellen oder Datenbanken je Collection würden
den Abgleich zu mehreren Abfragen gegen mehrere Dateien machen. Auf dem Node gilt dasselbe je
Hub.

**Erster Entwurf:**

```sql
CREATE TABLE documents (
  id          TEXT PRIMARY KEY,        -- ULID, vom Hub vergeben
  collection  TEXT NOT NULL,
  name        TEXT NOT NULL,           -- Pfad, siehe unten
  content     TEXT,                    -- NULL, wenn gelöscht
  meta        TEXT,                    -- freies JSON, der Hub deutet es nicht
  deleted     INTEGER NOT NULL DEFAULT 0,
  revision    INTEGER NOT NULL,        -- Abgleich: alles mit revision > X
  created_at  INTEGER NOT NULL,        -- ms seit Epoche
  created_by  TEXT NOT NULL,           -- User des Accounts
  updated_at  INTEGER NOT NULL,
  updated_by  TEXT NOT NULL            -- User des Accounts
);
CREATE UNIQUE INDEX documents_name ON documents(collection, name) WHERE deleted = 0;
CREATE INDEX documents_revision ON documents(collection, revision);

CREATE TABLE actions (                 -- Protokoll, befristet
  at          INTEGER NOT NULL,
  account     TEXT NOT NULL,           -- der Account, nicht der User
  carrier     TEXT,                    -- welcher Node es gebracht hat
  action      TEXT NOT NULL,           -- create, update, rename, delete, rotate,
                                       -- collection.add, node.grant, config.import …
  document_id TEXT,
  subject     TEXT,                    -- Ziel ohne Dokument: Collection, Node,
                                       -- bei grant/revoke <node>:<collection>
  revision    INTEGER
);

-- Nur am Hub, gleichen sich nicht ab:
CREATE TABLE accounts (                -- seit Task 005; die Rechte stehen in SYSTEM:A:-Zeilen
  name          TEXT PRIMARY KEY,
  description   TEXT,
  token_hash    TEXT NOT NULL,         -- sha256(token), maßgeblich, auch gesperrt
  locked        INTEGER NOT NULL DEFAULT 0,
  locked_rights TEXT,                  -- gemerkte Rechte eines gesperrten Accounts (JSON)
  created_at    INTEGER NOT NULL,
  created_by    TEXT NOT NULL
);
CREATE TABLE principal_names (         -- seit Task 007; belegte Node- und Account-Namen
  name        TEXT PRIMARY KEY,        -- abgeleitet aus nodes und accounts, nicht im Export
  kind        TEXT NOT NULL            -- node oder account
);
CREATE TABLE collections (
  name        TEXT PRIMARY KEY,
  description TEXT,
  created_at  INTEGER NOT NULL,
  created_by  TEXT NOT NULL
);
CREATE TABLE nodes (
  name        TEXT PRIMARY KEY,        -- vom Admin vergeben
  description TEXT,
  token_hash  TEXT NOT NULL,           -- sha256(token)
  locked      INTEGER NOT NULL DEFAULT 0,
  created_at  INTEGER NOT NULL,
  created_by  TEXT NOT NULL
);
CREATE TABLE node_collections (        -- das Recht replicate
  node        TEXT NOT NULL REFERENCES nodes(name),
  collection  TEXT NOT NULL REFERENCES collections(name),
  PRIMARY KEY (node, collection)
);
```

In `node.db`, ebenfalls lokal:

```sql
CREATE TABLE hubs (
  name        TEXT PRIMARY KEY,        -- Alias, vom Node vergeben
  entry_id    TEXT NOT NULL UNIQUE,    -- Kennung des Eintrags, ULID, nie wiederkehrend
  node_name   TEXT NOT NULL,           -- Name des Nodes am Hub (nodes.name dort)
  transport   TEXT NOT NULL,           -- local, http, https, ssh
  address     TEXT,                    -- leer bei local
  token       TEXT,                    -- das eigene Token des Nodes bei diesem Hub
  ssh_key     TEXT,
  hub_id      TEXT                     -- Kopie; maßgeblich ist db_info der Replica
);
CREATE TABLE hub_collections (         -- was der Node von diesem Hub haben will
  hub         TEXT NOT NULL REFERENCES hubs(name),
  collection  TEXT NOT NULL,
  PRIMARY KEY (hub, collection)
);
CREATE TABLE hub_sync (                -- Stand des Abgleichs; abgeleitet, nicht im Export
  hub         TEXT PRIMARY KEY REFERENCES hubs(name),
  entry_id    TEXT NOT NULL,
  ok_at       INTEGER,                 -- letzter Erfolg
  error       TEXT,                    -- letzter Fehler, leer nach einem Erfolg
  error_kind  TEXT,                    -- seine Art, etwa unreachable
  error_at    INTEGER
);
```

- **Lokale Tabellen:** Einzelne Werte stehen in `settings` (Schlüssel, Wert); Listen mit
  Struktur bekommen eigene Tabellen. Nichts davon gleicht sich ab. `config export` und
  `config import` nehmen sie mit (Exportformat 4, `tables:` je Rolle), `db_info`, `actions`
  und das abgeleitete `principal_names` nicht. Änderungen an ihnen zählen keine Revision hoch — außer bei Accounts, deren
  Rechte in `SYSTEM:A:`-Zeilen stehen. Der Export nimmt je Account Beschreibung, gesperrt,
  Hash und die Rechte je Collection mit; der Import gleicht die `SYSTEM:A:`-Zeilen daran an —
  vorhandene ändern, fehlende werden Löschmarken, neue entstehen —, unter einer Revision in
  derselben Transaktion. Fehlt der Accounts-Teil in Format 4 oder ist er null (`accounts:`
  ohne Wert), bricht er ab — nur `accounts: []` leert. Ein Export vor Format 4 lässt die
  Accounts, wie sie sind, und darf keinen Accounts-Teil tragen, in keiner Form (auch nicht
  null oder `[]`); geprüft wird am YAML-Knoten, denn null ließe sich nach dem Decodieren nicht
  von „fehlt“ unterscheiden.
- **Namen** von Collections, Nodes, Accounts und Hub-Aliasen: `[a-z0-9][a-z0-9._-]{0,62}`,
  kein `:` (Adressen sind `<hub>:<collection>`), kein Präfix `system`; Accounts und Nodes
  heißen nicht `admin` (Task 005). `documents.collection` hat
  keinen Fremdschlüssel auf `collections`; eine Collection lässt sich aber nur entfernen,
  solange keine Zeile in `documents` sie nennt, kein Node sie abgleichen darf und kein
  gesperrter Account Rechte in ihr gemerkt hat. **Ausgenommen sind Löschmarken von
  `SYSTEM:A:`-Zeilen (Task 007):** Sie blockieren das Entfernen nicht und bleiben stehen —
  beim `hub collection rm` wie beim Import. Physisch entfernen ließen sie sich nicht: Ein Node
  verwirft eine Collection nur, wenn ein Abgleich sie als nicht erlaubt meldet. War er offline,
  während sie entfernt, gleichnamig neu angelegt und wieder erlaubt wurde, gleicht er mit altem
  `seit` weiter ab; ohne Löschmarke bliebe seine lebende `SYSTEM:A:`-Zeile mit altem Hash und
  alten Rechten stehen. So bekommt er die Marke — oder die Zeile, die ein `grant` in der neuen
  Collection wiederbelebt hat.
- **Namen von Accounts und Nodes sind am Hub gemeinsam eindeutig.** Das Protokoll nennt nur
  Namen (`account`, `carrier`); „laptop“ darf dort nicht zweierlei bedeuten. Der Hub prüft
  das beim Anlegen und beim Import über die Tabellen `accounts` und `nodes`, in beide
  Richtungen — das ergibt die lesbare Meldung. **Abgesichert ist es in der Datenbank (Task
  007):** Die Tabelle `principal_names` hat je Node und Account eine Zeile mit dem Namen als
  Primärschlüssel, geschrieben in derselben Transaktion wie Anlegen und Entfernen
  (`INSERT … ON CONFLICT (name) DO NOTHING`, dann die Zahl der Zeilen prüfen — ein Fehler
  bräche unter PostgreSQL die Transaktion ab). Ist der Name dort schon belegt, etwa von einer
  gleichzeitigen Transaktion, an der die Vorprüfung vorbeisah, kommt derselbe Fehler wie aus
  der Vorprüfung (`ErrExists`), keine 500. Die Tabelle ist abgeleitet: nicht im Export, und
  `config import` baut sie in seiner Transaktion aus `nodes` und `accounts` neu auf. Nach
  `hub account rm` ist der Name wieder frei; die Löschmarken bleiben, ein neuer Account
  gleichen Namens belebt sie mit neuer Revision wieder.
- **Ein Node-Token ist nie ein Client-Token.** Der Node prüft Clients nur gegen
  `SYSTEM:A:`-Zeilen; ein Node steht dort gar nicht.
- **Das Token des Nodes steht im Klartext in `node.db`**, denn der Node muss es vorzeigen.
  Das Verzeichnis hat `0700`, die Datei `0600`.
- **Die CLI am Hub handelt als `admin`** — Account und User heißen so. So steht es in
  `created_by` und im Protokoll, ohne Träger. Schreibt ein Account über einen Node, steht sein
  User in `created_by`/`updated_by`, Account und Node im Protokoll (Task 014).

- **Nur Text, kein Typ.** Solange nur Texte gespeichert werden, braucht es keine Typspalte.
- **Metadaten: ein freies Feld `meta` (JSON), vom Hub nicht gedeutet**, nur gespeichert und
  mit abgeglichen. Was hineingehört, entscheidet der nutzende Dienst — gedacht für Angaben der
  Maschine, die kein Mensch bearbeitet. SQLite kann JSON-Felder abfragen (`json_extract`) und
  über Ausdrücke indizieren, falls später gefiltert werden soll.
- **Das Frontmatter steht im Text — entschieden am 2026-09-27**, nicht in `meta`, nicht in
  einer eigenen Spalte, nicht in einer Datei daneben: So sieht und ändert man es in VS Code, es
  bleibt beim Kopieren und beim Export erhalten, und Dateien aus einem Repository (etwa die
  `SKILL.md` von k-playbook) kommen unverändert hinein. Dieselbe Angabe steht nicht an zwei
  Stellen. Der Hub deutet es nicht; der Node liest es auf Wunsch aus und liefert es strukturiert
  (`list` und `read` mit `frontmatter`, siehe „Werkzeuge“ → „Allgemein — lesen“).
- **Der Name ist ein Pfad — entschieden am 2026-09-25**, eindeutig je Collection. Früher
  stand hier „frei“; eine Verzeichnisstruktur macht aber Vieles einfacher: Verzeichnisse
  auflisten, fortlaufend nummerierte Namen, das Ersetzen eines ganzen Verzeichnisses, den
  Export nach Markdown und die Übernahme der Ablage von k-playbook. Regeln, die der Hub beim
  Schreiben prüft:
  - relativ, Segmente durch `/` getrennt; kein `/` am Anfang oder Ende, kein leeres Segment,
    kein `.` oder `..`;
  - UTF-8 ohne Steuerzeichen und ohne `\`; Groß- und Kleinschreibung zählt; Länge begrenzt
    (etwa 1024 Bytes gesamt, 255 je Segment);
  - **Verzeichnisse gibt es nur implizit**, als Präfix vorhandener Namen — kein eigener
    Eintrag, kein leeres Verzeichnis;
  - wie im Dateisystem kann ein Name nicht zugleich Datei und Verzeichnis sein: Gibt es
    `tasks`, kann es kein `tasks/001-a.md` geben, und umgekehrt.
  Was ein Pfad inhaltlich bedeutet, entscheidet weiter der nutzende Dienst.
- **Die `id` bleibt**, obwohl Collection und Name eindeutig sind: Ohne sie wäre Umbenennen für
  jeden Node „gelöscht und neu“, Verweise brächen, der Index würde neu gebaut. Mit ihr ist
  Umbenennen eine gewöhnliche Änderung — und der Hub darf später umsortieren.
- **Gelöschtes bleibt als Löschmarke** stehen, ohne Inhalt, mit neuer Revision. Eine
  verschwundene Zeile erführe kein Node. Der eindeutige Index gilt nur für nicht Gelöschtes,
  damit ein Name wieder vergeben werden kann. Ablösen kommt später auf demselben Weg.
- **Auf dem Node zählt allein die `id`.** Die Eindeutigkeit der Namen sichert der Hub; die
  Replica hat keinen eindeutigen Index auf `(collection, name)`. Ein Delta liefert je Dokument
  nur den letzten Stand, und dabei kann ein Name vorübergehend doppelt vergeben sein: A wird
  von `x` in `y` umbenannt, B neu als `x` angelegt, danach A geändert — das Delta bringt B vor
  A, und B hieße `x`, solange A dort noch `x` heißt. Ein eindeutiger Index ließe den Abgleich
  scheitern.
- **Wer erzeugt und geändert hat** steht zweifach: `created_by`/`updated_by` in der Tabelle
  beantworten „wer war zuletzt dran“ ohne Umweg, mit dem User; das Protokoll `actions` den
  Rest — auch welcher Account und welcher Node —, solange es zurückreicht. Es ist befristet; Einträge über echtes Löschen durch den Admin bleiben
  dauerhaft.
- **Jede Datenbank — des Hubs, `node.db` und jede Replica — hat zwei weitere Tabellen**,
  beide `(key TEXT PRIMARY KEY, value TEXT NOT NULL)`:
  - `db_info` beschreibt die Datenbank selbst: Schemafassung (`schema_version`), Rolle
    (`role`, `hub`, `node` oder `replica`), Anlagezeit (`created_at`), am Hub die Revision
    (`revision`, siehe unten), in der Replica die `hub_id` und die `entry_id` des Hub-Eintrags.
    Wer eine Datenbank öffnet, prüft
    Rolle und Schemafassung; eine Hub-Datenbank als Node zu öffnen oder umgekehrt ist ein
    Fehler. Die Rolle `replica` ist keine Rolle der config — eine Replica gehört zum Node —,
    sie hält nur Replica und `node.db` auseinander.
  - `settings` hält die Einstellungen der Rolle — alles, was nicht in der config steht.
    `config export` sichert sie, `config import` schreibt sie je Rolle in einer Transaktion
    zurück.
- **Schemaänderungen:** SQLite kann `ADD COLUMN`, `RENAME COLUMN` (seit 3.25) und `DROP
  COLUMN` (seit 3.35). Typ oder Bedingung einer Spalte ändert man durch Neubau der Tabelle in
  einer Transaktion. Es braucht eine Schemafassung und Migrationen.
  **Befristete Abweichung, entschieden am 2026-09-25:** Solange es keine Daten gibt, die
  bleiben müssen, gibt es keine Migrationen. Die Schemafassung steht in `db_info`; passt sie
  nicht zum Binary, bricht jeder Zugriff mit einer Meldung ab, und die Datenbank wird neu
  angelegt. Die Einstellungen (`settings` und config) lassen sich über `config export` —
  mit dem bisherigen Binary — und `config import` retten, die Inhalte nicht. Ein Rahmen für
  Migrationen entsteht, sobald es Daten gibt, die bleiben müssen.
- **History (angedacht, zurückgestellt):** eine zweite Tabelle `document_versions (id,
  revision, content, …)`, in die vor jeder Änderung die alte Fassung kopiert wird, begrenzt
  auf einige Versionen.

**Abgleich — entschieden am 2026-09-25, so einfach wie möglich:**

- **Eine Folge je Hub, eine Revision je Collection.** Die Revision zählt über alle
  Collections des Hubs fort. Der Node merkt sich je Collection die letzte — meist sind
  alle gleich, aber eine neu hinzugekommene Collection beginnt bei 0.
- **Die Revision ist ein Zähler in einer Tabellenzeile** — der Zeile `revision` in
  `db_info` —, erhöht innerhalb der schreibenden Transaktion — keine `SEQUENCE` und kein
  Autoincrement. Gelesen, hochgezählt und geschrieben wird im Code, in derselben
  Transaktion; die Umwandlung des Textwerts in SQL wäre je Dialekt verschieden. Eine Sequenz in PostgreSQL vergibt
  Nummern beim Ziehen, nicht beim Commit: Zieht A die 11 und B die 12, committet B zuerst und
  gleicht ein Node dann ab, merkt er sich 12 und sieht A nie. Die Sperre auf der Zeile reiht
  die Schreiber hintereinander; so gilt in SQLite und PostgreSQL dasselbe. Umgesetzt: In
  PostgreSQL sperrt ein `UPDATE db_info SET value = value` die Zeile, bevor sie gelesen
  wird; in SQLite beginnt jede Transaktion als `BEGIN IMMEDIATE` und hält die Sperre von
  Anfang an.
- **Die Revision sperrt zu spät für Accounts (Task 007).** Sie sperrt erst beim ersten
  Schreiben einer Zeile; unter PostgreSQL (READ COMMITTED) läse ein `grant` oder `lock` davor
  den Hash aus `accounts`, ein gleichzeitiger `rotate` ersetzte ihn, und danach schriebe der
  erste die Zeilen mit dem alten Hash — der Node lehnte das neue Token ab. Deshalb sperrt jeder
  Schreibvorgang an einem Account **zuerst seine Zeile in `accounts`** (`UPDATE accounts SET
  name = name WHERE name = $1`, wie bei der Revision; `SELECT … FOR UPDATE` versteht SQLite
  nicht) und liest erst danach — ebenso `create`, `write`, `delete` und `rename` über einen
  Node (Task 014), bevor sie Hash, Sperre und User lesen. `rotate` beginnt mit dem bedingten
  Schreiben (`… WHERE name = $1 AND token_hash = <alter Hash> AND locked = 0`) — das ist
  zugleich die Sperre; trifft es keine Zeile, scheitert es ohne weitere Änderung. Der Import
  sperrt vor dem Lesen alle Zeilen von `accounts`.
- **Der Hub hat eine Identität.** `hub init` vergibt eine `hub_id` (ULID, in `db_info`); jede
  Antwort an einen Node trägt sie, der Node speichert sie in `db_info` seiner Replica —
  maßgeblich — und danach als Kopie in `hubs`. Weicht sie ab — etwa
  weil die Datenbank des Hubs neu angelegt wurde und die Revision wieder bei 0 beginnt —,
  verwirft der Node die Replica und gleicht von vorn ab. Sonst fragte er „alles seit 1200“,
  bekäme nichts, und die Replica wäre still veraltet.
- **Hub aus einer Sicherung.** Jede Antwort trägt auch die Revision H des Hubs. Liegt ein
  `seit` des Nodes über H, wurde der Hub mit gleicher `hub_id` zurückgespielt; der Node
  behandelt das wie einen Wechsel der `hub_id`. Das greift nur, bis der Hub wieder darüber
  hinaus geschrieben hat. **Grenze, festgehalten in [`vertrag.md`](vertrag.md):** Ein aus einer
  Sicherung zurückgespielter Hub braucht eine neue `hub_id`; bis es dafür ein Kommando gibt,
  verwirft der Node die Replica selbst (`node hub rm` und `node hub add`).
- **Eine Abfrage für alle Collections.** Der Node schickt eine Liste von Paaren (Collection,
  seit); der Hub fragt einmal ab, nach Revision sortiert, jede Collection über den Index
  `(collection, revision)`:
  `WHERE (collection='a' AND revision > 1200) OR (collection='b' AND revision > 0)`.
- **In Seiten, nicht in einem Rutsch.** Die Abfrage liefert eine begrenzte Anzahl und wird
  wiederholt, bis nichts mehr kommt. Jede Seite endet bei einer Revision R, an einer
  Revisionsgrenze: Ein Schreibvorgang kommt ganz oder gar nicht. Der Node wendet jede
  Seite in einer Transaktion an und setzt die Revision jeder angefragten Collection auf
  max(seit, R) — nie zurück, denn die Revision ist global, und eine Collection, die schon
  weiter war, fiele sonst zurück. Den Stand je Collection hält die Replica in `sync_state`.
  Ein abgebrochener Abgleich setzt dort fort, wo er stand; der erste vollständige Abgleich ist
  nur eine lange Folge von Seiten.
- **Lesestand ohne Transaktion, `bis` auf der letzten Seite.** Der Hub liest H zuerst und
  liefert nur Zeilen mit `revision ≤ H`; eine Lese-Transaktion wäre in SQLite IMMEDIATE. Auf
  der letzten Seite — auch einer leeren — ist R (`bis`, `until`) gleich H: Der Node hat dann
  alles bis H, auch in Collections, in denen sich nichts geändert hat.
- **Format:** JSON über HTTP, komprimiert (gzip). Ein Datenstrom zeilenweiser JSON-Objekte
  wäre die nächste Stufe, falls Seiten zu groß werden.
- **In `documents` steht nur, was sich abgleichen muss.** Das ist der einzige Grund für
  `SYSTEM:`-Zeilen. Was nur der Hub selbst braucht, steht in eigenen Tabellen des Hubs; was
  nur der Node braucht (seine Hubs, seine Einstellungen), in `node.db`.
- **Accounts sind Zeilen in `documents`** — damit gleichen sie sich ohne eigenen Mechanismus
  ab. Was nur der Hub braucht, steht seit Task 005 in einer eigenen Tabelle `accounts`
  (Beschreibung, gesperrt, angelegt, die gemerkten Rechte eines gesperrten Accounts) — früher
  hieß es hier „keine eigene Tabelle“. **Den Hash führt `accounts` maßgeblich und immer**, auch
  gesperrt und ohne Collection; die Zeilen tragen eine Kopie, und jede Änderung schreibt beides
  in derselben Transaktion. Je Account und Collection eine Zeile:
  - `name` = `SYSTEM:A:<account>`, also der Account-Name; der eindeutige Index auf
    `(collection, name)` sichert die Eindeutigkeit.
  - `content` = Hash des Tokens, der User und die Rechte in *dieser* Collection, etwa
    `{"hash": "…", "user": "kleist", "rights": {"write": true, "supersede": false}}`; `read`
    ergibt sich aus der Zeile selbst (Form in [`vertrag.md`](vertrag.md), „Account-Zeilen“;
    gebaut mit Task 006; die Scopes `vendor/<name>` als Liste `vendor` in `rights`, nur wenn es
    welche gibt, Task 016). `accounts` führt den User maßgeblich in der Spalte `"user"` —
    in Anführungszeichen, weil `user` in PostgreSQL reserviert ist — mit Index `accounts_user`.
  - Der User steht in jeder Zeile des Accounts; ändert der Admin ihn, ändern sich alle Zeilen
    in einer Transaktion, wie bei `rotate`. Vorhandene Dokumente behalten ihren User. Alle
    Accounts eines Users findet der Admin über die Daten, die nur der Hub über Accounts führt
    (mit Task 005 die Tabelle `accounts`, dort mit Index auf dem User) — nicht über JSON in
    `documents`.
  - Ein Node bekommt mit dem Abgleich genau die Zeilen seiner Collections — der beschränkte
    Auszug fällt von selbst heraus; kein Node erfährt, dass ein Account noch andere
    Collections hat.
  - Sperren und Entziehen laufen über Löschmarken. `rotate` ändert den Content aller Zeilen
    eines Accounts in einer Transaktion.
- **Der Präfix `SYSTEM:` ist reserviert** und die einzige Ausnahme von „der Name ist ein Pfad“.
  Am Hub darf kein Account einen solchen Namen anlegen, ändern oder löschen — nur der Hub
  selbst. Am Node kommen diese Zeilen nie in den Suchindex und nie in eine Antwort von
  `read` oder `list`.
- **Ein Index für die Account-Zeilen.** Für eine Suche über alle erlaubten Collections
  braucht der Node alle Zeilen eines Accounts, quer über die Collections; der Index auf
  `(collection, name)` hilft dabei nicht. Dafür:
  `CREATE INDEX documents_system ON documents(name) WHERE name LIKE 'SYSTEM:%';`
  Der Node fragt je Anfrage die Datenbank, ohne Cache. Die Abfrage nennt die Bedingung
  `name LIKE 'SYSTEM:%'` wörtlich mit, sonst benutzt SQLite den Teilindex nicht; genau grenzt
  `name = …` ein. Der Hub hat denselben Index.

## Suche über mehrere Collections

Collections unterscheiden sich in Frische, in Zuständigkeit und darin, was ein Treffer
überhaupt bedeutet. Ein gemeinsamer Rang wäre deshalb keine Aussage.

Die Werkzeuge bekommen ein Feld für die Collections (eine Adresse `<hub>:<collection>`, oder
alle erlaubten) und liefern getrennte Trefferlisten. Der Rang zählt je Liste ab 1, wie heute. Ein Punktwert
bleibt aus dem Vertrag heraus.

## Werkzeuge

**Sammelstelle, damit keines vergessen wird** — noch kein Vertrag. Gemeint sind die
MCP-Werkzeuge des Nodes für Clients; die Kommandozeile und der Vertrag zwischen Node und Hub
sind eigene Listen.

**Eine Oberfläche in VS Code** über dieselben Werkzeuge — Collections als Ordner im Explorer —
steht in [`vscode.md`](vscode.md); gebaut sind Lesen und, seit Task 014, Schreiben.

**Allgemein und für k-playbook.** Kephalaion ist ein allgemeiner Store. Trotzdem braucht es
Werkzeuge eigens für k-playbook, dem ersten und wichtigsten Nutzer. Grundsatz: Ein
Werkzeug für k-playbook ist eine bequeme Form über den allgemeinen Vorgängen, kein Sonderweg
im Hub. Wo die Grenze liegt und ob etwas allgemein taugt, wird je Werkzeug entschieden.

### Allgemein — lesen

**Adresse — entschieden am 2026-09-26:** Werkzeuge nennen eine Collection als
`<hub>:<collection>` (`team:wissen`), wie `whoami` sie ausgibt. Der Hub-Teil darf fehlen,
wenn der Client nur bei einem Hub angemeldet ist. Innerhalb der Collection steht `path` bzw.
`name`. Die URI der Erweiterung für VS Code (`keph://team/wissen/…`) bildet sich eins zu eins
darauf ab.

| Werkzeug | Zweck | Anmerkungen |
|---|---|---|
| `search` | Volltextsuche | Collections wählbar; getrennte Trefferlisten je Collection, Rang ab 1, kein Punktwert |
| `read` | ein Dokument lesen | per Name oder `id`; wahlweise ein Abschnitt; mit `content: false` nur die Angaben dazu, siehe unten. Gebaut in Task 009, ohne Abschnitte |
| `list` | Inhalt eines Verzeichnisses | siehe unten. Gebaut in Task 009 |
| `changes` | was sich seit einer Revision oder einem Zeitpunkt geändert hat | siehe unten; neu am 2026-09-26. Gebaut in Task 009 |
| `whoami` | Version, Hubs, Anmeldung, eigener Account, lesbare Collections, Stand des Abgleichs | siehe unten; kein eigenes `status`. Gebaut wie unten (Task 008) |

**`list` — neu aufgenommen am 2026-09-25**, um etwa das Neueste zu finden:

- `path`: das Verzeichnis; wahlweise mit Unterverzeichnissen;
- `sort`: `name`, `created` oder `updated`; `order`: auf- oder absteigend;
- `limit`: Anzahl der Ergebnisse, dazu ein Cursor zum Weiterblättern;
- `mask`: wahlweise eine Maske auf das letzte Segment, als Glob (`*.md`, `0*-*.md`), nicht
  als regulärer Ausdruck — einfacher für Aufrufer und in Go ausgewertet, unabhängig von der
  Datenbank. `*` geht nicht über `/` hinweg;
- Antwort je Eintrag: Name, `id`, angelegt und geändert (wann, von wem), Revision.
- **Verzeichnisse als Einträge (2026-09-26):** Ohne Unterverzeichnisse liefert `list` auch
  die Verzeichnisse der nächsten Ebene als eigene Einträge (Art `directory`, nur der Name),
  abgeleitet aus den Namen darunter. Sonst müsste ein Aufrufer, der blättert, alles
  rekursiv holen und die Ordner selbst bilden — die KI wie die Erweiterung für VS Code.
  `list` auf die Wurzel eines Hubs liefert die lesbaren Collections.

**`read` mit `content: false` — 2026-09-26**, statt eines eigenen Werkzeugs `stat`: Name →
Dokument, Verzeichnis oder nichts; dazu `id`, Revision, angelegt und geändert, Größe,
schreibbar ja/nein. Billig, weil ohne Inhalt; die KI fragt so „gibt es das, wie alt ist
es“, die Erweiterung für VS Code beantwortet damit `stat`, das VS Code sehr oft aufruft.

**`changes` — neu am 2026-09-26:** was sich in den lesbaren Collections geändert hat — einer
Collection, eines Hubs (`<hub>:`) oder ohne `collection` aller angemeldeten Hubs —, seit der
letzten Abfrage oder seit einem Zeitpunkt.

- `cursor` aus der letzten Antwort oder `since` (Zeitpunkt); ohne beides nur der Cursor für
  „ab jetzt“. Wahlweise `collection` und `path` als Präfix, `limit`;
- Antwort: je Dokument, das sich seitdem geändert hat, der aktuelle Stand — Name, `id`,
  Revision, gelöscht ja/nein, geändert (wann, von wem); Löschmarken zählen mit. Dazu der
  Cursor, mit dem der nächste Aufruf weiterfragt. Ein Dokument, das sich mehrmals geändert
  hat, erscheint einmal. Der Cursor trägt den Stand je Collection, weil Collections einzeln
  abgleichen und eine nachhinkende sonst übersprungen würde; ist die Replica neu angelegt
  worden, meldet `changes` das, und der Aufrufer liest neu mit `list`;
- **ohne alten Namen (2026-09-26):** Ein Umbenennen erkennt der Aufrufer an der `id`, die er
  aus `list` oder `read` kennt; einen Namen, den er nie gesehen hat, muss er auch nicht
  entfernen. So braucht die Replica kein Protokoll der Änderungen. Fehlt der alte Name
  später doch, lässt er sich nachrüsten;
- beantwortet aus der Replica, ohne Netz. Die KI fragt nach Zeit („was ist neu seit
  gestern“), die Erweiterung für VS Code nach Revision, in kurzen Abständen, und löst damit
  `onDidChangeFile` aus. Lückenlos ist nur das Weiterfragen mit dem Stand der letzten
  Antwort; ein Zeitpunkt ist der des Hubs beim Schreiben, nicht der des Abgleichs.
  Benachrichtigungen von MCP (`resources/subscribe`) brauchten eine Sitzung, die der Node
  bewusst nicht führt (siehe „Kommunikation“).

**Gebaut in Task 009 (2026-09-26): `list`, `read`, `changes`.** Alle drei lesen nur aus der
Replica, ohne Netz, und setzen auf dieselbe Anmeldung wie `whoami` auf (`mcpnode.Authenticate`,
je Aufruf, ohne Cache). Lesbar ist eine Collection, wenn der Client an ihrem Hub gültig
angemeldet ist und die Replica für seinen Account dort eine lebende `SYSTEM:A:`-Zeile hat — es
zählen also nur Collections, die der Node führt. Alles andere ergibt dieselbe Meldung
„`<hub>:<collection>` nicht lesbar“, gleich ob die Collection existiert. Fehlt die Replica eines
Hubs, meldet ein Aufruf mit Adresse „noch nie abgeglichen“, auch ohne gültige Anmeldung — wie
`never_synced` in `whoami`. Eine Replica, die sich nicht lesen lässt, betrifft nur ihren Hub:
Mit Adresse die Meldung „Hub `<hub>`: Replica nicht lesbar“, ohne Pfad; über alle Hubs steht
er in `unreadable_hubs`, und `changes` behält seinen Stand im Cursor. Ein abgebrochener
Aufruf ist ein Fehler der Anfrage. Festlegungen:

- **Adresse ohne Hub-Teil:** der eine Hub mit gültiger Anmeldung; ohne gültige Anmeldung und
  mit genau einem Hub-Eintrag dieser. Sonst nennt der Fehler die Hubs mit gültiger Anmeldung.
  `read` per `id` braucht `collection` nur bei mehreren Hubs, dann `<hub>:` oder die
  Collection.
- **`limit`:** Standard 100, höchstens 1000; mehr wird gekürzt, nicht abgelehnt. Gilt für
  `list` und `changes`.
- **Cursor:** JSON in base64url, für den Client undurchsichtig, ohne Geheimnisse; der Node
  prüft bei jedem Aufruf Anmeldung und Recht neu, ein veränderter Cursor öffnet also nichts.
  Er ist an die Anfrage gebunden (bei `list` alle Angaben außer `limit` und `frontmatter`,
  bei `changes` `collection` und `path`); passt er nicht, ist das ein Fehler.
- **`list`:** Namen sind voll, auch die der Verzeichnisse (`2026/09`, nicht `09`). Ohne
  `recursive` kommen zuerst die Verzeichnisse der nächsten Ebene nach Name, dann die
  Dokumente in der verlangten Ordnung; `sort` und `mask` gelten nur für Dokumente. Geblättert
  wird über die Stelle nach dem letzten Eintrag (Wert der Sortierung und Name) — ohne Lücke und
  Doppel bei unveränderter Replica; ändert sie sich dazwischen, deckt das `changes`. Die Liste
  der Collections blättert ebenso, nach Adresse. Tragen zwei lebende Zeilen denselben Namen
  (Umbenennungen, die in beliebiger Reihenfolge ankommen), gilt die jüngste.
- **`read`:** Der Inhalt ist der Text des Ergebnisses, die Angaben die Struktur daneben; mit
  `content: false` steht die Struktur auch im Text. Größe in Bytes. `writable` folgt je Name
  der Regel des Hubs (`contract.Rights.Writable`, Task 016): `write` auf der Collection, unter
  `vendor/<name>/` der Scope `vendor/<name>`, direkt in `vendor/` nie; bei Verzeichnissen und
  der Wurzel, ob darunter etwas angelegt werden dürfte. Per `id` sind eine Löschmarke, eine
  unbekannte `id` und ein Dokument einer nicht lesbaren Collection gleichermaßen `none`.
- **Zeiten** in RFC 3339, UTC, auf Millisekunden (`2026-09-26T10:00:00.000Z`), so genau, wie
  der Hub sie führt; `whoami` bleibt bei Sekunden.
- **`changes`:** Der Cursor trägt je Hub die **`generation`** der Replica — eine ULID in ihrem
  `db_info`, die `Create` vergibt und jedes Leeren in derselben Transaktion ersetzt
  (Schemafassung der Replica 4) — und je Collection die Revision, bis zu der der Aufrufer alles
  hat; endet eine Seite mitten in einer Revision, dazu die letzte `id`. Gelesen wird je
  Collection bis zu ihrem Stand in `sync_state`, nach Revision und `id`. Passt die `generation`
  nicht mehr, meldet `changes` für diesen Hub `reset` und setzt seinen Stand auf jetzt. Eine
  Collection, die neu lesbar ist, liefert alles; eine, die weggefallen ist, steht einmal in
  `dropped`. Der Stand einer Collection geht nie zurück. Mit `since` beginnt jede Collection
  bei der ersten Zeile, die der Hub zu oder nach dem Zeitpunkt geschrieben hat; die Antwort
  trägt danach einen gewöhnlichen Cursor.
- **Grenze von `path` in `changes`:** Wer ein Dokument aus dem Verzeichnis heraus umbenennt,
  erscheint dort nicht mehr — `changes` kennt keinen alten Namen. Lückenlos über Umbenennungen
  hinweg ist nur `changes` ohne `path`.

**Frontmatter in `list` und `read` — entschieden am 2026-09-27, gebaut in Task 015.**
Damit die KI eine Übersicht — etwa alle Skills mit ihrer Beschreibung — in einem Aufruf
bekommt, statt jede Datei zu lesen (`list` mit `recursive`, `mask: "SKILL.md"` und
`frontmatter`):

- **Parameter `frontmatter`** (Standard aus) bei `list` und `read`. Mit ihm trägt jeder passende
  Eintrag das Feld `frontmatter`, ein JSON-Objekt, oder `frontmatter_error` mit kurzem Grund;
  ohne ihn bleiben die Antworten, wie sie sind.
- **Nur `.md`-Dateien** (Endung ohne Unterscheidung von Groß- und Kleinschreibung). Frontmatter
  ist ein Block ganz am Anfang des Inhalts: eine Zeile genau `---`, darin YAML, bis zur nächsten
  Zeile genau `---` (UTF-8-BOM davor und `\r\n` erlaubt). Steht er nicht am Anfang, gibt es
  keines. Andere Schreibweisen (TOML, JSON) nicht. Die Datei bleibt eine gewöhnliche
  Markdown-Datei; YAML ist nur die Schreibweise im Block, die Antwort ist JSON.
- **Verzeichnisse und Collections** bekommen das Frontmatter ihrer `README.md` — ein Verzeichnis
  das von `<verzeichnis>/README.md`, eine Collection das von `README.md` auf ihrer obersten
  Ebene. Ein README beschreibt ohnehin sein Verzeichnis und ist für Menschen sichtbar; gibt es
  keines oder hat es kein Frontmatter, fehlt das Feld; ist sein Frontmatter fehlerhaft, tragen
  Verzeichnis bzw. Collection `frontmatter_error` — wie das Dokument `README.md` selbst. Die
  `README.md` selbst erscheint weiter als Dokument.
- **Fehler machen nichts unlesbar:** nicht geschlossen, ungültiges YAML, oben kein Objekt, ein
  Schlüssel, der kein Text ist, oder länger als 64 KiB — der Eintrag kommt mit
  `frontmatter_error`. Gelesen wird nur der Anfang des Inhalts (64 KiB je Eintrag; ein Gesamtmaß
  für eine Seite gibt es nicht, die Größe steuert die KI über `limit`) und nur für die Einträge
  der Seite, nach dem Blättern; `frontmatter` gehört nicht zum Cursor.
- Mit `recursive: true` gibt es keine Einträge für Verzeichnisse und damit kein Frontmatter von
  Verzeichnissen; die README-Dateien erscheinen als Dokumente. `read` liefert den Inhalt
  unverändert, einschließlich Frontmatter — per `name` wie per `id`, bei einem Verzeichnis und
  der Wurzel der Collection das der `README.md`, bei der Wurzel eines Hubs keines; mit
  `content: false` nur das Frontmatter ohne Text.
- **Gebaut (Task 015):** Paket `internal/frontmatter` (neutral, ohne Hub, Node und MCP; die
  Suche soll es mitnutzen) liest den Block über den Knotenbaum von yaml.v3, damit Zeitangaben
  der geschriebene Text bleiben (`2026-09-27` wird nicht `2026-09-27T00:00:00Z`) und Schlüssel
  am Tag geprüft werden; ein leerer Block ist `{}`. Die Replica liest mit `HeadByName`/`HeadByID`
  nur die ersten 64 KiB in Bytes (`substr(CAST(content AS BLOB) …)`).

**`whoami` — festgelegt am 2026-09-26.** Ein Werkzeug für „wer bin ich“ und „wie steht der
Node“; ein eigenes `status` brächte kaum mehr. Die Antwort:

| Feld | Inhalt | auch ohne Anmeldung |
|---|---|---|
| `version` | Version des Nodes; die Erweiterung für VS Code vergleicht sie mit ihrer | ja |
| `update` | neueste Version, ob es ein Update gibt, ob sich das Binary selbst ersetzen kann, Weg, Zeitpunkt der Prüfung — aus der zwischengespeicherten Antwort von `serve` | ja |
| je Hub: `hub` | Alias — **alle** Hub-Einträge des Nodes, nicht nur die mit Header-Paar | ja |
| `login` | `ok`, `invalid` (geschickt, aber ungültig) oder `missing` (nichts geschickt oder Replica nicht lesbar) | ja |
| `node` | Name dieses Nodes am Hub | ja |
| `sync` | letzter erfolgreicher Abgleich (Zeit), Revision der Replica, letzter Fehler mit Zeit — leer, wenn der letzte Versuch gelang | ja |
| `account`, `user` | Account und User | nein |
| `collections` | Collection, Adresse (`<hub>:<collection>`), Rechte | nein |
| `unknown_hubs` | Aliase aus Headern, zu denen der Node keinen Hub-Eintrag hat — nur der Alias | ja |

Gebaut in Task 008. `sync` trägt `last_success`, `revision`, `last_error` und
`last_error_at` (Zeiten in RFC 3339, UTC); `revision` ist der Stand, bis zu dem alle
Collections der Replica abgeglichen sind. Ohne Replica fehlt `revision`, und
`never_synced` ist gesetzt. `last_error` ist die Art des Fehlers als kurzer Satz („Hub nicht
erreichbar“), nicht die Meldung — die kann die Adresse nennen. Fehlt die Replica und kam ein
Header-Paar, ist `login` `invalid`, auch mit richtigen Zugangsdaten; den Grund erkennen Client
und Erweiterung an `sync`. Ein halbes Header-Paar ist `invalid`. Der Textteil nennt die
Version und je Hub eine Zeile.

**`update` — gebaut in Task 011 (2026-09-26).** Dieselbe Struktur wie `kephalaion upgrade
--check --json`, aus derselben Funktion (`upgrade.Report`): `state` (`ok`, `unchecked` vor der
ersten Prüfung, `failed` nach einem Fehler, dann mit `error`), `checked_at` (RFC 3339, UTC),
`version`, `dev_build`, `latest`, `update_available` (bei einem dev build nie), `self_upgrade`,
`method` (`self`, `explicit` für einen dev build, `admin` für die globale Installation,
`manual`), `command` und `hint`. `latest` und `update_available` gibt es nur bei `ok`; der Weg
steht immer darin. `serve` fragt als Node beim Start und danach höchstens einmal am Tag, nach
einem Fehler frühestens nach einer Stunde, in einer eigenen Goroutine, die mit `serve` endet;
die Antwort bleibt im Speicher. Der Weg gilt aus Sicht des `serve`-Prozesses — global also
der des Verwalters. `kephalaion node whoami` hat dieselbe Struktur, fragt aber wie `upgrade
--check` direkt. Der Textteil nennt das Update in einer Zeile nach der Version.

**Replica nicht lesbar — entschieden am 2026-09-26, gebaut in Task 012.** Lässt sich die
Replica eines Hubs nicht öffnen oder lesen — alte Schemafassung, ohne `entry_id`, beschädigt,
jeder Fehler außer einer fehlenden Datei und einem abgebrochenen ctx —, betrifft das nur
diesen Hub; die übrigen erscheinen vollständig. `login` ist dann `missing`, auch wenn ein
Header-Paar kam: Ohne lesbare Replica gibt es nichts, wogegen der Node prüfen könnte. Anders
als bei einer **fehlenden** Replica — dort macht ein Header-Paar `invalid`, und `sync` sagt
„noch nie abgeglichen“. In `sync` fehlt `revision`; `last_error` ist der feste Satz „Replica
nicht lesbar“, ohne Pfad und ohne Meldung, und ersetzt einen Fehler aus `hub_sync`;
`last_error_at` bleibt leer — die Replica hat keinen Zeitstempel, gegen den sich ein Fehler
aus `hub_sync` vergleichen ließe. `last_success` kommt weiter aus `hub_sync`, `never_synced`
ist nur gesetzt, wenn dort kein gelungener Abgleich steht. Die volle Meldung, samt Pfad, geht
ins Log von `serve` bzw. nach stderr von `node whoami`; `status` zeigt sie wie bisher je Hub.
Ein Fehler von `node.db` bleibt ein Fehler der ganzen Anfrage. Der nächste Abgleich verwirft
eine solche Replica, wenn sie eindeutig unlesbar ist, und legt sie neu an (siehe „Im
Hintergrund“).

Nie in der Antwort: Token, Hash, Adresse und Transport des Hubs, `hub_id`. Dass alle Hubs
erscheinen, ist unbedenklich: Der Node lauscht nur auf Loopback, die Prüfung von Host und
Origin hält Browser fern. `invalid` unterscheidet weiterhin nicht zwischen unbekanntem
Account, falschem Token und gesperrt.

**Dasselbe auf der Kommandozeile:** `kephalaion node whoami` listet die Accounts, die der
Node aus seinen Replicas kennt; `kephalaion node whoami <account>` zeigt, was `whoami` diesem
Account antworten würde. Ohne Token — wer die CLI aufruft, kann die Datenbanken des Nodes
ohnehin lesen. Ob ein Token gilt, prüft weiterhin `node account check`. Gebaut in Task 008:
Ohne Account nennt es Version, je Hub Node-Name und Stand und die Accounts aus den lebenden
`SYSTEM:A:`-Zeilen mit User, Collections und Rechten; mit Account ist `login` `ok`, wo er
lebende Zeilen hat, sonst `missing`. `--hub <alias>` grenzt ein, `--json` gibt die Struktur
des Werkzeugs aus — aus derselben Funktion. Einen Hub mit unlesbarer Replica zeigt die Liste
mit „Replica nicht lesbar“ und ohne Accounts.

**Keine Werkzeuge eigens für VS Code** (2026-09-26): Was die Erweiterung braucht —
`whoami`, `list`, `read`, `changes`, zum Schreiben die Werkzeuge unten —, taugt auch für
die KI. Ein eigenes Profil oder eine eigene Schnittstelle neben MCP entfällt; die
Erweiterung ist ein Client wie jeder andere ([`vscode.md`](vscode.md)).

### Allgemein — schreiben (Stufe 2 und 3)

| Werkzeug | Zweck | Anmerkungen |
|---|---|---|
| `create` | Dokument anlegen | scheitert, wenn der Name vergeben ist (eigener Fehlercode). Gebaut in Task 014 |
| `create_numbered` | Dokument mit fortlaufender Nummer anlegen | siehe „Zwei Arten von Eingaben“; liefert den erzeugten Namen |
| `write` | Dokument ersetzen | wahlweise mit der Revision, auf der es beruht. Gebaut in Task 014 |
| `append` | an ein Dokument anhängen | der Hub serialisiert |
| `replace_section` | einen Abschnitt ersetzen | Abschnitt über die Zerlegung, Anker |
| `rename` | umbenennen, verschieben | Änderung am Namen, `id` bleibt; auch ein Verzeichnis. Gebaut in Task 014 |
| `supersede` | ablösen | mit Nachfolger und Grund |
| `delete` | löschen | Löschmarke; Eigenes mit `write`, Fremdes mit `supersede`; ein Verzeichnis mit `recursive`. Gebaut in Task 014 |
| `replace_directory` | ein ganzes Verzeichnis ersetzen | verworfen am 2026-09-28: `kephalaion node dir push` aus Einzelvorgängen (Task 016), siehe „Einen Ordner abgleichen“ |

**Festgelegt am 2026-09-26 für `create`, `write`, `delete` und `rename`, umgesetzt in Task 014
(2026-09-27)** — `rename` ist dafür aus Stufe 3 in Stufe 2 vorgezogen, weil Umbenennen und
Verschieben im Explorer von VS Code darauf laufen und ein Ersatz aus Anlegen und Löschen die
`id` verlöre. Vertrag in [`vertrag.md`](vertrag.md), „Schreibvorgänge“:

- **Vier Vorgänge des Vertrags**, je einer je Werkzeug, in Fassung 1 (sie kommen nur hinzu).
  Der Node prüft Anmeldung und Lesbarkeit wie beim Lesen und reicht dann Account und Token an
  den Hub; ob geschrieben werden darf, entscheidet allein der Hub — so wirkt eine Sperre beim
  Schreiben sofort.
- **Rechte am Hub:** `write` für `create` und für Eigenes (`created_by` ist der User des
  Accounts), `supersede` für Fremdes, `write` ist dafür nicht nötig; unter `vendor/<name>/`
  allein der Scope `vendor/<name>`, direkt in `vendor/` niemand (Task 016) — bei Verzeichnissen
  für jedes Dokument darunter, bei `rename` mit altem und neuem Namen; ein einziges verbotenes lässt den ganzen Vorgang scheitern.
  Collection unbekannt, nicht für den Node erlaubt oder nicht für den Account: eine Antwort.
  `created_by`/`updated_by` ist der User; `actions` nennt Account und Node.
- **Revision als Vorbedingung:** `write`, `delete` und `rename` nehmen wahlweise die Revision,
  auf der sie beruhen (`base_revision`); weicht die des Dokuments ab, lehnt der Hub mit eigenem
  Code ab. Weil die Revision global ist und nur steigt, genügt der Vergleich auf Gleichheit.
- **Fehlercodes:** Name vergeben (`name_taken`), Revision veraltet (`stale_revision`), Datei
  und Verzeichnis zugleich (`path_conflict`), nicht gefunden (`not_found`), Recht fehlt
  (`forbidden`), nicht lesbar (`not_readable`). Die Werkzeuge melden dazu `unreachable` (Hub
  nicht erreicht, nichts gespeichert), `outcome_unknown` (Ausgang unklar), `unsupported` (der
  Hub kennt den Vorgang nicht, oder `https`/`ssh`; nichts gespeichert) und `internal` (ein
  Fehler des Nodes selbst — `node.db`, Replica nicht lesbar; nichts abgeschickt).
  `account_unauthenticated` des Hubs wird am Node `not_readable`, `unauthenticated` und
  `unsupported_version` gehen durch. Form: `isError`, die Meldung als Text und
  `structuredContent.error` mit `code` und `message` — Clients entscheiden nach dem Code.
- **Verzeichnisse als Ganzes:** `delete` mit `recursive` und `rename` eines Verzeichnisses
  wirken auf alle Dokumente darunter, alles oder nichts, eine Revision. `recursive` bei einem
  Dokument ist ohne Belang; ein Verzeichnis ohne `recursive` ist `invalid`.
- **Ziel von `rename`**, geprüft im Stand davor, das Recht vor dem Ziel: von derselben Art
  belegt — ein Dokument auf ein lebendes Dokument, ein Verzeichnis auf ein bestehendes
  Verzeichnis — ist `name_taken`; nichts wird überschrieben, nichts zusammengelegt. Von der
  anderen Art belegt — ein Dokument auf ein Verzeichnis oder umgekehrt — ist `path_conflict`.
  Das Ziel unter der Quelle (`x` nach `x/y`) und derselbe Name sind `invalid`. `rename` bleibt
  in seiner Collection.
- **Die eigene Änderung steht sofort in der Replica:** Die Antwort des Hubs trägt die
  geschriebenen Zeilen; der Node schreibt sie in die Replica, bevor er antwortet, und stößt
  danach den Abgleich an. Sonst lieferte `read` nach dem Speichern noch die alte Revision, und
  schon das zweite Speichern scheiterte an der eigenen Vorbedingung.
- **Nie wiederholt**, siehe „Transport, Token und Fehlschläge“. Nach Erfolg und nach
  unklarem Ausgang stößt der Node den Abgleich an, auch bei `sync_interval` `0`.
- **Leere Verzeichnisse** gibt es am Hub weiter nicht; die Erweiterung für VS Code merkt sich
  ein neu angelegtes, bis darin etwas liegt.
- **Grenzen:** Inhalt UTF-8 ohne NUL-Byte, höchstens 1 MiB, leer erlaubt; über HTTP trägt ein
  Schreibvorgang bis 7 MiB, ebenso der MCP-Eingang des Nodes — so passt jedes Dokument auch in
  der größten Form, die JSON daraus macht. Schreiben setzt voraus, dass die Replica Collection
  und Account-Zeile trägt (siehe „Authentifizierung“, „Wer wann prüft“).

### Verwaltung über MCP (vorgemerkt am 2026-09-25)

Was heute nur die Kommandozeile kann, soll auch über MCP gehen — nur mit dem passenden Recht:

- **Hub:** Collections, Nodes, Grants, später Accounts und Scopes; `status` des Hubs. Der
  Node reicht solche Aufrufe an den Hub durch, der Hub prüft das Recht des Accounts.
- **Node:** seine Hub-Einträge und gewünschten Collections, Stand des Abgleichs, Abgleich
  anstoßen.

Offen: ein eigenes Recht für die Verwaltung (etwa `admin` je Hub, nicht je Collection), wer
am Node verwalten darf — dort gibt es keine Accounts, nur die des Hubs —, und ob die
Werkzeuge nur erscheinen, wenn der Account das Recht hat. Tokens dürfen dabei nie in einer
Antwort an die KI stehen; `hub node add` über MCP bräuchte dafür einen anderen Weg als die
Anzeige.

### Für k-playbook — Kandidaten

Aus den heutigen Werkzeugen von k-playbook; je zu entscheiden, ob Kephalaion sie trägt, ob
sie auf den allgemeinen aufsetzen oder in k-playbook bleiben:

- **Eingang** für Rohmaterial: ablegen, auflisten, lesen (heute `knowledge_inbox_*`).
- **Warteschlange** der offenen Fragen: hinzufügen, auflisten, verwerfen
  (heute `knowledge_queue_*`).
- **Todos:** hinzufügen, auflisten, ändern, löschen (heute `todo_*`).
- **Tasks** — als eigene Werkzeuge, weil eine KI ein Werkzeug, das ihre Absicht beim Namen
  nennt, zuverlässiger benutzt als eine Folge allgemeiner Aufrufe. In VS Code bleiben sie
  über die Erweiterung sichtbar ([`vscode.md`](vscode.md)):

  | Werkzeug | Zweck | darunter |
  |---|---|---|
  | `task_create` | neuen Task anlegen: Kurzname, wahlweise Inhalt → Nummer und Name | `create_numbered` in `tasks/` |
  | `task_get` | Nummer → vollständiger Name, wahlweise mit Inhalt; sucht in `tasks/` und `tasks/done/` | `list` mit Maske `<nummer>-*` |
  | `task_list` | offene, erledigte oder alle; Neueste zuerst, Anzahl wählbar | `list` |
  | `task_done` | Task nach `tasks/done/` verschieben | `rename`, `id` bleibt |

  Weil erledigte Tasks bei der Vergabe mitzählen, ist eine Nummer eindeutig und reicht als
  Schlüssel. Finden sich doch zwei Dateien mit derselben Nummer (von Hand angelegt), meldet
  `task_get` beide, statt zu raten. Offen: in welcher Collection die Tasks eines Projekts
  liegen, und ob sie ein Standard des Nodes oder des Clients ist. Kommen sie doch hierher,
  liegen mit persönlichen Verzeichnissen (vorgemerkt, siehe „Collections, Accounts, Rechte“)
  die Tasks aller Entwickler in einer Collection, und jeder sieht bei der Arbeit nur seine.

- **Verallgemeinerung, später.** Die Werkzeuge für Tasks arbeiten auf einem Verzeichnis mit
  Voreinstellung — `tasks/`, Ablage `tasks/done/`, Breite drei Stellen. Früher hieß das hier
  „Reihen“ (`series_*`). Vorgemerkt ist stattdessen, dass ein Verzeichnis Eigenschaften trägt
  — `numbered` und `personal`, unabhängig voneinander —; allgemeine Werkzeuge richten sich
  danach, `task_*` bliebe als bequemer Name darüber.
- **Werkzeuge sind zuschaltbar.** Jedes Werkzeug mehr lenkt die KI ab. Die Werkzeuge für
  k-playbook erscheinen nur, wo sie gebraucht werden — etwa wenn eine Collection als
  k-playbook-Ablage gekennzeichnet ist. Wie genau, ist offen.
- **Veröffentlichen** der erzeugten Doku (heute `knowledge_publish`, allgemein
  `replace_directory`).
- **Stand der Ablage** (heute `knowledge_status`).

## Stufen

1. **Nur lesen.** Node liefert Suchen, Lesen, Abgleich; eine Collection, ein Token, ein Recht.
2. **Schreiben mit Rechten, ohne KI.** Dateien werden deterministisch abgelegt, der Hub prüft
   Recht und Form. Accounts, `rotate`, Ablehnungen, Fehlschläge; dazu Umbenennen, vorgezogen
   aus Stufe 3. Gebaut bis auf `create_numbered`: Accounts und `rotate` in Tasks 005–007,
   `create`, `write`, `delete` und `rename` in Task 014.
3. **Vorgänge auf Dateien.** Anhängen, Abschnitt ändern, abschließen, Verzeichnis ersetzen.
   Das Ersetzen eines Ordners aus einem lokalen Verzeichnis kommt vorher als Abgleich in der
   CLI (`node dir push|pull`, Task 016).
4. **Schnipsel und Einordnen durch den Hub** (zurückgestellt). Zuerst Regeln. Erst danach, und
   getrennt entschieden, eine KI im Hub, mit Protokoll und Warteschlange.
5. **Semantische Suche.** Einbettungen rechnet der Hub einmal für den Store und liefert
   sie mit. Offen bleibt die Frage, die schon einmal zum Verzicht geführt hat: Auch die
   *Frage* braucht eine Einbettung. Entweder ein kleines Modell lokal, oder BM25 zuerst und
   semantisch nur bei schlechter Antwort nachfragen.

## Offene Punkte

- **Wer darf schreiben:** entschieden — allein Token und Rechte bestimmen das, nicht die
  Frage, ob ein Mensch oder eine Sitzung aufruft. Offen bleibt, ob eine Collection zusätzlich
  „nur nach Bestätigung“ verlangen darf.
- **Was ist eine Collection** im Betrieb? Angelegt wird sie vom Admin am Hub.
- **Persönliche Verzeichnisse:** vorgemerkt (siehe „Collections, Accounts, Rechte“). Offen, ob
  es sie braucht, wer die Eigenschaft setzt und wie eine Übergabe an einen anderen User geht.
- **Erreichbarkeit:** entschieden — Verschlüsselung ist Pflicht, der Transport ist wählbar
  (HTTPS, SSH, lokal im selben Prozess). Offen ist, welcher entfernte zuerst gebaut wird.
- **Node als Dienst:** entschieden — er läuft ständig, pro User als Benutzerdienst oder
  global als Systemdienst; k-playbook prüft beim Briefing zusätzlich. Eingerichtet mit
  systemd, auf macOS (nur pro User) mit launchd (2026-09-26, „Installation und Betrieb“;
  gebaut in Task 011).
  Offen: ob die tägliche Frage nach einem Update abschaltbar sein muss (ohne Netz, Datenschutz).
- **k-playbook ↔ Kephalaion im Einzelnen:** Welche Werkzeuge eigens für k-playbook kommen
  (Kandidaten unter „Werkzeuge“)? Welche braucht k-playbook, die eine KI-Sitzung nicht sehen
  soll, und hängt das am Token? Das Ersetzen eines ganzen Verzeichnisses (heute `publish`)
  und das Update von `vendor/` laufen über den Abgleich in der CLI (`node dir push`,
  entschieden am 2026-09-28, „Einen Ordner abgleichen“), nicht über einen eigenen Vorgang im
  Vertrag. Die Projektablage und das Briefing regelt k-playbook; das Überlagern von
  mitgelieferten und eigenen Regeln auch (entschieden am 2026-09-27, „vendor/“).
- **Token-Rotation (später):** Accounts von Menschen und KIs rotieren am Hub mit einer Frist,
  in der altes und neues Token gelten; der neue Hash gleicht sich zu den Nodes ab. Ein Node
  rotiert sein Token selbst: Er erzeugt ein neues und meldet dem Hub nur den Hash, ab da gilt
  das neue (erster Vorgang, siehe „Authentifizierung“, „Einrichtung“). Wie das neue Token bis zu k-playbook gelangt, ist offen.
- **Vormerken bei Nichterreichbarkeit:** derzeit nein, es wird gemeldet. Ob ein Ausgangskorb
  später kommt, ist offen; er bringt Reihenfolge- und Doppelschreibfragen mit.
- **Anhängen bei mehreren Accounts:** Der Hub serialisiert. Für die lokale Ablage steht
  dasselbe als Task 078 (`append`) bereit; beide sollten denselben Vertrag haben.
- **Dopplungen:** Zwei Collections oder zwei Accounts legen dasselbe Wissen ab. Erkennt das
  der Hub, und was tut er dann?
- **Rückfragen an den Menschen:** Wer sieht die Warteschlange des Arbeitsprozesses, und was
  geschieht mit einem Schnipsel, den wochenlang niemand beantwortet?
- **KI im Hub:** getrennt zu entscheiden. Kosten, Anbieterbindung und eine neue
  Fehlerquelle in einem Dienst, der sonst nur verwaltet.
- **Protokoll der Entscheidungen:** wo es liegt, wer es durchsieht, und was beim Widerspruch
  geschieht.
