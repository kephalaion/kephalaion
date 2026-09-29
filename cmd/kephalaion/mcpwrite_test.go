package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kephalaion/kephalaion/internal/config"
	"github.com/kephalaion/kephalaion/internal/contract"
	"github.com/kephalaion/kephalaion/internal/contract/httpapi"
	"github.com/kephalaion/kephalaion/internal/node/mcpnode"
)

// mcpTool ruft ein Werkzeug am MCP-Eingang unter endpoint mit einem
// Header-Paar je Eintrag in pairs (Alias → Account, Token); ein Fehler des
// Werkzeugs ist kein Fehler des Tests. out nimmt die Struktur auf; es liefert
// das rohe Ergebnis als JSON und den Text.
func mcpTool(t *testing.T, endpoint string, pairs map[string][2]string, tool string, args, out any) (raw, text string) {
	t.Helper()
	h := http.Header{}
	for alias, p := range pairs {
		h.Set("X-Keph-Account-"+alias, p[0])
		h.Set("X-Keph-Token-"+alias, p[1])
	}
	ctx := context.Background()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: endpoint,
		HTTPClient: &http.Client{Transport: headerRT{h}}, DisableStandaloneSSE: true, MaxRetries: -1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	b, _ := json.Marshal(res.StructuredContent)
	if err := json.Unmarshal(b, out); err != nil {
		t.Fatal(err)
	}
	b, _ = json.Marshal(res)
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			text += tc.Text
		}
	}
	return string(b), text
}

// lostWrite schreibt am Hub und meldet dann einen unklaren Ausgang — wie eine
// Antwort, die unterwegs verloren ging.
type lostWrite struct {
	contract.Hub
}

func (l lostWrite) Create(ctx context.Context, req contract.CreateRequest) (contract.WriteResponse, error) {
	if _, err := l.Hub.Create(ctx, req); err != nil {
		return contract.WriteResponse{}, err
	}
	return contract.WriteResponse{}, fmt.Errorf("%w: Antwort verloren", contract.ErrOutcomeUnknown)
}

