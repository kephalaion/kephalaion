// Schranke, Pause und Budget der Erweiterung an einer entfernten nodeUrl (Task 025), ohne vscode,
// damit node --test sie prüft (vscode/test/pause.test.js; Begriffe in docs/begriffe.md: „probe“,
// „paused pair“, „budget“, „pause file“; docs/vscode.md, „Fehlversuche“). Ein ungültiges Paar
// sperrte sonst die eigene Adresse: Jede Anfrage mit ungültigem Paar ist an der Jail des Proxys
// ein Fehlversuch, 10 in 10 min (5 nach einer früheren Sperre) sperren 80/443 — und die Erweiterung
// fragt in jedem Fenster alle 3 s. Deshalb:
//
// - Schranke: Vor jeder anderen Anfrage an die Adresse geht genau eine Probe (whoami) mit den
//   aktuellen Paaren; alles andere wartet auf ihr Ergebnis. Neu geprobt wird, wenn sich ein Paar
//   ändert (Token-Datei, Wahl, Einstellung, „Neu verbinden“), nicht vor jeder Anfrage.
// - Pause je Paar: Ein ungültiges Paar geht nicht mehr mit, bis eine Probe es klärt — nach
//   wachsender Pause (SCHEDULE_MIN) und nur, wenn das Budget der Adresse es erlaubt.
// - Budget je Adresse: die Zeiten der Fehlversuche der letzten WINDOW_MS, über alle Paare und
//   Fenster; Proben von selbst nur unter RESERVE — die übrigen 2 der 4 bleiben für einen Übergang
//   gültig → ungültig im Betrieb samt Anfragen, die schon unterwegs sind.
// - Ein Stand für alle Fenster dieses Rechners: die Pausendatei unter XDG_STATE_HOME, atomar
//   geschrieben, vor jeder Anfrage billig neu gelesen (nur, wenn sie sich geändert hat); proben
//   darf zur selben Zeit nur ein Fenster (Sperrdatei mit O_EXCL, nach LOCK_STALE_MS verwaist).
//   Fehlt sie oder ist sie kaputt: leer gelesen, die nächste Probe schreibt sie neu. Lässt sich
//   dort nichts anlegen (Verzeichnis ohne Schreibrecht, Platte voll, schreibgeschützt
//   eingehängt): Pausen- und Sperrdatei zusammen am Ersatzort — ein Verzeichnis je Benutzer
//   unter os.tmpdir() —, bis der erste Ort wieder schreibbar ist (jedes Schreiben und Sperren
//   versucht ihn zuerst; gelingt es, zurück, mit dem Stand). Damit alle Fenster denselben Stand
//   sehen, auch eines, das am ersten Ort noch schreiben kann, liest jedes beide Orte und nimmt
//   den jüngeren Stand (writtenAt in der Datei), und die Sperre wird an beiden Orten geprüft.
//   Erst wenn auch der Ersatzort nicht geht: kein Ergebnis einer Probe und kein Fehler für die
//   Anfrage — das Fenster hält denselben Stand im Speicher und arbeitet damit weiter, bis das
//   Schreiben wieder gelingt; ohne Sperrdatei probt es für sich. Je Wechsel einmal im Log.
//   Grenze: Sind beide Orte unschreibbar, zählt der Stand nur je Fenster.
// - Probe ohne Ergebnis (keine Verbindung, 5xx des Proxys, Abbruch): Die Schranke bleibt zu —
//   keine Anfrage mit ungeprüften Paaren, die wartenden scheitern —, und die Adresse wird mit
//   wachsendem Abstand (RETRY_S, 5 s bis 60 s) neu geprobt, geteilt über die Pausendatei; ein
//   geändertes Paar und „Neu verbinden“ proben sofort. Eine 401 des Proxys ist ein Ergebnis:
//   ein Fehlversuch im Budget, alle geschickten Paare pausiert.
//
// Kein Token und kein Hash in der Datei: je Paar nur Adresse, Hub, Account und die Kennung der
// Token-Datei (Größe und Zeit). Uhr, Warten und Dateisystem sind ersetzbar (Tests).

'use strict';

const fs = require('fs');
const os = require('os');
const path = require('path');

