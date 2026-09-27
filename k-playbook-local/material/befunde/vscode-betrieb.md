---
thema: VS Code — Erweiterung im Betrieb prüfen
begonnen: 2026-09-27
zuletzt: 2026-09-27
status: offen
---

# VS Code — Erweiterung im Betrieb prüfen

Wie sieht man von außen, welche Version der Erweiterung läuft und was sie am Node tut?

## 2026-09-27 — Reload lädt die installierte .vsix, nicht den Stand in vscode/

**Befund:** „Developer: Reload Window“ startet die Version, die im VS-Code-Server installiert
ist; ein neuer Stand in `vscode/` wirkt erst nach `vsce package` und `code --install-extension`.
Nach Task 014 lief deshalb weiter 0.0.4 (nur lesen, Ordner schreibgeschützt), obwohl
`vscode/package.json` schon 0.0.5 mit Schreiben trug.
**Beleg:** `~/.vscode-server/extensions/extensions.json` → `kascada.kephalaion 0.0.4`;
Verzeichnisse `kascada.kephalaion-0.0.1` bis `-0.0.4`, keins für 0.0.5; Aktivierung im Log
`~/.vscode-server/data/logs/<start>/exthost10/remoteexthost.log`:
`ExtensionService#_doActivateExtension kascada.kephalaion, … activationEvent: 'onFileSystem:keph'`.
**Sicherheit:** bestaetigt

## 2026-09-27 — Wo man nachsieht: Output-Log und Journal des Dienstes

**Befund:** Der Output-Channel „Kephalaion“ liegt als Datei unter
`~/.vscode-server/data/logs/<start>/exthost<n>/output_logging_<zeit>/<n>-Kephalaion.log`
(höchstes `exthost<n>` = jüngster Extension Host). Die Anfragen der Erweiterung stehen im
Journal des Dienstes: `journalctl --user -u kephalaion.service | rg /mcp` — je Anfrage Status,
Dauer und Account, aber nicht das Werkzeug. `serve` schreibt als Dienst nicht mehr nach
`~/.local/state/kephalaion/serve.log` (stderr ist ein Socket des Journals).
**Beleg:** `ls -l /proc/<pid>/fd/2` → `socket:[…]`; `journalctl --user -u kephalaion.service`
→ `node POST /mcp 200 3.007ms account=kamran-desktop`.
**Sicherheit:** bestaetigt

## 2026-09-27 — Last: Anlauf 167 Anfragen je Minute, danach etwa 60

**Befund:** Nach einem Reload (0.0.4, ein eingebundener Ordner, Dokumente offen) kamen in der
ersten Minute 167 Anfragen, danach rund 60 je Minute, alle 200 in 1–3 ms. Erwartet waren im
Ruhezustand etwa 22 (`changes` alle 3 s, `whoami` alle 30 s); der Rest sind vermutlich `stat`
und `readDirectory`, die VS Code selbst auslöst. Welche Werkzeuge es sind, zeigt das Journal
nicht. Bei 1–3 ms je Anfrage unkritisch; ein kurzer Cache für `stat` würde es senken.
**Beleg:** `journalctl --user -u kephalaion.service --since 15:18 | rg /mcp | awk
'{print substr($3,1,5)}' | uniq -c` → `167 15:18`, `60 15:19`.
**Sicherheit:** unbestaetigt (Ursache der Differenz nicht belegt)
