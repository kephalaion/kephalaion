// Tests der Regeln der Erweiterung, ohne VS Code: node --test vscode/test/*.test.js (make vscode-test).

'use strict';

const test = require('node:test');
const assert = require('node:assert');
const {
  parseNodeUrl, selectHubs, mcpLogins, mcpHeaders, explainResponse, versionHint, hubStatus, whoamiOutcome, droppedHubs,
} = require('../rules');

test('nodeUrl: lokal, Proxy, host.docker.internal', () => {
  assert.deepStrictEqual(parseNodeUrl('http://127.0.0.1:7433'), { endpoint: 'http://127.0.0.1:7433/mcp', remote: false });
  assert.deepStrictEqual(parseNodeUrl('http://localhost:7433/'), { endpoint: 'http://localhost:7433/mcp', remote: false });
  assert.deepStrictEqual(parseNodeUrl('http://[::1]:7433'), { endpoint: 'http://[::1]:7433/mcp', remote: false });
  assert.deepStrictEqual(parseNodeUrl('http://host.docker.internal:7433'),
    { endpoint: 'http://host.docker.internal:7433/mcp', remote: true });
  assert.deepStrictEqual(parseNodeUrl('https://node.example.org/kephalaion/'),
    { endpoint: 'https://node.example.org/kephalaion/mcp', remote: true });
  assert.deepStrictEqual(parseNodeUrl('https://127.0.0.1:8443/kephalaion'),
    { endpoint: 'https://127.0.0.1:8443/kephalaion/mcp', remote: true });
});

test('nodeUrl: http zu einem fremden Host und anderes abgelehnt', () => {
  assert.throws(() => parseNodeUrl('http://node.example.org:7433'), /http nur zu diesem Rechner/);
  assert.throws(() => parseNodeUrl('http://9.141.8.157:7433'), /http nur zu diesem Rechner/);
  assert.throws(() => parseNodeUrl('https://node.example.org/kephalaion/mcp'), /ohne \/mcp am Ende/);
  assert.throws(() => parseNodeUrl('https://a:b@node.example.org/k'), /ohne Benutzer/);
  assert.throws(() => parseNodeUrl('https://node.example.org/k?x=1'), /ohne Benutzer, Query/);
  assert.throws(() => parseNodeUrl('ftp://node.example.org'), /erwartet http/);
  assert.throws(() => parseNodeUrl('127.0.0.1:7433'), /erwartet http/);
});

test('Wahl der Hubs: lokal alle, entfernt nur die gewählten, fest', () => {
  assert.deepStrictEqual(selectHubs(['vm', 'eigen'], undefined, false), { hubs: ['eigen', 'vm'] });
  assert.deepStrictEqual(selectHubs(['vm', 'eigen'], ['vm'], true), { hubs: ['vm'] });
  // Ein Hub kommt unter tokens/ hinzu: die Wahl bleibt.
  assert.deepStrictEqual(selectHubs(['vm', 'eigen', 'neu'], ['vm'], true), { hubs: ['vm'] });
  // Ohne Wahl nur bei genau einem Hub.
  assert.deepStrictEqual(selectHubs(['vm'], [], true), { hubs: ['vm'] });
  const two = selectHubs(['vm', 'eigen'], undefined, true);
  assert.deepStrictEqual(two.hubs, []);
  assert.match(two.error, /mehrere Hubs unter tokens\/ \(eigen, vm\).*kephalaion\.hubs/);
  assert.match(selectHubs([], undefined, true).error, /keine Token-Datei/);
  assert.match(selectHubs(['vm'], ['System'], true).error, /kein gültiger Alias/);
  // Ungültige Verzeichnisnamen zählen lokal nicht.
  assert.deepStrictEqual(selectHubs(['vm', 'Groß', 'system-x'], undefined, false), { hubs: ['vm'] });
});

test('Antworten des Proxys statt des Nodes', () => {
  assert.match(explainResponse(302, 'text/html'), /Weiterleitung.*Präfix falsch oder Anmeldung des Proxys/);
  assert.match(explainResponse(0, ''), /Weiterleitung/);
  assert.match(explainResponse(401, 'text/plain'), /Anmeldung des Proxys, nicht der Node.*Präfix falsch/);
  assert.match(explainResponse(200, 'text/html; charset=utf-8'), /keine Antwort eines MCP-Servers.*Präfix falsch/);
  assert.match(explainResponse(403, 'text/plain'), /Host-Prüfung des Nodes/);
  assert.match(explainResponse(404, 'text/plain'), /Präfix falsch\?/);
  assert.match(explainResponse(502, ''), /der Node dahinter nicht/);
  assert.strictEqual(explainResponse(200, 'application/json'), undefined);
  assert.strictEqual(explainResponse(200, 'text/event-stream'), undefined);
});

