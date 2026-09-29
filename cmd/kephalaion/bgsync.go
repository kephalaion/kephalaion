package main

import (
	"context"
	"sync"
	"time"

	"github.com/kephalaion/kephalaion/internal/config"
	hubstore "github.com/kephalaion/kephalaion/internal/hub/store"
	"github.com/kephalaion/kephalaion/internal/node/replica"
	nodestore "github.com/kephalaion/kephalaion/internal/node/store"
	"github.com/kephalaion/kephalaion/internal/reqlog"
)

// syncIdle ist, wie oft der Abgleich im Hintergrund bei sync_interval 0
// nachsieht, ob er wieder an ist. Tests stellen ihn kürzer.
var syncIdle = nodestore.DefaultSyncInterval

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
// vielleicht nicht mehr gesehen.
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

// newBackgroundSync bereitet den Abgleich im Hintergrund vor; er gilt, bis
// ctx endet. Angestoßen werden kann er gleich, die Runden beginnen mit run.
func newBackgroundSync(ctx context.Context, nodes nodestore.Store, cfg config.Config, hub hubstore.Store,
	log *reqlog.Logger) *backgroundSync {
	return &backgroundSync{ctx: ctx, nodes: nodes, cfg: cfg, hub: hub, log: log, running: map[string]bool{},
		again: map[string]nodestore.Hub{}, outcome: map[string]syncOutcome{}, skipped: map[string]bool{}}
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
	switch {
	case b.stopped || b.ctx.Err() != nil:
	case b.running[h.EntryID]:
		b.again[h.EntryID] = h
	default:
		b.start(h)
	}
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

// syncOne gleicht einen Eintrag ab und meldet das Ergebnis. Wurde er
// angestoßen, während er lief, gleicht er danach noch einmal ab.
func (b *backgroundSync) syncOne(h nodestore.Hub) {
	ctx := b.ctx
	conn := &connector{ctx: ctx, cfg: b.cfg, hub: b.hub}
	defer conn.close()
	res := (&replica.Syncer{Nodes: b.nodes}).SyncEntry(ctx, h, conn.connect)

	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.running, h.EntryID)
	if next, ok := b.again[h.EntryID]; ok {
		delete(b.again, h.EntryID)
		if ctx.Err() == nil && !res.Gone {
			defer b.start(next)
		}
	}
	if ctx.Err() != nil {
		return
	}
	if res.Gone {
		delete(b.outcome, h.EntryID)
		return
	}
	prev, seen := b.outcome[h.EntryID]
	if res.Err != nil {
		if !seen || !prev.failed || prev.kind != res.Kind {
			b.log.Printf("Abgleich %s gescheitert: %v", h.Name, res.Err)
		}
		b.outcome[h.EntryID] = syncOutcome{failed: true, kind: res.Kind}
		return
	}
	b.outcome[h.EntryID] = syncOutcome{}
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
