// Kephalaion in VS Code: Status aus whoami und ein FileSystemProvider für keph://.
// Lesen über list, read und changes aus der Replica; schreiben über create, write, delete
// und rename, die der Node an den Hub reicht. Siehe docs/vscode.md.

const vscode = require('vscode');
const fs = require('fs');
const os = require('os');
const path = require('path');
const rules = require('./rules');
const pause = require('./pause');

const SCHEME = 'keph';
const POLL_MS = 30000;

// --- Orte (docs/konzept.md, „Orte nach XDG“; docs/installation.md, „Verwalten“) ---

// Der feste Ort der globalen config (config.SystemConfig). Tests lenken ihn über setSystemPath in
// ein temporäres Verzeichnis — nur dafür, wie SystemPath im Binary: keine Umgebungsvariable, keine
// Einstellung.
let systemPath = '/etc/kephalaion/config.yaml';

function setSystemPath(p) {
  systemPath = p;
}

// Das Verzeichnis der config des Users, darin tokens/ — auch wenn die globale config gilt
// (assistant.TokensDir): Die Token-Dateien bleiben beim User.
function configDir() {
  return rules.userConfigDir(process.env, os.homedir()) || path.join(os.homedir(), '.config', 'kephalaion');
}

function tokensDir() {
  return path.join(configDir(), 'tokens');
}

// Gibt es die Datei? undefined, sonst der Fehlercode — für rules.locateConfig.
function statCode(p) {
  try {
    fs.statSync(p);
    return undefined;
  } catch (e) {
    return e.code || 'EIO';
  }
}

// Die Suche nach der config wie das Binary (rules.locateConfig): KEPHALAION_CONFIG > die des Users,
// wenn es sie gibt > die globale, wenn es sie gibt > der Ort des Users.
function locateConfig() {
  return rules.locateConfig({ env: process.env, home: os.homedir(), systemPath, stat: statCode, cwd: process.cwd() });
}

// Die Warnung, wenn es die config des Users und die globale nebeneinander gibt — derselbe Fehler
// mit demselben Weg wie bei kephalaion status; undefined sonst.
function bothKindsWarning(loc) {
  if (!loc || !loc.bothKinds) return undefined;
  return `zwei Arten der Installation auf diesem Rechner — es gibt die config pro User (${loc.userPath}) und die globale `
    + `(${loc.systemPath}); es gilt die pro User. Weg: die Installation pro User entfernen (docs/installation.md, `
    + '„Installation pro User entfernen“)';
}

// listen aus dem Abschnitt node: der gefundenen config (rules.parseNodeListen; fehlt listen, gilt
// 127.0.0.1:7433 wie im Binary). Liefert { listen } oder { error, reason }: no_config (die Datei
// fehlt; die Meldung nennt die geprüften Orte), config_unreadable (ein anderer Fehler beim Lesen)
// oder node_not_set_up (kein Abschnitt node:).
function nodeListen(loc) {
  if (loc.error) return { error: `keine config: ${loc.error}`, reason: 'no_config' };
  let text;
  try {
    text = fs.readFileSync(loc.path, 'utf8');
  } catch (e) {
    if (e.code === 'ENOENT') {
      return { error: `keine config gefunden (geprüft: ${loc.checked.join(', ')}) — kephalaion node init, oder `
        + 'kephalaion.nodeUrl setzen', reason: 'no_config' };
    }
    return { error: `config ${loc.path} nicht lesbar (${e.code || e.message})`, reason: 'config_unreadable' };
  }
  const r = rules.parseNodeListen(text);
  if (r.reason) return { error: `Node nicht eingerichtet in ${loc.path} (kein Abschnitt node:) — kephalaion node init`, reason: r.reason };
  return r;
}

// Die Adresse des Nodes: kephalaion.nodeUrl, geprüft wie node dir --node (rules.parseNodeUrl:
// http nur zu Loopback und host.docker.internal, sonst https), sonst listen aus der gefundenen
// config. Liefert { endpoint, remote } oder { error, reason? } — die Ursache nach nodeListen, wenn
// es eine der config ist; dazu location, die gefundene config (ohne nodeUrl).
function nodeTarget() {
  const set = vscode.workspace.getConfiguration('kephalaion').get('nodeUrl');
  if (set) {
    try {
      return rules.parseNodeUrl(set);
    } catch (e) {
      return { error: e.message };
    }
  }
  const loc = locateConfig();
  const r = nodeListen(loc);
  if (r.error) return { error: r.error, reason: r.reason, location: loc };
  return { endpoint: `http://${r.listen}/mcp`, remote: false, location: loc };
}

function nodeUrl() {
  return nodeTarget().endpoint;
}

// Die Wahl der Hubs für eine entfernte nodeUrl (kephalaion.hubs).
function chosenHubs() {
  return vscode.workspace.getConfiguration('kephalaion').get('hubs');
}

// Welche Hubs ihre Header-Paare an den Node schicken: lokal alle unter tokens/, an eine
// entfernte nodeUrl nur die gewählten (rules.selectHubs). { hubs } oder { hubs: [], error }.
function hubSelection() {
  const t = nodeTarget();
  return rules.selectHubs(Object.keys(tokenAccounts()), chosenHubs(), Boolean(t.remote));
}

// Token-Dateien: tokens/<hub>/<account>.token; *.pending wird übergangen.
// Liefert je Hub die Accounts, nach Namen sortiert.
function tokenAccounts() {
  const base = tokensDir();
  const out = {};
  let hubs = [];
  try {
    hubs = fs.readdirSync(base, { withFileTypes: true }).filter((d) => d.isDirectory());
  } catch {
    return out;
  }
  for (const hub of hubs) {
    let files = [];
    try {
      files = fs.readdirSync(path.join(base, hub.name)).filter((f) => f.endsWith('.token')).sort();
    } catch {
      continue;
    }
    if (files.length) out[hub.name] = files.map((f) => f.slice(0, -'.token'.length));
  }
  return out;
}

// Die Überschreibung je Hub steht in der Einstellung kephalaion.accounts ({hub: account}).
function chosenAccounts() {
  return vscode.workspace.getConfiguration('kephalaion').get('accounts') || {};
}

// Der Standard-Account je Hub aus tokens/<hub>/default (Task 037; kephalaion node account default):
// {hub: { account } | { error }}; ein Hub ohne die Datei fehlt. Gelesen wird nur die erste Zeile,
// ein Name, nie ein Token; ein Fehler nennt den Inhalt nicht.
function defaultAccounts() {
  const base = tokensDir();
  const out = {};
  let hubs = [];
  try {
    hubs = fs.readdirSync(base, { withFileTypes: true }).filter((d) => d.isDirectory());
  } catch {
    return out;
  }
  for (const hub of hubs) {
    let text;
    try {
      text = fs.readFileSync(path.join(base, hub.name, 'default'), 'utf8');
    } catch (e) {
      if (e.code !== 'ENOENT') out[hub.name] = { error: e.code || 'nicht lesbar' };
      continue;
    }
    out[hub.name] = rules.parseDefaultAccount(text);
  }
  return out;
}

// Welcher Account je Hub benutzt wird und warum — dieselbe Reihenfolge wie die CLI
// (rules.resolveAccount): override (Einstellung kephalaion.accounts), default (Standard), only
// (einzige Token-Datei). Fehlt die Wahl (missing) oder hat die Überschreibung keine Token-Datei,
// steht der Hub ohne account und ohne token da: Kein Paar geht hinaus, Statusleiste und Tooltip
// nennen die Kandidaten. Nur die Hubs aus hubSelection: An eine entfernte nodeUrl gehen nur die
// gewählten.
function readCredentials(log) {
  const base = tokensDir();
  const chosen = chosenAccounts();
  const standards = defaultAccounts();
  const creds = {};
  const all = tokenAccounts();
  for (const hub of hubSelection().hubs) {
    const accounts = all[hub];
    if (!accounts) continue;
    const r = rules.resolveAccount(accounts, chosen[hub], standards[hub]);
    for (const n of r.notes) log(`Hub ${hub}: ${n}`);
    if (!r.account) {
      if (r.missing) log(`Hub ${hub}: gewählter Account ${r.missing} hat keine Token-Datei — kein Rückfall`);
      creds[hub] = { account: undefined, token: '', tokenId: '-', accounts, how: r.how, missing: r.missing, notes: r.notes };
      continue;
    }
    const { account, how } = r;
    const file = path.join(base, hub, `${account}.token`);
    let token = '';
    try {
      token = fs.readFileSync(file, 'utf8').split('\n')[0].trim();
    } catch (e) {
      log(`Hub ${hub}: Token-Datei ${account}.token nicht lesbar: ${e.message}`);
    }
    // Die Kennung der Token-Datei (Größe und Zeit, ohne Inhalt) für die Pausendatei: Ändert sie
    // sich, ist das Paar neu zu proben (pause.js).
    let tokenId = '-';
    try {
      const st = fs.statSync(file);
      tokenId = `${st.size}:${st.mtimeMs}`;
    } catch {
      // bleibt '-'
    }
    creds[hub] = { account, token, tokenId, accounts, how, notes: r.notes };
  }
  return creds;
}

// --- Der Node als MCP-Server für Copilot (docs/vscode.md, „MCP-Server für Copilot“) ---
// Gemeldet über vscode.lm.registerMcpServerDefinitionProvider. Die gemeldete Definition trägt
// weder Header noch Token: VS Code speichert sie zwischen (mcp.extCachedServers im Speicher des
// Workspace). Die Header-Paare setzt erst resolveMcpServerDefinition ein, wenn VS Code den
// Server startet; die Verbindung baut der Extension Host auf, in dem die Erweiterung läuft
// (workspace: unter WSL im Linux) — wie die Erweiterung selbst den Node erreicht.

const MCP_PROVIDER_ID = 'kephalaion.node';
const MCP_LABEL = 'Kephalaion';

function mcpEnabled() {
  return vscode.workspace.getConfiguration('kephalaion').get('mcpServer.enabled', true) !== false;
}

// Protokolliert der Extension Host auf Trace, schreibt VS Code die Header jeder Anfrage an einen
// MCP-Server ins Log (nur Authorization wird verdeckt) — mit dem Token. Solange das gilt,
// meldet die Erweiterung den Server nicht.
function traceLogging() {
  return vscode.env.logLevel === vscode.LogLevel.Trace;
}

// Welcher Account je Hub in den MCP-Eintrag kommt — wie kephalaion node mcp headers, nur für
// die Hubs aus hubSelection (rules.mcpLogins): Einstellung, Standard, einzige Token-Datei. Nur
// Namen, kein Token.
function mcpLogins() {
  const sel = hubSelection();
  const out = rules.mcpLogins(tokenAccounts(), chosenAccounts(), sel.hubs, defaultAccounts());
  if (sel.error) out.skipped.push(sel.error);
  return out;
}

// Die Header-Paare für den Start des Servers, aus den Token-Dateien; ein pausiertes Paar (paused:
// { hub: … }, Task 025) fehlt. Meldungen nennen nie ein Token.
function mcpHeaders(log, paused) {
  const base = tokensDir();
  const { logins, skipped, notes } = mcpLogins();
  for (const s of skipped) log(`MCP-Server: Hub ${s}`);
  for (const n of notes) log(`MCP-Server: Hub ${n}`);
  const active = logins.filter((l) => {
    if (!paused || !paused[l.hub]) return true;
    log(`MCP-Server: Hub ${l.hub}: Paar ${l.account} pausiert (Anmeldung ungültig), nicht an den Server`);
    return false;
  });
  return rules.mcpHeaders(active,
    (hub, account) => fs.readFileSync(path.join(base, hub, `${account}.token`), 'utf8').split('\n')[0].trim(), log);
}

// Stand, an dem sich zeigt, ob die Definition neu zu melden ist: Adresse, Einstellung, Trace,
// Hubs mit ihren Accounts, die Token-Dateien und je Hub die Datei default des Standards (Name,
// Größe, Zeit) — ohne ihren Inhalt. So wird auch ein neuer Standard neu gemeldet (Task 037).
function mcpState() {
  const base = tokensDir();
  const files = [];
  for (const [hub, accounts] of Object.entries(tokenAccounts())) {
    for (const a of accounts) {
      try {
        const st = fs.statSync(path.join(base, hub, `${a}.token`));
        files.push(`${hub}/${a}:${st.size}:${st.mtimeMs}`);
      } catch {
        files.push(`${hub}/${a}:-`);
      }
    }
  }
  let hubDirs = [];
  try {
    hubDirs = fs.readdirSync(base, { withFileTypes: true }).filter((d) => d.isDirectory()).map((d) => d.name);
  } catch {
    // kein tokens/: kein Standard
  }
  for (const hub of hubDirs) {
    try {
      const st = fs.statSync(path.join(base, hub, 'default'));
      files.push(`${hub}/default:${st.size}:${st.mtimeMs}`);
    } catch {
      // kein Standard an diesem Hub
    }
  }
  return JSON.stringify([nodeUrl() || '', mcpEnabled(), traceLogging(), chosenAccounts(), chosenHubs() || [],
    files.sort()]);
}

class McpProvider {
  constructor(log, node) {
    this.log = log;
    this.node = node;
    this._changed = new vscode.EventEmitter();
    this.onDidChangeMcpServerDefinitions = this._changed.event;
    this._state = mcpState();
    this._warned = false;
  }

