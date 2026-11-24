package mcpnode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kephalaion/kephalaion/internal/contract"
	"github.com/kephalaion/kephalaion/internal/node/replica"
	"github.com/kephalaion/kephalaion/internal/node/store"
	"github.com/kephalaion/kephalaion/internal/upgrade"
)

// callSync ruft sync und liest die strukturierte Antwort, auch bei isError.
// raw ist das ganze Ergebnis als JSON — Text und Struktur.
func (e *docEnv) callSync(t *testing.T, header http.Header, hub string) (out SyncOutput, raw, errText string) {
	t.Helper()
	res, errText := e.call(t, header, "sync", SyncInput{Hub: hub}, nil)
	b, _ := json.Marshal(res.StructuredContent)
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	all, _ := json.Marshal(res)
	return out, string(all), errText
}

// wantSyncCode prüft einen Fehler von sync: isError, Code in der Struktur,
// dieselbe Meldung in Text und Struktur, mit want darin.
func wantSyncCode(t *testing.T, what string, out SyncOutput, errText, code string, want ...string) {
	t.Helper()
	if out.Error == nil || errText == "" {
		t.Errorf("%s: kein Fehler: %+v", what, out)
		return
	}
	if out.Error.Code != code || out.Error.Message != errText || out.Sync != nil {
		t.Errorf("%s: %+v, Text %q; erwartet Code %s", what, out.Error, errText, code)
	}
	for _, w := range want {
		if !strings.Contains(errText, w) {
			t.Errorf("%s: Meldung %q ohne %q", what, errText, w)
		}
	}
}

// status liest hub_sync eines Eintrags.
func (e *docEnv) status(t *testing.T, alias string) store.SyncStatus {
	t.Helper()
	st, err := e.nodes.SyncStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return st[alias]
}

// lineOf liefert die letzte Zeile des Logs mit part.
func lineOf(log, part string) string {
	last := ""
	for _, line := range strings.Split(log, "\n") {
		if strings.Contains(line, part) {
			last = line
		}
	}
	return last
}

