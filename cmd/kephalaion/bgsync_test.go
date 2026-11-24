package main

import (
	"context"
	"crypto/x509"
	"errors"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kephalaion/kephalaion/internal/config"
	"github.com/kephalaion/kephalaion/internal/contract"
	"github.com/kephalaion/kephalaion/internal/contract/httpapi"
	"github.com/kephalaion/kephalaion/internal/ident"
	"github.com/kephalaion/kephalaion/internal/node/mcpnode"
	"github.com/kephalaion/kephalaion/internal/node/replica"
	nodestore "github.com/kephalaion/kephalaion/internal/node/store"
	"github.com/kephalaion/kephalaion/internal/reqlog"
)

// eventually wartet, bis cond gilt, höchstens 15 s.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	if !waitUntil(cond) {
		t.Fatalf("wartet vergeblich auf: %s", what)
	}
}

// eventuallyLog wartet wie eventually auf etwas, das serve tut; scheitert
// es, steht der Log von serve in der Meldung.
func eventuallyLog(t *testing.T, srv *running, what string, cond func() bool) {
	t.Helper()
	if !waitUntil(cond) {
		t.Fatalf("wartet vergeblich auf: %s\nLog:\n%s", what, srv.log.String())
	}
}

// waitUntil prüft cond alle 50 ms, bis es gilt, höchstens 15 s.
func waitUntil(cond func() bool) bool {
	deadline := time.Now().Add(15 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
	return true
}

// syncStatus liest hub_sync eines Eintrags.
func syncStatus(t *testing.T, ns nodestore.Store, alias string) nodestore.SyncStatus {
	t.Helper()
	st, err := ns.SyncStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return st[alias]
}

// rounds wartet, bis der Eintrag n weitere Versuche festgehalten hat.
func rounds(t *testing.T, ns nodestore.Store, alias string, n int) {
	t.Helper()
	last := syncStatus(t, ns, alias)
	for range n {
		eventually(t, "nächste Runde von "+alias, func() bool {
			st := syncStatus(t, ns, alias)
			return st.OKAt > last.OKAt || st.ErrAt > last.ErrAt
		})
		last = syncStatus(t, ns, alias)
	}
}

// docIs sagt, ob die Replica eines Eintrags das Dokument mit diesem Inhalt
// hat.
func (e *commEnv) docIs(t *testing.T, addr, name, content string) bool {
	t.Helper()
	r := runIn(t, "", "node", "doc", "get", addr, name, e.c)
	return r.code == 0 && r.out == content
}

// noRetry lässt http ohne Wiederholung laufen: Ein nicht erreichbarer Hub
// kostet sonst Sekunden je Runde.
func noRetry(t *testing.T) {
	hookHTTP(t, func(address string, rootCAs *x509.CertPool) (contract.Hub, error) {
		c, err := httpapi.NewClient(address, rootCAs)
		if err != nil {
			return nil, err
		}
		c.Retries = 0
		return c, nil
	})
}

func closedAddress(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return "http://" + addr
}

func countLines(log, part string) int {
	n := 0
	for _, line := range strings.Split(log, "\n") {
		if strings.Contains(line, part) {
			n++
		}
	}
	return n
}

// serve gleicht über local und http im Hintergrund ab: Eine Änderung am Hub
// ist nach dem Abstand in beiden Replicas. Runden ohne Änderung loggen
// nichts.
func TestBackgroundSync(t *testing.T) {
	slow(t, "wartet auf Runden des Abgleichs (sync_interval 1s)")
	e := newCommEnv(t)
	ns := nodeStore(t, e.cfg)
	e.run(t, "config", "set", "node", "sync_interval", "1s").want(t, 0)
	extTok, err := ident.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	e.runIn(t, extTok, "node", "hub", "add", "extern", "--node", "laptop", "--transport", "ssh",
		"--address", "keph@hub.example.org", "--token-stdin").want(t, 0)
	srv := startServe(t, portZero(t, e.cfg))

	// Die erste Runde läuft nach dem Start im Hintergrund und bringt die drei
	// Account-Zeilen. Erst danach das Dokument: sonst kämen Account-Zeilen
	// und Dokument in einer Runde (4 Zeilen), und die Logzeile zu genau einer
	// Zeile bliebe aus.
	eventuallyLog(t, srv, "erste Runde von eigen und fern", func() bool {
		return syncStatus(t, ns, "eigen").OKAt != 0 && syncStatus(t, ns, "fern").OKAt != 0
	})
	e.runIn(t, "Inhalt", "hub", "doc", "put", "team-x", "a.md").want(t, 0)
	eventuallyLog(t, srv, "a.md in beiden Replicas", func() bool {
		return e.docIs(t, "eigen:team-x", "a.md", "Inhalt") && e.docIs(t, "fern:team-x", "a.md", "Inhalt")
	})
	eventuallyLog(t, srv, "Logzeilen", func() bool {
		log := srv.log.String()
		return contains(log, "Abgleich eigen: 1 Zeile, Revision", "Abgleich fern: 1 Zeile, Revision")
	})
	log := srv.log.String()
	if !contains(log, "Abgleich im Hintergrund alle 1s") {
		t.Errorf("Log:\n%s", log)
	}
	before := countLines(log, "Abgleich ")
	rounds(t, ns, "eigen", 2)
	rounds(t, ns, "fern", 2)
	if after := countLines(srv.log.String(), "Abgleich "); after != before {
		t.Errorf("Runden ohne Änderung loggen:\n%s", srv.log.String())
	}
	// ssh wird übergangen: eine Zeile beim Start, nicht je Runde, und kein
	// Stand.
	if n := countLines(srv.log.String(), "Abgleich extern: Transport ssh wird noch nicht unterstützt; übergangen"); n != 1 {
		t.Errorf("%d Zeilen zu extern:\n%s", n, srv.log.String())
	}
	if st := syncStatus(t, ns, "extern"); st != (nodestore.SyncStatus{}) {
		t.Errorf("hub_sync extern: %+v", st)
	}
	e.run(t, "node", "hub", "rm", "extern").want(t, 0)
	e.run(t, "status").want(t, 0, "serve:         läuft", "Abgleich:    zuletzt gelungen ")

	// node hub add während serve: ohne Neustart dabei.
	r := e.run(t, "hub", "node", "add", "laptop-2")
	r.want(t, 0)
	tok := tokenFrom(t, r.out)
	e.run(t, "hub", "node", "grant", "laptop-2", "team-x").want(t, 0)
	e.runIn(t, tok, "node", "hub", "add", "neu", "--node", "laptop-2", "--transport", "http", "--address", e.url,
		"--token-stdin").want(t, 0)
	e.run(t, "node", "collection", "add", "neu:team-x").want(t, 0)
	eventually(t, "a.md in der Replica von neu", func() bool { return e.docIs(t, "neu:team-x", "a.md", "Inhalt") })

	// node sync läuft daneben, auch mehrfach gleichzeitig mit dem
	// Hintergrund; danach stimmt die Replica.
	var wg sync.WaitGroup
	codes := make(chan result, 6)
	for i := range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if i%2 == 0 {
				e.runIn(t, "v"+string(rune('0'+i)), "hub", "doc", "put", "team-x", "b.md")
			}
			codes <- runIn(t, "", "node", "sync", e.c)
		}()
	}
	wg.Wait()
	close(codes)
	for r := range codes {
		if r.code != 0 {
			t.Errorf("node sync neben serve: %d\n%s%s", r.code, r.out, r.errOut)
		}
	}
	e.run(t, "node", "sync").want(t, 0)
	want := e.run(t, "hub", "doc", "get", "team-x", "b.md").out
	for _, alias := range []string{"eigen", "fern", "neu"} {
		if !e.docIs(t, alias+":team-x", "b.md", want) {
			t.Errorf("%s: b.md nicht %q", alias, want)
		}
	}
	if strings.Contains(srv.log.String(), "keph_") {
		t.Errorf("Token im Log:\n%s", srv.log.String())
	}
}

