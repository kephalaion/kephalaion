package main

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/kephalaion/kephalaion/internal/contract"
	"github.com/kephalaion/kephalaion/internal/contract/httpapi"
	"github.com/kephalaion/kephalaion/internal/ident"
	"github.com/kephalaion/kephalaion/internal/node/replica"
	nodestore "github.com/kephalaion/kephalaion/internal/node/store"
)

const nodeHubUsage = `Aufruf:
  kephalaion node hub add   <alias> --node <name am hub> --transport local|http|https|ssh
                            [--address adresse] [--ca-file pfad] [--ssh-key pfad] --token-stdin
  kephalaion node hub add   <alias> --node <name am hub> --transport local --create
  kephalaion node hub check <alias>
  kephalaion node hub list
  kephalaion node hub show  <alias>
  kephalaion node hub set   <alias> [--node …] [--transport …] [--address …] [--ca-file …] [--ssh-key …]
  kephalaion node hub token <alias> --token-stdin
  kephalaion node hub rm    <alias>

Kommandos:
  add     trägt einen Hub ein; den Alias vergibt der Node, den Node-Namen der
          Hub (kephalaion hub node add). Mit --create (nur bei local) legt add
          den Node am Hub derselben config selbst an und trägt sein Token
          direkt ein, ohne es anzuzeigen
  check   fragt den Hub, wer dieser Node für ihn ist (whoami): erreichbar,
          Node-Name, erlaubte Collections; merkt beim ersten Kontakt die
          hub_id — nennt der Hub eine andere als die Replica, wird sie geleert.
          Bei https nennt es Zertifikatsfehler, eine Antwort des Proxys ohne
          Hub (502, 503) und die Host-Prüfung des Hubs (403) im Klartext
  list    zeigt alle Hubs
  show    zeigt einen Hub samt gewünschten Collections; bei https die CA
          (Subject, Gültigkeit, SHA-256-Fingerabdruck je Zertifikat)
  set     ändert Node-Namen, Transport, Adresse, CA oder Schlüssel; der Rest
          bleibt
  token   ersetzt das Token dieses Nodes beim Hub
  rm      entfernt den Eintrag samt seinen gewünschten Collections und seiner
          Replica

Transporte:
  local   Hub im selben Prozess; verlangt einen Hub in derselben config, keine
          Adresse; höchstens ein Eintrag je Node
  http    nur auf diesem Rechner: http://localhost:<port> (auch 127.0.0.1,
          [::1]), ein Hub, der mit kephalaion serve lauscht — Klartext, deshalb
          nur Loopback
  https   https://<host>[:<port>], ein Hub auf einem anderen Rechner hinter
          einem Reverse-Proxy, der TLS beendet (docs/installation.md). Das
          Zertifikat prüft der Node gegen die System-Roots oder, mit
          --ca-file, gegen die dort genannte CA; scheitert die Prüfung, geht
          kein Token hinaus. Kein Client-Zertifikat
  ssh     [user@]host[:port], dazu optional --ssh-key; noch nicht gebaut

Wechselt set den Transport, fällt weg, was nicht passt: die Adresse bei local,
die CA außer bei https, der Schlüssel außer bei ssh.

Das Token liest --token-stdin als eine Zeile von der Standardeingabe; als
Argument wird es nie übergeben. Angezeigt wird es nur gekürzt.

Optionen:
  --node name        der Name, unter dem der Hub diesen Node kennt; mit ihm und
                     dem Token meldet sich der Node beim Hub an (Pflicht bei add)
  --transport art    local, http, https oder ssh
  --address adresse  Adresse des Hubs, je nach Transport
  --ca-file pfad     nur bei https: ein oder mehrere Zertifikate (PEM), gegen
                     die der Node das Zertifikat des Hubs prüft, statt der
                     System-Roots. Gespeichert wird der Inhalt der Datei, nicht
                     ihr Pfad; --ca-file "" bei set entfernt die CA wieder
  --ssh-key pfad     SSH-Schlüssel, nur bei ssh
  --token-stdin      Token von der Standardeingabe lesen
  --create           den Node am Hub derselben config anlegen (nur local); das
                     Token erzeugt der Hub, es wird nicht angezeigt
  --config pfad      Ort der config (siehe kephalaion node init --help)
`

