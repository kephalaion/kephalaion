---
thema: Schreiben über MCP gegen den laufenden Node
begonnen: 2026-09-27
zuletzt: 2026-09-27
status: geklaert
---

# Schreiben über MCP gegen den laufenden Node

Verhalten sich `create`, `write`, `delete` und `rename` im Betrieb (Dienst pro User, Hub-Eintrag
`home` über `local`) so, wie Konzept und Task 014 es festlegen?

## 2026-09-27 — Alle Schreibwerkzeuge verhalten sich im Betrieb wie festgelegt

**Befund:** Gegen den laufenden Node mit dem Account `kamran-desktop` (User `kamran`, `read` und
`write` in `home:eins`, kein `supersede`) verhalten sich die vier Werkzeuge wie festgelegt:
Urheber ist der User; `read` direkt nach dem Schreiben zeigt die neue Revision ohne Abgleich;
`name_taken`, `stale_revision` (write, rename, delete), `not_found`, `path_conflict` in beiden
Richtungen, `invalid` für `SYSTEM:`, `..`, `/` am Ende, leeren Namen, NUL und mehr als 1 MiB;
`rename` behält die `id`, ein Verzeichnis unter einer Revision; `delete` eines Verzeichnisses
nur mit `recursive`, dann unter einer Revision; ein gelöschter Name bekommt neu angelegt eine
neue `id`; ein Dokument von `admin` ergibt bei `write`, `delete` und `rename` `forbidden`
(„gehört admin, supersede fehlt“) und bleibt am Hub unverändert; ein eigenes Dokument, das
`admin` zuletzt geändert hat, bleibt eigenes; fremde Collection und fehlende Anmeldung
`not_readable` ohne Anfrage an den Hub; `changes` meldet die eigene Änderung nach 0,2 s (Anstoß
des Abgleichs). Genau 1 MiB aus Steuerzeichen (`\u0001`, als JSON rund 6 MiB) geht durch
(226 ms); ein gewöhnlicher Schreibvorgang dauert rund 12 ms. Im Log von `serve` je Anfrage eine
Zeile mit Account, Vorgang, Hub und Node, kein Token, kein Inhalt.
**Beleg:** Skript mit einzelnen POSTs `tools/call` an `http://127.0.0.1:7433/mcp` (Header wie
in `mcp-durchlauf-curl.md`), jedes Ergebnis zusätzlich mit `kephalaion hub doc get|list eins …`
geprüft, alles unter `test/mcp-20260927113450/`, danach mit `delete` und `recursive` entfernt:
38 von 38 bestanden (eine Prüfung im zweiten Lauf, siehe Sackgassen). Log mit `journalctl
--user -u kephalaion` nach dem Token, einer Marke im Inhalt und `keph_…` durchsucht: nichts.
Tool-Liste (`tools/list`): `create`, `write`, `delete`, `rename` mit `error.code` in der
Beschreibung.
**Sicherheit:** bestaetigt
**Frage:** Hält das Schreiben im echten Betrieb, was die Go-Tests von Task 014 zeigen?
**Sackgassen:** Den Cursor von `changes` nur mit `collection` holen und dann mit `path`
weiterfragen scheitert mit „cursor gehört zu einer anderen Anfrage“ — der Cursor ist an
`collection` und `path` gebunden (so festgelegt); mit demselben `path` geholt, geht es. Nicht
im Betrieb prüfbar, weil `home` über `local` im selben Prozess läuft: „Hub nicht erreichbar“,
„Ausgang unklar“ und die Grenze des Bodys am Hub über HTTP — das deckt `TestMCPWriteTwoNodes`
(Task 014) ab.

<!-- sitzung: 2026-09-27, Test auf Wunsch des Nutzers nach Task 014 -->