// Ein nicht erreichbarer Hub: Fehler in hub_sync und einmal im Log — der
// erste nach dem Start —, danach still, bis es wieder geht oder sich die Art
// des Fehlers ändert. Der andere Eintrag gleicht derweil weiter ab.
func TestBackgroundSyncErrors(t *testing.T) {
	slow(t, "wartet auf Runden des Abgleichs (sync_interval 1s)")
	e := newCommEnv(t)
	noRetry(t)
	ns := nodeStore(t, e.cfg)
	e.run(t, "config", "set", "node", "sync_interval", "1s").want(t, 0)
	e.run(t, "node", "hub", "set", "fern", "--address", closedAddress(t)).want(t, 0)
	srv := startServe(t, portZero(t, e.cfg))

	eventually(t, "Fehler in hub_sync", func() bool { return syncStatus(t, ns, "fern").ErrKind == "unreachable" })
	rounds(t, ns, "fern", 2)
	failed := func() int { return countLines(srv.log.String(), "Abgleich fern gescheitert") }
	if n := failed(); n != 1 {
		t.Errorf("%d Fehlerzeilen:\n%s", n, srv.log.String())
	}
	// eigen läuft derweil weiter.
	e.runIn(t, "x", "hub", "doc", "put", "team-x", "a.md").want(t, 0)
	eventually(t, "a.md in eigen", func() bool { return e.docIs(t, "eigen:team-x", "a.md", "x") })
	e.run(t, "status").want(t, 0, "; gescheitert ", "fern: http")

	// Wieder erreichbar.
	e.run(t, "node", "hub", "set", "fern", "--address", e.url).want(t, 0)
	eventuallyLog(t, srv, "fern geht wieder", func() bool {
		return strings.Contains(srv.log.String(), "Abgleich fern geht wieder") && syncStatus(t, ns, "fern").Err == ""
	})
	if !e.docIs(t, "fern:team-x", "a.md", "x") {
		t.Error("a.md fehlt in fern")
	}

	// Andere Art: gesperrt. Eine Zeile, dann still.
	e.run(t, "hub", "node", "lock", "laptop-http").want(t, 0)
	eventually(t, "gesperrt", func() bool { return syncStatus(t, ns, "fern").ErrKind == "unauthenticated" })
	rounds(t, ns, "fern", 2)
	if n := failed(); n != 2 {
		t.Errorf("%d Fehlerzeilen nach lock:\n%s", n, srv.log.String())
	}
	// Die Art wechselt ohne Erfolg dazwischen: wieder eine Zeile.
	e.run(t, "hub", "node", "unlock", "laptop-http").want(t, 0)
	e.run(t, "node", "hub", "set", "fern", "--address", closedAddress(t)).want(t, 0)
	eventually(t, "wieder nicht erreichbar", func() bool { return syncStatus(t, ns, "fern").ErrKind == "unreachable" })
	rounds(t, ns, "fern", 1)
	if n := failed(); n != 3 {
		t.Errorf("%d Fehlerzeilen nach Wechsel der Art:\n%s", n, srv.log.String())
	}
	if strings.Contains(srv.log.String(), "keph_") {
		t.Errorf("Token im Log:\n%s", srv.log.String())
	}
}

