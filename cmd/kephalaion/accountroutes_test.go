package main

import (
	"crypto/tls"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/kephalaion/kephalaion/internal/config"
	"github.com/kephalaion/kephalaion/internal/ident"
	"github.com/kephalaion/kephalaion/internal/node/mcpnode"
	"github.com/kephalaion/kephalaion/internal/testcert"
)

// postAccount schickt eine Anfrage an eine Route für Accounts mit dem
// Header-Paar eines Hubs.
func postAccount(t *testing.T, client *http.Client, url, alias, account, tok, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Keph-Account-"+alias, account)
	req.Header.Set("X-Keph-Token-"+alias, tok)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(data)
}

// Die Routen für Accounts durch serve (Task 028): Direkt nach hub account
// add und grant, ohne Abgleich, prüft und rotiert ein Client über den Node
// — zum Hub über local (eigen) und über HTTP (fern), am Node lokal und über
// einen Proxy mit TLS. Danach nimmt der MCP-Eingang das neue Token sofort
// an. Ein falsches Token über den Proxy ist ein Fehlversuch mit via; ein
// unbekannter Hub sieht dort genauso aus. Kein Token im Log.
func TestAccountRoutesThroughServe(t *testing.T) {
	slow(t, "serve mit Hub und Node, zwei Hubs, lokal und über einen Proxy")
	e := newCommEnv(t)
	e.run(t, "config", "set", "node", "sync_interval", "0").want(t, 0)
	srv := startServe(t, portZero(t, e.cfg))
	nodeAddr := srv.addrs[config.Node]
	direct := "http://" + nodeAddr
	ca := testcert.NewCA(t, "Proxy-CA")
	proxy := newProxyNode(t, nodeAddr, serverCert(ca.ServerNow(t, "127.0.0.1")), "/kephalaion", "", proxyLogin)
	viaProxy := proxy.URL + "/kephalaion"
	tlsClient := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: ca.Pool()}}}
	var secrets []string
	for _, tok := range e.tokens {
		secrets = append(secrets, tok)
	}

	var hiddenWrong string
	for _, alias := range []string{"eigen", "fern"} {
		acc := "neu-" + alias
		r := e.run(t, "hub", "account", "add", acc, "--user", "kleist")
		r.want(t, 0)
		setup := tokenFrom(t, r.out)
		e.run(t, "hub", "account", "grant", acc, "team-x", "--write").want(t, 0)
		next, err := ident.NewToken()
		if err != nil {
			t.Fatal(err)
		}
		secrets = append(secrets, setup, next)

		status, body := postAccount(t, http.DefaultClient, direct+mcpnode.AccountCheckPath, alias, acc, setup, "")
		var res mcpnode.AccountResult
		if err := json.Unmarshal([]byte(body), &res); status != http.StatusOK || err != nil || res.User != "kleist" ||
			!reflect.DeepEqual(res.Collections, []string{"team-x"}) {
			t.Fatalf("%s: check vor dem Abgleich: HTTP %d %s", alias, status, body)
		}
		status, body = postAccount(t, http.DefaultClient, direct+mcpnode.AccountRotatePath, alias, acc, setup,
			`{"new_hash":"`+ident.HashToken(next)+`"}`)
		res = mcpnode.AccountResult{}
		if err := json.Unmarshal([]byte(body), &res); status != http.StatusOK || err != nil ||
			!reflect.DeepEqual(res.Replica, []string{"team-x"}) {
			t.Fatalf("%s: rotate vor dem Abgleich: HTTP %d %s", alias, status, body)
		}
		// Der MCP-Eingang kennt das neue Token sofort.
		who := mcpWhoami(t, direct+"/mcp", map[string][2]string{alias: {acc, next}})
		ok := false
		for _, h := range who.Hubs {
			ok = ok || (h.Hub == alias && h.Login == mcpnode.LoginOK && h.Account == acc)
		}
		if !ok {
			t.Errorf("%s: MCP mit dem neuen Token: %+v", alias, who.Hubs)
		}

		// Über den Proxy: das Einrichtungstoken gilt nicht mehr — verdeckt,
		// ein Fehlversuch mit via; das neue gilt, mit Namen.
		status, hiddenWrong = postAccount(t, tlsClient, viaProxy+mcpnode.AccountCheckPath, alias, acc, setup, "")
		if status != http.StatusForbidden || !strings.Contains(hiddenWrong, `"hidden":true`) ||
			strings.Contains(hiddenWrong, alias) || strings.Contains(hiddenWrong, acc) {
			t.Errorf("%s: altes Token über den Proxy: HTTP %d %s", alias, status, hiddenWrong)
		}
		if !logLine(srv.log.String(), "node POST /account/check 403", "via=127.0.0.1 login=invalid hub="+alias) {
			t.Errorf("%s: kein Fehlversuch im Log:\n%s", alias, srv.log.String())
		}
		status, body = postAccount(t, tlsClient, viaProxy+mcpnode.AccountCheckPath, alias, acc, next, "")
		if status != http.StatusOK || !strings.Contains(body, `"hub":"`+alias+`"`) {
			t.Errorf("%s: neues Token über den Proxy: HTTP %d %s", alias, status, body)
		}
	}
	// Ein Hub, den der Node nicht kennt, sieht über den Proxy aus wie ein
	// falsches Token.
	status, body := postAccount(t, tlsClient, viaProxy+mcpnode.AccountCheckPath, "nirgends", "neu-fern", secrets[0], "")
	if status != http.StatusForbidden || body != hiddenWrong {
		t.Errorf("unbekannter Hub über den Proxy: HTTP %d %s, erwartet %s", status, body, hiddenWrong)
	}
	for _, s := range secrets {
		if strings.Contains(srv.log.String(), s) {
			t.Fatal("Token im Log von serve")
		}
	}
}