// sync über MCP (Task 042): Der Erfolg ist eindeutig — revision steht in der
// Antwort, last_error ist leer, last_success ist der Zeitpunkt des
// abgewarteten Abgleichs —, und die Replica hat danach, was am Hub neu ist.
func TestSyncTool(t *testing.T) {
	e := newDocEnv(t)
	hub := e.hubs["keph"]
	before := e.status(t, "keph")
	hub.put("wissen", "neu.md", "neu")
	if doc, _, _ := e.read(t, e.otto(), ReadInput{Collection: "keph:wissen", Name: "neu.md"}); doc.Kind != KindNone {
		t.Fatalf("vor dem Abgleich: %+v", doc)
	}
	out, raw, errText := e.callSync(t, e.otto(), "keph")
	if errText != "" {
		t.Fatal(errText)
	}
	st := e.status(t, "keph")
	if out.Hub != "keph" || out.Error != nil || out.Sync == nil || out.Sync.Revision == nil ||
		*out.Sync.Revision != hub.rev || out.Sync.LastError != "" || out.Sync.LastErrorAt != "" || out.Sync.NeverSynced ||
		out.Sync.LastSuccess != formatTime(st.OKAt) || st.OKAt <= before.OKAt {
		t.Errorf("Antwort %s, hub_sync %+v (vorher %+v)", raw, st, before)
	}
	if !strings.Contains(raw, `"revision":`) || strings.Contains(raw, "last_error") ||
		!strings.Contains(raw, fmt.Sprintf("Hub keph: abgeglichen %s, Revision %d", out.Sync.LastSuccess, hub.rev)) {
		t.Errorf("Antwort: %s", raw)
	}
	if w := e.takeWaits(); !slices.Equal(w, []string{"keph"}) {
		t.Errorf("wartende Anstöße %v", w)
	}
	if doc, _, _ := e.read(t, e.otto(), ReadInput{Collection: "keph:wissen", Name: "neu.md"}); doc.Kind != KindDocument {
		t.Errorf("nach dem Abgleich: %+v", doc)
	}
	// Die Zeile der Anfrage: Vorgang, Hub, Node — kein Code bei Erfolg.
	if line := lineOf(e.log.String(), "op=sync"); !strings.Contains(line, "hub=keph") ||
		!strings.Contains(line, "node=laptop") || strings.Contains(line, "code=") {
		t.Errorf("Logzeile: %q", line)
	}
	// Weder Adresse noch Token in der Antwort, kein Token im Log.
	for _, secret := range append([]string{"hub.example.org"}, e.secrets(t)...) {
		if strings.Contains(raw, secret) {
			t.Errorf("Antwort nennt %q: %s", secret, raw)
		}
	}
	for _, tok := range e.secrets(t) {
		if strings.Contains(e.log.String(), tok) {
			t.Errorf("Token im Log:\n%s", e.log.String())
		}
	}

	// Zeigt hub_sync beim Antworten schon einen späteren, gescheiterten
	// Abgleich, bleibt es der Erfolg des abgewarteten: last_error leer,
	// last_success sein Zeitpunkt.
	var at int64
	e.waitWith(func(ctx context.Context, h store.Hub) (SyncOutcome, error) {
		res, err := e.syncNow(ctx, h)
		at = res.At
		if rerr := e.nodes.RecordSync(ctx, h.Name, h.EntryID, store.SyncRecord{At: res.At + 5000,
			Err: "Post https://hub.example.org/v1/sync: dial tcp", ErrKind: string(replica.KindUnreachable)}); rerr != nil {
			t.Error(rerr)
		}
		return res, err
	})
	out, raw, errText = e.callSync(t, e.otto(), "keph")
	if errText != "" || out.Sync == nil || out.Sync.LastError != "" || out.Sync.LastSuccess != formatTime(at) ||
		out.Sync.Revision == nil || strings.Contains(raw, "last_error") || strings.Contains(raw, "hub.example.org") {
		t.Errorf("späterer Fehler in hub_sync: %s, %q", raw, errText)
	}
	if st := e.status(t, "keph"); st.ErrKind != string(replica.KindUnreachable) {
		t.Fatalf("hub_sync trägt den späteren Fehler nicht: %+v", st)
	}
	e.waitWith(nil)

	// Ein Hub-Eintrag ohne gewünschte Collection: revision steht mit 0 in der
	// Antwort. Der Aufrufer ist vor dem Abgleich noch angemeldet — die Replica
	// hat seine Zeile, bis der Abgleich die Collection entfernt.
	if err := e.nodes.RemoveCollection(context.Background(), "team", "notizen"); err != nil {
		t.Fatal(err)
	}
	out, raw, errText = e.callSync(t, e.anna(), "team")
	if errText != "" || out.Hub != "team" || out.Sync == nil || out.Sync.Revision == nil || *out.Sync.Revision != 0 ||
		out.Sync.LastSuccess == "" || !strings.Contains(raw, `"revision":0`) {
		t.Errorf("ohne gewünschte Collection: %s, %q", raw, errText)
	}
}

// secrets sind alle Tokens der Umgebung: die der Accounts und die der Nodes
// an ihren Hubs.
func (e *docEnv) secrets(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, tok := range e.tokens {
		out = append(out, tok)
	}
	hubs, err := e.nodes.Hubs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hubs {
		out = append(out, h.Token)
	}
	return out
}

