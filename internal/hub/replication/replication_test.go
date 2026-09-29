package replication

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"testing"

	"github.com/kephalaion/kephalaion/internal/config"
	"github.com/kephalaion/kephalaion/internal/contract"
	"github.com/kephalaion/kephalaion/internal/contract/httpapi"
	"github.com/kephalaion/kephalaion/internal/hub/store"
)

// fixture ist ein Hub mit den Collections a, b und c und dem Node laptop,
// der a und b abgleichen darf. client ist die Umsetzung des Vertrags, gegen
// die der Test läuft: der Hub selbst (local) oder ein Client über HTTP.
type fixture struct {
	st     store.Store
	hub    *Hub
	token  string
	client contract.Hub
	// url ist die Adresse des HTTP-Servers, leer bei local.
	url string
	// dbPath ist der Ort der Datenbank des Hubs.
	dbPath string
}

// transports sind die Umsetzungen, gegen die jeder Test des Vertrags läuft.
var transports = []string{"local", "http"}

// forTransports führt fn je Umsetzung aus; newFixture liefert dort einen Hub,
// den der Test über diese Umsetzung anspricht.
func forTransports(t *testing.T, fn func(t *testing.T, newFixture func(*testing.T) *fixture)) {
	for _, tr := range transports {
		t.Run(tr, func(t *testing.T) {
			fn(t, func(t *testing.T) *fixture { return newFixtureOver(t, tr) })
		})
	}
}

func newFixtureOver(t *testing.T, transport string) *fixture {
	t.Helper()
	f := newLocalFixture(t)
	if transport == "http" {
		srv := httptest.NewServer(httpapi.NewHandler(f.hub))
		t.Cleanup(srv.Close)
		c, err := httpapi.NewClient(srv.URL, nil)
		if err != nil {
			t.Fatal(err)
		}
		c.Retries = 0
		f.client, f.url = c, srv.URL
	}
	return f
}

func newLocalFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "hub.db")
	st, err := store.Create(ctx, config.SQLiteDB(dbPath))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	for _, c := range []string{"a", "b", "c"} {
		if err := st.AddCollection(ctx, c, ""); err != nil {
			t.Fatal(err)
		}
	}
	token, err := st.AddNode(ctx, "laptop", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []string{"a", "b"} {
		if err := st.Grant(ctx, "laptop", c); err != nil {
			t.Fatal(err)
		}
	}
	h := New(st)
	return &fixture{st: st, hub: h, token: token, client: h, dbPath: dbPath}
}

// api ist die Schnittstelle, gegen die die Tests laufen.
func (f *fixture) api() contract.Hub { return f.client }

func (f *fixture) put(t *testing.T, collection, name string) {
	t.Helper()
	if _, err := f.st.PutDocument(context.Background(), collection, name, "inhalt "+name); err != nil {
		t.Fatal(err)
	}
}

// importN schreibt n Dokumente in einer Revision.
func (f *fixture) importN(t *testing.T, collection, prefix string, n int) {
	t.Helper()
	docs := make([]store.DocumentInput, n)
	for i := range docs {
		docs[i] = store.DocumentInput{Name: fmt.Sprintf("%s%02d.md", prefix, i), Content: "x"}
	}
	if _, err := f.st.ImportDocuments(context.Background(), collection, docs); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) request(pageSize int, since ...contract.Since) contract.SyncRequest {
	return contract.SyncRequest{
		Version:     contract.Version,
		Auth:        contract.NodeAuth{Node: "laptop", Token: f.token},
		Collections: since,
		PageSize:    pageSize,
	}
}

