---
title: Kephalaion — VS Code
description: Erweiterung, die Collections über einen FileSystemProvider als Ordner in VS Code zeigt — lesen aus der Replica, schreiben über den Node am Hub; Ablauf, benötigte Werkzeuge, Status, Sprachen, Installation ohne Marketplace, gemeinsames Release, Ergebnis des Versuchs und Fundstellen.
---

# Kephalaion in VS Code

**Stand: Lesen und Schreiben gebaut** (2026-09-27, Version 0.0.5, siehe „Umsetzung: Lesen“ und
„Umsetzung: Schreiben“ unten; seit 0.0.6 auch der Node als MCP-Server für Copilot, siehe
„MCP-Server für Copilot“): Statusleiste aus `whoami`, Collections als Ordner mit Inhalt über
`list` und `read`, Änderungen über `changes`; speichern, neue Datei, neuer Ordner, löschen,
umbenennen und verschieben über `create`, `write`, `delete` und `rename` (Task 014). Was die
Erweiterung braucht, gibt es am Node: `whoami` in der Form, die die Statusleiste braucht —
Version, alle Hubs mit `login`, Node-Name und Stand des Abgleichs (Task 008) —, dazu `list`,
`read` und `changes` (Task 009, Festlegungen in [`konzept.md`](konzept.md), „Allgemein —
lesen“) und die Werkzeuge, die schreiben (Task 014, „Allgemein — schreiben“); `serve` gleicht im
Hintergrund ab. Das Schreiben ist mit einem Ersatz für `vscode` gegen den echten Node geprüft,
im echten VS Code noch nicht — die Handgriffe dafür stehen unter „Umsetzung: Schreiben“.
Begriffe nach [`begriffe.md`](begriffe.md), Hintergrund in [`konzept.md`](konzept.md).

## Wozu

Eine Collection erscheint in VS Code als Ordner neben dem Projekt: im Explorer blättern,
Dokumente lesen, in der Markdown-Vorschau ansehen, bearbeiten und speichern. Vor allem lassen
sich **Daten leicht übergeben** — Dateien per Drag & Drop oder Kopieren und Einfügen aus dem
Projekt in eine Collection ziehen und umgekehrt.

Die Erweiterung ist eine **Oberfläche für Menschen**. KI-Agenten brauchen sie nicht; sie
lesen die echte Platte und sprechen mit dem Node über MCP.

Damit können auch Dokumente wie Tasks in den Store, ohne aus VS Code zu verschwinden (siehe
[`konzept.md`](konzept.md), „Für k-playbook — Kandidaten“).

## Wie es läuft

VS Code kennt dafür die Schnittstelle `FileSystemProvider`. Eine Erweiterung meldet ein
eigenes URI-Schema an, etwa `keph`, und beantwortet für dessen URIs die Vorgänge eines
Dateisystems. Explorer, Editor, Vorschau, Speichern und Kopieren arbeiten danach mit
`keph://…` wie mit `file://…`.

```ts
vscode.workspace.registerFileSystemProvider('keph', provider, {
  isCaseSensitive: true,
  isReadonly: false,
});
```

**URI:** `keph://<hub-alias>/<collection>/<name>`, etwa
`keph://team/entscheidungen/2026/009-transport.md`. Der Alias ist der Hub-Eintrag des Nodes.

```
keph://<hub-alias>/                  ← lesbare Collections (whoami bzw. list auf die Wurzel)
keph://<hub-alias>/<collection>/     ← list
keph://<hub-alias>/<collection>/…    ← list / read
```

Welche Collections es gibt, fragt die Erweiterung ab; sie stehen nirgends in VS Code.

**Einbinden** als Ordner eines Multi-Root-Workspace:

```json
// projekt.code-workspace
{ "folders": [
  { "path": "." },
  { "uri": "keph://team/wissen", "name": "Wissen" }
]}
```

### Die Vorgänge und ihre Entsprechung

| `FileSystemProvider` | Kephalaion (MCP-Werkzeug des Nodes) |
|---|---|
| `stat(uri)` | `read` mit `content: false`: Dokument, Verzeichnis oder nichts; `updated` als `mtime`, Größe als `size`; ohne `writable` (`write` der Collection; unter `vendor/<name>/` der Scope `vendor/<name>`, seit Task 016 je Name; unter einem Verzeichnis-Scope immer, seit Task 021) `FilePermission.Readonly` |
| `readDirectory(uri)` | `list` mit `path`, ohne Unterverzeichnisse, Verzeichnisse als eigene Einträge, mit Cursor bis zum Ende |
| `readFile(uri)` | `read` — aus der Replica, lokal und schnell, auch offline |
| `writeFile(uri, …)` | gibt es das Dokument noch nicht (`read` mit `content: false`), `create` (ohne `options.create`: `FileNotFound`); sonst `write` mit der Revision aus diesem `read` (ohne `options.overwrite`: `FileExists`); ein Verzeichnis ist `FileIsADirectory`. Was die Replica schon kennt, fängt VS Code über `mtime` selbst ab („Datei ist neuer“); was noch nicht abgeglichen ist, lehnt der Hub an der Revision ab |
| `rename(alt, neu)` | `rename`, `id` bleibt; ein Verzeichnis als Ganzes, ohne `base_revision`. Nur innerhalb einer Collection, sonst eine Meldung; ein belegtes Ziel `FileExists`, mit `overwrite` eine Meldung — überschrieben wird nie |
| `delete(uri)` | `delete` (Löschmarke) — Eigenes mit `write`, Fremdes nur mit `supersede`; ein Verzeichnis als Ganzes, `recursive` aus den Optionen; ohne `base_revision` |
| `createDirectory(uri)` | nichts am Hub — Verzeichnisse sind nur Präfixe von Namen; die Erweiterung merkt sich das leere Verzeichnis in diesem Fenster, bis darin etwas angelegt oder es gelöscht wird |
| `watch` / Event `onDidChangeFile` | `changes` mit dem Cursor der letzten Antwort, abgefragt alle 3 s — aus der Replica, ohne Netz. Ein Umbenennen erkennt die Erweiterung an der `id` — seit 0.0.5 über eine Tabelle `id` → Name je Collection (alter Name gelöscht, neuer angelegt); `changes` nennt keinen alten Namen. Bei `reset` liest sie den Hub neu mit `list`, bei `dropped` entfernt sie die Collection; beide verwerfen die Tabelle. Nach eigenen Schreibvorgängen feuert sie die Ereignisse selbst |

