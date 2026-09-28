---
thema: Frontmatter lesen mit yaml.v3
begonnen: 2026-09-27
zuletzt: 2026-09-27
status: offen
---

# Frontmatter lesen mit yaml.v3

Wie liest `internal/frontmatter` das YAML eines Frontmatter-Blocks so, dass die Antwort ein
JSON-Objekt wird, ohne Werte zu verfälschen?

## 2026-09-27 — yaml.v3 macht aus einem Datum nach `any` ein `time.Time`

**Befund:** `go.yaml.in/yaml/v3` v3.0.5 decodiert einen unquotierten Skalar wie `2026-09-27`
beim Ziel `interface{}` zu `time.Time`, nicht zu Text; formatiert ergibt das
`2026-09-27T00:00:00Z` — ein Datum würde in der Antwort zur Uhrzeit. Deshalb geht das Paket
über den Knotenbaum (`yaml.Node`) und übernimmt bei Tag `!!timestamp` den geschriebenen Text
(`Node.Value`).
**Beleg:** Testlauf des ersten Entwurfs (Decodieren nach `any`, `time.Time` per
`Format(RFC3339Nano)`): `frontmatter_test.go: erwartet {"d":"2026-09-27",…}: … got
"{\"d\":\"2026-09-27T00:00:00Z\",…}"`. Mit dem Knotenbaum: Fall „Zeitangaben“ und „Zeit mit
Zone“ in `internal/frontmatter/frontmatter_test.go` grün.
**Sicherheit:** bestaetigt
**Frage:** Bleiben Zeitangaben Text, wenn man yaml.v3 direkt nach `any` decodiert?
**Sackgassen:** Der Kommentar „for backward compatibility … set it as a string“ aus älteren
yaml.v3-Fassungen (`decode.go`) ist in v3.0.5 nicht mehr zu finden (`rg 'backward compatibility'`
leer); auf dieses Verhalten kann man sich nicht verlassen. Ein nachträgliches Umformatieren von
`time.Time` verliert die Schreibweise (Datum ohne Uhrzeit, Zone) — nicht brauchbar.

<!-- sitzung: task-015-run-2026-09-27 -->

## 2026-09-27 — Der Knotenbaum erlaubt die Schlüsselprüfung am Tag

**Befund:** Über `yaml.Node` erkennt das Paket einen Schlüssel, der kein Text ist, am Tag
(`ShortTag() != "!!str"`: `1:`, `true:`, `2026-09-27:`, auch `<<` mit `!!merge`), und doppelte
Schlüssel selbst — yaml.v3 meldet doppelte Schlüssel nur beim Decodieren in eine Map, nicht
beim Bau des Knotenbaums. Beim direkten Decodieren nach `any` wäre ein Nicht-Text-Schlüssel nur
indirekt sichtbar (`map[interface{}]interface{}` statt `map[string]interface{}`).
**Beleg:** Fälle „Schlüssel Zahl“, „Schlüssel Wahrheitswert“, „Schlüssel tief“, „doppelter
Schlüssel“, „Schlüssel in Anführungszeichen“ in `internal/frontmatter/frontmatter_test.go`;
`yaml.v3 decode.go:333-334` (`stringMapType`, `generalMapType`).
**Sicherheit:** bestaetigt

<!-- sitzung: task-015-run-2026-09-27 -->
