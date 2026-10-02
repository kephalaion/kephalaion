---
title: Kephalaion — Installation und Betrieb
description: Wie Kephalaion ohne Clone installiert, als Dienst betrieben und aktualisiert wird — pro User (Linux, macOS) und global für alle User eines Linux-Rechners, von Hand, mit Ansible oder durch eine KI.
---

# Installation und Betrieb

Diese Anleitung gilt für Menschen, für Ansible und für eine KI gleichermaßen: Jeder Schritt
steht mit seinem Befehl da, die festen Angaben für eine Automatisierung stehen gesammelt im
letzten Abschnitt. Die Entscheidungen dahinter stehen in [`konzept.md`](konzept.md),
„Installation und Betrieb“, die Begriffe in [`begriffe.md`](begriffe.md).

Diese Datei beschreibt immer das Release, zu dem sie gehört:
`https://raw.githubusercontent.com/kephalaion/kephalaion/main/docs/installation.md` ist die
Anleitung zum neuesten Release (`main` trägt nur veröffentlichte Stände), mit einem Tag statt
`main` die zu genau dieser Version, etwa `…/kephalaion/v0.2.0/docs/installation.md`.

## Überblick

Es gibt zwei Arten, Kephalaion zu betreiben, und **je Rechner genau eine**: Beide wollten
dieselben Ports, und ein Client wüsste nicht, welchen Node er meint. Keine braucht Git oder Go;
Binary und `SHA256SUMS` kommen aus dem Release.

| | pro User | global |
|---|---|---|
| Für | einen Menschen auf seinem Rechner | alle User eines Rechners |
| Plattform | Linux, macOS | nur Linux, mit systemd |
| Binary | `~/.local/bin/kephalaion`, gehört dem User | `/usr/local/bin/kephalaion`, root, `0755` |
| Installiert durch | `install.sh` | Ansible oder der Verwalter, nach dieser Anleitung |
| Läuft als | der User | Systembenutzer `kephalaion` |
| config | `~/.config/kephalaion/config.yaml` | `/etc/kephalaion/config.yaml`, `0644` |
| Verzeichnis der config | gehört dem User | `/etc/kephalaion/`, gehört `kephalaion`, `0755` |
| Daten | `~/.local/share/kephalaion/`, `0700` | `/var/lib/kephalaion/`, gehört `kephalaion`, `0700` |
| Dienst | `kephalaion service install`: systemd `--user` bzw. LaunchAgent | System-Unit aus `kephalaion service unit --system` |
| Log | `journalctl --user -u kephalaion`, auf macOS `~/.local/state/kephalaion/serve.log` | `journalctl -u kephalaion` |
| Upgrade | der User: `kephalaion upgrade` | der Verwalter: Ansible oder `sudo kephalaion upgrade && sudo systemctl restart kephalaion` |

Pro User folgen die Orte `XDG_CONFIG_HOME`, `XDG_DATA_HOME` und `XDG_STATE_HOME`, auf macOS
ebenso. Global sind die Orte fest.

**Die config wird ohne Angabe gefunden**, in dieser Reihenfolge: `--config`,
`KEPHALAION_CONFIG`, die config des Users (wenn es sie gibt), `/etc/kephalaion/config.yaml`
(wenn es sie gibt), sonst der Ort des Users — dort legt `init` sie an. `kephalaion status`
zeigt, welche gilt und woher (`Quelle:`). Gibt es die config des Users und die globale
nebeneinander, meldet `status` zwei Arten auf einem Rechner als Fehler; der Weg ist, die
Installation pro User zu entfernen (unten).

**Global ist bisher nur für User auf dem Rechner selbst gebaut:** Der Node lauscht auf
Loopback (`127.0.0.1:7433`), das teilen alle User eines Rechners. Devcontainer erreichen ihn
noch nicht, weder global noch pro User; die beiden vorgesehenen Wege stehen in `konzept.md`,
„Devcontainer“.
Der Hub dagegen ist von anderen Rechnern erreichbar — über einen Reverse-Proxy auf seinem
Rechner, siehe „Hub für Nodes anderer Rechner“. Über denselben Proxy auch der MCP-Eingang des
Nodes, für Clients ohne eigenen Node (seit Task 023), siehe „Node für Clients anderer
Rechner“.

## Pro User

### Installieren

```sh
curl -fsSL https://github.com/kephalaion/kephalaion/releases/latest/download/install.sh | sh
```

Das Skript lädt das Binary der Plattform und `SHA256SUMS` aus dem neuesten Release, prüft die
Prüfsumme und legt das Binary atomar nach `~/.local/bin/kephalaion`. Liegt `~/.local/bin`
nicht im `PATH`, nennt es die Zeile fürs Shell-Profil. Eine bestimmte Version, ohne Frage an
die GitHub-API:

```sh
curl -fsSL https://github.com/kephalaion/kephalaion/releases/latest/download/install.sh | KEPHALAION_VERSION=v0.2.0 sh
```

**macOS:** Nur über `curl` bzw. `install.sh` laden, nicht über den Browser — eine im Browser
geladene Datei trägt die Quarantäne-Markierung, und macOS verweigert das nicht notarisierte
Binary. `~/.local/bin` steht auf macOS meist nicht im `PATH`; `install.sh` nennt die Zeile
für `~/.zshrc`.

### Rollen einrichten

Hub und Node werden je mit einem Aufruf eingerichtet, ohne Rückfragen und nie überschreibend
— Einzelheiten im [README](../README.md), „Einrichten“:

```sh
kephalaion node init
kephalaion hub init     # nur, wenn dieser Rechner auch den Hub trägt
```

Gibt es auf dem Rechner die globale config, bricht `init` ohne `--config` ab: Kephalaion ist
dann global eingerichtet.

### Dienst

```sh
kephalaion service install
kephalaion service status
kephalaion status          # Zeile „Dienst:“
```

`service install` schreibt unter Linux die Benutzer-Unit
`~/.config/systemd/user/kephalaion.service` (bzw. unter `$XDG_CONFIG_HOME`) und ruft
`systemctl --user daemon-reload` und `systemctl --user enable --now kephalaion.service`. Die
Unit startet genau dieses Binary mit `serve` (absoluter Pfad), dazu `--config`, wenn die config
nicht unter `~/.config/kephalaion/config.yaml` liegt; `Restart=on-failure`, `RestartSec=5s`,
`WantedBy=default.target`. Auf macOS schreibt es den LaunchAgent
`~/Library/LaunchAgents/io.github.kephalaion.plist` (`RunAtLoad`, `KeepAlive` bei erfolglosem
Ende, Log nach `~/.local/state/kephalaion/serve.log`) und lädt ihn mit `launchctl bootstrap
gui/<uid>`. Läuft der Dienst schon, schreibt `install` neu und startet ihn neu.
`kephalaion service unit` zeigt, was `install` schreiben würde, ohne etwas zu schreiben.

`service install` bricht ab,

- wenn Kephalaion auf dem Rechner global eingerichtet ist;
- ohne systemd (siehe unten);
- wenn keine Rolle eingerichtet ist;
- wenn `kephalaion serve` schon von Hand läuft (Sperre `<db>.lock`) — erst dieses `serve`
  beenden, sonst startete der Dienst immer wieder und scheiterte an der Sperre.

`init` richtet keinen Dienst ein; es nennt am Ende den nächsten Schritt.

**Linger:** Ohne Linger laufen die Dienste eines Users nur, solange er angemeldet ist. Für
den Node reicht das. Für einen Hub, den andere Rechner erreichen sollen, nicht — dann
einmal `loginctl enable-linger` (braucht auf manchen Systemen `sudo`). `service install`
schaltet es nicht selbst ein, nennt es aber, wenn ein Hub eingerichtet ist.

**WSL:** Der Dienst braucht systemd in der WSL (`[boot]` `systemd=true` in `/etc/wsl.conf`).
Die WSL fährt ohne offenes Terminal bzw. VS Code herunter; ein laufender Dienst hält sie
nicht wach.

Log: `journalctl --user -u kephalaion` (Linux), `~/.local/state/kephalaion/serve.log` (macOS).

Exit-Code von `service install`: 0, wenn der Dienst eingerichtet ist und läuft; 1, wenn er
abbricht oder der Dienst danach nicht läuft (dann steht der Zustand da, etwa `activating`,
und der Verweis aufs Log); 2 bei falschem Aufruf. `service status`: 0, wenn die Abfrage
gelang — ob eingerichtet und laufend, steht in der Ausgabe —, 1 ohne systemd oder bei einem
Fehler von `systemctl` bzw. `launchctl`.

### Upgrade

```sh
kephalaion upgrade --check          # nachsehen
kephalaion upgrade                  # auf das neueste Release
kephalaion upgrade --version v0.2.0 # genau diese Version, auch zurück; nötig für einen dev build
```

`upgrade` prüft die Prüfsumme gegen `SHA256SUMS` und ersetzt das Binary atomar; scheitert
etwas, bleibt das alte unverändert. Danach startet es einen laufenden Dienst pro User neu
(`systemctl --user restart kephalaion.service` bzw. `launchctl kickstart -k
gui/<uid>/io.github.kephalaion`). Was `--check` meldet, steht unten unter „Upgrade: was
Kephalaion meldet“.