  // Neu melden, wenn sich etwas geändert hat, das die Definition oder die Header betrifft.
  check() {
    const s = mcpState();
    if (s === this._state) return;
    this._state = s;
    this.log('MCP-Server: Stand geändert, neu gemeldet');
    this._changed.fire();
  }

  provideMcpServerDefinitions() {
    const target = nodeTarget();
    const url = target.endpoint;
    if (!mcpEnabled()) return [];
    if (!url) {
      this.log(`MCP-Server nicht gemeldet: ${target.error}`);
      return [];
    }
    // An eine entfernte nodeUrl ohne eindeutige Wahl der Hubs: kein Server, keine Tokens.
    const sel = hubSelection();
    if (sel.error) {
      this.log(`MCP-Server nicht gemeldet: ${sel.error}`);
      return [];
    }
    if (traceLogging()) {
      if (!this._warned) {
        this._warned = true;
        this.log('MCP-Server nicht gemeldet: Der Extension Host protokolliert auf Trace, VS Code schriebe das Token ins Log');
        vscode.window.showWarningMessage('Kephalaion: Solange VS Code auf „Trace“ protokolliert, wird der MCP-Server nicht '
          + 'gemeldet — VS Code schriebe sonst das Token ins Log. Log-Level zurücksetzen („Developer: Set Log Level…“).');
      }
      return [];
    }
    this._warned = false;
    // version: die Hubs mit ihren Accounts — ändern sie sich, liest VS Code die Werkzeuge neu.
    const version = mcpLogins().logins.map((l) => `${l.hub}=${l.account}`).join(',') || 'ohne Anmeldung';
    return [new vscode.McpHttpServerDefinition(MCP_LABEL, vscode.Uri.parse(url), {}, version)];
  }

  // Beim Start des Servers: An eine entfernte Adresse erst nach der Probe (Schranke, pause.js),
  // ohne die pausierten Paare. Ein laufender Server behält seine Header.
  async resolveMcpServerDefinition(server) {
    if (!mcpEnabled() || traceLogging()) return undefined;
    await this.node.ready();
    server.headers = mcpHeaders(this.log, this.node.paused());
    const hubs = Object.keys(server.headers).filter((h) => h.startsWith('X-Keph-Account-'))
      .map((h) => `${h.slice('X-Keph-Account-'.length)}=${server.headers[h]}`);
    this.log(`MCP-Server gestartet: ${server.uri}, ${hubs.length ? hubs.join(', ') : 'ohne Anmeldung'}`);
    return server;
  }
}

// --- MCP über HTTP, zustandslos (docs/konzept.md, „Kommunikation“) ---

// Fehler von fetch, bei denen keine Verbindung zustande kam: nichts abgeschickt.
const NOT_SENT = new Set(['ECONNREFUSED', 'ENOTFOUND', 'EADDRNOTAVAIL', 'EHOSTUNREACH', 'ENETUNREACH', 'EAI_AGAIN',
  'UND_ERR_CONNECT_TIMEOUT']);

// Ein Fehler auf dem Weg zum Node, keine Antwort eines Werkzeugs. kind: unreachable (nichts
// abgeschickt), unclear (abgeschickt, keine brauchbare Antwort) oder rejected (der Node hat
// die Anfrage abgelehnt, bevor ein Werkzeug lief). reason (Task 035) nennt daneben die Ursache
// für die Leiste, wenn es eine der bekannten ist: no_config, config_unreadable, node_not_set_up
// (ein Fehler der config, nichts abgeschickt) oder no_answer (nichts abgeschickt, NOT_SENT) —
// nicht in Error.cause, das bei fetch schon belegt ist. pause.js und fsError entscheiden nach
// kind, nie nach reason.
function nodeError(message, kind, reason) {
  const err = new Error(message);
  err.kind = kind;
  if (reason) err.reason = reason;
  return err;
}

// Der Text der Leiste zu einem Fehler auf dem Weg zum Node: nach reason, sonst nach kind.
const REASON_TEXT = {
  no_config: 'keine config',
  config_unreadable: 'config nicht lesbar',
  node_not_set_up: 'Node nicht eingerichtet',
  no_answer: 'Node antwortet nicht',
};
const KIND_TEXT = { unclear: 'Antwort unklar', rejected: 'Anfrage abgelehnt' };

function errorText(e) {
  return (e && REASON_TEXT[e.reason]) || (e && KIND_TEXT[e.kind]) || 'Node nicht erreichbar';
}

class Node {
  // onPauseChange wird gerufen, wenn sich der Stand der Pausen ändert (Statusleiste neu zeichnen).
  constructor(log, onPauseChange) {
    this.log = log;
    this.id = 0;
    // Schranke, Pause und Budget an einer entfernten Adresse (pause.js, Task 025) — ein Stand
    // für alle Fenster dieses Rechners in der Pausendatei.
    this.gate = new pause.Gate({ file: new pause.PauseFile(pause.pauseFilePath(), { log }), log, onChange: onPauseChange || (() => {}) });
  }

  // opts.hub: der Hub, den die Anfrage betrifft — ein pausiertes Paar scheitert ohne Anfrage.
  async call(tool, args, opts) {
    return (await this.request(tool, args, opts)).result.structuredContent;
  }

  // Wie call, dazu der Text des Ergebnisses. Bei read ist er nur das JSON der Struktur, der
  // Inhalt steht in data.content (Task 024); den Text braucht readFile nur für einen älteren
  // Node, bei dem er der Inhalt war.
  async callWithText(tool, args, opts) {
    const r = (await this.request(tool, args, opts)).result;
    return { data: r.structuredContent, text: (r.content || []).map((c) => c.text || '').join('') };
  }

  // Wie call, dazu die geschickten Paare (sent) — für das Erkennen an changes.
  async callSent(tool, args) {
    const r = await this.request(tool, args);
    return { data: r.result.structuredContent, sent: r.sent };
  }

  // Die Paare der Anmeldungen ohne Token: Hub, Account, Kennung der Token-Datei.
  pairs() {
    return Object.entries(this.credentials()).filter(([, c]) => c.token)
      .map(([hub, c]) => ({ hub, account: c.account, tokenId: c.tokenId }));
  }

  // Die Probe: whoami mit genau diesen Paaren, an der Schranke vorbei; request beobachtet die
  // Antwort.
  probe(pairs) {
    return this.request('whoami', {}, { probe: true, hubs: pairs.map((p) => p.hub) });
  }

  // Die Schranke: An eine entfernte Adresse erst nach der Probe; lokal nichts. Wirft, solange
  // eine Probe ohne Ergebnis die Schranke zu hält (pause.js) — dann startet der Server nicht.
  async ready() {
    const t = nodeTarget();
    if (!t.remote) return;
    await this.gate.ensure(t.endpoint, this.pairs(), (pairs) => this.probe(pairs));
  }

  // Die pausierten Paare an der entfernten Adresse ({ hub: { account, failures, nextProbe } });
  // lokal keine.
  paused() {
    const t = nodeTarget();
    if (!t.remote || !t.endpoint) return {};
    return this.gate.paused(t.endpoint, this.pairs());
  }

  // Fehlversuche der Adresse in den letzten 10 min (Budget), für die Anzeige.
  budget() {
    const t = nodeTarget();
    return t.remote && t.endpoint ? this.gate.budget(t.endpoint) : 0;
  }

  // Eine Antwort hat Paare als ungültig erwiesen (changes: alle Collections eines Hubs weg).
  noteInvalid(sent, hubs) {
    const t = nodeTarget();
    if (!t.remote || !hubs.length) return;
    this.gate.observe(t.endpoint, sent, { invalid: hubs });
  }

  // Tokens je Aufruf frisch, aber höchstens alle 5 s von der Platte (stat kommt oft).
  credentials() {
    const now = Date.now();
    if (!this._creds || now - this._credsAt > 5000) {
      this._creds = readCredentials(this.log);
      this._credsAt = now;
    }
    return this._creds;
  }

  // Nach einer anderen Wahl oder Einstellung sofort neu lesen — und an einer entfernten Adresse
  // neu proben (Zutun des Nutzers: zählt ins Budget, wartet nicht darauf).
  invalidate() {
    this._creds = undefined;
    this.gate.reset();
  }

  // Ein Aufruf, nie wiederholt — auch kein Schreibvorgang. Die Antwort eines Werkzeugs mit
  // Fehler trägt toolError, den Text als toolText und, bei den Werkzeugen, die schreiben, den
  // Code aus error.code als toolCode; nach ihm wird entschieden, nicht nach der Meldung.
  // Liefert { result, sent }: das Ergebnis und die geschickten Paare.
  //
  // An eine entfernte Adresse (Task 025): erst die Probe (Schranke), dann ohne die pausierten
  // Paare; betrifft die Anfrage einen pausierten Hub (opts.hub), scheitert sie ohne Anfrage
  // (kind paused); bleibt kein Paar, geht nichts hinaus. Blieb die Probe ohne Ergebnis (keine
  // Verbindung, 5xx des Proxys), wirft ensure mit deren kind, und nichts geht hinaus, bis die
  // nächste Probe fällig ist. Die Antwort wird beobachtet: eine 401 des Proxys heißt alle
  // geschickten Paare ungültig (ein Ergebnis, auch für die Probe), whoami sagt es je Hub
  // (rules.whoamiOutcome). opts.probe ist die Probe selbst (an der Schranke vorbei, Paare opts.hubs).
  async request(tool, args, opts = {}) {
    const target = nodeTarget();
    const url = target.endpoint;
    if (!url) throw nodeError(target.error, 'unreachable', target.reason);
    const sel = hubSelection();
    if (sel.error) throw nodeError(sel.error, 'unreachable');
    const creds = this.credentials();
    let hubs = Object.keys(creds).filter((h) => creds[h].token);
    if (target.remote && !opts.probe) {
      await this.gate.ensure(url, this.pairs(), (pairs) => this.probe(pairs));
      const paused = this.paused();
      if (opts.hub && paused[opts.hub]) {
        throw nodeError(`Hub ${opts.hub}: Anmeldung pausiert (ungültiges Paar), nächste Probe `
          + `${pause.inTime(paused[opts.hub].nextProbe - Date.now())}; nichts gefragt`, 'paused');
      }
      const active = hubs.filter((h) => !paused[h]);
      if (!active.length && hubs.length) throw nodeError('alle Paare pausiert (Anmeldung ungültig); nichts gefragt', 'paused');
      hubs = active;
    }
    if (opts.hubs) hubs = opts.hubs;
    const headers = {
      'Content-Type': 'application/json',
      Accept: 'application/json, text/event-stream',
    };
    const sent = [];
    for (const hub of hubs) {
      const c = creds[hub];
      if (!c || !c.token) continue;
      headers[`X-Keph-Account-${hub}`] = c.account;
      headers[`X-Keph-Token-${hub}`] = c.token;
      sent.push({ hub, account: c.account, tokenId: c.tokenId });
    }
    const observe = (outcome) => {
      if (target.remote && sent.length) this.gate.observe(url, sent, outcome);
    };
    const body = { jsonrpc: '2.0', id: ++this.id, method: 'tools/call', params: { name: tool, arguments: args || {} } };
    let res;
    try {
      // Keiner Weiterleitung folgen: Der Node sendet nie eine, und fetch schickte die Header-Paare
      // mit an das neue Ziel.
      res = await fetch(url, { method: 'POST', headers, body: JSON.stringify(body), redirect: 'manual' });
    } catch (e) {
      const code = e.cause && e.cause.code;
      const why = code || (e.cause && e.cause.message) || e.message;
      if (NOT_SENT.has(code)) throw nodeError(`Node ${url} antwortet nicht (${why})`, 'unreachable', 'no_answer');
      throw nodeError(`Verbindung zum Node ${url} abgebrochen (${why})`, 'unclear');
    }
    // Eine 401 des Proxys zählt an dessen Jail wie ein Fehlversuch — für die ganze Adresse.
    if (res.status === 401) observe({ invalid: 'all' });
    // Eine Antwort, die nicht vom Node kommt (Proxy: 401, Weiterleitung, HTML), hat er nie
    // ausgeführt — außer 5xx, da ist der Ausgang unklar.
    const why = rules.explainResponse(res.status, res.headers.get('content-type'));
    if (why) throw nodeError(`${url}: ${why}`, res.status >= 500 ? 'unclear' : 'rejected');
    if (!res.ok) throw nodeError(`${url}: HTTP ${res.status}`, res.status >= 500 ? 'unclear' : 'rejected');
    let msg;
    try {
      msg = parseResponse(await res.text(), res.headers.get('content-type') || '');
    } catch (e) {
      throw nodeError(`${url}: Antwort nicht lesbar (${e.message})`, 'unclear');
    }
    if (msg.error) throw nodeError(`${tool}: ${msg.error.message}`, 'rejected');
    if (msg.result && msg.result.isError) {
      const t = (msg.result.content || []).map((c) => c.text).join('\n');
      const sc = msg.result.structuredContent;
      const err = new Error(`${tool}: ${t}`);
      err.toolError = true; // Antwort des Werkzeugs, kein Fehler der Verbindung
      err.toolText = t;
      err.toolCode = sc && sc.error ? sc.error.code : undefined;
      throw err;
    }
    if (tool === 'whoami' && msg.result) observe(rules.whoamiOutcome(msg.result.structuredContent, sent.map((p) => p.hub)));
    return { result: msg.result, sent };
  }
}

