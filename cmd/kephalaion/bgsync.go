package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/kephalaion/kephalaion/internal/config"
	"github.com/kephalaion/kephalaion/internal/contract"
	hubstore "github.com/kephalaion/kephalaion/internal/hub/store"
	"github.com/kephalaion/kephalaion/internal/node/mcpnode"
	"github.com/kephalaion/kephalaion/internal/node/replica"
	nodestore "github.com/kephalaion/kephalaion/internal/node/store"
	"github.com/kephalaion/kephalaion/internal/reqlog"
)

// syncIdle ist, wie oft der Abgleich im Hintergrund bei sync_interval 0
// nachsieht, ob er wieder an ist. Tests stellen ihn kürzer.
var syncIdle = nodestore.DefaultSyncInterval

// recheckDelay ist die Frist der Drosselung von recheck je Hub-Eintrag:
// höchstens ein Anstoß je Frist; ein weiterer innerhalb der Frist wird an
// ihrem Ende nachgeholt, genau einer. Tests stellen sie kürzer.
var recheckDelay = 3 * time.Second

// backgroundSync ist der Abgleich im Hintergrund von serve (Rolle node): beim
// Start je Hub-Eintrag ein Abgleich, danach je Abstand (sync_interval, je
// Runde gelesen). Die Einträge liest jede Runde neu aus node.db, node hub
// add|rm wirkt also ohne Neustart. Jeder Eintrag gleicht in seiner eigenen
// Goroutine ab: Ein langsamer oder hängender Hub hält die anderen nicht auf;
// läuft sein Abgleich noch, übergeht ihn die nächste Runde. ssh wird
// übergangen, mit einer Logzeile, wenn der Eintrag zum ersten Mal auftaucht.
//
// Die Umsetzung des Vertrags wählt wie bei node sync der connector; local
// bekommt den Hub-Store desselben serve, wenn es ihn gibt.
//
// Außer der Reihe gleicht ein Eintrag ab, wenn ein Werkzeug über MCP
// geschrieben hat (kick) — auch bei sync_interval 0: Die Replica soll die
// eigene Änderung vom Hub bestätigt bekommen, und nach unklarem Ausgang sieht
// der Aufrufer erst danach, ob gespeichert ist. Läuft der Abgleich des
// Eintrags schon, folgt ihm ein weiterer: Der laufende hat den Schreibvorgang
// vielleicht nicht mehr gesehen. Ebenso, gedrosselt, wenn der Node über den
// Proxy ein Paar zu einem bekannten Account abgewiesen hat (recheck): Nach
// rotate an einem anderen Node gilt das neue Token so schon vor der
// nächsten Runde.
//
// Auf Anforderung (kickWait, Task 042) stößt das Werkzeug sync den Abgleich
// eines Eintrags an und wartet auf das Ergebnis des ersten, der nach seinem
// Aufruf begonnen hat: des gestarteten, oder — läuft schon einer — seines
// Nachfolgers, den sich alle teilen, die währenddessen kommen. Auch bei
// sync_interval 0, ungedrosselt: Angestoßen wird erst nach gültiger
// Anmeldung.
//
// Log: keine Zeile für einen Abgleich ohne Änderung; eine, wenn Zeilen kamen
// (Hub, Anzahl, Revision). Ein Fehler beim ersten Fehlschlag je Eintrag nach
// dem Start, beim Übergang von Erfolg zu Fehler und wenn sich die Art des
// Fehlers ändert, sonst still; eine Zeile, wenn es danach wieder geht. Nie
// ein Token — die Fehler nennen keins.
type backgroundSync struct {
	// ctx ist der Kontext von run; endet er, beginnt kein Abgleich mehr.
	ctx   context.Context
	nodes nodestore.Store
	cfg   config.Config
	// hub ist der Store des Hubs, wenn derselbe serve ihn trägt, sonst nil.
	hub hubstore.Store
	log *reqlog.Logger

	mu sync.Mutex
	// running hält die Einträge, deren Abgleich läuft, nach entry_id.
	running map[string]bool
	// again hält die Einträge, die angestoßen wurden, während ihr Abgleich
	// lief, nach entry_id: Nach dem laufenden gleichen sie noch einmal ab.
	again map[string]nodestore.Hub
	// waiting hält je Eintrag, dessen Abgleich läuft, die Wartenden, die
	// sein Ergebnis bekommen (kickWait): Er hat nach ihrem Aufruf begonnen.
	// next hält die, die kamen, während er lief; sie warten auf den
	// Nachfolger aus again. Beide nach entry_id.
	waiting map[string][]chan syncReport
	next    map[string][]chan syncReport
	// rechecks hält die Drosselung von recheck je Eintrag, nach entry_id.
	rechecks map[string]*recheckState
	// stopped: run endet; danach startet nichts mehr, damit wg.Wait nicht
	// neben einem wg.Add läuft.
	stopped bool
	// outcome ist das letzte Ergebnis je Eintrag, nach entry_id; fehlt es,
	// gab es seit dem Start noch keins.
	outcome map[string]syncOutcome
	// skipped hält die Einträge mit ssh, die schon gemeldet sind.
	skipped map[string]bool
	// interval ist der zuletzt gemeldete Abstand, badInterval der zuletzt
	// gemeldete ungültige Wert; gemeldet wird nur eine Änderung.
	interval    time.Duration
	reported    bool
	badInterval string
	wg          sync.WaitGroup
}

