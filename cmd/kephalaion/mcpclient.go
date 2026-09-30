package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kephalaion/kephalaion/internal/dirsync"
	"github.com/kephalaion/kephalaion/internal/node/mcpnode"
)

// Die Kommandozeile als Client des Nodes über MCP (node dir push|pull): der
// Client des go-sdk gegen /mcp, mit dem Header-Paar eines Hubs, und darüber
// dirsync.Target aus den Werkzeugen list, read, create, write und delete;
// dazu whoami für die Verzeichnis-Scopes, die push außerhalb von vendor/
// braucht. Das Token steht nur im Header; keine Meldung nennt es.

// headerTransport setzt die Header eines Clients an jede Anfrage.
type headerTransport struct {
	header http.Header
	next   http.RoundTripper
}

func (t headerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	for k, v := range t.header {
		r.Header[k] = v
	}
	return t.next.RoundTrip(r)
}

// mcpTarget ist ein Verzeichnis einer Collection am Node als dirsync.Target.
type mcpTarget struct {
	session *mcp.ClientSession
	// addr ist die Collection als <hub>:<collection>.
	addr                     string
	hub, collection, account string
}

// connectNode verbindet sich mit dem MCP-Eingang unter endpoint, angemeldet
// am Hub hub als account mit token, und liefert das Ziel für die Collection.
// done beendet die Sitzung.
func connectNode(ctx context.Context, endpoint, hub, collection, account, token string) (tgt *mcpTarget, done func(), err error) {
	h := http.Header{}
	h.Set("X-Keph-Account-"+hub, account)
	h.Set("X-Keph-Token-"+hub, token)
	client := mcp.NewClient(&mcp.Implementation{Name: "kephalaion", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: endpoint,
		HTTPClient:           &http.Client{Transport: headerTransport{header: h, next: http.DefaultTransport}},
		DisableStandaloneSSE: true, MaxRetries: -1}, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("Node unter %s nicht erreichbar: %w", endpoint, err)
	}
	return &mcpTarget{session: session, addr: hub + ":" + collection, hub: hub, collection: collection, account: account},
		func() { _ = session.Close() }, nil
}

// DirScopes fragt den Node über whoami nach den Verzeichnis-Scopes des
// Accounts in der Collection — aus der strukturierten Antwort (dirs), nicht
// aus dem Text. Der Node liest seine Replica: Es gilt der Stand des letzten
// Abgleichs. Ist der Account dort nicht angemeldet oder die Collection für ihn
// nicht lesbar, ist das ein Fehler; ebenso ein Node, der dirs nicht kennt
// (älter als Task 021).
func (t *mcpTarget) DirScopes(ctx context.Context) ([]string, error) {
	var out mcpnode.WhoamiOutput
	if _, err := t.call(ctx, "whoami", struct{}{}, &out); err != nil {
		return nil, err
	}
	i := slices.IndexFunc(out.Hubs, func(h mcpnode.HubInfo) bool { return h.Hub == t.hub })
	if i < 0 {
		return nil, fmt.Errorf("der Node kennt keinen Hub %s", t.hub)
	}
	h := out.Hubs[i]
	if h.Login != mcpnode.LoginOK {
		msg := fmt.Sprintf("Account %s ist am Node für den Hub %s nicht angemeldet (login %s)", t.account, t.hub, h.Login)
		if h.Sync.NeverSynced {
			msg += "; noch nie abgeglichen: kephalaion node sync " + t.hub
		}
		return nil, errors.New(msg)
	}
	j := slices.IndexFunc(h.Collections, func(c mcpnode.CollectionRights) bool { return c.Collection == t.collection })
	if j < 0 {
		return nil, fmt.Errorf("%s: nicht lesbar für den Account %s", t.addr, t.account)
	}
	dirs := h.Collections[j].Dirs
	if dirs == nil {
		return nil, fmt.Errorf("der Node nennt keine Verzeichnis-Scopes (dirs fehlt in whoami) — er ist älter als diese " +
			"Kommandozeile; den Node aktualisieren und serve neu starten")
	}
	return dirs, nil
}

