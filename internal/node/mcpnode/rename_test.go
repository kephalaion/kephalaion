package mcpnode

import (
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/kephalaion/kephalaion/internal/contract"
)

// Umbenennen über den Node: Die Replica ist sofort richtig — der alte Name
// ist weg, der neue da, die id bleibt —, ohne Abgleich. Ein Verzeichnis geht
// als Ganzes, die Antwort nennt es directory mit Zahl der Dokumente; ebenso
// delete mit recursive.
func TestRenameThenRead(t *testing.T) {
	e := newDocEnv(t)
	for _, n := range []string{"notiz.md", "ordner/a.md", "ordner/sub/b.md"} {
		if out := e.callWrite(t, e.anna(), "create", CreateInput{Collection: "keph:wissen", Name: n, Content: n}); out.Error != nil {
			t.Fatal(out.Error)
		}
	}
	e.takeKicks()
	now, _ := e.changes(t, e.anna(), ChangesInput{Collection: "keph:wissen"})
	doc, _, _ := e.read(t, e.anna(), ReadInput{Collection: "keph:wissen", Name: "notiz.md"})

	// Ein Dokument, mit der Revision aus read.
	out := e.callWrite(t, e.anna(), "rename", RenameInput{Collection: "keph:wissen", Name: "notiz.md",
		NewName: "archiv/notiz.md", BaseRevision: base(doc.Revision)})
	if out.Error != nil || out.Kind != KindDocument || out.Address != "keph:wissen" || out.Name != "archiv/notiz.md" ||
		out.ID != doc.ID || out.Revision <= doc.Revision || out.Size == nil || *out.Size != 8 || out.Count != 0 ||
		out.Updated == nil || out.Updated.By != "anna" || out.Deleted || out.Note != "" {
		t.Fatalf("rename: %+v", out)
	}
	if k := e.takeKicks(); !reflect.DeepEqual(k, []string{"keph"}) {
		t.Errorf("Anstoß nach rename: %v", k)
	}
	if old, _, _ := e.read(t, e.anna(), ReadInput{Collection: "keph:wissen", Name: "notiz.md"}); old.Kind != KindNone {
		t.Errorf("alter Name: %+v", old)
	}
	moved, res, _ := e.read(t, e.otto(), ReadInput{Collection: "keph:wissen", Name: "archiv/notiz.md"})
	if moved.Kind != KindDocument || moved.ID != doc.ID || moved.Revision != out.Revision || textOf(res) != "notiz.md" {
		t.Errorf("neuer Name: %+v", moved)
	}
	if byID, _, _ := e.read(t, e.anna(), ReadInput{Collection: "keph:", ID: doc.ID}); byID.Name != "archiv/notiz.md" {
		t.Errorf("per id: %+v", byID)
	}

	// Ein Verzeichnis: alle darunter, eine Revision.
	a, _, _ := e.read(t, e.anna(), ReadInput{Collection: "keph:wissen", Name: "ordner/sub/b.md"})
	dir := e.callWrite(t, e.anna(), "rename", RenameInput{Collection: "keph:wissen", Name: "ordner", NewName: "mappe"})
	if dir.Error != nil || dir.Kind != KindDirectory || dir.Name != "mappe" || dir.Count != 2 || dir.Revision <= out.Revision ||
		dir.ID != "" || dir.Size != nil || dir.Updated == nil || dir.Updated.By != "anna" || dir.Deleted {
		t.Fatalf("rename Verzeichnis: %+v", dir)
	}
	if old, _, _ := e.read(t, e.anna(), ReadInput{Collection: "keph:wissen", Name: "ordner"}); old.Kind != KindNone {
		t.Errorf("altes Verzeichnis: %+v", old)
	}
	if d, _, _ := e.read(t, e.anna(), ReadInput{Collection: "keph:wissen", Name: "mappe"}); d.Kind != KindDirectory {
		t.Errorf("neues Verzeichnis: %+v", d)
	}
	if b, _, _ := e.read(t, e.anna(), ReadInput{Collection: "keph:wissen", Name: "mappe/sub/b.md"}); b.ID != a.ID ||
		b.Revision != dir.Revision {
		t.Errorf("Dokument im neuen Verzeichnis: %+v", b)
	}
	list, _ := e.list(t, e.anna(), ListInput{Collection: "keph:wissen", Path: "mappe", Recursive: true})
	if got := names(list.Entries); !reflect.DeepEqual(got, []string{"mappe/a.md", "mappe/sub/b.md"}) {
		t.Errorf("list: %v", got)
	}
	// Nach dem Abgleich meldet changes jedes Dokument einmal, unter dem
	// neuen Namen.
	e.sync(t)
	got, _ := e.changes(t, e.anna(), ChangesInput{Collection: "keph:wissen", Cursor: now.Cursor})
	var changed []string
	for _, c := range got.Changes {
		changed = append(changed, c.Name)
	}
	if want := []string{"archiv/notiz.md", "mappe/a.md", "mappe/sub/b.md"}; !reflect.DeepEqual(changed, want) {
		t.Errorf("changes: %v, erwartet %v", changed, want)
	}

	// delete: ein Verzeichnis nur mit recursive.
	wantCode(t, "ohne recursive", e.callWrite(t, e.anna(), "delete", DeleteInput{Collection: "keph:wissen", Name: "mappe"}),
		"invalid", "mappe ist ein Verzeichnis")
	del := e.callWrite(t, e.anna(), "delete", DeleteInput{Collection: "keph:wissen", Name: "mappe", Recursive: true})
	if del.Error != nil || del.Kind != KindDirectory || del.Name != "mappe" || del.Count != 2 || !del.Deleted ||
		del.Revision <= dir.Revision {
		t.Fatalf("delete Verzeichnis: %+v", del)
	}
	if d, _, _ := e.read(t, e.anna(), ReadInput{Collection: "keph:wissen", Name: "mappe"}); d.Kind != KindNone {
		t.Errorf("gelöschtes Verzeichnis: %+v", d)
	}
	// recursive gilt für ein Dokument nicht.
	one := e.callWrite(t, e.anna(), "delete", DeleteInput{Collection: "keph:wissen", Name: "archiv/notiz.md", Recursive: true})
	if one.Error != nil || one.Kind != KindDocument || one.ID != doc.ID || !one.Deleted {
		t.Errorf("Dokument mit recursive: %+v", one)
	}
	if k := e.takeKicks(); len(k) != 3 {
		t.Errorf("Anstöße: %v", k)
	}
}

