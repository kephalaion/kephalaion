package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kephalaion/kephalaion/internal/contract"
	"github.com/kephalaion/kephalaion/internal/reqlog"
)

var writeAccount = contract.AccountAuth{Account: "bob", Token: "keph_account"}

// writeOp ist ein Schreibvorgang über den Client mit seiner Anfrage.
type writeOp struct {
	op   string
	req  any
	call func(c *Client, ctx context.Context) (contract.WriteResponse, error)
}

// writeOps sind die drei Schreibvorgänge, je mit einer kleinen Anfrage.
func writeOps() []writeOp {
	base := int64(3)
	create := contract.CreateRequest{Version: 1, Auth: auth, Account: writeAccount, Collection: "a", Name: "n.md",
		Content: "geheimer Inhalt"}
	write := contract.WriteRequest{Version: 1, Auth: auth, Account: writeAccount, Collection: "a", Name: "n.md",
		Content: "geheimer Inhalt", BaseRevision: &base}
	del := contract.DeleteRequest{Version: 1, Auth: auth, Account: writeAccount, Collection: "a", Name: "n.md",
		BaseRevision: &base}
	return []writeOp{
		{OpCreate, create, func(c *Client, ctx context.Context) (contract.WriteResponse, error) { return c.Create(ctx, create) }},
		{OpWrite, write, func(c *Client, ctx context.Context) (contract.WriteResponse, error) { return c.Write(ctx, write) }},
		{OpDelete, del, func(c *Client, ctx context.Context) (contract.WriteResponse, error) { return c.Delete(ctx, del) }},
	}
}

// Die Schreibvorgänge kommen mit allen Feldern am Hub an; das Log nennt
// Node und Account, nie Token oder Inhalt.
func TestWriteRoundTrip(t *testing.T) {
	hub := &echoHub{}
	var log bytes.Buffer
	srv := httptest.NewServer(reqlog.New(&log).Middleware("hub", NewHandler(hub)))
	defer srv.Close()
	c := newClient(t, srv.URL)
	for _, w := range writeOps() {
		resp, err := w.call(c, context.Background())
		if err != nil || resp.HubID != "01H" || resp.Version != 1 || resp.Revision != 7 || len(resp.Rows) != 1 {
			t.Errorf("%s = %+v, %v", w.op, resp, err)
		}
		if hub.auth != auth || !reflect.DeepEqual(hub.last, w.req) {
			t.Errorf("%s am Hub: %+v, %+v", w.op, hub.auth, hub.last)
		}
		if !strings.Contains(log.String(), "hub POST /v1/"+w.op+" 200") {
			t.Errorf("%s: Log %s", w.op, log.String())
		}
	}
	if l := log.String(); !strings.Contains(l, "node=laptop") || !strings.Contains(l, "account=bob") ||
		strings.Contains(l, "keph_") || strings.Contains(l, "geheim") {
		t.Errorf("Log: %s", l)
	}
	// Leerer Inhalt ist Inhalt: Der Client schickt content auch leer.
	req := contract.CreateRequest{Version: 1, Auth: auth, Account: writeAccount, Collection: "a", Name: "leer.md"}
	if _, err := c.Create(context.Background(), req); err != nil || !reflect.DeepEqual(hub.last, req) {
		t.Errorf("leerer Inhalt: %v, am Hub %+v", err, hub.last)
	}
}

// post schickt body roh an path und liefert Status und Code.
func post(t *testing.T, url, path string, body []byte) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url+path, bytes.NewReader(body))
	req.Header.Set(HeaderNode, "laptop")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var eb errorBody
	_ = json.NewDecoder(resp.Body).Decode(&eb)
	return resp.StatusCode, eb.Code
}