// findtime und maxretry der Jail (docs/installation.md, „Fehlversuche“); nach einer früheren Sperre
// zählt jeder Fehlversuch dort doppelt.
const WINDOW_MS = 10 * 60 * 1000;
const JAIL_LIMIT = 10;
// Proben von selbst nur, wenn weniger als RESERVE Fehlversuche im Fenster stehen.
const RESERVE = 2;
// Die wachsende Pause je Paar nach dem n-ten Fehlversuch, in Minuten; danach die letzte.
const SCHEDULE_MIN = [1, 2, 4, 8, 16, 30];
// Der Abstand bis zur nächsten Probe nach der n-ten ohne Ergebnis, in Sekunden; danach der letzte.
// 5 und 10 s fangen einen Neustart des Nodes (make deploy ROLE=kephalaion), 30 und 60 s eine
// längere Störung (Netz, VPN): Dann geht von allen Fenstern des Rechners zusammen höchstens eine
// Probe je Minute — weniger als das whoami eines einzelnen Fensters im Normalbetrieb (alle 30 s).
// Eine Probe ohne Ergebnis ist an der Jail kein Fehlversuch und zählt nicht ins Budget.
const RETRY_S = [5, 10, 30, 60];
// Eine Sperrdatei gilt nach dieser Zeit als verwaist (Extension Host abgestürzt).
const LOCK_STALE_MS = 30 * 1000;
const LOCK_POLL_MS = 200;
// Pausen älter als das fallen aus der Datei: Ein so altes Paar wird ohnehin neu geprobt.
const KEEP_MS = 24 * 60 * 60 * 1000;
const FILE_VERSION = 1;

// Der Ort der Pausendatei: XDG_STATE_HOME, sonst ~/.local/state (docs/konzept.md, „Orte nach XDG“).
function stateDir() {
  const xdg = process.env.XDG_STATE_HOME || path.join(os.homedir(), '.local', 'state');
  return path.join(xdg, 'kephalaion');
}

function pauseFilePath() {
  return path.join(stateDir(), 'vscode-pause.json');
}

// Der Ersatzort, wenn sich unter XDG_STATE_HOME nichts anlegen lässt: ein Verzeichnis je Benutzer
// unter os.tmpdir() (0700; unter Windows ist %TEMP% ohnehin je Benutzer), damit alle Fenster
// desselben Benutzers denselben wählen. uid, sonst der Benutzername.
function fallbackPauseFilePath() {
  return path.join(os.tmpdir(), `kephalaion-${userTag()}`, 'vscode-pause.json');
}

function userTag() {
  if (typeof process.getuid === 'function') return String(process.getuid());
  try {
    return os.userInfo().username.replace(/[^\w.-]/g, '_');
  } catch {
    return 'user';
  }
}

function lockPath(file) {
  return `${file}.lock`;
}

// Die Pause nach dem n-ten Fehlversuch eines Paars, in ms.
function pauseAfter(failures) {
  const i = Math.min(Math.max(failures, 1), SCHEDULE_MIN.length) - 1;
  return SCHEDULE_MIN[i] * 60 * 1000;
}

// Der Abstand nach der n-ten Probe ohne Ergebnis, in ms.
function retryAfter(count) {
  const i = Math.min(Math.max(count, 1), RETRY_S.length) - 1;
  return RETRY_S[i] * 1000;
}

function pairKey(address, hub, account) {
  return `${address}|${hub}|${account}`;
}

// unanswered: je Adresse die Proben ohne Ergebnis — Zahl, Zeit der letzten, Zeit der nächsten.
// writtenAt: wann der Stand geschrieben wurde — liegt an beiden Orten eine Datei, gilt der
// jüngere. Ältere Fassungen der Erweiterung kennen die Schlüssel nicht und lassen sie beim
// Schreiben fallen; das kostet dann nur eine Probe mehr, deshalb bleibt die Version der Datei.
function emptyState() {
  return { version: FILE_VERSION, writtenAt: 0, pairs: {}, budget: {}, unanswered: {} };
}

// Eine gelesene Datei in die Form bringen; alles Unbrauchbare fällt weg.
function normalize(raw) {
  const state = emptyState();
  if (!raw || typeof raw !== 'object' || raw.version !== FILE_VERSION) return state;
  if (Number.isFinite(raw.writtenAt)) state.writtenAt = raw.writtenAt;
  for (const [k, e] of Object.entries(raw.pairs || {})) {
    if (!e || typeof e !== 'object') continue;
    const { address, hub, account, token, failures, failedAt, nextProbe } = e;
    if ([address, hub, account, token].some((v) => typeof v !== 'string')) continue;
    if ([failures, failedAt, nextProbe].some((v) => !Number.isFinite(v))) continue;
    if (k !== pairKey(address, hub, account)) continue;
    state.pairs[k] = { address, hub, account, token, failures, failedAt, nextProbe };
  }
  for (const [address, times] of Object.entries(raw.budget || {})) {
    if (!Array.isArray(times)) continue;
    const ok = times.filter((t) => Number.isFinite(t));
    if (ok.length) state.budget[address] = ok.sort((a, b) => a - b);
  }
  for (const [address, u] of Object.entries(raw.unanswered || {})) {
    if (!u || typeof u !== 'object') continue;
    const { count, at, nextProbe } = u;
    if ([count, at, nextProbe].some((v) => !Number.isFinite(v))) continue;
    state.unanswered[address] = { count, at, nextProbe };
  }
  return state;
}

