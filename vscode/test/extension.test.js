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
    registerFileSystemProvider: noop,
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

// fetch: merkt sich Adresse, Header und redirect; antwortet wie ein Node über einen Proxy ohne
// gültige Anmeldung.
const calls = [];
globalThis.fetch = async (url, opts) => {
  calls.push({ url, headers: opts.headers, redirect: opts.redirect });
  const result = { content: [{ type: 'text', text: '' }], structuredContent: { hidden: true, hubs: [], unknown_hubs: [] } };
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
