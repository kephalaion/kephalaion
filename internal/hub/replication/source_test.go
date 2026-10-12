package replication

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/kephalaion/kephalaion/internal/contract"
	"github.com/kephalaion/kephalaion/internal/hub/store"
)

// Task 031: Eine Collection aus der Liste der Quellen (k-playbook) steht für
// jeden Node in allowed, öffentlich und fest abonniert, ohne grant; eine,
// die nur öffentlich ist, steht nur mit grant da und ist nicht fest
// abonniert.
func TestAllowedPublicSubscribed(t *testing.T) {
	forTransports(t, func(t *testing.T, newFixture func(*testing.T) *fixture) {
		f := newFixture(t)
		ctx := context.Background()
		resp := f.sync(t, f.request(10))
		if !slices.Equal(resp.Allowed, []string{"a", "b"}) || len(resp.Public) != 0 || len(resp.Subscribed) != 0 ||
			resp.Public == nil || resp.Subscribed == nil {
			t.Fatalf("vorher: %+v", resp)
		}
		if err := f.st.AddCollection(ctx, "k-playbook", "", false); err != nil {
			t.Fatal(err)
		}
		on := true
		for _, c := range []string{"b", "c"} {
			if err := f.st.SetCollection(ctx, c, store.CollectionChange{Public: &on}); err != nil {
				t.Fatal(err)
			}
		}
		resp = f.sync(t, f.request(10, contract.Since{Collection: "k-playbook"}, contract.Since{Collection: "c"}))
		if !slices.Equal(resp.Allowed, []string{"a", "b", "k-playbook"}) ||
			!slices.Equal(resp.Public, []string{"b", "k-playbook"}) ||
			!slices.Equal(resp.Subscribed, []string{"k-playbook"}) {
			t.Errorf("sync: allowed %v, public %v, subscribed %v", resp.Allowed, resp.Public, resp.Subscribed)
		}
		if !resp.Collections[0].Allowed || resp.Collections[1].Allowed {
			t.Errorf("Status je Collection: %+v", resp.Collections)
		}
		who, err := f.api().Whoami(ctx, contract.WhoamiRequest{Version: contract.Version,
			Auth: contract.NodeAuth{Node: "laptop", Token: f.token}})
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(who.Allowed, resp.Allowed) || !slices.Equal(who.Public, resp.Public) ||
			!slices.Equal(who.Subscribed, resp.Subscribed) {
			t.Errorf("whoami: %+v", who)
		}
	})
}

// Schreiben in k-playbook ist für jeden gültigen Account forbidden, nicht
// not_readable; in einer Collection, die nur öffentlich ist, ohne Zeile
// ebenso.
func TestWriteInSourceCollection(t *testing.T) {
	forTransports(t, func(t *testing.T, newFixture func(*testing.T) *fixture) {
		f := newFixture(t)
		ctx := context.Background()
		acc := f.writers(t)
		if err := f.st.AddCollection(ctx, "k-playbook", "", false); err != nil {
			t.Fatal(err)
		}
		on := true
		if err := f.st.SetCollection(ctx, "b", store.CollectionChange{Public: &on}); err != nil {
			t.Fatal(err)
		}
		node := contract.NodeAuth{Node: "laptop", Token: f.token}
		for _, c := range []struct {
			coll string
			want error
		}{{"k-playbook", contract.ErrForbidden}, {"b", contract.ErrForbidden}, {"c", contract.ErrNotReadable}} {
			_, err := f.api().Create(ctx, contract.CreateRequest{Version: contract.Version, Auth: node,
				Account: acc["leser"], Collection: c.coll, Name: "x.md", Content: "x"})
			if !errors.Is(err, c.want) {
				t.Errorf("%s: %v, erwartet %v", c.coll, err, c.want)
			}
		}
	})
}