// Schreiben über serve mit Hub und Node, local und http: Die eigene Änderung
// liest sich sofort, zweimal speichern geht mit der Revision aus read, der
// Abgleich kommt über den Anstoß (sync_interval 0). Über http: Hub
// gestoppt → unreachable, Lesen geht weiter; unklarer Ausgang → der
// angestoßene Abgleich bringt das Dokument in die Replica. Kein Token in
// Antwort und Log, kein Inhalt im Log.
func TestMCPWriteThroughServe(t *testing.T) {
	slow(t, "serve mit Hub und Node, wartet auf die angestoßenen Abgleiche")
	e := newCommEnv(t)
	e.run(t, "node", "sync").want(t, 0)
	// Nur der Anstoß gleicht ab.
	e.run(t, "config", "set", "node", "sync_interval", "0").want(t, 0)
	srv := startServe(t, portZero(t, e.cfg))
	endpoint := "http://" + srv.addrs[config.Node] + mcpnode.Path
	bob := map[string][2]string{"eigen": {"bob", e.tokens["bob"]}, "fern": {"bob", e.tokens["bob"]}}
	var raws []string
	write := func(tool string, args any) mcpnode.WriteOutput {
		t.Helper()
		var out mcpnode.WriteOutput
		raw, _ := mcpTool(t, endpoint, bob, tool, args, &out)
		raws = append(raws, raw)
		return out
	}
	read := func(addr, name string) (mcpnode.ReadOutput, string) {
		t.Helper()
		var out mcpnode.ReadOutput
		_, text := mcpTool(t, endpoint, bob, "read", mcpnode.ReadInput{Collection: addr, Name: name}, &out)
		return out, text
	}
	var now mcpnode.ChangesOutput
	mcpTool(t, endpoint, bob, "changes", mcpnode.ChangesInput{Collection: "eigen:team-x"}, &now)

	// local: anlegen und sofort lesen, zweimal schreiben.
	out := write("create", mcpnode.CreateInput{Collection: "eigen:team-x", Name: "neu.md", Content: "geheimer Inhalt"})
	if out.Error != nil || out.Revision == 0 || out.Updated == nil || out.Updated.By != "kleist" {
		t.Fatalf("create: %+v", out)
	}
	doc, text := read("eigen:team-x", "neu.md")
	if doc.Revision != out.Revision || text != "geheimer Inhalt" {
		t.Fatalf("read nach create: %+v, %q", doc, text)
	}
	for _, content := range []string{"zwei", "drei"} {
		w := write("write", mcpnode.WriteInput{Collection: "eigen:team-x", Name: "neu.md", Content: content,
			BaseRevision: &doc.Revision})
		if w.Error != nil || w.Revision <= doc.Revision {
			t.Fatalf("write %s: %+v", content, w)
		}
		if doc, text = read("eigen:team-x", "neu.md"); doc.Revision != w.Revision || text != content {
			t.Fatalf("read nach write %s: %+v, %q", content, doc, text)
		}
	}
	e.run(t, "hub", "doc", "get", "team-x", "neu.md").want(t, 0, "drei")
	// Der Anstoß gleicht ab: changes meldet das Dokument.
	eventuallyLog(t, srv, "changes meldet neu.md", func() bool {
		var got mcpnode.ChangesOutput
		mcpTool(t, endpoint, bob, "changes", mcpnode.ChangesInput{Collection: "eigen:team-x", Cursor: now.Cursor}, &got)
		return len(got.Changes) == 1 && got.Changes[0].Name == "neu.md" && got.Changes[0].Revision == doc.Revision
	})
	// Umbenennen: die Replica ist sofort richtig, ohne Abgleich; ein
	// Verzeichnis als Ganzes, delete mit recursive ebenso.
	moved := write("rename", mcpnode.RenameInput{Collection: "eigen:team-x", Name: "neu.md", NewName: "ordner/neu.md",
		BaseRevision: &doc.Revision})
	if moved.Error != nil || moved.ID != doc.ID || moved.Name != "ordner/neu.md" || moved.Revision <= doc.Revision {
		t.Fatalf("rename: %+v", moved)
	}
	if old, _ := read("eigen:team-x", "neu.md"); old.Kind != mcpnode.KindNone {
		t.Errorf("alter Name: %+v", old)
	}
	if doc, text := read("eigen:team-x", "ordner/neu.md"); doc.ID != moved.ID || text != "drei" {
		t.Errorf("neuer Name: %+v, %q", doc, text)
	}
	write("create", mcpnode.CreateInput{Collection: "eigen:team-x", Name: "ordner/zwei.md", Content: "zwei"})
	dir := write("rename", mcpnode.RenameInput{Collection: "eigen:team-x", Name: "ordner", NewName: "mappe"})
	if dir.Error != nil || dir.Kind != mcpnode.KindDirectory || dir.Count != 2 {
		t.Fatalf("rename Verzeichnis: %+v", dir)
	}
	if got, text := read("eigen:team-x", "mappe/neu.md"); got.ID != doc.ID || text != "drei" {
		t.Errorf("im neuen Verzeichnis: %+v, %q", got, text)
	}
	e.run(t, "hub", "doc", "get", "team-x", "mappe/zwei.md").want(t, 0, "zwei")
	e.run(t, "hub", "doc", "get", "team-x", "ordner/neu.md").want(t, 1)
	wantWriteCode(t, "Verzeichnis ohne recursive", write("delete", mcpnode.DeleteInput{Collection: "eigen:team-x",
		Name: "mappe"}), "invalid", "löschen nur mit recursive")
	gone := write("delete", mcpnode.DeleteInput{Collection: "eigen:team-x", Name: "mappe", Recursive: true})
	if gone.Error != nil || gone.Kind != mcpnode.KindDirectory || gone.Count != 2 || !gone.Deleted {
		t.Fatalf("delete Verzeichnis: %+v", gone)
	}
	if got, _ := read("eigen:team-x", "mappe"); got.Kind != mcpnode.KindNone {
		t.Errorf("gelöschtes Verzeichnis: %+v", got)
	}
	e.run(t, "hub", "doc", "get", "team-x", "mappe/neu.md").want(t, 1)

	// Ein fremdes Dokument ohne supersede: forbidden, vom Hub.
	e.runIn(t, "admin", "hub", "doc", "put", "team-x", "admin.md").want(t, 0)
	e.run(t, "node", "sync", "eigen").want(t, 0)
	wantWriteCode(t, "fremd", write("write", mcpnode.WriteInput{Collection: "eigen:team-x", Name: "admin.md", Content: "x"}),
		"forbidden", "gehört admin, supersede fehlt")

	// http: anlegen, dann ist der Hub weg.
	if out := write("create", mcpnode.CreateInput{Collection: "fern:team-x", Name: "fern.md", Content: "fern"}); out.Error != nil {
		t.Fatalf("create über http: %+v", out)
	}
	e.run(t, "node", "hub", "set", "fern", "--address", closedAddress(t)).want(t, 0)
	wantWriteCode(t, "Hub gestoppt", write("create", mcpnode.CreateInput{Collection: "fern:team-x", Name: "weg.md"}),
		"unreachable", "Hub fern nicht erreichbar, nichts gespeichert")
	if doc, text := read("fern:team-x", "fern.md"); doc.Kind != mcpnode.KindDocument || text != "fern" {
		t.Errorf("Lesen ohne Hub: %+v, %q", doc, text)
	}
	e.run(t, "hub", "doc", "get", "team-x", "weg.md").want(t, 1)

	// Umbenennen über http, der Hub ist wieder da.
	e.run(t, "node", "hub", "set", "fern", "--address", e.url).want(t, 0)
	if out := write("rename", mcpnode.RenameInput{Collection: "fern:team-x", Name: "fern.md", NewName: "fern/da.md"}); out.Error != nil ||
		out.Kind != mcpnode.KindDocument {
		t.Fatalf("rename über http: %+v", out)
	}
	if doc, text := read("fern:team-x", "fern/da.md"); doc.Kind != mcpnode.KindDocument || text != "fern" {
		t.Errorf("nach rename über http: %+v, %q", doc, text)
	}
	if doc, _ := read("fern:team-x", "fern.md"); doc.Kind != mcpnode.KindNone {
		t.Errorf("alter Name über http: %+v", doc)
	}

	// Unklarer Ausgang: Der Hub hat geschrieben, die Antwort ging verloren.
	hookHTTP(t, func(address string) (contract.Hub, error) {
		c, err := httpapi.NewClient(address, nil)
		return lostWrite{c}, err
	})
	wantWriteCode(t, "unklar", write("create", mcpnode.CreateInput{Collection: "fern:team-x", Name: "unklar.md",
		Content: "vielleicht"}), "outcome_unknown", "Ausgang unklar")
	eventuallyLog(t, srv, "unklar.md in der Replica nach dem angestoßenen Abgleich", func() bool {
		return e.docIs(t, "fern:team-x", "unklar.md", "vielleicht")
	})

	srv.stop(t)
	log := srv.log.String()
	for _, want := range []string{"op=create", "op=rename", "op=delete", "hub=eigen node=laptop", "hub=fern node=laptop-http",
		"account=bob", "code=unreachable", "code=outcome_unknown", "code=forbidden"} {
		if !strings.Contains(log, want) {
			t.Errorf("Log ohne %q:\n%s", want, log)
		}
	}
	if strings.Contains(log, "keph_") || strings.Contains(log, "geheimer Inhalt") {
		t.Errorf("Token oder Inhalt im Log:\n%s", log)
	}
	for _, raw := range raws {
		if strings.Contains(raw, "keph_") || strings.Contains(raw, e.url) {
			t.Errorf("Token oder Adresse in der Antwort: %s", raw)
		}
	}
}