func (f *fixture) sync(t *testing.T, req contract.SyncRequest) contract.SyncResponse {
	t.Helper()
	resp, err := f.api().Sync(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func revisions(rows []contract.Row) []int64 {
	out := make([]int64, len(rows))
	for i, r := range rows {
		out[i] = r.Revision
	}
	return out
}

// checkOrder prüft die Ordnung einer Seite: nach Revision, dann id.
func checkOrder(t *testing.T, rows []contract.Row) {
	t.Helper()
	for i := 1; i < len(rows); i++ {
		p, r := rows[i-1], rows[i]
		if p.Revision > r.Revision || (p.Revision == r.Revision && p.ID >= r.ID) {
			t.Errorf("Ordnung verletzt bei %d: %d/%s vor %d/%s", i, p.Revision, p.ID, r.Revision, r.ID)
		}
	}
}

// syncAll gleicht ab wie ein Node: je Collection max(seit, until), bis more
// nicht mehr gilt. Liefert die Seiten.
func syncAll(t *testing.T, f *fixture, pageSize int, since map[string]int64) []contract.SyncResponse {
	t.Helper()
	var pages []contract.SyncResponse
	for range 100 {
		names := make([]string, 0, len(since))
		for c := range since {
			names = append(names, c)
		}
		slices.Sort(names)
		req := f.request(pageSize)
		for _, c := range names {
			req.Collections = append(req.Collections, contract.Since{Collection: c, Since: since[c]})
		}
		resp := f.sync(t, req)
		checkOrder(t, resp.Rows)
		pages = append(pages, resp)
		for c, s := range since {
			since[c] = max(s, resp.Until)
		}
		if !resp.More {
			return pages
		}
	}
	t.Fatal("Abgleich endet nicht")
	return nil
}

func TestPageBoundaryInsideLargeRevision(t *testing.T) {
	forTransports(t, func(t *testing.T, newFixture func(*testing.T) *fixture) {
		f := newFixture(t)
		f.put(t, "a", "eins.md")      // Revision 1
		f.importN(t, "a", "gross", 5) // Revision 2, fünf Zeilen
		f.put(t, "b", "zwei.md")      // Revision 3

		// Seitengröße 3: Revision 2 reicht über die erste Seite und wird
		// weggelassen; die zweite Seite bringt sie ganz, obwohl sie größer ist.
		pages := syncAll(t, f, 3, map[string]int64{"a": 0, "b": 0})
		if len(pages) != 3 {
			t.Fatalf("%d Seiten, erwartet 3", len(pages))
		}
		want := []struct {
			revs  []int64
			until int64
			more  bool
		}{
			{[]int64{1}, 1, true},
			{[]int64{2, 2, 2, 2, 2}, 2, true},
			{[]int64{3}, 3, false},
		}
		for i, w := range want {
			p := pages[i]
			if !slices.Equal(revisions(p.Rows), w.revs) || p.Until != w.until || p.More != w.more || p.HubRevision != 3 {
				t.Errorf("Seite %d: Revisionen %v, until %d, more %v, H %d; erwartet %v, %d, %v, 3",
					i+1, revisions(p.Rows), p.Until, p.More, p.HubRevision, w.revs, w.until, w.more)
			}
		}

		// Die große Revision allein, als letzte: sie kommt ganz, more gilt nicht.
		f2 := newFixture(t)
		f2.importN(t, "a", "gross", 5)
		resp := f2.sync(t, f2.request(2, contract.Since{Collection: "a"}))
		if len(resp.Rows) != 5 || resp.Until != 1 || resp.More {
			t.Errorf("große Revision allein: %d Zeilen, until %d, more %v", len(resp.Rows), resp.Until, resp.More)
		}
	})
}

func TestSeveralRevisionsOverSeveralPages(t *testing.T) {
	forTransports(t, func(t *testing.T, newFixture func(*testing.T) *fixture) {
		f := newFixture(t)
		for i := range 7 {
			f.put(t, []string{"a", "b"}[i%2], fmt.Sprintf("d%d.md", i)) // Revisionen 1–7
		}
		f.put(t, "c", "fremd.md") // Revision 8, nicht erlaubt

		pages := syncAll(t, f, 3, map[string]int64{"a": 0, "b": 0})
		var got []int64
		var untils []int64
		for _, p := range pages {
			got = append(got, revisions(p.Rows)...)
			untils = append(untils, p.Until)
		}
		if !slices.Equal(got, []int64{1, 2, 3, 4, 5, 6, 7}) {
			t.Errorf("Revisionen %v", got)
		}
		// Die letzte Seite endet bei H = 8, obwohl die letzte Zeile 7 trägt.
		if !slices.Equal(untils, []int64{3, 6, 8}) {
			t.Errorf("until %v", untils)
		}

		// Verschiedene Stände je Collection: b ist schon bei 6, a beginnt bei 0.
		// Keine Zeile von b ≤ 6 kommt, b fällt nicht zurück.
		since := map[string]int64{"a": 0, "b": 6}
		pages = syncAll(t, f, 2, since)
		got = nil
		for _, p := range pages {
			for _, r := range p.Rows {
				if r.Collection == "b" && r.Revision <= 6 {
					t.Errorf("b: Zeile mit Revision %d, seit war 6", r.Revision)
				}
				got = append(got, r.Revision)
			}
		}
		if !slices.Equal(got, []int64{1, 3, 5, 7}) || since["a"] != 8 || since["b"] != 8 {
			t.Errorf("Revisionen %v, Stand %v", got, since)
		}
	})
}

func TestUnauthenticatedSameAnswer(t *testing.T) {
	forTransports(t, func(t *testing.T, newFixture func(*testing.T) *fixture) {
		f := newFixture(t)
		ctx := context.Background()
		f.put(t, "a", "eins.md")

		if _, err := f.st.AddNode(ctx, "gesperrt", ""); err != nil {
			t.Fatal(err)
		}
		lockedToken, err := f.st.NewNodeToken(ctx, "gesperrt")
		if err != nil {
			t.Fatal(err)
		}
		if err := f.st.Grant(ctx, "gesperrt", "a"); err != nil {
			t.Fatal(err)
		}
		if err := f.st.SetNodeLocked(ctx, "gesperrt", true); err != nil {
			t.Fatal(err)
		}

		cases := map[string]contract.NodeAuth{
			"falsches Token": {Node: "laptop", Token: lockedToken},
			"kein Token":     {Node: "laptop"},
			"gesperrt":       {Node: "gesperrt", Token: lockedToken},
			"unbekannt":      {Node: "fremd", Token: f.token},
			"ohne Namen":     {Token: f.token},
		}
		var msgs []string
		for name, auth := range cases {
			req := f.request(10, contract.Since{Collection: "a"})
			req.Auth = auth
			resp, err := f.api().Sync(ctx, req)
			if !errors.Is(err, contract.ErrUnauthenticated) {
				t.Errorf("%s: Fehler %v, erwartet nicht angemeldet", name, err)
				continue
			}
			var cerr *contract.Error
			if !errors.As(err, &cerr) || cerr.Code != contract.CodeUnauthenticated {
				t.Errorf("%s: Fehler %#v", name, err)
			}
			if resp.HubID != "" || len(resp.Rows) != 0 {
				t.Errorf("%s: Antwort trotz Fehler: %+v", name, resp)
			}
			msgs = append(msgs, err.Error())
		}
		for _, m := range msgs[1:] {
			if m != msgs[0] {
				t.Errorf("Meldungen verschieden: %q und %q", msgs[0], m)
			}
		}

		// Nach unlock gilt das Token wieder.
		if err := f.st.SetNodeLocked(ctx, "gesperrt", false); err != nil {
			t.Fatal(err)
		}
		req := f.request(10, contract.Since{Collection: "a"})
		req.Auth = contract.NodeAuth{Node: "gesperrt", Token: lockedToken}
		if resp := f.sync(t, req); len(resp.Rows) != 1 {
			t.Errorf("nach unlock: %+v", resp)
		}
	})
}

func TestNotAllowedCollection(t *testing.T) {
	forTransports(t, func(t *testing.T, newFixture func(*testing.T) *fixture) {
		f := newFixture(t)
		f.put(t, "a", "eins.md")
		f.put(t, "c", "fremd.md")
		info, err := f.st.Info(context.Background())
		if err != nil {
			t.Fatal(err)
		}

		resp := f.sync(t, f.request(10,
			contract.Since{Collection: "c"},             // vorhanden, nicht erlaubt
			contract.Since{Collection: "a"},             // erlaubt
			contract.Since{Collection: "unbekannt"},     // gibt es nicht
			contract.Since{Collection: "SYSTEM:kaputt"}, // kein gültiger Name
		))
		wantStatus := []contract.CollectionStatus{
			{Collection: "c", Allowed: false},
			{Collection: "a", Allowed: true},
			{Collection: "unbekannt", Allowed: false},
			{Collection: "SYSTEM:kaputt", Allowed: false},
		}
		if !slices.Equal(resp.Collections, wantStatus) {
			t.Errorf("Collections %+v", resp.Collections)
		}
		if !slices.Equal(resp.Allowed, []string{"a", "b"}) {
			t.Errorf("Allowed %v", resp.Allowed)
		}
		if len(resp.Rows) != 1 || resp.Rows[0].Collection != "a" {
			t.Errorf("Zeilen %+v", resp.Rows)
		}
		if resp.HubID != info.HubID || resp.Version != contract.Version || resp.HubRevision != info.Revision {
			t.Errorf("Antwort %+v, Info %+v", resp, info)
		}

		// Nach revoke ist a nicht mehr erlaubt.
		if err := f.st.Revoke(context.Background(), "laptop", "a"); err != nil {
			t.Fatal(err)
		}
		resp = f.sync(t, f.request(10, contract.Since{Collection: "a"}))
		if resp.Collections[0].Allowed || len(resp.Rows) != 0 || !slices.Equal(resp.Allowed, []string{"b"}) {
			t.Errorf("nach revoke: %+v", resp)
		}
	})
}

func TestUnsupportedVersion(t *testing.T) {
	forTransports(t, func(t *testing.T, newFixture func(*testing.T) *fixture) {
		f := newFixture(t)
		for _, v := range []int{0, 2, -1} {
			req := f.request(10, contract.Since{Collection: "a"})
			req.Version = v
			// Auch ohne gültige Anmeldung: die Fassung wird zuerst geprüft.
			_, err := f.api().Sync(context.Background(), req)
			if !errors.Is(err, contract.ErrUnsupportedVersion) {
				t.Errorf("Fassung %d: Fehler %v", v, err)
			}
		}
	})
}

func TestInvalidRequest(t *testing.T) {
	forTransports(t, func(t *testing.T, newFixture func(*testing.T) *fixture) {
		f := newFixture(t)
		cases := map[string]contract.SyncRequest{
			"Seitengröße 0":  f.request(0, contract.Since{Collection: "a"}),
			"Seitengröße -1": f.request(-1, contract.Since{Collection: "a"}),
			"doppelt":        f.request(10, contract.Since{Collection: "a"}, contract.Since{Collection: "a", Since: 3}),
			"seit negativ":   f.request(10, contract.Since{Collection: "a", Since: -1}),
		}
		for name, req := range cases {
			_, err := f.api().Sync(context.Background(), req)
			if !errors.Is(err, contract.ErrInvalid) || errors.Is(err, contract.ErrUnauthenticated) {
				t.Errorf("%s: Fehler %v, erwartet ungültige Anfrage", name, err)
			}
		}
	})
}

func TestPageSizeCapped(t *testing.T) {
	forTransports(t, func(t *testing.T, newFixture func(*testing.T) *fixture) {
		f := newFixture(t)
		f.hub.maxPageSize = 2
		for i := range 5 {
			f.put(t, "a", fmt.Sprintf("d%d.md", i))
		}
		resp := f.sync(t, f.request(1000, contract.Since{Collection: "a"}))
		if !slices.Equal(revisions(resp.Rows), []int64{1, 2}) || resp.Until != 2 || !resp.More {
			t.Errorf("Obergrenze 2: Revisionen %v, until %d, more %v", revisions(resp.Rows), resp.Until, resp.More)
		}
		if MaxPageSize < contract.DefaultPageSize {
			t.Errorf("MaxPageSize %d unter DefaultPageSize %d", MaxPageSize, contract.DefaultPageSize)
		}
	})
}

func TestEmptyPage(t *testing.T) {
	forTransports(t, func(t *testing.T, newFixture func(*testing.T) *fixture) {
		f := newFixture(t)
		f.put(t, "a", "eins.md")  // 1
		f.put(t, "c", "fremd.md") // 2, nicht erlaubt
		f.put(t, "a", "zwei.md")  // 3

		// Nichts Neues seit 3: leere Seite, until = H.
		resp := f.sync(t, f.request(10, contract.Since{Collection: "a", Since: 3}, contract.Since{Collection: "b"}))
		if len(resp.Rows) != 0 || resp.Until != 3 || resp.More || resp.HubRevision != 3 {
			t.Errorf("leere Seite: %+v", resp)
		}
		if resp.Rows == nil || resp.Collections == nil || resp.Allowed == nil {
			t.Errorf("leere Listen als nil: %+v", resp)
		}
		// Ohne Collections: nur erlaubte Collections und H.
		resp = f.sync(t, f.request(10))
		if len(resp.Rows) != 0 || resp.Until != 3 || resp.More || !slices.Equal(resp.Allowed, []string{"a", "b"}) {
			t.Errorf("ohne Collections: %+v", resp)
		}
		// seit über H (Hub aus Sicherung): leere Seite, until = H < seit — der
		// Node erkennt das an HubRevision.
		resp = f.sync(t, f.request(10, contract.Since{Collection: "a", Since: 9}))
		if len(resp.Rows) != 0 || resp.Until != 3 || resp.HubRevision != 3 {
			t.Errorf("seit über H: %+v", resp)
		}
	})
}

func TestDeletedRowsIncluded(t *testing.T) {
	forTransports(t, func(t *testing.T, newFixture func(*testing.T) *fixture) {
		f := newFixture(t)
		f.put(t, "a", "eins.md")
		if _, err := f.st.DeleteDocument(context.Background(), "a", "eins.md"); err != nil {
			t.Fatal(err)
		}
		resp := f.sync(t, f.request(10, contract.Since{Collection: "a"}))
		if len(resp.Rows) != 1 {
			t.Fatalf("Zeilen %+v", resp.Rows)
		}
		r := resp.Rows[0]
		if !r.Deleted || r.Content != nil || r.Meta != nil || r.Revision != 2 || r.Name != "eins.md" {
			t.Errorf("Löschmarke %+v", r)
		}
	})
}
