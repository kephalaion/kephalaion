Kephalaion ist eine geteilte Wissensablage aus Textdokumenten für Menschen, KIs und Projekte. Dieser MCP-Server ist ein Node: ein Dienst, meist auf deinem Rechner, der von jedem Hub, bei dem er eingetragen ist, eine Kopie hält (Replica). Der Hub ist der einzige Schreiber; du sprichst nie direkt mit ihm.

Lesen ist lokal, Schreiben geht über den Hub:
- `list`, `read`, `search` und `changes` lesen aus der Replica, auch ohne Netz.
- `create`, `write`, `delete` und `rename` gehen über den Hub. Deine Änderung steht sofort in der Replica, Änderungen anderer (auch an deinen Rechten) erst nach dem Abgleich, den der Node regelmäßig macht.
- `sync` fordert einen Abgleich an und wartet darauf; nur nötig, wenn du den Stand von jetzt brauchst.

Eine Collection heißt `<hub>:<collection>`, wie `whoami` sie nennt; bist du nur bei einem Hub angemeldet, darf der Hub-Teil fehlen. Darin ist ein Dokument ein Pfad wie `docs/manual/thema.md`, mit fester id und Revision. Rechte gelten je Collection.

Beginne mit `whoami`: je Hub die Anmeldung (`login`), deine Collections mit Rechten, der Stand des Abgleichs. Fehlt dort etwas, behebst du es nicht über MCP, auch nicht mit `sync`; der Mensch richtet es auf der Kommandozeile ein:
- `login` `missing`: am Rechner des Assistenten `kephalaion node account setup` mit Einrichtungstoken vom Admin, für einen schon eingerichteten Account `kephalaion node mcp add`; danach eine neue Sitzung.
- `login` `invalid`: `kephalaion node account list` zeigt, ob das Token beim Hub gilt. Gilt es, holt `kephalaion node sync` am Rechner des Nodes den Stand; sonst neu mit `kephalaion node account setup`.
- `never_synced` (noch keine Replica): `kephalaion node sync` am Rechner des Nodes.
Tokens siehst du nie, frag nicht danach.

`docs/` einer Collection ist das durchsuchbare Wissen: `search` sucht nur dort, `docs/index.md` gibt die Übersicht, `rebuild_docs` baut sie nach Änderungen neu.

Ausführlich: https://github.com/kephalaion/kephalaion, in docs/ konzept.md, begriffe.md und installation.md.
