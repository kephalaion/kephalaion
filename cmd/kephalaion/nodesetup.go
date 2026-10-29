package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/oklog/ulid/v2"

	"github.com/kephalaion/kephalaion/internal/assistant"
	"github.com/kephalaion/kephalaion/internal/config"
	"github.com/kephalaion/kephalaion/internal/contract"
	"github.com/kephalaion/kephalaion/internal/ident"
	"github.com/kephalaion/kephalaion/internal/node/replica"
	nodestore "github.com/kephalaion/kephalaion/internal/node/store"
	"github.com/kephalaion/kephalaion/internal/service"
	"github.com/kephalaion/kephalaion/internal/setupcode"
)

// node setup (Task 034): Ein Node auf einem anderen Rechner wird mit einem
// Einrichtungscode eingerichtet — config, Hub-Eintrag, node_rotate,
// Collections, Dienst, Assistenten, in einem Aufruf, wiederholbar.

const nodeSetupUsage = `Aufruf:
  kephalaion node setup <code> [--alias alias] [--ca-file pfad] [--no-service] [--config pfad]
  kephalaion node setup --code-stdin [--alias alias] [--ca-file pfad] [--no-service] [--config pfad]

Richtet mit einem Einrichtungscode (kephsetup_…, von kephalaion hub node add
bzw. hub node token am Hub) einen Node pro User ein, der den Hub abgleicht und
bei den KI-Assistenten eingetragen ist. Je Schritt eine Zeile, Tokens nur
gekürzt:

  1. Code lesen und alles prüfen, was kein Token braucht: Form und Adresse
     des Codes, Alias, --ca-file, die config und einen vorhandenen
     Hub-Eintrag. Ein Abbruch hier lässt den Code gültig.
  2. node init, falls die Rolle node fehlt. Neben der globalen config
     (/etc/kephalaion/config.yaml) bricht setup ab — nur pro User.
  3. Hub-Eintrag: Fehlt er, wird er mit whoami geprüft und erst danach
     gespeichert (ein gescheiterter Check hinterlässt nichts). Steht der
     Alias mit anderen Angaben da (Adresse, Node-Name, Transport, CA), bricht
     setup ab und überschreibt nichts.
  4. Steht er mit denselben Angaben da, entscheidet whoami: eine liegende
     .pending klären; Token im Code gleich dem gespeicherten: rotieren; sonst
     gilt das gespeicherte (schon rotiert, weiter) oder das aus dem Code (ein
     neuer Code aus hub node token: übernehmen und rotieren) — gilt keins,
     Abbruch mit dem Hinweis auf hub node token.
  5. Das Token des Nodes rotieren (node_rotate): danach ist der Code wertlos.
     Kennt der Hub den Vorgang nicht, Abbruch: erst den Hub aktualisieren.
  6. Die erlaubten Collections abonnieren (node collection add), ohne die
     fest abonnierten.
  7. Den Dienst einrichten (service install), falls noch nicht geschehen;
     --no-service übergeht das.
  8. Den Node bei den KI-Assistenten eintragen wie node mcp add --auto; ohne
     Token-Datei unter tokens/<alias>/ der Hinweis auf node account setup.
  9. Eine Übersicht: config, Hub-Eintrag, Collections, Dienst, Assistenten,
     Accounts.

Ein zweiter Aufruf setzt fort, wo der erste stehen blieb. Der Code darf als
Argument kommen, weil er sofort rotiert wird (sonst --code-stdin, eine Zeile).

Exit-Code: 0 eingerichtet, 1 Fehler, 2 falscher Aufruf.

Optionen:
  --code-stdin     den Code als eine Zeile von der Standardeingabe lesen
  --alias alias    der Alias des Hubs, wenn der Code keinen nennt (am Hub
                   nicht gesetzt: kephalaion config set hub alias <alias>)
  --ca-file pfad   nur bei https: die CA, gegen die der Node das Zertifikat
                   des Hubs prüft, statt der System-Roots (siehe node hub add)
  --no-service     keinen Dienst einrichten
  --config pfad    Ort der config (siehe kephalaion node init --help)
`

