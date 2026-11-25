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
// Der Ersatzort der Pausendatei (pause.js, unter os.tmpdir()) ebenfalls hierher.
fs.mkdirSync(path.join(home, 'tmp'));
process.env.TMPDIR = path.join(home, 'tmp');
process.env.TEMP = path.join(home, 'tmp');
process.env.TMP = path.join(home, 'tmp');
const pauseFile = path.join(home, 'state', 'kephalaion', 'vscode-pause.json');
process.env.KEPHALAION_CONFIG = path.join(home, 'config', 'kephalaion', 'config.yaml');
const tokens = path.join(home, 'config', 'kephalaion', 'tokens');
const TOKEN_A = `keph_${'A'.repeat(43)}`;
const TOKEN_B = `keph_${'B'.repeat(43)}`;

function putToken(hub, account, token) {
  fs.mkdirSync(path.join(tokens, hub), { recursive: true });
  fs.writeFileSync(path.join(tokens, hub, `${account}.token`), `${token}\n`, { mode: 0o600 });
}

// Der Standard-Account eines Hubs (Task 037): tokens/<hub>/default, nur der Name.
function putDefault(hub, account) {
  fs.mkdirSync(path.join(tokens, hub), { recursive: true });
  fs.writeFileSync(path.join(tokens, hub, 'default'), `${account}\n`, { mode: 0o600 });
}

// Einstellungen und die Spuren der Erweiterung.
const settings = {};
const logs = [];
let provider;
let fsProvider;
let statusItem;
const registered = {};
const disposables = [];
const terminalUpdates = [];
const terminalValues = new Map();
const terminals = [];
let terminalWriteError;
// Die Zuhörer der Ereignisse von workspace; workspaceChanged löst das Ordnerereignis aus und
// wartet auf alle Zuhörer, configChanged das Ereignis einer geänderten Einstellung.
const folderListeners = [];
const configListeners = [];
const workspaceChanged = (e = { added: [], removed: [] }) => Promise.all(folderListeners.map((fn) => fn(e)));
const configChanged = (...keys) => Promise.all(configListeners.map((fn) => fn({ affectsConfiguration: (k) => keys.includes(k) })));
// Die Spuren des Baums (Task 043): die View, Meldungen, Befehle, geschriebene Einstellungen.
let treeView;
const shownErrors = [];
const shownInfos = [];
const executed = [];
const settingUpdates = [];
const editorListeners = [];
const clipboard = [];
const shownDocuments = [];
// Der Speicher des Workspace (context.workspaceState), über einen Neustart der Erweiterung hinweg.
const workspaceStore = new Map();
// updateWorkspaceFolders wie VS Code: ändert die Liste, das Ereignis kommt danach; ein Aufruf
// vor dem Ereignis des vorigen wird abgelehnt (und gezählt).
const folderUpdates = [];
let folderEventPending = false;
let folderOverlaps = 0;
function updateWorkspaceFolders(start, deleteCount, ...add) {
  if (folderEventPending) {
    folderOverlaps++;
    return false;
  }
  folderUpdates.push([start, deleteCount, ...add.map((a) => `${a.uri.toString()} ${a.name}`)]);
  const list = vscode.workspace.workspaceFolders;
  list.splice(start, deleteCount, ...add.map((a) => ({ uri: a.uri, name: a.name })));
  list.forEach((f, i) => { f.index = i; });
  folderEventPending = true;
  setTimeout(() => {
    folderEventPending = false;
    workspaceChanged();
  }, 5);
  return true;
}

class Disposable {
  constructor(fn) {
    this.fn = fn;
  }

  dispose() {
    if (this.fn) this.fn();
  }
}

const noop = () => new Disposable();

// Ein Zuhörer in list, abmeldbar.
function listen(list, fn) {
  list.push(fn);
  return new Disposable(() => {
    const i = list.indexOf(fn);
    if (i >= 0) list.splice(i, 1);
  });
}

// Uri wie in VS Code, soweit die Erweiterung sie braucht: Teile, toString, with, joinPath.
class Uri {
  constructor({ scheme = 'file', authority = '', path: p = '', query = '', fragment = '' }) {
    Object.assign(this, { scheme, authority, path: p, query, fragment });
  }

  static from(o) {
    return new Uri(o);
  }

  static parse(s) {
    const m = /^([a-zA-Z][\w+.-]*):\/\/([^/]*)(.*)$/.exec(s);
    const u = m ? new Uri({ scheme: m[1], authority: m[2], path: m[3] || '/' }) : new Uri({ path: s });
    u.s = s;
    return u;
  }

  static file(p) {
    return new Uri({ scheme: 'file', path: p });
  }

  static joinPath(u, ...parts) {
    return u.with({ path: path.posix.join(u.path, ...parts) });
  }

  get fsPath() {
    return this.path;
  }

  with(change) {
    return new Uri({ ...this, ...change });
  }

  toString() {
    return `${this.scheme}://${this.authority}${this.path}`;
  }
}

// workspace.fs wie in VS Code: keph:// an den gemeldeten Provider, file:// auf die Platte.
const onDisk = (u) => u.scheme === 'file';
const diskFs = {
  stat: async (u) => {
    let st;
    try {
      st = fs.statSync(u.path);
    } catch {
      throw FileSystemError.FileNotFound(u.toString());
    }
    return { type: st.isDirectory() ? 2 : 1, ctime: 0, mtime: st.mtimeMs, size: st.size };
  },
  readDirectory: async (u) => fs.readdirSync(u.path, { withFileTypes: true }).map((d) => [d.name, d.isDirectory() ? 2 : 1]),
  readFile: async (u) => new Uint8Array(fs.readFileSync(u.path)),
  writeFile: async (u, c) => {
    fs.mkdirSync(path.dirname(u.path), { recursive: true });
    fs.writeFileSync(u.path, c);
  },
  createDirectory: async (u) => fs.mkdirSync(u.path, { recursive: true }),
};
const workspaceFs = {
  stat: (u) => (onDisk(u) ? diskFs.stat(u) : fsProvider.stat(u)),
  readDirectory: (u) => (onDisk(u) ? diskFs.readDirectory(u) : fsProvider.readDirectory(u)),
  readFile: (u) => (onDisk(u) ? diskFs.readFile(u) : fsProvider.readFile(u)),
  writeFile: (u, c) => (onDisk(u) ? diskFs.writeFile(u, c) : fsProvider.writeFile(u, c, { create: true, overwrite: true })),
  createDirectory: (u) => (onDisk(u) ? diskFs.createDirectory(u) : fsProvider.createDirectory(u)),
  delete: (u, o) => fsProvider.delete(u, { recursive: Boolean(o && o.recursive) }),
  rename: (a, b, o) => fsProvider.rename(a, b, { overwrite: Boolean(o && o.overwrite) }),
};

// Einstellungen der Erweiterung: get aus settings, inspect als globaler Wert, update schreibt
// settings und merkt sich Schlüssel, Wert und Ebene.
const kephConfig = {
  get: (k, d) => (k in settings && settings[k] !== undefined ? settings[k] : d),
  inspect: (k) => ({ key: `kephalaion.${k}`, globalValue: settings[k] }),
  update: async (k, v, target) => {
    settingUpdates.push({ key: k, value: v, target });
    settings[k] = v;
  },
};

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
    getConfiguration: (section, resource) => section === 'terminal.integrated' ? {
      inspect: () => ({ ...(terminalValues.get(resource) || {}) }),
      update: async (key, value, target) => {
        if (terminalWriteError) throw terminalWriteError;
        terminalUpdates.push({ key, value, target });
        terminalValues.set(undefined, { workspaceValue: value });
      },
    } : kephConfig,
    onDidChangeWorkspaceFolders: (fn) => listen(folderListeners, fn),
    registerFileSystemProvider: (scheme, p) => {
      fsProvider = p;
      return new Disposable();
    },
    onDidChangeConfiguration: (fn) => listen(configListeners, fn),
    workspaceFolders: [],
    updateWorkspaceFolders,
    fs: workspaceFs,
    openTextDocument: async (u) => {
      const text = new TextDecoder().decode(await workspaceFs.readFile(u));
      return { uri: u, getText: () => text };
    },
  },
  window: {
    createOutputChannel: () => ({ appendLine: (l) => logs.push(l), show: () => {}, dispose: () => {} }),
    createStatusBarItem: () => {
      statusItem = { show: () => {}, dispose: () => {} };
      return statusItem;
    },
    createTerminal: (options) => {
      const terminal = { options, shown: false, show() { this.shown = true; } };
      terminals.push(terminal);
      return terminal;
    },
    showWarningMessage: () => {},
    showErrorMessage: (m) => shownErrors.push(m),
    showInformationMessage: (m) => shownInfos.push(m),
    showQuickPick: async () => undefined,
    showInputBox: async () => undefined,
    showOpenDialog: async () => undefined,
    showTextDocument: async (doc, options) => {
      shownDocuments.push({ doc, options });
    },
    createTreeView: (id, options) => {
      const visibility = [];
      treeView = {
        id,
        options,
        provider: options.treeDataProvider,
        visible: true,
        revealed: [],
        selection: [],
        reveal: async (el, o) => treeView.revealed.push({ el, o }),
        onDidChangeVisibility: (fn) => listen(visibility, fn),
        dispose: () => {},
      };
      return treeView;
    },
    activeTextEditor: undefined,
    onDidChangeActiveTextEditor: (fn) => listen(editorListeners, fn),
  },
  commands: {
    registerCommand: (id, fn) => {
      registered[id] = fn;
      return new Disposable();
    },
    executeCommand: async (...args) => {
      executed.push(args);
    },
  },
  lm: {
    registerMcpServerDefinitionProvider: (id, p) => {
      provider = p;
      return new Disposable();
    },
  },
  env: { logLevel: 2, onDidChangeLogLevel: noop, clipboard: { writeText: async (t) => { clipboard.push(t); } } },
  ViewColumn: { Beside: -2 },
  Range: class {
    constructor(sl, sc, el, ec) {
      Object.assign(this, { start: { line: sl, character: sc }, end: { line: el, character: ec } });
    }
  },
  DataTransferItem: class {
    constructor(value) {
      this.value = value;
    }

    async asString() {
      return typeof this.value === 'string' ? this.value : JSON.stringify(this.value);
    }
  },
  DataTransfer: class {
    constructor() {
      this.items = new Map();
    }

    get(mime) {
      return this.items.get(mime);
    }

    set(mime, item) {
      this.items.set(mime, item);
    }
  },
  LogLevel: { Trace: 1 },
  EventEmitter: class {
    constructor() {
      this.listeners = [];
      this.event = (fn) => listen(this.listeners, fn);
    }

    fire(v) {
      for (const fn of [...this.listeners]) fn(v);
    }

    dispose() {
      this.listeners = [];
    }
  },
  TreeItem: class {
    constructor(labelOrUri, collapsibleState) {
      if (typeof labelOrUri === 'string') this.label = labelOrUri;
      else this.resourceUri = labelOrUri;
      this.collapsibleState = collapsibleState;
    }
  },
  TreeItemCollapsibleState: { None: 0, Collapsed: 1, Expanded: 2 },
  ThemeIcon: class {
    constructor(id) {
      this.id = id;
    }
  },
  Disposable,
  ThemeColor: class {},
  MarkdownString: class {
    constructor() {
      this.value = '';
    }

    appendMarkdown(s) {
      this.value += s;
    }
  },
  StatusBarAlignment: { Left: 1 },
  FileChangeType: { Changed: 1, Created: 2, Deleted: 3 },
  FileType: { File: 1, Directory: 2 },
  FilePermission: { Readonly: 1 },
  FileSystemError,
  QuickPickItemKind: { Separator: -1 },
  ConfigurationTarget: { Global: 1, Workspace: 2 },
  Uri,
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
  const { params } = JSON.parse(opts.body);
  calls.push({ url, headers: opts.headers, redirect: opts.redirect, params });
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
  const context = {
    subscriptions: [],
    extension: { packageJSON: { version: 'test' } },
    workspaceState: {
      get: (k) => workspaceStore.get(k),
      update: async (k, v) => (v === undefined ? workspaceStore.delete(k) : workspaceStore.set(k, v)),
    },
  };
  ext.activate(context);
  disposables.push(...context.subscriptions);
}

