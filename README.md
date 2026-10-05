# kephalaion

**Knowledge, distilled.**
*Shared memory for humans and agents*

> **Vorerst für den eigenen Gebrauch.** Kephalaion ist in früher Entwicklung und läuft bisher
> nur auf dem Rechner des Autors. Schnittstellen, Datenbankschema und Konfiguration ändern
> sich bis v1.0 ohne Rücksicht auf bestehende Installationen. Der Code ist öffentlich,
> Unterstützung gibt es keine.

Eine geteilte Wissensdatenbank mehrerer Nutzer und Projekte: ein Binary mit zwei Rollen —
dem Hub für Store, Journal und Accounts und dem Node als lokalem MCP-Server mit Replica.
Was geplant ist und warum, steht in [`docs/konzept.md`](docs/konzept.md), die Begriffe in
[`docs/begriffe.md`](docs/begriffe.md).

**Stand:** Gebaut sind das Gerüst (`version`, `upgrade` mit `--check --json`), die Installation
pro User und global samt Dienst (`service`, [`docs/installation.md`](docs/installation.md))
und das Einrichten der Rollen:
`hub init`, `node init`, `status`, `config show|set|unset|export|import`, dazu am Hub Collections,
Nodes und Accounts mit Rechten je Collection, am Node seine Hubs und die gewünschten
Collections. Der Hub nimmt Dokumente auf (`hub doc`, `hub import`), der Node gleicht sie in
seine Replica ab (`node sync`) und zeigt sie an (`node doc`), über `transport local` oder
`http` auf diesem Rechner. `kephalaion serve` lauscht je Rolle: der Hub für Nodes, der Node als
MCP-Server für Clients mit den Werkzeugen `whoami`, `list`, `read` und `changes` — gelesen
wird aus der Replica, ohne Netz — und `create`, `write`, `delete` und `rename` — geschrieben
wird über den Hub, mit Rechten je Collection —, und gleicht als Node im Hintergrund ab.
Accounts tauschen ihr Token am Node (`node account rotate`); `node whoami` zeigt, wen der Node
kennt. Eine Erweiterung für VS Code zeigt den Stand des Nodes und bindet Collections als Ordner
ein, zum Lesen und Schreiben. Ein Node auf einem anderen Rechner erreicht den Hub über
`https`: TLS beendet ein Reverse-Proxy vor dem Hub, der auf Loopback bleibt
([`docs/installation.md`](docs/installation.md)). Noch nicht gebaut: `ssh`, Suche, weitere
Werkzeuge (`create_numbered`, `append`, `replace_section`, `supersede`, `replace_directory`).

## Was gebraucht wird (grob)

Vorläufige Übersicht, damit nichts fehlt; ausgearbeitet wird sie später. Einzelheiten stehen
im Konzept.

- **Hub** — einmal je Installation, einziger Schreiber. Hält Store, Collections, Accounts,
  Nodes, Protokoll. Datenbank SQLite, später wahlweise PostgreSQL. Kein MCP, sucht nicht.
- **Node** — einmal je Rechner, als Dienst. MCP-Server über HTTP für Clients (Claude Code,
  Cursor, OpenCode, k-playbook). Hält je Hub eine Replica, indiziert und sucht lokal (FTS5).
- **Beide in einem Prozess** ist der häufige Fall; der Node erreicht einen Hub über `local`,
  `http` (nur `localhost`, zum Testen), `https` oder `ssh`.
- **Collections** — Einheit für Rechte und Abgleich. **Accounts** mit Token für Menschen, KIs
  und Programme; **Nodes** mit eigenem Token und den Collections, die sie abgleichen dürfen.
- **Dokumente** — Name ist ein Pfad, stabile `id`, Revision. Löschen als Löschmarke.
- **Abgleich** — der Node fragt „alles seit Revision X“, in Seiten; `hub_id` erkennt einen neu
  angelegten Hub.
- **Werkzeuge** — lesen (`search`, `read`, `list`, `changes`), schreiben (`create`, `create_numbered`,
  `write`, `append`, `replace_section`, `rename`, `supersede`, `delete`,
  `replace_directory`), dazu eigene für k-playbook (Eingang, Warteschlange, Todos, Tasks).
- **Kommandozeile** — `init`, `status`, `config`, Verwaltung von Collections, Nodes, Accounts
  und Hubs, Dokumente am Hub, `node sync`, `node account rotate`, `serve`; später `search`.
- **Stufen** — 1 lesen, 2 schreiben mit Rechten, 3 Vorgänge auf Dateien, 4 Schnipsel
  (zurückgestellt), 5 semantische Suche.

## Installation

Zwei Arten, je Rechner genau eine — beide ohne Git und Go, Binary und `SHA256SUMS` kommen aus
dem Release. Alles Weitere, auch für Ansible und KI, steht in
[`docs/installation.md`](docs/installation.md).

**Pro User** (Linux und macOS, amd64 und arm64):

```sh
curl -fsSL https://github.com/kephalaion/kephalaion/releases/latest/download/install.sh | sh
kephalaion node init          # Rollen einrichten, siehe „Einrichten“
kephalaion service install    # Dienst: systemd --user bzw. LaunchAgent, startet serve
```

Das Skript lädt das Binary der Plattform und `SHA256SUMS` aus dem neuesten Release, prüft die
Prüfsumme und legt das Binary nach `~/.local/bin/kephalaion`. Liegt `~/.local/bin` nicht im
`PATH`, nennt es die Zeile fürs Shell-Profil. Eine bestimmte Version:
`… | KEPHALAION_VERSION=v0.1.0 sh`. `service install` schreibt die Unit
`~/.config/systemd/user/kephalaion.service` (auf macOS den LaunchAgent
`io.github.kephalaion`), startet den Dienst und bricht ab, solange `serve` von Hand läuft;
`kephalaion service status` bzw. die Zeile `Dienst:` in `kephalaion status` zeigt ihn,
`kephalaion service uninstall` entfernt ihn. Log: `journalctl --user -u kephalaion`. Ohne
systemd startet ein eigener Supervisor `kephalaion serve`.

**Global** (nur Linux mit systemd, für alle User eines Rechners): Binary
`/usr/local/bin/kephalaion`, Systembenutzer `kephalaion`, config
`/etc/kephalaion/config.yaml`, Daten `/var/lib/kephalaion/`, System-Unit aus
`kephalaion service unit --system` — von Hand oder per Ansible nach
[`docs/installation.md`](docs/installation.md), „Global“. Die User sind Clients über Loopback;
verwaltet wird als Systembenutzer (`sudo -u kephalaion kephalaion …`). Devcontainer erreichen
den Node noch nicht, weder global noch pro User; die vorgesehenen Wege stehen in
[`docs/konzept.md`](docs/konzept.md), „Devcontainer“.

Die config wird ohne Angabe gefunden: `--config`, `KEPHALAION_CONFIG`, die des Users, wenn es
sie gibt, sonst `/etc/kephalaion/config.yaml`, wenn es sie gibt. `kephalaion status` nennt,
welche gilt und woher.

## Upgrade

