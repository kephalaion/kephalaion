package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kephalaion/kephalaion/internal/config"
	"github.com/kephalaion/kephalaion/internal/contract"
	"github.com/kephalaion/kephalaion/internal/hub/replication"
	hubstore "github.com/kephalaion/kephalaion/internal/hub/store"
	"github.com/kephalaion/kephalaion/internal/ident"
)

// syncSetup richtet Hub und Node in einer config ein: Collections wissen und
// team am Hub, Node laptop mit Recht auf wissen, am Node der Hub-Eintrag eigen
// über local, der beide will. Liefert die Option --config und die hub_id.
func syncSetup(t *testing.T, dir string) (c, hubID string) {
	t.Helper()
	cfg := setup(t, dir)
	c = "--config=" + cfg
	runT(t, "hub", "collection", "add", "wissen", c).want(t, 0)
	runT(t, "hub", "collection", "add", "team", c).want(t, 0)
	r := runT(t, "hub", "node", "add", "laptop", c)
	r.want(t, 0)
	tok := tokenFrom(t, r.out)
	runT(t, "hub", "node", "grant", "laptop", "wissen", c).want(t, 0)
	runIn(t, tok+"\n", "node", "hub", "add", "eigen", "--node", "laptop", "--transport", "local", "--token-stdin", c).
		want(t, 0)
	runT(t, "node", "collection", "add", "eigen:wissen", c).want(t, 0)
	runT(t, "node", "collection", "add", "eigen:team", c).want(t, 0)
	info, err := hubStore(t, cfg).Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return c, info.HubID
}

func TestNodeSyncFlow(t *testing.T) {
	dir := isolate(t)
	c, hubID := syncSetup(t, dir)
	src := filepath.Join(dir, "quelle")
	writeFile(t, filepath.Join(src, "a.md"), "A\n")
	writeFile(t, filepath.Join(src, "sub", "b.md"), "---\ntitel: b\n---\nB\n")
	runT(t, "hub", "import", "wissen", src, c).want(t, 0, "Revision 1")

	// Vor dem ersten Abgleich: keine Replica.
	runT(t, "status", c).want(t, 0, "eigen: local, als Node laptop\n      hub_id:      noch kein Abgleich",
		"Collections: team, wissen")
	runT(t, "node", "doc", "list", "eigen:wissen", c).
		want(t, 1, "Hub eigen: noch kein Abgleich; zuerst: kephalaion node sync eigen")
	runT(t, "node", "doc", "get", "eigen:wissen", "a.md", c).want(t, 1, "noch kein Abgleich")
	runT(t, "node", "doc", "list", "fehlt:wissen", c).want(t, 1, "Hub fehlt gibt es nicht")

	runT(t, "node", "sync", c).want(t, 0, "Hub eigen (hub_id "+hubID+"): 1 Seite",
		"wissen: abgeglichen, 2 Zeilen, Revision 1", "team: nicht erlaubt\n")

	r := runT(t, "node", "doc", "list", "eigen:wissen", c)
	r.want(t, 0, "NAME", "a.md", "sub/", "von admin")
	if strings.Contains(r.out, "b.md") {
		t.Errorf("list zeigt Unterverzeichnis-Inhalt:\n%s", r.out)
	}
	runT(t, "node", "doc", "list", "eigen:wissen", "sub", c).want(t, 0, "b.md")
	runT(t, "node", "doc", "list", "eigen:wissen", "leer", c).want(t, 0, "Keine Dokumente unter leer/ in eigen:wissen.")
	r = runT(t, "node", "doc", "get", "eigen:wissen", "sub/b.md", c)
	r.want(t, 0)
	if r.out != "---\ntitel: b\n---\nB\n" {
		t.Errorf("get = %q", r.out)
	}
	runT(t, "node", "doc", "get", "eigen:team", "a.md", c).want(t, 1, "Collection team gibt es in der Replica nicht")
	runT(t, "node", "doc", "get", "eigen:wissen", "fehlt.md", c).want(t, 1, "gibt es in der Replica nicht")
	runT(t, "node", "doc", "get", "eigen:wissen", "../a", c).want(t, 1, "'..'")
	runT(t, "node", "doc", "get", "ohne-doppelpunkt", "a.md", c).want(t, 1, "<hub>:<collection>")
	runT(t, "node", "doc", "get", "eigen:wissen", c).want(t, 2, "Es fehlt: <name>")

	runT(t, "status", c).want(t, 0, "      hub_id:      "+hubID, "        wissen: Revision 1, abgeglichen ",
		"        team: noch nicht abgeglichen")

	// Löschen am Hub: Die Löschmarke kommt an, das Dokument verschwindet.
	runT(t, "hub", "doc", "rm", "wissen", "a.md", c).want(t, 0, "Revision 2")
	runT(t, "node", "sync", "eigen", c).want(t, 0, "wissen: abgeglichen, 1 Zeile, Revision 2")
	runT(t, "node", "doc", "get", "eigen:wissen", "a.md", c).want(t, 1, "gibt es in der Replica nicht")
	r = runT(t, "node", "doc", "list", "eigen:wissen", c)
	r.want(t, 0, "sub/")
	if strings.Contains(r.out, "a.md") {
		t.Errorf("list nach rm:\n%s", r.out)
	}
	runT(t, "node", "sync", c).want(t, 0, "wissen: abgeglichen, 0 Zeilen, Revision 2")
	runT(t, "status", c).want(t, 0, "wissen: Revision 2")

	// Am Hub erlaubt, am Node nicht mehr gewünscht: aus der Replica entfernt.
	runT(t, "node", "collection", "rm", "eigen:wissen", c).want(t, 0)
	runT(t, "status", c).want(t, 0, "wissen: nicht mehr gewünscht, Revision 2")
	runT(t, "node", "sync", c).want(t, 0, "wissen: nicht mehr gewünscht, 2 Zeilen aus der Replica entfernt",
		"vom Hub außerdem erlaubt: wissen")
	runT(t, "node", "doc", "list", "eigen:wissen", c).want(t, 1, "Collection wissen gibt es in der Replica nicht")

	runT(t, "node", "sync", "fehlt", c).want(t, 1, "Hub fehlt gibt es nicht")
	runT(t, "node", "sync", "a", "b", c).want(t, 2, "Unerwartetes Argument: b")

	// node hub rm nimmt die Replica mit.
	runT(t, "node", "hub", "rm", "eigen", c).want(t, 0)
	runT(t, "node", "sync", c).want(t, 0, "Keine Hubs.")
}

