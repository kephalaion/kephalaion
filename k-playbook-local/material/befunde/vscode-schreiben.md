---
thema: VS Code — Erweiterung schreibt über MCP (0.0.5)
begonnen: 2026-09-27
zuletzt: 2026-09-27
status: offen
---

# VS Code — Erweiterung schreibt über MCP (0.0.5)

Was beim Bau und Prüfen des Schreibens im `FileSystemProvider` (Task 014, Etappe 5) nicht
offensichtlich war.

## 2026-09-27 — TextDecoder schneidet ein BOM still ab

**Befund:** `new TextDecoder('utf-8', { fatal: true })` entfernt ein führendes BOM aus dem
Ergebnis; erst `ignoreBOM: true` lässt es stehen. Ohne das verlöre `writeFile` das BOM einer
Datei beim Speichern, ohne Meldung.
**Beleg:** `node -e` mit den Bytes `ef bb bf 61`: Standard → `"a"`, `ignoreBOM: true` →
`"﻿a"`; Prüflauf gegen den echten Node: `bom.md` geschrieben und gelesen → `efbbbf61`.
`vscode/extension.js`, `decodeContent`.
**Sicherheit:** bestaetigt
**Frage:** Wie prüft man „streng UTF-8“, ohne den Inhalt zu verändern?
**Sackgassen:** `fatal: true` allein genügt nicht — es prüft, verändert aber.

<!-- sitzung: 9ac419cb-4700-4181-beb1-efc582ec6efd -->

## 2026-09-27 — read-Fehler tragen keinen Code

**Befund:** Nur die Werkzeuge, die schreiben, liefern `structuredContent.error.code`; `read`
(auch `list`, `changes`) antwortet bei einem Fehler nur mit `isError` und Text. Scheitert das
`read` vor einem Schreibvorgang (etwa Collection nicht lesbar), gibt die Erweiterung einen
`FileSystemError` mit der Meldung des Nodes weiter, nicht `NoPermissions` — sie entscheidet nie
nach der Meldung.
**Beleg:** Prüfskript im Scratchpad: `read` auf `home:zwei` → `toolError true`, `toolCode
undefined`; `write` auf `home:zwei` → `toolCode not_readable`; `writeFile
keph://home/zwei/x.md` → Code `Unknown` mit „nicht lesbar“. `internal/node/mcpnode/read.go`
(`toolFailure` als Fehler des Handlers, das go-sdk macht daraus nur Text).
**Sicherheit:** bestaetigt

<!-- sitzung: 9ac419cb-4700-4181-beb1-efc582ec6efd -->

## 2026-09-27 — Konflikt: stale_revision nur, solange die Replica zurückliegt

**Befund:** `writeFile` schickt die Revision aus dem `read` unmittelbar davor. Nach `hub doc
put` lehnt der Hub deshalb nur ab, solange die Replica den neuen Stand noch nicht hat (bis zum
nächsten Abgleich, Standard alle 30 s; jeder eigene Schreibvorgang stößt einen an). Ist sie
nachgezogen, sieht der Provider keinen Konflikt mehr — dann fängt ihn VS Code selbst über
`mtime` („Datei ist neuer“), bevor es `writeFile` ruft.
**Beleg:** Prüflauf gegen den echten Node: nach `hub doc put` Replica noch alt → `writeFile`
abgelehnt („hat Revision 13, der Vorgang beruht auf 12“), Hub unverändert; nach `node sync`
`stat.mtime` neuer (1790503447543 → 1790503449082).
**Sicherheit:** bestaetigt
**Frage:** Wie prüft man den Konflikt mit dem Ersatz für `vscode`, der keine `mtime`-Prüfung
kennt?
**Sackgassen:** Warten vor dem zweiten Speichern lässt den Abgleich die Replica nachziehen —
dann läuft das Speichern durch (im Ersatz richtig, weil dort VS Code fehlt). Der Test muss
direkt nach `hub doc put` speichern und vorher prüfen, dass die Replica noch alt ist.

<!-- sitzung: 9ac419cb-4700-4181-beb1-efc582ec6efd -->

## 2026-09-27 — Ersatz für vscode: zwei Clients in einem Prozess

**Befund:** Für zwei Instanzen der Erweiterung (zweiter Client für `changes`) muss
`extension.js` zweimal als eigenes Modul geladen werden, je mit eigenem `require`, das den
eigenen Ersatz liefert (`vm.runInThisContext(Module.wrap(src))`). `Module._load` zu
überschreiben (wie bis 0.0.4) ist global und gibt beiden denselben Ersatz. Zwei
Stolpersteine beim Prüfen: Ereignisse für die Collection tragen `keph://home/eins` ohne
Schrägstrich (ein `Uri.from` mit Pfad `/eins/` passt nicht); und der erste `changes`-Aufruf
eines Clients nach einer Pause meldet auch dessen eigene frühere Löschungen — eine Prüfung
„kein Deleted“ muss den Namen nennen.
**Beleg:** Prüfskripte im Scratchpad dieser Sitzung (`vs.js`, `real.js`); zwei zunächst
gescheiterte Prüfungen, beide Fehler im Test, nicht in der Erweiterung.
**Sicherheit:** bestaetigt

<!-- sitzung: 9ac419cb-4700-4181-beb1-efc582ec6efd -->

## 2026-09-27 — Wie VS Code den Provider beim Schreiben ruft

**Befund:** Der `FileService` von VS Code prüft vor `writeFile` selbst (Existenz, `mtime`/etag,
schreibgeschützt) und ruft den Provider dann mit `create: true, overwrite: true`; fehlende
Elternverzeichnisse legt er vorher Stufe für Stufe mit `createDirectory` an (mkdirp), auch vor
`rename`. „Ersetzen“ beim Verschieben löscht das Ziel vorher selbst (`delete` mit `recursive`)
und ruft dann `rename` mit `overwrite: true` — die Meldung „überschreibt nicht“ der Erweiterung
sieht man deshalb im Explorer kaum. Ein Fehler des Providers wird nie zu „Datei ist neuer“;
`stale_revision` kommt als gewöhnlicher Fehler mit Meldung an.
**Beleg:** Kenntnis von `src/vs/platform/files/common/fileService.ts` (`writeFile`, `mkdirp`,
`doMoveCopy`, `toFileOperationResult`); nicht im echten VS Code nachgesehen.
**Sicherheit:** unbestaetigt — Etappe 6 prüft es mit den Handgriffen im echten VS Code.

<!-- sitzung: 9ac419cb-4700-4181-beb1-efc582ec6efd -->