const nodeCollectionUsage = `Aufruf:
  kephalaion node collection add  <hub>:<collection>
  kephalaion node collection list
  kephalaion node collection rm   <hub>:<collection>

Kommandos:
  add    will die Collection von diesem Hub haben; geprüft wird nur, dass es
         den Hub-Eintrag gibt — ob der Hub sie erlaubt, zeigt der Abgleich
  list   zeigt alle gewünschten Collections
  rm     will sie nicht mehr haben

Optionen:
  --config pfad   Ort der config (siehe kephalaion node init --help)
`

func runNodeHub(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	u := nodeHubUsage
	return dispatch("node hub", u, args, stdout, stderr, map[string]func([]string) int{
		"add": func(a []string) int {
			c := newCommand("node hub add", u, stdout, stderr, "<alias>")
			nodeName := c.fs.String("node", "", "")
			transport := c.fs.String("transport", "", "")
			address := c.fs.String("address", "", "")
			caFile := c.fs.String("ca-file", "", "")
			sshKey := c.fs.String("ssh-key", "", "")
			tokenStdin := c.fs.Bool("token-stdin", false, "")
			create := c.fs.Bool("create", false, "")
			return c.nodeDo(a, func(ctx context.Context, s nodestore.Store, hubInConfig bool, pos []string) error {
				if *nodeName == "" {
					return errors.New("es fehlt --node: der Name, unter dem der Hub diesen Node kennt")
				}
				if *transport == "" {
					return errors.New("es fehlt --transport (local, http, https oder ssh)")
				}
				switch {
				case *create && *tokenStdin:
					return errors.New("--create erzeugt das Token selbst; --token-stdin passt nicht dazu")
				case *create && *transport != nodestore.TransportLocal:
					return errors.New("--create gibt es nur mit --transport local: nur den Hub derselben config kann der Node selbst anlegen")
				case !*create && !*tokenStdin:
					return errors.New("es fehlt --token-stdin; das Token wird nie als Argument übergeben")
				}
				h := nodestore.Hub{Name: pos[0], NodeName: *nodeName, Transport: *transport, Address: *address, SSHKey: *sshKey}
				// Erst alles andere prüfen, dann stdin lesen.
				if err := ident.CheckName("Hub", h.Name); err != nil {
					return err
				}
				ca, err := readCAFile(*caFile)
				if err != nil {
					return err
				}
				h.CA = ca
				if err := nodestore.CheckHub(withPlaceholderToken(h), hubInConfig); err != nil {
					return err
				}
				if *create {
					return c.addHubCreate(ctx, s, h, hubInConfig)
				}
				token, err := readToken(stdin)
				if err != nil {
					return err
				}
				h.Token = token
				if err := s.AddHub(ctx, h, hubInConfig); err != nil {
					return err
				}
				fmt.Fprintf(stdout, "Hub %s eingetragen (%s, als Node %s).\n", h.Name, describeTransport(h), h.NodeName)
				return nil
			})
		},
		"check": func(a []string) int {
			c := newCommand("node hub check", u, stdout, stderr, "<alias>")
			return c.nodeDo(a, func(ctx context.Context, s nodestore.Store, _ bool, pos []string) error {
				return c.hubCheck(ctx, s, pos[0])
			})
		},
		"list": func(a []string) int {
			c := newCommand("node hub list", u, stdout, stderr)
			return c.nodeDo(a, func(ctx context.Context, s nodestore.Store, _ bool, _ []string) error {
				hubs, err := s.Hubs(ctx)
				if err != nil {
					return err
				}
				if len(hubs) == 0 {
					fmt.Fprintln(stdout, "Keine Hubs.")
					return nil
				}
				tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
				fmt.Fprintln(tw, "ALIAS\tNODE\tTRANSPORT\tADRESSE\tCA\tTOKEN\tHUB_ID\tCOLLECTIONS")
				for _, h := range hubs {
					ca := "–"
					if h.CA != "" {
						ca = "ja"
					}
					fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", h.Name, h.NodeName, h.Transport, orDash(h.Address), ca,
						ident.MaskToken(h.Token), hubIDOrNone(h.HubID), joinOrNone(h.Collections))
				}
				return tw.Flush()
			})
		},
		"show": func(a []string) int {
			c := newCommand("node hub show", u, stdout, stderr, "<alias>")
			return c.nodeDo(a, func(ctx context.Context, s nodestore.Store, _ bool, pos []string) error {
				h, err := s.Hub(ctx, pos[0])
				if err != nil {
					return err
				}
				fmt.Fprintf(stdout, "Hub %s\n", h.Name)
				fmt.Fprintf(stdout, "  Node-Name:    %s\n", h.NodeName)
				fmt.Fprintf(stdout, "  Transport:    %s\n", h.Transport)
				fmt.Fprintf(stdout, "  Adresse:      %s\n", orDash(h.Address))
				if h.Transport == nodestore.TransportHTTPS {
					fmt.Fprintf(stdout, "  CA:           %s\n", describeCA(h.CA))
				}
				if h.Transport == nodestore.TransportSSH {
					fmt.Fprintf(stdout, "  SSH-Schlüssel: %s\n", orDash(h.SSHKey))
				}
				fmt.Fprintf(stdout, "  Token:        %s\n", ident.MaskToken(h.Token))
				fmt.Fprintf(stdout, "  hub_id:       %s\n", hubIDOrNone(h.HubID))
				fmt.Fprintf(stdout, "  Collections:  %s\n", joinOrNone(h.Collections))
				return nil
			})
		},
		"set": func(a []string) int {
			c := newCommand("node hub set", u, stdout, stderr, "<alias>")
			nodeName := c.fs.String("node", "", "")
			transport := c.fs.String("transport", "", "")
			address := c.fs.String("address", "", "")
			caFile := c.fs.String("ca-file", "", "")
			sshKey := c.fs.String("ssh-key", "", "")
			return c.nodeDo(a, func(ctx context.Context, s nodestore.Store, hubInConfig bool, pos []string) error {
				var upd nodestore.HubUpdate
				if c.isSet("node") {
					upd.NodeName = nodeName
				}
				if c.isSet("transport") {
					upd.Transport = transport
				}
				if c.isSet("address") {
					upd.Address = address
				}
				if c.isSet("ca-file") {
					// --ca-file "" entfernt die CA.
					ca, err := readCAFile(*caFile)
					if err != nil {
						return err
					}
					upd.CA = &ca
				}
				if c.isSet("ssh-key") {
					upd.SSHKey = sshKey
				}
				if upd == (nodestore.HubUpdate{}) {
					return errors.New("nichts zu ändern; erwartet --node, --transport, --address, --ca-file oder --ssh-key")
				}
				if err := s.SetHub(ctx, pos[0], upd, hubInConfig); err != nil {
					return err
				}
				h, err := s.Hub(ctx, pos[0])
				if err != nil {
					return err
				}
				fmt.Fprintf(stdout, "Hub %s geändert (%s, als Node %s).\n", h.Name, describeTransport(h), h.NodeName)
				return nil
			})
		},
		"token": func(a []string) int {
			c := newCommand("node hub token", u, stdout, stderr, "<alias>")
			tokenStdin := c.fs.Bool("token-stdin", false, "")
			return c.nodeDo(a, func(ctx context.Context, s nodestore.Store, _ bool, pos []string) error {
				if !*tokenStdin {
					return errors.New("es fehlt --token-stdin; das Token wird nie als Argument übergeben")
				}
				if _, err := s.Hub(ctx, pos[0]); err != nil {
					return err
				}
				token, err := readToken(stdin)
				if err != nil {
					return err
				}
				if err := s.SetHubToken(ctx, pos[0], token); err != nil {
					return err
				}
				fmt.Fprintf(stdout, "Hub %s: Token ersetzt (%s).\n", pos[0], ident.MaskToken(token))
				return nil
			})
		},
		"rm": func(a []string) int {
			c := newCommand("node hub rm", u, stdout, stderr, "<alias>")
			return c.nodeDo(a, func(ctx context.Context, s nodestore.Store, _ bool, pos []string) error {
				if err := s.RemoveHub(ctx, pos[0]); err != nil {
					return err
				}
				fmt.Fprintf(stdout, "Hub %s entfernt.\n", pos[0])
				return nil
			})
		},
	})
}