### Installation pro User entfernen

Nötig auch, bevor der Rechner global eingerichtet wird:

```sh
kephalaion service uninstall                       # Dienst anhalten und entfernen
kephalaion config export --output ~/keph-config.yaml   # wahlweise: Einstellungen sichern (enthält Tokens, 0600)
rm ~/.config/kephalaion/config.yaml                # die config — danach gilt sie nicht mehr
rm -r ~/.local/share/kephalaion                    # Datenbanken und Replicas, wenn sie weg sollen
rm ~/.local/bin/kephalaion                         # das Binary
```

Die Token-Dateien unter `~/.config/kephalaion/tokens/` gehören dem User als Client; sie
können bleiben — auch bei einer globalen Installation meldet sich der User mit seinen Tokens an.

## Global (Linux)

Ein Dienst für alle User eines Rechners, unter dem Systembenutzer `kephalaion`. Die User
betreiben nichts; sie sind Clients mit je einem Account und seinem Token.

**Voraussetzungen:** Linux mit systemd. Eine Installation pro User auf dem Rechner vorher
entfernen (oben) — Kephalaion prüft das bei der globalen Einrichtung nicht; `kephalaion
status` meldet es danach als Fehler.

### Schritte von Hand, in dieser Reihenfolge

```sh
# 1. Binary mit Prüfsumme
VERSION=v0.2.0                                   # das gewünschte Release
case "$(uname -m)" in x86_64) ARCH=amd64 ;; aarch64) ARCH=arm64 ;; esac
BASE="https://github.com/kephalaion/kephalaion/releases/download/$VERSION"
cd "$(mktemp -d)"
curl -fsSLO "$BASE/kephalaion-linux-$ARCH"
curl -fsSLO "$BASE/SHA256SUMS"
sha256sum --check --ignore-missing SHA256SUMS    # muss „OK“ melden
sudo install -o root -g root -m 0755 "kephalaion-linux-$ARCH" /usr/local/bin/kephalaion

# 2. Systembenutzer
sudo useradd --system --user-group --home-dir /var/lib/kephalaion --no-create-home \
  --shell /usr/sbin/nologin kephalaion

# 3. Verzeichnisse und Rechte
sudo install -d -o kephalaion -g kephalaion -m 0755 /etc/kephalaion
sudo install -d -o kephalaion -g kephalaion -m 0700 /var/lib/kephalaion

# 4. Rollen einrichten, als Systembenutzer, immer mit --config und --db
sudo -u kephalaion kephalaion node init --config /etc/kephalaion/config.yaml \
  --db sqlite:///var/lib/kephalaion/node.db
sudo -u kephalaion kephalaion hub init --config /etc/kephalaion/config.yaml \
  --db sqlite:///var/lib/kephalaion/hub.db       # nur, wenn dieser Rechner auch den Hub trägt

# 5. System-Unit aus dem Binary, einschalten, starten
kephalaion service unit --system | sudo tee /etc/systemd/system/kephalaion.service >/dev/null
sudo systemctl daemon-reload
sudo systemctl enable --now kephalaion
systemctl status kephalaion
```

**Rechte:** `/etc/kephalaion/` gehört `kephalaion` (`0755`), die config darin ist `0644` —
für alle lesbar, sie enthält kein Geheimnis und sagt, wo der Node lauscht. So schreibt `init`
sie als Systembenutzer. Der laufende Dienst schreibt dort nie: Die System-Unit hat
`ProtectSystem=strict`, schreibbar ist für ihn nur `/var/lib/kephalaion`. Die config ändert
nur, wer als Systembenutzer ein Kommando aufruft (`sudo -u kephalaion kephalaion …`).
`/var/lib/kephalaion/` ist `0700` und gehört `kephalaion`; die System-Unit hält das mit
`StateDirectory=kephalaion` und `StateDirectoryMode=0700`.

**Die System-Unit** (`kephalaion service unit --system`) startet `/usr/local/bin/kephalaion
serve` als `User=`/`Group=kephalaion` mit `KEPHALAION_CONFIG=/etc/kephalaion/config.yaml`,
`Restart=on-failure`, gehärtet mit `NoNewPrivileges`, `ProtectSystem=strict`, `ProtectHome`
und `PrivateTmp`, `WantedBy=multi-user.target`. Das Binary erzeugt sie, damit sie zu seiner
Version passt; `service unit --system` schreibt nichts.

### Hub-Einträge und Accounts, von Hand

Tokens gehen nie durch Ansible. Die Hub-Einträge des Nodes und das erste `rotate` jedes
Accounts richtet der Verwalter von Hand ein, als Systembenutzer. Ohne `--config` findet
`sudo -u kephalaion kephalaion …` die globale config selbst.

```sh
K="sudo -u kephalaion kephalaion"

# Trägt dieser Rechner auch den Hub: Transport local, der Node legt sich am Hub selbst an.
$K node hub add team --node server --transport local --create
$K hub collection add wissen
$K hub node grant server wissen
$K node collection add team:wissen

# Je User ein Account; das Einrichtungstoken zeigt der Hub genau einmal.
$K hub account add alice --user alice
$K hub account grant alice wissen --write

# Erstes rotate als Systembenutzer, über eine Datei nur für ihn …
DIR="$(sudo -u kephalaion mktemp -d)"
sudo -u kephalaion sh -c "umask 077; cat > $DIR/alice.token"   # Einrichtungstoken einfügen, Enter, Strg-D
$K node account rotate team alice --token-file "$DIR/alice.token"
# … und das neue Token dem User übergeben, am üblichen Ort.
sudo -u alice install -d -m 0700 ~alice/.config/kephalaion/tokens ~alice/.config/kephalaion/tokens/team
sudo install -o alice -g alice -m 0600 "$DIR/alice.token" ~alice/.config/kephalaion/tokens/team/alice.token
sudo rm -r "$DIR"
```

Liegt der Hub auf einem anderen Rechner, trägt `node hub add` ihn mit Transport `https`,
Adresse, gegebenenfalls `--ca-file` und dem Token des Nodes ein („Hub für Nodes anderer
Rechner“ unten; README, „Hub und Node auf einem Rechner“); das Token kommt über
`--token-stdin`, nie als Argument.

### Verwalten

Verwaltet wird als Systembenutzer: `sudo -u kephalaion kephalaion status`, `… hub account
…`, `… config set node sync_interval 1m` usw. Ein anderer User sieht mit `kephalaion
status` die globale config (`Quelle: global`), den Zustand der System-Unit und, statt eines
Fehlers an der Datenbank, den Hinweis auf die globale Installation (Exit-Code 0).
`kephalaion init` und `kephalaion service install` brechen für ihn ab.

Die User melden sich am Node wie pro User an: je Hub ein Header-Paar
(`X-Keph-Account-<hub>`, `X-Keph-Token-<hub>`) an `http://127.0.0.1:7433/mcp`, siehe README,
„serve und MCP“. Bei ihren KI-Assistenten tragen sie den Node selbst ein
(`kephalaion node mcp add`, „Bei den Assistenten anmelden“ unten).

## Bei den Assistenten anmelden

Damit die KI-Assistenten des Users die Werkzeuge des Nodes sehen, trägt Kephalaion den Node bei
ihnen als MCP-Server ein — ein Eintrag `kephalaion` je Assistent auf User-Ebene, für alle Hubs,
ohne das Token im Klartext ([`konzept.md`](konzept.md), „Bei den Assistenten angemeldet“):

```sh
kephalaion node mcp status       # je Assistent: eingetragen, fehlt, weicht ab
kephalaion node mcp add          # bei allen gefundenen eintragen (Claude Code, OpenCode, Codex)
kephalaion node mcp add --assistant codex --dry-run   # nur melden, was geschähe
kephalaion node mcp remove --assistant opencode       # nur diesen Eintrag entfernen
```

| Assistent | Eintrag | woher das Token kommt |
|---|---|---|
| Claude Code | `mcpServers.kephalaion` in `~/.claude.json` (über `claude mcp add-json --scope user`) | der Helfer `kephalaion node mcp headers` bei jeder Verbindung |
| OpenCode | `mcp.kephalaion` in `~/.config/opencode/opencode.json(c)` (über `opencode mcp add`) | `{file:~/.config/kephalaion/tokens/<hub>/<account>.token}` |
| Codex | `[mcp_servers.kephalaion]` in `~/.codex/config.toml` (bzw. `$CODEX_HOME`) | der Helfer, wie bei Claude Code |
| VS Code (Copilot) | die Erweiterung für VS Code meldet den Node selbst ([`vscode.md`](vscode.md), „MCP-Server für Copilot“) | aus den Token-Dateien, erst beim Start des Servers |

Der Helfer steht mit absolutem Pfad im Eintrag (`/home/<user>/.local/bin/kephalaion node mcp
headers --tokens-dir … --account <hub>=<account>`): Assistenten aus einer GUI erben kein
`PATH`, und Codex leert die Umgebung. Alles andere in den Dateien bleibt, auch Kommentare.
Gefunden werden die Assistenten über `PATH`; ein nicht gefundener wird übergangen und genannt.

**Wann eingetragen wird:**

