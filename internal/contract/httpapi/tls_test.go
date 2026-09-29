package httpapi

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kephalaion/kephalaion/internal/contract"
	"github.com/kephalaion/kephalaion/internal/testcert"
)

// tlsServer ist „Proxy plus Hub“ für die Tests: ein TLS-Server mit dem
// Zertifikat cert, der hub bedient und zählt, wie viele Anfragen ankamen und
// mit welchem Protokoll.
type tlsServer struct {
	*httptest.Server
	calls  atomic.Int32
	protos atomic.Value // string: das Protokoll der letzten Anfrage
}

func newTLSServer(t *testing.T, hub contract.Hub, cert tls.Certificate) *tlsServer {
	t.Helper()
	s := &tlsServer{}
	h := NewHandler(hub)
	s.Server = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.calls.Add(1)
		s.protos.Store(r.Proto)
		h.ServeHTTP(w, r)
	}))
	s.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	// Der Server böte HTTP/2 an; der Client bleibt bei HTTP/1.1.
	s.EnableHTTP2 = true
	// Gescheiterte Handshakes sind hier erwartet; ohne Log.
	s.Config.ErrorLog = log.New(io.Discard, "", 0)
	s.StartTLS()
	t.Cleanup(s.Close)
	return s
}

func newTLSClient(t *testing.T, url string, pool *x509.CertPool) *Client {
	t.Helper()
	c, err := NewClient(url, pool)
	if err != nil {
		t.Fatal(err)
	}
	c.Backoff = time.Millisecond
	return c
}

// notSentError prüft einen Fehler, der vor dem Abschicken entstand: kein
// Fehler des Vertrags, kein unklarer Ausgang, und die Meldung nennt den
// Grund.
func notSentError(t *testing.T, what string, err error, reason string) {
	t.Helper()
	var ce *contract.Error
	if err == nil || errors.As(err, &ce) || errors.Is(err, contract.ErrOutcomeUnknown) ||
		!strings.Contains(err.Error(), reason) {
		t.Errorf("%s: %v, erwartet Fehler vor dem Abschicken mit %q", what, err, reason)
	}
}

// Über TLS mit der richtigen CA gehen whoami, sync und create; gesprochen
// wird HTTP/1.1, obwohl der Server HTTP/2 anbietet.
func TestTLSRightCA(t *testing.T) {
	ca := testcert.NewCA(t, "Test-CA")
	hub := &echoHub{rows: 3}
	srv := newTLSServer(t, hub, ca.ServerNow(t, "127.0.0.1"))
	c := newTLSClient(t, srv.URL, ca.Pool())
	ctx := context.Background()
	if resp, err := c.Whoami(ctx, contract.WhoamiRequest{Version: 1, Auth: auth}); err != nil || resp.Node != "laptop" {
		t.Fatalf("whoami: %+v, %v", resp, err)
	}
	if resp, err := c.Sync(ctx, contract.SyncRequest{Version: 1, Auth: auth}); err != nil || len(resp.Rows) != 3 {
		t.Fatalf("sync: %+v, %v", resp, err)
	}
	if resp, err := c.Create(ctx, contract.CreateRequest{Version: 1, Auth: auth, Collection: "a", Name: "n.md"}); err != nil ||
		resp.Revision != 7 {
		t.Fatalf("create: %+v, %v", resp, err)
	}
	if hub.auth != auth {
		t.Errorf("Anmeldung am Hub = %+v", hub.auth)
	}
	if n := srv.calls.Load(); n != 3 {
		t.Errorf("%d Anfragen am Server, erwartet 3", n)
	}
	if p := srv.protos.Load(); p != "HTTP/1.1" {
		t.Errorf("Protokoll %v, erwartet HTTP/1.1", p)
	}
}

