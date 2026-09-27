package mcpnode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/oklog/ulid/v2"

	"github.com/kephalaion/kephalaion/internal/contract"
	"github.com/kephalaion/kephalaion/internal/node/store"
)

// callWrite ruft ein Werkzeug, das schreibt, und liest die Struktur — auch
// bei einem Fehler. Es prüft die Form: isError genau mit error, der Text ist
// dann die Meldung; kein Token in der Antwort.
func (e *docEnv) callWrite(t *testing.T, h http.Header, tool string, in any) WriteOutput {
	t.Helper()
	res, _ := e.call(t, h, tool, in, nil)
	var out WriteOutput
	b, _ := json.Marshal(res.StructuredContent)
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("%s: Struktur %s: %v", tool, b, err)
	}
	if res.IsError != (out.Error != nil) {
		t.Errorf("%s: isError %v, error %+v", tool, res.IsError, out.Error)
	}
	if out.Error != nil && (textOf(res) != out.Error.Message || out.Error.Code == "") {
		t.Errorf("%s: Text %q, error %+v", tool, textOf(res), out.Error)
	}
	raw, _ := json.Marshal(res)
	for _, tok := range e.tokens {
		if strings.Contains(string(raw), tok) {
			t.Errorf("%s: Token in der Antwort: %s", tool, raw)
		}
	}
	return out
}

// wantCode verlangt einen Fehler mit diesem Code; die Meldung enthält want.
func wantCode(t *testing.T, what string, out WriteOutput, code, want string) {
	t.Helper()
	if out.Error == nil || out.Error.Code != code || !strings.Contains(out.Error.Message, want) {
		t.Errorf("%s: %+v, erwartet %s mit %q", what, out.Error, code, want)
	}
}

func base(rev int64) *int64 { return &rev }

