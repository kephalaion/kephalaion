package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"slices"
	"sort"
	"strings"

	"github.com/kephalaion/kephalaion/internal/assistant"
	"github.com/kephalaion/kephalaion/internal/buildinfo"
	"github.com/kephalaion/kephalaion/internal/config"
	"github.com/kephalaion/kephalaion/internal/ident"
	"github.com/kephalaion/kephalaion/internal/node/mcpnode"
)

// node client (Task 037): die Auskunft für Clients außerhalb dieses Repos
// (etwa k-playbook) — welche config, welcher lokale Node, welcher Account je
// Hub auf diesem Rechner gilt. Sie öffnet keine Datenbank und schickt keine
// Anfrage mit Token; die Probe des Nodes geht ohne Token.

const nodeClientUsage = `Aufruf:
  kephalaion node client [--json] [--format <n>] [--no-probe] [--config pfad]
                         [--hub <alias>]… [--account <hub>=<account>]…

Die Auskunft für Clients: welche config gilt, wo der Node dieses Rechners
lauscht und welcher Account je Hub gilt — dieselbe Suche und dieselbe
Reihenfolge der Wahl wie in der CLI selbst. Clients außerhalb dieses Repos
(etwa k-playbook) fragen sie, statt die Suche nachzubauen. Sie öffnet keine
Datenbank und schickt keine Anfrage mit Token; kein Token steht in der
Ausgabe, nur Pfade der Token-Dateien.

  config   gesucht wie überall: --config, KEPHALAION_CONFIG, die config des
           Users, die globale. Fehlt sie, sagt die Auskunft das und nennt die
           Standardadresse des Nodes, statt abzubrechen; ist sie nicht lesbar
           oder ungültig, ebenso, mit dem Grund (ohne Inhalt der Datei).
  Node     immer der lokale Node dieses Rechners: listen im Abschnitt node:
           der config, sonst die Standardadresse 127.0.0.1:7433; 0.0.0.0 wird
           127.0.0.1, [::] wird [::1]. Erreichbar: eine Probe ohne Token,
           Frist 3 s, wie bei kephalaion status; --no-probe lässt sie aus.
           Einen entfernten Node beschreibt die Auskunft nicht (es gibt kein
           --node): Auf einem Client ohne eigenen Node (Einträge mit --node
           https://…) nennt sie die Standardadresse, in der Regel nicht
           erreichbar.
  Hubs     je Hub mit Verzeichnis unter tokens/, dazu je Hub aus --account;
           mit --hub genau die genannten, auch ohne Verzeichnis. Der Account
           je Hub: (1) --account <hub>=<account> (die Überschreibung des
           Clients, ohne Rückfall, auch ohne Token-Datei), (2) der
           Standard-Account (kephalaion node account default), (3) die
           einzige Token-Datei; sonst fehlt die Wahl, mit den Kandidaten.

Mit --json steht auf stdout genau ein JSON-Objekt, alles andere geht auf
stderr. Format 1:
  {"format": 1, "kephalaion": "<version>",
   "config": {"path": "<pfad>"|null, "source": "flag"|"env"|"user"|"system"|"none",
              "exists": true|false, "problem": "<grund>"|null},
   "node": {"listen": "<host:port>"|null, "url": "http://127.0.0.1:7433/mcp",
            "source": "config"|"default", "reachable": true|false|null,
            "version": "<version>"|null},
   "tokensDir": "<pfad>",
   "hubs": [{"alias": "<hub>", "account": "<account>"|null,
             "choice": "override"|"default"|"only"|"missing",
             "tokenFile": "<pfad>"|null, "candidates": ["…"], "pending": ["…"],
             "notes": ["…"]}]}
  source none und path null nur, wenn nichts angegeben ist und weder die
  config des Users noch die globale existiert. candidates sind die Accounts
  mit Token-Datei, pending die mit wartendem Token (.pending; allein kein
  Kandidat). notes sind nur zum Anzeigen; entschieden wird nach choice.

Fassungen: Ohne --format kommt die neueste. Eine höhere Nummer heißt: ein
Feld wurde umbenannt oder entfernt, oder eine Bedeutung hat sich geändert;
ein neues Feld erhöht sie nicht, Clients übergehen unbekannte Felder. Clients
prüfen genau und rufen mit --format <n> auf. Dieses Binary trägt: %s.

Exit-Codes:
  0   die Auskunft ist entstanden — auch ohne config, mit nicht lesbarer
      config, mit einem Node, der nicht antwortet, und wenn die Wahl fehlt
  1   die Auskunft konnte nicht entstehen (Heimatverzeichnis unbekannt)
  2   falscher Aufruf, auch ein --format, das dieses Binary nicht trägt

Optionen:
  --json                     als JSON (siehe oben)
  --format <n>               diese Fassung des JSON
  --no-probe                 den Node nicht fragen (reachable und version null)
  --config pfad              Ort der config (siehe kephalaion node init --help)
  --hub <alias>              nur diese Hubs; wiederholbar
  --account <hub>=<account>  die Überschreibung des Clients; wiederholbar
`

