package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kephalaion/kephalaion/internal/config"
	"github.com/kephalaion/kephalaion/internal/node/mcpnode"
)

// Das Werkzeug sync durch serve (Task 042): Der Abgleich läuft im Abgleich im
// Hintergrund desselben serve, auch bei sync_interval 0, und die Antwort
// trägt sein Ergebnis — Erfolg mit Stand über local und http; je Fehlschlag
// der Code der Tabelle, in der Meldung der feste Satz der Art. Die Adresse
// des Hubs steht nur in der Zeile des Abgleichs im Log, nie in der Antwort.
func TestMCPSyncThroughServe(t *testing.T) {
	e := newCommEnv(t)
	noRetry(t)
	e.run(t, "node", "sync").want(t, 0)
	// Keine Runden: Nur das Werkzeug gleicht ab.
	e.run(t, "config", "set", "node", "sync_interval", "0").want(t, 0)
	ns := nodeStore(t, e.cfg)
	srv := startServe(t, portZero(t, e.cfg))
	endpoint := "http://" + srv.addrs[config.Node] + mcpnode.Path
	alice := map[string][2]string{"eigen": {"alice", e.tokens["alice"]}, "fern": {"alice", e.tokens["alice"]}}
	var raws []string
	call := func(hub string) (mcpnode.SyncOutput, string) {
		t.Helper()
		var out mcpnode.SyncOutput
		raw, text := mcpTool(t, endpoint, alice, "sync", mcpnode.SyncInput{Hub: hub}, &out)
		raws = append(raws, raw)
		return out, text
	}
	failed := func(what, hub, code, want string) {
		t.Helper()
		out, text := call(hub)
		if out.Error == nil || out.Error.Code != code || out.Error.Message != text || out.Sync != nil ||
			!strings.Contains(text, want) {
			t.Errorf("%s: %+v, %q; erwartet %s mit %q\nLog:\n%s", what, out.Error, text, code, want, srv.log.String())
		}
		if !logLine(srv.log.String(), "op=sync", "hub="+hub, "code="+code) {
			t.Errorf("%s: keine Zeile der Anfrage mit code=%s:\n%s", what, code, srv.log.String())
		}
	}

	// Erfolg über http und über local: Was am Hub neu ist, steht danach in
	// der Replica, und last_success ist der Erfolg in hub_sync.
	e.runIn(t, "neu", "hub", "doc", "put", "team-x", "a.md").want(t, 0)
	for _, hub := range []string{"fern", "eigen"} {
		if e.docIs(t, hub+":team-x", "a.md", "neu") {
			t.Fatalf("%s: a.md schon vor dem Abgleich da (sync_interval 0)", hub)
		}
		before := syncStatus(t, ns, hub)
		out, text := call(hub)
		st := syncStatus(t, ns, hub)
		if out.Error != nil || out.Hub != hub || out.Sync == nil || out.Sync.Revision == nil || *out.Sync.Revision == 0 ||
			out.Sync.LastError != "" || out.Sync.LastSuccess != time.UnixMilli(st.OKAt).UTC().Format(time.RFC3339) ||
			st.OKAt <= before.OKAt || !strings.Contains(text, "Hub "+hub+": abgeglichen ") {
			t.Errorf("%s: %+v, %q, hub_sync %+v (vorher %+v)", hub, out, text, st, before)
		}
		if !e.docIs(t, hub+":team-x", "a.md", "neu") {
			t.Errorf("%s: a.md fehlt nach dem Abgleich", hub)
		}
	}
	if !logLine(srv.log.String(), "op=sync", "hub=fern", "node=laptop-http") ||
		!logLine(srv.log.String(), "op=sync", "hub=eigen", "node=laptop") {
		t.Errorf("Zeilen der Anfragen:\n%s", srv.log.String())
	}

	// Vor dem Hub antwortet etwas anderes mit 502, ohne Code des Vertrags
	// (ein Proxy vor einem gestoppten Hub): nicht erreicht, nicht abgelehnt.
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, "<html>502 Bad Gateway</html>")
	}))
	defer proxy.Close()
	e.run(t, "node", "hub", "set", "fern", "--address", proxy.URL).want(t, 0)
	failed("502 ohne Code des Vertrags", "fern", "unreachable", "Hub fern: Abgleich gescheitert — ")
	// Die Ursache steht in der Zeile des Abgleichs und darf die Adresse nennen.
	if !logLine(srv.log.String(), "Abgleich fern gescheitert", proxy.URL) {
		t.Errorf("Zeile des Abgleichs:\n%s", srv.log.String())
	}

	// Der Hub lehnt mit einem Fehler des Vertrags ab.
	refusing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"code":"invalid","message":"ungültige Anfrage: geheime Einzelheit"}`)
	}))
	defer refusing.Close()
	e.run(t, "node", "hub", "set", "fern", "--address", refusing.URL).want(t, 0)
	failed("Fehler des Vertrags", "fern", "hub_refused", "der Hub meldet einen Fehler")

	// Niemand lauscht.
	closed := closedAddress(t)
	e.run(t, "node", "hub", "set", "fern", "--address", closed).want(t, 0)
	failed("nicht erreicht", "fern", "unreachable", "Hub nicht erreichbar")

	// Der Hub nimmt den Node nicht an: durchgereicht wie bei den Werkzeugen,
	// die schreiben.
	e.run(t, "node", "hub", "set", "fern", "--address", e.url).want(t, 0)
	e.run(t, "hub", "node", "lock", "laptop-http").want(t, 0)
	failed("Node gesperrt", "fern", "unauthenticated", "der Hub nimmt diesen Node nicht an")
	e.run(t, "hub", "node", "unlock", "laptop-http").want(t, 0)
	if out, text := call("fern"); out.Error != nil || out.Sync == nil || out.Sync.LastError != "" {
		t.Errorf("nach unlock: %+v, %q", out, text)
	}

	// Ein Eintrag mit Replica, danach auf ssh gestellt: Der Aufrufer ist
	// weiter angemeldet, der Abgleich geht über den Transport noch nicht.
	e.run(t, "node", "hub", "set", "fern", "--transport", "ssh", "--address", "keph@hub.example.org").want(t, 0)
	failed("ssh", "fern", "unsupported", "Transport dieses Hubs")

	// Weder Adresse noch Token in einer Antwort, kein Token im Log.
	all := strings.Join(raws, "\n")
	for _, secret := range []string{"keph_", proxy.URL, refusing.URL, closed, e.url, "hub.example.org", "geheime Einzelheit"} {
		if strings.Contains(all, secret) {
			t.Errorf("eine Antwort nennt %q:\n%s", secret, all)
		}
	}
	if strings.Contains(srv.log.String(), "keph_") {
		t.Errorf("Token im Log:\n%s", srv.log.String())
	}
}
