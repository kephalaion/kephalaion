---
thema: JSON und ungültiges UTF-8 im Vertrag über HTTP
begonnen: 2026-09-27
zuletzt: 2026-09-27
status: geklaert
---

# JSON und ungültiges UTF-8 im Vertrag über HTTP

Ob ein Schreibvorgang über `local` und HTTP dasselbe ergibt, wenn Name oder Inhalt kein gültiges UTF-8 sind (Task 014, Etappe 2).

## 2026-09-27 — encoding/json ersetzt ungültiges UTF-8 still durch U+FFFD, in beide Richtungen

**Befund:** `json.Marshal` schreibt ein ungültiges Byte als `�`, `json.Unmarshal` macht aus rohen ungültigen Bytes ebenfalls U+FFFD — ohne Fehler. Ein Inhalt `"\xff"` ist über `local` `invalid` (`store.CheckContent`), kam über HTTP aber als gültiger Text `"�"` an und wurde gespeichert; ein Name mit ungültigem Byte hätte über HTTP ein Dokument unter anderem Namen angelegt.
**Beleg:** `TestWriteCodes/http` in `internal/hub/replication/write_test.go` (Fall „invalid kein UTF-8“: `<nil>` statt `invalid`, Revision 10 → 11). Kleines Programm mit Go 1.27.1: `json.Marshal(map[string]string{"content": "a\xffb"})` → `{"content":"a�b"}`; `json.Unmarshal` des rohen Bodys `{"content":"a\xffb"}` → `"a�b"`.
**Sicherheit:** bestaetigt
**Frage:** Laufen `local` und HTTP bei ungültigem UTF-8 auseinander, und wo lässt sich das abfangen?
**Warum es so ist:** `encoding/json` ist so dokumentiert („invalid UTF-8 … replaced with the Unicode replacement rune“); nach dem Dekodieren ist der Unterschied nicht mehr zu sehen.
**Sackgassen:** Am Hub nach dem Dekodieren prüfen geht nicht — der Text ist dann gültig. Die Collection braucht keine Prüfung: Ein ersetzter Collection-Name gibt es nicht, das Ergebnis ist auf beiden Wegen `not_readable`; ein ersetzter Account-Name oder ein ersetztes Token ergibt auf beiden Wegen `account_unauthenticated`.
**Lösung:** Der Client (`httpapi`) schickt `create`/`write`/`delete` mit ungültigem UTF-8 in Name oder Inhalt nicht ab und meldet `invalid` wie der Hub; der Handler nimmt einen Body, der kein gültiges UTF-8 ist, als „kein gültiges JSON“ (`invalid`). Beschrieben in `docs/vertrag.md`, „HTTP“.

<!-- sitzung: task-014-etappe-2 -->