function stop() {
  for (const d of disposables.splice(0)) d.dispose();
}

const settle = () => new Promise((r) => setTimeout(r, 50));

// Nach jedem Test die Erweiterung beenden, auch nach einem Fehler: Ihre Zeitgeber hielten den
// Prozess sonst am Leben.
test.afterEach(() => {
  stop();
  vscode.workspace.workspaceFolders = [];
  vscode.window.activeTextEditor = undefined;
  shownErrors.length = 0;
  shownInfos.length = 0;
  executed.length = 0;
  settingUpdates.length = 0;
  workspaceStore.clear();
  folderUpdates.length = 0;
  folderOverlaps = 0;
  terminalValues.clear();
  terminalUpdates.length = 0;
  terminals.length = 0;
  terminalWriteError = undefined;
});
test.after(() => fs.rmSync(home, { recursive: true, force: true }));

const folder = (name, scheme = 'file') => ({ name, uri: { scheme, authority: scheme === 'keph' ? 'vm' : '', path: `/${name}` } });

test('Terminal-cwd: bestehender Workspace beim Aktivieren, portabel und nur einmal', async () => {
  vscode.workspace.workspaceFolders = [folder('Wissen', 'keph'), folder('mein Projekt')];
  start();
  await settle();
  assert.deepStrictEqual(terminalUpdates, [{ key: 'cwd', value: '${workspaceFolder:mein Projekt}', target: 2 }]);
  await workspaceChanged();
  assert.strictEqual(terminalUpdates.length, 1);
  registered['kephalaion.openProjectTerminal']();
  assert.strictEqual(terminals[0].options.cwd, vscode.workspace.workspaceFolders[1].uri);
  assert.strictEqual(terminals[0].shown, true);
});

test('Terminal-cwd: erst nach tatsächlicher Workspace-Änderung, auch Remote-Projekt', async () => {
  const project = folder('Projekt', 'vscode-remote');
  project.uri.authority = 'wsl+Ubuntu';
  vscode.workspace.workspaceFolders = [project];
  start();
  await settle();
  assert.strictEqual(terminalUpdates.length, 0);
  vscode.workspace.workspaceFolders.push(folder('Wissen', 'keph'));
  await Promise.all([workspaceChanged(), workspaceChanged()]);
  assert.strictEqual(terminalUpdates.length, 1);
  registered['kephalaion.openProjectTerminal']();
  assert.strictEqual(terminals[0].options.cwd, project.uri);
});

test('Terminal-cwd: globale, Workspace- und Folder-Werte einschließlich leer/null respektieren', async () => {
  for (const key of ['globalValue', 'workspaceValue', 'workspaceFolderValue', 'globalLanguageValue',
    'workspaceLanguageValue', 'workspaceFolderLanguageValue']) {
    for (const value of ['', null, '/eigen']) {
      const project = folder('Projekt');
      const collection = folder('Wissen', 'keph');
      vscode.workspace.workspaceFolders = [project, collection];
      terminalValues.clear();
      terminalValues.set(key.startsWith('workspaceFolder') ? collection.uri : undefined, { [key]: value });
      start();
      await settle();
      assert.strictEqual(terminalUpdates.length, 0, `${key}: ${value}`);
      stop();
    }
  }
});

test('Terminal-cwd: ohne eindeutiges echtes Projekt nichts ändern oder öffnen', async () => {
  for (const folders of [[], [folder('Wissen', 'keph')], [folder('Projekt')],
    [folder('Wissen', 'keph'), folder('A'), folder('B')],
    [folder('Projekt', 'keph'), folder('Projekt')],
    [folder('Wissen', 'keph'), folder('x${env:HOME}')],
    [folder('Wissen', 'keph'), folder('virtuell', 'memfs')]]) {
    vscode.workspace.workspaceFolders = folders;
    start();
    await settle();
    registered['kephalaion.openProjectTerminal']();
    assert.strictEqual(terminalUpdates.length, 0);
    assert.strictEqual(terminals.length, 0);
    stop();
  }
});

test('Collection einbinden: Settings erst beim bestätigten Ereignis, Schreibfehler brechen Befehl nicht ab', async () => {
  const oldPick = vscode.window.showQuickPick;
  const oldUpdate = vscode.workspace.updateWorkspaceFolders;
  const project = folder('Projekt');
  vscode.workspace.workspaceFolders = [project];
  Object.assign(settings, { nodeUrl: 'http://127.0.0.1:7433', hubs: [] });
  putToken('vm', 'kamran', TOKEN_A);
  reply = (params, headers) => {
    const result = validNode(params, headers);
    if (params.name === 'whoami') {
      for (const hub of result.structuredContent.hubs) {
        hub.collections = [{ address: `${hub.hub}:wissen`, collection: 'wissen', rights: ['read'] }];
      }
    }
    return result;
  };
  try {
    vscode.window.showQuickPick = async (items) => items[0];
    let added;
    vscode.workspace.updateWorkspaceFolders = (index, count, value) => {
      assert.strictEqual(index, 1);
      assert.strictEqual(count, 0);
      added = value;
      return true;
    };
    start();
    await settle();
    await registered['kephalaion.addCollection']();
    assert.ok(added);
    assert.strictEqual(terminalUpdates.length, 0);
    vscode.workspace.workspaceFolders.push(added);
    terminalWriteError = new Error('Workspace nicht schreibbar');
    await workspaceChanged();
    assert.strictEqual(terminalUpdates.length, 0);
    assert.ok(logs.some((l) => l.includes('Workspace nicht schreibbar')));
  } finally {
    vscode.window.showQuickPick = oldPick;
    vscode.workspace.updateWorkspaceFolders = oldUpdate;
    reply = null;
  }
});

test('Terminal-cwd: Schreibfehler bleiben lokal und spätere Einbindung funktioniert', async () => {
  vscode.workspace.workspaceFolders = [folder('Projekt'), folder('Wissen', 'keph')];
  terminalWriteError = new Error('settings schreibgeschützt');
  start();
  await settle();
  assert.ok(registered['kephalaion.addCollection']);
  assert.ok(logs.some((l) => l.includes('Workspace-cwd konnte nicht ergänzt werden: settings schreibgeschützt')));
  terminalWriteError = undefined;
  await workspaceChanged();
  assert.strictEqual(terminalUpdates.length, 1);
});

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

// Ein Hub mit mehreren Accounts ohne Wahl und ohne Standard fehlt im Server, mit Hinweis im Log;
// die anderen bleiben. Mit Wahl (kephalaion.accounts) kommt der gewählte Account, mit Standard der
// Standard (Task 037).
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
    assert.ok(logs.some((l) => l.includes('Hub vm: Wahl fehlt — mehrere Accounts (alice, bob), keiner gewählt')), logs.join('\n'));
    settings.accounts = { vm: 'bob' };
    const [chosen] = provider.provideMcpServerDefinitions();
    assert.strictEqual(chosen.version, 'eigen=kp,vm=bob');
    assert.deepStrictEqual((await provider.resolveMcpServerDefinition(chosen)).headers, {
      'X-Keph-Account-eigen': 'kp', 'X-Keph-Token-eigen': TOKEN_B,
      'X-Keph-Account-vm': 'bob', 'X-Keph-Token-vm': TOKEN_B,
    });
    settings.accounts = {};
    putDefault('vm', 'alice');
    const [standard] = provider.provideMcpServerDefinitions();
    assert.strictEqual(standard.version, 'eigen=kp,vm=alice');
    assert.deepStrictEqual(hubsOf((await provider.resolveMcpServerDefinition(standard)).headers), ['eigen', 'vm']);
  } finally {
    settings.accounts = {};
  }
});