func runNodeCollection(args []string, stdout, stderr io.Writer) int {
	u := nodeCollectionUsage
	return dispatch("node collection", u, args, stdout, stderr, map[string]func([]string) int{
		"add": func(a []string) int {
			c := newCommand("node collection add", u, stdout, stderr, "<hub>:<collection>")
			return c.nodeDo(a, func(ctx context.Context, s nodestore.Store, _ bool, pos []string) error {
				hub, coll, err := ident.ParseAddress(pos[0])
				if err != nil {
					return err
				}
				if err := s.AddCollection(ctx, hub, coll); err != nil {
					return err
				}
				fmt.Fprintf(stdout, "Collection %s gewünscht.\n", pos[0])
				return nil
			})
		},
		"list": func(a []string) int {
			c := newCommand("node collection list", u, stdout, stderr)
			return c.nodeDo(a, func(ctx context.Context, s nodestore.Store, _ bool, _ []string) error {
				wanted, err := s.Collections(ctx)
				if err != nil {
					return err
				}
				if len(wanted) == 0 {
					fmt.Fprintln(stdout, "Keine Collections.")
					return nil
				}
				for _, w := range wanted {
					fmt.Fprintln(stdout, ident.Address(w.Hub, w.Collection))
				}
				return nil
			})
		},
		"rm": func(a []string) int {
			c := newCommand("node collection rm", u, stdout, stderr, "<hub>:<collection>")
			return c.nodeDo(a, func(ctx context.Context, s nodestore.Store, _ bool, pos []string) error {
				hub, coll, err := ident.ParseAddress(pos[0])
				if err != nil {
					return err
				}
				if err := s.RemoveCollection(ctx, hub, coll); err != nil {
					return err
				}
				fmt.Fprintf(stdout, "Collection %s nicht mehr gewünscht.\n", pos[0])
				return nil
			})
		},
	})
}