// Scheitert ein Eintrag, laufen die übrigen weiter; der Exit-Code ist 1.
func TestNodeSyncPerEntryErrors(t *testing.T) {
	dir := isolate(t)
	c, hubID := syncSetup(t, dir)
	runIn(t, "x", "hub", "doc", "put", "wissen", "a.md", c).want(t, 0)
	tok, err := ident.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	runIn(t, tok, "node", "hub", "add", "fern", "--node", "laptop", "--transport", "https",
		"--address", "https://hub.example.org", "--token-stdin", c).want(t, 0)
	runT(t, "node", "collection", "add", "fern:wissen", c).want(t, 0)

	r := runT(t, "node", "sync", c)
	r.want(t, 1, "Hub eigen (hub_id "+hubID+"): 1 Seite", "wissen: abgeglichen, 1 Zeile, Revision 1",
		"Hub fern: gescheitert")
	if !strings.Contains(r.errOut, "node sync: Hub fern: Transport https wird noch nicht unterstützt") ||
		strings.Contains(r.errOut, "eigen") {
		t.Errorf("stderr:\n%s", r.errOut)
	}
	runT(t, "node", "doc", "get", "eigen:wissen", "a.md", c).want(t, 0, "x")
	runT(t, "node", "sync", "eigen", c).want(t, 0)

	// Gesperrt: Fehler dieses Eintrags, die Replica bleibt lesbar.
	runT(t, "hub", "node", "lock", "laptop", c).want(t, 0)
	runT(t, "node", "sync", "eigen", c).want(t, 1, "Hub eigen (hub_id "+hubID+"): gescheitert",
		"team: nicht abgeglichen", "node sync: Hub eigen: nicht angemeldet")
	runT(t, "node", "doc", "get", "eigen:wissen", "a.md", c).want(t, 0, "x")
	runT(t, "hub", "node", "unlock", "laptop", c).want(t, 0)

	// local ohne Abschnitt hub: in der config — Fehler für diesen Eintrag,
	// der andere kommt trotzdem dran.
	cfgPath := strings.TrimPrefix(c, "--config=")
	cfg, _, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.SetSection(config.Hub, nil)
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	r = runT(t, "node", "sync", c)
	r.want(t, 1, "node sync: Hub eigen: Transport local verlangt einen Hub in derselben config",
		"node sync: Hub fern: Transport https wird noch nicht unterstützt")
	// Die Replica bleibt; status zeigt ihren Stand.
	runT(t, "status", c).want(t, 0, "hub_id:      "+hubID, "wissen: Revision 1", "fern: https https://hub.example.org")
}

func TestNodeSyncUsage(t *testing.T) {
	isolate(t)
	runT(t, "node", "--help").want(t, 0, "kephalaion node sync", "node doc list|get")
	runT(t, "node", "sync", "--help").want(t, 0, "noch nicht unterstützt", "Exit-Code ist dann 1")
	runT(t, "node", "doc", "--help").want(t, 0, "node doc list", "SYSTEM:")
	runT(t, "node", "doc").want(t, 2, "kephalaion node doc get")
	runT(t, "node", "doc", "rm").want(t, 2, "Unbekanntes Kommando: node doc rm")
	runT(t, "help").want(t, 0, "sync, doc")
	runT(t, "node", "sync").want(t, 1, "der Node ist nicht eingerichtet")
}