// Ablehnungen von rename und delete mit Verzeichnissen: Was die Pfadregeln
// schon ausschließen, lehnt der Node ab, ohne den Hub zu fragen; den Rest der
// Hub, mit seinem Code.
func TestRenameRejected(t *testing.T) {
	e := newDocEnv(t)
	e.fillWissen(t) // gehört kleist: dir/c.md, dir/sub/d.md, a.md, …
	own := e.callWrite(t, e.anna(), "create", CreateInput{Collection: "keph:wissen", Name: "eigen.md", Content: "x"})
	e.callWrite(t, e.anna(), "create", CreateInput{Collection: "keph:wissen", Name: "dir/eigen.md", Content: "x"})
	e.callWrite(t, e.anna(), "create", CreateInput{Collection: "keph:wissen", Name: "meins/x.md", Content: "x"})
	e.takeKicks()

	atNode := []struct {
		name string
		in   RenameInput
		want string
	}{
		{"gleicher Name", RenameInput{Collection: "keph:wissen", Name: "eigen.md", NewName: "eigen.md"}, "der neue Name ist der alte"},
		{"in sich selbst", RenameInput{Collection: "keph:wissen", Name: "meins", NewName: "meins/tiefer"}, "liegt darunter"},
		{"neuer Name ungültig", RenameInput{Collection: "keph:wissen", Name: "eigen.md", NewName: "../x.md"}, "new_name:"},
		{"neuer Name fehlt", RenameInput{Collection: "keph:wissen", Name: "eigen.md"}, "new_name:"},
		{"SYSTEM:", RenameInput{Collection: "keph:wissen", Name: "eigen.md", NewName: "SYSTEM:A:anna"}, "new_name:"},
		{"Name ungültig", RenameInput{Collection: "keph:wissen", Name: "meins/", NewName: "x"}, "name:"},
	}
	before := e.hubs["keph"].writeCalls()
	for _, c := range atNode {
		wantCode(t, c.name, e.callWrite(t, e.anna(), "rename", c.in), "invalid", c.want)
	}
	if n := e.hubs["keph"].writeCalls(); n != before {
		t.Errorf("%d Anfragen an den Hub", n-before)
	}

	atHub := []struct {
		name string
		tool string
		in   any
		code contract.Code
		want string
	}{
		{"Ziel belegt", "rename", RenameInput{Collection: "keph:wissen", Name: "eigen.md", NewName: "a.md"},
			contract.CodeNameTaken, "Dokument a.md gibt es schon"},
		{"Verzeichnis belegt", "rename", RenameInput{Collection: "keph:wissen", Name: "meins", NewName: "zdir"},
			contract.CodeNameTaken, "Verzeichnis zdir gibt es schon"},
		{"Dokument auf Verzeichnis", "rename", RenameInput{Collection: "keph:wissen", Name: "eigen.md", NewName: "zdir"},
			contract.CodePathConflict, ""},
		{"unter einer Datei", "rename", RenameInput{Collection: "keph:wissen", Name: "meins", NewName: "a.md/meins"},
			contract.CodePathConflict, ""},
		{"fremd mittendrin", "rename", RenameInput{Collection: "keph:wissen", Name: "dir", NewName: "neu"},
			contract.CodeForbidden, "gehört kleist, supersede fehlt"},
		{"fremd mittendrin löschen", "delete", DeleteInput{Collection: "keph:wissen", Name: "dir", Recursive: true},
			contract.CodeForbidden, "supersede fehlt"},
		{"weder noch", "rename", RenameInput{Collection: "keph:wissen", Name: "fehlt", NewName: "neu"},
			contract.CodeNotFound, "weder als Dokument noch als Verzeichnis"},
		{"veraltet", "rename", RenameInput{Collection: "keph:wissen", Name: "eigen.md", NewName: "neu.md",
			BaseRevision: base(own.Revision - 1)}, contract.CodeStaleRevision, fmt.Sprintf("hat Revision %d", own.Revision)},
		{"Verzeichnis mit Revision", "rename", RenameInput{Collection: "keph:wissen", Name: "meins", NewName: "neu",
			BaseRevision: base(own.Revision)}, contract.CodeInvalid, "base_revision gilt nur für Dokumente"},
		{"Verzeichnis ohne recursive", "delete", DeleteInput{Collection: "keph:wissen", Name: "meins"},
			contract.CodeInvalid, "meins ist ein Verzeichnis"},
		{"Verzeichnis löschen mit Revision", "delete", DeleteInput{Collection: "keph:wissen", Name: "meins", Recursive: true,
			BaseRevision: base(own.Revision)}, contract.CodeInvalid, "base_revision gilt nur für Dokumente"},
	}
	// Ein gescheitertes rename nennt den alten Namen, den es noch gibt.
	if out := e.callWrite(t, e.anna(), "rename", RenameInput{Collection: "keph:wissen", Name: "eigen.md",
		NewName: "a.md"}); out.Name != "eigen.md" || out.Address != "keph:wissen" || out.Kind != "" {
		t.Errorf("Name bei Fehler: %+v", out)
	}
	for _, c := range atHub {
		before := e.hubs["keph"].writeCalls()
		out := e.callWrite(t, e.anna(), c.tool, c.in)
		wantCode(t, c.name, out, string(c.code), c.want)
		if e.hubs["keph"].writeCalls() != before+1 {
			t.Errorf("%s: nicht beim Hub", c.name)
		}
	}
	if k := e.takeKicks(); len(k) != 0 {
		t.Errorf("Anstoß nach Ablehnung: %v", k)
	}
	// Nichts hat sich bewegt.
	for _, n := range []string{"eigen.md", "dir/eigen.md", "dir/c.md", "meins/x.md"} {
		if d, _, _ := e.read(t, e.anna(), ReadInput{Collection: "keph:wissen", Name: n}); d.Kind != KindDocument {
			t.Errorf("%s: %+v", n, d)
		}
	}
	// otto liest nur: forbidden vom Hub.
	wantCode(t, "otto", e.callWrite(t, e.otto(), "rename", RenameInput{Collection: "keph:wissen", Name: "eigen.md",
		NewName: "otto.md"}), string(contract.CodeForbidden), "")
	// Nicht lesbar: der Node fragt den Hub nicht.
	wantCode(t, "nicht lesbar", e.callWrite(t, http.Header{}, "rename", RenameInput{Collection: "keph:wissen",
		Name: "eigen.md", NewName: "x.md"}), string(contract.CodeNotReadable), "nicht lesbar")
}