// Task 030: Die Statusleiste warnt nur über genutzte Hubs. Das whoami eines Wegwerf-Nodes,
// gemessen am 2026-10-08: zwei Einträge auf denselben Hub, Token-Datei nur für
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
    assert.match(e.message, /antwortet nicht \(ECONNREFUSED\); nächste Probe in \d+ s, bis dahin nichts gefragt/);
    await settle();
    assert.strictEqual(calls.length, 1, `${calls.length} Anfragen an den Node`);
    assert.deepStrictEqual(hubsOf(calls[0].headers), ['vm']);
    // Die Ursache (reason) geht durch die Schranke mit (pause.closedError, Task 035).
    assert.strictEqual(statusItem.text, '$(warning) Keph: Node antwortet nicht');
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

// --- Task 035: die Suche nach der config wie das Binary, die Meldungen nach Ursache ---

// Der feste Ort der globalen config, für die Tests umgelenkt (setSystemPath, wie SystemPath in Go).
const systemConfig = path.join(home, 'etc', 'kephalaion', 'config.yaml');
ext.setSystemPath(systemConfig);
const userConfig = path.join(home, 'config', 'kephalaion', 'config.yaml');

function putConfig(file, text) {
  fs.mkdirSync(path.dirname(file), { recursive: true });
  fs.writeFileSync(file, text);
}

function rmConfig(file) {
  fs.rmSync(file, { recursive: true, force: true });
}

// Die Tests der Suche heben das KEPHALAION_CONFIG der Testdatei auf und stellen es danach wieder her.
async function withoutEnvConfig(fn) {
  const saved = process.env.KEPHALAION_CONFIG;
  delete process.env.KEPHALAION_CONFIG;
  delete settings.nodeUrl;
  try {
    await fn();
  } finally {
    process.env.KEPHALAION_CONFIG = saved;
    reply = null;
    rmConfig(userConfig);
    rmConfig(systemConfig);
  }
}

// Der Tooltip als Text, gleich ob String oder MarkdownString.
const tooltip = () => (typeof statusItem.tooltip === 'string' ? statusItem.tooltip : statusItem.tooltip.value);

// Nur die globale config: whoami geht an ihr listen, mit den Paaren aus tokens/ des Users; der
// MCP-Server für Copilot wird gemeldet. Log und Status nennen Ort und Quelle.
test('globale config: listen daraus, die Token-Dateien des Users, MCP-Server gemeldet', () => withoutEnvConfig(async () => {
  resetTokens();
  putToken('vm', 'kamran', TOKEN_A);
  putConfig(systemConfig, 'node:\n  db: sqlite:////var/lib/kephalaion/node.db\n  listen: "127.0.0.1:7500"\n');
  Object.assign(settings, { hubs: [], accounts: {} });
  logs.length = 0;
  start();
  await settle();
  assert.ok(calls.length > 0, 'keine Anfrage an den Node');
  for (const c of calls) {
    assert.strictEqual(c.url, 'http://127.0.0.1:7500/mcp');
    assert.deepStrictEqual(hubsOf(c.headers), ['vm']);
  }
  assert.strictEqual(statusItem.text, '$(database) Keph vm');
  const [def] = provider.provideMcpServerDefinitions();
  assert.strictEqual(def.uri.s, 'http://127.0.0.1:7500/mcp');
  assert.deepStrictEqual(hubsOf((await provider.resolveMcpServerDefinition(def)).headers), ['vm']);
  assert.ok(logs.some((l) => l.includes(`config ${systemConfig} (global)`)), logs.join('\n'));
  assert.ok(tooltip().includes(`config ${systemConfig} (global)`), tooltip());
  assert.ok(!tooltip().includes('zwei Arten'));
}));

// Fehlt listen im Abschnitt node:, gilt 127.0.0.1:7433 wie im Binary.
test('listen fehlt: 127.0.0.1:7433', () => withoutEnvConfig(async () => {
  resetTokens();
  putToken('vm', 'kamran', TOKEN_A);
  putConfig(userConfig, 'node:\n  db: sqlite:///x/node.db\n');
  Object.assign(settings, { hubs: [], accounts: {} });
  start();
  await settle();
  assert.ok(calls.length > 0);
  for (const c of calls) assert.strictEqual(c.url, 'http://127.0.0.1:7433/mcp');
  assert.ok(tooltip().includes(`config ${userConfig} (pro User)`), tooltip());
}));

// Ohne config: eigene Meldung in Leiste und Tooltip, mit beiden geprüften Orten; keine Anfrage.
test('keine config: eigene Meldung mit den geprüften Orten', () => withoutEnvConfig(async () => {
  resetTokens();
  putToken('vm', 'kamran', TOKEN_A);
  Object.assign(settings, { hubs: [], accounts: {} });
  logs.length = 0;
  start();
  await settle();
  assert.strictEqual(calls.length, 0, 'eine Anfrage ohne Adresse');
  assert.strictEqual(statusItem.text, '$(warning) Keph: keine config');
  assert.ok(tooltip().includes(`geprüft: ${userConfig}, ${systemConfig}`), tooltip());
  assert.ok(tooltip().includes(`config ${userConfig} (pro User, fehlt)`), tooltip());
  assert.deepStrictEqual(provider.provideMcpServerDefinitions(), []);
  assert.ok(logs.some((l) => l.includes('keine Adresse: keine config gefunden')), logs.join('\n'));
  logs.length = 0;
  await registered['kephalaion.showStatus']();
  assert.ok(logs.some((l) => l.startsWith('keine config: keine config gefunden (geprüft:')), logs.join('\n'));
}));

// Der Node antwortet nicht (ECONNREFUSED): „antwortet nicht“ mit Adresse.
test('Node antwortet nicht: eigene Meldung mit Adresse', () => withoutEnvConfig(async () => {
  resetTokens();
  putToken('vm', 'kamran', TOKEN_A);
  putConfig(userConfig, 'node:\n  db: sqlite:///x/node.db\n  listen: 127.0.0.1:7500\n');
  Object.assign(settings, { hubs: [], accounts: {} });
  reply = () => { throw Object.assign(new Error('fetch failed'), { cause: { code: 'ECONNREFUSED' } }); };
  start();
  await settle();
  assert.strictEqual(statusItem.text, '$(warning) Keph: Node antwortet nicht');
  assert.ok(tooltip().includes('Node http://127.0.0.1:7500/mcp antwortet nicht (ECONNREFUSED)'), tooltip());
}));

// config nicht lesbar (hier ein Verzeichnis an ihrem Ort) und kein Abschnitt node:: je eine eigene
// Meldung in Leiste und Tooltip.
test('config nicht lesbar und Node nicht eingerichtet: eigene Meldungen', () => withoutEnvConfig(async () => {
  resetTokens();
  putToken('vm', 'kamran', TOKEN_A);
  fs.mkdirSync(userConfig, { recursive: true });
  Object.assign(settings, { hubs: [], accounts: {} });
  start();
  await settle();
  assert.strictEqual(calls.length, 0);
  assert.strictEqual(statusItem.text, '$(warning) Keph: config nicht lesbar');
  assert.ok(tooltip().includes(`config ${userConfig} nicht lesbar (EISDIR)`), tooltip());
  stop();
  rmConfig(userConfig);
  putConfig(userConfig, 'hub:\n  db: sqlite:///x/hub.db\n  listen: 127.0.0.1:7434\n');
  start();
  await settle();
  assert.strictEqual(calls.length, 0);
  assert.strictEqual(statusItem.text, '$(warning) Keph: Node nicht eingerichtet');
  assert.ok(tooltip().includes(`Node nicht eingerichtet in ${userConfig} (kein Abschnitt node:)`), tooltip());
}));

// Beide Arten: die config des Users gilt; Tooltip und Log nennen den Fehler mit demselben Weg wie
// kephalaion status.
test('zwei Arten der Installation: die des Users gilt, Tooltip und Log nennen den Fehler', () => withoutEnvConfig(async () => {
  resetTokens();
  putToken('vm', 'kamran', TOKEN_A);
  putConfig(userConfig, 'node:\n  db: sqlite:///x/node.db\n  listen: 127.0.0.1:7501\n');
  putConfig(systemConfig, 'node:\n  db: sqlite:///y/node.db\n  listen: 127.0.0.1:7502\n');
  Object.assign(settings, { hubs: [], accounts: {} });
  logs.length = 0;
  start();
  await settle();
  assert.ok(calls.length > 0);
  for (const c of calls) assert.strictEqual(c.url, 'http://127.0.0.1:7501/mcp');
  assert.strictEqual(statusItem.text, '$(database) Keph vm');
  const want = `zwei Arten der Installation auf diesem Rechner — es gibt die config pro User (${userConfig}) und die globale `
    + `(${systemConfig})`;
  assert.ok(tooltip().includes(`Fehler: ${want}`), tooltip());
  assert.ok(tooltip().includes('Installation pro User entfernen'), tooltip());
  assert.ok(logs.some((l) => l.includes(`Fehler: ${want}`)), logs.join('\n'));
}));

// Ohne Ursache entscheidet kind: 5xx ist unklar, 403 abgelehnt.
test('5xx und 403: die Texte für unclear und rejected', () => withoutEnvConfig(async () => {
  resetTokens();
  putToken('vm', 'kamran', TOKEN_A);
  putConfig(userConfig, 'node:\n  db: sqlite:///x/node.db\n');
  Object.assign(settings, { hubs: [], accounts: {} });
  reply = () => new Response('down', { status: 503, headers: { 'content-type': 'text/plain' } });
  start();
  await settle();
  assert.strictEqual(statusItem.text, '$(warning) Keph: Antwort unklar');
  assert.ok(tooltip().includes('HTTP 503'), tooltip());
  stop();
  reply = () => new Response('no', { status: 403, headers: { 'content-type': 'text/plain' } });
  start();
  await settle();
  assert.strictEqual(statusItem.text, '$(warning) Keph: Anfrage abgelehnt');
  assert.ok(tooltip().includes('HTTP 403'), tooltip());
}));

