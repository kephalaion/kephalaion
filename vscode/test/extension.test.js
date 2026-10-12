// Die Erweiterung mit einem Ersatz für vscode und für fetch (Task 023): Welche Header-Paare gehen
// an welche nodeUrl, im FileSystemProvider (whoami beim Start) und im MCP-Server für Copilot?
// Dazu Schranke und Pause an einer entfernten nodeUrl (Task 025; die Zeitfolgen prüft
// pause.test.js). Kein Netz, Dummy-Tokens in einem temporären HOME, die Pausendatei darunter.

'use strict';

const test = require('node:test');
const assert = require('node:assert');
const fs = require('fs');
const os = require('os');
const path = require('path');
const crypto = require('crypto');
const Module = require('module');

const home = fs.mkdtempSync(path.join(os.tmpdir(), 'keph-ext-'));
process.env.XDG_CONFIG_HOME = path.join(home, 'config');
process.env.XDG_STATE_HOME = path.join(home, 'state');
const pauseFile = path.join(home, 'state', 'kephalaion', 'vscode-pause.json');
process.env.KEPHALAION_CONFIG = path.join(home, 'config', 'kephalaion', 'config.yaml');
const tokens = path.join(home, 'config', 'kephalaion', 'tokens');
const TOKEN_A = `keph_${'A'.repeat(43)}`;
const TOKEN_B = `keph_${'B'.repeat(43)}`;

function putToken(hub, account, token) {
  fs.mkdirSync(path.join(tokens, hub), { recursive: true });
  fs.writeFileSync(path.join(tokens, hub, `${account}.token`), `${token}\n`, { mode: 0o600 });
}

// Einstellungen und die Spuren der Erweiterung.
const settings = {};
const logs = [];
let provider;
let fsProvider;
let statusItem;
const registered = {};
const disposables = [];

class Disposable {
  constructor(fn) {
    this.fn = fn;
  }

  dispose() {
    if (this.fn) this.fn();
  }
}

const noop = () => new Disposable();

// FileSystemError wie in VS Code: ein Fehler mit code.
class FileSystemError extends Error {
  constructor(message, code = 'Unknown') {
    super(typeof message === 'string' ? message : String(message && message.path || ''));
    this.code = code;
  }
}
for (const code of ['FileNotFound', 'FileExists', 'FileNotADirectory', 'FileIsADirectory', 'NoPermissions', 'Unavailable']) {
  FileSystemError[code] = (m) => new FileSystemError(m, code);
}

const vscode = {
  workspace: {
    getConfiguration: () => ({ get: (k, d) => (k in settings ? settings[k] : d), update: async () => {} }),
    registerFileSystemProvider: (scheme, p) => {
      fsProvider = p;
      return new Disposable();
    },
    onDidChangeConfiguration: noop,
    workspaceFolders: [],
    updateWorkspaceFolders: () => {},
  },
  window: {
    createOutputChannel: () => ({ appendLine: (l) => logs.push(l), show: () => {}, dispose: () => {} }),
    createStatusBarItem: () => {
      statusItem = { show: () => {}, dispose: () => {} };
      return statusItem;
    },
    showWarningMessage: () => {},
    showQuickPick: async () => undefined,
  },
  commands: {
    registerCommand: (id, fn) => {
      registered[id] = fn;
      return new Disposable();
    },
    executeCommand: () => {},
  },
  lm: {
    registerMcpServerDefinitionProvider: (id, p) => {
      provider = p;
      return new Disposable();
    },
  },
  env: { logLevel: 2, onDidChangeLogLevel: noop },
  LogLevel: { Trace: 1 },
  EventEmitter: class {
    constructor() {
      this.event = noop;
    }

    fire() {}

    dispose() {}
  },
  Disposable,
  ThemeColor: class {},
  MarkdownString: class {
    appendMarkdown() {}
  },
  StatusBarAlignment: { Left: 1 },
  FileChangeType: { Changed: 1, Created: 2, Deleted: 3 },
  FileType: { File: 1, Directory: 2 },
  FilePermission: { Readonly: 1 },
  FileSystemError,
  QuickPickItemKind: { Separator: -1 },
  ConfigurationTarget: { Global: 1 },
  Uri: { parse: (s) => ({ toString: () => s, s }), from: (o) => o },
  McpHttpServerDefinition: class {
    constructor(label, uri, headers, version) {
      Object.assign(this, { label, uri, headers, version });
    }
  },
};

