package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kephalaion/kephalaion/internal/assistant"
	"github.com/kephalaion/kephalaion/internal/config"
	"github.com/kephalaion/kephalaion/internal/contract"
	"github.com/kephalaion/kephalaion/internal/hub/replication"
	hubstore "github.com/kephalaion/kephalaion/internal/hub/store"
	"github.com/kephalaion/kephalaion/internal/ident"
	"github.com/kephalaion/kephalaion/internal/node/mcpnode"
	nodestore "github.com/kephalaion/kephalaion/internal/node/store"
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
	runIn(t, tok, "node", "hub", "add", "fern", "--node", "laptop", "--transport", "ssh",
		"--address", "keph@hub.example.org", "--token-stdin", c).want(t, 0)
	runT(t, "node", "collection", "add", "fern:wissen", c).want(t, 0)

	r := runT(t, "node", "sync", c)
	r.want(t, 1, "Hub eigen (hub_id "+hubID+"): 1 Seite", "wissen: abgeglichen, 1 Zeile, Revision 1",
		"Hub fern: gescheitert")
	if !strings.Contains(r.errOut, "node sync: Hub fern: Transport ssh wird noch nicht unterstützt") ||
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
		"node sync: Hub fern: Transport ssh wird noch nicht unterstützt")
	// Die Replica bleibt; status zeigt ihren Stand.
	runT(t, "status", c).want(t, 0, "hub_id:      "+hubID, "wissen: Revision 1", "fern: ssh keph@hub.example.org")
}

