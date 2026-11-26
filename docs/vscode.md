---
title: Kephalaion — VS Code
description: Erweiterung, die Collections über einen FileSystemProvider in VS Code zeigt — als Baumansicht „Kephalaion“ im Explorer — lesen aus der Replica, schreiben über den Node am Hub; Ablauf, benötigte Werkzeuge, Baumansicht mit Ziehen und Ablegen, Dokument öffnen und Suchen, Herausnehmen von `keph://`-Workspace-Ordnern, Status, Sprachen, Installation ohne Marketplace (`kephalaion vscode install`), eingebettet ins Binary mit gleicher Version, Ergebnis des Versuchs und Fundstellen.
---

# Kephalaion in VS Code

**Stand: Lesen und Schreiben gebaut** (2026-09-27, Version 0.0.5, siehe „Umsetzung: Lesen“ und
„Umsetzung: Schreiben“ unten; seit 0.0.6 auch der Node als MCP-Server für Copilot, siehe
„MCP-Server für Copilot“): Statusleiste aus `whoami`, Collections (damals als Workspace-Ordner) mit Inhalt über
`list` und `read`, Änderungen über `changes`; speichern, neue Datei, neuer Ordner, löschen,
umbenennen und verschieben über `create`, `write`, `delete` und `rename` (Task 014). Was die
Erweiterung braucht, gibt es am Node: `whoami` in der Form, die die Statusleiste braucht —
Version, alle Hubs mit `login`, Node-Name und Stand des Abgleichs (Task 008) —, dazu `list`,
`read` und `changes` (Task 009, Festlegungen in [`konzept.md`](konzept.md), „Allgemein —
lesen“) und die Werkzeuge, die schreiben (Task 014, „Allgemein — schreiben“); `serve` gleicht im
Hintergrund ab. Das Schreiben ist mit einem Ersatz für `vscode` gegen den echten Node geprüft,
im echten VS Code noch nicht — die Handgriffe dafür stehen unter „Umsetzung: Schreiben“.
Seit Task 043 (2026-11-25) zeigt die Erweiterung die Collections als Baum im Explorer (siehe
„Baumansicht“), dazu „Kephalaion: Dokument öffnen“ und „Kephalaion: Suchen“ (siehe „Suche:
eigene Befehle statt der Suche von VS Code“); der Nutzer hat den Baum am 2026-11-25 im echten
VS Code geprüft. Seit Task 044 (2026-11-26) ist der Baum der einzige Weg: Die Erweiterung hängt
keine Collection mehr als Workspace-Ordner ein und nimmt einen `keph://`-Ordner neben dem
Projekt selbst heraus.
Begriffe nach [`begriffe.md`](begriffe.md), Hintergrund in [`konzept.md`](konzept.md).

## Wozu

Eine Collection erscheint in VS Code neben dem Projekt im Baum „Kephalaion“ im Explorer:
blättern, Dokumente lesen, in der Markdown-Vorschau ansehen, bearbeiten und speichern. Vor
allem lassen sich **Daten leicht übergeben** — Dateien per Drag & Drop aus dem Projekt in eine
Collection ziehen, und mit „In den Projektordner kopieren…“ zurück (siehe „Baumansicht“).

Die Erweiterung ist eine **Oberfläche für Menschen**. KI-Agenten brauchen sie nicht; sie
lesen die echte Platte und sprechen mit dem Node über MCP.

Damit können auch Dokumente wie Tasks in den Store, ohne aus VS Code zu verschwinden (siehe
[`konzept.md`](konzept.md), „Für k-playbook — Kandidaten“).

## Wie es läuft

VS Code kennt dafür die Schnittstelle `FileSystemProvider`. Eine Erweiterung meldet ein
eigenes URI-Schema an, etwa `keph`, und beantwortet für dessen URIs die Vorgänge eines
Dateisystems. Der Baum „Kephalaion“, Editor, Vorschau und Speichern arbeiten danach mit
`keph://…` wie mit `file://…`.

```ts
vscode.workspace.registerFileSystemProvider('keph', provider, {
  isCaseSensitive: true,
  isReadonly: false,
});
```

**URI:** `keph://<hub-alias>/<collection>/<name>`, etwa
`keph://team/entscheidungen/2026/009-transport.md`. Der Alias ist der Hub-Eintrag des Nodes.

```
keph://<hub-alias>/                  ← lesbare Collections (whoami bzw. list auf die Wurzel)
keph://<hub-alias>/<collection>/     ← list
keph://<hub-alias>/<collection>/…    ← list / read
```

Welche Collections es gibt, fragt die Erweiterung ab; sie stehen nirgends in VS Code.

**Welche Collections der Baum zeigt,** sagt die Einstellung `kephalaion.collections`; für ein
Projekt gehört sie in die `settings` seiner Workspace-Datei. Ungesetzt zeigt der Baum alle
lesbaren Collections.

```json
// projekt.code-workspace
{ "folders": [ { "path": "." } ],
  "settings": { "kephalaion.collections": ["team:wissen"] } }
```

Ein `keph://`-Ordner unter `folders` gehört nicht dazu: Neben einem anderen Ordner nimmt die
Erweiterung ihn heraus (siehe „Baumansicht“). Ein Workspace nur aus `keph://`-Ordnern ist ein
geduldeter Grenzfall, kein empfohlener Weg: von Hand angelegt, die Erweiterung lässt ihn stehen,
und der Provider funktioniert darin wie bisher.

### Terminal im Projektordner (bis Task 044)