// Veraltetes entfernen: Budget-Zeiten außerhalb des Fensters, alte Pausen.
function prune(state, now) {
  for (const [address, times] of Object.entries(state.budget)) {
    const keep = times.filter((t) => now - t < WINDOW_MS && t <= now);
    if (keep.length) state.budget[address] = keep;
    else delete state.budget[address];
  }
  for (const [k, e] of Object.entries(state.pairs)) {
    if (now - e.failedAt > KEEP_MS) delete state.pairs[k];
  }
  for (const [address, u] of Object.entries(state.unanswered)) {
    if (now - u.at > KEEP_MS) delete state.unanswered[address];
  }
}

// Ein Ergebnis für die Adresse beendet den Abstand; sagt, ob sich etwas geändert hat.
function clearUnanswered(state, address) {
  if (!state.unanswered[address]) return false;
  delete state.unanswered[address];
  return true;
}

// Die Fehlversuche einer Adresse im Fenster, nach Zeit.
function budgetTimes(state, address, now) {
  return (state.budget[address] || []).filter((t) => now - t < WINDOW_MS && t <= now).sort((a, b) => a - b);
}

// Wann das Budget wieder eine Probe erlaubt: jetzt, oder wenn genug Zeiten gealtert sind.
function budgetFreeAt(state, address, now) {
  const times = budgetTimes(state, address, now);
  if (times.length < RESERVE) return now;
  return times[times.length - RESERVE] + WINDOW_MS;
}

// Die Pausendatei an zwei Orten: file (unter XDG_STATE_HOME) und fallback (unter os.tmpdir(),
// null: keiner). Gelesen werden beide, der jüngere Stand gilt; geschrieben und gesperrt wird am
// ersten Ort, der es zulässt — der erste wird jedes Mal zuerst versucht, so kehrt ein Fenster
// von selbst zurück. Lesen-Ändern-Schreiben atomar; die Sperrdatei neben der Datei des Orts.
// fsImpl und now sind ersetzbar; log bekommt je einmal einen Wechsel des Orts, wenn kein Ort
// (mehr) schreibbar ist oder keine Sperrdatei anlegbar, und wenn es wieder geht.
class PauseFile {
  constructor(file, { fallback = fallbackPauseFilePath(), now = Date.now, fsImpl = fs, log = () => {} } = {}) {
    this.primary = file;
    this.fallback = fallback && fallback !== file ? fallback : null;
    // file: der Ort, an dem dieses Fenster zuletzt schreiben oder sperren konnte.
    this.file = file;
    // lockFile: die zuletzt genommene Sperrdatei (unlock löscht sie).
    this.lockFile = lockPath(file);
    this.now = now;
    this.fs = fsImpl;
    this.log = log;
    // Je Ort die Kennung der gelesenen Datei und ihr Stand.
    this._stamps = [];
    this._read = [];
    this._state = emptyState();
    // unwritable: Das letzte Schreiben scheiterte an beiden Orten — der Stand gilt im Speicher,
    // bis es wieder gelingt; bis dahin wird keine Datei gelesen (sie trüge den Stand nicht).
    this.unwritable = false;
    this._lockUnwritable = false;
  }

  // Die Orte in der Reihenfolge, in der sie versucht werden.
  places() {
    return this.fallback ? [this.primary, this.fallback] : [this.primary];
  }

  // Das Verzeichnis des Orts anlegen (0700) und prüfen, dass es dem Benutzer gehört: Unter
  // os.tmpdir() könnte ein anderer es vorher angelegt haben — dann wäre es kein Ort für uns
  // (seine Sperre, seine Pausen). Wirft; der Ort gilt dann als nicht schreibbar.
  ensureDir(file) {
    const dir = path.dirname(file);
    this.fs.mkdirSync(dir, { recursive: true, mode: 0o700 });
    if (typeof process.getuid === 'function') {
      const { uid } = this.fs.statSync(dir);
      if (uid !== process.getuid()) {
        throw Object.assign(new Error(`${dir} gehört einem anderen Benutzer (uid ${uid})`), { code: 'EACCES' });
      }
    }
  }