const load = Module._load;
Module._load = function (request, ...rest) {
  if (request === 'vscode') return vscode;
  return load.call(this, request, ...rest);
};

// Die Hubs der Header-Paare einer Anfrage, nach Alias.
function hubsOf(headers) {
  return Object.keys(headers || {}).filter((h) => h.startsWith('X-Keph-Token-')).map((h) => h.slice(13)).sort();
}

// Die verdeckte Antwort: über einen Proxy ohne gültige Anmeldung.
const HIDDEN = { content: [{ type: 'text', text: '' }], structuredContent: { hidden: true, hubs: [], unknown_hubs: [] } };

// Ein Node, an dem jedes geschickte Paar gilt (whoami); sonst eine leere Struktur.
function validNode(params, headers) {
  if (params.name !== 'whoami') return { content: [{ type: 'text', text: '' }], structuredContent: {} };
  const hubs = hubsOf(headers).map((hub) => ({ hub, login: 'ok', node: 'n', account: headers[`X-Keph-Account-${hub}`],
    user: 'u', sync: { last_success: '2026-10-09T10:00:00Z', revision: 1 }, collections: [] }));
  return { content: [{ type: 'text', text: '' }], structuredContent: { version: 'dev', hubs, unknown_hubs: [] } };
}

// fetch: merkt sich Adresse, Header und redirect; antwortet mit reply(params, headers), wenn das
// ein Ergebnis für den Aufruf liefert (oder eine Response), sonst wie ein Node, an dem jedes Paar
// gilt.
const calls = [];
let reply = null;
globalThis.fetch = async (url, opts) => {
  calls.push({ url, headers: opts.headers, redirect: opts.redirect });
  const { params } = JSON.parse(opts.body);
  const r = reply && reply(params, opts.headers);
  if (r instanceof Response) return r;
  const result = r || validNode(params, opts.headers);
  return new Response(JSON.stringify({ jsonrpc: '2.0', id: 1, result }),
    { status: 200, headers: { 'content-type': 'application/json' } });
};

const ext = require('../extension');

// Jeder Test beginnt ohne Pausen: eine frische Pausendatei.
function start() {
  calls.length = 0;
  fs.rmSync(path.dirname(pauseFile), { recursive: true, force: true });
  const context = { subscriptions: [], extension: { packageJSON: { version: 'test' } } };
  ext.activate(context);
  disposables.push(...context.subscriptions);
}

function stop() {
  for (const d of disposables.splice(0)) d.dispose();
}

const settle = () => new Promise((r) => setTimeout(r, 50));

// Nach jedem Test die Erweiterung beenden, auch nach einem Fehler: Ihre Zeitgeber hielten den
// Prozess sonst am Leben.
test.afterEach(() => stop());
test.after(() => fs.rmSync(home, { recursive: true, force: true }));

test('entfernte nodeUrl: nur die gewählten Hubs, auch nach einem neuen Hub unter tokens/', async () => {
  putToken('vm', 'kamran', TOKEN_A);
  putToken('eigen', 'kp', TOKEN_B);
  Object.assign(settings, { nodeUrl: 'https://node.example.org/kephalaion', hubs: ['vm'] });
  start();
  await settle();
  assert.ok(calls.length > 0, 'keine Anfrage an den Node');
  for (const c of calls) {
    assert.strictEqual(c.url, 'https://node.example.org/kephalaion/mcp');
    assert.strictEqual(c.redirect, 'manual');
    assert.deepStrictEqual(hubsOf(c.headers), ['vm']);
  }
  const [def] = provider.provideMcpServerDefinitions();
  assert.strictEqual(def.uri.s, 'https://node.example.org/kephalaion/mcp');
  assert.deepStrictEqual(Object.keys(def.headers), [], 'die gemeldete Definition trägt keine Header');
  assert.deepStrictEqual(hubsOf((await provider.resolveMcpServerDefinition(def)).headers), ['vm']);
  // Ein Hub kommt hinzu: die Paare bleiben.
  putToken('neu', 'x', TOKEN_B);
  assert.deepStrictEqual(hubsOf((await provider.resolveMcpServerDefinition(def)).headers), ['vm']);
  stop();
  start();
  await settle();
  for (const c of calls) assert.deepStrictEqual(hubsOf(c.headers), ['vm']);
});

