package main

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/kephalaion/kephalaion/internal/config"
	hubstore "github.com/kephalaion/kephalaion/internal/hub/store"
	"github.com/kephalaion/kephalaion/internal/ident"
	"github.com/kephalaion/kephalaion/internal/setupcode"
)

// codePattern findet einen Einrichtungscode in einer Ausgabe.
var codePattern = regexp.MustCompile(setupcode.Prefix + `[A-Za-z0-9_-]+`)

// codeFrom holt den Einrichtungscode aus der Ausgabe von hub node add|token:
// genau einer, und kein Token im Klartext daneben.
func codeFrom(t *testing.T, out string) (string, setupcode.Code) {
	t.Helper()
	found := codePattern.FindAllString(out, -1)
	var full []string
	for _, f := range found {
		if _, err := setupcode.Decode(f); err == nil {
			full = append(full, f)
		}
	}
	if len(full) != 1 {
		t.Fatalf("%d Einrichtungscodes in der Ausgabe, erwartet genau einen:\n%s", len(full), out)
	}
	c, _ := setupcode.Decode(full[0])
	if strings.Contains(out, c.Token) {
		t.Fatalf("das Token steht im Klartext neben dem Code:\n%s", out)
	}
	for _, f := range strings.Fields(out) {
		if strings.HasPrefix(f, ident.TokenPrefix) && ident.CheckToken(f) == nil {
			t.Fatalf("ein Token im Klartext in der Ausgabe:\n%s", out)
		}
	}
	return full[0], c
}

// nodeHashOK sagt, ob token das gespeicherte Token des Nodes am Hub ist.
func nodeHashOK(t *testing.T, cfg, node, token string) bool {
	t.Helper()
	n, err := hubStore(t, cfg).Node(context.Background(), node)
	if err != nil {
		t.Fatal(err)
	}
	return n.TokenHash == ident.HashToken(token)
}