// setupNode ist ein Aufruf von node setup.
type setupNode struct {
	c         *command
	stdin     io.Reader
	argCode   string
	fromStdin bool
	alias     string
	caFile    string
	noService bool

	code setupcode.Code
	ca   string
	loc  config.Location
	cfg  config.Config
	// subscribed sind die fest abonnierten Collections aus whoami.
	subscribed []string
	// warned: ein Schritt nach dem Rotieren ging nicht ganz; der Exit-Code
	// bleibt 0, der Node ist eingerichtet.
	warned bool
}

func runNodeSetup(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	c := newCommand("node setup", nodeSetupUsage, stdout, stderr)
	c.optional = 1
	fromStdin := c.fs.Bool("code-stdin", false, "")
	alias := c.fs.String("alias", "", "")
	caFile := c.fs.String("ca-file", "", "")
	noService := c.fs.Bool("no-service", false, "")
	pos, code, ok := c.parse(args)
	if !ok {
		return code
	}
	s := &setupNode{c: c, stdin: stdin, fromStdin: *fromStdin, alias: *alias, caFile: *caFile, noService: *noService}
	if len(pos) == 1 {
		s.argCode = pos[0]
	}
	return s.run()
}

func (s *setupNode) logf(format string, a ...any) { fmt.Fprintf(s.c.stdout, format+"\n", a...) }

func (s *setupNode) usage(format string, a ...any) int {
	fmt.Fprintf(s.c.stderr, "%s: "+format+"\n", append([]any{s.c.name}, a...)...)
	return 2
}

func (s *setupNode) fail(format string, a ...any) int {
	fmt.Fprintf(s.c.stderr, "%s: "+format+"\n", append([]any{s.c.name}, a...)...)
	return 1
}

func (s *setupNode) warn(format string, a ...any) {
	s.warned = true
	fmt.Fprintf(s.c.stderr, "%s: Warnung: "+format+"\n", append([]any{s.c.name}, a...)...)
}

func (s *setupNode) run() int {
	ctx := context.Background()
	if code := s.readCode(); code != 0 {
		return code
	}
	if code := s.precheck(ctx); code != 0 {
		return code
	}
	if code := s.initNode(); code != 0 {
		return code
	}
	st, hubInConfig, err := s.c.openNode(ctx)
	if err != nil {
		return s.fail("%v", err)
	}
	defer st.Close()
	h, code := s.hubEntry(ctx, st, hubInConfig)
	if code != 0 {
		return code
	}
	if code := s.collections(ctx, st, h); code != 0 {
		return code
	}
	s.service(ctx)
	s.register(ctx)
	s.overview(ctx, st)
	return 0
}

// readCode liest den Code (Argument oder eine Zeile von stdin) und prüft ihn
// samt Alias und --ca-file — ohne das Token zu benutzen.
func (s *setupNode) readCode() int {
	if (s.argCode != "") == s.fromStdin {
		return s.usage("erwartet den Einrichtungscode als Argument oder --code-stdin, genau eines")
	}
	raw := s.argCode
	if s.fromStdin {
		line, err := bufio.NewReader(s.stdin).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return s.fail("Code von stdin: %v", err)
		}
		raw = strings.TrimSpace(line)
		if raw == "" {
			return s.usage("kein Code auf stdin (eine Zeile %s…)", setupcode.Prefix)
		}
	}
	code, err := setupcode.Decode(raw)
	if err != nil {
		return s.usage("%v", err)
	}
	s.code = code
	switch {
	case code.Alias != "" && s.alias != "" && s.alias != code.Alias:
		return s.usage("der Code nennt den Alias %s, --alias %s weicht ab — der Alias kommt vom Hub, damit er auf "+
			"allen Rechnern derselbe ist", code.Alias, s.alias)
	case code.Alias == "" && s.alias == "":
		return s.usage("der Code nennt keinen Alias (am Hub ist alias nicht gesetzt: kephalaion config set hub alias " +
			"<alias>); --alias <alias> angeben — den Namen, unter dem andere Rechner diesen Hub eintragen")
	case code.Alias == "":
		if err := ident.CheckName("Hub", s.alias); err != nil {
			return s.usage("--alias: %v", err)
		}
		s.code.Alias = s.alias
	}
	if s.caFile != "" && code.Transport() != nodestore.TransportHTTPS {
		return s.usage("--ca-file gibt es nur bei https; der Code nennt %s", code.Address)
	}
	ca, err := readCAFile(s.caFile)
	if err != nil {
		return s.fail("%v", err)
	}
	s.ca = ca
	s.logf("Einrichtungscode gelesen: Hub %s (%s %s), Node %s, Token %s", s.code.Alias, code.Transport(),
		code.Address, code.Node, ident.MaskToken(code.Token))
	return 0
}