// nodeDo wertet die Argumente aus, öffnet den Node und führt fn aus.
func (c *command) nodeDo(args []string, fn func(ctx context.Context, s nodestore.Store, hubInConfig bool, pos []string) error) int {
	pos, code, ok := c.parse(args)
	if !ok {
		return code
	}
	ctx := context.Background()
	s, hubInConfig, err := c.openNode(ctx)
	if err != nil {
		return c.fail(err)
	}
	defer s.Close()
	if err := fn(ctx, s, hubInConfig, pos); err != nil {
		return c.fail(err)
	}
	return 0
}

func describeTransport(h nodestore.Hub) string {
	if h.Address == "" {
		return h.Transport
	}
	return h.Transport + " " + h.Address
}

// explainHubError übersetzt für node hub check, was whoami meldet: einen
// Zertifikatsfehler (nicht vertraut, falscher Name, abgelaufen), die Antwort
// eines Proxys, hinter dem der Hub nicht antwortet (502, 503, 504), und die
// Host-Prüfung des Hubs (403 ohne Vertragsform). Alles andere bleibt, wie es
// ist. Der Satz sagt, was zu tun ist; die Meldung von Go steht dahinter.
func explainHubError(h nodestore.Hub, err error) error {
	host := h.Address
	if u, perr := url.Parse(h.Address); perr == nil && u.Hostname() != "" {
		host = u.Hostname()
	}
	var unknown x509.UnknownAuthorityError
	var hostErr x509.HostnameError
	var invalid x509.CertificateInvalidError
	var status *httpapi.StatusError
	switch {
	case errors.As(err, &unknown):
		issuer := "unbekannt"
		if unknown.Cert != nil {
			issuer = unknown.Cert.Issuer.String()
		}
		hint := "--ca-file?"
		if h.CA != "" {
			hint = "passt die CA aus --ca-file?"
		}
		return fmt.Errorf("Zertifikat von %s nicht vertraut (Aussteller %s; %s) — %v", host, issuer, hint, unknown)
	case errors.As(err, &hostErr):
		return fmt.Errorf("Zertifikat gilt nicht für %s (ausgestellt für %s) — %v", host,
			joinOrNone(certNames(hostErr.Certificate)), hostErr)
	case errors.As(err, &invalid) && invalid.Reason == x509.Expired && invalid.Cert != nil:
		if now := time.Now(); now.Before(invalid.Cert.NotBefore) {
			return fmt.Errorf("Zertifikat gilt erst ab %s — %v", invalid.Cert.NotBefore.UTC().Format(time.RFC3339), invalid)
		}
		return fmt.Errorf("Zertifikat abgelaufen seit %s — %v", invalid.Cert.NotAfter.UTC().Format(time.RFC3339), invalid)
	case errors.As(err, &status):
		switch status.Status {
		case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			return fmt.Errorf("Proxy antwortet, aber der Hub dahinter nicht (%v): läuft kephalaion serve auf dem "+
				"Rechner des Hubs, und zeigt der Proxy auf sein listen?", status)
		case http.StatusForbidden:
			if h.Transport == nodestore.TransportHTTPS {
				return fmt.Errorf("Host-Prüfung des Hubs schlägt fehl (%v): setzt der Proxy Host auf die "+
					"Loopback-Adresse des Hubs (header_up Host {upstream_hostport}, etwa localhost:7434)?", status)
			}
			return fmt.Errorf("Host-Prüfung des Hubs schlägt fehl (%v): ein Tunnel geht nur mit gleichem Port", status)
		}
	}
	return err
}