// An einer entfernten nodeUrl mit geschlossener Schranke: ensure wirft dieselbe Art wie bisher
// (kind unreachable), fsError liefert denselben Fehler wie bisher (Unavailable); die Ursache
// kommt nur dazu.
test('Schranke an einer entfernten nodeUrl: kind wie bisher, reason daneben', async () => {
  resetTokens();
  putToken('vm', 'kamran', TOKEN_A);
  Object.assign(settings, { nodeUrl: REMOTE, hubs: ['vm'], accounts: {} });
  reply = () => { throw Object.assign(new Error('fetch failed'), { cause: { code: 'ECONNREFUSED' } }); };
  try {
    start();
    const uri = { authority: 'vm', path: '/test', toString: () => 'keph://vm/test' };
    const e = await rejects(fsProvider.stat(uri), 'Unavailable');
    assert.match(e.message, /antwortet nicht \(ECONNREFUSED\); nächste Probe/);
    await settle();
    const [def] = provider.provideMcpServerDefinitions();
    let thrown;
    try {
      await provider.resolveMcpServerDefinition(def);
    } catch (err) {
      thrown = err;
    }
    assert.ok(thrown, 'ensure warf nicht');
    assert.strictEqual(thrown.kind, 'unreachable');
    assert.strictEqual(thrown.reason, 'no_answer');
    assert.strictEqual(calls.length, 1);
  } finally {
    reply = null;
  }
});

// --- Die Wahl des Accounts wie in der CLI (Task 037; docs/vscode.md, „Status und Auswahl“) ---

// whoami eines lokalen Nodes mit den Hub-Einträgen all: ok mit dem geschickten Account, sonst
// missing (kein Paar geschickt).
function localWhoami(all) {
  const sync = { last_success: '2026-11-10T10:00:00Z', revision: 1 };
  return (p, headers) => p.name === 'whoami' && {
    content: [{ type: 'text', text: '' }],
    structuredContent: {
      version: 'dev',
      hubs: all.map((hub) => {
        const account = (headers || {})[`X-Keph-Account-${hub}`];
        return account ? { hub, login: 'ok', node: 'n', account, user: 'u', sync, collections: [] }
          : { hub, login: 'missing', node: 'n', sync };
      }),
      unknown_hubs: [],
    },
  };
}

// readCredentials in der laufenden Erweiterung: welcher Account geht an den Node (whoami beim
// Start), was sagen Statusleiste, Tooltip und „Status anzeigen“. Dieselben Fälle wie die Go-Tests
// (Standard, Überschreibung vor Standard, veralteter Standard, zwei Dateien ohne Standard, eine
// Datei ohne Standard), dazu eine Überschreibung ohne Token-Datei: kein Rückfall, der Hub bleibt
// ohne Account. Den ersten nach Namen gibt es nicht mehr.
test('Wahl des Accounts: Standard, Einstellung, einzige Datei, Wahl fehlt — gesendet und angezeigt', async () => {
  resetTokens();
  Object.assign(settings, { nodeUrl: 'http://127.0.0.1:7433', hubs: [], accounts: {} });
  reply = localWhoami(['vm']);
  const run = async () => {
    stop();
    calls.length = 0;
    start();
    await settle();
    logs.length = 0;
    await registered['kephalaion.showStatus']();
    const sent = calls.length ? (calls[0].headers || {})['X-Keph-Account-vm'] : undefined;
    for (const t of [TOKEN_A, TOKEN_B]) assert.ok(!logs.join('\n').includes(t), 'Token im Log');
    return { sent, summary: logs.join('\n'), text: statusItem.text, tip: tooltip() };
  };
  try {
    // Eine Datei ohne Standard: die einzige.
    putToken('vm', 'alice', TOKEN_A);
    let r = await run();
    assert.strictEqual(r.sent, 'alice');
    assert.strictEqual(r.text, '$(database) Keph vm');
    assert.ok(r.summary.includes('  Account alice (einzige Token-Datei)'), r.summary);
    assert.ok(r.tip.includes('Account alice (einzige Token-Datei)'), r.tip);

    // Zwei Dateien ohne Standard: Wahl fehlt — kein Paar, gelb, die Kandidaten.
    putToken('vm', 'bob', TOKEN_B);
    r = await run();
    assert.strictEqual(r.sent, undefined);
    assert.strictEqual(r.text, '$(warning) Keph vm');
    assert.ok(r.summary.includes('  Warnung: Wahl fehlt (alice, bob) — Account wählen'), r.summary);
    assert.ok(r.tip.includes('Wahl fehlt — Kandidaten: alice, bob'), r.tip);

    // Standard.
    putDefault('vm', 'bob');
    r = await run();
    assert.strictEqual(r.sent, 'bob');
    assert.strictEqual(r.text, '$(database) Keph vm');
    assert.ok(r.summary.includes('  Account bob (Standard), aus alice, bob'), r.summary);

    // Überschreibung vor Standard.
    settings.accounts = { vm: 'alice' };
    r = await run();
    assert.strictEqual(r.sent, 'alice');
    assert.ok(r.tip.includes('Account alice (Einstellung kephalaion.accounts)'), r.tip);

    // Überschreibung ohne Token-Datei: kein Rückfall auf den Standard, Hinweis, gelb.
    settings.accounts = { vm: 'carol' };
    r = await run();
    assert.strictEqual(r.sent, undefined);
    assert.strictEqual(r.text, '$(warning) Keph vm');
    assert.ok(r.summary.includes('gewählter Account carol hat keine Token-Datei'), r.summary);

    // Veralteter Standard: Hinweis, die nächste Regel — bei zwei Dateien fehlt die Wahl.
    settings.accounts = {};
    putDefault('vm', 'carol');
    r = await run();
    assert.strictEqual(r.sent, undefined);
    assert.ok(r.summary.includes('Wahl fehlt (alice, bob)'), r.summary);
    assert.ok(r.summary.includes('der Standard carol hat keine Token-Datei'), r.summary);
    // … bei einer Datei gilt sie.
    fs.rmSync(path.join(tokens, 'vm', 'bob.token'));
    r = await run();
    assert.strictEqual(r.sent, 'alice');
    assert.ok(r.summary.includes('Account alice (einzige Token-Datei); der Standard carol hat keine Token-Datei'), r.summary);
  } finally {
    settings.accounts = {};
    reply = null;
  }
});

// Ein neuer oder geänderter Standard ändert die Wahl: Die Definition des MCP-Servers wird neu
// gemeldet (mcpState nimmt tokens/<hub>/default auf, ohne Inhalt).
test('MCP-Server: Standard geändert — neu gemeldet', async () => {
  resetTokens();
  putToken('vm', 'alice', TOKEN_A);
  putToken('vm', 'bob', TOKEN_B);
  Object.assign(settings, { nodeUrl: 'http://127.0.0.1:7433', hubs: [], accounts: {} });
  start();
  await settle();
  assert.strictEqual(provider.provideMcpServerDefinitions()[0].version, 'ohne Anmeldung');
  const changed = () => logs.filter((l) => l.endsWith('MCP-Server: Stand geändert, neu gemeldet')).length;
  logs.length = 0;
  provider.check();
  assert.strictEqual(changed(), 0, 'ohne Änderung neu gemeldet');
  putDefault('vm', 'bob');
  provider.check();
  assert.strictEqual(changed(), 1, logs.join('\n'));
  assert.strictEqual(provider.provideMcpServerDefinitions()[0].version, 'vm=bob');
  putDefault('vm', 'alice2');
  provider.check();
  assert.strictEqual(changed(), 2, logs.join('\n'));
  fs.rmSync(path.join(tokens, 'vm', 'default'));
  provider.check();
  assert.strictEqual(changed(), 3, logs.join('\n'));
  assert.ok(!logs.join('\n').includes(TOKEN_A) && !logs.join('\n').includes(TOKEN_B), 'Token im Log');
});

// --- Der collection tree (Task 043; docs/vscode.md, „Baumansicht“) ---

