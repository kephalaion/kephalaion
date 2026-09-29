package replica

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"slices"

	"github.com/oklog/ulid/v2"

	"github.com/kephalaion/kephalaion/internal/contract"
	"github.com/kephalaion/kephalaion/internal/node/store"
	"github.com/kephalaion/kephalaion/internal/sqlitedb"
)

// Connect liefert zu einem Hub-Eintrag die Umsetzung des Vertrags, über die
// der Node ihn erreicht — bei local den Hub der eigenen config. Ein Fehler
// (Transport noch nicht unterstützt, kein Hub in der config) betrifft nur
// diesen Eintrag; die übrigen laufen weiter. Die Umsetzung wählt
// cmd/kephalaion: Der Node kennt den Hub nur über den Vertrag.
type Connect func(h store.Hub) (contract.Hub, error)

// Syncer gleicht die Replicas eines Nodes ab.
type Syncer struct {
	// Nodes ist die Datenbank des Nodes: Hub-Einträge, gewünschte
	// Collections, Ort der Replicas.
	Nodes store.Store
	// PageSize ist die Seitengröße der Anfragen; 0 heißt
	// contract.DefaultPageSize. Keine Nutzeroption, Tests stellen sie
	// kleiner.
	PageSize int
	// now liefert die Zeit für synced_at; nil heißt sqlitedb.NowMillis.
	now func() int64
}

// Status sagt, was der Abgleich mit einer Collection getan hat.
type Status int

// Mögliche Ergebnisse je Collection.
const (
	// Synced: abgeglichen; Rows Zeilen kamen an, der Stand ist Revision.
	Synced Status = iota
	// NotAllowed: der Hub erlaubt sie nicht (mehr), oder er kennt sie
	// nicht; aus der Replica entfernt.
	NotAllowed
	// NotWanted: der Node will sie nicht mehr; aus der Replica entfernt.
	NotWanted
)

func (s Status) String() string {
	switch s {
	case NotAllowed:
		return "nicht erlaubt"
	case NotWanted:
		return "nicht mehr gewünscht"
	}
	return "abgeglichen"
}

// CollectionResult ist das Ergebnis des Abgleichs für eine Collection.
type CollectionResult struct {
	Collection string
	Status     Status
	// Rows zählt die angekommenen Zeilen (Synced), Löschmarken und
	// SYSTEM:-Zeilen eingeschlossen.
	Rows int
	// Revision ist der Stand nach dem Abgleich (Synced).
	Revision int64
	// Removed zählt die Zeilen, die aus der Replica entfernt wurden
	// (NotAllowed, NotWanted).
	Removed int64
}

// HubResult ist das Ergebnis des Abgleichs für einen Hub-Eintrag. Err ist
// gesetzt, wenn der Abgleich dieses Eintrags scheiterte; was bis dahin
// ankam, ist geschrieben und steht in Collections. Fehler des Vertrags
// (contract.ErrUnauthenticated usw.) lassen sich mit errors.Is erkennen.
type HubResult struct {
	// Hub ist der Alias des Eintrags.
	Hub string
	// HubID ist die hub_id des Hubs, sobald er geantwortet hat.
	HubID string
	// Reset nennt den Grund, wenn die Replica geleert und von vorn
	// abgeglichen wurde, sonst leer.
	Reset string
	// Allowed sind alle Collections, die der Hub diesem Node erlaubt, auch
	// nicht gewünschte.
	Allowed []string
	// Pages zählt die angewandten Seiten.
	Pages int
	// Collections nennt erst die gewünschten Collections in der Reihenfolge
	// des Eintrags, dann die nicht mehr gewünschten, die entfernt wurden.
	Collections []CollectionResult
	Err         error
	// Kind ist die Art des Fehlers (ErrorKind), wenn Err gesetzt ist.
	Kind ErrorKind
	// Gone sagt, dass der Eintrag während des Abgleichs entfernt oder unter
	// demselben Alias neu angelegt wurde. Dann ist nichts festgehalten, auch
	// kein Fehler: Der neue Eintrag hat seinen eigenen Stand.
	Gone bool
}