// certNames sind die Namen und Adressen, für die ein Zertifikat gilt.
func certNames(cert *x509.Certificate) []string {
	if cert == nil {
		return nil
	}
	names := append([]string{}, cert.DNSNames...)
	for _, ip := range cert.IPAddresses {
		names = append(names, ip.String())
	}
	return names
}

// readCAFile liest die Datei aus --ca-file; ein leerer Pfad ist keine CA.
// Geprüft wird der Inhalt im Node-Store (CheckHub), wie beim Import.
func readCAFile(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("--ca-file: %w", err)
	}
	if _, err := nodestore.ParseCA(string(data)); err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	return string(data), nil
}

// withPlaceholderToken liefert den Eintrag mit einem gültigen Token, um ihn
// vor dem Lesen des echten von stdin zu prüfen.
func withPlaceholderToken(h nodestore.Hub) nodestore.Hub {
	placeholder, err := ident.NewToken()
	if err == nil {
		h.Token = placeholder
	}
	return h
}

// describeCA beschreibt die CA eines Eintrags für show: je Zertifikat
// Subject, Gültigkeit und SHA-256-Fingerabdruck; ohne CA „–“ (System-Roots).
func describeCA(ca string) string {
	if ca == "" {
		return "– (System-Roots)"
	}
	certs, err := nodestore.ParseCA(ca)
	if err != nil {
		return "nicht lesbar: " + err.Error()
	}
	lines := make([]string, 0, len(certs))
	for _, cert := range certs {
		sum := sha256.Sum256(cert.Raw)
		hexSum := hex.EncodeToString(sum[:])
		parts := make([]string, 0, len(hexSum)/2)
		for i := 0; i+1 < len(hexSum); i += 2 {
			parts = append(parts, hexSum[i:i+2])
		}
		lines = append(lines, fmt.Sprintf("%s, gültig %s – %s, SHA-256 %s", cert.Subject.String(),
			cert.NotBefore.UTC().Format("2006-01-02"), cert.NotAfter.UTC().Format("2006-01-02"), strings.Join(parts, ":")))
	}
	return strings.Join(lines, "\n                ")
}

func hubIDOrNone(id string) string {
	if id == "" {
		return "noch kein Kontakt"
	}
	return id
}

