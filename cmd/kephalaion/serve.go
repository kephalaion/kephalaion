package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/kephalaion/kephalaion/internal/buildinfo"
	"github.com/kephalaion/kephalaion/internal/config"
	"github.com/kephalaion/kephalaion/internal/contract"
	"github.com/kephalaion/kephalaion/internal/contract/httpapi"
	"github.com/kephalaion/kephalaion/internal/hub/gui"
	"github.com/kephalaion/kephalaion/internal/hub/replication"
	hubstore "github.com/kephalaion/kephalaion/internal/hub/store"
	"github.com/kephalaion/kephalaion/internal/loopback"
	"github.com/kephalaion/kephalaion/internal/node/mcpnode"
	nodestore "github.com/kephalaion/kephalaion/internal/node/store"
	"github.com/kephalaion/kephalaion/internal/reqlog"
	"github.com/kephalaion/kephalaion/internal/upgrade"
)

const serveUsage = `Aufruf:
  kephalaion serve [--config pfad]

Der Dienst: startet je eingerichteter Rolle einen HTTP-Listener auf ihrem
listen aus der config — beide Rollen in einem Prozess, wenn beide
eingerichtet sind — und läuft, bis SIGINT oder SIGTERM ihn beendet.

  hub    an der Wurzel (/) für einen Browser (Accept mit text/html) die
         Weboberfläche: Sie zeigt dem am Proxy angemeldeten User (Header
         X-User) alle seine Accounts mit ihren Rechten, ohne Token; ihre Teile
         liegen unter /gui/. Ohne Proxy setzt niemand X-User, die Seite zeigt
         dann nur „keine Anmeldung des Proxys“ (die Accounts nennt kephalaion
         hub account list --user <user>). Der Hub glaubt jedem X-User auf
         Loopback: Ein Prozess dieses Rechners sieht so Accounts und Rechte
         eines beliebigen Users, keine Tokens. Für alles andere (curl) dort
         eine kurze Begrüßung mit der Version. Der Vertrag für Nodes unter
         /hub: POST /hub/v1/whoami, /hub/v1/rotate, /hub/v1/sync und die
         Schreibvorgänge /hub/v1/create, /hub/v1/write, /hub/v1/delete,
         /hub/v1/rename — die Adresse eines Hub-Eintrags endet deshalb auf
         /hub (http://localhost:7434/hub). /v1/… an der Wurzel antwortet 404
         mit dem Hinweis auf /hub; eine Umleitung gibt es nie, auch nicht
         hinter einem Proxy mit Präfix
  node   MCP für Clients unter /mcp: whoami, list, read und changes aus der
         Replica, create, write, delete und rename über den Hub — ist er
         nicht erreichbar, wird nichts gespeichert, gelesen wird weiter

Beide lauschen nur auf diesem Rechner (127.0.0.1, ::1, localhost):
Klartext-HTTP verlässt den Rechner nicht. Ein anderes listen bricht den
Start ab. Nodes anderer Rechner erreichen den Hub über einen Reverse-Proxy
auf seinem Rechner, der TLS beendet und an den Hub auf Loopback weiterreicht
(Transport https, siehe docs/installation.md). Beide beantworten nur
Anfragen, deren Host dieser Rechner mit dem eigenen Port ist, sonst 403; der
Proxy setzt Host deshalb auf die Loopback-Adresse des Hubs, ein Tunnel geht
nur mit gleichem Port (ssh -L 7434:localhost:7434).

Eine Sperre auf einer Datei neben jeder Datenbank (<db>.lock) verhindert einen
zweiten serve auf derselben Rolle; die übrigen Kommandos laufen daneben wie
immer. kephalaion status zeigt, ob serve läuft.

Als Node gleicht serve seine Replicas selbst ab: beim Start je Hub-Eintrag,
danach im Abstand sync_interval aus den settings des Nodes (Standard 30s,
kephalaion config set node sync_interval 1m; 0 schaltet ab). Die Hub-Einträge
liest jede Runde neu, node hub add|rm wirkt ohne Neustart; ein langsamer Hub
hält die anderen nicht auf. Transport local nimmt den Hub desselben serve,
http und https den Hub unter seiner Adresse; ssh wird noch übergangen.
Erfolg und letzter Fehler je Hub stehen in node.db (kephalaion status).
kephalaion node sync läuft daneben wie immer. Nach einem Schreibvorgang über
MCP (create, write, delete, rename) — gelungen oder mit unklarem Ausgang —
gleicht serve den Hub außer der Reihe ab, auch bei sync_interval 0.

Als Node fragt serve außerdem höchstens einmal am Tag bei GitHub nach dem
neuesten Release (nach einem Fehler frühestens nach einer Stunde) und gibt
die Antwort im Werkzeug whoami mit (update) — aus seiner Sicht: global also
der Weg des Verwalters. Die Antwort bleibt im Speicher.

Logs gehen nach stderr: eine Zeile je Anfrage mit Methode, Pfad, Status,
Dauer und den Namen von Node bzw. Account, hinter einem Proxy die Adresse
des Aufrufers aus X-Forwarded-For (via), bei einem Schreibvorgang über MCP
dazu Vorgang, Hub und Fehlercode — nie ein Token, nie ein Inhalt. Vom
Abgleich im Hintergrund eine Zeile, wenn Zeilen kamen, eine beim ersten Fehler
eines Hubs und wenn sich die Art des Fehlers ändert, und eine, wenn es wieder
geht.

Optionen:
  --config pfad   Ort der config (siehe kephalaion hub init --help)
`