```sh
kephalaion upgrade --check    # nachsehen: neueste Version, selbst ersetzbar?, Weg
kephalaion upgrade --check --json   # dasselbe als JSON (für k-playbook)
kephalaion upgrade            # auf das neueste Release; startet den Dienst pro User neu
kephalaion upgrade --version v0.1.0   # genau diese Version, auch zurück
```

`upgrade` prüft die Prüfsumme und ersetzt das Binary atomar; scheitert etwas, bleibt das alte
unverändert. Vorabversionen (`v0.2.0-rc1`) und ältere Versionen gibt es nur mit `--version`,
ebenso das Ersetzen eines selbst gebauten `dev`-Binarys. Darf der Aufrufer das Binary nicht
ersetzen, bricht `upgrade` vor dem Download ab und nennt den Weg — global macht das Upgrade
der Verwalter: Ansible oder `sudo kephalaion upgrade && sudo systemctl restart kephalaion`.
Ob es eine neue Version gibt, meldet auch das MCP-Werkzeug `whoami` (Feld `update`); `serve`
fragt GitHub dafür höchstens einmal am Tag. Ohne Anmeldung erlaubt die GitHub-API 60
Anfragen je Stunde.

## Einrichten

Hub und Node werden je mit einem Aufruf eingerichtet — ohne Rückfragen, nie überschreibend:

```sh
kephalaion hub init     # Datenbank ~/.local/share/kephalaion/hub.db, Abschnitt hub: in der config
kephalaion node init    # Datenbank ~/.local/share/kephalaion/node.db, Abschnitt node: in der config
kephalaion node init --db sqlite:///pfad/node.db   # anderer Ort, absoluter Pfad
kephalaion hub init --listen 127.0.0.1:7500        # anderer Port
```

`init` legt die Datenbank samt Schema an und trägt die Rolle in die config
`~/.config/kephalaion/config.yaml` ein (ohne `--config` nur pro User: Ist der Rechner global
eingerichtet, bricht es ab), mit `db:` und `listen:` — wo `kephalaion serve` für
die Rolle lauscht; Standard Node `127.0.0.1:7433`, Hub `127.0.0.1:7434`. `serve` lauscht
nur auf `127.0.0.1`, `::1` oder `localhost`: Klartext-HTTP verlässt den Rechner nicht; für
Nodes anderer Rechner steht ein Reverse-Proxy vor dem Hub (`https`, siehe
[`docs/installation.md`](docs/installation.md)). Steht die Rolle schon dort oder gibt es die
Datenbankdatei schon, bricht es ab. Die Orte folgen `XDG_CONFIG_HOME` und `XDG_DATA_HOME`;
eine andere config wählt `--config` oder `KEPHALAION_CONFIG`. PostgreSQL ist vorgesehen, aber
noch nicht unterstützt.

```sh
kephalaion status       # welche Rollen, wo ihre Datenbank liegt, ob serve läuft, Kennzahlen
kephalaion config show  # config und settings je Rolle
kephalaion config set node sync_interval 1m          # Abstand des Abgleichs im Hintergrund
kephalaion config unset node sync_interval           # wieder der Standard (30s)
kephalaion config export --output keph-config.yaml   # Einstellungen sichern, ohne Inhalte
kephalaion config import keph-config.yaml            # in eingerichtete Rollen zurückschreiben
```

`status` und alle anderen Kommandos öffnen nur vorhandene Datenbanken, angelegt wird nur mit
`init` — einzige Ausnahme ist die Replica, die der erste Abgleich anlegt (siehe unten).
Migrationen gibt es noch nicht: Passt die Schemafassung einer Datenbank nicht zum
Binary, ist sie neu anzulegen; die Einstellungen rettet `config export`/`import`, die Inhalte
nicht. Dokumente am Hub sind wieder einzuspielen (`hub import`); eine Replica mit fremder
Schemafassung verwirft der Abgleich selbst und gleicht sie neu ab. Mit `https` stieg
`node.db` auf Schemafassung 5 (neu: `hubs.ca`; Fassung 4 brachte `hubs.entry_id` und
`hub_sync`) — jede vorhandene `node.db` lehnt das Binary ab. Der Weg, in dieser Reihenfolge
(`init` überschreibt nicht und bricht bei eingetragener Rolle ab):

```sh
kephalaion config export --output keph-config.yaml   # 1. mit dem ALTEN Binary; enthält Tokens, 0600
kephalaion service uninstall                          # 2. serve stoppen (global: sudo systemctl stop kephalaion)
rm ~/.local/share/kephalaion/node.db*                 # 3. node.db (samt -wal, -shm, .lock) und die Replicas daneben
rm -r ~/.local/share/kephalaion/replicas
$EDITOR ~/.config/kephalaion/config.yaml              # 4. den Abschnitt node: von Hand herausnehmen
kephalaion node init --db sqlite://$HOME/.local/share/kephalaion/node.db --listen 127.0.0.1:7433   # 5. neu, mit dem neuen Binary
kephalaion config import keph-config.yaml             # 6. Hub-Einträge samt Tokens, Collections, settings
kephalaion service install                            # 7. serve starten; die Replicas gleichen sich neu ab
```

Die Hub-Einträge behalten ihre Tokens über Export und Import; die Replicas legt der erste
Abgleich neu an. Global dasselbe als Systembenutzer mit `--config /etc/kephalaion/config.yaml`
und `--db sqlite:///var/lib/kephalaion/node.db` ([`docs/installation.md`](docs/installation.md),
„Neue Schemafassung“).

### Hub und Node auf einem Rechner

Der Hub legt Collections und einen Node-Eintrag an und erlaubt ihm Collections; das Token
des Nodes zeigt er genau einmal und speichert nur den Hash. Der Node trägt den Hub unter
einem Alias ein, mit dem Namen, unter dem der Hub ihn kennt (`--node`), und seinem Token
über die Standardeingabe — nie als Argument, sonst stünde es im Shell-Verlauf und in der
Prozessliste.

```sh
kephalaion hub init
kephalaion node init

kephalaion hub collection add team-x --description "Wissen von Team X"

# Transport local: Hub im selben Prozess. --create legt den Node am Hub derselben
# config selbst an und trägt sein Token direkt ein, ohne es anzuzeigen.
kephalaion node hub add privat --node laptop --transport local --create
kephalaion hub node grant laptop team-x
kephalaion node collection add privat:team-x
kephalaion node hub check privat   # whoami: erreichbar, Node-Name, erlaubte Collections

# oder http auf localhost — verhält sich wie eine getrennte Installation. Dafür ein
# eigener Node, dessen Token der Hub einmal anzeigt; kephalaion serve muss laufen.
kephalaion hub node add laptop-http --description "dieser Rechner, über HTTP"
kephalaion hub node grant laptop-http team-x
read -rs TOKEN            # Token einfügen, Enter
printf '%s\n' "$TOKEN" | kephalaion node hub add test --node laptop-http --transport http \
  --address http://localhost:7434/hub --token-stdin      # /hub: dort liegt der Vertrag
unset TOKEN

kephalaion status       # Collections, Accounts und Nodes am Hub, Hubs und Collections am Node
```