// Ein Node im Speicher: Collections mit Rechten und Dokumenten ({ 'vm:omni': { rights, docs:
// { name: text } } }), dazu list (auch recursive, in Seiten zu pageSize), read, create, write,
// rename, delete, changes (aus node.changes) und search (aus node.searchResults).
function memNode(collections, { pageSize = 1000 } = {}) {
  let rev = 10;
  const store = {};
  for (const [address, c] of Object.entries(collections)) {
    store[address] = { rights: c.rights, docs: new Map() };
    for (const [n, t] of Object.entries(c.docs || {})) store[address].docs.set(n, { id: `id${++rev}`, revision: rev, content: t });
  }
  const ok = (sc) => ({ content: [{ type: 'text', text: '' }], structuredContent: sc });
  const fail = (code, text) => ({ isError: true, content: [{ type: 'text', text }], structuredContent: { error: { code, message: text } } });
  const under = (n, dir) => !dir || n.startsWith(`${dir}/`);
  const isDir = (col, n) => [...col.docs.keys()].some((d) => d.startsWith(`${n}/`));
  const fn = (params) => {
    const a = params.arguments || {};
    const col = store[a.collection];
    const doc = col && col.docs.get(a.name);
    const writable = Boolean(col && col.rights.includes('write'));
    const docOut = (name, d) => ({ kind: 'document', address: a.collection, name, id: d.id, revision: d.revision, size: d.content.length, writable });
    switch (params.name) {
      case 'whoami': {
        const hubs = {};
        for (const [address, c] of Object.entries(store)) {
          const [hub, collection] = address.split(':');
          hubs[hub] = hubs[hub] || { hub, login: 'ok', node: 'n', account: 'kamran', user: 'u', sync: { revision: 1 }, collections: [] };
          hubs[hub].collections.push({ address, collection, rights: c.rights });
        }
        return ok({ version: 'dev', hubs: Object.values(hubs), unknown_hubs: [] });
      }
      case 'changes':
        return ok({ cursor: `c${rev}`, changes: fn.changes.splice(0), more: false });
      case 'list': {
        if (!col) return fail('not_readable', `${a.collection} nicht lesbar`);
        const dir = a.path || '';
        let entries;
        const docs = [...col.docs.keys()].filter((n) => under(n, dir)).sort();
        if (a.recursive) {
          entries = docs.map((n) => ({ kind: 'document', name: n, id: col.docs.get(n).id, revision: col.docs.get(n).revision }));
        } else {
          const pre = dir ? `${dir}/` : '';
          const dirs = [...new Set(docs.map((n) => n.slice(pre.length)).filter((r) => r.includes('/')).map((r) => r.split('/')[0]))];
          entries = [...dirs.map((d) => ({ kind: 'directory', name: pre + d })),
            ...docs.filter((n) => !n.slice(pre.length).includes('/'))
              .map((n) => ({ kind: 'document', name: n, id: col.docs.get(n).id, revision: col.docs.get(n).revision }))];
        }
        const start = a.cursor ? Number(a.cursor) : 0;
        const more = start + pageSize < entries.length;
        return ok({ entries: entries.slice(start, start + pageSize), more, cursor: more ? String(start + pageSize) : undefined });
      }
      case 'read':
        if (!col) return fail('not_readable', `${a.collection} nicht lesbar`);
        if (doc) return ok({ ...docOut(a.name, doc), ...(a.content === false ? {} : { content: doc.content }) });
        if (!a.name || isDir(col, a.name)) return ok({ kind: 'directory', address: a.collection, name: a.name, writable });
        return ok({ kind: 'none', address: a.collection, name: a.name, writable });
      case 'create':
        if (!writable) return fail('forbidden', 'ohne Recht write');
        if (doc || isDir(col, a.name)) return fail('name_taken', `${a.name} ist belegt`);
        col.docs.set(a.name, { id: `id${++rev}`, revision: rev, content: a.content });
        return ok(docOut(a.name, col.docs.get(a.name)));
      case 'write':
        if (!writable) return fail('forbidden', 'ohne Recht write');
        if (!doc) return fail('not_found', `${a.name} fehlt`);
        Object.assign(doc, { revision: ++rev, content: a.content });
        return ok(docOut(a.name, doc));
      case 'rename': {
        if (!writable) return fail('forbidden', 'ohne Recht write');
        if (col.docs.has(a.new_name) || isDir(col, a.new_name)) return fail('name_taken', `${a.new_name} ist belegt`);
        if (doc) {
          col.docs.delete(a.name);
          doc.revision = ++rev;
          col.docs.set(a.new_name, doc);
          return ok(docOut(a.new_name, doc));
        }
        const moved = [...col.docs.keys()].filter((n) => n.startsWith(`${a.name}/`));
        ++rev;
        for (const n of moved) {
          const d = col.docs.get(n);
          col.docs.delete(n);
          col.docs.set(a.new_name + n.slice(a.name.length), d);
        }
        return ok({ kind: 'directory', address: a.collection, name: a.new_name, revision: rev, count: moved.length });
      }
      case 'delete': {
        if (!writable) return fail('forbidden', 'ohne Recht write');
        if (doc) {
          col.docs.delete(a.name);
          return ok({ ...docOut(a.name, doc), revision: ++rev });
        }
        if (!a.recursive) return fail('invalid', `${a.name} ist ein Verzeichnis`);
        const gone = [...col.docs.keys()].filter((n) => n.startsWith(`${a.name}/`));
        for (const n of gone) col.docs.delete(n);
        return ok({ kind: 'directory', address: a.collection, name: a.name, revision: ++rev, count: gone.length });
      }
      case 'search':
        return ok({ results: fn.searchResults });
      default:
        return null;
    }
  };
  fn.store = store;
  fn.changes = [];
  fn.searchResults = [];
  return fn;
}

const OMNI = { rights: ['read', 'write'], docs: { 'README.md': '# Omni\n', 'docs/a.md': '# A\n', 'docs/sub/b.md': '# B\n' } };
const WISSEN = { rights: ['read'], docs: { 'x.md': 'x' } };

// Die Erweiterung mit einem Node im Speicher; settings.collections wie übergeben (undefined:
// ungesetzt).
async function treeStart(collections, collectionsSetting) {
  resetTokens();
  putToken('vm', 'kamran', TOKEN_A);
  putToken('home', 'kamran', TOKEN_B);
  Object.assign(settings, { nodeUrl: 'http://127.0.0.1:7433', hubs: [], accounts: {}, collections: collectionsSetting });
  const node = memNode(collections);
  reply = node;
  start();
  await settle();
  return node;
}

const tp = () => treeView.provider;
const label = (n) => {
  const it = tp().getTreeItem(n);
  return it.label || it.resourceUri.path.split('/').pop();
};
// Die Elemente unter n, als Liste ihrer Namen.
const childNames = async (n) => (await tp().getChildren(n)).map(label);
// Ein Element des Baums nach seinem Pfad unter der Collection address.
async function nodeAt(address, name = '') {
  const roots = await tp().getChildren();
  let n = roots.find((r) => tp().getTreeItem(r).label === address);
  for (const part of name.split('/').filter(Boolean)) n = (await tp().getChildren(n)).find((c) => label(c) === part);
  return n;
}
// Was onDidChangeTreeData meldet, als Liste (undefined: alles).
function treeEvents() {
  const seen = [];
  tp().onDidChangeTreeData((e) => seen.push(e));
  return seen;
}

test('Baum: Wurzel ungesetzt alle lesbaren, gesetzt genau diese mit Hinweis', async () => {
  try {
    await treeStart({ 'vm:omni': OMNI, 'vm:wissen': WISSEN, 'home:notizen': { rights: ['read', 'write'] } });
    assert.strictEqual(treeView.id, 'kephalaion.collections');
    const roots = await tp().getChildren();
    assert.deepStrictEqual(roots.map((r) => tp().getTreeItem(r).label), ['vm:omni', 'vm:wissen', 'home:notizen']);
    const items = roots.map((r) => tp().getTreeItem(r));
    assert.deepStrictEqual(items.map((i) => [i.description, i.contextValue, i.collapsibleState]),
      [[undefined, 'collection-writable', 1], ['nur lesen', 'collection-readonly', 1], [undefined, 'collection-writable', 1]]);
    assert.strictEqual(items[0].id, 'keph://vm/omni');
    assert.strictEqual(items[0].resourceUri.toString(), 'keph://vm/omni');
    assert.ok(executed.some((c) => c[0] === 'setContext' && c[1] === 'kephalaion.signedIn' && c[2] === true));

    // Gesetzt: genau diese, in dieser Reihenfolge; die nicht lesbare mit Hinweis, ohne Kinder.
    const events = treeEvents();
    settings.collections = ['vm:wissen', 'vm:fremd'];
    await configChanged('kephalaion.collections');
    assert.deepStrictEqual(events, [undefined], 'die Einstellung lädt den ganzen Baum neu');
    const set = await tp().getChildren();
    assert.deepStrictEqual(set.map((r) => tp().getTreeItem(r).label), ['vm:wissen', 'vm:fremd']);
    const fremd = tp().getTreeItem(set[1]);
    assert.deepStrictEqual([fremd.description, fremd.contextValue, fremd.collapsibleState], ['nicht lesbar', 'collection-unreadable', 0]);
    assert.deepStrictEqual(await tp().getChildren(set[1]), []);
    settings.collections = [];
    assert.deepStrictEqual(await tp().getChildren(), [], 'leer gesetzt: kein Knoten (viewsWelcome)');
  } finally {
    reply = null;
    settings.collections = undefined;
  }
});

test('Baum: Kinder aus list, Verzeichnisse zuerst; ein Klick öffnet das Dokument', async () => {
  try {
    await treeStart({ 'vm:omni': OMNI });
    const omni = await nodeAt('vm:omni');
    assert.deepStrictEqual(await childNames(omni), ['docs', 'README.md']);
    const docs = await nodeAt('vm:omni', 'docs');
    assert.deepStrictEqual(await childNames(docs), ['sub', 'a.md']);
    const listed = calls.filter((c) => c.params.name === 'list').map((c) => c.params.arguments);
    assert.deepStrictEqual(listed.slice(-1), [{ collection: 'vm:omni', path: 'docs', limit: 1000 }]);
    const a = await nodeAt('vm:omni', 'docs/a.md');
    const item = tp().getTreeItem(a);
    assert.strictEqual(item.id, 'keph://vm/omni/docs/a.md');
    assert.strictEqual(item.contextValue, 'document-writable');
    assert.strictEqual(item.collapsibleState, 0);
    assert.strictEqual(item.command.command, 'vscode.open');
    assert.strictEqual(item.command.arguments[0].toString(), 'keph://vm/omni/docs/a.md');
    assert.strictEqual(tp().getTreeItem(docs).contextValue, 'directory-writable');
    // Dasselbe Element je URI, und getParent führt zurück bis zur Collection.
    assert.strictEqual(await nodeAt('vm:omni', 'docs/a.md'), a);
    assert.strictEqual(tp().getParent(a), docs);
    assert.strictEqual(tp().getParent(docs), omni);
    assert.strictEqual(tp().getParent(omni), undefined);
  } finally {
    reply = null;
  }
});

