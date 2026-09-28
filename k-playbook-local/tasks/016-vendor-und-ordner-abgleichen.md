# Task 016 — vendor/ und Ordner abgleichen

Unter `vendor/<name>/` schreibt nur, wer den Scope `vendor/<name>` hat, und die Kommandozeile
gleicht einen lokalen Ordner mit einem Verzeichnis einer Collection ab (`node dir push|pull`) —
damit k-playbook seine Vorlagen in den Store bringen und aktualisieren kann.

## Intent

Ein lokaler Ordner lässt sich in ein Verzeichnis einer Collection übertragen, das dessen alten
Inhalt ersetzt, und ebenso zurückholen; die mitgelieferten Vorlagen unter `vendor/<name>/`
schreibt nur ein Account mit dem passenden Scope.
- Unter `vendor/<name>/` zählt allein der Scope `vendor/<name>` — ohne `write`, unabhängig vom
  Urheber; ein Account mit nur diesem Scope kann sonst nichts schreiben.
- `push` und `pull` vergleichen den Inhalt, nicht das Datum, und bestehen aus den vorhandenen
  Einzelvorgängen — kein neuer Vorgang im Vertrag, keine Größengrenze für den Ordner.
- Abbrechen und Höchstzeit sind jederzeit möglich; ein zweiter Lauf ändert nur, was noch
  abweicht, und nach einem abgeschlossenen Lauf nichts.
- Die Dateien liest und schreibt die CLI dort, wo sie aufgerufen wird — nie der Node.
- `push` schreibt vorerst nur unter `vendor/`.

## Referenzen

- `docs/konzept.md` — „vendor/ — Vorlagen mit eigenem Scope“ und „Einen Ordner abgleichen:
  push und pull“ (unter „Collections, Accounts, Rechte“), Tabelle „Rechte“, „Datenmodell“
  (keine leeren Ordner), „Allgemein — schreiben“ (Werkzeuge, Fehlercodes).
- `docs/begriffe.md` — `vendor/`, `dir push`/`dir pull`, Scope `vendor/<name>` (als geplant
  markiert), `write`, `supersede`, `base_revision`, Fehlercodes.
- `docs/vertrag.md` — „Account-Zeilen“ (Form von `rights`), Fassung.
- `k-playbook-local/k-playbook.md` — Regeln (Hub und Node getrennt, Token nie als Argument,
  Export und Import prüfen wie die CLI, Account-Zeile zuerst sperren).
- `k-playbook-local/material/befunde/mcp-schreiben-betrieb.md`,
  `mcp-durchlauf-curl.md` — wie die Werkzeuge sich im Betrieb verhalten und von Hand
  aufgerufen werden.
- Code:
  - `internal/contract/account.go` — `Rights` (`Write`, `Supersede`), Inhalt der Account-Zeilen;
  - `internal/hub/store/write.go` — Rechteprüfung der Schreibvorgänge (`ErrForbidden`,
    Gründe „write fehlt“, „gehört …, supersede fehlt“), `rename` und `delete` von Verzeichnissen
    je Dokument;
  - `internal/hub/store/accounts.go`, `cmd/kephalaion/accountcmd.go` bzw. `hubcmd.go` —
    `hub account grant|show|list`;
  - `cmd/kephalaion/exportfile.go` — `exportFormat = 5`;
  - `internal/node/mcpnode/read.go` (`writable` aus `a.rights[…].Write`), `whoami.go`;
  - `cmd/kephalaion/mcpwrite_test.go` — MCP-Client des go-sdk gegen `serve`, Vorbild für
    Tests und für den Client der CLI;
  - `vscode/extension.js` — liest Adresse (`listen`) und Token-Dateien wie die CLI es soll.

## Ziel

1. Am Hub: Scope `vendor/<name>` je Account und Collection; Prüfung jedes Schreibvorgangs nach
   der Regel unten; `grant`, `show`, Export und Import; am Node `writable` nach derselben Regel.
2. In der CLI: `kephalaion node dir push` und `node dir pull` als Client des Nodes über MCP.

## Kontext