// Der Durchlauf von Task 014: serve mit Hub und Node, dazu ein zweiter Node
// mit eigener config und eigenem serve, der den Hub über http erreicht — zwei
// Rechner auf einem, zwei Accounts desselben Users. Beide schreiben, und der
// Anstoß nach dem eigenen Schreiben bringt auch das des anderen; ein
// Konflikt zwischen beiden wird abgelehnt (stale_revision, name_taken), nichts
// wird still überschrieben; der Urheber ist der User, actions nennt Account
// und Node. Ist der Hub gestoppt, speichert der zweite Node nichts
// (unreachable) und liest weiter.
func TestMCPWriteTwoNodes(t *testing.T) {
	slow(t, "zwei serve, wartet auf die angestoßenen Abgleiche")
	e := newCommEnv(t)
	r := e.run(t, "hub", "account", "add", "bob-vm", "--user", "kleist")
	r.want(t, 0)
	bobVM := tokenFrom(t, r.out)
	e.run(t, "hub", "account", "grant", "bob-vm", "team-x", "--write").want(t, 0)
	r = e.run(t, "hub", "node", "add", "vm")
	r.want(t, 0)
	vmToken := tokenFrom(t, r.out)
	e.run(t, "hub", "node", "grant", "vm", "team-x").want(t, 0)

	// Erster Rechner: Hub und Node in einem serve; nur der Anstoß gleicht ab.
	e.run(t, "node", "sync", "eigen").want(t, 0)
	e.run(t, "config", "set", "node", "sync_interval", "0").want(t, 0)
	srvA := startServe(t, portZero(t, e.cfg))

	// Zweiter Rechner: nur ein Node, der Hub über http unter der Adresse von
	// serve.
	dir := filepath.Join(e.dir, "vm")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfgB := filepath.Join(dir, "config.yaml")
	cB := "--config=" + cfgB
	runT(t, "node", "init", "--db", "sqlite://"+filepath.Join(dir, "node.db"), cB).want(t, 0)
	runIn(t, vmToken, "node", "hub", "add", "zentral", "--node", "vm", "--transport", "http",
		"--address", "http://"+srvA.addrs[config.Hub], "--token-stdin", cB).want(t, 0)
	runT(t, "node", "collection", "add", "zentral:team-x", cB).want(t, 0)
	runT(t, "config", "set", "node", "sync_interval", "0", cB).want(t, 0)
	runT(t, "node", "sync", cB).want(t, 0)
	cfg, _, err := config.Load(cfgB)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Node.Listen = "127.0.0.1:0"
	srvB := startServe(t, cfg)

	type client struct {
		endpoint, addr string
		pairs          map[string][2]string
	}
	A := client{"http://" + srvA.addrs[config.Node] + mcpnode.Path, "eigen:team-x",
		map[string][2]string{"eigen": {"bob", e.tokens["bob"]}}}
	B := client{"http://" + srvB.addrs[config.Node] + mcpnode.Path, "zentral:team-x",
		map[string][2]string{"zentral": {"bob-vm", bobVM}}}
	write := func(c client, tool string, args any) mcpnode.WriteOutput {
		t.Helper()
		var out mcpnode.WriteOutput
		mcpTool(t, c.endpoint, c.pairs, tool, args, &out)
		return out
	}
	read := func(c client, name string) (mcpnode.ReadOutput, string) {
		t.Helper()
		var out mcpnode.ReadOutput
		_, text := mcpTool(t, c.endpoint, c.pairs, "read", mcpnode.ReadInput{Collection: c.addr, Name: name}, &out)
		return out, text
	}
	hubHas := func(name, content string) {
		t.Helper()
		if r := e.run(t, "hub", "doc", "get", "team-x", name); r.code != 0 || r.out != content {
			t.Errorf("am Hub %s: %q (Exit %d), erwartet %q", name, r.out, r.code, content)
		}
	}

	// Beide schreiben. B kennt geteilt.md erst nach dem Abgleich, den sein
	// eigenes create anstößt.
	fromA := write(A, "create", mcpnode.CreateInput{Collection: A.addr, Name: "geteilt.md", Content: "von A"})
	if fromA.Error != nil || fromA.Updated == nil || fromA.Updated.By != "kleist" {
		t.Fatalf("create von A: %+v", fromA)
	}
	if doc, _ := read(B, "geteilt.md"); doc.Kind != mcpnode.KindNone {
		t.Fatalf("B kennt geteilt.md vor dem Abgleich: %+v", doc)
	}
	fromB := write(B, "create", mcpnode.CreateInput{Collection: B.addr, Name: "von-b.md", Content: "von B"})
	if fromB.Error != nil || fromB.Updated == nil || fromB.Updated.By != "kleist" || fromB.Revision <= fromA.Revision {
		t.Fatalf("create von B: %+v", fromB)
	}
	eventuallyLog(t, srvB, "B liest geteilt.md nach dem angestoßenen Abgleich", func() bool {
		doc, text := read(B, "geteilt.md")
		return doc.Revision == fromA.Revision && text == "von A"
	})
	e.run(t, "node", "sync", "eigen").want(t, 0)
	if doc, text := read(A, "von-b.md"); doc.ID != fromB.ID || text != "von B" || doc.Created == nil ||
		doc.Created.By != "kleist" {
		t.Errorf("A liest von-b.md: %+v, %q", doc, text)
	}

	// Konflikt: A ändert geteilt.md; B beruht noch auf dem alten Stand und
	// wird abgelehnt. Erst nach dem Abgleich schreibt B auf den neuen.
	base := fromA.Revision
	newA := write(A, "write", mcpnode.WriteInput{Collection: A.addr, Name: "geteilt.md", Content: "A zwei",
		BaseRevision: &base})
	if newA.Error != nil || newA.Revision <= base {
		t.Fatalf("write von A: %+v", newA)
	}
	if doc, _ := read(B, "geteilt.md"); doc.Revision != base {
		t.Fatalf("B schon abgeglichen: %+v", doc)
	}
	wantWriteCode(t, "B auf altem Stand", write(B, "write", mcpnode.WriteInput{Collection: B.addr, Name: "geteilt.md",
		Content: "B zwei", BaseRevision: &base}), "stale_revision",
		fmt.Sprintf("hat Revision %d, der Vorgang beruht auf %d", newA.Revision, base))
	hubHas("geteilt.md", "A zwei")
	runT(t, "node", "sync", cB).want(t, 0)
	doc, text := read(B, "geteilt.md")
	if doc.Revision != newA.Revision || text != "A zwei" {
		t.Fatalf("B nach dem Abgleich: %+v, %q", doc, text)
	}
	if out := write(B, "write", mcpnode.WriteInput{Collection: B.addr, Name: "geteilt.md", Content: "B zwei",
		BaseRevision: &doc.Revision}); out.Error != nil {
		t.Fatalf("write von B auf neuem Stand: %+v", out)
	}
	hubHas("geteilt.md", "B zwei")

	// Derselbe Name von beiden: A legt an, B bekommt name_taken — beim
	// Anlegen wie beim Umbenennen; nichts wird überschrieben.
	if out := write(A, "create", mcpnode.CreateInput{Collection: A.addr, Name: "gleich.md", Content: "A"}); out.Error != nil {
		t.Fatalf("create gleich.md von A: %+v", out)
	}
	wantWriteCode(t, "B legt gleich.md an", write(B, "create", mcpnode.CreateInput{Collection: B.addr, Name: "gleich.md",
		Content: "B"}), "name_taken", "gleich.md gibt es in team-x schon")
	wantWriteCode(t, "B benennt auf gleich.md um", write(B, "rename", mcpnode.RenameInput{Collection: B.addr,
		Name: "von-b.md", NewName: "gleich.md"}), "name_taken", "gleich.md gibt es in team-x schon")
	hubHas("gleich.md", "A")
	hubHas("von-b.md", "von B")

	// Urheber: je Vorgang der Account und der Node, der ihn trug.
	rows, err := rawHub(t, e.dir).Query(`SELECT account, carrier, action FROM actions WHERE document_id = ?
		ORDER BY revision`, fromA.ID)
	if err != nil {
		t.Fatal(err)
	}
	var acts []string
	for rows.Next() {
		var account, carrier, action string
		if err := rows.Scan(&account, &carrier, &action); err != nil {
			t.Fatal(err)
		}
		acts = append(acts, account+"@"+carrier+":"+action)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(acts, " "); got != "bob@laptop:create bob@laptop:update bob-vm@vm:update" {
		t.Errorf("actions von geteilt.md: %s", got)
	}

	// Der Hub ist gestoppt: B speichert nichts und liest weiter.
	srvA.stop(t)
	wantWriteCode(t, "Hub gestoppt", write(B, "write", mcpnode.WriteInput{Collection: B.addr, Name: "von-b.md",
		Content: "offline"}), "unreachable", "Hub zentral nicht erreichbar, nichts gespeichert")
	if doc, text := read(B, "von-b.md"); doc.Kind != mcpnode.KindDocument || text != "von B" {
		t.Errorf("B liest ohne Hub: %+v, %q", doc, text)
	}
	if doc, text := read(B, "geteilt.md"); doc.Kind != mcpnode.KindDocument || text != "B zwei" {
		t.Errorf("B liest ohne Hub: %+v, %q", doc, text)
	}
	hubHas("von-b.md", "von B")

	srvB.stop(t)
	hubLog, logB := srvA.log.String(), srvB.log.String()
	for _, want := range [][]string{
		{"hub POST /v1/create 200", "node=vm", "account=bob-vm"},
		{"hub POST /v1/write 409", "node=vm", "account=bob-vm"},
		{"node POST /mcp 200", "op=create", "hub=eigen node=laptop", "account=bob"},
	} {
		if !logLine(hubLog, want...) {
			t.Errorf("Log von serve A ohne Zeile mit %q:\n%s", want, hubLog)
		}
	}
	for _, want := range [][]string{
		{"op=create", "hub=zentral node=vm", "account=bob-vm"},
		{"op=write", "code=stale_revision"},
		{"op=create", "code=name_taken"},
		{"op=rename", "code=name_taken"},
		{"op=write", "code=unreachable"},
	} {
		if !logLine(logB, want...) {
			t.Errorf("Log von serve B ohne Zeile mit %q:\n%s", want, logB)
		}
	}
	for _, log := range []string{hubLog, logB} {
		if strings.Contains(log, "keph_") || strings.Contains(log, "von A") || strings.Contains(log, "B zwei") {
			t.Errorf("Token oder Inhalt im Log:\n%s", log)
		}
	}
}

// logLine sagt, ob eine Zeile des Logs alle parts enthält.
func logLine(log string, parts ...string) bool {
	for _, line := range strings.Split(log, "\n") {
		if contains(line, parts...) {
			return true
		}
	}
	return false
}

func wantWriteCode(t *testing.T, what string, out mcpnode.WriteOutput, code, want string) {
	t.Helper()
	if out.Error == nil || out.Error.Code != code || !strings.Contains(out.Error.Message, want) {
		t.Errorf("%s: %+v, erwartet %s mit %q", what, out.Error, code, want)
	}
}

// Über https schreibt der Node noch nicht: unsupported, ohne Anfrage.
func TestMCPWriteUnsupportedTransport(t *testing.T) {
	e := newCommEnv(t)
	link := nodeHubLink(config.Config{}, nil, nil)
	h, err := nodeStore(t, e.cfg).Hub(context.Background(), "fern")
	if err != nil {
		t.Fatal(err)
	}
	h.Transport = "https"
	if _, _, err := link.Connect(context.Background(), h); err == nil || !strings.Contains(err.Error(), "noch nicht unterstützt") ||
		!errors.Is(err, mcpnode.ErrUnsupported) {
		t.Errorf("https: %v", err)
	}
	h.Transport = "local"
	if _, _, err := link.Connect(context.Background(), h); err == nil || errors.Is(err, mcpnode.ErrUnsupported) {
		t.Errorf("local ohne Hub in der config: %v", err)
	}
}
