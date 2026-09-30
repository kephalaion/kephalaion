package main

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/kephalaion/kephalaion/internal/config"
)

// dirEnv ist commEnv mit serve (Hub und Node), dem Account kp, der in team-x
// nur den Scope vendor/x hat, und seiner Token-Datei.
type dirEnv struct {
	*commEnv
	srv     *running
	nodeURL string
	tokenKP string
	fileKP  string
	// outputs sammelt alles, was die Kommandos ausgaben — kein Token darf
	// darin stehen.
	outputs []string
}

func newDirEnv(t *testing.T) *dirEnv {
	t.Helper()
	e := &dirEnv{commEnv: newCommEnv(t)}
	r := e.run(t, "hub", "account", "add", "kp")
	r.want(t, 0)
	e.tokenKP = tokenFrom(t, r.out)
	e.run(t, "hub", "account", "grant", "kp", "team-x", "--vendor", "x").want(t, 0)
	e.run(t, "node", "sync").want(t, 0)
	e.run(t, "config", "set", "node", "sync_interval", "0").want(t, 0)
	e.srv = startServe(t, portZero(t, e.cfg))
	e.nodeURL = "http://" + e.srv.addrs[config.Node]
	e.fileKP = e.tokenFile(t, "kp", e.tokenKP)
	return e
}

// cmd ruft node dir mit den Zugangsdaten von kp über --node.
func (e *dirEnv) cmd(t *testing.T, verb, dir, local string, args ...string) result {
	t.Helper()
	all := append([]string{"node", "dir", verb, "eigen:team-x", dir, local, "--node", e.nodeURL, "--account", "kp",
		"--token-file", e.fileKP}, args...)
	r := e.run(t, all...)
	e.outputs = append(e.outputs, r.out, r.errOut)
	return r
}

// hubDocs liest die lebenden Dokumente unter dir in team-x am Hub.
func (e *dirEnv) hubDocs(t *testing.T, dir string) map[string]string {
	t.Helper()
	docs, err := hubStore(t, e.cfg).Documents(context.Background(), "team-x", dir)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, d := range docs {
		out[d.Name] = d.Content
	}
	return out
}