Ohne `--create` trägt `node hub add` einen Node ein, den der Hub schon kennt: Name mit
`--node`, Token über die Standardeingabe (`--token-stdin`). Die Adresse eines `serve` endet
auf `/hub`: Der Hub-Listener bedient den Vertrag dort; an seiner Wurzel antwortet er einem
Browser mit der Weboberfläche, sonst mit einer Begrüßung. `hub init` und `status` nennen die
Adresse. Ein Hub auf einem anderen Rechner
ist `https` (`--address https://<host>/<präfix>/hub`, etwa `https://<name>/kephalaion/hub`:
der Präfix gehört dem Proxy, `/hub` dem Binary): TLS beendet ein Reverse-Proxy auf seinem
Rechner, das Zertifikat prüft der Node gegen die System-Roots oder eine mitgegebene CA
(`--ca-file <pfad>`, gespeichert wird der Inhalt); `node hub check` erklärt Zertifikatsfehler,
einen Proxy ohne Hub dahinter, die Host-Prüfung und eine Adresse ohne `/hub` — Aufbau in
[`docs/installation.md`](docs/installation.md), „Hub für Nodes anderer Rechner“. `ssh`
(`--address [user@]host[:port]`, optional `--ssh-key`) lässt sich eintragen, aber noch nicht
benutzen. Namen von Collections, Nodes, Accounts und Hub-Aliasen bestehen aus
`a–z`, `0–9`, `.`, `_` und `-`, beginnen mit Buchstabe oder Ziffer, haben höchstens 63 Zeichen
und nicht den Präfix `system`; Nodes und Accounts heißen nicht `admin` und teilen sich die
Namen. Die Hilfe zeigt alle Kommandos: `kephalaion hub node --help`,
`kephalaion node hub --help`.

`config export` sichert auch die Collections, Nodes und Accounts (nur mit Hash, Accounts samt
User und Rechten je Collection) und die Hubs des Nodes samt ihrem Token im Klartext — die Datei
entsteht deshalb mit `0600`. Das Exportformat ist 8 (`format: 8`; seit 8 je Recht die
Verzeichnis-Scopes `dirs`, seit 7 je Hub-Eintrag die CA als `ca`, seit 6 je Recht die Scopes
`vendor`; ältere Fassungen lesen sich ohne und dürfen sie nicht tragen), `user` je Account ist
dort Pflicht. `config import` gleicht am Hub die Account-Zeilen an den Export an; ein Export im
Format 4 setzt den User jedes Accounts auf dessen Namen, einer vor Format 4 lässt die Accounts
unberührt, einer im Format 1 ersetzt nur die `settings`, einer im Format 2 geht nur, wenn er am
Node keine Hub-Einträge enthält — ihnen fehlt `node_name`. Dokumente und Replicas gehören nicht zum
Export.

### Accounts

Ein Account ist, wer zugreift — Mensch, KI oder Programm. Der Hub legt ihn an, zeigt sein
Einrichtungstoken einmal und speichert nur den Hash; die Rechte gelten je Collection: `read`
immer, dazu wahlweise `write` (Eigenes anlegen, ändern, löschen) und `supersede` (Fremdes
ändern, ablösen, löschen). Dazu Scopes `vendor/<name>`: Unter `vendor/<name>/` — mitgelieferte
Vorlagen, etwa von k-playbook — zählt allein der Scope, ohne `write` und unabhängig vom
Urheber; direkt in `vendor/` schreibt über einen Node niemand. Ein Account nur mit dem Scope
pflegt seine Vorlagen und kann sonst nichts schreiben. Und **Verzeichnis-Scopes** (`--dir
<pfad>`, angezeigt als `dir <pfad>/`): Unter `<pfad>/` darf der Account anlegen, ändern,
löschen und umbenennen, ohne `write` und unabhängig vom Urheber — die Grenze ist ein ganzes
Segment (`docs` deckt `docs/…`, nicht `docs2/…`), nicht die Wurzel, nicht `vendor`. Anders als
`vendor/<name>` nimmt er niemandem etwas: Wer `write` oder `supersede` hat, schreibt dort weiter
wie sonst. Er gibt `node dir push` ein Ziel außerhalb von `vendor/` (siehe „Einen Ordner
abgleichen“).

Jeder Account gehört einem **User** — ein Merkmal, kein Zugang: kein Token, keine Rechte. Wer
auf zwei Rechnern je eine KI-Sitzung hat, hat zwei Accounts und einen User. Ohne `--user` ist
der User der Name des Accounts (etwa für eine Automatisierung). Der User steht in
`created_by`/`updated_by` dessen, was der Account schreibt; `admin` ist reserviert.

```sh
kephalaion hub account add alice --user maria --description "Laptop"  # zeigt das Einrichtungstoken einmal
kephalaion hub account add alice-vm --user maria           # zweiter Account desselben Users
kephalaion hub account list --user maria
kephalaion hub account set alice-vm --user bob             # alle Zeilen des Accounts, eine Revision
kephalaion hub account grant alice team-x                  # read
kephalaion hub account grant alice team-x --write          # setzt vollständig: read, write
kephalaion hub account grant alice team-x                  # und wieder nur read
kephalaion hub account grant k-playbook team-x --vendor k-playbook   # nur der Scope: schreibt unter vendor/k-playbook/
kephalaion hub account grant alice team-x --write --dir test-docs    # dazu der Verzeichnis-Scope test-docs/
kephalaion hub account show alice      # Rechte je Collection: team-x: read, write, dir test-docs/
kephalaion hub account lock alice      # Zeilen werden Löschmarken, die Rechte bleiben gemerkt
kephalaion hub account unlock alice
kephalaion hub account token alice     # neues Einrichtungstoken, das alte gilt nicht mehr
```

Das Einrichtungstoken ist das erste Token des Accounts. Sein erster Vorgang tauscht es am
Node gegen ein eigenes — `rotate`, ein Kommando der Kommandozeile, nie ein MCP-Werkzeug, damit
das Token nicht im Kontext der KI steht:

```sh
T=~/.config/kephalaion/tokens/privat          # je Hub ein Verzeichnis, benannt nach dem Alias
install -d -m 700 ~/.config/kephalaion/tokens "$T"
install -m 600 /dev/null "$T/alice.token"
read -rs TOKEN && printf '%s\n' "$TOKEN" > "$T/alice.token" && unset TOKEN
kephalaion node account rotate privat alice --token-file "$T/alice.token"
kephalaion node account check  privat alice --token-file "$T/alice.token"
```

Die Token-Dateien liegen unter `~/.config/kephalaion/tokens/<hub>/<account>.token`: je Hub
ein Verzeichnis mit dem Alias des Hub-Eintrags, darin je Account eine Datei. Die Erweiterung
für VS Code findet sie dort.

`rotate` erzeugt das neue Token am Node, schreibt es vor dem Aufruf nach
`alice.token.pending`, meldet sich mit dem alten an und schickt dem Hub nur den Hash des
neuen. Nach Erfolg ersetzt es die Datei und schreibt die Zeilen des Accounts in die Replica —
nur die gewünschter Collections —, der Account ist also sofort bekannt. Scheitert der Aufruf
eindeutig, bleibt die Datei; ist der Ausgang unklar (Zeitüberschreitung), bleiben beide
Dateien, und `node account check` klärt, welches Token gilt, und räumt auf. Mit
`--token-stdin` statt `--token-file` liest `rotate` das alte Token von der Standardeingabe und
gibt das neue einmal aus. `rotate` wird nie wiederholt: Danach gilt das alte Token nicht mehr.

