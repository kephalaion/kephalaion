package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/oklog/ulid/v2"

	"github.com/kephalaion/kephalaion/internal/ident"
	"github.com/kephalaion/kephalaion/internal/testcert"
)

func newStore(t *testing.T) Store {
	t.Helper()
	s, err := Create(context.Background(), newDB(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func token(t *testing.T) string {
	t.Helper()
	tok, err := ident.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func ptr(s string) *string { return &s }

func TestTransportRules(t *testing.T) {
	tok := token(t)
	ca, ca2 := testcert.NewCA(t, "Test-CA").PEM, testcert.NewCA(t, "Zweite CA").PEM
	const key = "-----BEGIN EC PRIVATE KEY-----\nMHcCAQEEIA==\n-----END EC PRIVATE KEY-----\n"
	cases := []struct {
		name  string
		h     Hub
		local bool
		ok    bool
	}{
		{"local mit Hub", Hub{Transport: "local"}, true, true},
		{"local ohne Hub", Hub{Transport: "local"}, false, false},
		{"local mit Adresse", Hub{Transport: "local", Address: "x"}, true, false},
		{"http localhost", Hub{Transport: "http", Address: "http://localhost:8080"}, false, true},
		{"http 127.0.0.1", Hub{Transport: "http", Address: "http://127.0.0.1:8080"}, false, true},
		{"http ::1", Hub{Transport: "http", Address: "http://[::1]:8080"}, false, true},
		{"http entfernt", Hub{Transport: "http", Address: "http://hub.example.org"}, false, false},
		{"http mit https-URL", Hub{Transport: "http", Address: "https://localhost"}, false, false},
		{"http ohne Adresse", Hub{Transport: "http"}, false, false},
		{"https", Hub{Transport: "https", Address: "https://hub.example.org"}, false, true},
		{"https mit http-URL", Hub{Transport: "https", Address: "http://hub.example.org"}, false, false},
		{"https ohne Host", Hub{Transport: "https", Address: "https://"}, false, false},
		{"ssh", Hub{Transport: "ssh", Address: "keph@hub:2222"}, false, true},
		{"ssh mit Schlüssel", Hub{Transport: "ssh", Address: "hub", SSHKey: "/k/id"}, false, true},
		{"ssh ohne Adresse", Hub{Transport: "ssh"}, false, false},
		{"Schlüssel bei https", Hub{Transport: "https", Address: "https://h", SSHKey: "/k"}, false, false},
		{"https mit CA", Hub{Transport: "https", Address: "https://h", CA: ca}, false, true},
		{"https mit zwei CAs", Hub{Transport: "https", Address: "https://h", CA: ca + ca2}, false, true},
		{"https mit kaputter CA", Hub{Transport: "https", Address: "https://h", CA: "kein PEM"}, false, false},
		{"https mit Schlüssel statt CA", Hub{Transport: "https", Address: "https://h", CA: key}, false, false},
		{"CA bei http", Hub{Transport: "http", Address: "http://localhost:1", CA: ca}, false, false},
		{"CA bei ssh", Hub{Transport: "ssh", Address: "h", CA: ca}, false, false},
		{"CA bei local", Hub{Transport: "local", CA: ca}, true, false},
		{"unbekannt", Hub{Transport: "ftp", Address: "ftp://h"}, false, false},
		{"ohne Transport", Hub{}, true, false},
	}
	for _, c := range cases {
		c.h.Name = "privat"
		c.h.NodeName = "laptop"
		c.h.Token = tok
		err := CheckHub(c.h, c.local)
		if (err == nil) != c.ok {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	if err := CheckHub(Hub{Name: "privat", NodeName: "laptop", Transport: "local", Token: "keph_kurz"}, true); err == nil {
		t.Error("ungültiges Token angenommen")
	}
	if err := CheckHub(Hub{Name: "System", NodeName: "laptop", Transport: "local", Token: tok}, true); err == nil {
		t.Error("ungültiger Name angenommen")
	}
	if err := CheckHub(Hub{Name: "privat", Transport: "local", Token: tok}, true); err == nil ||
		!strings.Contains(err.Error(), "--node") {
		t.Errorf("fehlender Node-Name: %v", err)
	}
	for _, bad := range []string{"Laptop", "system-x", "a:b"} {
		if err := CheckHub(Hub{Name: "privat", NodeName: bad, Transport: "local", Token: tok}, true); err == nil {
			t.Errorf("ungültiger Node-Name %q angenommen", bad)
		}
	}
}

func TestHubs(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	tok := token(t)
	if err := s.AddHub(ctx, Hub{Name: "lokal", NodeName: "laptop", Transport: "local", Token: tok}, false); err == nil {
		t.Error("local ohne Hub in der config angenommen")
	}
	if err := s.AddHub(ctx, Hub{Name: "lokal", NodeName: "laptop", Transport: "local", Token: tok}, true); err != nil {
		t.Fatal(err)
	}
	if err := s.AddHub(ctx, Hub{Name: "zweit", NodeName: "laptop", Transport: "local", Token: tok}, true); err == nil ||
		!strings.Contains(err.Error(), "local") {
		t.Errorf("zweites local: %v", err)
	}
	if err := s.AddHub(ctx, Hub{Name: "lokal", NodeName: "laptop", Transport: "http", Address: "http://localhost:1", Token: tok}, true); !errors.Is(err, ErrExists) {
		t.Errorf("doppelt: %v", err)
	}
	if err := s.AddHub(ctx, Hub{Name: "test", NodeName: "laptop", Transport: "http", Address: "http://localhost:8080", Token: tok}, true); err != nil {
		t.Fatal(err)
	}
	hubs, err := s.Hubs(ctx)
	if err != nil || len(hubs) != 2 || hubs[0].Name != "lokal" || hubs[1].Address != "http://localhost:8080" ||
		hubs[1].Token != tok || hubs[1].HubID != "" {
		t.Errorf("Hubs = %+v, %v", hubs, err)
	}
	tok2 := token(t)
	if err := s.SetHubToken(ctx, "test", tok2); err != nil {
		t.Fatal(err)
	}
	if err := s.SetHubToken(ctx, "test", "keph_x"); err == nil {
		t.Error("ungültiges Token angenommen")
	}
	if err := s.SetHubToken(ctx, "fehlt", tok2); !errors.Is(err, ErrNotFound) {
		t.Errorf("token auf Fehlendes: %v", err)
	}
	if h, _ := s.Hub(ctx, "test"); h.Token != tok2 {
		t.Error("Token nicht ersetzt")
	}
	if _, err := s.Hub(ctx, "fehlt"); !errors.Is(err, ErrNotFound) {
		t.Errorf("show auf Fehlendes: %v", err)
	}
}

func TestSetHub(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	tok := token(t)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.AddHub(ctx, Hub{Name: "lokal", NodeName: "laptop", Transport: "local", Token: tok}, true))
	must(s.AddHub(ctx, Hub{Name: "fern", NodeName: "laptop", Transport: "ssh", Address: "keph@hub", SSHKey: "/k/id", Token: tok}, true))
	must(s.AddCollection(ctx, "fern", "team-x"))
	// hub_id von Hand, wie nach einem Kontakt.
	db := s.(*sqliteStore).db
	const hubID = "01J8Z3N6Q4T3V5W7X9Y0A1B2C3"
	if _, err := db.ExecContext(ctx, `UPDATE hubs SET hub_id = ? WHERE name = 'fern'`, hubID); err != nil {
		t.Fatal(err)
	}

	// Ein zweites local wird abgewiesen, nichts ändert sich.
	if err := s.SetHub(ctx, "fern", HubUpdate{Transport: ptr("local")}, true); err == nil {
		t.Fatal("zweites local angenommen")
	}
	if h, _ := s.Hub(ctx, "fern"); h.Transport != "ssh" || h.Address != "keph@hub" {
		t.Errorf("nach Abweisung verändert: %+v", h)
	}
	// Adresse ändern, Rest bleibt.
	must(s.SetHub(ctx, "fern", HubUpdate{Address: ptr("keph@hub2:22")}, true))
	if h, _ := s.Hub(ctx, "fern"); h.Address != "keph@hub2:22" || h.SSHKey != "/k/id" || h.Transport != "ssh" {
		t.Errorf("nach Adresse: %+v", h)
	}
	// --ssh-key bei anderem Transport wird abgewiesen.
	if err := s.SetHub(ctx, "lokal", HubUpdate{SSHKey: ptr("/k")}, true); err == nil {
		t.Error("ssh-key bei local angenommen")
	}
	// Wechsel auf local verwirft Adresse und Schlüssel; hub_collections und
	// hub_id bleiben.
	must(s.RemoveHub(ctx, "lokal"))
	must(s.SetHub(ctx, "fern", HubUpdate{Transport: ptr("local")}, true))
	h, _ := s.Hub(ctx, "fern")
	if h.Transport != "local" || h.Address != "" || h.SSHKey != "" || h.HubID != hubID || h.Token != tok ||
		!reflect.DeepEqual(h.Collections, []string{"team-x"}) {
		t.Errorf("nach Wechsel auf local: %+v", h)
	}
	// Wechsel auf https braucht eine passende Adresse.
	if err := s.SetHub(ctx, "fern", HubUpdate{Transport: ptr("https")}, true); err == nil {
		t.Error("https ohne Adresse angenommen")
	}
	must(s.SetHub(ctx, "fern", HubUpdate{Transport: ptr("https"), Address: ptr("https://hub.example.org")}, true))
	// Wechsel auf local mit Adresse wird abgewiesen.
	if err := s.SetHub(ctx, "fern", HubUpdate{Transport: ptr("local"), Address: ptr("x")}, true); err == nil {
		t.Error("local mit Adresse angenommen")
	}
	if err := s.SetHub(ctx, "fehlt", HubUpdate{Address: ptr("x")}, true); !errors.Is(err, ErrNotFound) {
		t.Errorf("set auf Fehlendes: %v", err)
	}
	// Node-Name ändern, Rest bleibt; ein ungültiger wird abgewiesen.
	must(s.SetHub(ctx, "fern", HubUpdate{NodeName: ptr("rechner-2")}, true))
	if h, _ := s.Hub(ctx, "fern"); h.NodeName != "rechner-2" || h.Transport != "https" || h.HubID != hubID {
		t.Errorf("nach Node-Name: %+v", h)
	}
	for _, bad := range []string{"", "System", "a:b"} {
		if err := s.SetHub(ctx, "fern", HubUpdate{NodeName: ptr(bad)}, true); err == nil {
			t.Errorf("Node-Name %q angenommen", bad)
		}
	}
	if h, _ := s.Hub(ctx, "fern"); h.NodeName != "rechner-2" {
		t.Errorf("nach Abweisung verändert: %+v", h)
	}
}

func TestCollections(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	tok := token(t)
	if err := s.AddCollection(ctx, "privat", "team-x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("ohne Hub-Eintrag: %v", err)
	}
	if err := s.AddHub(ctx, Hub{Name: "privat", NodeName: "laptop", Transport: "https", Address: "https://h", Token: tok}, false); err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][2]string{{"privat", "Team"}, {"privat", "system"}, {"privat", "a:b"}, {"Privat", "x"}, {"privat", ""}} {
		if err := s.AddCollection(ctx, bad[0], bad[1]); err == nil {
			t.Errorf("%v angenommen", bad)
		}
	}
	if err := s.AddCollection(ctx, "privat", "team-x"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddCollection(ctx, "privat", "team-x"); !errors.Is(err, ErrExists) {
		t.Errorf("doppelt: %v", err)
	}
	if err := s.AddCollection(ctx, "privat", "notizen"); err != nil {
		t.Fatal(err)
	}
	got, err := s.Collections(ctx)
	want := []Wanted{{"privat", "notizen"}, {"privat", "team-x"}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("Collections = %v, %v", got, err)
	}
	if err := s.RemoveCollection(ctx, "privat", "notizen"); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveCollection(ctx, "privat", "notizen"); !errors.Is(err, ErrNotFound) {
		t.Errorf("rm doppelt: %v", err)
	}
	// rm des Hubs entfernt Abhängiges.
	if err := s.RemoveHub(ctx, "privat"); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveHub(ctx, "privat"); !errors.Is(err, ErrNotFound) {
		t.Errorf("rm doppelt: %v", err)
	}
	if got, _ := s.Collections(ctx); len(got) != 0 {
		t.Errorf("Reste: %v", got)
	}
}

func TestCheckTables(t *testing.T) {
	tok := token(t)
	ok := Tables{
		Hubs:   []Hub{{Name: "a", NodeName: "laptop", Transport: "local", Token: tok}, {Name: "b", NodeName: "laptop", Transport: "https", Address: "https://h", Token: tok}},
		Wanted: []Wanted{{"a", "x"}},
	}
	if err := CheckTables(ok, true); err != nil {
		t.Fatal(err)
	}
	if err := CheckTables(ok, false); err == nil {
		t.Error("local ohne Hub in der config angenommen")
	}
	bad := []Tables{
		{Hubs: []Hub{ok.Hubs[0], {Name: "c", NodeName: "laptop", Transport: "local", Token: tok}}},
		{Hubs: []Hub{ok.Hubs[1], ok.Hubs[1]}},
		{Hubs: ok.Hubs, Wanted: []Wanted{{"x", "y"}}},
		{Hubs: ok.Hubs, Wanted: []Wanted{{"a", "Y"}}},
		{Hubs: ok.Hubs, Wanted: []Wanted{{"a", "x"}, {"a", "x"}}},
		{Hubs: []Hub{{Name: "a", NodeName: "laptop", Transport: "https", Address: "https://h", Token: "keph_x"}}},
		{Hubs: []Hub{{Name: "a", NodeName: "laptop", Transport: "https", Address: "https://h", Token: tok, HubID: "kaputt"}}},
		{Hubs: []Hub{{Name: "a", Transport: "https", Address: "https://h", Token: tok}}},
		{Hubs: []Hub{{Name: "a", NodeName: "System-x", Transport: "https", Address: "https://h", Token: tok}}},
	}
	for i, b := range bad {
		if err := CheckTables(b, true); err == nil {
			t.Errorf("Fall %d angenommen", i)
		}
	}
}

func TestReplicaPath(t *testing.T) {
	got := ReplicaPath("/daten/kephalaion/node.db", "privat")
	if want := filepath.Join("/daten/kephalaion", "replicas", "privat.db"); got != want {
		t.Errorf("ReplicaPath = %q, erwartet %q", got, want)
	}
}

func TestSetHubID(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	if err := s.AddHub(ctx, Hub{Name: "privat", NodeName: "laptop", Transport: "https",
		Address: "https://hub.example.org", Token: token(t)}, false); err != nil {
		t.Fatal(err)
	}
	h, err := s.Hub(ctx, "privat")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ulid.ParseStrict(h.EntryID); err != nil {
		t.Fatalf("entry_id %q: %v", h.EntryID, err)
	}
	id := ulid.Make().String()
	if err := s.SetHubID(ctx, "privat", h.EntryID, id); err != nil {
		t.Fatal(err)
	}
	if h, err := s.Hub(ctx, "privat"); err != nil || h.HubID != id {
		t.Errorf("Hub = %+v, %v", h, err)
	}
	if err := s.SetHubID(ctx, "privat", h.EntryID, "keine-ulid"); err == nil {
		t.Error("SetHubID ohne ULID ging durch")
	}
	if err := s.SetHubID(ctx, "fremd", h.EntryID, id); !errors.Is(err, ErrEntryGone) {
		t.Errorf("SetHubID unbekannt: %v", err)
	}
	// rm und add unter demselben Alias: neue entry_id; wer noch die alte
	// hält, schreibt nichts mehr.
	if err := s.RemoveHub(ctx, "privat"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddHub(ctx, Hub{Name: "privat", NodeName: "laptop", Transport: "https",
		Address: "https://hub.example.org", Token: token(t)}, false); err != nil {
		t.Fatal(err)
	}
	h2, err := s.Hub(ctx, "privat")
	if err != nil || h2.EntryID == h.EntryID {
		t.Fatalf("neuer Eintrag: %+v, %v", h2, err)
	}
	if err := s.SetHubID(ctx, "privat", h.EntryID, ulid.Make().String()); !errors.Is(err, ErrEntryGone) {
		t.Errorf("SetHubID mit alter entry_id: %v", err)
	}
	if h, _ := s.Hub(ctx, "privat"); h.HubID != "" {
		t.Errorf("alte entry_id schrieb hub_id %q", h.HubID)
	}
}

// TestRemoveHubRemovesReplica: node hub rm nimmt die Replica samt -wal und
// -shm mit; die Replicas anderer Einträge bleiben.
func TestRemoveHubRemovesReplica(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	for _, name := range []string{"privat", "team"} {
		if err := s.AddHub(ctx, Hub{Name: name, NodeName: "laptop", Transport: "https",
			Address: "https://hub.example.org", Token: token(t)}, false); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(s.ReplicaPath("privat")), 0o700); err != nil {
		t.Fatal(err)
	}
	var files []string
	for _, name := range []string{"privat", "team"} {
		for _, suffix := range []string{"", "-wal", "-shm"} {
			p := s.ReplicaPath(name) + suffix
			if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
			files = append(files, p)
		}
	}
	if err := s.RemoveHub(ctx, "privat"); err != nil {
		t.Fatal(err)
	}
	for _, p := range files {
		_, err := os.Stat(p)
		gone := errors.Is(err, os.ErrNotExist)
		if want := strings.Contains(p, "privat"); gone != want {
			t.Errorf("%s: entfernt = %v, erwartet %v", p, gone, want)
		}
	}
	// Ohne Replica geht rm ebenso.
	if err := s.RemoveHub(ctx, "team"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddHub(ctx, Hub{Name: "neu", NodeName: "laptop", Transport: "https",
		Address: "https://hub.example.org", Token: token(t)}, false); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveHub(ctx, "neu"); err != nil {
		t.Errorf("rm ohne Replica: %v", err)
	}
}

// TestImportRemovesStaleReplicas: config import ersetzt die Hub-Einträge;
// die Replicas der Aliase, die danach fehlen, gehen mit, die übrigen
// bleiben. Ohne Tabellen (nur settings) bleibt alles.
func TestImportRemovesStaleReplicas(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	hub := func(name string) Hub {
		return Hub{Name: name, NodeName: "laptop", Transport: "https", Address: "https://hub.example.org", Token: token(t)}
	}
	for _, name := range []string{"privat", "team"} {
		if err := s.AddHub(ctx, hub(name), false); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(s.ReplicaPath("privat")), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"privat", "team"} {
		for _, suffix := range []string{"", "-wal"} {
			if err := os.WriteFile(s.ReplicaPath(name)+suffix, []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	gone := func(name string) bool {
		_, err := os.Stat(s.ReplicaPath(name))
		return errors.Is(err, os.ErrNotExist)
	}

	if err := s.Import(ctx, map[string]string{}, nil, false); err != nil {
		t.Fatal(err)
	}
	if gone("privat") || gone("team") {
		t.Fatal("Import ohne Tabellen hat Replicas entfernt")
	}

	if err := s.Import(ctx, map[string]string{}, &Tables{Hubs: []Hub{hub("team"), hub("neu")}}, false); err != nil {
		t.Fatal(err)
	}
	if !gone("privat") {
		t.Error("Replica privat nach Import noch da")
	}
	if _, err := os.Stat(s.ReplicaPath("privat") + "-wal"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("privat-wal nach Import: %v", err)
	}
	if gone("team") {
		t.Error("Replica team entfernt, obwohl der Alias bleibt")
	}
}

// ParseCA liest ein oder mehrere Zertifikate; anderes ist ein Fehler, der
// sagt, was fehlt.
func TestParseCA(t *testing.T) {
	ca, ca2 := testcert.NewCA(t, "Eins"), testcert.NewCA(t, "Zwei")
	certs, err := ParseCA(ca.PEM + "\n" + ca2.PEM)
	if err != nil || len(certs) != 2 || certs[0].Subject.CommonName != "Eins" || certs[1].Subject.CommonName != "Zwei" {
		t.Errorf("ParseCA = %d Zertifikate, %v", len(certs), err)
	}
	for _, bad := range []struct{ name, text, want string }{
		{"leer", "", "kein Zertifikat"},
		{"Text", "hallo", "kein Zertifikat"},
		{"Schlüssel", "-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----\n", "nicht PRIVATE KEY"},
		{"kaputt", "-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n", "Zertifikat 1 nicht lesbar"},
		{"zweites kaputt", ca.PEM + "-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n", "Zertifikat 2 nicht lesbar"},
	} {
		if _, err := ParseCA(bad.text); err == nil || !strings.Contains(err.Error(), bad.want) {
			t.Errorf("%s: %v, erwartet %q", bad.name, err, bad.want)
		}
	}
}

// Die CA eines https-Eintrags: gespeichert als Text, geändert und entfernt
// mit set, verworfen beim Wechsel des Transports, im Import geprüft.
func TestHubCA(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	tok := token(t)
	ca, ca2 := testcert.NewCA(t, "Eins").PEM, testcert.NewCA(t, "Zwei").PEM
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := s.AddHub(ctx, Hub{Name: "fern", NodeName: "laptop", Transport: "https", Address: "https://h", CA: "x", Token: tok}, false); err == nil {
		t.Fatal("kaputte CA angenommen")
	}
	must(s.AddHub(ctx, Hub{Name: "fern", NodeName: "laptop", Transport: "https", Address: "https://h", CA: ca, Token: tok}, false))
	if h, _ := s.Hub(ctx, "fern"); h.CA != ca {
		t.Errorf("CA nach add: %q", h.CA)
	}
	if hubs, _ := s.Hubs(ctx); len(hubs) != 1 || hubs[0].CA != ca {
		t.Errorf("Hubs: %+v", hubs)
	}
	must(s.SetHub(ctx, "fern", HubUpdate{CA: ptr(ca2)}, false))
	if h, _ := s.Hub(ctx, "fern"); h.CA != ca2 {
		t.Errorf("CA nach set: %q", h.CA)
	}
	if err := s.SetHub(ctx, "fern", HubUpdate{CA: ptr("kaputt")}, false); err == nil {
		t.Error("kaputte CA per set angenommen")
	}
	if h, _ := s.Hub(ctx, "fern"); h.CA != ca2 {
		t.Errorf("nach Abweisung verändert: %q", h.CA)
	}
	// Adresse ändern lässt die CA stehen.
	must(s.SetHub(ctx, "fern", HubUpdate{Address: ptr("https://h2")}, false))
	if h, _ := s.Hub(ctx, "fern"); h.CA != ca2 || h.Address != "https://h2" {
		t.Errorf("nach Adresse: %+v", h)
	}
	// Wechsel auf ssh verwirft sie, zurück auf https bleibt sie weg.
	must(s.SetHub(ctx, "fern", HubUpdate{Transport: ptr("ssh"), Address: ptr("h")}, false))
	if h, _ := s.Hub(ctx, "fern"); h.CA != "" || h.Transport != "ssh" {
		t.Errorf("nach Wechsel auf ssh: %+v", h)
	}
	must(s.SetHub(ctx, "fern", HubUpdate{Transport: ptr("https"), Address: ptr("https://h")}, false))
	if h, _ := s.Hub(ctx, "fern"); h.CA != "" {
		t.Errorf("CA nach Rückwechsel: %q", h.CA)
	}
	// Leer entfernt sie; bei einem anderen Transport wird sie abgewiesen.
	must(s.SetHub(ctx, "fern", HubUpdate{CA: ptr(ca)}, false))
	must(s.SetHub(ctx, "fern", HubUpdate{CA: ptr("")}, false))
	if h, _ := s.Hub(ctx, "fern"); h.CA != "" {
		t.Errorf("CA nach Entfernen: %q", h.CA)
	}
	if err := s.SetHub(ctx, "fern", HubUpdate{Transport: ptr("ssh"), Address: ptr("h"), CA: ptr(ca)}, false); err == nil ||
		!strings.Contains(err.Error(), "nur bei Transport https") {
		t.Errorf("CA bei ssh: %v", err)
	}
	// Wechsel auf https mit CA in einem Zug.
	must(s.SetHub(ctx, "fern", HubUpdate{Transport: ptr("ssh"), Address: ptr("h")}, false))
	must(s.SetHub(ctx, "fern", HubUpdate{Transport: ptr("https"), Address: ptr("https://h"), CA: ptr(ca)}, false))
	if h, _ := s.Hub(ctx, "fern"); h.CA != ca {
		t.Errorf("CA nach Wechsel auf https: %q", h.CA)
	}

	// Import: geprüft wie die CLI, gespeichert wie add.
	tables := Tables{Hubs: []Hub{{Name: "a", NodeName: "laptop", Transport: "https", Address: "https://h", CA: ca2, Token: tok}}}
	must(s.Import(ctx, map[string]string{}, &tables, false))
	if got, _ := s.Tables(ctx); len(got.Hubs) != 1 || got.Hubs[0].CA != ca2 {
		t.Errorf("Tables nach Import: %+v", got)
	}
	bad := Tables{Hubs: []Hub{{Name: "a", NodeName: "laptop", Transport: "http", Address: "http://localhost:1", CA: ca, Token: tok}}}
	if err := s.Import(ctx, map[string]string{}, &bad, false); err == nil {
		t.Error("Import mit CA bei http angenommen")
	}
	if got, _ := s.Tables(ctx); len(got.Hubs) != 1 || got.Hubs[0].CA != ca2 {
		t.Errorf("Tables nach abgewiesenem Import: %+v", got)
	}
}