// call ruft ein Werkzeug und liest die strukturierte Antwort nach out. Ein
// Fehler des Werkzeugs (isError) kommt als *dirsync.OpError — mit dem Code
// aus der Struktur, wenn das Werkzeug einen liefert (create, write, delete),
// sonst nur mit der Meldung; ein Fehler des Transports bleibt, wie er ist.
func (t *mcpTarget) call(ctx context.Context, tool string, args, out any) (text string, err error) {
	res, err := t.session.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		return "", fmt.Errorf("Werkzeug %s: %w", tool, err)
	}
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			text += tc.Text
		}
	}
	if res.IsError {
		oe := &dirsync.OpError{Message: strings.TrimSpace(text)}
		var w mcpnode.WriteOutput
		if b, err := json.Marshal(res.StructuredContent); err == nil && json.Unmarshal(b, &w) == nil && w.Error != nil {
			oe.Code, oe.Message = w.Error.Code, w.Error.Message
		}
		return "", oe
	}
	if out != nil {
		b, err := json.Marshal(res.StructuredContent)
		if err != nil {
			return "", err
		}
		if err := json.Unmarshal(b, out); err != nil {
			return "", fmt.Errorf("Werkzeug %s: Antwort nicht lesbar: %w", tool, err)
		}
	}
	return text, nil
}

// List liest ein Verzeichnis über list ohne recursive, Seite für Seite bis
// zum Ende.
func (t *mcpTarget) List(ctx context.Context, dir string) ([]dirsync.Entry, error) {
	var out []dirsync.Entry
	cursor := ""
	for {
		var page mcpnode.ListOutput
		if _, err := t.call(ctx, "list", mcpnode.ListInput{Collection: t.addr, Path: dir, Limit: mcpnode.MaxLimit,
			Cursor: cursor}, &page); err != nil {
			return nil, err
		}
		for _, e := range page.Entries {
			switch e.Kind {
			case mcpnode.KindDirectory:
				out = append(out, dirsync.Entry{Name: e.Name, Dir: true})
			case mcpnode.KindDocument:
				out = append(out, dirsync.Entry{Name: e.Name, Revision: e.Revision})
			}
		}
		if !page.More {
			return out, nil
		}
		if page.Cursor == "" {
			return nil, errors.New("list: more ohne cursor")
		}
		cursor = page.Cursor
	}
}

// Read liest ein Dokument über read; kind none oder directory ist kein
// Dokument.
func (t *mcpTarget) Read(ctx context.Context, name string) (dirsync.Document, bool, error) {
	var out mcpnode.ReadOutput
	text, err := t.call(ctx, "read", mcpnode.ReadInput{Collection: t.addr, Name: name}, &out)
	if err != nil {
		return dirsync.Document{}, false, err
	}
	if out.Kind != mcpnode.KindDocument {
		return dirsync.Document{}, false, nil
	}
	return dirsync.Document{Content: text, Revision: out.Revision}, true, nil
}

func (t *mcpTarget) Create(ctx context.Context, name, content string) error {
	_, err := t.call(ctx, "create", mcpnode.CreateInput{Collection: t.addr, Name: name, Content: content}, nil)
	return err
}

func (t *mcpTarget) Write(ctx context.Context, name, content string, base int64) error {
	_, err := t.call(ctx, "write", mcpnode.WriteInput{Collection: t.addr, Name: name, Content: content, BaseRevision: &base}, nil)
	return err
}

func (t *mcpTarget) Delete(ctx context.Context, name string, base int64) error {
	_, err := t.call(ctx, "delete", mcpnode.DeleteInput{Collection: t.addr, Name: name, BaseRevision: &base}, nil)
	return err
}

func (t *mcpTarget) DeleteDir(ctx context.Context, dir string) error {
	_, err := t.call(ctx, "delete", mcpnode.DeleteInput{Collection: t.addr, Name: dir, Recursive: true}, nil)
	return err
}
