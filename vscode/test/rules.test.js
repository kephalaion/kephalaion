// Tests der Regeln der Erweiterung, ohne VS Code: node --test vscode/test/*.test.js (make vscode-test).

'use strict';

const test = require('node:test');
const assert = require('node:assert');
const { parseNodeUrl, selectHubs, mcpLogins, mcpHeaders, explainResponse } = require('../rules');

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