// sync_interval 0 schaltet den Abgleich im Hintergrund ab, auch beim Start;
// wieder an, gleicht serve ohne Neustart ab.
func TestBackgroundSyncOff(t *testing.T) {
	slow(t, "wartet 500 ms und auf die Runde nach dem Einschalten")
	e := newCommEnv(t)
	old := syncIdle
	syncIdle = 100 * time.Millisecond
	t.Cleanup(func() { syncIdle = old })
	e.run(t, "config", "set", "node", "sync_interval", "0").want(t, 0)
	e.runIn(t, "x", "hub", "doc", "put", "team-x", "a.md").want(t, 0)
	srv := startServe(t, portZero(t, e.cfg))
	eventuallyLog(t, srv, "Logzeile aus", func() bool {
		return strings.Contains(srv.log.String(), "Abgleich im Hintergrund aus (sync_interval 0)")
	})
	time.Sleep(500 * time.Millisecond)
	e.run(t, "node", "doc", "get", "eigen:team-x", "a.md").want(t, 1, "noch kein Abgleich")

	e.run(t, "config", "set", "node", "sync_interval", "1s").want(t, 0)
	eventually(t, "a.md nach dem Einschalten", func() bool { return e.docIs(t, "eigen:team-x", "a.md", "x") })
	if !strings.Contains(srv.log.String(), "Abgleich im Hintergrund alle 1s") {
		t.Errorf("Log:\n%s", srv.log.String())
	}
}

// heldHub hält den ersten Abgleich an, bis release geschlossen wird oder
// sein Kontext endet.
type heldHub struct {
	contract.Hub
	once    *sync.Once
	entered chan struct{}
	release chan struct{}
}

func (h heldHub) Sync(ctx context.Context, req contract.SyncRequest) (contract.SyncResponse, error) {
	first := false
	h.once.Do(func() { first = true })
	if first {
		close(h.entered)
		select {
		case <-h.release:
		case <-ctx.Done():
			return contract.SyncResponse{}, ctx.Err()
		}
	}
	return h.Hub.Sync(ctx, req)
}

// holdHTTP hält den ersten Abgleich über http an.
func holdHTTP(t *testing.T) heldHub {
	h := heldHub{once: &sync.Once{}, entered: make(chan struct{}), release: make(chan struct{})}
	hookHTTP(t, func(address string, rootCAs *x509.CertPool) (contract.Hub, error) {
		c, err := httpapi.NewClient(address, rootCAs)
		if err != nil {
			return nil, err
		}
		h := h
		h.Hub = c
		return h, nil
	})
	return h
}

// node hub rm und add unter demselben Alias, während serve den alten
// Eintrag abgleicht: Der alte schreibt nichts in den neuen, kein Fehler im
// Log; die nächste Runde gleicht den neuen ab.
func TestBackgroundSyncRemoveAddDuringSync(t *testing.T) {
	slow(t, "wartet auf die nächste Runde (sync_interval 1s)")
	e := newCommEnv(t)
	e.runIn(t, "x", "hub", "doc", "put", "team-x", "a.md").want(t, 0)
	ns := nodeStore(t, e.cfg)
	held := holdHTTP(t)
	e.run(t, "config", "set", "node", "sync_interval", "1s").want(t, 0)
	srv := startServe(t, portZero(t, e.cfg))
	select {
	case <-held.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("kein Abgleich über http")
	}
	old, err := ns.Hub(context.Background(), "fern")
	if err != nil {
		t.Fatal(err)
	}
	r := e.run(t, "hub", "node", "token", "laptop-http")
	r.want(t, 0)
	tok := tokenFrom(t, r.out)
	e.run(t, "node", "hub", "rm", "fern").want(t, 0)
	e.runIn(t, tok, "node", "hub", "add", "fern", "--node", "laptop-http", "--transport", "http", "--address", e.url,
		"--token-stdin").want(t, 0)
	e.run(t, "node", "collection", "add", "fern:team-x").want(t, 0)
	cur, err := ns.Hub(context.Background(), "fern")
	if err != nil || cur.EntryID == old.EntryID {
		t.Fatalf("neuer Eintrag: %+v, %v", cur, err)
	}
	close(held.release)
	eventually(t, "neuer Eintrag abgeglichen", func() bool {
		return syncStatus(t, ns, "fern").OKAt != 0 && e.docIs(t, "fern:team-x", "a.md", "x")
	})
	rep, err := replica.Open(context.Background(), ns.ReplicaPath("fern"))
	if err != nil {
		t.Fatal(err)
	}
	defer rep.Close()
	if rep.EntryID() != cur.EntryID {
		t.Errorf("Replica gehört zu %s, erwartet %s", rep.EntryID(), cur.EntryID)
	}
	if strings.Contains(srv.log.String(), "Abgleich fern gescheitert") {
		t.Errorf("Log:\n%s", srv.log.String())
	}
}

