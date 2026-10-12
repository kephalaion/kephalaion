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

// fetchRelease ruft fetch_release und liest die strukturierte Antwort, auch
// bei isError.
func (e *docEnv) fetchRelease(t *testing.T, header http.Header, coll string) (FetchReleaseOutput, string) {
	t.Helper()
	res, errText := e.call(t, header, "fetch_release", FetchReleaseInput{Collection: coll}, nil)
	var out FetchReleaseOutput
	b, _ := json.Marshal(res.StructuredContent)
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out, errText
}

// fetch_release reicht an den Hub, mit Account und Token des Aufrufers, und
// stößt danach den Abgleich an; jeder gültig angemeldete Account darf es,
// ob die Collection lesbar ist und eine Quelle hat, entscheidet der Hub.
func TestFetchRelease(t *testing.T) {
	e := newDocEnv(t)
	hub := e.hubs["keph"]
	hub.fetchResp = contract.FetchResponse{HubID: hub.id, Version: 1, Collection: "k-playbook",
		Status: contract.FetchFetched, Tag: "v1.0.0", Revision: 9, Created: 3, Deleted: 1}
	out, errText := e.fetchRelease(t, e.otto(), "keph:k-playbook")
	if errText != "" {
		t.Fatal(errText)
	}
	want := FetchReleaseOutput{Address: "keph:k-playbook", Status: "fetched", Tag: "v1.0.0", Revision: 9, Created: 3, Deleted: 1}
	if out != want {
		t.Errorf("Antwort %+v", out)
	}
	if len(hub.fetched) != 1 || hub.fetched[0].Account.Account != "otto" || hub.fetched[0].Account.Token != e.tokens["keph/otto"] ||
		hub.fetched[0].Collection != "k-playbook" || hub.fetched[0].Auth.Node != "laptop" {
		t.Errorf("Anfrage am Hub: %+v", hub.fetched)
	}
	if k := e.takeKicks(); !slices.Equal(k, []string{"keph"}) {
		t.Errorf("Anstöße %v", k)
	}
	// Der Hub-Teil ist entbehrlich, wenn der Aufrufer nur an einem Hub
	// angemeldet ist; die Log-Zeile nennt den Vorgang.
	if _, errText := e.fetchRelease(t, e.otto(), "k-playbook"); errText != "" {
		t.Errorf("ohne Hub-Teil: %s", errText)
	}
	if !strings.Contains(e.log.String(), "op=fetch_release") {
		t.Errorf("Log ohne Vorgang:\n%s", e.log.String())
	}
	if strings.Contains(e.log.String(), e.tokens["keph/otto"]) {
		t.Errorf("Token im Log:\n%s", e.log.String())
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
		{"ohne Anmeldung", nil, "keph:k-playbook", nil, "not_readable", false},
		{"falsches Token", pair("keph", "otto", e.tokens["keph/anna"]), "keph:k-playbook", nil, "not_readable", false},
		{"unbekannter Hub", e.otto(), "fremd:k-playbook", nil, "not_readable", false},
		{"ohne Collection", e.otto(), "keph:", nil, "invalid", false},
		{"keine Quelle", e.otto(), "keph:wissen", &contract.Error{Code: contract.CodeNoSource, Message: "keine Quelle"},
			"no_source", false},
		{"nicht lesbar am Hub", e.otto(), "keph:k-playbook", contract.ErrNotReadable, "not_readable", false},
		{"Account abgelehnt", e.otto(), "keph:k-playbook", contract.ErrAccountUnauthenticated, "not_readable", false},
		{"Quelle fehlt", e.otto(), "keph:k-playbook", contract.ErrSourceMissing, "source_missing", false},
		{"unklar", e.otto(), "keph:k-playbook", fmt.Errorf("%w: weg", contract.ErrOutcomeUnknown), "outcome_unknown", true},
		{"alter Hub", e.otto(), "keph:k-playbook", contract.ErrUnknownOperation, "unsupported", false},
		{"nicht erreicht", e.otto(), "keph:k-playbook", errors.New("Verbindung abgelehnt"), "unreachable", false},
	}
	for _, c := range cases {
		hub.fetchErr = c.err
		out, errText := e.fetchRelease(t, c.header, c.coll)
		if errText == "" || out.Error == nil || out.Error.Code != c.code {
			t.Errorf("%s: %+v, %q", c.name, out, errText)
		}
		if k := e.takeKicks(); (len(k) > 0) != c.kick {
			t.Errorf("%s: Anstöße %v", c.name, k)
		}
	}
}