// shutdownGrace ist die Frist, in der beim Beenden der Abgleich im
// Hintergrund abbricht und danach laufende Anfragen noch fertig werden — je
// einmal. Tests stellen sie kürzer.
var shutdownGrace = 10 * time.Second

// serveReady meldet, dass alle Listener stehen; Tests setzen es.
var serveReady func(addrs map[config.Role]string)

// serveBackground bekommt den Kanal, der schließt, wenn der Abgleich im
// Hintergrund zu Ende ist — auch nach dem Ende von serve; Tests setzen es.
var serveBackground func(done <-chan struct{})

func runServe(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("serve", serveUsage, stderr)
	cfgFlag := fs.String("config", "", "")
	if _, code, ok := parseFlags(fs, args, serveUsage, 0, stderr); !ok {
		return code
	}
	fail := func(err error) int {
		fmt.Fprintf(stderr, "serve: %v\n", err)
		return 1
	}
	loc, err := config.Locate(*cfgFlag)
	if err != nil {
		return fail(err)
	}
	cfg, _, err := config.Load(loc.Path)
	if err != nil {
		return fail(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := serve(ctx, cfg, loc.System(), reqlog.New(stderr), serveReady); err != nil {
		return fail(err)
	}
	return 0
}

// checkServeListen prüft ein listen für serve: host:port auf Loopback. Port 0
// (ein freier Port) lässt serve zu, die config nicht — Tests brauchen ihn.
func checkServeListen(r config.Role, addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || port == "" {
		return fmt.Errorf("%s: listen %q: erwartet host:port", r, addr)
	}
	if !loopback.IsHost(host) {
		return fmt.Errorf("%s: listen %s: serve lauscht nur auf diesem Rechner (127.0.0.1, ::1 oder localhost) — "+
			"Klartext-HTTP verlässt den Rechner nicht; Nodes anderer Rechner erreichen den Hub über einen "+
			"Reverse-Proxy (https, siehe docs/installation.md)", r, addr)
	}
	return nil
}

// role ist eine laufende Rolle: Datenbank, Sperre, Listener, Server.
type role struct {
	name   config.Role
	lock   *os.File
	close  func()
	ln     net.Listener
	server *http.Server
	// hub bzw. nodes ist der geöffnete Store der Rolle.
	hub   hubstore.Store
	nodes nodestore.Store
}

// serve startet die Listener aller eingerichteten Rollen und läuft, bis ctx
// endet; dann beendet es sie mit Frist. Bevor irgendetwas lauscht, ist alles
// geprüft, gesperrt und geöffnet. system sagt, ob die globale config gilt
// (für den Weg des Upgrades in whoami). ready bekommt die tatsächlichen
// Adressen.
func serve(ctx context.Context, cfg config.Config, system bool, log *reqlog.Logger,
	ready func(map[config.Role]string)) error {
	if cfg.Empty() {
		return errors.New("keine Rolle eingerichtet; zuerst kephalaion hub init oder kephalaion node init")
	}
	for _, r := range config.Roles {
		if cfg.Section(r) != nil {
			if err := checkServeListen(r, cfg.Listen(r)); err != nil {
				return err
			}
		}
	}
	// Als Node fragt serve höchstens einmal am Tag nach einer neuen Version
	// und gibt die Antwort in whoami mit; bis dahin ohne Frage an GitHub.
	var updates *upgrade.Watcher
	if cfg.Node != nil {
		u := newUpgrader(io.Discard)
		u.System = system
		updates = upgrade.NewWatcher(u.Report, u.Local())
		updates.Changed = func(r upgrade.Report) { log.Printf("%s", r.Summary()) }
	}
	var roles []*role
	defer func() {
		for _, rl := range roles {
			rl.close()
			_ = rl.lock.Close()
		}
	}()
	for _, r := range config.Roles {
		sec := cfg.Section(r)
		if sec == nil {
			continue
		}
		rl, err := startRole(ctx, cfg, r, sec, updates)
		if err != nil {
			return err
		}
		roles = append(roles, rl)
	}
	// Der Abgleich im Hintergrund, wenn serve den Node trägt; local nimmt
	// den Hub desselben serve. Die Werkzeuge des Nodes, die schreiben, stoßen
	// ihn an; sie erreichen den Hub über denselben connector.
	bgCtx, stopBg := context.WithCancel(ctx)
	defer stopBg()
	var hub hubstore.Store
	var nodes nodestore.Store
	for _, rl := range roles {
		if rl.hub != nil {
			hub = rl.hub
		}
		if rl.nodes != nil {
			nodes = rl.nodes
		}
	}
	var bg *backgroundSync
	if nodes != nil {
		bg = newBackgroundSync(bgCtx, nodes, cfg, hub, log)
		for _, rl := range roles {
			if rl.nodes != nil {
				rl.server.Handler = newNodeHandler(rl.nodes, updates.Report, nodeHubLink(cfg, hub, bg.kick))
			}
		}
	}
	addrs := map[config.Role]string{}
	for _, rl := range roles {
		ln, err := net.Listen("tcp", cfg.Listen(rl.name))
		if err != nil {
			for _, o := range roles {
				if o.ln != nil {
					_ = o.ln.Close()
				}
			}
			return fmt.Errorf("%s: %w", rl.name, err)
		}
		rl.ln = ln
		addrs[rl.name] = ln.Addr().String()
		rl.server.Handler = log.Middleware(string(rl.name), rl.server.Handler)
	}

	errc := make(chan error, len(roles))
	var wg sync.WaitGroup
	for _, rl := range roles {
		log.Printf("%s lauscht auf %s", rl.name, addrs[rl.name])
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := rl.server.Serve(rl.ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errc <- fmt.Errorf("%s: %w", rl.name, err)
			}
		}()
	}
	bgDone := make(chan struct{})
	if bg != nil {
		go func() {
			defer close(bgDone)
			var wg sync.WaitGroup
			wg.Add(1)
			go func() {
				defer wg.Done()
				updates.Run(bgCtx)
			}()
			bg.run()
			wg.Wait()
		}()
	} else {
		close(bgDone)
	}
	if serveBackground != nil {
		serveBackground(bgDone)
	}
	if ready != nil {
		ready(addrs)
	}
	var result error
	select {
	case <-ctx.Done():
		log.Printf("beende (Frist %s)", shutdownGrace)
	case result = <-errc:
	}
	// Ein laufender Abgleich bricht ab; geschrieben ist nur, was als ganze
	// Seite ankam. Die Stores bleiben offen, bis er fertig ist — höchstens
	// shutdownGrace: Ein Hub, der den Abbruch nicht beachtet, hält das Beenden
	// nicht auf. Ein Abgleich, der danach noch läuft, bekommt Fehler von den
	// geschlossenen Stores und hält wegen des abgebrochenen ctx nichts in
	// hub_sync fest; der Prozess endet ohnehin.
	stopBg()
	bgWait := time.NewTimer(shutdownGrace)
	select {
	case <-bgDone:
		bgWait.Stop()
	case <-bgWait.C:
		log.Printf("Abgleich im Hintergrund endet nicht in der Frist %s; beende trotzdem", shutdownGrace)
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	for _, rl := range roles {
		if err := rl.server.Shutdown(shutdownCtx); err != nil {
			_ = rl.server.Close()
		}
	}
	wg.Wait()
	if result == nil {
		log.Printf("beendet")
	}
	return result
}

// startRole nimmt die Sperre einer Rolle, öffnet ihre Datenbank und baut
// ihren Server; lauschen tut es noch nicht.
func startRole(ctx context.Context, cfg config.Config, r config.Role, sec *config.Section,
	updates *upgrade.Watcher) (*role, error) {
	addr, err := config.ParseDB(sec.DB)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", r, err)
	}
	if addr.Kind != config.SQLite {
		return nil, fmt.Errorf("%s: Datenbankart %q: %w", r, addr.Kind, config.ErrUnsupported)
	}
	lock, err := takeLock(lockPath(addr.Path))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", r, err)
	}
	rl := &role{name: r, lock: lock}
	switch r {
	case config.Hub:
		st, err := hubstore.Open(ctx, addr)
		if err != nil {
			_ = lock.Close()
			return nil, fmt.Errorf("hub: %w", err)
		}
		rl.close = func() { _ = st.Close() }
		rl.hub = st
		rl.server = &http.Server{
			// Host wie am Node: dieser Rechner mit dem eigenen Port, sonst 403.
			Handler:           loopback.Guard(newHubHandler(st, hubViewer())),
			ReadHeaderTimeout: httpapi.ReadHeaderTimeout,
			ReadTimeout:       httpapi.ReadTimeout,
			WriteTimeout:      httpapi.WriteTimeout,
			IdleTimeout:       httpapi.IdleTimeout,
		}
	case config.Node:
		st, err := nodestore.Open(ctx, addr)
		if err != nil {
			_ = lock.Close()
			return nil, fmt.Errorf("node: %w", err)
		}
		rl.close = func() { _ = st.Close() }
		rl.nodes = st
		// Den Handler setzt serve, wenn der Abgleich im Hintergrund steht.
		rl.server = &http.Server{
			ReadHeaderTimeout: httpapi.ReadHeaderTimeout,
			IdleTimeout:       httpapi.IdleTimeout,
		}
	}
	return rl, nil
}

