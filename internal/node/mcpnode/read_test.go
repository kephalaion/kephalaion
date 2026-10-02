package mcpnode

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kephalaion/kephalaion/internal/contract"
)

func (e *docEnv) read(t *testing.T, h map[string][]string, in ReadInput) (ReadOutput, *mcp.CallToolResult, string) {
	t.Helper()
	var out ReadOutput
	res, errText := e.call(t, h, "read", in, &out)
	if errText == "" {
		raw, _ := json.Marshal(res)
		if strings.Contains(string(raw), "SYSTEM") {
			t.Errorf("SYSTEM: in der Antwort: %s", raw)
		}
		checkTextIsStructure(t, res)
	}
	return out, res, errText
}

// checkTextIsStructure prüft, dass ein Ergebnis genau einen Textblock hat und
// er das JSON der Struktur ist — er trägt nichts darüber hinaus, auch nicht
// den Inhalt neben der Struktur (Task 024).
func checkTextIsStructure(t *testing.T, res *mcp.CallToolResult) {
	t.Helper()
	if len(res.Content) != 1 {
		t.Errorf("%d Blöcke statt einem: %+v", len(res.Content), res.Content)
		return
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Errorf("kein Textblock: %+v", res.Content[0])
		return
	}
	var text any
	if err := json.Unmarshal([]byte(tc.Text), &text); err != nil || !reflect.DeepEqual(text, res.StructuredContent) {
		t.Errorf("Text ist nicht das JSON der Struktur (%v): %q, Struktur %s", err, tc.Text, rawOf(res))
	}
}

// contentOf ist das Feld content der Antwort von read; fehlt es, ein Wert,
// den kein Dokument der Tests hat.
func contentOf(out ReadOutput) string {
	if out.Content == nil {
		return "<ohne content>"
	}
	return *out.Content
}

func no() *bool { b := false; return &b }

func TestReadDocument(t *testing.T) {
	e := newDocEnv(t)
	ids := e.fillWissen(t)
	out, res, errText := e.read(t, e.anna(), ReadInput{Collection: "keph:wissen", Name: "dir/c.md"})
	if errText != "" || out.Kind != KindDocument || out.Address != "keph:wissen" || out.Name != "dir/c.md" ||
		out.ID != ids["dir/c.md"] || out.Revision == 0 || out.Size == nil || *out.Size != int64(len("Inhalt von dir/c.md")) ||
		out.Created == nil || out.Updated == nil || out.Writable == nil || !*out.Writable {
		t.Fatalf("per Name: %+v, %s", out, errText)
	}
	if got := contentOf(out); got != "Inhalt von dir/c.md" {
		t.Errorf("Inhalt: %q", got)
	}
	// Der Inhalt steht in der Struktur, also auch im Text, ihrem JSON.
	var text ReadOutput
	if err := json.Unmarshal([]byte(textOf(res)), &text); err != nil || contentOf(text) != "Inhalt von dir/c.md" {
		t.Errorf("Text: %q, %v", textOf(res), err)
	}
	// Per id, ohne Hub-Teil (otto ist nur an keph angemeldet), ohne Recht
	// write.
	out, _, _ = e.read(t, e.otto(), ReadInput{ID: ids["a.md"]})
	if out.Kind != KindDocument || out.Name != "a.md" || out.Address != "keph:wissen" || out.Writable == nil ||
		*out.Writable || contentOf(out) != "neu" {
		t.Errorf("per id: %+v, %q", out, contentOf(out))
	}
	// Mit mehreren Hubs braucht id den Hub.
	if _, _, errText := e.read(t, e.anna(), ReadInput{ID: ids["a.md"]}); !strings.Contains(errText, "angemeldet an keph, team") {
		t.Errorf("id ohne Hub: %s", errText)
	}
	out, _, _ = e.read(t, e.anna(), ReadInput{Collection: "keph:", ID: ids["a.md"]})
	if out.Kind != KindDocument || out.Name != "a.md" || contentOf(out) != "neu" {
		t.Errorf("id mit Hub: %+v", out)
	}
	// content: false — nur die Angaben, kein Feld content; per Name und per id.
	for _, in := range []ReadInput{{Collection: "keph:wissen", Name: "a.md", Content: no()}, {Collection: "keph:", ID: ids["a.md"], Content: no()}} {
		out, res, _ = e.read(t, e.anna(), in)
		if out.Kind != KindDocument || out.Size == nil || *out.Size != 3 || out.Content != nil ||
			strings.Contains(rawOf(res), `"content"`) || strings.Contains(textOf(res), `"neu"`) ||
			!strings.Contains(textOf(res), `"kind":"document"`) {
			t.Errorf("ohne Inhalt %+v: %+v, %q", in, out, textOf(res))
		}
	}
	// Leerer Inhalt ist ein Dokument mit Größe 0 und content "".
	leer := e.hubs["keph"].put("wissen", "leer.md", "")
	// Zeichen, die das JSON des Textes maskiert, kommen in content unverändert an.
	special := "# <b> & \"c\"\n\tZeile\n"
	e.hubs["keph"].put("wissen", "sonder.md", special)
	e.sync(t)
	for _, in := range []ReadInput{{Collection: "keph:wissen", Name: "leer.md"}, {ID: leer, Collection: "keph:"}} {
		out, res, _ = e.read(t, e.anna(), in)
		if out.Kind != KindDocument || *out.Size != 0 || out.Content == nil || *out.Content != "" ||
			!strings.Contains(rawOf(res), `"content":""`) {
			t.Errorf("leer %+v: %+v, %s", in, out, rawOf(res))
		}
	}
	out, _, _ = e.read(t, e.anna(), ReadInput{Collection: "keph:wissen", Name: "sonder.md"})
	if contentOf(out) != special || *out.Size != int64(len(special)) {
		t.Errorf("Sonderzeichen: %q", contentOf(out))
	}
}