  // Der Stand: aus beiden Orten der jüngere (writtenAt; gleich alt: der erste Ort), je Ort neu
  // gelesen nur, wenn Inode, Größe oder Zeit anders sind. Fehlt eine Datei oder ist sie kaputt,
  // zählt sie nicht — Schranke und Sperrdatei gelten trotzdem. Solange sich an keinem Ort
  // schreiben lässt: der Stand im Speicher.
  load() {
    if (this.unwritable) return this._state;
    let changed = false;
    this.places().forEach((file, i) => {
      let st;
      try {
        st = this.fs.statSync(file);
      } catch {
        if (this._stamps[i] !== undefined || this._read[i] !== undefined) changed = true;
        this._stamps[i] = undefined;
        this._read[i] = undefined;
        return;
      }
      const stamp = `${st.ino}:${st.size}:${st.mtimeMs}`;
      if (stamp === this._stamps[i]) return;
      try {
        this._read[i] = normalize(JSON.parse(this.fs.readFileSync(file, 'utf8')));
      } catch {
        this._read[i] = undefined;
      }
      this._stamps[i] = stamp;
      changed = true;
    });
    if (changed) this._state = newest(this._read);
    return this._state;
  }

  // Lesen-Ändern-Schreiben: fn ändert den frisch gelesenen Stand und sagt, ob sich etwas
  // geändert hat; nur dann wird bereinigt und atomar geschrieben. Liefert den Stand.
  update(fn) {
    this._stamps = [];
    const state = this.load();
    const changed = fn(state);
    if (changed) {
      prune(state, this.now());
      this.write(state);
    }
    return state;
  }

  // Atomar: neue Datei, dann rename; 0600 — auch wenn kein Token darin steht. Erst am ersten
  // Ort, scheitert das (EACCES, ENOSPC, EROFS) am Ersatzort — ein Wechsel kommt einmal ins Log,
  // der Stand geht mit. Scheitert beides: kein Fehler nach oben — der Stand bleibt im Speicher
  // (unwritable), das nächste Schreiben versucht beide Orte erneut; einmal im Log. Sagt, ob
  // geschrieben wurde.
  write(state) {
    state.writtenAt = this.now();
    this._stamps = [];
    this._state = state;
    const failures = [];
    for (const file of this.places()) {
      const tmp = `${file}.${process.pid}.${Math.random().toString(36).slice(2, 10)}.tmp`;
      try {
        this.ensureDir(file);
        this.fs.writeFileSync(tmp, JSON.stringify(state), { mode: 0o600 });
        this.fs.renameSync(tmp, file);
      } catch (error) {
        this.remove(tmp);
        failures.push({ file, error });
        continue;
      }
      this.moveTo(file, failures[0]);
      if (this.unwritable) {
        this.unwritable = false;
        this.log(`Pausendatei ${file} wieder schreibbar: ein Stand für alle Fenster`);
      }
      return true;
    }
    if (!this.unwritable) {
      this.unwritable = true;
      this.log(`Pausendatei ${describe(failures)} nicht schreibbar: Pause, Budget und Abstand gelten `
        + 'nur in diesem Fenster, bis das Schreiben wieder gelingt');
    }
    return false;
  }

  // Der Ort dieses Fensters wechselt; failure ist der Fehler am ersten Ort (keiner: dort liegt
  // eine Sperrdatei, die sich nicht entfernen lässt). Einmal ins Log.
  moveTo(file, failure) {
    if (file === this.file) return;
    const from = this.file;
    this.file = file;
    if (file === this.primary) {
      this.log(`Pausendatei ${file} wieder schreibbar: zurück vom Ersatzort ${from}, mit dem Stand`);
    } else {
      const why = failure ? failure.error.message : 'eine Sperrdatei liegt dort und lässt sich nicht entfernen';
      this.log(`Pausendatei ${from} nicht schreibbar (${why}): Pausen- und Sperrdatei am `
        + `Ersatzort ${file}, bis der erste Ort wieder schreibbar ist`);
    }
  }