// ErrorKind ist die Art eines gescheiterten Abgleichs. Sie steht mit der
// Meldung im Stand des Eintrags (hub_sync); das Log vergleicht sie, nicht die
// Meldung, und whoami zeigt statt der Meldung — die Adresse des Hubs nennen
// kann — einen festen Text je Art.
type ErrorKind string

// Die Arten.
const (
	// KindConnect: Der Eintrag lässt sich nicht verbinden (Transport noch
	// nicht unterstützt, kein Hub in der config).
	KindConnect ErrorKind = "connect"
	// KindUnreachable: Der Hub antwortet nicht (Netz, Zeitüberschreitung).
	KindUnreachable ErrorKind = "unreachable"
	// KindUnauthenticated: Der Hub nimmt den Node nicht an.
	KindUnauthenticated ErrorKind = "unauthenticated"
	// KindVersion: Der Hub bedient die Fassung des Vertrags nicht.
	KindVersion ErrorKind = "unsupported_version"
	// KindHub: Der Hub meldet einen anderen Fehler.
	KindHub ErrorKind = "hub"
	// KindProtocol: Die Antwort des Hubs passt nicht zur Anfrage.
	KindProtocol ErrorKind = "protocol"
	// KindReplica: Replica oder node.db ließen sich nicht lesen oder
	// schreiben, oder die Replica änderte sich wiederholt unter dem Abgleich.
	KindReplica ErrorKind = "replica"
)

// Text ist die Art als kurzer Satz ohne Einzelheiten — ohne Adresse, ohne
// Namen —, wie whoami ihn zeigt.
func (k ErrorKind) Text() string {
	switch k {
	case KindConnect:
		return "Verbindung zum Hub nicht einrichtbar"
	case KindUnreachable:
		return "Hub nicht erreichbar"
	case KindUnauthenticated:
		return "der Hub nimmt diesen Node nicht an"
	case KindVersion:
		return "der Hub bedient die Fassung des Vertrags nicht"
	case KindHub:
		return "der Hub meldet einen Fehler"
	case KindProtocol:
		return "die Antwort des Hubs passt nicht"
	case KindReplica:
		return "Replica nicht schreibbar"
	}
	return "Abgleich gescheitert"
}

// kindError hängt einem Fehler seine Art an.
type kindError struct {
	kind ErrorKind
	err  error
}

func (e *kindError) Error() string { return e.err.Error() }
func (e *kindError) Unwrap() error { return e.err }

func withKind(kind ErrorKind, err error) error {
	if err == nil {
		return nil
	}
	return &kindError{kind: kind, err: err}
}

// kindOf liefert die Art eines Fehlers; ohne angehängte Art KindReplica —
// was nicht vom Hub kommt, ist lokal.
func kindOf(err error) ErrorKind {
	var ke *kindError
	if errors.As(err, &ke) {
		return ke.kind
	}
	return KindReplica
}

// hubKind ordnet einen Fehler aus dem Aufruf des Hubs ein. Ein
// Zertifikatsfehler und eine Gegenseite ohne TLS zählen als nicht erreicht:
// Der Handshake scheiterte, der Hub hat nichts gesehen.
func hubKind(err error) ErrorKind {
	var ce *contract.Error
	var ne net.Error
	var tlsErr *tls.CertificateVerificationError
	var rec tls.RecordHeaderError
	switch {
	case errors.Is(err, contract.ErrUnauthenticated):
		return KindUnauthenticated
	case errors.Is(err, contract.ErrUnsupportedVersion):
		return KindVersion
	case errors.As(err, &ce):
		return KindHub
	case errors.As(err, &ne), errors.Is(err, context.DeadlineExceeded), errors.As(err, &tlsErr),
		errors.As(err, &rec), errors.Is(err, http.ErrSchemeMismatch):
		return KindUnreachable
	}
	return KindHub
}

