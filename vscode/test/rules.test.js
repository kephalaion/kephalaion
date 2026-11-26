// Tests der Regeln der Erweiterung, ohne VS Code: node --test vscode/test/*.test.js (make vscode-test).

'use strict';

const test = require('node:test');
const assert = require('node:assert');
const {
  parseNodeUrl, selectHubs, mcpLogins, mcpHeaders, explainResponse, versionHint, hubStatus, whoamiOutcome, droppedHubs,
  resolveAccount, parseDefaultAccount,
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

// Die Reihenfolge der Wahl wie in der CLI (Task 037, assistant.Resolve): Überschreibung
// (kephalaion.accounts), Standard (tokens/<hub>/default), einzige Token-Datei; sonst fehlt die Wahl.
// Dieselben Fälle wie die Go-Tests: Standard, Überschreibung vor Standard, veralteter Standard, zwei
// Dateien ohne Standard, eine Datei ohne Standard; dazu eine Überschreibung ohne Token-Datei (kein
// Rückfall). Kein Fall nimmt den ersten nach Namen.
test('Wahl des Accounts: Überschreibung, Standard, einzige Datei, Wahl fehlt', () => {
  const two = ['bob', 'alice'];
  assert.deepStrictEqual(resolveAccount(two, undefined, { account: 'bob' }),
    { account: 'bob', how: 'default', candidates: ['alice', 'bob'], notes: [] });
  assert.deepStrictEqual(resolveAccount(two, 'alice', { account: 'bob' }),
    { account: 'alice', how: 'override', candidates: ['alice', 'bob'], notes: [] });
  const stale = resolveAccount(two, undefined, { account: 'carol' });
  assert.strictEqual(stale.account, undefined);
  assert.strictEqual(stale.how, 'missing');
  assert.deepStrictEqual(stale.notes, ['der Standard carol hat keine Token-Datei; es gilt die nächste Regel']);
  assert.deepStrictEqual(resolveAccount(['kp'], undefined, { account: 'carol' }).account, 'kp');
  assert.deepStrictEqual(resolveAccount(two, undefined, undefined),
    { how: 'missing', candidates: ['alice', 'bob'], notes: [] });
  assert.deepStrictEqual(resolveAccount(['kp'], undefined, undefined),
    { account: 'kp', how: 'only', candidates: ['kp'], notes: [] });
  // Überschreibung ohne Token-Datei: kein Rückfall auf den Standard oder die einzige Datei.
  assert.deepStrictEqual(resolveAccount(['kp'], 'carol', { account: 'kp' }),
    { how: 'override', missing: 'carol', candidates: ['kp'], notes: [] });
  // Ein Standard, der sich nicht lesen lässt: Hinweis, nächste Regel.
  const bad = resolveAccount(['kp'], undefined, { error: 'EACCES' });
  assert.strictEqual(bad.how, 'only');
  assert.match(bad.notes[0], /Standard nicht lesbar \(EACCES\)/);
});

test('Standard aus der Datei default: erste Zeile, Account-Name, Fehler ohne Inhalt', () => {
  assert.deepStrictEqual(parseDefaultAccount('bob\n'), { account: 'bob' });
  assert.deepStrictEqual(parseDefaultAccount('  bob  \nrest'), { account: 'bob' });
  for (const bad of ['', 'admin\n', 'Bob\n', 'system-x\n', `keph_${'A'.repeat(43)}\n`]) {
    const r = parseDefaultAccount(bad);
    assert.ok(r.error, bad);
    assert.ok(!r.error.includes('keph_'), 'Inhalt im Fehler');
  }
});

test('MCP-Eintrag: mcpLogins mit Standard wie die CLI', () => {
  const accounts = { vm: ['alice', 'bob'], eigen: ['kp'] };
  let r = mcpLogins(accounts, {}, ['eigen', 'vm'], {});
  assert.deepStrictEqual(r.logins, [{ hub: 'eigen', account: 'kp', how: 'only' }]);
  assert.match(r.skipped[0], /^vm: Wahl fehlt — mehrere Accounts \(alice, bob\)/);
  r = mcpLogins(accounts, {}, ['eigen', 'vm'], { vm: { account: 'bob' } });
  assert.deepStrictEqual(r.logins, [{ hub: 'eigen', account: 'kp', how: 'only' }, { hub: 'vm', account: 'bob', how: 'default' }]);
  assert.deepStrictEqual(r.skipped, []);
  r = mcpLogins(accounts, { vm: 'alice' }, ['vm'], { vm: { account: 'bob' } });
  assert.deepStrictEqual(r.logins, [{ hub: 'vm', account: 'alice', how: 'override' }]);
  r = mcpLogins(accounts, { vm: 'carol' }, ['vm'], { vm: { account: 'bob' } });
  assert.deepStrictEqual(r.logins, []);
  assert.match(r.skipped[0], /^vm: gewählter Account carol hat keine Token-Datei/);
  r = mcpLogins(accounts, {}, ['vm'], { vm: { account: 'carol' } });
  assert.deepStrictEqual(r.notes, ['vm: der Standard carol hat keine Token-Datei; es gilt die nächste Regel']);
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
// genutzte Hubs warnen. Die Fälle folgen einem Wegwerf-Node,
// gemessen am 2026-10-08: zwei Einträge auf denselben Hub, Token-Datei nur für einen.
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
  // Mehrere Gründe an einem Hub, mehrere Hubs; „Wahl fehlt“ mit den Kandidaten (Task 037).
  st = hubStatus([{ ...ok, login: 'invalid', sync: failed }, { hub: 'b', login: 'missing', node: 'n', sync: synced }],
    { a: { account: 'x' }, b: { accounts: ['p', 'q'] } }, ['b']);
  assert.strictEqual(st.warning, 'a: Abgleich: Hub nicht erreichbar, Anmeldung ungültig; b: Wahl fehlt (p, q) — Account wählen');
  // Eine Überschreibung ohne Token-Datei: eigener Grund, nicht „keine Zugangsdaten angekommen“.
  st = hubStatus([{ hub: 'b', login: 'missing', node: 'n', sync: synced }], { b: { accounts: ['p'], missing: 'r' } }, []);
  assert.strictEqual(st.warning, 'b: gewählter Account r hat keine Token-Datei — Account wählen');
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

// --- Der collection tree (Task 043) ---

const {
  splitAddress, shownCollections, folderRemovalPlan, mergeCollections, dropTarget, headingLine,
  parseProjectCollection, removalWritesSetting,
} = require('../rules');

const READABLE = [
  { address: 'vm:omni', hub: 'vm', collection: 'omni', rights: ['read', 'write'] },
  { address: 'vm:wissen', hub: 'vm', collection: 'wissen', rights: ['read'] },
  { address: 'home:notizen', hub: 'home', collection: 'notizen', rights: ['read', 'write'] },
];

test('splitAddress: hub und collection, sonst undefined', () => {
  assert.deepStrictEqual(splitAddress('vm:omni'), { hub: 'vm', collection: 'omni' });
  for (const bad of ['', 'vm', 'vm:', ':omni', 'vm:omni/docs', undefined]) assert.strictEqual(splitAddress(bad), undefined, bad);
});

test('shownCollections: ungesetzt die Collections des Projekts, ohne keine', () => {
  const pick = (r) => r.map((c) => [c.address, c.readable, c.writable]);
  assert.deepStrictEqual(shownCollections(undefined, READABLE), [], 'ohne Projekt-Collection: leer');
  assert.deepStrictEqual(shownCollections(undefined, READABLE, []), []);
  assert.deepStrictEqual(pick(shownCollections(undefined, READABLE, ['vm:wissen'])), [['vm:wissen', true, false]]);
  // Zwei, in der Reihenfolge der Projektordner, doppelte einmal; eine nicht lesbare mit Kennzeichen.
  assert.deepStrictEqual(pick(shownCollections(null, READABLE, ['home:notizen', 'vm:fremd', 'home:notizen'])),
    [['home:notizen', true, true], ['vm:fremd', false, false]]);
});

test('shownCollections: eine gesetzte Einstellung geht vor die Collections des Projekts', () => {
  assert.deepStrictEqual(shownCollections(['vm:omni'], READABLE, ['vm:wissen']).map((c) => c.address), ['vm:omni']);
  assert.deepStrictEqual(shownCollections([], READABLE, ['vm:wissen']), [], 'leer gesetzt: keine');
});

test('parseProjectCollection: Block kephalaion: mit Kommentaren, Anführungszeichen, Kommentar hinter dem Wert', () => {
  const want = { hub: 'vm', collection: 'kephalaion-dev', address: 'vm:kephalaion-dev' };
  assert.deepStrictEqual(parseProjectCollection([
    'version: 1',
    'kephalaion:',
    '  # Anbindung an die Collection dieses Projekts: der Alias des',
    '  # Hubs (auf jedem Rechner gleich) und die Collection.',
    '  hub: vm',
    '  collection: kephalaion-dev',
    '',
    'maintenance:',
    '  hub: anders',
  ].join('\n')), want);
  assert.deepStrictEqual(parseProjectCollection('kephalaion:  # Projekt\r\n  hub: "vm"\r\n  collection: \'kephalaion-dev\'\r\n'), want);
  assert.deepStrictEqual(parseProjectCollection('kephalaion:\n    hub: vm   # der Alias\n    collection: kephalaion-dev # die Collection\n'), want);
  assert.deepStrictEqual(parseProjectCollection('kephalaion:\n  hub: "vm" # x\n  collection: \'kephalaion-dev\' # y\n'), want);
  // Andere Schlüssel im Block und tiefer Eingerücktes zählen nicht; der erste Wert gilt.
  assert.deepStrictEqual(parseProjectCollection([
    'kephalaion:',
    '  account: egal',
    '  extra:',
    '    collection: tiefer',
    '  hub: vm',
    '  collection: kephalaion-dev',
    '  collection: zweiter',
  ].join('\n')), want);
});

test('parseProjectCollection: Block nicht auf oberster Ebene, fehlender Wert, ungültig', () => {
  // Ein anderer Block gleichen Namens, tiefer eingerückt: kein Block auf oberster Ebene.
  const nested = parseProjectCollection('projekt:\n  kephalaion:\n    hub: vm\n    collection: omni\n');
  assert.match(nested.reason, /kein Block kephalaion: auf oberster Ebene/);
  assert.strictEqual(nested.address, undefined);
  assert.match(parseProjectCollection('').reason, /kein Block/);
  assert.match(parseProjectCollection('# kephalaion:\n#   hub: vm\n').reason, /kein Block/);
  assert.match(parseProjectCollection('kephalaion:\n  hub: vm\n').reason, /fehlt collection/);
  assert.match(parseProjectCollection('kephalaion:\n  collection: omni\n').reason, /fehlt hub/);
  assert.match(parseProjectCollection('kephalaion:\n  hub: ""\n  collection: # leer\n').reason, /fehlt hub und collection/);
  // Der Block endet mit dem nächsten Schlüssel auf oberster Ebene.
  assert.match(parseProjectCollection('kephalaion:\n  hub: vm\nanders:\n  collection: omni\n').reason, /fehlt collection/);
  // Ein anderer Block gleichen Namens tiefer eingerückt im Block selbst zählt nicht.
  assert.match(parseProjectCollection('kephalaion:\n  hub: vm\n  sub:\n    kephalaion:\n      collection: omni\n').reason,
    /fehlt collection/);
  assert.match(parseProjectCollection('kephalaion:\n  hub: VM Hub\n  collection: omni\n').reason, /kein gültiger Alias/);
  assert.match(parseProjectCollection('kephalaion:\n  hub: vm\n  collection: omni/docs\n').reason, /kein Name einer Collection/);
});

test('removalWritesSetting: gesetzt immer, ungesetzt nur, wenn eine herausgenommene nicht gezeigt wird', () => {
  assert.strictEqual(removalWritesSetting(['vm:omni'], ['vm:omni'], ['vm:omni']), true);
  assert.strictEqual(removalWritesSetting([], [], []), true);
  assert.strictEqual(removalWritesSetting(undefined, ['vm:omni'], ['vm:kephalaion-dev']), true);
  assert.strictEqual(removalWritesSetting(undefined, ['vm:kephalaion-dev'], ['vm:kephalaion-dev']), false);
  assert.strictEqual(removalWritesSetting(undefined, [], []), false);
  assert.strictEqual(removalWritesSetting(null, ['vm:omni'], undefined), true);
  // Zusammen mit mergeCollections: die gezeigte eigene und die herausgenommene.
  assert.deepStrictEqual(mergeCollections(undefined, ['vm:omni'], [], ['vm:kephalaion-dev']), ['vm:kephalaion-dev', 'vm:omni']);
});

test('shownCollections: gesetzt genau diese, in dieser Reihenfolge, nicht lesbare mit Kennzeichen', () => {
  const r = shownCollections(['home:notizen', 'vm:fremd', 'vm:omni', 'home:notizen', ' ', 'kaputt'], READABLE);
  assert.deepStrictEqual(r, [
    { address: 'home:notizen', hub: 'home', collection: 'notizen', readable: true, writable: true },
    { address: 'vm:fremd', hub: 'vm', collection: 'fremd', readable: false, writable: false },
    { address: 'vm:omni', hub: 'vm', collection: 'omni', readable: true, writable: true },
    { address: 'kaputt', hub: '', collection: '', readable: false, writable: false },
  ]);
  assert.deepStrictEqual(shownCollections([], READABLE), [], 'leer gesetzt: keine');
});

const wf = (index, scheme, p, authority = scheme === 'keph' ? 'vm' : '') => ({
  index, scheme, authority, path: p, name: `F${index}`, uri: `${scheme}://${authority}${p}`,
});

test('folderRemovalPlan: Bereiche der keph://-Ordner von hinten nach vorn', () => {
  const plan = folderRemovalPlan([
    wf(0, 'file', '/projekt'), wf(1, 'keph', '/omni'), wf(2, 'keph', '/wissen/docs'), wf(3, 'file', '/zwei'),
    wf(4, 'keph', '/omni/sub'), wf(5, 'vscode-remote', '/drei'),
  ]);
  assert.deepStrictEqual(plan.ranges, [{ start: 4, count: 1 }, { start: 1, count: 2 }]);
  assert.deepStrictEqual(plan.collections, ['vm:omni', 'vm:wissen']);
  assert.deepStrictEqual(plan.subpaths.map((s) => s.uri), ['keph://vm/wissen/docs', 'keph://vm/omni/sub']);
  assert.deepStrictEqual(plan.removed.map((r) => r.uri), ['keph://vm/omni', 'keph://vm/wissen/docs', 'keph://vm/omni/sub']);
  assert.strictEqual(plan.touchesFirst, false);
  assert.strictEqual(plan.onlyKeph, false);
});

test('folderRemovalPlan: Index 0, ohne keph://, nur keph://', () => {
  const first = folderRemovalPlan([wf(1, 'file', '/p'), wf(0, 'keph', '/omni')]);
  assert.deepStrictEqual(first.ranges, [{ start: 0, count: 1 }]);
  assert.strictEqual(first.touchesFirst, true);
  const none = folderRemovalPlan([wf(0, 'file', '/p')]);
  assert.deepStrictEqual([none.ranges, none.onlyKeph, none.removed], [[], false, []]);
  const only = folderRemovalPlan([wf(0, 'keph', '/omni'), wf(1, 'keph', '/wissen')]);
  assert.deepStrictEqual([only.ranges, only.onlyKeph, only.removed, only.collections], [[], true, [], []]);
  assert.deepStrictEqual(folderRemovalPlan([]).ranges, []);
});

test('mergeCollections: ungesetzt von den gezeigten aus, gesetzt vom Wert', () => {
  assert.deepStrictEqual(mergeCollections(undefined, ['vm:neu'], [], ['vm:omni', 'vm:wissen']), ['vm:omni', 'vm:wissen', 'vm:neu']);
  assert.deepStrictEqual(mergeCollections(undefined, [], ['vm:omni'], ['vm:omni', 'vm:wissen']), ['vm:wissen']);
  assert.deepStrictEqual(mergeCollections(['vm:wissen'], ['vm:omni', 'vm:wissen'], [], ['egal:x']), ['vm:wissen', 'vm:omni']);
  assert.deepStrictEqual(mergeCollections([], ['vm:omni'], [], ['vm:wissen']), ['vm:omni']);
  assert.deepStrictEqual(mergeCollections(['vm:omni'], [], ['vm:omni'], []), []);
});

const ku = (p, authority = 'vm') => ({ scheme: 'keph', authority, path: p });

test('dropTarget: Ziel ist das Verzeichnis, bei einem Dokument sein Verzeichnis', () => {
  const r = dropTarget({ authority: 'vm', path: '/omni/docs', kind: 'directory' }, [ku('/omni/a.md'), ku('/omni/docs/b.md')]);
  assert.strictEqual(r.error, undefined);
  assert.deepStrictEqual([r.hub, r.collection, r.dir], ['vm', 'omni', 'docs']);
  assert.deepStrictEqual(r.items.map((i) => [i.name, i.op]), [['docs/a.md', 'move']], 'schon am Ziel: still heraus');
  const d = dropTarget({ authority: 'vm', path: '/omni/docs/x.md', kind: 'document' }, [ku('/omni/a.md')]);
  assert.deepStrictEqual(d.items.map((i) => i.name), ['docs/a.md']);
  const c = dropTarget({ authority: 'vm', path: '/omni', kind: 'collection' }, [ku('/omni/docs/a.md')]);
  assert.deepStrictEqual([c.dir, c.items.map((i) => i.name)], ['', ['a.md']]);
});

test('dropTarget: Kopie aus dem Projekt, Ablehnungen', () => {
  const r = dropTarget({ authority: 'vm', path: '/omni', kind: 'collection' },
    [{ scheme: 'file', path: '/home/k/projekt/notiz.md' }, { scheme: 'vscode-remote', authority: 'wsl+u', path: '/p/ordner' }]);
  assert.deepStrictEqual(r.items.map((i) => [i.name, i.op]), [['notiz.md', 'copy'], ['ordner', 'copy']]);
  assert.match(dropTarget({ authority: 'vm', path: '/omni', kind: 'collection' }, [ku('/wissen/a.md')]).error,
    /nur innerhalb einer Collection.*kein Kopieren zwischen Collections/);
  assert.match(dropTarget({ authority: 'vm', path: '/omni', kind: 'collection' }, [ku('/omni/a.md', 'home')]).error,
    /nur innerhalb einer Collection/);
  assert.match(dropTarget({ authority: 'vm', path: '/omni/docs/sub', kind: 'directory' }, [ku('/omni/docs')]).error,
    /in sich selbst/);
  assert.match(dropTarget({ authority: 'vm', path: '/omni', kind: 'collection' }, [ku('/omni')]).error, /Collection/);
  assert.match(dropTarget({ authority: 'vm', path: '/omni', kind: 'collection' }, [{ scheme: 'untitled', path: '/U' }]).error,
    /untitled/);
  assert.ok(dropTarget(undefined, [ku('/omni/a.md')]).error);
});

test('headingLine: ATX-Überschrift, nicht im Code-Block, sonst -1', () => {
  const text = '---\ntitle: x\n---\n# Titel\n\n```\n## Setup\n```\n## Setup ##\ntext\n### Weiter';
  assert.strictEqual(headingLine(text, 'Setup'), 8);
  assert.strictEqual(headingLine(text, 'Titel'), 3);
  assert.strictEqual(headingLine(text, 'Weiter'), 10);
  assert.strictEqual(headingLine(text, 'Fehlt'), -1);
  assert.strictEqual(headingLine(text, ''), -1);
  assert.strictEqual(headingLine('a\r\n## B\r\n', 'B'), 1);
});

test('treeNameError: relative Namen mit /, sonst eine Meldung', () => {
  const { treeNameError } = require('../rules');
  for (const ok of ['a.md', 'docs/a.md', 'ordner', 'a b/c.md']) assert.strictEqual(treeNameError(ok), undefined, ok);
  for (const bad of ['', '  ', ' a.md', '/a.md', 'a/', 'a//b', 'a/../b', './a', 'a\\b']) assert.ok(treeNameError(bad), bad);
});