### serve und MCP

`kephalaion serve` startet je eingerichteter Rolle einen Listener auf ihrem `listen`: den Hub
für Nodes (der Vertrag unter `/hub`: `POST /hub/v1/whoami|rotate|sync` und
`/hub/v1/create|write|delete|rename`, siehe [`docs/vertrag.md`](docs/vertrag.md); an der
Wurzel `/` für einen Browser die Weboberfläche (siehe „Weboberfläche“), sonst eine kurze
Begrüßung mit der Version, `/v1/…` dort ist 404 mit dem Hinweis auf
`/hub`, nie eine Umleitung), den Node als MCP-Server für Clients unter `/mcp`. Als
Dienst startet ihn `kephalaion service install` (siehe „Installation“); von Hand läuft er im
Vordergrund, schreibt je Anfrage eine Zeile nach stderr (Methode, Pfad, Status, Dauer, Node- und
Account-Namen, bei einem Schreibvorgang über MCP Vorgang, Hub und Fehlercode — nie ein Token,
nie ein Inhalt) und endet mit SIGINT oder SIGTERM. Eine Sperre auf `<db>.lock` neben jeder Datenbank verhindert
einen zweiten `serve` auf derselben Rolle; alle anderen Kommandos laufen daneben, auch
`node sync`, `node hub rm|add` und `config import`. Beide Rollen beantworten nur
Anfragen, deren `Host` dieser Rechner mit dem eigenen Port ist, sonst 403; ein Reverse-Proxy
davor setzt `Host` deshalb auf `localhost:7434`, ein SSH-Tunnel zum Hub geht nur mit gleichem
Port (`ssh -L 7434:localhost:7434 …`). Hinter einem Proxy nennt die Logzeile die Adresse des
Aufrufers aus `X-Forwarded-For` (`via`).

**Der Node über einen Proxy** (Task 023): Derselbe Reverse-Proxy, der den Hub nach außen
reicht, kann auch `/mcp` reichen — als `https://<name>/kephalaion/mcp`, für Clients auf einem
anderen Rechner ohne eigenen Node. Trägt eine Anfrage `X-Forwarded-For`, kam sie über den
Proxy; ohne gültige Anmeldung an mindestens einem Hub antwortet der Node dann verdeckt: keine
Version, kein `update`, keine Namen von Node und Hubs. Lokal bleibt alles, wie es ist. Bei einem
falschen Token antwortet der Node nie 401; die Logzeile trägt stattdessen `login=invalid`
direkt hinter `via`, und eine fail2ban-Jail auf dem Rechner des Proxys zählt sie. Vorlage für
Caddy (mit dem Filter, der die Token-Header aus dem Log hält), Jail und der Weg eines Clients
ohne Node: [`docs/installation.md`](docs/installation.md), „Node für Clients anderer Rechner“.

```sh
kephalaion service install         # als Dienst; oder von Hand, etwa zum Testen:
kephalaion serve 2>> ~/.local/state/kephalaion/serve.log &
kephalaion status | grep serve     # serve: läuft
```

**Bei den KI-Assistenten anmelden:** `kephalaion node mcp add` trägt den Node bei Claude Code,
OpenCode und Codex als MCP-Server `kephalaion` ein, auf User-Ebene und für alle Hubs, das
Token nie im Klartext (Claude Code und Codex holen die Header bei jeder Verbindung über
`kephalaion node mcp headers`, OpenCode verweist auf die Token-Datei); VS Code meldet die
Erweiterung (unten). `kephalaion node mcp status` zeigt je Assistent, ob er eingetragen ist,
`remove` entfernt den Eintrag. `install.sh` und `node account rotate` stoßen das von selbst an.
Einzelheiten: [`docs/installation.md`](docs/installation.md), „Bei den Assistenten anmelden“.
Einen Node auf einem anderen Rechner trägt `kephalaion node mcp add --node
https://<name>/kephalaion --hub <alias>` ein — auch ohne eigene config; zuerst prüft es den Node
(Zertifikat, `initialize` ohne Token) und schreibt nur, wenn er antwortet, und an ihn gehen nur
die Header-Paare der gewählten Hubs (`--hub`, wiederholbar; ohne Wahl nur, wenn unter `tokens/`
genau ein Hub liegt). `node mcp status --node …` vergleicht mit dieser Adresse.

Als Node gleicht `serve` seine Replicas selbst ab: beim Start je Hub-Eintrag, danach im
Abstand `sync_interval` aus den `settings` des Nodes — Standard 30 s, mindestens `1s`, `0`
schaltet ab; `config set` wirkt ohne Neustart. Die Hub-Einträge liest jede Runde neu, `node
hub add|rm` wirkt also sofort; ein langsamer Hub hält die anderen nicht auf. `local` nimmt den
Hub desselben `serve`, `http` und `https` den unter seiner Adresse; `ssh` wird noch
übergangen (eine Logzeile beim Start). Das Log nennt einen Abgleich nur, wenn Zeilen kamen,
einen Fehler beim ersten Mal und wenn sich seine Art ändert, und die Erholung:

```text
2026-09-26T10:15:02+02:00 Abgleich im Hintergrund alle 30s
2026-09-26T10:15:02+02:00 Abgleich privat: 3 Zeilen, Revision 7
2026-09-26T10:15:02+02:00 Abgleich test gescheitert: Hub http://localhost:7434/hub: dial tcp 127.0.0.1:7434: connect: connection refused
2026-09-26T10:20:32+02:00 Abgleich test geht wieder
```

Letzter Erfolg und letzter Fehler je Hub stehen in `node.db` (Tabelle `hub_sync`, nicht im
Export), geschrieben auch von `node sync`; `status` und `whoami` zeigen sie.

Ein MCP-Client meldet sich am Node je Hub mit einem Header-Paar an —
`X-Keph-Account-<alias>` und `X-Keph-Token-<alias>`, der Alias ist der des Hub-Eintrags am
Node, Groß- und Kleinschreibung der Header zählt nicht. Die Header stehen in der
MCP-Konfiguration des Clients; die KI sieht sie nicht. Für Claude Code etwa `.mcp.json`, das
Token aus einer Umgebungsvariable, damit es nicht im Repository steht:

```json
{
  "mcpServers": {
    "kephalaion": {
      "type": "http",
      "url": "http://127.0.0.1:7433/mcp",
      "headers": {
        "X-Keph-Account-privat": "${KEPH_ACCOUNT}",
        "X-Keph-Token-privat": "${KEPH_TOKEN}"
      }
    }
  }
}
```

```sh
export KEPH_ACCOUNT=alice
export KEPH_TOKEN="$(cat ~/.config/kephalaion/tokens/privat/alice.token)"
```