// precheck prüft config und einen vorhandenen Hub-Eintrag, noch ohne Token.
func (s *setupNode) precheck(ctx context.Context) int {
	loc, err := config.Locate(*s.c.cfg)
	if err != nil {
		return s.fail("%v", err)
	}
	s.loc = loc
	if loc.System() || (!loc.Explicit() && loc.SystemExists) {
		return s.fail("Kephalaion ist auf diesem Rechner global eingerichtet (%s); node setup richtet nur pro User "+
			"ein. Die globale Installation richtet der Verwalter ein (docs/installation.md); der Code gilt weiter.",
			config.SystemPath)
	}
	cfg, _, err := config.Load(loc.Path)
	if err != nil {
		return s.fail("%v", err)
	}
	s.cfg = cfg
	if cfg.Section(config.Node) == nil {
		return 0
	}
	st, hubInConfig, err := s.c.openNode(ctx)
	if err != nil {
		return s.fail("%v\nNichts geändert; der Code gilt weiter.", err)
	}
	defer st.Close()
	h, err := st.Hub(ctx, s.code.Alias)
	if errors.Is(err, nodestore.ErrNotFound) {
		if err := nodestore.CheckHub(s.wanted(), hubInConfig); err != nil {
			return s.fail("%v\nNichts geändert; der Code gilt weiter.", err)
		}
		return 0
	}
	if err != nil {
		return s.fail("%v", err)
	}
	if diff := s.differences(h); len(diff) > 0 {
		return s.fail("Hub %s ist schon anders eingetragen (%s); node setup überschreibt nichts. Ansehen: kephalaion "+
			"node hub show %s — ändern mit node hub set, oder entfernen mit node hub rm und node setup erneut. Der "+
			"Code gilt weiter.", h.Name, strings.Join(diff, "; "), h.Name)
	}
	return 0
}

// wanted ist der Hub-Eintrag, wie der Code ihn beschreibt.
func (s *setupNode) wanted() nodestore.Hub {
	return nodestore.Hub{Name: s.code.Alias, NodeName: s.code.Node, Transport: s.code.Transport(),
		Address: s.code.Address, CA: s.ca, Token: s.code.Token}
}

// differences vergleicht einen vorhandenen Eintrag mit dem Code: Adresse,
// Node-Name, Transport und — nur mit --ca-file — die CA; nie das Token.
func (s *setupNode) differences(h nodestore.Hub) []string {
	want := s.wanted()
	var diff []string
	if h.Transport != want.Transport {
		diff = append(diff, fmt.Sprintf("Transport %s statt %s", h.Transport, want.Transport))
	}
	if h.Address != want.Address {
		diff = append(diff, fmt.Sprintf("Adresse %s statt %s", orDash(h.Address), want.Address))
	}
	if h.NodeName != want.NodeName {
		diff = append(diff, fmt.Sprintf("Node %s statt %s", h.NodeName, want.NodeName))
	}
	if s.caFile != "" && h.CA != want.CA {
		diff = append(diff, "eine andere CA als in --ca-file")
	}
	return diff
}

// initNode richtet die Rolle node ein, wenn sie fehlt — wie node init.
func (s *setupNode) initNode() int {
	if sec := s.cfg.Section(config.Node); sec != nil {
		s.logf("Node: schon eingerichtet (config %s, db %s)", s.loc.Path, sec.DB)
		return 0
	}
	var args []string
	if *s.c.cfg != "" {
		args = append(args, "--config", *s.c.cfg)
	}
	var out, errOut bytes.Buffer
	if code := runInit(config.Node, args, &out, &errOut); code != 0 {
		fmt.Fprint(s.c.stderr, errOut.String())
		return s.fail("node init gescheitert; nichts weiter geändert, der Code gilt weiter.")
	}
	cfg, _, err := config.Load(s.loc.Path)
	if err != nil {
		return s.fail("%v", err)
	}
	s.cfg = cfg
	sec := cfg.Section(config.Node)
	s.logf("Node eingerichtet (node init): config %s, db %s, listen %s", s.loc.Path, sec.DB, sec.Listen)
	return 0
}

