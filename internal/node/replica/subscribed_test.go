package replica

import (
	"context"
	"testing"
)

// Task 031: Eine fest abonnierte Collection gleicht der Node ab, ohne sie zu
// wollen; die Merkmale stehen in sync_state. Fällt sie aus subscribed und
// will er sie nicht selbst, entfernt er sie.
func TestSubscribedCollection(t *testing.T) {
	e := newEnv(t, "a")
	e.hub.allowed["k-playbook"] = true
	e.hub.public["k-playbook"] = true
	e.hub.subscribed["k-playbook"] = true
	e.hub.put("a", "x.md", "a")
	e.hub.put("k-playbook", "defaults/README.md", "k")

	res := e.ok()
	if got := e.states(); len(got) != 2 || got["k-playbook"] != e.hub.rev || got["a"] != e.hub.rev {
		t.Fatalf("Stände: %v", got)
	}
	if r := result(res, "k-playbook"); r.Status != Synced || r.Rows != 1 {
		t.Errorf("Ergebnis k-playbook: %+v", r)
	}
	e.checkMirror("a", "k-playbook")
	sts, err := e.replica().States(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range sts {
		want := s.Collection == "k-playbook"
		if s.Public != want || s.Subscribed != want {
			t.Errorf("Merkmale %+v", s)
		}
	}
	if pub, err := e.replica().PublicCollections(context.Background()); err != nil || len(pub) != 1 || pub[0] != "k-playbook" {
		t.Errorf("PublicCollections: %v %v", pub, err)
	}

	// Der nächste Abgleich fragt k-playbook gleich mit, ab ihrem Stand.
	e.hub.put("k-playbook", "defaults/neu.md", "n")
	before := len(e.hub.requests)
	e.ok()
	req := e.hub.requests[before]
	if len(req.Collections) != 2 || req.Collections[1].Collection != "k-playbook" || req.Collections[1].Since == 0 {
		t.Errorf("Anfrage: %+v", req.Collections)
	}
	e.checkMirror("a", "k-playbook")

	// Nicht mehr fest abonniert und nicht gewünscht: entfernt.
	delete(e.hub.subscribed, "k-playbook")
	res = e.ok()
	if r := result(res, "k-playbook"); r.Status != NotWanted || r.Removed != 2 {
		t.Errorf("nach Abbestellen: %+v", r)
	}
	if got := e.states(); len(got) != 1 {
		t.Errorf("Stände nach Abbestellen: %v", got)
	}

	// Gewünscht und fest abonniert: bleibt, auch wenn subscribed wegfällt.
	e.hub.subscribed["k-playbook"] = true
	e.want("k-playbook")
	e.ok()
	delete(e.hub.subscribed, "k-playbook")
	e.ok()
	e.checkMirror("a", "k-playbook")
}

// Nennt der Hub eine fest abonnierte Collection, erlaubt sie aber nicht,
// fragt der Node sie höchstens einmal je Abgleich und endet.
func TestSubscribedNotAllowed(t *testing.T) {
	e := newEnv(t, "a")
	e.hub.subscribed["x"] = true
	e.hub.allowed["x"] = true
	e.ok()
	// Die Attrappe nennt x weiter in allowed und subscribed, sagt aber je
	// Collection „nicht erlaubt“ — widersprüchlich. Der Node entfernt x und
	// fragt nicht noch einmal.
	e.hub.allowed["x"] = false
	before := len(e.hub.requests)
	res := e.ok()
	if n := len(e.hub.requests) - before; n != 1 {
		t.Errorf("%d Anfragen, erwartet 1", n)
	}
	if r := result(res, "x"); r.Status != NotAllowed && r.Status != NotWanted {
		t.Errorf("x: %+v", r)
	}
	if got := e.states(); len(got) != 1 {
		t.Errorf("Stände: %v", got)
	}
}