// content ist bei create und write Pflicht: fehlt es oder ist es null, ist
// die Anfrage ungültig und erreicht den Hub nicht.
func TestWriteContentRequired(t *testing.T) {
	hub := &echoHub{}
	srv := httptest.NewServer(NewHandler(hub))
	defer srv.Close()
	for _, op := range []string{OpCreate, OpWrite} {
		for body, want := range map[string]int{
			`{"account":{"account":"bob","token":"t"},"collection":"a","name":"n.md"}`:                400,
			`{"account":{"account":"bob","token":"t"},"collection":"a","name":"n.md","content":null}`: 400,
			``: 400,
			`{"account":{"account":"bob","token":"t"},"collection":"a","name":"n.md","content":""}`:    200,
			`{"account":{"account":"bob","token":"t"},"collection":"a","name":"n.md","content":"x"}`:   200,
			`{"account":{"account":"bob","token":"t"},"collection":"a","name":"n.md","content":"x"}{}`: 400,
		} {
			hub.last = nil
			status, code := post(t, srv.URL, "/v1/"+op, []byte(body))
			if status != want || (want == 400 && (code != "invalid" || hub.last != nil)) {
				t.Errorf("%s %s: HTTP %d %q, am Hub %v", op, body, status, code, hub.last)
			}
		}
	}
	// delete trägt keinen Inhalt.
	if status, _ := post(t, srv.URL, "/v1/delete", []byte(`{"collection":"a","name":"n.md"}`)); status != 200 {
		t.Errorf("delete ohne content: HTTP %d", status)
	}
}

// Die Grenze des Bodys trägt ein Dokument an der Größengrenze, auch wenn
// JSON jedes Byte auf sechs aufbläht. Was darüber liegt (413), prüft
// TestHTTPStatus in internal/hub/replication — dort wartet net/http nach
// einer 413 ohnehin 0,5 s.
func TestWriteAtSizeLimit(t *testing.T) {
	hub := &echoHub{}
	srv := httptest.NewServer(NewHandler(hub))
	defer srv.Close()
	c := newClient(t, srv.URL)

	content := strings.Repeat("\x01<>&", contract.MaxDocumentBytes/4)
	req := contract.WriteRequest{Version: 1, Auth: auth, Account: writeAccount, Collection: "a",
		Name: strings.Repeat("<", 200) + "/" + strings.Repeat("&", 200) + ".md", Content: content}
	body, _ := json.Marshal(req)
	if len(content) != contract.MaxDocumentBytes || len(body) < 6*contract.MaxDocumentBytes || len(body) > MaxWriteBodyBytes {
		t.Fatalf("Inhalt %d, Body %d Bytes", len(content), len(body))
	}
	if _, err := c.Write(context.Background(), req); err != nil {
		t.Fatalf("Dokument an der Grenze: %v", err)
	}
	if got, ok := hub.last.(contract.WriteRequest); !ok || got.Content != content || got.Name != req.Name {
		t.Error("Inhalt kam nicht unverändert an")
	}
}

// Schreibvorgänge wiederholt der Client nie: nach 5xx und nach einer
// Zeitüberschreitung ist der Ausgang unklar, nach genau einem Aufruf.
func TestWriteNoRetry(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	status := http.StatusInternalServerError
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if status == 0 {
			<-release
			return
		}
		w.WriteHeader(status)
		fmt.Fprint(w, `{"code":"internal","message":"kaputt"}`)
	}))
	defer srv.Close()
	defer close(release)
	c := newClient(t, srv.URL)
	c.Retries = 3
	for _, s := range []int{http.StatusInternalServerError, http.StatusServiceUnavailable, 0} {
		status = s
		if s == 0 {
			c.ShortTimeout = 50 * time.Millisecond // Zeitüberschreitung
		}
		for _, w := range writeOps() {
			calls.Store(0)
			_, err := w.call(c, context.Background())
			var ce *contract.Error
			if n := calls.Load(); n != 1 || !errors.Is(err, contract.ErrOutcomeUnknown) || errors.As(err, &ce) {
				t.Errorf("%s nach %d: %d Aufrufe, %v", w.op, s, n, err)
			}
			if err != nil && (strings.Contains(err.Error(), "keph_") || strings.Contains(err.Error(), "geheim")) {
				t.Errorf("%s: Fehler nennt Token oder Inhalt: %v", w.op, err)
			}
		}
	}
}

// Eine abgelehnte Verbindung und eine Weiterleitung sind eindeutig: nichts
// geschehen, kein unklarer Ausgang, kein Fehler des Vertrags.
func TestWriteNotSent(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	refused := newClient(t, "http://"+addr)

	var targetCalls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targetCalls.Add(1) }))
	defer target.Close()
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer src.Close()
	redirected := newClient(t, src.URL)

	for name, c := range map[string]*Client{"Verbindung abgelehnt": refused, "Weiterleitung": redirected} {
		for _, w := range writeOps() {
			_, err := w.call(c, context.Background())
			var ce *contract.Error
			if err == nil || errors.Is(err, contract.ErrOutcomeUnknown) || errors.Is(err, contract.ErrUnknownOperation) ||
				errors.As(err, &ce) {
				t.Errorf("%s, %s: %v", name, w.op, err)
			}
		}
	}
	if n := targetCalls.Load(); n != 0 {
		t.Errorf("Ziel der Weiterleitung bekam %d Anfragen", n)
	}
}

