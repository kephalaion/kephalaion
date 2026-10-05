// Kephalaion in VS Code: Status aus whoami und ein FileSystemProvider für keph://.
// Lesen über list, read und changes aus der Replica; schreiben über create, write, delete
// und rename, die der Node an den Hub reicht. Siehe docs/vscode.md.

const vscode = require('vscode');
const fs = require('fs');
const os = require('os');
const path = require('path');
const rules = require('./rules');

const SCHEME = 'keph';
const POLL_MS = 30000;

// --- Orte (docs/konzept.md, „Orte nach XDG“) ---

function configDir() {
  const xdg = process.env.XDG_CONFIG_HOME || path.join(os.homedir(), '.config');
  return path.join(xdg, 'kephalaion');
}

function configFile() {
  return process.env.KEPHALAION_CONFIG || path.join(configDir(), 'config.yaml');
}

// listen aus dem Abschnitt node: der config — ohne YAML-Bibliothek, die Datei ist flach.
function nodeListen(file) {
  let text;
  try {
    text = fs.readFileSync(file, 'utf8');
  } catch {
    return undefined;
  }
  let section = '';
  for (const line of text.split('\n')) {
    const top = line.match(/^([A-Za-z_]+):\s*$/);
    if (top) {
      section = top[1];
      continue;
    }
    const m = line.match(/^\s+listen:\s*["']?([^"'\s#]+)/);
    if (m && section === 'node') return m[1];
  }
  return undefined;
}

// Die Adresse des Nodes: kephalaion.nodeUrl, geprüft wie node dir --node (rules.parseNodeUrl:
// http nur zu Loopback und host.docker.internal, sonst https), sonst listen aus der config.
// Liefert { endpoint, remote } oder { error }.
function nodeTarget() {
  const set = vscode.workspace.getConfiguration('kephalaion').get('nodeUrl');
  if (set) {
    try {
      return rules.parseNodeUrl(set);
    } catch (e) {
      return { error: e.message };
    }
  }
  let listen = nodeListen(configFile());
  if (!listen) return { error: `keine Adresse des Nodes: listen fehlt in ${configFile()}` };
  if (listen.startsWith(':')) listen = '127.0.0.1' + listen;
  return { endpoint: `http://${listen}/mcp`, remote: false };
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
  const base = path.join(configDir(), 'tokens');
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

// Der gewählte Account je Hub steht in der Einstellung kephalaion.accounts ({hub: account}).
function chosenAccounts() {
  return vscode.workspace.getConfiguration('kephalaion').get('accounts') || {};
}

// Welcher Account je Hub benutzt wird und warum: only (einziger), chosen (Einstellung),
// first (mehrere, keiner oder ein unbekannter gewählt — der erste nach Namen).
// Nur die Hubs aus hubSelection: An eine entfernte nodeUrl gehen nur die gewählten.
function readCredentials(log) {
  const base = path.join(configDir(), 'tokens');
  const chosen = chosenAccounts();
  const creds = {};
  const all = tokenAccounts();
  for (const hub of hubSelection().hubs) {
    const accounts = all[hub];
    if (!accounts) continue;
    let account = accounts[0];
    let how = accounts.length === 1 ? 'only' : 'first';
    if (chosen[hub] && accounts.includes(chosen[hub])) {
      account = chosen[hub];
      how = 'chosen';
    } else if (chosen[hub]) {
      log(`Hub ${hub}: gewählter Account ${chosen[hub]} hat keine Token-Datei, nehme ${account}`);
    }
    let token = '';
    try {
      token = fs.readFileSync(path.join(base, hub, `${account}.token`), 'utf8').split('\n')[0].trim();
    } catch (e) {
      log(`Hub ${hub}: Token-Datei ${account}.token nicht lesbar: ${e.message}`);
    }
    const missing = how === 'first' && chosen[hub] ? chosen[hub] : undefined;
    creds[hub] = { account, token, accounts, how, missing };
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
// die Hubs aus hubSelection (rules.mcpLogins). Nur Namen, kein Token.
function mcpLogins() {
  const sel = hubSelection();
  const out = rules.mcpLogins(tokenAccounts(), chosenAccounts(), sel.hubs);
  if (sel.error) out.skipped.push(sel.error);
  return out;
}

// Die Header-Paare für den Start des Servers, aus den Token-Dateien. Meldungen nennen nie ein
// Token.
function mcpHeaders(log) {
  const base = path.join(configDir(), 'tokens');
  const { logins, skipped } = mcpLogins();
  for (const s of skipped) log(`MCP-Server: Hub ${s}`);
  return rules.mcpHeaders(logins,
    (hub, account) => fs.readFileSync(path.join(base, hub, `${account}.token`), 'utf8').split('\n')[0].trim(), log);
}

// Stand, an dem sich zeigt, ob die Definition neu zu melden ist: Adresse, Einstellung, Trace,
// Hubs mit ihren Accounts und die Token-Dateien (Name, Größe, Zeit) — ohne ihren Inhalt.
function mcpState() {
  const base = path.join(configDir(), 'tokens');
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
  return JSON.stringify([nodeUrl() || '', mcpEnabled(), traceLogging(), chosenAccounts(), chosenHubs() || [],
    files.sort()]);
}

class McpProvider {
  constructor(log) {
    this.log = log;
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

  resolveMcpServerDefinition(server) {
    if (!mcpEnabled() || traceLogging()) return undefined;
    server.headers = mcpHeaders(this.log);
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
// die Anfrage abgelehnt, bevor ein Werkzeug lief).
function nodeError(message, kind) {
  const err = new Error(message);
  err.kind = kind;
  return err;
}

class Node {
  constructor(log) {
    this.log = log;
    this.id = 0;
  }

  async call(tool, args) {
    return (await this.request(tool, args)).structuredContent;
  }

  // Wie call, dazu der Text des Ergebnisses. Bei read ist er nur das JSON der Struktur, der
  // Inhalt steht in data.content (Task 024); den Text braucht readFile nur für einen älteren
  // Node, bei dem er der Inhalt war.
  async callWithText(tool, args) {
    const r = await this.request(tool, args);
    return { data: r.structuredContent, text: (r.content || []).map((c) => c.text || '').join('') };
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

  // Nach einer anderen Wahl oder Einstellung sofort neu lesen.
  invalidate() {
    this._creds = undefined;
  }

  // Ein Aufruf, nie wiederholt — auch kein Schreibvorgang. Die Antwort eines Werkzeugs mit
  // Fehler trägt toolError, den Text als toolText und, bei den Werkzeugen, die schreiben, den
  // Code aus error.code als toolCode; nach ihm wird entschieden, nicht nach der Meldung.
  async request(tool, args) {
    const target = nodeTarget();
    const url = target.endpoint;
    if (!url) throw nodeError(target.error, 'unreachable');
    const sel = hubSelection();
    if (sel.error) throw nodeError(sel.error, 'unreachable');
    const headers = {
      'Content-Type': 'application/json',
      Accept: 'application/json, text/event-stream',
    };
    for (const [hub, c] of Object.entries(this.credentials())) {
      if (!c.token) continue;
      headers[`X-Keph-Account-${hub}`] = c.account;
      headers[`X-Keph-Token-${hub}`] = c.token;
    }
    const body = { jsonrpc: '2.0', id: ++this.id, method: 'tools/call', params: { name: tool, arguments: args || {} } };
    let res;
    try {
      // Keiner Weiterleitung folgen: Der Node sendet nie eine, und fetch schickte die Header-Paare
      // mit an das neue Ziel.
      res = await fetch(url, { method: 'POST', headers, body: JSON.stringify(body), redirect: 'manual' });
    } catch (e) {
      const code = e.cause && e.cause.code;
      const why = code || (e.cause && e.cause.message) || e.message;
      if (NOT_SENT.has(code)) throw nodeError(`Node ${url} nicht erreichbar (${why})`, 'unreachable');
      throw nodeError(`Verbindung zum Node ${url} abgebrochen (${why})`, 'unclear');
    }
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
    return msg.result;
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
      this.who = undefined;
      this.error = e.message;
      this.log(`whoami: ${this.error}`);
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

  // Hinweis zur Wahl des Accounts eines Hubs, leer bei nur einem Account.
  accountNote(hub) {
    const c = this.node.credentials()[hub];
    if (!c || c.accounts.length < 2) return '';
    if (c.missing) return `gewählter Account ${c.missing} hat keine Token-Datei — benutzt ${c.account}; wählen im Menü`;
    if (c.how === 'first') return `mehrere Accounts (${c.accounts.join(', ')}), keiner gewählt — benutzt ${c.account}; wählen im Menü`;
    return `gewählt aus ${c.accounts.join(', ')}`;
  }

  // Hubs, an denen mehrere Accounts liegen und keiner gewählt ist.
  unchosen() {
    return Object.entries(this.node.credentials()).filter(([, c]) => c.how === 'first').map(([h]) => h);
  }

  // Dasselbe wie der Tooltip, als Text — für „Kephalaion: Status anzeigen“.
  summary() {
    const lines = [`Kephalaion — Node ${nodeUrl() || '(keine Adresse)'}`];
    if (!this.who) {
      lines.push(`nicht erreichbar: ${this.error}`);
      return lines;
    }
    if (this.who.hidden) {
      lines.push(`Keine gültige Anmeldung am Node (über den Proxy nennt er dann weder Version noch Hubs): ${HIDDEN_HINT}`);
      return lines;
    }
    lines.push(`Version Node ${this.who.version}, Erweiterung ${ext.packageJSON.version}`);
    for (const h of this.hubs()) {
      let l = `Hub ${h.hub} (Node ${h.node}): Anmeldung ${h.login}`;
      if (h.login === 'ok') l += `, Account ${h.account}, User ${h.user}`;
      lines.push(l);
      if (this.accountNote(h.hub)) lines.push(`  ${this.accountNote(h.hub)}`);
      for (const c of h.collections || []) lines.push(`  ${c.address}: ${c.rights.join(', ')}`);
      const sy = h.sync;
      lines.push(sy.never_synced ? '  noch nie abgeglichen' : `  abgeglichen ${sy.last_success}, Revision ${sy.revision}`);
      if (sy.last_error) lines.push(`  letzter Fehler ${sy.last_error_at}: ${sy.last_error}`);
    }
    if ((this.who.unknown_hubs || []).length) lines.push(`Token-Verzeichnisse ohne Hub am Node: ${this.who.unknown_hubs.join(', ')}`);
    return lines;
  }

  render() {
    const it = this.item;
    if (!this.who) {
      it.text = '$(warning) Keph: Node nicht erreichbar';
      it.tooltip = this.error;
      it.backgroundColor = new vscode.ThemeColor('statusBarItem.warningBackground');
      return;
    }
    if (this.who.hidden) {
      it.text = '$(warning) Keph: nicht angemeldet';
      it.tooltip = `Keine gültige Anmeldung am Node ${nodeUrl()}: ${HIDDEN_HINT}`;
      it.backgroundColor = new vscode.ThemeColor('statusBarItem.warningBackground');
      return;
    }
    const hubs = this.hubs();
    const unchosen = this.unchosen();
    const bad = hubs.filter((h) => h.login !== 'ok' || h.sync.last_error || unchosen.includes(h.hub));
    it.text = `${bad.length ? '$(warning)' : '$(database)'} Keph ${hubs.map((h) => h.hub).join(' ')}`;
    it.backgroundColor = bad.length ? new vscode.ThemeColor('statusBarItem.warningBackground') : undefined;

    const md = new vscode.MarkdownString();
    md.appendMarkdown(`**Kephalaion** — Node ${this.who.version}, Erweiterung ${ext.packageJSON.version}\n\n`);
    for (const h of hubs) {
      md.appendMarkdown(`**${h.hub}** (Node ${h.node}): Anmeldung \`${h.login}\``);
      if (h.login === 'ok') md.appendMarkdown(`, Account ${h.account}, User ${h.user}`);
      md.appendMarkdown('\n\n');
      if (this.accountNote(h.hub)) md.appendMarkdown(`${this.accountNote(h.hub)}\n\n`);
      for (const c of h.collections || []) md.appendMarkdown(`- \`${c.address}\` — ${c.rights.join(', ')}\n`);
      const s = h.sync;
      if (s.never_synced) md.appendMarkdown('\nnoch nie abgeglichen\n\n');
      else md.appendMarkdown(`\nabgeglichen ${s.last_success}, Revision ${s.revision}\n\n`);
      if (s.last_error) md.appendMarkdown(`letzter Fehler ${s.last_error_at}: ${s.last_error}\n\n`);
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
// ein Fehler der Verbindung Unavailable.
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
      d = await this.node.call('read', { collection: `${hub}:${col}`, name, content: false });
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
        const r = await this.node.call('list', args);
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

  async readFile(uri) {
    const { hub, col, name } = this.split(uri);
    if (!col || !name) throw vscode.FileSystemError.FileIsADirectory(uri);
    let r;
    try {
      r = await this.node.callWithText('read', { collection: `${hub}:${col}`, name });
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
      d = await this.node.call('read', { collection: `${hub}:${col}`, name, content: false });
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
      r = await this.node.call(tool, args);
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
    try {
      r = await this.node.call('changes', this.cursor ? { cursor: this.cursor } : {});
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
    if (r.more) return this.pollChanges();
  }
}

// --- Aktivierung ---

let ext;

function activate(context) {
  ext = context.extension;
  const out = vscode.window.createOutputChannel('Kephalaion');
  const log = (s) => out.appendLine(`${new Date().toISOString()} ${s}`);
  const node = new Node(log);
  const status = new Status(node, log);
  const kfs = new KephFs(node, status, log);

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
        vscode.window.showWarningMessage(`Kephalaion: keine Token-Dateien unter ${path.join(configDir(), 'tokens')}.`);
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
            description: active ? (creds[hub].how === 'first' ? 'benutzt, nicht gewählt' : 'aktiv') : '',
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
    vscode.commands.registerCommand('kephalaion.addCollection', async () => {
      await status.refresh();
      const items = status.hubs().flatMap((h) => status.collections(h.hub).map((c) => ({
        label: c.address,
        description: c.rights.join(', '),
        hub: h.hub,
        collection: c.collection,
      })));
      if (items.length === 0) {
        vscode.window.showWarningMessage('Kephalaion: keine lesbare Collection — Anmeldung prüfen (Statusleiste).');
        return;
      }
      const pick = await vscode.window.showQuickPick(items, { placeHolder: 'Collection als Ordner einbinden' });
      if (!pick) return;
      const n = vscode.workspace.workspaceFolders ? vscode.workspace.workspaceFolders.length : 0;
      vscode.workspace.updateWorkspaceFolders(n, 0, {
        uri: docUri(pick.hub, pick.collection, ''),
        name: `Keph ${pick.label}`,
      });
    }),
    vscode.commands.registerCommand('kephalaion.menu', async () => {
      const pick = await vscode.window.showQuickPick([
        { label: '$(info) Status anzeigen', cmd: 'kephalaion.showStatus' },
        { label: '$(refresh) Neu verbinden', cmd: 'kephalaion.refresh' },
        { label: '$(account) Account wählen', cmd: 'kephalaion.chooseAccount' },
        { label: '$(folder-library) Collection einbinden', cmd: 'kephalaion.addCollection' },
        { label: '$(output) Log anzeigen', cmd: 'kephalaion.showLog' },
      ]);
      if (pick) vscode.commands.executeCommand(pick.cmd);
    }),
  );

  // Der Node als MCP-Server für Copilot; die API gibt es ab VS Code 1.101.
  const mcp = new McpProvider(log);
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

  log(`Node: ${nodeUrl() || '(keine Adresse)'}, config ${configFile()}`);
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

module.exports = { activate, deactivate };