  // Die Sperrdatei nehmen: 'got' (genommen), 'busy' (ein anderes Fenster probt — an welchem Ort
  // auch immer), 'none' (an keinem Ort anlegbar oder schreibbar — dann ohne Sperre weiter, einmal
  // im Log). Eine verwaiste wird ersetzt. Genommen wird sie am ersten Ort, der es zulässt; das
  // ist dann auch der Ort der Pausendatei.
  tryLock() {
    const places = this.places();
    for (let attempt = 0; attempt < 3; attempt++) {
      for (const file of places) {
        const { status } = this.lockInfo(lockPath(file));
        if (status === 'busy') return 'busy';
        if (status === 'stale') this.remove(lockPath(file));
      }
      const failures = [];
      let raced = false;
      for (const [i, file] of places.entries()) {
        const lock = lockPath(file);
        let fd;
        try {
          this.ensureDir(file);
          fd = this.fs.openSync(lock, 'wx', 0o600);
        } catch (error) {
          // EEXIST: ein anderes Fenster war eben schneller (oder die verwaiste ließ sich nicht
          // entfernen) — der nächste Ort, danach noch einmal von vorn. Sonst ist der Ort nicht
          // nutzbar: der nächste.
          if (error.code === 'EEXIST') raced = true;
          else failures.push({ file: lock, error });
          continue;
        }
        let error;
        try {
          this.fs.writeSync(fd, JSON.stringify({ pid: process.pid, at: this.now() }));
        } catch (e) {
          error = e;
        } finally {
          this.fs.closeSync(fd);
        }
        if (error) {
          // Angelegt, aber leer (ENOSPC): nicht liegen lassen, andere hielten sie für verwaist.
          this.remove(lock);
          failures.push({ file: lock, error });
          continue;
        }
        // Genommen. Hat ein Fenster am anderen Ort in der Lücke zwischen Prüfen und Anlegen auch
        // eine genommen (nur, wenn Fenster an verschiedenen Orten sind)? Dann zurück und 'busy':
        // Von zweien, die einander sehen, geben beide nach und versuchen es nach LOCK_POLL_MS
        // neu; wer die andere nicht sah, war vor ihr da und behält seine.
        for (const [j, other] of places.entries()) {
          if (j !== i && this.lockInfo(lockPath(other)).status === 'busy') {
            this.remove(lock);
            return 'busy';
          }
        }
        this.lockFile = lock;
        this.moveTo(file, failures[0]);
        if (this._lockUnwritable) {
          this._lockUnwritable = false;
          this.log(`Sperrdatei ${lock} wieder anlegbar: zur selben Zeit probt nur ein Fenster`);
        }
        return 'got';
      }
      if (!raced) return this.lockFailed(failures);
    }
    return 'busy';
  }

  lockFailed(failures) {
    if (!this._lockUnwritable) {
      this._lockUnwritable = true;
      this.log(`Sperrdatei ${describe(failures)} nicht anlegbar: dieses Fenster probt ohne Sperre`);
    }
    return 'none';
  }

  // 'busy', 'stale' (älter als LOCK_STALE_MS oder unlesbar) oder 'gone' (keine).
  lockInfo(lock) {
    try {
      const { at } = JSON.parse(this.fs.readFileSync(lock, 'utf8'));
      return { status: Number.isFinite(at) && this.now() - at <= LOCK_STALE_MS ? 'busy' : 'stale' };
    } catch (e) {
      return { status: e && e.code === 'ENOENT' ? 'gone' : 'stale' };
    }
  }

  unlock() {
    this.remove(this.lockFile);
  }

  remove(file) {
    try {
      this.fs.unlinkSync(file);
    } catch {
      // schon weg, nie entstanden oder nicht löschbar
    }
  }
}

// Der jüngste der gelesenen Stände; keiner: leer.
function newest(reads) {
  let best;
  for (const s of reads) {
    if (s && (!best || s.writtenAt > best.writtenAt)) best = s;
  }
  return best || emptyState();
}

// „<pfad> (<fehler>) und Ersatzort <pfad> (<fehler>)“ für das Log.
function describe(failures) {
  return failures.map(({ file, error }, i) => `${i ? 'und Ersatzort ' : ''}${file} (${error.message})`).join(' ');
}

function defaultSleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