Für mehrere Hubs steht je Hub ein Paar darin. Der Node prüft das Paar bei jeder Anfrage gegen
die Account-Zeilen seiner Replica, ohne Cache; `initialize` geht ohne Anmeldung. Das Werkzeug
`whoami` zeigt die Version des Nodes und je Hub-Eintrag — alle, nicht nur die mit Header-Paar —
`login` (`ok`, `invalid` oder `missing`), den Namen des Nodes am Hub und den Stand des
Abgleichs (letzter Erfolg, Revision, letzter Fehler); bei `ok` Account, User, Collections und
Rechte (`rights` als Liste wie in der Kommandozeile, etwa `read`, `write`, `vendor/k-playbook`,
`dir docs/`; die Verzeichnis-Scopes dazu als Liste `dirs`, immer da, auch leer). Header zu
Aliasen, die der Node nicht kennt, stehen in `unknown_hubs`. Nie ein Token, ein Hash, die
Adresse, der Transport oder die `hub_id`. Hat der Node einen Hub noch nie
abgeglichen, ist `login` dort `invalid`, und `sync` sagt `never_synced`. Ein gesperrter
Account gilt am Node nach dem nächsten Abgleich nicht mehr.

Dasselbe auf der Kommandozeile, ohne Token:

```sh
kephalaion node whoami                  # Version, Hubs mit Stand, bekannte Accounts
kephalaion node whoami bob              # was whoami einem Client mit bobs Zugangsdaten antwortet
kephalaion node whoami bob --hub privat --json   # nur ein Hub, als JSON wie das Werkzeug
```

Gelesen wird über drei Werkzeuge, alle aus der Replica und nur in Collections, in denen der
gültig angemeldete Account `read` hat. Eine Collection heißt `<hub>:<collection>`; der Hub-Teil
darf fehlen, wenn der Client nur an einem Hub angemeldet ist. Was er nicht lesen darf, gibt es
für ihn nicht — dieselbe Meldung „nicht lesbar“ wie für eine unbekannte Collection. Hat der
Node einen Hub noch nie abgeglichen, sagt die Meldung das.

- **`list`** — ohne `collection` die lesbaren Collections aller Hubs, mit `<hub>:` die eines
  Hubs; sonst ein Verzeichnis (`path`): zuerst die Unterverzeichnisse, dann die Dokumente mit
  Name, `id`, Revision, angelegt und geändert (wann, von wem) und Größe. `recursive`, `sort`
  (`name`, `created`, `updated`), `order`, `mask` (Glob auf das letzte Segment, etwa `*.md`),
  `limit` (Standard 100, höchstens 1000) und `cursor` zum Weiterblättern. Mit `frontmatter`
  trägt jedes `.md`-Dokument sein Frontmatter (Block am Anfang zwischen zwei Zeilen `---`, YAML)
  als JSON-Objekt `frontmatter`, Verzeichnisse und Collections das ihrer `README.md`; lässt es
  sich nicht lesen, steht der Grund in `frontmatter_error`. So bekommt die KI alle Skills mit
  Beschreibung in einem Aufruf: `recursive`, `mask: "SKILL.md"`, `frontmatter: true`.
- **`read`** — ein Dokument per `collection` und `name` oder per `id`; der Inhalt steht im
  Feld `content` der Antwort (ein leeres Dokument hat `""`). `kind` ist `document`, `directory`
  oder `none`; mit `content: false` nur die Angaben samt `writable`, ohne `content`. Mit
  `frontmatter` dazu das Frontmatter des `.md`-Dokuments, bei einem Verzeichnis oder der Wurzel
  der Collection das seiner `README.md`; `content` bleibt der volle Text. Der Text des
  Ergebnisses ist nur das JSON dieser Struktur — so kommt der Inhalt auch bei einem Client an,
  der nur die Struktur weiterreicht (Claude Code).
- **`changes`** — was sich geändert hat: ohne Argumente nur ein `cursor` für „ab jetzt“, mit
  dem `cursor` der letzten Antwort lückenlos alles danach — je Dokument einmal, Löschmarken
  eingeschlossen. `reset` nennt Hubs, deren Replica neu angelegt wurde (neu mit `list` lesen),
  `dropped` Collections, die nicht mehr lesbar sind. `since` (RFC 3339) statt `cursor` fragt
  nach der Zeit des Hubs, nicht lückenlos.

Löschmarken erscheinen nur in `changes`, `SYSTEM:`-Namen nie. Eine Replica, die sich nicht
lesen lässt, betrifft nur ihren Hub (`unreadable_hubs`).
Der Node lehnt Anfragen mit fremdem `Host` oder fremder `Origin` mit 403 ab (Schutz gegen
DNS-Rebinding aus dem Browser).

Geschrieben wird über vier Werkzeuge, mit derselben Adresse wie beim Lesen, immer über den
Hub — der Node beschreibt seine Replica nur mit dem, was der Hub antwortet:

- **`create`** — ein Dokument anlegen (`collection`, `name`, `content`, auch leer).
- **`write`** — den Inhalt ersetzen; mit `base_revision` (aus `read`) nur, wenn das Dokument
  noch diese Revision hat.
- **`delete`** — löschen, es bleibt eine Löschmarke; ein Verzeichnis nur mit `recursive: true`,
  dann alle Dokumente darunter.
- **`rename`** — umbenennen oder verschieben (`new_name`) in derselben Collection, die `id`
  bleibt; auch ein Verzeichnis.

Der Node prüft vorher wie beim Lesen Anmeldung und Lesbarkeit gegen seine Replica und reicht
Account und Token an den Hub; ob geschrieben werden darf, entscheidet allein der Hub — eine
Sperre wirkt beim Schreiben sofort. `write` braucht, wer anlegt oder Eigenes ändert (angelegt
vom eigenen User, auch über einen anderen seiner Accounts), `supersede`, wer Fremdes ändert,
umbenennt oder löscht. Ein Verzeichnis geht als Ganzes: das Recht je Dokument darunter, alles
oder nichts, eine Revision. `created_by`/`updated_by` ist der User des Accounts; welcher
Account über welchen Node schrieb, steht im Protokoll des Hubs (`actions`). Die Antwort nennt
Adresse, Name, `id`, Revision, angelegt, geändert und Größe, bei einem Verzeichnis
`kind: directory` und die Zahl der Dokumente (`count`). Die eigene Änderung steht schon in der
Replica, wenn die Antwort kommt — `read` liefert sofort die neue Revision, zweimal hintereinander
speichern geht —, und der Node stößt den Abgleich dieses Hubs an, auch bei `sync_interval` `0`.
`changes` meldet die eigene Änderung erst nach diesem Abgleich.

Nichts wird still überschrieben, und kein Schreibvorgang wird wiederholt. Ein Fehler hat
`isError`, die Meldung als Text und in der Struktur `error.code` — ein Client entscheidet nach
dem Code, nicht nach der Meldung:

| Code | Bedeutung |
|---|---|
| `name_taken` | der Name ist vergeben (`create`, Ziel von `rename`); nichts wird überschrieben, zwei Verzeichnisse werden nicht zusammengelegt |
| `stale_revision` | das Dokument hat nicht mehr die Revision `base_revision`; die Meldung nennt die aktuelle |
| `path_conflict` | der Name wäre zugleich Datei und Verzeichnis |
| `not_found` | kein solches Dokument, bei `delete` und `rename` auch kein Verzeichnis |
| `forbidden` | das Recht fehlt; die Meldung nennt den Grund („gehört admin, supersede fehlt“) |
| `not_readable` | nicht lesbar, wie beim Lesen — auch ein Account, den der Hub nicht (mehr) annimmt |
| `invalid` | ungültig: Name, Inhalt (kein UTF-8, ein NUL-Byte, über 1 MiB), ein Verzeichnis ohne `recursive` |
| `unreachable` | der Hub ist nicht erreicht worden, nichts gespeichert; gelesen wird weiter |
| `outcome_unknown` | abgeschickt, aber keine brauchbare Antwort — gespeichert sein kann es; nicht wiederholen, nach dem angestoßenen Abgleich mit `read` nachsehen |
| `unsupported` | der Hub kann noch nicht schreiben, oder sein Transport (`ssh`) ist noch nicht gebaut; nichts gespeichert |
| `internal` | ein Fehler des Nodes selbst (`node.db`, Replica nicht lesbar); nichts abgeschickt |

Dazu reicht der Node `unauthenticated` (der Hub nimmt den Node nicht an) und
`unsupported_version` vom Hub durch. **Grenze:** Schreiben setzt voraus, dass die Replica die
Collection und die Zeile des Accounts schon trägt — nach `hub account grant` erst nach dem
nächsten Abgleich; hat der Node den Hub noch nie abgeglichen, nennt die Meldung
`kephalaion node sync <hub>` als Ausweg. `hub doc put|rm` und `hub import` bleiben Vorgänge des
Admins am Hub, ohne Node.

### Weboberfläche

Der Hub-Listener zeigt einem Browser an seiner Wurzel eine Seite — hinter einem
Reverse-Proxy unter dessen Präfix, etwa `https://<name>/kephalaion/`, hinter dessen
Anmeldung. Sie zeigt dem dort angemeldeten User alle seine Accounts am Hub: je Account Name,
Beschreibung, Status (gesperrt: die Rechte ruhen) und je Collection lesen, schreiben (`write`:
Neues und Eigenes), Fremdes ändern (`supersede`) und die Scopes `vendor/<name>` und `dir
<pfad>/`, mit einer kurzen Erklärung der Rechte. Verwalten kann sie nichts; Accounts, Rechte
und Nodes bleiben in der Kommandozeile (`hub account …`).

- **Kein Token:** Die Seite fragt kein Token ab, zeigt und überträgt keins. Wen sie zeigt,
  sagt die Anmeldung des Proxys im Header `X-User` (seit Task 026; vorher fragte sie Account
  und Account-Token ab). Wo das Token eines Accounts meist liegt, nennt sie nur:
  `~/.config/kephalaion/tokens/<hub>/<account>.token`.
- **Ohne Proxy** (lokal `http://localhost:7434/`) setzt niemand `X-User`: Die Seite zeigt nur
  „keine Anmeldung des Proxys“. Lokal nennt `kephalaion hub account list --user <user>` die
  Accounts; der Eingang der Seite antwortet auf `curl -H 'X-User: <user>'
  http://localhost:7434/gui/api/user` mit den Accounts, ohne `X-User` mit 403.
- **Grenze:** Der Hub glaubt jedem `X-User`, der ihn auf Loopback erreicht — ein Prozess auf
  seinem Rechner sieht so Accounts und Rechte eines beliebigen Users (keine Tokens).
- **`curl`** bekommt an der Wurzel weiter die Begrüßung (`text/plain`); die Seite kommt nur,
  wenn `Accept` `text/html` nennt: `curl -H 'Accept: text/html' http://localhost:7434/`.
- **Nach außen** gehört die Seite hinter die Anmeldung eines Proxys, der `X-User` des Browsers
  verwirft und den geprüften setzt; Seite und Eingang antworten nie 401 — siehe
  [`docs/installation.md`](docs/installation.md), „Hub für Nodes anderer Rechner“.

### Dokumente einspielen und abgleichen

Der ganze Weg auf einem Rechner, mit Hub und Node in derselben config: Der Hub nimmt
Dokumente auf, der Node gleicht sie über `transport local` in seine Replica ab und liest sie
dort. Über `http` geht es genauso, mit laufendem `serve`. Ausgangspunkt ist ein Verzeichnis `~/wissen/team-x` mit `leitfaden.md`,
`tasks/001-start.md`, `tasks/002-ende.md` und einem `.git/`.

```sh
kephalaion hub init
kephalaion node init
kephalaion hub collection add team-x --description "Wissen von Team X"
kephalaion node hub add privat --node laptop --transport local --create
kephalaion hub node grant laptop team-x
kephalaion node collection add privat:team-x

kephalaion hub import team-x ~/wissen/team-x      # ein Verzeichnis, eine Revision
echo "Heute: Abgleich ausprobieren" | kephalaion hub doc put team-x notizen/heute.md

kephalaion node sync                              # alle Hub-Einträge; oder: node sync privat
kephalaion node doc list privat:team-x
kephalaion node doc get privat:team-x leitfaden.md
kephalaion status
```

`hub import` liest das Verzeichnis rekursiv; der Name eines Dokuments ist sein relativer
Pfad, mit `--prefix pfad/` davor. Versteckte Dateien und Verzeichnisse wie `.git` übergeht
es still; was kein UTF-8-Text, größer als 1 MiB oder keine gewöhnliche Datei ist, meldet es
als „übersprungen“. Alle Dokumente eines Imports sind ein Schreibvorgang mit einer Revision;
scheitert eines (ungültiger Name, Konflikt zwischen Dokument und Verzeichnis), wird nichts
geschrieben. Was im Verzeichnis fehlt, bleibt am Hub stehen.

```text
angelegt: leitfaden.md
angelegt: tasks/001-start.md
angelegt: tasks/002-ende.md
Import nach team-x: 3 angelegt, 0 ersetzt, 0 unverändert, 0 übersprungen — Revision 1.
```

`hub doc put` legt ein Dokument an oder ersetzt es, der Inhalt kommt von der
Standardeingabe oder aus `--file pfad`; unveränderter Inhalt zählt keine Revision. `hub doc
get|list` lesen am Hub, `hub doc rm` löscht — es bleibt eine Löschmarke, die der Abgleich
weitergibt. Namen mit dem Präfix `SYSTEM:` schreibt nur der Hub selbst.

`node sync` fragt je gewünschter Collection alles seit dem letzten Stand ab, in Seiten; den
ersten Abgleich eines Hub-Eintrags legt seine Replica an, `replicas/<alias>.db` neben
`node.db`:

```text
Hub privat (hub_id 01M3ECGQP32QBHTGXERZSMVBWR): 1 Seite
  team-x: abgeglichen, 4 Zeilen, Revision 2
```

`node doc list|get` lesen nur die Replica, so wie der letzte Abgleich sie hinterlassen hat,
ohne Löschmarken und `SYSTEM:`-Zeilen:

```text
NAME          REVISION  GEÄNDERT
leitfaden.md  1         2026-09-26 10:15 von admin
notizen/      –         –
tasks/        –         –
```

Nach `kephalaion hub doc rm team-x notizen/heute.md` bringt der nächste `node sync` die
Löschmarke (`team-x: abgeglichen, 1 Zeile, Revision 3`), und `node doc list` zeigt
`notizen/` nicht mehr. `status` zeigt am Node den Abstand des Abgleichs im Hintergrund und je
Hub-Eintrag die `hub_id` aus der Replica, je Collection Stand und letzten Abgleich und den
Stand des Abgleichs aus `hub_sync`:

