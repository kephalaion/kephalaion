package mcpnode

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

// Wiederholung über den Node mit request_id (Task 032): Nach unklarem
// Ausgang schickt der Client denselben Aufruf mit demselben Schlüssel noch
// einmal und bekommt replayed — ohne Art, ohne Zeilen; die Replica bleibt
// unberührt, bis der angestoßene Abgleich nachholt.
func TestWriteReplayed(t *testing.T) {
	e := newDocEnv(t)
	f := e.hubs["keph"]
	in := CreateInput{Collection: "keph:wissen", Name: "neu.md", Content: "neu", RequestID: "k-1"}

	f.failWith(errLost, true)
	out := e.callWrite(t, e.anna(), "create", in)
	wantCode(t, "unklar", out, CodeOutcomeUnknown, "Mit request_id denselben Aufruf")
	if k := e.takeKicks(); !reflect.DeepEqual(k, []string{"keph"}) {
		t.Errorf("Anstoß nach unklarem Ausgang: %v", k)
	}
	f.failWith(nil, false)

	out = e.callWrite(t, e.anna(), "create", in)
	if out.Error != nil || !out.Replayed || out.Kind != "" || out.Address != "keph:wissen" || out.Name != "neu.md" ||
		len(out.IDs) != 1 || out.Revision == 0 || out.ID != "" || out.Size != nil || out.Updated != nil {
		t.Fatalf("Wiederholung: %+v", out)
	}
	if k := e.takeKicks(); !reflect.DeepEqual(k, []string{"keph"}) {
		t.Errorf("Anstoß nach der Wiederholung: %v", k)
	}
	if doc, _, _ := e.read(t, e.anna(), ReadInput{Collection: "keph:wissen", Name: "neu.md"}); doc.Kind != KindNone {
		t.Errorf("Replica vor dem Abgleich geschrieben: %+v", doc)
	}
	e.sync(t)
	doc, _, _ := e.read(t, e.anna(), ReadInput{Collection: "keph:wissen", Name: "neu.md"})
	if doc.Kind != KindDocument || doc.ID != out.IDs[0] || doc.Revision != out.Revision || contentOf(doc) != "neu" {
		t.Errorf("nach dem Abgleich: %+v", doc)
	}

	// Nach einem Erfolg geht rename weiter; seine Wiederholung schreibt
	// keine alte Zeile in die Replica zurück, auch wenn das Dokument danach
	// geändert wurde.
	ren := e.callWrite(t, e.anna(), "rename", RenameInput{Collection: "keph:wissen", Name: "neu.md", NewName: "alt.md",
		RequestID: "r-1"})
	if ren.Error != nil || ren.Replayed || ren.Kind != KindDocument || ren.ID != doc.ID {
		t.Fatalf("rename: %+v", ren)
	}
	w := e.callWrite(t, e.anna(), "write", WriteInput{Collection: "keph:wissen", Name: "alt.md", Content: "später"})
	if w.Error != nil {
		t.Fatal(w.Error)
	}
	again := e.callWrite(t, e.anna(), "rename", RenameInput{Collection: "keph:wissen", Name: "neu.md", NewName: "alt.md",
		RequestID: "r-1"})
	if again.Error != nil || !again.Replayed || again.Name != "alt.md" || again.Revision != ren.Revision ||
		!reflect.DeepEqual(again.IDs, []string{doc.ID}) {
		t.Fatalf("Wiederholung von rename: %+v", again)
	}
	if cur, _, _ := e.read(t, e.anna(), ReadInput{Collection: "keph:wissen", Name: "alt.md"}); cur.Revision != w.Revision ||
		contentOf(cur) != "später" {
		t.Errorf("Replica zurückgesetzt: %+v, %q", cur, contentOf(cur))
	}
	e.takeKicks()

	// Der Schlüssel geht an den Hub, je Aufruf; ohne Schlüssel leer.
	f.mu.Lock()
	ids := slices.Clone(f.requestIDs)
	f.mu.Unlock()
	if want := []string{"k-1", "k-1", "r-1", "", "r-1"}; !reflect.DeepEqual(ids, want) {
		t.Errorf("Schlüssel am Hub: %q, erwartet %q", ids, want)
	}

	// Eine ungültige Form lehnt schon der Node ab, ohne den Hub.
	calls := f.writeCalls()
	bad := e.callWrite(t, e.anna(), "delete", DeleteInput{Collection: "keph:wissen", Name: "alt.md", RequestID: "a b"})
	wantCode(t, "Form", bad, "invalid", "request_id")
	if f.writeCalls() != calls || !strings.Contains(bad.Error.Message, "A–Z") {
		t.Errorf("Hub gefragt oder Meldung ohne Form: %d → %d, %q", calls, f.writeCalls(), bad.Error.Message)
	}
}
