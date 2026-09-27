package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
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

	// Unklarer Ausgang: Der Hub hat geschrieben, die Antwort ging verloren.
	e.run(t, "node", "hub", "set", "fern", "--address", e.url).want(t, 0)
	hookHTTP(t, func(address string) (contract.Hub, error) {
		c, err := httpapi.NewClient(address)
		return lostWrite{c}, err
	})
	wantWriteCode(t, "unklar", write("create", mcpnode.CreateInput{Collection: "fern:team-x", Name: "unklar.md",
		Content: "vielleicht"}), "outcome_unknown", "Ausgang unklar")
	eventuallyLog(t, srv, "unklar.md in der Replica nach dem angestoßenen Abgleich", func() bool {
		return e.docIs(t, "fern:team-x", "unklar.md", "vielleicht")
	})

	srv.stop(t)
	log := srv.log.String()
	for _, want := range []string{"op=create", "hub=eigen node=laptop", "hub=fern node=laptop-http",
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
