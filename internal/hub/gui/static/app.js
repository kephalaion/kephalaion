// Weboberfläche des Hubs: zeigt die Accounts des Users, mit dem der Browser
// am Proxy angemeldet ist, mit ihren Collections und Rechten
// (GET gui/api/user).
//
// Regeln dieser Datei:
//   - Nur relative Pfade. Die Seite liegt an der Wurzel des Hub-Listeners,
//     hinter einem Proxy unter dessen Präfix; das Binary kennt ihn nicht.
//   - Die Seite fragt kein Token ab und zeigt keins: Wen sie zeigt, sagt der
//     Hub (viewer, user) aus der Anmeldung des Proxys.
//   - Alles, was vom Hub kommt, wird per textContent gesetzt, nie als HTML.
//   - Einer Umleitung folgt sie nicht: Sie kommt von der Anmeldung davor und
//     heißt, dass die Anmeldung abgelaufen ist.
//   - Sie setzt nicht voraus, dass der gezeigte User (user) der angemeldete
//     (viewer) ist.
(function () {
  "use strict";

  // Der Eingang der Seite, relativ zur Seite.
  var USER_API = "gui/api/user";
  var TIMEOUT_MS = 15000;

  var TEXT = {
    loading: "Lade die Accounts …",
    unauthenticated: "Keine Anmeldung des Proxys: Der Hub hat zu dieser Anfrage keinen " +
      "angemeldeten User bekommen. Die Seite zeigt Accounts nur über den Proxy mit seiner " +
      "Anmeldung. Ohne Proxy, direkt am Hub, gibt es diese Anmeldung nicht — dort zeigt " +
      "kephalaion hub account list --user <user> die Accounts.",
    invalidUser: "Der Name deiner Anmeldung am Proxy ist am Hub kein gültiger User " +
      "(klein geschrieben, a–z, 0–9, Punkt, Unterstrich und Bindestrich, nicht admin). " +
      "Accounts gehören zu einem User gleichen Namens.",
    forbidden: "Die Accounts dieses Users zeigt dir der Hub nicht — nur deine eigenen.",
    invalid: "Der Hub hat die Anfrage abgelehnt: ",
    internal: "Fehler am Hub. Die Einzelheit steht in seinem Log.",
    expired: "Deine Anmeldung an dieser Seite ist abgelaufen — Seite neu laden und neu anmelden.",
    unreachable: "Hub nicht erreichbar.",
    unexpected: "Hub nicht erreichbar: unerwartete Antwort",
    ownTitle: "Deine Accounts",
    otherTitle: "Accounts von ",
    ownEmpty: "Du hast an diesem Hub noch keinen Account.",
    otherEmpty: " hat an diesem Hub noch keinen Account.",
    noCollection: "Dieser Account hat noch keine Collection.",
    active: "aktiv",
    locked: "gesperrt — die Rechte ruhen",
    lockedCaption: "Gemerkte Rechte: Sie ruhen, solange der Account gesperrt ist.",
    tokenHint: "Sein Token liegt meist unter ",
    tokenHintEnd: " — die Seite zeigt es nicht."
  };

  var message = document.getElementById("message");
  var reload = document.getElementById("reload");
  var status = document.getElementById("status");
  var result = document.getElementById("result");

  function showMessage(text, kind) {
    message.textContent = text;
    message.className = "message " + kind;
    status.hidden = false;
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
  // Fehlerantwort trägt code, ein Erfolg viewer, user und accounts. Ohne
  // diese Form kommt die Antwort von davor:
  //   - eine Umleitung, 401 oder 403, oder eine Seite statt JSON — die
  //     Anmeldung an der Seite ist abgelaufen;
  //   - alles andere (502, 503 eines Proxys) — der Hub ist nicht erreichbar.
  function outcome(resp, data) {
    if (resp.type === "opaqueredirect") {
      return { kind: "expired" };
    }
    if (data && typeof data.code === "string") {
      if (resp.status === 403 && data.code === "unauthenticated") {
        return { kind: "unauthenticated" };
      }
      if (resp.status === 403 && data.code === "invalid_user") {
        return { kind: "invalidUser" };
      }
      if (resp.status === 403 && data.code === "forbidden") {
        return { kind: "forbidden" };
      }
      if (resp.status >= 400 && resp.status < 500) {
        return { kind: "invalid", detail: typeof data.message === "string" ? data.message : "" };
      }
      return { kind: "internal" };
    }
    if (resp.status === 200 && data && typeof data.viewer === "string" && typeof data.user === "string" &&
        Array.isArray(data.accounts)) {
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

  // yesNo zeigt ein Recht; resting: der Account ist gesperrt, ein Recht ruht.
  function yesNo(label, yes, resting) {
    var td = cell(label);
    if (yes && resting) {
      td.appendChild(text("span", "ja (ruht)", "resting"));
    } else {
      td.appendChild(text("span", yes ? "ja" : "nein", yes ? "yes" : "no"));
    }
    return td;
  }

  function collectionRow(c, resting) {
    var rights = c.rights && typeof c.rights === "object" ? c.rights : {};
    var vendor = Array.isArray(rights.vendor) ? rights.vendor : [];
    var dirs = Array.isArray(rights.dirs) ? rights.dirs : [];
    var tr = document.createElement("tr");

    var th = document.createElement("th");
    th.scope = "row";
    th.appendChild(text("span", String(c.name), "name"));
    if (typeof c.description === "string" && c.description !== "") {
      th.appendChild(text("span", c.description, "sub"));
    }
    tr.appendChild(th);

    tr.appendChild(yesNo("Lesen", true, resting));
    tr.appendChild(yesNo("Schreiben (write)", rights.write === true, resting));
    tr.appendChild(yesNo("Fremdes (supersede)", rights.supersede === true, resting));

    // Die Scopes wie in der Kommandozeile: vendor/<name> und die
    // Verzeichnis-Scopes als „dir <pfad>/“.
    var labels = vendor.map(function (name) {
      return "vendor/" + String(name);
    }).concat(dirs.map(function (dir) {
      return "dir " + String(dir) + "/";
    }));
    var scopes = cell("Scopes");
    if (labels.length === 0) {
      scopes.appendChild(text("span", "—", "no"));
    } else {
      var list = document.createElement("ul");
      list.className = resting ? "scopes resting" : "scopes";
      labels.forEach(function (label) {
        var li = document.createElement("li");
        li.appendChild(text("code", label));
        list.appendChild(li);
      });
      scopes.appendChild(list);
    }
    tr.appendChild(scopes);
    return tr;
  }

  // head baut den Kopf der Tabelle; Schreiben und Fremdes tragen ihre
  // Erklärung darunter.
  function head() {
    var thead = document.createElement("thead");
    var tr = document.createElement("tr");
    [
      ["Collection"],
      ["Lesen"],
      ["Schreiben", "Neues anlegen, Eigenes ändern (write)"],
      ["Fremdes", "ändern, löschen, umbenennen (supersede)"],
      ["Scopes", "vendor/<name>, dir <pfad>/"]
    ].forEach(function (col) {
      var th = text("th", col[0]);
      th.scope = "col";
      if (col.length > 1) {
        th.appendChild(text("span", col[1], "sub"));
      }
      tr.appendChild(th);
    });
    thead.appendChild(tr);
    return thead;
  }

  // accountBlock baut den Block eines Accounts: Name, Status, Beschreibung,
  // die Tabelle seiner Collections und wo sein Token meist liegt.
  function accountBlock(a) {
    var locked = a.locked === true;
    var block = document.createElement("article");
    block.className = locked ? "account locked" : "account";

    var title = document.createElement("h3");
    title.appendChild(text("span", String(a.name), "name"));
    title.appendChild(text("span", locked ? TEXT.locked : TEXT.active, locked ? "badge locked" : "badge"));
    block.appendChild(title);
    if (typeof a.description === "string" && a.description !== "") {
      block.appendChild(text("p", a.description, "description"));
    }

    var rows = document.createElement("tbody");
    (Array.isArray(a.collections) ? a.collections : []).forEach(function (c) {
      if (c && typeof c === "object") {
        rows.appendChild(collectionRow(c, locked));
      }
    });
    if (rows.childElementCount === 0) {
      block.appendChild(text("p", TEXT.noCollection, "hint"));
    } else {
      var box = document.createElement("div");
      box.className = "table-box";
      var table = document.createElement("table");
      if (locked) {
        table.appendChild(text("caption", TEXT.lockedCaption));
      }
      table.appendChild(head());
      table.appendChild(rows);
      box.appendChild(table);
      block.appendChild(box);
    }

    var hint = text("p", TEXT.tokenHint, "hint");
    hint.appendChild(text("code", "~/.config/kephalaion/tokens/<hub>/" + String(a.name) + ".token"));
    hint.appendChild(document.createTextNode(TEXT.tokenHintEnd));
    block.appendChild(hint);
    return block;
  }

  // showResult zeigt die Accounts von data.user. Ob das der angemeldete User
  // (data.viewer) ist, entscheidet nur Überschrift und Text — die Seite
  // zeigt jede Antwort gleich.
  function showResult(data) {
    var own = data.user === data.viewer;
    document.getElementById("viewer").textContent = data.viewer;
    document.getElementById("viewer-line").hidden = false;
    document.getElementById("result-title").textContent = own ? TEXT.ownTitle : TEXT.otherTitle + data.user;
    document.getElementById("result-user").textContent = data.user;

    var list = document.getElementById("accounts");
    list.textContent = "";
    data.accounts.forEach(function (a) {
      if (a && typeof a === "object") {
        list.appendChild(accountBlock(a));
      }
    });
    var empty = document.getElementById("result-empty");
    empty.textContent = own ? TEXT.ownEmpty : data.user + TEXT.otherEmpty;
    empty.hidden = list.childElementCount !== 0;

    status.hidden = true;
    result.hidden = false;
  }

  function load() {
    var abort = new AbortController();
    var timer = window.setTimeout(function () { abort.abort(); }, TIMEOUT_MS);
    // redirect "manual": Der Hub leitet nie um. Eine Umleitung kommt von der
    // Anmeldung davor (authproxy ohne Sitzung: 302 zur Anmeldung).
    return fetch(USER_API, {
      method: "GET",
      headers: { "Accept": "application/json" },
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

  function show(out) {
    switch (out.kind) {
      case "ok":
        showResult(out.data);
        return;
      case "unauthenticated":
        showMessage(TEXT.unauthenticated, "error");
        return;
      case "invalidUser":
        showMessage(TEXT.invalidUser, "error");
        return;
      case "forbidden":
        showMessage(TEXT.forbidden, "error");
        return;
      case "invalid":
        showMessage(TEXT.invalid + out.detail, "error");
        return;
      case "internal":
        showMessage(TEXT.internal, "error");
        reload.hidden = false;
        return;
      case "expired":
        showMessage(TEXT.expired, "error");
        reload.hidden = false;
        reload.focus();
        return;
      case "unexpected":
        showMessage(TEXT.unexpected + " (HTTP " + out.status + ").", "error");
        reload.hidden = false;
        return;
      default:
        showMessage(TEXT.unreachable, "error");
        reload.hidden = false;
    }
  }

  reload.addEventListener("click", function () { window.location.reload(); });

  showMessage(TEXT.loading, "info");
  load().then(show);
}());