// Das Feld hub: Ein angegebener Alias wird nur als Name geprüft, nie als
// Adresse aufgelöst; ein unbekannter ist ein Fehler, nie ein Rückfall auf den
// einzigen Hub. Fehlt hub, gilt die Regel für den fehlenden Hub-Teil. Ohne
// gültige Anmeldung am Hub stößt nichts einen Abgleich an.
func TestSyncToolHub(t *testing.T) {
	e := newDocEnv(t)
	ctx := context.Background()
	none := func(what string) {
		t.Helper()
		if w := e.takeWaits(); len(w) != 0 {
			t.Errorf("%s: Abgleich angestoßen: %v", what, w)
		}
	}

	// Nicht angemeldet: not_readable, ohne Abgleich — ohne Header, mit
	// falschem Token, und otto an team (dort hat er keinen Account).
	for name, h := range map[string]http.Header{"ohne Header": nil, "falsches Token": pair("keph", "otto", token(t)),
		"nur an keph": e.otto()} {
		hub := "keph"
		if name == "nur an keph" {
			hub = "team"
		}
		out, _, errText := e.callSync(t, h, hub)
		wantSyncCode(t, name, out, errText, "not_readable", "Hub "+hub, "ohne gültige Anmeldung")
		none(name)
	}

	// Ungültiger Alias: invalid; die Meldung nennt das Feld hub und die
	// Aliase, die in Frage kommen. Eine Adresse ist kein Alias.
	for _, bad := range []string{"Keph", "keph:wissen", "wissen/x", "-keph"} {
		out, _, errText := e.callSync(t, e.anna(), bad)
		wantSyncCode(t, "ungültig "+bad, out, errText, "invalid", "Feld hub: ", "keph, team")
		if out.Hub != bad {
			t.Errorf("%s: hub %q", bad, out.Hub)
		}
		none(bad)
	}

	// hub fehlt, zwei Einträge: mit gültiger Anmeldung an einem gilt dieser,
	// an zweien ist es invalid, an keinem ebenso.
	out, _, errText := e.callSync(t, e.otto(), "")
	if errText != "" || out.Hub != "keph" || out.Sync == nil {
		t.Errorf("hub fehlt, angemeldet an einem: %+v, %q", out, errText)
	}
	if w := e.takeWaits(); !slices.Equal(w, []string{"keph"}) {
		t.Errorf("hub fehlt, angemeldet an einem: Anstöße %v", w)
	}
	out, _, errText = e.callSync(t, e.anna(), "")
	wantSyncCode(t, "hub fehlt, zwei Anmeldungen", out, errText, "invalid", "Feld hub fehlt", "keph, team")
	none("zwei Anmeldungen")
	out, _, errText = e.callSync(t, nil, "")
	wantSyncCode(t, "hub fehlt, keine Anmeldung", out, errText, "invalid", "Feld hub fehlt", "keph, team")
	none("keine Anmeldung")

	// Unbekannter Alias: unknown_hub, nie der andere Hub.
	out, _, errText = e.callSync(t, e.anna(), "fremd")
	wantSyncCode(t, "unbekannt", out, errText, "unknown_hub", "Hub fremd", "keph, team")
	none("unbekannt")

	// Nur noch ein Eintrag: Ein unbekannter Alias bleibt unknown_hub; ohne hub
	// gilt der eine — mit Anmeldung ein Abgleich, ohne not_readable.
	if err := e.nodes.RemoveHub(ctx, "team"); err != nil {
		t.Fatal(err)
	}
	out, _, errText = e.callSync(t, e.otto(), "fremd")
	wantSyncCode(t, "unbekannt bei einem Hub", out, errText, "unknown_hub", "Hub fremd")
	none("unbekannt bei einem Hub")
	out, _, errText = e.callSync(t, e.otto(), "")
	if errText != "" || out.Hub != "keph" || out.Sync == nil {
		t.Errorf("hub fehlt, ein Eintrag: %+v, %q", out, errText)
	}
	e.takeWaits()
	out, _, errText = e.callSync(t, nil, "")
	wantSyncCode(t, "hub fehlt, ein Eintrag, ohne Anmeldung", out, errText, "not_readable", "Hub keph")
	none("ein Eintrag, ohne Anmeldung")
	if line := lineOf(e.log.String(), "op=sync"); !strings.Contains(line, "hub=keph") ||
		!strings.Contains(line, "node=laptop") || !strings.Contains(line, "code=not_readable") {
		t.Errorf("Logzeile: %q", line)
	}
}

