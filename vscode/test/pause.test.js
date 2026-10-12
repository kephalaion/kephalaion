// Schranke, Pause und Budget (vscode/pause.js, Task 025) ohne vscode: node --test vscode/test/*.test.js
// (make vscode-test). Die Uhr stellen die Tests; zwei Instanzen an derselben Datei sind zwei
// Fenster; ein Ersatz des Nodes zählt die Anfragen mit ungültigem Paar — die Fehlversuche, wie die
// Jail des Proxys sie sähe. Dummy-Tokens; kein Netz.

'use strict';

const test = require('node:test');
const assert = require('node:assert');
const crypto = require('crypto');
const fs = require('fs');
const os = require('os');
const path = require('path');
const { PauseFile, Gate, pauseAfter, retryAfter, WINDOW_MS, RESERVE, LOCK_STALE_MS } = require('../pause');
const { whoamiOutcome } = require('../rules');

const TOKEN = `keph_${'A'.repeat(43)}`;
const ADDRESS = 'https://node.example.org/kephalaion/mcp';
const MIN = 60 * 1000;
const SEC = 1000;

// Eine Uhr, die die Tests stellen.
function clock(start = 1_700_000_000_000) {
  let t = start;
  const now = () => t;
  now.advance = (ms) => { t += ms; };
  return now;
}

// Warten ohne Zeit: nur die Ereignisschleife weiterlassen (die Sperrdatei eines anderen Fensters).
const yieldSleep = () => new Promise((resolve) => setImmediate(resolve));

const dirs = [];
function tmpFile() {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'keph-pause-'));
  dirs.push(dir);
  return path.join(dir, 'kephalaion', 'vscode-pause.json');
}
test.after(() => { for (const d of dirs) fs.rmSync(d, { recursive: true, force: true }); });

const pair = (hub, account = 'kamran', tokenId = '48:1700000000000') => ({ hub, account, tokenId });

// Ein Dateisystem, dem sich das Schreibrecht nehmen lässt wie einem Verzeichnis 0500 (readonly:
// EACCES beim Anlegen und Umbenennen) oder einer vollen Platte (full: ENOSPC beim Schreiben, die
// Datei entsteht leer, wie wirklich) — ohne chmod, das unter root nichts verböte. Alles andere ist
// das echte Dateisystem.
function brittleFs() {
  const err = (code, what) => Object.assign(new Error(`${code}: ${what}`), { code });
  const impl = Object.create(fs);
  impl.readonly = false;
  impl.full = false;
  impl.writeFileSync = (file, ...rest) => {
    if (impl.readonly) throw err('EACCES', `permission denied, open '${file}'`);
    if (impl.full) {
      fs.writeFileSync(file, '');
      throw err('ENOSPC', `no space left on device, write '${file}'`);
    }
    return fs.writeFileSync(file, ...rest);
  };
  impl.renameSync = (from, to) => {
    if (impl.readonly) throw err('EACCES', `permission denied, rename '${from}' -> '${to}'`);
    return fs.renameSync(from, to);
  };
  impl.openSync = (file, flags, ...rest) => {
    if (impl.readonly && flags === 'wx') throw err('EACCES', `permission denied, open '${file}'`);
    return fs.openSync(file, flags, ...rest);
  };
  impl.writeSync = (fd, ...rest) => {
    if (impl.full) throw err('ENOSPC', 'no space left on device, write');
    return fs.writeSync(fd, ...rest);
  };
  return impl;
}

// Der Ersatz des Nodes über den Proxy: je Hub, ob das Paar gilt. Jede Anfrage mit mindestens
// einem ungültigen Paar ist ein Fehlversuch (ein Fehlversuch ist eine Anfrage, gleich wie viele
// Paare ungültig sind); alle ungültig heißt verdeckt. down: keine Verbindung (nichts kommt an);
// status: der Proxy antwortet selbst — 401 zählt an seiner Jail, 5xx nicht (der Node liegt).
// attempts zählt jede Anfrage, die ein Fenster abschickt, requests nur die, die ankommen.
class FakeNode {
  constructor(valid) {
    this.valid = { ...valid };
    this.failures = [];
    this.requests = 0;
    this.attempts = 0;
    this.attemptsAt = [];
    this.down = false;
    this.status = 0;
  }

  answer(sent, now) {
    this.attempts++;
    this.attemptsAt.push(now);
    if (this.down) throw Object.assign(new Error(`Node ${ADDRESS} nicht erreichbar (ECONNREFUSED)`), { kind: 'unreachable' });
    this.requests++;
    if (this.status === 401) this.failures.push(now);
    if (this.status) return { status: this.status };
    const bad = sent.filter((p) => !this.valid[p.hub]);
    if (bad.length) this.failures.push(now);
    if (bad.length === sent.length) return { hidden: true, hubs: [] };
    return { hubs: sent.map((p) => ({ hub: p.hub, login: this.valid[p.hub] ? 'ok' : 'invalid' })) };
  }

  // Die meisten Fehlversuche in einem Fenster von 10 min — was die Jail höchstens zählte.
  worstWindow() {
    let worst = 0;
    for (const t of this.failures) {
      worst = Math.max(worst, this.failures.filter((u) => u >= t && u - t < WINDOW_MS).length);
    }
    return worst;
  }
}