type syncOutcome struct {
	failed bool
	kind   replica.ErrorKind
}

// syncReport ist, was ein Wartender (kickWait) vom Abgleich bekommt, von dem
// er abhängt: dessen Ergebnis, oder err, wenn es keins gibt —
// mcpnode.ErrSyncGone (der Eintrag wurde entfernt oder neu angelegt) oder
// mcpnode.ErrSyncStopped (serve endet).
type syncReport struct {
	out mcpnode.SyncOutcome
	err error
}

// wake gibt allen Wartenden einer Liste dasselbe Ergebnis. Jeder Kanal hat
// Platz für eins und bekommt genau eins: Es blockiert nie, auch wenn der
// Wartende schon gegangen ist.
func wake(waiters []chan syncReport, rep syncReport) {
	for _, ch := range waiters {
		ch <- rep
	}
}

// recheckState ist die Drosselung von recheck für einen Eintrag.
type recheckState struct {
	// last ist, wann der Eintrag zuletzt über recheck angestoßen wurde.
	last time.Time
	// pending: ein Anstoß wartet auf das Ende der Frist.
	pending bool
}

// newBackgroundSync bereitet den Abgleich im Hintergrund vor; er gilt, bis
// ctx endet. Angestoßen werden kann er gleich, die Runden beginnen mit run.
func newBackgroundSync(ctx context.Context, nodes nodestore.Store, cfg config.Config, hub hubstore.Store,
	log *reqlog.Logger) *backgroundSync {
	return &backgroundSync{ctx: ctx, nodes: nodes, cfg: cfg, hub: hub, log: log, running: map[string]bool{},
		again: map[string]nodestore.Hub{}, waiting: map[string][]chan syncReport{},
		next: map[string][]chan syncReport{}, rechecks: map[string]*recheckState{},
		outcome: map[string]syncOutcome{}, skipped: map[string]bool{}}
}

