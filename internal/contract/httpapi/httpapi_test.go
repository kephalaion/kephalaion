package httpapi

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kephalaion/kephalaion/internal/contract"
	"github.com/kephalaion/kephalaion/internal/reqlog"
)

// echoHub ist eine Attrappe von contract.Hub: Sie merkt sich die Anmeldung
// und die letzte Schreibanfrage und antwortet mit err, wenn gesetzt.
type echoHub struct {
	auth contract.NodeAuth
	err  error
	rows int
	// last ist die letzte Schreibanfrage (CreateRequest, WriteRequest,
	// DeleteRequest oder RenameRequest).
	last any
}

// written antwortet auf einen Schreibvorgang mit einer Zeile.
func (e *echoHub) written(auth contract.NodeAuth, version int, req any) (contract.WriteResponse, error) {
	e.auth, e.last = auth, req
	if e.err != nil {
		return contract.WriteResponse{}, e.err
	}
	return contract.WriteResponse{HubID: "01H", Version: version, Revision: 7,
		Rows: []contract.Row{{ID: "01D", Collection: "a", Name: "n.md", Revision: 7}}}, nil
}

func (e *echoHub) Create(_ context.Context, req contract.CreateRequest) (contract.WriteResponse, error) {
	return e.written(req.Auth, req.Version, req)
}

func (e *echoHub) Write(_ context.Context, req contract.WriteRequest) (contract.WriteResponse, error) {
	return e.written(req.Auth, req.Version, req)
}

func (e *echoHub) Delete(_ context.Context, req contract.DeleteRequest) (contract.WriteResponse, error) {
	return e.written(req.Auth, req.Version, req)
}

func (e *echoHub) Rename(_ context.Context, req contract.RenameRequest) (contract.WriteResponse, error) {
	return e.written(req.Auth, req.Version, req)
}

func (e *echoHub) Whoami(_ context.Context, req contract.WhoamiRequest) (contract.WhoamiResponse, error) {
	e.auth = req.Auth
	if e.err != nil {
		return contract.WhoamiResponse{}, e.err
	}
	return contract.WhoamiResponse{HubID: "01H", Version: req.Version, Node: req.Auth.Node, Allowed: []string{"a"}}, nil
}

func (e *echoHub) Rotate(_ context.Context, req contract.RotateRequest) (contract.RotateResponse, error) {
	e.auth = req.Auth
	if e.err != nil {
		return contract.RotateResponse{}, e.err
	}
	return contract.RotateResponse{HubID: "01H", Version: req.Version, Rows: []contract.Row{}}, nil
}

func (e *echoHub) Sync(_ context.Context, req contract.SyncRequest) (contract.SyncResponse, error) {
	e.auth = req.Auth
	if e.err != nil {
		return contract.SyncResponse{}, e.err
	}
	rows := make([]contract.Row, e.rows)
	for i := range rows {
		rows[i] = contract.Row{ID: strings.Repeat("x", 20), Collection: "a", Name: "n.md"}
	}
	return contract.SyncResponse{HubID: "01H", Version: req.Version, Rows: rows}, nil
}

func newClient(t *testing.T, url string) *Client {
	t.Helper()
	c, err := NewClient(url)
	if err != nil {
		t.Fatal(err)
	}
	c.Backoff = time.Millisecond
	return c
}

var auth = contract.NodeAuth{Node: "laptop", Token: "keph_geheim"}

func TestRoundTripAndHeaders(t *testing.T) {
	hub := &echoHub{rows: 200}
	var log bytes.Buffer
	srv := httptest.NewServer(reqlog.New(&log).Middleware("hub", NewHandler(hub)))
	defer srv.Close()
	c := newClient(t, srv.URL)
	resp, err := c.Sync(context.Background(), contract.SyncRequest{Version: 1, Auth: auth, PageSize: 1})
	if err != nil || len(resp.Rows) != 200 || resp.HubID != "01H" {
		t.Fatalf("Sync = %+v, %v", resp, err)
	}
	if hub.auth != auth {
		t.Errorf("Anmeldung am Hub = %+v", hub.auth)
	}
	if !strings.Contains(log.String(), "hub POST /v1/sync 200") || !strings.Contains(log.String(), "node=laptop") ||
		strings.Contains(log.String(), "geheim") {
		t.Errorf("Log: %s", log.String())
	}
}

