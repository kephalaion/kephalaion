package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/kephalaion/kephalaion/internal/loopback"
	"github.com/kephalaion/kephalaion/internal/node/mcpnode"
	nodestore "github.com/kephalaion/kephalaion/internal/node/store"
)

// Die Adresse eines Nodes für die Kommandozeile als seinen Client (node dir
// push|pull, node mcp add|status --node, Task 023): die Basis ohne /mcp —
// lokal http://127.0.0.1:7433, über einen Proxy https://<name>/<präfix>,
// etwa https://<name>/kephalaion; angehängt wird /mcp. http geht nur zu
// diesem Rechner (localhost, 127.0.0.1, [::1]) und zu host.docker.internal
// (Devcontainer), alles andere nur über https — sonst gingen Tokens im
// Klartext übers Netz. Keine Query, kein User, keiner Weiterleitung folgen.

// dockerHost ist der Name, unter dem ein Container den Rechner erreicht, auf
// dem er läuft (Docker Desktop).
const dockerHost = "host.docker.internal"

// nodeAddress ist eine geprüfte Adresse eines Nodes.
type nodeAddress struct {
	// Base ist die Adresse ohne / am Ende und ohne /mcp.
	Base string
	// Host ist der Host ohne Port.
	Host  string
	HTTPS bool
}

// Endpoint ist die Adresse des MCP-Eingangs: Base mit /mcp.
func (a nodeAddress) Endpoint() string { return a.Base + mcpnode.Path }

// Remote sagt, ob die Adresse entfernt ist: https, oder http zu
// host.docker.internal. Lokal ist nur http zu Loopback. Für einen Eintrag
// mit entfernter Adresse gilt die Wahl der Hubs (node mcp add --hub).
func (a nodeAddress) Remote() bool { return a.HTTPS || !loopback.IsHost(a.Host) }

// parseNodeAddress prüft eine Adresse aus --node.
func parseNodeAddress(raw string) (nodeAddress, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Opaque != "" {
		return nodeAddress{}, fmt.Errorf("--node %q: erwartet http://<host>:<port> (dieser Rechner) oder "+
			"https://<name>/<präfix> (über einen Proxy, etwa https://<name>/kephalaion)", raw)
	}
	if u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return nodeAddress{}, fmt.Errorf("--node %q: ohne Benutzer, Query und Fragment", raw)
	}
	host := u.Hostname()
	if u.Scheme == "http" && !loopback.IsHost(host) && !strings.EqualFold(host, dockerHost) {
		return nodeAddress{}, fmt.Errorf("--node %q: http nur zu diesem Rechner (localhost, 127.0.0.1, [::1]) und zu "+
			"%s — sonst ginge das Token im Klartext übers Netz; zu einem anderen Rechner https://<name>/<präfix> über "+
			"den Proxy vor dem Node", raw, dockerHost)
	}
	u.Path, u.RawPath = strings.TrimRight(u.Path, "/"), ""
	if strings.HasSuffix(u.Path, mcpnode.Path) {
		return nodeAddress{}, fmt.Errorf("--node %q: die Adresse ohne %s am Ende — %s hängt die Kommandozeile an "+
			"(etwa %s://%s%s)", raw, mcpnode.Path, mcpnode.Path, u.Scheme, u.Host,
			strings.TrimSuffix(u.Path, mcpnode.Path))
	}
	return nodeAddress{Base: u.String(), Host: host, HTTPS: u.Scheme == "https"}, nil
}

// localNodeAddress ist die Adresse eines Nodes, der auf listen lauscht.
func localNodeAddress(listen string) nodeAddress {
	if strings.HasPrefix(listen, ":") {
		listen = "127.0.0.1" + listen
	}
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		host = listen
	}
	return nodeAddress{Base: "http://" + listen, Host: host}
}

// readNodeCA liest --ca-file für eine Adresse: nur mit https; leer heißt
// System-Roots.
func readNodeCA(addr nodeAddress, path string) (*x509.CertPool, error) {
	if path == "" {
		return nil, nil
	}
	if !addr.HTTPS {
		return nil, errors.New("--ca-file gibt es nur mit einer https-Adresse (--node https://…)")
	}
	text, err := readCAFile(path)
	if err != nil {
		return nil, err
	}
	certs, err := nodestore.ParseCA(text)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	for _, c := range certs {
		pool.AddCert(c)
	}
	return pool, nil
}

// nodeDial ersetzt in Tests den Aufbau der Verbindung zum Node (etwa
// host.docker.internal auf 127.0.0.1); nil heißt wie üblich.
var nodeDial func(ctx context.Context, network, addr string) (net.Conn, error)

