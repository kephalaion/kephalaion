package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"

	"github.com/kephalaion/kephalaion/internal/assistant"
	"github.com/kephalaion/kephalaion/internal/config"
	"github.com/kephalaion/kephalaion/internal/contract"
	"github.com/kephalaion/kephalaion/internal/ident"
	"github.com/kephalaion/kephalaion/internal/node/replica"
	nodestore "github.com/kephalaion/kephalaion/internal/node/store"
)

const nodeAccountUsage = `Aufruf:
  kephalaion node account setup  <hub> <account> [<token> | --token-stdin]
                                 [--node url [--ca-file pfad]]
  kephalaion node account rotate <hub> <account> (--token-file pfad | --token-stdin)
  kephalaion node account check  <hub> <account> (--token-file pfad | --token-stdin)
  kephalaion node account list   [--node url [--ca-file pfad]] [--json]

Kommandos:
  setup    richtet einen Account mit seinem Einrichtungstoken (aus kephalaion
           hub account add bzw. token) auf diesem Rechner ein — ohne eigenen
           Node und ohne node.db, über einen Node, den dieser Rechner
           erreicht: Das neue Token entsteht hier, der Node lässt den Hub
           rotieren (zu Node und Hub geht nur der Hash), das Token landet in
           ~/.config/kephalaion/tokens/<hub>/<account>.token (0600), und der
           Node wird bei den KI-Assistenten eingetragen.
  rotate   ersetzt das Token eines Accounts am Hub: Der Node erzeugt ein neues
           Token, meldet sich mit dem alten an und schickt dem Hub nur den Hash
           des neuen. Danach gilt nur noch das neue. Nach Erfolg schreibt der
           Node die Zeilen des Accounts in seine Replica — nur die gewünschter
           Collections —, der Account ist also sofort bekannt, ohne Abgleich.
           Braucht die node.db dieses Rechners mit dem Hub-Eintrag; ohne sie:
           setup.
  check    fragt den Hub, ob das Token gilt (whoami). Liegt neben der
           Token-Datei eine Datei .pending, prüft check beide und räumt auf:
           gilt das neue, ersetzt es die Datei; gilt das alte, löscht es
           .pending; gilt keines, bleiben beide.
  list     zeigt alle Token-Dateien dieses Linux-Users unter
           ~/.config/kephalaion/tokens/ (<account>.token und .pending) mit
           Hub, Account, Zustand, User und Collections — gefragt beim Node,
           je Datei genau eine Anfrage, nicht in einer eigenen Datenbank.

setup:
  <hub> ist der Alias des Hub-Eintrags am Node, <token> das Einrichtungstoken.
  Es darf hier — und nur hier — als Argument kommen, weil es sofort rotiert
  wird; --token-stdin liest es als eine Zeile von der Standardeingabe. Grenze:
  Auf einem Rechner mit mehreren Usern sieht jeder die Prozessliste; gilt das
  Token beim Rotieren nicht mehr, erzeugt der Admin ein neues.
  Der Node ist --node <url> — http://127.0.0.1:<port> oder über einen Proxy
  https://<name>/<präfix>, etwa https://<name>/kephalaion (die Basis ohne
  /mcp; --ca-file prüft das Zertifikat gegen diese CA) —, sonst listen im
  Abschnitt node: der config des Users bzw. der globalen. Steht dort kein
  Node, ist --node Pflicht.
  Ablauf, je Schritt eine Zeile, Tokens nur gekürzt: zuerst alles ohne Token
  — Adresse, keine Token-Datei da (sonst Abbruch, bevor das Token gelesen
  wird; was gilt, zeigt kephalaion node account list), der Node antwortet
  (initialize ohne Token). Dann das neue Token nach <datei>.pending (0600),
  rotate über den Node, die Token-Datei, das Eintragen bei den Assistenten:
  mit dem Node der config wie kephalaion node mcp add --auto, mit --node wie
  kephalaion node mcp add --node <url> --hub <hub>.
  Nie wiederholt. Lehnt der Hub ab, kennt der Node den Hub nicht oder
  erreicht er ihn nicht, ist nichts geändert: .pending wird gelöscht, das
  Einrichtungstoken gilt weiter. Ist der Ausgang unklar (auch ein 502 des
  Proxys, eine Zeitüberschreitung), bleibt .pending. Derselbe Aufruf klärt
  sie dann, ohne zu rotieren: Gilt ihr Token, wird es die Token-Datei; gilt
  es nicht, aber das Einrichtungstoken, wird .pending gelöscht (Exit 3,
  setup erneut aufrufen); gilt keins, bleibt sie — ein neues
  Einrichtungstoken erzeugt der Admin (kephalaion hub account token
  <account>).

list:
  Der Node wie bei setup (--node, sonst listen der config; ohne Node Exit 2).
  Zustände: gültig; ungültig (der Hub lehnt das Token ab); unbekannt (der
  Node kennt den Hub nicht); über einen Proxy ungültig oder unbekannt (die
  verdeckte Antwort trennt beides nicht); nicht geprüft (Node oder Hub nicht
  erreichbar, der Hub nimmt den Node nicht an); unlesbar (die Datei hält kein
  gültiges Token — sie wird nicht gefragt). Ein ungültiges Token ist am Node
  ein Fehlversuch wie am MCP-Eingang: höchstens einer je Datei.

--token-file pfad (rotate, check)
  Die Datei hält das Token als eine Zeile. rotate liest das alte, schreibt
  das neue vor dem Aufruf nach pfad.pending (0600) und ersetzt nach Erfolg die
  Datei; .pending verschwindet. Scheitert der Aufruf eindeutig, bleibt die
  Datei, und .pending wird gelöscht. Ist der Ausgang unklar (etwa eine
  Zeitüberschreitung), bleiben beide — dann zuerst check. Solange pfad.pending
  liegt, verweigert rotate einen neuen Versuch.
  Liegt die Datei unter tokens/ neben der config des Users
  (~/.config/kephalaion/tokens/<hub>/<account>.token), meldet rotate — und
  check, wenn es die Datei ersetzt — den Node danach bei den KI-Assistenten
  an, wie kephalaion node mcp add --auto: nur bei denen mit Eintrag, beim
  ersten Account bei allen gefundenen. Scheitert das, bleibt es bei einer
  Warnung.
--token-stdin
  rotate, check: liest das alte Token als eine Zeile von der
  Standardeingabe; rotate gibt das neue nach Erfolg einmal aus, bei unklarem
  Ausgang ebenfalls, deutlich als unklar markiert. setup: das
  Einrichtungstoken statt als Argument.

rotate ist ein Kommando, kein MCP-Werkzeug: Das Token stünde sonst im Kontext
der KI. Es wird nie wiederholt — danach gilt das alte Token nicht mehr.

Optionen:
  --node url      setup, list: der Node, über den rotiert bzw. geprüft wird
                  (siehe oben)
  --ca-file pfad  setup, list: mit --node https://… das Zertifikat gegen
                  diese CA (PEM) prüfen statt gegen die System-Roots
  --json          list: als JSON ({"node", "files": [{hub, account, kind,
                  file, state, user, collections, reason}]})
  --config pfad   Ort der config (siehe kephalaion node init --help)

Exit-Codes von setup:
  0   eingerichtet
  1   Fehler: Node nicht erreichbar, Token-Datei schon da, abgelehnt,
      Ausgang unklar (.pending bleibt), nicht zu entscheiden
  2   falscher Aufruf, auch ohne Node in der config und ohne --node
  3   eine liegende .pending ist geklärt und gelöscht: setup erneut aufrufen

Exit-Codes von list:
  0   Liste ausgegeben (auch mit Dateien, die nicht geprüft sind)
  1   der Node antwortet nicht (alle Dateien nicht geprüft) oder tokens/
      ist nicht lesbar
  2   falscher Aufruf, auch ohne Node in der config und ohne --node
`

