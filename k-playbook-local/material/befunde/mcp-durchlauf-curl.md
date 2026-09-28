---
thema: Werkzeuge des Nodes von Hand über curl aufrufen
begonnen: 2026-09-27
zuletzt: 2026-09-27
status: geklaert
---

# Werkzeuge des Nodes von Hand über curl aufrufen

Wie prüft man ein Werkzeug gegen den laufenden Node (`serve` als Dienst), ohne Client-Code?

## 2026-09-27 — Ein einzelner POST `tools/call` genügt, ohne `initialize` und ohne Sitzung

**Befund:** Der Node antwortet auf einen einzelnen POST an `/mcp` mit `tools/call` direkt als
JSON (`Content-Type: application/json`, kein SSE, keine `Mcp-Session-Id`), wenn der Header
`Accept: application/json, text/event-stream` gesetzt ist; `initialize` ist nicht nötig. Die
Anmeldung sind die Header `X-Keph-Account-<alias>` und `X-Keph-Token-<alias>`, das Token aus
`~/.config/kephalaion/tokens/<alias>/<account>.token`. Das neue Binary kommt per `make
dev-install` in den Dienst (ersetzt `~/.local/bin/kephalaion`, startet `kephalaion.service` neu).
**Beleg:** Durchlauf Task 015: `curl -i -X POST http://127.0.0.1:7433/mcp … -d
'{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list","arguments":{…}}}'` →
`HTTP/1.1 200 OK`, `Content-Type: application/json`, Body `{"jsonrpc":"2.0","id":1,"result":
{"content":[…],"structuredContent":{…}}}`.
**Sicherheit:** bestaetigt
**Sackgassen:** Die Antwort als SSE zu lesen (`sed -n 's/^data: //p'`) liefert nichts — der Node
schickt bei einem einzelnen Aufruf kein `text/event-stream`.

<!-- sitzung: task-015-run-2026-09-27 -->
