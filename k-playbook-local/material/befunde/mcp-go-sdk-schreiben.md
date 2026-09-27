---
thema: go-sdk (MCP) und die Werkzeuge, die schreiben
begonnen: 2026-09-27
zuletzt: 2026-09-27
status: geklaert
---

# go-sdk (MCP) und die Werkzeuge, die schreiben

Was das go-sdk v1.7.0 von sich aus tut, wenn der Node über MCP Dokumente annimmt und Fehler mit Code melden soll (Task 014, Etappe 3).

## 2026-09-27 — Der Streamable-HTTP-Handler nimmt ohne Einstellung nur 4 MiB Body an

**Befund:** `mcp.StreamableHTTPOptions.MaxRequestBodyBytes` ist ohne Angabe `mcp.DefaultMaxRequestBodyBytes` = 4 MiB; darüber antwortet der Handler 413. Ein Dokument von 1 MiB (`contract.MaxDocumentBytes`), dessen Bytes JSON als `\u00XX` schreibt, braucht gut 6 MiB. Der Node setzt deshalb `mcpnode.MaxRequestBytes` (7 MiB, dieselbe Rechnung wie `httpapi.MaxWriteBodyBytes`).
**Beleg:** `~/go/pkg/mod/github.com/modelcontextprotocol/go-sdk@v1.7.0/mcp/streamable.go:198-249` (Option, Vorgabe, `http.MaxBytesReader`); `TestWriteRequestLimit` in `internal/node/mcpnode/write_test.go` (1 MiB aus `\x01` geht durch, ein Body über der Grenze ergibt 413).
**Sicherheit:** bestaetigt

## 2026-09-27 — Ein Fehler aus einem typisierten Werkzeug trägt keine Struktur

**Befund:** Gibt ein `ToolHandlerFor` einen `error` zurück, macht das SDK daraus `isError: true` mit der Meldung als Text — ohne `structuredContent`. Soll ein Fehler maschinell lesbar einen Code tragen, gibt der Handler stattdessen ein eigenes `*mcp.CallToolResult` mit `IsError: true` und Text zurück und den Code in der Ausgabestruktur (`WriteOutput.Error`) — das SDK setzt `structuredContent` aus ihr und prüft sie gegen das Ausgabeschema, lässt den Text aber stehen, weil `Content` gesetzt ist.
**Beleg:** `~/go/pkg/mod/github.com/modelcontextprotocol/go-sdk@v1.7.0/mcp/server.go:378-430` (`toolForErr`: Fehler → `SetError`; sonst `StructuredContent` aus der Ausgabe, Text nur, wenn `Content` leer ist); rohe Antwort im Test: `{"content":[{"type":"text","text":"Hub keph: Dokument neu.md hat Revision 15, …"}],"structuredContent":{"address":"keph:wissen","error":{"code":"stale_revision","message":"…"},"name":"neu.md"},"isError":true}`.
**Sicherheit:** bestaetigt
**Frage:** Wie meldet ein Werkzeug einen Code neben der Meldung, den die Erweiterung auswertet, ohne die Meldung zu deuten?
**Warum es so ist:** Die MCP-Spezifikation trennt Protokollfehler (JSON-RPC) von Fehlern des Werkzeugs (`isError`); das SDK bildet einen Go-`error` auf den zweiten ab, kennt aber keinen Code.
**Sackgassen:** Ein `*jsonrpc.Error` aus dem Handler wäre ein Protokollfehler — der Client sähe keinen Werkzeugfehler, und die Lese-Werkzeuge melden anders. Pflichtfelder im Schema (ohne `omitempty`) prüft das SDK vor dem Handler; fehlen sie, kommt `isError` ohne Code — bewusst hingenommen, das ist ein Fehler des Clients.

<!-- sitzung: task-014-etappe-3 -->