func runNodeAccount(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	u := nodeAccountUsage
	leaf := func(name string, fn func(c *command, ctx context.Context, s nodestore.Store, cfg config.Config,
		h nodestore.Hub, account string, src tokenSource) error) func([]string) int {
		return func(a []string) int {
			c := newCommand("node account "+name, u, stdout, stderr, "<hub>", "<account>")
			file := c.fs.String("token-file", "", "")
			fromStdin := c.fs.Bool("token-stdin", false, "")
			return c.nodeDo(a, func(ctx context.Context, s nodestore.Store, _ bool, pos []string) error {
				if (*file == "") == !*fromStdin {
					return errors.New("erwartet genau eines von --token-file pfad und --token-stdin; das Token wird nie als Argument übergeben")
				}
				if err := ident.CheckPrincipalName("Account", pos[1]); err != nil {
					return err
				}
				h, err := s.Hub(ctx, pos[0])
				if err != nil {
					return err
				}
				cfg, err := c.loadConfig()
				if err != nil {
					return err
				}
				return fn(c, ctx, s, cfg, h, pos[1], tokenSource{file: *file, stdin: stdin})
			})
		}
	}
	return dispatch("node account", u, args, stdout, stderr, map[string]func([]string) int{
		"setup":  func(a []string) int { return runNodeAccountSetup(a, stdin, stdout, stderr) },
		"list":   func(a []string) int { return runNodeAccountList(a, stdout, stderr) },
		"rotate": leaf("rotate", (*command).accountRotate),
		"check":  leaf("check", (*command).accountCheck),
	})
}