// Ein Fenster: die Schranke über der gemeinsamen Datei und die Anfragen der Erweiterung — wie
// Node.request in extension.js: erst die Probe, dann ohne pausierte Paare; die Antwort wird
// beobachtet (rules.whoamiOutcome).
class Window {
  constructor(file, now, node, pairs, log = () => {}, fsImpl = fs) {
    this.node = node;
    this.now = now;
    this.pairs = pairs;
    this.gate = new Gate({ file: new PauseFile(file, { now, fsImpl, log }), now, sleep: yieldSleep, log });
  }

  async request() {
    await this.gate.ensure(ADDRESS, this.pairs, (send) => this.send(send));
    const send = this.gate.sendable(ADDRESS, this.pairs);
    if (!send.length) return 'paused';
    return this.send(send);
  }

  // Die Anfrage geht hinaus (der Node zählt sie), die Antwort kommt einen Tick später — so sind
  // zwei Fenster zugleich wirklich zwei Anfragen unterwegs. Wie Node.request in extension.js:
  // keine Verbindung wirft (unreachable), eine 401 des Proxys ist ein Ergebnis für alle
  // geschickten Paare und wirft danach (rejected), 5xx wirft ohne Ergebnis (unclear).
  async send(send) {
    const who = this.node.answer(send, this.now());
    await yieldSleep();
    if (who.status === 401) {
      this.gate.observe(ADDRESS, send, { invalid: 'all' });
      throw Object.assign(new Error(`${ADDRESS}: eine Anmeldung des Proxys, nicht der Node (HTTP 401)`), { kind: 'rejected' });
    }
    if (who.status) {
      throw Object.assign(new Error(`${ADDRESS}: Proxy antwortet, aber der Node dahinter nicht (HTTP ${who.status})`), { kind: 'unclear' });
    }
    this.gate.observe(ADDRESS, send, whoamiOutcome(who, send.map((p) => p.hub)));
    return who;
  }

  // Wie request, aber ein Fehler kommt als Wert zurück (die Erweiterung fängt ihn je Aufruf).
  async tryRequest() {
    return this.request().catch((e) => e);
  }

  paused() {
    return this.gate.paused(ADDRESS, this.pairs);
  }
}

// Lässt Fenster über eine Spanne laufen wie die Erweiterung: alle 3 s eine Anfrage je Fenster
// (changes), alle 30 s eine zweite (whoami); together: beide Fenster zugleich, ohne aufeinander
// zu warten — Anfragen, die schon unterwegs sind.
async function run(windows, now, ms, { together = false } = {}) {
  for (let t = 0; t < ms; t += 3 * SEC) {
    for (let i = 0; i < (t % (30 * SEC) === 0 ? 2 : 1); i++) {
      if (together) await Promise.all(windows.map((w) => w.tryRequest()));
      else for (const w of windows) await w.tryRequest();
    }
    now.advance(3 * SEC);
  }
}

test('Pause: 1, 2, 4, 8, 16, 30 min, danach 30', () => {
  assert.deepStrictEqual([1, 2, 3, 4, 5, 6, 7, 20].map((n) => pauseAfter(n) / MIN), [1, 2, 4, 8, 16, 30, 30, 30]);
  assert.strictEqual(pauseAfter(0), MIN);
});

test('Schranke: Start mit ungültigem Paar — genau eine Anfrage, danach nichts bis zur Probe', async () => {
  const now = clock();
  const node = new FakeNode({ vm: false });
  const w = new Window(tmpFile(), now, node, [pair('vm')]);
  // Fünf Anfragen zugleich (whoami, changes, stat, readDirectory, readFile beim Start): eine Probe.
  const results = await Promise.all([1, 2, 3, 4, 5].map(() => w.request()));
  assert.strictEqual(node.requests, 1, 'mehr als eine Anfrage mit dem Paar');
  assert.deepStrictEqual(results, ['paused', 'paused', 'paused', 'paused', 'paused']);
  const p = w.paused();
  assert.strictEqual(p.vm.failures, 1);
  assert.strictEqual(p.vm.nextProbe, now() + MIN);
  // In der nächsten Minute nichts; nach 1 min die zweite Probe; danach erst, wenn das Budget
  // wieder unter 2 liegt — nach 10 min.
  now.advance(59 * SEC);
  await w.request();
  assert.strictEqual(node.requests, 1);
  now.advance(1 * SEC);
  await w.request();
  assert.strictEqual(node.requests, 2);
  assert.strictEqual(w.paused().vm.failures, 2);
  now.advance(5 * MIN);
  await w.request();
  assert.strictEqual(node.requests, 2, 'Probe trotz vollem Budget');
  now.advance(4 * MIN);
  await w.request();
  assert.strictEqual(node.requests, 3);
});

test('Zwei Fenster zugleich: eine Probe, nicht zwei; das zweite übernimmt die Pause', async () => {
  const now = clock();
  const node = new FakeNode({ vm: false });
  const file = tmpFile();
  const logs = [];
  const a = new Window(file, now, node, [pair('vm')], (l) => logs.push(`a ${l}`));
  const b = new Window(file, now, node, [pair('vm')], (l) => logs.push(`b ${l}`));
  await Promise.all([a.request(), b.request(), a.request(), b.request()]);
  assert.strictEqual(node.requests, 1, 'zwei Fenster, zwei Proben');
  assert.strictEqual(b.paused().vm.failures, 1, 'das zweite Fenster kennt die Pause nicht');
  assert.ok(!fs.existsSync(`${file}.lock`), 'Sperrdatei bleibt liegen');
  // Ein drittes Fenster, das später startet: keine Probe, übernimmt aus der Datei.
  const c = new Window(file, now, node, [pair('vm')]);
  assert.strictEqual(await c.request(), 'paused');
  assert.strictEqual(node.requests, 1);
  assert.ok(logs.some((l) => l.includes('pausiert') && l.includes('Fehlversuch')), logs.join('\n'));
  assert.ok(!logs.join('\n').includes(TOKEN));
});