// run läuft, bis der ctx des Abgleichs endet; dann bricht es laufende
// Abgleiche ab — jede Seite ist eine Transaktion — und wartet auf sie.
func (b *backgroundSync) run() {
	ctx := b.ctx
	defer func() {
		b.mu.Lock()
		b.stopped = true
		b.mu.Unlock()
		b.wg.Wait()
	}()
	for {
		wait := syncIdle
		if d := b.readInterval(ctx); d > 0 {
			b.round(ctx)
			wait = d
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
	}
}

// readInterval liest sync_interval. Ein ungültiger Wert (etwa aus einem
// Import) gilt als Standard und wird einmal gemeldet; eine Änderung des
// Abstands ebenso.
func (b *backgroundSync) readInterval(ctx context.Context) time.Duration {
	settings, err := b.nodes.Settings(ctx)
	if err != nil {
		if ctx.Err() == nil {
			b.log.Printf("Abgleich: settings nicht lesbar: %v", err)
		}
		return nodestore.DefaultSyncInterval
	}
	d, err := nodestore.SyncInterval(settings)
	if err != nil {
		if v := settings[nodestore.SettingSyncInterval]; v != b.badInterval {
			b.badInterval = v
			b.log.Printf("Abgleich: %v; es gilt %s", err, d)
		}
	} else {
		b.badInterval = ""
	}
	if !b.reported || d != b.interval {
		b.reported, b.interval = true, d
		if d == 0 {
			b.log.Printf("Abgleich im Hintergrund aus (sync_interval 0)")
		} else {
			b.log.Printf("Abgleich im Hintergrund alle %s", d)
		}
	}
	return d
}

// round startet den Abgleich jedes Eintrags, der nicht schon läuft.
func (b *backgroundSync) round(ctx context.Context) {
	hubs, err := b.nodes.Hubs(ctx)
	if err != nil {
		if ctx.Err() == nil {
			b.log.Printf("Abgleich: Hub-Einträge nicht lesbar: %v", err)
		}
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, h := range hubs {
		if h.Transport == nodestore.TransportSSH {
			if !b.skipped[h.EntryID] {
				b.skipped[h.EntryID] = true
				b.log.Printf("Abgleich %s: Transport %s wird noch nicht unterstützt; übergangen", h.Name, h.Transport)
			}
			continue
		}
		if b.running[h.EntryID] {
			continue
		}
		b.start(h)
	}
}

// kick stößt den Abgleich eines Eintrags an, ohne zu warten — nach einem
// Schreibvorgang über MCP. Läuft er schon, folgt ihm einer. ssh übergeht er
// wie die Runden.
func (b *backgroundSync) kick(h nodestore.Hub) {
	if h.Transport == nodestore.TransportSSH {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.kickLocked(h)
}

// kickLocked ist kick mit gehaltenem b.mu.
func (b *backgroundSync) kickLocked(h nodestore.Hub) {
	switch {
	case b.stopped || b.ctx.Err() != nil:
	case b.running[h.EntryID]:
		b.again[h.EntryID] = h
	default:
		b.start(h)
	}
}

// kickWait stößt den Abgleich eines Eintrags an und wartet auf das Ergebnis
// des ersten Abgleichs, der nach diesem Aufruf begonnen hat (Task 042, das
// Werkzeug sync): Läuft keiner, startet einer; läuft schon einer, gilt sein
// Nachfolger aus again — der laufende kann verpasst haben, worum es dem
// Aufrufer geht. Alle, die während desselben Abgleichs kommen, teilen sich
// den einen Nachfolger.
//
// Das Ergebnis kommt aus syncOne: Erfolg mit dem Zeitpunkt aus hub_sync oder
// gescheitert mit Art. Ein Fehler heißt, dass es kein Ergebnis gibt:
// mcpnode.ErrUnsupported für ssh und mcpnode.ErrSyncStopped nach dem Ende von
// serve, beide sofort und ohne Abgleich; mcpnode.ErrSyncGone, wenn der Eintrag
// währenddessen entfernt oder neu angelegt wurde, und mcpnode.ErrSyncStopped,
// wenn serve währenddessen endet — auch für den, der auf einen Nachfolger
// wartet, der deshalb nicht startet; sonst der Fehler von ctx, wenn der
// Aufrufer abbricht oder seine Frist abläuft. Dann läuft der Abgleich weiter.
// Ungedrosselt, auch bei sync_interval 0.
func (b *backgroundSync) kickWait(ctx context.Context, h nodestore.Hub) (mcpnode.SyncOutcome, error) {
	if h.Transport == nodestore.TransportSSH {
		return mcpnode.SyncOutcome{}, fmt.Errorf("Transport %s wird %w", h.Transport, mcpnode.ErrUnsupported)
	}
	ch := make(chan syncReport, 1)
	b.mu.Lock()
	switch {
	case b.stopped || b.ctx.Err() != nil:
		b.mu.Unlock()
		return mcpnode.SyncOutcome{}, mcpnode.ErrSyncStopped
	case b.running[h.EntryID]:
		b.again[h.EntryID] = h
		b.next[h.EntryID] = append(b.next[h.EntryID], ch)
	default:
		b.waiting[h.EntryID] = append(b.waiting[h.EntryID], ch)
		b.start(h)
	}
	b.mu.Unlock()
	var err error
	select {
	case rep := <-ch:
		return rep.out, rep.err
	case <-ctx.Done():
		err = ctx.Err()
	case <-b.ctx.Done():
		// Auch wenn der Hub den Abbruch nicht beachtet und der Abgleich
		// hängt: Wer wartet, erfährt das Ende von serve sofort.
		err = mcpnode.ErrSyncStopped
	}
	// Ein Ergebnis, das zugleich bereitliegt, geht vor.
	select {
	case rep := <-ch:
		return rep.out, rep.err
	default:
	}
	return mcpnode.SyncOutcome{}, err
}

// recheck stößt den Abgleich eines Eintrags an, weil ein Client über den
// Proxy mit einem Paar abgewiesen wurde, dessen Account die Replica kennt —
// nach node account rotate an einem anderen Node ist ihre Zeile bis zum
// nächsten Abgleich veraltet (Task 025, mcpnode.HubLink.Recheck). Gedrosselt,
// damit Fehlversuche den Hub nicht in Takt bringen: höchstens ein Anstoß je
// recheckDelay je Eintrag; ein Anstoß innerhalb der Frist fällt nicht weg,
// sondern folgt an ihrem Ende, genau einer. Wartet nicht; ssh wird
// übergangen wie bei kick. Nach dem Ende startet nichts mehr.
func (b *backgroundSync) recheck(h nodestore.Hub) {
	if h.Transport == nodestore.TransportSSH {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.stopped || b.ctx.Err() != nil {
		return
	}
	st := b.rechecks[h.EntryID]
	if st == nil {
		st = &recheckState{}
		b.rechecks[h.EntryID] = st
	}
	now := time.Now()
	if since := now.Sub(st.last); st.last.IsZero() || since >= recheckDelay {
		st.last = now
		b.kickLocked(h)
		return
	}
	if st.pending {
		return
	}
	st.pending = true
	time.AfterFunc(recheckDelay-now.Sub(st.last), func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		st.pending = false
		if b.stopped || b.ctx.Err() != nil {
			return
		}
		st.last = time.Now()
		b.kickLocked(h)
	})
}

// start gleicht einen Eintrag in einer eigenen Goroutine ab; b.mu ist
// gehalten.
func (b *backgroundSync) start(h nodestore.Hub) {
	if b.stopped {
		return
	}
	b.running[h.EntryID] = true
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		b.syncOne(h)
	}()
}

// syncOne gleicht einen Eintrag ab und meldet das Ergebnis — dem Log und den
// Wartenden dieses Laufs (kickWait). Wurde er angestoßen, während er lief,
// gleicht er danach noch einmal ab; wer auf diesen Nachfolger wartet, hängt
// dann an ihm. Startet der Nachfolger nicht (ctx beendet, Eintrag entfernt
// oder neu angelegt, serve endet), bekommen seine Wartenden sofort den
// Fehler. Ein Erfolg ist nur, was die Prüfungen auf ctx und Gone hinter sich
// hat und keinen Fehler trägt: SyncEntry hält sonst nichts in hub_sync fest
// und kann trotzdem ohne Fehler zurückkommen.
func (b *backgroundSync) syncOne(h nodestore.Hub) {
	ctx := b.ctx
	conn := &connector{ctx: ctx, cfg: b.cfg, hub: b.hub}
	defer conn.close()
	res := (&replica.Syncer{Nodes: b.nodes}).SyncEntry(ctx, h, conn.connect)

	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.running, h.EntryID)
	waiters := b.waiting[h.EntryID]
	delete(b.waiting, h.EntryID)
	successors := b.next[h.EntryID]
	delete(b.next, h.EntryID)
	// none ist, was Wartende ohne Ergebnis erfahren: warum dieser Lauf keins
	// hat und kein Nachfolger startet. nil heißt: Es gibt ein Ergebnis.
	var none error
	switch {
	case ctx.Err() != nil || b.stopped:
		none = mcpnode.ErrSyncStopped
	case res.Gone:
		none = mcpnode.ErrSyncGone
	}
	if next, ok := b.again[h.EntryID]; ok {
		delete(b.again, h.EntryID)
		if none == nil {
			// b.mu bleibt bis zum Start gehalten: stopped ändert sich
			// dazwischen nicht, der Nachfolger startet also.
			b.waiting[h.EntryID] = successors
			successors = nil
			defer b.start(next)
		}
	}
	if none != nil {
		wake(successors, syncReport{err: none})
	}
	if ctx.Err() != nil {
		wake(waiters, syncReport{err: mcpnode.ErrSyncStopped})
		return
	}
	if res.Gone {
		delete(b.outcome, h.EntryID)
		wake(waiters, syncReport{err: mcpnode.ErrSyncGone})
		return
	}
	prev, seen := b.outcome[h.EntryID]
	if res.Err != nil {
		if !seen || !prev.failed || prev.kind != res.Kind {
			b.log.Printf("Abgleich %s gescheitert: %v", h.Name, res.Err)
		}
		b.outcome[h.EntryID] = syncOutcome{failed: true, kind: res.Kind}
		// Den Wartenden nur Art und Einordnung, nie res.Err: Sein Text kann
		// die Adresse des Hubs nennen.
		var ce *contract.Error
		wake(waiters, syncReport{out: mcpnode.SyncOutcome{Kind: res.Kind, Contract: errors.As(res.Err, &ce)}})
		return
	}
	b.outcome[h.EntryID] = syncOutcome{}
	wake(waiters, syncReport{out: mcpnode.SyncOutcome{At: res.RecordedAt}})
	if seen && prev.failed {
		b.log.Printf("Abgleich %s geht wieder", h.Name)
	}
	if res.Reset != "" {
		b.log.Printf("Abgleich %s: %s", h.Name, res.Reset)
	}
	rows, rev, have := 0, int64(0), false
	for _, cr := range res.Collections {
		if cr.Status != replica.Synced {
			continue
		}
		rows += cr.Rows
		if !have || cr.Revision < rev {
			rev, have = cr.Revision, true
		}
	}
	if rows > 0 {
		b.log.Printf("Abgleich %s: %s, Revision %d", h.Name, rowsText(int64(rows)), rev)
	}
}