func TestReadDirectoryAndNone(t *testing.T) {
	e := newDocEnv(t)
	ids := e.fillWissen(t)
	cases := []struct {
		in   ReadInput
		kind string
		name string
	}{
		{ReadInput{Collection: "keph:wissen", Name: "dir"}, KindDirectory, "dir"},
		{ReadInput{Collection: "keph:wissen", Name: "dir/sub/"}, KindDirectory, "dir/sub"},
		{ReadInput{Collection: "keph:wissen"}, KindDirectory, ""},
		{ReadInput{Collection: "wissen", Name: ""}, KindDirectory, ""},
		{ReadInput{Collection: "keph:"}, KindDirectory, ""},
		{ReadInput{Collection: "keph:wissen", Name: "fehlt.md"}, KindNone, "fehlt.md"},
		{ReadInput{Collection: "keph:wissen", Name: "di"}, KindNone, "di"},
		{ReadInput{Collection: "keph:wissen", Name: "a.md/x"}, KindNone, "a.md/x"},
		// Eine Löschmarke ist nichts, per Name wie per id.
		{ReadInput{Collection: "keph:wissen", Name: "gone.md"}, KindNone, "gone.md"},
		{ReadInput{ID: ids["gone.md"]}, KindNone, ""},
		{ReadInput{ID: "01ZZZZZZZZZZZZZZZZZZZZZZZZ"}, KindNone, ""},
		// Die id eines Dokuments aus einer anderen Collection ist dort nichts.
		{ReadInput{Collection: "keph:privat", ID: ids["a.md"]}, KindNone, ""},
	}
	for _, c := range cases {
		// Verzeichnis und none tragen kein Feld content.
		out, res, errText := e.read(t, pairOf(e, "keph", "anna"), c.in)
		if errText != "" || out.Kind != c.kind || out.Name != c.name || out.Content != nil ||
			strings.Contains(rawOf(res), `"content"`) {
			t.Errorf("%+v: %+v, %s", c.in, out, errText)
		}
	}
	out, _, _ := e.read(t, e.anna(), ReadInput{Collection: "keph:wissen", Name: "dir"})
	if out.Writable == nil || !*out.Writable || out.ID != "" || out.Size != nil {
		t.Errorf("Verzeichnis: %+v", out)
	}
	out, _, _ = e.read(t, e.anna(), ReadInput{Collection: "keph:"})
	if out.Address != "keph:" || out.Writable != nil {
		t.Errorf("Wurzel des Hubs: %+v", out)
	}
	// otto darf privat nicht lesen: per id nichts, per Name nicht lesbar —
	// wie eine Collection, die es nicht gibt.
	geheim := e.hubs["keph"].put("privat", "zwei.md", "x")
	e.sync(t)
	if out, _, _ := e.read(t, e.otto(), ReadInput{ID: geheim}); out.Kind != KindNone {
		t.Errorf("fremde id: %+v", out)
	}
	for _, in := range []ReadInput{
		{Collection: "keph:privat", Name: "zwei.md"}, {Collection: "keph:privat"}, {Collection: "keph:gibts-nicht"},
	} {
		if _, _, errText := e.read(t, e.otto(), in); !strings.Contains(errText, "nicht lesbar") {
			t.Errorf("otto %+v: %s", in, errText)
		}
	}
	for _, in := range []ReadInput{
		{Collection: "keph:wissen", Name: "SYSTEM:A:anna"}, {Collection: "keph:wissen", Name: "/a.md"},
		{Collection: "keph:wissen", Name: "a.md", ID: ids["a.md"]}, {Name: "a.md"}, {Collection: "keph:", Name: "a.md"},
	} {
		if _, _, errText := e.read(t, e.anna(), in); errText == "" {
			t.Errorf("%+v ohne Fehler", in)
		}
	}
}

