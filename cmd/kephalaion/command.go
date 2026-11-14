package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/kephalaion/kephalaion/internal/config"
	hubstore "github.com/kephalaion/kephalaion/internal/hub/store"
	"github.com/kephalaion/kephalaion/internal/ident"
	nodestore "github.com/kephalaion/kephalaion/internal/node/store"
)

// dispatch verteilt die Kommandos einer Gruppe wie `hub node` auf ihre
// Blätter.
func dispatch(group, usage string, args []string, stdout, stderr io.Writer, cmds map[string]func(args []string) int) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	}
	fn, ok := cmds[args[0]]
	if !ok {
		fmt.Fprintf(stderr, "Unbekanntes Kommando: %s %s\n\n", group, args[0])
		fmt.Fprint(stderr, usage)
		return 2
	}
	return fn(args[1:])
}

// command ist ein Blatt-Kommando wie `hub node add`: Optionen samt --config,
// Pflicht-Positionsargumente, Fehlerausgabe.
type command struct {
	name  string
	usage string
	args  []string
	// optional zählt die Positionsargumente, die nach args folgen dürfen.
	optional int
	stdout   io.Writer
	stderr   io.Writer
	fs       *flag.FlagSet
	cfg      *string
}

// newCommand legt ein Blatt-Kommando an. args nennt die Positionsargumente,
// die es verlangt, für die Meldung, wenn eines fehlt.
func newCommand(name, usage string, stdout, stderr io.Writer, args ...string) *command {
	c := &command{name: name, usage: usage, args: args, stdout: stdout, stderr: stderr}
	c.fs = newFlagSet(name, usage, stderr)
	c.cfg = c.fs.String("config", "", "")
	return c
}

// parse wertet Optionen und Positionsargumente aus.
func (c *command) parse(args []string) (pos []string, code int, ok bool) {
	if tokenAsArgument(args) {
		fmt.Fprintf(c.stderr, "%s: das Token wird nie als Argument übergeben — es stünde sonst im "+
			"Shell-Verlauf und in der Prozessliste; --token-stdin liest es von der Standardeingabe\n", c.name)
		return nil, 2, false
	}
	pos, code, ok = parseFlags(c.fs, args, c.usage, len(c.args)+c.optional, c.stderr)
	if !ok {
		return nil, code, false
	}
	if len(pos) < len(c.args) {
		fmt.Fprintf(c.stderr, "Es fehlt: %s\n\n", c.args[len(pos)])
		fmt.Fprint(c.stderr, c.usage)
		return nil, 2, false
	}
	return pos, 0, true
}

// tokenAsArgument erkennt den naheliegenden Fehlgriff --token statt
// --token-stdin, damit er eine eigene Meldung bekommt statt „flag provided but
// not defined“. Nach -- ist alles Positionsargument.
func tokenAsArgument(args []string) bool {
	for _, a := range args {
		if a == "--" {
			return false
		}
		name, _, _ := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if strings.HasPrefix(a, "-") && name == "token" {
			return true
		}
	}
	return false
}

// isSet sagt, ob eine Option angegeben wurde, auch mit leerem Wert.
func (c *command) isSet(name string) bool {
	set := false
	c.fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			set = true
		}
	})
	return set
}

// fail meldet einen Fehler und liefert Exit-Code 1. errReported hat seine
// Meldung schon selbst geschrieben.
func (c *command) fail(err error) int {
	if !errors.Is(err, errReported) {
		fmt.Fprintf(c.stderr, "%s: %v\n", c.name, err)
	}
	return 1
}

func (c *command) loadConfig() (config.Config, error) {
	path, err := config.Path(*c.cfg)
	if err != nil {
		return config.Config{}, err
	}
	cfg, _, err := config.Load(path)
	return cfg, err
}

// openHub öffnet die Datenbank des Hubs aus der config.
func (c *command) openHub(ctx context.Context) (hubstore.Store, error) {
	cfg, err := c.loadConfig()
	if err != nil {
		return nil, err
	}
	return openHubOf(ctx, cfg)
}

// openHubOf öffnet die Datenbank des Hubs aus einer geladenen config.
func openHubOf(ctx context.Context, cfg config.Config) (hubstore.Store, error) {
	addr, err := hubDBOf(cfg)
	if err != nil {
		return nil, err
	}
	return hubstore.Open(ctx, addr)
}

// openNode öffnet die Datenbank des Nodes aus der config. hubInConfig sagt,
// ob in derselben config ein Hub eingerichtet ist — die Voraussetzung für
// Transport local. Der Hub selbst wird dafür nicht geöffnet.
func (c *command) openNode(ctx context.Context) (s nodestore.Store, hubInConfig bool, err error) {
	cfg, err := c.loadConfig()
	if err != nil {
		return nil, false, err
	}
	s, err = openNodeOf(ctx, cfg)
	if err != nil {
		return nil, false, err
	}
	return s, cfg.Section(config.Hub) != nil, nil
}

// openNodeOf öffnet die Datenbank des Nodes aus einer geladenen config.
func openNodeOf(ctx context.Context, cfg config.Config) (nodestore.Store, error) {
	sec := cfg.Section(config.Node)
	if sec == nil {
		return nil, errors.New("der Node ist nicht eingerichtet; zuerst: kephalaion node init")
	}
	addr, err := config.ParseDB(sec.DB)
	if err != nil {
		return nil, err
	}
	return nodestore.Open(ctx, addr)
}

// readToken liest ein Token als eine Zeile von stdin und prüft sein Format.
// Ein Token wird nie als Argument übergeben: Es stünde sonst im
// Shell-Verlauf und in der Prozessliste.
func readToken(stdin io.Reader) (string, error) {
	line, err := bufio.NewReader(stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("Token von stdin: %w", err)
	}
	token := strings.TrimSpace(line)
	if token == "" {
		return "", errors.New("kein Token auf stdin (eine Zeile keph_…)")
	}
	if err := ident.CheckToken(token); err != nil {
		return "", err
	}
	return token, nil
}

// orDash liefert „–“ für einen leeren Text.
func orDash(s string) string {
	if s == "" {
		return "–"
	}
	return s
}

// joinOrNone liefert die Liste mit Komma oder „keine“.
func joinOrNone(list []string) string {
	if len(list) == 0 {
		return "keine"
	}
	return strings.Join(list, ", ")
}
