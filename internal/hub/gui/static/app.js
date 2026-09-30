// Weboberfläche des Hubs: fragt Kephalaion-Account und Account-Token ab,
// prüft beides am Hub (POST gui/api/whoami) und zeigt den Zugriff des
// Accounts.
//
// Regeln dieser Datei:
//   - Nur relative Pfade. Die Seite liegt an der Wurzel des Hub-Listeners,
//     hinter einem Proxy unter dessen Präfix; das Binary kennt ihn nicht.
//   - Das Token lebt nur in Variablen der einen Prüfung: Der Browser legt es
//     nirgends ab (kein Web Storage, kein Cookie), und es steht nie in einer
//     Adresse. Nach der Antwort ist das Feld leer.
//   - Was nicht wie ein Kephalaion-Token aussieht, wird nicht abgeschickt —
//     ein versehentlich eingefügtes Passwort der Anmeldung an der Seite geht
//     so nie an den Hub.
//   - Alles, was vom Hub kommt, wird per textContent gesetzt, nie als HTML.
(function () {
  "use strict";

  // Form eines Tokens wie ident.CheckToken: keph_ und 32 Bytes base64url
  // ohne Padding, also 43 Zeichen.
  var TOKEN_FORM = /^keph_[A-Za-z0-9_-]{43}$/;
  var TOKEN_PREFIX = "keph_";
  // Namensregel wie ident.CheckName. Maßgeblich prüft der Hub; hier hält sie
  // nur Tippfehler und Fremdes im Feld des Accounts zurück.
  var NAME_FORM = /^[a-z0-9][a-z0-9._-]{0,62}$/;
  // Der Eingang der Seite, relativ zur Seite.
  var WHOAMI = "gui/api/whoami";
  var TIMEOUT_MS = 15000;

  var TEXT = {
    accountMissing: "Der Kephalaion-Account fehlt: der Name des Accounts, zu dem das Token gehört.",
    accountForm: "Das ist kein Name eines Kephalaion-Accounts: klein geschrieben, a–z, 0–9, " +
      "Punkt, Unterstrich und Bindestrich, höchstens 63 Zeichen. Es wurde nichts abgeschickt.",
    tokenMissing: "Das Account-Token fehlt. Es beginnt mit keph_ und liegt meist unter " +
      "~/.config/kephalaion/tokens/<hub>/<account>.token.",
    tokenForeign: "Das ist kein Kephalaion-Token (die beginnen mit keph_). Gemeint ist nicht das " +
      "Passwort dieser Seite, sondern das Token deines Kephalaion-Accounts, meist unter " +
      "~/.config/kephalaion/tokens/<hub>/<account>.token. Es wurde nichts abgeschickt.",
    tokenForm: "Das Token ist nicht vollständig: Nach keph_ folgen genau 43 Zeichen " +
      "(A–Z, a–z, 0–9, - und _). Es wurde nichts abgeschickt.",
    checking: "Prüfe am Hub …",
    unauthenticated: "Account oder Token stimmt nicht. Wiederholte Fehlversuche können deinen " +
      "Rechner für eine Weile sperren.",
    invalid: "Der Hub hat die Anfrage abgelehnt: ",
    internal: "Fehler am Hub. Die Einzelheit steht in seinem Log.",
    expired: "Deine Anmeldung an dieser Seite ist abgelaufen — Seite neu laden und neu anmelden.",
    unreachable: "Hub nicht erreichbar.",
    unexpected: "Hub nicht erreichbar: unerwartete Antwort"
  };

  var form = document.getElementById("ask-form");
  var accountInput = document.getElementById("keph-account");
  var tokenInput = document.getElementById("keph-account-token");
  var toggle = document.getElementById("token-toggle");
  var submit = document.getElementById("ask-submit");
  var reload = document.getElementById("reload");
  var message = document.getElementById("message");
  var ask = document.getElementById("ask");
  var result = document.getElementById("result");
  var again = document.getElementById("again");

  var busy = false;
  // expired: Die Anmeldung an der Seite ist abgelaufen. Jede weitere Prüfung
  // wäre nur eine weitere 401 vor dem Hub; es hilft allein das Neuladen.
  var expired = false;

  function showMessage(text, kind) {
    message.textContent = text;
    message.className = "message " + kind;
    message.hidden = false;
  }

  function clearMessage() {
    message.textContent = "";
    message.hidden = true;
  }

  function hideToken() {
    tokenInput.type = "password";
    toggle.textContent = "anzeigen";
    toggle.setAttribute("aria-pressed", "false");
  }

  function clearToken() {
    tokenInput.value = "";
    hideToken();
  }

  function setBusy(on) {
    busy = on;
    submit.disabled = on || expired;
  }

  // checkInput prüft die Eingabe, bevor irgendetwas den Browser verlässt.
  // Liefert den Text der Ablehnung und das Feld dazu, oder null.
  function checkInput(account, token) {
    if (account === "") {
      return { text: TEXT.accountMissing, field: accountInput };
    }
    if (!NAME_FORM.test(account)) {
      return { text: TEXT.accountForm, field: accountInput };
    }
    if (token === "") {
      return { text: TEXT.tokenMissing, field: tokenInput };
    }
    if (token.indexOf(TOKEN_PREFIX) !== 0) {
      return { text: TEXT.tokenForeign, field: tokenInput };
    }
    if (!TOKEN_FORM.test(token)) {
      return { text: TEXT.tokenForm, field: tokenInput };
    }
    return null;
  }

  // readJSON liest den Body als JSON-Objekt, oder null — eine HTML-Seite
  // oder ein Text ist keine Antwort des Hubs.
  function readJSON(resp) {
    var type = resp.headers.get("Content-Type") || "";
    if (type.toLowerCase().indexOf("application/json") !== 0) {
      return Promise.resolve(null);
    }
    return resp.json().then(function (data) {
      return data !== null && typeof data === "object" && !Array.isArray(data) ? data : null;
    }, function () {
      return null;
    });
  }

  // outcome ordnet eine Antwort ein. Der Hub ist am JSON zu erkennen: eine
  // Fehlerantwort trägt code, ein Erfolg account und collections. Ohne diese
  // Form kommt die Antwort von davor:
  //   - eine Umleitung, 401 oder 403, oder eine Seite statt JSON — die
  //     Anmeldung an der Seite ist abgelaufen;
  //   - alles andere (502, 503 eines Proxys) — der Hub ist nicht erreichbar.
  function outcome(resp, data) {
    if (resp.type === "opaqueredirect") {
      return { kind: "expired" };
    }
    if (data && typeof data.code === "string") {
      if (resp.status === 401 && data.code === "unauthenticated") {
        return { kind: "unauthenticated" };
      }
      if (resp.status >= 400 && resp.status < 500) {
        return { kind: "invalid", detail: typeof data.message === "string" ? data.message : "" };
      }
      return { kind: "internal" };
    }
    if (resp.status === 200 && data && typeof data.account === "string" && Array.isArray(data.collections)) {
      return { kind: "ok", data: data };
    }
    if (resp.status === 401 || resp.status === 403 || (resp.status >= 200 && resp.status < 400)) {
      return { kind: "expired" };
    }
    if (resp.status === 502 || resp.status === 503 || resp.status === 504) {
      return { kind: "unreachable" };
    }
    return { kind: "unexpected", status: resp.status };
  }

  function text(tag, value, className) {
    var el = document.createElement(tag);
    el.textContent = value;
    if (className) {
      el.className = className;
    }
    return el;
  }

  // cell baut eine Zelle; label ist die Spalte, die bei schmaler Breite vor
  // dem Wert steht (style.css).
  function cell(label) {
    var td = document.createElement("td");
    td.setAttribute("data-label", label);
    return td;
  }

  function yesNo(label, yes) {
    var td = cell(label);
    td.appendChild(text("span", yes ? "ja" : "nein", yes ? "yes" : "no"));
    return td;
  }

  function collectionRow(c) {
    var rights = c.rights && typeof c.rights === "object" ? c.rights : {};
    var vendor = Array.isArray(rights.vendor) ? rights.vendor : [];
    var tr = document.createElement("tr");

    var th = document.createElement("th");
    th.scope = "row";
    th.appendChild(text("span", String(c.name), "name"));
    if (typeof c.description === "string" && c.description !== "") {
      th.appendChild(text("span", c.description, "sub"));
    }
    tr.appendChild(th);

    tr.appendChild(yesNo("Lesen", true));
    tr.appendChild(yesNo("Schreiben (write)", rights.write === true));
    tr.appendChild(yesNo("Fremdes (supersede)", rights.supersede === true));

    var scopes = cell("Scopes");
    if (vendor.length === 0) {
      scopes.appendChild(text("span", "—", "no"));
    } else {
      var list = document.createElement("ul");
      list.className = "scopes";
      vendor.forEach(function (name) {
        var li = document.createElement("li");
        li.appendChild(text("code", "vendor/" + String(name)));
        list.appendChild(li);
      });
      scopes.appendChild(list);
    }
    tr.appendChild(scopes);
    return tr;
  }

  function showResult(data) {
    document.getElementById("result-account").textContent = data.account;
    document.getElementById("result-user").textContent = typeof data.user === "string" ? data.user : "";
    var description = document.getElementById("result-description");
    description.textContent = typeof data.description === "string" ? data.description : "";
    description.hidden = description.textContent === "";

    var rows = document.getElementById("result-rows");
    rows.textContent = "";
    data.collections.forEach(function (c) {
      if (c && typeof c === "object") {
        rows.appendChild(collectionRow(c));
      }
    });
    var none = rows.childElementCount === 0;
    document.getElementById("result-empty").hidden = !none;
    document.getElementById("result-table").hidden = none;

    ask.hidden = true;
    result.hidden = false;
    document.getElementById("result-title").focus();
  }

  // reset verwirft alles: die Übersicht, die Meldung und beide Felder.
  function reset() {
    document.getElementById("result-account").textContent = "";
    document.getElementById("result-user").textContent = "";
    document.getElementById("result-description").textContent = "";
    document.getElementById("result-rows").textContent = "";
    result.hidden = true;
    ask.hidden = false;
    clearMessage();
    accountInput.value = "";
    clearToken();
    accountInput.focus();
  }

  function check(account, token) {
    var abort = new AbortController();
    var timer = window.setTimeout(function () { abort.abort(); }, TIMEOUT_MS);
    // redirect "manual": Der Hub leitet nie um. Eine Umleitung kommt von der
    // Anmeldung davor; ihr zu folgen hieße bei 307 oder 308, das Token an ihr
    // Ziel zu schicken.
    return fetch(WHOAMI, {
      method: "POST",
      headers: { "Content-Type": "application/json", "Accept": "application/json" },
      body: JSON.stringify({ account: account, token: token }),
      credentials: "same-origin",
      cache: "no-store",
      redirect: "manual",
      referrerPolicy: "no-referrer",
      signal: abort.signal
    }).then(function (resp) {
      return readJSON(resp).then(function (data) { return outcome(resp, data); });
    }).then(function (out) {
      window.clearTimeout(timer);
      return out;
    }, function () {
      window.clearTimeout(timer);
      return { kind: "unreachable" };
    });
  }

  form.addEventListener("submit", function (ev) {
    ev.preventDefault();
    if (busy || expired) {
      return;
    }
    var account = accountInput.value.trim();
    var token = tokenInput.value.trim();
    var refused = checkInput(account, token);
    if (refused) {
      showMessage(refused.text, "error");
      refused.field.focus();
      return;
    }
    setBusy(true);
    showMessage(TEXT.checking, "info");
    check(account, token).then(function (out) {
      // Das Token bleibt nicht liegen, gleich wie die Prüfung ausging.
      token = "";
      clearToken();
      switch (out.kind) {
        case "ok":
          clearMessage();
          accountInput.value = "";
          showResult(out.data);
          break;
        case "unauthenticated":
          showMessage(TEXT.unauthenticated, "error");
          tokenInput.focus();
          break;
        case "invalid":
          showMessage(TEXT.invalid + out.detail, "error");
          break;
        case "internal":
          showMessage(TEXT.internal, "error");
          break;
        case "expired":
          expired = true;
          accountInput.value = "";
          showMessage(TEXT.expired, "error");
          reload.hidden = false;
          reload.focus();
          break;
        case "unexpected":
          showMessage(TEXT.unexpected + " (HTTP " + out.status + ").", "error");
          break;
        default:
          showMessage(TEXT.unreachable, "error");
      }
      setBusy(false);
    });
  });

  toggle.addEventListener("click", function () {
    var show = tokenInput.type === "password";
    tokenInput.type = show ? "text" : "password";
    toggle.textContent = show ? "verbergen" : "anzeigen";
    toggle.setAttribute("aria-pressed", show ? "true" : "false");
  });

  again.addEventListener("click", reset);
  reload.addEventListener("click", function () { window.location.reload(); });

  // Ein Browser stellt Felder nach dem Neuladen gern wieder her: Die Seite
  // beginnt immer leer.
  accountInput.value = "";
  clearToken();
  submit.disabled = false;
}());