// Sync gleicht alle Hub-Einträge ab, oder nur den mit dem Alias only, einen
// nach dem anderen (SyncEntry). Der Fehler betrifft nur das Lesen der
// Einträge (oder einen unbekannten Alias); was je Eintrag scheitert, steht in
// HubResult.Err.
func (s *Syncer) Sync(ctx context.Context, only string, connect Connect) ([]HubResult, error) {
	var hubs []store.Hub
	if only != "" {
		h, err := s.Nodes.Hub(ctx, only)
		if err != nil {
			return nil, err
		}
		hubs = []store.Hub{h}
	} else {
		var err error
		if hubs, err = s.Nodes.Hubs(ctx); err != nil {
			return nil, err
		}
	}
	out := make([]HubResult, 0, len(hubs))
	for _, h := range hubs {
		out = append(out, s.SyncEntry(ctx, h, connect))
	}
	return out, nil
}

// SyncEntry verbindet einen Eintrag, gleicht ihn ab (SyncHub) und hält das
// Ergebnis im Stand des Eintrags fest (hub_sync) — außer der Abgleich wurde
// abgebrochen (ctx) oder der Eintrag besteht nicht mehr (Gone).
func (s *Syncer) SyncEntry(ctx context.Context, h store.Hub, connect Connect) HubResult {
	var res HubResult
	if hub, err := connect(h); err != nil {
		res = HubResult{Hub: h.Name, HubID: h.HubID, Err: err, Kind: KindConnect}
	} else {
		res = s.SyncHub(ctx, h, hub)
	}
	if res.Gone || ctx.Err() != nil {
		return res
	}
	rec := store.SyncRecord{At: s.nowMillis()}
	if res.Err != nil {
		rec.Err, rec.ErrKind = res.Err.Error(), string(res.Kind)
	}
	if err := s.Nodes.RecordSync(ctx, h.Name, h.EntryID, rec); errors.Is(err, store.ErrEntryGone) {
		res.Gone = true
	} else if err != nil && res.Err == nil {
		res.Err, res.Kind = err, KindReplica
	}
	return res
}

func (s *Syncer) nowMillis() int64 {
	if s.now != nil {
		return s.now()
	}
	return sqlitedb.NowMillis()
}

// openForSync öffnet die Replica eines Eintrags. Fehlt sie, ist sie nil. Ist
// ihre Schemafassung eine andere oder ist sie eindeutig unlesbar
// (unreadable), wird sie verworfen — sie ist abgeleitet —, und reason sagt
// es. Gehört sie zu einem anderen Eintrag als h, wird sie ebenso verworfen,
// aber nur, wenn h noch der gültige Eintrag des Alias ist; sonst ist h selbst
// veraltet: store.ErrEntryGone. Jeder andere Fehler verwirft nichts.
func openForSync(ctx context.Context, nodes store.Store, h store.Hub) (r *Replica, reason string, err error) {
	path := nodes.ReplicaPath(h.Name)
	r, err = Open(ctx, path)
	var sv *sqlitedb.SchemaVersionError
	switch {
	case err == nil:
		if r.EntryID() == h.EntryID {
			return r, "", nil
		}
		_ = r.Close()
		cur, err := nodes.Hub(ctx, h.Name)
		if errors.Is(err, store.ErrNotFound) || (err == nil && cur.EntryID != h.EntryID) {
			return nil, "", fmt.Errorf("Hub-Eintrag %s %w", h.Name, store.ErrEntryGone)
		}
		if err != nil {
			return nil, "", err
		}
		if err := Remove(path); err != nil {
			return nil, "", fmt.Errorf("Replica %s verwerfen: %w", path, err)
		}
		return nil, "die Replica gehörte zu einem früheren Eintrag gleichen Namens; neu angelegt", nil
	case errors.Is(err, sqlitedb.ErrNotFound):
		return nil, "", nil
	case errors.As(err, &sv):
		if err := Remove(path); err != nil {
			return nil, "", fmt.Errorf("Replica %s verwerfen: %w", path, err)
		}
		return nil, fmt.Sprintf("Schemafassung %s der Replica passt nicht zu diesem Binary (erwartet %s); neu angelegt",
			sv.Got, sv.Want), nil
	case ctx.Err() == nil && unreadable(err):
		if err := Remove(path); err != nil {
			return nil, "", fmt.Errorf("Replica %s verwerfen: %w", path, err)
		}
		return nil, fmt.Sprintf("die Replica war nicht lesbar (%v); neu angelegt", err), nil
	}
	return nil, "", err
}

