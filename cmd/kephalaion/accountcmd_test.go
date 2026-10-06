package main

import (
	"strings"
	"testing"

	"github.com/kephalaion/kephalaion/internal/ident"
)

func TestHubAccountFlow(t *testing.T) {
	dir := isolate(t)
	cfg := setup(t, dir)
	c := "--config=" + cfg
	runT(t, "hub", "collection", "add", "team-x", c).want(t, 0)
	runT(t, "hub", "collection", "add", "privat", c).want(t, 0)
	runT(t, "hub", "node", "add", "laptop", c).want(t, 0)

	r := runT(t, "hub", "account", "add", "bob", "--description", "Bob", c)
	r.want(t, 0, "Account bob angelegt (User bob)", "wird nicht wieder angezeigt", "node account setup <hub> bob <einrichtungstoken>", "node account rotate <hub> bob")
	tok := tokenFrom(t, r.out)
	runT(t, "hub", "account", "add", "bob", c).want(t, 1, "gibt es schon")
	runT(t, "hub", "account", "add", "admin", c).want(t, 1, "reserviert")
	runT(t, "hub", "account", "add", "laptop", c).want(t, 1, "an einen Node vergeben")
	runT(t, "hub", "node", "add", "bob", c).want(t, 1, "an einen Account vergeben")
	runT(t, "hub", "node", "add", "admin", c).want(t, 1, "reserviert")

	runT(t, "hub", "account", "grant", "bob", "team-x", "--write", c).want(t, 0, "team-x erlaubt (read, write)")
	runT(t, "hub", "account", "grant", "bob", "team-x", "--write", c).want(t, 0, "unverändert")
	runT(t, "hub", "account", "grant", "bob", "privat", c).want(t, 0, "privat erlaubt (read)")
	runT(t, "hub", "account", "grant", "bob", "fehlt", c).want(t, 1, "Collection fehlt gibt es nicht")
	runT(t, "hub", "account", "grant", "bob", c).want(t, 2, "Es fehlt: <collection>")
	runT(t, "hub", "account", "list", c).want(t, 0, "bob", "aktiv", "privat (read), team-x (read, write)", "Bob")
	r = runT(t, "hub", "account", "show", "bob", c)
	r.want(t, 0, "Account bob", "Rechte:", "team-x: read, write", "nur als Hash", "von admin")
	if strings.Contains(r.out, tok) || strings.Contains(r.out, ident.HashToken(tok)) {
		t.Error("show zeigt Token oder Hash")
	}
	// grant ohne --write entzieht write.
	runT(t, "hub", "account", "grant", "bob", "team-x", "--supersede", c).want(t, 0, "(read, supersede)")

	runT(t, "hub", "account", "lock", "bob", c).want(t, 0, "gesperrt")
	runT(t, "hub", "account", "lock", "bob", c).want(t, 1, "schon gesperrt")
	runT(t, "hub", "account", "show", "bob", c).want(t, 0, "gesperrt", "gemerkt", "team-x: read, supersede")
	runT(t, "status", c).want(t, 0, "Accounts:      bob (gesperrt)")
	runT(t, "hub", "account", "unlock", "bob", c).want(t, 0, "entsperrt")
	runT(t, "hub", "account", "revoke", "bob", "privat", c).want(t, 0, "privat nicht mehr erlaubt")
	runT(t, "hub", "account", "revoke", "bob", "privat", c).want(t, 1, "keine Rechte")
	r = runT(t, "hub", "account", "token", "bob", c)
	r.want(t, 0, "neues Einrichtungstoken")
	if tokenFrom(t, r.out) == tok {
		t.Error("token liefert das alte Token")
	}
	runT(t, "hub", "account", "set", "bob", "--description", "Robert", c).want(t, 0, "geändert")
	runT(t, "hub", "account", "set", "bob", c).want(t, 1, "nichts zu ändern")
	runT(t, "hub", "account", "rm", "bob", c).want(t, 0, "entfernt")
	runT(t, "hub", "account", "list", c).want(t, 0, "Keine Accounts.")
	// Der Name ist wieder frei.
	runT(t, "hub", "node", "add", "bob", c).want(t, 0)
	runT(t, "hub", "account", "--help").want(t, 0, "--supersede")
}

// hub account add|set|list|show mit --user: ohne Angabe der Name des
// Accounts, admin und ungültige Namen abgewiesen, ein User darf wie ein Node
// heißen; list --user filtert.
func TestHubAccountUser(t *testing.T) {
	dir := isolate(t)
	cfg := setup(t, dir)
	c := "--config=" + cfg
	runT(t, "hub", "collection", "add", "team-x", c).want(t, 0)
	runT(t, "hub", "node", "add", "laptop", c).want(t, 0)

	runT(t, "hub", "account", "add", "bob", c).want(t, 0, "(User bob)")
	runT(t, "hub", "account", "add", "desk", "--user", "kleist", c).want(t, 0, "Account desk angelegt (User kleist)")
	runT(t, "hub", "account", "add", "vm", "--user", "laptop", c).want(t, 0, "(User laptop)")
	runT(t, "hub", "account", "add", "x1", "--user", "admin", c).want(t, 1, "User", "reserviert")
	runT(t, "hub", "account", "add", "x2", "--user", "Kleist", c).want(t, 1, "User", "ungültiger Name")
	runT(t, "hub", "account", "add", "x3", "--user", "", c).want(t, 1, "User: Name fehlt")
	runT(t, "hub", "account", "list", c).want(t, 0, "USER", "desk  kleist", "vm    laptop")

	runT(t, "hub", "account", "show", "bob", c).want(t, 0, "User:         bob")
	runT(t, "hub", "account", "grant", "bob", "team-x", c).want(t, 0)
	runT(t, "hub", "account", "set", "bob", "--user", "kleist", c).want(t, 0, "Account bob geändert")
	runT(t, "hub", "account", "set", "bob", "--user", "admin", c).want(t, 1, "reserviert")
	runT(t, "hub", "account", "show", "bob", c).want(t, 0, "User:         kleist")

	r := runT(t, "hub", "account", "list", "--user", "kleist", c)
	r.want(t, 0, "bob", "desk")
	if strings.Contains(r.out, "vm") {
		t.Errorf("list --user kleist zeigt vm:\n%s", r.out)
	}
	runT(t, "hub", "account", "list", "--user", "niemand", c).want(t, 0, "Keine Accounts des Users niemand.")
	runT(t, "hub", "account", "list", "--user", "Falsch", c).want(t, 1, "ungültiger Name")
}