// node sync hält Erfolg und Fehler je Hub fest; status zeigt beides, ein
// Erfolg leert den Fehler. Der Stand steht nicht im Export.
func TestNodeSyncRecordsStatus(t *testing.T) {
	dir := isolate(t)
	c, _ := syncSetup(t, dir)
	cfgPath := strings.TrimPrefix(c, "--config=")
	runT(t, "status", c).want(t, 0, "  Abgleich:      im Hintergrund alle 30s (Standard)",
		"      Abgleich:    noch keiner festgehalten")

	runT(t, "node", "sync", c).want(t, 0)
	r := runT(t, "status", c)
	r.want(t, 0, "      Abgleich:    zuletzt gelungen ")
	if strings.Contains(r.out, "gescheitert") {
		t.Errorf("status nach Erfolg:\n%s", r.out)
	}

	runT(t, "hub", "node", "lock", "laptop", c).want(t, 0)
	runT(t, "node", "sync", c).want(t, 1)
	runT(t, "status", c).want(t, 0, "      Abgleich:    zuletzt gelungen ", "; gescheitert ", ": nicht angemeldet")
	st, err := nodeStore(t, cfgPath).SyncStatus(context.Background())
	if err != nil || st["eigen"].ErrKind != "unauthenticated" || st["eigen"].OKAt == 0 || st["eigen"].ErrAt == 0 {
		t.Errorf("hub_sync: %+v, %v", st, err)
	}

	runT(t, "hub", "node", "unlock", "laptop", c).want(t, 0)
	runT(t, "node", "sync", c).want(t, 0)
	st, _ = nodeStore(t, cfgPath).SyncStatus(context.Background())
	if st["eigen"].Err != "" || st["eigen"].ErrKind != "" || st["eigen"].ErrAt != 0 {
		t.Errorf("Fehler nach Erfolg: %+v", st["eigen"])
	}

	exp := runT(t, "config", "export", c)
	exp.want(t, 0)
	for _, not := range []string{"hub_sync", "ok_at", "error", "entry_id"} {
		if strings.Contains(exp.out, not) {
			t.Errorf("Export enthält %q:\n%s", not, exp.out)
		}
	}
}

func TestConfigSetUnset(t *testing.T) {
	dir := isolate(t)
	cfgPath := setup(t, dir)
	c := "--config=" + cfgPath
	runT(t, "config", "set", "node", "sync_interval", "5s", c).want(t, 0, "node sync_interval = 5s")
	runT(t, "config", "show", c).want(t, 0, "settings node:\n  sync_interval = 5s")
	runT(t, "status", c).want(t, 0, "Abgleich:      im Hintergrund alle 5s")
	runT(t, "config", "set", "node", "sync_interval", "0", c).want(t, 0)
	runT(t, "status", c).want(t, 0, "Abgleich:      im Hintergrund aus (sync_interval 0)")

	for _, bad := range []struct {
		args []string
		msg  string
	}{
		{[]string{"node", "sync_intervall", "5s"}, `unbekannter Schlüssel "sync_intervall"`},
		{[]string{"node", "sync_interval", "bald"}, `"bald" ist keine Dauer`},
		{[]string{"node", "sync_interval", "500ms"}, "kürzer als 1s"},
		{[]string{"hub", "sync_interval", "5s"}, "die Rolle hub kennt noch keine"},
		{[]string{"beide", "sync_interval", "5s"}, `unbekannte Rolle "beide"`},
	} {
		runT(t, append(append([]string{"config", "set"}, bad.args...), c)...).want(t, 1, bad.msg)
	}
	runT(t, "config", "set", "node", "sync_interval", c).want(t, 2, "Es fehlt: <wert>")
	runT(t, "config", "unset", "node", "fremd", c).want(t, 1, "unbekannter Schlüssel")
	if got := getSettings(t, cfgPath, config.Node); got["sync_interval"] != "0" || len(got) != 1 {
		t.Errorf("settings nach Fehlgriffen: %v", got)
	}

	runT(t, "config", "unset", "node", "sync_interval", c).want(t, 0, "entfernt; es gilt der Standard")
	runT(t, "config", "unset", "node", "sync_interval", c).want(t, 0, "war nicht gesetzt")
	runT(t, "status", c).want(t, 0, "Abgleich:      im Hintergrund alle 30s (Standard)")
	runT(t, "config", "--help").want(t, 0, "config set", "config unset")
	runT(t, "config", "set", "--help").want(t, 0, "sync_interval", "0 schaltet ihn ab")
}

