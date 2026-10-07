// Weboberfläche des Hubs: zeigt die Accounts des Users, mit dem der Browser
// am Proxy angemeldet ist, mit ihren Collections und Rechten
// (GET gui/api/user). Einem Admin der Weboberfläche (der Hub meldet admin)
// zeigt sie zuerst die Liste aller Accounts nach User (GET gui/api/users);
// ein Klick auf einen User zeigt dessen Ansicht (gui/api/user?name=…),
// „Alle Accounts“ führt zurück — innerhalb der Seite, die Adresse bleibt.
//
// Regeln dieser Datei:
//   - Nur relative Pfade. Die Seite liegt an der Wurzel des Hub-Listeners,
//     hinter einem Proxy unter dessen Präfix; das Binary kennt ihn nicht.
//   - Sie fragt nur ihre beiden Eingänge (USER_API, USERS_API), über genau
//     einen fetch in apiGet, immer per GET ohne Body.
//   - Die Seite fragt kein Token ab und zeigt keins: Wen sie zeigt und ob
//     der Angemeldete Admin ist, sagt der Hub (viewer, admin, user) aus der
//     Anmeldung des Proxys.
//   - Alles, was vom Hub kommt, wird per textContent gesetzt, nie als HTML.
//   - Einer Umleitung folgt sie nicht: Sie kommt von der Anmeldung davor und
//     heißt, dass die Anmeldung abgelaufen ist.
//   - Sie setzt nicht voraus, dass der gezeigte User (user) der angemeldete
//     (viewer) ist.
//   - Sie liest die Adresse nicht, ändert sie nicht und legt nichts ab: Die
//     Ansicht wechselt nur innerhalb der Seite.
(function () {
  "use strict";

  // Die Eingänge der Seite, relativ zur Seite: die Accounts eines Users und
  // die Liste aller (nur für einen Admin der Weboberfläche).
  var USER_API = "gui/api/user";
  var USERS_API = "gui/api/users";
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
    listForbidden: "Die Liste aller Accounts zeigt dir der Hub nicht — nur einem Admin der " +
      "Weboberfläche (Rolle kephalaion.admin am Proxy).",
    invalid: "Der Hub hat die Anfrage abgelehnt: ",
    internal: "Fehler am Hub. Die Einzelheit steht in seinem Log.",
    expired: "Deine Anmeldung an dieser Seite ist abgelaufen — Seite neu laden und neu anmelden.",
    unreachable: "Hub nicht erreichbar.",
    unexpected: "Hub nicht erreichbar: unerwartete Antwort",
    loadingList: "Lade alle Accounts …",
    loadingUser: "Lade die Accounts von ",
    loadingEnd: " …",
    listEmpty: "An diesem Hub gibt es noch keinen Account.",
    listUsers: " User mit ",
    listAccount: " Account",
    listAccounts: " Accounts",
    you: "(du)",
    noCollections: "keine Collection",
    remembered: "gemerkt: ",
    lockedShort: "gesperrt",
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
  var list = document.getElementById("list");
  var back = document.getElementById("back");

  function showMessage(text, kind) {
    message.textContent = text;
    message.className = "message " + kind;
    status.hidden = false;
  }

  // busy blendet die Ansichten aus und zeigt, was geladen wird.
  function busy(text) {
    result.hidden = true;
    list.hidden = true;
    reload.hidden = true;
    showMessage(text, "info");
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

  // isUser erkennt den Erfolg von gui/api/user: viewer, user und accounts.
  function isUser(data) {
    return typeof data.viewer === "string" && typeof data.user === "string" && Array.isArray(data.accounts);
  }

  // isUsers erkennt den Erfolg von gui/api/users: viewer, admin und users.
  function isUsers(data) {
    return typeof data.viewer === "string" && data.admin === true && Array.isArray(data.users);
  }

  // outcome ordnet eine Antwort ein. Der Hub ist am JSON zu erkennen: eine
  // Fehlerantwort trägt code, ein Erfolg die Form, die valid erkennt (isUser,
  // isUsers). Ohne diese Form kommt die Antwort von davor:
  //   - eine Umleitung, 401 oder 403, oder eine Seite statt JSON — die
  //     Anmeldung an der Seite ist abgelaufen;
  //   - alles andere (502, 503 eines Proxys) — der Hub ist nicht erreichbar.
  function outcome(resp, data, valid) {
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
    if (resp.status === 200 && data && valid(data)) {
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
      var items = document.createElement("ul");
      items.className = resting ? "scopes resting" : "scopes";
      labels.forEach(function (label) {
        var li = document.createElement("li");
        li.appendChild(text("code", label));
        items.appendChild(li);
      });
      scopes.appendChild(items);
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

  // showViewer nennt im Kopf, wer angemeldet ist.
  function showViewer(viewer) {
    document.getElementById("viewer").textContent = viewer;
    document.getElementById("viewer-line").hidden = false;
  }

  // showResult zeigt die Accounts von data.user. Ob das der angemeldete User
  // (data.viewer) ist, entscheidet nur Überschrift und Text — die Seite
  // zeigt jede Antwort gleich. Einem Admin bietet sie „Alle Accounts“.
  function showResult(data) {
    var own = data.user === data.viewer;
    showViewer(data.viewer);
    document.getElementById("result-nav").hidden = data.admin !== true;
    document.getElementById("result-title").textContent = own ? TEXT.ownTitle : TEXT.otherTitle + data.user;
    document.getElementById("result-user").textContent = data.user;

    var blocks = document.getElementById("accounts");
    blocks.textContent = "";
    data.accounts.forEach(function (a) {
      if (a && typeof a === "object") {
        blocks.appendChild(accountBlock(a));
      }
    });
    var empty = document.getElementById("result-empty");
    empty.textContent = own ? TEXT.ownEmpty : data.user + TEXT.otherEmpty;
    empty.hidden = blocks.childElementCount !== 0;

    status.hidden = true;
    list.hidden = true;
    result.hidden = false;
  }

  // listAccount baut die Zeile eines Accounts in der Liste: Name, Status und
  // die Collections, durch Komma getrennt — bei einem gesperrten die
  // gemerkten.
  function listAccount(a) {
    var locked = a.locked === true;
    var li = document.createElement("li");
    li.appendChild(text("span", String(a.name), "name"));
    li.appendChild(text("span", locked ? TEXT.lockedShort : TEXT.active, locked ? "badge locked" : "badge"));
    var names = (Array.isArray(a.collections) ? a.collections : []).map(String);
    if (names.length === 0) {
      li.appendChild(text("span", TEXT.noCollections, "collections no"));
    } else if (locked) {
      li.appendChild(text("span", TEXT.remembered + names.join(", "), "collections resting"));
    } else {
      li.appendChild(text("span", names.join(", "), "collections"));
    }
    return li;
  }

  // userGroup baut die Gruppe eines Users: sein Name als Knopf, der seine
  // Ansicht öffnet, der eigene mit „(du)“, darunter seine Accounts.
  function userGroup(name, accounts, own) {
    var group = document.createElement("article");
    group.className = "user-group";
    var title = document.createElement("h3");
    var open = text("button", name, "user-name");
    open.type = "button";
    open.addEventListener("click", function () { openUser(name); });
    title.appendChild(open);
    if (own) {
      title.appendChild(text("span", TEXT.you, "you"));
    }
    group.appendChild(title);
    var rows = document.createElement("ul");
    rows.className = "account-list";
    accounts.forEach(function (a) {
      rows.appendChild(listAccount(a));
    });
    group.appendChild(rows);
    return group;
  }

  // showList zeigt die Liste aller Accounts nach User, oben die Zahl der User
  // und Accounts.
  function showList(data) {
    showViewer(data.viewer);
    var groups = document.getElementById("users");
    groups.textContent = "";
    var users = 0;
    var accounts = 0;
    data.users.forEach(function (u) {
      if (u && typeof u === "object" && typeof u.name === "string") {
        var accs = (Array.isArray(u.accounts) ? u.accounts : []).filter(function (a) {
          return a && typeof a === "object";
        });
        groups.appendChild(userGroup(u.name, accs, u.name === data.viewer));
        users += 1;
        accounts += accs.length;
      }
    });
    document.getElementById("list-count").textContent = users + TEXT.listUsers + accounts +
      (accounts === 1 ? TEXT.listAccount : TEXT.listAccounts);
    var empty = document.getElementById("list-empty");
    empty.textContent = TEXT.listEmpty;
    empty.hidden = users !== 0;

    status.hidden = true;
    result.hidden = true;
    list.hidden = false;
  }

  // apiGet fragt einen Eingang der Seite und ordnet die Antwort ein. Es ist
  // der einzige fetch der Seite, und gerufen wird es nur mit den beiden
  // festen Eingängen (loadUser, loadUsers): immer GET ohne Body, ohne einer
  // Umleitung zu folgen. redirect "manual": Der Hub leitet nie um. Eine
  // Umleitung kommt von der Anmeldung davor (authproxy ohne Sitzung: 302 zur
  // Anmeldung).
  function apiGet(path, valid) {
    var abort = new AbortController();
    var timer = window.setTimeout(function () { abort.abort(); }, TIMEOUT_MS);
    return fetch(path, {
      method: "GET",
      headers: { "Accept": "application/json" },
      credentials: "same-origin",
      cache: "no-store",
      redirect: "manual",
      referrerPolicy: "no-referrer",
      signal: abort.signal
    }).then(function (resp) {
      return readJSON(resp).then(function (data) { return outcome(resp, data, valid); });
    }).then(function (out) {
      window.clearTimeout(timer);
      return out;
    }, function () {
      window.clearTimeout(timer);
      return { kind: "unreachable" };
    });
  }

  // loadUser lädt die Accounts des angemeldeten Users oder, mit name, die
  // eines anderen (nur für einen Admin).
  function loadUser(name) {
    if (typeof name !== "string") {
      return apiGet(USER_API, isUser);
    }
    return apiGet(USER_API + "?name=" + encodeURIComponent(name), isUser);
  }

  // loadUsers lädt die Liste aller Accounts (nur für einen Admin).
  function loadUsers() {
    return apiGet(USERS_API, isUsers);
  }

  // show zeigt den Ausgang einer Anfrage: bei Erfolg die Ansicht, die
  // geladen wurde (view: "user" oder "list"), sonst die Meldung. nav sagt,
  // dass ein Klick die Ansicht gewechselt hat: Dann bekommt ihre Überschrift
  // den Fokus, und jede Meldung bietet „Seite neu laden“.
  function show(out, view, nav) {
    if (out.kind === "ok") {
      if (view === "list") {
        showList(out.data);
      } else {
        showResult(out.data);
      }
      if (nav) {
        document.getElementById(view === "list" ? "list-title" : "result-title").focus();
      }
      return;
    }
    result.hidden = true;
    list.hidden = true;
    reload.hidden = !nav;
    switch (out.kind) {
      case "unauthenticated":
        showMessage(TEXT.unauthenticated, "error");
        return;
      case "invalidUser":
        showMessage(TEXT.invalidUser, "error");
        return;
      case "forbidden":
        showMessage(view === "list" ? TEXT.listForbidden : TEXT.forbidden, "error");
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

  // openList lädt die Liste aller Accounts und zeigt sie.
  function openList(nav) {
    busy(TEXT.loadingList);
    loadUsers().then(function (out) { show(out, "list", nav); });
  }

  // openUser lädt die Accounts eines Users aus der Liste und zeigt sie.
  function openUser(name) {
    busy(TEXT.loadingUser + name + TEXT.loadingEnd);
    loadUser(name).then(function (out) { show(out, "user", true); });
  }

  reload.addEventListener("click", function () { window.location.reload(); });
  back.addEventListener("click", function () { openList(true); });

  // Die Seite fragt zuerst nach dem angemeldeten User. Erst wenn der Hub ihn
  // als Admin meldet, holt sie die Liste — so sieht ein anderer keine 403.
  showMessage(TEXT.loading, "info");
  loadUser().then(function (out) {
    if (out.kind === "ok" && out.data.admin === true) {
      openList(false);
      return;
    }
    show(out, "user", false);
  });
}());