// pairOf sind die Header eines Accounts an einem Hub.
func pairOf(e *docEnv, hub, account string) map[string][]string {
	return pair(hub, account, e.tokens[hub+"/"+account])
}

// writable folgt je Name der Regel des Hubs (contract.Rights): unter
// vendor/<name>/ zählt allein der Scope, direkt in vendor/ ist es nie wahr,
// sonst write — für Dokumente per Name und per id, für Verzeichnisse und die
// Wurzel nach dem, was darunter läge. Drei Accounts: nur der Scope, nur
// write, beides.
func TestReadWritableVendor(t *testing.T) {
	e := newDocEnv(t)
	hub := e.hubs["keph"]
	e.tokens["keph/kp"], e.tokens["keph/beide"] = token(t), token(t)
	hub.grant("kp", "wissen", e.tokens["keph/kp"], contract.Rights{Vendor: []string{"k-playbook"}})
	hub.grant("beide", "wissen", e.tokens["keph/beide"], contract.Rights{Write: true, Vendor: []string{"k-playbook"}})
	ids := map[string]string{}
	for _, name := range []string{"a.md", "docs/b.md", "vendor/direkt.md", "vendor/k-playbook/rules/r.md",
		"vendor/anders/x.md", "Vendor/k-playbook/x.md"} {
		ids[name] = hub.put("wissen", name, "x")
	}
	e.sync(t)
	// anna: nur write; kp: nur der Scope; beide: write und der Scope.
	accounts := map[string]http.Header{"anna": pairOf(e, "keph", "anna"), "kp": pairOf(e, "keph", "kp"),
		"beide": pairOf(e, "keph", "beide")}
	cases := []struct {
		name            string
		kind            string
		anna, kp, beide bool
	}{
		{"a.md", KindDocument, true, false, true},
		{"docs/b.md", KindDocument, true, false, true},
		{"Vendor/k-playbook/x.md", KindDocument, true, false, true},
		{"vendor/direkt.md", KindDocument, false, false, false},
		{"vendor/k-playbook/rules/r.md", KindDocument, false, true, true},
		{"vendor/anders/x.md", KindDocument, false, false, false},
		{"", KindDirectory, true, false, true},
		{"docs", KindDirectory, true, false, true},
		{"vendor", KindDirectory, false, false, false},
		{"vendor/", KindDirectory, false, false, false},
		{"vendor/k-playbook", KindDirectory, false, true, true},
		{"vendor/k-playbook/rules", KindDirectory, false, true, true},
		{"vendor/anders", KindDirectory, false, false, false},
	}
	for _, c := range cases {
		want := map[string]bool{"anna": c.anna, "kp": c.kp, "beide": c.beide}
		for account, h := range accounts {
			out, _, errText := e.read(t, h, ReadInput{Collection: "keph:wissen", Name: c.name})
			if errText != "" || out.Kind != c.kind || out.Writable == nil || *out.Writable != want[account] {
				t.Errorf("%s liest %q: %+v, %s; erwartet %s writable %v", account, c.name, out, errText, c.kind, want[account])
			}
			if c.kind != KindDocument {
				continue
			}
			out, _, errText = e.read(t, h, ReadInput{Collection: "keph:", ID: ids[c.name]})
			if errText != "" || out.Kind != c.kind || out.Writable == nil || *out.Writable != want[account] {
				t.Errorf("%s liest id von %q: %+v, %s; erwartet writable %v", account, c.name, out, errText, want[account])
			}
		}
	}
	// Ohne jedes Recht ist nichts writable, auch nicht unter vendor/<name>/.
	for _, name := range []string{"a.md", "vendor/k-playbook/rules/r.md", "", "vendor/k-playbook"} {
		out, _, _ := e.read(t, e.otto(), ReadInput{Collection: "keph:wissen", Name: name})
		if out.Writable == nil || *out.Writable {
			t.Errorf("otto liest %q: %+v", name, out)
		}
	}
}

