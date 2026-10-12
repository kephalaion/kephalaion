package replication

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/kephalaion/kephalaion/internal/contract"
	"github.com/kephalaion/kephalaion/internal/contract/httpapi"
	"github.com/kephalaion/kephalaion/internal/hub/source"
	"github.com/kephalaion/kephalaion/internal/hub/source/sourcetest"
	"github.com/kephalaion/kephalaion/internal/hub/store"
)

// fetchFixture ist der Hub der Tests mit k-playbook und der öffentlichen
// Collection b (laptop darf sie abgleichen), fetch gegen ein nachgespieltes
// GitHub.
func fetchFixture(t *testing.T, transport string) (*fixture, *sourcetest.GitHub, map[string]contract.AccountAuth) {
	t.Helper()
	gh := sourcetest.New("kascada/k-playbook", "k-playbook-defaults.tar.gz")
	t.Cleanup(gh.Close)
	f := newLocalFixture(t)
	c := source.NewClient("test")
	c.Base = gh.URL
	f.hub.WithSource(c)
	if transport == "http" {
		srv := httptest.NewServer(httpapi.NewHandler(f.hub))
		t.Cleanup(srv.Close)
		cl, err := httpapi.NewClient(srv.URL, nil)
		if err != nil {
			t.Fatal(err)
		}
		f.client = cl
	}
	ctx := context.Background()
	if err := f.st.AddCollection(ctx, "k-playbook", "", false); err != nil {
		t.Fatal(err)
	}
	on := true
	if err := f.st.SetCollection(ctx, "b", store.CollectionChange{Public: &on}); err != nil {
		t.Fatal(err)
	}
	return f, gh, f.writers(t)
}

func (f *fixture) fetch(acc contract.AccountAuth, coll string) (contract.FetchResponse, error) {
	return f.api().Fetch(context.Background(), contract.FetchRequest{Version: contract.Version,
		Auth: contract.NodeAuth{Node: "laptop", Token: f.token}, Account: acc, Collection: coll})
}

// fetch über local und HTTP: Prüfreihenfolge, geholt, schon aktuell, die
// Codes der Quelle; geschrieben als system, in actions Account und Node.
func TestFetch(t *testing.T) {
	for _, tr := range transports {
		t.Run(tr, func(t *testing.T) {
			f, gh, acc := fetchFixture(t, tr)
			leser := acc["leser"]
			cases := []struct {
				name string
				acc  contract.AccountAuth
				coll string
				want error
			}{
				{"Account ungültig", contract.AccountAuth{Account: "leser", Token: acc["bob"].Token}, "k-playbook",
					contract.ErrAccountUnauthenticated},
				{"gesperrt", acc["gesperrt"], "k-playbook", contract.ErrAccountUnauthenticated},
				{"unbekannte Collection", leser, "fehlt", contract.ErrNotReadable},
				{"ungültiger Name", leser, "Fehlt:x", contract.ErrNotReadable},
				{"Node darf c nicht", acc["bob"], "c", contract.ErrNotReadable},
				{"öffentlich, ohne Quelle", leser, "b", contract.ErrNoSource},
				{"eigene Collection ohne Quelle", leser, "a", contract.ErrNoSource},
				{"kein Release", leser, "k-playbook", contract.ErrSourceMissing},
			}
			for _, c := range cases {
				if _, err := f.fetch(c.acc, c.coll); !errors.Is(err, c.want) {
					t.Errorf("%s: %v, erwartet %v", c.name, err, c.want)
				}
			}
			gh.Release("v1", nil)
			if _, err := f.fetch(leser, "k-playbook"); !errors.Is(err, contract.ErrSourceMissing) {
				t.Errorf("ohne Asset: %v", err)
			}
			gh.Release("v1", []byte("kein archiv"))
			if _, err := f.fetch(leser, "k-playbook"); !errors.Is(err, contract.ErrSourceInvalid) {
				t.Errorf("kaputtes Archiv: %v", err)
			}
			gh.Release("v1", sourcetest.Archive(map[string]string{"README.md": "r", "rules/a.md": "a"}))
			resp, err := f.fetch(leser, "k-playbook")
			if err != nil {
				t.Fatal(err)
			}
			if resp.Status != contract.FetchFetched || resp.Tag != "v1" || resp.Created != 2 || resp.Revision == 0 ||
				resp.Collection != "k-playbook" || resp.HubID == "" || resp.Version != contract.Version {
				t.Errorf("geholt: %+v", resp)
			}
			resp, err = f.fetch(acc["bob"], "k-playbook")
			if err != nil || resp.Status != contract.FetchCurrent || resp.Tag != "v1" || resp.Revision != 0 {
				t.Errorf("schon aktuell: %+v %v", resp, err)
			}
			docs, err := f.st.Documents(context.Background(), "k-playbook", "defaults")
			if err != nil || len(docs) != 2 || docs[0].CreatedBy != source.Author {
				t.Errorf("Dokumente: %+v %v", docs, err)
			}
			// Der neue Stand kommt mit sync, ohne grant.
			page := f.sync(t, f.request(100, contract.Since{Collection: "k-playbook"}))
			if !page.Collections[0].Allowed || len(page.Rows) != 2 {
				t.Errorf("sync: %+v", page)
			}
		})
	}
}

// Ohne WithSource holt der Hub nicht: source_unreachable, kein Netz.
func TestFetchWithoutSource(t *testing.T) {
	f := newLocalFixture(t)
	acc := f.writers(t)
	if err := f.st.AddCollection(context.Background(), "k-playbook", "", false); err != nil {
		t.Fatal(err)
	}
	if _, err := f.fetch(acc["leser"], "k-playbook"); !errors.Is(err, contract.ErrSourceUnreachable) {
		t.Errorf("ohne Client: %v", err)
	}
}