// nodeClient ist der HTTP-Client zu einem Node: bei https das Zertifikat
// gegen rootCAs (nil: System-Roots), TLS mindestens 1.2, HTTP/1.1, kein
// Client-Zertifikat — wie der Weg zum Hub (httpapi.NewClient). Er folgt
// keiner Weiterleitung: Der Node sendet nie eine, und Go schickte den Body
// samt Header-Paaren an das neue Ziel. header kommt an jede Anfrage.
func nodeClient(rootCAs *x509.CertPool, header http.Header) *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{RootCAs: rootCAs, MinVersion: tls.VersionTLS12, NextProtos: []string{"http/1.1"}}
	tr.ForceAttemptHTTP2 = false
	if nodeDial != nil {
		tr.DialContext = nodeDial
	}
	var rt http.RoundTripper = tr
	if len(header) > 0 {
		rt = headerTransport{header: header, next: tr}
	}
	return &http.Client{Transport: rt, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
}

// probeTimeout begrenzt die Prüfung des Nodes.
const probeTimeout = 15 * time.Second

// nodeInfo ist, was initialize über den Node sagt: die Version — leer, wenn
// die Antwort verdeckt ist (über einen Proxy ohne gültige Anmeldung).
type nodeInfo struct {
	Version string
}

// probeNode fragt den Node mit initialize, ohne Header-Paar: erst prüfen,
// dann verbinden oder eintragen. Ein Fehler sagt in Klartext, was nicht
// stimmt — Zertifikat, Gegenseite ohne TLS, nicht erreichbar, Präfix falsch
// oder Anmeldung des Proxys (401, 302, HTML), Host-Prüfung, kein Node
// dahinter. Ohne Token zählt die Probe nie als Fehlversuch am Node.
func probeNode(ctx context.Context, addr nodeAddress, rootCAs *x509.CertPool, caFile string) (nodeInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},` +
		`"clientInfo":{"name":"kephalaion","version":"1"}}}`
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, addr.Endpoint(), strings.NewReader(body))
	if err != nil {
		return nodeInfo{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := nodeClient(rootCAs, nil).Do(req)
	if err != nil {
		return nodeInfo{}, explainNodeError(addr, caFile, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return readInitialize(addr, resp, data)
}

// proxyHint ist der Satz zu einer Antwort, die nicht vom Node kommt.
const proxyHint = "Präfix falsch oder Anmeldung des Proxys — der Node antwortet nie mit 401, einer Weiterleitung " +
	"oder HTML; die Adresse ist die Basis ohne /mcp (etwa https://<name>/kephalaion), und die Route zu /mcp steht " +
	"ohne Anmeldung des Proxys (forward_auth)"

// readInitialize liest die Antwort auf initialize; alles, was nicht die
// Antwort eines Kephalaion-Nodes ist, wird erklärt.
func readInitialize(addr nodeAddress, resp *http.Response, data []byte) (nodeInfo, error) {
	where := "Node unter " + addr.Endpoint()
	status := fmt.Sprintf("HTTP %d", resp.StatusCode)
	if text := shortBody(resp, data); text != "" {
		status += ": " + text
	}
	switch code := resp.StatusCode; {
	case code >= 300 && code < 400:
		loc := resp.Header.Get("Location")
		return nodeInfo{}, fmt.Errorf("%s: eine Weiterleitung (%s nach %q): %s", where, status, loc, proxyHint)
	case code == http.StatusUnauthorized:
		return nodeInfo{}, fmt.Errorf("%s: eine Anmeldung des Proxys, nicht der Node (%s): %s", where, status, proxyHint)
	case code == http.StatusForbidden:
		return nodeInfo{}, fmt.Errorf("%s: Host-Prüfung des Nodes schlägt fehl (%s): %s", where, status, hostHint(addr))
	case code == http.StatusNotFound:
		return nodeInfo{}, fmt.Errorf("%s: dort antwortet kein Node (%s): Präfix falsch? Die Adresse ist die Basis "+
			"ohne /mcp — lokal http://127.0.0.1:7433, über einen Proxy etwa https://<name>/kephalaion", where, status)
	case code == http.StatusBadGateway || code == http.StatusServiceUnavailable || code == http.StatusGatewayTimeout:
		return nodeInfo{}, fmt.Errorf("%s: Proxy antwortet, aber der Node dahinter nicht (%s): läuft kephalaion "+
			"serve, und zeigt der Proxy auf listen des Nodes?", where, status)
	case code != http.StatusOK:
		return nodeInfo{}, fmt.Errorf("%s: unerwartete Antwort (%s)", where, status)
	}
	mt, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if mt == "text/event-stream" {
		// Die Antwort als SSE: der letzte data-Block.
		var last []byte
		for _, line := range bytes.Split(data, []byte("\n")) {
			if rest, ok := bytes.CutPrefix(line, []byte("data:")); ok {
				last = bytes.TrimSpace(rest)
			}
		}
		data, mt = last, "application/json"
	}
	if mt != "application/json" {
		return nodeInfo{}, fmt.Errorf("%s: keine Antwort eines MCP-Servers (HTTP 200, %s): %s", where,
			orNone(resp.Header.Get("Content-Type")), proxyHint)
	}
	var msg struct {
		Result struct {
			ServerInfo struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"serverInfo"`
		} `json:"result"`
	}
	if err := json.Unmarshal(data, &msg); err != nil || msg.Result.ServerInfo.Name != "kephalaion" {
		return nodeInfo{}, fmt.Errorf("%s: antwortet, aber nicht als Kephalaion-Node (serverInfo %q): %s", where,
			msg.Result.ServerInfo.Name, proxyHint)
	}
	return nodeInfo{Version: msg.Result.ServerInfo.Version}, nil
}

