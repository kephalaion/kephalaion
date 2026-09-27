package contract

import (
	"encoding/json"
	"errors"
	"fmt"
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
}

// Die Schreibvorgänge als JSON: Account als Objekt wie bei whoami, content
// auch leer, base_revision nur, wenn gesetzt; Fassung und Anmeldung des
// Nodes nie im Body.
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