test('entfernte nodeUrl ohne Wahl bei mehreren Hubs: kein Token geht hinaus', async () => {
  putToken('vm', 'kamran', TOKEN_A);
  putToken('eigen', 'kp', TOKEN_B);
  Object.assign(settings, { nodeUrl: 'https://node.example.org/kephalaion', hubs: [] });
  logs.length = 0;
  start();
  await settle();
  assert.strictEqual(calls.length, 0, 'eine Anfrage ging hinaus');
  assert.deepStrictEqual(provider.provideMcpServerDefinitions(), []);
  assert.ok(logs.some((l) => l.includes('kephalaion.hubs')), logs.join('\n'));
});

test('http zu einem fremden Host abgelehnt, zu host.docker.internal erlaubt', async () => {
  putToken('vm', 'kamran', TOKEN_A);
  Object.assign(settings, { nodeUrl: 'http://node.example.org:7433', hubs: ['vm'] });
  logs.length = 0;
  start();
  await settle();
  assert.strictEqual(calls.length, 0, 'eine Anfrage ging per http an einen fremden Host');
  assert.deepStrictEqual(provider.provideMcpServerDefinitions(), []);
  assert.ok(logs.some((l) => l.includes('http nur zu diesem Rechner')), logs.join('\n'));
  stop();
  Object.assign(settings, { nodeUrl: 'http://host.docker.internal:7433', hubs: ['vm'] });
  start();
  await settle();
  assert.ok(calls.length > 0);
  for (const c of calls) {
    assert.strictEqual(c.url, 'http://host.docker.internal:7433/mcp');
    assert.deepStrictEqual(hubsOf(c.headers), ['vm']);
  }
});

test('lokale nodeUrl: alle Hubs wie bisher', async () => {
  putToken('vm', 'kamran', TOKEN_A);
  putToken('eigen', 'kp', TOKEN_B);
  putToken('neu', 'x', TOKEN_B);
  Object.assign(settings, { nodeUrl: 'http://127.0.0.1:7433', hubs: ['vm'] });
  start();
  await settle();
  assert.ok(calls.length > 0);
  for (const c of calls) assert.deepStrictEqual(hubsOf(c.headers), ['eigen', 'neu', 'vm']);
});

// Task 024: Der Inhalt steht im Feld content der Struktur, der Text ist nur ihr JSON. Bei einem
// älteren Node fehlt content, dort ist der Text der Inhalt.
test('readFile: Inhalt aus content, bei einem älteren Node aus dem Text', async () => {
  putToken('vm', 'kamran', TOKEN_A);
  Object.assign(settings, { nodeUrl: 'http://127.0.0.1:7433', hubs: [] });
  start();
  await settle();
  const doc = { kind: 'document', address: 'vm:test', name: 'a.md', id: '01ABC', revision: 3, size: 6, writable: true };
  const readWith = async (result) => {
    reply = (p) => (p.name === 'read' ? result : null);
    try {
      return new TextDecoder().decode(await fsProvider.readFile({ authority: 'vm', path: '/test/a.md' }));
    } finally {
      reply = null;
    }
  };
  const asNew = (d) => ({ content: [{ type: 'text', text: JSON.stringify(d) }], structuredContent: d });
  assert.strictEqual(await readWith(asNew({ ...doc, content: '# Neu <&>\n' })), '# Neu <&>\n');
  assert.strictEqual(await readWith(asNew({ ...doc, size: 0, content: '' })), '', 'leeres Dokument');
  assert.strictEqual(await readWith({ content: [{ type: 'text', text: '# Alt\n' }], structuredContent: doc }), '# Alt\n',
    'älterer Node');
});

// --- Die Zusagen des MCP-Servers für Copilot (docs/vscode.md, „MCP-Server für Copilot“) ---