test('Header-Paare an eine entfernte nodeUrl: nur die gewählten Hubs, auch nach einem neuen', () => {
  const tokA = `keph_${'A'.repeat(43)}`;
  const tokB = `keph_${'B'.repeat(43)}`;
  const files = { vm: { kamran: tokA }, eigen: { kp: tokB } };
  const accounts = () => Object.fromEntries(Object.entries(files).map(([h, a]) => [h, Object.keys(a).sort()]));
  const read = (hub, account) => files[hub][account];
  const pairs = (chosenHubs, remote) => {
    const sel = selectHubs(Object.keys(files), chosenHubs, remote);
    return mcpHeaders(mcpLogins(accounts(), {}, sel.hubs).logins, read, () => {});
  };
  assert.deepStrictEqual(pairs(['vm'], true), { 'X-Keph-Account-vm': 'kamran', 'X-Keph-Token-vm': tokA });
  files.neu = { x: tokB };
  assert.deepStrictEqual(pairs(['vm'], true), { 'X-Keph-Account-vm': 'kamran', 'X-Keph-Token-vm': tokA });
  // Ohne Wahl bei mehreren Hubs: kein Paar.
  assert.deepStrictEqual(pairs(undefined, true), {});
  // Lokal alle, wie bisher.
  assert.deepStrictEqual(Object.keys(pairs(undefined, false)).sort(), ['X-Keph-Account-eigen', 'X-Keph-Account-neu',
    'X-Keph-Account-vm', 'X-Keph-Token-eigen', 'X-Keph-Token-neu', 'X-Keph-Token-vm']);
  // Ein gewählter Hub ohne Token-Datei wird genannt, ein falsches Token fehlt.
  const sk = mcpLogins(accounts(), {}, ['vm', 'fehlt']).skipped;
  assert.deepStrictEqual(sk, ['fehlt: gewählt, aber keine Token-Datei unter tokens/fehlt/']);
  files.vm.kamran = 'kein-token';
  const logged = [];
  assert.deepStrictEqual(pairs(['vm'], true), {});
  mcpHeaders([{ hub: 'vm', account: 'kamran' }], read, (l) => logged.push(l));
  assert.deepStrictEqual(logged, ['MCP-Server: Hub vm: kamran.token hält kein gültiges Token']);
});

test('Version: Hinweis nur bei echter Abweichung, nie bei dev oder 0.0.0', () => {
  assert.strictEqual(versionHint('v0.3.0', '0.3.0', 'Visual Studio Code'), undefined);
  // Vorabversion: vX.Y.Z-… trägt die Erweiterung X.Y.Z.
  assert.strictEqual(versionHint('v0.3.0-rc1', '0.3.0', 'Visual Studio Code'), undefined);
  assert.strictEqual(versionHint('dev', '0.3.0', 'Visual Studio Code'), undefined);
  assert.strictEqual(versionHint('v0.3.0', '0.0.0', 'Visual Studio Code'), undefined);
  assert.strictEqual(versionHint('v0.0.0', '0.3.0', 'Visual Studio Code'), undefined);
  assert.strictEqual(versionHint(undefined, '0.3.0', 'Visual Studio Code'), undefined);
  assert.strictEqual(versionHint('v0.4.0', '0.3.0', 'Visual Studio Code'),
    'Erweiterung 0.3.0 passt nicht zum Node 0.4.0 — im Terminal „kephalaion vscode install“, danach „Developer: Reload Window“.');
  assert.match(versionHint('v0.4.0-rc2', '0.3.0', 'Visual Studio Code'), /Node 0\.4\.0 /);
});

test('Version: der Hinweis nennt das CLI des Editors', () => {
  assert.match(versionHint('v0.4.0', '0.3.0', 'Cursor'), /„kephalaion vscode install --code cursor“/);
  assert.match(versionHint('v0.4.0', '0.3.0', 'VSCodium'), /--code codium“/);
  assert.match(versionHint('v0.4.0', '0.3.0', 'Visual Studio Code - Insiders'), /--code code-insiders“/);
  assert.match(versionHint('v0.4.0', '0.3.0', undefined), /„kephalaion vscode install“/);
});