test('Zeitfolge: beim Start mit zwei Fenstern höchstens 2 Fehlversuche in 10 min, Proben nach Plan', async () => {
  const now = clock();
  const node = new FakeNode({ vm: false });
  const file = tmpFile();
  const windows = [new Window(file, now, node, [pair('vm')]), new Window(file, now, node, [pair('vm')])];
  const start = now();
  await run(windows, now, 25 * MIN);
  const at = node.failures.map((t) => (t - start) / MIN);
  // t = 0 und 1 (Pause 1 min), die Pause von 2 min wäre bei 3 um, das Budget erlaubt es erst
  // bei 10; dann 4 min Pause → 14, 8 min → 22 (das Budget hat dann je 1).
  assert.deepStrictEqual(at.slice(0, 5), [0, 1, 10, 14, 22], at.join(', '));
  assert.ok(node.worstWindow() <= RESERVE, `${node.worstWindow()} Fehlversuche in 10 min: ${at.join(', ')}`);
  // Das Token wird gültig (rotate ist abgeglichen): Die nächste Probe hebt die Pause auf, beide
  // Fenster arbeiten wieder; die Datei ist bereinigt.
  node.valid.vm = true;
  const before = node.requests;
  await run(windows, now, 31 * MIN);
  assert.ok(node.requests > before + 100, 'die Fenster arbeiten nicht weiter');
  assert.deepStrictEqual(windows.map((w) => Object.keys(w.paused())), [[], []]);
  assert.deepStrictEqual(JSON.parse(fs.readFileSync(file, 'utf8')).pairs, {});
});

test('Zeitfolge: Übergang gültig → ungültig im Betrieb mit zwei Anfragen unterwegs — höchstens 4 in 10 min', async () => {
  const now = clock();
  const node = new FakeNode({ vm: true });
  const file = tmpFile();
  const windows = [new Window(file, now, node, [pair('vm')]), new Window(file, now, node, [pair('vm')])];
  await run(windows, now, 2 * MIN, { together: true });
  assert.strictEqual(node.failures.length, 0);
  const flip = now();
  node.valid.vm = false;
  await run(windows, now, 20 * MIN, { together: true });
  const at = node.failures.map((t) => (t - flip) / MIN);
  // Der Übergang: beide Fenster hatten die Anfrage schon abgeschickt — 2. Danach zählt das
  // Budget: erst nach 10 min wieder eine Probe.
  assert.deepStrictEqual(at.slice(0, 3), [0, 0, 10], at.join(', '));
  assert.ok(node.worstWindow() <= 4, `${node.worstWindow()} Fehlversuche in 10 min: ${at.join(', ')}`);
});

test('Zeitfolge: Paar A seit 7 min pausiert, dann Paar B im Betrieb ungültig — höchstens 4 in 10 min', async () => {
  const now = clock();
  const node = new FakeNode({ vm: false, eigen: true });
  const file = tmpFile();
  const pairs = [pair('vm'), pair('eigen', 'kp')];
  const windows = [new Window(file, now, node, pairs), new Window(file, now, node, pairs)];
  const start = now();
  await run(windows, now, 7 * MIN, { together: true });
  // Nur das gültige Paar geht mit; A probt bei 0 und 1.
  assert.deepStrictEqual(node.failures.map((t) => (t - start) / MIN), [0, 1]);
  assert.ok(node.requests > 200, 'das gültige Paar arbeitet nicht weiter');
  node.valid.eigen = false;
  await run(windows, now, 20 * MIN, { together: true });
  const at = node.failures.map((t) => (t - start) / MIN);
  assert.deepStrictEqual(at.slice(0, 4), [0, 1, 7, 7], at.join(', '));
  assert.ok(node.worstWindow() <= 4, `${node.worstWindow()} Fehlversuche in 10 min: ${at.join(', ')}`);
  // Nach dem Übergang geht an die Adresse nichts außer Proben.
  const probes = node.requests;
  await run(windows, now, 3 * MIN, { together: true });
  assert.ok(node.requests - probes <= 2, `${node.requests - probes} Anfragen ohne gültiges Paar`);
});

test('Geänderte Token-Datei: sofort eine Probe; gültig → Pause weg, Datei bereinigt, Budget bleibt 10 min', async () => {
  const now = clock();
  const node = new FakeNode({ vm: false });
  const file = tmpFile();
  const w = new Window(file, now, node, [pair('vm')]);
  await w.request();
  now.advance(MIN);
  await w.request();
  assert.strictEqual(node.requests, 2);
  // Budget 2: Ohne Zutun keine Probe. Die Token-Datei ändert sich: sofort, zählt ins Budget.
  now.advance(10 * SEC);
  w.pairs = [pair('vm', 'kamran', '48:1700000099000')];
  await w.request();
  assert.strictEqual(node.requests, 3, 'keine Probe nach geänderter Token-Datei');
  assert.strictEqual(w.paused().vm.failures, 1, 'die Zählung des alten Tokens gilt weiter');
  assert.strictEqual(w.gate.budget(ADDRESS), 3);
  // Das neue Token gilt; eine andere Wahl (reset) probt sofort: Pause weg, Datei ohne das Paar.
  node.valid.vm = true;
  w.gate.reset();
  assert.ok(await w.request() !== 'paused');
  assert.deepStrictEqual(w.paused(), {});
  const state = JSON.parse(fs.readFileSync(file, 'utf8'));
  assert.deepStrictEqual(state.pairs, {});
  assert.strictEqual(state.budget[ADDRESS].length, 3, 'die Zeiten im Budget bleiben');
  now.advance(10 * MIN);
  assert.strictEqual(w.gate.budget(ADDRESS), 0);
});