// Die Token-Dateien von vorn, damit die Tests darunter nicht von den früheren abhängen.
function resetTokens() {
  fs.rmSync(tokens, { recursive: true, force: true });
}

// Die gemeldete Definition speichert VS Code zwischen: Sie trägt weder Header noch Token, auch nicht
// in version. Die Header-Paare setzt erst resolveMcpServerDefinition ein; kein Log nennt ein Token.
test('MCP-Server: gemeldete Definition ohne Header, die Paare erst beim Start', async () => {
  resetTokens();
  putToken('vm', 'kamran', TOKEN_A);
  putToken('eigen', 'kp', TOKEN_B);
  Object.assign(settings, { nodeUrl: 'http://127.0.0.1:7433', hubs: [], accounts: {} });
  logs.length = 0;
  start();
  await settle();
  const defs = provider.provideMcpServerDefinitions();
  assert.strictEqual(defs.length, 1);
  const [def] = defs;
  assert.deepStrictEqual(def.headers, {}, 'die gemeldete Definition trägt keine Header');
  assert.strictEqual(def.version, 'eigen=kp,vm=kamran');
  for (const t of [TOKEN_A, TOKEN_B]) assert.ok(!JSON.stringify(defs).includes(t), 'Token in der Definition');
  const started = await provider.resolveMcpServerDefinition(def);
  assert.deepStrictEqual(started.headers, {
    'X-Keph-Account-eigen': 'kp', 'X-Keph-Token-eigen': TOKEN_B,
    'X-Keph-Account-vm': 'kamran', 'X-Keph-Token-vm': TOKEN_A,
  });
  // Eine neue Meldung trägt wieder keine Header.
  assert.deepStrictEqual(provider.provideMcpServerDefinitions()[0].headers, {});
  for (const t of [TOKEN_A, TOKEN_B]) assert.ok(!logs.join('\n').includes(t), 'Token im Log');
});

// Protokolliert der Extension Host auf Trace, schriebe VS Code die Header ins Log: kein Server, kein
// Start einer schon gemeldeten Definition, eine Warnung — einmal, nicht bei jeder Abfrage. Danach
// wieder wie gewohnt.
test('MCP-Server: bei Trace nicht gemeldet und nicht gestartet', async () => {
  resetTokens();
  putToken('vm', 'kamran', TOKEN_A);
  Object.assign(settings, { nodeUrl: 'http://127.0.0.1:7433', hubs: [], accounts: {} });
  const warnings = [];
  const { showWarningMessage } = vscode.window;
  const { logLevel } = vscode.env;
  vscode.window.showWarningMessage = (m) => warnings.push(m);
  logs.length = 0;
  try {
    start();
    await settle();
    const [def] = provider.provideMcpServerDefinitions();
    assert.ok(def, 'ohne Trace gemeldet');
    vscode.env.logLevel = vscode.LogLevel.Trace;
    assert.deepStrictEqual(provider.provideMcpServerDefinitions(), []);
    assert.deepStrictEqual(provider.provideMcpServerDefinitions(), []);
    assert.strictEqual(await provider.resolveMcpServerDefinition(def), undefined);
    assert.deepStrictEqual(def.headers, {}, 'bei Trace keine Header eingesetzt');
    assert.strictEqual(warnings.length, 1, warnings.join('\n'));
    assert.match(warnings[0], /Trace/);
    assert.ok(logs.some((l) => l.includes('nicht gemeldet: Der Extension Host protokolliert auf Trace')), logs.join('\n'));
    assert.ok(!logs.join('\n').includes(TOKEN_A), 'Token im Log');
    vscode.env.logLevel = logLevel;
    const [again] = provider.provideMcpServerDefinitions();
    assert.deepStrictEqual(hubsOf((await provider.resolveMcpServerDefinition(again)).headers), ['vm']);
  } finally {
    vscode.window.showWarningMessage = showWarningMessage;
    vscode.env.logLevel = logLevel;
  }
});