// Die Schranke eines Fensters über der Pausendatei. pairs sind je Anfrage die aktuellen Paare
// der Erweiterung: [{ hub, account, tokenId }] (tokenId: Größe und Zeit der Token-Datei, ohne
// Inhalt); address die Adresse des Nodes. probe(pairs) schickt das whoami mit genau diesen
// Paaren und beobachtet die Antwort selbst (observe). Ein Ergebnis ist eine Antwort, die observe
// gesehen hat (auch 401, hidden, invalid je Hub), oder ein whoami, das durchlief; wirft probe
// ohne das, war es eine Probe ohne Ergebnis — ensure wirft dann, die Schranke bleibt zu.
class Gate {
  constructor({ file, now = Date.now, sleep = defaultSleep, log = () => {}, onChange = () => {} }) {
    this.file = file;
    this.now = now;
    this.sleep = sleep;
    this.log = log;
    this.onChange = onChange;
    // probedSig: die Paare, mit denen zuletzt mit Ergebnis geprobt wurde (oder deren Pause
    // übernommen ist). attemptedSig: die Paare des letzten Versuchs, auch ohne Ergebnis — eine
    // Änderung seitdem probt sofort, am Abstand der Adresse vorbei.
    this.probedSig = undefined;
    this.attemptedSig = undefined;
    // force: Zutun des Nutzers — die nächste Probe nimmt alle Paare, ohne Rücksicht aufs Budget.
    this.force = false;
    this.inflight = undefined;
    // observed: observe hat während der laufenden Probe ein Ergebnis gesehen.
    this.observed = false;
    // lastUnanswered: { address, error } der letzten Probe ohne Ergebnis dieses Fensters.
    this.lastUnanswered = undefined;
    this._lastPaused = undefined;
  }

  // Nach Zutun des Nutzers (andere Wahl, Einstellung, „Neu verbinden“): sofort einmal proben,
  // zählt ins Budget, wartet nicht darauf.
  reset() {
    this.probedSig = undefined;
    this.force = true;
  }

  signature(address, pairs) {
    return JSON.stringify([address, pairs.map((p) => [p.hub, p.account, p.tokenId]).sort()]);
  }

  // Was die nächste Probe schickt: { sig, send, closed, restart }. send ist leer, wenn keine Probe
  // ansteht — nichts Neues, nichts fällig, oder das Budget erlaubt keine. Nicht pausierte Paare
  // gehen mit jeder Probe mit (so sagt die Antwort je Hub, was gilt); eine Probe steht an, wenn
  // die Paare neu oder geändert sind, eine Token-Datei eines pausierten Paars anders ist (sofort)
  // oder eine Pause abgelaufen ist und das Budget es erlaubt. closed: Eine Probe stünde an, aber
  // der Abstand nach einer Probe ohne Ergebnis läuft noch (der Eintrag der Adresse) — nichts geht
  // hinaus; nicht nach Zutun des Nutzers (force) und nicht, wenn sich die Paare seit dem letzten
  // Versuch dieses Fensters geändert haben (restart: der Abstand beginnt dann von vorn).
  plan(address, pairs) {
    const state = this.file.load();
    const now = this.now();
    const sig = this.signature(address, pairs);
    const fresh = this.force || sig !== this.probedSig;
    const budgetOk = this.force || budgetTimes(state, address, now).length < RESERVE;
    const send = [];
    let needed = false;
    for (const p of pairs) {
      const e = state.pairs[pairKey(address, p.hub, p.account)];
      if (!e) {
        send.push(p);
        if (fresh) needed = true;
      } else if (this.force || e.token !== p.tokenId) {
        send.push(p);
        needed = true;
      } else if (e.nextProbe <= now && budgetOk) {
        send.push(p);
        needed = true;
      }
    }
    if (!needed) return { sig, send: [] };
    const restart = this.force || (this.attemptedSig !== undefined && sig !== this.attemptedSig);
    const u = state.unanswered[address];
    if (u && u.nextProbe > now && !restart) return { sig, send: [], closed: u };
    return { sig, send, restart };
  }

  // Vor einer Anfrage: proben, wenn eine Probe ansteht — eine für alle, die gleichzeitig warten.
  // Unter der Sperrdatei wird der Plan neu gemacht: Ein anderes Fenster war vielleicht schneller,
  // dann gilt dessen Ergebnis aus der Datei. Wirft, solange die Schranke nach einer Probe ohne
  // Ergebnis zu ist (closedError): Die Anfrage geht dann nicht hinaus; die Kennung „geprobt“
  // (probedSig) bekommt nur eine Probe mit Ergebnis.
  async ensure(address, pairs, probe) {
    if (this.inflight) return this.inflight;
    const first = this.plan(address, pairs);
    if (first.closed) throw this.closedError(address, first.closed);
    if (!first.send.length) {
      this.probedSig = first.sig;
      this.force = false;
      return undefined;
    }
    this.inflight = (async () => {
      try {
        await this.withLock(async () => {
          const again = this.plan(address, pairs);
          if (again.closed) throw this.closedError(address, again.closed);
          if (again.send.length) await this.attempt(address, again, probe);
        });
        this.probedSig = first.sig;
      } finally {
        this.force = false;
        this.inflight = undefined;
      }
    })();
    return this.inflight;
  }