// Schreiben über den Node: Die eigene Änderung steht sofort in der Replica —
// read liefert die neue Revision ohne Abgleich, zweimal hintereinander
// speichern geht mit der Revision aus read. changes meldet sie erst nach dem
// Abgleich. Jeder Erfolg stößt den Abgleich des Hubs an.
func TestWriteThenRead(t *testing.T) {
	e := newDocEnv(t)
	now, _ := e.changes(t, e.anna(), ChangesInput{Collection: "keph:wissen"})

	out := e.callWrite(t, e.anna(), "create", CreateInput{Collection: "keph:wissen", Name: "notiz.md", Content: "eins"})
	if out.Error != nil || out.Kind != KindDocument || out.Address != "keph:wissen" || out.Name != "notiz.md" ||
		out.ID == "" || out.Revision == 0 || out.Size == nil || *out.Size != 4 || out.Updated == nil ||
		out.Updated.By != "anna" || out.Created == nil || out.Deleted || out.Note != "" {
		t.Fatalf("create: %+v", out)
	}
	if k := e.takeKicks(); !reflect.DeepEqual(k, []string{"keph"}) {
		t.Errorf("Anstoß nach create: %v", k)
	}
	doc, res, _ := e.read(t, e.anna(), ReadInput{Collection: "keph:wissen", Name: "notiz.md"})
	if doc.Kind != KindDocument || doc.ID != out.ID || doc.Revision != out.Revision || textOf(res) != "eins" {
		t.Fatalf("read nach create: %+v, %q", doc, textOf(res))
	}
	// Zweimal hintereinander, je mit der Revision aus read.
	for _, content := range []string{"zwei", "drei"} {
		w := e.callWrite(t, e.anna(), "write", WriteInput{Collection: "keph:wissen", Name: "notiz.md", Content: content,
			BaseRevision: base(doc.Revision)})
		if w.Error != nil || w.ID != out.ID || w.Revision <= doc.Revision || *w.Size != int64(len(content)) {
			t.Fatalf("write %s: %+v", content, w)
		}
		doc, res, _ = e.read(t, e.otto(), ReadInput{Collection: "wissen", Name: "notiz.md"})
		if doc.Revision != w.Revision || textOf(res) != content {
			t.Fatalf("read nach write %s: %+v, %q", content, doc, textOf(res))
		}
	}
	// Unverändert: keine neue Revision, die bestehende in der Antwort.
	same := e.callWrite(t, e.anna(), "write", WriteInput{Collection: "keph:wissen", Name: "notiz.md", Content: "drei",
		BaseRevision: base(doc.Revision)})
	if same.Error != nil || same.Revision != doc.Revision {
		t.Errorf("unverändert: %+v, erwartet Revision %d", same, doc.Revision)
	}
	if k := e.takeKicks(); len(k) != 3 {
		t.Errorf("Anstöße nach drei write: %v", k)
	}

	// changes liest bis sync_state: die eigene Änderung erst nach dem
	// Abgleich.
	if got, _ := e.changes(t, e.anna(), ChangesInput{Collection: "keph:wissen", Cursor: now.Cursor}); len(got.Changes) != 0 {
		t.Errorf("changes vor dem Abgleich: %+v", got.Changes)
	}
	e.sync(t)
	got, _ := e.changes(t, e.anna(), ChangesInput{Collection: "keph:wissen", Cursor: now.Cursor})
	if len(got.Changes) != 1 || got.Changes[0].ID != out.ID || got.Changes[0].Revision != doc.Revision {
		t.Fatalf("changes nach dem Abgleich: %+v", got.Changes)
	}

	// delete mit der Revision aus read: Löschmarke, read sagt none.
	d := e.callWrite(t, e.anna(), "delete", DeleteInput{Collection: "keph:wissen", Name: "notiz.md",
		BaseRevision: base(doc.Revision)})
	if d.Error != nil || !d.Deleted || d.ID != out.ID || d.Revision <= doc.Revision || d.Size != nil {
		t.Fatalf("delete: %+v", d)
	}
	if doc, _, _ := e.read(t, e.anna(), ReadInput{Collection: "keph:wissen", Name: "notiz.md"}); doc.Kind != KindNone {
		t.Errorf("read nach delete: %+v", doc)
	}
	if k := e.takeKicks(); !reflect.DeepEqual(k, []string{"keph"}) {
		t.Errorf("Anstoß nach delete: %v", k)
	}
	// Ohne Hub-Teil nur, wenn eindeutig.
	wantCode(t, "ohne Hub-Teil", e.callWrite(t, e.anna(), "create", CreateInput{Collection: "wissen", Name: "x.md"}),
		"invalid", "angemeldet an keph, team")
	// Leeres Dokument, wie „Neue Datei“ in VS Code.
	empty := e.callWrite(t, e.anna(), "create", CreateInput{Collection: "keph:wissen", Name: "leer.md"})
	if empty.Error != nil || empty.Size == nil || *empty.Size != 0 {
		t.Errorf("leer: %+v", empty)
	}
}