// Ein Zertifikatsfehler — falsche CA, keine CA gegen Selbstsigniertes,
// falscher Name, abgelaufen — tritt vor dem Abschicken auf: kein unklarer
// Ausgang bei rotate und create, am Server kommt nichts an, keine
// Wiederholung bei whoami.
func TestTLSCertificateErrors(t *testing.T) {
	ca, other := testcert.NewCA(t, "Richtige CA"), testcert.NewCA(t, "Andere CA")
	cases := []struct {
		name   string
		cert   tls.Certificate
		pool   *x509.CertPool
		reason string
	}{
		{"falsche CA", ca.ServerNow(t, "127.0.0.1"), other.Pool(), "unknown authority"},
		{"ohne CA", ca.ServerNow(t, "127.0.0.1"), nil, "unknown authority"},
		{"falscher Name", ca.ServerNow(t, "hub.example.org"), ca.Pool(), "127.0.0.1"},
		{"abgelaufen", ca.ServerExpired(t, "127.0.0.1"), ca.Pool(), "expired"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newTLSServer(t, &echoHub{}, tc.cert)
			c := newTLSClient(t, srv.URL, tc.pool)
			c.Retries = 3
			ctx := context.Background()
			start := time.Now()
			_, err := c.Whoami(ctx, contract.WhoamiRequest{Version: 1, Auth: auth})
			notSentError(t, "whoami", err, tc.reason)
			if d := time.Since(start); d > 500*time.Millisecond {
				t.Errorf("whoami dauerte %s: wiederholt trotz Zertifikatsfehler?", d)
			}
			_, err = c.Rotate(ctx, contract.RotateRequest{Version: 1, Auth: auth})
			notSentError(t, "rotate", err, tc.reason)
			_, err = c.Create(ctx, contract.CreateRequest{Version: 1, Auth: auth})
			notSentError(t, "create", err, tc.reason)
			if n := srv.calls.Load(); n != 0 {
				t.Errorf("%d Anfragen kamen am Server an", n)
			}
			if strings.Contains(err.Error(), "geheim") {
				t.Errorf("Fehler nennt das Token: %v", err)
			}
		})
	}
}

// Kein Zertifikatsfehler zählt weiter als unklar: Eine Zeitüberschreitung
// nach dem Abschicken über TLS ist für rotate unklar wie ohne TLS.
func TestTLSTimeoutIsUnclear(t *testing.T) {
	ca := testcert.NewCA(t, "Test-CA")
	release := make(chan struct{})
	var calls atomic.Int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		<-release
	}))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{ca.ServerNow(t, "127.0.0.1")}}
	srv.StartTLS()
	// Erst den Handler freigeben, dann den Server schließen (LIFO).
	defer srv.Close()
	defer close(release)
	c := newTLSClient(t, srv.URL, ca.Pool())
	c.ShortTimeout = 50 * time.Millisecond
	_, err := c.Rotate(context.Background(), contract.RotateRequest{Version: 1, Auth: auth})
	if !errors.Is(err, contract.ErrOutcomeUnknown) || calls.Load() != 1 {
		t.Errorf("Zeitüberschreitung über TLS: %d Aufrufe, %v", calls.Load(), err)
	}
}

// Die Gegenseite spricht kein TLS (https:// gegen einen Klartext-Port) oder
// nur Klartext gegen den TLS-Port (http:// gegen den TLS-Server): beides
// scheitert eindeutig, ohne Fehler des Vertrags; der Hub führt nichts aus.
func TestTLSMismatch(t *testing.T) {
	ca := testcert.NewCA(t, "Test-CA")
	plain := httptest.NewServer(NewHandler(&echoHub{}))
	defer plain.Close()
	c := newTLSClient(t, "https://"+strings.TrimPrefix(plain.URL, "http://"), ca.Pool())
	_, err := c.Rotate(context.Background(), contract.RotateRequest{Version: 1, Auth: auth})
	notSentError(t, "https gegen Klartext", err, "HTTP response to HTTPS client")

	srv := newTLSServer(t, &echoHub{}, ca.ServerNow(t, "127.0.0.1"))
	c = newTLSClient(t, "http://"+strings.TrimPrefix(srv.URL, "https://"), nil)
	c.Retries = 0
	_, err = c.Whoami(context.Background(), contract.WhoamiRequest{Version: 1, Auth: auth})
	var ce *contract.Error
	if err == nil || errors.As(err, &ce) {
		t.Errorf("http gegen TLS-Port: %v", err)
	}
	if n := srv.calls.Load(); n != 0 {
		t.Errorf("%d Anfragen kamen am Handler an", n)
	}
}