test('Erkennen: 401 des Proxys pausiert alle Paare der Adresse; hidden alle geschickten; login je Hub', async () => {
  const now = clock();
  const file = tmpFile();
  const gate = new Gate({ file: new PauseFile(file, { now }), now, sleep: yieldSleep });
  const pairs = [pair('vm'), pair('eigen', 'kp')];
  gate.observe(ADDRESS, pairs, { invalid: 'all' });
  assert.deepStrictEqual(Object.keys(gate.paused(ADDRESS, pairs)).sort(), ['eigen', 'vm']);
  assert.strictEqual(gate.budget(ADDRESS), 1, 'ein Fehlversuch ist eine Anfrage');
  // Die Probe klärt: vm gilt, eigen nicht.
  gate.observe(ADDRESS, pairs, whoamiOutcome({ hubs: [{ hub: 'vm', login: 'ok' }, { hub: 'eigen', login: 'invalid' }] },
    ['vm', 'eigen']));
  assert.deepStrictEqual(Object.keys(gate.paused(ADDRESS, pairs)), ['eigen']);
  assert.strictEqual(gate.paused(ADDRESS, pairs).eigen.failures, 2);
  // Nur die geschickten zählen: eine Antwort ohne eigen sagt nichts über eigen.
  gate.observe(ADDRESS, [pair('vm')], whoamiOutcome({ hubs: [{ hub: 'vm', login: 'ok' }, { hub: 'eigen', login: 'ok' }] }, ['vm']));
  assert.deepStrictEqual(Object.keys(gate.paused(ADDRESS, pairs)), ['eigen']);
  // Ohne Ergebnis (Fehler der Verbindung) ändert sich nichts.
  gate.observe(ADDRESS, pairs, { invalid: [], ok: [] });
  assert.strictEqual(gate.budget(ADDRESS), 2);
});

test('Kein Token und kein Hash in der Datei; atomar geschrieben, 0600', async () => {
  const now = clock();
  const node = new FakeNode({ vm: false });
  const file = tmpFile();
  const w = new Window(file, now, node, [pair('vm')]);
  await w.request();
  const text = fs.readFileSync(file, 'utf8');
  assert.ok(!text.includes(TOKEN));
  assert.ok(!text.includes(crypto.createHash('sha256').update(TOKEN).digest('hex')));
  assert.ok(text.includes('"token":"48:1700000000000"'), 'die Kennung der Token-Datei steht darin');
  assert.strictEqual(fs.statSync(file).mode & 0o777, 0o600);
  assert.deepStrictEqual(fs.readdirSync(path.dirname(file)), ['vscode-pause.json'], 'eine Zwischendatei bleibt liegen');
});

test('Kaputte Datei: leer gelesen, Schranke und Sperrdatei gelten trotzdem; verwaiste Sperre ersetzt', async () => {
  const now = clock();
  const file = tmpFile();
  fs.mkdirSync(path.dirname(file), { recursive: true });
  fs.writeFileSync(file, '{"version":1,"pairs":{"x":5},"budget":"nein"');
  const node = new FakeNode({ vm: false });
  const w = new Window(file, now, node, [pair('vm')]);
  await Promise.all([w.request(), w.request()]);
  assert.strictEqual(node.requests, 1);
  assert.strictEqual(w.paused().vm.failures, 1);
  assert.deepStrictEqual(JSON.parse(fs.readFileSync(file, 'utf8')).budget[ADDRESS].length, 1);
  // Eine verwaiste Sperrdatei (älter als LOCK_STALE_MS) hält niemanden auf.
  const pf = new PauseFile(file, { now });
  fs.writeFileSync(`${file}.lock`, JSON.stringify({ pid: 1, at: now() - LOCK_STALE_MS - 1 }));
  assert.strictEqual(pf.tryLock(), 'got');
  assert.strictEqual(pf.tryLock(), 'busy');
  pf.unlock();
  fs.writeFileSync(`${file}.lock`, 'Müll');
  assert.strictEqual(pf.tryLock(), 'got');
  pf.unlock();
  assert.ok(!fs.existsSync(`${file}.lock`));
});

test('Ein Stand je Adresse: eine andere nodeUrl hat eigene Pausen und eigenes Budget', async () => {
  const now = clock();
  const file = tmpFile();
  const gate = new Gate({ file: new PauseFile(file, { now }), now, sleep: yieldSleep });
  gate.observe(ADDRESS, [pair('vm')], { invalid: 'all' });
  assert.deepStrictEqual(gate.paused('https://other.example.org/kephalaion/mcp', [pair('vm')]), {});
  assert.strictEqual(gate.budget('https://other.example.org/kephalaion/mcp'), 0);
});

test('Abstand nach Proben ohne Ergebnis: 5, 10, 30, 60 s, danach 60', () => {
  assert.deepStrictEqual([1, 2, 3, 4, 5, 9].map((n) => retryAfter(n) / SEC), [5, 10, 30, 60, 60, 60]);
  assert.strictEqual(retryAfter(0), 5 * SEC);
});