- **Konflikte:** VS Code vergleicht `mtime` beim Speichern selbst und fragt nach, wenn die
  Datei inzwischen neuer ist. Zusätzlich lehnt der Hub ab, wenn die mitgeschickte Revision
  veraltet ist (siehe [`konzept.md`](konzept.md), „Zwei Arten von Eingaben“). Die
  Erweiterung meldet das als gewöhnlichen Fehler mit der Meldung des Nodes (`stale_revision`)
  — ein Fehler des Providers löst in VS Code nie „Datei ist neuer“ aus; nichts wird still
  überschrieben.
- **Offline wird gelesen, nicht geschrieben.** Ist der Hub nicht erreichbar, werfen
  `writeFile`, `delete` und `rename` `FileSystemError.Unavailable` mit der Meldung des Nodes.
  Lesen läuft weiter.
- **Rechte:** Collections ohne Schreibrecht meldet `stat` als schreibgeschützt
  (`FilePermission.Readonly`); VS Code öffnet sie dann nur zum Lesen. Das Recht gilt je
  Collection (`write`, aus `writable`): Ein fremdes Dokument ohne `supersede` erscheint
  schreibbar und scheitert erst beim Speichern mit `NoPermissions` („gehört admin, supersede
  fehlt“). Bewusste Grenze: Ein Account mit `supersede`, aber ohne `write`, sieht die
  Collection schreibgeschützt, obwohl der Hub ihm das Ändern fremder Dokumente erlaubte.
- **Verbindung und Stand:** `whoami` — Account, Hubs, lesbare Collections und
  Rechte, Stand des Abgleichs, Version.
- **Die Replica bleibt unberührt.** Die Erweiterung schreibt nie in sie; jeder
  Schreibvorgang geht über den Node zum Hub, die Replica zieht über den Abgleich nach — wie bei
  jedem anderen Client.

### Welche Werkzeuge — keine eigens für VS Code

Entschieden am 2026-09-26: Die Erweiterung braucht **`whoami`, `list`, `read` und
`changes`**, zum Schreiben `create`, `write`, `rename`, `delete` (gebaut in Task 014). Alle taugen auch für
die KI; es gibt keine Werkzeuge nur für VS Code, kein eigenes Profil, keine Schnittstelle
neben MCP. Dafür wurden drei Werkzeuge im Konzept erweitert bzw. neu aufgenommen
([`konzept.md`](konzept.md), „Allgemein — lesen“):

- `list` liefert Verzeichnisse der nächsten Ebene als eigene Einträge;
- `read` mit `content: false` ersetzt ein eigenes `stat`;
- `changes` meldet, was sich seit einer Revision oder einem Zeitpunkt geändert hat.

Erwogen und verworfen: Werkzeuge mit dem Vermerk „nur für VS Code“ in der Beschreibung — die
KI lädt die Beschreibung trotzdem mit und ruft sie gelegentlich auf; ein Profil (eigener
Pfad oder Header, der Zusatzwerkzeuge freischaltet); MCP-„Resources“ — ohne Verzeichnisse,
und deren Benachrichtigungen brauchen eine Sitzung.

## Status und Auswahl

- **Statusleiste:** etwa `Keph ✓ team` bzw. `Keph ⚠ offline`. Tooltip: Account je Hub,
  letzter Abgleich, Revision, Version von Node und Erweiterung — mit Warnung, wenn sie
  verschieden sind. Quelle ist `whoami`, dasselbe Werkzeug, das die KI benutzt: je Hub
  `login` (`ok`, `invalid`, `missing`) und `sync` (`last_success`, `revision`, `last_error`,
  `never_synced`). Ist `login` `invalid` und `sync.never_synced` gesetzt, liegt es nicht am
  Token, sondern der Node hat den Hub noch nie abgeglichen. `unknown_hubs` nennt Header zu
  Aliasen, die der Node nicht kennt — ein Hinweis auf eine falsch eingerichtete
  Erweiterung.
- **Klick darauf:** ein kleines Menü — neu verbinden, Token setzen, Collection einbinden,
  Log anzeigen (Output-Channel „Kephalaion“ mit den Aufrufen und Fehlern).