```text
node: eingerichtet
  …
  Abgleich:      im Hintergrund alle 30s (Standard)
  Hubs:
    privat: local, als Node laptop
      hub_id:      01M3ECGQP32QBHTGXERZSMVBWR
      Collections:
        team-x: Revision 3, abgeglichen 2026-09-26 10:15
      Abgleich:    zuletzt gelungen 2026-09-26 10:15:02
```

`node sync` geht über `local` (der Hub derselben config, im selben Prozess), über `http`
(ein Hub auf diesem Rechner mit laufendem `serve`) und über `https` (ein Hub auf einem
anderen Rechner hinter einem Reverse-Proxy); bei Fehlern des Netzes wiederholen `http` und
`https` jede Seite bis zu dreimal, bei einem Zertifikatsfehler nie. Für `ssh` meldet es
„noch nicht unterstützt“, ohne
`hub:`-Abschnitt in der config scheitert auch `local`. Ein gescheiterter Eintrag hält die
übrigen nicht auf; die Meldung steht auf stderr, und der Exit-Code ist 1 — hier mit dem
Eintrag `test` (`http`), während `serve` nicht läuft:

```text
Hub privat (hub_id 01M3ECGQP32QBHTGXERZSMVBWR): 1 Seite
  team-x: abgeglichen, 0 Zeilen, Revision 3
Hub test: gescheitert
node sync: Hub test: Hub http://localhost:7434/hub: dial tcp 127.0.0.1:7434: connect: connection refused
```

Collections, die der Hub nicht (mehr) erlaubt oder die der Node nicht mehr will
(`node collection rm`), entfernt `node sync` aus der Replica. Nennt der Hub eine andere
`hub_id` als bisher oder steht die Replica weiter als der Hub, leert `node sync` sie und
gleicht von vorn ab. Die Replica ist abgeleitet: `node hub rm` löscht sie mit, ebenso `config import` für
Aliase, die im Export fehlen. Die Regeln des Abgleichs stehen in
[`docs/vertrag.md`](docs/vertrag.md).

`node sync` und der Abgleich von `serve` dürfen gleichzeitig laufen, auch neben `node hub
rm|add` und `config import`: Jede Seite prüft in ihrer Transaktion, dass die Replica noch zu
Eintrag (`entry_id`), `hub_id` und Stand passt, und schreibt sonst nichts. Ein Abgleich für
einen inzwischen entfernten oder neu angelegten Eintrag schreibt nie in den neuen.

### Einen Ordner abgleichen: node dir push und pull

`kephalaion node dir push|pull` gleicht einen lokalen Ordner mit einem Verzeichnis einer
Collection ab — als Client des Nodes über MCP, mit Account und Token des Aufrufers, dort
aufgerufen, wo der Ordner liegt. Verglichen wird der Inhalt, nicht das Datum; der Abgleich
besteht aus den Einzelvorgängen `list`, `read`, `create`, `write` und `delete`, je Ebene nach
Name: erst löschen, was fehlt oder die andere Art hat, dann anlegen und schreiben, dann
absteigen. Ein zweiter Lauf ändert nur, was noch abweicht. So bringt k-playbook seine
Vorlagen nach `vendor/k-playbook/` — mit einem Account, der nur den Scope hat:

```sh
kephalaion hub account add k-playbook
kephalaion hub account grant k-playbook team-x --vendor k-playbook
# … Token nach ~/.config/kephalaion/tokens/privat/k-playbook.token, rotate wie oben …

kephalaion node dir push privat:team-x vendor/k-playbook ./k-playbook \
  --exclude installer --last VERSION           # VERSION zuletzt: Zeichen eines vollständigen Laufs
kephalaion node dir push privat:team-x vendor/k-playbook ./k-playbook --exclude installer --dry-run
kephalaion node dir pull privat:team-x vendor/k-playbook /tmp/vorlagen --delete
```

`push` ersetzt den Inhalt des Verzeichnisses — was dort fehlt, wird gelöscht — und schreibt
deshalb nur dorthin, wo ein Admin es ausdrücklich erlaubt hat: unter `vendor/<name>/` und in
ein Verzeichnis, für das der Account in der Collection einen **Verzeichnis-Scope** hat, gleich
dem Scope oder darunter; nie an die Wurzel einer Collection. Die Scopes fragt `push` vor dem
ersten Vorgang beim Node ab (`whoami`, auch mit `--dry-run`); jedes andere Ziel ist ein falscher
Aufruf (Exit 2, nichts geschrieben), die Meldung nennt die freigegebenen Verzeichnisse und
`hub account grant … --dir <pfad>`. Der Node kennt die Rechte im Stand des letzten Abgleichs:
Nach einer neuen Freigabe oder einem Entzug erst `kephalaion node sync <hub>` (oder den Abgleich
im Hintergrund von `serve` abwarten). Der Hub prüft jeden Vorgang trotzdem selbst — die Sperre
in der Kommandozeile ist ein Schutz vor Versehen, keine Grenze am Hub. Weil der Scope additiv
ist, können andere mit `write` im freigegebenen Verzeichnis anlegen und ändern; das verliert
der nächste `push` ohne Warnung.

```sh
kephalaion hub account grant alice team-x --write --dir test-docs   # am Hub; grant setzt vollständig
kephalaion node sync privat                                          # am Node: Rechte in die Replica
kephalaion node dir push privat:team-x test-docs ./docs
```

Vorab liest `push` den ganzen Ordner ein: Jede Datei muss UTF-8 ohne NUL und höchstens 1 MiB
sein und einen gültigen Namen haben, sonst bricht `push` mit allen Treffern ab, ohne zu
schreiben — mit `--exclude glob` (wiederholbar, auf Namen jeder Ebene) ausnehmen. Symlinks
werden übergangen und gemeldet, leere Ordner entstehen im Store nicht, `.git` bleibt in Quelle
und Ziel unberührt, ebenso jeder Treffer von `--exclude`. `pull` schreibt lokal nur, was
abweicht oder fehlt (neu `0644`, Ordner `0755`), löscht nur mit `--delete`, nie außerhalb des
Ordners und folgt keinem Symlink darin. `--dry-run` zeigt, was geschähe.

Abbrechen (SIGINT/SIGTERM) und `--timeout` wirken zwischen zwei Vorgängen: Der laufende geht
zu Ende, dann meldet die Kommandozeile, wie weit sie kam — erneut ausführen setzt fort.
Konflikte (`stale_revision`, `name_taken`, `path_conflict`, `not_found`) und ein unklarer
Ausgang werden gemeldet, nicht wiederholt; der Lauf geht weiter und endet unvollständig.
Exit-Codes: 0 fertig, 1 Fehler, 2 falscher Aufruf, 3 unvollständig. Die Adresse des Nodes
kommt aus `listen` der config oder `--node <url>` — auch ein Node hinter einem Proxy auf einem
anderen Rechner, `https://<name>/kephalaion` (Zertifikat gegen die System-Roots oder
`--ca-file`; `http` nur zu diesem Rechner und zu `host.docker.internal`) —, der Account aus `--account` oder der
einzigen Token-Datei unter `tokens/<hub>/`, das Token aus ihr, `--token-file` oder
`--token-stdin` — nie als Argument, nie in einer Ausgabe.