// answerShape nimmt genau ein Dokument unter dem Namen oder, wo der Vorgang
// ein Verzeichnis nehmen kann, Zeilen nur darunter.
func TestAnswerShape(t *testing.T) {
	row := func(coll, name string) contract.Row { return contract.Row{ID: name, Collection: coll, Name: name} }
	cases := []struct {
		what  string
		dirOK bool
		rows  []contract.Row
		doc   string
		dir   bool
		err   string
	}{
		{"Dokument", false, []contract.Row{row("w", "a")}, "a", false, ""},
		{"Dokument, dir erlaubt", true, []contract.Row{row("w", "a")}, "a", false, ""},
		{"Verzeichnis", true, []contract.Row{row("w", "a/x"), row("w", "a/y/z")}, "", true, ""},
		{"Verzeichnis nicht erlaubt", false, []contract.Row{row("w", "a/x")}, "", false, "erwartet a"},
		{"leer", true, nil, "", false, "0 Zeilen"},
		{"zweimal", false, []contract.Row{row("w", "a"), row("w", "a")}, "", false, "2 Zeilen"},
		{"Dokument und darunter", true, []contract.Row{row("w", "a"), row("w", "a/x")}, "a", false, "1 darunter"},
		{"fremder Name", true, []contract.Row{row("w", "a/x"), row("w", "ab")}, "", false, "erwartet a"},
		{"fremde Collection", true, []contract.Row{row("v", "a")}, "", false, "Collection"},
	}
	for _, c := range cases {
		doc, dir, err := answerShape("w", "a", c.dirOK, c.rows)
		gotDoc := ""
		if doc != nil {
			gotDoc = doc.Name
		}
		if gotDoc != c.doc || dir != c.dir || (c.err == "") != (err == nil) ||
			(err != nil && !strings.Contains(err.Error(), c.err)) {
			t.Errorf("%s: %q, %v, %v", c.what, gotDoc, dir, err)
		}
	}
}