- **Node nicht erreichbar:** Dann scheitert auch das Lesen, weil die Erweiterung die Replica
  nie selbst öffnet. Die Statusleiste zeigt das, mit einem Hinweis, wie der Node gestartet
  wird.
- **In VS Code wird gewählt, was angezeigt wird:** „Collection einbinden“ bietet die
  lesbaren Collections aus `whoami` zur Auswahl an und fügt die gewählte als Ordner in den
  Workspace ein.
- **Über die CLI wird eingerichtet:** Hubs, Tokens, gewünschte Collections, Accounts. Das
  ist Verwaltung mit Tokens und heute schon Kommandozeile; die Verwaltung über MCP ist im
  Konzept nur vorgemerkt. Nicht doppelt bauen.
- **Welcher Node:** Es gibt einen je Rechner. Die Erweiterung läuft neben ihm (siehe
  `extensionKind`) und kann die Adresse aus `listen` in `~/.config/kephalaion/config.yaml`
  lesen; eine Einstellung `kephalaion.nodeUrl` braucht es nur zum Überschreiben.
- **Account und Token** trägt die Erweiterung als Header ein, wie jeder Client
  (`X-Keph-Account-<hub>`, `X-Keph-Token-<hub>`). **Sie liest sie aus den Token-Dateien**
  `~/.config/kephalaion/tokens/<hub>/<account>.token` (siehe [`konzept.md`](konzept.md),
  „Orte nach XDG“) — sie läuft als `workspace` auf demselben Rechner und braucht so keine
  eigene Einrichtung. Die Zuordnung ist eindeutig: Das Verzeichnis ist der Alias des Hubs und
  damit der Name im Header, der Dateiname der Account; Dateien auf `.pending` übergeht sie.
  `whoami` bestätigt danach nur, dass die Anmeldung gilt (`login: ok`). **Mehrere Accounts an
  einem Hub:** Auswahl über „Kephalaion: Account wählen“ (auch im Menü der Statusleiste); die
  Wahl steht in der Einstellung `kephalaion.accounts` (`{"<hub>": "<account>"}`) — nur Namen,
  kein Token —, Scope `machine-overridable`: je Rechner, nicht über Settings Sync, ein
  Workspace kann sie überschreiben. Ohne Wahl nimmt die Erweiterung den ersten nach Namen und
  zeigt die Statusleiste gelb, ebenso bei einem gewählten Account ohne Token-Datei.
  Der `SecretStorage` von VS Code bleibt nur der Ausweg, wenn keine Datei da ist; nie
  `settings.json` — die landet leicht im Repository oder in Settings Sync.
  Geprüft am 2026-09-26: `whoami` mit dem Token aus `tokens/home/kamran-desktop.token`
  liefert `login: ok`, Account `kamran-desktop`, User `kamran`, Collection `home:eins` mit
  `read` und `write`. Die MCP-Konfigurationen der KI-Clients (`.mcp.json`,
  `.cursor/mcp.json`, `opencode.json`,
  `.vscode/mcp.json`) liest und schreibt die Erweiterung nicht; den Node als MCP-Server für
  Copilot meldet sie über die API für Erweiterungen (unten, „MCP-Server für Copilot“).

## MCP-Server für Copilot (Task 022, 0.0.6)

Die Erweiterung meldet den Node als MCP-Server „Kephalaion“, damit Copilot in VS Code die
Werkzeuge des Nodes sieht — ohne Eintrag in `mcp.json` und ohne Token in einer Datei von
VS Code. `code --add-mcp` wäre der andere Weg, wirkt aber im Remote-Terminal (WSL, SSH) nicht
(`kephalaion node mcp add --assistant vscode` nennt deshalb nur die Erweiterung).

- **API:** `vscode.lm.registerMcpServerDefinitionProvider` mit dem Beitrag
  `mcpServerDefinitionProviders` (Anbieter `kephalaion.node`), stabil seit VS Code 1.101 —
  deshalb `engines.vscode` `^1.101.0`. Die Definition ist ein `McpHttpServerDefinition` mit der
  Adresse wie `nodeUrl` (`listen` bzw. `kephalaion.nodeUrl`, dazu `/mcp`).
- **Das Token erst beim Start.** `provideMcpServerDefinitions` meldet die Definition ohne jeden
  Header; `version` nennt nur Hubs und Accounts (`vm=kamran-wsl`). VS Code speichert gemeldete
  Definitionen samt Headern zwischen (`mcp.extCachedServers` im Speicher des Workspace, bei WSL
  auf der Windows-Seite) — deshalb dort nichts Geheimes. Die Header-Paare setzt
  `resolveMcpServerDefinition` ein, wenn VS Code den Server startet; die aufgelöste Definition
  speichert VS Code nicht, es reicht sie an den Extension Host weiter, der die Verbindung
  aufbaut.
- **Wer verbindet:** der Extension Host, in dem die Erweiterung läuft (`workspace`, unter WSL im
  Linux), mit dem `fetch` von Node.js — wie die Erweiterung selbst. `Host` ist die Adresse aus
  `listen`, ein `Origin` schickt er nicht; die Prüfung des Nodes lässt die Anfrage durch.