// writable mit einem Verzeichnis-Scope (Task 021): unter <pfad>/ wahr, ohne
// write und auch für Fremdes; daneben (docs2), darüber (Wurzel, Elternteil)
// und anderswo nach der allgemeinen Regel. Der Scope ist additiv: write gilt
// daneben weiter. whoami nennt ihn in Text und Struktur (dirs).
func TestReadWritableDirs(t *testing.T) {
	e := newDocEnv(t)
	hub := e.hubs["keph"]
	e.tokens["keph/dirs"], e.tokens["keph/beide"] = token(t), token(t)
	hub.grant("dirs", "wissen", e.tokens["keph/dirs"], contract.Rights{Dirs: []string{"docs", "tief/er"}})
	hub.grant("beide", "wissen", e.tokens["keph/beide"], contract.Rights{Write: true, Dirs: []string{"docs"}})
	ids := map[string]string{}
	for _, name := range []string{"a.md", "docs/b.md", "docs/sub/c.md", "docs2/d.md", "tief/e.md", "tief/er/f.md"} {
		ids[name] = hub.put("wissen", name, "x")
	}
	e.sync(t)
	// anna: nur write; dirs: nur die Verzeichnis-Scopes; beide: write und
	// docs.
	accounts := map[string]http.Header{"anna": pairOf(e, "keph", "anna"), "dirs": pairOf(e, "keph", "dirs"),
		"beide": pairOf(e, "keph", "beide")}
	cases := []struct {
		name             string
		kind             string
		anna, dirs, both bool
	}{
		{"a.md", KindDocument, true, false, true},
		{"docs/b.md", KindDocument, true, true, true},
		{"docs/sub/c.md", KindDocument, true, true, true},
		{"docs2/d.md", KindDocument, true, false, true},
		{"tief/e.md", KindDocument, true, false, true},
		{"tief/er/f.md", KindDocument, true, true, true},
		{"", KindDirectory, true, false, true},
		{"docs", KindDirectory, true, true, true},
		{"docs/", KindDirectory, true, true, true},
		{"docs/sub", KindDirectory, true, true, true},
		{"docs2", KindDirectory, true, false, true},
		{"tief", KindDirectory, true, false, true},
		{"tief/er", KindDirectory, true, true, true},
	}
	for _, c := range cases {
		want := map[string]bool{"anna": c.anna, "dirs": c.dirs, "beide": c.both}
		for account, h := range accounts {
			out, _, errText := e.read(t, h, ReadInput{Collection: "keph:wissen", Name: c.name})
			if errText != "" || out.Kind != c.kind || out.Writable == nil || *out.Writable != want[account] {
				t.Errorf("%s liest %q: %+v, %s; erwartet %s writable %v", account, c.name, out, errText, c.kind, want[account])
			}
			if c.kind != KindDocument {
				continue
			}
			out, _, errText = e.read(t, h, ReadInput{Collection: "keph:", ID: ids[c.name]})
			if errText != "" || out.Kind != c.kind || out.Writable == nil || *out.Writable != want[account] {
				t.Errorf("%s liest id von %q: %+v, %s; erwartet writable %v", account, c.name, out, errText, want[account])
			}
		}
	}

	// whoami: im Text als „dir <pfad>/“, in der Struktur als dirs — immer
	// eine Liste, auch leer.
	var who WhoamiOutput
	res, errText := e.call(t, pairOf(e, "keph", "dirs"), "whoami", struct{}{}, &who)
	if errText != "" {
		t.Fatal(errText)
	}
	var text string
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			text += tc.Text
		}
	}
	if !strings.Contains(text, "angemeldet als dirs (User dirs): keph:wissen (read, dir docs/, dir tief/er/)") {
		t.Errorf("Text:\n%s", text)
	}
	var got *HubInfo
	for i := range who.Hubs {
		if who.Hubs[i].Hub == "keph" {
			got = &who.Hubs[i]
		}
	}
	want := []CollectionRights{{Collection: "wissen", Address: "keph:wissen", Rights: []string{"read", "dir docs/", "dir tief/er/"},
		Dirs: []string{"docs", "tief/er"}}}
	if got == nil || !reflect.DeepEqual(got.Collections, want) {
		t.Errorf("whoami: %+v", got)
	}
	raw, _ := json.Marshal(res.StructuredContent)
	if !strings.Contains(string(raw), `"dirs":["docs","tief/er"]`) {
		t.Errorf("Struktur: %s", raw)
	}
	res, _ = e.call(t, pairOf(e, "keph", "anna"), "whoami", struct{}{}, nil)
	raw, _ = json.Marshal(res.StructuredContent)
	if !strings.Contains(string(raw), `"collection":"wissen","dirs":[],"rights":["read","write"]`) {
		t.Errorf("Struktur ohne Verzeichnis-Scopes: %s", raw)
	}
}