  // Die Probe selbst, unter der Sperrdatei. Ohne Ergebnis — probe wirft, und observe hat nichts
  // gesehen: keine Verbindung, 5xx des Proxys, Abbruch, Zeitüberschreitung — bekommt die Adresse
  // den nächsten Abstand (RETRY_S) in der Pausendatei, von vorn nach Zutun des Nutzers oder
  // geänderten Paaren (restart), und ensure wirft. Ein Ergebnis beendet den Abstand.
  async attempt(address, { sig, send, restart }, probe) {
    this.attemptedSig = sig;
    this.observed = false;
    let error;
    try {
      await probe(send);
    } catch (e) {
      if (!this.observed) error = e instanceof Error ? e : new Error(String(e));
    }
    if (!error) {
      this.lastUnanswered = undefined;
      this.file.update((state) => clearUnanswered(state, address));
      return;
    }
    const now = this.now();
    const u = this.file.update((state) => {
      const prev = restart ? undefined : state.unanswered[address];
      const count = (prev ? prev.count : 0) + 1;
      state.unanswered[address] = { count, at: now, nextProbe: now + retryAfter(count) };
      return true;
    }).unanswered[address];
    this.lastUnanswered = { address, error };
    this.log(`Probe an ${address} ohne Ergebnis (${error.message}), ${u.count}. Mal; nächste `
      + `${inTime(u.nextProbe - now)}, bis dahin nichts gefragt`);
    throw this.closedError(address, u);
  }

  // Der Fehler, mit dem Anfragen scheitern, solange die Schranke nach einer Probe ohne Ergebnis
  // zu ist: die Ursache, wenn dieses Fenster sie kennt, sonst der Hinweis auf ein anderes Fenster;
  // kind wie die Ursache (die Erweiterung macht daraus Unavailable), sonst unreachable.
  closedError(address, u) {
    const own = this.lastUnanswered && this.lastUnanswered.address === address ? this.lastUnanswered.error : undefined;
    const why = own ? own.message : `Probe an ${address} in einem anderen Fenster ohne Ergebnis`;
    const err = new Error(`${why}; nächste Probe ${inTime(u.nextProbe - this.now())}, bis dahin nichts gefragt`);
    err.kind = own && own.kind ? own.kind : 'unreachable';
    err.unanswered = u.count;
    return err;
  }

  // fn unter der Sperrdatei; wartet auf ein anderes Fenster, höchstens LOCK_STALE_MS (dann ist
  // die Sperre verwaist und wird ersetzt). Ohne Sperrdatei (nicht anlegbar) trotzdem.
  async withLock(fn) {
    let held = false;
    for (let i = 0; i < LOCK_STALE_MS / LOCK_POLL_MS + 1; i++) {
      const r = this.file.tryLock();
      if (r !== 'busy') {
        held = r === 'got';
        break;
      }
      await this.sleep(LOCK_POLL_MS);
    }
    try {
      return await fn();
    } finally {
      if (held) this.file.unlock();
    }
  }