// Die Statusleiste (Task 030): Ein Node bedient viele Hubs, ein Client nutzt davon einige — nur
// genutzte Hubs warnen. Die Fälle folgen dem Wegwerf-Node aus dem Befund vscode-betrieb.md
// (2026-10-08): zwei Einträge auf denselben Hub, Token-Datei nur für einen.
test('Statusleiste: ein Hub ohne Token des Clients ist missing und keine Warnung', () => {
  const synced = { last_success: '2026-10-08T15:39:58Z', revision: 1 };
  const ok = { hub: 't030a', login: 'ok', node: 'n030-local', account: 'acc030', user: 'u030', sync: synced };
  const miss = { hub: 't030b', login: 'missing', node: 'n030-http', sync: synced };
  assert.deepStrictEqual(hubStatus([ok, miss], { t030a: { account: 'acc030' } }, []), {
    hubs: [{ hub: 't030a', used: true }, { hub: 't030b', used: false, note: 'nicht genutzt — kein Token' }],
    used: ['t030a'],
    warning: undefined,
  });
  // missing trotz Token-Datei: die Datei ist leer oder nicht lesbar (readCredentials liefert
  // token '', das Paar geht nicht hinaus) — Warnung, die Meldung nennt die Datei.
  let st = hubStatus([ok, miss], { t030a: { account: 'acc030' }, t030b: { account: 'acc030', token: '' } }, []);
  assert.deepStrictEqual(st.used, ['t030a', 't030b']);
  assert.strictEqual(st.hubs[1].warning, 'Token-Datei tokens/t030b/acc030.token leer oder nicht lesbar');
  assert.strictEqual(st.warning, 't030b: Token-Datei tokens/t030b/acc030.token leer oder nicht lesbar');
  // missing mit „Replica nicht lesbar“: ein Abgleichfehler, nicht die Datei.
  const unreadable = { ...miss, sync: { last_success: synced.last_success, last_error: 'Replica nicht lesbar' } };
  st = hubStatus([ok, unreadable], { t030a: { account: 'acc030' }, t030b: { account: 'acc030', token: '' } }, []);
  assert.strictEqual(st.hubs[1].warning, 'Abgleich: Replica nicht lesbar');
  // Derselbe Hub ohne Token des Clients: nicht genutzt, auch mit diesem Fehler keine Warnung.
  st = hubStatus([ok, unreadable], { t030a: { account: 'acc030' } }, []);
  assert.strictEqual(st.warning, undefined);
  assert.strictEqual(st.hubs[1].note, 'nicht genutzt — kein Token');
});

test('Statusleiste: invalid, Abgleichfehler, ohne Wahl, kein genutzter Hub', () => {
  const synced = { last_success: '2026-10-08T15:39:58Z', revision: 1 };
  const ok = { hub: 'a', login: 'ok', node: 'n', account: 'x', user: 'u', sync: synced };
  // invalid ist immer genutzt (der Node hat ein Paar gesehen), auch ohne Eintrag in credentials.
  let st = hubStatus([{ hub: 'a', login: 'invalid', node: 'n', sync: synced }], {}, []);
  assert.deepStrictEqual(st.used, ['a']);
  assert.strictEqual(st.warning, 'a: Anmeldung ungültig');
  // invalid mit never_synced liegt nicht am Token (docs/vscode.md, „Status und Auswahl“).
  st = hubStatus([{ hub: 'a', login: 'invalid', node: 'n', sync: { never_synced: true } }], { a: { account: 'x' } }, []);
  assert.match(st.hubs[0].warning, /^noch nie abgeglichen/);
  // Ein Fehler im Abgleich warnt nur am genutzten Hub; am nicht genutzten bleibt er dem Tooltip.
  const failed = { last_success: synced.last_success, revision: 1, last_error: 'Hub nicht erreichbar', last_error_at: '2026-10-08T16:00:00Z' };
  st = hubStatus([{ ...ok, sync: failed }, { hub: 'b', login: 'missing', node: 'n', sync: failed }], { a: { account: 'x' } }, []);
  assert.strictEqual(st.hubs[0].warning, 'Abgleich: Hub nicht erreichbar');
  assert.strictEqual(st.hubs[1].warning, undefined);
  assert.strictEqual(st.warning, 'a: Abgleich: Hub nicht erreichbar');
  // Mehrere Gründe an einem Hub, mehrere Hubs.
  st = hubStatus([{ ...ok, login: 'invalid', sync: failed }, { ...ok, hub: 'b' }], { a: { account: 'x' }, b: { account: 'x' } }, ['b']);
  assert.strictEqual(st.warning, 'a: Abgleich: Hub nicht erreichbar, Anmeldung ungültig; b: mehrere Accounts, keiner gewählt');
  // Kein genutzter Hub: alle missing ohne Token — und gar kein Hub.
  const miss = (hub) => ({ hub, login: 'missing', node: 'n', sync: synced });
  st = hubStatus([miss('a'), miss('b')], {}, []);
  assert.deepStrictEqual(st.used, []);
  assert.match(st.warning, /^kein genutzter Hub/);
  assert.match(hubStatus([], {}, []).warning, /^kein genutzter Hub/);
});