test('Baum: Änderung aus changes lädt das Verzeichnis darüber neu, gebündelt', async () => {
  try {
    const node = await treeStart({ 'vm:omni': OMNI });
    const docs = await nodeAt('vm:omni', 'docs');
    await nodeAt('vm:omni', 'docs/sub');
    await fsProvider.pollChanges(); // Ausgangspunkt
    const events = treeEvents();
    // Ein anderer legt zwei Dokumente an und ändert eines, das der Baum kennt.
    node.store['vm:omni'].docs.set('docs/neu.md', { id: 'idn', revision: 50, content: '' });
    node.changes.push({ address: 'vm:omni', name: 'docs/neu.md', id: 'idn', revision: 50 },
      { address: 'vm:omni', name: 'docs/sub/c.md', id: 'idc', revision: 51 },
      { address: 'vm:omni', name: 'docs/a.md', id: 'ida', revision: 52 });
    await fsProvider.pollChanges();
    await settle();
    assert.deepStrictEqual(events, [docs], 'genau einmal, nur docs (sub liegt darunter)');
    assert.deepStrictEqual(await childNames(docs), ['sub', 'a.md', 'neu.md']);
  } finally {
    reply = null;
  }
});

test('Baum: das aktive keph://-Dokument wird gezeigt, ohne Fokus', async () => {
  try {
    await treeStart({ 'vm:omni': OMNI, 'vm:wissen': WISSEN }, ['vm:omni']);
    const uri = Uri.parse('keph://vm/omni/docs/sub/b.md');
    vscode.window.activeTextEditor = { document: { uri } };
    for (const fn of editorListeners) fn(vscode.window.activeTextEditor);
    assert.strictEqual(treeView.revealed.length, 1);
    const { el, o } = treeView.revealed[0];
    assert.strictEqual(el.uri.toString(), 'keph://vm/omni/docs/sub/b.md');
    assert.deepStrictEqual(o, { select: true, focus: false, expand: false });
    assert.strictEqual(tp().getParent(tp().getParent(el)).name, 'docs');
    // Nicht gezeigt: nicht sichtbar, fremde Collection, anderes Schema.
    treeView.visible = false;
    for (const fn of editorListeners) fn(vscode.window.activeTextEditor);
    treeView.visible = true;
    vscode.window.activeTextEditor = { document: { uri: Uri.parse('keph://vm/wissen/x.md') } };
    for (const fn of editorListeners) fn(vscode.window.activeTextEditor);
    vscode.window.activeTextEditor = { document: { uri: Uri.parse('file:///p/x.md') } };
    for (const fn of editorListeners) fn(vscode.window.activeTextEditor);
    assert.strictEqual(treeView.revealed.length, 1);
  } finally {
    reply = null;
    settings.collections = undefined;
  }
});

// Ersetzt eine Funktion von vscode.window für fn und stellt sie danach wieder her.
async function withWindow(over, fn) {
  const saved = {};
  for (const k of Object.keys(over)) {
    saved[k] = vscode.window[k];
    vscode.window[k] = over[k];
  }
  try {
    return await fn();
  } finally {
    Object.assign(vscode.window, saved);
  }
}

// Die schreibenden Aufrufe an den Node seit mark, als [Werkzeug, Name, neuer Name].
const writes = (mark) => calls.slice(mark).filter((c) => ['create', 'write', 'rename', 'delete'].includes(c.params.name))
  .map((c) => [c.params.name, c.params.arguments.name, c.params.arguments.new_name].filter((x) => x !== undefined));

test('Baum bearbeiten: neue Datei und neuer Ordner über den Provider, die Datei wird geöffnet', async () => {
  try {
    const node = await treeStart({ 'vm:omni': OMNI });
    const docs = await nodeAt('vm:omni', 'docs');
    const a = await nodeAt('vm:omni', 'docs/a.md');
    let mark = calls.length;
    await withWindow({ showInputBox: async (o) => (o.validateInput('../x') ? 'neu.md' : 'falsch') }, async () => {
      await registered['kephalaion.tree.newFile'](a); // auf einem Dokument: in dessen Verzeichnis
    });
    assert.deepStrictEqual(writes(mark), [['create', 'docs/neu.md']]);
    assert.strictEqual(node.store['vm:omni'].docs.get('docs/neu.md').content, '');
    const opened = executed.find((c) => c[0] === 'vscode.open');
    assert.strictEqual(opened[1].toString(), 'keph://vm/omni/docs/neu.md');
    // Belegt: nichts angelegt, Meldung.
    mark = calls.length;
    await withWindow({ showInputBox: async () => 'a.md' }, () => registered['kephalaion.tree.newFile'](docs));
    assert.deepStrictEqual(writes(mark), []);
    assert.match(shownErrors.pop(), /docs\/a\.md gibt es schon; nichts angelegt/);
    // Neuer Ordner: nur in diesem Fenster gemerkt, der Baum zeigt ihn.
    await withWindow({ showInputBox: async () => 'leer' }, () => registered['kephalaion.tree.newFolder'](docs));
    assert.deepStrictEqual(writes(mark), []);
    await settle();
    assert.deepStrictEqual(await childNames(docs), ['leer', 'sub', 'a.md', 'neu.md']);
    // Abgebrochen: nichts.
    await registered['kephalaion.tree.newFile'](docs);
    assert.deepStrictEqual(writes(mark), []);
  } finally {
    reply = null;
  }
});

test('Baum bearbeiten: neue Datei aus der Titelleiste in der einzigen schreibbaren Collection', async () => {
  try {
    await treeStart({ 'vm:omni': OMNI, 'vm:wissen': WISSEN });
    const mark = calls.length;
    await withWindow({ showInputBox: async () => 'oben.md' }, () => registered['kephalaion.tree.newFile']());
    assert.deepStrictEqual(writes(mark), [['create', 'oben.md']]);
    assert.strictEqual(calls.find((c) => c.params.name === 'create').params.arguments.collection, 'vm:omni');
  } finally {
    reply = null;
  }
});

test('Baum bearbeiten: umbenennen mit vorbelegtem Namen, löschen nur nach Bestätigung', async () => {
  try {
    const node = await treeStart({ 'vm:omni': OMNI });
    const a = await nodeAt('vm:omni', 'docs/a.md');
    let asked;
    let mark = calls.length;
    await withWindow({ showInputBox: async (o) => { asked = o; return 'docs/z.md'; } }, () => registered['kephalaion.tree.rename'](a));
    assert.strictEqual(asked.value, 'docs/a.md');
    assert.deepStrictEqual(asked.valueSelection, [5, 6]);
    assert.deepStrictEqual(writes(mark), [['rename', 'docs/a.md', 'docs/z.md']]);
    // Auf einen belegten Namen: abgelehnt mit der Meldung des Providers, nichts geschrieben.
    const z = await nodeAt('vm:omni', 'docs/z.md');
    mark = calls.length;
    await withWindow({ showInputBox: async () => 'README.md' }, () => registered['kephalaion.tree.rename'](z));
    assert.deepStrictEqual(writes(mark), []);
    assert.strictEqual(shownErrors.length, 1);

    // Löschen: abgelehnte Bestätigung schreibt nichts.
    const sub = await nodeAt('vm:omni', 'docs/sub');
    let question;
    mark = calls.length;
    await withWindow({ showWarningMessage: async (m, o, ...b) => { question = { m, o, b }; return undefined; } },
      () => registered['kephalaion.tree.delete'](sub, [sub, z]));
    assert.deepStrictEqual(writes(mark), []);
    assert.strictEqual(question.o.modal, true);
    assert.match(question.m, /2 Einträge löschen\?/);
    assert.deepStrictEqual(question.b, ['Löschen']);
    // Bestätigt: das Verzeichnis recursive, das Dokument einzeln.
    await withWindow({ showWarningMessage: async () => 'Löschen' }, () => registered['kephalaion.tree.delete'](sub, [sub, z]));
    assert.deepStrictEqual(writes(mark), [['delete', 'docs/sub'], ['delete', 'docs/z.md']]);
    const del = calls.filter((c) => c.params.name === 'delete').map((c) => c.params.arguments.recursive);
    assert.deepStrictEqual(del, [true, false]);
    assert.deepStrictEqual([...node.store['vm:omni'].docs.keys()], ['README.md']);
  } finally {
    reply = null;
  }
});

test('Baum bearbeiten: Fehler des Providers als Meldung, zur Seite öffnen, kopieren', async () => {
  try {
    await treeStart({ 'vm:wissen': WISSEN });
    const x = await nodeAt('vm:wissen', 'x.md');
    assert.strictEqual(tp().getTreeItem(x).contextValue, 'document-readonly');
    await withWindow({ showWarningMessage: async () => 'Löschen' }, () => registered['kephalaion.tree.delete'](x));
    assert.match(shownErrors.pop(), /^Kephalaion: /);
    registered['kephalaion.tree.openToSide'](x);
    assert.deepStrictEqual(executed.filter((c) => c[0] === 'vscode.open').map((c) => [c[1].toString(), c[2]]),
      [['keph://vm/wissen/x.md', -2]]);
    await registered['kephalaion.tree.copyUri'](x);
    await registered['kephalaion.tree.copyRelativePath'](x);
    assert.deepStrictEqual(clipboard.splice(0), ['keph://vm/wissen/x.md', 'x.md']);
  } finally {
    reply = null;
  }
});

// Ein DataTransfer, wie VS Code ihn beim Ablegen übergibt: aus dem eigenen Baum die Elemente,
// aus dem Explorer oder vom Betriebssystem text/uri-list.
function ownDrop(...els) {
  const dt = new vscode.DataTransfer();
  dt.set('application/vnd.code.tree.kephalaion.collections', new vscode.DataTransferItem(els));
  return dt;
}
function uriDrop(...uris) {
  const dt = new vscode.DataTransfer();
  dt.set('text/uri-list', new vscode.DataTransferItem(uris.map((u) => u.toString()).join('\r\n')));
  return dt;
}
const dnd = () => treeView.options.dragAndDropController;