- **Von Hand** mit `kephalaion node mcp add` — bei allen gefundenen Assistenten (oder den mit
  `--assistant` genannten), jederzeit wiederholbar; ein richtiger Eintrag bleibt unverändert.
- **Automatisch** am Ende von `install.sh` (`kephalaion node mcp add --auto`, bei einer
  Erstinstallation ohne Wirkung) und nach `kephalaion node account rotate` bzw. `check`, wenn
  die Token-Datei unter `~/.config/kephalaion/tokens/` liegt. Diese Anstöße ändern nur
  Assistenten, die schon einen Eintrag haben; hat noch keiner einen, tragen sie überall ein —
  so entsteht der Eintrag mit dem ersten Account von selbst.
- Ein `remove --assistant <name>` hält gegen die automatischen Anstöße, **nicht** gegen ein
  `add` von Hand ohne `--assistant`. Nach `remove` bei allen tragen die Anstöße wieder überall
  ein.
- **Bekannte Grenzen:** Ein später installierter Assistent bekommt den Eintrag nur über `add`
  von Hand. Ebenso OpenCode, nachdem `add` seinen Eintrag entfernt hat, weil keine Token-Datei
  mehr da war — ein `{file:…}`-Verweis auf eine fehlende Datei machte die ganze config von
  OpenCode ungültig, deshalb nimmt `add` nur vorhandene Token-Dateien auf und bereinigt tote
  Verweise; `status` warnt.

**Mehrere Accounts an einem Hub:** `add` nimmt je Hub die einzige Token-Datei. Liegen mehrere
da, wählt `--account <hub>=<account>` (wiederholbar); die Wahl steht danach ausdrücklich im
Eintrag und bleibt bei jedem weiteren `add`, solange ihre Datei da ist. Ohne Wahl wird der Hub
übergangen und genannt (Exit 1), die übrigen werden eingetragen.

**Nach `rotate`** stimmt der Eintrag ohne Zutun — der Helfer bzw. der Verweis liest die Datei —,
wirksam aber erst mit einer neuen Sitzung des Assistenten bzw. einer Neuverbindung (in VS Code:
den Server „Kephalaion“ neu starten).

**Nach einem Update** des Binarys (`upgrade`, `make dev-install`) jeden Assistenten neu
verbinden: eine neue Sitzung in Claude Code, OpenCode und Codex (bzw. dort den MCP-Server neu
starten), in VS Code „Developer: Reload Window“. Ein Assistent liest die Werkzeuge samt
Output-Schema beim Verbinden. Ändert ein Update ein Schema, kann eine laufende Sitzung die
Antworten gegen das alte prüfen und ablehnen: Die Schemas sind streng
(`additionalProperties: false`), seit Task 024 trägt etwa `read` das Feld `content`.

**Global** trägt jeder User für sich ein, Ansible trägt nichts ein: Das erste `rotate` des
Verwalters als Systembenutzer schreibt in eine Datei außerhalb des eigenen `tokens/` und stößt
nichts an. Nach der Übergabe der Token-Datei (oben, „Hub-Einträge und Accounts, von Hand“) ruft
der User selbst auf:

```sh
kephalaion node mcp add
```

**`node mcp headers` ist für die Assistenten, nicht für Menschen:** Es ist die einzige Ausgabe
mit Token und schreibt deshalb nicht in ein Terminal. Die Shell eines KI-Agenten ist aber kein
Terminal — ein Agent ruft es nie gegen echte Token-Dateien so auf, dass die Ausgabe bei ihm
ankommt; höchstens die Schlüssel: `kephalaion node mcp headers | jq -r 'keys[]'`.

Exit-Codes von `add`, `remove` und `status`: 0 fertig, 1 Fehler bei einem Assistenten oder ein
übergangener Hub, 2 falscher Aufruf.

## Hub für Nodes anderer Rechner: hinter einem Reverse-Proxy

Der Hub lauscht nur auf Loopback und spricht kein TLS — das bleibt so. Ein Node auf einem
anderen Rechner erreicht ihn über einen **Reverse-Proxy auf dem Rechner des Hubs** (hier
Caddy), der nach außen `https` spricht, TLS beendet und `/kephalaion/hub/*` an
`localhost:7434` weiterreicht. Der Hub braucht dafür keine Änderung und keine Einstellung:
Der Proxy nimmt nur seinen Präfix `/kephalaion` weg — das Binary ordnet seine Teile selbst,
der Hub-Listener bedient den Vertrag unter `/hub/` und antwortet an seiner Wurzel einem
Browser mit der Weboberfläche, allem anderen mit einer Begrüßung — und setzt `Host` auf die
Loopback-Adresse des Hubs, damit dessen Host-Prüfung
gilt (entschieden am 2026-09-28 und 2026-09-29, [`konzept.md`](konzept.md),
„Kommunikation“). Die Adresse für Nodes ist `https://<name>/kephalaion/hub`. Die Identität
bleibt das Token; TLS verschlüsselt und weist den Server aus. Ein Node, dessen
Zertifikatsprüfung scheitert, schickt kein Token.

### Caddyfile

`/etc/caddy/Caddyfile` (apt-Paket `caddy`, eigene Unit, bindet 443 selbst — die Unit trägt
`CAP_NET_BIND_SERVICE`). Vorlage für Weg 2 (nur IP, Caddys eigene CA); für Weg 1 steht der
Name statt der IP, und die Zeile `tls internal` entfällt:

```text
https://9.141.8.157 {
    tls internal
    handle /kephalaion/hub/* {
        uri strip_prefix /kephalaion
        request_body {
            max_size 8MiB
        }
        reverse_proxy localhost:7434 {
            header_up Host {upstream_hostport}
        }
    }
    handle {
        respond 404
    }
    log {
        output file /var/log/caddy/kephalaion.log
    }
}
```

- Nur `/kephalaion/hub/*` geht zum Binary, alles andere ist 404 — der Proxy zeigt nach außen
  nichts vom Hub, was der Vertrag nicht kennt, außer dem kurzen Text an `/kephalaion/hub/`
  (ohne Version). `uri strip_prefix /kephalaion` nimmt nur den Präfix des Proxys weg; der
  Hub-Listener sieht `/hub/v1/…` und bedient den Vertrag dort. Der Node trägt die Adresse
  mit `/hub` am Ende ein (`--address https://<name>/kephalaion/hub`), der Client hängt
  `/v1/<vorgang>` an. Ein anderer Präfix als `/kephalaion` geht ebenso; `/hub` gehört dem
  Binary.
- **Option — der Rest von `/kephalaion/*` nach außen: die Weboberfläche.** Unter
  `https://<name>/kephalaion/` liegt dann die Weboberfläche des Hubs (seit Task 020; `curl`
  bekommt an derselben Stelle die Begrüßung), ihre Teile unter `/kephalaion/gui/…`. Wer sie
  zeigen will, stellt eine Anmeldung davor (`forward_auth` auf einen
  Auth-Dienst): ein zweiter Block `handle /kephalaion/*` mit `forward_auth`, ebenfalls `uri
  strip_prefix /kephalaion` und demselben `reverse_proxy`, dazu `redir /kephalaion
  /kephalaion/ 308` vor beiden — die Seite nennt ihre Teile relativ (`gui/app.js`,
  `gui/api/whoami`) und braucht den Schrägstrich am Ende. Der eine Block deckt Seite und
  Teile; mehr braucht der Proxy nicht. Der Hub-Block bleibt ohne Anmeldung (Nodes sind Maschinen
  und weisen sich per Token aus) und greift zuerst — Caddy sortiert gleiche Direktiven nach
  Pfadlänge, deshalb `handle` statt `handle_path` in beiden Blöcken. Die Anmeldung davor ist
  Pflicht: Begrüßung und Seite nennen die Version, und der Eingang der Seite prüft
  Account-Token, ohne Fehlversuche zu begrenzen. Ohne Sitzung antwortet dann dort der
  Auth-Dienst (302 oder 401), nie das Binary — Proben gegen die Wurzel gehen nur auf dem
  Rechner des Hubs (unten).
  - **Zwei Anmeldungen.** Die des Proxys lässt zur Seite; die Seite fragt danach den
    Kephalaion-Account und sein Account-Token ab (das aus
    `~/.config/kephalaion/tokens/<hub>/<account>.token`, beginnt mit `keph_`) — nicht das
    Passwort der Anmeldung davor und nicht das Token eines Nodes — und zeigt, worauf der
    Account Zugriff hat. Das Token geht nur im Body eines `POST` an `gui/api/whoami`, wird
    nicht gespeichert und steht nie im Log; was nicht wie ein Token aussieht, schickt die
    Seite nicht ab.
  - **Ein falsches Token ist eine 401 des Hubs** (JSON mit `code` `unauthenticated`,
    „Account oder Token stimmt nicht“ — dieselbe Antwort für unbekannt, falsch und gesperrt).
    Zählt der Proxy 401 (fail2ban, „Bekannte Grenze“), zählt sie mit; die Seite warnt davor.
  - **Läuft die Sitzung des Proxys ab**, während die Seite offen ist, antwortet auf den
    `POST` der Auth-Dienst statt des Hubs (authproxy: 401 `text/plain`; Caddy reicht sie
    durch). Die Seite erkennt das am fehlenden JSON mit `code` und sagt „Deine Anmeldung an
    dieser Seite ist abgelaufen — Seite neu laden und neu anmelden“, ohne zum Wiederholen
    einzuladen: Auch diese 401 zählt für fail2ban, obwohl das Token stimmt.
  - **Proben.** Im Browser mit Sitzung `https://<name>/kephalaion/` → die Seite; ein
    Account-Token → die Übersicht. Auf dem Rechner des Hubs: `curl -H 'Accept: text/html'
    http://localhost:7434/` → die Seite (HTML), `curl http://localhost:7434/` → weiter die
    Begrüßung, `curl -X POST -H 'Content-Type: application/json' -d '{}'
    http://localhost:7434/gui/api/whoami` → 400 `invalid` („account fehlt“; keine 401). Von
    außen ohne Sitzung: `curl https://<name>/kephalaion/` → 302 des Auth-Dienstes. Im Log des
    Hubs (`journalctl -u kephalaion`) steht je Prüfung `hub POST /gui/api/whoami 200 …
    via=<adresse> account=<name>` — nie das Token.