// Antwort als JSON oder als SSE mit einem data:-Block.
function parseResponse(text, type) {
  if (type.includes('text/event-stream')) {
    const data = text.split('\n').filter((l) => l.startsWith('data:')).map((l) => l.slice(5).trim());
    return JSON.parse(data[data.length - 1]);
  }
  return JSON.parse(text);
}

// --- Status ---

// Was whoami mit hidden heißt: über einen Proxy, an keinem Hub gültig angemeldet.
const HIDDEN_HINT = 'stimmen die Token-Dateien, und heißen die Hubs unter tokens/ (bzw. in kephalaion.hubs) wie die '
  + 'Hub-Einträge am Node?';

class Status {
  constructor(node, log) {
    this.node = node;
    this.log = log;
    this.who = undefined;
    this.error = undefined;
    this.versionNoted = false;
    this.item = vscode.window.createStatusBarItem(vscode.StatusBarAlignment.Left, 50);
    this.item.command = 'kephalaion.menu';
    this.item.show();
    this._changed = new vscode.EventEmitter();
    this.onChanged = this._changed.event;
  }

  async refresh() {
    try {
      this.who = await this.node.call('whoami');
      this.error = undefined;
      this.noteVersion();
    } catch (e) {
      if (e.kind === 'paused') {
        // Alle Paare pausiert: keine Anfrage; die Leiste zeigt die Pausen aus der Datei.
        this.who = { paused: true, hubs: [], unknown_hubs: [] };
        this.error = undefined;
      } else {
        this.who = undefined;
        this.error = e.message;
        this.errorText = errorText(e);
        this.log(`whoami: ${this.error}`);
      }
    }
    this.render();
    this._changed.fire();
  }

  hubs() {
    return (this.who && this.who.hubs) || [];
  }

  // Einmal je Sitzung: Trägt die Erweiterung nicht die Version des Nodes, ein Hinweis, wie sie
  // nachzieht (rules.versionHint).
  noteVersion() {
    if (this.versionNoted || !this.who || this.who.hidden) return;
    const hint = rules.versionHint(this.who.version, ext.packageJSON.version, vscode.env.appName);
    if (!hint) return;
    this.versionNoted = true;
    this.log(hint);
    vscode.window.showInformationMessage(`Kephalaion: ${hint}`);
  }

  // Lesbare Collections eines Hubs; leer, wenn die Anmeldung nicht gilt.
  collections(hub) {
    const h = this.hubs().find((x) => x.hub === hub);
    if (!h || h.login !== 'ok') return [];
    return (h.collections || []).filter((c) => (c.rights || []).includes('read'));
  }

  // Die Herkunft der Wahl des Accounts eines Hubs (Task 037): Einstellung, Standard oder einzige
  // Token-Datei; bei „Wahl fehlt“ die Kandidaten, bei einer Überschreibung ohne Token-Datei sie —
  // jeweils mit „Account wählen“; dazu ein Standard ohne Token-Datei. Leer ohne Zugangsdaten.
  accountNote(hub) {
    const c = this.node.credentials()[hub];
    if (!c) return '';
    let note;
    if (c.missing) {
      note = `gewählter Account ${c.missing} hat keine Token-Datei (Einstellung kephalaion.accounts) — kein Rückfall; `
        + `Kandidaten: ${c.accounts.join(', ')}; „Account wählen“ im Menü`;
    } else if (!c.account) {
      note = `Wahl fehlt — Kandidaten: ${c.accounts.join(', ')}; „Account wählen“ im Menü oder kephalaion node account default`;
    } else {
      note = `Account ${c.account} (${rules.choiceText(c.how)})`;
      if (c.accounts.length > 1) note += `, aus ${c.accounts.join(', ')}`;
    }
    for (const n of c.notes || []) note += `; ${n}`;
    return note;
  }

  // Hubs, an denen die Wahl fehlt: mehrere Accounts, keine Einstellung, kein Standard.
  unchosen() {
    return Object.entries(this.node.credentials()).filter(([, c]) => c.how === 'missing').map(([h]) => h);
  }

  // Die Regel der Statusleiste auf den Hubs von whoami (rules.hubStatus): genutzt sind die Hubs
  // mit Zugangsdaten, nur sie warnen — ein pausiertes Paar mit dem Grund „pausiert“; Tooltip und
  // summary() lesen beide hieraus.
  status() {
    return rules.hubStatus(this.hubs(), this.node.credentials(), this.unchosen(), this.node.paused(), Date.now());
  }

  // Die Zeile zur Version: ohne Antwort (alle Paare pausiert) nur die Erweiterung.
  versionLine() {
    return this.who.paused
      ? `Node nicht gefragt (alle Paare pausiert, ${this.node.budget()} Fehlversuche in 10 min), Erweiterung ${ext.packageJSON.version}`
      : `Version Node ${this.who.version}, Erweiterung ${ext.packageJSON.version}`;
  }

  // Die Zeile zur config: Ort und Quelle, dazu die Warnung bei zwei Arten der Installation.
  configLines() {
    const t = nodeTarget();
    if (!t.location) return [];
    const loc = t.location;
    // Ohne config nennt die Zeile den Ort, an dem sie fehlt (wie „(fehlt)“ bei kephalaion status).
    const missing = t.reason === 'no_config' && !loc.error ? ', fehlt' : '';
    const out = loc.error ? [`config: ${loc.error}`]
      : [`config ${loc.path} (${rules.configSourceText(loc.source)}${missing})`];
    const w = bothKindsWarning(loc);
    if (w) out.push(`Fehler: ${w}`);
    return out;
  }

  // Dasselbe wie der Tooltip, als Text — für „Kephalaion: Status anzeigen“.
  summary() {
    const lines = [`Kephalaion — Node ${nodeUrl() || '(keine Adresse)'}`, ...this.configLines()];
    if (!this.who) {
      lines.push(`${this.errorText || 'Node nicht erreichbar'}: ${this.error}`);
      return lines;
    }
    if (this.who.hidden) {
      lines.push(`Keine gültige Anmeldung am Node (über den Proxy nennt er dann weder Version noch Hubs): ${HIDDEN_HINT}`);
      return lines;
    }
    lines.push(this.versionLine());
    const st = this.status();
    if (!st.used.length) lines.push(`Warnung: ${st.warning}`);
    for (const s of st.hubs) {
      const h = this.hubs().find((x) => x.hub === s.hub);
      if (!h) {
        // Vom Node nicht genannt: pausiert, keine Anfrage.
        lines.push(`Hub ${s.hub}: keine Anfrage`);
        lines.push(`  Warnung: ${s.warning}`);
        continue;
      }
      let l = `Hub ${h.hub} (Node ${h.node}): ${s.used ? `Anmeldung ${h.login}` : s.note}`;
      if (h.login === 'ok') l += `, Account ${h.account}, User ${h.user}`;
      lines.push(l);
      if (s.warning) lines.push(`  Warnung: ${s.warning}`);
      if (this.accountNote(h.hub)) lines.push(`  ${this.accountNote(h.hub)}`);
      for (const c of h.collections || []) lines.push(`  ${c.address}: ${c.rights.join(', ')}`);
      const sy = h.sync || {};
      lines.push(sy.never_synced ? '  noch nie abgeglichen' : `  abgeglichen ${sy.last_success}, Revision ${sy.revision}`);
      if (sy.last_error) lines.push(`  letzter Fehler ${sy.last_error_at}: ${sy.last_error}`);
    }
    if ((this.who.unknown_hubs || []).length) lines.push(`Token-Verzeichnisse ohne Hub am Node: ${this.who.unknown_hubs.join(', ')}`);
    return lines;
  }

  render() {
    const it = this.item;
    // Der Tooltip beginnt mit der config — mit dem Fehler, wenn es zwei Arten der Installation gibt.
    const cfg = this.configLines().join('\n');
    if (!this.who) {
      // Der Text nach der Ursache (Task 035): keine config, config nicht lesbar, Node nicht
      // eingerichtet, Node antwortet nicht; sonst nach kind; sonst „Node nicht erreichbar“.
      it.text = `$(warning) Keph: ${this.errorText || 'Node nicht erreichbar'}`;
      it.tooltip = cfg ? `${this.error}\n${cfg}` : this.error;
      it.backgroundColor = new vscode.ThemeColor('statusBarItem.warningBackground');
      return;
    }
    if (this.who.hidden) {
      it.text = '$(warning) Keph: nicht angemeldet';
      it.tooltip = `Keine gültige Anmeldung am Node ${nodeUrl()}: ${HIDDEN_HINT}${cfg ? `\n${cfg}` : ''}`;
      it.backgroundColor = new vscode.ThemeColor('statusBarItem.warningBackground');
      return;
    }
    // Die Leiste nennt nur die genutzten Hubs; gelb nur, was dieser Client beheben muss.
    const st = this.status();
    it.text = st.used.length ? `${st.warning ? '$(warning)' : '$(database)'} Keph ${st.used.join(' ')}` : '$(warning) Keph: kein genutzter Hub';
    it.backgroundColor = st.warning ? new vscode.ThemeColor('statusBarItem.warningBackground') : undefined;

    const md = new vscode.MarkdownString();
    md.appendMarkdown(`**Kephalaion** — ${this.versionLine()}\n\n`);
    if (cfg) md.appendMarkdown(`${cfg.replace(/\n/g, '\n\n')}\n\n`);
    if (!st.used.length) md.appendMarkdown(`${st.warning}\n\n`);
    for (const s of st.hubs) {
      const h = this.hubs().find((x) => x.hub === s.hub);
      if (!h) {
        md.appendMarkdown(`**${s.hub}**: keine Anfrage\n\nWarnung: ${s.warning}\n\n`);
        continue;
      }
      md.appendMarkdown(`**${h.hub}** (Node ${h.node}): ${s.used ? `Anmeldung \`${h.login}\`` : s.note}`);
      if (h.login === 'ok') md.appendMarkdown(`, Account ${h.account}, User ${h.user}`);
      md.appendMarkdown('\n\n');
      if (s.warning) md.appendMarkdown(`Warnung: ${s.warning}\n\n`);
      if (this.accountNote(h.hub)) md.appendMarkdown(`${this.accountNote(h.hub)}\n\n`);
      for (const c of h.collections || []) md.appendMarkdown(`- \`${c.address}\` — ${c.rights.join(', ')}\n`);
      const sy = h.sync || {};
      if (sy.never_synced) md.appendMarkdown('\nnoch nie abgeglichen\n\n');
      else md.appendMarkdown(`\nabgeglichen ${sy.last_success}, Revision ${sy.revision}\n\n`);
      if (sy.last_error) md.appendMarkdown(`letzter Fehler ${sy.last_error_at}: ${sy.last_error}\n\n`);
    }
    if ((this.who.unknown_hubs || []).length) {
      md.appendMarkdown(`Token-Verzeichnisse ohne Hub am Node: ${this.who.unknown_hubs.join(', ')}\n`);
    }
    it.tooltip = md;
  }
}

// --- Dateisystem: keph://<hub>/<collection>/… ---
// Die Authority ist der Hub-Alias, das erste Segment die Collection, der Rest der Name.
// Namen kommen vom Node immer als voller Pfad ab der Collection.

const CHANGES_MS = 3000;
// Höchstgröße eines Dokuments in Bytes (UTF-8), wie am Hub (contract.MaxDocumentBytes).
const MAX_DOCUMENT_BYTES = 1 << 20;

// Fehler beim Lesen: eine Antwort des Werkzeugs („nicht lesbar“ u. ä.) wird FileNotFound,
// ein Fehler der Verbindung Unavailable — auch ein pausiertes Paar (ohne Anfrage): nicht
// FileNotFound, sonst hielte VS Code die Dateien für gelöscht.
function toFsError(e, uri) {
  if (e.toolError) return vscode.FileSystemError.FileNotFound(uri);
  return vscode.FileSystemError.Unavailable(`Kephalaion: ${e.message}`);
}

// Fehler eines Werkzeugs, das schreibt, als FileSystemError — nach dem Code des Nodes, nie
// nach der Meldung (docs/vscode.md). Ein Fehler auf dem Weg zum Node: nichts abgeschickt,
// oder abgeschickt ohne brauchbare Antwort — dann ist der Ausgang unklar.
function writeError(e) {
  if (e.toolError) {
    const msg = `Kephalaion: ${e.toolText}`;
    switch (e.toolCode) {
      case 'name_taken':
        return vscode.FileSystemError.FileExists(msg);
      case 'not_found':
        return vscode.FileSystemError.FileNotFound(msg);
      case 'forbidden':
      case 'not_readable':
        return vscode.FileSystemError.NoPermissions(msg);
      case 'unreachable':
      case 'outcome_unknown':
        return vscode.FileSystemError.Unavailable(msg);
    }
    return new vscode.FileSystemError(msg);
  }
  if (e.kind === 'unclear') {
    return vscode.FileSystemError.Unavailable(`Kephalaion: ${e.message} — Ausgang unklar, gespeichert sein kann es. ` +
      'Nicht wiederholen: nachsehen, sobald der Node wieder antwortet.');
  }
  if (e.kind === 'rejected') return new vscode.FileSystemError(`Kephalaion: ${e.message}; nichts gespeichert`);
  return vscode.FileSystemError.Unavailable(`Kephalaion: ${e.message}; nichts gespeichert`);
}