func TestGzipWhenAsked(t *testing.T) {
	srv := httptest.NewServer(NewHandler(&echoHub{rows: 100}))
	defer srv.Close()
	for _, ask := range []bool{true, false} {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/sync", strings.NewReader(`{"page_size":1}`))
		req.Header.Set(HeaderNode, "laptop")
		if ask {
			req.Header.Set("Accept-Encoding", "gzip")
		}
		// Eigene Transportschicht ohne automatisches gzip, um die Antwort roh
		// zu sehen.
		resp, err := (&http.Client{Transport: &http.Transport{DisableCompression: true}}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		gz := resp.Header.Get("Content-Encoding") == "gzip"
		if gz != ask {
			t.Errorf("erbeten %v, gzip %v", ask, gz)
		}
		if gz {
			r, err := gzip.NewReader(bytes.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			body, _ = io.ReadAll(r)
		}
		if !strings.Contains(string(body), `"hub_id":"01H"`) {
			t.Errorf("Body: %.80s", body)
		}
	}
}

func TestErrorCodes(t *testing.T) {
	hub := &echoHub{}
	srv := httptest.NewServer(NewHandler(hub))
	defer srv.Close()
	c := newClient(t, srv.URL)
	for _, code := range contract.Codes {
		hub.err = &contract.Error{Code: code, Message: "m " + string(code)}
		_, err := c.Whoami(context.Background(), contract.WhoamiRequest{Version: 1, Auth: auth})
		var ce *contract.Error
		if !errors.As(err, &ce) || ce.Code != code || ce.Message != "m "+string(code) {
			t.Errorf("%s: %v", code, err)
		}
		// Auch über einen Schreibvorgang; not_found (404) ist kein
		// unbekannter Vorgang.
		_, err = c.Create(context.Background(), contract.CreateRequest{Version: 1, Auth: auth})
		if !errors.As(err, &ce) || ce.Code != code || errors.Is(err, contract.ErrUnknownOperation) ||
			errors.Is(err, contract.ErrOutcomeUnknown) {
			t.Errorf("create, %s: %v", code, err)
		}
	}
}

// whoami und sync wiederholen bei Fehlern des Hubs (5xx) und des Transports,
// rotate nie; sein Ausgang ist dann unklar.
func TestRetries(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, `{"code":"internal","message":"kaputt"}`, http.StatusInternalServerError)
	}))
	defer srv.Close()
	c := newClient(t, srv.URL)
	c.Retries = 2
	_, err := c.Whoami(context.Background(), contract.WhoamiRequest{Version: 1, Auth: auth})
	if err == nil || calls.Load() != 3 || errors.Is(err, contract.ErrOutcomeUnknown) {
		t.Errorf("whoami: %d Aufrufe, %v", calls.Load(), err)
	}
	calls.Store(0)
	_, err = c.Rotate(context.Background(), contract.RotateRequest{Version: 1, Auth: auth})
	if calls.Load() != 1 || !errors.Is(err, contract.ErrOutcomeUnknown) {
		t.Errorf("rotate: %d Aufrufe, %v", calls.Load(), err)
	}
	if strings.Contains(err.Error(), "geheim") {
		t.Errorf("Fehler nennt das Token: %v", err)
	}
}

// Eine Zeitüberschreitung nach dem Abschicken ist für rotate unklar; eine
// abgelehnte Verbindung nicht.
func TestRotateOutcome(t *testing.T) {
	release := make(chan struct{})
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		<-release
	}))
	defer srv.Close()
	defer close(release)
	c := newClient(t, srv.URL)
	c.ShortTimeout = 50 * time.Millisecond
	_, err := c.Rotate(context.Background(), contract.RotateRequest{Version: 1, Auth: auth})
	if !errors.Is(err, contract.ErrOutcomeUnknown) || calls.Load() != 1 {
		t.Errorf("Zeitüberschreitung: %d Aufrufe, %v", calls.Load(), err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	c = newClient(t, "http://"+addr)
	_, err = c.Rotate(context.Background(), contract.RotateRequest{Version: 1, Auth: auth})
	if err == nil || errors.Is(err, contract.ErrOutcomeUnknown) {
		t.Errorf("Verbindung abgelehnt: %v", err)
	}
}

func TestParsePathAndClientAddress(t *testing.T) {
	for p, want := range map[string]struct {
		v  int
		op string
		ok bool
	}{
		"/v1/sync":        {1, "sync", true},
		"/v2/whoami":      {2, "whoami", true},
		"/v-1/sync":       {-1, "sync", true},
		"/v1/":            {0, "", false},
		"/vx/sync":        {0, "", false},
		"/v1/a/b":         {0, "", false},
		"/sync":           {0, "", false},
		"/v99999999999/x": {0, "", false},
	} {
		v, op, ok := parsePath(p)
		if v != want.v || op != want.op || ok != want.ok {
			t.Errorf("parsePath(%q) = %d, %q, %v", p, v, op, ok)
		}
	}
	for _, bad := range []string{"localhost:7434", "ftp://x", "http://", "http://x/pfad", "http://u:p@x"} {
		if _, err := NewClient(bad); err == nil {
			t.Errorf("NewClient(%q) angenommen", bad)
		}
	}
}

// Der Client folgt keiner Weiterleitung: Das Ziel bekommt weder Body noch
// Token, und rotate hinter 307 scheitert eindeutig, ohne Wiederholung.
func TestNoRedirect(t *testing.T) {
	var targetCalls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetCalls.Add(1)
		http.Error(w, "nicht hier", http.StatusTeapot)
	}))
	defer target.Close()
	var calls atomic.Int32
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Redirect(w, r, target.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer src.Close()
	c := newClient(t, src.URL)
	c.Backoff = time.Millisecond
	ctx := context.Background()
	_, err := c.Rotate(ctx, contract.RotateRequest{Version: contract.Version, Auth: contract.NodeAuth{Node: "n", Token: "keph_node"},
		Account: "bob", Token: "keph_account", NewHash: strings.Repeat("a", 64)})
	if err == nil || errors.Is(err, contract.ErrOutcomeUnknown) || !strings.Contains(err.Error(), "307") {
		t.Errorf("rotate hinter 307: %v, erwartet eindeutigen Fehler", err)
	}
	if _, err := c.Whoami(ctx, contract.WhoamiRequest{Version: contract.Version}); err == nil {
		t.Error("whoami hinter 307 gelang")
	}
	if n := targetCalls.Load(); n != 0 {
		t.Errorf("Ziel der Weiterleitung bekam %d Anfragen", n)
	}
	if n := calls.Load(); n != 2 {
		t.Errorf("%d Anfragen, erwartet 2 (keine Wiederholung)", n)
	}
}
