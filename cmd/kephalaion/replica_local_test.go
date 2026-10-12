package main

// Abgleich gegen den echten Hub über local, als Funktionsaufruf im selben
// Prozess. Der Test steht hier, weil nur cmd/kephalaion Hub und Node
// zugleich kennen darf (internal/separation_test.go); die Verdrahtung für
// `node sync` folgt ihm.

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/kephalaion/kephalaion/internal/config"
	"github.com/kephalaion/kephalaion/internal/contract"
	"github.com/kephalaion/kephalaion/internal/hub/replication"
	hubstore "github.com/kephalaion/kephalaion/internal/hub/store"
	"github.com/kephalaion/kephalaion/internal/node/replica"
	nodestore "github.com/kephalaion/kephalaion/internal/node/store"
	"github.com/kephalaion/kephalaion/internal/sqlitedb"
)

func TestReplicaSyncWithLocalHub(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	hub, err := hubstore.Create(ctx, config.SQLiteDB(filepath.Join(dir, "hub.db")))
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()
	nodes, err := nodestore.Create(ctx, config.SQLiteDB(filepath.Join(dir, "node.db")))
	if err != nil {
		t.Fatal(err)
	}
	defer nodes.Close()

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []string{"wissen", "team"} {
		must(hub.AddCollection(ctx, c, "", false))
	}
	token, err := hub.AddNode(ctx, "laptop", "")
	must(err)
	must(hub.Grant(ctx, "laptop", "wissen"))
	docs := make([]hubstore.DocumentInput, 7)
	for i := range docs {
		docs[i] = hubstore.DocumentInput{Name: fmt.Sprintf("import/%02d.md", i), Content: "x"}
	}
	_, err = hub.ImportDocuments(ctx, "wissen", docs)
	must(err)
	for i := range 3 {
		_, err := hub.PutDocument(ctx, "wissen", fmt.Sprintf("einzeln/%d.md", i), "y")
		must(err)
	}

	must(nodes.AddHub(ctx, nodestore.Hub{Name: "eigen", NodeName: "laptop", Transport: nodestore.TransportLocal,
		Token: token}, true))
	must(nodes.AddCollection(ctx, "eigen", "wissen"))
	must(nodes.AddCollection(ctx, "eigen", "team"))

	// Seitengröße 2: Der Import (eine Revision mit 7 Zeilen) kommt ganz als
	// eine Seite, die drei Einzelnen in zwei weiteren.
	s := &replica.Syncer{Nodes: nodes, PageSize: 2}
	connect := func(nodestore.Hub) (contract.Hub, error) { return replication.New(hub), nil }
	sync := func() replica.HubResult {
		t.Helper()
		res, err := s.Sync(ctx, "", connect)
		must(err)
		if len(res) != 1 {
			t.Fatalf("%d Ergebnisse", len(res))
		}
		return res[0]
	}

	res := sync()
	if res.Err != nil {
		t.Fatal(res.Err)
	}
	if res.Pages != 3 {
		t.Errorf("Seiten = %d, erwartet 3", res.Pages)
	}
	info, err := hub.Info(ctx)
	must(err)
	if res.HubID != info.HubID {
		t.Errorf("hub_id = %q, Hub hat %q", res.HubID, info.HubID)
	}
	want := []replica.CollectionResult{
		{Collection: "team", Status: replica.NotAllowed},
		{Collection: "wissen", Status: replica.Synced, Rows: 10, Revision: info.Revision},
	}
	if !reflect.DeepEqual(res.Collections, want) {
		t.Errorf("Collections = %+v", res.Collections)
	}

	names := func() []string {
		t.Helper()
		r, err := replica.Open(ctx, nodes.ReplicaPath("eigen"))
		must(err)
		defer r.Close()
		docs, err := r.Documents(ctx, "wissen", "")
		must(err)
		out := []string{}
		for _, d := range docs {
			out = append(out, d.Name)
		}
		return out
	}
	if got := names(); len(got) != 10 {
		t.Errorf("Replica: %v", got)
	}

	// Löschen und Folgeabgleich: nur das Neue kommt.
	_, err = hub.DeleteDocument(ctx, "wissen", "einzeln/0.md")
	must(err)
	res = sync()
	if res.Err != nil || res.Collections[1].Rows != 1 {
		t.Errorf("Folgeabgleich = %+v", res)
	}
	if got := names(); len(got) != 9 || got[0] != "einzeln/1.md" {
		t.Errorf("Replica nach rm: %v", got)
	}

	// Gesperrt: Fehler dieses Eintrags, die Replica bleibt.
	must(hub.SetNodeLocked(ctx, "laptop", true))
	if res := sync(); !errors.Is(res.Err, contract.ErrUnauthenticated) {
		t.Errorf("gesperrt: %v", res.Err)
	}
	must(hub.SetNodeLocked(ctx, "laptop", false))

	// revoke: wissen verschwindet aus der Replica.
	must(hub.Revoke(ctx, "laptop", "wissen"))
	res = sync()
	if res.Err != nil || res.Collections[1].Status != replica.NotAllowed || res.Collections[1].Removed != 10 {
		t.Errorf("nach revoke = %+v", res)
	}

	// node hub rm nimmt die Replica mit.
	must(nodes.RemoveHub(ctx, "eigen"))
	if _, err := replica.Open(ctx, nodes.ReplicaPath("eigen")); !errors.Is(err, sqlitedb.ErrNotFound) {
		t.Errorf("Replica nach hub rm: %v", err)
	}
}
