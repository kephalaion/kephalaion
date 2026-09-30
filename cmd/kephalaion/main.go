// Command kephalaion ist das eine Binary des Projekts. Unterkommandos werden
// über das erste Argument gewählt.
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/kephalaion/kephalaion/internal/buildinfo"
	"github.com/kephalaion/kephalaion/internal/config"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

const usage = `kephalaion — geteilte Wissensdatenbank mehrerer Nutzer und Projekte

Aufruf:
  kephalaion <kommando> [optionen]

Kommandos:
  help        zeigt diese Übersicht
  version     zeigt Version, Commit, Go-Version und Plattform
  upgrade     aktualisiert dieses Binary auf das neueste Release
  hub         richtet den Hub ein, pflegt seine Collections, Nodes und
              Accounts und spielt Dokumente ein (init, collection, node,
              account, doc, import)
  node        richtet den Node ein, pflegt seine Hubs und gewünschten
              Collections, tauscht Tokens von Accounts, gleicht ab, zeigt
              Dokumente der Replica, gleicht Ordner ab, meldet sich bei den
              KI-Assistenten an und zeigt, wer wer ist (init, hub,
              collection, account, sync, doc, dir, mcp, whoami)
  serve       der Dienst: lauscht je eingerichteter Rolle auf ihrem listen
              (Hub: Vertrag für Nodes, Node: MCP für Clients) und gleicht
              als Node im Hintergrund ab
  service     richtet den Dienst ein, der serve startet (install, uninstall,
              status), und gibt Units aus (unit, unit --system)
  status      zeigt, welche config gilt, welche Rollen eingerichtet sind, wo
              ihre Datenbank liegt und ob der Dienst läuft
  config      zeigt, setzt, sichert und stellt die Einstellungen wieder her
              (show, set, unset, export, import)

Hilfe zu einem Kommando: kephalaion <kommando> --help

Siehe https://github.com/kephalaion/kephalaion
`

// run verteilt auf die Unterkommandos und liefert den Exit-Code. stdin
// liest nur, wer es ausdrücklich verlangt: --token-stdin, und hub doc put
// ohne --file.
func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stdout, usage)
		return 0
	}
	switch args[0] {
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	case "version", "--version":
		printVersion(stdout)
		return 0
	case "upgrade":
		return runUpgrade(args[1:], stdout, stderr)
	case "hub":
		return runRole(config.Hub, args[1:], stdin, stdout, stderr)
	case "node":
		return runRole(config.Node, args[1:], stdin, stdout, stderr)
	case "status":
		return runStatus(args[1:], stdout, stderr)
	case "serve":
		return runServe(args[1:], stdout, stderr)
	case "service":
		return runService(args[1:], stdout, stderr)
	case "config":
		return runConfig(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "Unbekanntes Kommando: %s\n\n", args[0])
		fmt.Fprint(stderr, usage)
		return 2
	}
}

func printVersion(w io.Writer) {
	info := buildinfo.Get()
	commit := info.Commit
	if commit == "" {
		commit = "unbekannt"
	}
	fmt.Fprintf(w, "kephalaion %s\n", info.Version)
	fmt.Fprintf(w, "  Commit:    %s\n", commit)
	fmt.Fprintf(w, "  Go:        %s\n", info.GoVersion)
	fmt.Fprintf(w, "  Plattform: %s\n", info.Platform())
}