// Ein Hub, der den Vorgang nicht kennt, antwortet 404 mit invalid — so wie
// jeder Hub vor Task 014 auf /v1/create. Der Client meldet das als eigenen
// Fall: nicht ungültig, nicht unklar, ohne Wiederholung.
func TestUnknownOperation(t *testing.T) {
	var calls atomic.Int32
	current := NewHandler(&echoHub{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, op, _ := parsePath(r.URL.Path)
		if op == OpCreate || op == OpWrite || op == OpDelete {
			calls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprintf(w, `{"code":"invalid","message":"unbekannter Vorgang \"%s\""}`, op)
			return
		}
		current.ServeHTTP(w, r)
	}))
	defer srv.Close()
	c := newClient(t, srv.URL)
	c.Retries = 3
	for _, w := range writeOps() {
		calls.Store(0)
		_, err := w.call(c, context.Background())
		if !errors.Is(err, contract.ErrUnknownOperation) || errors.Is(err, contract.ErrInvalid) ||
			errors.Is(err, contract.ErrOutcomeUnknown) || calls.Load() != 1 {
			t.Errorf("%s: %d Aufrufe, %v", w.op, calls.Load(), err)
		}
	}
	if _, err := c.Whoami(context.Background(), contract.WhoamiRequest{Version: 1, Auth: auth}); err != nil {
		t.Errorf("whoami am alten Hub: %v", err)
	}

	// Der Handler selbst antwortet auf einen unbekannten Vorgang ebenso.
	srv2 := httptest.NewServer(NewHandler(&echoHub{}))
	defer srv2.Close()
	var out contract.WriteResponse
	err := newClient(t, srv2.URL).call(context.Background(), "gibtsnicht", 1, auth, struct{}{}, &out, false, time.Second)
	if !errors.Is(err, contract.ErrUnknownOperation) {
		t.Errorf("unbekannter Vorgang am Handler: %v", err)
	}
}

// JSON trüge ungültiges UTF-8 nicht unverändert (encoding/json ersetzt es
// durch U+FFFD): Der Client schickt einen solchen Namen oder Inhalt nicht
// ab und meldet invalid wie der Hub; der Handler nimmt einen solchen Body
// nicht an.
func TestWriteInvalidUTF8(t *testing.T) {
	var calls atomic.Int32
	hub := &echoHub{}
	h := NewHandler(hub)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		h.ServeHTTP(w, r)
	}))
	defer srv.Close()
	c := newClient(t, srv.URL)
	ctx := context.Background()
	for name, call := range map[string]func() error{
		"create Inhalt": func() error {
			_, err := c.Create(ctx, contract.CreateRequest{Version: 1, Auth: auth, Name: "n.md", Content: "a\xffb"})
			return err
		},
		"write Inhalt": func() error {
			_, err := c.Write(ctx, contract.WriteRequest{Version: 1, Auth: auth, Name: "n.md", Content: "\xc3"})
			return err
		},
		"write Name": func() error {
			_, err := c.Write(ctx, contract.WriteRequest{Version: 1, Auth: auth, Name: "n\xff.md"})
			return err
		},
		"delete Name": func() error {
			_, err := c.Delete(ctx, contract.DeleteRequest{Version: 1, Auth: auth, Name: "n\xff.md"})
			return err
		},
	} {
		if err := call(); !errors.Is(err, contract.ErrInvalid) || calls.Load() != 0 {
			t.Errorf("%s: %d Aufrufe, %v", name, calls.Load(), err)
		}
	}
	for _, op := range []string{OpCreate, OpWhoami} {
		status, code := post(t, srv.URL, "/v1/"+op, []byte("{\"name\":\"n.md\",\"content\":\"a\xffb\"}"))
		if status != 400 || code != "invalid" || hub.last != nil {
			t.Errorf("%s mit rohem ungültigem UTF-8: HTTP %d %q, am Hub %v", op, status, code, hub.last)
		}
	}
}