```text
+ vendor/k-playbook/rules/befunde.md
~ vendor/k-playbook/VERSION
push privat:team-x vendor/k-playbook/: 115 angelegt, 0 geändert, 0 gelöscht, 0 unverändert, 84 übergangen, 0 gemeldet, 1.671s
```

## VS Code

Die Erweiterung unter [`vscode/`](vscode/) zeigt Collections als Ordner im Explorer
(`keph://<hub>/<collection>/…`) und den Stand des Nodes in der Statusleiste. Sie spricht mit
dem Node über MCP wie jeder andere Client. Hintergrund: [`docs/vscode.md`](docs/vscode.md).

Noch nicht Teil der Installation; bis dahin aus dem Repository bauen, ohne Marketplace. Unter
WSL aus einem Terminal der WSL — dann landet sie im VS-Code-Server der WSL, wo sie laufen
muss (`extensionKind: workspace`):

```sh
make vscode-install   # baut dist/kephalaion-<version>.vsix und installiert sie mit code
```

Danach „Developer: Reload Window“. Nur bauen: `make vscode-vsix`. Braucht Node.js (`vsce`
kommt per `npx`). Auf einem SSH-Remote gehört sie ebenso dorthin, wo der Node läuft: dort aus
einem Terminal von VS Code installieren. In einem Devcontainer läuft sie im Container; wie sie
dort einen Node erreicht, ist noch nicht gebaut ([`docs/konzept.md`](docs/konzept.md),
„Devcontainer“).

- **Einrichtung braucht sie keine.** Die Adresse des Nodes liest sie aus `listen` im
  Abschnitt `node:` der config (Einstellung `kephalaion.nodeUrl` zum Überschreiben), Account
  und Token aus `~/.config/kephalaion/tokens/<hub>/<account>.token` — das Verzeichnis ist der
  Alias des Hubs, der Dateiname der Account. Liegen an einem Hub mehrere, wählt man einen
  mit „Kephalaion: Account wählen“; die Wahl steht in der Einstellung `kephalaion.accounts`,
  je Rechner. Ohne Wahl nimmt sie den ersten nach Namen und zeigt die Statusleiste gelb.
- **Statusleiste:** `Keph <hub>`, der Tooltip zeigt, was `whoami` liefert — Version, je Hub
  Anmeldung, Account, User, Collections mit Rechten und Stand des Abgleichs. Gelb, wenn der
  Node nicht erreichbar ist, eine Anmeldung nicht gilt oder kein Account gewählt ist;
  abgefragt alle 30 s.
- **Menü** per Klick auf die Statusleiste: Status anzeigen (dasselbe als Text im Output
  „Kephalaion“), neu verbinden, Account wählen, Collection einbinden, Log.
- **„Kephalaion: Collection einbinden“** bietet die lesbaren Collections zur Auswahl an und
  fügt die gewählte als Ordner in den Workspace ein. Verzeichnisse und Dokumente kommen über
  `list` und `read` aus der Replica; Änderungen erscheinen nach dem Abgleich des Nodes von
  selbst (`changes`, alle 3 s), ein Umbenennen als alter Name weg, neuer da.
- **MCP-Server für Copilot** (0.0.6, VS Code ab 1.101): Die Erweiterung meldet den Node als
  MCP-Server „Kephalaion“, mit den Header-Paaren aus den Token-Dateien; das Token setzt sie erst
  beim Start des Servers ein. Solange VS Code auf „Trace“ protokolliert, meldet sie ihn nicht
  (VS Code schriebe die Header sonst ins Log). Abschalten mit `kephalaion.mcpServer.enabled`;
  [`docs/vscode.md`](docs/vscode.md), „MCP-Server für Copilot“.
- **Ein Node auf einem anderen Rechner** (0.0.7): `kephalaion.nodeUrl` auf
  `https://<name>/kephalaion` (hinter einem Proxy; `http` nur zu diesem Rechner und zu
  `host.docker.internal`), dazu `kephalaion.hubs` mit den Aliasen der Hubs, deren Header-Paare
  dorthin gehen (etwa `["vm"]`; leer nur bei genau einem Hub unter `tokens/`).
  [`docs/vscode.md`](docs/vscode.md), „Ein Node auf einem anderen Rechner“.
- **Schreiben** (0.0.5) über die Werkzeuge oben: speichern, neue Datei und neuer Ordner,
  löschen, umbenennen und verschieben im Explorer, auch ganze Ordner und per Drag & Drop.
  Schreibbar ist eine Collection mit `write`; ein fremdes Dokument ohne `supersede` scheitert
  erst beim Speichern, ein Account mit `supersede`, aber ohne `write` sieht die Collection
  schreibgeschützt. Nur Text (UTF-8 ohne NUL-Byte) bis 1 MiB; umbenennen und verschieben nur
  innerhalb einer Collection, ohne ein belegtes Ziel zu überschreiben; ein leerer Ordner besteht
  nur in diesem Fenster, bis darin etwas liegt. Ein Konflikt wird abgelehnt; ist der Hub nicht
  erreichbar, wird nichts gespeichert. Einzelheiten und was im echten VS Code noch zu prüfen
  ist: [`docs/vscode.md`](docs/vscode.md), „Umsetzung: Schreiben“.

## Bauen

Go in der Version aus der `toolchain`-Zeile von `go.mod`; ein älteres Go lädt sie selbst
nach.

```sh
make check          # gofmt, go vet, alle Tests, Syntax von install.sh
make check-quick    # dasselbe ohne die langsamen Tests (go test -short), für Zwischenstände
make dist           # alle vier Plattformen und SHA256SUMS nach dist/
make dev-install    # diese Plattform bauen, ~/.local/bin/kephalaion ersetzen, laufenden Dienst neu starten
make vscode-install # VS-Code-Erweiterung bauen und mit code installieren (braucht Node.js)
make                # alle Targets
```

CI prüft jeden Push auf `dev` und `main` auf Linux (samt Cross-Build aller vier Plattformen
und shellcheck): `dev` nur mit den schnellen Tests (`make check-quick`), `main` und Pull
Requests vollständig (`make check`). Ein Job für macOS (Tests, LaunchAgent mit
`plutil -lint`, `install.sh`) ist vorhanden, aber seit 2026-09-26 abgeschaltet (`if: false` in
`.github/workflows/ci.yml`).

`main` ist der Standard-Branch und trägt nur veröffentlichte Stände; ein Clone bekommt den
Release-Stand. Gearbeitet wird auf `dev` — nach dem Klonen `git switch dev`.

Ein Release entsteht mit `make -C k-playbook-local release VERSION=vX.Y.Z`: Es prüft `dev`
(gepusht, ein vollständiger CI-Lauf für genau diesen Stand grün — fehlt er, stößt es ihn an
und wartet darauf), schiebt `main` per Fast-Forward darauf und pusht den Tag. Aus dem Tag
`v*` baut `.github/workflows/release.yml` das Release und veröffentlicht es mit den Binaries,
`SHA256SUMS` und `install.sh`.

## Lizenz

Apache-2.0, siehe [`LICENSE`](LICENSE).