// newNodeHandler ist der Eingang des Nodes für Clients: MCP unter /mcp,
// daneben die Routen für Accounts (/account/rotate, /account/check), die
// über link zum Hub gehen wie die Werkzeuge, die schreiben.
func newNodeHandler(st nodestore.Store, update func() upgrade.Report, link mcpnode.HubLink) http.Handler {
	return mcpnode.NewHandler(st, buildinfo.Get().Version, update, link)
}

// hubPath ist der Ort des Vertrags am Hub-Listener; die Adresse eines
// Hub-Eintrags endet darauf.
const hubPath = "/hub"

// guiPath ist der Ort der Teile der Weboberfläche am Hub-Listener: ihre
// Dateien und ihr Eingang. Die Seite selbst liegt an der Wurzel und nennt
// sie relativ (gui/app.js), damit sie auch unter dem Präfix eines Proxys
// stimmen.
const guiPath = "/gui"

// hubViewer ist die eine Stelle, an der entsteht, wem die Weboberfläche des
// Hubs den angemeldeten User glaubt (gui.Viewer): vorerst X-User der
// Anmeldung des Proxys ohne weitere Prüfung (gui.HeaderViewer). Ein geheimer
// Header des Proxys aus der config kommt hier dazu (eigene Task); Eingang und
// Seite bleiben dabei, wie sie sind.
func hubViewer() gui.Viewer { return gui.HeaderViewer }