// Der kritische Befund des Reviews (vscode/pause.js:313): Eine Probe, die an der Verbindung
// scheitert, galt als geprobt, und bis zur nächsten Änderung ging jede Anfrage mit allen Paaren
// hinaus — nach Rückkehr des Nodes mit ungültigem Paar changes alle 3 s. Jetzt: keine Kennung,
// nichts hinaus, die Anfragen scheitern mit der Ursache; neu geprobt mit wachsendem Abstand.
test('Probe ohne Ergebnis: Schranke bleibt zu, Abstand 5, 10, 30, 60 s; nach Rückkehr des Nodes erst die Probe', async () => {
  const now = clock();
  const node = new FakeNode({ vm: false });
  node.down = true;
  const file = tmpFile();
  const logs = [];
  const w = new Window(file, now, node, [pair('vm')], (l) => logs.push(l));
  // Fünf Anfragen zugleich (Start): eine Probe; alle scheitern mit der Ursache, nichts kam an.
  const results = await Promise.all([1, 2, 3, 4, 5].map(() => w.tryRequest()));
  assert.strictEqual(node.attempts, 1, 'mehr als eine Probe');
  for (const r of results) {
    assert.ok(r instanceof Error, 'eine Anfrage ging durch');
    assert.strictEqual(r.kind, 'unreachable');
    assert.match(r.message, /nicht erreichbar \(ECONNREFUSED\); nächste Probe in 5 s, bis dahin nichts gefragt/);
  }
  assert.strictEqual(w.gate.probedSig, undefined, 'als geprobt gekennzeichnet');
  assert.deepStrictEqual(w.paused(), {}, 'ohne Ergebnis pausiert');
  assert.strictEqual(w.gate.budget(ADDRESS), 0, 'ohne Ergebnis im Budget');
  assert.ok(logs.some((l) => l.includes('ohne Ergebnis') && l.includes('1. Mal')), logs.join('\n'));
  const state = JSON.parse(fs.readFileSync(file, 'utf8'));
  assert.strictEqual(state.unanswered[ADDRESS].count, 1);
  assert.strictEqual(state.unanswered[ADDRESS].nextProbe, now() + 5 * SEC);
  // Innerhalb des Abstands keine neue Probe; jede Anfrage scheitert ohne Versand.
  now.advance(4 * SEC);
  const e = await w.tryRequest();
  assert.strictEqual(node.attempts, 1, 'Probe innerhalb des Abstands');
  assert.match(e.message, /nächste Probe in 1 s/);
  // Nach Ablauf genau eine neue Probe; der Abstand wächst: 10, 30, 60, dann 60.
  for (const [wait, next] of [[1, 10], [10, 30], [30, 60], [60, 60], [60, 60]]) {
    now.advance(wait * SEC);
    const before = node.attempts;
    await Promise.all([w.tryRequest(), w.tryRequest()]);
    assert.strictEqual(node.attempts, before + 1, `nach ${wait} s: ${node.attempts - before} Proben`);
    assert.strictEqual(JSON.parse(fs.readFileSync(file, 'utf8')).unanswered[ADDRESS].nextProbe, now() + next * SEC);
    await w.tryRequest();
    assert.strictEqual(node.attempts, before + 1);
  }
  assert.strictEqual(node.requests, 0, 'etwas kam beim Node an');
  // Der Node ist zurück, das Paar ungültig: erst die Probe (ein Fehlversuch), dann nichts mehr —
  // nicht changes alle 3 s.
  node.down = false;
  now.advance(60 * SEC);
  await run([w], now, 50 * SEC);
  assert.strictEqual(node.requests, 1, `${node.requests} Anfragen mit ungültigem Paar`);
  assert.strictEqual(node.failures.length, 1);
  assert.strictEqual(w.paused().vm.failures, 1);
  assert.ok(w.gate.probedSig, 'nicht als geprobt gekennzeichnet');
  assert.strictEqual(JSON.parse(fs.readFileSync(file, 'utf8')).unanswered[ADDRESS], undefined, 'der Abstand bleibt stehen');
  assert.ok(!fs.readFileSync(file, 'utf8').includes(TOKEN));
});

test('Probe ohne Ergebnis: 502 des Proxys wie keine Verbindung — kein Fehlversuch, nichts pausiert; Erfolg öffnet und setzt den Abstand zurück', async () => {
  const now = clock();
  const node = new FakeNode({ vm: true });
  node.status = 502;
  const file = tmpFile();
  const w = new Window(file, now, node, [pair('vm')]);
  const e = await w.tryRequest();
  assert.strictEqual(e.kind, 'unclear');
  assert.match(e.message, /HTTP 502\); nächste Probe in 5 s/);
  assert.strictEqual(node.attempts, 1);
  assert.strictEqual(node.failures.length, 0, 'eine 502 zählte als Fehlversuch');
  assert.strictEqual(w.gate.budget(ADDRESS), 0);
  assert.deepStrictEqual(w.paused(), {});
  assert.strictEqual(w.gate.probedSig, undefined);
  await w.tryRequest();
  assert.strictEqual(node.attempts, 1, 'zweite Probe innerhalb des Abstands');
  now.advance(5 * SEC);
  await w.tryRequest();
  assert.strictEqual(node.attempts, 2);
  // Der Node antwortet wieder: Die Probe gilt, die Schranke ist offen, der Abstand ist weg.
  node.status = 0;
  now.advance(10 * SEC);
  assert.ok((await w.request()).hubs, 'die Anfrage ging nicht durch');
  await w.request();
  assert.strictEqual(node.requests, 5, 'zwei 502, dann Probe, Anfrage, Anfrage');
  assert.ok(w.gate.probedSig);
  assert.deepStrictEqual(JSON.parse(fs.readFileSync(file, 'utf8')).unanswered, {});
  // Zurückgesetzt: Nach einer späteren Probe ohne Ergebnis (geändertes Paar, Node liegt) beginnt
  // der Abstand wieder bei 5 s, nicht bei 10.
  node.status = 503;
  w.pairs = [pair('vm', 'kamran', '48:1700000099000')];
  assert.match((await w.tryRequest()).message, /nächste Probe in 5 s/);
});