// localTree liest alle regulären Dateien unter root, relativ mit '/'.
func localTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, p)
			out[filepath.ToSlash(rel)] = string(b)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func writeLocal(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// node dir push und pull über serve: Der Account nur mit dem Scope vendor/x
// legt vendor/x/ an, ein zweiter Lauf ändert nichts, Änderung, Löschung und
// Artwechsel kommen am Hub an; pull holt es in einen leeren Ordner und
// gleicht einen veränderten an; push außerhalb von vendor/ wird abgelehnt,
// ebenso ein Account nur mit write; kein Token in Ausgabe und Log.
func TestNodeDirPushPull(t *testing.T) {
	slow(t, "serve mit Hub und Node, viele Aufrufe über MCP")
	e := newDirEnv(t)
	src := filepath.Join(e.dir, "src")
	writeLocal(t, src, map[string]string{"a.md": "a", "b.md": "b", "sub/c.md": "c", ".git/HEAD": "ref"})

	r := e.cmd(t, "push", "vendor/x", src)
	r.want(t, 0, "+ vendor/x/a.md", "+ vendor/x/b.md", "+ vendor/x/sub/c.md", "push eigen:team-x vendor/x/: 3 angelegt, 0 geändert, 0 gelöscht, 0 unverändert")
	if got := e.hubDocs(t, "vendor/x"); !reflect.DeepEqual(got, map[string]string{"vendor/x/a.md": "a", "vendor/x/b.md": "b",
		"vendor/x/sub/c.md": "c"}) {
		t.Errorf("am Hub: %v", got)
	}
	e.cmd(t, "push", "vendor/x", src).want(t, 0, "0 angelegt, 0 geändert, 0 gelöscht, 3 unverändert")

	// Änderung, Löschung, Artwechsel in beide Richtungen, ein neuer Ordner.
	writeLocal(t, src, map[string]string{"a.md": "a2", "neu/d.md": "d"})
	if err := os.Remove(filepath.Join(src, "b.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(src, "sub")); err != nil {
		t.Fatal(err)
	}
	writeLocal(t, src, map[string]string{"sub": "jetzt eine Datei"})
	e.cmd(t, "push", "vendor/x", src).want(t, 0, "- vendor/x/b.md", "- vendor/x/sub/", "~ vendor/x/a.md", "+ vendor/x/sub",
		"+ vendor/x/neu/d.md", "2 angelegt, 1 geändert, 2 gelöscht, 0 unverändert")
	want := map[string]string{"vendor/x/a.md": "a2", "vendor/x/sub": "jetzt eine Datei", "vendor/x/neu/d.md": "d"}
	if got := e.hubDocs(t, "vendor/x"); !reflect.DeepEqual(got, want) {
		t.Errorf("am Hub: %v", got)
	}
	e.run(t, "hub", "doc", "get", "team-x", "vendor/x/b.md").want(t, 1)

	// --dry-run schreibt nichts.
	writeLocal(t, src, map[string]string{"e.md": "e"})
	e.cmd(t, "push", "vendor/x", src, "--dry-run").want(t, 0, "dry-run", "+ vendor/x/e.md", "1 angelegt")
	if got := e.hubDocs(t, "vendor/x"); !reflect.DeepEqual(got, want) {
		t.Errorf("dry-run schreibt: %v", got)
	}
	e.cmd(t, "push", "vendor/x", src).want(t, 0, "1 angelegt")
	want["vendor/x/e.md"] = "e"

	// Außerhalb von vendor/ ohne Verzeichnis-Scope lehnt push ab (falscher
	// Aufruf); der Hub bleibt.
	e.cmd(t, "push", "docs", src).want(t, 2, "kp hat in eigen:team-x keinen für docs/ (freigegeben: keine)")
	e.cmd(t, "push", "vendor", src).want(t, 2, "push schreibt nicht nach vendor/ selbst, nur nach vendor/<name>/")
	e.cmd(t, "push", "", src).want(t, 2, "push schreibt nie an die Wurzel einer Collection")
	e.cmd(t, "push", "vendor/x", src, "--exclude", "[").want(t, 1, "kein gültiger Glob")
	// Ein Account nur mit write kann unter vendor/ nichts ändern: forbidden
	// bricht ab, mit Bericht bis dahin.
	bob := e.tokenFile(t, "bob", e.tokens["bob"])
	srcBob := filepath.Join(e.dir, "src-bob")
	writeLocal(t, srcBob, map[string]string{"a.md": "von bob", "neu/d.md": "d", "sub": "jetzt eine Datei", "e.md": "e"})
	r = e.run(t, "node", "dir", "push", "eigen:team-x", "vendor/x", srcBob, "--node", e.nodeURL, "--account", "bob",
		"--token-file", bob)
	e.outputs = append(e.outputs, r.out, r.errOut)
	r.want(t, 1, "write vendor/x/a.md: forbidden", "Scope vendor/x fehlt", "abgebrochen")
	if got := e.hubDocs(t, "vendor/x"); !reflect.DeepEqual(got, want) {
		t.Errorf("bob hat geschrieben: %v", got)
	}
	// Ohne Recht in der Collection: nicht lesbar.
	alice := e.tokenFile(t, "alice", e.tokens["alice"])
	r = e.run(t, "node", "dir", "pull", "eigen:privat", "vendor/x", filepath.Join(e.dir, "nichts"), "--node", e.nodeURL,
		"--account", "alice", "--token-file", alice)
	e.outputs = append(e.outputs, r.out, r.errOut)
	r.want(t, 1, "nicht lesbar")

	// pull in einen leeren Ordner: derselbe Baum wie die Quelle (ohne .git).
	dst := filepath.Join(e.dir, "dst")
	e.cmd(t, "pull", "vendor/x", dst).want(t, 0, "+ vendor/x/a.md", "pull eigen:team-x vendor/x/: 4 angelegt")
	wantLocal := map[string]string{"a.md": "a2", "sub": "jetzt eine Datei", "neu/d.md": "d", "e.md": "e"}
	if got := localTree(t, dst); !reflect.DeepEqual(got, wantLocal) {
		t.Errorf("geholt: %v", got)
	}
	e.cmd(t, "pull", "vendor/x", dst).want(t, 0, "0 angelegt, 0 geändert, 0 gelöscht, 4 unverändert")
	// Verändert: geänderte, gelöschte und überzählige Datei; ohne --delete
	// bleibt die überzählige, mit --delete geht sie.
	writeLocal(t, dst, map[string]string{"a.md": "anders", "extra.md": "x"})
	if err := os.Remove(filepath.Join(dst, "e.md")); err != nil {
		t.Fatal(err)
	}
	e.cmd(t, "pull", "vendor/x", dst).want(t, 0, "~ vendor/x/a.md", "+ vendor/x/e.md", "1 angelegt, 1 geändert, 0 gelöscht, 2 unverändert")
	wantLocal["extra.md"] = "x"
	if got := localTree(t, dst); !reflect.DeepEqual(got, wantLocal) {
		t.Errorf("ohne --delete: %v", got)
	}
	e.cmd(t, "pull", "vendor/x", dst, "--delete").want(t, 0, "- vendor/x/extra.md", "0 angelegt, 0 geändert, 1 gelöscht, 4 unverändert")
	delete(wantLocal, "extra.md")
	if got := localTree(t, dst); !reflect.DeepEqual(got, wantLocal) {
		t.Errorf("mit --delete: %v", got)
	}
	// pull darf jedes Verzeichnis, auch die Wurzel.
	e.cmd(t, "pull", "", filepath.Join(e.dir, "alles"), "--dry-run").want(t, 0, "pull eigen:team-x /", "4 angelegt")

	// Falsche Aufrufe.
	e.run(t, "node", "dir", "push", "eigen:team-x", "vendor/x", src, "--node", e.nodeURL, "--token-file", e.fileKP).want(t, 2,
		"--account fehlt")
	e.run(t, "node", "dir", "push", "eigen:team-x", "vendor/x", src, "--node", e.nodeURL, "--account", "kp", "--token-file",
		e.fileKP, "--token-stdin").want(t, 2, "schließen sich aus")
	e.run(t, "node", "dir", "push", "eigen:team-x", "vendor/x", "--node", e.nodeURL).want(t, 2, "Es fehlt: <lokaler-ordner>")
	e.run(t, "node", "dir", "push", "team-x", "vendor/x", src, "--node", e.nodeURL).want(t, 2, "erwartet <hub>:<collection>")
	e.run(t, "node", "dir", "pull", "eigen:team-x", "vendor/x", dst, "--node", e.nodeURL, "--account", "kp", "--token-file",
		e.fileKP, "--last", "x").want(t, 2, "-last")
	e.run(t, "node", "dir", "push", "eigen:team-x", "vendor/x", src, "--node", e.nodeURL, "--account", "kp", "--token-file",
		e.fileKP, "--delete").want(t, 2, "-delete")
	e.run(t, "node", "dir", "--help").want(t, 0, "Exit-Codes", "--delete", "--last")
	e.run(t, "node", "dir").want(t, 2, "Aufruf:")
	// Node nicht erreichbar: nichts geschrieben, Exit 1, ohne Token.
	r = e.run(t, "node", "dir", "push", "eigen:team-x", "vendor/x", src, "--node", closedAddress(t), "--account", "kp",
		"--token-file", e.fileKP)
	e.outputs = append(e.outputs, r.out, r.errOut)
	r.want(t, 1, "nicht erreichbar")

	e.srv.stop(t)
	for _, out := range append(e.outputs, e.srv.log.String()) {
		if strings.Contains(out, "keph_") {
			t.Errorf("Token in Ausgabe oder Log:\n%s", out)
		}
	}
	log := e.srv.log.String()
	for _, want := range []string{"op=create", "op=write", "op=delete", "account=kp", "code=forbidden"} {
		if !strings.Contains(log, want) {
			t.Errorf("Log ohne %q", want)
		}
	}
}

// cancelOnCreate beendet den Lauf, sobald die erste Zeile „+ “ kommt: wie
// SIGINT mitten im Lauf — der laufende Vorgang ging zu Ende, der nächste
// beginnt nicht.
type cancelOnCreate struct {
	buf    bytes.Buffer
	cancel context.CancelFunc
	done   bool
}

func (c *cancelOnCreate) Write(p []byte) (int, error) {
	n, err := c.buf.Write(p)
	if !c.done && strings.Contains(c.buf.String(), "\n+ ") || strings.HasPrefix(c.buf.String(), "+ ") {
		c.done = true
		c.cancel()
	}
	return n, err
}

// Abbruch (SIGINT/SIGTERM über signalContext) mitten im Lauf: Exit 3, der
// Bericht sagt „abgebrochen“, und ein neuer Lauf setzt fort. --timeout
// ebenso.
func TestNodeDirInterrupted(t *testing.T) {
	e := newDirEnv(t)
	src := filepath.Join(e.dir, "src")
	files := map[string]string{}
	for _, n := range []string{"a", "b", "c", "d", "e"} {
		files[n+".md"] = n
	}
	writeLocal(t, src, files)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	old := signalContext
	signalContext = func(context.Context) (context.Context, context.CancelFunc) { return ctx, func() {} }
	t.Cleanup(func() { signalContext = old })
	out := &cancelOnCreate{cancel: cancel}
	var errOut bytes.Buffer
	code := run([]string{"node", "dir", "push", "eigen:team-x", "vendor/x", src, "--node", e.nodeURL, "--account", "kp",
		"--token-file", e.fileKP, e.c}, strings.NewReader(""), out, &errOut)
	if code != 3 || !strings.Contains(out.buf.String(), "1 angelegt") || !strings.Contains(out.buf.String(), "abgebrochen, unvollständig — erneut ausführen") {
		t.Fatalf("Exit %d:\n%s%s", code, out.buf.String(), errOut.String())
	}
	if got := e.hubDocs(t, "vendor/x"); len(got) != 1 {
		t.Errorf("nach dem Abbruch am Hub: %v", got)
	}
	signalContext = old
	e.cmd(t, "push", "vendor/x", src).want(t, 0, "4 angelegt, 0 geändert, 0 gelöscht, 1 unverändert")
	if got := e.hubDocs(t, "vendor/x"); len(got) != 5 {
		t.Errorf("nach dem Fortsetzen am Hub: %v", got)
	}

	// Höchstzeit: schon abgelaufen, bevor der erste Vorgang beginnt — nichts
	// geschieht, Exit 3.
	writeLocal(t, src, map[string]string{"f.md": "f"})
	e.cmd(t, "push", "vendor/x", src, "--timeout", "1ns").want(t, 3, "Höchstzeit erreicht, unvollständig")
	if got := e.hubDocs(t, "vendor/x"); len(got) != 5 {
		t.Errorf("trotz Höchstzeit geschrieben: %v", got)
	}
	e.cmd(t, "push", "vendor/x", src, "--timeout", "-1s").want(t, 2, "--timeout")
}

// Anmeldung ohne Optionen: die Adresse aus listen der config, der Account
// als einzige Token-Datei unter tokens/<hub>/ neben der config des Users;
// bei mehreren nennt der Fehler sie, --account wählt; .pending zählt nicht.
func TestNodeDirCredentials(t *testing.T) {
	e := newDirEnv(t)
	src := filepath.Join(e.dir, "src")
	writeLocal(t, src, map[string]string{"a.md": "a"})
	// Eine config mit dem listen von serve.
	cfg, _, err := config.Load(e.cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Node.Listen = e.srv.addrs[config.Node]
	cfgPath := filepath.Join(e.dir, "listen.yaml")
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	userCfg, err := config.UserPath()
	if err != nil {
		t.Fatal(err)
	}
	tokens := filepath.Join(filepath.Dir(userCfg), "tokens", "eigen")
	if err := os.MkdirAll(tokens, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tokens, "kp.token"), []byte(e.tokenKP+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tokens, "kp.token.pending"), []byte("keph_pending\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	push := func(args ...string) result {
		t.Helper()
		r := runT(t, append([]string{"node", "dir", "push", "eigen:team-x", "vendor/x", src, "--dry-run", "--config", cfgPath},
			args...)...)
		if strings.Contains(r.out+r.errOut, "keph_") {
			t.Errorf("Token in der Ausgabe:\n%s%s", r.out, r.errOut)
		}
		return r
	}
	push().want(t, 0, "+ vendor/x/a.md")
	if err := os.WriteFile(filepath.Join(tokens, "bob.token"), []byte(e.tokens["bob"]+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	push().want(t, 1, "mehrere Token-Dateien", "bob, kp", "--account wählt")
	push("--account", "kp").want(t, 0, "+ vendor/x/a.md")
	push("--account", "niemand").want(t, 1, "niemand.token")
	// Ohne Token-Datei für den Hub: der Hinweis nennt das Verzeichnis.
	runT(t, "node", "dir", "push", "fern:team-x", "vendor/x", src, "--dry-run", "--config", cfgPath).want(t, 1,
		"keine Token-Datei unter", filepath.Join("tokens", "fern"))
	// Ohne Node in der config: --node ist nötig.
	hubOnly := filepath.Join(e.dir, "hub-only.yaml")
	if err := config.Save(hubOnly, config.Config{Hub: cfg.Hub}); err != nil {
		t.Fatal(err)
	}
	runT(t, "node", "dir", "push", "eigen:team-x", "vendor/x", src, "--dry-run", "--config", hubOnly, "--account", "kp").want(t, 1,
		"kein Node in der config", "--node")
	// --node ohne http://.
	runT(t, "node", "dir", "push", "eigen:team-x", "vendor/x", src, "--node", "localhost:1", "--account", "kp", "--config", cfgPath).want(t, 1,
		"erwartet http://")
	// --token-stdin.
	runIn(t, e.tokenKP+"\n", "node", "dir", "push", "eigen:team-x", "vendor/x", src, "--dry-run", "--account", "kp",
		"--token-stdin", "--config", cfgPath).want(t, 0, "+ vendor/x/a.md")
}

// push in freigegebene Verzeichnisse (Task 021): Ein Account nur mit den
// Verzeichnis-Scopes docs und a/b pusht nach docs/ und docs/sub, auch über
// Dokumente eines anderen Users. Die Wurzel, docs2, das Elternverzeichnis a
// und ein anderes Verzeichnis lehnt push ab — Exit 2, nichts geschrieben, auch
// mit --dry-run —, ebenso einen Account nur mit write. Die Prüfung liest die
// Replica des Nodes: grant und Entzug wirken erst nach node sync.
func TestNodeDirPushDirScope(t *testing.T) {
	slow(t, "serve mit Hub und Node, viele Aufrufe über MCP")
	e := newDirEnv(t)
	r := e.run(t, "hub", "account", "add", "pusher")
	r.want(t, 0)
	file := e.tokenFile(t, "pusher", tokenFrom(t, r.out))
	e.run(t, "hub", "account", "grant", "pusher", "team-x", "--dir", "docs", "--dir", "a/b").want(t, 0,
		"team-x erlaubt (read, dir a/b/, dir docs/)")
	for name, content := range map[string]string{"docs/fremd.md": "vom Admin", "docs/sub/alt.md": "alt", "docs2/x.md": "x",
		"a/x.md": "x", "notes/x.md": "x", "wurzel.md": "w"} {
		e.runIn(t, content, "hub", "doc", "put", "team-x", name).want(t, 0)
	}
	e.run(t, "node", "sync").want(t, 0)
	push := func(dir, src string, args ...string) result {
		t.Helper()
		all := append([]string{"node", "dir", "push", "eigen:team-x", dir, src, "--node", e.nodeURL, "--account", "pusher",
			"--token-file", file}, args...)
		r := e.run(t, all...)
		e.outputs = append(e.outputs, r.out, r.errOut)
		return r
	}
	src := filepath.Join(e.dir, "src")
	writeLocal(t, src, map[string]string{"a.md": "a", "sub/b.md": "b"})

	// Abgelehnt, bevor etwas geschrieben wird — mit und ohne --dry-run.
	before := e.hubDocs(t, "")
	for _, c := range []struct{ dir, want string }{
		{"", "push schreibt nie an die Wurzel einer Collection"},
		{"docs2", "pusher hat in eigen:team-x keinen für docs2/ (freigegeben: a/b/, docs/)"},
		{"a", "keinen für a/ (freigegeben: a/b/, docs/)"},
		{"notes", "keinen für notes/"},
		{"do", "keinen für do/"},
	} {
		for _, extra := range [][]string{nil, {"--dry-run"}} {
			r := push(c.dir, src, extra...)
			r.want(t, 2, c.want)
			if c.dir != "" {
				r.want(t, 2, "kephalaion hub account grant pusher team-x --dir <pfad>", "kephalaion node sync eigen")
			}
			if strings.Contains(r.out, "+ ") || strings.Contains(r.out, "dry-run") {
				t.Errorf("push %q %v meldet Vorgänge:\n%s", c.dir, extra, r.out)
			}
		}
	}
	if got := e.hubDocs(t, ""); !reflect.DeepEqual(got, before) {
		t.Errorf("abgelehnt, aber geschrieben: %v", got)
	}

	// docs/: ersetzt den Inhalt, auch die Dokumente des Admins; --dry-run
	// zuerst schreibt nichts.
	push("docs", src, "--dry-run").want(t, 0, "dry-run", "- docs/fremd.md", "+ docs/a.md")
	if got := e.hubDocs(t, ""); !reflect.DeepEqual(got, before) {
		t.Errorf("dry-run schreibt: %v", got)
	}
	push("docs", src).want(t, 0, "- docs/fremd.md", "- docs/sub/alt.md", "+ docs/a.md", "+ docs/sub/b.md",
		"push eigen:team-x docs/: 2 angelegt, 0 geändert, 2 gelöscht")
	if got := e.hubDocs(t, "docs"); !reflect.DeepEqual(got, map[string]string{"docs/a.md": "a", "docs/sub/b.md": "b"}) {
		t.Errorf("docs/ am Hub: %v", got)
	}
	// Darunter und im geschachtelten Scope ebenso.
	srcSub := filepath.Join(e.dir, "src-sub")
	writeLocal(t, srcSub, map[string]string{"c.md": "c"})
	push("docs/sub/", srcSub).want(t, 0, "- docs/sub/b.md", "+ docs/sub/c.md")
	push("a/b", srcSub).want(t, 0, "+ a/b/c.md")
	if got := e.hubDocs(t, "a"); !reflect.DeepEqual(got, map[string]string{"a/x.md": "x", "a/b/c.md": "c"}) {
		t.Errorf("a/ am Hub: %v", got)
	}

	// Nur write (bob): nach docs/ abgelehnt, nichts geschrieben.
	bob := e.tokenFile(t, "bob", e.tokens["bob"])
	r = e.run(t, "node", "dir", "push", "eigen:team-x", "docs", src, "--node", e.nodeURL, "--account", "bob", "--token-file", bob)
	e.outputs = append(e.outputs, r.out, r.errOut)
	r.want(t, 2, "bob hat in eigen:team-x keinen für docs/ (freigegeben: keine)")

	// Entzug: bis zum Abgleich kennt der Node noch den alten Stand — der Hub
	// lehnt dann selbst ab —, danach lehnt schon die Kommandozeile ab.
	e.run(t, "hub", "account", "grant", "pusher", "team-x").want(t, 0, "team-x erlaubt (read)")
	writeLocal(t, src, map[string]string{"neu.md": "n"})
	push("docs", src).want(t, 1, "forbidden", "write fehlt")
	e.run(t, "node", "sync").want(t, 0)
	push("docs", src).want(t, 2, "keinen für docs/ (freigegeben: keine)")
	push("docs", src, "--dry-run").want(t, 2, "keinen für docs/")
	// Wieder erteilt: erst nach dem Abgleich.
	e.run(t, "hub", "account", "grant", "pusher", "team-x", "--dir", "docs").want(t, 0)
	push("docs", src).want(t, 2, "Eine eben erteilte Freigabe kennt der Node erst nach dem Abgleich")
	e.run(t, "node", "sync").want(t, 0)
	push("docs", src).want(t, 0, "+ docs/neu.md")
	if got := e.hubDocs(t, "docs"); !reflect.DeepEqual(got, map[string]string{"docs/a.md": "a", "docs/neu.md": "n",
		"docs/sub/b.md": "b"}) {
		t.Errorf("docs/ am Hub: %v", got)
	}
	// vendor/<name> wie bisher: ohne den Scope vendor/x verboten am Hub.
	push("vendor/x", src).want(t, 1, "Scope vendor/x fehlt")

	e.srv.stop(t)
	for _, out := range append(e.outputs, e.srv.log.String()) {
		if strings.Contains(out, "keph_") {
			t.Errorf("Token in Ausgabe oder Log:\n%s", out)
		}
	}
}