// hubEntry legt den Hub-Eintrag an oder setzt einen vorhandenen fort und
// rotiert das Token, soweit nötig (Schritte 3 bis 5). Es liefert den
// Eintrag mit dem Token, das jetzt gilt.
func (s *setupNode) hubEntry(ctx context.Context, st nodestore.Store, hubInConfig bool) (nodestore.Hub, int) {
	h, err := st.Hub(ctx, s.code.Alias)
	switch {
	case errors.Is(err, nodestore.ErrNotFound):
		return s.newEntry(ctx, st, hubInConfig)
	case err != nil:
		return h, s.fail("%v", err)
	}
	if diff := s.differences(h); len(diff) > 0 {
		// Zwischen Vorprüfung und jetzt geändert (ein anderer Prozess).
		return h, s.fail("Hub %s ist schon anders eingetragen (%s); node setup überschreibt nichts.", h.Name,
			strings.Join(diff, "; "))
	}
	s.logf("Hub %s: schon eingetragen (%s, als Node %s)", h.Name, describeTransport(h), h.NodeName)
	r, closeHub, code := s.rotation(ctx, st, h)
	if code != 0 {
		return h, code
	}
	defer closeHub()

	// 4.1 Eine liegende .pending zuerst.
	if ok, err := r.hasPending(); err != nil {
		return h, s.fail("%v", err)
	} else if ok {
		out, valid, err := r.clarify(h.Token, s.code.Token)
		if err != nil {
			return h, s.fail("%v", err)
		}
		switch {
		case out == clarifyAdopted:
			s.logf("Der frühere node_rotate kam an; das Token ist übernommen, der Code ist wertlos.")
			return r.h, 0
		case valid == s.code.Token && valid != h.Token:
			return s.adoptCodeAndRotate(r)
		case s.code.Token == h.Token:
			return s.rotate(r)
		}
		s.logf("Das gespeicherte Token gilt: schon rotiert, der Code ist wertlos.")
		return r.h, 0
	}
	// 4.2 Das Token im Code ist das gespeicherte: rotieren.
	if s.code.Token == h.Token {
		return s.rotate(r)
	}
	// 4.3 Ein anderes: whoami entscheidet. Erst das gespeicherte — nach einem
	// gelungenen setup der übliche Fall, so kostet ein zweiter Aufruf mit
	// demselben Code keinen Fehlversuch.
	v, _, err := r.check(h.Token)
	s.logf("Gespeichertes Token (%s): %s", ident.MaskToken(h.Token), v.text())
	switch v {
	case tokenValid:
		s.logf("Schon rotiert: Das gespeicherte Token gilt, der Code ist wertlos.")
		return r.h, 0
	case tokenUnknown:
		return h, s.fail("Hub %s nicht erreicht oder Ausgang unklar: %v\nNichts geändert; später erneut aufrufen.",
			h.Name, err)
	}
	v, _, err = r.check(s.code.Token)
	s.logf("Token aus dem Code (%s): %s", ident.MaskToken(s.code.Token), v.text())
	switch v {
	case tokenValid:
		return s.adoptCodeAndRotate(r)
	case tokenUnknown:
		return h, s.fail("Hub %s nicht erreicht oder Ausgang unklar: %v\nNichts geändert; später erneut aufrufen.",
			h.Name, err)
	}
	return h, s.fail("Weder das gespeicherte Token noch das aus dem Code gilt am Hub %s. Einen neuen Einrichtungscode "+
		"erzeugt der Admin: kephalaion hub node token %s — dann node setup damit.", h.Name, h.NodeName)
}

// rotation verbindet zum Hub eines Eintrags.
func (s *setupNode) rotation(ctx context.Context, st nodestore.Store, h nodestore.Hub) (*nodeRotation, func(), int) {
	hub, closeHub, err := connectHub(ctx, s.cfg, h)
	if err != nil {
		return nil, nil, s.fail("%v", err)
	}
	return &nodeRotation{ctx: ctx, s: st, h: h, hub: hub, logf: s.logf}, closeHub, 0
}