// Ein Hub mit mehreren Accounts ohne Wahl (kephalaion.accounts) fehlt im Server, mit Hinweis im
// Log; die anderen bleiben. Mit Wahl kommt der gewählte Account.
test('MCP-Server: Hub mit mehreren Accounts ohne Wahl ausgelassen', async () => {
  resetTokens();
  putToken('vm', 'alice', TOKEN_A);
  putToken('vm', 'bob', TOKEN_B);
  putToken('eigen', 'kp', TOKEN_B);
  Object.assign(settings, { nodeUrl: 'http://127.0.0.1:7433', hubs: [], accounts: {} });
  logs.length = 0;
  try {
    start();
    await settle();
    const [def] = provider.provideMcpServerDefinitions();
    assert.strictEqual(def.version, 'eigen=kp');
    const { headers } = await provider.resolveMcpServerDefinition(def);
    assert.deepStrictEqual(headers, { 'X-Keph-Account-eigen': 'kp', 'X-Keph-Token-eigen': TOKEN_B });
    assert.ok(logs.some((l) => l.includes('Hub vm: mehrere Accounts (alice, bob), keiner gewählt')), logs.join('\n'));
    settings.accounts = { vm: 'bob' };
    const [chosen] = provider.provideMcpServerDefinitions();
    assert.strictEqual(chosen.version, 'eigen=kp,vm=bob');
    assert.deepStrictEqual((await provider.resolveMcpServerDefinition(chosen)).headers, {
      'X-Keph-Account-eigen': 'kp', 'X-Keph-Token-eigen': TOKEN_B,
      'X-Keph-Account-vm': 'bob', 'X-Keph-Token-vm': TOKEN_B,
    });
  } finally {
    settings.accounts = {};
  }
});

// Task 030: Die Statusleiste warnt nur über genutzte Hubs. Das whoami des Wegwerf-Nodes aus dem
// Befund vscode-betrieb.md (2026-10-08): zwei Einträge auf denselben Hub, Token-Datei nur für
// t030a — neutral; eine leere Token-Datei für t030b ist missing trotz Token-Datei — gelb.
test('Statusleiste: Hub ohne Token des Clients neutral, leere Token-Datei gelb', async () => {
  putToken('t030a', 'acc030', TOKEN_A);
  Object.assign(settings, { nodeUrl: 'http://127.0.0.1:7433' });
  const sync = { last_success: '2026-10-08T15:39:58Z', revision: 1 };
  reply = (p) => p.name === 'whoami' && {
    content: [{ type: 'text', text: '' }],
    structuredContent: {
      version: 'dev',
      hubs: [
        { hub: 't030a', login: 'ok', node: 'n030-local', account: 'acc030', user: 'u030', sync,
          collections: [{ collection: 't030coll', address: 't030a:t030coll', rights: ['read', 'write'], dirs: [] }] },
        { hub: 't030b', login: 'missing', node: 'n030-http', sync },
      ],
      unknown_hubs: [],
    },
  };
  start();
  await settle();
  assert.strictEqual(statusItem.text, '$(database) Keph t030a');
  assert.strictEqual(statusItem.backgroundColor, undefined);
  logs.length = 0;
  await registered['kephalaion.showStatus']();
  assert.ok(logs.includes('Hub t030a (Node n030-local): Anmeldung ok, Account acc030, User u030'), logs.join('\n'));
  assert.ok(logs.includes('Hub t030b (Node n030-http): nicht genutzt — kein Token'), logs.join('\n'));
  assert.ok(!logs.some((l) => l.includes('Warnung')), logs.join('\n'));
  // Leere Token-Datei für t030b: das Paar geht nicht hinaus, der Node meldet weiter missing.
  stop();
  putToken('t030b', 'acc030', '');
  start();
  await settle();
  assert.strictEqual(statusItem.text, '$(warning) Keph t030a t030b');
  assert.ok(statusItem.backgroundColor);
  logs.length = 0;
  await registered['kephalaion.showStatus']();
  assert.ok(logs.includes('  Warnung: Token-Datei tokens/t030b/acc030.token leer oder nicht lesbar'), logs.join('\n'));
  fs.rmSync(path.join(tokens, 't030b'), { recursive: true, force: true });
});

// --- Schranke und Pause an einer entfernten nodeUrl (Task 025; docs/vscode.md, „Fehlversuche“) ---