// Fehler des read vor einem Schreibvorgang: Es ist noch nichts abgeschickt. read trägt
// keinen Code; die Meldung des Nodes geht unverändert weiter.
function lookError(e) {
  if (e.toolError) return new vscode.FileSystemError(`Kephalaion: ${e.toolText}`);
  return vscode.FileSystemError.Unavailable(`Kephalaion: ${e.message}; nichts gespeichert`);
}

// Der Inhalt für create und write: streng UTF-8, ohne NUL, höchstens 1 MiB — wie der Hub
// prüft, aber hier mit einer klaren Meldung, bevor etwas abgeschickt wird. Ein BOM bleibt
// (ignoreBOM: sonst schnitte TextDecoder es still ab).
function decodeContent(content, name) {
  if (content.byteLength > MAX_DOCUMENT_BYTES) {
    throw new vscode.FileSystemError(`Kephalaion: ${name} ist zu groß (${content.byteLength} Byte, höchstens ` +
      `${MAX_DOCUMENT_BYTES} Byte = 1 MiB); nichts gespeichert`);
  }
  let text;
  try {
    text = new TextDecoder('utf-8', { fatal: true, ignoreBOM: true }).decode(content);
  } catch {
    throw new vscode.FileSystemError(`Kephalaion: ${name} ist keine Textdatei (kein gültiges UTF-8) — gespeichert ` +
      'wird nur Text; nichts gespeichert');
  }
  if (text.includes('\0')) {
    throw new vscode.FileSystemError(`Kephalaion: ${name} ist keine Textdatei (enthält NUL) — gespeichert wird nur ` +
      'Text; nichts gespeichert');
  }
  return text;
}

function time(t) {
  const n = t && Date.parse(t.at);
  return Number.isFinite(n) ? n : 0;
}

function hubUri(hub) {
  return vscode.Uri.from({ scheme: SCHEME, authority: hub, path: '/' });
}

// URI eines Namens in einer Collection; ohne Namen die Collection selbst.
function docUri(hub, col, name) {
  return vscode.Uri.from({ scheme: SCHEME, authority: hub, path: '/' + (name ? `${col}/${name}` : col) });
}

function parentOf(name) {
  const i = name.lastIndexOf('/');
  return i < 0 ? '' : name.slice(0, i);
}

// Die Ereignisse einer Runde, jedes nur einmal.
class Events {
  constructor() {
    this.list = [];
    this.seen = new Set();
  }

  add(type, uri) {
    const k = `${type} ${uri.toString()}`;
    if (this.seen.has(k)) return;
    this.seen.add(k);
    this.list.push({ type, uri });
  }

  // Ein Name und Changed für jedes Verzeichnis darüber bis zur Collection: Der Explorer liest
  // ein Verzeichnis neu, wenn es selbst geändert gemeldet wird — auch neu entstandene oder
  // leer gewordene Verzeichnisse darüber.
  name(type, hub, col, name) {
    this.add(type, docUri(hub, col, name));
    for (let p = parentOf(name); ; p = parentOf(p)) {
      this.add(vscode.FileChangeType.Changed, docUri(hub, col, p));
      if (!p) break;
    }
  }
}

class KephFs {
  constructor(node, status, log) {
    this.node = node;
    this.status = status;
    this.log = log;
    this.cursor = undefined;
    this._emitter = new vscode.EventEmitter();
    this.onDidChangeFile = this._emitter.event;
    // Je Collection (<hub>:<collection>) id → {name, rev}, gespeist aus list, read, changes
    // und den Antworten eigener Schreibvorgänge. Daran erkennt changes ein Umbenennen: changes
    // nennt keinen alten Namen, nur die id.
    this.ids = new Map();
    // Je Collection die leeren Verzeichnisse, die es nur hier gibt (createDirectory) — der
    // Hub kennt keine leeren Verzeichnisse —, bis darin etwas angelegt oder es gelöscht wird.
    this.dirs = new Map();
    // Je Collection, ob der Account write hat (writable aus read).
    this.writable = new Map();
    // Neue Anmeldung oder andere Collections: die Wurzeln neu lesen lassen — nur dann,
    // nicht bei jedem whoami.
    this.shape = '';
    status.onChanged(() => {
      const shape = JSON.stringify(status.hubs().map((h) => [h.hub, h.login, status.collections(h.hub).map((c) => c.collection)]));
      if (shape === this.shape) return;
      this.shape = shape;
      const events = status.hubs().map((h) => ({ type: vscode.FileChangeType.Changed, uri: hubUri(h.hub) }));
      if (events.length) this._emitter.fire(events);
    });
  }

  watch() {
    return new vscode.Disposable(() => {});
  }

  split(uri) {
    const [col, ...rest] = uri.path.split('/').filter(Boolean);
    return { hub: uri.authority, col, name: rest.join('/') };
  }

  dir(writable) {
    const d = { type: vscode.FileType.Directory, ctime: 0, mtime: 0, size: 0 };
    if (!writable) d.permissions = vscode.FilePermission.Readonly;
    return d;
  }

  fire(ev) {
    if (ev.list.length) this._emitter.fire(ev.list);
  }

  // --- id → Name ---

  table(hub, col) {
    const k = `${hub}:${col}`;
    let t = this.ids.get(k);
    if (!t) this.ids.set(k, (t = new Map()));
    return t;
  }

  // Ein Dokument mit id, Name und Revision; ein älterer Stand überschreibt keinen neueren
  // (eine Antwort von changes, die vor dem eigenen Schreibvorgang gelesen wurde).
  know(hub, col, id, name, rev) {
    if (!id) return;
    const t = this.table(hub, col);
    const k = t.get(id);
    if (!k || !(rev < k.rev)) t.set(id, { name, rev });
  }

  // Ein eigenes Umbenennen eines Verzeichnisses: Die Antwort nennt keine ids, also jeden
  // Eintrag unter from/ nach to/.
  moveIds(hub, col, from, to, rev) {
    const t = this.table(hub, col);
    for (const [id, k] of t) if (k.name.startsWith(from + '/')) t.set(id, { name: to + k.name.slice(from.length), rev });
  }

  // Ein eigenes Löschen: das Dokument name oder alles darunter.
  dropIds(hub, col, name) {
    const t = this.table(hub, col);
    for (const [id, k] of t) if (k.name === name || k.name.startsWith(name + '/')) t.delete(id);
  }

  // Was read über einen Namen sagt: id, Schreibrecht der Collection, und ob gemerkte leere
  // Verzeichnisse jetzt am Hub bestehen.
  seen(hub, col, d) {
    if (typeof d.writable === 'boolean') this.writable.set(`${hub}:${col}`, d.writable);
    if (d.kind === 'document') this.know(hub, col, d.id, d.name, d.revision);
    if (d.kind === 'document' || d.kind === 'directory') this.settle(hub, col, d.name);
  }

  // --- leere Verzeichnisse, nur hier gemerkt ---

  dirSet(hub, col) {
    const k = `${hub}:${col}`;
    let s = this.dirs.get(k);
    if (!s) this.dirs.set(k, (s = new Set()));
    return s;
  }

  // name ist ein gemerktes Verzeichnis oder liegt über einem.
  hasDir(hub, col, name) {
    for (const d of this.dirSet(hub, col)) if (d === name || d.startsWith(name + '/')) return true;
    return false;
  }

  // Gemerkte Verzeichnisse direkt unter name ('' ist die Wurzel der Collection).
  dirsIn(hub, col, name) {
    const pre = name ? name + '/' : '';
    const out = new Set();
    for (const d of this.dirSet(hub, col)) if (d.startsWith(pre)) out.add(d.slice(pre.length).split('/')[0]);
    return [...out];
  }

  // name besteht am Hub (oder darin liegt jetzt etwas): name und die gemerkten Verzeichnisse
  // darüber bestehen dort ebenfalls.
  settle(hub, col, name) {
    const s = this.dirSet(hub, col);
    for (let p = name; p; p = parentOf(p)) s.delete(p);
  }

  // name gelöscht: das gemerkte Verzeichnis und alle darunter vergessen.
  dropDirs(hub, col, name) {
    const s = this.dirSet(hub, col);
    for (const d of [...s]) if (d === name || d.startsWith(name + '/')) s.delete(d);
  }

  // name umbenannt: gemerkte Verzeichnisse unter from nach to.
  moveDirs(hub, col, from, to) {
    const s = this.dirSet(hub, col);
    for (const d of [...s]) {
      if (d !== from && !d.startsWith(from + '/')) continue;
      s.delete(d);
      s.add(to + d.slice(from.length));
    }
  }

  // --- lesen ---

  async stat(uri) {
    const { hub, col, name } = this.split(uri);
    if (!col) return this.dir(false);
    let d;
    try {
      d = await this.node.call('read', { collection: `${hub}:${col}`, name, content: false }, { hub });
    } catch (e) {
      throw toFsError(e, uri);
    }
    this.seen(hub, col, d);
    // Schreibbar nach dem Recht write der Collection. Ein fremdes Dokument ohne supersede
    // scheitert erst beim Speichern (NoPermissions); supersede ohne write bleibt hier
    // schreibgeschützt — bewusste Grenze.
    if (d.kind === 'directory') return this.dir(d.writable);
    if (d.kind === 'document') {
      const s = { type: vscode.FileType.File, ctime: time(d.created), mtime: time(d.updated), size: d.size || 0 };
      if (!d.writable) s.permissions = vscode.FilePermission.Readonly;
      return s;
    }
    if (name && this.hasDir(hub, col, name)) return this.dir(this.writable.get(`${hub}:${col}`) !== false);
    throw vscode.FileSystemError.FileNotFound(uri);
  }

  async readDirectory(uri) {
    const { hub, col, name } = this.split(uri);
    const args = col ? { collection: `${hub}:${col}`, path: name, limit: 1000 } : { collection: `${hub}:`, limit: 1000 };
    const out = [];
    try {
      for (;;) {
        const r = await this.node.call('list', args, { hub });
        for (const e of r.entries || []) {
          const last = e.name.split('/').pop();
          if (e.kind === 'document') {
            out.push([last, vscode.FileType.File]);
            this.know(hub, col, e.id, e.name, e.revision);
          } else {
            out.push([last, vscode.FileType.Directory]); // directory, collection
          }
        }
        if (!r.more || !r.cursor) break;
        args.cursor = r.cursor;
      }
    } catch (e) {
      throw toFsError(e, uri);
    }
    if (col) {
      const have = new Set(out.map(([n]) => n));
      for (const d of this.dirsIn(hub, col, name)) if (!have.has(d)) out.push([d, vscode.FileType.Directory]);
    }
    return out;
  }

  // Alle Dokumente einer Collection, für „Kephalaion: Dokument öffnen“: list mit recursive, mit
  // cursor bis zum Ende; die ids merkt es wie readDirectory. Liefert die Namen.
  async listDocuments(hub, col) {
    const args = { collection: `${hub}:${col}`, recursive: true, limit: 1000 };
    const out = [];
    for (;;) {
      const r = await this.node.call('list', args, { hub });
      for (const e of r.entries || []) {
        if (e.kind !== 'document') continue;
        out.push(e.name);
        this.know(hub, col, e.id, e.name, e.revision);
      }
      if (!r.more || !r.cursor) break;
      args.cursor = r.cursor;
    }
    return out;
  }

  async readFile(uri) {
    const { hub, col, name } = this.split(uri);
    if (!col || !name) throw vscode.FileSystemError.FileIsADirectory(uri);
    let r;
    try {
      r = await this.node.callWithText('read', { collection: `${hub}:${col}`, name }, { hub });
    } catch (e) {
      throw toFsError(e, uri);
    }
    this.seen(hub, col, r.data);
    if (r.data.kind === 'directory' || (r.data.kind === 'none' && this.hasDir(hub, col, name))) {
      throw vscode.FileSystemError.FileIsADirectory(uri);
    }
    if (r.data.kind !== 'document') throw vscode.FileSystemError.FileNotFound(uri);
    // Den Inhalt trägt das Feld content. Fehlt es, ist der Node älter (vor Task 024): Dort war
    // der Inhalt der Text des Ergebnisses.
    return new TextEncoder().encode(typeof r.data.content === 'string' ? r.data.content : r.text);
  }

  // --- schreiben: über den Node am Hub, nie wiederholt; nach Erfolg die Ereignisse selbst ---

  // read mit content: false vor einem Schreibvorgang.
  async look(hub, col, name) {
    let d;
    try {
      d = await this.node.call('read', { collection: `${hub}:${col}`, name, content: false }, { hub });
    } catch (e) {
      throw lookError(e);
    }
    this.seen(hub, col, d);
    return d;
  }

