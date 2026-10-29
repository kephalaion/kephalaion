package replication

import (
	"context"
	"errors"
	"testing"

	"github.com/kephalaion/kephalaion/internal/contract"
	"github.com/kephalaion/kephalaion/internal/ident"
)

// node_rotate über local und HTTP: gelingt einmal, danach gilt nur das neue
// Token; mit dem alten, gesperrt und mit falscher Form abgelehnt.
func TestRotateNode(t *testing.T) {
	forTransports(t, func(t *testing.T, newFixture func(*testing.T) *fixture) {
		ctx := context.Background()
		f := newFixture(t)
		next := "keph_neu-" + t.Name()
		req := func(token, newHash string) contract.NodeRotateRequest {
			return contract.NodeRotateRequest{Version: contract.Version,
				Auth: contract.NodeAuth{Node: "laptop", Token: token}, NewHash: newHash}
		}
		// Form und Fassung vor der Anmeldung.
		if _, err := f.api().RotateNode(ctx, req(f.token, "kein-hash")); !errors.Is(err, contract.ErrInvalid) {
			t.Errorf("ungültiger Hash: %v", err)
		}
		bad := req(f.token, ident.HashToken(next))
		bad.Version = 2
		if _, err := f.api().RotateNode(ctx, bad); !errors.Is(err, contract.ErrUnsupportedVersion) {
			t.Errorf("Fassung 2: %v", err)
		}
		if _, err := f.api().RotateNode(ctx, req(f.token, ident.HashToken(f.token))); !errors.Is(err, contract.ErrInvalid) {
			t.Errorf("neuer Hash gleich dem alten: %v", err)
		}
		if _, err := f.api().RotateNode(ctx, req("keph_falsch", ident.HashToken(next))); !errors.Is(err, contract.ErrUnauthenticated) {
			t.Errorf("falsches Token: %v", err)
		}

		resp, err := f.api().RotateNode(ctx, req(f.token, ident.HashToken(next)))
		if err != nil {
			t.Fatal(err)
		}
		info, _ := f.st.Info(ctx)
		if resp.HubID != info.HubID || resp.Node != "laptop" || resp.Version != contract.Version {
			t.Errorf("Antwort: %+v", resp)
		}
		// Das alte Token gilt nicht mehr, das neue schon.
		if _, err := f.api().Whoami(ctx, contract.WhoamiRequest{Version: contract.Version,
			Auth: contract.NodeAuth{Node: "laptop", Token: f.token}}); !errors.Is(err, contract.ErrUnauthenticated) {
			t.Errorf("whoami mit altem Token: %v", err)
		}
		if _, err := f.api().Whoami(ctx, contract.WhoamiRequest{Version: contract.Version,
			Auth: contract.NodeAuth{Node: "laptop", Token: next}}); err != nil {
			t.Errorf("whoami mit neuem Token: %v", err)
		}
		// Ein zweites Mal mit dem alten: abgelehnt.
		if _, err := f.api().RotateNode(ctx, req(f.token, ident.HashToken("keph_dritt"))); !errors.Is(err, contract.ErrUnauthenticated) {
			t.Errorf("zweites Mal mit altem Token: %v", err)
		}
		// Gesperrt: abgelehnt.
		if err := f.st.SetNodeLocked(ctx, "laptop", true); err != nil {
			t.Fatal(err)
		}
		if _, err := f.api().RotateNode(ctx, req(next, ident.HashToken("keph_dritt"))); !errors.Is(err, contract.ErrUnauthenticated) {
			t.Errorf("gesperrt: %v", err)
		}
	})
}