- `header_up Host {upstream_hostport}` schickt `Host: localhost:7434`; genau das verlangt der
  Hub (`vertrag.md`, „Host“). Ohne die Zeile antwortet er 403, und `node hub check` sagt es.
- `request_body max_size 8MiB` liegt über den 7 MiB, die der Hub für einen Schreibvorgang
  annimmt; Caddys Zeitlimits lassen eine `sync`-Seite von bis zu 10 Minuten durch (Standard:
  keine Grenze für die Antwort des Upstreams). TLS mindestens 1.2 ist Caddys Standard.
- Das Zugriffslog ist die Grundlage für fail2ban (siehe „Bekannte Grenze“) — als Datei wie
  hier oder, ohne `output`, im Journal von `caddy.service`; der Filter muss zur Quelle passen.
  Nodes schicken ihr Token als `Authorization: Bearer`, das Caddy von sich aus schwärzt. Kommt
  der MCP-Eingang des Nodes dazu, gehören alle Request-Header aus dem Log, an zwei Stellen
  („Node für Clients anderer Rechner“).
- Prüfen vor dem Einspielen: `caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile`,
  danach `systemctl reload caddy`.
- **Proben.** Von außen, ohne Token: `curl https://<name>/kephalaion/hub/` → 200, `Kephalaion
  Hub. Nodes: POST /hub/v1/<vorgang>` (ohne Version); `curl -X POST
  https://<name>/kephalaion/hub/v1/whoami` → 401 in Vertragsform (`unauthenticated`, vom Hub —
  zählt bei einer Ratenbegrenzung des Proxys als Fehlversuch, also nicht wiederholen);
  `curl https://<name>/kephalaion/hub/nix` → 404 in Vertragsform (`invalid`) vom Hub; ohne
  die Option oben `curl https://<name>/kephalaion/` → 404 des Proxys. Auf dem Rechner des
  Hubs: `curl http://localhost:7434/` → Begrüßung mit Version (`Kephalaion <version>, Rolle
  hub.`; mit `-H 'Accept: text/html'` die Weboberfläche); `curl -X POST
  http://localhost:7434/v1/whoami` → 404 `text/plain` mit dem Hinweis
  „der Vertrag liegt unter /hub/v1/… — fehlt /hub am Ende der Adresse des Hub-Eintrags?“.
  Vom Node: `node hub check <alias>` — mit `/hub` „erreichbar“; ohne `/hub` „der Pfad ist
  nicht der Hub (HTTP 404: …): fehlt /hub am Ende der Adresse …“ (404 des Proxys oder der
  Wurzel), hinter der Anmeldung der Option „eine Anmeldung des Proxys, nicht der Hub (HTTP
  401: …): … oder fehlt /hub am Ende der Adresse?“. Kein Pfad des Binarys antwortet mit einer
  Umleitung.

### Zertifikat: zwei Wege

Der Code kann beide; welcher gilt, entscheidet der Betrieb.

1. **Mit Namen (empfohlen, sobald es einen gibt).** Ein DNS-Name auf der öffentlichen IP —
   in Azure ein DNS-Label (`<label>.<region>.cloudapp.azure.com`) oder ein eigener Name. Caddy
   holt das Zertifikat selbst bei Let's Encrypt: per HTTP-01 muss **Port 80 für alle offen**
   sein (nur für die Prüfung, dort gibt es keine Inhalte; auch bei jeder Verlängerung), oder
   per DNS-01 mit dem DNS-Plugin des Anbieters (nicht für `cloudapp.azure.com`, die Zone
   gehört Azure). Die Nodes brauchen keine CA: `node hub add … --address
   https://<name>/kephalaion/hub` ohne `--ca-file`, das Zertifikat gilt gegen die
   System-Roots. Nur mit dem Namen, nicht mit der IP — das Zertifikat gilt für den Namen.
2. **Ohne Namen (nur IP).** Let's Encrypt scheidet aus. Entweder Caddys eigene CA (`tls
   internal`: Caddy stellt auch für eine IP-Adresse ein Zertifikat aus) oder eine eigene,
   offline geführte CA mit einem Server-Zertifikat mit IP-SAN (`tls /etc/caddy/hub.crt
   /etc/caddy/hub.key`). Das CA-Zertifikat geht an jeden Node: bei `tls internal` liegt es
   auf dem Rechner des Hubs unter
   `/var/lib/caddy/.local/share/caddy/pki/authorities/local/root.crt`; der Node trägt es mit
   `--ca-file` ein (gespeichert wird der Inhalt, nicht der Pfad). Wechselt Caddy seine CA
   (Neuinstallation), müssen die Nodes die neue bekommen (`node hub set … --ca-file`).

### Freigabe der Ports

| Port | Für wen | Wofür |
|---|---|---|
| 443 | die Rechner der Nodes (Allowlist, wie bei SSH) | `https` zum Proxy |
| 80 | alle | nur bei Weg 1: die Prüfung durch Let's Encrypt |

In Azure geschieht das in der NSG vor der VM. Steht vor der VM ein Load Balancer, NAT oder
eine Firewall (der Metadatendienst nennt am NIC keine öffentliche IP), braucht der Port auch
dort eine Regel — auf der VM zu bestätigen.

### Node-Seite

```sh
# Am Hub: ein Node je Rechner; das Token geht per SSH zum Rechner des Nodes, nie als Argument.
kephalaion hub node add wsl-kleist
kephalaion hub node grant wsl-kleist test

# Am Node (Weg 1, Name):
read -rs TOKEN; printf '%s\n' "$TOKEN" | kephalaion node hub add vm --node wsl-kleist \
  --transport https --address https://hub.example.org/kephalaion/hub --token-stdin; unset TOKEN
# Am Node (Weg 2, IP mit CA):
printf '%s\n' "$TOKEN" | kephalaion node hub add vm --node wsl-kleist \
  --transport https --address https://9.141.8.157/kephalaion/hub --ca-file ~/vm-ca.pem --token-stdin

kephalaion node hub check vm          # erreichbar, hub_id, Node-Name, erlaubte Collections
kephalaion node collection add vm:test
kephalaion node sync vm
```

`node hub check` nennt, was schiefgeht: „Zertifikat von … nicht vertraut (Aussteller …;
--ca-file?)“, „Zertifikat gilt nicht für …“, „Zertifikat abgelaufen seit …“, „Proxy
antwortet, aber der Hub dahinter nicht (HTTP 502)“ (läuft `kephalaion serve`?),
„Host-Prüfung des Hubs schlägt fehl (HTTP 403)“ (fehlt `header_up Host`?), „der Pfad ist
nicht der Hub (HTTP 404)“ (fehlt `/hub` am Ende der Adresse, oder kennt der Proxy die Route
nicht? — der Text nach dem Status sagt, wer antwortete: die Wurzel des Binarys nennt `/hub`),
„eine Anmeldung des Proxys, nicht der Hub (HTTP 401)“ (die Route steht hinter
`forward_auth`, oder die Adresse ohne `/hub` landet in der Anmeldung vor dem Rest; eine
Weiterleitung zur Anmeldung meldet der Client als 3xx). Ein 404 ohne Vertragsform gilt auch
bei `rotate` und den Schreibvorgängen als „nicht erreicht“ (nichts geschehen), nicht als
unklar. Der Port 443 ist in der Adresse weglassbar. Am Hub steht jede Anfrage im Log mit der Adresse des Aufrufers aus
`X-Forwarded-For` (`via`), ohne Token; im Caddy-Log stehen 200 auf `/kephalaion/hub/v1/…` und 404 daneben.

### Ansible

Zu den Aufgaben oben kommen für den Rechner des Hubs (alles wiederholbar; die NSG liegt
außerhalb der VM, etwa in Terraform):

```yaml
- name: Reverse-Proxy vor dem Hub (Caddy)
  hosts: kephalaion
  become: true
  vars:
    caddy_site: "https://9.141.8.157"   # Weg 1: "https://hub.example.org"
    caddy_tls: "tls internal"           # Weg 1: ""; eigene CA: "tls /etc/caddy/hub.crt /etc/caddy/hub.key"
  tasks:
    - name: Caddy-Repo (Schlüssel und Quelle)
      ansible.builtin.deb822_repository:
        name: caddy-stable
        types: deb
        uris: https://dl.cloudsmith.io/public/caddy/stable/deb/debian
        suites: any-version
        components: main
        signed_by: https://dl.cloudsmith.io/public/caddy/stable/gpg.key

    - name: Caddy
      ansible.builtin.apt:
        name: caddy
        update_cache: true

    - name: Zertifikat und Schlüssel der eigenen CA
      ansible.builtin.copy:
        src: "{{ item }}"
        dest: "/etc/caddy/{{ item }}"
        owner: caddy
        group: caddy
        mode: "0600"
      loop: [hub.crt, hub.key]
      when: caddy_tls is search('^tls /')
      notify: caddy neu laden

    - name: Caddyfile
      ansible.builtin.template:
        src: Caddyfile.j2
        dest: /etc/caddy/Caddyfile
        owner: root
        group: root
        mode: "0644"
        validate: caddy validate --config %s --adapter caddyfile
      notify: caddy neu laden

    - name: Caddy läuft
      ansible.builtin.systemd:
        name: caddy
        enabled: true
        state: started

  handlers:
    - name: caddy neu laden
      ansible.builtin.systemd:
        name: caddy
        state: reloaded