// Beenden während eines Abgleichs: serve endet sofort, der Abgleich bricht
// ab, und nichts wird als Fehler festgehalten.
func TestBackgroundSyncShutdown(t *testing.T) {
	e := newCommEnv(t)
	ns := nodeStore(t, e.cfg)
	held := holdHTTP(t)
	srv := startServe(t, portZero(t, e.cfg))
	select {
	case <-held.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("kein Abgleich über http")
	}
	start := time.Now()
	srv.stop(t)
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("serve endet erst nach %s", d)
	}
	if st := syncStatus(t, ns, "fern"); st != (nodestore.SyncStatus{}) {
		t.Errorf("hub_sync nach Abbruch: %+v", st)
	}
	if log := srv.log.String(); !strings.Contains(log, "beendet") || strings.Contains(log, "Abgleich fern gescheitert") {
		t.Errorf("Log:\n%s", log)
	}
}

// deafHub hält den ersten Abgleich an, bis release geschlossen wird, und
// beachtet den Abbruch nicht; danach antwortet es, als wäre nichts gewesen.
type deafHub struct {
	contract.Hub
	once    *sync.Once
	entered chan struct{}
	release chan struct{}
}

func (h deafHub) Sync(ctx context.Context, req contract.SyncRequest) (contract.SyncResponse, error) {
	first := false
	h.once.Do(func() { first = true })
	if first {
		close(h.entered)
		<-h.release
		return h.Hub.Sync(context.Background(), req)
	}
	return h.Hub.Sync(ctx, req)
}

// Ein Hub, der den Abbruch nicht beachtet, hält das Beenden von serve
// höchstens shutdownGrace auf. Der hängende Abgleich läuft danach weiter,
// schreibt aber nichts — weder in hub_sync noch eine Replica — und
// panict nicht.
func TestServeShutdownDeafHub(t *testing.T) {
	slow(t, "wartet die Frist shutdownGrace ab")
	e := newCommEnv(t)
	ns := nodeStore(t, e.cfg)
	deaf := deafHub{once: &sync.Once{}, entered: make(chan struct{}), release: make(chan struct{})}
	hookHTTP(t, func(address string, rootCAs *x509.CertPool) (contract.Hub, error) {
		c, err := httpapi.NewClient(address, rootCAs)
		if err != nil {
			return nil, err
		}
		h := deaf
		h.Hub = c
		return h, nil
	})
	oldGrace, oldBg := shutdownGrace, serveBackground
	shutdownGrace = 300 * time.Millisecond
	bgDone := make(chan (<-chan struct{}), 1)
	serveBackground = func(done <-chan struct{}) { bgDone <- done }
	t.Cleanup(func() { shutdownGrace, serveBackground = oldGrace, oldBg })

	srv := startServe(t, portZero(t, e.cfg))
	done := <-bgDone
	select {
	case <-deaf.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("kein Abgleich über http")
	}
	released := false
	defer func() {
		if !released {
			close(deaf.release)
		}
	}()
	start := time.Now()
	srv.stop(t)
	if d := time.Since(start); d > shutdownGrace+3*time.Second {
		t.Errorf("serve endet erst nach %s", d)
	}
	log := srv.log.String()
	if !contains(log, "Abgleich im Hintergrund endet nicht in der Frist 300ms; beende trotzdem", "beendet") {
		t.Errorf("Log:\n%s", log)
	}
	select {
	case <-done:
		t.Fatal("Abgleich im Hintergrund schon zu Ende, obwohl der Hub hängt")
	default:
	}

	// Den Hub freigeben und auf das Ende des Abgleichs warten: Er antwortet
	// jetzt, der Abgleich ist aber abgebrochen und schreibt nichts.
	released = true
	close(deaf.release)
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("Abgleich im Hintergrund endet nicht")
	}
	if st := syncStatus(t, ns, "fern"); st != (nodestore.SyncStatus{}) {
		t.Errorf("hub_sync nach Abbruch: %+v", st)
	}
	if _, err := os.Stat(ns.ReplicaPath("fern")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Replica von fern nach Abbruch: %v", err)
	}
	if strings.Contains(srv.log.String(), "Abgleich fern gescheitert") {
		t.Errorf("Log:\n%s", srv.log.String())
	}
}

// syncCounter zählt die Anfragen des Abgleichs.
type syncCounter struct {
	contract.Hub
	n *atomic.Int32
}

func (c syncCounter) Sync(ctx context.Context, req contract.SyncRequest) (contract.SyncResponse, error) {
	c.n.Add(1)
	return c.Hub.Sync(ctx, req)
}

