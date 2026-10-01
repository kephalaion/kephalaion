// Regeln der Erweiterung ohne vscode, damit node --test sie prüfen kann (vscode/test/): die
// Adresse des Nodes, welche Hubs ihre Header-Paare bekommen, und was eine Antwort heißt, die
// nicht vom Node kommt. Dieselben Regeln wie auf der Kommandozeile (docs/begriffe.md, „node
// address“, „--hub“; docs/vscode.md, „Ein Node auf einem anderen Rechner“).

'use strict';

const LOOPBACK = new Set(['localhost', '127.0.0.1', '[::1]']);
const DOCKER_HOST = 'host.docker.internal';
const HUB_ALIAS = /^[a-z0-9][a-z0-9._-]{0,62}$/;

// Ein Alias nach der Namensregel, ohne den Präfix system.
function validAlias(hub) {
  return typeof hub === 'string' && HUB_ALIAS.test(hub) && !hub.toLowerCase().startsWith('system');
}

// Die Adresse aus kephalaion.nodeUrl: die Basis ohne /mcp — lokal http://127.0.0.1:7433, über
// einen Proxy https://<name>/<präfix>. http nur zu diesem Rechner und zu host.docker.internal,
// sonst ginge das Token im Klartext übers Netz. Liefert { endpoint, remote }; entfernt ist alles
// außer http zu Loopback. Wirft bei einer Adresse, die nicht geht.
function parseNodeUrl(raw) {
  const what = `kephalaion.nodeUrl „${raw}“`;
  let u;
  try {
    u = new URL(raw);
  } catch {
    throw new Error(`${what}: erwartet http://<host>:<port> (dieser Rechner) oder https://<name>/<präfix> (über einen Proxy)`);
  }
  if (u.protocol !== 'http:' && u.protocol !== 'https:') {
    throw new Error(`${what}: erwartet http://<host>:<port> (dieser Rechner) oder https://<name>/<präfix> (über einen Proxy)`);
  }
  if (u.username || u.password || u.search || u.hash || raw.includes('?')) {
    throw new Error(`${what}: ohne Benutzer, Query und Fragment`);
  }
  const host = u.hostname.toLowerCase();
  const loopback = LOOPBACK.has(host);
  if (u.protocol === 'http:' && !loopback && host !== DOCKER_HOST) {
    throw new Error(`${what}: http nur zu diesem Rechner (localhost, 127.0.0.1, [::1]) und zu ${DOCKER_HOST} — `
      + 'sonst ginge das Token im Klartext übers Netz; zu einem anderen Rechner https://<name>/<präfix> über den Proxy');
  }
  const p = u.pathname.replace(/\/+$/, '');
  if (p.endsWith('/mcp')) throw new Error(`${what}: die Adresse ohne /mcp am Ende — /mcp hängt die Erweiterung an`);
  const base = `${u.protocol}//${u.host}${p}`;
  return { endpoint: `${base}/mcp`, remote: u.protocol === 'https:' || !loopback };
}

// Welche Hubs ihre Header-Paare an den Node schicken. Lokal alle Hubs unter tokens/ (wie
// kephalaion node mcp headers). An eine entfernte Adresse nur die gewählten (kephalaion.hubs) —
// fest in der Einstellung, ein Hub, der später unter tokens/ hinzukommt, geht nicht mit; ohne
// Wahl nur, wenn unter tokens/ genau einer liegt. tokenHubs sind die Aliase unter tokens/,
// chosen der Wert von kephalaion.hubs. Liefert { hubs } oder { hubs: [], error }.
function selectHubs(tokenHubs, chosen, remote) {
  const present = tokenHubs.filter(validAlias).sort();
  if (!remote) return { hubs: present };
  const wanted = Array.isArray(chosen) ? [...new Set(chosen)].sort() : [];
  if (wanted.length) {
    const bad = wanted.filter((h) => !validAlias(h));
    if (bad.length) return { hubs: [], error: `kephalaion.hubs: kein gültiger Alias (${bad.join(', ')})` };
    return { hubs: wanted };
  }
  if (present.length === 1) return { hubs: present };
  if (present.length === 0) return { hubs: [], error: 'keine Token-Datei unter tokens/<hub>/ — ohne Anmeldung geht nichts an den Node' };
  return {
    hubs: [],
    error: `mehrere Hubs unter tokens/ (${present.join(', ')}): an eine entfernte nodeUrl gehen nur die Header-Paare `
      + 'der gewählten — kephalaion.hubs wählt sie (etwa ["vm"])',
  };
}

