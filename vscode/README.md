# Kephalaion für VS Code

Zeigt Collections von Kephalaion als Baum „Kephalaion“ im Explorer
(`keph://<hub>/<collection>/…`), dazu den Stand des Nodes in der Statusleiste. Idee und Weg:
[`../docs/vscode.md`](../docs/vscode.md).

Stand (Task 045, 2026-11-26): lesen und schreiben im Baum und im Editor; Collections sind keine
Workspace-Ordner mehr; ohne Einstellung zeigt der Baum nur die Collection des Projekts; der Node
als MCP-Server für Copilot. Braucht VS Code 1.101 oder neuer.

- **Lesen** aus der Replica des Nodes über `list`, `read` und `changes` — auch offline, solange
  der Node läuft. Änderungen anderer erscheinen nach dem Abgleich des Nodes, ein Umbenennen
  als alter Name weg, neuer da.
- **Schreiben** über den Node beim Hub: speichern, neue Datei, löschen, umbenennen und
  verschieben — auch ganze Ordner, per Drag & Drop. Schreibbar ist eine Collection mit dem
  Recht `write`; ein fremdes Dokument ohne `supersede` scheitert erst beim Speichern.
  Ein Konflikt (inzwischen geändert) wird abgelehnt, nichts wird still überschrieben.
- **Baum „Kephalaion“** im Explorer (seit Task 043): die Collections als `<hub>:<collection>`,
  darunter Verzeichnisse und Dokumente; Klick öffnet. Kontextmenü und Titelleiste: neue Datei,
  neuer Ordner, umbenennen (F2), löschen (Entf, mit Bestätigung), aktualisieren, zur Seite
  öffnen, URI bzw. relativen Pfad kopieren, „In den Projektordner kopieren…“. Ziehen und
  Ablegen: im Baum innerhalb einer Collection verschieben, Dateien und Ordner aus dem Projekt
  in den Baum kopieren (nie überschreiben), Dokumente in den Editor ziehen.
- **Welche Collections:** Einstellung `kephalaion.collections` (Liste von `<hub>:<collection>`);
  gesetzt geht sie vor. Ungesetzt die **Collection des Projekts**: `hub` und `collection` im
  Block `kephalaion:` der `.k-playbook/K-PLAYBOOK.yaml` jedes Projektordners (nur dort, nicht
  weiter oben); eine Änderung der Datei zeigt der Baum sofort. **Geändert mit Task 045:**
  Bisher zeigte er ungesetzt alle lesbaren Collections; ein Projekt ohne `K-PLAYBOOK.yaml`
  zeigt nach dem Update einen leeren Baum — zurück mit „Collection im Baum zeigen“ oder mit
  gesetzter `kephalaion.collections`. „Kephalaion: Collection im Baum zeigen“ und „… aus dem
  Baum nehmen“ schreiben die Einstellung — ungesetzt von den gezeigten aus; danach zählt die
  Datei nicht mehr, bis man die Einstellung entfernt — auf Workspace-Ebene, wenn ein Workspace
  oder Ordner offen ist — **eine eingecheckte `.code-workspace` oder `.vscode/settings.json`
  ändert sich dadurch.**
- **Kein Workspace-Ordner:** Die Erweiterung hängt keine Collection in den Workspace ein. Einen
  `keph://`-Ordner neben einem anderen Ordner nimmt sie beim Start und nach jeder Änderung der
  Workspace-Ordner heraus, ohne Einstellung (in einem gespeicherten Workspace schreibt VS Code
  dabei die Ordnerliste der `.code-workspace`-Datei um); ist `kephalaion.collections` gesetzt
  oder wird seine Collection nicht gezeigt, nimmt sie sie dort auf — keine verschwindet still.
  So fragt „Neues Terminal“ nicht nach dem Ordner, und Sprachserver, die an einem
  `keph://`-Ordner scheitern (etwa ty), sehen keinen. Ein Workspace nur aus Collections bleibt,
  ein geduldeter Grenzfall. Für ein Projekt ohne `K-PLAYBOOK.yaml` gehört
  `"kephalaion.collections": ["<hub>:<collection>"]` in die `settings` seiner Workspace-Datei.
  Eine noch gesetzte Einstellung `kephalaion.workspaceFolders` (bis Task 044) zeigt VS Code als
  unbekannt; sie wirkt nicht mehr.
- **Dokument öffnen und Suchen:** „Kephalaion: Dokument öffnen“ (alle Dokumente der Collections
  im Baum) und „Kephalaion: Suchen“ (das Werkzeug `search` des Nodes) — Strg+P und
  Strg+Umschalt+F finden in Collections nichts.
- **Grenzen:** nur Text (UTF-8, ohne NUL) bis 1 MiB je Dokument; umbenennen und verschieben
  nur innerhalb einer Collection, ohne ein belegtes Ziel zu überschreiben; ein leerer Ordner
  besteht nur in diesem Fenster, bis darin etwas liegt. Ist der Hub nicht erreichbar, wird
  nicht gespeichert — die Meldung sagt es. „Kephalaion: Suchen“ sucht nur unter `docs/` einer
  Collection, in einfachen englischen Wörtern. Im Baum kein Kopieren und Einfügen über die
  Zwischenablage (dafür Ziehen und Ablegen und „In den Projektordner kopieren…“) und kein
  Kopieren zwischen Collections (nur eine Meldung).
- **Node:** Adresse aus `listen` im Abschnitt `node:` der config
  (`~/.config/kephalaion/config.yaml`, abweichend `KEPHALAION_CONFIG`, `XDG_CONFIG_HOME`);
  überschreibbar mit der Einstellung `kephalaion.nodeUrl`.
- **Anmeldung:** aus `~/.config/kephalaion/tokens/<hub>/<account>.token`; ohne Einrichtung. Der
  Account je Hub in derselben Reihenfolge wie die Kommandozeile: die Einstellung
  `kephalaion.accounts` („Kephalaion: Account wählen“), der Standard-Account
  (`kephalaion node account default`), die einzige Token-Datei. Trifft nichts zu, fehlt die
  Wahl; die Statusleiste nennt die Kandidaten.
- **MCP-Server für Copilot:** Die Erweiterung meldet den Node als MCP-Server „Kephalaion“, mit
  den Header-Paaren aus den Token-Dateien wie `kephalaion node mcp headers` (ein Hub, an dem
  die Wahl fehlt, fehlt). Das Token setzt sie erst beim Start des Servers ein, nie
  in die gemeldete Definition. Solange VS Code auf „Trace“ protokolliert, meldet sie ihn nicht:
  VS Code schriebe die Header sonst ins Log. Nach `kephalaion node account rotate` den Server
  neu starten („MCP: Server auflisten“ → Kephalaion → Neu starten). Abschalten mit
  `kephalaion.mcpServer.enabled: false`.

Bauen und installieren (unter WSL aus einem WSL-Terminal, dann landet die Erweiterung im
VS-Code-Server der WSL):

```sh
make vscode-install   # baut dist/kephalaion-<version>.vsix und installiert sie mit code
```

Nur bauen: `make vscode-vsix`. Braucht Node.js (`vsce` kommt per `npx`).

Danach „Developer: Reload Window“. Die Statusleiste zeigt `Keph <hub>`; ein Klick öffnet das
Menü (Status, neu verbinden, Account wählen, Collection im Baum zeigen bzw. herausnehmen,
Dokument öffnen, Suchen, Log).