- **Voraussetzung:** Tasks 014 und 015 abgeschlossen (Schreibwerkzeuge, `list`/`read`).
- **Die Regel für jeden Schreibvorgang** (`create`, `write`, `delete`, `rename`) über einen
  Node, je betroffenem Dokument — bei `rename` mit altem **und** neuem Namen, bei Verzeichnissen
  für jedes Dokument darunter:
  - Name unter `vendor/<name>/` (erstes Segment `vendor`, zweites `<name>`, darunter weitere):
    erlaubt genau mit dem Scope `vendor/<name>` in dieser Collection. `write` ist dort weder
    nötig noch genügt es; `created_by` spielt keine Rolle (kein `supersede`).
  - Name direkt in `vendor/` (`vendor/x.md`) oder genau `vendor`: niemand.
  - Sonst wie bisher: `write` für Neues und Eigenes, `supersede` für Fremdes.
  - `SYSTEM:` bleibt dem Hub vorbehalten (unverändert).
  - Fehlt der Scope: `forbidden` mit Grund („vendor/k-playbook fehlt“).
  - Nur `vendor` in Kleinschreibung ist besonders. Die CLI am Hub (`hub doc`, `hub import`)
    darf wie überall. Lesen unverändert: wer die Collection lesen darf.
- **Speicherung des Scopes:** in `rights` der Account-Zeile als Liste, etwa
  `{"write":false,"supersede":false,"vendor":["k-playbook"]}`; fehlend = leer. `<name>` folgt
  der Namensregel (`ident.CheckName`). Kommt nur hinzu — Fassung 1 bleibt, `docs/vertrag.md`
  („Account-Zeilen“) und `internal/contract` im selben Commit nachziehen. Ein älterer Node
  kennt `vendor` nicht; ihm bleibt `writable` dort ungenau, der Hub prüft trotzdem.
- **Admin:** `hub account grant <account> <collection> [--write] [--supersede] [--vendor
  <name>]…` setzt die Rechte der Collection vollständig, wie bisher (ohne `--vendor` keiner).
  `hub account show|list` und `whoami` nennen den Scope (`Rights.String`, etwa „read,
  vendor/k-playbook“). Exportformat 6 trägt ihn; Format 5 und älter lesen ohne ihn. Ein
  gesperrter Account merkt ihn mit (`locked_rights`).
- **Am Node:** `writable` in `read` (und wo `list` es trägt) je Name nach der Regel: unter
  `vendor/<name>/` der Scope, direkt in `vendor/` falsch, sonst `write` wie bisher. Die
  Erweiterung für VS Code zeigt `vendor/` damit ohne eigene Änderung richtig an.
- **CLI als Client des Nodes:**
  - Adresse aus `listen` im Abschnitt `node` der gefundenen config (`config.Locate`), sonst
    `--node <url>` (nötig etwa im Devcontainer, sobald er den Node erreicht);
  - Account: `--account <name>`; ohne Angabe der einzige unter
    `~/.config/kephalaion/tokens/<hub>/`, bei mehreren ein Fehler mit ihren Namen;
  - Token: aus `tokens/<hub>/<account>.token` (Dateien auf `.pending` übergehen), oder
    `--token-file`, oder `--token-stdin` — nie als Argument, nie in einer Ausgabe;
  - MCP über den Client des go-sdk mit den Headern `X-Keph-Account-<hub>`,
    `X-Keph-Token-<hub>`; Werkzeuge `list`, `read`, `create`, `write`, `delete`.
