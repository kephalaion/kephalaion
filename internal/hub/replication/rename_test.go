package replication

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/kephalaion/kephalaion/internal/contract"
)

func (f *fixture) renameReq(acc contract.AccountAuth, collection, name, newName string, base *int64) contract.RenameRequest {
	return contract.RenameRequest{Version: contract.Version, Auth: f.node(), Account: acc, Collection: collection,
		Name: name, NewName: newName, BaseRevision: base}
}

func (f *fixture) deleteDirReq(acc contract.AccountAuth, collection, name string) contract.DeleteRequest {
	r := f.deleteReq(acc, collection, name, nil)
	r.Recursive = true
	return r
}

// checkWrittenAll prüft eine Antwort mit mehreren Zeilen: Kopf, eine
// Revision für alle, die Namen in dieser Reihenfolge, jede Zeile wie im
// Abgleich.
func (f *fixture) checkWrittenAll(t *testing.T, what string, resp contract.WriteResponse, err error, names ...string) []contract.Row {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
	info, _ := f.st.Info(context.Background())
	if resp.HubID != info.HubID || resp.Version != contract.Version || resp.Revision != info.Revision ||
		len(resp.Rows) != len(names) {
		t.Fatalf("%s: Antwort %+v", what, resp)
	}
	for i, r := range resp.Rows {
		if r.Name != names[i] || r.Revision != resp.Revision {
			t.Errorf("%s: Zeile %d %s, Revision %d, erwartet %s, %d", what, i, r.Name, r.Revision, names[i], resp.Revision)
		}
		if got := f.syncedRow(t, r.Collection, r.ID); !reflect.DeepEqual(got, r) {
			t.Errorf("%s: Zeile der Antwort %+v, im Abgleich %+v", what, r, got)
		}
	}
	return resp.Rows
}

// rename und delete mit recursive über local und HTTP: Die id bleibt, ein
// Verzeichnis geht als Ganzes unter einer Revision, die Antwort trägt alle
// Zeilen, nach Name.
func TestRenameAndDirectories(t *testing.T) {
	forTransports(t, func(t *testing.T, newFixture func(*testing.T) *fixture) {
		f := newFixture(t)
		ctx := context.Background()
		acc := f.writers(t)
		bob := acc["bob"]
		created, err := f.api().Create(ctx, f.createReq(bob, "a", "notiz.md", "eins"))
		if err != nil {
			t.Fatal(err)
		}

		// Ein Dokument: id bleibt, Urheber bleibt, actions nennt rename.
		resp, err := f.api().Rename(ctx, f.renameReq(acc["bob2"], "a", "notiz.md", "archiv/notiz.md", rev(created.Revision)))
		row := f.checkWritten(t, "rename", resp, err)
		if row.ID != created.Rows[0].ID || row.Name != "archiv/notiz.md" || *row.Content != "eins" ||
			row.CreatedBy != "kleist" || row.UpdatedBy != "kleist" || resp.Revision != created.Revision+1 {
			t.Errorf("rename: %+v", row)
		}
		var account, carrier string
		if err := f.raw(t).QueryRow(`SELECT account, carrier FROM actions WHERE document_id = ? AND action = 'rename'`,
			row.ID).Scan(&account, &carrier); err != nil || account != "bob2" || carrier != "laptop" {
			t.Errorf("actions: %s %s, %v", account, carrier, err)
		}

		// Ein Verzeichnis umbenennen: alle darunter, eine Revision.
		for _, n := range []string{"dir/a.md", "dir/sub/b.md", "dirx.md"} {
			if _, err := f.api().Create(ctx, f.createReq(bob, "a", n, n)); err != nil {
				t.Fatal(err)
			}
		}
		resp, err = f.api().Rename(ctx, f.renameReq(bob, "a", "dir", "ordner/neu", nil))
		rows := f.checkWrittenAll(t, "rename Verzeichnis", resp, err, "ordner/neu/a.md", "ordner/neu/sub/b.md")
		if *rows[1].Content != "dir/sub/b.md" {
			t.Errorf("Inhalt: %+v", rows[1])
		}
		if _, err := f.st.Document(ctx, "a", "dir/a.md"); err == nil {
			t.Error("alter Name lebt noch")
		}

		// Ein Verzeichnis löschen: nur mit recursive, alle Löschmarken.
		resp, err = f.api().Delete(ctx, f.deleteDirReq(bob, "a", "ordner"))
		for _, r := range f.checkWrittenAll(t, "delete Verzeichnis", resp, err, "ordner/neu/a.md", "ordner/neu/sub/b.md") {
			if !r.Deleted || r.Content != nil {
				t.Errorf("Löschmarke: %+v", r)
			}
		}
		// recursive gilt für ein Dokument nicht.
		resp, err = f.api().Delete(ctx, f.deleteDirReq(bob, "a", "dirx.md"))
		if r := f.checkWritten(t, "delete Dokument mit recursive", resp, err); !r.Deleted || r.Name != "dirx.md" {
			t.Errorf("Dokument mit recursive: %+v", r)
		}
	})
}