- **Accounts wie `node mcp headers`:** je Hub der gewählte Account (`kephalaion.accounts`),
  sonst der einzige mit Token-Datei. Ein Hub mit mehreren Accounts ohne Wahl oder mit einem
  gewählten ohne Token-Datei fehlt im MCP-Eintrag (anders als in Statusleiste und Dateisystem,
  wo die Erweiterung den ersten nimmt); das Log „Kephalaion“ nennt ihn.
- **Neu gemeldet** wird, wenn sich Adresse, Wahl, die Einstellung oder die Token-Dateien ändern
  (geprüft alle 30 s). Eine laufende Verbindung behält ihre Header: nach `kephalaion node account
  rotate` den Server neu starten („MCP: Server auflisten“ → Kephalaion → Neu starten) oder das
  Fenster neu laden.
- **Trace:** Protokolliert der Extension Host auf „Trace“ (`vscode.env.logLevel`), schreibt
  VS Code bei jeder Anfrage an einen MCP-Server die Header ins Log des Servers — nur
  `Authorization` wird verdeckt, ein `X-Keph-Token-…` nicht. Solange das gilt, meldet die
  Erweiterung den Server nicht (Warnung, Eintrag im Log „Kephalaion“) und startet ihn nicht;
  geht das Log-Level zurück, meldet sie ihn wieder.
  **Entschieden am 2026-10-01 (Nutzer):** so, mit dieser Sperre. Verworfen: das Restrisiko nur
  dokumentieren (das Token stünde bei Trace in einer Log-Datei des Fensters, unter WSL auf der
  Windows-Seite) und VS Code aus Task 022 nehmen. Ein Weg ohne die Grenze bräuchte eine
  Anmeldung über `Authorization`, die VS Code verdeckt — eine Änderung an der Anmeldung des
  Nodes (Header-Paare je Hub), nicht Teil dieser Task.
- **Abschalten:** Einstellung `kephalaion.mcpServer.enabled` (Vorgabe `true`, Scope
  `machine-overridable`).
- **Starten:** VS Code führt den Server nach dem Laden unter „MCP: List Servers“, startet ihn
  aber nicht von selbst — dort „Start Server“ (oder einen Chat mit Werkzeugen beginnen, wenn
  VS Code Server dabei selbst startet). Beim Start versucht VS Code zuerst `GET /mcp` (der
  zustandslose Node antwortet 405) und arbeitet dann mit `POST`.
- **Abgenommen am 2026-10-01** auf der WSL mit VS Code 1.140.0 und Copilot (Agent-Modus):
  Server gestartet („Starting server from Remote extension host“, 8 Werkzeuge), `whoami` mit
  Account; im Log des Nodes die Anfragen mit Account. Danach stand das Token weder unter
  `~/.vscode-server` noch in den Daten von VS Code unter Windows (`AppData/Roaming/Code`,
  durchsucht mit GNU grep; nicht lesbar nur drei gesperrte `LOCK`-Dateien). Befund
  `material/befunde/mcp-client-registrierung.md`.

## Sprachen

**Eine VS-Code-Erweiterung ist JavaScript bzw. TypeScript.** Sie läuft im Extension Host, einem
Node.js-Prozess (oder als Web-Erweiterung in einem Browser-Worker). Andere Sprachen gehen nur
mittelbar:

- **Dünne Erweiterung in TypeScript, Logik in Go.** Die Erweiterung übersetzt nur zwischen
  VS Code und einem Prozess in einer anderen Sprache — über stdio (JSON-RPC, wie beim Language
  Server Protocol) oder HTTP. So arbeiten gopls, rust-analyzer und die meisten großen
  Erweiterungen.
- **WASM:** VS Code kann WASI-Module laden (`@vscode/wasm-wasi`), Go lässt sich nach WASM
  übersetzen. Für Kephalaion ungeeignet: SQLite, Dateizugriff und Netz sind aus WASM heraus
  mühsam, und die Logik gibt es ohnehin schon im Node.

**Für Kephalaion: ein dünner Übersetzer; die Logik bleibt im Binary.** Gebaut ist er in
reinem JavaScript ohne Abhängigkeiten (siehe „Umsetzung“).

**Weg zum Node: MCP über HTTP.** Der Node ist ohnehin ein MCP-Server über HTTP auf
`127.0.0.1` (siehe [`konzept.md`](konzept.md), „Kommunikation“), und `list`, `read`, `write`,
`rename`, `delete` sind genau die Vorgänge, die der Provider braucht. Die Erweiterung ist damit
ein MCP-Client wie jeder andere; ein eigenes Protokoll entfällt, und das SDK
(`@modelcontextprotocol/sdk`) braucht sie nicht — JSON-RPC über das `fetch` von Node.js genügt.
Voraussetzung ist ein laufender Node (`kephalaion serve`). Account und Token trägt die
Erweiterung als Header ein, wie jeder Client; sie liest sie aus den Token-Dateien (siehe
„Status und Auswahl“), nie aus den Einstellungen.

## WSL und Remote: `extensionKind`

VS Code teilt Erweiterungen in zwei Arten:

- **`ui`** — läuft auf dem Rechner, auf dem das Fenster ist (unter WSL: Windows).
- **`workspace`** — läuft dort, wo der Workspace liegt (unter WSL: im Linux der WSL, im
  VS-Code-Server).