test('Geändertes Paar oder Neu verbinden proben sofort, am Abstand vorbei, und beginnen ihn von vorn', async () => {
  const now = clock();
  const node = new FakeNode({ vm: true });
  node.down = true;
  const file = tmpFile();
  const w = new Window(file, now, node, [pair('vm')]);
  await w.tryRequest();
  now.advance(5 * SEC);
  assert.match((await w.tryRequest()).message, /nächste Probe in 10 s/);
  assert.strictEqual(node.attempts, 2);
  // Die Token-Datei ändert sich: sofort, und der nächste Abstand ist wieder 5 s.
  w.pairs = [pair('vm', 'kamran', '48:1700000099000')];
  assert.match((await w.tryRequest()).message, /nächste Probe in 5 s/);
  assert.strictEqual(node.attempts, 3, 'keine Probe nach geänderter Token-Datei');
  await w.tryRequest();
  assert.strictEqual(node.attempts, 3, 'dieselben Paare proben erneut');
  // „Neu verbinden“: sofort, und der Node ist wieder da — die Schranke öffnet.
  w.gate.reset();
  assert.match((await w.tryRequest()).message, /nächste Probe in 5 s/);
  assert.strictEqual(node.attempts, 4);
  node.down = false;
  w.gate.reset();
  assert.ok((await w.request()).hubs);
  assert.strictEqual(node.attempts, 6, 'Probe und Anfrage');
  assert.strictEqual(node.requests, 2);
  assert.deepStrictEqual(JSON.parse(fs.readFileSync(file, 'utf8')).unanswered, {});
});

test('401 des Proxys ist ein Ergebnis: Fehlversuch im Budget, alle Paare pausiert, kein Abstand', async () => {
  const now = clock();
  const node = new FakeNode({ vm: true, eigen: true });
  node.status = 401;
  const file = tmpFile();
  const w = new Window(file, now, node, [pair('vm'), pair('eigen', 'kp')]);
  assert.strictEqual(await w.request(), 'paused');
  assert.strictEqual(node.attempts, 1);
  assert.strictEqual(node.failures.length, 1);
  assert.strictEqual(w.gate.budget(ADDRESS), 1, 'die 401 fehlt im Budget');
  assert.deepStrictEqual(Object.keys(w.paused()).sort(), ['eigen', 'vm']);
  assert.ok(w.gate.probedSig, 'die 401 galt als Probe ohne Ergebnis');
  assert.deepStrictEqual(JSON.parse(fs.readFileSync(file, 'utf8')).unanswered, {});
  // Danach nichts mehr bis zur nächsten Probe nach der Pause (1 min), nicht nach 5 s.
  now.advance(30 * SEC);
  assert.strictEqual(await w.request(), 'paused');
  assert.strictEqual(node.attempts, 1);
});

test('Zwei Fenster: der Abstand gilt je Adresse über die Pausendatei — zusammen höchstens eine Probe je Abstand', async () => {
  const now = clock();
  const node = new FakeNode({ vm: true });
  node.down = true;
  const file = tmpFile();
  const a = new Window(file, now, node, [pair('vm')]);
  const b = new Window(file, now, node, [pair('vm')]);
  const [ea, eb] = await Promise.all([a.tryRequest(), b.tryRequest()]);
  assert.strictEqual(node.attempts, 1, 'beide Fenster probten');
  assert.match(ea.message, /nicht erreichbar/);
  assert.match(eb.message, /in einem anderen Fenster ohne Ergebnis; nächste Probe in 5 s/);
  assert.strictEqual(eb.kind, 'unreachable');
  // Ein drittes Fenster, das jetzt startet, probt nicht.
  const c = new Window(file, now, node, [pair('vm')]);
  assert.match((await c.tryRequest()).message, /anderen Fenster/);
  assert.strictEqual(node.attempts, 1);
  // Über 3 min im Takt der Erweiterung (alle 3 s je Fenster): Proben bei 0, 6, 18, 48, 108, 168 s.
  const start = now();
  await run([a, b, c], now, 3 * MIN, { together: true });
  assert.deepStrictEqual(node.attemptsAt.map((t) => (t - start) / SEC), [0, 6, 18, 48, 108, 168]);
  assert.strictEqual(node.requests, 0);
  // Der Node ist zurück: Die nächste Probe öffnet die Schranke für alle Fenster.
  node.down = false;
  await run([a, b, c], now, 2 * MIN, { together: true });
  assert.strictEqual(node.attempts, 7 + node.requests - 1, 'weitere Proben nach der Rückkehr');
  assert.ok(node.requests > 60, `${node.requests} Anfragen: die Fenster arbeiten nicht weiter`);
  assert.deepStrictEqual(JSON.parse(fs.readFileSync(file, 'utf8')).unanswered, {});
  assert.ok(!fs.existsSync(`${file}.lock`), 'Sperrdatei bleibt liegen');
});