// Jede Ablehnung von rename und delete mit Verzeichnissen, ohne dass sich
// am Hub etwas ändert — auch nicht, wenn nur ein Dokument mittendrin fremd
// ist.
func TestRenameAndDirectoryCodes(t *testing.T) {
	forTransports(t, func(t *testing.T, newFixture func(*testing.T) *fixture) {
		f := newFixture(t)
		ctx := context.Background()
		acc := f.writers(t)
		bob := acc["bob"]
		for _, n := range []string{"eigen.md", "dir/a.md", "dir/c.md", "ziel/x.md"} {
			if _, err := f.api().Create(ctx, f.createReq(bob, "a", n, "x")); err != nil {
				t.Fatal(err)
			}
		}
		f.put(t, "a", "dir/b.md") // gehört admin, mittendrin
		cur := f.revision(t)
		rename := func(r contract.RenameRequest) func(context.Context, contract.Hub) (contract.WriteResponse, error) {
			return func(ctx context.Context, h contract.Hub) (contract.WriteResponse, error) { return h.Rename(ctx, r) }
		}
		del := func(r contract.DeleteRequest) func(context.Context, contract.Hub) (contract.WriteResponse, error) {
			return func(ctx context.Context, h contract.Hub) (contract.WriteResponse, error) { return h.Delete(ctx, r) }
		}
		dirBase := f.deleteDirReq(bob, "a", "dir")
		dirBase.BaseRevision = rev(cur)
		badNode := f.renameReq(bob, "a", "eigen.md", "eigen.md", nil)
		badNode.Auth.Token = bob.Token

		failures := []writeFailure{
			{"name_taken", rename(f.renameReq(bob, "a", "eigen.md", "dir/a.md", nil)), contract.ErrNameTaken, "dir/a.md"},
			// Recht vor Ziel: eve darf mit supersede alles in dir.
			{"name_taken Verzeichnis", rename(f.renameReq(acc["eve"], "a", "dir", "ziel", nil)), contract.ErrNameTaken,
				"umbenennen legt nicht zusammen"},
			{"path_conflict Dokument auf Verzeichnis", rename(f.renameReq(bob, "a", "eigen.md", "ziel", nil)),
				contract.ErrPathConflict, ""},
			{"path_conflict unter Datei", rename(f.renameReq(acc["eve"], "a", "dir", "eigen.md/dir", nil)), contract.ErrPathConflict, ""},
			{"Recht vor Ziel", rename(f.renameReq(bob, "a", "dir", "ziel", nil)), contract.ErrForbidden, ""},
			{"invalid x nach x/y", rename(f.renameReq(bob, "a", "dir", "dir/y", nil)), contract.ErrInvalid, "liegt darunter"},
			{"invalid gleicher Name", rename(f.renameReq(bob, "a", "eigen.md", "eigen.md", nil)), contract.ErrInvalid, "der alte"},
			{"invalid new_name", rename(f.renameReq(bob, "a", "eigen.md", "SYSTEM:A:bob", nil)), contract.ErrInvalid, "neuer Name"},
			{"invalid new_name kein UTF-8", rename(f.renameReq(bob, "a", "eigen.md", "neu\xff.md", nil)), contract.ErrInvalid, "UTF-8"},
			{"invalid base Verzeichnis", rename(f.renameReq(bob, "a", "dir", "neu", rev(cur))), contract.ErrInvalid,
				"base_revision gilt nur für Dokumente"},
			{"invalid base 0", rename(f.renameReq(bob, "a", "eigen.md", "neu.md", rev(0))), contract.ErrInvalid, "base_revision"},
			{"not_found", rename(f.renameReq(bob, "a", "fehlt", "neu", nil)), contract.ErrNotFound, "fehlt"},
			{"stale_revision", rename(f.renameReq(bob, "a", "eigen.md", "neu.md", rev(cur+1))), contract.ErrStaleRevision, ""},
			{"forbidden fremd mittendrin", rename(f.renameReq(bob, "a", "dir", "neu", nil)), contract.ErrForbidden,
				"dir/b.md in a umbenennen: gehört admin"},
			{"forbidden ohne write", rename(f.renameReq(acc["leser"], "a", "eigen.md", "neu.md", nil)), contract.ErrForbidden, ""},
			{"not_readable", rename(f.renameReq(bob, "b", "eigen.md", "neu.md", nil)), contract.ErrNotReadable, ""},
			{"account", rename(f.renameReq(acc["gesperrt"], "a", "eigen.md", "neu.md", nil)), contract.ErrAccountUnauthenticated, ""},
			{"Form vor Node", rename(badNode), contract.ErrInvalid, ""},
			{"delete Verzeichnis ohne recursive", del(f.deleteReq(bob, "a", "dir", nil)), contract.ErrInvalid,
				"dir ist in a ein Verzeichnis mit 3 Dokumenten; löschen nur mit recursive"},
			{"delete Verzeichnis mit base", del(dirBase), contract.ErrInvalid, "base_revision gilt nur für Dokumente"},
			{"delete fremd mittendrin", del(f.deleteDirReq(bob, "a", "dir")), contract.ErrForbidden, "dir/b.md in a löschen"},
			{"delete weder noch", del(f.deleteDirReq(bob, "a", "fehlt")), contract.ErrNotFound, ""},
			{"delete Wurzel", del(f.deleteDirReq(bob, "a", "")), contract.ErrInvalid, ""},
		}
		for _, c := range failures {
			resp, err := c.call(ctx, f.api())
			var ce *contract.Error
			if !errors.Is(err, c.want) || !errors.As(err, &ce) || !strings.Contains(err.Error(), c.msg) {
				t.Errorf("%s: %v, erwartet %v mit %q", c.name, err, c.want, c.msg)
				continue
			}
			if resp.HubID != "" || resp.Rows != nil {
				t.Errorf("%s: Antwort trotz Fehler: %+v", c.name, resp)
			}
		}
		if after := f.revision(t); after != cur {
			t.Errorf("Fehlversuche haben geschrieben: Revision %d → %d", cur, after)
		}
		// eve hat supersede: das ganze Verzeichnis, auch Eigenes von bob.
		resp, err := f.api().Rename(ctx, f.renameReq(acc["eve"], "a", "dir", "neu", nil))
		f.checkWrittenAll(t, "eve", resp, err, "neu/a.md", "neu/b.md", "neu/c.md")
	})
}