// newEntry prüft den Eintrag aus dem Code mit whoami und speichert ihn erst
// danach; dann rotiert es.
func (s *setupNode) newEntry(ctx context.Context, st nodestore.Store, hubInConfig bool) (nodestore.Hub, int) {
	h := s.wanted()
	if err := nodestore.CheckHub(h, hubInConfig); err != nil {
		return h, s.fail("%v\nNichts gespeichert; der Code gilt weiter.", err)
	}
	hub, closeHub, err := connectHub(ctx, s.cfg, h)
	if err != nil {
		return h, s.fail("%v\nNichts gespeichert; der Code gilt weiter.", err)
	}
	defer closeHub()
	s.logf("Prüfe den Hub %s (%s, whoami) …", h.Name, describeTransport(h))
	resp, err := hub.Whoami(ctx, contract.WhoamiRequest{Version: contract.Version,
		Auth: contract.NodeAuth{Node: h.NodeName, Token: h.Token}})
	switch {
	case errors.Is(err, contract.ErrUnauthenticated):
		return h, s.fail("Der Hub %s nimmt den Node %s mit dem Token aus dem Code nicht an — schon eingelöst, ersetzt "+
			"oder gesperrt? Einen neuen Einrichtungscode erzeugt der Admin: kephalaion hub node token %s. Nichts "+
			"gespeichert.", h.Name, h.NodeName, h.NodeName)
	case err != nil:
		explained := explainHubError(h, err)
		hint := ""
		if h.Transport == nodestore.TransportHTTPS && s.caFile == "" && !strings.Contains(explained.Error(), "--ca-file") {
			hint = " Prüft der Hub mit einer eigenen CA, braucht es --ca-file <pfad>."
		}
		return h, s.fail("Hub %s (%s): %v\nNichts gespeichert; der Code gilt weiter.%s", h.Name, describeTransport(h),
			explained, hint)
	}
	if _, err := ulid.ParseStrict(resp.HubID); err != nil {
		return h, s.fail("Hub %s nennt als hub_id %q, keine ULID; nichts gespeichert", h.Name, resp.HubID)
	}
	if err := st.AddHub(ctx, h, hubInConfig); err != nil {
		return h, s.fail("%v\nDer Code gilt weiter.", err)
	}
	stored, err := st.Hub(ctx, h.Name)
	if err != nil {
		return h, s.fail("%v", err)
	}
	if _, err := replica.AdoptHubID(ctx, st, stored, resp.HubID); err != nil {
		return stored, s.fail("%v", err)
	}
	s.logf("Hub %s eingetragen und geprüft (%s, als Node %s, hub_id %s)", h.Name, describeTransport(h), h.NodeName,
		resp.HubID)
	stored, err = st.Hub(ctx, h.Name)
	if err != nil {
		return h, s.fail("%v", err)
	}
	r := &nodeRotation{ctx: ctx, s: st, h: stored, hub: hub, logf: s.logf}
	return s.rotate(r)
}

// adoptCodeAndRotate übernimmt das Token aus einem neuen Code (hub node
// token) in den Eintrag und rotiert es.
func (s *setupNode) adoptCodeAndRotate(r *nodeRotation) (nodestore.Hub, int) {
	if err := r.s.ReplaceHubToken(r.ctx, r.h.Name, r.h.EntryID, s.code.Token); err != nil {
		return r.h, s.fail("%v", err)
	}
	r.h.Token = s.code.Token
	s.logf("Neuer Code (hub node token): das Token daraus ist übernommen.")
	return s.rotate(r)
}

// rotate rotiert das Token und übersetzt ein Scheitern (Schritt 5).
func (s *setupNode) rotate(r *nodeRotation) (nodestore.Hub, int) {
	err := r.rotate()
	if err == nil {
		s.logf("Der Code ist jetzt wertlos.")
		return r.h, 0
	}
	var re *rotateError
	if errors.As(err, &re) {
		switch re.kind {
		case rotateRejected:
			return r.h, s.fail("%v\nDas Token aus dem Code gilt nicht mehr — ein alter Code? Einen neuen erzeugt der "+
				"Admin: kephalaion hub node token %s", err, r.h.NodeName)
		case rotateOldHub:
			return r.h, s.fail("%v\nDer Hub-Eintrag %s bleibt; nach dem Update des Hubs setzt derselbe Aufruf fort.", err,
				r.h.Name)
		case rotateUnclear:
			return r.h, s.fail("%v\nDerselbe Aufruf klärt es und setzt fort.", err)
		}
	}
	return r.h, s.fail("%v", err)
}