// Der kritische Befund des zweiten Reviews (vscode/pause.js:444): Ließ sich die Pausendatei nicht
// schreiben, galt der Schreibfehler als Ergebnis der Probe — Kennung gesetzt, keine Pause, alle
// Paare hinaus; bei liegendem Node fehlte der Abstand, jede Anfrage probte neu. Jetzt: Der
// Schreibfehler wirft nicht, der Stand gilt im Speicher dieses Fensters, und sonst alles wie mit
// Datei; je einmal im Log; beim nächsten gelungenen Schreiben wieder die Datei.
test('Pausendatei nicht schreibbar: kein Ergebnis — Schranke zu, Abstand 5–60 s und Pause im Speicher; wieder schreibbar → Datei', async () => {
  const now = clock();
  const node = new FakeNode({ vm: false });
  node.down = true;
  const file = tmpFile();
  fs.mkdirSync(path.dirname(file), { recursive: true });
  const bfs = brittleFs();
  bfs.readonly = true;
  const logs = [];
  const w = new Window(file, now, node, [pair('vm')], (l) => logs.push(l), bfs);
  // Sechs Anfragen zugleich und sechs nacheinander (der Probelauf des Reviews): eine Probe.
  const results = await Promise.all([1, 2, 3, 4, 5, 6].map(() => w.tryRequest()));
  for (let i = 0; i < 6; i++) results.push(await w.tryRequest());
  assert.strictEqual(node.attempts, 1, `${node.attempts} Proben`);
  for (const r of results) {
    assert.ok(r instanceof Error, 'eine Anfrage ging durch');
    assert.strictEqual(r.kind, 'unreachable', `${r.message}`);
    assert.match(r.message, /ECONNREFUSED\); nächste Probe in 5 s/);
  }
  assert.strictEqual(w.gate.probedSig, undefined, 'der Schreibfehler galt als Ergebnis');
  assert.ok(!fs.existsSync(file) && !fs.existsSync(`${file}.lock`));
  // Der Abstand wächst im Speicher wie in der Datei: 10, 30, 60, dann 60 s, je genau eine Probe.
  for (const [wait, next] of [[5, 10], [10, 30], [30, 60], [60, 60], [60, 60]]) {
    now.advance(wait * SEC);
    const before = node.attempts;
    await Promise.all([w.tryRequest(), w.tryRequest()]);
    assert.strictEqual(node.attempts, before + 1, `nach ${wait} s: ${node.attempts - before} Proben`);
    assert.strictEqual(w.gate.file.load().unanswered[ADDRESS].nextProbe, now() + next * SEC, 'der Abstand im Speicher');
    await w.tryRequest();
    assert.strictEqual(node.attempts, before + 1);
  }
  assert.strictEqual(node.requests, 0, 'etwas kam beim Node an');
  // Je einmal im Log, nicht je Anfrage: die Pausendatei und die Sperrdatei, mit Pfad und Fehler.
  assert.strictEqual(logs.filter((l) => l.includes('Pausendatei') && l.includes('nicht schreibbar')).length, 1, logs.join('\n'));
  assert.strictEqual(logs.filter((l) => l.includes('Sperrdatei') && l.includes('nicht anlegbar')).length, 1, logs.join('\n'));
  assert.ok(logs.some((l) => l.includes(file) && l.includes('EACCES')), logs.join('\n'));
  assert.ok(!logs.join('\n').includes(TOKEN));
  // Der Node ist zurück, das Paar ungültig: Die Probe ist ein Ergebnis — Kennung gesetzt, das
  // Paar pausiert im Speicher, nichts sendbar, kein changes alle 3 s; die Datei fehlt weiter.
  node.down = false;
  now.advance(60 * SEC);
  await run([w], now, 50 * SEC);
  assert.strictEqual(node.requests, 1, `${node.requests} Anfragen mit ungültigem Paar`);
  assert.ok(w.gate.probedSig, 'nicht als geprobt gekennzeichnet');
  assert.strictEqual(w.paused().vm.failures, 1);
  assert.deepStrictEqual(w.gate.sendable(ADDRESS, w.pairs), []);
  assert.strictEqual(w.gate.budget(ADDRESS), 1);
  assert.deepStrictEqual(fs.readdirSync(path.dirname(file)), [], 'Datei oder Zwischendatei entstand trotzdem');
  assert.strictEqual(logs.filter((l) => l.includes('nicht schreibbar')).length, 1, 'je Anfrage im Log');
  // Das Schreibrecht ist zurück: Die nächste Probe (nach der Pause von 1 min) schreibt die Datei
  // mit dem Stand aus dem Speicher, einmal im Log; ein zweites Fenster übernimmt die Pause.
  bfs.readonly = false;
  now.advance(MIN);
  assert.strictEqual(await w.request(), 'paused');
  assert.strictEqual(node.requests, 2);
  const state = JSON.parse(fs.readFileSync(file, 'utf8'));
  assert.strictEqual(state.pairs[`${ADDRESS}|vm|kamran`].failures, 2, 'die Zählung aus dem Speicher fehlt in der Datei');
  assert.strictEqual(state.budget[ADDRESS].length, 2);
  assert.strictEqual(logs.filter((l) => l.includes('Pausendatei') && l.includes('wieder schreibbar')).length, 1, logs.join('\n'));
  assert.strictEqual(logs.filter((l) => l.includes('Sperrdatei') && l.includes('wieder anlegbar')).length, 1, logs.join('\n'));
  assert.deepStrictEqual(fs.readdirSync(path.dirname(file)), ['vscode-pause.json']);
  const b = new Window(file, now, node, [pair('vm')]);
  assert.strictEqual(await b.request(), 'paused');
  assert.strictEqual(node.requests, 2, 'das zweite Fenster probte trotz der Datei');
  assert.strictEqual(b.paused().vm.failures, 2);
});

