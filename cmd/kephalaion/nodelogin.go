package main

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/kephalaion/kephalaion/internal/assistant"
	"github.com/kephalaion/kephalaion/internal/config"
	"github.com/kephalaion/kephalaion/internal/ident"
)

// nodeLogin sind die Optionen, mit denen ein Kommando als Client des Nodes
// über MCP geht — node dir push|pull und node sync (Task 042) —, und was
// daraus folgt: die Adresse des Nodes, Account und Token. Eine Stelle für
// beide Kommandos.
type nodeLogin struct {
	c          *command
	stdin      io.Reader
	node       *string
	caFile     *string
	account    *string
	tokenFile  *string
	tokenStdin *bool
}

// addNodeLogin meldet die Optionen der Anmeldung an c an: --node, --ca-file,
// --account, --token-file und --token-stdin.
func addNodeLogin(c *command, stdin io.Reader) *nodeLogin {
	return &nodeLogin{c: c, stdin: stdin,
		node:       c.fs.String("node", "", ""),
		caFile:     c.fs.String("ca-file", "", ""),
		account:    c.fs.String("account", "", ""),
		tokenFile:  c.fs.String("token-file", "", ""),
		tokenStdin: c.fs.Bool("token-stdin", false, ""),
	}
}

// check prüft, was sich an den Optionen ohne Verbindung prüfen lässt:
// --token-file und --token-stdin schließen sich aus und gelten nur mit
// --account; der Account folgt der Namensregel. Ein Fehler ist ein falscher
// Aufruf.
func (l *nodeLogin) check() error {
	if *l.tokenFile != "" && *l.tokenStdin {
		return errors.New("--token-file und --token-stdin schließen sich aus; das Token wird nie als Argument übergeben")
	}
	if (*l.tokenFile != "" || *l.tokenStdin) && *l.account == "" {
		return errors.New("--account fehlt: mit --token-file oder --token-stdin ist der Account anzugeben")
	}
	if *l.account != "" {
		if err := ident.CheckPrincipalName("Account", *l.account); err != nil {
			return err
		}
	}
	return nil
}

// given nennt die Optionen der Anmeldung, die der Aufruf trägt — für ein
// Kommando, das sie auf einem seiner Wege nicht nimmt.
func (l *nodeLogin) given() []string {
	var out []string
	for _, name := range []string{"node", "ca-file", "account", "token-file", "token-stdin"} {
		if l.c.isSet(name) {
			out = append(out, "--"+name)
		}
	}
	return out
}

// address ist die Adresse des Nodes: --node, sonst listen im Abschnitt node:
// der config.
func (l *nodeLogin) address() (nodeAddress, error) {
	if *l.node != "" {
		return parseNodeAddress(*l.node)
	}
	if *l.caFile != "" {
		return nodeAddress{}, errors.New("--ca-file nur zusammen mit --node https://…")
	}
	loc, err := config.Locate(*l.c.cfg)
	if err != nil {
		return nodeAddress{}, err
	}
	cfg, _, err := config.Load(loc.Path)
	if err != nil {
		return nodeAddress{}, err
	}
	listen := cfg.Listen(config.Node)
	if listen == "" {
		return nodeAddress{}, fmt.Errorf("kein Node in der config %s; Adresse mit --node <url> angeben", loc.Path)
	}
	return localNodeAddress(listen), nil
}

// credentials liefert Account und Token für einen Hub: --token-file oder
// --token-stdin mit --account, sonst die Token-Datei unter tokens/<hub>/ nach
// der Reihenfolge der Wahl (Task 037): --account, der Standard, die einzige.
// Fehlt die Wahl, bricht es mit den Kandidaten ab.
func (l *nodeLogin) credentials(hub string) (account, token string, err error) {
	switch {
	case *l.tokenFile != "":
		token, err = readTokenFile(*l.tokenFile)
		return *l.account, token, err
	case *l.tokenStdin:
		token, err = readToken(l.stdin)
		return *l.account, token, err
	}
	// Dieselben Token-Dateien und dieselbe Wahl wie node mcp
	// (internal/assistant).
	base, err := assistant.TokensDir()
	if err != nil {
		return "", "", err
	}
	dir := filepath.Join(base, hub)
	r := assistant.Resolve(base, hub, *l.account)
	for _, n := range r.Notes {
		fmt.Fprintf(l.c.stderr, "%s: Hinweis: Hub %s: %s\n", l.c.name, hub, n)
	}
	switch {
	case r.How == assistant.ByOverride && r.File == "":
		// Ohne Rückfall: Die Meldung nennt die fehlende Datei.
		return "", "", fmt.Errorf("Token-Datei %s fehlt: %s hat keine Token-Datei an diesem Hub",
			assistant.TokenFile(base, hub, r.Account), r.Account)
	case r.File != "":
		token, err = readTokenFile(r.File)
		return r.Account, token, err
	case len(r.Candidates) > 1:
		return "", "", fmt.Errorf("Wahl fehlt: mehrere Token-Dateien unter %s: %s, kein Standard; --account wählt, "+
			"oder dauerhaft: kephalaion node account default %s <account>", dir, strings.Join(r.Candidates, ", "), hub)
	case r.Reason != "":
		return "", "", errors.New(r.Reason)
	}
	return "", "", fmt.Errorf("keine Token-Datei unter %s (<account>.token); --account mit --token-file oder --token-stdin", dir)
}
