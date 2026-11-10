package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/kephalaion/kephalaion/internal/assistant"
	"github.com/kephalaion/kephalaion/internal/ident"
	"github.com/kephalaion/kephalaion/internal/node/mcpnode"
)

// node account default (Task 037): der Standard-Account je Hub in
// tokens/<hub>/default, den alle Clients dieses Rechners lesen — nach der
// eigenen Überschreibung, vor der einzigen Token-Datei.

func runNodeAccountDefault(args []string, stdout, stderr io.Writer) int {
	c := newCommand("node account default", nodeAccountUsage, stdout, stderr, "<hub>")
	c.optional = 1
	unset := c.fs.Bool("unset", false, "")
	pos, code, ok := c.parse(args)
	if !ok {
		return code
	}
	hub := pos[0]
	if err := ident.CheckName("Hub", hub); err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", c.name, err)
		return 2
	}
	account := ""
	if len(pos) == 2 {
		account = pos[1]
		if err := ident.CheckPrincipalName("Account", account); err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", c.name, err)
			return 2
		}
	}
	if *unset && account != "" {
		fmt.Fprintf(stderr, "%s: --unset entfernt den Standard; ohne <account>\n", c.name)
		return 2
	}
	dir, err := assistant.TokensDir()
	if err != nil {
		return c.fail(err)
	}
	switch {
	case *unset:
		removed, err := assistant.RemoveDefault(dir, hub)
		if err != nil {
			return c.fail(err)
		}
		if removed {
			fmt.Fprintf(stdout, "Standard für Hub %s entfernt.\n", hub)
		} else {
			fmt.Fprintf(stdout, "Hub %s hatte keinen Standard.\n", hub)
		}
		printResolution(stdout, assistant.Resolve(dir, hub, ""))
		return 0
	case account == "":
		def, err := assistant.ReadDefault(dir, hub)
		switch {
		case err != nil:
			fmt.Fprintf(stdout, "Standard für Hub %s: nicht lesbar\n", hub)
		case def == "":
			fmt.Fprintf(stdout, "Standard für Hub %s: keiner\n", hub)
		default:
			fmt.Fprintf(stdout, "Standard für Hub %s: %s\n", hub, def)
		}
		printResolution(stdout, assistant.Resolve(dir, hub, ""))
		return 0
	}
	return setDefault(c, dir, hub, account)
}

// printResolution zeigt, welcher Account an einem Hub ohne Überschreibung
// gilt, und die Hinweise dazu.
func printResolution(w io.Writer, r assistant.Resolution) {
	fmt.Fprintf(w, "Token-Dateien: %s\n", joinOrNone(r.Candidates))
	switch r.How {
	case assistant.ByDefault:
		fmt.Fprintf(w, "Es gilt: %s (Standard)\n", r.Account)
	case assistant.ByOnly:
		fmt.Fprintf(w, "Es gilt: %s (einzige Token-Datei)\n", r.Account)
	default:
		if len(r.Candidates) > 1 {
			fmt.Fprintf(w, "Es gilt: keiner — Wahl fehlt (%s); setzen: kephalaion node account default %s <account>\n",
				strings.Join(r.Candidates, ", "), r.Hub)
		} else {
			fmt.Fprintln(w, "Es gilt: keiner — keine Token-Datei")
		}
	}
	for _, n := range r.Notes {
		fmt.Fprintf(w, "Hinweis: %s\n", n)
	}
}