test('Pausendatei nicht schreibbar: Probe mit invalid pausiert das Paar im Speicher, nichts sendbar, nächste Probe nach der Pause', async () => {
  const now = clock();
  const node = new FakeNode({ vm: false });
  const file = tmpFile();
  fs.mkdirSync(path.dirname(file), { recursive: true });
  const bfs = brittleFs();
  bfs.readonly = true;
  const w = new Window(file, now, node, [pair('vm')], undefined, bfs);
  const results = await Promise.all([1, 2, 3, 4, 5].map(() => w.request()));
  assert.deepStrictEqual(results, ['paused', 'paused', 'paused', 'paused', 'paused']);
  assert.strictEqual(node.requests, 1, 'mehr als eine Anfrage mit dem Paar');
  assert.ok(w.gate.probedSig, 'die Probe mit Ergebnis setzte die Kennung nicht');
  assert.strictEqual(w.paused().vm.nextProbe, now() + MIN);
  assert.ok(!fs.existsSync(file));
  // In der Minute nichts, danach die zweite Probe; dann hält das Budget (2) bis 10 min nach der ersten.
  await run([w], now, 57 * SEC);
  assert.strictEqual(node.requests, 1);
  now.advance(3 * SEC);
  assert.strictEqual(await w.request(), 'paused');
  assert.strictEqual(node.requests, 2);
  assert.strictEqual(w.paused().vm.failures, 2);
  assert.strictEqual(w.gate.budget(ADDRESS), 2);
  await run([w], now, 8 * MIN);
  assert.strictEqual(node.requests, 2, 'Probe trotz vollem Budget');
  assert.ok(node.worstWindow() <= RESERVE);
});

test('Platte voll (ENOSPC): keine Zwischendatei, keine leere Sperrdatei, der Stand im Speicher; danach wieder die Datei mit allem', () => {
  const now = clock();
  const file = tmpFile();
  const bfs = brittleFs();
  const logs = [];
  const pf = new PauseFile(file, { now, fsImpl: bfs, log: (l) => logs.push(l) });
  bfs.full = true;
  assert.strictEqual(pf.tryLock(), 'none');
  assert.strictEqual(pf.tryLock(), 'none');
  assert.deepStrictEqual(fs.readdirSync(path.dirname(file)), [], 'eine leere Sperrdatei bleibt liegen');
  pf.update((s) => { s.budget[ADDRESS] = [now()]; return true; });
  assert.strictEqual(pf.unwritable, true);
  assert.deepStrictEqual(fs.readdirSync(path.dirname(file)), [], 'eine Zwischendatei bleibt liegen');
  assert.deepStrictEqual(pf.load().budget[ADDRESS], [now()], 'der Stand im Speicher ist weg');
  // Das nächste Lesen-Ändern-Schreiben baut auf dem Speicher auf, nicht auf der fehlenden Datei.
  pf.update((s) => { s.unanswered[ADDRESS] = { count: 1, at: now(), nextProbe: now() + 5 * SEC }; return true; });
  assert.strictEqual(pf.load().budget[ADDRESS].length, 1);
  assert.strictEqual(logs.length, 2, logs.join('\n'));
  // Platz ist wieder da: Das nächste Schreiben legt die Datei an, mit allem aus dem Speicher.
  bfs.full = false;
  pf.update((s) => { s.budget[ADDRESS].push(now()); return true; });
  assert.strictEqual(pf.unwritable, false);
  const onDisk = JSON.parse(fs.readFileSync(file, 'utf8'));
  assert.strictEqual(onDisk.budget[ADDRESS].length, 2);
  assert.strictEqual(onDisk.unanswered[ADDRESS].count, 1);
  assert.strictEqual(pf.tryLock(), 'got');
  pf.unlock();
  assert.strictEqual(logs.length, 4, logs.join('\n'));
  assert.deepStrictEqual(fs.readdirSync(path.dirname(file)), ['vscode-pause.json']);
  assert.strictEqual(fs.statSync(file).mode & 0o777, 0o600);
});

// Dasselbe am echten Dateisystem: ein Verzeichnis 0500, wie es root angelegt haben könnte.
test('Verzeichnis 0500 am echten Dateisystem: EACCES ist kein Ergebnis — eine Probe, Pause im Speicher',
  { skip: typeof process.getuid === 'function' && process.getuid() === 0 ? 'unter root verbietet 0500 nichts' : false },
  async () => {
    const now = clock();
    const node = new FakeNode({ vm: false });
    node.down = true;
    const file = tmpFile();
    fs.mkdirSync(path.dirname(file), { recursive: true });
    fs.chmodSync(path.dirname(file), 0o500);
    try {
      const logs = [];
      const w = new Window(file, now, node, [pair('vm')], (l) => logs.push(l));
      for (let i = 0; i < 6; i++) await w.tryRequest();
      assert.strictEqual(node.attempts, 1, `${node.attempts} Proben`);
      assert.strictEqual(w.gate.probedSig, undefined);
      now.advance(5 * SEC);
      node.down = false;
      assert.strictEqual(await w.request(), 'paused');
      assert.strictEqual(node.requests, 1);
      assert.ok(w.gate.probedSig);
      assert.strictEqual(w.paused().vm.failures, 1);
      assert.ok(logs.some((l) => l.includes('nicht schreibbar') && l.includes('EACCES')), logs.join('\n'));
      assert.ok(!fs.existsSync(file));
    } finally {
      fs.chmodSync(path.dirname(file), 0o700);
    }
  });