- **`node dir push <hub>:<collection> <verzeichnis> <lokaler-ordner>`** — ersetzt den Inhalt
  von `<verzeichnis>` durch den des lokalen Ordners:
  - **Vorab**, bevor irgendetwas geschrieben wird: den lokalen Ordner einlesen; nur reguläre
    Dateien, Symlinks übergangen (gemeldet), `.git` immer ausgelassen, `--exclude <glob>`
    (wiederholbar) auf Datei- und Ordnernamen jeder Ebene. Jede Datei muss UTF-8 ohne NUL
    und höchstens 1 MiB sein, jeder Name `ident.CheckDocName` bestehen — sonst Abbruch mit
    der Liste **aller** Treffer, ohne zu schreiben.
  - **Vorerst nur unter `vendor/`:** `<verzeichnis>` muss `vendor/<name>` oder darunter sein,
    sonst Abbruch mit Hinweis. Schutz vor Versehen, keine Grenze am Hub.
  - **Abgleich je Ebene, rekursiv**, Quelle gegen Ziel (`list` ohne `recursive`, Cursor bis zum
    Ende): (1) im Ziel löschen, was in der Quelle fehlt oder dort die andere Art hat — Dokument
    mit `base_revision`, Ordner mit `recursive`; (2) Dateien anlegen (`create`) oder, wenn der
    Inhalt abweicht, schreiben (`write` mit `base_revision` aus dem `read`) — verglichen wird
    der Inhalt, Byte für Byte, über `read` des Ziels, nicht das Datum und kein Hash;
    (3) in die Ordner der Quelle absteigen. Leere Ordner der Quelle entstehen im Store nicht.
    Namen in fester Reihenfolge (nach Name), damit Läufe gleich aussehen.
  - **`--last <pfad>`** (relativ zum lokalen Ordner): diese Datei zuletzt schreiben — für eine
    Markierung wie `VERSION`, die zeigt, dass ein Lauf vollständig war.
  - **`--dry-run`:** nur lesen und ausgeben, was angelegt (`+`), geändert (`~`) und gelöscht
    (`-`) würde.
  - **Abbrechen:** SIGINT/SIGTERM lassen den laufenden Vorgang zu Ende gehen, dann Schluss.
    **`--timeout <dauer>`** (Go-Dauer): nach Ablauf ebenso zwischen zwei Vorgängen. Beides
    endet mit Bericht und eigenem Exit-Code (unvollständig), „erneut ausführen“.
  - **Fehler eines Vorgangs:** `stale_revision`, `name_taken`, `path_conflict` (jemand schrieb
    dazwischen) und ein unklarer Ausgang werden gemeldet, nicht wiederholt; der Lauf geht
    weiter und endet als unvollständig — der nächste Lauf gleicht an. `forbidden`,
    `not_readable`, `unreachable` brechen ab.
  - **Bericht:** angelegt, geändert, gelöscht, unverändert, übergangen, Dauer.
- **`node dir pull <hub>:<collection> <verzeichnis> <lokaler-ordner>`** — derselbe Abgleich in
  die andere Richtung, beliebiges `<verzeichnis>`:
  - Dateien schreiben, deren Inhalt abweicht oder die fehlen; Ordner anlegen; lokal gelöscht
    wird nur mit **`--delete`** (was im Store fehlt; auch Ordner).
  - **Nie außerhalb des Zielordners schreiben** und keinem Symlink im Zielordner folgen — ein
    Symlink an einer Stelle, die geschrieben werden müsste, ist ein Fehler dieser Datei.
  - Einheitlicher Stand: Weicht die Revision eines Dokuments beim `read` von der aus `list`
    ab, hat sich während des Laufs etwas geändert — dann von vorn, höchstens dreimal, sonst
    Abbruch mit Hinweis.
  - `--dry-run`, `--timeout`, Abbrechen und Bericht wie bei `push`. Neue Dateien `0644`, neue
    Ordner `0755` (umask gilt).
- **Aufbau im Code:** den Abgleich in einem eigenen, neutralen Paket gegen eine kleine
  Schnittstelle (Ziel lesen, anlegen, schreiben, löschen) — getestet mit einer Fälschung; die
  Umsetzung über MCP und die Kommandos in `cmd/kephalaion` (etwa `dircmd.go`). Weder Hub noch
  Node werden importiert (`internal/separation_test.go`).
- **Exit-Codes** in der Hilfe nennen: 0 fertig, 1 Fehler, 2 falscher Aufruf, 3 unvollständig
  (abgebrochen, Höchstzeit, gemeldete Konflikte).
- **Testdaten im echten Store:** nur unter `test/` in `home:eins` (Regel des Nutzers).
  `vendor/` liegt außerhalb davon — der Durchlauf mit `vendor/` läuft deshalb in einem eigenen,
  vorübergehenden Aufbau (eigene config, eigenes `serve` auf freien Ports); im echten Store
  `vendor/` nur nach Rückfrage.
