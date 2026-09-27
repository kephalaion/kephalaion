package replica

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"

	"github.com/oklog/ulid/v2"

	"github.com/kephalaion/kephalaion/internal/contract"
)

// fakeHub ist eine Attrappe von contract.Hub: Dokumente im Speicher, eine
// Revision je Schreibvorgang, Seiten an Revisionsgrenzen wie in
// docs/vertrag.md. Anders als der echte Hub kann sie umbenennen, die
// hub_id wechseln und auf eine frühere Revision zurückfallen.
type fakeHub struct {
	id      string
	rev     int64
	docs    map[string]contract.Row
	node    string
	token   string
	allowed map[string]bool
	// failAt lässt den Aufruf mit dieser Nummer (ab 1) scheitern; 0 nie.
	failAt   int
	calls    int
	requests []contract.SyncRequest
}

var errTransport = errors.New("Verbindung abgebrochen")

func newFakeHub(token string, allowed ...string) *fakeHub {
	f := &fakeHub{id: ulid.Make().String(), docs: map[string]contract.Row{}, node: "laptop", token: token,
		allowed: map[string]bool{}}
	for _, c := range allowed {
		f.allowed[c] = true
	}
	return f
}

// write ist ein Schreibvorgang: eine Revision für alles, was fn tut.
func (f *fakeHub) write(fn func(rev int64)) int64 {
	f.rev++
	fn(f.rev)
	return f.rev
}

func str(s string) *string { return &s }

// newDoc legt in einem laufenden Schreibvorgang ein Dokument an und liefert
// seine id.
func (f *fakeHub) newDoc(rev int64, collection, name, content string) string {
	id := ulid.Make().String()
	f.docs[id] = contract.Row{ID: id, Collection: collection, Name: name, Content: str(content),
		Revision: rev, CreatedAt: rev, CreatedBy: "admin", UpdatedAt: rev, UpdatedBy: "admin"}
	return id
}

// put legt ein Dokument in einem eigenen Schreibvorgang an.
func (f *fakeHub) put(collection, name, content string) string {
	var id string
	f.write(func(rev int64) { id = f.newDoc(rev, collection, name, content) })
	return id
}

// change ändert ein Dokument in einem laufenden Schreibvorgang.
func (f *fakeHub) change(rev int64, id string, fn func(r *contract.Row)) {
	r := f.docs[id]
	fn(&r)
	r.Revision, r.UpdatedAt = rev, rev
	f.docs[id] = r
}

// restore spielt den Hub auf eine frühere Revision zurück, mit gleicher
// hub_id — wie eine Sicherung: Was danach geschrieben wurde, fehlt.
func (f *fakeHub) restore(rev int64) {
	for id, r := range f.docs {
		if r.Revision > rev {
			delete(f.docs, id)
		}
	}
	f.rev = rev
}

func (f *fakeHub) Sync(_ context.Context, req contract.SyncRequest) (contract.SyncResponse, error) {
	f.calls++
	f.requests = append(f.requests, req)
	if f.calls == f.failAt {
		return contract.SyncResponse{}, errTransport
	}
	if req.Version != contract.Version {
		return contract.SyncResponse{}, contract.ErrUnsupportedVersion
	}
	if req.Auth.Node != f.node || req.Auth.Token != f.token {
		return contract.SyncResponse{}, contract.ErrUnauthenticated
	}
	if req.PageSize <= 0 {
		return contract.SyncResponse{}, contract.Invalid(fmt.Sprintf("Seitengröße %d", req.PageSize))
	}
	resp := contract.SyncResponse{HubID: f.id, Version: contract.Version, HubRevision: f.rev,
		Collections: []contract.CollectionStatus{}, Allowed: []string{}, Rows: []contract.Row{}}
	for c := range f.allowed {
		resp.Allowed = append(resp.Allowed, c)
	}
	sort.Strings(resp.Allowed)
	since := map[string]int64{}
	for _, s := range req.Collections {
		ok := f.allowed[s.Collection]
		resp.Collections = append(resp.Collections, contract.CollectionStatus{Collection: s.Collection, Allowed: ok})
		if ok {
			since[s.Collection] = s.Since
		}
	}
	var rows []contract.Row
	for _, r := range f.docs {
		if s, ok := since[r.Collection]; ok && r.Revision > s && r.Revision <= f.rev {
			rows = append(rows, r)
		}
	}
	slices.SortFunc(rows, func(a, b contract.Row) int {
		if a.Revision != b.Revision {
			return int(a.Revision - b.Revision)
		}
		if a.ID < b.ID {
			return -1
		}
		return 1
	})
	// Ganze Revisionen, bis die Seitengröße erreicht ist; eine einzelne
	// größere Revision kommt ganz.
	for i := 0; i < len(rows); {
		j := i
		for j < len(rows) && rows[j].Revision == rows[i].Revision {
			j++
		}
		if len(resp.Rows) > 0 && len(resp.Rows)+(j-i) > req.PageSize {
			resp.More = true
			break
		}
		resp.Rows = append(resp.Rows, rows[i:j]...)
		i = j
	}
	resp.Until = f.rev
	if resp.More {
		resp.Until = resp.Rows[len(resp.Rows)-1].Revision
	}
	return resp, nil
}

// Whoami und Rotate braucht der Abgleich nicht; die Attrappe bestätigt nur
// den Node.
func (f *fakeHub) Whoami(_ context.Context, req contract.WhoamiRequest) (contract.WhoamiResponse, error) {
	if req.Auth.Node != f.node || req.Auth.Token != f.token {
		return contract.WhoamiResponse{}, contract.ErrUnauthenticated
	}
	return contract.WhoamiResponse{HubID: f.id, Version: contract.Version, Node: f.node, Allowed: []string{}}, nil
}

func (f *fakeHub) Rotate(context.Context, contract.RotateRequest) (contract.RotateResponse, error) {
	return contract.RotateResponse{}, errors.New("rotate: in der Attrappe nicht umgesetzt")
}

func (f *fakeHub) Create(context.Context, contract.CreateRequest) (contract.WriteResponse, error) {
	return contract.WriteResponse{}, errors.New("create: in der Attrappe nicht umgesetzt")
}

func (f *fakeHub) Write(context.Context, contract.WriteRequest) (contract.WriteResponse, error) {
	return contract.WriteResponse{}, errors.New("write: in der Attrappe nicht umgesetzt")
}

func (f *fakeHub) Delete(context.Context, contract.DeleteRequest) (contract.WriteResponse, error) {
	return contract.WriteResponse{}, errors.New("delete: in der Attrappe nicht umgesetzt")
}