**Die Erweiterung muss `workspace` sein**, in `package.json`:

```json
"extensionKind": ["workspace"]
```

Nur dort erreicht sie den Node auf `127.0.0.1` der WSL und das Binary unter
`~/.local/bin/`. Als `ui` liefe sie unter Windows und sähe weder das eine noch das andere.
Dasselbe gilt für SSH-Remotes: Die Erweiterung läuft neben dem Node. Im Devcontainer läuft
sie im Container; wie sie dort einen Node erreicht, hängt vom Weg ab — noch nicht gebaut
([`konzept.md`](konzept.md), „Devcontainer“). Folge: Sie muss auch **dort** installiert sein, nicht nur im Windows-VS-Code (siehe
Installation).

## Installation — ohne Marketplace

Der Marketplace ist nicht nötig. Eine Erweiterung ist eine Datei `.vsix` und lässt sich lokal
installieren:

```sh
code --install-extension kephalaion-0.3.0.vsix
```

oder in VS Code über „Extensions: Install from VSIX…“. Unter WSL installiert `code` aus einem
Terminal der WSL in den VS-Code-Server der WSL, also dorthin, wo eine Erweiterung der Art
`workspace` hingehört — bestätigt am 2026-09-26: `code` meldet dabei „Installing extensions on
WSL: Ubuntu…“. Cursor und VSCodium nehmen dieselbe Datei.

Was ohne Marketplace fehlt: automatische Updates. Die übernimmt das gemeinsame Release (unten).

## Ein Repository, ein Release

**Entschieden am 2026-09-26: Die Erweiterung liegt in diesem Repository und wird mit dem
Binary zusammen veröffentlicht, unter derselben Version.** Ein Release erneuert beide, auch
wenn sich nur eines geändert hat. Bei wenigen Nutzern ist das unkritisch; bei vielen Nutzern
lässt es sich später trennen.

- **Gleiche Version heißt: kein Abgleich von Versionen zwischen Erweiterung und Node.** Die
  Erweiterung fragt beim Start die Version des Nodes ab (`whoami`) und weist auf einen
  Unterschied hin, statt Kompatibilitäten zu verwalten.
- **Verzeichnis:** etwa `vscode/` im Repository, mit eigenem `package.json`. Der Build
  braucht Node.js und `@vscode/vsce`; das kommt zur CI hinzu.
- **Auslieferung, zwei Möglichkeiten:**
  - als eigenes Asset `kephalaion-<version>.vsix` am Release, neben den Binaries und in
    `SHA256SUMS`;
  - oder **ins Binary eingebettet** (`go:embed`, eine `.vsix` ist klein) mit einem Befehl
    wie `kephalaion vscode install`, der sie auspackt und `code --install-extension`
    aufruft. Dann bringt `kephalaion upgrade` die passende Erweiterung gleich mit, und es gibt
    nur eine Datei zu verteilen. Der Go-Build hängt dann vom Bau der Erweiterung ab
    (Makefile).
  - Neigung: eingebettet — ein Binary, eine Version, ein Upgrade.
- **Vorabversionen:** `vsce` nimmt nach bisheriger Kenntnis keine Version mit Suffix
  (`0.3.0-rc1`) an, sondern nur `major.minor.patch` und kennzeichnet Vorabversionen über
  einen Schalter. Beim Bau zu prüfen, wie ein Tag `v0.3.0-rc1` auf die Version der
  Erweiterung abgebildet wird.

## Suche: bewusst nicht

Die Suche von VS Code (Strg+Umschalt+F) und Quick Open (Strg+P) finden in `keph://`-Ordnern
nichts. Die Schnittstellen dafür (`TextSearchProvider`, `FileSearchProvider`) sind
„proposed API“ und in einer normalen Installation nicht freigeschaltet.

**Das ist gewollt** (2026-09-26): In VS Code wird Code gesucht, nicht Dokumentation;
Treffer aus der Doku stören dort. Gesucht wird im Store über MCP — von der KI, mit der Suche
des Nodes (FTS5, später mehr), die schneller und besser rankt, als es die Textsuche von
VS Code könnte. Die Erweiterung bekommt keinen eigenen Suchbefehl.

**Ausnahme, beobachtet am 2026-09-26:** Dokumente, die VS Code gerade geladen hat, findet die
Suche doch — sie durchsucht neben der Platte immer auch die offenen Dokumente, damit
Ungespeichertes gefunden wird, unabhängig vom Dateisystem. Nach dem Schließen verschwindet der
Treffer (mit etwas Verzögerung, erst nach einer weiteren Suche). Das ist in Ordnung.

## Offen

- Wie der Node-Dienst gestartet wird, wenn er nicht läuft — Hinweis in VS Code oder selbst
  starten (wie k-playbook beim Briefing).
- Abstand der Abfrage von `changes` — fest, oder länger, solange das Fenster nicht im Fokus
  ist.
- Leere Verzeichnisse: vorerst nur in der Erweiterung gemerkt (Task 014); ob später als
  `SYSTEM:D:`-Zeile am Hub, bleibt offen (vgl. „Persönliche Verzeichnisse“ im Konzept).
- Das Schreiben im echten VS Code: die Handgriffe unter „Umsetzung: Schreiben“, „Im echten
  VS Code noch zu prüfen“ — vor allem, ob der `FileService` den Provider so ruft wie
  angenommen.