test('Ziehen: aus dem Baum als text/uri-list, Mime-Typen', async () => {
  try {
    await treeStart({ 'vm:omni': OMNI });
    assert.deepStrictEqual(dnd().dragMimeTypes, ['text/uri-list']);
    assert.deepStrictEqual(dnd().dropMimeTypes, ['application/vnd.code.tree.kephalaion.collections', 'text/uri-list']);
    const dt = new vscode.DataTransfer();
    dnd().handleDrag([await nodeAt('vm:omni', 'docs/a.md'), await nodeAt('vm:omni', 'README.md')], dt);
    assert.strictEqual(await dt.get('text/uri-list').asString(), 'keph://vm/omni/docs/a.md\r\nkeph://vm/omni/README.md');
  } finally {
    reply = null;
  }
});

test('Ablegen: verschieben in der Collection, abgelehnt zwischen Collections', async () => {
  try {
    const node = await treeStart({ 'vm:omni': OMNI, 'vm:wissen': { rights: ['read', 'write'], docs: { 'x.md': 'x' } } });
    const a = await nodeAt('vm:omni', 'docs/a.md');
    const sub = await nodeAt('vm:omni', 'docs/sub');
    let mark = calls.length;
    await dnd().handleDrop(await nodeAt('vm:omni', 'README.md'), ownDrop(a, sub)); // auf ein Dokument: in sein Verzeichnis
    assert.deepStrictEqual(writes(mark), [['rename', 'docs/a.md', 'a.md'], ['rename', 'docs/sub', 'sub']]);
    assert.deepStrictEqual([...node.store['vm:omni'].docs.keys()].sort(), ['README.md', 'a.md', 'sub/b.md']);
    // Zwischen Collections: nichts, Meldung — auch kein Kopieren.
    mark = calls.length;
    await dnd().handleDrop(await nodeAt('vm:omni'), ownDrop(await nodeAt('vm:wissen', 'x.md')));
    assert.deepStrictEqual(writes(mark), []);
    assert.match(shownErrors.pop(), /nur innerhalb einer Collection.*kein Kopieren zwischen Collections.*nichts geändert/);
  } finally {
    reply = null;
  }
});

test('Ablegen: Kopie aus file:// mit Ordner, belegtes Ziel abgelehnt, nur lesen abgelehnt', async () => {
  const dir = fs.mkdtempSync(path.join(home, 'projekt-'));
  fs.writeFileSync(path.join(dir, 'notiz.md'), '# Notiz\n');
  fs.mkdirSync(path.join(dir, 'ordner', 'tief'), { recursive: true });
  fs.writeFileSync(path.join(dir, 'ordner', 'b.md'), 'b');
  fs.writeFileSync(path.join(dir, 'ordner', 'tief', 'c.md'), 'c');
  fs.writeFileSync(path.join(dir, 'ordner', 'bild.bin'), Buffer.from([0, 1, 2]));
  fs.writeFileSync(path.join(dir, 'a.md'), 'überschreibt nicht');
  try {
    const node = await treeStart({ 'vm:omni': OMNI, 'vm:wissen': WISSEN });
    const docs = await nodeAt('vm:omni', 'docs');
    let mark = calls.length;
    await dnd().handleDrop(docs, uriDrop(Uri.file(path.join(dir, 'notiz.md')), Uri.file(path.join(dir, 'ordner'))));
    assert.deepStrictEqual(writes(mark).sort(), [['create', 'docs/notiz.md'], ['create', 'docs/ordner/b.md'],
      ['create', 'docs/ordner/tief/c.md']]);
    assert.strictEqual(node.store['vm:omni'].docs.get('docs/notiz.md').content, '# Notiz\n');
    // Die Binärdatei geht nicht, hält die anderen nicht auf; die Meldung sagt es.
    assert.match(shownErrors.pop(), /1 nicht kopiert \(3 kopiert\).*bild\.bin.*keine Textdatei/);
    // Belegt: nichts überschrieben.
    mark = calls.length;
    await dnd().handleDrop(docs, uriDrop(Uri.file(path.join(dir, 'a.md'))));
    assert.deepStrictEqual(writes(mark), []);
    assert.match(shownErrors.pop(), /keph:\/\/vm\/omni\/docs\/a\.md ist belegt; nichts kopiert, nichts überschrieben/);
    assert.strictEqual(node.store['vm:omni'].docs.get('docs/a.md').content, '# A\n');
    // Eine Collection ohne write: nichts.
    await dnd().handleDrop(await nodeAt('vm:wissen'), uriDrop(Uri.file(path.join(dir, 'notiz.md'))));
    assert.deepStrictEqual(writes(mark), []);
    assert.match(shownErrors.pop(), /vm:wissen nur lesen/);
  } finally {
    reply = null;
  }
});

test('In den Projektordner kopieren: Dokument und Verzeichnis, nie überschreiben', async () => {
  const dir = fs.mkdtempSync(path.join(home, 'ziel-'));
  fs.writeFileSync(path.join(dir, 'README.md'), 'eigen');
  try {
    await treeStart({ 'vm:omni': OMNI });
    vscode.workspace.workspaceFolders = [{ name: 'p', index: 0, uri: Uri.file(dir) }];
    let asked;
    await withWindow({ showOpenDialog: async (o) => { asked = o; return [Uri.file(dir)]; } },
      async () => registered['kephalaion.tree.copyToProject'](undefined,
        [await nodeAt('vm:omni', 'docs'), await nodeAt('vm:omni', 'README.md')]));
    assert.strictEqual(asked.defaultUri.path, dir);
    assert.strictEqual(asked.canSelectFolders, true);
    assert.strictEqual(fs.readFileSync(path.join(dir, 'docs', 'a.md'), 'utf8'), '# A\n');
    assert.strictEqual(fs.readFileSync(path.join(dir, 'docs', 'sub', 'b.md'), 'utf8'), '# B\n');
    assert.strictEqual(fs.readFileSync(path.join(dir, 'README.md'), 'utf8'), 'eigen', 'überschrieben');
    assert.match(shownErrors.pop(), /README\.md ist belegt/);
  } finally {
    reply = null;
  }
});

test('Dokument öffnen: list mit recursive über alle gezeigten Collections, Blättern über cursor', async () => {
  try {
    await treeStart({ 'vm:omni': OMNI, 'vm:wissen': WISSEN, 'vm:aus': { rights: ['read'], docs: { 'n.md': '' } } },
      ['vm:omni', 'vm:wissen', 'vm:fremd']);
    // Seiten zu zwei Einträgen: der Node blättert, die Erweiterung fragt bis zum Ende.
    const paged = memNode({ 'vm:omni': OMNI, 'vm:wissen': WISSEN, 'vm:aus': { rights: ['read'] } }, { pageSize: 2 });
    reply = paged;
    let items;
    let opts;
    await withWindow({ showQuickPick: async (p, o) => { items = await p; opts = o; return items[2]; } },
      () => registered['kephalaion.openDocument']());
    assert.deepStrictEqual(items.map((i) => [i.label, i.description]),
      [['README.md', 'vm:omni'], ['docs/a.md', 'vm:omni'], ['docs/sub/b.md', 'vm:omni'], ['x.md', 'vm:wissen']]);
    assert.strictEqual(opts.matchOnDescription, true);
    const lists = calls.filter((c) => c.params.name === 'list').map((c) => c.params.arguments);
    assert.deepStrictEqual(lists.filter((a) => a.collection === 'vm:omni'), [
      { collection: 'vm:omni', recursive: true, limit: 1000 },
      { collection: 'vm:omni', recursive: true, limit: 1000, cursor: '2' },
    ]);
    assert.ok(!lists.some((a) => a.collection === 'vm:aus'), 'nicht gezeigte Collection gelistet');
    assert.strictEqual(executed.find((c) => c[0] === 'vscode.open')[1].toString(), 'keph://vm/omni/docs/sub/b.md');
  } finally {
    reply = null;
    settings.collections = undefined;
  }
});