// rights in whoami trägt je Angabe genau ein Element, auch wenn ein
// Verzeichnis-Scope Komma und Leerzeichen im Namen hat (Task 021, Review):
// Die Liste entsteht aus den Rechten (contract.Rights.List), nicht aus ihrem
// Text. Ein Scope vendor/<name> daneben bleibt ein eigenes Element.
func TestWhoamiRightsDirWithComma(t *testing.T) {
	e := newDocEnv(t)
	e.tokens["keph/komma"] = token(t)
	rights, err := contract.NormalizeRights(contract.Rights{Vendor: []string{"k-playbook"}, Dirs: []string{"a, b", "docs"}})
	if err != nil {
		t.Fatal(err)
	}
	e.hubs["keph"].grant("komma", "wissen", e.tokens["keph/komma"], rights)
	e.sync(t)

	var who WhoamiOutput
	res, errText := e.call(t, pairOf(e, "keph", "komma"), "whoami", struct{}{}, &who)
	if errText != "" {
		t.Fatal(errText)
	}
	var got *HubInfo
	for i := range who.Hubs {
		if who.Hubs[i].Hub == "keph" {
			got = &who.Hubs[i]
		}
	}
	want := []CollectionRights{{Collection: "wissen", Address: "keph:wissen",
		Rights: []string{"read", "vendor/k-playbook", "dir a, b/", "dir docs/"}, Dirs: []string{"a, b", "docs"}}}
	if got == nil || !reflect.DeepEqual(got.Collections, want) {
		t.Errorf("whoami: %+v", got)
	}
	raw, _ := json.Marshal(res.StructuredContent)
	if !strings.Contains(string(raw), `"dirs":["a, b","docs"],"rights":["read","vendor/k-playbook","dir a, b/","dir docs/"]`) {
		t.Errorf("Struktur: %s", raw)
	}
	// Der Textteil bleibt der von contract.Rights.String.
	var text string
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			text += tc.Text
		}
	}
	if !strings.Contains(text, "keph:wissen (read, vendor/k-playbook, dir a, b/, dir docs/)") {
		t.Errorf("Text:\n%s", text)
	}
}

// Das Output-Schema von read nennt content als Text, die Beschreibung sagt,
// dass der Inhalt dort steht (Task 024).
func TestReadSchema(t *testing.T) {
	e := newDocEnv(t)
	ctx := context.Background()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: e.url + Path,
		HTTPClient: &http.Client{Transport: headerTransport{e.anna()}}, DisableStandaloneSSE: true, MaxRetries: -1}, nil)
	if err != nil {
		t.Fatalf("initialize: %v", err)
	}
	defer session.Close()
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools.Tools {
		if tool.Name != "read" {
			continue
		}
		raw, _ := json.Marshal(tool.OutputSchema)
		var schema struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if err := json.Unmarshal(raw, &schema); err != nil || !strings.Contains(string(schema.Properties["content"]), `"string"`) {
			t.Errorf("Output-Schema: %s", raw)
		}
		if !strings.Contains(tool.Description, "im Feld content") {
			t.Errorf("Beschreibung: %s", tool.Description)
		}
		return
	}
	t.Fatal("kein Werkzeug read")
}