// unreadable sagt, ob ein Fehler von Open eine eindeutig unlesbare Replica
// meldet, die der Abgleich verwerfen darf: SQLite erkennt die Datei nicht als
// Datenbank oder findet sie beschädigt (beim Öffnen oder beim Lesen von
// db_info), db_info fehlt oder nennt keine Rolle, oder hub_id bzw. entry_id
// fehlen. Nicht dazu gehören eine belegte Datei (BUSY, LOCKED), ein
// abgebrochener ctx, Fehler von stat oder der Zugriffsrechte und eine Datei
// fremder Rolle: Sie bleiben ein Fehler des Abgleichs.
func unreadable(err error) bool {
	return sqlitedb.IsCorrupt(err) || errors.Is(err, sqlitedb.ErrNoInfo) || errors.Is(err, errNoIDs)
}

// createForSync legt die Replica an. War ein anderer schneller, meldet es
// ErrChanged: Der Abgleich setzt neu auf und öffnet die vorhandene.
func createForSync(ctx context.Context, path, hubID, entryID string) (*Replica, error) {
	r, err := Create(ctx, path, hubID, entryID)
	if errors.Is(err, sqlitedb.ErrExists) {
		return nil, ErrChanged
	}
	return r, err
}

// checkResponse prüft eine Antwort gegen ihre Anfrage, soweit der Node es
// kann, bevor er etwas schreibt.
func checkResponse(req contract.SyncRequest, resp contract.SyncResponse) error {
	if resp.Version != contract.Version {
		return fmt.Errorf("der Hub antwortet in Fassung %d, erwartet %d", resp.Version, contract.Version)
	}
	if _, err := ulid.ParseStrict(resp.HubID); err != nil {
		return fmt.Errorf("der Hub nennt als hub_id %q, keine ULID", resp.HubID)
	}
	if len(resp.Collections) != len(req.Collections) {
		return fmt.Errorf("der Hub nennt %d Collections, angefragt waren %d", len(resp.Collections), len(req.Collections))
	}
	for i, c := range resp.Collections {
		if c.Collection != req.Collections[i].Collection {
			return fmt.Errorf("der Hub nennt Collection %q, angefragt war %q", c.Collection, req.Collections[i].Collection)
		}
	}
	return nil
}

// SyncHub gleicht einen Hub-Eintrag über hub ab (docs/vertrag.md, „Regeln
// für den Node“):
//
//   - Nicht mehr gewünschte Collections verlassen die Replica zuerst.
//   - Je gewünschter Collection fragt der Node ab ihrem Stand (neu: 0).
//   - Die erste Antwort legt die Replica an, wenn es sie nicht gibt. Nennt
//     sie eine andere hub_id als db_info der Replica, oder liegt ein Stand
//     über der Revision des Hubs (Hub aus einer Sicherung), wird die Replica
//     geleert und von vorn abgeglichen — höchstens einmal je Aufruf.
//   - Danach schreibt der Node die Kopie der hub_id in node.db.
//   - Jede Seite ist eine Transaktion: nicht erlaubte Collections entfernen,
//     Zeilen per id einfügen oder ersetzen, je Collection den Stand auf
//     max(Stand, until). Dann die nächste Seite, solange more gilt.
//
// Laufen zwei Abgleiche derselben Replica nebeneinander (serve und node
// sync, auch in getrennten Prozessen), schreibt jede Seite nur, wenn die
// Replica noch zu Eintrag, hub_id und Stand passt, von denen sie ausging;
// sonst setzt SyncHub neu auf (höchstens zweimal). Wurde der Eintrag
// inzwischen entfernt oder neu angelegt, bricht es ab (Gone) und schreibt
// nichts mehr — weder in die Replica noch hub_id oder Stand in node.db.
func (s *Syncer) SyncHub(ctx context.Context, h store.Hub, hub contract.Hub) HubResult {
	const attempts = 3
	for attempt := 1; ; attempt++ {
		res := HubResult{Hub: h.Name, HubID: h.HubID}
		err := s.syncHub(ctx, h, hub, &res)
		if err == nil {
			return res
		}
		res.Err, res.Kind = err, kindOf(err)
		if errors.Is(err, store.ErrEntryGone) {
			res.Gone = true
			return res
		}
		if !errors.Is(err, ErrChanged) || ctx.Err() != nil {
			return res
		}
		cur, cerr := s.Nodes.Hub(ctx, h.Name)
		if errors.Is(cerr, store.ErrNotFound) || (cerr == nil && cur.EntryID != h.EntryID) {
			res.Gone = true
			return res
		}
		if cerr != nil || attempt >= attempts {
			return res
		}
		h = cur
	}
}