```

`Caddyfile.j2` ist die Vorlage oben mit `{{ caddy_site }}` in der ersten Zeile und
`{{ caddy_tls }}` an der Stelle von `tls internal` (leer bei Weg 1). Bei `tls internal`
holt ein Lauf das CA-Zertifikat für die Nodes mit `ansible.builtin.slurp` von
`/var/lib/caddy/.local/share/caddy/pki/authorities/local/root.crt`; das Token je Node bleibt
Handarbeit (`hub node add`, Übergabe per SSH).

### Bekannte Grenze

Der Hub begrenzt Fehlversuche nicht ([`vertrag.md`](vertrag.md), „Bekannte Grenzen“) — weder
am Vertrag noch am Eingang der Weboberfläche; nach außen wird das dringlicher. Caddy hat ohne
Plugin keine Ratenbegrenzung. Übergang: fail2ban auf Caddys Zugriffslog, die Allowlist in der
NSG, und die Begrenzung am Hub als eigene Aufgabe.

So wie auf der Dev-VM eingerichtet, zählt fail2ban **jede 401 auf 80/443**, ohne Bindung an
einen Pfad: Der Filter liest das Journal von `caddy.service` (dort loggt Caddy ohne `output`;
ein eigenes Log gibt es nicht) und greift auf `"status":401`. Es zählen also gleich: das
abgewiesene Token eines Nodes am Hub (`/kephalaion/hub/v1/`), ein falsches Account-Token an der
Weboberfläche (`/kephalaion/gui/api/whoami`), ein falsches Passwort an der Anmeldung des
Proxys und ein `POST` ohne Sitzung — auch der einer Seite, deren Anmeldung abgelaufen ist.
10 in 10 Minuten sperren dort die Adresse für 80 und 443; 403 zählt nicht, 400 auch nicht.
Wer ins Log einer Datei schreibt wie im Caddyfile oben, richtet den Filter auf die Datei.
Fehlversuche am MCP-Eingang des Nodes sind kein 401; sie zählt eine eigene Jail auf dem Log
des Nodes („Node für Clients anderer Rechner“, „Fehlversuche“).

### Auf dem Rechner des Hubs zu bestätigen

Zwei Annahmen dieser Anleitung sind nur aus der Caddy-Dokumentation belegt, nicht im Betrieb:
dass `header_up Host {upstream_hostport}` beim Hub als `Host: localhost:7434` ankommt (sonst
403, siehe `node hub check`; auf der Dev-VM am 2026-09-29 bestätigt), und ob die öffentliche
IP über LB/NAT kommt und dort eine eigene Freigabe braucht. Seit Task 019 dazu: dass `uri
strip_prefix /kephalaion` nur den Präfix wegnimmt und der Hub `/hub/v1/…` sieht (sonst 404
mit dem Hinweis auf `/hub`, siehe „Proben“).

## Node für Clients anderer Rechner: MCP und Kommandozeile über https

Auch der MCP-Eingang des Nodes (`/mcp`) ist über denselben Reverse-Proxy erreichbar — für
Clients auf einem anderen Rechner, vor allem solche **ohne eigenen Node**: die Assistenten
(`node mcp add --node`), die Erweiterung für VS Code (`kephalaion.nodeUrl`) und die
Kommandozeile (`node dir push|pull --node`). Der Node lauscht weiter nur auf Loopback und
spricht kein TLS; TLS beendet der Proxy, und der Node prüft `Host` und `Origin` wie lokal.
Die **Adresse für Clients** ist `https://<name>/kephalaion` — die Basis ohne `/mcp`, die
Clients hängen `/mcp` an. Entschieden am 2026-10-01, gebaut in Task 023, zuerst auf der
Dev-VM ([`konzept.md`](konzept.md), „Entfernt: MCP über HTTPS“). Für Devcontainer ist dieser
Weg nicht gedacht ([`konzept.md`](konzept.md), „Devcontainer“). Auf einem Rechner mit eigenem
Node bleibt alles lokal wie bisher.

### Caddyfile

Die Vorlage aus „Hub für Nodes anderer Rechner“ mit dem Block für den Node und dem Filter
für das Log (Weg 1 mit Namen; Weg 2 wie oben mit IP und `tls internal`):

```text
{
    # Kein Request-Header im Log: auch Fehlerlog und reverse_proxy (Logger default).
    log default {
        format filter {
            wrap json
            fields {
                request>headers delete
            }
        }
    }
}

https://hub.example.org {
    # Zugriffslog (Grundlage für fail2ban), ohne Request-Header.
    log {
        format filter {
            wrap json
            fields {
                request>headers delete
            }
        }
    }
    handle /kephalaion/hub/* {
        uri strip_prefix /kephalaion
        request_body {
            max_size 8MiB
        }
        reverse_proxy localhost:7434 {
            header_up Host {upstream_hostport}
        }
    }
    handle /kephalaion/mcp {
        uri strip_prefix /kephalaion
        request_body {
            max_size 8MiB
        }
        reverse_proxy localhost:7433 {
            header_up Host {upstream_hostport}
        }
    }
    handle {
        respond 404
    }
}
```

- **Kein Token im Log des Proxys.** Caddy schreibt die Request-Header mit ins Log und schwärzt
  von sich aus nur `Authorization`, `Cookie`, `Proxy-Authorization` und `Set-Cookie`. Ein
  MCP-Client schickt sein Token aber als `X-Keph-Token-<alias>` — ohne Filter stünde es im
  Klartext im Log. Der Name hängt am Alias, ein Filter auf einzelne Header-Namen deckt das nicht
  ab; deshalb fallen alle Request-Header heraus (`request>headers delete`), und zwar an **zwei**
  Stellen: im `log` der Site (Zugriffslog) und als globale Option `log default`. Dorthin
  schreiben das Fehlerlog (ab Status 500, etwa 502, während Kephalaion neu startet) und
  `reverse_proxy` (wenn ein Stream abbricht) — beide mit dem ganzen Request samt Headern. Ein
  Filter nur auf `log` reicht also nicht. `remote_ip` und `status` bleiben; ein fail2ban-Filter
  auf 401 greift weiter. Mit `output file …` gilt dasselbe. Erst wenn der Filter ausgerollt und
  mit einem Dummy-Token geprüft ist, geht ein echtes Token durch den Proxy; steht doch eines im
  Log, wird es rotiert (`node account rotate`).
- **`X-Forwarded-For` ist Voraussetzung.** Daran erkennt der Node, dass eine Anfrage über den
  Proxy kam. Ohne gültige Anmeldung an mindestens einem Hub antwortet er dann **verdeckt**:
  keine Version (`initialize` nennt eine leere Version), kein `update`, keine Namen von Node und
  Hubs — er antwortet, als hätte er keinen Hub-Eintrag. Mit gültiger Anmeldung an einem Hub
  antwortet er wie lokal. Caddys `reverse_proxy` setzt den Header selbst und verwirft einen, den
  der Client mitschickt (solange `trusted_proxies` nicht gesetzt ist). Ein Proxy ohne diesen
  Header ließe jede Anfrage lokal aussehen: Der Node nennte dann jedem Version und Hubs, und
  Fehlversuche stünden ohne Adresse im Log.
- **Ohne Anmeldung des Proxys.** MCP-Clients können kein Formular; sie weisen sich je Hub mit
  `X-Keph-Account-<alias>` und `X-Keph-Token-<alias>` aus, wie Nodes am Hub mit ihrem Token.
  Nach außen ist `/kephalaion/mcp` so offen wie `/kephalaion/hub/`. Steht die Option mit
  `forward_auth` für den Rest von `/kephalaion/*` darin („Hub für Nodes anderer Rechner“), greift
  dieser Block zuerst (Caddy sortiert `handle` nach Pfadlänge).