const REMOTE = 'https://node.example.org/kephalaion';
// whoami eines Nodes über den Proxy mit den Hub-Einträgen all, an dem nur die Paare der Hubs in
// valid gelten: je Eintrag ok, invalid oder missing (kein Paar geschickt) — wie der Node; ohne
// gültiges Paar verdeckt.
function proxyNode(valid, all = valid) {
  return (params, headers) => {
    if (params.name !== 'whoami') return null;
    const sent = hubsOf(headers);
    if (!sent.some((h) => valid.includes(h))) return HIDDEN;
    const sync = { last_success: '2026-10-09T10:00:00Z', revision: 1 };
    const hubs = all.map((hub) => {
      if (!sent.includes(hub)) return { hub, login: 'missing', node: 'n', sync };
      if (!valid.includes(hub)) return { hub, login: 'invalid', node: 'n', sync };
      return { hub, login: 'ok', node: 'n', account: headers[`X-Keph-Account-${hub}`], user: 'u', sync,
        collections: [{ collection: 'test', address: `${hub}:test`, rights: ['read'], dirs: [] }] };
    });
    return { content: [{ type: 'text', text: '' }], structuredContent: { version: 'dev', hubs, unknown_hubs: [] } };
  };
}

async function rejects(p, code) {
  try {
    await p;
  } catch (e) {
    assert.strictEqual(e.code, code, e.message);
    return e;
  }
  assert.fail(`kein ${code}`);
  return undefined;
}

// Beim Start mit ungültigem Paar und wiederhergestellten Ordnern geht genau eine Anfrage mit
// dem Paar hinaus (die Probe); whoami, changes, stat, readDirectory und readFile warten darauf und
// scheitern als Unavailable — nicht FileNotFound. Die Leiste zeigt „pausiert“; weder Datei noch
// Log nennen ein Token oder seinen Hash.
test('Schranke: Start mit ungültigem Paar — genau eine Anfrage, Dateisystem Unavailable', async () => {
  resetTokens();
  putToken('vm', 'kamran', TOKEN_A);
  Object.assign(settings, { nodeUrl: REMOTE, hubs: ['vm'], accounts: {} });
  reply = proxyNode([]);
  logs.length = 0;
  try {
    start();
    const uri = { authority: 'vm', path: '/test', toString: () => 'keph://vm/test' };
    const pending = [fsProvider.stat(uri), fsProvider.readDirectory(uri), fsProvider.readFile({ ...uri, path: '/test/a.md' })];
    for (const p of pending) await rejects(p, 'Unavailable');
    await settle();
    assert.strictEqual(calls.length, 1, `${calls.length} Anfragen an den Node`);
    assert.deepStrictEqual(hubsOf(calls[0].headers), ['vm']);
    assert.strictEqual(statusItem.text, '$(warning) Keph vm');
    assert.ok(statusItem.backgroundColor);
    await rejects(fsProvider.stat(uri), 'Unavailable');
    await fsProvider.pollChanges();
    assert.strictEqual(calls.length, 1, 'eine weitere Anfrage trotz Pause');
    logs.length = 0;
    await registered['kephalaion.showStatus']();
    assert.ok(logs.some((l) => l.startsWith('Node nicht gefragt (alle Paare pausiert, 1 Fehlversuche in 10 min)')), logs.join('\n'));
    assert.ok(logs.some((l) => /Warnung: pausiert — Anmeldung ungültig \(1 Fehlversuch\), nächste Probe in 1 min/.test(l)),
      logs.join('\n'));
    assert.strictEqual(calls.length, 1, 'showStatus fragte trotz Pause');
    const file = fs.readFileSync(pauseFile, 'utf8');
    assert.ok(!file.includes(TOKEN_A) && !file.includes(crypto.createHash('sha256').update(TOKEN_A).digest('hex')));
    assert.ok(!logs.join('\n').includes(TOKEN_A));
    // Der MCP-Server für Copilot: ohne das pausierte Paar.
    const [def] = provider.provideMcpServerDefinitions();
    assert.deepStrictEqual((await provider.resolveMcpServerDefinition(def)).headers, {});
    assert.strictEqual(calls.length, 1);
  } finally {
    reply = null;
  }
});

