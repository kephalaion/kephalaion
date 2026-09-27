---
thema: rename und Verzeichnisse als Ganzes
begonnen: 2026-09-27
zuletzt: 2026-09-27
status: geklaert
---

# rename und Verzeichnisse als Ganzes

Was beim Bau von `rename` und `delete` mit `recursive` durch alle Schichten nicht offensichtlich war (Task 014, Etappe 4).

## 2026-09-27 — localHub bettete contract.Hub ein; ein neuer Vorgang lief ohne outcome durch

**Befund:** `localHub` in `cmd/kephalaion/synccmd.go` bettete `contract.Hub` ein und überschrieb nur `Rotate`, `Create`, `Write` und `Delete` mit `outcome`. Nach dem Hinzufügen von `Rename` zu `contract.Hub` baute alles ohne Fehler: Die eingebettete Methode wurde befördert, und ein Datenbankfehler von `rename` über `local` wäre als gewöhnlicher Fehler angekommen — der Node hätte „nicht erreichbar, nichts gespeichert“ gemeldet, obwohl der Hub geschrieben haben kann. Jetzt hält `localHub` den Hub in einem Feld und setzt jede Methode ausdrücklich um (`var _ contract.Hub = localHub{}`); ein neuer Vorgang bricht den Bau, bis er dort steht.
**Beleg:** `go build ./...` lief nach der Erweiterung von `contract.Hub` um `Rename` durch, während `*Client` (httpapi) und die Test-Attrappen mit „missing method Rename“ scheiterten; `TestLocalWriteOutcome` in `cmd/kephalaion/synccmd_test.go` prüft jetzt auch `rename` und `delete` mit `recursive` auf `ErrOutcomeUnknown`.
**Sicherheit:** bestaetigt
**Frage:** Warum meldete der Compiler bei `localHub` nichts, als der Vertrag einen Vorgang bekam?
**Warum es so ist:** Einbetten war bequem, solange nur `Rotate` gehüllt werden musste; mit jedem Schreibvorgang wurde die Liste länger, und die Lücke für den nächsten blieb unsichtbar.
**Sackgassen:** Ein Test je Vorgang allein hätte die Lücke nur gefunden, wenn jemand ihn für den neuen Vorgang auch schreibt — genau das vergisst man in demselben Moment. Ein Reflexionstest über die Methodenmenge wäre möglich, aber das Feld statt der Einbettung macht es zur Bauzeit sichtbar, ohne zusätzlichen Test.

<!-- sitzung: task-014-etappe-4 -->

## 2026-09-27 — rename eines Verzeichnisses kommt zeilenweise ohne Zwischennamen aus

**Befund:** Der eindeutige Teilindex `documents_name ON documents(collection, name) WHERE deleted = 0` wird beim zeilenweisen `UPDATE … SET name` nie verletzt, auch ohne Zwischennamen: Alle neuen Namen liegen unter `new_name/`, alle alten unter `name/`. Gleich sein könnte ein neuer mit einem alten nur, wenn einer der Präfixe im anderen liegt — `new_name` unter `name` lehnt `ident.CheckRename` ab (`invalid`), `name` unter `new_name` belegt das Ziel (Verzeichnis: `name_taken`, Dokument: `path_conflict`), geprüft vor dem ersten `UPDATE`. Das gilt ebenso für PostgreSQL, das einen eindeutigen Index je Zeile prüft und nicht aufschieben lässt.
**Beleg:** `internal/hub/store/write.go` (`RenameDocumentAs`, `renameTargetFree`); `TestRenameDirectoryAs` und `TestRenameDirectoryRejected` in `internal/hub/store/rename_test.go` (SQLite). Für PostgreSQL nur aus der Überlegung, nicht gelaufen.
**Sicherheit:** unbestaetigt

<!-- sitzung: task-014-etappe-4 -->