  // Ein Schreibvorgang über den Node. Das Log nennt Vorgang, Name und Ausgang, nie den Inhalt.
  async change(tool, args) {
    let r;
    try {
      r = await this.node.call(tool, args, { hub: args.collection.split(':')[0] });
    } catch (e) {
      this.log(`${tool} ${args.collection} ${args.name}: ${e.toolCode || e.kind || 'Fehler'} — ${e.toolText || e.message}`);
      throw writeError(e);
    }
    const what = r.kind === 'directory' ? `Verzeichnis, ${r.count} Dokumente` : `id ${r.id}`;
    this.log(`${tool} ${r.address} ${args.name}${tool === 'rename' ? ` → ${r.name}` : ''}: Revision ${r.revision} (${what})`);
    if (r.note) {
      this.log(`${tool} ${r.address} ${r.name}: ${r.note}`);
      vscode.window.showWarningMessage(`Kephalaion: ${r.name}: ${r.note}`);
    }
    return r;
  }

  // Neu: read → none → create (ohne options.create FileNotFound); bestehend: write mit der
  // Revision aus diesem read (ohne options.overwrite FileExists) — was noch nicht abgeglichen
  // ist, lehnt der Hub an der Revision ab. Den Vergleich über mtime macht VS Code selbst.
  async writeFile(uri, content, options = {}) {
    const { hub, col, name } = this.split(uri);
    if (!col || !name) throw vscode.FileSystemError.FileIsADirectory(uri);
    const text = decodeContent(content, name);
    const d = await this.look(hub, col, name);
    if (d.kind === 'directory' || (d.kind === 'none' && this.hasDir(hub, col, name))) {
      throw vscode.FileSystemError.FileIsADirectory(uri);
    }
    const collection = `${hub}:${col}`;
    const ev = new Events();
    let r;
    if (d.kind === 'document') {
      if (!options.overwrite) throw vscode.FileSystemError.FileExists(uri);
      r = await this.change('write', { collection, name, content: text, base_revision: d.revision });
      ev.add(vscode.FileChangeType.Changed, uri);
    } else {
      if (!options.create) throw vscode.FileSystemError.FileNotFound(uri);
      r = await this.change('create', { collection, name, content: text });
      this.settle(hub, col, parentOf(name));
      ev.name(vscode.FileChangeType.Created, hub, col, name);
    }
    this.know(hub, col, r.id, r.name, r.revision);
    this.fire(ev);
  }

  // Ein Dokument, oder ein Verzeichnis als Ganzes mit recursive aus den Optionen; ohne
  // recursive lehnt der Node ein Verzeichnis ab. Ein nur gemerktes leeres Verzeichnis wird
  // vergessen, ohne den Hub zu fragen.
  async delete(uri, options) {
    const { hub, col, name } = this.split(uri);
    if (!col || !name) throw vscode.FileSystemError.NoPermissions('Kephalaion: Collections verwaltet die CLI am Hub');
    const d = await this.look(hub, col, name);
    const ev = new Events();
    if (d.kind === 'none') {
      if (!this.hasDir(hub, col, name)) throw vscode.FileSystemError.FileNotFound(uri);
      this.dropDirs(hub, col, name);
      this.log(`delete ${hub}:${col} ${name}: leeres Verzeichnis, nur hier gemerkt — vergessen`);
    } else {
      const recursive = !!(options && options.recursive);
      await this.change('delete', { collection: `${hub}:${col}`, name, recursive });
      this.dropIds(hub, col, name);
      this.dropDirs(hub, col, name);
    }
    ev.name(vscode.FileChangeType.Deleted, hub, col, name);
    this.fire(ev);
  }

  // Nur innerhalb einer Collection, ein Verzeichnis als Ganzes; die id bleibt. Ein belegtes
  // Ziel wird nie überschrieben (VS Code löscht es bei „Ersetzen“ vorher selbst).
  async rename(oldUri, newUri, options) {
    const a = this.split(oldUri);
    const b = this.split(newUri);
    if (a.hub !== b.hub || a.col !== b.col) {
      throw new vscode.FileSystemError(`Kephalaion: Verschieben nur innerhalb einer Collection, nicht von ` +
        `${a.hub}:${a.col || ''} nach ${b.hub}:${b.col || ''} — dafür kopieren und löschen`);
    }
    const { hub, col } = a;
    if (!col || !a.name || !b.name) {
      throw vscode.FileSystemError.NoPermissions('Kephalaion: Collections verwaltet die CLI am Hub');
    }
    const [src, dst] = await Promise.all([this.look(hub, col, a.name), this.look(hub, col, b.name)]);
    const srcDir = src.kind === 'none' && this.hasDir(hub, col, a.name);
    if (src.kind === 'none' && !srcDir) throw vscode.FileSystemError.FileNotFound(oldUri);
    if (dst.kind !== 'none' || this.hasDir(hub, col, b.name)) {
      if (options && options.overwrite) {
        throw new vscode.FileSystemError(`Kephalaion: ${b.name} ist belegt; Umbenennen überschreibt nicht — das Ziel ` +
          'zuerst löschen');
      }
      throw vscode.FileSystemError.FileExists(newUri);
    }
    if (srcDir) {
      this.log(`rename ${hub}:${col} ${a.name} → ${b.name}: leeres Verzeichnis, nur hier gemerkt`);
    } else {
      const r = await this.change('rename', { collection: `${hub}:${col}`, name: a.name, new_name: b.name });
      if (r.kind === 'directory') this.moveIds(hub, col, a.name, b.name, r.revision);
      else this.know(hub, col, r.id, r.name, r.revision);
      this.settle(hub, col, parentOf(b.name));
    }
    this.moveDirs(hub, col, a.name, b.name);
    const ev = new Events();
    ev.name(vscode.FileChangeType.Deleted, hub, col, a.name);
    ev.name(vscode.FileChangeType.Created, hub, col, b.name);
    this.fire(ev);
  }

  // Nichts am Hub: Verzeichnisse sind dort nur Präfixe von Namen. Das leere Verzeichnis wird
  // hier gemerkt, bis darin etwas angelegt oder es gelöscht wird; stat und readDirectory
  // zeigen es.
  async createDirectory(uri) {
    const { hub, col, name } = this.split(uri);
    if (!col) throw vscode.FileSystemError.NoPermissions('Kephalaion: Collections legt die CLI am Hub an');
    if (!name) throw vscode.FileSystemError.FileExists(uri);
    const parent = parentOf(name);
    const [d, p] = await Promise.all([this.look(hub, col, name), this.look(hub, col, parent)]);
    if (d.kind !== 'none' || this.hasDir(hub, col, name)) throw vscode.FileSystemError.FileExists(uri);
    if (p.kind === 'document') throw vscode.FileSystemError.FileNotADirectory(docUri(hub, col, parent));
    if (p.kind === 'none' && !this.hasDir(hub, col, parent)) throw vscode.FileSystemError.FileNotFound(docUri(hub, col, parent));
    if (this.writable.get(`${hub}:${col}`) === false) {
      throw vscode.FileSystemError.NoPermissions(`Kephalaion: ${hub}:${col} ohne Recht write`);
    }
    this.dirSet(hub, col).add(name);
    this.log(`createDirectory ${hub}:${col} ${name}: nur hier gemerkt, bis darin etwas liegt`);
    const ev = new Events();
    ev.name(vscode.FileChangeType.Created, hub, col, name);
    this.fire(ev);
  }

  // Anderer Account: Rechte und lesbare Collections können anders sein. changes setzt neu
  // an, die ids und Rechte gelten nicht mehr, und alle Hubs werden neu gelesen.
  rebase(hubs) {
    this.cursor = undefined;
    this.ids.clear();
    this.writable.clear();
    const events = hubs.map((hub) => ({ type: vscode.FileChangeType.Changed, uri: hubUri(hub) }));
    if (events.length) this._emitter.fire(events);
  }

  // changes lückenlos weiterfragen und daraus onDidChangeFile auslösen. Ohne cursor liefert
  // der erste Aufruf nur den Ausgangspunkt „ab jetzt“.
  async pollChanges() {
    let r;
    let sent;
    try {
      ({ data: r, sent } = await this.node.callSent('changes', this.cursor ? { cursor: this.cursor } : {}));
    } catch (e) {
      if (e.toolError) {
        // z. B. ungültiger cursor: neu ansetzen; was dazwischen lag, ist verloren.
        this.cursor = undefined;
        this.ids.clear();
      }
      return;
    }
    const first = this.cursor === undefined;
    this.cursor = r.cursor;
    if (first) return;
    const ev = new Events();
    for (const c of r.changes || []) {
      const i = c.address.indexOf(':');
      const hub = c.address.slice(0, i);
      const col = c.address.slice(i + 1);
      const t = this.table(hub, col);
      const known = t.get(c.id);
      const stale = known && c.revision < known.rev;
      if (known && !stale && known.name !== c.name) {
        // Umbenannt (die id kennt einen anderen Namen): der alte gelöscht, der neue angelegt
        // bzw. gelöscht, wenn es danach gelöscht wurde.
        ev.name(vscode.FileChangeType.Deleted, hub, col, known.name);
        ev.name(c.deleted ? vscode.FileChangeType.Deleted : vscode.FileChangeType.Created, hub, col, c.name);
        this.log(`changes: ${c.address} ${known.name} → ${c.name}${c.deleted ? ' gelöscht' : ''} (Revision ${c.revision})`);
      } else {
        ev.name(c.deleted ? vscode.FileChangeType.Deleted : vscode.FileChangeType.Changed, hub, col, c.name);
        this.log(`changes: ${c.address} ${c.name}${c.deleted ? ' gelöscht' : ''} (Revision ${c.revision})`);
      }
      if (stale) continue;
      if (c.deleted) {
        t.delete(c.id);
      } else {
        t.set(c.id, { name: c.name, rev: c.revision });
        this.settle(hub, col, parentOf(c.name));
      }
    }
    // reset: Replica des Hubs neu angelegt — die ids des Hubs verwerfen und neu lesen lassen;
    // dropped: Collection nicht mehr lesbar — ihre ids verwerfen.
    for (const hub of r.reset || []) {
      for (const k of [...this.ids.keys()]) if (k.startsWith(`${hub}:`)) this.ids.delete(k);
      ev.add(vscode.FileChangeType.Changed, hubUri(hub));
    }
    for (const d of r.dropped || []) {
      const address = String(d.address || d);
      this.ids.delete(address);
      ev.add(vscode.FileChangeType.Changed, hubUri(address.split(':')[0]));
    }
    this.fire(ev);
    // Ein Hub, der eben noch lesbar war und es gar nicht mehr ist (alle seine Collections aus dem
    // letzten whoami sind dropped): sein Paar ist ungültig geworden — rotate auf einem anderen
    // Rechner, Account gesperrt (rules.droppedHubs, Task 025).
    if ((r.dropped || []).length && sent) {
      const readable = {};
      for (const p of sent) readable[p.hub] = this.status.collections(p.hub).map((c) => c.address);
      this.node.noteInvalid(sent, rules.droppedHubs(r.dropped, readable));
    }
    if (r.more) return this.pollChanges();
  }
}

// --- Der collection tree: Collections als Baum im Explorer (Task 043; docs/vscode.md, „Baumansicht“) ---
// Liest und schreibt nur über KephFs, mit denselben keph://-URIs wie ein Workspace-Ordner.

const VIEW_ID = 'kephalaion.collections';

// Der Wert von kephalaion.collections, oder undefined, wenn er auf keiner Ebene gesetzt ist
// (Festlegung 3): Die Vorgabe zählt nicht, darum inspect statt get.
function collectionsSetting() {
  const i = vscode.workspace.getConfiguration('kephalaion').inspect('collections');
  if (!i) return undefined;
  for (const k of ['workspaceFolderValue', 'workspaceValue', 'globalValue']) if (i[k] !== undefined) return i[k];
  return undefined;
}

// Die lesbaren Collections der genutzten Hubs aus dem letzten whoami.
function readableCollections(status) {
  return status.hubs().flatMap((h) => status.collections(h.hub).map((c) => ({
    address: c.address, hub: h.hub, collection: c.collection, rights: c.rights || [],
  })));
}

// Gibt es eine gültige Anmeldung an einem Hub? Für viewsWelcome.
function signedIn(status) {
  return Boolean(status.who && !status.who.hidden && status.hubs().some((h) => h.login === 'ok'));
}

// Ein Element des Baums: kind collection, directory oder document; dazu hub, collection, name
// (Pfad in der Collection, leer für die Collection) und uri. Je URI gibt es genau ein Objekt:
// VS Code erkennt ein Element für refresh und reveal an seiner Identität.
class CollectionTree {
  constructor(kfs, status, log) {
    this.kfs = kfs;
    this.status = status;
    this.log = log;
    this.nodes = new Map();
    this.shape = '';
    this.pending = undefined;
    this._changed = new vscode.EventEmitter();
    this.onDidChangeTreeData = this._changed.event;
    kfs.onDidChangeFile((events) => this.filesChanged(events));
    status.onChanged(() => this.reshape(false));
  }

  // Die gezeigten Collections nach Einstellung und whoami (rules.shownCollections).
  shown() {
    const readable = readableCollections(this.status);
    return rules.shownCollections(collectionsSetting(), readable, readable.map((c) => c.address));
  }

  // Der Eintrag einer gezeigten Collection, undefined, wenn sie nicht gezeigt ist.
  info(hub, collection) {
    return this.shown().find((c) => c.hub === hub && c.collection === collection);
  }

