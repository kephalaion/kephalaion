package contract

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func ptr(s string) *string { return &s }

// TestJSON prüft, dass Anfrage und Antwort als JSON laufen: Fassung und
// Anmeldung stehen nicht im Body, NULL bleibt null und kommt als nil zurück.
func TestJSON(t *testing.T) {
	req := SyncRequest{
		Version:     Version,
		Auth:        NodeAuth{Node: "laptop", Token: "keph_geheim"},
		Collections: []Since{{Collection: "a", Since: 3}},
		PageSize:    DefaultPageSize,
	}
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	if s := string(b); strings.Contains(s, "keph_geheim") || strings.Contains(s, "laptop") ||
		s != `{"collections":[{"collection":"a","since":3}],"page_size":500}` {
		t.Errorf("Anfrage als JSON: %s", s)
	}

	resp := SyncResponse{
		HubID: "01HUB", Version: Version,
		Collections: []CollectionStatus{{Collection: "a", Allowed: true}},
		Allowed:     []string{"a"},
		Rows: []Row{
			{ID: "1", Collection: "a", Name: "x.md", Content: ptr(""), Meta: ptr(`{"k":1}`), Revision: 2},
			{ID: "2", Collection: "a", Name: "y.md", Deleted: true, Revision: 3},
		},
		HubRevision: 3, Until: 3,
	}
	b, err = json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"content":null,"meta":null,"deleted":true`) {
		t.Errorf("Löschmarke als JSON: %s", b)
	}
	var back SyncResponse
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	r0, r1 := back.Rows[0], back.Rows[1]
	if r0.Content == nil || *r0.Content != "" || r0.Meta == nil || *r0.Meta != `{"k":1}` {
		t.Errorf("leerer Inhalt und meta: %+v", r0)
	}
	if r1.Content != nil || r1.Meta != nil || !r1.Deleted {
		t.Errorf("NULL: %+v", r1)
	}
	if back.HubID != "01HUB" || back.Version != Version || back.Until != 3 || back.HubRevision != 3 {
		t.Errorf("Antwort: %+v", back)
	}
}

func TestErrorIs(t *testing.T) {
	err := fmt.Errorf("hub x: %w", Invalid("Seitengröße 0"))
	if !errors.Is(err, ErrInvalid) || errors.Is(err, ErrUnauthenticated) {
		t.Errorf("errors.Is: %v", err)
	}
	var e *Error
	if !errors.As(err, &e) || e.Code != CodeInvalid {
		t.Errorf("errors.As: %#v", err)
	}
	b, err := json.Marshal(ErrUnsupportedVersion)
	if err != nil || string(b) != `{"code":"unsupported_version","message":"Fassung nicht unterstützt"}` {
		t.Errorf("Fehler als JSON: %s, %v", b, err)
	}
}

// Die Account-Zeile trägt hash, user und rights in genau dieser Form; ohne
// user ist sie ein Fehler dieser Zeile.
func TestAccountContent(t *testing.T) {
	hash := strings.Repeat("a", 64)
	s, err := EncodeAccountContent(AccountContent{Hash: hash, User: "kleist", Rights: Rights{Write: true}})
	if err != nil || s != `{"hash":"`+hash+`","user":"kleist","rights":{"write":true,"supersede":false}}` {
		t.Errorf("Inhalt = %s, %v", s, err)
	}
	if _, err := EncodeAccountContent(AccountContent{Hash: hash}); err == nil {
		t.Error("Inhalt ohne user geschrieben")
	}
	c, err := DecodeAccountContent(s)
	if err != nil || c.User != "kleist" || c.Hash != hash || !c.Rights.Write {
		t.Errorf("gelesen = %+v, %v", c, err)
	}
	for _, bad := range []string{
		`{"hash":"` + hash + `","rights":{"write":false,"supersede":false}}`,
		`{"hash":"` + hash + `","user":null,"rights":{}}`,
		`{"hash":"` + hash + `","user":"","rights":{}}`,
		`{"hash":"kurz","user":"kleist","rights":{}}`,
	} {
		if _, err := DecodeAccountContent(bad); err == nil {
			t.Errorf("angenommen: %s", bad)
		}
	}

	// Die Scopes stehen nur, wenn es welche gibt, sortiert und ohne Doppel;
	// eine Zeile ohne vendor (Hub vor Task 016) liest sich als leere Liste.
	s, err = EncodeAccountContent(AccountContent{Hash: hash, User: "kleist",
		Rights: Rights{Vendor: []string{"zwei", "eins", "zwei"}}})
	if err != nil || s != `{"hash":"`+hash+`","user":"kleist","rights":{"write":false,"supersede":false,"vendor":["eins","zwei"]}}` {
		t.Errorf("Inhalt mit Scopes = %s, %v", s, err)
	}
	c, err = DecodeAccountContent(s)
	if err != nil || !reflect.DeepEqual(c.Rights.Vendor, []string{"eins", "zwei"}) {
		t.Errorf("Scopes gelesen = %+v, %v", c, err)
	}
	c, err = DecodeAccountContent(`{"hash":"` + hash + `","user":"kleist","rights":{"write":true,"supersede":false,"vendor":[]}}`)
	if err != nil || c.Rights.Vendor != nil {
		t.Errorf("leere Scopes gelesen = %+v, %v", c, err)
	}
}

// Die Rechte als Text und im Vergleich: read immer zuerst, die Scopes
// zuletzt; gleich sind Rechte mit denselben Scopes, gleich in welcher
// Reihenfolge. NormalizeRights prüft die Namen und sortiert.
func TestRightsStringAndEqual(t *testing.T) {
	for _, c := range []struct {
		r    Rights
		want string
	}{
		{Rights{}, "read"},
		{Rights{Write: true}, "read, write"},
		{Rights{Write: true, Supersede: true}, "read, write, supersede"},
		{Rights{Vendor: []string{"k-playbook"}}, "read, vendor/k-playbook"},
		{Rights{Supersede: true, Vendor: []string{"a", "b"}}, "read, supersede, vendor/a, vendor/b"},
	} {
		if got := c.r.String(); got != c.want {
			t.Errorf("%+v: %q, erwartet %q", c.r, got, c.want)
		}
	}
	a := Rights{Write: true, Vendor: []string{"b", "a"}}
	b := Rights{Write: true, Vendor: []string{"a", "b", "a"}}
	if !a.Equal(b) || !b.Equal(a) || !(Rights{}).Equal(Rights{Vendor: []string{}}) {
		t.Error("gleiche Rechte gelten als verschieden")
	}
	for _, o := range []Rights{{Write: true}, {Write: true, Vendor: []string{"a"}}, {Vendor: []string{"a", "b"}},
		{Write: true, Supersede: true, Vendor: []string{"a", "b"}}} {
		if a.Equal(o) {
			t.Errorf("%+v gilt als gleich %+v", a, o)
		}
	}
	n, err := NormalizeRights(b)
	if err != nil || !reflect.DeepEqual(n, Rights{Write: true, Vendor: []string{"a", "b"}}) {
		t.Errorf("NormalizeRights = %+v, %v", n, err)
	}
	if n, err := NormalizeRights(Rights{Vendor: []string{}}); err != nil || n.Vendor != nil {
		t.Errorf("leer normalisiert = %+v, %v", n, err)
	}
	for _, bad := range []string{"", "K-Playbook", "system-x", "a:b", "mit leerzeichen"} {
		if _, err := NormalizeRights(Rights{Vendor: []string{"ok", bad}}); err == nil ||
			!strings.Contains(err.Error(), "Scope vendor/<name>") {
			t.Errorf("Scope %q angenommen: %v", bad, err)
		}
	}
}

// Die eine Regel, welches Recht ein Name braucht: unter vendor/<name>/ allein
// der Scope, ohne Rücksicht auf write und Urheber; vendor selbst und direkt
// darin niemand; sonst write für Eigenes, supersede für Fremdes. Nur die
// Kleinschreibung vendor ist besonders.
func TestMayWrite(t *testing.T) {
	for _, c := range []struct {
		name     string
		vendor   string
		reserved bool
	}{
		{"x.md", "", false},
		{"docs/vendor/x.md", "", false},
		{"Vendor/k/x.md", "", false},
		{"vendors/k/x.md", "", false},
		{"vendor", "", true},
		{"vendor/x.md", "", true},
		{"vendor/k-playbook", "", true},
		{"vendor/k-playbook/x.md", "k-playbook", false},
		{"vendor/k-playbook/a/b/c.md", "k-playbook", false},
		{"vendor/Foo/x.md", "Foo", false},
	} {
		v, r := VendorOf(c.name)
		if v != c.vendor || r != c.reserved {
			t.Errorf("VendorOf(%q) = %q, %v; erwartet %q, %v", c.name, v, r, c.vendor, c.reserved)
		}
	}
	scope := Rights{Vendor: []string{"k-playbook"}}
	writer := Rights{Write: true}
	super := Rights{Supersede: true}
	both := Rights{Write: true, Supersede: true, Vendor: []string{"k-playbook"}}
	none := Rights{}
	for _, c := range []struct {
		what string
		r    Rights
		name string
		own  bool
		want Denial
	}{
		{"Scope unter vendor/<name>/, fremd", scope, "vendor/k-playbook/x.md", false, 0},
		{"Scope unter vendor/<name>/, eigen", scope, "vendor/k-playbook/a/x.md", true, 0},
		{"Scope anderswo, eigen", scope, "x.md", true, DenyWrite},
		{"Scope anderswo, fremd", scope, "x.md", false, DenySupersede},
		{"Scope, anderer Name", scope, "vendor/anders/x.md", true, DenyVendor},
		{"Scope, direkt in vendor/", scope, "vendor/x.md", true, DenyReserved},
		{"Scope, vendor selbst", scope, "vendor", true, DenyReserved},
		{"write unter vendor/<name>/", writer, "vendor/k-playbook/x.md", true, DenyVendor},
		{"supersede unter vendor/<name>/", super, "vendor/k-playbook/x.md", false, DenyVendor},
		{"alles, direkt in vendor/", both, "vendor/x.md", true, DenyReserved},
		{"alles unter vendor/<name>/", both, "vendor/k-playbook/x.md", false, 0},
		{"write, eigen", writer, "x.md", true, 0},
		{"write, fremd", writer, "x.md", false, DenySupersede},
		{"supersede, fremd", super, "x.md", false, 0},
		{"supersede, eigen", super, "x.md", true, DenyWrite},
		{"nichts, eigen", none, "x.md", true, DenyWrite},
		{"nichts, fremd", none, "x.md", false, DenySupersede},
	} {
		d := c.r.MayWrite(c.name, c.own)
		switch {
		case c.want == 0 && d != nil:
			t.Errorf("%s: %v, erwartet erlaubt", c.what, d)
		case c.want != 0 && d == nil:
			t.Errorf("%s: erlaubt, erwartet %d", c.what, c.want)
		case c.want != 0 && d.Kind != c.want:
			t.Errorf("%s: %v (%d), erwartet %d", c.what, d, d.Kind, c.want)
		}
	}
	if d := scope.MayWrite("vendor/anders/x.md", true); d.Vendor != "anders" || d.Error() != "Scope vendor/anders fehlt" {
		t.Errorf("Grund: %v (%q)", d, d.Vendor)
	}
	if d := none.MayWrite("vendor/x.md", true); d.Error() != "direkt in vendor/ schreibt niemand" {
		t.Errorf("Grund: %v", d)
	}

	// writable: für ein Dokument wie Neues oder Eigenes, für ein Verzeichnis
	// nach dem, was darunter läge.
	for _, c := range []struct {
		r    Rights
		name string
		want bool
	}{
		{scope, "vendor/k-playbook/x.md", true}, {scope, "x.md", false}, {scope, "vendor/x.md", false},
		{writer, "x.md", true}, {writer, "vendor/k-playbook/x.md", false}, {super, "x.md", false},
	} {
		if got := c.r.Writable(c.name); got != c.want {
			t.Errorf("%+v Writable(%q) = %v", c.r, c.name, got)
		}
	}
	for _, c := range []struct {
		r    Rights
		dir  string
		want bool
	}{
		{scope, "", false}, {scope, "vendor", false}, {scope, "vendor/", false}, {scope, "vendor/k-playbook", true},
		{scope, "vendor/k-playbook/", true}, {scope, "vendor/k-playbook/tief", true}, {scope, "vendor/anders", false},
		{scope, "docs", false}, {writer, "", true}, {writer, "docs", true}, {writer, "vendor", false},
		{writer, "vendor/k-playbook", false}, {super, "", false}, {both, "vendor", false}, {both, "vendor/k-playbook", true},
	} {
		if got := c.r.WritableUnder(c.dir); got != c.want {
			t.Errorf("%+v WritableUnder(%q) = %v", c.r, c.dir, got)
		}
	}
}

// Die Schreibvorgänge als JSON: Account als Objekt wie bei whoami, content
// auch leer, base_revision und recursive nur, wenn gesetzt; Fassung und
// Anmeldung des Nodes nie im Body.
func TestWriteJSON(t *testing.T) {
	acc := AccountAuth{Account: "bob", Token: "keph_account"}
	node := NodeAuth{Node: "laptop", Token: "keph_node"}
	base := int64(4)
	for _, c := range []struct {
		req  any
		want string
	}{
		{CreateRequest{Version: Version, Auth: node, Account: acc, Collection: "a", Name: "n.md"},
			`{"account":{"account":"bob","token":"keph_account"},"collection":"a","name":"n.md","content":""}`},
		{WriteRequest{Version: Version, Auth: node, Account: acc, Collection: "a", Name: "n.md", Content: "x", BaseRevision: &base},
			`{"account":{"account":"bob","token":"keph_account"},"collection":"a","name":"n.md","content":"x","base_revision":4}`},
		{DeleteRequest{Version: Version, Auth: node, Account: acc, Collection: "a", Name: "n.md"},
			`{"account":{"account":"bob","token":"keph_account"},"collection":"a","name":"n.md"}`},
		{DeleteRequest{Version: Version, Auth: node, Account: acc, Collection: "a", Name: "dir", Recursive: true},
			`{"account":{"account":"bob","token":"keph_account"},"collection":"a","name":"dir","recursive":true}`},
		{RenameRequest{Version: Version, Auth: node, Account: acc, Collection: "a", Name: "n.md", NewName: "neu/n.md"},
			`{"account":{"account":"bob","token":"keph_account"},"collection":"a","name":"n.md","new_name":"neu/n.md"}`},
		{RenameRequest{Version: Version, Auth: node, Account: acc, Collection: "a", Name: "n.md", NewName: "m.md", BaseRevision: &base},
			`{"account":{"account":"bob","token":"keph_account"},"collection":"a","name":"n.md","new_name":"m.md","base_revision":4}`},
	} {
		b, err := json.Marshal(c.req)
		if err != nil || string(b) != c.want {
			t.Errorf("%T als JSON: %s, %v", c.req, b, err)
		}
	}
	b, _ := json.Marshal(WriteResponse{HubID: "01H", Version: Version, Revision: 5, Rows: []Row{}})
	if string(b) != `{"hub_id":"01H","version":1,"revision":5,"rows":[]}` {
		t.Errorf("Antwort als JSON: %s", b)
	}
}

// Jeder Code steht einmal in Codes; der unbekannte Vorgang und der unklare
// Ausgang sind keine Fehler des Vertrags.
func TestCodes(t *testing.T) {
	seen := map[Code]bool{}
	for _, c := range Codes {
		if seen[c] {
			t.Errorf("Code %s doppelt", c)
		}
		seen[c] = true
	}
	for _, c := range []Code{CodeNotReadable, CodeForbidden, CodeNotFound, CodeNameTaken, CodePathConflict, CodeStaleRevision} {
		if !seen[c] {
			t.Errorf("Code %s fehlt in Codes", c)
		}
	}
	var e *Error
	for _, err := range []error{ErrUnknownOperation, ErrOutcomeUnknown} {
		if errors.As(err, &e) || errors.Is(err, ErrInvalid) {
			t.Errorf("%v gilt als Fehler des Vertrags", err)
		}
	}
	if err := fmt.Errorf("Hub: %w", &Error{Code: CodeStaleRevision, Message: "Revision 7"}); !errors.Is(err, ErrStaleRevision) {
		t.Errorf("errors.Is: %v", err)
	}
}

// Der Verzeichnis-Scope (Task 021): unter <pfad>/ schreiben ohne write und
// unabhängig vom Urheber, Grenze ein ganzes Segment, additiv zu write und
// supersede; vendor/ bleibt, wie es ist. In der Zeile als dirs, sortiert und
// ohne Doppel, nur wenn es welche gibt; in String als „dir <pfad>/“.
func TestDirScope(t *testing.T) {
	dirs := Rights{Dirs: []string{"docs"}}
	nested := Rights{Dirs: []string{"a/b"}}
	writer := Rights{Write: true, Dirs: []string{"docs"}}
	for _, c := range []struct {
		what string
		r    Rights
		name string
		own  bool
		want Denial
	}{
		{"Scope, darunter, fremd", dirs, "docs/x.md", false, 0},
		{"Scope, darunter, eigen", dirs, "docs/x.md", true, 0},
		{"Scope, tief darunter, fremd", dirs, "docs/a/b/c.md", false, 0},
		{"Scope, das Verzeichnis selbst als Dokument", dirs, "docs", true, DenyWrite},
		{"Scope, daneben (docs2)", dirs, "docs2/x.md", true, DenyWrite},
		{"Scope, daneben fremd", dirs, "docs2/x.md", false, DenySupersede},
		{"Scope, darüber (Wurzel)", dirs, "x.md", true, DenyWrite},
		{"Scope, anderes Verzeichnis", dirs, "notes/x.md", false, DenySupersede},
		{"Scope, gleicher Name tiefer", dirs, "a/docs/x.md", true, DenyWrite},
		{"geschachtelt, darunter", nested, "a/b/x.md", false, 0},
		{"geschachtelt, darüber", nested, "a/x.md", true, DenyWrite},
		{"geschachtelt, daneben", nested, "a/bc/x.md", true, DenyWrite},
		{"Scope, unter vendor/<name>/", dirs, "vendor/k/x.md", true, DenyVendor},
		{"Scope, direkt in vendor/", dirs, "vendor/x.md", true, DenyReserved},
		{"write und Scope, fremd darunter", writer, "docs/x.md", false, 0},
		{"write und Scope, eigen daneben", writer, "notes/x.md", true, 0},
		{"write und Scope, fremd daneben", writer, "notes/x.md", false, DenySupersede},
	} {
		d := c.r.MayWrite(c.name, c.own)
		switch {
		case c.want == 0 && d != nil:
			t.Errorf("%s: %v, erwartet erlaubt", c.what, d)
		case c.want != 0 && d == nil:
			t.Errorf("%s: erlaubt, erwartet %d", c.what, c.want)
		case c.want != 0 && d.Kind != c.want:
			t.Errorf("%s: %v (%d), erwartet %d", c.what, d, d.Kind, c.want)
		}
	}
	if d := dirs.MayWrite("docs2/x.md", false); d.Error() != "supersede fehlt" {
		t.Errorf("Grund außerhalb des Scopes: %v", d)
	}
	for _, c := range []struct {
		r    Rights
		dir  string
		want bool
	}{
		{dirs, "", false}, {dirs, "docs", true}, {dirs, "docs/", true}, {dirs, "docs/sub", true}, {dirs, "docs2", false},
		{dirs, "notes", false}, {nested, "a", false}, {nested, "a/b", true}, {nested, "a/b/c", true},
	} {
		if got := c.r.WritableUnder(c.dir); got != c.want {
			t.Errorf("%+v WritableUnder(%q) = %v", c.r, c.dir, got)
		}
	}
	if !dirs.Writable("docs/x.md") || dirs.Writable("docs2/x.md") || dirs.Writable("docs") {
		t.Error("Writable mit Verzeichnis-Scope")
	}
	if d, ok := (Rights{Dirs: []string{"a", "a/b"}}).DirScopeOf("a/b/c.md"); !ok || d != "a" {
		t.Errorf("DirScopeOf: %q, %v", d, ok)
	}

	// Form: geprüft, sortiert, ohne Doppel; leer wird nil.
	n, err := NormalizeRights(Rights{Write: true, Dirs: []string{"z", "docs/sub", "docs", "z"}})
	if err != nil || !reflect.DeepEqual(n, Rights{Write: true, Dirs: []string{"docs", "docs/sub", "z"}}) {
		t.Errorf("NormalizeRights = %+v, %v", n, err)
	}
	if n, err := NormalizeRights(Rights{Dirs: []string{}}); err != nil || n.Dirs != nil {
		t.Errorf("leer normalisiert = %+v, %v", n, err)
	}
	for _, c := range []struct{ dir, want string }{
		{"", "Verzeichnis-Scope: Verzeichnis fehlt"},
		{"vendor", "unter vendor/ gilt allein der Scope vendor/<name>"},
		{"vendor/x", "unter vendor/ gilt allein"},
		{"vendor/x/y", "unter vendor/ gilt allein"},
		{"..", "'.' und '..'"},
		{"a/../b", "'.' und '..'"},
		{"docs/", "endet mit '/'"},
		{"/docs", "beginnt oder endet mit '/'"},
		{"a//b", "leeres Segment"},
		{"SYSTEM:x", "dem Hub vorbehalten"},
	} {
		if _, err := NormalizeRights(Rights{Dirs: []string{"ok", c.dir}}); err == nil || !strings.Contains(err.Error(), c.want) ||
			!strings.Contains(err.Error(), "Verzeichnis-Scope") {
			t.Errorf("Verzeichnis-Scope %q: %v, erwartet %q", c.dir, err, c.want)
		}
	}
	// Nur die Kleinschreibung vendor ist besonders.
	if _, err := NormalizeRights(Rights{Dirs: []string{"Vendor", "vendors/x", "docs/vendor"}}); err != nil {
		t.Errorf("Vendor, vendors/x, docs/vendor abgelehnt: %v", err)
	}

	// Anzeige und Vergleich.
	for _, c := range []struct {
		r    Rights
		want string
	}{
		{Rights{Dirs: []string{"test-docs"}}, "read, dir test-docs/"},
		{Rights{Write: true, Vendor: []string{"k"}, Dirs: []string{"a", "b/c"}}, "read, write, vendor/k, dir a/, dir b/c/"},
	} {
		if got := c.r.String(); got != c.want {
			t.Errorf("%+v: %q, erwartet %q", c.r, got, c.want)
		}
	}
	// Einzeln (List): je Angabe genau ein Element, im Wortlaut und in der
	// Reihenfolge von String — auch wenn ein Verzeichnis Komma und
	// Leerzeichen trägt; der Text ließe sich dann nicht mehr zerlegen.
	for _, c := range []struct {
		r    Rights
		want []string
	}{
		{Rights{}, []string{"read"}},
		{Rights{Write: true, Supersede: true, Vendor: []string{"a", "b"}, Dirs: []string{"docs", "tief/er"}},
			[]string{"read", "write", "supersede", "vendor/a", "vendor/b", "dir docs/", "dir tief/er/"}},
		{Rights{Vendor: []string{"k-playbook"}, Dirs: []string{"a, b", "c, dir d/e"}},
			[]string{"read", "vendor/k-playbook", "dir a, b/", "dir c, dir d/e/"}},
	} {
		n, err := NormalizeRights(c.r)
		if err != nil {
			t.Errorf("%+v abgelehnt: %v", c.r, err)
			continue
		}
		if got := n.List(); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%+v: List %q, erwartet %q", c.r, got, c.want)
		}
		if got, want := n.String(), strings.Join(c.want, ", "); got != want {
			t.Errorf("%+v: %q, erwartet %q", c.r, got, want)
		}
	}
	if !(Rights{Dirs: []string{"b", "a"}}).Equal(Rights{Dirs: []string{"a", "b", "a"}}) || !(Rights{}).Equal(Rights{Dirs: []string{}}) {
		t.Error("gleiche Verzeichnis-Scopes gelten als verschieden")
	}
	if (Rights{Dirs: []string{"a"}}).Equal(Rights{}) || (Rights{Dirs: []string{"a"}}).Equal(Rights{Vendor: []string{"a"}}) {
		t.Error("verschiedene Verzeichnis-Scopes gelten als gleich")
	}

	// In der Zeile: dirs nur, wenn es welche gibt, sortiert und ohne Doppel;
	// eine Zeile ohne dirs (Hub vor Task 021) liest sich als leere Liste.
	hash := strings.Repeat("b", 64)
	s, err := EncodeAccountContent(AccountContent{Hash: hash, User: "kleist",
		Rights: Rights{Vendor: []string{"k"}, Dirs: []string{"z", "a", "z"}}})
	if err != nil || s != `{"hash":"`+hash+`","user":"kleist","rights":{"write":false,"supersede":false,"vendor":["k"],"dirs":["a","z"]}}` {
		t.Errorf("Inhalt mit Verzeichnis-Scopes = %s, %v", s, err)
	}
	c, err := DecodeAccountContent(s)
	if err != nil || !reflect.DeepEqual(c.Rights.Dirs, []string{"a", "z"}) {
		t.Errorf("Verzeichnis-Scopes gelesen = %+v, %v", c, err)
	}
	c, err = DecodeAccountContent(`{"hash":"` + hash + `","user":"kleist","rights":{"write":true,"supersede":false,"dirs":[]}}`)
	if err != nil || c.Rights.Dirs != nil {
		t.Errorf("leere Verzeichnis-Scopes gelesen = %+v, %v", c, err)
	}
	c, err = DecodeAccountContent(`{"hash":"` + hash + `","user":"kleist","rights":{"write":true,"supersede":false}}`)
	if err != nil || c.Rights.Dirs != nil || !c.Rights.Write {
		t.Errorf("Zeile ohne dirs gelesen = %+v, %v", c, err)
	}
}