// newHubHandler ist der Hub-Listener ohne die Host-Prüfung (die legt
// startRole außen herum): Das Binary ordnet seine Teile selbst, ein Proxy
// davor nimmt nur seinen Präfix weg.
//
//   - An der Wurzel entscheidet Accept: Nennt er text/html (ein Browser),
//     kommt die Weboberfläche (internal/hub/gui), sonst eine kurze Begrüßung
//     mit der Version (text/plain, für curl) — beide mit Vary: Accept.
//   - Unter /gui/ die Teile der Seite: ihre Dateien (gui.Files: /gui/app.js,
//     /gui/style.css, /gui/icon.svg) und ihr Eingang GET /gui/api/user, der
//     die Accounts des angemeldeten Users nennt — wem er glaubt, sagt viewer;
//     er ist kein Teil des Vertrags. /gui, /gui/ und alles andere darunter
//     404.
//   - An /hub und /hub/ ein kurzer Text ohne Version (diese Route liegt nach
//     außen ohne Anmeldung), unter /hub/ der Handler des Vertrags, der
//     /v1/… sieht.
//   - /v1/… an der Wurzel ist die alte Adresse ohne /hub und bekommt 404 mit
//     dem Hinweis, alles andere 404 — beides text/plain, ohne Vertragsform:
//     Die Wurzel gehört dem Binary, nicht dem Hub, und der Client zählt ein
//     404 ohne Vertragsform als nicht erreicht.
//
// Nie ein 3xx: /hub, /gui und /v1 sind eigens registriert (sonst leitete der
// Mux GET /hub mit 301 auf /hub/ um, ebenso /gui), und ein Pfad, den der Mux
// bereinigen würde, wird vorher mit 404 beantwortet — eine absolute Location
// ohne den Präfix des Proxys ginge ins Leere.
func newHubHandler(st hubstore.Store, viewer gui.Viewer) http.Handler {
	version := buildinfo.Get().Version
	mux := http.NewServeMux()
	page := gui.NewPage(version)
	mux.HandleFunc("/{$}", func(w http.ResponseWriter, r *http.Request) {
		// Seite oder Begrüßung hängt an Accept; ein Cache dazwischen muss das
		// wissen.
		w.Header().Add("Vary", "Accept")
		if (r.Method == http.MethodGet || r.Method == http.MethodHead) && gui.AcceptsHTML(r) {
			page.ServeHTTP(w, r)
			return
		}
		hubText(w, r, http.StatusOK, "Kephalaion "+version+", Rolle hub.\n"+
			"Der Hub antwortet unter "+hubPath+"/v1/<vorgang>.")
	})
	notFound := func(w http.ResponseWriter, r *http.Request) {
		hubText(w, r, http.StatusNotFound, "unbekannter Pfad "+r.URL.Path)
	}
	for _, name := range gui.Files {
		mux.Handle(guiPath+"/"+name, gui.NewFile(name))
	}
	mux.Handle(guiPath+"/api/user", gui.NewUser(st, viewer))
	mux.HandleFunc(guiPath, notFound)
	mux.HandleFunc(guiPath+"/", notFound)
	short := func(w http.ResponseWriter, r *http.Request) {
		hubText(w, r, http.StatusOK, "Kephalaion Hub. Nodes: POST "+hubPath+"/v1/<vorgang>")
	}
	mux.HandleFunc(hubPath, short)
	mux.HandleFunc(hubPath+"/{$}", short)
	mux.Handle(hubPath+"/", http.StripPrefix(hubPath, httpapi.NewHandler(replication.New(st))))
	oldAddress := func(w http.ResponseWriter, r *http.Request) {
		hubText(w, r, http.StatusNotFound, "unbekannter Pfad "+r.URL.Path+": der Vertrag liegt unter "+hubPath+
			"/v1/… — fehlt "+hubPath+" am Ende der Adresse des Hub-Eintrags?")
	}
	mux.HandleFunc("/v1", oldAddress)
	mux.HandleFunc("/v1/", oldAddress)
	mux.HandleFunc("/", notFound)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cleanRequestPath(r.URL.Path) != r.URL.Path {
			notFound(w, r)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

// cleanRequestPath bereinigt einen Pfad so, wie http.ServeMux es vor dem
// Abgleich tut (path.Clean; ein Schrägstrich am Ende bleibt). Weicht das
// Ergebnis vom Pfad ab — doppelte Schrägstriche, Segmente . oder .. —,
// antwortete der Mux mit 301 auf den bereinigten Pfad.
func cleanRequestPath(p string) string {
	if p == "" {
		return "/"
	}
	if p[0] != '/' {
		p = "/" + p
	}
	np := path.Clean(p)
	if strings.HasSuffix(p, "/") && np != "/" {
		np += "/"
	}
	return np
}

// hubText antwortet mit einer kurzen Textnachricht: text/plain, nicht zu
// cachen, ohne Body bei HEAD. Anderes als GET und HEAD ist an diesen Orten
// 405 — bei einem 404 bleibt es beim 404, gleich welcher Methode.
func hubText(w http.ResponseWriter, r *http.Request, status int, text string) {
	h := w.Header()
	h.Set("Content-Type", "text/plain; charset=utf-8")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "no-store")
	if status == http.StatusOK && r.Method != http.MethodGet && r.Method != http.MethodHead {
		h.Set("Allow", "GET, HEAD")
		status, text = http.StatusMethodNotAllowed, "nur GET"
	}
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		_, _ = io.WriteString(w, text+"\n")
	}
}