func TestNodeSyncUsage(t *testing.T) {
	isolate(t)
	runT(t, "node", "--help").want(t, 0, "kephalaion node sync", "node doc list|get")
	runT(t, "node", "sync", "--help").want(t, 0, "https", "--ca-file", "noch nicht unterstützt", "Exit-Code ist dann 1")
	// Als Client des Nodes (Task 042): die Optionen, wann es über MCP geht,
	// und was dort ohne Alias gilt.
	runT(t, "node", "sync", "--help").want(t, 0, "--node url", "--account name", "--token-file pfad", "--token-stdin",
		"reinen Client", "über MCP", "den einen Hub, für den unter <config-dir>/tokens/", "falscher Aufruf (Exit 2)",
		"nennt die Aliase", "40 s", "am Rechner des Nodes")
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
		{[]string{"hub", "sync_interval", "5s"}, "unbekannter Schlüssel"},
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

func (f failWriteAfterCommit) Rename(ctx context.Context, req contract.RenameRequest) (contract.WriteResponse, error) {
	if _, err := f.Hub.Rename(ctx, req); err != nil {
		return contract.WriteResponse{}, err
	}
	return contract.WriteResponse{}, errDBGone
}

func (f failWriteAfterCommit) Fetch(ctx context.Context, req contract.FetchRequest) (contract.FetchResponse, error) {
	if _, err := f.Hub.Fetch(ctx, req); err != nil {
		return contract.FetchResponse{}, err
	}
	return contract.FetchResponse{}, errDBGone
}

func (f failWriteAfterCommit) RebuildDocs(ctx context.Context, req contract.RebuildDocsRequest) (contract.RebuildDocsResponse, error) {
	if _, err := f.Hub.RebuildDocs(ctx, req); err != nil {
		return contract.RebuildDocsResponse{}, err
	}
	return contract.RebuildDocsResponse{}, errDBGone
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
	_, err = hub.Rename(ctx, contract.RenameRequest{Version: contract.Version, Auth: node, Account: acc,
		Collection: "team-x", Name: "neu.md", NewName: "dir/neu.md"})
	unclear("rename", err)
	if doc, err := hs.Document(ctx, "team-x", "dir/neu.md"); err != nil || doc.Content != "zwei" {
		t.Fatalf("nach rename: %+v, %v", doc, err)
	}
	_, err = hub.Delete(ctx, contract.DeleteRequest{Version: contract.Version, Auth: node, Account: acc,
		Collection: "team-x", Name: "dir", Recursive: true})
	unclear("delete", err)
	if _, err := hs.Document(ctx, "team-x", "dir/neu.md"); !errors.Is(err, hubstore.ErrNotFound) {
		t.Errorf("nach delete: %v", err)
	}

	// Ein Fehler des Vertrags ist eindeutig, auch über local.
	_, err = hub.Delete(ctx, contract.DeleteRequest{Version: contract.Version, Auth: node, Account: acc,
		Collection: "team-x", Name: "dir/neu.md"})
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

// clientEnv ist commEnv mit laufendem serve und der config eines reinen
// Clients (Task 042): ein Abschnitt node: mit dem listen von serve, dessen
// Datenbank samt Verzeichnis hier fehlt — wie im Devcontainer mit der config
// des Hosts. Die Token-Dateien liegen unter tokens/ neben der config des
// Users.
type clientEnv struct {
	*commEnv
	srv     *running
	nodeURL string
	// client ist die Option --config des reinen Clients.
	client string
	tokens string
}

func newClientEnv(t *testing.T) *clientEnv {
	t.Helper()
	e := &clientEnv{commEnv: newCommEnv(t)}
	e.run(t, "node", "sync").want(t, 0)
	// Keine Runden: Nur der angeforderte Abgleich gleicht ab.
	e.run(t, "config", "set", "node", "sync_interval", "0").want(t, 0)
	e.srv = startServe(t, portZero(t, e.cfg))
	e.nodeURL = "http://" + e.srv.addrs[config.Node]
	gone := filepath.Join(e.dir, "host", "var", "lib", "kephalaion")
	path := filepath.Join(e.dir, "client.yaml")
	if err := config.Save(path, config.Config{Node: &config.Section{DB: "sqlite://" + filepath.Join(gone, "node.db"),
		Listen: e.srv.addrs[config.Node]}}); err != nil {
		t.Fatal(err)
	}
	e.client = "--config=" + path
	var err error
	if e.tokens, err = assistant.TokensDir(); err != nil {
		t.Fatal(err)
	}
	return e
}

// token legt die Token-Datei eines Accounts für einen Hub unter tokens/ an.
func (e *clientEnv) token(t *testing.T, hub, account string) {
	t.Helper()
	dir := filepath.Join(e.tokens, hub)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, account+".token"), []byte(e.commEnv.tokens[account]+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// noToken prüft, dass eine Ausgabe kein Token nennt.
func noToken(t *testing.T, r result) result {
	t.Helper()
	if strings.Contains(r.out+r.errOut, "keph_") {
		t.Errorf("Token in der Ausgabe:\n%s%s", r.out, r.errOut)
	}
	return r
}

// node sync auf einem reinen Client (Task 042): Statt „Datenbankdatei fehlt“
// fordert es den Abgleich beim Node an und endet mit Exit 0, wenn der
// abgeglichen hat — mit Alias, und ohne Alias mit genau einem Hub unter
// tokens/. Bei mehreren oder keinem Hub ist es ein falscher Aufruf.
func TestNodeSyncPureClient(t *testing.T) {
	slow(t, "serve mit Hub und Node, mehrere angeforderte Abgleiche über MCP")
	e := newClientEnv(t)
	e.token(t, "fern", "alice")

	// Mit Alias: Was am Hub neu ist, steht danach in der Replica des Nodes.
	e.runIn(t, "eins", "hub", "doc", "put", "team-x", "a.md").want(t, 0)
	if e.docIs(t, "fern:team-x", "a.md", "eins") {
		t.Fatal("a.md schon vor dem Abgleich da (sync_interval 0)")
	}
	r := noToken(t, runT(t, "node", "sync", "fern", e.client))
	r.want(t, 0, "Hub fern: abgeglichen ", ", Revision ", "(über den Node "+e.nodeURL+")")
	if r.errOut != "" || !e.docIs(t, "fern:team-x", "a.md", "eins") {
		t.Errorf("nach dem Abgleich: a.md fehlt oder stderr %q", r.errOut)
	}
	if !logLine(e.srv.log.String(), "op=sync", "hub=fern", "account=alice") {
		t.Errorf("Zeile der Anfrage:\n%s", e.srv.log.String())
	}

	// Ohne Alias, genau ein Hub unter tokens/: dieser.
	e.runIn(t, "zwei", "hub", "doc", "put", "team-x", "a.md").want(t, 0)
	noToken(t, runT(t, "node", "sync", e.client)).want(t, 0, "Hub fern: abgeglichen ")
	if !e.docIs(t, "fern:team-x", "a.md", "zwei") {
		t.Error("ohne Alias: a.md nicht abgeglichen")
	}

	// Ohne Token-Datei für den Hub, und --account ohne Token-Datei: Fehler
	// der Anmeldung wie bei node dir, ohne Rückfall.
	noToken(t, runT(t, "node", "sync", "fremd", e.client)).want(t, 1, "keine Token-Datei unter", filepath.Join("tokens", "fremd"))
	noToken(t, runT(t, "node", "sync", "fern", "--account", "bob", e.client)).want(t, 1, "bob.token")
	// Ein Fehler des Werkzeugs: Exit 1 mit Code und Meldung. Ein Alias, den
	// der Node nicht kennt, ist unknown_hub — nie ein anderer Hub.
	bobFile := e.tokenFile(t, "bob", e.commEnv.tokens["bob"])
	r = noToken(t, runT(t, "node", "sync", "fremd", "--account", "bob", "--token-file", bobFile, e.client))
	r.want(t, 1, "node sync: unknown_hub: Hub fremd: dieser Node hat keinen Hub-Eintrag mit diesem Alias")
	if r.out != "" {
		t.Errorf("Fehler des Werkzeugs mit Ausgabe: %q", r.out)
	}
	// Angemeldet mit einem Token, das am Hub nicht gilt: not_readable.
	noToken(t, runT(t, "node", "sync", "fern", "--account", "niemand", "--token-file", bobFile, e.client)).want(t, 1,
		"node sync: not_readable: Hub fern")
	// --token-stdin.
	noToken(t, runIn(t, e.commEnv.tokens["bob"]+"\n", "node", "sync", "fern", "--account", "bob", "--token-stdin",
		e.client)).want(t, 0, "Hub fern: abgeglichen ")
}

// Ohne Alias gleicht node sync als Client genau einen Hub ab: Liegen unter
// tokens/ mehrere oder keiner, ist es ein falscher Aufruf, der die Aliase
// nennt — ohne den Node zu fragen. Falsche Optionen der Anmeldung ebenso.
func TestNodeSyncPureClientUsage(t *testing.T) {
	e := newClientEnv(t)
	e.token(t, "fern", "alice")
	e.token(t, "eigen", "alice")
	bobFile := e.tokenFile(t, "bob", e.commEnv.tokens["bob"])
	noToken(t, runT(t, "node", "sync", e.client)).want(t, 2, "genau einen Hub", "mehrere: eigen, fern",
		"kephalaion node sync <alias>")
	// Falsche Optionen der Anmeldung; --ca-file ohne --node ist ein Fehler der
	// gemeinsamen Funktion, wie bei node dir.
	runT(t, "node", "sync", "fern", "--token-file", bobFile, e.client).want(t, 2, "--account fehlt")
	runT(t, "node", "sync", "fern", "--account", "bob", "--token-file", bobFile, "--token-stdin", e.client).want(t, 2,
		"schließen sich aus")
	runT(t, "node", "sync", "Fern", e.client).want(t, 2, "ungültiger Name")
	runT(t, "node", "sync", "fern", "--ca-file", bobFile, e.client).want(t, 1, "--ca-file nur zusammen mit --node")
	// Kein Hub unter tokens/.
	if err := os.RemoveAll(e.tokens); err != nil {
		t.Fatal(err)
	}
	noToken(t, runT(t, "node", "sync", e.client)).want(t, 2, "genau einen Hub", "liegt keiner")
	if strings.Contains(e.srv.log.String(), "op=sync") {
		t.Errorf("ein falscher Aufruf hat den Node gefragt:\n%s", e.srv.log.String())
	}
	// Mit Alias geht es: Exit 0 statt „Datenbankdatei fehlt“ (ausführlich in
	// TestNodeSyncPureClient).
	e.token(t, "fern", "alice")
	noToken(t, runT(t, "node", "sync", "fern", e.client)).want(t, 0, "Hub fern: abgeglichen ", "(über den Node ")
}

// node sync mit --node geht immer über MCP: auf einem Client ohne eigenen
// Node (kein Abschnitt node: in der config) und auf dem Rechner des Nodes
// selbst. Ohne --node bleibt dort alles, wie es war.
func TestNodeSyncNodeOption(t *testing.T) {
	slow(t, "serve mit Hub und Node, mehrere angeforderte Abgleiche über MCP")
	e := newClientEnv(t)
	aliceFile := e.tokenFile(t, "alice", e.commEnv.tokens["alice"])
	login := []string{"--node", e.nodeURL, "--account", "alice", "--token-file", aliceFile}

	// Ein Client ohne eigenen Node: eine config ohne Abschnitt node:, und gar
	// keine config.
	hubOnly := filepath.Join(e.dir, "hub-only.yaml")
	cfg, _, err := config.Load(e.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := config.Save(hubOnly, config.Config{Hub: cfg.Hub}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []string{"--config=" + hubOnly, "--config=" + filepath.Join(e.dir, "fehlt.yaml")} {
		noToken(t, runT(t, append([]string{"node", "sync", "fern", c}, login...)...)).want(t, 0,
			"Hub fern: abgeglichen ", "(über den Node "+e.nodeURL+")")
	}
	// Ohne --node und ohne Node in der config: der bisherige Fehler.
	runT(t, "node", "sync", "fern", "--config="+hubOnly).want(t, 1, "der Node ist nicht eingerichtet")

	// Auf dem Rechner des Nodes: mit --node über MCP — ein Hub, mit Stand.
	e.runIn(t, "drei", "hub", "doc", "put", "team-x", "a.md").want(t, 0)
	before := e.srv.log.String()
	noToken(t, e.run(t, append([]string{"node", "sync", "eigen"}, login...)...)).want(t, 0,
		"Hub eigen: abgeglichen ", "(über den Node "+e.nodeURL+")")
	if !e.docIs(t, "eigen:team-x", "a.md", "drei") || e.docIs(t, "fern:team-x", "a.md", "drei") {
		t.Error("mit --node: genau eigen sollte abgeglichen sein")
	}
	if !logLine(strings.TrimPrefix(e.srv.log.String(), before), "op=sync", "hub=eigen") {
		t.Errorf("mit --node kein Aufruf des Werkzeugs:\n%s", e.srv.log.String())
	}
	// Ohne --node wie bisher: gegen node.db, alle Einträge, ohne den Node zu
	// fragen.
	before = e.srv.log.String()
	e.run(t, "node", "sync").want(t, 0, "Hub eigen (hub_id ", "Hub fern (hub_id ", "team-x: abgeglichen")
	if strings.Contains(strings.TrimPrefix(e.srv.log.String(), before), "op=sync") {
		t.Errorf("ohne --node ein Aufruf des Werkzeugs:\n%s", e.srv.log.String())
	}
}

// Auf dem Rechner des Nodes, ohne --node, sind die Optionen der Anmeldung ein
// falscher Aufruf — gemeldet, bevor node.db geöffnet wird. Fehlt nur die
// Datei der Datenbank, das Verzeichnis aber nicht, ist der Rechner kein
// reiner Client: Es bleibt der bisherige Fehler.
func TestNodeSyncLocalOptions(t *testing.T) {
	e := newCommEnv(t)
	aliceFile := e.tokenFile(t, "alice", e.tokens["alice"])
	for want, args := range map[string][]string{
		"--account gilt nur":               {"--account", "alice"},
		"--token-file gilt nur":            {"--token-file", aliceFile},
		"--token-stdin gilt nur":           {"--token-stdin"},
		"--ca-file gilt nur":               {"--ca-file", aliceFile},
		"--account, --token-file gilt nur": {"fern", "--account", "alice", "--token-file", aliceFile},
	} {
		r := e.run(t, append([]string{"node", "sync"}, args...)...)
		r.want(t, 2, want+" als Client des Nodes")
		if r.out != "" {
			t.Errorf("%v: Ausgabe %q", args, r.out)
		}
	}
	if st := syncStatus(t, nodeStore(t, e.cfg), "fern"); st != (nodestore.SyncStatus{}) {
		t.Errorf("ein falscher Aufruf hat abgeglichen: %+v", st)
	}

	dir := filepath.Join(e.dir, "leer")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	broken := filepath.Join(e.dir, "broken.yaml")
	if err := config.Save(broken, config.Config{Node: &config.Section{DB: "sqlite://" + filepath.Join(dir, "node.db"),
		Listen: closedPort(t)}}); err != nil {
		t.Fatal(err)
	}
	runT(t, "node", "sync", "fern", "--config="+broken).want(t, 1, "node sync: Datenbankdatei fehlt: "+
		filepath.Join(dir, "node.db"))
}

// Ein Node, der das Werkzeug sync nicht kennt (älter als Task 042): node sync
// erkennt es an der Werkzeugliste, ruft nichts auf und nennt das
// Aktualisieren.
func TestNodeSyncOldNode(t *testing.T) {
	dir := isolate(t)
	srv := mcp.NewServer(&mcp.Implementation{Name: "kephalaion", Version: "0.4.3"}, nil)
	mcp.AddTool(srv, &mcp.Tool{Name: "whoami", Description: "alt"},
		func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, mcpnode.WhoamiOutput, error) {
			return nil, mcpnode.WhoamiOutput{Version: "0.4.3", Hubs: []mcpnode.HubInfo{}, UnknownHubs: []string{}}, nil
		})
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	var calls atomic.Int32
	old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != mcpnode.Path {
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		if bytes.Contains(body, []byte(`"tools/call"`)) {
			calls.Add(1)
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		h.ServeHTTP(w, r)
	}))
	defer old.Close()
	tok, err := ident.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "anna.token")
	writeFile(t, file, tok+"\n")
	r := noToken(t, runT(t, "node", "sync", "fern", "--node", old.URL, "--account", "anna", "--token-file", file))
	r.want(t, 1, "kennt das Werkzeug sync nicht", "den Node aktualisieren und serve neu starten")
	if n := calls.Load(); n != 0 || r.out != "" {
		t.Errorf("%d Aufrufe eines Werkzeugs, Ausgabe %q", n, r.out)
	}
}