// Ablehnungen des Hubs kommen mit ihrem Code an, endgültig; der Abgleich wird
// nicht angestoßen. account_unauthenticated und not_readable des Hubs sind am
// Node not_readable.
func TestWriteRejected(t *testing.T) {
	e := newDocEnv(t)
	ids := e.fillWissen(t)
	own := e.callWrite(t, e.anna(), "create", CreateInput{Collection: "keph:wissen", Name: "eigen.md", Content: "x"})
	e.takeKicks()
	cases := []struct {
		name string
		h    http.Header
		tool string
		in   any
		code contract.Code
		want string
	}{
		{"Name vergeben", e.anna(), "create", CreateInput{Collection: "keph:wissen", Name: "a.md"}, contract.CodeNameTaken,
			"Hub keph: Dokument a.md gibt es schon"},
		{"Datei und Verzeichnis", e.anna(), "create", CreateInput{Collection: "keph:wissen", Name: "a.md/x"},
			contract.CodePathConflict, "zugleich Datei und Verzeichnis"},
		{"Verzeichnis", e.anna(), "create", CreateInput{Collection: "keph:wissen", Name: "dir"}, contract.CodePathConflict, ""},
		{"fehlt", e.anna(), "write", WriteInput{Collection: "keph:wissen", Name: "fehlt.md"}, contract.CodeNotFound, "fehlt.md"},
		{"delete Verzeichnis", e.anna(), "delete", DeleteInput{Collection: "keph:wissen", Name: "dir"}, contract.CodeNotFound, ""},
		{"fremd ohne supersede", e.anna(), "write", WriteInput{Collection: "keph:wissen", Name: "a.md", Content: "y"},
			contract.CodeForbidden, "gehört kleist, supersede fehlt"},
		{"fremd löschen", e.anna(), "delete", DeleteInput{Collection: "keph:wissen", Name: "b.txt"}, contract.CodeForbidden, ""},
		{"ohne write", e.otto(), "create", CreateInput{Collection: "keph:wissen", Name: "otto.md"}, contract.CodeForbidden,
			"write fehlt"},
		{"veraltet", e.anna(), "write", WriteInput{Collection: "keph:wissen", Name: "eigen.md", Content: "z",
			BaseRevision: base(own.Revision - 1)}, contract.CodeStaleRevision, fmt.Sprintf("hat Revision %d", own.Revision)},
		{"veraltet löschen", e.anna(), "delete", DeleteInput{Collection: "keph:wissen", Name: "eigen.md",
			BaseRevision: base(own.Revision + 1)}, contract.CodeStaleRevision, ""},
	}
	for _, c := range cases {
		before := e.hubs["keph"].writeCalls()
		out := e.callWrite(t, c.h, c.tool, c.in)
		wantCode(t, c.name, out, string(c.code), c.want)
		if out.Address != "keph:wissen" || e.hubs["keph"].writeCalls() != before+1 {
			t.Errorf("%s: Adresse %q, Aufrufe %d → %d", c.name, out.Address, before, e.hubs["keph"].writeCalls())
		}
	}
	if k := e.takeKicks(); len(k) != 0 {
		t.Errorf("Anstoß nach Ablehnung: %v", k)
	}
	if doc, _, _ := e.read(t, e.anna(), ReadInput{ID: ids["a.md"], Collection: "keph:"}); doc.Revision == 0 {
		t.Errorf("a.md: %+v", doc)
	}

	// Am Hub gesperrt, der Node weiß es noch nicht: der Hub fragt, der Node
	// meldet dasselbe wie seine eigene Prüfung.
	e.hubs["keph"].grant("otto", "wissen", "", contract.Rights{})
	out := e.callWrite(t, e.otto(), "create", CreateInput{Collection: "keph:wissen", Name: "otto.md"})
	wantCode(t, "gesperrt am Hub", out, string(contract.CodeNotReadable), "keph:wissen nicht lesbar")
	// Der Hub erlaubt dem Node die Collection nicht mehr.
	e.hubs["keph"].allowed["privat"] = false
	out = e.callWrite(t, e.anna(), "delete", DeleteInput{Collection: "keph:privat", Name: "geheim.md"})
	wantCode(t, "nicht mehr erlaubt", out, string(contract.CodeNotReadable), "keph:privat nicht lesbar")
}

