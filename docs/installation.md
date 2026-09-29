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
noch nicht; dafür fehlt das Lauschen auf der Docker-Bridge (`konzept.md`, „Kommunikation“).
Der Hub dagegen ist von anderen Rechnern erreichbar — über einen Reverse-Proxy auf seinem
Rechner, siehe „Hub für Nodes anderer Rechner“.

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
„serve und MCP“.

## Hub für Nodes anderer Rechner: hinter einem Reverse-Proxy

Der Hub lauscht nur auf Loopback und spricht kein TLS — das bleibt so. Ein Node auf einem
anderen Rechner erreicht ihn über einen **Reverse-Proxy auf dem Rechner des Hubs** (hier
Caddy), der nach außen `https` spricht, TLS beendet und `/v1/*` an `localhost:7434`
weiterreicht. Der Hub braucht dafür keine Änderung und keine Einstellung: Der Proxy setzt
`Host` auf die Loopback-Adresse des Hubs, damit dessen Host-Prüfung gilt (entschieden am
2026-09-28, [`konzept.md`](konzept.md), „Kommunikation“). Die Identität bleibt das Token;
TLS verschlüsselt und weist den Server aus. Ein Node, dessen Zertifikatsprüfung scheitert,
schickt kein Token.

### Caddyfile

`/etc/caddy/Caddyfile` (apt-Paket `caddy`, eigene Unit, bindet 443 selbst — die Unit trägt
`CAP_NET_BIND_SERVICE`). Vorlage für Weg 2 (nur IP, Caddys eigene CA); für Weg 1 steht der
Name statt der IP, und die Zeile `tls internal` entfällt:

```text
https://9.141.8.157 {
    tls internal
    handle /v1/* {
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

- Nur `/v1/*` geht zum Hub, alles andere ist 404 — der Proxy zeigt nach außen nichts vom
  Hub, was der Vertrag nicht kennt.
- Bedient der Proxy schon andere Dienste unter demselben Namen (eine Anmeldung per
  `forward_auth` davor, eine Webseite), bekommt der Hub einen **Präfix**: statt `handle
  /v1/*` ein `handle_path /kephhub/*` mit demselben Inhalt, vor dem Sammel-`handle` und ohne
  `forward_auth` (Nodes sind Maschinen und weisen sich per Token aus). `handle_path` nimmt
  `/kephhub` weg, der Hub sieht `/v1/…`; der Node trägt die Adresse mit dem Präfix ein
  (`--address https://<name>/kephhub`), der Client hängt `/v1/<vorgang>` an.
- `header_up Host {upstream_hostport}` schickt `Host: localhost:7434`; genau das verlangt der
  Hub (`vertrag.md`, „Host“). Ohne die Zeile antwortet er 403, und `node hub check` sagt es.
- `request_body max_size 8MiB` liegt über den 7 MiB, die der Hub für einen Schreibvorgang
  annimmt; Caddys Zeitlimits lassen eine `sync`-Seite von bis zu 10 Minuten durch (Standard:
  keine Grenze für die Antwort des Upstreams). TLS mindestens 1.2 ist Caddys Standard.
- Das Zugriffslog als Datei ist die Grundlage für fail2ban (siehe „Bekannte Grenze“).
- Prüfen vor dem Einspielen: `caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile`,
  danach `systemctl reload caddy`.

### Zertifikat: zwei Wege

Der Code kann beide; welcher gilt, entscheidet der Betrieb.

1. **Mit Namen (empfohlen, sobald es einen gibt).** Ein DNS-Name auf der öffentlichen IP —
   in Azure ein DNS-Label (`<label>.<region>.cloudapp.azure.com`) oder ein eigener Name. Caddy
   holt das Zertifikat selbst bei Let's Encrypt: per HTTP-01 muss **Port 80 für alle offen**
   sein (nur für die Prüfung, dort gibt es keine Inhalte; auch bei jeder Verlängerung), oder
   per DNS-01 mit dem DNS-Plugin des Anbieters (nicht für `cloudapp.azure.com`, die Zone
   gehört Azure). Die Nodes brauchen keine CA: `node hub add … --address https://<name>` ohne
   `--ca-file`, das Zertifikat gilt gegen die System-Roots. Nur mit dem Namen, nicht mit der
   IP — das Zertifikat gilt für den Namen.
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
  --transport https --address https://hub.example.org --token-stdin; unset TOKEN
# Am Node (Weg 2, IP mit CA):
printf '%s\n' "$TOKEN" | kephalaion node hub add vm --node wsl-kleist \
  --transport https --address https://9.141.8.157 --ca-file ~/vm-ca.pem --token-stdin

kephalaion node hub check vm          # erreichbar, hub_id, Node-Name, erlaubte Collections
kephalaion node collection add vm:test
kephalaion node sync vm
```

`node hub check` nennt, was schiefgeht: „Zertifikat von … nicht vertraut (Aussteller …;
--ca-file?)“, „Zertifikat gilt nicht für …“, „Zertifikat abgelaufen seit …“, „Proxy
antwortet, aber der Hub dahinter nicht (HTTP 502)“ (läuft `kephalaion serve`?),
„Host-Prüfung des Hubs schlägt fehl (HTTP 403)“ (fehlt `header_up Host`?), „der Proxy kennt
den Pfad nicht (HTTP 404)“ (stimmt der Präfix, steht die Route?), „eine Anmeldung des
Proxys, nicht der Hub (HTTP 401)“ (die Route steht hinter `forward_auth`; eine Weiterleitung
zur Anmeldung meldet der Client als 3xx). Der Port 443 ist in der Adresse weglassbar. Am Hub steht jede Anfrage im Log mit der Adresse des Aufrufers aus
`X-Forwarded-For` (`via`), ohne Token; im Caddy-Log stehen 200 auf `/v1/…` und 404 daneben.

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

Der Hub begrenzt Fehlversuche nicht ([`vertrag.md`](vertrag.md), „Bekannte Grenzen“); nach
außen wird das dringlicher. Caddy hat ohne Plugin keine Ratenbegrenzung. Übergang: fail2ban
auf `/var/log/caddy/kephalaion.log` (401 und 403 auf `/v1/`), die Allowlist in der NSG, und
die Begrenzung am Hub als eigene Aufgabe.

### Auf dem Rechner des Hubs zu bestätigen

Zwei Annahmen dieser Anleitung sind nur aus der Caddy-Dokumentation belegt, nicht im Betrieb:
dass `header_up Host {upstream_hostport}` beim Hub als `Host: localhost:7434` ankommt (sonst
403, siehe `node hub check`), und ob die öffentliche IP über LB/NAT kommt und dort eine
eigene Freigabe braucht.

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
| Reverse-Proxy | Caddy, `/etc/caddy/Caddyfile`: nur `/v1/*` nach `localhost:7434` mit `header_up Host {upstream_hostport}`; nach außen 443 (und 80 nur für Let's Encrypt) — siehe „Hub für Nodes anderer Rechner“ |

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