test('Statusleiste: entfernte nodeUrl, ein Hub des Nodes nicht gewählt — neutral', () => {
  // Wie readCredentials: nur die Hubs aus selectHubs bekommen einen Eintrag.
  const sel = selectHubs(['vm', 'eigen'], ['vm'], true);
  const creds = Object.fromEntries(sel.hubs.map((h) => [h, { account: 'kamran' }]));
  const hubs = [
    { hub: 'eigen', login: 'missing', node: 'vm-node', sync: { last_success: '2026-10-08T15:00:00Z', revision: 3 } },
    { hub: 'vm', login: 'ok', node: 'vm-node', account: 'kamran', user: 'k', sync: { last_success: '2026-10-08T15:00:00Z', revision: 41 } },
  ];
  const st = hubStatus(hubs, creds, []);
  assert.strictEqual(st.warning, undefined);
  assert.deepStrictEqual(st.used, ['vm']);
  assert.strictEqual(st.hubs[0].note, 'nicht genutzt — kein Token');
});

// Task 025: Ein pausiertes Paar geht nicht hinaus, der Node meldet den Hub als missing — der
// Grund ist „pausiert“ mit nächster Probe, Fehlversuchen, Schwelle der Jail und Abhilfe, nicht
// „Token-Datei leer“. Ein pausierter Hub ohne Antwort des Nodes (alle Paare pausiert) steht
// trotzdem als genutzt in der Liste.
test('Statusleiste: pausiertes Paar — eigener Grund, auch ohne Antwort des Nodes', () => {
  const now = 1_700_000_000_000;
  const sync = { last_success: '2026-10-09T10:00:00Z', revision: 3 };
  const hubs = [
    { hub: 'vm', login: 'missing', node: 'vm-node', sync },
    { hub: 'eigen', login: 'ok', node: 'n', account: 'kp', user: 'u', sync },
  ];
  const creds = { vm: { account: 'kamran', token: 'x' }, eigen: { account: 'kp', token: 'y' } };
  const paused = { vm: { account: 'kamran', failures: 2, nextProbe: now + 3 * 60 * 1000 } };
  const st = hubStatus(hubs, creds, [], paused, now);
  assert.deepStrictEqual(st.used, ['vm', 'eigen']);
  assert.match(st.hubs[0].warning, /^pausiert — Anmeldung ungültig \(2 Fehlversuche\), nächste Probe in 3 min; die Jail/);
  assert.match(st.hubs[0].warning, /10 in 10 min \(nach einer früheren Sperre 5\)\. Abhilfe: Token-Datei erneuern; nach rotate/);
  assert.ok(!st.hubs[0].warning.includes('leer oder nicht lesbar'));
  assert.strictEqual(st.hubs[1].warning, undefined);
  assert.match(st.warning, /^vm: pausiert/);
  // Keine Antwort des Nodes: Der pausierte Hub steht trotzdem da, genutzt und gelb.
  const alone = hubStatus([], creds, [], { vm: { account: 'kamran', failures: 1, nextProbe: now - 1 } }, now);
  assert.deepStrictEqual(alone.used, ['vm']);
  assert.deepStrictEqual(alone.hubs, [{ hub: 'vm', used: true, warning: alone.hubs[0].warning }]);
  assert.match(alone.hubs[0].warning, /\(1 Fehlversuch\), nächste Probe jetzt;/);
  // Ohne Pause wie bisher.
  assert.match(hubStatus(hubs, creds, [], {}, now).hubs[0].warning, /Token-Datei tokens\/vm\/kamran\.token leer/);
});

