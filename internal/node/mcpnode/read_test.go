package mcpnode

import (
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
	}
	return out, res, errText
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
	if got := textOf(res); got != "Inhalt von dir/c.md" {
		t.Errorf("Inhalt: %q", got)
	}
	// Per id, ohne Hub-Teil (otto ist nur an keph angemeldet), ohne Recht
	// write.
	out, res, _ = e.read(t, e.otto(), ReadInput{ID: ids["a.md"]})
	if out.Kind != KindDocument || out.Name != "a.md" || out.Address != "keph:wissen" || out.Writable == nil ||
		*out.Writable || textOf(res) != "neu" {
		t.Errorf("per id: %+v, %q", out, textOf(res))
	}
	// Mit mehreren Hubs braucht id den Hub.
	if _, _, errText := e.read(t, e.anna(), ReadInput{ID: ids["a.md"]}); !strings.Contains(errText, "angemeldet an keph, team") {
		t.Errorf("id ohne Hub: %s", errText)
	}
	out, _, _ = e.read(t, e.anna(), ReadInput{Collection: "keph:", ID: ids["a.md"]})
	if out.Kind != KindDocument || out.Name != "a.md" {
		t.Errorf("id mit Hub: %+v", out)
	}
	// content: false — nur die Angaben, als JSON im Text.
	out, res, _ = e.read(t, e.anna(), ReadInput{Collection: "keph:wissen", Name: "a.md", Content: no()})
	if out.Kind != KindDocument || out.Size == nil || *out.Size != 3 || strings.Contains(textOf(res), `"neu"`) ||
		!strings.Contains(textOf(res), `"kind":"document"`) {
		t.Errorf("ohne Inhalt: %+v, %q", out, textOf(res))
	}
	// Leerer Inhalt ist ein Dokument mit Größe 0.
	e.hubs["keph"].put("wissen", "leer.md", "")
	e.sync(t)
	out, res, _ = e.read(t, e.anna(), ReadInput{Collection: "keph:wissen", Name: "leer.md"})
	if out.Kind != KindDocument || *out.Size != 0 || textOf(res) != "" {
		t.Errorf("leer: %+v, %q", out, textOf(res))
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
		out, _, errText := e.read(t, pairOf(e, "keph", "anna"), c.in)
		if errText != "" || out.Kind != c.kind || out.Name != c.name {
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
