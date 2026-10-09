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
const { PauseFile, Gate, pauseAfter, WINDOW_MS, RESERVE, LOCK_STALE_MS } = require('../pause');
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

// Der Ersatz des Nodes über den Proxy: je Hub, ob das Paar gilt. Jede Anfrage mit mindestens
// einem ungültigen Paar ist ein Fehlversuch (ein Fehlversuch ist eine Anfrage, gleich wie viele
// Paare ungültig sind); alle ungültig heißt verdeckt.
class FakeNode {
  constructor(valid) {
    this.valid = { ...valid };
    this.failures = [];
    this.requests = 0;
  }

  answer(sent, now) {
    this.requests++;
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
  constructor(file, now, node, pairs, log) {
    this.node = node;
    this.now = now;
    this.pairs = pairs;
    this.gate = new Gate({ file: new PauseFile(file, { now }), now, sleep: yieldSleep, log: log || (() => {}) });
  }

  async request() {
    await this.gate.ensure(ADDRESS, this.pairs, (send) => this.send(send));
    const send = this.gate.sendable(ADDRESS, this.pairs);
    if (!send.length) return 'paused';
    return this.send(send);
  }

  // Die Anfrage geht hinaus (der Node zählt sie), die Antwort kommt einen Tick später — so sind
  // zwei Fenster zugleich wirklich zwei Anfragen unterwegs.
  async send(send) {
    const who = this.node.answer(send, this.now());
    await yieldSleep();
    this.gate.observe(ADDRESS, send, whoamiOutcome(who, send.map((p) => p.hub)));
    return who;
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
      if (together) await Promise.all(windows.map((w) => w.request()));
      else for (const w of windows) await w.request();
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