// Task 025, Erkennen: hidden heißt alle geschickten Paare ungültig; sonst login je Hub, nur für
// geschickte Hubs.
test('whoami: was die Antwort über die geschickten Paare sagt', () => {
  assert.deepStrictEqual(whoamiOutcome({ hidden: true, hubs: [] }, ['vm', 'eigen']), { invalid: ['eigen', 'vm'], ok: [] });
  const who = { hubs: [{ hub: 'vm', login: 'invalid' }, { hub: 'eigen', login: 'ok' }, { hub: 'andere', login: 'missing' },
    { hub: 'fremd', login: 'invalid' }] };
  assert.deepStrictEqual(whoamiOutcome(who, ['vm', 'eigen', 'andere']), { invalid: ['vm'], ok: ['eigen'] });
  assert.deepStrictEqual(whoamiOutcome(who, []), { invalid: [], ok: [] });
  assert.deepStrictEqual(whoamiOutcome(undefined, ['vm']), { invalid: [], ok: [] });
  // Noch nie abgeglichen ist invalid: Der Node zählt es als Fehlversuch.
  assert.deepStrictEqual(whoamiOutcome({ hubs: [{ hub: 'vm', login: 'invalid', sync: { never_synced: true } }] }, ['vm']),
    { invalid: ['vm'], ok: [] });
});

// Task 025, Erkennen an changes: Ein Hub, der eben noch lesbar war und es gar nicht mehr ist —
// alle seine Collections dropped. Fällt nur eine weg, ist das ein entzogenes Recht.
test('changes: ein Hub, dessen Collections alle dropped sind', () => {
  const readable = { vm: ['vm:test', 'vm:wissen'], eigen: ['eigen:notizen'], leer: [] };
  assert.deepStrictEqual(droppedHubs(['vm:test', 'vm:wissen'], readable), ['vm']);
  assert.deepStrictEqual(droppedHubs(['vm:test'], readable), []);
  assert.deepStrictEqual(droppedHubs([{ address: 'eigen:notizen' }, 'vm:wissen', 'vm:test'], readable), ['eigen', 'vm']);
  assert.deepStrictEqual(droppedHubs([], readable), []);
  assert.deepStrictEqual(droppedHubs(undefined, readable), []);
});

// --- Task 035: die Suche nach der config wie config.Locate im Binary ---

const {
  locateConfig, userConfigDir, parseNodeListen, configSourceText, DEFAULT_NODE_LISTEN,
} = require('../rules');

// stat über eine Tabelle: Pfad → undefined (gibt es) oder Fehlercode.
function statOf(table) {
  return (p) => (p in table ? table[p] : 'ENOENT');
}
const HOME = '/home/u';
const USER = '/home/u/.config/kephalaion/config.yaml';
const SYS = '/etc/kephalaion/config.yaml';
const base = { env: {}, home: HOME, systemPath: SYS, cwd: '/work' };

test('config: nur User, nur global, beide, keine', () => {
  const only = locateConfig({ ...base, stat: statOf({ [USER]: undefined }) });
  assert.deepStrictEqual(only, { path: USER, source: 'user', userPath: USER, systemPath: SYS, checked: [USER, SYS],
    userExists: true, systemExists: false, bothKinds: false });
  const sys = locateConfig({ ...base, stat: statOf({ [SYS]: undefined }) });
  assert.strictEqual(sys.path, SYS);
  assert.strictEqual(sys.source, 'system');
  assert.deepStrictEqual([sys.userExists, sys.systemExists, sys.bothKinds], [false, true, false]);
  // Beide: die des Users gilt, bothKinds sagt es.
  const both = locateConfig({ ...base, stat: statOf({ [USER]: undefined, [SYS]: undefined }) });
  assert.strictEqual(both.path, USER);
  assert.strictEqual(both.source, 'user');
  assert.strictEqual(both.bothKinds, true);
  // Keine: der Ort des Users, die geprüften Orte beide.
  const none = locateConfig({ ...base, stat: statOf({}) });
  assert.strictEqual(none.path, USER);
  assert.strictEqual(none.source, 'user');
  assert.deepStrictEqual(none.checked, [USER, SYS]);
  assert.deepStrictEqual([none.userExists, none.systemExists], [false, false]);
});