- Wie die Erweiterung mit `personal`-Verzeichnissen umgeht (nur Eigenes zeigen, Schalter für
  alles).
- Ob eine TreeView zusätzlich zum Dateisystem sinnvoll ist, etwa für Status und Abgleich.

## Versuch: Dummy (2026-09-26)

Unter [`vscode/`](../vscode/): reines JavaScript ohne Abhängigkeiten, ein
`FileSystemProvider` für `keph://` mit einem festen Verzeichnis und einer Datei, nur lesen,
ohne Verbindung zum Node. Zwei Befehle: „Dummy-Datei öffnen“ und „Dummy-Ordner zum Workspace
hinzufügen“. Gebaut mit `npx @vscode/vsce package --skip-license`, installiert mit
`code --install-extension` aus der WSL.

Ergebnis:

- Die Datei öffnet sich schreibgeschützt, die Markdown-Vorschau funktioniert.
- Der Ordner erscheint im Explorer als Ordner des Workspace; das Fenster wird dabei zu
  einem Workspace mit mehreren Ordnern und lädt neu.
- Die Suche verhält sich wie oben beschrieben.
- Der Weg trägt; offen ist nur noch die Anbindung an den Node.

## Umsetzung: Lesen (2026-09-26)

Ohne Task gebaut, in zwei Schritten, weiter reines JavaScript ohne Abhängigkeiten
([`vscode/extension.js`](../vscode/extension.js)). MCP über das `fetch` von Node.js, JSON-RPC
ohne Sitzung — das SDK braucht es dafür nicht.

- **0.0.2 — `whoami`:** Adresse aus `listen`, Tokens aus `tokens/<hub>/<account>.token`
  (höchstens alle 5 s neu von der Platte), Statusleiste mit Tooltip, abgefragt alle 30 s;
  Menü; „Collection einbinden“. Im echten VS Code geprüft.
- **0.0.3 — Inhalte:** `stat` über `read` mit `content: false`, `readDirectory` über `list`
  (Namen kommen als voller Pfad ab der Collection, die Erweiterung nimmt das letzte Segment;
  blättert mit `cursor`), `readFile` über `read` — der Inhalt ist der Text des Ergebnisses.
  `changes` alle 3 s mit dem Cursor der letzten Antwort; je Eintrag `Changed` bzw. `Deleted`
  für das Dokument und `Changed` für jedes Verzeichnis darüber bis zur Collection, damit der
  Explorer neue und leer gewordene Verzeichnisse sieht. Ein Umbenennen kam so als
  gelöscht und neu an, die `id` wertete die Erweiterung dafür nicht aus — *überholt mit 0.0.5:*
  Sie wird jetzt ausgewertet, siehe „Umsetzung: Schreiben“. `reset` und
  `dropped` lassen die Wurzel des Hubs neu lesen. Dazu „Kephalaion: Status anzeigen“ — der
  Inhalt des Tooltips im Output „Kephalaion“, weil der Tooltip schwer zu finden ist (der
  Tooltip eines Ordners im Explorer zeigt nur den Pfad, etwa `\eins`).
- **Fehler beim Lesen:** Eine Antwort des Werkzeugs mit Fehler („nicht lesbar“) wird zu
  `FileNotFound`, ein Fehler der Verbindung zu `Unavailable` — gilt weiter für `stat`,
  `readDirectory` und `readFile`; beim Schreiben entscheidet seit 0.0.5 der Code (siehe
  „Umsetzung: Schreiben“).
- ~~**Alles schreibgeschützt**, auch mit `writable: true`, bis Schreiben gebaut ist.~~
  *Überholt mit 0.0.5:* schreibgeschützt nur ohne `writable`, `isReadonly` ist `false`.
- **Geprüft** mit einem Ersatz für das Modul `vscode` gegen den laufenden Node (`home:eins`,
  Dokumente unter `test/`): Verzeichnisse, Metadaten, Inhalt, „nicht gefunden“, fremde
  Collection, Ereignisse nach `hub doc put` und `node sync`. Im echten VS Code: ein Dokument
  aus `keph://home/eins` geöffnet.
- **0.0.4 — Account wählen:** alle `tokens/<hub>/*.token` je Hub, Wahl über „Kephalaion:
  Account wählen“ in `kephalaion.accounts` (siehe „Status und Auswahl“). Eine andere Wahl —
  auch von Hand in `settings.json` — liest die Tokens sofort neu, setzt `changes` neu an und
  lässt die Hubs neu lesen, weil Rechte und Collections dann andere sein können. Tooltip und
  „Status anzeigen“ nennen die Accounts eines Hubs und welcher gewählt ist. Geprüft mit dem
  Ersatz für `vscode` und Kopien der Token-Dateien samt einem erfundenen zweiten Account:
  ohne Wahl, gewählt, gewählt ohne Datei, falscher Account gewählt; `.pending` übergangen.

## Umsetzung: Schreiben (2026-09-27, 0.0.5)

Mit Task 014 gebaut, weiter reines JavaScript ohne Abhängigkeiten
([`vscode/extension.js`](../vscode/extension.js), [`vscode/README.md`](../vscode/README.md)).
Jeder Schreibvorgang geht über die Werkzeuge des Nodes zum Hub (`create`, `write`, `delete`,
`rename`), wird nie wiederholt, und nach Erfolg feuert die Erweiterung die Ereignisse selbst.