- **Nur genau `/kephalaion/mcp`**, der Node sieht `/mcp`. Eine andere Adresse
  (`…/kephalaion/mcp/`, `…/kephalaion/x/mcp`, ohne Präfix) landet in `respond 404` bzw. in der
  Anmeldung der Option — dort zählt ein 401 für fail2ban. Die Kommandozeile und die Erweiterung
  erklären das als „Präfix falsch oder Anmeldung des Proxys“.
- `header_up Host {upstream_hostport}` wie beim Hub (sonst 403 der Host-Prüfung des Nodes);
  `request_body max_size 8MiB` über den 7 MiB, die `/mcp` annimmt. `Origin` reicht Caddy
  durch: Eine fremde `Origin` bekommt vom Node 403, wie lokal (MCP für Browser ist nicht
  vorgesehen).

### Fehlversuche: eine Jail auf dem Log des Nodes

Der Node antwortet bei falschem Token nie 401, sondern wie lokal mit einer Antwort des Werkzeugs
(`isError`) — ein 401 nähmen MCP-Clients als Beginn einer Anmeldung per OAuth, und lokal verlöre
`whoami` seine Hilfe bei falsch eingerichteten Clients. Ein fail2ban-Filter auf 401 im Log des
Proxys sieht davon also nichts. Stattdessen schreibt der Node je Anfrage eine Zeile; trägt die
Anfrage mindestens ein ungültiges Header-Paar (falsches Token, unbekannter oder gesperrter
Account), steht `login=invalid` direkt hinter `via`:

```text
2026-10-02T12:22:48+02:00 node POST /mcp 200 7.854ms via=80.131.88.238 login=invalid account=kamran-wsl
```

**Ein Fehlversuch ist eine Anfrage**, gleich wie viele Paare darin ungültig sind. Eine Anfrage
ohne Paar (`initialize` ohne Token) zählt nicht, ebenso eine nur mit Paaren zu Aliasen, die der
Node nicht kennt, und eine ohne `via` (lokal, nicht über den Proxy). Die Zeile lässt sich über
Header nicht fälschen: Namen stehen nur nach der Namensregel darin (sonst `(ungültig)`), `via`
und `login=invalid` an fester Stelle davor; `via` ist nur verlässlich, weil der Proxy einen
mitgeschickten `X-Forwarded-For` verwirft. Grenze: Ein Prozess auf dem Rechner des Nodes
erreicht ihn über Loopback ohne Proxy und kann den Header selbst setzen — so eine Zeile mit
fremder Adresse und deren Sperre erzeugen; wer dort arbeitet, gilt als vertrauenswürdig.

Filter und Jail für die globale Installation (System-Unit `kephalaion.service`, Log im
Journal), Schwelle und Fenster wie bei einer Jail für 401 im Log des Proxys:

```ini
# /etc/fail2ban/filter.d/kephalaion-mcp.conf
[Definition]
failregex = (?:^|\s)node [A-Z]+ /mcp \d{3} \S+ via=<ADDR> login=invalid(?:\s|$)
ignoreregex =
journalmatch = _SYSTEMD_UNIT=kephalaion.service

# /etc/fail2ban/jail.d/kephalaion.local (Auszug)
[kephalaion-mcp]
enabled  = true
backend  = systemd
filter   = kephalaion-mcp
port     = http,https
findtime = 10m
maxretry = 10
bantime  = 1h
```

Prüfen ohne Sperre: `fail2ban-regex systemd-journal /etc/fail2ban/filter.d/kephalaion-mcp.conf`
trifft nur Zeilen mit `via` und `login=invalid`; `fail2ban-client status kephalaion-mcp` zählt.
Der Filter ist nicht an der Zeit verankert: fail2ban liefert die Zeile aus dem Journal mit
eigenem Präfix (`<host> kephalaion[<pid>]: …`).

**Folgen einer Sperre.** fail2ban sperrt die ganze Adresse auf 80 und 443 — auch den Abgleich
eines Nodes vom selben Rechner mit dem Hub hinter demselben Proxy und die Weboberfläche. Die
eigene Adresse ist nicht ausgenommen, nur was die Jail ausdrücklich ausnimmt. Ein falsch
eingerichteter Assistent zählt schon beim Start einer Sitzung mehrere Fehlversuche
(`initialize`, `tools/list`, je eine Anfrage) und danach einen je Werkzeugaufruf: Nach wenigen
Sitzungen ist die Adresse gesperrt — ebenso mit einem veralteten Token, etwa einer Kopie nach
`rotate` am Original. Erkennbar an `login=invalid` im Log des Nodes; Abhilfe: das Token beim
Client erneuern, dann `fail2ban-client set kephalaion-mcp unbanip <adresse>` per SSH. Eine
Jail für 401 im Log des Proxys zählt getrennt.

### Proben

Von außen (ohne Token; nur Dummy-Tokens, solange der Filter nicht geprüft ist):

```sh
U=https://hub.example.org/kephalaion/mcp
H='-H Content-Type:application/json -H Accept:application/json,text/event-stream'
# initialize ohne Anmeldung: 200, "serverInfo":{"name":"kephalaion","version":""}
curl -sS $H -X POST "$U" --data '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"probe","version":"0"}}}'
# whoami ohne Paar: "hidden":true, leere hubs, keine Version
curl -sS $H -X POST "$U" --data '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"whoami","arguments":{}}}'
# fremde Origin: 403 vom Node (kein 401, zählt nirgends)
curl -sS -o /dev/null -w '%{http_code}\n' $H -H 'Origin: https://example.com' -X POST "$U" --data '{}'
```

Auf dem Rechner des Nodes zeigt dasselbe `whoami` an `http://127.0.0.1:7433/mcp` (ohne
`X-Forwarded-For`) alles wie bisher; `ss -ltn` zeigt den Node nur auf `127.0.0.1:7433`. Im Log
des Proxys steht kein `X-Keph-Token-…` — je Token gesucht, ohne es auszugeben:
`journalctl -u caddy | grep -c -F -f <token-datei>` → 0.

### Client-Seite

```sh
N=https://hub.example.org/kephalaion

# Kommandozeile: Ordner abgleichen über den entfernten Node
kephalaion node dir pull vm:test vendor/test-docs ./test-docs --node "$N"
kephalaion node dir push vm:test vendor/meins ./meins --node "$N"     # Ziel wie lokal (vendor/, Scope)

# Assistenten: Eintrag mit dieser Adresse, nur die gewählten Hubs
kephalaion node mcp add --node "$N" --hub vm
kephalaion node mcp status --node "$N" --hub vm   # eingetragen / weicht ab / fehlt
```

- `<hub>` und `--hub` nennen den **Alias des Hub-Eintrags am entfernten Node** (auf der VM
  `vm`); so heißt auch das Verzeichnis unter `tokens/`. Der Account kommt wie lokal aus
  `--account` oder der einzigen Token-Datei unter `~/.config/kephalaion/tokens/<alias>/`.
- `http` geht nur zu diesem Rechner (`localhost`, `127.0.0.1`, `[::1]`) und zu
  `host.docker.internal`, alles andere nur über `https` — sonst gingen Tokens im Klartext übers
  Netz. Keine Query, kein User in der Adresse, keiner Weiterleitung wird gefolgt.
- **Erst prüfen, dann eintragen.** Vor dem ersten Token fragen `node dir`, `node mcp add` und
  `status` den Node an: das Zertifikat gegen die System-Roots oder `--ca-file`, dann
  `initialize` ohne Token. Scheitert das, schreibt `add` nichts und sagt warum: Zertifikat
  nicht vertraut, für einen anderen Namen oder abgelaufen; Gegenseite ohne TLS; „Präfix falsch
  oder Anmeldung des Proxys“ (401, Weiterleitung, HTML); nicht erreichbar.
- **Wahl der Hubs** (`--hub <alias>`, wiederholbar): An eine entfernte Adresse gehen nur die
  Header-Paare der gewählten Hubs — sonst gingen Tokens von Hubs, die der entfernte Node nicht
  kennt, durch den Proxy. Die Wahl steht fest im Eintrag (beim Helfer als `--hub`, bei OpenCode
  als Verweise nur auf ihre Token-Dateien); ein Hub, der später unter `tokens/` hinzukommt,
  geht nicht mit. Ohne `--hub` nimmt `add` den Hub nur, wenn unter `tokens/` genau einer liegt.
- Die Erweiterung für VS Code folgt denselben Regeln: `kephalaion.nodeUrl` auf die Adresse,
  `kephalaion.hubs` als Wahl ([`vscode.md`](vscode.md)).

### Ein Client ohne eigenen Node

Ein Rechner, auf dem kein Node läuft, braucht nur das Binary (für `node mcp headers`, den
Helfer von Claude Code und Codex, und für `node dir`), einen Account am Hub und dessen Token.
Kein neues Kommando:

1. **Am Hub** ein eigener Account je Rechner (Schema `<user>-<rechner>`), mit Rechten:
   `hub account add alice-laptop --user alice`, `hub account grant alice-laptop wissen
   --write`. Das Einrichtungstoken zeigt der Hub genau einmal.