// clientFormats sind die Fassungen der Auskunft, die dieses Binary trägt;
// die letzte ist die neueste.
var clientFormats = []int{1}

// clientInfo ist die Auskunft, Format 1.
type clientInfo struct {
	Format     int          `json:"format"`
	Kephalaion string       `json:"kephalaion"`
	Config     clientConfig `json:"config"`
	Node       clientNode   `json:"node"`
	TokensDir  string       `json:"tokensDir"`
	Hubs       []clientHub  `json:"hubs"`
}

type clientConfig struct {
	Path    *string `json:"path"`
	Source  string  `json:"source"`
	Exists  bool    `json:"exists"`
	Problem *string `json:"problem"`
}

type clientNode struct {
	Listen    *string `json:"listen"`
	URL       string  `json:"url"`
	Source    string  `json:"source"`
	Reachable *bool   `json:"reachable"`
	Version   *string `json:"version"`
}

type clientHub struct {
	Alias      string   `json:"alias"`
	Account    *string  `json:"account"`
	Choice     string   `json:"choice"`
	TokenFile  *string  `json:"tokenFile"`
	Candidates []string `json:"candidates"`
	Pending    []string `json:"pending"`
	Notes      []string `json:"notes"`
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func runNodeClient(args []string, stdout, stderr io.Writer) int {
	formats := make([]string, len(clientFormats))
	for i, f := range clientFormats {
		formats[i] = fmt.Sprint(f)
	}
	usage := fmt.Sprintf(nodeClientUsage, strings.Join(formats, ", "))
	c := newCommand("node client", usage, stdout, stderr)
	asJSON := c.fs.Bool("json", false, "")
	format := c.fs.Int("format", clientFormats[len(clientFormats)-1], "")
	noProbe := c.fs.Bool("no-probe", false, "")
	var hubFlags, accounts stringList
	c.fs.Var(&hubFlags, "hub", "")
	c.fs.Var(&accounts, "account", "")
	if _, code, ok := c.parse(args); !ok {
		return code
	}
	if !slices.Contains(clientFormats, *format) {
		fmt.Fprintf(stderr, "%s: --format %d: dieses Binary trägt die Fassungen %s\n", c.name, *format,
			strings.Join(formats, ", "))
		return 2
	}
	choice, err := assistant.ParseChoice(accounts)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", c.name, err)
		return 2
	}
	var hubs []string
	for _, h := range hubFlags {
		if err := ident.CheckName("Hub", h); err != nil {
			fmt.Fprintf(stderr, "%s: --hub %q: %v\n", c.name, h, err)
			return 2
		}
		if !slices.Contains(hubs, h) {
			hubs = append(hubs, h)
		}
	}
	if len(hubs) > 0 {
		for _, h := range choice.Hubs() {
			if !slices.Contains(hubs, h) {
				fmt.Fprintf(stderr, "%s: --account %s=%s: der Hub %s steht nicht unter --hub\n", c.name, h, choice[h], h)
				return 2
			}
		}
	}
	info, err := clientAuskunft(*c.cfg, hubs, choice, !*noProbe)
	if err != nil {
		return c.fail(err)
	}
	info.Format = *format
	if *asJSON {
		data, err := json.MarshalIndent(info, "", "  ")
		if err != nil {
			return c.fail(err)
		}
		fmt.Fprintf(stdout, "%s\n", data)
		return 0
	}
	printClientInfo(stdout, info)
	return 0
}

// clientAuskunft stellt die Auskunft zusammen: config, lokaler Node (mit
// Probe ohne Token, wenn probe), tokens/ und je Hub die Wahl.
func clientAuskunft(cfgFlag string, hubs []string, choice assistant.Choice, probe bool) (clientInfo, error) {
	info := clientInfo{Kephalaion: buildinfo.Get().Version, Hubs: []clientHub{}}
	tokens, err := assistant.TokensDir()
	if err != nil {
		return info, err
	}
	info.TokensDir = tokens

	// config: dieselbe Suche wie überall.
	var cfg config.Config
	loc, err := config.Locate(cfgFlag)
	if err != nil {
		return info, err
	}
	switch {
	case !loc.Explicit() && !loc.UserExists && !loc.SystemExists:
		info.Config.Source = "none"
	default:
		info.Config.Path = strPtr(loc.Path)
		info.Config.Source = string(loc.Source)
		cfg, info.Config.Exists, info.Config.Problem = readClientConfig(loc.Path)
	}

	// Node: immer der lokale.
	listen := config.DefaultListenNode
	info.Node.Source = "default"
	if sec := cfg.Section(config.Node); sec != nil && sec.Listen != "" {
		listen = sec.Listen
		info.Node.Listen = strPtr(sec.Listen)
		info.Node.Source = "config"
	}
	hostport := loopbackListen(listen)
	info.Node.URL = "http://" + hostport + mcpnode.Path
	if probe {
		nodeInfo, err := probeNodeWithin(context.Background(), localNodeAddress(hostport), nil, "", statusProbeTimeout)
		ok := err == nil
		info.Node.Reachable = &ok
		if ok {
			info.Node.Version = strPtr(nodeInfo.Version)
		}
	}

	// Hubs: mit --hub genau diese, sonst die Verzeichnisse unter tokens/ und
	// die aus --account.
	if len(hubs) == 0 {
		dirs, err := assistant.Hubs(tokens)
		if err != nil {
			return info, err
		}
		hubs = dirs
		for _, h := range choice.Hubs() {
			if !slices.Contains(hubs, h) {
				hubs = append(hubs, h)
			}
		}
	}
	sort.Strings(hubs)
	for _, hub := range hubs {
		r := assistant.Resolve(tokens, hub, choice[hub])
		h := clientHub{Alias: hub, Account: strPtr(r.Account), Choice: string(r.How), TokenFile: strPtr(r.File),
			Candidates: r.Candidates, Pending: r.Pending, Notes: append([]string{}, r.Notes...)}
		switch {
		case r.Reason != "":
			h.Notes = append(h.Notes, r.Reason)
		case r.How == assistant.Missing && len(r.Candidates) == 0:
			h.Notes = append(h.Notes, "keine Token-Datei unter "+assistant.TokenFile(tokens, hub, "<account>"))
		}
		info.Hubs = append(info.Hubs, h)
	}
	return info, nil
}