  node(hub, collection, name, kind) {
    const uri = docUri(hub, collection, name);
    const key = uri.toString();
    let n = this.nodes.get(key);
    if (!n) {
      n = { uri, hub, collection, name, kind };
      this.nodes.set(key, n);
    }
    n.kind = kind;
    return n;
  }

  // Das Element einer keph://-URI, wenn der Baum es kennt.
  known(uri) {
    return this.nodes.get(uri.toString());
  }

  // Das Element einer keph://-URI für reveal und die Befehle: die Collection, sonst ein Dokument
  // oder Verzeichnis nach kind.
  nodeFor(uri, kind) {
    const { hub, col, name } = this.kfs.split(uri);
    if (!col) return undefined;
    return this.node(hub, col, name, name ? kind : 'collection');
  }

  // Alles neu: nach anderer Anmeldung, anderer Einstellung, einer Hub-URI. force auch ohne
  // geänderte Form (Befehl „Aktualisieren“).
  reshape(force) {
    const shape = JSON.stringify([signedIn(this.status), this.shown()]);
    vscode.commands.executeCommand('setContext', 'kephalaion.signedIn', signedIn(this.status));
    if (!force && shape === this.shape) return;
    this.shape = shape;
    this.pending = undefined;
    this._changed.fire(undefined);
  }

  // Ereignisse des Providers: Angelegt oder gelöscht lädt das Verzeichnis darüber neu; geändert
  // nur, wenn der Baum den Namen noch nicht kennt (ein Dokument, das ein anderer angelegt hat —
  // changes unterscheidet das nicht). Eine Hub-URI lädt alles neu. Gebündelt: ein Neuladen je
  // Runde, und ein Verzeichnis nicht, wenn eines darüber ohnehin neu geladen wird. Nach einem
  // Löschen (auch dem alten Namen eines Verschiebens) prüft flush, ob das Verzeichnis darüber
  // noch besteht: Der Hub kennt keine leeren Verzeichnisse, mit dem letzten Dokument verschwindet
  // es — dann lädt das nächste bestehende darüber neu.
  filesChanged(events) {
    let all = false;
    const targets = new Map();
    for (const e of events || []) {
      if (!e.uri || e.uri.scheme !== SCHEME) continue;
      const { hub, col, name } = this.kfs.split(e.uri);
      if (!col) {
        all = true;
        continue;
      }
      if (e.type === vscode.FileChangeType.Changed && this.known(e.uri)) continue;
      if (!name) continue;
      const deleted = e.type === vscode.FileChangeType.Deleted;
      if (deleted) this.forget(e.uri);
      const p = this.known(docUri(hub, col, parentOf(name)));
      if (!p) continue;
      const k = p.uri.toString();
      targets.set(k, { n: p, check: deleted || Boolean(targets.get(k) && targets.get(k).check) });
    }
    if (all) {
      this.reshape(true);
      return;
    }
    if (!targets.size) return;
    if (!this.pending) {
      this.pending = new Map();
      setTimeout(() => this.flush(), 0);
    }
    for (const [k, t] of targets) {
      const had = this.pending.get(k);
      this.pending.set(k, { n: t.n, check: t.check || Boolean(had && had.check) });
    }
  }

  // Ein gelöschter Name und alles darunter: Der Baum kennt ihn nicht mehr, damit ein späteres
  // Anlegen unter demselben Namen (geändert gemeldet) das Verzeichnis neu lädt.
  forget(uri) {
    const key = uri.toString();
    for (const k of [...this.nodes.keys()]) if (k === key || k.startsWith(`${key}/`)) this.nodes.delete(k);
  }

  async flush() {
    const pending = this.pending;
    this.pending = undefined;
    if (!pending || !pending.size) return;
    const found = new Map();
    for (const t of pending.values()) {
      const n = t.check ? await this.existing(t.n) : t.n;
      found.set(n.uri.toString(), n);
    }
    const list = [...found.values()];
    const under = (n, a) => n !== a && n.hub === a.hub && n.collection === a.collection && (!a.name || n.name.startsWith(`${a.name}/`));
    const top = list.filter((n) => !list.some((a) => under(n, a)));
    this._changed.fire(top.length === 1 ? top[0] : top);
  }

  // Das Verzeichnis n oder, wenn es nicht mehr besteht, das nächste bestehende darüber bis zur
  // Collection; ein verschwundenes kennt der Baum danach nicht mehr. Nur FileNotFound heißt
  // „besteht nicht“ — ein anderer Fehler lädt n selbst neu, getChildren meldet ihn.
  async existing(n) {
    while (n.kind === 'directory') {
      try {
        await this.kfs.stat(n.uri);
        return n;
      } catch (e) {
        if (!e || e.code !== 'FileNotFound') return n;
      }
      this.forget(n.uri);
      n = this.getParent(n);
    }
    return n;
  }

  getTreeItem(n) {
    const c = this.info(n.hub, n.collection);
    const writable = Boolean(c && c.writable);
    let item;
    if (n.kind === 'collection') {
      const readable = Boolean(c && c.readable);
      item = new vscode.TreeItem(c ? c.address : `${n.hub}:${n.collection}`,
        readable ? vscode.TreeItemCollapsibleState.Collapsed : vscode.TreeItemCollapsibleState.None);
      item.iconPath = new vscode.ThemeIcon(readable ? 'database' : 'warning');
      if (!readable) {
        item.description = 'nicht lesbar';
        item.tooltip = `${item.label}: whoami meldet diese Collection nicht als lesbar — Anmeldung, Recht oder Schreibweise `
          + 'in kephalaion.collections prüfen';
        item.contextValue = 'collection-unreadable';
      } else {
        if (!writable) item.description = 'nur lesen';
        item.contextValue = `collection-${writable ? 'writable' : 'readonly'}`;
      }
    } else {
      item = new vscode.TreeItem(n.uri, n.kind === 'directory'
        ? vscode.TreeItemCollapsibleState.Collapsed : vscode.TreeItemCollapsibleState.None);
      item.contextValue = `${n.kind}-${writable ? 'writable' : 'readonly'}`;
      if (n.kind === 'document') item.command = { command: 'vscode.open', title: 'Öffnen', arguments: [n.uri] };
    }
    item.id = n.uri.toString();
    item.resourceUri = n.uri;
    return item;
  }

  async getChildren(n) {
    if (!n) {
      const shown = this.shown();
      this.shape = JSON.stringify([signedIn(this.status), shown]);
      return shown.map((c) => this.node(c.hub, c.collection, '', 'collection'));
    }
    if (n.kind === 'document') return [];
    if (n.kind === 'collection') {
      const c = this.info(n.hub, n.collection);
      if (!c || !c.readable) return [];
    }
    let entries;
    try {
      entries = await this.kfs.readDirectory(n.uri);
    } catch (e) {
      this.log(`Baum: ${n.hub}:${n.collection} ${n.name || '/'} nicht lesbar: ${e.message}`);
      vscode.window.showErrorMessage(e.message.startsWith('Kephalaion') ? e.message : `Kephalaion: ${e.message}`);
      return [];
    }
    const dirs = entries.filter(([, t]) => t === vscode.FileType.Directory).map(([x]) => x).sort();
    const docs = entries.filter(([, t]) => t !== vscode.FileType.Directory).map(([x]) => x).sort();
    const pre = n.name ? `${n.name}/` : '';
    return [...dirs.map((d) => this.node(n.hub, n.collection, pre + d, 'directory')),
      ...docs.map((d) => this.node(n.hub, n.collection, pre + d, 'document'))];
  }

  getParent(n) {
    if (!n || n.kind === 'collection') return undefined;
    const p = parentOf(n.name);
    return this.node(n.hub, n.collection, p, p ? 'directory' : 'collection');
  }
}

// Ein Fehler eines Befehls im Baum als Meldung, mit dem Text des Providers bzw. des Nodes.
function showTreeError(log, what, e) {
  const msg = e && e.message ? e.message : String(e);
  log(`Baum: ${what}: ${msg}`);
  vscode.window.showErrorMessage(msg.startsWith('Kephalaion') ? msg : `Kephalaion: ${msg}`);
}

// Gibt es die URI? Nur FileNotFound heißt nein; jeder andere Fehler geht weiter.
async function uriExists(uri) {
  try {
    await vscode.workspace.fs.stat(uri);
    return true;
  } catch (e) {
    if (e && e.code === 'FileNotFound') return false;
    throw e;
  }
}

// Legt uri mit content neu an und überschreibt nie — auch nicht, wenn zwischen der Prüfung in
// copyTree und hier ein anderer denselben Namen anlegt. workspace.fs.writeFile kennt kein „nur
// anlegen“ (es überschreibt immer), daher: keph:// über den Provider ohne overwrite (ein belegter
// Name ist dort FileExists, am Hub name_taken), file:// mit dem Flag wx (O_EXCL). Für jedes
// andere Schema bleibt nur die Prüfung unmittelbar davor.
async function writeNew(kfs, uri, content) {
  try {
    if (uri.scheme === SCHEME) {
      await kfs.writeFile(uri, content, { create: true, overwrite: false });
    } else if (uri.scheme === 'file') {
      await fs.promises.writeFile(uri.fsPath, content, { flag: 'wx' });
    } else {
      if (await uriExists(uri)) throw vscode.FileSystemError.FileExists(uri);
      await vscode.workspace.fs.writeFile(uri, content);
    }
  } catch (e) {
    if (e && (e.code === 'FileExists' || e.code === 'EEXIST')) throw new Error(`${uri.toString()} ist belegt; nicht überschrieben`);
    throw e;
  }
}

// Kopiert src nach dst, ein Verzeichnis mit allem darunter — von einem Provider zum anderen
// (Projekt → Collection, Collection → Projekt); gelesen über workspace.fs, geschrieben mit
// writeNew. Ein belegtes dst wird nie überschrieben: Dann geht nichts; ein Name darunter, den
// inzwischen ein anderer angelegt hat, bleibt, wie er ist, und zählt als nicht kopiert. Ein
// Dokument, das nicht geht (keine Textdatei, zu groß), hält die übrigen nicht auf. Liefert
// { copied, failed: [Meldung] }.
async function copyTree(kfs, src, dst) {
  if (await uriExists(dst)) throw new Error(`Kephalaion: ${dst.toString()} ist belegt; nichts kopiert, nichts überschrieben`);
  const out = { copied: 0, failed: [] };
  const walk = async (s, d) => {
    let st;
    try {
      st = await vscode.workspace.fs.stat(s);
      if (st.type & vscode.FileType.Directory) {
        await vscode.workspace.fs.createDirectory(d);
        for (const [name] of await vscode.workspace.fs.readDirectory(s)) {
          await walk(vscode.Uri.joinPath(s, name), vscode.Uri.joinPath(d, name));
        }
        return;
      }
      await writeNew(kfs, d, await vscode.workspace.fs.readFile(s));
      out.copied++;
    } catch (e) {
      out.failed.push(`${s.toString()}: ${e.message}`);
    }
  };
  await walk(src, dst);
  return out;
}

// Das Ergebnis von copyTree als Log und, wenn etwas nicht ging, als Meldung.
function reportCopy(log, what, results) {
  const copied = results.reduce((n, r) => n + r.copied, 0);
  const failed = results.flatMap((r) => r.failed);
  log(`Baum: ${what}: ${copied} kopiert${failed.length ? `, ${failed.length} nicht` : ''}`);
  for (const f of failed) log(`Baum: ${what}: nicht kopiert ${f}`);
  if (failed.length) {
    vscode.window.showErrorMessage(`Kephalaion: ${failed.length} nicht kopiert (${copied} kopiert) — ${failed[0]}`
      + `${failed.length > 1 ? ' … (alle im Log)' : ''}`);
  }
}

// Der Mime-Typ, unter dem VS Code die gezogenen Elemente dieses Baums mitgibt.
const TREE_MIME = `application/vnd.code.tree.${VIEW_ID}`;

// Ziehen und Ablegen im Baum (Etappe 4): Aus dem Baum gehen die URIs als text/uri-list (in den
// Editor, und in den Explorer, falls er sie annimmt). Abgelegt wird nach rules.dropTarget:
// Elemente des Baums innerhalb einer Collection verschieben, Dateien und Ordner aus dem Projekt
// oder vom Betriebssystem kopieren; nie überschreiben, nie zwischen Collections.
class TreeDragAndDrop {
  constructor(tree, log) {
    this.tree = tree;
    this.log = log;
    this.dragMimeTypes = ['text/uri-list'];
    this.dropMimeTypes = [TREE_MIME, 'text/uri-list'];
  }

  handleDrag(source, dataTransfer) {
    dataTransfer.set('text/uri-list', new vscode.DataTransferItem(source.map((n) => n.uri.toString()).join('\r\n')));
  }