// Ein Status ohne Code des Vertrags — vom Proxy (502) oder von der
// Host-Prüfung (403 als Text) — kommt als StatusError mit dem Text der
// Antwort an, in einer Zeile; bei rotate bleibt er unklar (abgeschickt).
func TestStatusError(t *testing.T) {
	var status atomic.Int32
	status.Store(http.StatusForbidden)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status.Load() == http.StatusForbidden {
			http.Error(w, "Forbidden: Host \"x:1\"\nist nicht dieser Rechner\n", http.StatusForbidden)
			return
		}
		w.WriteHeader(int(status.Load()))
	}))
	defer srv.Close()
	c := newClient(t, srv.URL)
	c.Retries = 0
	ctx := context.Background()
	var se *StatusError
	_, err := c.Whoami(ctx, contract.WhoamiRequest{Version: 1, Auth: auth})
	if !errors.As(err, &se) || se.Status != 403 || se.Message != `Forbidden: Host "x:1" ist nicht dieser Rechner` ||
		strings.Contains(err.Error(), "\n") {
		t.Errorf("403: %v (%+v)", err, se)
	}
	status.Store(http.StatusBadGateway)
	se = nil
	_, err = c.Whoami(ctx, contract.WhoamiRequest{Version: 1, Auth: auth})
	if !errors.As(err, &se) || se.Status != 502 || se.Message != "Bad Gateway" {
		t.Errorf("502: %v (%+v)", err, se)
	}
	_, err = c.Rotate(ctx, contract.RotateRequest{Version: 1, Auth: auth})
	if !errors.Is(err, contract.ErrOutcomeUnknown) || !strings.Contains(err.Error(), "HTTP 502") {
		t.Errorf("rotate hinter 502: %v", err)
	}
}

// Die abgelehnte Verbindung bleibt wie bisher eindeutig — auch mit
// Root-CAs, die nie zum Zug kommen.
func TestTLSRefused(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	c := newTLSClient(t, "https://"+addr, testcert.NewCA(t, "CA").Pool())
	_, err = c.Rotate(context.Background(), contract.RotateRequest{Version: 1, Auth: auth})
	if err == nil || errors.Is(err, contract.ErrOutcomeUnknown) {
		t.Errorf("Verbindung abgelehnt: %v", err)
	}
}

// Ein Pfad in der Adresse ist ein Präfix: Der Client schickt
// <adresse>/v1/<vorgang>, ein Proxy davor nimmt den Präfix weg.
func TestClientPathPrefix(t *testing.T) {
	hub := &echoHub{}
	var seen atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.Store(r.URL.Path)
		http.StripPrefix("/kephhub", NewHandler(hub)).ServeHTTP(w, r)
	}))
	defer srv.Close()
	c := newClient(t, srv.URL+"/kephhub/")
	resp, err := c.Whoami(context.Background(), contract.WhoamiRequest{Version: 1, Auth: auth})
	if err != nil || resp.Node != "laptop" {
		t.Fatalf("whoami hinter Präfix: %+v, %v", resp, err)
	}
	if p := seen.Load(); p != "/kephhub/v1/whoami" {
		t.Errorf("Pfad am Server %v, erwartet /kephhub/v1/whoami", p)
	}
	// Ohne Präfix im Eintrag landet die Anfrage daneben: 404 mit invalid,
	// also ein unbekannter Vorgang aus Sicht des Clients.
	c = newClient(t, srv.URL)
	if _, err := c.Whoami(context.Background(), contract.WhoamiRequest{Version: 1, Auth: auth}); err == nil {
		t.Error("whoami ohne Präfix gelang")
	}
}