// collections abonniert die erlaubten Collections, ohne die fest
// abonnierten (Schritt 6).
func (s *setupNode) collections(ctx context.Context, st nodestore.Store, h nodestore.Hub) int {
	hub, closeHub, err := connectHub(ctx, s.cfg, h)
	if err != nil {
		return s.fail("%v", err)
	}
	defer closeHub()
	resp, err := hub.Whoami(ctx, contract.WhoamiRequest{Version: contract.Version,
		Auth: contract.NodeAuth{Node: h.NodeName, Token: h.Token}})
	if err != nil {
		return s.fail("Collections: whoami am Hub %s: %v\nDas Token ist rotiert; derselbe Aufruf setzt fort.", h.Name,
			explainHubError(h, err))
	}
	current, err := st.Hub(ctx, h.Name)
	if err != nil {
		return s.fail("%v", err)
	}
	s.subscribed = resp.Subscribed
	var added, had []string
	for _, c := range resp.Allowed {
		switch {
		case slices.Contains(resp.Subscribed, c):
		case slices.Contains(current.Collections, c):
			had = append(had, c)
		default:
			if err := st.AddCollection(ctx, h.Name, c); err != nil {
				return s.fail("Collection %s: %v", ident.Address(h.Name, c), err)
			}
			added = append(added, c)
		}
	}
	switch {
	case len(resp.Allowed) == 0:
		s.logf("Collections: Der Hub erlaubt diesem Node noch keine — der Admin: kephalaion hub node grant %s "+
			"<collection>, dann node setup erneut oder kephalaion node collection add %s:<collection>", h.NodeName, h.Name)
	default:
		s.logf("Collections abonniert: %s; schon abonniert: %s; fest abonniert: %s", joinOrNone(added),
			joinOrNone(had), joinOrNone(resp.Subscribed))
	}
	return 0
}

// service richtet den Dienst ein, wenn er fehlt (Schritt 7). Scheitert das,
// bleibt es bei einer Warnung: Der Node ist eingerichtet.
func (s *setupNode) service(ctx context.Context) {
	if s.noService {
		s.logf("Dienst: übergangen (--no-service); später: kephalaion service install")
		return
	}
	m := newServiceManager()
	if err := m.Check(); err != nil {
		if errors.Is(err, service.ErrNoSystemd) {
			s.warn("kein Dienst eingerichtet: %s", noSystemdHint(s.loc))
			return
		}
		s.warn("kein Dienst eingerichtet: %v", err)
		return
	}
	if st, err := m.UserState(ctx); err == nil && st.Installed {
		s.logf("Dienst: schon eingerichtet (%s, läuft: %s); er liest den Hub-Eintrag in der nächsten Runde",
			m.Kind(), yesNo(st.Running))
		return
	}
	var args []string
	if *s.c.cfg != "" {
		args = append(args, "--config", *s.c.cfg)
	}
	var out, errOut bytes.Buffer
	if code := runServiceInstall(args, &out, &errOut); code != 0 {
		s.warn("service install: %s%s", strings.TrimSpace(errOut.String()), strings.TrimSpace(out.String()))
		return
	}
	s.logf("Dienst eingerichtet (%s); Log: %s", m.Kind(), m.LogHint())
}

// register trägt den Node bei den KI-Assistenten ein wie der automatische
// Anstoß (Schritt 8).
func (s *setupNode) register(ctx context.Context) {
	dir, err := assistant.TokensDir()
	if err != nil {
		s.warn("%v", err)
		return
	}
	accounts, err := assistant.Accounts(filepath.Join(dir, s.code.Alias))
	if err != nil {
		s.warn("%v", err)
		return
	}
	if len(accounts) == 0 {
		s.logf("Noch keine Token-Datei unter %s/%s/: den ersten Account einrichten — kephalaion node account setup "+
			"%s <account> <einrichtungstoken> (trägt danach selbst bei den Assistenten ein)", dir, s.code.Alias,
			s.code.Alias)
	}
	target, err := assistantTarget(*s.c.cfg)
	if err != nil {
		s.warn("bei den KI-Assistenten nicht eingetragen: %v", err)
		return
	}
	s.logf("Trage den Node bei den KI-Assistenten ein (wie node mcp add --auto) …")
	if registerAssistants(ctx, s.c.name, target, assistant.AddOptions{Auto: true}, s.c.stdout, s.c.stderr) != 0 {
		s.warn("die Anmeldung bei den KI-Assistenten ist nicht vollständig (kephalaion node mcp status)")
	}
}