  async handleDrop(target, dataTransfer) {
    let sources;
    const own = dataTransfer.get(TREE_MIME);
    if (own && Array.isArray(own.value)) {
      sources = own.value.map((n) => n.uri);
    } else {
      const list = dataTransfer.get('text/uri-list');
      if (!list) return;
      sources = (await list.asString()).split(/\r?\n/).map((l) => l.trim()).filter((l) => l && !l.startsWith('#'))
        .map((l) => vscode.Uri.parse(l));
    }
    if (!sources.length) return;
    const info = target && this.tree.info(target.hub, target.collection);
    let plan = rules.dropTarget(target ? { authority: target.uri.authority, path: target.uri.path, kind: target.kind } : undefined,
      sources);
    if (!plan.error && (!info || !info.readable)) plan = { error: `${target.hub}:${target.collection} ist nicht lesbar` };
    if (!plan.error && !info.writable) plan = { error: `${info.address} nur lesen (ohne Recht write)` };
    if (plan.error) {
      this.log(`Baum: ablegen abgelehnt: ${plan.error}`);
      vscode.window.showErrorMessage(`Kephalaion: ${plan.error}; nichts geändert`);
      return;
    }
    const copies = [];
    for (const it of plan.items) {
      const dst = docUri(plan.hub, plan.collection, it.name);
      try {
        if (it.op === 'move') await vscode.workspace.fs.rename(it.source, dst, { overwrite: false });
        else copies.push(await copyTree(this.tree.kfs, it.source, dst));
      } catch (e) {
        showTreeError(this.log, `ablegen ${it.source.toString()} → ${dst.toString()}`, e);
      }
    }
    if (copies.length) reportCopy(this.log, `in ${plan.hub}:${plan.collection} kopiert`, copies);
  }
}

// „Kephalaion: Dokument öffnen“ und „Kephalaion: Suchen“ (Etappe 5) — eigene Befehle statt
// Strg+P und Strg+Umschalt+F, die in keph:// nichts finden (docs/vscode.md, „Suche: eigene
// Befehle statt der Suche von VS Code“). Beide über die gezeigten, lesbaren Collections;
// geöffnet wird über keph://-URIs, also über KephFs.
function findCommands(tree, kfs, node, log) {
  const readable = () => tree.shown().filter((c) => c.readable);
  const none = () => vscode.window.showWarningMessage('Kephalaion: keine lesbare Collection im Baum — Anmeldung oder '
    + 'kephalaion.collections prüfen.');

  const openDocument = async () => {
    const cols = readable();
    if (!cols.length) return none();
    const failed = [];
    const items = Promise.all(cols.map((c) => kfs.listDocuments(c.hub, c.collection)
      .then((names) => names.map((n) => ({ label: n, description: c.address, uri: docUri(c.hub, c.collection, n) })))
      .catch((e) => {
        failed.push(`${c.address}: ${e.message}`);
        log(`Dokument öffnen: ${c.address} nicht gelistet: ${e.message}`);
        return [];
      }))).then((lists) => lists.flat());
    const pick = await vscode.window.showQuickPick(items, {
      placeHolder: 'Dokument öffnen — Name in den Collections des Baums', matchOnDescription: true,
    });
    if (failed.length) vscode.window.showWarningMessage(`Kephalaion: nicht alle Collections gelistet — ${failed.join('; ')}`);
    if (pick) await vscode.commands.executeCommand('vscode.open', pick.uri);
  };

  const search = async () => {
    const cols = readable();
    if (!cols.length) return none();
    const query = await vscode.window.showInputBox({
      prompt: 'Suchen in docs/ der Collections im Baum',
      placeHolder: 'einfache englische Wörter — gesucht wird nur unter docs/, nicht im ganzen Text der Collection',
    });
    if (!query || !query.trim()) return undefined;
    const shown = new Set(cols.map((c) => c.address));
    let r;
    try {
      r = await node.call('search', { query: query.trim(), limit: 20 });
    } catch (e) {
      showTreeError(log, `suchen „${query}“`, e);
      return undefined;
    }
    const items = (r.results || []).filter((x) => shown.has(x.address)).flatMap((x) => (x.hits || []).map((h) => {
      const sec = h.section || {};
      const trail = (sec.headings && sec.headings.length ? sec.headings.join(' › ') : sec.heading) || '';
      return { label: h.title || h.name, description: x.address, detail: trail ? `${trail} — ${h.name}` : h.name, address: x.address, hit: h };
    }));
    if (!items.length) {
      vscode.window.showInformationMessage(`Kephalaion: nichts gefunden für „${query.trim()}“ in docs/ der Collections im Baum`
        + `${r.hint ? ` — ${r.hint}` : ''}`);
      return undefined;
    }
    const pick = await vscode.window.showQuickPick(items, {
      placeHolder: `Treffer für „${query.trim()}“ — Titel, Abschnitt, Collection`, matchOnDescription: true, matchOnDetail: true,
    });
    if (!pick) return undefined;
    const { hub, collection } = rules.splitAddress(pick.address);
    const uri = docUri(hub, collection, pick.hit.name);
    try {
      const doc = await vscode.workspace.openTextDocument(uri);
      const line = Math.max(0, rules.headingLine(doc.getText(), (pick.hit.section || {}).heading));
      await vscode.window.showTextDocument(doc, { selection: new vscode.Range(line, 0, line, 0) });
    } catch (e) {
      showTreeError(log, `öffnen ${uri.toString()}`, e);
    }
    return undefined;
  };

  return { 'kephalaion.openDocument': openDocument, 'kephalaion.search': search };
}

// --- Auswahl der Collections und das Herausnehmen der keph://-Ordner (Task 043 Etappe 6, Task 044) ---

// Wohin kephalaion.collections geschrieben wird (Festlegung 4): auf Workspace-Ebene, wenn ein
// Workspace oder Ordner offen ist — in .code-workspace bzw. .vscode/settings.json —, sonst in
// die Einstellungen des Benutzers.
function collectionsTarget() {
  return vscode.workspace.workspaceFile || (vscode.workspace.workspaceFolders || []).length
    ? vscode.ConfigurationTarget.Workspace : vscode.ConfigurationTarget.Global;
}

// Schreibt kephalaion.collections nach rules.mergeCollections: ungesetzt von den gezeigten aus.
async function writeCollections(tree, add, remove, log) {
  const value = rules.mergeCollections(collectionsSetting(), add, remove, tree.shown().map((c) => c.address));
  const target = collectionsTarget();
  await vscode.workspace.getConfiguration('kephalaion').update('collections', value, target);
  log(`Baum: kephalaion.collections = ${JSON.stringify(value)} (${target === vscode.ConfigurationTarget.Workspace
    ? 'Workspace' : 'Benutzer'})`);
}

// Der Schlüssel im Speicher des Workspace, unter dem die Erweiterung bis Task 044 die selbst
// herausgenommenen Ordner zum Wiedereinhängen merkte. Er wird beim Aktivieren einmal geleert und
// nicht mehr geschrieben.
const REMOVED_FOLDERS = 'kephalaion.removedFolders';

// Ein Aufruf von updateWorkspaceFolders, dann das Warten auf sein Ereignis (Festlegung 6): Vor
// dem Ereignis darf kein weiterer Aufruf kommen. false, wenn VS Code ablehnt oder nach 10 s
// kein Ereignis kam.
function changeFolders(log, start, deleteCount, ...add) {
  return new Promise((resolve) => {
    let done = false;
    let timer;
    let sub;
    const finish = (ok) => {
      if (done) return;
      done = true;
      clearTimeout(timer);
      if (sub) sub.dispose();
      resolve(ok);
    };
    sub = vscode.workspace.onDidChangeWorkspaceFolders(() => finish(true));
    timer = setTimeout(() => {
      log(`Workspace-Ordner: kein Ereignis nach updateWorkspaceFolders(${start}, ${deleteCount}) — abgebrochen`);
      finish(false);
    }, 10000);
    let ok = false;
    try {
      ok = vscode.workspace.updateWorkspaceFolders(start, deleteCount, ...add);
    } catch (e) {
      log(`Workspace-Ordner: updateWorkspaceFolders(${start}, ${deleteCount}): ${e.message}`);
    }
    if (!ok) {
      log(`Workspace-Ordner: updateWorkspaceFolders(${start}, ${deleteCount}) abgelehnt`);
      finish(false);
    }
  });
}

// Nimmt die keph://-Ordner aus dem Workspace, jedes Mal aus dem Ist-Zustand und seit Task 044
// ohne Einstellung: nach rules.folderRemovalPlan Bereich für Bereich von hinten, vorher ihre
// Collections in kephalaion.collections, wenn die Einstellung gesetzt ist, damit der Baum sie
// zeigt. Index 0 startet den Extension Host neu, danach geht es beim Aktivieren weiter. Ein
// Workspace nur aus keph://-Ordnern bleibt.
async function syncWorkspaceFolders(tree, log, state) {
  const folders = (vscode.workspace.workspaceFolders || []).map((f, i) => ({
    scheme: f.uri.scheme, authority: f.uri.authority, path: f.uri.path, uri: f.uri.toString(), name: f.name,
    index: typeof f.index === 'number' ? f.index : i,
  }));
  const plan = rules.folderRemovalPlan(folders);
  if (plan.onlyKeph) {
    log('Workspace-Ordner: der Workspace besteht nur aus keph://-Ordnern — nichts herausgenommen');
    if (!state.onlyKephNoted) {
      state.onlyKephNoted = true;
      vscode.window.showInformationMessage('Kephalaion: Dieser Workspace besteht nur aus Collections — sie bleiben '
        + 'als Ordner, ein leerer Workspace hilft niemandem. Neben einem Projektordner zeigt sie der Baum „Kephalaion“.');
    }
    return;
  }
  if (!plan.ranges.length) return;
  for (const s of plan.subpaths) log(`Workspace-Ordner: ${s.uri} zeigt nur einen Teil — im Baum als ganze Collection ${s.address}`);
  if (collectionsSetting() !== undefined) await writeCollections(tree, plan.collections, [], log);
  log(`Workspace-Ordner: nehme heraus: ${plan.removed.map((r) => r.uri).join(', ')}`
    + `${plan.touchesFirst ? ' (Index 0: VS Code startet den Extension Host neu)' : ''}`);
  for (const r of plan.ranges) {
    if (!(await changeFolders(log, r.start, r.count))) return;
  }
}

// „Collection im Baum zeigen“ und „… aus dem Baum nehmen“; sie schreiben kephalaion.collections
// nur auf Aufruf (Festlegung 4).
function selectionCommands(tree, status, log) {
  const showInTree = async () => {
    await status.refresh();
    const shown = new Set(tree.shown().map((c) => c.address));
    const items = readableCollections(status).filter((c) => !shown.has(c.address))
      .map((c) => ({ label: c.address, description: c.rights.join(', ') }));
    if (!items.length) {
      vscode.window.showInformationMessage('Kephalaion: Alle lesbaren Collections sind schon im Baum.');
      return;
    }
    const pick = await vscode.window.showQuickPick(items, { placeHolder: 'Collection im Baum zeigen' });
    if (!pick) return;
    try {
      await writeCollections(tree, [pick.label], [], log);
      await vscode.commands.executeCommand(`${VIEW_ID}.focus`);
    } catch (e) {
      showTreeError(log, `kephalaion.collections schreiben (${pick.label})`, e);
    }
  };
  const hideFromTree = async (n) => {
    let address = n && n.kind === 'collection' ? `${n.hub}:${n.collection}` : undefined;
    if (!address) {
      const items = tree.shown().map((c) => ({ label: c.address, description: c.readable ? '' : 'nicht lesbar' }));
      if (!items.length) return;
      const pick = await vscode.window.showQuickPick(items, { placeHolder: 'Collection aus dem Baum nehmen' });
      if (!pick) return;
      address = pick.label;
    }
    try {
      await writeCollections(tree, [], [address], log);
    } catch (e) {
      showTreeError(log, `kephalaion.collections schreiben (${address})`, e);
    }
  };
  return { 'kephalaion.showInTree': showInTree, 'kephalaion.hideFromTree': hideFromTree };
}

