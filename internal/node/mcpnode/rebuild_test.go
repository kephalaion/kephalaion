package mcpnode

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/kephalaion/kephalaion/internal/contract"
)

// rebuildDocs ruft rebuild_docs und liest die strukturierte Antwort, auch
// bei isError.
func (e *docEnv) rebuildDocs(t *testing.T, header http.Header, coll string) (RebuildDocsOutput, string) {
	t.Helper()
	res, errText := e.call(t, header, "rebuild_docs", RebuildDocsInput{Collection: coll}, nil)
	var out RebuildDocsOutput
	b, _ := json.Marshal(res.StructuredContent)
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out, errText
}

// rebuild_docs reicht an den Hub, mit Account und Token des Aufrufers, und
// stößt danach den Abgleich an — wie fetch_release (Task 033).
func TestRebuildDocs(t *testing.T) {
	e := newDocEnv(t)
	hub := e.hubs["keph"]
	hub.rebuildResp = contract.RebuildDocsResponse{HubID: hub.id, Version: 1, Collection: "wissen",
		Status: contract.RebuildRebuilt, Revision: 9, Created: 3, Updated: 1}
	out, errText := e.rebuildDocs(t, e.otto(), "keph:wissen")
	if errText != "" {
		t.Fatal(errText)
	}
	want := RebuildDocsOutput{Address: "keph:wissen", Status: "rebuilt", Revision: 9, Created: 3, Updated: 1}
	if out != want {
		t.Errorf("Antwort %+v", out)
	}
	if len(hub.rebuilt) != 1 || hub.rebuilt[0].Account.Account != "otto" ||
		hub.rebuilt[0].Account.Token != e.tokens["keph/otto"] || hub.rebuilt[0].Collection != "wissen" ||
		hub.rebuilt[0].Auth.Node != "laptop" {
		t.Errorf("Anfrage am Hub: %+v", hub.rebuilt)
	}
	if k := e.takeKicks(); !slices.Equal(k, []string{"keph"}) {
		t.Errorf("Anstöße %v", k)
	}
	if !strings.Contains(e.log.String(), "op=rebuild_docs") || strings.Contains(e.log.String(), e.tokens["keph/otto"]) {
		t.Errorf("Log:\n%s", e.log.String())
	}
	hub.rebuildResp = contract.RebuildDocsResponse{HubID: hub.id, Version: 1, Collection: "wissen",
		Status: contract.RebuildNoDocs}
	if out, _ := e.rebuildDocs(t, e.otto(), "wissen"); out.Status != "no_docs" || out.Revision != 0 {
		t.Errorf("ohne docs/: %+v", out)
	}
	e.takeKicks()

	cases := []struct {
		name   string
		header http.Header
		coll   string
		err    error
		code   string
		kick   bool
	}{
		{"ohne Anmeldung", nil, "keph:wissen", nil, "not_readable", false},
		{"unbekannter Hub", e.otto(), "fremd:wissen", nil, "not_readable", false},
		{"ohne Collection", e.otto(), "keph:", nil, "invalid", false},
		{"Quelle", e.otto(), "keph:k-playbook", &contract.Error{Code: contract.CodeForbidden, Message: "Quelle"},
			"forbidden", false},
		{"nicht lesbar am Hub", e.otto(), "keph:wissen", contract.ErrNotReadable, "not_readable", false},
		{"Account abgelehnt", e.otto(), "keph:wissen", contract.ErrAccountUnauthenticated, "not_readable", false},
		{"unklar", e.otto(), "keph:wissen", fmt.Errorf("%w: weg", contract.ErrOutcomeUnknown), "outcome_unknown", true},
		{"alter Hub", e.otto(), "keph:wissen", contract.ErrUnknownOperation, "unsupported", false},
		{"nicht erreicht", e.otto(), "keph:wissen", errors.New("Verbindung abgelehnt"), "unreachable", false},
	}
	for _, c := range cases {
		hub.rebuildErr = c.err
		out, errText := e.rebuildDocs(t, c.header, c.coll)
		if errText == "" || out.Error == nil || out.Error.Code != c.code {
			t.Errorf("%s: %+v, %q", c.name, out, errText)
		}
		if k := e.takeKicks(); (len(k) > 0) != c.kick {
			t.Errorf("%s: Anstöße %v", c.name, k)
		}
	}
}