// Was der Node schon an der Replica ablehnt, erreicht den Hub nie: nicht
// angemeldet, nicht lesbar, noch nie abgeglichen (mit dem Ausweg), Replica
// nicht lesbar, ungültige Adresse oder ungültiger Name.
func TestWriteCheckedAtNode(t *testing.T) {
	e := newDocEnv(t)
	ctx := context.Background()
	if err := e.nodes.AddHub(ctx, store.Hub{Name: "neu", NodeName: "laptop", Transport: store.TransportHTTP,
		Address: "http://127.0.0.1:1", Token: token(t)}, false); err != nil {
		t.Fatal(err)
	}
	e.breakReplica(t, "team")
	wrong := pair("keph", "anna", token(t))
	create := func(coll, name string) CreateInput { return CreateInput{Collection: coll, Name: name, Content: "x"} }
	cases := []struct {
		name string
		h    http.Header
		in   CreateInput
		code string
		want string
	}{
		{"ohne Anmeldung", nil, create("keph:wissen", "x.md"), "not_readable", "keph:wissen nicht lesbar"},
		{"falsches Token", wrong, create("keph:wissen", "x.md"), "not_readable", "keph:wissen nicht lesbar"},
		{"ohne Recht", e.otto(), create("keph:privat", "x.md"), "not_readable", "keph:privat nicht lesbar"},
		{"unbekannt", e.anna(), create("keph:gibtsnicht", "x.md"), "not_readable", "keph:gibtsnicht nicht lesbar"},
		{"unbekannter Hub", e.anna(), create("fremd:wissen", "x.md"), "not_readable", "fremd:wissen nicht lesbar"},
		{"noch nie abgeglichen", e.anna(), create("neu:wissen", "x.md"), "not_readable",
			"Hub neu: noch nie abgeglichen, der Node hat keine Replica; zuerst: kephalaion node sync neu"},
		{"Replica nicht lesbar", e.anna(), create("team:notizen", "x.md"), CodeInternal, "Hub team: Replica nicht lesbar"},
		{"ohne Collection", e.anna(), create("keph:", "x.md"), "invalid", "ohne Collection"},
		{"Adresse ungültig", e.anna(), create("keph:Wissen", "x.md"), "invalid", "Collection"},
		{"Name ungültig", e.anna(), create("keph:wissen", "../x.md"), "invalid", "name:"},
		{"SYSTEM:", e.anna(), create("keph:wissen", "SYSTEM:A:anna"), "invalid", "name:"},
		{"leerer Name", e.anna(), create("keph:wissen", ""), "invalid", "name:"},
	}
	for _, c := range cases {
		wantCode(t, c.name, e.callWrite(t, c.h, "create", c.in), c.code, c.want)
	}
	for alias, f := range e.hubs {
		if n := f.writeCalls(); n != 0 {
			t.Errorf("Hub %s: %d Anfragen", alias, n)
		}
	}
	if k := e.takeKicks(); len(k) != 0 {
		t.Errorf("Anstoß: %v", k)
	}
	// Die Pfade der Replicas stehen in keiner Antwort.
	res, _ := e.call(t, e.anna(), "create", create("team:notizen", "x.md"), nil)
	if raw, _ := json.Marshal(res); strings.Contains(string(raw), ".db") {
		t.Errorf("Pfad in der Antwort: %s", raw)
	}
}

// lostHub schreibt am Hub und meldet dann einen unklaren Ausgang, wie eine
// Antwort, die unterwegs verloren ging.
var errLost = fmt.Errorf("%w: Zeitüberschreitung nach dem Abschicken an http://127.0.0.1:7434", contract.ErrOutcomeUnknown)