// failWriteAfterCommit führt Schreibvorgänge am echten Hub aus und meldet
// danach einen gewöhnlichen Fehler — wie eine Datenbank, die nach dem Commit
// ausfällt. Einen Fehler des Vertrags reicht es durch.
type failWriteAfterCommit struct {
	contract.Hub
}

var errDBGone = errors.New("Datenbank weg")

func (f failWriteAfterCommit) Create(ctx context.Context, req contract.CreateRequest) (contract.WriteResponse, error) {
	if _, err := f.Hub.Create(ctx, req); err != nil {
		return contract.WriteResponse{}, err
	}
	return contract.WriteResponse{}, errDBGone
}

func (f failWriteAfterCommit) Write(ctx context.Context, req contract.WriteRequest) (contract.WriteResponse, error) {
	if _, err := f.Hub.Write(ctx, req); err != nil {
		return contract.WriteResponse{}, err
	}
	return contract.WriteResponse{}, errDBGone
}

func (f failWriteAfterCommit) Delete(ctx context.Context, req contract.DeleteRequest) (contract.WriteResponse, error) {
	if _, err := f.Hub.Delete(ctx, req); err != nil {
		return contract.WriteResponse{}, err
	}
	return contract.WriteResponse{}, errDBGone
}

// Über local ist ein Fehler eines Schreibvorgangs, der kein Fehler des
// Vertrags ist, ein unklarer Ausgang — der Hub kann geschrieben haben, wie
// über HTTP bei 500. Ein Fehler des Vertrags bleibt eindeutig.
func TestLocalWriteOutcome(t *testing.T) {
	e := newCommEnv(t)
	old := newLocalHub
	newLocalHub = func(st hubstore.Store) contract.Hub { return failWriteAfterCommit{replication.New(st)} }
	t.Cleanup(func() { newLocalHub = old })

	ctx := context.Background()
	cfg, _, err := config.Load(e.cfg)
	if err != nil {
		t.Fatal(err)
	}
	h, err := nodeStore(t, e.cfg).Hub(ctx, "eigen")
	if err != nil {
		t.Fatal(err)
	}
	hub, closeHub, err := connectHub(ctx, cfg, h)
	if err != nil {
		t.Fatal(err)
	}
	defer closeHub()
	node := contract.NodeAuth{Node: h.NodeName, Token: h.Token}
	acc := contract.AccountAuth{Account: "bob", Token: e.tokens["bob"]}
	hs := hubStore(t, e.cfg)
	unclear := func(what string, err error) {
		t.Helper()
		var ce *contract.Error
		if !errors.Is(err, contract.ErrOutcomeUnknown) || errors.As(err, &ce) || !strings.Contains(err.Error(), "Datenbank weg") {
			t.Errorf("%s: %v, erwartet unklaren Ausgang", what, err)
		}
	}

	_, err = hub.Create(ctx, contract.CreateRequest{Version: contract.Version, Auth: node, Account: acc,
		Collection: "team-x", Name: "neu.md", Content: "eins"})
	unclear("create", err)
	doc, err := hs.Document(ctx, "team-x", "neu.md")
	if err != nil || doc.Content != "eins" {
		t.Fatalf("am Hub: %+v, %v", doc, err)
	}
	_, err = hub.Write(ctx, contract.WriteRequest{Version: contract.Version, Auth: node, Account: acc,
		Collection: "team-x", Name: "neu.md", Content: "zwei", BaseRevision: &doc.Revision})
	unclear("write", err)
	_, err = hub.Delete(ctx, contract.DeleteRequest{Version: contract.Version, Auth: node, Account: acc,
		Collection: "team-x", Name: "neu.md"})
	unclear("delete", err)
	if _, err := hs.Document(ctx, "team-x", "neu.md"); !errors.Is(err, hubstore.ErrNotFound) {
		t.Errorf("nach delete: %v", err)
	}

	// Ein Fehler des Vertrags ist eindeutig, auch über local.
	_, err = hub.Delete(ctx, contract.DeleteRequest{Version: contract.Version, Auth: node, Account: acc,
		Collection: "team-x", Name: "neu.md"})
	if !errors.Is(err, contract.ErrNotFound) || errors.Is(err, contract.ErrOutcomeUnknown) {
		t.Errorf("not_found: %v", err)
	}
	// Ein abgebrochener Aufruf kann nach dem Commit abgebrochen sein.
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = hub.Create(canceled, contract.CreateRequest{Version: contract.Version, Auth: node, Account: acc,
		Collection: "team-x", Name: "abgebrochen.md"})
	if !errors.Is(err, contract.ErrOutcomeUnknown) {
		t.Errorf("abgebrochen: %v", err)
	}
}