// tokenSource ist, woher ein Token kommt: eine Datei oder stdin.
type tokenSource struct {
	file  string
	stdin io.Reader
}

func (t tokenSource) pending() string { return t.file + ".pending" }

// readTokenFile liest ein Token aus der ersten Zeile einer Datei.
func readTokenFile(path string) (string, error) {
	return assistant.ReadTokenFile(path)
}

// writePending legt pfad.pending mit 0600 an; gibt es sie schon, scheitert es.
func writePending(path, token string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if _, err := f.WriteString(token + "\n"); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return fmt.Errorf("%s: %w", path, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return fmt.Errorf("%s: %w", path, err)
	}
	return f.Close()
}

func fileExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

// connectHub liefert die Umsetzung des Vertrags für einen Eintrag; close
// räumt sie weg.
func connectHub(ctx context.Context, cfg config.Config, h nodestore.Hub) (contract.Hub, func(), error) {
	conn := &connector{ctx: ctx, cfg: cfg}
	hub, err := conn.connect(h)
	if err != nil {
		conn.close()
		return nil, nil, fmt.Errorf("Hub %s: %w", h.Name, err)
	}
	return hub, conn.close, nil
}

// errReported heißt: Die Meldung steht schon da, der Exit-Code ist 1.
var errReported = errors.New("gemeldet")