2. **Das erste `rotate` an einem Node**, der den Hub als Eintrag hat — etwa am Node des
   Hub-Rechners als Systembenutzer, wie unter „Hub-Einträge und Accounts, von Hand“ (`$K node
   account rotate team alice-laptop --token-file "$DIR/alice-laptop.token"`).
3. **Die Token-Datei übergeben**, per SSH und nie als Argument, nach
   `~/.config/kephalaion/tokens/<alias>/<account>.token` mit `0600` (Verzeichnisse `0700`).
   `<alias>` ist der Alias des Hub-Eintrags **am entfernten Node** (`team`), denn er steht im
   Namen des Headers.
4. **Die Adresse des Clients freigeben**, wo 443 nur für freigegebene Adressen offen ist (in
   Azure die NSG).
5. `kephalaion node mcp add --node https://<name>/kephalaion` (mit `--hub`, wenn unter
   `tokens/` mehr als ein Hub liegt); `node dir … --node https://<name>/kephalaion`.

**Rotation:** `node account rotate` braucht einen Hub-Eintrag am eigenen Node, und die
automatischen Anstöße (`install.sh`, `rotate`, `check`; „Bei den Assistenten anmelden“) laufen
auf einem Rechner ohne Node nie. Rotiert wird deshalb wie Schritt 2 an einem Node mit
Hub-Eintrag (mit der aktuellen Token-Datei als `--token-file`), danach wie Schritt 3 übergeben.
Der Eintrag bei den Assistenten bleibt richtig (er liest die Datei), wirksam mit einer neuen
Sitzung. Bis die neue Datei da ist, schickt der Client das alte Token — jede Anfrage ein
Fehlversuch für die Jail.

### Grenzen

- **Ein Rechner mit eigenem Node:** `--node` überschreibt die Adresse aus der config nur für
  diesen Aufruf. Die automatischen Anstöße setzen wieder die lokale Adresse ein, und `node mcp
  status` ohne `--node` meldet bis dahin „weicht ab“. Gedacht ist die entfernte Adresse für
  Rechner ohne eigenen Node.
- **Ein abweichender Alias geht nicht.** Auf einem Rechner mit eigenem Node trägt das
  Verzeichnis unter `tokens/` den eigenen Alias des Hubs; der entfernte Node erwartet seinen.
  Das geht nur, wenn beide gleich heißen (auf der WSL heißen beide `vm`).
- **Eigene CA:** `--ca-file` dient nur der Prüfung vor dem Eintragen. Die Assistenten prüfen
  das Zertifikat mit ihren eigenen Trust-Stores: Mit Let's Encrypt (Weg 1) nehmen Claude Code,
  OpenCode, Codex und die Erweiterung für VS Code die Adresse ohne Weiteres an (abgenommen am
  2026-10-02 gegen die Dev-VM). Für eine eigene CA (Weg 2) kennen die Assistenten je einen Weg —
  Codex `CODEX_CA_CERTIFICATE` oder `SSL_CERT_FILE`, OpenCode `NODE_EXTRA_CA_CERTS` (oder die
  System-CA), Claude Code `NODE_EXTRA_CA_CERTS`, die Erweiterung für VS Code die CA des Extension
  Hosts (`NODE_EXTRA_CA_CERTS`) —, gesetzt in der Umgebung, in der der Assistent startet. Geprüft
  ist das nicht; nur die Zeichenketten stehen in den Binaries (Befund
  `mcp-client-registrierung.md`).
- **Devcontainer:** nicht dieser Weg ([`konzept.md`](konzept.md), „Devcontainer“).

### Ansible

Wie „Hub für Nodes anderer Rechner“, „Ansible“: `Caddyfile.j2` ist die Vorlage oben (mit
beiden Filtern und dem Block für `/kephalaion/mcp`), dazu Filter und Jail für fail2ban als
Dateien:

```yaml
- name: fail2ban für den MCP-Eingang des Nodes
  ansible.builtin.copy:
    src: "{{ item.src }}"
    dest: "{{ item.dest }}"
    owner: root
    group: root
    mode: "0644"
  loop:
    - { src: fail2ban/kephalaion-mcp.conf, dest: /etc/fail2ban/filter.d/kephalaion-mcp.conf }
    - { src: fail2ban/kephalaion.local, dest: /etc/fail2ban/jail.d/kephalaion.local }
  notify: fail2ban neu laden    # Handler: fail2ban-client -t, dann fail2ban-client reload
```

**Reihenfolge:** erst das Binary mit Task 023 (ein älterer Node nennt über den Block seine
Version), dann das Caddyfile, dann die Jail; Tokens der Clients erst danach. Wie es auf der
Dev-VM aussieht: `~/dev/vm/kephalaion/README.md`, „Stand 2026-10-02“.

## Neue Schemafassung: node.db neu anlegen

Migrationen gibt es noch nicht; ein Sprung der Schemafassung (zuletzt `node.db` 4 → 5 mit
`hubs.ca`, Task 018) heißt: Das neue Binary lehnt die vorhandene `node.db` ab, sie ist neu
anzulegen. `init` überschreibt nicht und bricht ab, solange die Rolle in der config steht —
deshalb in dieser Reihenfolge, global als Systembenutzer:

```sh
K="sudo -u kephalaion kephalaion"
$K config export --output /var/lib/kephalaion/keph-config.yaml   # 1. mit dem ALTEN Binary (Tokens, 0600)
# 2. das neue Binary installieren (oben), dann:
sudo systemctl stop kephalaion                                    # 3. serve stoppen
sudo rm /var/lib/kephalaion/node.db /var/lib/kephalaion/node.db.lock
sudo rm -r /var/lib/kephalaion/replicas                           # 4. node.db und die Replicas daneben
sudo -u kephalaion $EDITOR /etc/kephalaion/config.yaml            # 5. den Abschnitt node: herausnehmen
$K node init --config /etc/kephalaion/config.yaml \
  --db sqlite:///var/lib/kephalaion/node.db --listen 127.0.0.1:7433   # 6. neu anlegen
$K config import /var/lib/kephalaion/keph-config.yaml             # 7. Hub-Einträge samt Tokens zurück
sudo rm /var/lib/kephalaion/keph-config.yaml
sudo systemctl start kephalaion                                   # 8. die Replicas gleichen sich neu ab
```

Die Hub-Einträge behalten ihre Tokens über Export und Import, `hub.db` bleibt unberührt, die
Replicas legt der erste Abgleich neu an. Pro User dasselbe ohne `sudo` mit den Orten unter
`~/.local/share/kephalaion/` und `kephalaion service uninstall` bzw. `install` statt
`systemctl` (README, „Einrichten“).

## Upgrade: was Kephalaion meldet

`kephalaion upgrade --check` fragt GitHub nach dem neuesten Release und meldet, ob es eine
neuere Version gibt, ob sich **dieses** Binary selbst ersetzen kann (Schreibrecht im
Verzeichnis, geprüft mit einer Probedatei wie beim Ersetzen) und den Weg:

| `method` | wann | Weg (`command`) |
|---|---|---|
| `self` | Schreibrecht | `kephalaion upgrade` |
| `explicit` | Schreibrecht, dev build | `kephalaion upgrade --version vX.Y.Z` |
| `admin` | kein Schreibrecht, die globale config gilt | `sudo kephalaion upgrade && sudo systemctl restart kephalaion` — oder über Ansible (Version anheben) |
| `manual` | kein Schreibrecht, sonst | — (wer im Verzeichnis schreiben darf) |

`kephalaion upgrade --check --json` gibt dasselbe als JSON aus, für k-playbook und Skripte:

```json
{
  "state": "ok",
  "checked_at": "2026-09-26T08:00:00Z",
  "version": "v0.1.1",
  "dev_build": false,
  "latest": "v0.2.0",
  "update_available": true,
  "self_upgrade": false,
  "method": "admin",
  "command": "sudo kephalaion upgrade && sudo systemctl restart kephalaion",
  "hint": "globale Installation — das Upgrade macht der Verwalter: über Ansible (Version anheben) oder sudo kephalaion upgrade && sudo systemctl restart kephalaion"
}
```

| Feld | Bedeutung |
|---|---|
| `state` | `ok` (GitHub hat geantwortet), `failed` (Frage gescheitert, Grund in `error`), `unchecked` (noch nicht gefragt; nur in `whoami` von `serve`) |
| `error` | Grund, wenn `state` nicht `ok` ist |
| `checked_at` | Zeitpunkt der Frage, RFC 3339, UTC |
| `version`, `dev_build` | installierte Version; `dev_build` für ein selbst gebautes Binary |
| `latest`, `update_available` | neuestes Release ohne Suffix und ob es neuer ist — nur bei `ok`; bei einem dev build ist `update_available` nie gesetzt |
| `self_upgrade` | ob dieses Binary sich selbst ersetzen kann |
| `method`, `command`, `hint` | der Weg (Tabelle oben), als Befehl und als Satz |

Exit-Code von `upgrade --check` (mit und ohne `--json`): 0, wenn GitHub geantwortet hat —
gleich, ob es eine neue Version gibt —; 1, wenn die Frage scheitert (kein Netz, Rate-Limit,
kein Release; mit `--json` steht der Grund auch im JSON); 2 bei falschem Aufruf. `upgrade`
ohne `--check`: 0 erledigt, 1 gescheitert (das Binary bleibt unverändert), 2 falscher Aufruf.
Ohne Schreibrecht bricht `upgrade` vor dem Download ab und nennt den Weg.