test('config: KEPHALAION_CONFIG geht vor, auch relativ, und prüft beide Arten trotzdem', () => {
  const abs = locateConfig({ ...base, env: { KEPHALAION_CONFIG: '/tmp/k.yaml' }, stat: statOf({ [SYS]: undefined }) });
  assert.deepStrictEqual([abs.path, abs.source, abs.checked], ['/tmp/k.yaml', 'env', ['/tmp/k.yaml']]);
  assert.strictEqual(abs.systemExists, true);
  const rel = locateConfig({ ...base, env: { KEPHALAION_CONFIG: 'etc/k.yaml' }, stat: statOf({}) });
  assert.deepStrictEqual([rel.path, rel.source], ['/work/etc/k.yaml', 'env']);
  const both = locateConfig({ ...base, env: { KEPHALAION_CONFIG: '/tmp/k.yaml' },
    stat: statOf({ [USER]: undefined, [SYS]: undefined }) });
  assert.strictEqual(both.bothKinds, true);
});

test('config: EACCES zählt beim User nicht, bei der globalen schon', () => {
  // Die config des Users ohne Rechte (sudo -u mit fremdem HOME): wie nicht vorhanden — die
  // globale gilt.
  const user = locateConfig({ ...base, stat: statOf({ [USER]: 'EACCES', [SYS]: undefined }) });
  assert.deepStrictEqual([user.path, user.source, user.userExists, user.bothKinds], [SYS, 'system', false, false]);
  // Nur die des Users ohne Rechte: nichts gilt, der Ort des Users.
  const alone = locateConfig({ ...base, stat: statOf({ [USER]: 'EACCES' }) });
  assert.deepStrictEqual([alone.path, alone.source, alone.userExists], [USER, 'user', false]);
  // Die globale ohne Rechte zählt: Sie gilt, das Lesen meldet dann den Fehler.
  const sys = locateConfig({ ...base, stat: statOf({ [SYS]: 'EACCES' }) });
  assert.deepStrictEqual([sys.path, sys.source, sys.systemExists], [SYS, 'system', true]);
});

test('config: XDG_CONFIG_HOME nur absolut, wie UserPath und TokensDir', () => {
  assert.strictEqual(userConfigDir({ XDG_CONFIG_HOME: '/x' }, HOME), '/x/kephalaion');
  assert.strictEqual(userConfigDir({ XDG_CONFIG_HOME: 'rel' }, HOME), '/home/u/.config/kephalaion');
  assert.strictEqual(userConfigDir({}, HOME), '/home/u/.config/kephalaion');
  assert.strictEqual(userConfigDir({ XDG_CONFIG_HOME: 'rel' }, undefined), undefined);
  const rel = locateConfig({ ...base, env: { XDG_CONFIG_HOME: 'rel' }, stat: statOf({ [USER]: undefined }) });
  assert.strictEqual(rel.path, USER);
  const abs = locateConfig({ ...base, env: { XDG_CONFIG_HOME: '/x' }, stat: statOf({ '/x/kephalaion/config.yaml': undefined }) });
  assert.strictEqual(abs.path, '/x/kephalaion/config.yaml');
  // Heimatverzeichnis unbekannt: die globale gilt, wenn es sie gibt, sonst ein Fehler.
  const sys = locateConfig({ ...base, home: undefined, stat: statOf({ [SYS]: undefined }) });
  assert.strictEqual(sys.source, 'system');
  const none = locateConfig({ ...base, home: undefined, stat: statOf({}) });
  assert.match(none.error, /Heimatverzeichnis unbekannt/);
  assert.deepStrictEqual(none.checked, [SYS]);
  assert.strictEqual(configSourceText('system'), 'global');
});

test('listen aus dem Abschnitt node: Standard, Port allein, kein Abschnitt', () => {
  assert.deepStrictEqual(parseNodeListen('node:\n  db: sqlite:///x/node.db\n  listen: "127.0.0.1:7500"\n'),
    { listen: '127.0.0.1:7500' });
  assert.deepStrictEqual(parseNodeListen('hub:\n  listen: 127.0.0.1:7434\nnode:\n  db: sqlite:///x/node.db\n'),
    { listen: DEFAULT_NODE_LISTEN });
  assert.deepStrictEqual(parseNodeListen('node: # Node\n  listen: :7500\n'), { listen: '127.0.0.1:7500' });
  assert.deepStrictEqual(parseNodeListen('hub:\n  listen: 127.0.0.1:7434\n'), { reason: 'node_not_set_up' });
  assert.deepStrictEqual(parseNodeListen(''), { reason: 'node_not_set_up' });
  assert.deepStrictEqual(parseNodeListen('node:\n'), { reason: 'node_not_set_up' });
});