// progress hält fest, was der Abgleich eines Eintrags je Collection getan
// hat.
type progress struct {
	order  []string
	byName map[string]*CollectionResult
}

func (p *progress) get(c string, st Status) *CollectionResult {
	if r, ok := p.byName[c]; ok {
		r.Status = st
		return r
	}
	r := &CollectionResult{Collection: c, Status: st}
	p.byName[c] = r
	p.order = append(p.order, c)
	return r
}

func (p *progress) results() []CollectionResult {
	out := make([]CollectionResult, 0, len(p.order))
	for _, c := range p.order {
		out = append(out, *p.byName[c])
	}
	return out
}

func (s *Syncer) syncHub(ctx context.Context, h store.Hub, hub contract.Hub, res *HubResult) error {
	pageSize := s.PageSize
	if pageSize <= 0 {
		pageSize = contract.DefaultPageSize
	}
	now := s.now
	if now == nil {
		now = sqlitedb.NowMillis
	}
	prog := &progress{byName: map[string]*CollectionResult{}}
	for _, c := range h.Collections {
		prog.get(c, Synced)
	}
	defer func() { res.Collections = prog.results() }()

	path := s.Nodes.ReplicaPath(h.Name)
	rep, reason, err := openForSync(ctx, s.Nodes, h)
	if err != nil {
		return err
	}
	res.Reset = reason
	defer func() {
		if rep != nil {
			_ = rep.Close()
		}
	}()

	state := map[string]int64{}
	if rep != nil {
		if err := s.dropUnwanted(ctx, rep, h.Collections, prog, now()); err != nil {
			return err
		}
		states, err := rep.States(ctx)
		if err != nil {
			return err
		}
		for _, st := range states {
			state[st.Collection] = st.Revision
		}
	}

	pending := slices.Clone(h.Collections)
	reset := false
	for {
		req := contract.SyncRequest{
			Version:     contract.Version,
			Auth:        contract.NodeAuth{Node: h.NodeName, Token: h.Token},
			Collections: make([]contract.Since, 0, len(pending)),
			PageSize:    pageSize,
		}
		for _, c := range pending {
			req.Collections = append(req.Collections, contract.Since{Collection: c, Since: state[c]})
		}
		resp, err := hub.Sync(ctx, req)
		if err != nil {
			return withKind(hubKind(err), err)
		}
		if err := checkResponse(req, resp); err != nil {
			return withKind(KindProtocol, err)
		}

		if rep == nil {
			if rep, err = createForSync(ctx, path, resp.HubID, h.EntryID); err != nil {
				return err
			}
		} else if why := mismatch(rep, state, resp); why != "" {
			// Die Seite gehört zu Ständen, die nicht mehr gelten: verwerfen,
			// Replica leeren, von vorn fragen.
			if reset {
				return withKind(KindProtocol, fmt.Errorf("der Hub wechselt während des Abgleichs erneut: %s", why))
			}
			if err := rep.reset(ctx, resp.HubID); err != nil {
				return err
			}
			reset = true
			res.Reset = why
			state = map[string]int64{}
			for _, r := range prog.byName {
				if r.Status == Synced {
					r.Rows, r.Revision = 0, 0
				}
			}
			continue
		}
		if h.HubID != resp.HubID {
			// Die Kopie folgt der maßgeblichen hub_id der Replica.
			if err := s.Nodes.SetHubID(ctx, h.Name, h.EntryID, resp.HubID); err != nil {
				return err
			}
			h.HubID = resp.HubID
		}
		res.HubID = resp.HubID
		res.Allowed = resp.Allowed

		p := page{rows: resp.Rows, advance: map[string]int64{}, until: resp.Until, now: now()}
		next := make([]string, 0, len(pending))
		for _, cs := range resp.Collections {
			if cs.Allowed {
				p.advance[cs.Collection] = state[cs.Collection]
				next = append(next, cs.Collection)
			} else {
				p.drop = append(p.drop, cs.Collection)
			}
		}
		for _, row := range resp.Rows {
			if _, ok := p.advance[row.Collection]; !ok {
				return withKind(KindProtocol, fmt.Errorf("der Hub liefert eine Zeile aus Collection %q, "+
					"die nicht angefragt oder nicht erlaubt ist", row.Collection))
			}
		}
		minSince := int64(-1)
		for _, c := range next {
			if minSince < 0 || state[c] < minSince {
				minSince = state[c]
			}
		}
		drops, err := rep.apply(ctx, p)
		if err != nil {
			return err
		}
		res.Pages++
		for c, d := range drops {
			prog.get(c, NotAllowed).Removed += d.rows
		}
		for _, row := range resp.Rows {
			prog.get(row.Collection, Synced).Rows++
		}
		for c := range p.advance {
			state[c] = max(state[c], resp.Until)
			prog.get(c, Synced).Revision = state[c]
		}
		pending = next
		if !resp.More || len(pending) == 0 {
			return nil
		}
		// Mit more muss until über dem kleinsten Stand liegen, sonst fragte
		// der Node ewig dasselbe.
		if resp.Until <= minSince {
			return withKind(KindProtocol, fmt.Errorf("der Hub meldet weitere Zeilen, kommt aber nicht über Revision %d hinaus",
				resp.Until))
		}
	}
}