// Der kritische Befund des Reviews zu Task 025: Scheitert die Probe an der Verbindung (Node oder
// Proxy beim Start nicht erreichbar), bleibt die Schranke zu — keine Anfrage mit ungeprüften
// Paaren, auch nicht, wenn der Node gleich darauf mit ungültigem Paar zurück ist; neu geprobt
// wird mit wachsendem Abstand (Zeitfolge in pause.test.js), nicht bei jedem changes.
test('Schranke: Node beim Start nicht erreichbar — eine Probe, nichts weiter, Dateisystem Unavailable', async () => {
  resetTokens();
  putToken('vm', 'kamran', TOKEN_A);
  Object.assign(settings, { nodeUrl: REMOTE, hubs: ['vm'], accounts: {} });
  reply = () => { throw Object.assign(new Error('fetch failed'), { cause: { code: 'ECONNREFUSED' } }); };
  logs.length = 0;
  try {
    start();
    const uri = { authority: 'vm', path: '/test', toString: () => 'keph://vm/test' };
    const e = await rejects(fsProvider.stat(uri), 'Unavailable');
    assert.match(e.message, /nicht erreichbar \(ECONNREFUSED\); nächste Probe in \d+ s, bis dahin nichts gefragt/);
    await settle();
    assert.strictEqual(calls.length, 1, `${calls.length} Anfragen an den Node`);
    assert.deepStrictEqual(hubsOf(calls[0].headers), ['vm']);
    assert.strictEqual(statusItem.text, '$(warning) Keph: Node nicht erreichbar');
    assert.ok(logs.some((l) => l.includes('ohne Ergebnis') && l.includes('1. Mal')), logs.join('\n'));
    // Der Node ist zurück, das Paar ungültig: Innerhalb des Abstands geht nichts hinaus — weder
    // changes noch das Dateisystem noch der Start des MCP-Servers.
    reply = proxyNode([]);
    await fsProvider.pollChanges();
    await rejects(fsProvider.readDirectory(uri), 'Unavailable');
    const [def] = provider.provideMcpServerDefinitions();
    await assert.rejects(provider.resolveMcpServerDefinition(def), /nächste Probe/);
    assert.strictEqual(calls.length, 1, 'eine Anfrage mit ungeprüftem Paar');
    const text = fs.readFileSync(pauseFile, 'utf8');
    const file = JSON.parse(text);
    const [address] = Object.keys(file.unanswered);
    assert.ok(address && address.startsWith(REMOTE), address);
    assert.strictEqual(file.unanswered[address].count, 1);
    assert.deepStrictEqual(file.pairs, {});
    assert.ok(!text.includes(TOKEN_A) && !logs.join('\n').includes(TOKEN_A));
  } finally {
    reply = null;
  }
});

// Zwei Hubs, einer ungültig: Nach der Probe geht nur das gültige Paar mit; Aufrufe für den
// anderen Hub scheitern ohne Anfrage, der MCP-Server bekommt nur das gültige Paar.
test('Pause je Paar: zwei Hubs, einer ungültig — nur das gültige Paar, der andere ohne Anfrage', async () => {
  resetTokens();
  putToken('vm', 'kamran', TOKEN_A);
  putToken('eigen', 'kp', TOKEN_B);
  Object.assign(settings, { nodeUrl: REMOTE, hubs: ['vm', 'eigen'], accounts: {} });
  reply = proxyNode(['vm'], ['vm', 'eigen']);
  try {
    start();
    await settle();
    assert.ok(calls.length >= 2, `${calls.length} Anfragen`);
    assert.deepStrictEqual(hubsOf(calls[0].headers), ['eigen', 'vm'], 'die Probe mit beiden Paaren');
    for (const c of calls.slice(1)) assert.deepStrictEqual(hubsOf(c.headers), ['vm'], 'das ungültige Paar ging noch einmal mit');
    assert.strictEqual(statusItem.text, '$(warning) Keph vm eigen');
    const n = calls.length;
    await rejects(fsProvider.stat({ authority: 'eigen', path: '/test', toString: () => 'keph://eigen/test' }), 'Unavailable');
    assert.strictEqual(calls.length, n, 'Anfrage für den pausierten Hub');
    reply = (p, h) => (p.name === 'read' ? { content: [], structuredContent: { kind: 'directory', name: '', writable: false } }
      : proxyNode(['vm'], ['vm', 'eigen'])(p, h));
    await fsProvider.stat({ authority: 'vm', path: '/test', toString: () => 'keph://vm/test' });
    assert.strictEqual(calls.length, n + 1);
    assert.deepStrictEqual(hubsOf(calls[n].headers), ['vm']);
    const [def] = provider.provideMcpServerDefinitions();
    assert.deepStrictEqual(hubsOf((await provider.resolveMcpServerDefinition(def)).headers), ['vm']);
    logs.length = 0;
    await registered['kephalaion.showStatus']();
    assert.ok(logs.some((l) => l.includes('Hub eigen (Node n): Anmeldung missing')), logs.join('\n'));
    assert.ok(logs.some((l) => l.includes('Warnung: pausiert — Anmeldung ungültig (1 Fehlversuch)')), logs.join('\n'));
    assert.ok(!logs.some((l) => l.includes('leer oder nicht lesbar')), logs.join('\n'));
  } finally {
    reply = null;
  }
});