// Der Anstoß nach einem Schreibvorgang gleicht einen Eintrag außer der Reihe
// ab, auch ohne Runden (sync_interval 0). Kommt er, während der Abgleich
// läuft, folgt genau einer — mehrere Anstöße fallen zusammen. ssh wird
// übergangen; nach dem Ende startet nichts mehr.
func TestBackgroundSyncKick(t *testing.T) {
	e := newCommEnv(t)
	var calls atomic.Int32
	h := holdHTTP(t)
	hookHTTP(t, func(address string, rootCAs *x509.CertPool) (contract.Hub, error) {
		c, err := httpapi.NewClient(address, rootCAs)
		if err != nil {
			return nil, err
		}
		held := h
		held.Hub = syncCounter{c, &calls}
		return held, nil
	})
	ns := nodeStore(t, e.cfg)
	cfg, _, err := config.Load(e.cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var log syncBuffer
	bg := newBackgroundSync(ctx, ns, cfg, nil, reqlog.New(&log))
	fern, err := ns.Hub(ctx, "fern")
	if err != nil {
		t.Fatal(err)
	}
	bg.kick(fern)
	<-h.entered
	bg.kick(fern)
	bg.kick(fern)
	close(h.release)
	eventually(t, "zwei Abgleiche von fern", func() bool {
		bg.mu.Lock()
		defer bg.mu.Unlock()
		return calls.Load() == 2 && !bg.running[fern.EntryID]
	})
	if st := syncStatus(t, ns, "fern"); st.OKAt == 0 {
		t.Errorf("hub_sync: %+v", st)
	}
	extern := fern
	extern.EntryID, extern.Transport = "01ANDERER", nodestore.TransportSSH
	bg.kick(extern)
	cancel()
	bg.kick(fern)
	bg.wg.Wait()
	if n := calls.Load(); n != 2 || len(bg.running) != 0 {
		t.Errorf("%d Abgleiche, laufend %v", n, bg.running)
	}
}

// Der Anstoß nach einem abgewiesenen Paar (recheck, Task 025) ist
// gedrosselt: Der erste gleicht sofort ab; weitere innerhalb der Frist
// fallen zu genau einem zusammen, der an ihrem Ende folgt; nach der Frist
// wieder sofort. ssh wird übergangen; nach dem Ende startet nichts mehr.
func TestBackgroundSyncRecheck(t *testing.T) {
	slow(t, "wartet die Frist der Drosselung ab (1 s)")
	e := newCommEnv(t)
	var calls atomic.Int32
	hookHTTP(t, func(address string, rootCAs *x509.CertPool) (contract.Hub, error) {
		c, err := httpapi.NewClient(address, rootCAs)
		if err != nil {
			return nil, err
		}
		return syncCounter{c, &calls}, nil
	})
	oldDelay := recheckDelay
	recheckDelay = time.Second
	t.Cleanup(func() { recheckDelay = oldDelay })
	ns := nodeStore(t, e.cfg)
	cfg, _, err := config.Load(e.cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var log syncBuffer
	bg := newBackgroundSync(ctx, ns, cfg, nil, reqlog.New(&log))
	fern, err := ns.Hub(ctx, "fern")
	if err != nil {
		t.Fatal(err)
	}
	idle := func() bool {
		bg.mu.Lock()
		defer bg.mu.Unlock()
		return !bg.running[fern.EntryID]
	}
	pending := func() bool {
		bg.mu.Lock()
		defer bg.mu.Unlock()
		return bg.rechecks[fern.EntryID].pending
	}
	start := time.Now()
	bg.recheck(fern)
	eventually(t, "erster Abgleich", func() bool { return calls.Load() == 1 && idle() })
	if time.Since(start) >= recheckDelay {
		t.Skip("der erste Abgleich dauerte länger als die Frist; die Drosselung lässt sich so nicht messen")
	}
	// Innerhalb der Frist: drei weitere stoßen nichts an, einer wartet.
	bg.recheck(fern)
	bg.recheck(fern)
	bg.recheck(fern)
	if n := calls.Load(); n != 1 || !pending() {
		t.Fatalf("innerhalb der Frist: %d Abgleiche, wartend %v", n, pending())
	}
	eventually(t, "nachgeholter Abgleich", func() bool { return calls.Load() == 2 && idle() })
	if d := time.Since(start); d < recheckDelay {
		t.Errorf("nachgeholt nach %s, vor dem Ende der Frist %s", d, recheckDelay)
	}
	if pending() {
		t.Error("nach dem Nachholen wartet noch einer")
	}
	// Danach nichts mehr: Die drei Anstöße waren genau einer.
	time.Sleep(recheckDelay + recheckDelay/2)
	if n := calls.Load(); n != 2 {
		t.Errorf("nach dem Nachholen %d Abgleiche, erwartet 2", n)
	}
	// Nach der Frist wieder sofort.
	bg.recheck(fern)
	eventually(t, "dritter Abgleich", func() bool { return calls.Load() == 3 && idle() })
	if st := syncStatus(t, ns, "fern"); st.OKAt == 0 {
		t.Errorf("hub_sync: %+v", st)
	}
	extern := fern
	extern.EntryID, extern.Transport = "01ANDERER", nodestore.TransportSSH
	bg.recheck(extern)
	cancel()
	bg.recheck(fern)
	bg.wg.Wait()
	if n := calls.Load(); n != 3 || len(bg.running) != 0 {
		t.Errorf("%d Abgleiche, laufend %v", n, bg.running)
	}
}

// waitEnv ist ein Abgleich im Hintergrund ohne Runden, für den wartenden
// Anstoß (kickWait, Task 042): der Eintrag fern über http, dessen Abgleiche
// calls zählt.
type waitEnv struct {
	e      *commEnv
	ns     nodestore.Store
	bg     *backgroundSync
	fern   nodestore.Hub
	calls  *atomic.Int32
	cancel context.CancelFunc
}

// newWaitEnv baut den Abgleich im Hintergrund über newCommEnv. wrap legt
// sich um den Client des Eintrags fern, nil lässt ihn, wie er ist.
func newWaitEnv(t *testing.T, wrap func(contract.Hub) contract.Hub) *waitEnv {
	t.Helper()
	w := &waitEnv{e: newCommEnv(t), calls: &atomic.Int32{}}
	hookHTTP(t, func(address string, rootCAs *x509.CertPool) (contract.Hub, error) {
		c, err := httpapi.NewClient(address, rootCAs)
		if err != nil {
			return nil, err
		}
		c.Retries = 0
		var hub contract.Hub = syncCounter{c, w.calls}
		if wrap != nil {
			hub = wrap(hub)
		}
		return hub, nil
	})
	w.ns = nodeStore(t, w.e.cfg)
	cfg, _, err := config.Load(w.e.cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	w.cancel = cancel
	var log syncBuffer
	w.bg = newBackgroundSync(ctx, w.ns, cfg, nil, reqlog.New(&log))
	t.Cleanup(func() {
		cancel()
		w.bg.wg.Wait()
	})
	if w.fern, err = w.ns.Hub(ctx, "fern"); err != nil {
		t.Fatal(err)
	}
	return w
}

// held hält den ersten Abgleich von fern an.
func heldWrap() (heldHub, func(contract.Hub) contract.Hub) {
	h := heldHub{once: &sync.Once{}, entered: make(chan struct{}), release: make(chan struct{})}
	return h, func(hub contract.Hub) contract.Hub {
		held := h
		held.Hub = hub
		return held
	}
}

// waitResult ist, was kickWait in einer Goroutine geliefert hat.
type waitResult struct {
	out mcpnode.SyncOutcome
	err error
}

// goWait ruft kickWait in einer Goroutine.
func (w *waitEnv) goWait(ctx context.Context, h nodestore.Hub) <-chan waitResult {
	ch := make(chan waitResult, 1)
	go func() {
		out, err := w.bg.kickWait(ctx, h)
		ch <- waitResult{out, err}
	}()
	return ch
}

// waiters sagt, wie viele auf den laufenden Abgleich von fern warten und wie
// viele auf seinen Nachfolger.
func (w *waitEnv) waiters() (current, next int) {
	w.bg.mu.Lock()
	defer w.bg.mu.Unlock()
	return len(w.bg.waiting[w.fern.EntryID]), len(w.bg.next[w.fern.EntryID])
}

func (w *waitEnv) idle() bool {
	w.bg.mu.Lock()
	defer w.bg.mu.Unlock()
	return !w.bg.running[w.fern.EntryID] && len(w.bg.waiting) == 0 && len(w.bg.next) == 0 && len(w.bg.again) == 0
}

func take(t *testing.T, what string, ch <-chan waitResult) waitResult {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(15 * time.Second):
		t.Fatalf("wartet vergeblich auf: %s", what)
	}
	return waitResult{}
}

// Der wartende Anstoß (Task 042), wenn kein Abgleich läuft: Er startet einen
// und liefert dessen Ergebnis — Erfolg mit dem Zeitpunkt aus hub_sync. Auch
// ohne Runden (sync_interval 0).
func TestBackgroundSyncWait(t *testing.T) {
	w := newWaitEnv(t, nil)
	w.e.run(t, "config", "set", "node", "sync_interval", "0").want(t, 0)
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.bg.run()
	}()
	out, err := w.bg.kickWait(context.Background(), w.fern)
	if err != nil || out.Kind != "" || out.Contract {
		t.Fatalf("kickWait: %+v, %v", out, err)
	}
	st := syncStatus(t, w.ns, "fern")
	if n := w.calls.Load(); n != 1 || out.At == 0 || out.At != st.OKAt || st.Err != "" {
		t.Errorf("%d Abgleiche, Ergebnis %+v, hub_sync %+v", n, out, st)
	}
	if !w.idle() {
		t.Error("nach dem Abgleich bleibt etwas zurück")
	}
	// Ein zweiter Aufruf gleicht wieder ab: Er liefert nie das Ergebnis eines
	// Abgleichs, der vor ihm begonnen hat.
	out2, err := w.bg.kickWait(context.Background(), w.fern)
	if err != nil || w.calls.Load() != 2 || out2.At < out.At {
		t.Errorf("zweiter Aufruf: %+v, %v, %d Abgleiche", out2, err, w.calls.Load())
	}
	// Ohne Runden: Der Stand von eigen bleibt leer.
	if st := syncStatus(t, w.ns, "eigen"); st != (nodestore.SyncStatus{}) {
		t.Errorf("sync_interval 0, hub_sync eigen: %+v", st)
	}
	w.cancel()
	<-done
	// Nach dem Ende: sofort der Fehler, kein Abgleich.
	if _, err := w.bg.kickWait(context.Background(), w.fern); !errors.Is(err, mcpnode.ErrSyncStopped) {
		t.Errorf("nach dem Ende: %v", err)
	}
	if n := w.calls.Load(); n != 2 {
		t.Errorf("%d Abgleiche nach dem Ende", n)
	}
}

// Läuft schon ein Abgleich, zählt für den Wartenden erst der Nachfolger; alle,
// die währenddessen kommen, teilen sich den einen.
func TestBackgroundSyncWaitSuccessor(t *testing.T) {
	held, wrap := heldWrap()
	w := newWaitEnv(t, wrap)
	w.bg.kick(w.fern)
	<-held.entered
	ctx := context.Background()
	a, b, c := w.goWait(ctx, w.fern), w.goWait(ctx, w.fern), w.goWait(ctx, w.fern)
	eventually(t, "drei warten auf den Nachfolger", func() bool {
		cur, next := w.waiters()
		return cur == 0 && next == 3
	})
	// Solange der Vorgänger läuft, antwortet keiner.
	select {
	case r := <-a:
		t.Fatalf("Antwort vor dem Nachfolger: %+v", r)
	case <-time.After(100 * time.Millisecond):
	}
	first := syncStatus(t, w.ns, "fern")
	if first.OKAt != 0 {
		t.Fatalf("hub_sync vor der Freigabe: %+v", first)
	}
	close(held.release)
	ra, rb, rc := take(t, "a", a), take(t, "b", b), take(t, "c", c)
	for _, r := range []waitResult{ra, rb, rc} {
		if r.err != nil || r.out.Kind != "" || r.out.At == 0 || r.out != ra.out {
			t.Errorf("Ergebnis %+v, %v; erwartet wie %+v", r.out, r.err, ra.out)
		}
	}
	eventually(t, "Ruhe", w.idle)
	// Vorgänger und genau ein Nachfolger; das Ergebnis ist das des
	// Nachfolgers.
	if n := w.calls.Load(); n != 2 {
		t.Errorf("%d Abgleiche, erwartet 2", n)
	}
	if st := syncStatus(t, w.ns, "fern"); ra.out.At != st.OKAt {
		t.Errorf("Ergebnis %d, hub_sync %+v", ra.out.At, st)
	}
}

// failingHub lässt jeden Abgleich mit dem Fehler scheitern, den err gerade
// hält.
type failingHub struct {
	contract.Hub
	err *atomic.Pointer[error]
}

func (f failingHub) Sync(context.Context, contract.SyncRequest) (contract.SyncResponse, error) {
	return contract.SyncResponse{}, *f.err.Load()
}

// Ein gescheiterter Abgleich ist nie ein Erfolg: Der wartende Anstoß liefert
// die Art und, für die Art hub, ob der Hub mit einem Fehler des Vertrags
// abgelehnt hat — nicht den Fehlertext.
func TestBackgroundSyncWaitFailure(t *testing.T) {
	var fail atomic.Pointer[error]
	w := newWaitEnv(t, func(hub contract.Hub) contract.Hub { return failingHub{hub, &fail} })
	cases := []struct {
		name     string
		err      error
		kind     replica.ErrorKind
		contract bool
	}{
		{"Fehler des Vertrags", &contract.Error{Code: contract.CodeInvalid, Message: "abgelehnt"}, replica.KindHub, true},
		{"ohne Fehler des Vertrags", errors.New("502 Bad Gateway von http://hub.example.org"), replica.KindHub, false},
		{"Node nicht angenommen", contract.ErrUnauthenticated, replica.KindUnauthenticated, true},
		{"Fassung", contract.ErrUnsupportedVersion, replica.KindVersion, true},
		{"nicht erreicht", &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")},
			replica.KindUnreachable, false},
	}
	for _, c := range cases {
		fail.Store(&c.err)
		out, err := w.bg.kickWait(context.Background(), w.fern)
		if err != nil || out.Kind != c.kind || out.Contract != c.contract || out.At != 0 {
			t.Errorf("%s: kickWait: %+v, %v", c.name, out, err)
		}
		if st := syncStatus(t, w.ns, "fern"); st.ErrKind != string(c.kind) || st.OKAt != 0 {
			t.Errorf("%s: hub_sync: %+v", c.name, st)
		}
	}
	if n := w.calls.Load(); n != 0 {
		// failingHub liegt über dem Zähler: Der echte Hub wurde nie gefragt.
		t.Errorf("%d Anfragen am echten Hub", n)
	}
}

// Bricht der Aufrufer ab oder läuft seine Frist ab, endet nur das Warten:
// Der Abgleich läuft weiter und hält sein Ergebnis fest.
func TestBackgroundSyncWaitCallerGone(t *testing.T) {
	held, wrap := heldWrap()
	w := newWaitEnv(t, wrap)
	ctx, cancel := context.WithCancel(context.Background())
	ch := w.goWait(ctx, w.fern)
	<-held.entered
	cancel()
	if r := take(t, "abgebrochener Aufruf", ch); !errors.Is(r.err, context.Canceled) {
		t.Fatalf("abgebrochen: %+v, %v", r.out, r.err)
	}
	// Ein zweiter mit Frist wartet auf den Nachfolger und gibt ebenso auf.
	tctx, tcancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer tcancel()
	if _, err := w.bg.kickWait(tctx, w.fern); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Frist: %v", err)
	}
	if st := syncStatus(t, w.ns, "fern"); st != (nodestore.SyncStatus{}) {
		t.Fatalf("hub_sync vor der Freigabe: %+v", st)
	}
	close(held.release)
	eventually(t, "der Abgleich läuft weiter, samt Nachfolger", func() bool {
		return w.idle() && w.calls.Load() == 2 && syncStatus(t, w.ns, "fern").OKAt != 0
	})
}