// overview zeigt zum Schluss, was gilt (Schritt 9): config, Hub-Eintrag,
// Collections, Dienst, Assistenten, Accounts. Kein Token ungekürzt.
func (s *setupNode) overview(ctx context.Context, st nodestore.Store) {
	w := s.c.stdout
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Übersicht")
	printConfigLine(w, s.loc, true)
	if data, err := os.ReadFile(s.loc.Path); err == nil {
		for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
			fmt.Fprintf(w, "  %s\n", line)
		}
	}
	h, err := st.Hub(ctx, s.code.Alias)
	if err != nil {
		fmt.Fprintf(w, "Hub %s: %v\n", s.code.Alias, err)
		return
	}
	fmt.Fprintf(w, "Hub-Eintrag %s (node.db)\n", h.Name)
	fmt.Fprintf(w, "  Node-Name:    %s\n", h.NodeName)
	fmt.Fprintf(w, "  Transport:    %s\n", h.Transport)
	fmt.Fprintf(w, "  Adresse:      %s\n", orDash(h.Address))
	if h.Transport == nodestore.TransportHTTPS {
		fmt.Fprintf(w, "  CA:           %s\n", describeCA(h.CA))
	}
	fmt.Fprintf(w, "  Token:        %s\n", ident.MaskToken(h.Token))
	fmt.Fprintf(w, "  hub_id:       %s\n", hubIDOrNone(h.HubID))
	if pendingExists(st, h) {
		fmt.Fprintf(w, "  .pending:     %s liegt — derselbe Aufruf klärt sie\n", st.PendingTokenPath(h.EntryID))
	}
	fmt.Fprintf(w, "Collections:    abonniert %s; fest abonniert %s\n", joinOrNone(h.Collections),
		joinOrNone(s.subscribed))
	m := newServiceManager()
	fmt.Fprintf(w, "Dienst:         %s; Log: %s\n", serviceLine(ctx, s.loc), m.LogHint())
	s.overviewAssistants(ctx, w)
	s.overviewAccounts(w)
}

// overviewAssistants zeigt die Assistenten wie node mcp status.
func (s *setupNode) overviewAssistants(ctx context.Context, w io.Writer) {
	target, err := assistantTarget(*s.c.cfg)
	if err != nil {
		fmt.Fprintf(w, "Assistenten:    %v\n", err)
		return
	}
	statuses, _, err := newAssistantManager().Status(ctx, target, nil)
	if err != nil {
		fmt.Fprintf(w, "Assistenten:    %v\n", err)
		return
	}
	fmt.Fprintf(w, "Assistenten (Node %s):\n", target.URL)
	for _, st := range statuses {
		line := fmt.Sprintf("  %s: %s", st.Assistant, st.State)
		switch st.State {
		case assistant.StateRegistered:
			line += " (" + loginList(st.Logins) + ")"
		case assistant.StateDiffers, assistant.StateExtension:
			line += " — " + st.Detail
		case assistant.StateUnknown:
			line += " — " + st.Err.Error()
		}
		fmt.Fprintln(w, line)
	}
}

// overviewAccounts zeigt die Token-Dateien des Hubs, ohne sie zu prüfen —
// jede Prüfung einer ungültigen wäre ein Fehlversuch; den Zustand zeigt
// node account list.
func (s *setupNode) overviewAccounts(w io.Writer) {
	dir, err := assistant.TokensDir()
	if err != nil {
		fmt.Fprintf(w, "Accounts:       %v\n", err)
		return
	}
	files, err := tokenFiles(dir)
	if err != nil {
		fmt.Fprintf(w, "Accounts:       %v\n", err)
		return
	}
	var mine []string
	for _, f := range files {
		if f.Hub == s.code.Alias {
			mine = append(mine, fmt.Sprintf("%s (.%s)", f.Account, f.Kind))
		}
	}
	if len(mine) == 0 {
		fmt.Fprintf(w, "Accounts:       keine unter %s/%s/ (kephalaion node account setup %s <account> "+
			"<einrichtungstoken>)\n", dir, s.code.Alias, s.code.Alias)
		return
	}
	fmt.Fprintf(w, "Accounts:       %s unter %s/%s/ — Zustand: kephalaion node account list\n", strings.Join(mine, ", "),
		dir, s.code.Alias)
}