- **`stat` und Schreibschutz:** schreibgeschützt nur ohne `writable` — ein Recht je
  Collection (`write`); `isReadonly` ist `false`. Ein fremdes Dokument ohne `supersede`
  scheitert erst beim Speichern mit `NoPermissions`; `supersede` ohne `write` bleibt
  schreibgeschützt (bewusste Grenze, siehe „Rechte“ oben).
- **`writeFile`:** zuerst `read` mit `content: false`; `none` → `create`, `document` →
  `write` mit dessen Revision, `directory` → `FileIsADirectory`. Der Inhalt wird vorher streng
  als UTF-8 geprüft — ein BOM bleibt erhalten —, ohne NUL-Byte und höchstens 1 MiB; sonst eine
  klare Meldung („keine Textdatei“, „zu groß“), ohne dass etwas abgeschickt wird.
- **`delete`** mit `recursive` aus den Optionen; ein nur gemerktes leeres Verzeichnis wird
  vergessen, ohne den Hub zu fragen. **`rename`** nur innerhalb einer Collection; über
  Collections oder Hubs eine Meldung („dafür kopieren und löschen“), ein belegtes Ziel
  `FileExists`, mit `overwrite` die Meldung, dass Umbenennen nicht überschreibt. `delete` und
  `rename` schicken kein `base_revision`.
- **`createDirectory`** legt am Hub nichts an: Das leere Verzeichnis gilt nur in diesem
  Fenster, bis darin etwas liegt oder es gelöscht wird; `stat` und `readDirectory` zeigen es.
  Nach dem Neuladen des Fensters ist es weg — gewollt.
- **Tabelle `id` → Name** je Collection, gespeist aus `list`, `read`, `changes` und den
  Antworten eigener Schreibvorgänge; ein älterer Stand überschreibt keinen neueren. Meldet
  `changes` eine bekannte `id` unter neuem Namen: `Deleted` für den alten Namen, `Created` für
  den neuen (bzw. `Deleted`, wenn er danach gelöscht wurde), `Changed` für beide Elternpfade —
  so sieht ein zweites VS Code das Umbenennen. `reset` verwirft die Tabellen des Hubs,
  `dropped` die der Collection, ein anderer Account alle.
- **Fehler nach dem Code** in `structuredContent.error.code`, nie nach der Meldung:

  | Code des Nodes | `FileSystemError` |
  |---|---|
  | `name_taken` | `FileExists` |
  | `not_found` | `FileNotFound` |
  | `forbidden`, `not_readable` | `NoPermissions` |
  | `unreachable`, `outcome_unknown` | `Unavailable`, mit der Meldung |
  | alle anderen, auch `stale_revision` | ein Fehler mit der Meldung des Nodes |

  Die Fehler von `read` (auch vor einem Schreibvorgang) tragen keinen Code; die Erweiterung gibt
  dann die Meldung des Nodes weiter. Auf dem Weg zum Node unterscheidet sie drei Arten: nicht
  erreichbar (nichts abgeschickt — „nichts gespeichert“), Ausgang unklar (Verbindung nach dem
  Abschicken abgebrochen, 5xx, unlesbare Antwort — „gespeichert sein kann es, nicht
  wiederholen“) und abgelehnt.
- **Geprüft** mit dem Ersatz für `vscode` gegen den laufenden Node (`home:eins`, unter
  `test/schreiben/…`, 118 Prüfungen): neue Datei leer, dann mit Inhalt; zweimal speichern;
  Konflikt nach `hub doc put`; Löschen von Datei und Verzeichnis; Umbenennen von Datei und
  Verzeichnis mit einem zweiten Client, der über `changes` den alten Namen verschwinden und den
  neuen kommen sieht (auch umbenennen, dann löschen, ein Abgleich); leeres Verzeichnis anlegen
  und befüllen; Binärdatei, NUL und Größe abgelehnt; fremdes Dokument erst beim Speichern
  `NoPermissions`; Node nicht erreichbar. In einem isolierten Aufbau mit Node über `http`: Hub
  gestoppt → `Unavailable`, Lesen geht weiter; `outcome_unknown` und abgebrochene Verbindung.
  Der Weg darunter — zwei Nodes, Konflikt zwischen ihnen, Hub gestoppt — steht als Go-Test
  `TestMCPWriteTwoNodes` in `cmd/kephalaion`. Befund dazu:
  `k-playbook-local/material/befunde/vscode-schreiben.md`.

### Im echten VS Code noch zu prüfen

**Offen, Stand 2026-09-27** — der Nutzer geht die Handgriffe selbst durch, nur unter `test/`
in `home:eins`. Im Output „Kephalaion“ (Menü → Log) steht jeder Aufruf mit Vorgang, Name und
Ausgang; daran lässt sich ablesen, was VS Code wirklich aufruft.

1. **Installieren**, aus einem Terminal der WSL:
   `code --install-extension vscode/kephalaion-0.0.5.vsix` (im Repository; fehlt die Datei:
   `cd vscode && npx --yes @vscode/vsce package --skip-license`), dann „Developer: Reload
   Window“. Die Statusleiste zeigt `Keph home`, `keph://home/eins` ist eingebunden.