// Endet serve während des Wartens, erfährt es jeder Wartende sofort — der auf
// den laufenden Abgleich wartet wie der auf den Nachfolger, auch wenn der Hub
// den Abbruch nicht beachtet.
func TestBackgroundSyncWaitServeEnds(t *testing.T) {
	deaf := deafHub{once: &sync.Once{}, entered: make(chan struct{}), release: make(chan struct{})}
	w := newWaitEnv(t, func(hub contract.Hub) contract.Hub {
		d := deaf
		d.Hub = hub
		return d
	})
	defer close(deaf.release)
	ctx := context.Background()
	a := w.goWait(ctx, w.fern)
	<-deaf.entered
	b := w.goWait(ctx, w.fern)
	eventually(t, "einer wartet auf den Abgleich, einer auf den Nachfolger", func() bool {
		cur, next := w.waiters()
		return cur == 1 && next == 1
	})
	start := time.Now()
	w.cancel()
	for name, ch := range map[string]<-chan waitResult{"laufend": a, "Nachfolger": b} {
		if r := take(t, name, ch); !errors.Is(r.err, mcpnode.ErrSyncStopped) {
			t.Errorf("%s: %+v, %v", name, r.out, r.err)
		}
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("die Wartenden erfahren das Ende erst nach %s", d)
	}
}