// addHubCreate trägt einen local-Eintrag ein und legt den Node dafür am Hub
// derselben config an. Geprüft wird vorher alles, was der Node prüfen kann;
// scheitert das Eintragen danach doch, wird der Node am Hub wieder entfernt.
// Das Token erzeugt der Hub, es geht direkt in node.db und wird nie
// angezeigt.
func (c *command) addHubCreate(ctx context.Context, s nodestore.Store, h nodestore.Hub, hubInConfig bool) error {
	placeholder, err := ident.NewToken()
	if err != nil {
		return err
	}
	check := h
	check.Token = placeholder
	if err := nodestore.CheckHub(check, hubInConfig); err != nil {
		return err
	}
	if _, err := s.Hub(ctx, h.Name); err == nil {
		return fmt.Errorf("Hub %s %w", h.Name, nodestore.ErrExists)
	} else if !errors.Is(err, nodestore.ErrNotFound) {
		return err
	}
	hubs, err := s.Hubs(ctx)
	if err != nil {
		return err
	}
	for _, o := range hubs {
		if o.Transport == nodestore.TransportLocal {
			return fmt.Errorf("Hub %s: es gibt schon einen Eintrag mit Transport local (%s); höchstens einer je Node", h.Name, o.Name)
		}
	}
	cfg, err := c.loadConfig()
	if err != nil {
		return err
	}
	hs, err := openHubOf(ctx, cfg)
	if err != nil {
		return err
	}
	defer hs.Close()
	token, err := hs.AddNode(ctx, h.NodeName, "")
	if err != nil {
		return fmt.Errorf("am Hub: %w", err)
	}
	h.Token = token
	if err := s.AddHub(ctx, h, hubInConfig); err != nil {
		if rmErr := hs.RemoveNode(ctx, h.NodeName); rmErr != nil {
			return fmt.Errorf("%w; der am Hub angelegte Node %s ließ sich nicht wieder entfernen: %v", err, h.NodeName, rmErr)
		}
		return fmt.Errorf("%w; der am Hub angelegte Node %s ist wieder entfernt", err, h.NodeName)
	}
	fmt.Fprintf(c.stdout, "Node %s am Hub angelegt, Token direkt eingetragen (%s).\n", h.NodeName, ident.MaskToken(token))
	fmt.Fprintf(c.stdout, "Hub %s eingetragen (%s, als Node %s).\n", h.Name, describeTransport(h), h.NodeName)
	fmt.Fprintf(c.stdout, "Collections erlauben: kephalaion hub node grant %s <collection>\n", h.NodeName)
	return nil
}

// hubCheck fragt den Hub eines Eintrags mit whoami und zeigt, was er
// antwortet. Beim ersten Kontakt merkt der Node die hub_id; nennt der Hub
// eine andere als die Replica, wird sie geleert.
func (c *command) hubCheck(ctx context.Context, s nodestore.Store, alias string) error {
	h, err := s.Hub(ctx, alias)
	if err != nil {
		return err
	}
	cfg, err := c.loadConfig()
	if err != nil {
		return err
	}
	conn := &connector{ctx: ctx, cfg: cfg}
	defer conn.close()
	hub, err := conn.connect(h)
	if err != nil {
		return fmt.Errorf("Hub %s: %w", alias, err)
	}
	resp, err := hub.Whoami(ctx, contract.WhoamiRequest{Version: contract.Version,
		Auth: contract.NodeAuth{Node: h.NodeName, Token: h.Token}})
	if err != nil {
		return fmt.Errorf("Hub %s (%s): %w", alias, describeTransport(h), explainHubError(h, err))
	}
	if _, err := ulid.ParseStrict(resp.HubID); err != nil {
		return fmt.Errorf("Hub %s nennt als hub_id %q, keine ULID", alias, resp.HubID)
	}
	first := h.HubID == ""
	reset, err := replica.AdoptHubID(ctx, s, h, resp.HubID)
	if err != nil {
		return err
	}
	fmt.Fprintf(c.stdout, "Hub %s: erreichbar (%s)\n", alias, describeTransport(h))
	idNote := ""
	if first {
		idNote = " (erster Kontakt, gemerkt)"
	}
	fmt.Fprintf(c.stdout, "  hub_id:       %s%s\n", resp.HubID, idNote)
	if reset != "" {
		fmt.Fprintf(c.stdout, "  Replica:      %s\n", reset)
	}
	fmt.Fprintf(c.stdout, "  Node-Name:    %s\n", resp.Node)
	fmt.Fprintf(c.stdout, "  erlaubt:      %s\n", joinOrNone(resp.Allowed))
	var missing []string
	for _, w := range h.Collections {
		if !slices.Contains(resp.Allowed, w) {
			missing = append(missing, w)
		}
	}
	if len(missing) > 0 {
		fmt.Fprintf(c.stdout, "  gewünscht, aber nicht erlaubt: %s\n", joinOrNone(missing))
	}
	return nil
}
