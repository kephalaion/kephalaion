# Kephalaion für VS Code

Zeigt Collections von Kephalaion als Ordner (`keph://<hub>/<collection>/…`) und den Stand des
Nodes in der Statusleiste. Idee und Weg: [`../docs/vscode.md`](../docs/vscode.md).

Stand (0.0.5): lesen und schreiben.

- **Lesen** aus der Replica des Nodes über `list`, `read` und `changes` — auch offline, solange
  der Node läuft. Änderungen anderer erscheinen nach dem Abgleich des Nodes, ein Umbenennen
  als alter Name weg, neuer da.
- **Schreiben** über den Node beim Hub: speichern, neue Datei, löschen, umbenennen und
  verschieben — auch ganze Ordner, per Drag & Drop. Schreibbar ist eine Collection mit dem
  Recht `write`; ein fremdes Dokument ohne `supersede` scheitert erst beim Speichern.
  Ein Konflikt (inzwischen geändert) wird abgelehnt, nichts wird still überschrieben.
- **Grenzen:** nur Text (UTF-8, ohne NUL) bis 1 MiB je Dokument; umbenennen und verschieben
  nur innerhalb einer Collection, ohne ein belegtes Ziel zu überschreiben; ein leerer Ordner
  besteht nur in diesem Fenster, bis darin etwas liegt. Ist der Hub nicht erreichbar, wird
  nicht gespeichert — die Meldung sagt es.
- **Node:** Adresse aus `listen` im Abschnitt `node:` der config
  (`~/.config/kephalaion/config.yaml`, abweichend `KEPHALAION_CONFIG`, `XDG_CONFIG_HOME`);
  überschreibbar mit der Einstellung `kephalaion.nodeUrl`.
- **Anmeldung:** aus `~/.config/kephalaion/tokens/<hub>/<account>.token`; ohne Einrichtung. Bei
  mehreren Accounts an einem Hub: „Kephalaion: Account wählen“.

Bauen und installieren (unter WSL aus einem WSL-Terminal, dann landet die Erweiterung im
VS-Code-Server der WSL):

```sh
make vscode-install   # baut dist/kephalaion-<version>.vsix und installiert sie mit code
```

Nur bauen: `make vscode-vsix`. Braucht Node.js (`vsce` kommt per `npx`).

Danach „Developer: Reload Window“. Die Statusleiste zeigt `Keph <hub>`; ein Klick öffnet das
Menü (Status, neu verbinden, Account wählen, Collection einbinden, Log).