// Wird der Eintrag entfernt, während der Vorgänger läuft, startet der
// Nachfolger nicht: Wer auf ihn wartet, bekommt sofort den Fehler.
func TestBackgroundSyncWaitGoneBeforeSuccessor(t *testing.T) {
	held, wrap := heldWrap()
	w := newWaitEnv(t, wrap)
	w.bg.kick(w.fern)
	<-held.entered
	ch := w.goWait(context.Background(), w.fern)
	eventually(t, "einer wartet auf den Nachfolger", func() bool {
		_, next := w.waiters()
		return next == 1
	})
	w.e.run(t, "node", "hub", "rm", "fern").want(t, 0)
	close(held.release)
	if r := take(t, "Wartender", ch); !errors.Is(r.err, mcpnode.ErrSyncGone) || r.out != (mcpnode.SyncOutcome{}) {
		t.Errorf("Ergebnis %+v, %v", r.out, r.err)
	}
	eventually(t, "Ruhe", w.idle)
	if n := w.calls.Load(); n != 1 {
		t.Errorf("%d Abgleiche: der Nachfolger darf nicht starten", n)
	}
}

// Wird der Eintrag während des abgewarteten Abgleichs entfernt, ist das kein
// Erfolg — auch wenn der Abgleich ohne Fehler zurückkommt.
func TestBackgroundSyncWaitGoneDuringSync(t *testing.T) {
	held, wrap := heldWrap()
	w := newWaitEnv(t, wrap)
	ch := w.goWait(context.Background(), w.fern)
	<-held.entered
	w.e.run(t, "node", "hub", "rm", "fern").want(t, 0)
	close(held.release)
	if r := take(t, "Wartender", ch); !errors.Is(r.err, mcpnode.ErrSyncGone) || r.out != (mcpnode.SyncOutcome{}) {
		t.Errorf("Ergebnis %+v, %v", r.out, r.err)
	}
	eventually(t, "Ruhe", w.idle)
}