test('Suchen: search des Nodes, nur gezeigte Collections, Treffer öffnet an der Überschrift', async () => {
  try {
    const node = await treeStart({
      'vm:omni': { rights: ['read'], docs: { 'docs/setup.md': '---\ntitle: Setup\n---\n# Setup\n\ntext\n\n## Install it\n\nmore\n' } },
      'vm:aus': { rights: ['read'] },
    }, ['vm:omni']);
    node.searchResults = [
      { address: 'vm:omni', hits: [
        { rank: 1, name: 'docs/setup.md', title: 'Setup', section: { heading: 'Install it', headings: ['Setup', 'Install it'], anchor: 'install-it' } },
        { rank: 2, name: 'docs/setup.md', title: 'Setup', section: { heading: '', headings: [], anchor: '' } },
      ] },
      { address: 'vm:aus', hits: [{ rank: 1, name: 'docs/x.md', title: 'X', section: { heading: 'X', headings: ['X'], anchor: 'x' } }] },
    ];
    let prompt;
    let items;
    const pick = async (i) => {
      shownDocuments.length = 0;
      await withWindow({
        showInputBox: async (o) => { prompt = o; return ' install '; },
        showQuickPick: async (p) => { items = await p; return items[i]; },
      }, () => registered['kephalaion.search']());
    };
    await pick(0);
    assert.match(prompt.placeHolder, /englische Wörter.*nur unter docs\//);
    assert.deepStrictEqual(calls.filter((c) => c.params.name === 'search').map((c) => c.params.arguments), [{ query: 'install', limit: 20 }]);
    assert.deepStrictEqual(items.map((i) => [i.label, i.description, i.detail]), [
      ['Setup', 'vm:omni', 'Setup › Install it — docs/setup.md'], ['Setup', 'vm:omni', 'docs/setup.md']]);
    assert.strictEqual(shownDocuments[0].doc.uri.toString(), 'keph://vm/omni/docs/setup.md');
    assert.strictEqual(shownDocuments[0].options.selection.start.line, 7);
    // Vor der ersten Überschrift: Anfang des Dokuments.
    await pick(1);
    assert.strictEqual(shownDocuments[0].options.selection.start.line, 0);
    // Nichts gefunden: Hinweis mit der Grenze.
    node.searchResults = [];
    await withWindow({ showInputBox: async () => 'nothing' }, () => registered['kephalaion.search']());
    assert.match(shownInfos.pop(), /nichts gefunden für „nothing“ in docs\//);
  } finally {
    reply = null;
    settings.collections = undefined;
  }
});

// --- Auswahl der Collections und der Schalter kephalaion.workspaceFolders (Etappe 6) ---

const kfolder = (p, name = `Keph ${p}`) => ({ name, uri: Uri.parse(`keph://vm${p}`) });
const pfolder = (name) => ({ name, uri: Uri.file(`/home/k/${name}`) });
const folderUris = () => vscode.workspace.workspaceFolders.map((f) => f.uri.toString());
// Wartet, bis die Warteschlange der Erweiterung leer ist (Ereignisse kommen nach 5 ms).
const quiet = () => new Promise((r) => setTimeout(r, 120));

test('Auswahl: zeigen und herausnehmen schreiben kephalaion.collections auf Workspace-Ebene, ungesetzt von den gezeigten aus', async () => {
  try {
    vscode.workspace.workspaceFolders = [pfolder('projekt')];
    await treeStart({ 'vm:omni': OMNI, 'vm:wissen': WISSEN, 'home:notizen': { rights: ['read'] } });
    // Ungesetzt zeigt der Baum alle: „zeigen“ hat nichts anzubieten.
    await registered['kephalaion.showInTree']();
    assert.match(shownInfos.pop(), /schon im Baum/);
    assert.deepStrictEqual(settingUpdates, []);
    // Herausnehmen aus dem Kontextmenü: das erste Mal die gezeigten minus diese.
    await registered['kephalaion.hideFromTree'](await nodeAt('vm:wissen'));
    assert.deepStrictEqual(settingUpdates.pop(), { key: 'collections', value: ['vm:omni', 'home:notizen'], target: 2 });
    // Zeigen: nur die nicht gezeigten, ans Ende.
    let offered;
    await withWindow({ showQuickPick: async (items) => { offered = items; return items[0]; } },
      () => registered['kephalaion.showInTree']());
    assert.deepStrictEqual(offered.map((i) => i.label), ['vm:wissen']);
    assert.deepStrictEqual(settingUpdates.pop(), { key: 'collections', value: ['vm:omni', 'home:notizen', 'vm:wissen'], target: 2 });
    assert.ok(executed.some((c) => c[0] === 'kephalaion.collections.focus'));
    // Ohne offenen Workspace: in die Einstellungen des Benutzers.
    vscode.workspace.workspaceFolders = [];
    await registered['kephalaion.hideFromTree'](await nodeAt('vm:omni'));
    assert.deepStrictEqual(settingUpdates.pop(), { key: 'collections', value: ['home:notizen', 'vm:wissen'], target: 1 });
  } finally {
    reply = null;
    settings.collections = undefined;
  }
});

test('Schalter mit true (Vorgabe): keine Ordner angefasst, keine Einstellung geschrieben', async () => {
  try {
    vscode.workspace.workspaceFolders = [pfolder('projekt'), kfolder('/omni')];
    settings.workspaceFolders = undefined;
    await treeStart({ 'vm:omni': OMNI });
    await quiet();
    assert.deepStrictEqual(folderUpdates, []);
    assert.deepStrictEqual(settingUpdates.filter((u) => u.key === 'collections'), []);
    assert.strictEqual(workspaceStore.size, 0);
  } finally {
    reply = null;
  }
});

test('Schalter false: nur keph://-Ordner heraus, Bereich für Bereich von hinten; true hängt genau diese wieder ein', async () => {
  try {
    vscode.workspace.workspaceFolders = [pfolder('projekt'), kfolder('/omni'), kfolder('/wissen/docs'), pfolder('zwei'),
      kfolder('/omni/sub'), pfolder('drei')];
    settings.workspaceFolders = false;
    await treeStart({ 'vm:omni': OMNI, 'vm:wissen': WISSEN, 'vm:aus': { rights: ['read'] } }, ['vm:aus']);
    await quiet();
    assert.deepStrictEqual(folderUpdates, [[4, 1], [1, 2]]);
    assert.strictEqual(folderOverlaps, 0, 'ein Aufruf vor dem Ereignis des vorigen');
    assert.deepStrictEqual(folderUris(), ['file:///home/k/projekt', 'file:///home/k/zwei', 'file:///home/k/drei']);
    // Gesetzte Einstellung: die Collections der Ordner kommen dazu (Unterpfad abgeschnitten).
    assert.deepStrictEqual(settingUpdates.filter((u) => u.key === 'collections').map((u) => u.value),
      [['vm:aus', 'vm:omni', 'vm:wissen']]);
    assert.ok(logs.some((l) => l.includes('keph://vm/wissen/docs zeigt nur einen Teil — im Baum als ganze Collection vm:wissen')));
    assert.deepStrictEqual(workspaceStore.get('kephalaion.removedFolders').map((r) => r.uri),
      ['keph://vm/omni', 'keph://vm/wissen/docs', 'keph://vm/omni/sub']);

    // Von außen kommt ein keph://-Ordner dazu: wieder heraus, und gemerkt.
    vscode.workspace.workspaceFolders.push(kfolder('/neu'));
    folderUpdates.length = 0;
    await workspaceChanged();
    await quiet();
    assert.deepStrictEqual(folderUpdates, [[3, 1]]);
    assert.strictEqual(workspaceStore.get('kephalaion.removedFolders').length, 4);

    // Zurück auf true: die gemerkten am Ende, in einem Aufruf; ein fremder Ordner bleibt außen vor.
    folderUpdates.length = 0;
    settings.workspaceFolders = true;
    await configChanged('kephalaion.workspaceFolders');
    await quiet();
    assert.deepStrictEqual(folderUpdates, [[3, 0, 'keph://vm/omni Keph /omni', 'keph://vm/wissen/docs Keph /wissen/docs',
      'keph://vm/omni/sub Keph /omni/sub', 'keph://vm/neu Keph /neu']]);
    assert.strictEqual(workspaceStore.has('kephalaion.removedFolders'), false);
    // Noch einmal true: nichts mehr zu tun.
    folderUpdates.length = 0;
    await configChanged('kephalaion.workspaceFolders');
    await quiet();
    assert.deepStrictEqual(folderUpdates, []);
  } finally {
    reply = null;
    settings.collections = undefined;
    settings.workspaceFolders = undefined;
  }
});

test('Schalter false: Index 0, Neustart mitten im Herausnehmen, reiner Collection-Workspace bleibt', async () => {
  try {
    // Index 0: Die Erweiterung merkt sich die Ordner vorher — ein Neustart danach macht weiter.
    vscode.workspace.workspaceFolders = [kfolder('/omni'), pfolder('projekt'), kfolder('/wissen')];
    settings.workspaceFolders = false;
    await treeStart({ 'vm:omni': OMNI, 'vm:wissen': WISSEN });
    await quiet();
    assert.deepStrictEqual(folderUpdates, [[2, 1], [0, 1]]);
    assert.ok(logs.some((l) => l.includes('Index 0: VS Code startet den Extension Host neu')));
    assert.deepStrictEqual(settingUpdates.filter((u) => u.key === 'collections'), [], 'ungesetzt bleibt ungesetzt');
    // Neustart, als wäre der Ordner an Index 0 noch da (VS Code startete vor dem Ereignis neu):
    // der Plan entsteht neu aus dem Ist-Zustand.
    stop();
    vscode.workspace.workspaceFolders.unshift(kfolder('/omni'));
    folderUpdates.length = 0;
    start();
    await quiet();
    assert.deepStrictEqual(folderUpdates, [[0, 1]]);
    assert.deepStrictEqual(workspaceStore.get('kephalaion.removedFolders').map((r) => r.uri), ['keph://vm/wissen', 'keph://vm/omni']);

    // Nur Collections: nichts heraus, einmal ein Hinweis.
    stop();
    vscode.workspace.workspaceFolders = [kfolder('/omni'), kfolder('/wissen')];
    folderUpdates.length = 0;
    shownInfos.length = 0;
    start();
    await quiet();
    await workspaceChanged();
    await quiet();
    assert.deepStrictEqual(folderUpdates, []);
    assert.strictEqual(shownInfos.filter((m) => m.includes('nur aus Collections')).length, 1);
    assert.ok(logs.some((l) => l.includes('besteht nur aus keph://-Ordnern — nichts herausgenommen')));
  } finally {
    reply = null;
    settings.workspaceFolders = undefined;
  }
});

test('Collection einbinden mit false: im Baum zeigen statt einen Ordner einzuhängen', async () => {
  try {
    vscode.workspace.workspaceFolders = [pfolder('projekt')];
    settings.workspaceFolders = false;
    await treeStart({ 'vm:omni': OMNI, 'vm:wissen': WISSEN }, ['vm:omni']);
    await withWindow({ showQuickPick: async (items) => items.find((i) => i.label === 'vm:wissen') },
      () => registered['kephalaion.addCollection']());
    assert.deepStrictEqual(folderUpdates, []);
    assert.deepStrictEqual(settingUpdates.pop(), { key: 'collections', value: ['vm:omni', 'vm:wissen'], target: 2 });
    assert.ok(executed.some((c) => c[0] === 'kephalaion.collections.focus'));
  } finally {
    reply = null;
    settings.collections = undefined;
    settings.workspaceFolders = undefined;
  }
});
