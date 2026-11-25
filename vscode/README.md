# Kephalaion für VS Code

Zeigt Collections von Kephalaion als Baum „Kephalaion“ im Explorer und wahlweise als Ordner des
Workspace (`keph://<hub>/<collection>/…`), dazu den Stand des Nodes in der Statusleiste. Idee
und Weg: [`../docs/vscode.md`](../docs/vscode.md).

Stand (0.0.6): lesen und schreiben; der Node als MCP-Server für Copilot. Braucht VS Code 1.101
oder neuer.

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
- **Welche Collections:** Einstellung `kephalaion.collections` (Liste von `<hub>:<collection>`),
  ungesetzt alle lesbaren. „Kephalaion: Collection im Baum zeigen“ und „… aus dem Baum nehmen“
  schreiben sie auf Workspace-Ebene, wenn ein Workspace oder Ordner offen ist — **eine
  eingecheckte `.code-workspace` oder `.vscode/settings.json` ändert sich dadurch.**
- **Ohne Workspace-Ordner:** `kephalaion.workspaceFolders: false` nimmt alle `keph://`-Ordner
  aus dem Workspace (in einem gespeicherten Workspace schreibt VS Code dabei die
  `.code-workspace`-Datei um), zeigt ihre Collections im Baum und merkt sich die Ordner; zurück
  auf `true` kommen genau diese wieder. Ein Workspace nur aus Collections bleibt. So fragt
  „Neues Terminal“ nicht mehr nach dem Ordner, und Sprachserver, die an einem `keph://`-Ordner
  scheitern (etwa ty), sehen keinen mehr. Standard ist vorerst `true`, wie bisher.
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

**Terminal:** Bei Collections neben genau einem echten Projekt ergänzt die Erweiterung beim
Start und nach Workspace-Änderungen eine portable Workspace-cwd-Vorgabe, ohne explizite
Benutzer-/Workspace-/Ordner-Werte zu überschreiben. Mehrere Projekte oder mehrdeutige Namen
bleiben unverändert; Schreibfehler stehen nur im Log. VS Codes Standardbefehl kann wegen der
verschiedenen URI-Schemata trotzdem nach dem Ordner fragen. **„Kephalaion: Projektterminal
öffnen“** startet direkt im eindeutigen Projekt, ohne Auswahl und ohne Einstellungen zu ändern
(expliziter Projektbefehl, unabhängig von cwd-Vorgaben). Details und Quellen in
[`../docs/vscode.md`](../docs/vscode.md#terminal-im-projektordner).

Bauen und installieren (unter WSL aus einem WSL-Terminal, dann landet die Erweiterung im
VS-Code-Server der WSL):

```sh
make vscode-install   # baut dist/kephalaion-<version>.vsix und installiert sie mit code
```

Nur bauen: `make vscode-vsix`. Braucht Node.js (`vsce` kommt per `npx`).

Danach „Developer: Reload Window“. Die Statusleiste zeigt `Keph <hub>`; ein Klick öffnet das
Menü (Status, neu verbinden, Account wählen, Collection einbinden, Collection im Baum zeigen
bzw. herausnehmen, Dokument öffnen, Suchen, Log).