// setDefault setzt den Standard eines Hubs und zieht die Einträge der
// Assistenten nach: mit eigenem Node wie node mcp add --auto, ein
// OpenCode-Eintrag mit dem bisherigen Standard folgt dem neuen. Danach nennt
// es jeden Eintrag, dessen ausdrückliche Wahl vom neuen Standard abweicht.
func setDefault(c *command, dir, hub, account string) int {
	file := assistant.TokenFile(dir, hub, account)
	if ok, err := fileExists(file); err != nil {
		return c.fail(err)
	} else if !ok {
		prev := assistant.Resolve(dir, hub, "")
		return c.fail(fmt.Errorf("%s hat keine Token-Datei an diesem Hub (%s); der Standard braucht eine. Token-Dateien: %s. "+
			"Einen Account einrichten: kephalaion node account setup %s %s <einrichtungstoken>", account, file,
			joinOrNone(prev.Candidates), hub, account))
	}
	prev := assistant.Resolve(dir, hub, "")
	if err := assistant.WriteDefault(dir, hub, account); err != nil {
		return c.fail(err)
	}
	switch {
	case prev.How == assistant.ByDefault && prev.Account == account:
		fmt.Fprintf(c.stdout, "Standard für Hub %s: %s (unverändert).\n", hub, account)
	case prev.How == assistant.ByDefault:
		fmt.Fprintf(c.stdout, "Standard für Hub %s: %s (bisher %s).\n", hub, account, prev.Account)
	case prev.How == assistant.ByOnly:
		fmt.Fprintf(c.stdout, "Standard für Hub %s: %s (bisher keiner; es galt die einzige Token-Datei %s).\n", hub,
			account, prev.Account)
	default:
		fmt.Fprintf(c.stdout, "Standard für Hub %s: %s (bisher keiner).\n", hub, account)
	}
	follow := assistant.Choice{}
	if prev.How == assistant.ByDefault || prev.How == assistant.ByOnly {
		follow[hub] = prev.Account
	}
	ctx := context.Background()
	// Ohne eigenen Node kein add --auto: Der Helfer von Claude Code und Codex
	// liest den Standard bei jeder Verbindung; OpenCode bekommt den Weg.
	target, err := assistantTarget(*c.cfg)
	noNode := errors.Is(err, errNoNode)
	switch {
	case noNode:
		fmt.Fprintln(c.stdout, "Kein Node in der config: Eingetragen wird nichts. Claude Code und Codex folgen dem Standard "+
			"ohne node mcp add — ihr Helfer liest ihn bei jeder Verbindung, außer bei einer ausdrücklichen Wahl.")
	case err != nil:
		fmt.Fprintf(c.stderr, "%s: Warnung: bei den KI-Assistenten nicht nachgezogen: %v\n", c.name, err)
	default:
		if registerAssistants(ctx, c.name, target, assistant.AddOptions{Auto: true, Follow: follow}, c.stdout,
			c.stderr) != 0 {
			fmt.Fprintf(c.stderr, "%s: Warnung: Die Anmeldung bei den KI-Assistenten ist nicht vollständig "+
				"(kephalaion node mcp status); der Standard ist gesetzt.\n", c.name)
		}
	}
	for _, e := range newAssistantManager().Entries(ctx) {
		if e.Err != nil || !e.Exists {
			continue
		}
		have := e.Choice[hub]
		if have == "" || have == account {
			continue
		}
		switch {
		case e.Assistant == assistant.OpenCode && noNode:
			base := strings.TrimSuffix(e.URL, mcpnode.Path)
			if base == "" {
				base = "…"
			}
			fmt.Fprintf(c.stdout, "opencode: trägt %s=%s fest im Eintrag, nicht den Standard %s; nachziehen: kephalaion node "+
				"mcp add --node %s --assistant opencode --account %s=%s\n", hub, have, account, base, hub, account)
		case e.Assistant == assistant.OpenCode:
			fmt.Fprintf(c.stdout, "opencode: ausdrückliche Wahl %s=%s im Eintrag bleibt, nicht der Standard %s; ändern: "+
				"kephalaion node mcp add --assistant opencode --account %s=%s\n", hub, have, account, hub, account)
		default:
			fmt.Fprintf(c.stdout, "%s: ausdrückliche Wahl %s=%s im Eintrag bleibt, nicht der Standard %s; dem Standard "+
				"folgen: kephalaion node mcp remove --assistant %s, danach kephalaion node mcp add --assistant %s\n",
				e.Assistant, hub, have, account, e.Assistant, e.Assistant)
		}
	}
	return 0
}