2. **Speichern:** ein Dokument unter `test/` öffnen, ändern, Strg+S — und gleich noch einmal
   ändern und speichern: kein Konflikt. `kephalaion hub doc get eins test/…` zeigt den Stand.
3. **Neue Datei** im Explorer unter `test/`: erscheint leer (`create` mit leerem Inhalt),
   dann Inhalt tippen und speichern (`write`).
4. **Neuer Ordner** unter `test/`, darin eine neue Datei: danach besteht der Ordner am Hub.
   Einen zweiten leeren Ordner anlegen und „Developer: Reload Window“: Er ist weg — gewollt,
   leere Ordner gelten nur je Fenster.
5. **Ordner per Drag & Drop** aus dem Projekt (Explorer von VS Code oder von Windows) nach
   `keph://home/eins/test/`, mit Unterordnern: alles kommt an. Einmal mit einer Binärdatei
   (etwa `.png`) darin: nur diese scheitert mit „keine Textdatei“, die übrigen kommen an.
6. **Umbenennen** einer Datei und eines Ordners im Explorer (F2): Die `id` bleibt — im Log
   nennt die Zeile `rename …` dieselbe `id` wie das `create` der Datei aus Punkt 3 —, und ein
   zweites Fenster mit derselben Collection sieht den alten Namen verschwinden und den neuen
   kommen.
7. **Verschieben** per Drag & Drop innerhalb der Collection. Auf ein **belegtes Ziel** mit
   „Ersetzen“: Was geschieht? Erwartet ist, dass VS Code das Ziel selbst löscht und dann
   umbenennt (im Log `delete …`, dann `rename …`); sonst erscheint die Meldung „Umbenennen
   überschreibt nicht“.
8. **Verschieben in eine andere Collection** (oder einen anderen Hub; braucht eine zweite
   eingebundene Collection mit `write`): die Meldung „Verschieben nur innerhalb einer
   Collection … kopieren und löschen“; Strg+Drag kopiert dagegen.
9. **Löschen** einer Datei und eines Ordners im Explorer (Entf), die Rückfrage bestätigen —
   einen Papierkorb gibt es für `keph://` nicht; danach fehlen beide auch am Hub
   (`kephalaion hub doc list eins test/`).
10. **„Datei ist neuer“:** ein Dokument unter `test/` öffnen und ändern, ohne zu speichern; am
    Hub `echo neu | kephalaion hub doc put eins test/…`; `kephalaion node sync home` (oder den
    Abgleich abwarten, bis 30 s); dann in VS Code speichern: VS Code meldet, die Datei sei
    neuer. „Vergleichen“ zeigt beide Stände, „Überschreiben“ speichert — danach am Hub den
    eigenen Inhalt prüfen.
11. **Konflikt vor dem Abgleich:** wie 10, aber sofort nach `hub doc put` speichern, bevor
    abgeglichen ist: eine Fehlermeldung „… hat Revision …, der Vorgang beruht auf …“, am Hub
    bleibt `neu`.
12. **Fremdes Dokument:** `echo fremd | kephalaion hub doc put eins test/fremd.md` (gehört
    `admin`), in VS Code öffnen — es ist nicht schreibgeschützt —, ändern und speichern: erst
    jetzt „gehört admin, supersede fehlt“ (`NoPermissions`); am Hub bleibt `fremd`.
13. **Noch unbestätigt** (Befund `vscode-schreiben.md`, „Wie VS Code den Provider beim Schreiben
    ruft“), im Log nachsehen: ruft der `FileService` `writeFile` immer mit `create` und
    `overwrite` auf (Speichern und neue Datei gelingen, kein `FileExists`/`FileNotFound`), legt
    er fehlende Eltern über `createDirectory` an (beim Drop eines verschachtelten Ordners Zeilen
    `createDirectory … nur hier gemerkt` vor den `create`), und löscht er bei „Ersetzen“ das Ziel
    selbst vor `rename` (Punkt 7)?

## Fundstellen

- API-Referenz `FileSystemProvider`:
  https://code.visualstudio.com/api/references/vscode-api#FileSystemProvider
- Beispiel eines Dateisystems im Speicher („MemFS“), die beste Vorlage:
  https://github.com/microsoft/vscode-extension-samples/tree/main/fsprovider-sample
- Virtuelle Workspaces — was in ihnen geht und was nicht:
  https://code.visualstudio.com/api/extension-guides/virtual-workspaces
- Erweiterungen unter Remote, WSL, Devcontainer (`extensionKind`):
  https://code.visualstudio.com/api/advanced-topics/remote-extensions
- Einstieg in eine erste Erweiterung:
  https://code.visualstudio.com/api/get-started/your-first-extension
- Verpacken und lokal installieren (`vsce package`, `.vsix`):
  https://code.visualstudio.com/api/working-with-extensions/publishing-extension
- WASI in VS Code (nur zur Einordnung):
  https://code.visualstudio.com/blogs/2023/06/05/vscode-wasm-wasi
- MCP-Client in TypeScript: https://github.com/modelcontextprotocol/typescript-sdk
- Vorbild aus der Praxis: „GitHub Repositories“ von Microsoft, bindet Repos als
  `vscode-vfs://` ein, ohne zu klonen.