// readClientConfig liest die config für die Auskunft: ob es sie gibt und,
// wenn sie nicht lesbar oder ungültig ist, den Grund — ohne Inhalt der
// Datei, auch nicht aus einer Meldung des YAML-Lesers.
func readClientConfig(path string) (cfg config.Config, exists bool, problem *string) {
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return config.Config{}, false, nil
	case err != nil:
		return config.Config{}, true, strPtr("nicht lesbar: " + pathErrReason(err).Error())
	}
	cfg, err = config.Parse(data)
	if err != nil {
		return config.Config{}, true, strPtr("ungültig: keine gültige config (Einzelheiten: kephalaion status)")
	}
	return cfg, true, nil
}

// loopbackListen setzt eine Wildcard in listen auf Loopback um: 0.0.0.0 wird
// 127.0.0.1, [::] wird [::1]. Eine Adresse ohne Host meint diesen Rechner.
func loopbackListen(listen string) string {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return listen
	}
	switch host {
	case "", "0.0.0.0":
		host = "127.0.0.1"
	case "::":
		host = "::1"
	}
	return net.JoinHostPort(host, port)
}

// printClientInfo gibt die Auskunft lesbar aus.
func printClientInfo(w io.Writer, info clientInfo) {
	switch {
	case info.Config.Path == nil:
		fmt.Fprintln(w, "config:    keine (weder die des Users noch die globale)")
	default:
		state := "vorhanden"
		if !info.Config.Exists {
			state = "fehlt"
		}
		fmt.Fprintf(w, "config:    %s (%s, %s)\n", *info.Config.Path, clientSourceText(info.Config.Source), state)
	}
	if info.Config.Problem != nil {
		fmt.Fprintf(w, "           %s\n", *info.Config.Problem)
	}
	from := "aus der config"
	if info.Node.Source == "default" {
		from = "Standardadresse"
	}
	state := ""
	switch {
	case info.Node.Reachable == nil:
		state = " — nicht geprüft"
	case *info.Node.Reachable && info.Node.Version != nil:
		state = " — erreichbar, Version " + *info.Node.Version
	case *info.Node.Reachable:
		state = " — erreichbar"
	default:
		state = " — nicht erreichbar"
	}
	fmt.Fprintf(w, "Node:      %s (%s)%s\n", info.Node.URL, from, state)
	fmt.Fprintf(w, "Tokens:    %s\n", info.TokensDir)
	if len(info.Hubs) == 0 {
		fmt.Fprintln(w, "Hubs:      keine")
	}
	for _, h := range info.Hubs {
		line := fmt.Sprintf("Hub %s: ", h.Alias)
		switch {
		case h.Account == nil:
			line += "Wahl fehlt"
		case h.TokenFile == nil:
			line += *h.Account + " (" + clientChoiceText(h.Choice) + ", ohne Token-Datei)"
		default:
			line += *h.Account + " (" + clientChoiceText(h.Choice) + "), " + *h.TokenFile
		}
		fmt.Fprintln(w, line)
		fmt.Fprintf(w, "  Kandidaten: %s\n", joinOrNone(h.Candidates))
		if len(h.Pending) > 0 {
			fmt.Fprintf(w, "  wartend:    %s\n", strings.Join(h.Pending, ", "))
		}
		for _, n := range h.Notes {
			fmt.Fprintf(w, "  Hinweis:    %s\n", n)
		}
	}
}

func clientSourceText(s string) string {
	switch s {
	case string(config.FromFlag):
		return "--config"
	case string(config.FromEnv):
		return config.EnvConfig
	case string(config.FromUser):
		return "pro User"
	case string(config.FromSystem):
		return "global"
	}
	return s
}

func clientChoiceText(c string) string {
	switch c {
	case string(assistant.ByOverride):
		return "--account"
	case string(assistant.ByDefault):
		return "Standard"
	case string(assistant.ByOnly):
		return "einzige Token-Datei"
	}
	return c
}