// Die Befehle zum Bearbeiten im Baum (Etappe 3). Alles über vscode.workspace.fs auf
// keph://-URIs, also über KephFs; ein Befehl aus dem Kontextmenü bekommt das Element und die
// Auswahl, einer aus der Titelleiste oder per Taste keines — dann gilt die Auswahl des Baums.
function treeCommands(tree, view, log) {
  const address = (n) => `${n.hub}:${n.collection}`;
  const where = (n) => (n.name ? `${address(n)} ${n.name}` : address(n));
  // Die gewählten Elemente: die Auswahl aus dem Kontextmenü, sonst das Element, sonst die
  // Auswahl des Baums.
  const chosen = (n, all) => {
    if (Array.isArray(all) && all.length) return all;
    if (n) return [n];
    return [...(view.selection || [])];
  };
  // Wo eine neue Datei oder ein neuer Ordner hinkommt: das Element, bei einem Dokument sein
  // Verzeichnis; ohne Element die einzige schreibbare Collection oder eine Wahl.
  const targetDir = async (n) => {
    n = n || (view.selection || [])[0];
    if (n) return n.kind === 'document' ? tree.getParent(n) : n;
    const w = tree.shown().filter((c) => c.readable && c.writable);
    if (!w.length) {
      vscode.window.showWarningMessage('Kephalaion: keine schreibbare Collection im Baum.');
      return undefined;
    }
    const c = w.length === 1 ? w[0]
      : (await vscode.window.showQuickPick(w.map((x) => ({ label: x.address, c: x })), { placeHolder: 'In welche Collection?' }) || {}).c;
    return c ? tree.node(c.hub, c.collection, '', 'collection') : undefined;
  };
  const askName = (prompt, value, selection) => vscode.window.showInputBox({
    prompt, value, valueSelection: selection, validateInput: (v) => rules.treeNameError(v),
  });

  const newEntry = (folder) => async (n) => {
    const dir = await targetDir(n);
    if (!dir) return;
    const name = await askName(`${folder ? 'Neuer Ordner' : 'Neue Datei'} in ${where(dir)} — Name, auch mit Unterverzeichnis (a/b.md)`);
    if (!name) return;
    const full = dir.name ? `${dir.name}/${name}` : name;
    const uri = docUri(dir.hub, dir.collection, full);
    try {
      if (await uriExists(uri)) throw new Error(`Kephalaion: ${address(dir)} ${full} gibt es schon; nichts angelegt`);
      if (folder) {
        await vscode.workspace.fs.createDirectory(uri);
      } else {
        await vscode.workspace.fs.writeFile(uri, new Uint8Array());
        await vscode.commands.executeCommand('vscode.open', uri);
      }
    } catch (e) {
      showTreeError(log, `${folder ? 'neuer Ordner' : 'neue Datei'} ${address(dir)} ${full}`, e);
    }
  };

  const rename = async (n) => {
    n = n || (view.selection || [])[0];
    if (!n || n.kind === 'collection') return;
    const start = n.name.lastIndexOf('/') + 1;
    const dot = n.kind === 'document' ? n.name.lastIndexOf('.') : -1;
    const to = await askName(`Umbenennen in ${address(n)} — neuer Name, auch in ein anderes Verzeichnis (a/b.md)`, n.name,
      [start, dot > start ? dot : n.name.length]);
    if (!to || to === n.name) return;
    try {
      await vscode.workspace.fs.rename(n.uri, docUri(n.hub, n.collection, to), { overwrite: false });
    } catch (e) {
      showTreeError(log, `umbenennen ${where(n)} → ${to}`, e);
    }
  };

  const remove = async (n, all) => {
    const list = chosen(n, all).filter((x) => x.kind !== 'collection');
    if (!list.length) return;
    const dirs = list.filter((x) => x.kind === 'directory').length;
    const what = list.length === 1 ? `„${where(list[0])}“` : `${list.length} Einträge`;
    const detail = dirs ? 'Ein Verzeichnis wird mit allen Dokumenten darunter gelöscht. ' : '';
    const ok = await vscode.window.showWarningMessage(`${what} löschen?`,
      { modal: true, detail: `${detail}Das geht über den Hub und lässt sich hier nicht rückgängig machen.` }, 'Löschen');
    if (ok !== 'Löschen') return;
    for (const x of list) {
      try {
        await vscode.workspace.fs.delete(x.uri, { recursive: x.kind === 'directory', useTrash: false });
      } catch (e) {
        showTreeError(log, `löschen ${where(x)}`, e);
      }
    }
  };

  return {
    'kephalaion.tree.newFile': newEntry(false),
    'kephalaion.tree.newFolder': newEntry(true),
    'kephalaion.tree.rename': rename,
    'kephalaion.tree.delete': remove,
    'kephalaion.tree.refresh': () => tree.reshape(true),
    'kephalaion.tree.openToSide': (n, all) => {
      for (const x of chosen(n, all).filter((y) => y.kind === 'document')) {
        vscode.commands.executeCommand('vscode.open', x.uri, vscode.ViewColumn.Beside);
      }
    },
    'kephalaion.tree.copyUri': (n, all) => vscode.env.clipboard.writeText(chosen(n, all).map((x) => x.uri.toString()).join('\n')),
    'kephalaion.tree.copyRelativePath': (n, all) => vscode.env.clipboard.writeText(chosen(n, all).map((x) => x.name).join('\n')),
    // Vom Baum ins Projekt, auch wenn der Explorer ein Ablegen aus dem Baum nicht annimmt: Ziel
    // ist ein Ordner nach Wahl, vorgeschlagen der Projektordner; nie überschreiben.
    'kephalaion.tree.copyToProject': async (n, all) => {
      const list = chosen(n, all);
      if (!list.length) return;
      const project = (vscode.workspace.workspaceFolders || []).find((f) => f.uri.scheme === 'file' || f.uri.scheme === 'vscode-remote');
      const picked = await vscode.window.showOpenDialog({
        canSelectFiles: false, canSelectFolders: true, canSelectMany: false, defaultUri: project && project.uri,
        openLabel: 'Hierher kopieren', title: `${list.length === 1 ? where(list[0]) : `${list.length} Einträge`} in den Projektordner kopieren`,
      });
      if (!picked || !picked.length) return;
      const results = [];
      for (const x of list) {
        const dst = vscode.Uri.joinPath(picked[0], x.name ? x.name.split('/').pop() : x.collection);
        try {
          results.push(await copyTree(tree.kfs, x.uri, dst));
        } catch (e) {
          showTreeError(log, `in den Projektordner ${where(x)} → ${dst.toString()}`, e);
        }
      }
      if (results.length) reportCopy(log, `nach ${picked[0].toString()} kopiert`, results);
    },
  };
}

// --- Aktivierung ---

let ext;

function activate(context) {
  ext = context.extension;
  const out = vscode.window.createOutputChannel('Kephalaion');
  const log = (s) => out.appendLine(`${new Date().toISOString()} ${s}`);
  let status;
  const node = new Node(log, () => status && status.render());
  status = new Status(node, log);
  const kfs = new KephFs(node, status, log);
  const tree = new CollectionTree(kfs, status, log);
  const view = vscode.window.createTreeView(VIEW_ID, {
    treeDataProvider: tree,
    showCollapseAll: true,
    canSelectMany: true,
    dragAndDropController: new TreeDragAndDrop(tree, log),
  });
  // Das aktive keph://-Dokument im Baum zeigen, ohne den Fokus zu nehmen — nur, wenn der Baum
  // sichtbar ist und die Collection zeigt.
  const revealActive = () => {
    const ed = vscode.window.activeTextEditor;
    if (!view.visible || !ed || !ed.document || !ed.document.uri || ed.document.uri.scheme !== SCHEME) return;
    const n = tree.nodeFor(ed.document.uri, 'document');
    if (!n || !tree.info(n.hub, n.collection)) return;
    Promise.resolve(view.reveal(n, { select: true, focus: false, expand: false })).catch((e) => {
      log(`Baum: ${ed.document.uri.toString()} nicht gezeigt: ${e.message}`);
    });
  };

  // Eine Warteschlange für Aktivierung und Ordnerereignis (Festlegung 6): Das Herausnehmen der
  // keph://-Ordner überholt sich nicht. Zuerst wird der Schlüssel aus der Zeit vor Task 044
  // geleert.
  let folderQueue = Promise.resolve();
  const enqueue = (fn) => {
    folderQueue = folderQueue.then(fn).catch((e) => log(`Workspace-Ordner: ${e.message}`));
    return folderQueue;
  };
  const folderState = {};
  const syncFolders = () => enqueue(() => syncWorkspaceFolders(tree, log, folderState));
  if (context.workspaceState.get(REMOVED_FOLDERS) !== undefined) {
    enqueue(() => context.workspaceState.update(REMOVED_FOLDERS, undefined));
  }
  context.subscriptions.push(vscode.workspace.onDidChangeWorkspaceFolders(syncFolders));
  syncFolders();

  for (const [id, fn] of Object.entries({ ...treeCommands(tree, view, log), ...findCommands(tree, kfs, node, log),
    ...selectionCommands(tree, status, log) })) {
    context.subscriptions.push(vscode.commands.registerCommand(id, fn));
  }
  context.subscriptions.push(
    view,
    tree._changed,
    vscode.window.onDidChangeActiveTextEditor(revealActive),
    view.onDidChangeVisibility(revealActive),
    vscode.workspace.onDidChangeConfiguration((e) => {
      if (e.affectsConfiguration('kephalaion.collections')) tree.reshape(true);
    }),
  );

  context.subscriptions.push(
    out,
    status.item,
    vscode.workspace.registerFileSystemProvider(SCHEME, kfs, {
      isCaseSensitive: true,
      isReadonly: false,
    }),
    vscode.commands.registerCommand('kephalaion.refresh', () => {
      node.invalidate();
      return status.refresh();
    }),
    vscode.commands.registerCommand('kephalaion.chooseAccount', async () => {
      const all = tokenAccounts();
      const hubs = Object.keys(all).sort();
      if (hubs.length === 0) {
        vscode.window.showWarningMessage(`Kephalaion: keine Token-Dateien unter ${tokensDir()}.`);
        return;
      }
      const creds = node.credentials();
      const items = [];
      for (const hub of hubs) {
        items.push({ label: hub, kind: vscode.QuickPickItemKind.Separator });
        for (const account of all[hub]) {
          const active = creds[hub] && creds[hub].account === account;
          items.push({
            label: `${active ? '$(check)' : '$(blank)'} ${account}`,
            description: active ? `aktiv — ${rules.choiceText(creds[hub].how)}` : '',
            detail: `Hub ${hub}`,
            hub,
            account,
          });
        }
      }
      const pick = await vscode.window.showQuickPick(items, { placeHolder: 'Account je Hub wählen' });
      if (!pick || !pick.hub) return;
      const cfg = vscode.workspace.getConfiguration('kephalaion');
      // In den Einstellungen des Benutzers — auf diesem Rechner, nicht synchronisiert
      // (Scope machine-overridable); ein Workspace kann es überschreiben.
      await cfg.update('accounts', { ...chosenAccounts(), [pick.hub]: pick.account }, vscode.ConfigurationTarget.Global);
      log(`Hub ${pick.hub}: Account ${pick.account} gewählt`);
    }),
    vscode.commands.registerCommand('kephalaion.showLog', () => out.show()),
    vscode.commands.registerCommand('kephalaion.showStatus', async () => {
      await status.refresh();
      out.appendLine('');
      for (const l of status.summary()) out.appendLine(l);
      out.show();
    }),
    vscode.commands.registerCommand('kephalaion.menu', async () => {
      const pick = await vscode.window.showQuickPick([
        { label: '$(info) Status anzeigen', cmd: 'kephalaion.showStatus' },
        { label: '$(refresh) Neu verbinden', cmd: 'kephalaion.refresh' },
        { label: '$(account) Account wählen', cmd: 'kephalaion.chooseAccount' },
        { label: '$(list-tree) Collection im Baum zeigen', cmd: 'kephalaion.showInTree' },
        { label: '$(list-flat) Collection aus dem Baum nehmen', cmd: 'kephalaion.hideFromTree' },
        { label: '$(go-to-file) Dokument öffnen', cmd: 'kephalaion.openDocument' },
        { label: '$(search) Suchen (docs/)', cmd: 'kephalaion.search' },
        { label: '$(output) Log anzeigen', cmd: 'kephalaion.showLog' },
      ]);
      if (pick) vscode.commands.executeCommand(pick.cmd);
    }),
  );

  // Der Node als MCP-Server für Copilot; die API gibt es ab VS Code 1.101.
  const mcp = new McpProvider(log, node);
  if (vscode.lm && typeof vscode.lm.registerMcpServerDefinitionProvider === 'function') {
    context.subscriptions.push(
      vscode.lm.registerMcpServerDefinitionProvider(MCP_PROVIDER_ID, mcp),
      mcp._changed,
      vscode.env.onDidChangeLogLevel(() => mcp.check()),
    );
  } else {
    log('MCP-Server nicht gemeldet: diese Fassung von VS Code kennt vscode.lm.registerMcpServerDefinitionProvider nicht');
  }

  // Andere Wahl des Accounts oder andere Adresse — auch von Hand in settings.json.
  context.subscriptions.push(vscode.workspace.onDidChangeConfiguration((e) => {
    if (e.affectsConfiguration('kephalaion.mcpServer.enabled')) mcp.check();
    if (!e.affectsConfiguration('kephalaion.accounts') && !e.affectsConfiguration('kephalaion.nodeUrl')
      && !e.affectsConfiguration('kephalaion.hubs')) return;
    node.invalidate();
    kfs.rebase(hubSelection().hubs);
    status.refresh();
    mcp.check();
  }));

  // Beim Start: Adresse, Ort und Quelle der config (Task 035), der Fehler bei zwei Arten.
  const target = nodeTarget();
  const loc = target.location || locateConfig();
  log(`Node: ${target.endpoint || `(keine Adresse: ${target.error})`}, config ${loc.error ? `unbekannt (${loc.error})`
    : `${loc.path} (${rules.configSourceText(loc.source)})`}`);
  if (bothKindsWarning(loc)) log(`Fehler: ${bothKindsWarning(loc)}`);
  status.refresh();
  // Alle 30 s: Status, und ob sich Token-Dateien geändert haben (rotate, neuer Account).
  const timer = setInterval(() => {
    status.refresh();
    mcp.check();
  }, POLL_MS);
  let polling = false;
  const pollChanges = async () => {
    if (polling) return;
    polling = true;
    try {
      await kfs.pollChanges();
    } finally {
      polling = false;
    }
  };
  pollChanges();
  const changesTimer = setInterval(pollChanges, CHANGES_MS);
  context.subscriptions.push(new vscode.Disposable(() => {
    clearInterval(timer);
    clearInterval(changesTimer);
  }));
}

function deactivate() {}

// setSystemPath nur für die Tests (vscode/test/extension.test.js), wie SystemPath im Binary.
module.exports = { activate, deactivate, setSystemPath };