func TestHubSettings(t *testing.T) {
	dir := isolate(t)
	cfg := setup(t, dir)
	c := "--config=" + cfg
	runT(t, "config", "set", "hub", "public_url", "https://vm.example/kephalaion/hub", c).
		want(t, 0, "hub public_url = https://vm.example/kephalaion/hub")
	runT(t, "config", "set", "hub", "alias", "vm", c).want(t, 0, "hub alias = vm")
	runT(t, "config", "set", "hub", "public_url", "http://vm.example/hub", c).want(t, 1, "public_url", "https://")
	runT(t, "config", "set", "hub", "public_url", "https://vm.example/hub?x=1", c).want(t, 1, "ohne Query")
	runT(t, "config", "set", "hub", "alias", "a:b", c).want(t, 1, "alias")
	runT(t, "config", "set", "hub", "gibtsnicht", "x", c).want(t, 1, "unbekannter Schlüssel", "alias", "public_url")
	runT(t, "config", "unset", "hub", "gibtsnicht", c).want(t, 1, "unbekannter Schlüssel")
	runT(t, "config", "show", c).want(t, 0, "settings hub:", "alias = vm", "public_url = https://vm.example/kephalaion/hub")

	// Export und Import tragen sie; ein Import mit ungültigem Wert schreibt
	// nichts.
	exp := filepath.Join(dir, "export.yaml")
	runT(t, "config", "export", "--output", exp, c).want(t, 0)
	runT(t, "config", "unset", "hub", "alias", c).want(t, 0, "entfernt")
	runT(t, "config", "unset", "hub", "alias", c).want(t, 0, "war nicht gesetzt")
	runT(t, "config", "import", exp, c).want(t, 0)
	if got := getSettings(t, cfg, config.Hub); got["alias"] != "vm" || got["public_url"] != "https://vm.example/kephalaion/hub" {
		t.Errorf("settings nach Import: %v", got)
	}
	data, err := os.ReadFile(exp)
	if err != nil {
		t.Fatal(err)
	}
	bad := strings.Replace(string(data), "alias: vm", "alias: A:B", 1)
	if bad == string(data) {
		t.Fatalf("alias nicht im Export:\n%s", data)
	}
	badFile := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(badFile, []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	runT(t, "config", "import", badFile, c).want(t, 1, "Nichts geschrieben", "alias")
	if err := hubStore(t, cfg).Import(context.Background(), map[string]string{"public_url": "ftp://x"}, nil); err == nil {
		t.Error("Import mit ungültiger public_url angenommen")
	}
}

// hub node add und token: ohne public_url das Token wie bisher, mit ihr (oder
// --address) genau einmal der Code und kein Token; --no-code gibt das Token.
func TestHubNodeAddCode(t *testing.T) {
	dir := isolate(t)
	cfg := setup(t, dir)
	c := "--config=" + cfg
	runT(t, "hub", "collection", "add", "team-x", c).want(t, 0)
	runT(t, "hub", "collection", "add", "privat", c).want(t, 0)

	// Ohne public_url: das Token, mit Hinweis auf den Code.
	r := runT(t, "hub", "node", "add", "alt", c)
	r.want(t, 0, "--token-stdin", "config set hub public_url")
	if codePattern.MatchString(r.out) {
		t.Errorf("Code ohne public_url:\n%s", r.out)
	}
	tokenFrom(t, r.out)

	// --address gilt ohne public_url; ohne alias der Hinweis auf --alias.
	r = runT(t, "hub", "node", "add", "laptop", "--grant", "team-x", "--grant", "privat",
		"--address", "https://hub.example/kephalaion/hub", c)
	r.want(t, 0, "Node laptop angelegt.", "team-x erlaubt", "privat erlaubt", "node setup <code>", "--alias")
	_, code := codeFrom(t, r.out)
	if code.Address != "https://hub.example/kephalaion/hub" || code.Alias != "" || code.Node != "laptop" {
		t.Errorf("Code: %+v", code)
	}
	if !nodeHashOK(t, cfg, "laptop", code.Token) {
		t.Error("das Token im Code ist nicht das des Nodes")
	}
	n, err := hubStore(t, cfg).Node(context.Background(), "laptop")
	if err != nil || strings.Join(n.Collections, ",") != "privat,team-x" {
		t.Errorf("Collections %v, %v", n.Collections, err)
	}

	runT(t, "config", "set", "hub", "public_url", "https://vm.example/kephalaion/hub", c).want(t, 0)
	runT(t, "config", "set", "hub", "alias", "vm", c).want(t, 0)
	r = runT(t, "hub", "node", "add", "mac", c)
	r.want(t, 0, "Adresse https://vm.example/kephalaion/hub, Alias vm, Node mac")
	_, code = codeFrom(t, r.out)
	if code.Alias != "vm" || code.Address != "https://vm.example/kephalaion/hub" || !nodeHashOK(t, cfg, "mac", code.Token) {
		t.Errorf("Code: %+v", code)
	}

	// hub node token: ein neuer Code, der alte gilt nicht mehr.
	r = runT(t, "hub", "node", "token", "mac", c)
	r.want(t, 0, "das alte gilt nicht mehr", "node setup <code>")
	_, code2 := codeFrom(t, r.out)
	if code2.Token == code.Token || !nodeHashOK(t, cfg, "mac", code2.Token) {
		t.Error("hub node token: kein neues Token im Code")
	}

	// --no-code: das Token wie bisher, kein Code.
	r = runT(t, "hub", "node", "token", "mac", "--no-code", c)
	r.want(t, 0, "node hub token <alias> --token-stdin")
	if codePattern.MatchString(r.out) {
		t.Errorf("Code trotz --no-code:\n%s", r.out)
	}
	if !nodeHashOK(t, cfg, "mac", tokenFrom(t, r.out)) {
		t.Error("--no-code: Token nicht das des Nodes")
	}
	runT(t, "hub", "node", "add", "x1", "--no-code", "--address", "https://h/hub", c).want(t, 1, "--no-code")

	// Vorher geprüft: eine fehlende Collection, eine doppelte, eine falsche
	// Adresse — der Node entsteht nicht.
	runT(t, "hub", "node", "add", "neu", "--grant", "fehlt", c).want(t, 1, "--grant", "gibt es nicht")
	runT(t, "hub", "node", "add", "neu", "--grant", "team-x", "--grant", "team-x", c).want(t, 1, "zweimal")
	runT(t, "hub", "node", "add", "neu", "--address", "http://hub.example/hub", c).want(t, 1, "--address")
	runT(t, "hub", "node", "show", "neu", c).want(t, 1, "gibt es nicht")
	if _, err := hubStore(t, cfg).Node(context.Background(), "neu"); err == nil {
		t.Error("Node neu trotz Fehler angelegt")
	}
	_ = hubstore.SettingAlias
}