// mismatch sagt, warum die Replica nicht zur Antwort passt: andere hub_id,
// oder ein Stand über der Revision des Hubs. Leer heißt: passt.
func mismatch(rep *Replica, state map[string]int64, resp contract.SyncResponse) string {
	if rep.HubID() != resp.HubID {
		return fmt.Sprintf("hub_id gewechselt (%s → %s); Replica geleert und von vorn abgeglichen", rep.HubID(), resp.HubID)
	}
	var top int64
	for _, rev := range state {
		top = max(top, rev)
	}
	if top > resp.HubRevision {
		return fmt.Sprintf("Stand %d der Replica liegt über der Revision %d des Hubs (aus einer Sicherung "+
			"zurückgespielt?); Replica geleert und von vorn abgeglichen", top, resp.HubRevision)
	}
	return ""
}

// dropUnwanted entfernt aus der Replica, was der Node nicht mehr will, in
// einer Transaktion.
func (s *Syncer) dropUnwanted(ctx context.Context, rep *Replica, wanted []string, prog *progress, now int64) error {
	have, err := rep.Collections(ctx)
	if err != nil {
		return err
	}
	var drop []string
	for _, c := range have {
		if !slices.Contains(wanted, c) {
			drop = append(drop, c)
		}
	}
	if len(drop) == 0 {
		return nil
	}
	drops, err := rep.apply(ctx, page{drop: drop, now: now})
	if err != nil {
		return err
	}
	for _, c := range drop {
		prog.get(c, NotWanted).Removed = drops[c].rows
	}
	return nil
}