func (c *command) accountRotate(ctx context.Context, s nodestore.Store, cfg config.Config, h nodestore.Hub,
	account string, src tokenSource) error {
	var old string
	var err error
	if src.file != "" {
		if ok, err := fileExists(src.pending()); err != nil {
			return err
		} else if ok {
			return fmt.Errorf("%s liegt noch von einem früheren rotate mit unklarem Ausgang; zuerst: "+
				"kephalaion node account check %s %s --token-file %s", src.pending(), h.Name, account, src.file)
		}
		old, err = readTokenFile(src.file)
	} else {
		old, err = readToken(src.stdin)
	}
	if err != nil {
		return err
	}
	hub, closeHub, err := connectHub(ctx, cfg, h)
	if err != nil {
		return err
	}
	defer closeHub()
	newToken, err := ident.NewToken()
	if err != nil {
		return err
	}
	// Das neue Token liegt, bevor irgendetwas abgeschickt wird.
	if src.file != "" {
		if err := writePending(src.pending(), newToken); err != nil {
			return err
		}
	}
	resp, err := hub.Rotate(ctx, contract.RotateRequest{Version: contract.Version,
		Auth:    contract.NodeAuth{Node: h.NodeName, Token: h.Token},
		Account: account, Token: old, NewHash: ident.HashToken(newToken)})
	switch {
	case errors.Is(err, contract.ErrOutcomeUnknown):
		if src.file != "" {
			fmt.Fprintf(c.stderr, "node account rotate: Ausgang unklar: %v\n", err)
			fmt.Fprintf(c.stderr, "Beide Dateien bleiben: %s (alt) und %s (neu). Prüfen, welches gilt:\n", src.file, src.pending())
			fmt.Fprintf(c.stderr, "  kephalaion node account check %s %s --token-file %s\n", h.Name, account, src.file)
			return errReported
		}
		fmt.Fprintf(c.stderr, "node account rotate: Ausgang unklar: %v\n", err)
		fmt.Fprintln(c.stdout, "UNKLAR — PRÜFEN: Das neue Token gilt vielleicht schon, vielleicht noch das alte.")
		fmt.Fprintln(c.stdout, "Neues Token (wird nicht wieder angezeigt; beide aufbewahren, bis check es klärt):")
		fmt.Fprintf(c.stdout, "  %s\n", newToken)
		fmt.Fprintf(c.stdout, "Prüfen: kephalaion node account check %s %s --token-stdin\n", h.Name, account)
		return errReported
	case err != nil:
		if src.file != "" {
			if rmErr := os.Remove(src.pending()); rmErr != nil {
				return fmt.Errorf("rotate gescheitert: %w; %s ließ sich nicht löschen: %v", err, src.pending(), rmErr)
			}
			return fmt.Errorf("rotate gescheitert, die Token-Datei bleibt unverändert: %w", err)
		}
		return fmt.Errorf("rotate gescheitert, das alte Token gilt weiter: %w", err)
	}

	// Erfolg: zuerst das Token sichern, dann die Replica.
	if src.file != "" {
		if err := config.WriteFileAtomic(src.file, []byte(newToken+"\n"), 0o600); err != nil {
			fmt.Fprintf(c.stderr, "node account rotate: Das neue Token gilt, aber %s ließ sich nicht ersetzen: %v\n", src.file, err)
			fmt.Fprintf(c.stderr, "Das neue Token steht in %s; von Hand nach %s verschieben.\n", src.pending(), src.file)
			return errReported
		}
		if err := os.Remove(src.pending()); err != nil {
			fmt.Fprintf(c.stderr, "node account rotate: %s ließ sich nicht löschen: %v\n", src.pending(), err)
		}
		fmt.Fprintf(c.stdout, "Account %s: Token ersetzt (%s), gespeichert in %s.\n", account, ident.MaskToken(newToken), src.file)
		// Zuletzt, nach den Meldungen zur Replica: der Anstoß bei den
		// KI-Assistenten (erster Account: Eintrag überall).
		defer c.autoRegister(src.file)
	} else {
		fmt.Fprintf(c.stdout, "Account %s: Token ersetzt.\n", account)
		fmt.Fprintln(c.stdout, "Neues Token (wird nicht wieder angezeigt; das alte gilt nicht mehr):")
		fmt.Fprintf(c.stdout, "  %s\n", newToken)
	}
	written, reset, err := replica.WriteAccountRows(ctx, s, h, resp.HubID, resp.Rows)
	if reset != "" {
		fmt.Fprintf(c.stdout, "Replica %s: %s\n", h.Name, reset)
	}
	if err != nil {
		// Der Erfolg bleibt: Das Token ist gesichert, der Abgleich holt die
		// Zeilen nach.
		fmt.Fprintf(c.stderr, "node account rotate: Replica nicht geschrieben: %v\n", err)
		fmt.Fprintf(c.stderr, "Nachholen: kephalaion node sync %s\n", h.Name)
		return nil
	}
	var offered []string
	for _, r := range resp.Rows {
		offered = append(offered, r.Collection)
	}
	if len(written) == 0 {
		fmt.Fprintf(c.stdout, "Der Node will keine der Collections des Accounts, die er abgleichen darf (%s); die Replica bleibt. "+
			"Wünschen mit: kephalaion node collection add %s:<collection>\n", joinOrNone(offered), h.Name)
		return nil
	}
	fmt.Fprintf(c.stdout, "Replica %s: Account %s bekannt in %s%s.\n", h.Name, account, strings.Join(written, ", "), userNote(resp.Rows))
	return nil
}