Bis Task 044 (2026-11-26) ergänzte die Erweiterung in einem Workspace mit `keph://`-Ordner und
genau einem echten Projektordner die Workspace-Einstellung `terminal.integrated.cwd` als
`${workspaceFolder:<Name>}`, und der Befehl „Kephalaion: Projektterminal öffnen“ öffnete ein
Terminal direkt im Projektordner. Beides gab es nur, weil „Terminal: Neues Terminal erstellen“
bei mehr als einem Workspace-Ordner nach dem Ordner fragt; die cwd-Vorgabe allein verhinderte
das nicht, denn VS Code bildet je Workspace-Ordner eine cwd-URI mit Schema und Authority (belegt
an [`terminalActions.ts` (VS Code 1.101.0)](https://github.com/microsoft/vscode/blob/1.101.0/src/vs/workbench/contrib/terminal/browser/terminalActions.ts),
Funktionen `pickTerminalCwd`, `resolveWorkspaceFolderCwd`, `shrinkWorkspaceFolderCwdPairs`).
Ohne `keph://`-Ordner neben dem Projekt gibt es diese Frage nicht; beides fiel deshalb mit dem
Einhängen weg. **Stehen bleibt** eine früher geschriebene Workspace-Einstellung
`terminal.integrated.cwd`: Die Erweiterung weiß nicht, ob der Nutzer sie inzwischen selbst
gesetzt hat, und der Wert zeigt weiter auf den Projektordner. Eine Tastenbelegung auf
`kephalaion.openProjectTerminal` läuft ins Leere.

### Die Vorgänge und ihre Entsprechung

Den Provider rufen der Baum „Kephalaion“ (über `vscode.workspace.fs`), der Editor, die
Markdown-Vorschau und das Ziehen und Ablegen, alle über die `keph://`-URI eines Dokuments; einen
Ordner im Explorer braucht es dafür nicht.

| `FileSystemProvider` | Kephalaion (MCP-Werkzeug des Nodes) |
|---|---|
| `stat(uri)` | `read` mit `content: false`: Dokument, Verzeichnis oder nichts; `updated` als `mtime`, Größe als `size`; ohne `writable` (`write` der Collection; unter `vendor/<name>/` der Scope `vendor/<name>`, seit Task 016 je Name; unter einem Verzeichnis-Scope immer, seit Task 021) `FilePermission.Readonly` |
| `readDirectory(uri)` | `list` mit `path`, ohne Unterverzeichnisse, Verzeichnisse als eigene Einträge, mit Cursor bis zum Ende |
| `readFile(uri)` | `read` — aus der Replica, lokal und schnell, auch offline; der Inhalt aus dem Feld `content`, seit 0.0.8 (Task 024) |
| `writeFile(uri, …)` | gibt es das Dokument noch nicht (`read` mit `content: false`), `create` (ohne `options.create`: `FileNotFound`); sonst `write` mit der Revision aus diesem `read` (ohne `options.overwrite`: `FileExists`); ein Verzeichnis ist `FileIsADirectory`. Was die Replica schon kennt, fängt VS Code über `mtime` selbst ab („Datei ist neuer“); was noch nicht abgeglichen ist, lehnt der Hub an der Revision ab |
| `rename(alt, neu)` | `rename`, `id` bleibt; ein Verzeichnis als Ganzes, ohne `base_revision`. Nur innerhalb einer Collection, sonst eine Meldung; ein belegtes Ziel `FileExists`, mit `overwrite` eine Meldung — überschrieben wird nie |
| `delete(uri)` | `delete` (Löschmarke) — Eigenes mit `write`, Fremdes nur mit `supersede`; ein Verzeichnis als Ganzes, `recursive` aus den Optionen; ohne `base_revision` |
| `createDirectory(uri)` | nichts am Hub — Verzeichnisse sind nur Präfixe von Namen; die Erweiterung merkt sich das leere Verzeichnis in diesem Fenster, bis darin etwas angelegt oder es gelöscht wird |
| `watch` / Event `onDidChangeFile` | `changes` mit dem Cursor der letzten Antwort, abgefragt alle 3 s — aus der Replica, ohne Netz. Ein Umbenennen erkennt die Erweiterung an der `id` — seit 0.0.5 über eine Tabelle `id` → Name je Collection (alter Name gelöscht, neuer angelegt); `changes` nennt keinen alten Namen. Bei `reset` liest sie den Hub neu mit `list`, bei `dropped` entfernt sie die Collection; beide verwerfen die Tabelle. Nach eigenen Schreibvorgängen feuert sie die Ereignisse selbst |

- **Konflikte:** VS Code vergleicht `mtime` beim Speichern selbst und fragt nach, wenn die
  Datei inzwischen neuer ist. Zusätzlich lehnt der Hub ab, wenn die mitgeschickte Revision
  veraltet ist (siehe [`konzept.md`](konzept.md), „Zwei Arten von Eingaben“). Die
  Erweiterung meldet das als gewöhnlichen Fehler mit der Meldung des Nodes (`stale_revision`)
  — ein Fehler des Providers löst in VS Code nie „Datei ist neuer“ aus; nichts wird still
  überschrieben.
- **Offline wird gelesen, nicht geschrieben.** Ist der Hub nicht erreichbar, werfen
  `writeFile`, `delete` und `rename` `FileSystemError.Unavailable` mit der Meldung des Nodes.
  Lesen läuft weiter.
- **Rechte:** Collections ohne Schreibrecht meldet `stat` als schreibgeschützt
  (`FilePermission.Readonly`); VS Code öffnet sie dann nur zum Lesen. Das Recht gilt je
  Collection (`write`, aus `writable`): Ein fremdes Dokument ohne `supersede` erscheint
  schreibbar und scheitert erst beim Speichern mit `NoPermissions` („gehört admin, supersede
  fehlt“). Bewusste Grenze: Ein Account mit `supersede`, aber ohne `write`, sieht die
  Collection schreibgeschützt, obwohl der Hub ihm das Ändern fremder Dokumente erlaubte.
- **Verbindung und Stand:** `whoami` — Account, Hubs, lesbare Collections und
  Rechte, Stand des Abgleichs, Version.
- **Die Replica bleibt unberührt.** Die Erweiterung schreibt nie in sie; jeder
  Schreibvorgang geht über den Node zum Hub, die Replica zieht über den Abgleich nach — wie bei
  jedem anderen Client.

### Welche Werkzeuge — keine eigens für VS Code

Entschieden am 2026-09-26: Die Erweiterung braucht **`whoami`, `list`, `read` und
`changes`**, zum Schreiben `create`, `write`, `rename`, `delete` (gebaut in Task 014). Alle taugen auch für
die KI; es gibt keine Werkzeuge nur für VS Code, kein eigenes Profil, keine Schnittstelle
neben MCP. Dafür wurden drei Werkzeuge im Konzept erweitert bzw. neu aufgenommen
([`konzept.md`](konzept.md), „Allgemein — lesen“):

- `list` liefert Verzeichnisse der nächsten Ebene als eigene Einträge;
- `read` mit `content: false` ersetzt ein eigenes `stat`;
- `changes` meldet, was sich seit einer Revision oder einem Zeitpunkt geändert hat.

Erwogen und verworfen: Werkzeuge mit dem Vermerk „nur für VS Code“ in der Beschreibung — die
KI lädt die Beschreibung trotzdem mit und ruft sie gelegentlich auf; ein Profil (eigener
Pfad oder Header, der Zusatzwerkzeuge freischaltet); MCP-„Resources“ — ohne Verzeichnisse,
und deren Benachrichtigungen brauchen eine Sitzung.

## Baumansicht (Task 043, 2026-11-25)

Der Abschnitt **„Kephalaion“ im Explorer** (View `kephalaion.collections`, Begriff
**collection tree** in [`begriffe.md`](begriffe.md)) zeigt die Collections als Baum — ein
seit Task 044 der einzige Weg; Workspace-Ordner hängt die Erweiterung nicht mehr ein. Baum und
Editor lesen und schreiben über denselben `FileSystemProvider` (`KephFs`) und dieselben
`keph://`-URIs: Editor,
Dirty-State, Undo, Diff, Konflikt über `mtime` und Revision, Markdown-Vorschau mit relativen
Links und das Aktualisieren über `onDidChangeFile` hängen an der URI, nicht am Ordner. Am Node
kam kein Werkzeug dazu.

**Anlass** (gemessen am 2026-11-25 im OMNI-Setup, dessen `omni.code-workspace` neben dem
Projekt `{ "uri": "keph://vm/omni" }` einbindet): ty (Astral) bricht beim Start ab, sobald ein
Workspace-Ordner kein `file://` ist (`Workspace URI is not a file or directory`); nach fünf
Abstürzen startet VS Code ihn nicht mehr, auch nicht für das lokale Projekt — gemessen mit ty
0.0.110, ty-Erweiterung 2026.114.0, VS Code 1.148, gleich mit `vscode-vfs://`; gemeldet als
https://github.com/astral-sh/ty/issues/5115. Pylance überspringt solche Ordner, „Python
Environments“ hält `keph://vm/omni` für ein Projekt. Dazu fragt „Neues Terminal“ bei mehr als
einem Workspace-Ordner nach dem Ordner (siehe „Terminal im Projektordner (bis Task 044)“).
**Ob das Herausnehmen der `keph://`-Ordner den Absturz von ty behebt, ist offen**, bis der
Handgriff unten es gezeigt hat; ty stürzt schon vor der Aktivierung der Erweiterung ab.

**Inhalt.** Oben die gezeigten Collections als Knoten `<hub>:<collection>` (nicht Hubs mit
Collections darunter; die Adresse nennt den Hub), mit „nur lesen“, wenn `whoami` kein `write`
meldet. Darunter Verzeichnisse (zuerst) und Dokumente aus `readDirectory`. Jedes Element trägt
seine `keph://`-URI als `resourceUri` (Datei-Icons und Dekorationen von VS Code) und als `id`.
Ein Klick öffnet ein Dokument im normalen Editor (`vscode.open`); das aktive `keph://`-Dokument
wird im Baum gezeigt (`reveal`, ohne den Fokus zu nehmen, nur bei sichtbarem Baum).
Aktualisiert wird aus `onDidChangeFile`: angelegt, gelöscht oder umbenannt lädt das Verzeichnis
darüber neu, gebündelt je Runde von `changes`. Nach einem Löschen (auch dem alten Namen eines
Umbenennens) prüft der Baum mit `stat`, ob das Verzeichnis darüber noch besteht — mit dem letzten
Dokument verschwindet es, der Hub kennt keine leeren Verzeichnisse —, und lädt dann das nächste
bestehende darüber neu. Eine Änderung am Inhalt lädt nichts neu; eine Hub-URI, ein
anderer Stand von `whoami` oder eine andere `kephalaion.collections` den ganzen Baum. Ohne
Collection zeigt der Baum, je nach Anmeldung, „Collection im Baum zeigen“ oder den Weg zum
Status (`viewsWelcome`).

**Welche Collections** (`kephalaion.collections`, Scope `window`): ungesetzt — auf keiner
Ebene, über `inspect` — alle lesbaren Collections der genutzten Hubs. Gesetzt, auch leer, genau
diese in dieser Reihenfolge; eine Adresse, die `whoami` nicht als lesbar meldet, erscheint als
Knoten „nicht lesbar“ statt still zu fehlen. „Kephalaion: Collection im Baum zeigen“ (Auswahl
der lesbaren, die nicht gezeigt sind) und „Kephalaion: Collection aus dem Baum nehmen“ (auch im
Kontextmenü des Collection-Knotens) schreiben die Einstellung auf **Workspace-Ebene**, wenn ein
Workspace oder Ordner offen ist, sonst global. Beim ersten Schreiben einer ungesetzten
Einstellung schreiben sie die bisher gezeigten plus bzw. minus die gewählte, damit sich das Bild
nicht sprunghaft ändert. **In einem Fenster mit einem Ordner landet der Wert in dessen
`.vscode/settings.json`, in einem gespeicherten Workspace in der `.code-workspace`-Datei —
eine eingecheckte Datei ändert sich dadurch.** Von sich aus schreibt die Erweiterung
`kephalaion.collections` nur beim Herausnehmen eines `keph://`-Ordners, und nur, wenn die
Einstellung schon gesetzt ist (unten).

**Bearbeiten** über Kontextmenü und Titelleiste: neue Datei (wird danach geöffnet), neuer
Ordner (wie bisher nur in diesem Fenster gemerkt, bis darin etwas liegt), umbenennen (F2; ein
Eingabefeld mit dem alten Namen, auch in ein anderes Verzeichnis), löschen (Entf; modale
Bestätigung, ein Verzeichnis mit allem darunter), aktualisieren, zur Seite öffnen, URI und
relativen Pfad kopieren. Alles über `vscode.workspace.fs` auf `keph://`-URIs, also über
`KephFs`; schreibende Einträge nur bei einer Collection mit `write`. Ein Fehler des Providers
erscheint als Meldung mit dem Text des Nodes. Eine neue Datei mit einem Namen, den es schon
gibt, legt nichts an.

**Ziehen und Ablegen** (`TreeDragAndDropController`):

- **Im Baum:** Elemente innerhalb einer Collection verschieben, wie `rename` (die `id` bleibt).
  Ziel ist das Verzeichnis, auf das abgelegt wird, bei einem Dokument sein Verzeichnis. Zwischen
  Collections kommt nur eine Meldung — kein Verschieben und, anders als Strg+Drag im Explorer,
  auch kein Kopieren.
- **Aus dem Explorer oder vom Betriebssystem** (`text/uri-list` mit `file://` oder
  `vscode-remote://`): Dateien und Ordner werden kopiert, Ordner mit allem darunter, Dokumente
  mit `create`. Ein belegtes Ziel wird nie überschrieben — dann geht für diesen Eintrag nichts.
  Auch ein Name darunter, den ein anderer erst während des Kopierens anlegt, bleibt, wie er ist,
  und zählt als nicht kopiert: Geschrieben wird nicht mit `workspace.fs.writeFile` (das
  überschreibt immer), sondern über `KephFs` ohne `overwrite` (am Hub `create`, belegt ist
  `name_taken`) bzw. ins Projekt mit dem Flag `wx`.
  Eine Datei, die nicht geht (keine Textdatei, zu groß), hält die übrigen nicht auf; die
  Meldung nennt sie, das Log alle.
- **Aus dem Baum** gehen die URIs als `text/uri-list`: in den Editor (öffnet das Dokument) und,
  falls der Explorer es annimmt, in den Projektordner — das ist im echten VS Code zu prüfen.
- **„In den Projektordner kopieren…“** im Kontextmenü ist der Weg vom Baum ins Projekt,
  unabhängig davon: Ziel per Ordnerauswahl, vorgeschlagen der Projektordner; nie überschreiben.

**Herausnehmen der `keph://`-Ordner** (seit Task 044 ohne Einstellung; bis dahin der Schalter
`kephalaion.workspaceFolders`, siehe [`begriffe.md`](begriffe.md)):

- Beim Aktivieren und nach jeder Änderung der Workspace-Ordner nimmt die Erweiterung jeden
  `keph://`-Ordner heraus, wenn der Workspace noch mindestens einen anderen Ordner hat. Vorher
  übernimmt sie deren Collections in `kephalaion.collections`, wenn die Einstellung gesetzt ist
  (ungesetzt zeigt der Baum ohnehin alle). Ein Ordner mit Unterpfad (`keph://vm/omni/docs`)
  wird als seine ganze Collection übernommen; das Log nennt es.
- Sie merkt sich nichts und hängt nichts wieder ein. Den Schlüssel `kephalaion.removedFolders`
  im Speicher des Workspace (`workspaceState`), unter dem sie bis Task 044 die herausgenommenen
  Ordner für das Zurückschalten merkte, leert sie beim Aktivieren einmal.
- Kein Befehl hängt mehr einen Workspace-Ordner ein; „Collection im Baum zeigen“ und „… aus dem
  Baum nehmen“ ändern nur `kephalaion.collections`.

**Spuren des alten Wegs:** Eine noch gesetzte Einstellung `kephalaion.workspaceFolders` zeigt
VS Code ausgegraut als unbekannt; die Erweiterung liest sie nicht mehr, auch `true` hängt nichts
ein. Eine Tastenbelegung auf die früheren Befehle `kephalaion.addCollection` („Collection
einbinden“) oder `kephalaion.openProjectTerminal` läuft ins Leere. Eine früher geschriebene
`terminal.integrated.cwd` bleibt stehen (siehe „Terminal im Projektordner (bis Task 044)“).

**Fallen beim Herausnehmen** (`vscode.workspace.updateWorkspaceFolders`): Ein Aufruf ändert
nur einen zusammenhängenden Bereich, und vor dem nächsten muss das Ereignis
`onDidChangeWorkspaceFolders` des vorigen gekommen sein. Die Erweiterung nimmt deshalb Bereich
für Bereich von hinten nach vorn heraus (`folderRemovalPlan` in `vscode/rules.js`) und wartet
je auf das Ereignis (höchstens 10 s). Aktivierung und Ordnerereignis laufen durch eine
Warteschlange, damit sich nichts überholt. Ändert sich der erste Ordner (ein `keph://`-Ordner an
Index 0), startet VS Code den Extension Host neu; die Erweiterung macht beim Aktivieren dort
weiter — der Plan entsteht jedes Mal neu aus dem Ist-Zustand. In einem gespeicherten Workspace
schreibt VS Code dabei die `.code-workspace`-Datei um (nur die Ordnerliste: die Zeile des
Ordners fällt weg).

**Grenzen:**

- **Ein Workspace nur aus `keph://`-Ordnern bleibt:** Die Erweiterung nimmt dort nichts heraus
  — ein leerer Workspace hilft niemandem — und sagt es im Log und einmal als Hinweis. Ein
  solcher Workspace ist ein geduldeter Grenzfall, kein empfohlener Weg: von Hand angelegt, der
  Provider funktioniert darin wie bisher, und nur dort gelten noch die Handgriffe im Explorer
  aus den datierten Abschnitten unten.
- **Eine `.code-workspace`-Datei mit einer Zeile `keph://…`** (im OMNI-Setup pflegt jeder seine
  `omni.code-workspace` selbst, bei manchen steht noch `{ "uri": "keph://vm/omni" }` darin): Die
  Erweiterung nimmt den Ordner beim ersten Start heraus und schreibt dabei die Ordnerliste der
  Datei um; das ist gewollt. Wer nur `vm:omni` im Baum sehen will, setzt
  `"kephalaion.collections": ["vm:omni"]` in die `settings` der Datei. Ein Skript, das die Zeile
  immer wieder zurückschrieb, gibt es dort seit 2026-10-29 nicht mehr.
- **Strg+P und Strg+Umschalt+F** sehen Collections weiter nicht, auch nicht im Baum; dafür gibt
  es „Kephalaion: Dokument öffnen“ und „Kephalaion: Suchen“ (unten). Die Suche geht nur über
  `docs/`.
- **Kein Kopieren und Einfügen über die Zwischenablage** zwischen Baum und Explorer oder
  innerhalb einer Collection; dafür Ziehen und Ablegen und „In den Projektordner kopieren…“.
- **Kein Kopieren zwischen Collections** (im Explorer eines `keph://`-Ordners ging das mit
  Strg+Drag); im Baum kommt nur eine Meldung.
- **Kein Inline-Umbenennen** in einem eigenen Baum; dafür das Eingabefeld.

**Folgeschritt, erledigt mit Task 044 (2026-11-26):** Task 043 ließ den alten Weg hinter dem
Schalter `kephalaion.workspaceFolders` (Standard `true`) stehen; vorgesehen war, erst den
Standard auf `false` zu setzen und später das Einhängen zu entfernen. Nach der Prüfung des
Baums im echten VS Code am 2026-11-25 ist beides in einem Zug geschehen. Die Anleitung für neue
Mitarbeiter im OMNI-Projekt und eine Vorgabe von `kephalaion.collections` durch dessen
Devcontainer gehören dem OMNI-Setup.

### Baumansicht im echten VS Code noch zu prüfen

**Geprüft vom Nutzer am 2026-11-25** im echten VS Code: der Baum („klappt gut“; der Abschnitt
lässt sich im Explorer verschieben). **Offen, Stand 2026-11-26:** Punkt 8 (Ziehen Baum →
Explorer), das Verhalten von ty und die Punkte 12 und 13 in der Fassung von Task 044, bis der
Nutzer sie meldet. Der Nutzer geht die Handgriffe selbst durch, mit einer Collection mit `write`
und nur unter einem Testverzeichnis; jede Zeile im Output „Kephalaion“ (Menü → Log) zeigt, was
VS Code aufgerufen hat. Ergebnisse hier eintragen.

1. **Blättern:** Explorer → Abschnitt „Kephalaion“: die Collections als `<hub>:<collection>`,
   eine ohne `write` mit „nur lesen“; Verzeichnisse aufklappen.
2. **Öffnen und speichern:** Klick auf ein Dokument öffnet es; ändern, Strg+S; der Baum zeigt
   das aktive Dokument. Konflikt wie unter „Umsetzung: Schreiben“, Punkt 10 und 11.
3. **Markdown-Vorschau** eines Dokuments mit relativem Link auf ein anderes Dokument der
   Collection: Der Link öffnet das Ziel.
4. **Neue Datei, neuer Ordner** über die Titelleiste und das Kontextmenü; die neue Datei öffnet
   sich leer.
5. **Umbenennen** (F2 und Kontextmenü) einer Datei und eines Ordners; **löschen** (Entf) mit
   Bestätigung; „Abbrechen“ lässt alles stehen.
6. **Ziehen Explorer → Baum:** eine Datei und einen Ordner mit Unterordnern aus dem Projekt auf
   ein Verzeichnis im Baum; einmal mit einer Binärdatei darin; einmal auf ein belegtes Ziel.
7. **Ziehen Baum → Editor:** ein Dokument in den Editorbereich ziehen.
8. **Ziehen Baum → Explorer:** ein Dokument auf den Projektordner im Explorer ziehen.
   **Ergebnis notieren** (nimmt der Explorer es an, kopiert er, oder geschieht nichts?).
9. **„In den Projektordner kopieren…“** für ein Dokument und ein Verzeichnis; einmal auf ein
   belegtes Ziel.
10. **Verschieben im Baum** innerhalb einer Collection; zwischen zwei Collections: nur die
    Meldung.
11. **„Kephalaion: Dokument öffnen“** und **„Kephalaion: Suchen“** (Titelleiste, Menü der
    Statusleiste): ein Treffer öffnet das Dokument an der Überschrift des Abschnitts.
12. **Herausnehmen in einem gespeicherten Workspace** mit Projekt und `keph://`-Ordner **an
    Index 1** öffnen: Der Ordner verschwindet, die `.code-workspace`-Datei verliert nur diese
    Zeile, der Baum zeigt die Collection. Danach „Neues Terminal“ (+): **ohne Ordnerauswahl**?
    Und **läuft ty** — sofort, erst nach „Developer: Reload Window“ oder erst nach einem
    Neustart von VS Code? **Ergebnis notieren.**
13. Wie 12, aber mit dem `keph://`-Ordner **an Index 0**: Der Extension Host startet neu, die
    Erweiterung nimmt den Ordner trotzdem heraus (Log).
14. **„Collection im Baum zeigen“ und „… aus dem Baum nehmen“** in einem Fenster mit Ordner:
    Der Wert steht danach in `.vscode/settings.json` (bzw. der `.code-workspace`-Datei).

## Status und Auswahl

- **Statusleiste:** `Keph <hub …>` mit den genutzten Hubs, gelb mit Warnsymbol, wenn etwas
  zu beheben ist. Tooltip: Account je Hub mit der Herkunft der Wahl (Einstellung, Standard,
  einzige Token-Datei; bei „Wahl fehlt“ die Kandidaten, Task 037), letzter Abgleich, Revision,
  Version von Node und Erweiterung — mit Hinweis, wenn sie verschieden sind. Quelle ist `whoami`, dasselbe
  Werkzeug, das die KI benutzt: je Hub `login` (`ok`, `invalid`, `missing`) und `sync`
  (`last_success`, `revision`, `last_error`, `never_synced`). Ist `login` `invalid` und
  `sync.never_synced` gesetzt, liegt es nicht am Token, sondern der Node hat den Hub noch nie
  abgeglichen. `unknown_hubs` nennt Header zu Aliasen, die der Node nicht kennt — ein Hinweis
  auf eine falsch eingerichtete Erweiterung.
- **Wann gelb (Task 030):** Ein Node bedient viele Hubs, ein Hub viele Collections; ein Client
  nutzt davon nur einige. Die Leiste zeigt, ob **das, was dieser Client nutzt**, in Ordnung
  ist — nicht, ob er an jedem Hub des Nodes angemeldet ist. Genutzt (**used hub**,
  [`begriffe.md`](begriffe.md)) ist ein Hub, für den die Erweiterung ein Header-Paar hat —
  lokal jeder mit Token-Datei unter `tokens/<hub>/`, an eine entfernte `nodeUrl` nur die in
  `kephalaion.hubs` gewählten — oder den der Node als `ok` oder `invalid` meldet. Gelb ist nur,
  was der Client beheben muss: `invalid`; `missing` **trotz** Token-Datei (die Datei ist leer
  oder nicht lesbar, das Paar geht nicht hinaus — die Meldung nennt die Datei); ein Fehler im
  Abgleich eines genutzten Hubs (auch „Replica nicht lesbar“, das `whoami` als `missing` mit
  `last_error` meldet); „Wahl fehlt“ (mehrere Accounts, keine Einstellung, kein Standard — mit
  den Kandidaten) und eine Einstellung `kephalaion.accounts`, deren Account keine Token-Datei
  hat; ein **pausiertes Paar** an einer entfernten
  `nodeUrl` (Task 025, „Fehlversuche“ unten: der Grund „pausiert — Anmeldung ungültig (n
  Fehlversuche), nächste Probe in … min“ mit der Schwelle der Jail und der Abhilfe; das Paar geht
  nicht hinaus, der Node meldet den Hub als `missing`, die Regel führt ihn trotzdem als genutzt —
  auch ohne Antwort des Nodes, wenn alle Paare pausiert sind); wie bisher Node nicht erreichbar
  und die verdeckte Antwort (`hidden`) — und kein einziger genutzter Hub. **Nicht gelb** ist ein Hub
  des Nodes, für den der Client kein Token hat: Er ist `missing` ohne Fehlversuch, ein
  normaler Zustand an einem geteilten Node (die VM: ein Node, mehrere Hubs, viele Clients in
  DevContainern). Die Leiste nennt ihn nicht; Tooltip und „Kephalaion: Status anzeigen“ führen
  ihn als „nicht genutzt — kein Token“, auch mit seinem Abgleichfehler. Die Regel ist
  `hubStatus` in [`vscode/rules.js`](../vscode/rules.js) (Tests in
  `vscode/test/rules.test.js`); `render()` und `summary()` nehmen beide sie. Belegt am
  2026-10-08 an einem Wegwerf-Node mit zwei Einträgen auf denselben Hub und Token-Datei nur
  für einen: `whoami` mit dem einen Paar meldet `ok` neben `missing`. Bis dahin färbte jeder Hub
  mit `login` ≠ `ok` die
  Leiste gelb — eine Annahme der ersten Fassung (2026-09-26: ein Node je User mit nur eigenen
  Hubs, `missing` hieß „Token vergessen“).
- **Klick darauf:** ein kleines Menü — Status anzeigen, neu verbinden, Account wählen,
  Collection im Baum zeigen, Collection aus dem Baum nehmen, Dokument öffnen, Suchen (`docs/`),
  Log anzeigen (Output-Channel „Kephalaion“ mit den Aufrufen und Fehlern).
- **Node nicht erreichbar:** Dann scheitert auch das Lesen, weil die Erweiterung die Replica
  nie selbst öffnet. Die Statusleiste zeigt das — seit Task 035 mit der Ursache: „Keph: keine
  config“ (der Tooltip nennt die geprüften Orte), „Keph: config nicht lesbar“ (mit dem
  Fehler), „Keph: Node nicht eingerichtet“ (kein Abschnitt `node:` in der Datei), „Keph: Node
  antwortet nicht“ (nichts abgeschickt, mit Adresse und Grund wie `ECONNREFUSED`); ohne eine
  dieser Ursachen nach der Art des Fehlers „Keph: Antwort unklar“ (abgeschickt, keine brauchbare
  Antwort, 5xx) oder „Keph: Anfrage abgelehnt“ (403, 404, Antwort des Proxys); alles Übrige
  (ungültige `nodeUrl`, Wahl der Hubs) bleibt „Keph: Node nicht erreichbar“. Die Ursache steht
  im Fehler als eigenes Feld `reason` neben `kind`; `pause.js` und die Fehler des Dateisystems
  entscheiden weiter nach `kind` (unten, „Die config wie das Binary“).
- **In VS Code wird gewählt, was angezeigt wird:** „Kephalaion: Collection im Baum zeigen“
  bietet die lesbaren Collections aus `whoami` an, die der Baum noch nicht zeigt, und schreibt
  die gewählte in `kephalaion.collections`; „… aus dem Baum nehmen“ nimmt sie wieder heraus
  (siehe „Baumansicht“).
- **Über die CLI wird eingerichtet:** Hubs, Tokens, gewünschte Collections, Accounts. Das
  ist Verwaltung mit Tokens und heute schon Kommandozeile; die Verwaltung über MCP ist im
  Konzept nur vorgemerkt. Nicht doppelt bauen.
- **Welcher Node:** Es gibt einen je Rechner. Die Erweiterung läuft neben ihm (siehe
  `extensionKind`) und liest die Adresse aus `listen` im Abschnitt `node:` der config, die
  sie wie das Binary sucht (seit Task 035: `KEPHALAION_CONFIG`, die des Users, wenn es sie
  gibt, sonst `/etc/kephalaion/config.yaml`; unten, „Die config wie das Binary“); fehlt
  `listen`, gilt `127.0.0.1:7433`. Eine Einstellung `kephalaion.nodeUrl` braucht es nur zum
  Überschreiben — oder für einen Node auf einem anderen Rechner (unten, „Ein Node auf einem
  anderen Rechner“).
- **Account und Token** trägt die Erweiterung als Header ein, wie jeder Client
  (`X-Keph-Account-<hub>`, `X-Keph-Token-<hub>`). **Sie liest sie aus den Token-Dateien**
  `~/.config/kephalaion/tokens/<hub>/<account>.token` (siehe [`konzept.md`](konzept.md),
  „Orte nach XDG“) — sie läuft als `workspace` auf demselben Rechner und braucht so keine
  eigene Einrichtung. Die Zuordnung ist eindeutig: Das Verzeichnis ist der Alias des Hubs und
  damit der Name im Header, der Dateiname der Account; Dateien auf `.pending` übergeht sie.
  `whoami` bestätigt danach nur, dass die Anmeldung gilt (`login: ok`). **Mehrere Accounts an
  einem Hub (seit Task 037 wie die Kommandozeile):** Die Erweiterung wählt je Hub in derselben
  Reihenfolge wie jeder Client ([`installation.md`](installation.md), „Mehrere Accounts an einem
  Hub“): (1) die Einstellung `kephalaion.accounts` (`{"<hub>": "<account>"}`) — nur Namen,
  kein Token —, gesetzt über „Kephalaion: Account wählen“ (auch im Menü der Statusleiste), Scope
  `machine-overridable`: je Rechner, nicht über Settings Sync, ein Workspace kann sie
  überschreiben; (2) der **Standard-Account** des Hubs aus `tokens/<hub>/default`
  (`kephalaion node account default`); (3) die einzige Token-Datei. Trifft nichts zu, **fehlt
  die Wahl**: Für den Hub geht kein Paar hinaus, die Statusleiste ist gelb, Tooltip und
  „Kephalaion: Status anzeigen“ nennen die Kandidaten und „Account wählen“. Den ersten Account
  nach Namen nimmt die Erweiterung nicht mehr. Nennt `kephalaion.accounts` einen Account ohne
  Token-Datei, gibt es keinen Rückfall: ein Hinweis, der Hub bleibt ohne Account (gelb). Nennt
  der Standard einen Account ohne Token-Datei, steht ein Hinweis im Tooltip, und die nächste
  Regel greift. Tooltip und „Status anzeigen“ nennen je Hub die Herkunft der Wahl
  (`Account bob (Standard), aus alice, bob`); „Account wählen“ schreibt weiter
  `kephalaion.accounts`, als Überschreibung vor dem Standard.
  Der `SecretStorage` von VS Code bleibt nur der Ausweg, wenn keine Datei da ist; nie
  `settings.json` — die landet leicht im Repository oder in Settings Sync.
  Geprüft am 2026-09-26: `whoami` mit dem Token aus `tokens/home/kamran-desktop.token`
  liefert `login: ok`, Account `kamran-desktop`, User `kamran`, Collection `home:eins` mit
  `read` und `write`. Die MCP-Konfigurationen der KI-Clients (`.mcp.json`,
  `.cursor/mcp.json`, `opencode.json`,
  `.vscode/mcp.json`) liest und schreibt die Erweiterung nicht; den Node als MCP-Server für
  Copilot meldet sie über die API für Erweiterungen (unten, „MCP-Server für Copilot“).

## MCP-Server für Copilot (Task 022, 0.0.6)

Die Erweiterung meldet den Node als MCP-Server „Kephalaion“, damit Copilot in VS Code die
Werkzeuge des Nodes sieht — ohne Eintrag in `mcp.json` und ohne Token in einer Datei von
VS Code. `code --add-mcp` wäre der andere Weg, wirkt aber im Remote-Terminal (WSL, SSH) nicht
(`kephalaion node mcp add --assistant vscode` nennt deshalb nur die Erweiterung).

- **API:** `vscode.lm.registerMcpServerDefinitionProvider` mit dem Beitrag
  `mcpServerDefinitionProviders` (Anbieter `kephalaion.node`), stabil seit VS Code 1.101 —
  deshalb `engines.vscode` `^1.101.0`. Die Definition ist ein `McpHttpServerDefinition` mit der
  Adresse wie `nodeUrl` (`listen` bzw. `kephalaion.nodeUrl`, dazu `/mcp`).
- **Das Token erst beim Start.** `provideMcpServerDefinitions` meldet die Definition ohne jeden
  Header; `version` nennt nur Hubs und Accounts (`vm=kamran-wsl`). VS Code speichert gemeldete
  Definitionen samt Headern zwischen (`mcp.extCachedServers` im Speicher des Workspace, bei WSL
  auf der Windows-Seite) — deshalb dort nichts Geheimes. Die Header-Paare setzt
  `resolveMcpServerDefinition` ein, wenn VS Code den Server startet; die aufgelöste Definition
  speichert VS Code nicht, es reicht sie an den Extension Host weiter, der die Verbindung
  aufbaut.
- **Wer verbindet:** der Extension Host, in dem die Erweiterung läuft (`workspace`, unter WSL im
  Linux), mit dem `fetch` von Node.js — wie die Erweiterung selbst. `Host` ist die Adresse aus
  `listen`, ein `Origin` schickt er nicht; die Prüfung des Nodes lässt die Anfrage durch.
- **Accounts wie `node mcp headers`:** je Hub die Einstellung (`kephalaion.accounts`), sonst der
  Standard-Account (`tokens/<hub>/default`), sonst der einzige mit Token-Datei (Task 037). Ein
  Hub, an dem die Wahl fehlt, oder einer, dessen Einstellung einen Account ohne Token-Datei
  nennt, fehlt im MCP-Eintrag — wie in Statusleiste und Dateisystem; das Log „Kephalaion“ nennt
  ihn, ebenso einen Standard ohne Token-Datei.
- **Neu gemeldet** wird, wenn sich Adresse, Wahl, die Einstellung, die Token-Dateien oder der
  Standard ändern — `tokens/<hub>/default` zählt mit Name, Größe und Zeit, ohne Inhalt (geprüft
  alle 30 s). Eine laufende Verbindung behält ihre Header: nach `kephalaion node account
  rotate` den Server neu starten („MCP: Server auflisten“ → Kephalaion → Neu starten) oder das
  Fenster neu laden.
- **Trace:** Protokolliert der Extension Host auf „Trace“ (`vscode.env.logLevel`), schreibt
  VS Code bei jeder Anfrage an einen MCP-Server die Header ins Log des Servers — nur
  `Authorization` wird verdeckt, ein `X-Keph-Token-…` nicht. Solange das gilt, meldet die
  Erweiterung den Server nicht (Warnung, Eintrag im Log „Kephalaion“) und startet ihn nicht;
  geht das Log-Level zurück, meldet sie ihn wieder.
  **Entschieden am 2026-10-01 (Nutzer):** so, mit dieser Sperre. Verworfen: das Restrisiko nur
  dokumentieren (das Token stünde bei Trace in einer Log-Datei des Fensters, unter WSL auf der
  Windows-Seite) und VS Code aus Task 022 nehmen. Ein Weg ohne die Grenze bräuchte eine
  Anmeldung über `Authorization`, die VS Code verdeckt — eine Änderung an der Anmeldung des
  Nodes (Header-Paare je Hub), nicht Teil dieser Task.
- **Abschalten:** Einstellung `kephalaion.mcpServer.enabled` (Vorgabe `true`, Scope
  `machine-overridable`).
- **Starten:** VS Code führt den Server nach dem Laden unter „MCP: List Servers“, startet ihn
  aber nicht von selbst — dort „Start Server“ (oder einen Chat mit Werkzeugen beginnen, wenn
  VS Code Server dabei selbst startet). Beim Start versucht VS Code zuerst `GET /mcp` (der
  zustandslose Node antwortet 405) und arbeitet dann mit `POST`.
- **Abgenommen am 2026-10-01** auf der WSL mit VS Code 1.140.0 und Copilot (Agent-Modus):
  Server gestartet („Starting server from Remote extension host“, 8 Werkzeuge), `whoami` mit
  Account; im Log des Nodes die Anfragen mit Account. Danach stand das Token weder unter
  `~/.vscode-server` noch in den Daten von VS Code unter Windows (`AppData/Roaming/Code`,
  durchsucht mit GNU grep; nicht lesbar nur drei gesperrte `LOCK`-Dateien).

## Ein Node auf einem anderen Rechner (Task 023, 0.0.7)

Die Erweiterung kann statt des Nodes dieses Rechners einen Node hinter einem Reverse-Proxy
ansprechen — gedacht für einen Rechner ohne eigenen Node, etwa den der Dev-VM
([`installation.md`](installation.md), „Node für Clients anderer Rechner“). Für sie gelten
dieselben Regeln wie für `kephalaion node mcp add --node`:

- **`kephalaion.nodeUrl`** ist die Adresse ohne `/mcp`, über den Proxy
  `https://<name>/kephalaion`. `http` geht nur zu diesem Rechner (`localhost`, `127.0.0.1`,
  `[::1]`) und zu `host.docker.internal`, alles andere nur `https` — sonst gingen Tokens im
  Klartext übers Netz; eine solche Adresse lehnt die Erweiterung ab, mit Meldung, und schickt
  nichts. Keine Query, kein User, keiner Weiterleitung folgt sie. Antwortet statt des Nodes der
  Proxy (401, Weiterleitung, HTML), meldet sie „Präfix falsch oder Anmeldung des Proxys“.
- **`kephalaion.hubs`** ist die Wahl der Hubs (Liste von Aliasen, etwa `["vm"]`; Scope
  `machine-overridable`): An eine entfernte Adresse (`https`, `http://host.docker.internal`)
  gehen nur die Header-Paare dieser Hubs — im Dateisystem wie im MCP-Server für Copilot.
  Der Alias ist der des Hub-Eintrags **am entfernten Node**, so heißt auch das Verzeichnis unter
  `tokens/`. Die Wahl ist fest: Ein Hub, der später unter `tokens/` hinzukommt, geht nicht mit.
  Leer gilt nur, wenn unter `tokens/` genau ein Hub liegt; sonst schickt die Erweiterung nichts
  und meldet den MCP-Server nicht (Hinweis im Log „Kephalaion“). Für die lokale Adresse ist die
  Einstellung ohne Belang — dort gehen wie bisher alle Hubs mit Token-Datei.
- **Zertifikat:** Die Erweiterung verbindet mit dem `fetch` von Node.js im Extension Host; ein
  Zertifikat von Let's Encrypt gilt dort ohne Weiteres. Eine eigene CA bräuchte
  `NODE_EXTRA_CA_CERTS` in der Umgebung des Extension Hosts — nicht geprüft.
- **Ohne gültige Anmeldung** antwortet der Node über den Proxy verdeckt (keine Version, keine
  Hubs); die Statusleiste zeigt dann „Keph: nicht angemeldet“ und fragt im Tooltip, ob die
  Token-Dateien stimmen und die Hubs unter `tokens/` (bzw. in `kephalaion.hubs`) wie die
  Hub-Einträge am Node heißen.
- **Fehlversuche (Task 025):** Jede Anfrage mit einem ungültigen Paar ist auf dem Rechner des
  Proxys ein Fehlversuch (fail2ban zählt im Zugriffslog von Caddy; 10 in 10 min sperren die Adresse
  auf 80/443, nach einer früheren Sperre schon 5). Gemessen schickte die Erweiterung rund 2
  Anfragen je Sekunde (381 in 186 s, A10 unten) — mit einem ungültigen Token wäre der Rechner nach
  wenigen Sekunden gesperrt gewesen, samt dem Abgleich eines Nodes dort mit dem Hub hinter
  demselben Proxy. Deshalb gilt an einer entfernten `nodeUrl` (`remote` aus `rules.parseNodeUrl`;
  lokal bleibt alles wie bisher), Modul [`vscode/pause.js`](../vscode/pause.js), Begriffe in
  [`begriffe.md`](begriffe.md) („probe“, „paused pair“, „budget“, „pause file“):
  - **Schranke:** Vor jeder anderen Anfrage an die Adresse geht genau eine **Probe** (`whoami`)
    mit den aktuellen Paaren; `stat`, `readDirectory`, `readFile`, `changes`, die Schreibvorgänge
    und der Start des MCP-Servers warten auf ihr Ergebnis — ein gemeinsames, keine zweite. Neu
    geprobt wird, wenn sich ein Paar ändert (Token-Datei, Wahl des Accounts, `kephalaion.hubs`,
    `nodeUrl`, „Neu verbinden“ — sofort, zählt ins Budget, wartet nicht darauf), nicht vor jeder
    Anfrage. **Probe ohne Ergebnis** (keine Verbindung, 5xx des Proxys, etwa 502 beim Neustart
    des Nodes, Abbruch): Die Schranke bleibt zu — keine Anfrage mit ungeprüften Paaren; `stat`,
    `readDirectory` und `readFile` scheitern als `Unavailable` mit der Ursache, der MCP-Server
    startet nicht, die Leiste zeigt die Ursache (seit Task 035 etwa „Node antwortet nicht“ oder
    „Antwort unklar“, vorher immer „Node nicht erreichbar“) —, und die Adresse wird mit
    wachsendem Abstand neu geprobt: 5, 10, 30 s, dann alle 60 s (von allen Fenstern des Rechners
    zusammen höchstens eine Probe je Minute, der Abstand steht in der Pausendatei). Ein geändertes
    Paar und „Neu verbinden“ proben sofort und beginnen den Abstand von vorn. Eine solche Probe
    ist an der Jail kein Fehlversuch und zählt nicht ins Budget; eine 401 des Proxys dagegen ist
    ein Ergebnis (Fehlversuch, alle geschickten Paare pausiert).
  - **Erkennen:** `whoami` mit `hidden` (alle geschickten Paare ungültig) oder `login` `invalid`
    je Hub; `changes`, das alle eben noch lesbaren Collections eines Hubs als `dropped` meldet
    (ein ungültiges Paar macht alle unlesbar; fällt nur eine weg, ist das ein entzogenes Recht,
    kein Fehlversuch); eine 401 des Proxys (`rules.explainResponse`; sie betrifft die ganze
    Adresse, denn sie zählt in `caddy-auth`). Die Anfrage, die es entdeckt, zählt als Fehlversuch.
    Grenze: Verliert der Account alle Collections eines Hubs (Recht entzogen, gelöscht), sieht das
    aus wie ein ungültiges Paar — bis zur nächsten Probe.
  - **Pause je Paar:** Ein ungültiges Paar geht nicht mehr mit, weder in Anfragen der Erweiterung
    noch beim Start des MCP-Servers (`resolveMcpServerDefinition`); die übrigen Paare arbeiten
    weiter. Ein Aufruf des Dateisystems für einen pausierten Hub scheitert ohne Anfrage als
    `Unavailable` — nicht `FileNotFound`, sonst hielte VS Code die Dateien für gelöscht. Bleibt
    kein Paar übrig, geht an die Adresse nichts außer der nächsten Probe. Ein laufender MCP-Server
    behält seine Header und wird nicht neu gestartet.
  - **Ein Stand für alle Fenster:** Pause und Budget stehen in der **Pausendatei**
    `$XDG_STATE_HOME/kephalaion/vscode-pause.json` (sonst `~/.local/state/kephalaion/`), die alle
    Extension Hosts dieses Rechners lesen und schreiben — unabhängig von Profil, Ausgabe und
    Fenster, nicht in `globalStorageUri`. Je Paar Adresse, Hub, Account, die Kennung der
    Token-Datei (Größe und Zeit, ohne Inhalt), Zahl der Fehlversuche und Zeit der nächsten Probe;
    je Adresse das Budget und der Abstand nach Proben ohne Ergebnis (`unanswered`: Zahl, Zeit
    der letzten und der nächsten). Kein Token, kein Hash; atomar geschrieben (`rename`, `0600`);
    vor jeder Anfrage übernimmt ein Fenster den Stand, billig (nur, wenn sie sich geändert hat);
    proben darf zur selben Zeit nur ein Fenster (Sperrdatei mit `O_EXCL`, nach 30 s verwaist),
    die anderen übernehmen das Ergebnis; ist sie kaputt oder fehlt sie, gelten Schranke und
    Sperrdatei trotzdem (leer gelesen, die nächste Probe schreibt sie neu). **Ersatzort:** Lässt
    sich dort nichts anlegen (Verzeichnis ohne Schreibrecht, etwa von root angelegt; Platte voll;
    schreibgeschützt eingehängt), liegen Pausendatei und Sperrdatei zusammen am Ersatzort
    `os.tmpdir()/kephalaion-<uid>/vscode-pause.json` (Benutzername ohne uid; Verzeichnis `0700`,
    nur, wenn es dem Benutzer gehört; unter Windows ist `%TEMP%` ohnehin je Benutzer), den alle
    Fenster desselben Benutzers teilen. Reihenfolge: Jedes Schreiben und jedes Sperren versucht
    zuerst den ersten Ort, dann den Ersatzort — gelingt der erste wieder, kehrt das Fenster
    zurück, mit dem Stand. Damit alle Fenster denselben Stand sehen — auch eines, das am ersten
    Ort noch schreiben kann —, liest jedes beide Orte und nimmt den jüngeren (`writtenAt` in der
    Datei), und die Sperre wird an beiden Orten geprüft. Je Wechsel einmal im Log „Kephalaion“,
    mit beiden Pfaden und dem Fehler. Erst wenn auch der Ersatzort nicht geht, ist das kein
    Ergebnis einer Probe und kein Fehler für die Anfrage: Das Fenster hält Pause, Budget und
    Abstand im Speicher und arbeitet damit genauso weiter; jedes weitere Schreiben versucht beide
    Orte erneut, und gelingt es, gilt wieder der geteilte Stand (je einmal im Log, mit Pfaden und
    Fehlern, nicht je Anfrage — auch für die Sperrdatei: ohne sie probt das Fenster für sich).
    Grenze: Sind beide Orte unschreibbar, zählt der Stand nur je Fenster. Grenze: ein Stand je
    Benutzer und Betriebssystem, auf dem die Token-Dateien liegen — Fenster unter Windows und in
    der WSL desselben Rechners teilen ihn nicht (dieselbe Adresse nach außen), haben aber ohnehin
    eigene Token-Dateien.
  - **Wann wieder geprobt wird:** sofort einmal, wenn sich die Kennung der Token-Datei des Paars
    ändert (spätestens beim nächsten Lesen, 5 s), ebenso bei anderer Wahl und „Neu verbinden“;
    sonst nach wachsender Pause — 1, 2, 4, 8, 16, 30 min, danach alle 30 min — und nur, wenn das
    **Budget** der Adresse es erlaubt: weniger als 2 Fehlversuche in den letzten 10 min, über alle
    Paare und Fenster (jede Anfrage mit ungültigem Paar, wie der Node sie zählt — Proben wie der
    Übergang gültig → ungültig im Betrieb samt Anfragen, die andere Fenster schon abgeschickt
    hatten). Die übrigen 2 der 4 bleiben für so einen Übergang; nach einer früheren Sperre reichen
    der Jail schon 5. Ohne Zutun des Nutzers so über alle Fenster höchstens 4 Fehlversuche in
    10 min; darüber nur, wenn im Betrieb binnen 10 min mehrere Paare ungültig werden oder beim
    Übergang schon mehr als 2 Anfragen zugleich unterwegs sind (viele Fenster im selben Moment).
    Nach `rotate` klärt die erste Probe nach 1 min den Fall, sobald der entfernte Node abgeglichen
    hat — und das tut er seit Task 025 von selbst, sobald er das neue Token abweist. Jede Probe
    von Hand zählt einen Fehlversuch. Nach einer gültigen Antwort ist die Pause des Paars weg und
    die Datei bereinigt; die Zeiten im Budget bleiben, bis sie 10 min alt sind.
  - **Anzeige:** Die Statusleiste zeigt den pausierten Hub gelb mit dem Grund „pausiert“ aus
    `rules.hubStatus` („Wann gelb“ oben); der Tooltip nennt die nächste Probe, die Zahl der
    Fehlversuche, die Schwelle der Jail und die Abhilfe (Token-Datei erneuern; nach `rotate`
    erledigt es sich nach dem Abgleich des entfernten Nodes). Sind alle Paare pausiert, zeigt sie
    „Node nicht gefragt (alle Paare pausiert, n Fehlversuche in 10 min)“. Im Log „Kephalaion“
    steht nur ein Wechsel des Stands, nicht jede Anfrage.
  - **Tests** (`make vscode-test`): die Zeitfolgen mit ersetzbarer Uhr und zwei Instanzen an
    derselben Datei in `vscode/test/pause.test.js` (Start, Übergang im Betrieb, Paar A pausiert
    und Paar B wird ungültig — je höchstens 4 in 10 min, gezählt an den Anfragen, wie ein Ersatz
    des Nodes sie sieht), das Erkennen in `rules.test.js`, die Erweiterung selbst (eine Anfrage
    beim Start, `Unavailable`, zwei Hubs, lokal unverändert, MCP-Server ohne das pausierte Paar,
    401 und `dropped`, Node beim Start nicht erreichbar) in `extension.test.js`; die Probe ohne
    Ergebnis (Abstand 5–60 s, 502, 401 als Ergebnis, geändertes Paar, mehrere Fenster) in
    `pause.test.js`. Gegen die VM belegt am 2026-10-09 mit zwei
    Instanzen des Moduls und einem Dummy-Token: eine Anfrage, eine Zeile im Journal von Caddy
    (`~/dev/vm/ansible/roles/kephalaion/README.md`, „Stand 2026-10-09“); die Abnahme mit
    mehreren Fenstern steht unter „Zu testen“ in [`fortschritt.md`](fortschritt.md).

**Abgenommen am 2026-10-02** (Task 023, A10, durch den Nutzer) gegen die Dev-VM mit 0.0.7:
Statusleiste gelb (der zweite Hub der VM, `vmhttp`, ohne Anmeldung — erwartet), `whoami` in
Copilot über den Node der VM (Node `vm-node`, Hub `vm` angemeldet), 0-mal `login=invalid`, kein
Token im Log des Proxys und in den Daten von VS Code; danach beide Einstellungen wieder entfernt.
*Protokoll von damals:* `vmhttp` ist am 2026-10-08 von der VM entfernt, und seit Task 030 wäre
das Gelb auch mit `vmhttp` nicht mehr erwartet — ein Hub ohne Token des Clients ist keine
Warnung („Status und Auswahl“, „Wann gelb“).

## `read` mit `content` (Task 024, 0.0.8)

Der Node liefert den Inhalt eines Dokuments seit Task 024 im Feld `content` der Struktur; der
Text des Ergebnisses ist nur noch ihr JSON ([`konzept.md`](konzept.md), „Allgemein — lesen“).
`readFile` nimmt seit 0.0.8 `content`, wenn es ein String ist, sonst den Text — so liest die
Erweiterung auch an einem älteren Node richtig. Geprüft mit `node --test`
(`vscode/test/extension.test.js`): neuer Node, leeres Dokument, älterer Node.

**Eine ältere Erweiterung (bis 0.0.7) an einem neuen Node** zeigt in einer geöffneten Datei
statt des Textes das JSON der Struktur (`{"address":…,"content":"…",…}`). **Nicht
speichern:** Speichern schickte dieses JSON als Inhalt an den Hub und überschriebe das
Dokument. Erst die Erweiterung aktualisieren (`kephalaion vscode install`), dann „Developer: Reload
Window“. Eine eigene Prüfung dagegen gibt es nicht: Binary
und Erweiterung erscheinen zusammen, und der Fehler ist vor dem Speichern sichtbar.

Nach dem Update des Nodes braucht auch Copilot „Developer: Reload Window“: VS Code kennt sonst
das alte Output-Schema von `read` ([`installation.md`](installation.md), „Bei den Assistenten
anmelden“).

**Abgenommen am 2026-10-02** (Task 024, durch den Nutzer) auf der WSL mit 0.0.8 nach
„Developer: Reload Window“: `vendor/test-docs/vscode.md` aus `vm:test` zeigt Markdown samt
Frontmatter, kein JSON. Eine an `wsl-hallo.md` angehängte Zeile kam beim Speichern als Markdown
am Hub an (neue Revision). `read` im Copilot-Chat liefert den Inhalt.

## Die config wie das Binary (Task 035)

Bis Task 035 las die Erweiterung nur `KEPHALAION_CONFIG` oder die config des Users. Auf einem
Rechner mit globaler Installation (die VM, VS Code über Remote-SSH) fand sie so keine config
und zeigte „Node nicht erreichbar“, obwohl der Node lief; die Umgehung war
`kephalaion.nodeUrl`. Seitdem sucht sie wie `config.Locate` im Binary — nachgebaut als reine
Funktion `locateConfig` in [`vscode/rules.js`](../vscode/rules.js), nicht über einen Aufruf
des Binarys, das nicht im `PATH` des Editor-Servers liegen muss:

1. `KEPHALAION_CONFIG`, zu einem absoluten Pfad aufgelöst.
2. Die config des Users (`$XDG_CONFIG_HOME/kephalaion/config.yaml`, die Variable zählt wie
   im Binary nur mit absolutem Wert, sonst `~/.config/kephalaion/config.yaml`), wenn es sie
   gibt. Eine ohne Leserecht (`EACCES`) zählt als nicht vorhanden, wie `userFileExists`: Sie
   gehört einem anderen User.
3. `/etc/kephalaion/config.yaml`, wenn es sie gibt. Hier zählt nur `ENOENT` als „gibt es
   nicht“, wie `fileExists`: Ein Rechteproblem meldet die Erweiterung als „config nicht
   lesbar“, statt es still zu übergehen.
4. Sonst der Ort des Users, an dem die config fehlt — die Meldung „keine config“ nennt beide
   geprüften Orte.

Die Token-Dateien bleiben in jedem Fall unter `tokens/` neben der config des Users
(`assistant.TokensDir`), auch wenn die globale gilt; in der globalen stehen sie nie. Fehlt
`listen` im Abschnitt `node:`, gilt `127.0.0.1:7433` wie `config.DefaultListenNode`; fehlt
der Abschnitt ganz, heißt es „Node nicht eingerichtet in <Datei>“. Gibt es die config des
Users und die globale nebeneinander, gilt wie bei `Locate` die des Users; Tooltip, „Status
anzeigen“ und das Log nennen den Fehler „zwei Arten der Installation“ mit demselben Weg wie
`kephalaion status`. Das Log beim Start nennt Ort und Quelle der config (`pro User`,
`global`, `KEPHALAION_CONFIG`), der Tooltip ebenso.

Die Fehler auf dem Weg zum Node tragen neben `kind` (`unreachable`, `unclear`, `rejected`;
danach entscheiden `pause.js` und die Fehler des Dateisystems, unverändert) ein eigenes Feld
`reason` mit der Ursache: `no_config`, `config_unreadable`, `node_not_set_up` (Fehler der
config, nichts abgeschickt) oder `no_answer` (nichts abgeschickt, auch an einer entfernten
`nodeUrl`). Nicht `Error.cause`: Das trägt in `request` schon den Fehler von `fetch`. Die
Leiste zeigt je Ursache einen eigenen Text („Status und Auswahl“ oben); `closedError` der
Schranke reicht `reason` neben `kind` weiter, die Schranke selbst verhält sich wie bisher.

Der feste Ort der globalen config lässt sich nur für die Tests umlenken (ein exportierter
Setter, wie `SystemPath` im Binary) — keine Einstellung, keine Umgebungsvariable. Tests
(`make vscode-test`): je Fall von `Locate` ein Test für `locateConfig` in `rules.test.js`
(nur User, nur global, beide, keine, `KEPHALAION_CONFIG` absolut und relativ, `EACCES` beim
User und global, relatives `XDG_CONFIG_HOME`), dazu `parseNodeListen`; in `extension.test.js`
die globale config mit den Token-Dateien des Users und gemeldetem MCP-Server, fehlendes
`listen`, die Meldungen für keine config, config nicht lesbar, Node nicht eingerichtet, Node
antwortet nicht, zwei Arten, 5xx und 403, und die Schranke an einer entfernten `nodeUrl` mit
unverändertem `kind`. Gegenstück auf der Kommandozeile: `kephalaion status` fragt seit Task
035 auch bei globaler Installation, ob der Node antwortet und ob die Anmeldung des Users gilt
([`installation.md`](installation.md), „Verwalten“).

## Sprachen

**Eine VS-Code-Erweiterung ist JavaScript bzw. TypeScript.** Sie läuft im Extension Host, einem
Node.js-Prozess (oder als Web-Erweiterung in einem Browser-Worker). Andere Sprachen gehen nur
mittelbar:

- **Dünne Erweiterung in TypeScript, Logik in Go.** Die Erweiterung übersetzt nur zwischen
  VS Code und einem Prozess in einer anderen Sprache — über stdio (JSON-RPC, wie beim Language
  Server Protocol) oder HTTP. So arbeiten gopls, rust-analyzer und die meisten großen
  Erweiterungen.
- **WASM:** VS Code kann WASI-Module laden (`@vscode/wasm-wasi`), Go lässt sich nach WASM
  übersetzen. Für Kephalaion ungeeignet: SQLite, Dateizugriff und Netz sind aus WASM heraus
  mühsam, und die Logik gibt es ohnehin schon im Node.

**Für Kephalaion: ein dünner Übersetzer; die Logik bleibt im Binary.** Gebaut ist er in
reinem JavaScript ohne Abhängigkeiten (siehe „Umsetzung“).

**Weg zum Node: MCP über HTTP.** Der Node ist ohnehin ein MCP-Server über HTTP auf
`127.0.0.1` (siehe [`konzept.md`](konzept.md), „Kommunikation“), und `list`, `read`, `write`,
`rename`, `delete` sind genau die Vorgänge, die der Provider braucht. Die Erweiterung ist damit
ein MCP-Client wie jeder andere; ein eigenes Protokoll entfällt, und das SDK
(`@modelcontextprotocol/sdk`) braucht sie nicht — JSON-RPC über das `fetch` von Node.js genügt.
Voraussetzung ist ein laufender Node (`kephalaion serve`). Account und Token trägt die
Erweiterung als Header ein, wie jeder Client; sie liest sie aus den Token-Dateien (siehe
„Status und Auswahl“), nie aus den Einstellungen.

## WSL und Remote: `extensionKind`

VS Code teilt Erweiterungen in zwei Arten:

- **`ui`** — läuft auf dem Rechner, auf dem das Fenster ist (unter WSL: Windows).
- **`workspace`** — läuft dort, wo der Workspace liegt (unter WSL: im Linux der WSL, im
  VS-Code-Server).

**Die Erweiterung muss `workspace` sein**, in `package.json`:

```json
"extensionKind": ["workspace"]
```

Nur dort erreicht sie den Node auf `127.0.0.1` der WSL und das Binary unter
`~/.local/bin/`. Als `ui` liefe sie unter Windows und sähe weder das eine noch das andere.
Dasselbe gilt für SSH-Remotes: Die Erweiterung läuft neben dem Node. Im Devcontainer läuft
sie im Container; wie sie dort einen Node erreicht, hängt vom Weg ab — noch nicht gebaut
([`konzept.md`](konzept.md), „Devcontainer“). Folge: Sie muss auch **dort** installiert sein, nicht nur im Windows-VS-Code (siehe
Installation).

## Installation — ohne Marketplace

Der Marketplace ist nicht nötig. Die Erweiterung steckt im Binary (Task 027). `install.sh`
installiert sie nach dem Binary gleich mit, wenn das Editor-CLI eindeutig ist — `code`, sonst
das einzige von `code-insiders`, `cursor`, `codium` —, über `vscode install --code <cli>`. Sind
es mehrere, nennt es nur den Befehl; ohne CLI tut es nichts; scheitert es, bleibt es bei einem
Hinweis. Ohne diesen Schritt bekäme sie niemand von selbst, denn `kephalaion upgrade` erneuert
sie nur dort, wo sie schon liegt. Von Hand installiert sie sich mit einem Befehl, **als
eigener User, nicht über sudo**:

```sh
kephalaion vscode install                # VS Code; sonst das einzige Editor-CLI im PATH
kephalaion vscode install --code cursor  # Cursor; ebenso codium, code-insiders
```

`install` schreibt die eingebettete `.vsix` in eine temporäre Datei und ruft `<cli>
--install-extension <datei> --force` — immer neu, auch über eine höhere Fassung. Ohne `--code`
nimmt es `code`, wenn es im `PATH` ist, sonst das einzige von `code-insiders`, `cursor`,
`codium`; sind es mehrere oder keins, bricht es ab und nennt die gefundenen. Danach im Editor
„Developer: Reload Window“. Ohne CLI im `PATH` (etwa ein Editor ohne Shell-Befehl):

```sh
kephalaion vscode vsix -o kephalaion.vsix
```

und im Editor „Extensions: Install from VSIX…“. `kephalaion vscode status` zeigt die
eingebettete Version und die installierten Fassungen je Editor (VS Code, Insiders, Cursor,
VSCodium, samt ihrer Remote-Server-Verzeichnisse wie `~/.vscode-server`, `~/.cursor-server`).
Ein Binary, das ohne Node.js gebaut ist, trägt keine Erweiterung und sagt das (`kephalaion
version`, `vscode status`; `install` und `vsix` enden mit Exit 1).

Unter WSL installiert `code` aus einem Terminal der WSL in den VS-Code-Server der WSL, also
dorthin, wo eine Erweiterung der Art `workspace` hingehört — bestätigt am 2026-09-26: `code`
meldet dabei „Installing extensions on WSL: Ubuntu…“. Dasselbe gilt für ein SSH-Remote und
einen Devcontainer; root ohne sudo (etwa im Devcontainer) installiert normal.

**Einmal beim Übergang:** Das erste `kephalaion upgrade` auf eine Fassung mit eingebetteter
Erweiterung läuft noch mit dem alten Binary, das die Erweiterung nicht anfasst. Danach einmal
von Hand `kephalaion vscode install`; jedes weitere `upgrade` erneuert sie selbst.

Für die Entwicklung im Quell-Repo baut `make vscode-install` die Erweiterung aus `vscode/` und
installiert sie mit `code`.

## Ein Repository, ein Release

**Entschieden am 2026-09-26, umgesetzt mit Task 027 (2026-10-05): Die Erweiterung liegt in
diesem Repository, steckt im Binary (`go:embed`, `internal/vscodeext`) und trägt dessen
Version.** Ein Release erneuert beide, auch wenn sich nur eines geändert hat.

- **Version:** Tag `vX.Y.Z` → `X.Y.Z`, Vorabversion `vX.Y.Z-…` → `X.Y.Z` (VS Code nimmt nur
  `x.y.z`; `vsce` prüft das nicht), dev build → `0.0.0`. `make vscode-vsix` gibt sie `vsce`
  als Argument; `vscode/package.json` steht fest auf `0.0.0`, die Nummer pflegt niemand von
  Hand.
- **Kein Abgleich von Versionen:** Die Erweiterung vergleicht die Version aus `whoami` mit
  ihrer eigenen und weist einmal je Sitzung auf einen Unterschied hin, mit `kephalaion vscode
  install` (für Cursor usw. mit `--code`). Ist eine Seite ein dev build (`dev` bzw. `0.0.0`),
  kein Hinweis.
- **`upgrade` installiert neu, statt zu vergleichen:** Nach dem Ersetzen des Binarys und dem
  Neustart des Dienstes ruft `kephalaion upgrade` das **neue** Binary mit `vscode install
  --code <cli>` auf — je Editor, in dem die Erweiterung installiert ist, einmal. Fehlt das CLI
  im `PATH` (typisch: SSH-Shell ohne `code`), ein Hinweis; scheitert es, eine Warnung mit dem
  Befehl von Hand. Der Exit-Code des Upgrades ändert sich dadurch nie. Bei der globalen
  Installation und unter sudo installiert `upgrade` nichts und nennt nur `kephalaion vscode
  install` als eigener User.
- **Bauen:** `make build`, `dist` und `dev-install` bauen die `.vsix` mit `vsce` (gepinnt, per
  `npx`) und brauchen dafür Node.js. Fehlt es oder scheitert `vsce`, nur eine Warnung und ein
  Binary ohne Erweiterung; mit `REQUIRE_VSCODE=1` (Release, CI) ein Abbruch. `make
  vscode-verify` weist am gebauten Binary nach, dass es die Erweiterung mit der richtigen
  Version trägt; Release und CI rufen es. `go build` und `go test ./...` laufen weiter ohne
  Node.js. Grenze: Ein schlichtes `go build` nach einem `make` bettet die liegende `.vsix`
  ein; `make clean` räumt sie weg.

## Suche: eigene Befehle statt der Suche von VS Code

Die Suche von VS Code (Strg+Umschalt+F) und Quick Open (Strg+P) finden in `keph://`-Ordnern
nichts, im Baum ebenso wenig. Die Schnittstellen dafür (`TextSearchProvider`,
`FileSearchProvider`) sind „proposed API“ und in einer normalen Installation nicht
freigeschaltet. Daran ändert sich nichts.

**Weiter gewollt** (2026-09-26): In VS Code wird Code gesucht, nicht Dokumentation; Treffer aus
der Doku gehören nicht in die Codesuche von VS Code. Gesucht wird im Store über MCP — von der KI,
mit der Suche des Nodes (FTS5), die besser rankt, als es die Textsuche von VS Code könnte.

**Seit 2026-11-25 (Task 043) hat die Erweiterung aber zwei eigene Befehle** — eine Abkehr von
der Entscheidung vom 2026-09-26, „Die Erweiterung bekommt keinen eigenen Suchbefehl“, vom
Nutzer am 2026-11-25 bestätigt. Grund: Eigene Befehle mischen keine Doku-Treffer in die
Codesuche von VS Code, die Sorge von damals bleibt gewahrt; und ohne Workspace-Ordner braucht
der Mensch einen Weg zu einem Dokument, dessen Ort er nicht kennt.

- **„Kephalaion: Dokument öffnen“** statt Strg+P: `list` mit `recursive` je gezeigter
  Collection, mit Cursor bis zum Ende (`KephFs.listDocuments`, derselbe Node-Aufruf wie
  `readDirectory`); eine Auswahl mit dem Namen und der Adresse, gesucht auch in der Adresse.
  Geöffnet wird über die `keph://`-URI.
- **„Kephalaion: Suchen“** statt Strg+Umschalt+F: ein Eingabefeld, dann das Werkzeug `search`
  des Nodes; die Treffer der gezeigten Collections mit Titel, Abschnitt und Adresse. Die Auswahl
  öffnet das Dokument und setzt den Cursor auf die Überschrift des Abschnitts (im Text gesucht;
  findet sie sich nicht, an den Anfang).
- **Grenze:** `search` sucht nur unter `docs/` einer Collection und in einfachen englischen
  Wörtern ([`indizierung.md`](indizierung.md)); der Platzhalter des Eingabefelds sagt es. Eine
  Volltextsuche über die ganze Collection gibt es nicht, und ein Werkzeug nur dafür kommt nicht
  dazu (keine Werkzeuge nur für VS Code, siehe „Welche Werkzeuge“).
- Beide stehen im Menü der Statusleiste und in der Titelleiste des Baums.

**Ausnahme, beobachtet am 2026-09-26:** Dokumente, die VS Code gerade geladen hat, findet die
Suche doch — sie durchsucht neben der Platte immer auch die offenen Dokumente, damit
Ungespeichertes gefunden wird, unabhängig vom Dateisystem. Nach dem Schließen verschwindet der
Treffer (mit etwas Verzögerung, erst nach einer weiteren Suche). Das ist in Ordnung.

## Offen

- Wie der Node-Dienst gestartet wird, wenn er nicht läuft — Hinweis in VS Code oder selbst
  starten (wie k-playbook beim Briefing).
- Abstand der Abfrage von `changes` — fest, oder länger, solange das Fenster nicht im Fokus
  ist. Fehlversuche begrenzt seit Task 025 die Pause an einer entfernten Adresse; die Rate selbst
  ist unverändert.
- Leere Verzeichnisse: vorerst nur in der Erweiterung gemerkt (Task 014); ob später als
  `SYSTEM:D:`-Zeile am Hub, bleibt offen (vgl. „Persönliche Verzeichnisse“ im Konzept).
- ~~Das Schreiben im echten VS Code: die Handgriffe unter „Umsetzung: Schreiben“, „Im echten
  VS Code noch zu prüfen“ — vor allem, ob der `FileService` den Provider so ruft wie
  angenommen.~~ *Für Baum und Editor erledigt mit der Prüfung vom 2026-11-25* (siehe
  „Baumansicht im echten VS Code noch zu prüfen“); die Handgriffe im Explorer gelten seit
  Task 044 nur noch im reinen Collection-Workspace.
- Wie die Erweiterung mit `personal`-Verzeichnissen umgeht (nur Eigenes zeigen, Schalter für
  alles).
- ~~Ob eine TreeView zusätzlich zum Dateisystem sinnvoll ist, etwa für Status und Abgleich.~~
  *Beantwortet mit Task 043:* ja, als Baum der Collections über demselben Dateisystem (siehe
  „Baumansicht“); Status und Abgleich bleiben in der Statusleiste.

## Versuch: Dummy (2026-09-26)

*Dieser und die beiden folgenden datierten Abschnitte sind Geschichte: Sie beschreiben den Weg
über einen `keph://`-Workspace-Ordner, den es seit Task 044 nicht mehr gibt; ihre Handgriffe im
Explorer gelten nur noch im reinen Collection-Workspace (siehe „Baumansicht“, „Grenzen“).*

Unter [`vscode/`](../vscode/): reines JavaScript ohne Abhängigkeiten, ein
`FileSystemProvider` für `keph://` mit einem festen Verzeichnis und einer Datei, nur lesen,
ohne Verbindung zum Node. Zwei Befehle: „Dummy-Datei öffnen“ und „Dummy-Ordner zum Workspace
hinzufügen“. Gebaut mit `npx @vscode/vsce package --skip-license`, installiert mit
`code --install-extension` aus der WSL.

Ergebnis:

- Die Datei öffnet sich schreibgeschützt, die Markdown-Vorschau funktioniert.
- Der Ordner erscheint im Explorer als Ordner des Workspace; das Fenster wird dabei zu
  einem Workspace mit mehreren Ordnern und lädt neu.
- Die Suche verhält sich wie oben beschrieben.
- Der Weg trägt; offen ist nur noch die Anbindung an den Node.

## Umsetzung: Lesen (2026-09-26)

Ohne Task gebaut, in zwei Schritten, weiter reines JavaScript ohne Abhängigkeiten
([`vscode/extension.js`](../vscode/extension.js)). MCP über das `fetch` von Node.js, JSON-RPC
ohne Sitzung — das SDK braucht es dafür nicht.

- **0.0.2 — `whoami`:** Adresse aus `listen`, Tokens aus `tokens/<hub>/<account>.token`
  (höchstens alle 5 s neu von der Platte), Statusleiste mit Tooltip, abgefragt alle 30 s;
  Menü; „Collection einbinden“. Im echten VS Code geprüft.
- **0.0.3 — Inhalte:** `stat` über `read` mit `content: false`, `readDirectory` über `list`
  (Namen kommen als voller Pfad ab der Collection, die Erweiterung nimmt das letzte Segment;
  blättert mit `cursor`), `readFile` über `read` — der Inhalt ist der Text des Ergebnisses
  (*überholt mit 0.0.8:* der Inhalt steht im Feld `content`, siehe „`read` mit `content`“).
  `changes` alle 3 s mit dem Cursor der letzten Antwort; je Eintrag `Changed` bzw. `Deleted`
  für das Dokument und `Changed` für jedes Verzeichnis darüber bis zur Collection, damit der
  Explorer neue und leer gewordene Verzeichnisse sieht. Ein Umbenennen kam so als
  gelöscht und neu an, die `id` wertete die Erweiterung dafür nicht aus — *überholt mit 0.0.5:*
  Sie wird jetzt ausgewertet, siehe „Umsetzung: Schreiben“. `reset` und
  `dropped` lassen die Wurzel des Hubs neu lesen. Dazu „Kephalaion: Status anzeigen“ — der
  Inhalt des Tooltips im Output „Kephalaion“, weil der Tooltip schwer zu finden ist (der
  Tooltip eines Ordners im Explorer zeigt nur den Pfad, etwa `\eins`).
- **Fehler beim Lesen:** Eine Antwort des Werkzeugs mit Fehler („nicht lesbar“) wird zu
  `FileNotFound`, ein Fehler der Verbindung zu `Unavailable` — gilt weiter für `stat`,
  `readDirectory` und `readFile`; beim Schreiben entscheidet seit 0.0.5 der Code (siehe
  „Umsetzung: Schreiben“).
- ~~**Alles schreibgeschützt**, auch mit `writable: true`, bis Schreiben gebaut ist.~~
  *Überholt mit 0.0.5:* schreibgeschützt nur ohne `writable`, `isReadonly` ist `false`.
- **Geprüft** mit einem Ersatz für das Modul `vscode` gegen den laufenden Node (`home:eins`,
  Dokumente unter `test/`): Verzeichnisse, Metadaten, Inhalt, „nicht gefunden“, fremde
  Collection, Ereignisse nach `hub doc put` und `node sync`. Im echten VS Code: ein Dokument
  aus `keph://home/eins` geöffnet.
- **0.0.4 — Account wählen:** alle `tokens/<hub>/*.token` je Hub, Wahl über „Kephalaion:
  Account wählen“ in `kephalaion.accounts` (siehe „Status und Auswahl“). Eine andere Wahl —
  auch von Hand in `settings.json` — liest die Tokens sofort neu, setzt `changes` neu an und
  lässt die Hubs neu lesen, weil Rechte und Collections dann andere sein können. Tooltip und
  „Status anzeigen“ nennen die Accounts eines Hubs und welcher gewählt ist. Geprüft mit dem
  Ersatz für `vscode` und Kopien der Token-Dateien samt einem erfundenen zweiten Account:
  ohne Wahl, gewählt, gewählt ohne Datei, falscher Account gewählt; `.pending` übergangen.

## Umsetzung: Schreiben (2026-09-27, 0.0.5)

Mit Task 014 gebaut, weiter reines JavaScript ohne Abhängigkeiten
([`vscode/extension.js`](../vscode/extension.js), [`vscode/README.md`](../vscode/README.md)).
Jeder Schreibvorgang geht über die Werkzeuge des Nodes zum Hub (`create`, `write`, `delete`,
`rename`), wird nie wiederholt, und nach Erfolg feuert die Erweiterung die Ereignisse selbst.

- **`stat` und Schreibschutz:** schreibgeschützt nur ohne `writable` — ein Recht je
  Collection (`write`); `isReadonly` ist `false`. Ein fremdes Dokument ohne `supersede`
  scheitert erst beim Speichern mit `NoPermissions`; `supersede` ohne `write` bleibt
  schreibgeschützt (bewusste Grenze, siehe „Rechte“ oben).
- **`writeFile`:** zuerst `read` mit `content: false`; `none` → `create`, `document` →
  `write` mit dessen Revision, `directory` → `FileIsADirectory`. Der Inhalt wird vorher streng
  als UTF-8 geprüft — ein BOM bleibt erhalten —, ohne NUL-Byte und höchstens 1 MiB; sonst eine
  klare Meldung („keine Textdatei“, „zu groß“), ohne dass etwas abgeschickt wird.
- **`delete`** mit `recursive` aus den Optionen; ein nur gemerktes leeres Verzeichnis wird
  vergessen, ohne den Hub zu fragen. **`rename`** nur innerhalb einer Collection; über
  Collections oder Hubs eine Meldung („dafür kopieren und löschen“), ein belegtes Ziel
  `FileExists`, mit `overwrite` die Meldung, dass Umbenennen nicht überschreibt. `delete` und
  `rename` schicken kein `base_revision`.
- **`createDirectory`** legt am Hub nichts an: Das leere Verzeichnis gilt nur in diesem
  Fenster, bis darin etwas liegt oder es gelöscht wird; `stat` und `readDirectory` zeigen es.
  Nach dem Neuladen des Fensters ist es weg — gewollt.
- **Tabelle `id` → Name** je Collection, gespeist aus `list`, `read`, `changes` und den
  Antworten eigener Schreibvorgänge; ein älterer Stand überschreibt keinen neueren. Meldet
  `changes` eine bekannte `id` unter neuem Namen: `Deleted` für den alten Namen, `Created` für
  den neuen (bzw. `Deleted`, wenn er danach gelöscht wurde), `Changed` für beide Elternpfade —
  so sieht ein zweites VS Code das Umbenennen. `reset` verwirft die Tabellen des Hubs,
  `dropped` die der Collection, ein anderer Account alle.
- **Fehler nach dem Code** in `structuredContent.error.code`, nie nach der Meldung:

  | Code des Nodes | `FileSystemError` |
  |---|---|
  | `name_taken` | `FileExists` |
  | `not_found` | `FileNotFound` |
  | `forbidden`, `not_readable` | `NoPermissions` |
  | `unreachable`, `outcome_unknown` | `Unavailable`, mit der Meldung |
  | alle anderen, auch `stale_revision` | ein Fehler mit der Meldung des Nodes |

  Die Fehler von `read` (auch vor einem Schreibvorgang) tragen keinen Code; die Erweiterung gibt
  dann die Meldung des Nodes weiter. Auf dem Weg zum Node unterscheidet sie drei Arten: nicht
  erreichbar (nichts abgeschickt — „nichts gespeichert“), Ausgang unklar (Verbindung nach dem
  Abschicken abgebrochen, 5xx, unlesbare Antwort — „gespeichert sein kann es, nicht
  wiederholen“) und abgelehnt.
- **Geprüft** mit dem Ersatz für `vscode` gegen den laufenden Node (`home:eins`, unter
  `test/schreiben/…`, 118 Prüfungen): neue Datei leer, dann mit Inhalt; zweimal speichern;
  Konflikt nach `hub doc put`; Löschen von Datei und Verzeichnis; Umbenennen von Datei und
  Verzeichnis mit einem zweiten Client, der über `changes` den alten Namen verschwinden und den
  neuen kommen sieht (auch umbenennen, dann löschen, ein Abgleich); leeres Verzeichnis anlegen
  und befüllen; Binärdatei, NUL und Größe abgelehnt; fremdes Dokument erst beim Speichern
  `NoPermissions`; Node nicht erreichbar. In einem isolierten Aufbau mit Node über `http`: Hub
  gestoppt → `Unavailable`, Lesen geht weiter; `outcome_unknown` und abgebrochene Verbindung.
  Der Weg darunter — zwei Nodes, Konflikt zwischen ihnen, Hub gestoppt — steht als Go-Test
  `TestMCPWriteTwoNodes` in `cmd/kephalaion`.

### Im echten VS Code noch zu prüfen

*Seit Task 044 gelten diese Handgriffe im Explorer nur noch im reinen Collection-Workspace.*

**Offen, Stand 2026-09-27** — der Nutzer geht die Handgriffe selbst durch, nur unter `test/`
in `home:eins`. Im Output „Kephalaion“ (Menü → Log) steht jeder Aufruf mit Vorgang, Name und
Ausgang; daran lässt sich ablesen, was VS Code wirklich aufruft.

1. **Installieren**, aus einem Terminal der WSL:
   `code --install-extension vscode/kephalaion-0.0.5.vsix` (im Repository; fehlt die Datei:
   `cd vscode && npx --yes @vscode/vsce package --skip-license`), dann „Developer: Reload
   Window“. Die Statusleiste zeigt `Keph home`, `keph://home/eins` ist eingebunden.
2. **Speichern:** ein Dokument unter `test/` öffnen, ändern, Strg+S — und gleich noch einmal
   ändern und speichern: kein Konflikt. `kephalaion hub doc get eins test/…` zeigt den Stand.
3. **Neue Datei** im Explorer unter `test/`: erscheint leer (`create` mit leerem Inhalt),
   dann Inhalt tippen und speichern (`write`).
4. **Neuer Ordner** unter `test/`, darin eine neue Datei: danach besteht der Ordner am Hub.
   Einen zweiten leeren Ordner anlegen und „Developer: Reload Window“: Er ist weg — gewollt,
   leere Ordner gelten nur je Fenster.
5. **Ordner per Drag & Drop** aus dem Projekt (Explorer von VS Code oder von Windows) nach
   `keph://home/eins/test/`, mit Unterordnern: alles kommt an. Einmal mit einer Binärdatei
   (etwa `.png`) darin: nur diese scheitert mit „keine Textdatei“, die übrigen kommen an.
6. **Umbenennen** einer Datei und eines Ordners im Explorer (F2): Die `id` bleibt — im Log
   nennt die Zeile `rename …` dieselbe `id` wie das `create` der Datei aus Punkt 3 —, und ein
   zweites Fenster mit derselben Collection sieht den alten Namen verschwinden und den neuen
   kommen.
7. **Verschieben** per Drag & Drop innerhalb der Collection. Auf ein **belegtes Ziel** mit
   „Ersetzen“: Was geschieht? Erwartet ist, dass VS Code das Ziel selbst löscht und dann
   umbenennt (im Log `delete …`, dann `rename …`); sonst erscheint die Meldung „Umbenennen
   überschreibt nicht“.
8. **Verschieben in eine andere Collection** (oder einen anderen Hub; braucht eine zweite
   eingebundene Collection mit `write`): die Meldung „Verschieben nur innerhalb einer
   Collection … kopieren und löschen“; Strg+Drag kopiert dagegen.
9. **Löschen** einer Datei und eines Ordners im Explorer (Entf), die Rückfrage bestätigen —
   einen Papierkorb gibt es für `keph://` nicht; danach fehlen beide auch am Hub
   (`kephalaion hub doc list eins test/`).
10. **„Datei ist neuer“:** ein Dokument unter `test/` öffnen und ändern, ohne zu speichern; am
    Hub `echo neu | kephalaion hub doc put eins test/…`; `kephalaion node sync home` (oder den
    Abgleich abwarten, bis 30 s); dann in VS Code speichern: VS Code meldet, die Datei sei
    neuer. „Vergleichen“ zeigt beide Stände, „Überschreiben“ speichert — danach am Hub den
    eigenen Inhalt prüfen.
11. **Konflikt vor dem Abgleich:** wie 10, aber sofort nach `hub doc put` speichern, bevor
    abgeglichen ist: eine Fehlermeldung „… hat Revision …, der Vorgang beruht auf …“, am Hub
    bleibt `neu`.
12. **Fremdes Dokument:** `echo fremd | kephalaion hub doc put eins test/fremd.md` (gehört
    `admin`), in VS Code öffnen — es ist nicht schreibgeschützt —, ändern und speichern: erst
    jetzt „gehört admin, supersede fehlt“ (`NoPermissions`); am Hub bleibt `fremd`.
13. **Noch unbestätigt** (abgeleitet aus `src/vs/platform/files/common/fileService.ts` von VS
    Code — `writeFile`, `mkdirp`, `doMoveCopy` —, nicht im echten VS Code nachgesehen), im Log
    nachsehen: ruft der `FileService` `writeFile` immer mit `create` und
    `overwrite` auf (Speichern und neue Datei gelingen, kein `FileExists`/`FileNotFound`), legt
    er fehlende Eltern über `createDirectory` an (beim Drop eines verschachtelten Ordners Zeilen
    `createDirectory … nur hier gemerkt` vor den `create`), und löscht er bei „Ersetzen“ das Ziel
    selbst vor `rename` (Punkt 7)?

## Fundstellen

- API-Referenz `FileSystemProvider`:
  https://code.visualstudio.com/api/references/vscode-api#FileSystemProvider
- Beispiel eines Dateisystems im Speicher („MemFS“), die beste Vorlage:
  https://github.com/microsoft/vscode-extension-samples/tree/main/fsprovider-sample
- Virtuelle Workspaces — was in ihnen geht und was nicht:
  https://code.visualstudio.com/api/extension-guides/virtual-workspaces
- Erweiterungen unter Remote, WSL, Devcontainer (`extensionKind`):
  https://code.visualstudio.com/api/advanced-topics/remote-extensions
- Einstieg in eine erste Erweiterung:
  https://code.visualstudio.com/api/get-started/your-first-extension
- Verpacken und lokal installieren (`vsce package`, `.vsix`):
  https://code.visualstudio.com/api/working-with-extensions/publishing-extension
- WASI in VS Code (nur zur Einordnung):
  https://code.visualstudio.com/blogs/2023/06/05/vscode-wasm-wasi
- MCP-Client in TypeScript: https://github.com/modelcontextprotocol/typescript-sdk
- Vorbild aus der Praxis: „GitHub Repositories“ von Microsoft, bindet Repos als
  `vscode-vfs://` ein, ohne zu klonen.