- **Nicht in diesem Task:** `push` außerhalb von `vendor/`; das Überlagern und das Briefing
  (Sache von k-playbook); ein Update-Zeitplan; ein neuer Vorgang im Vertrag
  (`replace_directory` ist verworfen); die Erweiterung (keine Änderung nötig).

## Zu bauen

### Etappe 1 — Scope vendor/<name> am Hub

- `Rights` um `vendor`, Account-Zeilen, `grant --vendor`, `show`/`list`, `whoami`-Text,
  Exportformat 6 (Import von 5 und älter ohne Scope), gemerkte Rechte beim Sperren;
  Rechteprüfung nach der Regel; `docs/vertrag.md` und `internal/contract` im selben Commit.
- Tests: Scope ohne `write` schreibt unter `vendor/<name>/`, sonst nirgends; `write` und
  `supersede` ohne Scope nicht unter `vendor/`; direkt in `vendor/` niemand; Dokument von
  admin unter `vendor/<name>/` mit Scope änderbar; `rename` hinein, heraus, innerhalb, auch
  eines Verzeichnisses (je Dokument, alles oder nichts); `delete` mit `recursive` über
  `vendor/<name>/`; falscher `<name>`; Export/Import 6 und 5; gesperrt und entsperrt.

### Etappe 2 — writable am Node

- `writable` nach der Regel in `read` (und `list`, falls dort vorhanden); Beschreibungen der
  Werkzeuge nur, wo nötig, kurz.
- Tests mit dem MCP-Client des go-sdk: Account mit nur Scope, mit nur `write`, mit beidem;
  `read` in `vendor/<name>/`, direkt in `vendor/`, außerhalb.

### Etappe 3 — Abgleich als Paket

- Algorithmus gegen die Schnittstelle, mit Fälschung getestet: anlegen, ändern, unverändert,
  löschen, Art gewechselt (Datei ↔ Ordner), tiefe Ordner, leere Ordner der Quelle,
  `--last`, `--dry-run`, Abbruch zwischen zwei Vorgängen, Höchstzeit, `stale_revision`
  mitten im Lauf (weiter, unvollständig), zweiter Lauf ohne Änderung, Vorabprüfung (Binärdatei,
  NUL, zu groß, ungültiger Name — alle Treffer, nichts geschrieben), Symlink, `.git`,
  `--exclude`; `pull` mit und ohne `--delete`, Symlink im Zielordner, geänderter Stand während
  des Lesens.

### Etappe 4 — CLI node dir push und pull

- Kommandos, Hilfe, Adresse, Account, Token, Signale, Exit-Codes; Sperre auf `vendor/` bei
  `push`; Client über MCP.
- Tests über `serve` (wie `mcpwrite_test.go`): Account mit nur Scope `vendor/x` legt
  `vendor/x/` an, zweiter Lauf ändert nichts, Änderung, Löschung und Artwechsel kommen an;
  `pull` holt es in einen leeren Ordner und gleicht einen veränderten an; `push` außerhalb von
  `vendor/` abgelehnt; kein Token in Ausgabe und Log; SIGINT mitten im Lauf, dann Fortsetzen.

### Etappe 5 — Durchlauf und Doku

- Durchlauf in einem vorübergehenden Aufbau mit Hub und Node: Account `k-playbook` nur mit
  `vendor/k-playbook`; `push` der echten Inhalte von k-playbook (`rules`, `reviews`,
  `commands`, `skills`, `docs`, `guidelines`; nicht `installer`) nach `vendor/k-playbook/` mit
  `--last VERSION`; zweiter Lauf ohne Änderung; eine Datei lokal ändern, eine löschen, erneut;
  einen Lauf abbrechen und fortsetzen; `pull` in einen leeren Ordner und Vergleich mit
  `diff -r`; ein Account mit nur `write` kann unter `vendor/` nichts ändern.
- Doku auf dem Stand nach Task 015 nachziehen: `README.md` (Scope, `node dir push|pull`),
  `docs/begriffe.md` (Markierung „geplant“ entfernen), `docs/konzept.md` („Stand“, „vendor/“
  und „Einen Ordner abgleichen“: gebaut), `docs/vertrag.md` (Account-Zeilen),
  `k-playbook-local/k-playbook.md` (neues Paket, Regel für `vendor/`, Exportformat 6),
  `docs/fortschritt.md`.