**Über MCP:** Das Werkzeug `whoami` des Nodes trägt dieselben Angaben im Feld `update`, aus
Sicht des `serve`-Prozesses — global also der Weg des Verwalters. `serve` fragt GitHub beim
Start und danach höchstens einmal am Tag, nach einem Fehler frühestens nach einer Stunde, und
merkt sich die Antwort; `whoami` fragt GitHub nie. Ohne Anmeldung erlaubt die GitHub-API 60
Anfragen je Stunde und Adresse, geteilt von allen Usern eines Rechners. Ersetzen kann das
Binary über MCP niemand.

**Global** kann ein User nicht upgraden. Der Verwalter hebt die Version in Ansible an (das
Playbook lädt das neue Binary und startet den Dienst neu) oder ruft
`sudo kephalaion upgrade && sudo systemctl restart kephalaion`.

## Ohne systemd

Auf Systemen ohne systemd (Alpine mit OpenRC, schlanke Container, WSL ohne `systemd=true`)
richtet Kephalaion keinen Dienst ein; `service install` bricht mit dem Hinweis ab. Ein eigener
Supervisor startet dann

```sh
kephalaion serve                                   # pro User
KEPHALAION_CONFIG=/etc/kephalaion/config.yaml kephalaion serve   # global, als Systembenutzer kephalaion
```

`serve` läuft im Vordergrund, schreibt sein Log nach stderr und endet mit SIGINT oder SIGTERM;
eigene Dateien für andere Supervisoren gibt es nicht. `kephalaion status` zeigt dann
`Dienst: ohne systemd` — kein Fehler.

## Für Automatisierung (Ansible, KI)

Feste Angaben, die sich nicht ohne Hinweis im Release ändern:

| Angabe | Wert |
|---|---|
| Binary eines Releases | `https://github.com/kephalaion/kephalaion/releases/download/<tag>/kephalaion-<os>-<arch>` |
| Prüfsummen | `https://github.com/kephalaion/kephalaion/releases/download/<tag>/SHA256SUMS` (Format von `sha256sum`) |
| neuestes Release | `https://github.com/kephalaion/kephalaion/releases/latest` (Weiterleitung auf `…/tag/<tag>`), API `https://api.github.com/repos/kephalaion/kephalaion/releases/latest` |
| `<tag>` | `vX.Y.Z`; mit Suffix (`-rc1`) eine Vorabversion, nie `latest` |
| `<os>` | `linux` (global nur `linux`), `darwin` |
| `<arch>` | `amd64` (aus `x86_64`), `arm64` (aus `aarch64`) — `ansible_architecture` bzw. `uname -m` |
| Binary | `/usr/local/bin/kephalaion`, `root:root`, `0755` |
| Systembenutzer | `kephalaion`, `--system`, eigene Gruppe, Home `/var/lib/kephalaion`, Shell `/usr/sbin/nologin` |
| config | `/etc/kephalaion/config.yaml`, `0644`; Verzeichnis `/etc/kephalaion/`, `kephalaion:kephalaion`, `0755` |
| Daten | `/var/lib/kephalaion/` (`node.db`, `hub.db`, `replicas/`), `kephalaion:kephalaion`, `0700` |
| Unit | `/etc/systemd/system/kephalaion.service`, Inhalt aus `kephalaion service unit --system`, `0644` |
| Ports | Node `127.0.0.1:7433` (MCP unter `/mcp`), Hub `127.0.0.1:7434` |
| Reverse-Proxy | Caddy, `/etc/caddy/Caddyfile`: nur `/kephalaion/hub/*` nach `localhost:7434`, mit `uri strip_prefix /kephalaion` und `header_up Host {upstream_hostport}`; Adresse für Nodes `https://<name>/kephalaion/hub`; wahlweise der Rest von `/kephalaion/*` (Weboberfläche unter `https://<name>/kephalaion/`) hinter `forward_auth`; nach außen 443 (und 80 nur für Let's Encrypt) — siehe „Hub für Nodes anderer Rechner“ |

Reihenfolge und Regeln:

1. Binary laden, gegen `SHA256SUMS` prüfen, ablegen. Ändert sich das Binary, danach den Dienst
   neu starten.
2. Systembenutzer, dann die Verzeichnisse mit ihren Rechten.
3. `init` als Systembenutzer, **nur wenn die Datenbank fehlt** (`creates:`); immer mit
   `--config /etc/kephalaion/config.yaml` und `--db sqlite:///var/lib/kephalaion/<rolle>.db`.
   `init` überschreibt nie und bricht ab, wenn es die Rolle schon gibt.
4. Unit aus `kephalaion service unit --system` ablegen, `daemon-reload`, einschalten und
   starten. Ändert sich die Unit, neu starten.
5. **Kein Token.** Hub-Einträge des Nodes und das erste `rotate` der Accounts richtet der
   Verwalter von Hand ein (oben); die Tokens der User gehören den Usern bzw. k-playbook.
   Bei den KI-Assistenten trägt Ansible nichts ein; das macht jeder User selbst
   (`kephalaion node mcp add`, „Bei den Assistenten anmelden“).

Alle Schritte sind wiederholbar: Ein zweiter Lauf ändert nichts, solange Version und Unit
gleich bleiben.

### Beispiel mit Ansible

```yaml
- name: Kephalaion global installieren
  hosts: kephalaion
  become: true
  vars:
    kephalaion_version: v0.2.0
    kephalaion_hub: false          # true, wenn dieser Rechner auch den Hub trägt
    kephalaion_manage_service: true   # false nur zum Testen ohne systemd
    kephalaion_arch: "{{ {'x86_64': 'amd64', 'aarch64': 'arm64'}[ansible_facts['architecture']] }}"
    kephalaion_base: "https://github.com/kephalaion/kephalaion/releases/download/{{ kephalaion_version }}"
  tasks:
    - name: Binary laden und gegen SHA256SUMS prüfen
      ansible.builtin.get_url:
        url: "{{ kephalaion_base }}/kephalaion-linux-{{ kephalaion_arch }}"
        checksum: "sha256:{{ kephalaion_base }}/SHA256SUMS"
        dest: /usr/local/bin/kephalaion
        owner: root
        group: root
        mode: "0755"
      notify: kephalaion neu starten

    - name: Systembenutzer
      ansible.builtin.user:
        name: kephalaion
        system: true
        home: /var/lib/kephalaion
        create_home: false
        shell: /usr/sbin/nologin

    - name: Verzeichnis der config
      ansible.builtin.file:
        path: /etc/kephalaion
        state: directory
        owner: kephalaion
        group: kephalaion
        mode: "0755"

    - name: Datenverzeichnis
      ansible.builtin.file:
        path: /var/lib/kephalaion
        state: directory
        owner: kephalaion
        group: kephalaion
        mode: "0700"

    - name: Node einrichten, wenn die Datenbank fehlt
      ansible.builtin.command:
        argv: [/usr/local/bin/kephalaion, node, init, --config, /etc/kephalaion/config.yaml,
               --db, "sqlite:///var/lib/kephalaion/node.db"]
        creates: /var/lib/kephalaion/node.db
      become_user: kephalaion

    - name: Hub einrichten, wenn gewünscht und die Datenbank fehlt
      ansible.builtin.command:
        argv: [/usr/local/bin/kephalaion, hub, init, --config, /etc/kephalaion/config.yaml,
               --db, "sqlite:///var/lib/kephalaion/hub.db"]
        creates: /var/lib/kephalaion/hub.db
      become_user: kephalaion
      when: kephalaion_hub | bool

    - name: System-Unit aus dem Binary erzeugen
      ansible.builtin.command:
        argv: [/usr/local/bin/kephalaion, service, unit, --system]
      register: kephalaion_unit
      changed_when: false

    - name: System-Unit ablegen
      ansible.builtin.copy:
        content: "{{ kephalaion_unit.stdout }}\n"
        dest: /etc/systemd/system/kephalaion.service
        owner: root
        group: root
        mode: "0644"
      notify: kephalaion neu starten

    - name: Dienst einschalten und starten
      ansible.builtin.systemd:
        name: kephalaion
        enabled: true
        state: started
        daemon_reload: true
      when: kephalaion_manage_service | bool

  handlers:
    - name: kephalaion neu starten
      ansible.builtin.systemd:
        name: kephalaion
        state: restarted
        daemon_reload: true
      when: kephalaion_manage_service | bool
```

`get_url` lädt nur, wenn sich die Prüfsumme ändert; mit einer URL als `checksum` sucht das
Modul die Zeile zum Namen des Assets in `SHA256SUMS` selbst heraus. `become_user` braucht auf
dem Rechner `sudo`. Ohne systemd — etwa in einem Test-Container — übergeht
`-e kephalaion_manage_service=false` die Aufgabe mit systemd und den Handler; `--skip-tags`
reichte dafür nicht, Ansible führt benachrichtigte Handler trotzdem aus. Ein Upgrade ist ein
Lauf mit neuer `kephalaion_version`: neues Binary, der Handler startet den Dienst neu.