  // Was eine Antwort über die geschickten Paare sagt: outcome.invalid sind Hubs mit ungültigem
  // Paar oder 'all' (verdeckte Antwort, 401 des Proxys: alle geschickten), outcome.ok Hubs mit
  // gültigem. Ein ungültiges Paar wird pausiert (wachsende Pause), ein gültiges verliert seine
  // Pause; jede Anfrage mit ungültigem Paar zählt einmal ins Budget.
  observe(address, sent, outcome) {
    if (!sent || !sent.length || !outcome) return;
    const invalid = outcome.invalid === 'all' ? new Set(sent.map((p) => p.hub)) : new Set(outcome.invalid || []);
    const ok = new Set(outcome.ok || []);
    if (!invalid.size && !ok.size) return;
    const now = this.now();
    this.file.update((state) => {
      let changed = clearUnanswered(state, address);
      for (const p of sent) {
        const key = pairKey(address, p.hub, p.account);
        if (invalid.has(p.hub)) {
          const prev = state.pairs[key];
          const failures = (prev && prev.token === p.tokenId ? prev.failures : 0) + 1;
          state.pairs[key] = { address, hub: p.hub, account: p.account, token: p.tokenId, failures, failedAt: now,
            nextProbe: now + pauseAfter(failures) };
          changed = true;
        } else if (ok.has(p.hub) && state.pairs[key]) {
          delete state.pairs[key];
          changed = true;
        }
      }
      if (invalid.size) {
        (state.budget[address] = state.budget[address] || []).push(now);
        changed = true;
      }
      return changed;
    });
    // Ein Ergebnis — auch für eine laufende Probe, die danach wirft (401 des Proxys). Erst nach
    // dem Vermerk: Was davor scheitert, ist kein Ergebnis (die Pausendatei wirft nicht; ein
    // Fehler hier wäre einer des Moduls, und die Probe gälte als ohne Ergebnis).
    this.observed = true;
    // Den neuen Stand nennt paused() beim nächsten Aufruf mit allen Paaren (Statusleiste).
    this.onChange();
  }

  // Die pausierten Paare unter pairs: { hub: { account, failures, nextProbe } }; nextProbe
  // berücksichtigt das Budget. Ein Paar, dessen Token-Datei sich geändert hat, ist nicht
  // pausiert. Ein Wechsel des Stands kommt ins Log und löst onChange aus — nicht jede Anfrage.
  paused(address, pairs) {
    const state = this.file.load();
    const now = this.now();
    const free = budgetFreeAt(state, address, now);
    const out = {};
    for (const p of pairs) {
      const e = state.pairs[pairKey(address, p.hub, p.account)];
      if (e && e.token === p.tokenId) {
        out[p.hub] = { account: p.account, failures: e.failures, nextProbe: Math.max(e.nextProbe, free) };
      }
    }
    const key = JSON.stringify([address, Object.entries(out).sort()]);
    if (key !== this._lastPaused) {
      const before = this._lastPaused ? JSON.parse(this._lastPaused) : undefined;
      this._lastPaused = key;
      for (const [hub, p] of Object.entries(out)) {
        this.log(`Hub ${hub}: Paar ${p.account} pausiert — Anmeldung ungültig (${p.failures}. Fehlversuch), nächste Probe `
          + `${inTime(p.nextProbe - now)}; Budget der Adresse ${budgetTimes(state, address, now).length} in 10 min`);
      }
      if (before && before[0] === address) {
        for (const [hub] of before[1]) {
          if (!out[hub]) this.log(`Hub ${hub}: Pause aufgehoben`);
        }
      }
      this.onChange();
    }
    return out;
  }

  // Die Paare ohne die pausierten.
  sendable(address, pairs) {
    const paused = this.paused(address, pairs);
    return pairs.filter((p) => !paused[p.hub]);
  }

  // Für die Anzeige: die Fehlversuche der Adresse im Fenster.
  budget(address) {
    return budgetTimes(this.file.load(), address, this.now()).length;
  }
}

// „in 3 min“, „in 20 s“, „jetzt“ — ab 59,5 s in Minuten, gerundet.
function inTime(ms) {
  if (ms <= 0) return 'jetzt';
  if (ms < 59.5 * 1000) return `in ${Math.ceil(ms / 1000)} s`;
  return `in ${Math.max(1, Math.round(ms / 60000))} min`;
}

// Der Grund in der Statusleiste für ein pausiertes Paar (rules.hubStatus): nächste Probe, Zahl
// der Fehlversuche, Schwelle der Jail, Abhilfe.
function pauseWarning(p, now) {
  const n = p.failures;
  return `pausiert — Anmeldung ungültig (${n} Fehlversuch${n === 1 ? '' : 'e'}), nächste Probe ${inTime(p.nextProbe - now)}; `
    + `die Jail des Proxys sperrt die Adresse nach ${JAIL_LIMIT} in 10 min (nach einer früheren Sperre 5). Abhilfe: `
    + 'Token-Datei erneuern; nach rotate auf einem anderen Rechner erledigt es sich nach dem Abgleich des entfernten Nodes';
}

module.exports = {
  PauseFile, Gate, pauseFilePath, fallbackPauseFilePath, stateDir, pauseAfter, retryAfter, pairKey, pauseWarning, inTime,
  WINDOW_MS, JAIL_LIMIT, RESERVE, SCHEDULE_MIN, RETRY_S, LOCK_STALE_MS,
};