// Lokal bleibt alles: keine Probe vorab, keine Pause, keine Pausendatei — auch mit ungültigem Paar.
test('lokale nodeUrl: ohne Probe und Pause, auch mit ungültigem Paar', async () => {
  resetTokens();
  putToken('vm', 'kamran', TOKEN_A);
  Object.assign(settings, { nodeUrl: 'http://127.0.0.1:7433', hubs: [], accounts: {} });
  reply = () => HIDDEN;
  try {
    start();
    await settle();
    assert.ok(calls.length >= 2, `${calls.length} Anfragen (whoami und changes gehen beide hinaus)`);
    for (const c of calls) assert.deepStrictEqual(hubsOf(c.headers), ['vm']);
    assert.ok(!fs.existsSync(pauseFile), 'Pausendatei lokal');
    assert.strictEqual(statusItem.text, '$(warning) Keph: nicht angemeldet');
  } finally {
    reply = null;
  }
});

// Erkennen im Betrieb: Eine 401 des Proxys pausiert alle Paare der Adresse; danach geht nichts mehr
// hinaus. Ein Hub, dessen Collections changes alle als dropped meldet, ist ungültig geworden.
test('Erkennen: 401 des Proxys und dropped in changes pausieren das Paar', async () => {
  resetTokens();
  putToken('vm', 'kamran', TOKEN_A);
  Object.assign(settings, { nodeUrl: REMOTE, hubs: ['vm'], accounts: {} });
  reply = proxyNode(['vm']);
  try {
    start();
    await settle();
    const n = calls.length;
    assert.ok(n >= 2);
    reply = () => new Response('auth', { status: 401, headers: { 'content-type': 'text/plain' } });
    await fsProvider.pollChanges();
    assert.strictEqual(calls.length, n + 1);
    await fsProvider.pollChanges();
    assert.strictEqual(calls.length, n + 1, 'nach der 401 ging noch eine Anfrage hinaus');
    assert.strictEqual(statusItem.text, '$(warning) Keph vm');
  } finally {
    reply = null;
  }
  // dropped: die eben noch lesbare Collection vm:test fällt weg — das Paar ist ungültig.
  stop();
  const cursor = { changes: [], cursor: 'c1', more: false };
  reply = (p, h) => (p.name === 'changes' ? { content: [], structuredContent: cursor } : proxyNode(['vm'])(p, h));
  try {
    start();
    await settle();
    await fsProvider.pollChanges();
    const n = calls.length;
    cursor.dropped = ['vm:test'];
    await fsProvider.pollChanges();
    assert.strictEqual(calls.length, n + 1);
    assert.strictEqual(statusItem.text, '$(warning) Keph vm');
    await fsProvider.pollChanges();
    assert.strictEqual(calls.length, n + 1, 'nach dropped ging noch eine Anfrage hinaus');
  } finally {
    reply = null;
  }
});