// Der Weg zum Hub: nicht erreichbar, noch nicht unterstützt, unklar. Nur nach
// unklarem Ausgang stößt der Node den Abgleich an; hat der Hub geschrieben,
// steht das Dokument danach in der Replica. Lesen geht die ganze Zeit.
func TestWriteTransport(t *testing.T) {
	e := newDocEnv(t)
	e.fillWissen(t)
	f := e.hubs["keph"]
	in := CreateInput{Collection: "keph:wissen", Name: "neu.md", Content: "neu"}
	readable := func(what string) {
		t.Helper()
		if doc, _, errText := e.read(t, e.anna(), ReadInput{Collection: "keph:wissen", Name: "a.md"}); errText != "" ||
			doc.Kind != KindDocument {
			t.Errorf("%s: read %+v, %s", what, doc, errText)
		}
	}

	e.connect = func(store.Hub) (contract.Hub, error) {
		return nil, errors.New("dial tcp 127.0.0.1:7434: connect: connection refused")
	}
	out := e.callWrite(t, e.anna(), "create", in)
	wantCode(t, "Verbindung verweigert", out, CodeUnreachable, "Hub keph nicht erreichbar, nichts gespeichert")
	if strings.Contains(out.Error.Message, "7434") || !strings.Contains(e.log.String(), "connection refused") {
		t.Errorf("Adresse in der Meldung oder nicht im Log: %q\n%s", out.Error.Message, e.log.String())
	}
	readable("nicht erreichbar")
	e.connect = func(store.Hub) (contract.Hub, error) {
		return nil, fmt.Errorf("Transport https wird %w; bisher gehen local und http", ErrUnsupported)
	}
	out = e.callWrite(t, e.anna(), "create", in)
	wantCode(t, "https", out, CodeUnsupported, "noch nicht unterstützt; nichts gespeichert")
	if strings.Contains(out.Error.Message, "https") {
		t.Errorf("Transport in der Meldung: %q", out.Error.Message)
	}
	e.connect = nil

	for _, c := range []struct {
		name string
		err  error
		code string
		want string
	}{
		{"Fehler des Transports", errors.New("Post \"http://127.0.0.1:7434/v1/create\": EOF vor dem Abschicken"),
			CodeUnreachable, "nicht erreichbar"},
		{"Hub ohne Vorgang", fmt.Errorf("%w: 404", contract.ErrUnknownOperation), CodeUnsupported,
			"Hub keph kann noch nicht schreiben"},
		{"Node abgelehnt", contract.ErrUnauthenticated, "unauthenticated", "Hub keph nimmt diesen Node nicht an"},
		{"Fassung", contract.ErrUnsupportedVersion, "unsupported_version", "Fassung"},
	} {
		f.failWith(c.err, false)
		wantCode(t, c.name, e.callWrite(t, e.anna(), "create", in), c.code, c.want)
	}
	if k := e.takeKicks(); len(k) != 0 {
		t.Errorf("Anstoß ohne Schreiben: %v", k)
	}

	// Unklar, und der Hub hat geschrieben.
	f.failWith(errLost, true)
	out = e.callWrite(t, e.anna(), "create", in)
	wantCode(t, "unklar", out, CodeOutcomeUnknown, "gespeichert sein kann es")
	if !strings.Contains(out.Error.Message, "read") || strings.Contains(out.Error.Message, "7434") {
		t.Errorf("Meldung: %q", out.Error.Message)
	}
	if k := e.takeKicks(); !reflect.DeepEqual(k, []string{"keph"}) {
		t.Errorf("Anstoß nach unklarem Ausgang: %v", k)
	}
	readable("unklar")
	if doc, _, _ := e.read(t, e.anna(), ReadInput{Collection: "keph:wissen", Name: "neu.md"}); doc.Kind != KindNone {
		t.Errorf("vor dem Abgleich schon da: %+v", doc)
	}
	e.sync(t) // der angestoßene Abgleich
	if doc, res, _ := e.read(t, e.anna(), ReadInput{Collection: "keph:wissen", Name: "neu.md"}); doc.Kind != KindDocument ||
		textOf(res) != "neu" {
		t.Errorf("nach dem Abgleich: %+v", doc)
	}
	f.failWith(nil, false)
	if !strings.Contains(e.log.String(), "code=outcome_unknown") || !strings.Contains(e.log.String(), "Zeitüberschreitung") {
		t.Errorf("Log:\n%s", e.log.String())
	}
}