const TOKEN_FORMAT = /^keph_[A-Za-z0-9_-]{43}$/;

// Welcher Account je Hub in den MCP-Eintrag kommt — wie kephalaion node mcp headers: der gewählte
// (kephalaion.accounts), sonst der einzige; nur für die Hubs aus selectHubs. Ein Hub mit
// mehreren Accounts ohne Wahl, mit einem gewählten ohne Token-Datei oder ohne Token-Datei fehlt
// und steht in skipped. tokenAccounts ist {hub: [accounts]}. Nur Namen, kein Token.
function mcpLogins(tokenAccounts, chosenAccounts, hubs) {
  const chosen = chosenAccounts || {};
  const logins = [];
  const skipped = [];
  for (const hub of hubs) {
    const accounts = tokenAccounts[hub] || [];
    if (!validAlias(hub)) {
      skipped.push(`${hub}: kein gültiger Alias`);
    } else if (chosen[hub]) {
      if (accounts.includes(chosen[hub])) logins.push({ hub, account: chosen[hub] });
      else skipped.push(`${hub}: gewählter Account ${chosen[hub]} hat keine Token-Datei`);
    } else if (accounts.length === 1) {
      logins.push({ hub, account: accounts[0] });
    } else if (accounts.length === 0) {
      skipped.push(`${hub}: gewählt, aber keine Token-Datei unter tokens/${hub}/`);
    } else {
      skipped.push(`${hub}: mehrere Accounts (${accounts.join(', ')}), keiner gewählt — „Kephalaion: Account wählen“`);
    }
  }
  return { logins, skipped };
}

// Die Header-Paare der Anmeldungen; readToken(hub, account) liest die erste Zeile der
// Token-Datei. Ein Hub, dessen Datei sich nicht lesen lässt oder kein Token hält, fehlt; log
// nennt nie ein Token.
function mcpHeaders(logins, readToken, log) {
  const headers = {};
  for (const { hub, account } of logins) {
    let token;
    try {
      token = readToken(hub, account);
    } catch (e) {
      log(`MCP-Server: Hub ${hub}: ${account}.token nicht lesbar (${e.code || 'Fehler'})`);
      continue;
    }
    if (!TOKEN_FORMAT.test(token)) {
      log(`MCP-Server: Hub ${hub}: ${account}.token hält kein gültiges Token`);
      continue;
    }
    headers[`X-Keph-Account-${hub}`] = account;
    headers[`X-Keph-Token-${hub}`] = token;
  }
  return headers;
}

const PROXY_HINT = 'Präfix falsch oder Anmeldung des Proxys — der Node antwortet nie mit 401, einer Weiterleitung oder '
  + 'HTML; nodeUrl ist die Basis ohne /mcp (etwa https://<name>/kephalaion)';

// Was eine Antwort heißt, die nicht die des Nodes ist (fetch mit redirect: 'manual'); undefined
// bei einer Antwort, die passt. status 0 ist eine Weiterleitung, die fetch verdeckt.
function explainResponse(status, contentType) {
  const type = (contentType || '').split(';')[0].trim().toLowerCase();
  if (status === 0 || (status >= 300 && status < 400)) return `eine Weiterleitung (HTTP ${status}): ${PROXY_HINT}`;
  if (status === 401) return `eine Anmeldung des Proxys, nicht der Node (HTTP 401): ${PROXY_HINT}`;
  if (status === 403) {
    return 'Host-Prüfung des Nodes schlägt fehl (HTTP 403): setzt der Proxy Host auf die Loopback-Adresse des Nodes '
      + `(header_up Host {upstream_hostport})? ${DOCKER_HOST} nimmt der Node nicht an`;
  }
  if (status === 404) return 'dort antwortet kein Node (HTTP 404): Präfix falsch? nodeUrl ist die Basis ohne /mcp';
  if (status === 502 || status === 503 || status === 504) {
    return `Proxy antwortet, aber der Node dahinter nicht (HTTP ${status}): läuft kephalaion serve?`;
  }
  if (status < 200 || status >= 300) return undefined;
  if (type !== 'application/json' && type !== 'text/event-stream') {
    return `keine Antwort eines MCP-Servers (HTTP ${status}, ${type || 'ohne Content-Type'}): ${PROXY_HINT}`;
  }
  return undefined;
}

module.exports = { parseNodeUrl, selectHubs, mcpLogins, mcpHeaders, explainResponse, validAlias, PROXY_HINT };