// Ein Eintrag ohne Replica: never_synced mit eigenem Code, mit und ohne
// Header-Paar — geprüft vor der Anmeldung, die ohne Replica immer ungültig
// ist. Die Abhilfe liegt am Rechner des Nodes.
func TestSyncToolNeverSynced(t *testing.T) {
	e := newDocEnv(t)
	if err := e.nodes.AddHub(context.Background(), store.Hub{Name: "neu", NodeName: "laptop",
		Transport: store.TransportHTTPS, Address: "https://hub.example.org", Token: token(t)}, false); err != nil {
		t.Fatal(err)
	}
	for name, h := range map[string]http.Header{
		"ohne Header":       nil,
		"mit Paar":          pair("neu", "anna", e.tokens["keph/anna"]),
		"an keph":           e.otto(),
		"über Proxy, keph":  forwarded(e.otto()),
		"über Proxy, beide": forwarded(merge(e.otto(), pair("neu", "anna", e.tokens["keph/anna"]))),
	} {
		out, _, errText := e.callSync(t, h, "neu")
		wantSyncCode(t, name, out, errText, "never_synced", "Hub neu: noch nie abgeglichen",
			"kephalaion node sync neu am Rechner des Nodes")
	}
	if w := e.takeWaits(); len(w) != 0 {
		t.Errorf("Abgleich angestoßen: %v", w)
	}
	// Über einen Proxy ohne gültige Anmeldung an irgendeinem Hub bleibt auch
	// das verdeckt.
	for name, h := range map[string]http.Header{
		"ohne Paar": forwarded(nil),
		"mit Paar":  forwarded(pair("neu", "anna", e.tokens["keph/anna"])),
	} {
		out, _, errText := e.callSync(t, h, "neu")
		wantSyncCode(t, "verdeckt, "+name, out, errText, "not_readable")
		if strings.Contains(errText, "abgeglichen") {
			t.Errorf("verdeckt, %s: %q", name, errText)
		}
	}
}

// Über einen Proxy ohne gültige Anmeldung ist die Antwort verdeckt: dieselbe
// für einen Alias, den es gibt, und einen, den es nicht gibt; kein Name eines
// Hubs oder Nodes, den der Aufrufer nicht selbst genannt hat; kein Abgleich.
func TestSyncToolHidden(t *testing.T) {
	e := newDocEnv(t)
	h := forwarded(pair("keph", "otto", token(t)))
	known, _, textKnown := e.callSync(t, h, "keph")
	unknown, rawUnknown, textUnknown := e.callSync(t, h, "fremd")
	wantSyncCode(t, "verdeckt, bekannt", known, textKnown, "not_readable")
	wantSyncCode(t, "verdeckt, unbekannt", unknown, textUnknown, "not_readable")
	// Bis auf den Alias, den der Aufrufer selbst genannt hat, dieselbe Meldung.
	if strings.ReplaceAll(textKnown, "keph:", "X:") != strings.ReplaceAll(textUnknown, "fremd:", "X:") ||
		known.Hub != "keph" || unknown.Hub != "fremd" {
		t.Errorf("verdeckt unterscheidbar:\n%s\n%s", textKnown, textUnknown)
	}
	// hub fehlt und ein ungültiger Alias: invalid, ohne Namen.
	out, raw, errText := e.callSync(t, h, "")
	wantSyncCode(t, "verdeckt, hub fehlt", out, errText, "invalid", "Feld hub fehlt")
	bad, rawBad, textBad := e.callSync(t, h, "Fremd:x")
	wantSyncCode(t, "verdeckt, ungültig", bad, textBad, "invalid", "Feld hub: ")
	for what, s := range map[string]string{"unbekannt": rawUnknown, "hub fehlt": raw, "ungültig": rawBad} {
		// Der Name des Servers (kephalaion) ist kein Name eines Hubs.
		s = strings.ReplaceAll(s, `"name":"kephalaion"`, "")
		for _, name := range []string{"keph", "team", "laptop", "hub.example.org", "test"} {
			if strings.Contains(s, name) {
				t.Errorf("verdeckt, %s: Antwort nennt %q: %s", what, name, s)
			}
		}
	}
	if w := e.takeWaits(); len(w) != 0 {
		t.Errorf("verdeckt: Abgleich angestoßen: %v", w)
	}
	// Mit gültiger Anmeldung an einem Hub antwortet der Node wie lokal.
	out, _, errText = e.callSync(t, forwarded(e.otto()), "fremd")
	wantSyncCode(t, "über Proxy, angemeldet", out, errText, "unknown_hub", "keph, team")
	if out, _, errText := e.callSync(t, forwarded(e.otto()), "keph"); errText != "" || out.Sync == nil {
		t.Errorf("über Proxy, angemeldet: %+v, %q", out, errText)
	}
}