// Lässt sich die Antwort nicht in die Replica schreiben — hier nennt sie eine
// andere hub_id —, bleibt es ein Erfolg mit Hinweis; der Abgleich holt nach.
func TestWriteReplicaNotWritten(t *testing.T) {
	e := newDocEnv(t)
	f := e.hubs["keph"]
	f.mu.Lock()
	f.answerID = ulid.Make().String()
	f.mu.Unlock()
	out := e.callWrite(t, e.anna(), "create", CreateInput{Collection: "keph:wissen", Name: "neu.md", Content: "neu"})
	if out.Error != nil || out.Revision == 0 || out.ID == "" || !strings.Contains(out.Note, "nicht in die Replica") {
		t.Fatalf("create: %+v", out)
	}
	if k := e.takeKicks(); !reflect.DeepEqual(k, []string{"keph"}) {
		t.Errorf("Anstoß: %v", k)
	}
	if doc, _, _ := e.read(t, e.anna(), ReadInput{Collection: "keph:wissen", Name: "neu.md"}); doc.Kind != KindNone {
		t.Errorf("trotz fremder hub_id in der Replica: %+v", doc)
	}
	e.sync(t)
	if doc, _, _ := e.read(t, e.anna(), ReadInput{Collection: "keph:wissen", Name: "neu.md"}); doc.Revision != out.Revision {
		t.Errorf("nach dem Abgleich: %+v", doc)
	}
}

// Der MCP-Eingang trägt jedes Dokument, das der Hub annimmt, auch wenn JSON
// jedes Byte als \u00XX schreibt; darüber hinaus lehnt er ab (413).
func TestWriteRequestLimit(t *testing.T) {
	e := newDocEnv(t)
	big := strings.Repeat("\x01", contract.MaxDocumentBytes)
	out := e.callWrite(t, e.anna(), "create", CreateInput{Collection: "keph:wissen", Name: "gross.md", Content: big})
	if out.Error != nil || out.Size == nil || *out.Size != int64(len(big)) {
		t.Fatalf("1 MiB aus Steuerzeichen: %+v", out.Error)
	}
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"create","arguments":{"content":"` +
		strings.Repeat("x", MaxRequestBytes) + `"}}}`
	req, _ := http.NewRequest(http.MethodPost, e.url+Path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("über der Grenze: %d", resp.StatusCode)
	}
}

// Das Log nennt Vorgang, Hub, Node, Account und Code — nie Token oder
// Inhalt; die Antworten tragen kein Token (callWrite).
func TestWriteLog(t *testing.T) {
	e := newDocEnv(t)
	e.callWrite(t, e.anna(), "create", CreateInput{Collection: "keph:wissen", Name: "log.md", Content: "geheimer Inhalt"})
	e.callWrite(t, e.anna(), "create", CreateInput{Collection: "keph:wissen", Name: "log.md", Content: "geheimer Inhalt"})
	log := e.log.String()
	for _, want := range []string{"op=create", "hub=keph", "node=laptop", "account=anna", "code=name_taken"} {
		if !strings.Contains(log, want) {
			t.Errorf("Log ohne %s:\n%s", want, log)
		}
	}
	if strings.Contains(log, "geheimer Inhalt") {
		t.Errorf("Inhalt im Log:\n%s", log)
	}
	for _, tok := range e.tokens {
		if strings.Contains(log, tok) {
			t.Errorf("Token im Log:\n%s", log)
		}
	}
	for _, f := range e.hubs {
		f.mu.Lock()
		for _, r := range f.docs {
			if r.Name == "log.md" && r.CreatedBy != "anna" {
				t.Errorf("created_by %q", r.CreatedBy)
			}
		}
		f.mu.Unlock()
	}
}

// Ohne Weg zum Hub (HubLink leer) melden die Werkzeuge unsupported.
func TestWriteWithoutLink(t *testing.T) {
	e := newEnv(t)
	res, out, err := (&Node{nodes: e.nodes}).create(context.Background(), callWith(pair("keph", "bob", e.tokens["keph/bob"])),
		CreateInput{Collection: "keph:team-x", Name: "x.md"})
	if err != nil || res == nil || !res.IsError || out.Error == nil || out.Error.Code != CodeUnsupported ||
		!strings.Contains(textOf(res), "nicht eingerichtet") {
		t.Errorf("ohne HubLink: %+v, %+v, %v", res, out, err)
	}
}
