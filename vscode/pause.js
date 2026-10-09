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

// Die Pause nach dem n-ten Fehlversuch eines Paars, in ms.
function pauseAfter(failures) {
  const i = Math.min(Math.max(failures, 1), SCHEDULE_MIN.length) - 1;
  return SCHEDULE_MIN[i] * 60 * 1000;
}

function pairKey(address, hub, account) {
  return `${address}|${hub}|${account}`;
}

function emptyState() {
  return { version: FILE_VERSION, pairs: {}, budget: {} };
}

// Eine gelesene Datei in die Form bringen; alles Unbrauchbare fällt weg.
function normalize(raw) {
  const state = emptyState();
  if (!raw || typeof raw !== 'object' || raw.version !== FILE_VERSION) return state;
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

// Die Pausendatei: lesen, wenn sie sich geändert hat; Lesen-Ändern-Schreiben atomar; die
// Sperrdatei daneben. fsImpl und now sind ersetzbar.
class PauseFile {
  constructor(file, { now = Date.now, fsImpl = fs } = {}) {
    this.file = file;
    this.lockFile = `${file}.lock`;
    this.now = now;
    this.fs = fsImpl;
    this._stamp = undefined;
    this._state = emptyState();
  }

  ensureDir() {
    try {
      this.fs.mkdirSync(path.dirname(this.file), { recursive: true, mode: 0o700 });
    } catch {
      // Ein Fehler zeigt sich beim Schreiben.
    }
  }

  // Der Stand aus der Datei, neu gelesen nur, wenn Inode, Größe oder Zeit anders sind. Fehlt
  // sie oder ist sie kaputt: leer — Schranke und Sperrdatei gelten trotzdem.
  load() {
    let st;
    try {
      st = this.fs.statSync(this.file);
    } catch {
      this._stamp = undefined;
      this._state = emptyState();
      return this._state;
    }
    const stamp = `${st.ino}:${st.size}:${st.mtimeMs}`;
    if (stamp === this._stamp) return this._state;
    try {
      this._state = normalize(JSON.parse(this.fs.readFileSync(this.file, 'utf8')));
    } catch {
      this._state = emptyState();
    }
    this._stamp = stamp;
    return this._state;
  }

  // Lesen-Ändern-Schreiben: fn ändert den frisch gelesenen Stand und sagt, ob sich etwas
  // geändert hat; nur dann wird bereinigt und atomar geschrieben. Liefert den Stand.
  update(fn) {
    this._stamp = undefined;
    const state = this.load();
    const changed = fn(state);
    if (changed) {
      prune(state, this.now());
      this.write(state);
    }
    return state;
  }

  // Atomar: neue Datei, dann rename; 0600 — auch wenn kein Token darin steht.
  write(state) {
    this.ensureDir();
    const tmp = `${this.file}.${process.pid}.${Math.random().toString(36).slice(2, 10)}.tmp`;
    this.fs.writeFileSync(tmp, JSON.stringify(state), { mode: 0o600 });
    this.fs.renameSync(tmp, this.file);
    this._stamp = undefined;
    this._state = state;
  }

  // Die Sperrdatei nehmen: 'got' (genommen), 'busy' (ein anderes Fenster probt), 'none' (lässt
  // sich nicht anlegen — dann ohne Sperre weiter). Eine verwaiste wird ersetzt.
  tryLock() {
    this.ensureDir();
    for (let attempt = 0; attempt < 3; attempt++) {
      let fd;
      try {
        fd = this.fs.openSync(this.lockFile, 'wx', 0o600);
      } catch (e) {
        if (e.code !== 'EEXIST') return 'none';
        const stale = this.lockStale();
        if (stale === 'busy') return 'busy';
        if (stale === 'stale') {
          try {
            this.fs.unlinkSync(this.lockFile);
          } catch {
            // schon weg
          }
        }
        continue; // 'gone' oder 'stale': noch einmal anlegen
      }
      try {
        this.fs.writeSync(fd, JSON.stringify({ pid: process.pid, at: this.now() }));
      } finally {
        this.fs.closeSync(fd);
      }
      return 'got';
    }
    return 'busy';
  }

  // 'busy', 'stale' (älter als LOCK_STALE_MS oder unlesbar) oder 'gone' (gerade freigegeben).
  lockStale() {
    try {
      const { at } = JSON.parse(this.fs.readFileSync(this.lockFile, 'utf8'));
      return Number.isFinite(at) && this.now() - at <= LOCK_STALE_MS ? 'busy' : 'stale';
    } catch (e) {
      return e && e.code === 'ENOENT' ? 'gone' : 'stale';
    }
  }

  unlock() {
    try {
      this.fs.unlinkSync(this.lockFile);
    } catch {
      // schon weg
    }
  }
}

function defaultSleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

// Die Schranke eines Fensters über der Pausendatei. pairs sind je Anfrage die aktuellen Paare
// der Erweiterung: [{ hub, account, tokenId }] (tokenId: Größe und Zeit der Token-Datei, ohne
// Inhalt); address die Adresse des Nodes. probe(pairs) schickt das whoami mit genau diesen
// Paaren und beobachtet die Antwort selbst (observe); hier zählt nur, dass es lief.
class Gate {
  constructor({ file, now = Date.now, sleep = defaultSleep, log = () => {}, onChange = () => {} }) {
    this.file = file;
    this.now = now;
    this.sleep = sleep;
    this.log = log;
    this.onChange = onChange;
    // probedSig: die Paare, mit denen zuletzt geprobt wurde (oder deren Pause übernommen ist).
    this.probedSig = undefined;
    // force: Zutun des Nutzers — die nächste Probe nimmt alle Paare, ohne Rücksicht aufs Budget.
    this.force = false;
    this.inflight = undefined;
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

  // Was die nächste Probe schickt: { sig, send }. send ist leer, wenn keine Probe ansteht —
  // nichts Neues, nichts fällig, oder das Budget erlaubt keine. Nicht pausierte Paare gehen mit
  // jeder Probe mit (so sagt die Antwort je Hub, was gilt); eine Probe steht an, wenn die Paare
  // neu oder geändert sind, eine Token-Datei eines pausierten Paars anders ist (sofort) oder eine
  // Pause abgelaufen ist und das Budget es erlaubt.
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
    return { sig, send: needed ? send : [] };
  }

  // Vor einer Anfrage: proben, wenn eine Probe ansteht — eine für alle, die gleichzeitig warten.
  // Unter der Sperrdatei wird der Plan neu gemacht: Ein anderes Fenster war vielleicht schneller,
  // dann gilt dessen Ergebnis aus der Datei.
  async ensure(address, pairs, probe) {
    if (this.inflight) return this.inflight;
    const { sig, send } = this.plan(address, pairs);
    if (!send.length) {
      this.probedSig = sig;
      this.force = false;
      return undefined;
    }
    this.inflight = (async () => {
      try {
        await this.withLock(async () => {
          const again = this.plan(address, pairs);
          if (!again.send.length) return;
          try {
            await probe(again.send);
          } catch {
            // Die Antwort hat der Aufrufer beobachtet; ein Fehler der Verbindung ändert nichts.
          }
        });
      } finally {
        this.probedSig = sig;
        this.force = false;
        this.inflight = undefined;
      }
    })();
    return this.inflight;
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
      let changed = false;
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
  PauseFile, Gate, pauseFilePath, stateDir, pauseAfter, pairKey, pauseWarning, inTime,
  WINDOW_MS, JAIL_LIMIT, RESERVE, SCHEDULE_MIN, LOCK_STALE_MS,
};
