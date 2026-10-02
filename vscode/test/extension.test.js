// Die Erweiterung mit einem Ersatz für vscode und für fetch (Task 023): Welche Header-Paare gehen
// an welche nodeUrl, im FileSystemProvider (whoami beim Start) und im MCP-Server für Copilot?
// Kein Netz, Dummy-Tokens in einem temporären HOME.

'use strict';

const test = require('node:test');
const assert = require('node:assert');
const fs = require('fs');
const os = require('os');
const path = require('path');
const Module = require('module');

const home = fs.mkdtempSync(path.join(os.tmpdir(), 'keph-ext-'));
process.env.XDG_CONFIG_HOME = path.join(home, 'config');
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
    createStatusBarItem: () => ({ show: () => {}, dispose: () => {} }),
    showWarningMessage: () => {},
    showQuickPick: async () => undefined,
  },
  commands: { registerCommand: noop, executeCommand: () => {} },
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
  FileSystemError: {},
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

// fetch: merkt sich Adresse, Header und redirect; antwortet mit reply, wenn das ein Ergebnis für
// den Aufruf liefert, sonst wie ein Node über einen Proxy ohne gültige Anmeldung.
const calls = [];
let reply = null;
globalThis.fetch = async (url, opts) => {
  calls.push({ url, headers: opts.headers, redirect: opts.redirect });
  const { params } = JSON.parse(opts.body);
  const result = (reply && reply(params)) ||
    { content: [{ type: 'text', text: '' }], structuredContent: { hidden: true, hubs: [], unknown_hubs: [] } };
  return new Response(JSON.stringify({ jsonrpc: '2.0', id: 1, result }),
    { status: 200, headers: { 'content-type': 'application/json' } });
};

const ext = require('../extension');

function start() {
  calls.length = 0;
  const context = { subscriptions: [], extension: { packageJSON: { version: 'test' } } };
  ext.activate(context);
  disposables.push(...context.subscriptions);
}

function stop() {
  for (const d of disposables.splice(0)) d.dispose();
}

// Die Hubs der Header-Paare einer Anfrage, nach Alias.
function hubsOf(headers) {
  return Object.keys(headers || {}).filter((h) => h.startsWith('X-Keph-Token-')).map((h) => h.slice(13)).sort();
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
  assert.deepStrictEqual(hubsOf(provider.resolveMcpServerDefinition(def).headers), ['vm']);
  // Ein Hub kommt hinzu: die Paare bleiben.
  putToken('neu', 'x', TOKEN_B);
  assert.deepStrictEqual(hubsOf(provider.resolveMcpServerDefinition(def).headers), ['vm']);
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
  const started = provider.resolveMcpServerDefinition(def);
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
    assert.strictEqual(provider.resolveMcpServerDefinition(def), undefined);
    assert.deepStrictEqual(def.headers, {}, 'bei Trace keine Header eingesetzt');
    assert.strictEqual(warnings.length, 1, warnings.join('\n'));
    assert.match(warnings[0], /Trace/);
    assert.ok(logs.some((l) => l.includes('nicht gemeldet: Der Extension Host protokolliert auf Trace')), logs.join('\n'));
    assert.ok(!logs.join('\n').includes(TOKEN_A), 'Token im Log');
    vscode.env.logLevel = logLevel;
    const [again] = provider.provideMcpServerDefinitions();
    assert.deepStrictEqual(hubsOf(provider.resolveMcpServerDefinition(again).headers), ['vm']);
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
    const { headers } = provider.resolveMcpServerDefinition(def);
    assert.deepStrictEqual(headers, { 'X-Keph-Account-eigen': 'kp', 'X-Keph-Token-eigen': TOKEN_B });
    assert.ok(logs.some((l) => l.includes('Hub vm: mehrere Accounts (alice, bob), keiner gewählt')), logs.join('\n'));
    settings.accounts = { vm: 'bob' };
    const [chosen] = provider.provideMcpServerDefinitions();
    assert.strictEqual(chosen.version, 'eigen=kp,vm=bob');
    assert.deepStrictEqual(provider.resolveMcpServerDefinition(chosen).headers, {
      'X-Keph-Account-eigen': 'kp', 'X-Keph-Token-eigen': TOKEN_B,
      'X-Keph-Account-vm': 'bob', 'X-Keph-Token-vm': TOKEN_B,
    });
  } finally {
    settings.accounts = {};
  }
});