// userNote nennt den User aus den Zeilen, die rotate geliefert hat — „ (User
// kleist)“ —, oder nichts, wenn keine Zeile ihn lesbar trägt.
func userNote(rows []contract.Row) string {
	for _, r := range rows {
		if r.Content == nil {
			continue
		}
		if c, err := contract.DecodeAccountContent(*r.Content); err == nil {
			return " (User " + c.User + ")"
		}
	}
	return ""
}

// whoamiAccount fragt den Hub, ob ein Token für den Account gilt.
func whoamiAccount(ctx context.Context, hub contract.Hub, h nodestore.Hub, account, token string) (*contract.AccountStatus, error) {
	resp, err := hub.Whoami(ctx, contract.WhoamiRequest{Version: contract.Version,
		Auth:    contract.NodeAuth{Node: h.NodeName, Token: h.Token},
		Account: &contract.AccountAuth{Account: account, Token: token}})
	if err != nil {
		return nil, err
	}
	if resp.Account == nil {
		return nil, errors.New("der Hub antwortet ohne Auskunft zum Account")
	}
	return resp.Account, nil
}

func (c *command) accountCheck(ctx context.Context, _ nodestore.Store, cfg config.Config, h nodestore.Hub,
	account string, src tokenSource) error {
	var current string
	var err error
	if src.file != "" {
		current, err = readTokenFile(src.file)
	} else {
		current, err = readToken(src.stdin)
	}
	if err != nil {
		return err
	}
	pending := false
	if src.file != "" {
		if pending, err = fileExists(src.pending()); err != nil {
			return err
		}
	}
	hub, closeHub, err := connectHub(ctx, cfg, h)
	if err != nil {
		return err
	}
	defer closeHub()
	valid := func(st *contract.AccountStatus) string {
		return fmt.Sprintf("Account %s gilt am Hub %s (User %s); Collections dieses Nodes: %s", account, h.Name, st.User,
			joinOrNone(st.Collections))
	}

	if !pending {
		st, err := whoamiAccount(ctx, hub, h, account, current)
		if err != nil {
			return err
		}
		if !st.Valid {
			return fmt.Errorf("Account %s gilt am Hub %s nicht mit diesem Token (unbekannt, falsches Token oder gesperrt)", account, h.Name)
		}
		fmt.Fprintln(c.stdout, valid(st)+".")
		return nil
	}

	// .pending liegt: erst das neue prüfen, dann das alte.
	newToken, err := readTokenFile(src.pending())
	if err != nil {
		return err
	}
	st, err := whoamiAccount(ctx, hub, h, account, newToken)
	if err != nil {
		return err
	}
	if st.Valid {
		if err := config.WriteFileAtomic(src.file, []byte(newToken+"\n"), 0o600); err != nil {
			return fmt.Errorf("das neue Token gilt, aber %s ließ sich nicht ersetzen: %w", src.file, err)
		}
		if err := os.Remove(src.pending()); err != nil {
			return err
		}
		fmt.Fprintf(c.stdout, "Das neue Token gilt — der rotate kam an. %s ersetzt, %s gelöscht.\n", src.file, src.pending())
		fmt.Fprintln(c.stdout, valid(st)+".")
		fmt.Fprintf(c.stdout, "Die Replica holt der Abgleich nach: kephalaion node sync %s\n", h.Name)
		c.autoRegister(src.file)
		return nil
	}
	st, err = whoamiAccount(ctx, hub, h, account, current)
	if err != nil {
		return err
	}
	if st.Valid {
		if err := os.Remove(src.pending()); err != nil {
			return err
		}
		fmt.Fprintf(c.stdout, "Das alte Token gilt — der rotate kam nicht an. %s gelöscht; rotate lässt sich wiederholen.\n", src.pending())
		fmt.Fprintln(c.stdout, valid(st)+".")
		return nil
	}
	return fmt.Errorf("weder das Token in %s noch das in %s gilt am Hub %s (Account unbekannt oder gesperrt?); beide Dateien bleiben",
		src.file, src.pending(), h.Name)
}