// Unter demselben Alias neu angelegt: Der Aufrufer kennt noch den alten
// Eintrag; sein Abgleich endet ohne Ergebnis und schreibt nichts in den Stand
// des neuen.
func TestBackgroundSyncWaitEntryReplaced(t *testing.T) {
	w := newWaitEnv(t, nil)
	r := w.e.run(t, "hub", "node", "token", "laptop-http")
	r.want(t, 0)
	tok := tokenFrom(t, r.out)
	w.e.run(t, "node", "hub", "rm", "fern").want(t, 0)
	w.e.runIn(t, tok, "node", "hub", "add", "fern", "--node", "laptop-http", "--transport", "http", "--address",
		w.e.url, "--token-stdin").want(t, 0)
	if out, err := w.bg.kickWait(context.Background(), w.fern); !errors.Is(err, mcpnode.ErrSyncGone) {
		t.Errorf("alter Eintrag: %+v, %v", out, err)
	}
	if st := syncStatus(t, w.ns, "fern"); st != (nodestore.SyncStatus{}) {
		t.Errorf("hub_sync des neuen Eintrags: %+v", st)
	}
}

// Ein Eintrag mit ssh: sofort der Fehler, kein Abgleich startet.
func TestBackgroundSyncWaitSSH(t *testing.T) {
	w := newWaitEnv(t, nil)
	extern := w.fern
	extern.Transport = nodestore.TransportSSH
	out, err := w.bg.kickWait(context.Background(), extern)
	if !errors.Is(err, mcpnode.ErrUnsupported) || out != (mcpnode.SyncOutcome{}) {
		t.Errorf("ssh: %+v, %v", out, err)
	}
	if n := w.calls.Load(); n != 0 || !w.idle() {
		t.Errorf("ssh: %d Abgleiche", n)
	}
}