// hostHint sagt, woran eine 403 der Host-Prüfung liegt.
func hostHint(addr nodeAddress) string {
	switch {
	case strings.EqualFold(addr.Host, dockerHost):
		return dockerHost + " nimmt der Node nicht als Host an; im Devcontainer ein Weiterleiter von 127.0.0.1:<port> " +
			"zum Host (docs/konzept.md, „Devcontainer“)"
	case addr.HTTPS:
		return "setzt der Proxy Host auf die Loopback-Adresse des Nodes (header_up Host {upstream_hostport}, etwa " +
			"127.0.0.1:7433)?"
	}
	return "ein Tunnel geht nur mit gleichem Port"
}

// shortBody ist der Anfang einer Antwort als eine Zeile, außer HTML.
func shortBody(resp *http.Response, data []byte) string {
	mt, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if mt == "text/html" {
		return "HTML"
	}
	text := strings.Join(strings.Fields(string(data)), " ")
	if len(text) > 200 {
		text = text[:200] + "…"
	}
	if text == "" {
		return http.StatusText(resp.StatusCode)
	}
	return text
}

func orNone(s string) string {
	if s == "" {
		return "ohne Content-Type"
	}
	return s
}

// explainNodeError erklärt einen Fehler beim Verbinden mit dem Node:
// Zertifikat nicht vertraut, für einen anderen Namen, abgelaufen; eine
// Gegenseite ohne TLS; nicht erreichbar.
func explainNodeError(addr nodeAddress, caFile string, err error) error {
	where := "Node unter " + addr.Endpoint()
	var unknown x509.UnknownAuthorityError
	var hostErr x509.HostnameError
	var invalid x509.CertificateInvalidError
	var rec tls.RecordHeaderError
	switch {
	case errors.As(err, &unknown):
		issuer := "unbekannt"
		if unknown.Cert != nil {
			issuer = unknown.Cert.Issuer.String()
		}
		hint := "--ca-file?"
		if caFile != "" {
			hint = "passt die CA aus --ca-file?"
		}
		return fmt.Errorf("%s: Zertifikat von %s nicht vertraut (Aussteller %s; %s) — %v", where, addr.Host, issuer,
			hint, unknown)
	case errors.As(err, &hostErr):
		return fmt.Errorf("%s: Zertifikat gilt nicht für %s (ausgestellt für %s) — %v", where, addr.Host,
			joinOrNone(certNames(hostErr.Certificate)), hostErr)
	case errors.As(err, &invalid) && invalid.Reason == x509.Expired && invalid.Cert != nil:
		if now := time.Now(); now.Before(invalid.Cert.NotBefore) {
			return fmt.Errorf("%s: Zertifikat gilt erst ab %s — %v", where,
				invalid.Cert.NotBefore.UTC().Format(time.RFC3339), invalid)
		}
		return fmt.Errorf("%s: Zertifikat abgelaufen seit %s — %v", where, invalid.Cert.NotAfter.UTC().Format(time.RFC3339),
			invalid)
	case errors.Is(err, http.ErrSchemeMismatch) || errors.As(err, &rec):
		return fmt.Errorf("%s: die Gegenseite spricht kein TLS — https an einen Port ohne TLS? Der Node selbst "+
			"lauscht nur mit http auf Loopback; https spricht der Proxy davor", where)
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	return fmt.Errorf("%s nicht erreichbar: %w", where, err)
}