// Kein Erfolg ohne gelungenen Abgleich: je Art eines gescheiterten Abgleichs
// der Code der Tabelle „Codes je Fall“ und in der Meldung der feste Satz der
// Art — nie der Fehlertext des Abgleichs, der die Adresse nennen kann.
// Entfernter Eintrag, Ende von serve und ssh haben ihre eigenen Meldungen.
func TestSyncToolFailures(t *testing.T) {
	e := newDocEnv(t)
	kinds := []struct {
		kind     replica.ErrorKind
		contract bool
		code     string
	}{
		{replica.KindUnreachable, false, "unreachable"},
		{replica.KindConnect, false, "unreachable"},
		// Ein HTTP-Status ohne Code des Vertrags (502 eines Proxys vor einem
		// gestoppten Hub), eine Antwort, die kein JSON ist.
		{replica.KindHub, false, "unreachable"},
		{replica.KindHub, true, "hub_refused"},
		{replica.KindProtocol, false, "hub_refused"},
		{replica.KindUnauthenticated, true, "unauthenticated"},
		{replica.KindVersion, true, "unsupported_version"},
		{replica.KindReplica, false, "internal"},
		{replica.ErrorKind("neu"), false, "internal"},
	}
	for _, k := range kinds {
		e.waitWith(func(context.Context, store.Hub) (SyncOutcome, error) {
			return SyncOutcome{Kind: k.kind, Contract: k.contract}, nil
		})
		out, raw, errText := e.callSync(t, e.otto(), "keph")
		what := fmt.Sprintf("Art %s, Vertrag %v", k.kind, k.contract)
		wantSyncCode(t, what, out, errText, k.code, "Hub keph: Abgleich gescheitert — "+k.kind.Text())
		if strings.Contains(raw, "hub.example.org") || strings.Contains(raw, "revision") {
			t.Errorf("%s: %s", what, raw)
		}
		if line := lineOf(e.log.String(), "op=sync"); !strings.Contains(line, "hub=keph") ||
			!strings.Contains(line, "node=laptop") || !strings.Contains(line, "code="+k.code) {
			t.Errorf("%s: Logzeile %q", what, line)
		}
	}

	// Ein echter Abgleich, der am Hub scheitert: Der Fehlertext nennt die
	// Adresse, die Antwort nicht.
	e.waitWith(func(ctx context.Context, h store.Hub) (SyncOutcome, error) {
		res := (&replica.Syncer{Nodes: e.nodes}).SyncEntry(ctx, h, func(store.Hub) (contract.Hub, error) {
			return nil, errors.New("Post https://hub.example.org/hub/v1/sync: 502 Bad Gateway")
		})
		if res.Err == nil || !strings.Contains(res.Err.Error(), "hub.example.org") {
			t.Errorf("Abgleich: %v", res.Err)
		}
		return SyncOutcome{Kind: res.Kind}, nil
	})
	out, raw, errText := e.callSync(t, e.otto(), "keph")
	wantSyncCode(t, "nicht verbunden", out, errText, "unreachable", replica.KindConnect.Text())
	if strings.Contains(raw, "hub.example.org") || strings.Contains(raw, "502") {
		t.Errorf("Antwort nennt den Fehlertext: %s", raw)
	}

	others := []struct {
		name string
		err  error
		code string
		want string
	}{
		{"ssh", fmt.Errorf("Transport ssh wird %w", ErrUnsupported), "unsupported", "Transport dieses Hubs"},
		{"entfernt", ErrSyncGone, "internal", "Den Aufruf wiederholen."},
		{"serve endet", ErrSyncStopped, "internal", "sobald der Node wieder läuft"},
		{"anderes", errors.New("Pfad /var/lib/kephalaion"), "internal", "nicht angestoßen"},
	}
	for _, c := range others {
		e.waitWith(func(context.Context, store.Hub) (SyncOutcome, error) { return SyncOutcome{}, c.err })
		out, raw, errText := e.callSync(t, e.otto(), "keph")
		wantSyncCode(t, c.name, out, errText, c.code, "Hub keph", c.want)
		if strings.Contains(raw, "/var/lib") {
			t.Errorf("%s: %s", c.name, raw)
		}
	}

	// Die Replica ist nach dem Abgleich nicht mehr da oder nicht lesbar: ein
	// Fehler mit Code, kein Erfolg ohne revision.
	e.waitWith(func(ctx context.Context, h store.Hub) (SyncOutcome, error) {
		res, err := e.syncNow(ctx, h)
		if rerr := replica.Remove(e.nodes.ReplicaPath(h.Name)); rerr != nil {
			t.Error(rerr)
		}
		return res, err
	})
	out, raw, errText = e.callSync(t, e.anna(), "team")
	wantSyncCode(t, "Replica weg", out, errText, "internal", "Hub team", "Den Aufruf wiederholen.")
	if strings.Contains(raw, "revision") {
		t.Errorf("Replica weg: %s", raw)
	}
	e.waitWith(func(ctx context.Context, h store.Hub) (SyncOutcome, error) {
		res, err := e.syncNow(ctx, h)
		path := e.nodes.ReplicaPath(h.Name)
		if rerr := replica.Remove(path); rerr != nil {
			t.Error(rerr)
		}
		if werr := os.WriteFile(path, []byte("keine Datenbank"), 0o600); werr != nil {
			t.Error(werr)
		}
		return res, err
	})
	out, raw, errText = e.callSync(t, e.otto(), "keph")
	wantSyncCode(t, "Replica unlesbar", out, errText, "internal", "Hub keph: "+ReplicaUnreadable)
	if strings.Contains(raw, "revision") || strings.Contains(raw, e.nodes.ReplicaPath("keph")) {
		t.Errorf("Replica unlesbar: %s", raw)
	}
}

