package main

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kephalaion/kephalaion/internal/node/mcpnode"
)

// oldReadOutput ist die Struktur von read vor Task 024: ohne content. Der
// Inhalt stand im Text des Ergebnisses.
type oldReadOutput struct {
	Kind     string `json:"kind"`
	Address  string `json:"address"`
	Name     string `json:"name"`
	Revision int64  `json:"revision,omitempty"`
}

// readTarget verbindet ein mcpTarget im Speicher mit einem kleinen Server,
// dem add das Werkzeug read gibt.
func readTarget(t *testing.T, add func(*mcp.Server)) *mcpTarget {
	t.Helper()
	ctx := context.Background()
	srv := mcp.NewServer(&mcp.Implementation{Name: "node", Version: "1"}, nil)
	add(srv)
	st, ct := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return &mcpTarget{session: cs, addr: "vm:test", hub: "vm", collection: "test", account: "kamran"}
}

// Read nimmt den Inhalt aus content; ein älterer Node ohne das Feld liefert
// ihn als Text des Ergebnisses, dann gilt der (Task 024).
func TestMCPTargetReadContent(t *testing.T) {
	docs := map[string]string{"a.md": "# Inhalt <&>\n", "leer.md": ""}
	revs := map[string]int64{"a.md": 3, "leer.md": 4}
	cases := []struct {
		form string
		add  func(*mcp.Server)
	}{
		{"neu", func(s *mcp.Server) {
			mcp.AddTool(s, &mcp.Tool{Name: "read"}, func(_ context.Context, _ *mcp.CallToolRequest,
				in mcpnode.ReadInput) (*mcp.CallToolResult, mcpnode.ReadOutput, error) {
				out := mcpnode.ReadOutput{Kind: mcpnode.KindNone, Address: in.Collection, Name: in.Name}
				if c, ok := docs[in.Name]; ok {
					out.Kind, out.Revision, out.Content = mcpnode.KindDocument, revs[in.Name], &c
				} else if in.Name == "dir" {
					out.Kind = mcpnode.KindDirectory
				}
				return nil, out, nil
			})
		}},
		{"alt", func(s *mcp.Server) {
			mcp.AddTool(s, &mcp.Tool{Name: "read"}, func(_ context.Context, _ *mcp.CallToolRequest,
				in mcpnode.ReadInput) (*mcp.CallToolResult, oldReadOutput, error) {
				out := oldReadOutput{Kind: mcpnode.KindNone, Address: in.Collection, Name: in.Name}
				if c, ok := docs[in.Name]; ok {
					out.Kind, out.Revision = mcpnode.KindDocument, revs[in.Name]
					return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: c}}}, out, nil
				}
				if in.Name == "dir" {
					out.Kind = mcpnode.KindDirectory
				}
				return nil, out, nil
			})
		}},
	}
	for _, c := range cases {
		tgt := readTarget(t, c.add)
		for name, want := range docs {
			doc, ok, err := tgt.Read(context.Background(), name)
			if err != nil || !ok || doc.Content != want || doc.Revision != revs[name] {
				t.Errorf("%s %s: %+v, %v, %v", c.form, name, doc, ok, err)
			}
		}
		for _, name := range []string{"dir", "fehlt.md"} {
			if doc, ok, err := tgt.Read(context.Background(), name); err != nil || ok {
				t.Errorf("%s %s: %+v, %v, %v", c.form, name, doc, ok, err)
			}
		}
	}
}