// nodeHubLink ist der Weg der Werkzeuge des Nodes, die schreiben, zum Hub:
// derselbe connector wie beim Abgleich — local nimmt hub, den Hub desselben
// serve, wenn er ihn trägt —, und kick stößt den Abgleich eines Eintrags an.
// Ein Transport, den der connector noch nicht kann (ssh), kommt als
// mcpnode.ErrUnsupported an; jeder andere Fehler beim Verbinden heißt für
// das Werkzeug: Hub nicht erreichbar.
func nodeHubLink(cfg config.Config, hub hubstore.Store, kick func(nodestore.Hub)) mcpnode.HubLink {
	return mcpnode.HubLink{
		Connect: func(ctx context.Context, h nodestore.Hub) (contract.Hub, func(), error) {
			conn := &connector{ctx: ctx, cfg: cfg, hub: hub}
			c, err := conn.connect(h)
			if err != nil {
				conn.close()
				if errors.Is(err, errTransportUnsupported) {
					err = fmt.Errorf("%w: %v", mcpnode.ErrUnsupported, err)
				}
				return nil, nil, err
			}
			return c, conn.close, nil
		},
		Sync: kick,
	}
}

// lockPath ist die Sperrdatei neben einer Datenbank: <db>.lock.
func lockPath(dbPath string) string { return dbPath + ".lock" }

// takeLock nimmt die Sperre für serve, ohne zu warten: flock exklusiv auf der
// Sperrdatei, die dafür angelegt wird und liegen bleibt. Die Sperre hält,
// solange die Datei offen ist, und fällt mit dem Prozess.
func takeLock(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("Sperrdatei %s: %w", path, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("serve läuft schon für diese Rolle (Sperre %s)", path)
		}
		return nil, fmt.Errorf("Sperrdatei %s: %w", path, err)
	}
	return f, nil
}

// serveRunning prüft ohne zu warten, ob ein serve die Sperre einer Datenbank
// hält. Es legt keine Sperrdatei an.
func serveRunning(dbPath string) (bool, error) {
	f, err := os.Open(lockPath(dbPath))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer f.Close()
	err = syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return false, nil
}