// Ohne wartenden Anstoß (ein Eingang ohne Abgleich im Hintergrund):
// unsupported, wie die Werkzeuge ohne Connect.
func TestSyncToolWithoutLink(t *testing.T) {
	e := newDocEnv(t)
	n := &Node{nodes: e.nodes, version: "test", update: func() upgrade.Report { return testUpdate }}
	res, out, err := n.sync(context.Background(), callWith(e.otto()), SyncInput{Hub: "keph"})
	if err != nil || res == nil || !res.IsError || out.Error == nil || out.Error.Code != CodeUnsupported ||
		out.Hub != "keph" {
		t.Errorf("ohne Anstoß: %+v, %+v, %v", res, out, err)
	}
}

// Die Wartefrist: Läuft sie ab, ist das ein Fehler mit eigenem Code und der
// Zahl in der Meldung — auch wenn der wartende Anstoß mit dem Fehler des ctx
// zurückkommt, den toolFailure sonst ohne Code durchreicht. Bricht dagegen
// der Aufrufer ab, ist es ein Fehler der Anfrage ohne Code.
func TestSyncToolTimeout(t *testing.T) {
	if SyncWaitLimit != 40*time.Second || SyncWaitLimit > 45*time.Second || limitText(SyncWaitLimit) != "40 s" ||
		!strings.Contains(syncDescription, "höchstens nach 40 s") {
		t.Errorf("Wartefrist %s (%s); abgestimmt sind höchstens 45 s, dokumentiert 40 s", SyncWaitLimit,
			limitText(SyncWaitLimit))
	}
	e := newDocEnv(t)
	old := syncWaitLimit
	syncWaitLimit = 50 * time.Millisecond
	t.Cleanup(func() { syncWaitLimit = old })

	// Der Abgleich hängt: Der wartende Anstoß kommt erst mit dem ctx zurück.
	e.waitWith(func(ctx context.Context, _ store.Hub) (SyncOutcome, error) {
		<-ctx.Done()
		return SyncOutcome{}, ctx.Err()
	})
	out, raw, errText := e.callSync(t, e.otto(), "keph")
	wantSyncCode(t, "Frist", out, errText, "sync_timeout", "Hub keph", "nach 50ms", "Wartefrist",
		"läuft im Hintergrund weiter")
	if strings.Contains(raw, "revision") || strings.Contains(raw, "hub.example.org") {
		t.Errorf("Frist: %s", raw)
	}
	if line := lineOf(e.log.String(), "op=sync"); !strings.Contains(line, "code=sync_timeout") ||
		!strings.Contains(line, "hub=keph") {
		t.Errorf("Logzeile: %q", line)
	}
	// Ebenso, wenn der Anstoß den Fehler der Frist einhüllt.
	e.waitWith(func(ctx context.Context, _ store.Hub) (SyncOutcome, error) {
		<-ctx.Done()
		return SyncOutcome{}, fmt.Errorf("warten: %w", context.DeadlineExceeded)
	})
	out, _, errText = e.callSync(t, e.otto(), "keph")
	wantSyncCode(t, "Frist, eingehüllt", out, errText, "sync_timeout")

	// Der Aufrufer bricht ab, lange vor der Frist: ein Fehler der Anfrage,
	// kein Ergebnis mit Code.
	syncWaitLimit = time.Minute
	n := &Node{nodes: e.nodes, version: "test", update: func() upgrade.Report { return testUpdate }, link: e.link()}
	entered := make(chan struct{})
	e.waitWith(func(ctx context.Context, _ store.Hub) (SyncOutcome, error) {
		close(entered)
		<-ctx.Done()
		return SyncOutcome{}, ctx.Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-entered
		cancel()
	}()
	res, direct, err := n.sync(ctx, callWith(e.otto()), SyncInput{Hub: "keph"})
	if !errors.Is(err, context.Canceled) || res != nil || direct.Error != nil || direct.Sync != nil {
		t.Errorf("abgebrochen: %+v, %+v, %v", res, direct, err)
	}
	// Ebenso eine Frist des Aufrufers selbst: nicht die Wartefrist des Nodes.
	e.waitWith(func(ctx context.Context, _ store.Hub) (SyncOutcome, error) {
		<-ctx.Done()
		return SyncOutcome{}, ctx.Err()
	})
	tctx, tcancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer tcancel()
	res, direct, err = n.sync(tctx, callWith(e.otto()), SyncInput{Hub: "keph"})
	if !errors.Is(err, context.DeadlineExceeded) || res != nil || direct.Error != nil {
		t.Errorf("Frist des Aufrufers: %+v, %+v, %v", res, direct, err)
	}
}
