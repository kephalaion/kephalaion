package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kephalaion/kephalaion/internal/buildinfo"
	"github.com/kephalaion/kephalaion/internal/config"
	"github.com/kephalaion/kephalaion/internal/contract/httpapi"
	"github.com/kephalaion/kephalaion/internal/hub/replication"
	"github.com/kephalaion/kephalaion/internal/ident"
	nodestore "github.com/kephalaion/kephalaion/internal/node/store"
	"github.com/kephalaion/kephalaion/internal/service"
	"github.com/kephalaion/kephalaion/internal/upgrade"
)

// autoEnv ist ein Rechner für upgrade --auto: ein nachgespieltes GitHub mit
// dem Release v0.2.0, das installierte Binary in einem eigenen Verzeichnis,
// systemd nachgespielt und die Teile nach außen eingespeist — die Frage an
// die Hubs (hubs), die Probe des Nodes (probe) und das Umbenennen (rename).
// Das neue Binary ist ein Skript: Es schreibt jeden Aufruf samt
// KEPHALAION_CONFIG nach calls.log und antwortet mit den Dateien stdout,
// stderr und exit in seinem Verzeichnis — so läuft die Probe über den echten
// Aufruf.
type autoEnv struct {
	dir     string
	exe     string
	newBin  []byte
	script  string // Verzeichnis mit calls.log, stdout, stderr, exit
	srv     *httptest.Server
	sums    []byte // nil: die passende SHA256SUMS
	sd      *fakeSystemd
	cfgPath string
	nodeDB  string
	// hubs ist die Antwort je Alias auf die Frage nach dem Release.
	hubs    map[string]hubAnswer
	onAsk   func()
	asked   []string
	probe   func(listen string) (nodeInfo, error)
	rename  func(oldpath, newpath string) error
	renames []string
}

type hubAnswer struct {
	release string
	err     error
}

// newAutoEnv legt den Rechner an; installed ist die Version des
// installierten Binarys. Eine Rolle gibt es noch nicht.
func newAutoEnv(t *testing.T, installed string) *autoEnv {
	t.Helper()
	dir := isolate(t)
	withSudo(t, false)
	e := &autoEnv{dir: dir, hubs: map[string]hubAnswer{}, script: filepath.Join(dir, "neu")}
	e.sd = withSystemd(t, dir)
	if err := os.MkdirAll(e.script, 0o755); err != nil {
		t.Fatal(err)
	}
	e.newBin = []byte("#!/bin/sh\n" +
		"printf '%s|%s\\n' \"$*\" \"$KEPHALAION_CONFIG\" >> '" + e.script + "/calls.log'\n" +
		"[ -f '" + e.script + "/stderr' ] && cat '" + e.script + "/stderr' >&2\n" +
		"[ -f '" + e.script + "/stdout' ] && cat '" + e.script + "/stdout'\n" +
		"exit \"$(cat '" + e.script + "/exit' 2>/dev/null || echo 0)\"\n")
	e.answer(t, `{"result":"ok","version":"v0.2.0","roles":{}}`, "", 0)

	name := upgrade.AssetName("linux", "amd64")
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/kephalaion/kephalaion/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"tag_name":"v0.2.0","assets":[{"name":%q,"browser_download_url":"%s/d/%s"},`+
			`{"name":"SHA256SUMS","browser_download_url":"%s/d/SHA256SUMS"}]}`, name, e.srv.URL, name, e.srv.URL)
	})
	mux.HandleFunc("/d/"+name, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(e.newBin) })
	mux.HandleFunc("/d/SHA256SUMS", func(w http.ResponseWriter, r *http.Request) {
		if e.sums != nil {
			_, _ = w.Write(e.sums)
			return
		}
		sum := sha256.Sum256(e.newBin)
		fmt.Fprintf(w, "%s  %s\n", hex.EncodeToString(sum[:]), name)
	})
	e.srv = httptest.NewServer(mux)
	t.Cleanup(e.srv.Close)

	bin, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e.exe = filepath.Join(bin, "kephalaion")
	if err := os.WriteFile(e.exe, []byte("alt"), 0o755); err != nil {
		t.Fatal(err)
	}
	oldU := newUpgrader
	newUpgrader = func(out io.Writer) *upgrade.Upgrader {
		u := upgrade.New(out)
		u.APIBase, u.Client = e.srv.URL, e.srv.Client()
		u.Current = buildinfo.Info{Version: installed, OS: "linux", Arch: "amd64"}
		u.Executable = func() (string, error) { return e.exe, nil }
		u.Rename = func(oldpath, newpath string) error {
			e.renames = append(e.renames, filepath.Base(oldpath)+" → "+filepath.Base(newpath))
			if e.rename != nil {
				if err := e.rename(oldpath, newpath); err != nil {
					return err
				}
			}
			return os.Rename(oldpath, newpath)
		}
		return u
	}

	oldAsk, oldProbe, oldPre := autoAskHub, autoProbeNode, autoPreflight
	oldWithin, oldPoll := autoRestartWithin, autoRestartPoll
	autoAskHub = func(_ context.Context, _ config.Config, h nodestore.Hub) (string, error) {
		e.asked = append(e.asked, h.Name)
		if e.onAsk != nil {
			e.onAsk()
		}
		a, ok := e.hubs[h.Name]
		if !ok {
			return "", fmt.Errorf("Hub %s im Test nicht beantwortet", h.Name)
		}
		return a.release, a.err
	}
	// Ohne eigene Probe antwortet der Node, solange der Dienst läuft, mit
	// der Version des Binarys, das gerade installiert ist. Kein Test fragt
	// einen echten Node.
	autoProbeNode = func(_ context.Context, listen string, _ time.Duration) (nodeInfo, error) {
		if e.probe != nil {
			return e.probe(listen)
		}
		if !e.sd.active {
			return nodeInfo{}, errors.New("Node unter " + listen + " nicht erreichbar")
		}
		if b, _ := os.ReadFile(e.exe); string(b) == string(e.newBin) {
			return nodeInfo{Version: "v0.2.0"}, nil
		}
		return nodeInfo{Version: installed}, nil
	}
	autoRestartWithin, autoRestartPoll = 200*time.Millisecond, 5*time.Millisecond
	t.Cleanup(func() {
		newUpgrader = oldU
		autoAskHub, autoProbeNode, autoPreflight = oldAsk, oldProbe, oldPre
		autoRestartWithin, autoRestartPoll = oldWithin, oldPoll
	})
	return e
}

// answer legt fest, was das neue Binary auf die Probe antwortet.
func (e *autoEnv) answer(t *testing.T, stdout, stderr string, exit int) {
	t.Helper()
	for name, content := range map[string]string{"stdout": stdout, "stderr": stderr, "exit": fmt.Sprint(exit)} {
		if err := os.WriteFile(filepath.Join(e.script, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// initNode richtet den Node pro User ein, mit dem Hub-Eintrag vm (Antwort
// v0.2.0) und dem laufenden Dienst pro User für dieses Binary.
func (e *autoEnv) initNode(t *testing.T) {
	t.Helper()
	runT(t, "node", "init").want(t, 0)
	e.cfgPath = filepath.Join(e.dir, "config", "kephalaion", "config.yaml")
	e.nodeDB = filepath.Join(e.dir, "data", "kephalaion", "node.db")
	e.addHub(t, "vm", "v0.2.0")
	loc, err := config.Locate("")
	if err != nil {
		t.Fatal(err)
	}
	e.installUnit(t, serveCallFor(e.exe, loc))
	e.sd.active = true
}

// addHub trägt einen Hub-Eintrag ein, der mit release antwortet.
func (e *autoEnv) addHub(t *testing.T, alias, release string) {
	t.Helper()
	tok, err := ident.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	runIn(t, tok, "node", "hub", "add", alias, "--node", "wsl", "--transport", "http", "--address",
		"http://127.0.0.1:1/hub", "--token-stdin").want(t, 0)
	e.hubs[alias] = hubAnswer{release: release}
}

// installUnit legt die Unit pro User für den Aufruf call ab.
func (e *autoEnv) installUnit(t *testing.T, call []string) {
	t.Helper()
	text, err := service.UserUnit(call)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(e.sd.m.UnitPath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.sd.m.UnitPath(), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// auto ruft upgrade --auto --json auf, prüft den Exit-Code und liest das
// Ergebnis.
func (e *autoEnv) auto(t *testing.T, code int) autoResult {
	t.Helper()
	r := runT(t, "upgrade", "--auto", "--json")
	if r.code != code {
		t.Fatalf("Exit-Code %d, erwartet %d\nstdout:\n%s\nstderr:\n%s", r.code, code, r.out, r.errOut)
	}
	var res autoResult
	if err := json.Unmarshal([]byte(r.out), &res); err != nil {
		t.Fatalf("%v:\n%s", err, r.out)
	}
	if res.Warnings == nil {
		t.Errorf("warnings ist nicht [] :\n%s", r.out)
	}
	return res
}

// want prüft Ergebnis, Grund und dass der Hinweis alle parts enthält.
func (res autoResult) want(t *testing.T, outcome, reason string, parts ...string) {
	t.Helper()
	if res.Outcome != outcome || res.Reason != reason {
		t.Fatalf("Ergebnis %s/%s, erwartet %s/%s: %+v", res.Outcome, res.Reason, outcome, reason, res)
	}
	if res.Hint == "" {
		t.Errorf("ohne hint: %+v", res)
	}
	for _, p := range parts {
		if !strings.Contains(res.Hint, p) {
			t.Errorf("hint ohne %q: %s", p, res.Hint)
		}
	}
}

// unchanged prüft für einen Fall vor dem Ersetzen: Das Binary ist das alte,
// keine temporäre Datei liegt neben ihm (die Sperrdatei ist keine), kein
// Neustart. Mit dbBefore auch, dass node.db unverändert ist.
func (e *autoEnv) unchanged(t *testing.T, dbBefore string) {
	t.Helper()
	if b, _ := os.ReadFile(e.exe); string(b) != "alt" {
		t.Errorf("Binary verändert: %q", b)
	}
	assertNoUpgradeTemp(t, filepath.Dir(e.exe))
	for _, c := range e.sd.calls {
		if strings.Contains(c, "restart") {
			t.Errorf("Neustart: %s", c)
		}
	}
	if dbBefore != "" {
		if got := fileState(t, e.nodeDB); got != dbBefore {
			t.Error("node.db verändert")
		}
	}
}

// assertNoUpgradeTemp prüft, dass keine Datei nach TempPattern in dir liegt.
func assertNoUpgradeTemp(t *testing.T, dir string) {
	t.Helper()
	left, err := filepath.Glob(filepath.Join(dir, upgrade.TempPattern))
	if err != nil {
		t.Fatal(err)
	}
	if len(left) > 0 {
		t.Errorf("temporäre Dateien zurückgeblieben: %v", left)
	}
}

// preflightCalls liest die Aufrufe des neuen Binarys.
func (e *autoEnv) preflightCalls(t *testing.T) []string {
	t.Helper()
	return readLines(t, filepath.Join(e.script, "calls.log"))
}

// Stufe 3: Das Release ist nicht neuer — current, auch wenn das installierte
// neuer ist; --auto stuft nie zurück. Ohne --json dasselbe in Zeilen.
func TestUpgradeAutoCurrent(t *testing.T) {
	for _, installed := range []string{"v0.2.0", "v0.3.0"} {
		t.Run(installed, func(t *testing.T) {
			e := newAutoEnv(t, installed)
			e.initNode(t)
			res := e.auto(t, 0)
			res.want(t, autoCurrent, "", "nichts zu tun")
			if res.From != installed || res.To != "v0.2.0" || res.Check == nil || res.Check.Latest != "v0.2.0" ||
				res.Restarted != nil || res.RolledBack != nil || res.Error != "" {
				t.Errorf("current: %+v", res)
			}
			e.unchanged(t, "")
			if len(e.asked) != 0 {
				t.Errorf("Hubs gefragt: %v", e.asked)
			}
			runT(t, "upgrade", "--auto").want(t, 0, "upgrade --auto: current", "von:            "+installed,
				"Ziel:           v0.2.0", "Hinweis:")
		})
	}
}

// Stufe 2: ein dev build wird nie automatisch ersetzt.
func TestUpgradeAutoDevBuild(t *testing.T) {
	e := newAutoEnv(t, "dev")
	e.initNode(t)
	e.auto(t, 0).want(t, autoSkipped, reasonDevBuild, "kephalaion upgrade --version v0.2.0")
	e.unchanged(t, "")
}

// Stufe 1: Scheitert die Frage an GitHub, ist es failed, check — vor allen
// lokalen Gründen, auch bei globaler config. Exit 1.
func TestUpgradeAutoCheckFailed(t *testing.T) {
	e := newAutoEnv(t, "v0.1.0")
	writeSystemConfig(t, filepath.Join(e.dir, "var"))
	e.srv.Close()
	res := e.auto(t, 1)
	res.want(t, autoFailed, reasonCheck)
	if !strings.Contains(res.Error, "keine Verbindung") || res.Check == nil || res.Check.State != upgrade.StateFailed ||
		res.To != "" {
		t.Errorf("check: %+v", res)
	}
	e.unchanged(t, "")
}

// Stufe 4: Gilt die globale config, ist es immer admin — ohne Schreibrecht,
// mit Schreibrecht (wie root) und im Mischfall: ein beschreibbares Binary pro
// User vor /usr/local/bin, während die System-Unit läuft.
func TestUpgradeAutoAdmin(t *testing.T) {
	t.Run("ohne Schreibrecht", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root darf überall schreiben")
		}
		e := newAutoEnv(t, "v0.1.0")
		writeSystemConfig(t, filepath.Join(e.dir, "var"))
		if err := os.Chmod(filepath.Dir(e.exe), 0o555); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(filepath.Dir(e.exe), 0o755) })
		e.auto(t, 0).want(t, autoSkipped, reasonAdmin, "Verwalter", "sudo kephalaion upgrade")
		e.unchanged(t, "")
	})
	t.Run("mit Schreibrecht", func(t *testing.T) {
		e := newAutoEnv(t, "v0.1.0")
		writeSystemConfig(t, filepath.Join(e.dir, "var"))
		e.auto(t, 0).want(t, autoSkipped, reasonAdmin, "Ansible")
		e.unchanged(t, "")
	})
	t.Run("Mischfall", func(t *testing.T) {
		e := newAutoEnv(t, "v0.1.0")
		writeSystemConfig(t, filepath.Join(e.dir, "var"))
		e.sd.system = "active"
		e.probe = func(string) (nodeInfo, error) { return nodeInfo{Version: "v0.1.0"}, nil }
		e.auto(t, 0).want(t, autoSkipped, reasonAdmin)
		e.unchanged(t, "")
		if len(e.preflightCalls(t)) != 0 {
			t.Errorf("Probe aufgerufen: %v", e.preflightCalls(t))
		}
	})
}

// Stufe 5: Eine config pro User mit hub: wird nie automatisch aktualisiert.
func TestUpgradeAutoHub(t *testing.T) {
	e := newAutoEnv(t, "v0.1.0")
	runT(t, "hub", "init").want(t, 0)
	runT(t, "node", "init").want(t, 0)
	e.auto(t, 0).want(t, autoSkipped, reasonHub, "kephalaion upgrade")
	e.unchanged(t, "")
}

// Stufe 6: Ohne Schreibrecht im Verzeichnis des Binarys ist es manual.
func TestUpgradeAutoManual(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root darf überall schreiben")
	}
	e := newAutoEnv(t, "v0.1.0")
	e.initNode(t)
	if err := os.Chmod(filepath.Dir(e.exe), 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Dir(e.exe), 0o755) })
	e.auto(t, 0).want(t, autoSkipped, reasonManual, "kein Schreibrecht in "+filepath.Dir(e.exe))
	e.unchanged(t, "")
}

// Stufe 7: Der Node läuft nicht als Dienst pro User mit diesem Binary.
func TestUpgradeAutoNotHere(t *testing.T) {
	// Container ohne Datenbank: die config des Hosts, Datei und Verzeichnis
	// fehlen.
	t.Run("Container", func(t *testing.T) {
		e := newAutoEnv(t, "v0.1.0")
		cfg := config.Config{Node: &config.Section{DB: "sqlite://" + filepath.Join(e.dir, "host", "node.db")}}
		if err := config.Save(filepath.Join(e.dir, "config", "kephalaion", "config.yaml"), cfg); err != nil {
			t.Fatal(err)
		}
		e.auto(t, 0).want(t, autoSkipped, reasonNotHere, "reiner Client", "auf dem Rechner, auf dem der Node läuft")
		e.unchanged(t, "")
	})
	// Ohne config, aber an der Standardadresse antwortet ein Node.
	t.Run("ohne config", func(t *testing.T) {
		e := newAutoEnv(t, "v0.1.0")
		var asked string
		e.probe = func(listen string) (nodeInfo, error) {
			asked = listen
			return nodeInfo{Version: "v0.1.0"}, nil
		}
		e.auto(t, 0).want(t, autoSkipped, reasonNotHere, config.DefaultListenNode, "Devcontainer")
		if asked != config.DefaultListenNode {
			t.Errorf("gefragt: %q", asked)
		}
		e.unchanged(t, "")
	})
	// Eine Unit pro User mit einem anderen Binary oder einer anderen config.
	t.Run("abweichende Unit", func(t *testing.T) {
		e := newAutoEnv(t, "v0.1.0")
		e.initNode(t)
		before := fileState(t, e.nodeDB)
		e.installUnit(t, []string{"/opt/anders/kephalaion", "serve", "--config", e.cfgPath})
		e.auto(t, 0).want(t, autoSkipped, reasonNotHere, "/opt/anders/kephalaion", "kephalaion service install")
		e.unchanged(t, before)
		e.installUnit(t, []string{e.exe, "serve", "--config", filepath.Join(e.dir, "andere.yaml")})
		e.auto(t, 0).want(t, autoSkipped, reasonNotHere, e.cfgPath)
		e.installUnit(t, []string{e.exe, "serve"})
		e.auto(t, 0).want(t, autoSkipped, reasonNotHere)
		e.unchanged(t, before)
		if len(e.asked) != 0 {
			t.Errorf("Hubs gefragt: %v", e.asked)
		}
	})
	// Ein Node antwortet unter listen, aber kein Dienst pro User läuft.
	t.Run("Node ohne Dienst", func(t *testing.T) {
		e := newAutoEnv(t, "v0.1.0")
		e.initNode(t)
		e.sd.active = false
		e.probe = func(string) (nodeInfo, error) { return nodeInfo{Version: "v0.1.0"}, nil }
		e.auto(t, 0).want(t, autoSkipped, reasonNotHere, "kein Dienst pro User läuft", config.DefaultListenNode)
		e.unchanged(t, "")
		// Ohne Unit ebenso.
		if err := os.Remove(e.sd.m.UnitPath()); err != nil {
			t.Fatal(err)
		}
		e.auto(t, 0).want(t, autoSkipped, reasonNotHere, "serve läuft von Hand")
		e.unchanged(t, "")
	})
}

// Stufe 8: Ein Hub nennt ein älteres Release — hub_older mit seinem Alias;
// node.db bleibt unverändert.
func TestUpgradeAutoHubOlder(t *testing.T) {
	e := newAutoEnv(t, "v0.1.0")
	e.initNode(t)
	e.addHub(t, "zweit", "v0.3.0")
	e.hubs["vm"] = hubAnswer{release: "v0.1.5"}
	before := fileState(t, e.nodeDB)
	res := e.auto(t, 0)
	res.want(t, autoSkipped, reasonHubOlder, "Hub vm hat v0.1.5", "v0.2.0")
	if strings.Contains(res.Hint, "zweit") {
		t.Errorf("hint nennt einen passenden Hub: %s", res.Hint)
	}
	e.unchanged(t, before)
	if strings.Join(e.asked, ",") != "vm,zweit" {
		t.Errorf("gefragt: %v", e.asked)
	}
}

// Stufe 8: Der Hub ist unbekannt — ohne Feld, als dev build, ohne Antwort
// oder weil node.db nicht lesbar ist (dann nennt hint den Fehler).
func TestUpgradeAutoHubUnknown(t *testing.T) {
	e := newAutoEnv(t, "v0.1.0")
	e.initNode(t)
	before := fileState(t, e.nodeDB)
	for name, tc := range map[string]struct {
		a    hubAnswer
		want string
	}{
		"Feld fehlt":    {hubAnswer{release: ""}, "Hub vm nennt kein Release"},
		"dev build":     {hubAnswer{release: "dev"}, "Hub vm läuft als dev build"},
		"keine Antwort": {hubAnswer{err: errors.New("Zeitüberschreitung")}, "Hub vm antwortet nicht (Zeitüberschreitung)"},
	} {
		e.hubs["vm"] = tc.a
		res := e.auto(t, 0)
		res.want(t, autoSkipped, reasonHubUnknown, tc.want)
		e.unchanged(t, before)
		if t.Failed() {
			t.Fatalf("Fall %s", name)
		}
	}
	// Nur die Datei fehlt, ihr Verzeichnis nicht: kein reiner Client,
	// sondern ein Node, dessen Einrichtung kaputt ist.
	if err := os.Remove(e.nodeDB); err != nil {
		t.Fatal(err)
	}
	e.asked = nil
	e.auto(t, 0).want(t, autoSkipped, reasonHubUnknown, "nicht lesbar", e.nodeDB, "am Node selbst")
	if len(e.asked) != 0 {
		t.Errorf("Hubs gefragt: %v", e.asked)
	}
	e.unchanged(t, "")
}

// Stufe 9: Hält ein anderer Lauf die Sperre, ist es busy. Hat ein anderer
// Lauf das Binary inzwischen ersetzt, ist es current.
func TestUpgradeAutoLock(t *testing.T) {
	e := newAutoEnv(t, "v0.1.0")
	e.initNode(t)
	lock, err := upgrade.Lock(filepath.Dir(e.exe))
	if err != nil {
		t.Fatal(err)
	}
	e.auto(t, 0).want(t, autoSkipped, reasonBusy)
	e.unchanged(t, "")
	_ = lock.Close()
	if !exists(filepath.Join(filepath.Dir(e.exe), upgrade.LockName)) {
		t.Error("Sperrdatei fehlt")
	}

	// Während der Frage an den Hub ersetzt ein anderer Lauf das Binary.
	e.onAsk = func() {
		tmp := e.exe + ".anderer"
		if err := os.WriteFile(tmp, []byte("anders"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(tmp, e.exe); err != nil {
			t.Fatal(err)
		}
	}
	e.auto(t, 0).want(t, autoCurrent, "", "anderer Lauf")
	if b, _ := os.ReadFile(e.exe); string(b) != "anders" {
		t.Errorf("Binary %q", b)
	}
	assertNoUpgradeTemp(t, filepath.Dir(e.exe))
	if len(e.preflightCalls(t)) != 0 {
		t.Errorf("Probe aufgerufen: %v", e.preflightCalls(t))
	}
}

// Stufe 10: Stimmt die Prüfsumme nicht, ist es failed, download; nichts
// bleibt liegen.
func TestUpgradeAutoDownload(t *testing.T) {
	e := newAutoEnv(t, "v0.1.0")
	e.initNode(t)
	e.sums = []byte(strings.Repeat("0", 64) + "  " + upgrade.AssetName("linux", "amd64") + "\n")
	res := e.auto(t, 1)
	res.want(t, autoFailed, reasonDownload)
	if !strings.Contains(res.Error, "Prüfsumme") {
		t.Errorf("error: %s", res.Error)
	}
	e.unchanged(t, "")
	if len(e.preflightCalls(t)) != 0 {
		t.Errorf("Probe aufgerufen: %v", e.preflightCalls(t))
	}
}

// Stufe 11: Die Probe mit dem neuen Binary läuft über den echten Aufruf, mit
// derselben config. Ein Veto wegen der Schemafassung ist schema, jedes
// andere release; hint kommt aus dem neuen Binary. Kennt es --preflight
// nicht oder ist die Antwort unlesbar: failed, preflight.
func TestUpgradeAutoPreflight(t *testing.T) {
	e := newAutoEnv(t, "v0.1.0")
	e.initNode(t)
	before := fileState(t, e.nodeDB)

	e.answer(t, `{"result":"veto","reason":"schema","hint":"v0.2.0 erwartet Schemafassung 6; siehe „Neue Schemafassung“",`+
		`"version":"v0.2.0","roles":{"node":{"state":"schema","want":"6","got":"5"}}}`, "", 0)
	e.auto(t, 0).want(t, autoSkipped, reasonSchema, "v0.2.0 erwartet Schemafassung 6", "Neue Schemafassung")
	e.unchanged(t, before)
	if got := e.preflightCalls(t); len(got) != 1 || got[0] != "upgrade --preflight --json|"+e.cfgPath {
		t.Errorf("Aufrufe des neuen Binarys: %q", got)
	}

	// Ein Grund, den das installierte Binary nicht kennt.
	e.answer(t, `{"result":"veto","reason":"x_user_roles","hint":"vor v0.2.0 X-User-Roles im Proxy setzen",`+
		`"version":"v0.2.0","roles":{}}`, "", 0)
	e.auto(t, 0).want(t, autoSkipped, reasonRelease, "X-User-Roles im Proxy setzen")
	e.unchanged(t, before)

	// Ein Release ohne --preflight.
	e.answer(t, "", "flag provided but not defined: -preflight", 2)
	res := e.auto(t, 1)
	res.want(t, autoFailed, reasonPreflight)
	if !strings.Contains(res.Error, "flag provided but not defined") {
		t.Errorf("error: %s", res.Error)
	}
	e.unchanged(t, before)

	// Unlesbare Antwort, und eine ohne ok oder veto.
	for _, out := range []string{"Müll", `{"result":"vielleicht"}`} {
		e.answer(t, out, "", 0)
		e.auto(t, 1).want(t, autoFailed, reasonPreflight)
		e.unchanged(t, before)
	}
}

// Stufe 12: Scheitert das Ersetzen, ist es failed, replace — danach liegt
// auch der schon angelegte Rückweg nicht mehr da.
func TestUpgradeAutoReplace(t *testing.T) {
	e := newAutoEnv(t, "v0.1.0")
	e.initNode(t)
	e.rename = func(_, newpath string) error {
		if newpath == e.exe {
			return errors.New("Gerät beschäftigt")
		}
		return nil
	}
	res := e.auto(t, 1)
	res.want(t, autoFailed, reasonReplace)
	if !strings.Contains(res.Error, "Gerät beschäftigt") || len(e.renames) != 1 {
		t.Errorf("replace: %+v, Umbenennungen %v", res, e.renames)
	}
	e.unchanged(t, "")
}

// Stufe 13: upgraded mit Neustart — das neue Binary ist da, der Dienst neu
// gestartet und der Node meldet die Zielversion; der Rückweg ist gelöscht,
// die Sperrdatei bleibt. node.db ist unverändert, die Probe lief mit
// derselben config.
func TestUpgradeAutoUpgradedWithRestart(t *testing.T) {
	e := newAutoEnv(t, "v0.1.0")
	e.initNode(t)
	before := fileState(t, e.nodeDB)
	e.sd.calls = nil
	res := e.auto(t, 0)
	res.want(t, autoUpgraded, "", "v0.2.0", "Dienst")
	if res.From != "v0.1.0" || res.To != "v0.2.0" || res.Restarted == nil || !*res.Restarted ||
		res.RolledBack != nil || res.Error != "" || len(res.Warnings) != 0 || res.Check == nil {
		t.Errorf("upgraded: %+v", res)
	}
	if b, _ := os.ReadFile(e.exe); string(b) != string(e.newBin) {
		t.Errorf("Binary nicht ersetzt: %q", b)
	}
	if fi, _ := os.Stat(e.exe); fi.Mode().Perm() != 0o755 {
		t.Errorf("Rechte %v", fi.Mode().Perm())
	}
	assertNoUpgradeTemp(t, filepath.Dir(e.exe))
	if !exists(filepath.Join(filepath.Dir(e.exe), upgrade.LockName)) {
		t.Error("Sperrdatei fehlt")
	}
	if got := strings.Count(strings.Join(e.sd.calls, "\n"), "systemctl --user restart kephalaion.service"); got != 1 {
		t.Errorf("%d Neustarts: %q", got, e.sd.calls)
	}
	if fileState(t, e.nodeDB) != before {
		t.Error("node.db verändert")
	}
	if got := e.preflightCalls(t); len(got) != 1 || got[0] != "upgrade --preflight --json|"+e.cfgPath {
		t.Errorf("Aufrufe des neuen Binarys: %q", got)
	}

	// Ohne --json dasselbe in Zeilen; der zweite Lauf ist current.
	if err := os.WriteFile(e.exe, []byte("alt"), 0o755); err != nil {
		t.Fatal(err)
	}
	runT(t, "upgrade", "--auto").want(t, 0, "upgrade --auto: upgraded", "von:            v0.1.0",
		"Ziel:           v0.2.0", "neu gestartet:  ja")
}

// Stufe 13 ohne Dienst: Ohne config und ohne antwortenden Node, und mit Node
// ohne laufenden Dienst — upgraded, ohne Neustart, restarted fehlt.
func TestUpgradeAutoUpgradedWithoutService(t *testing.T) {
	t.Run("ohne config", func(t *testing.T) {
		e := newAutoEnv(t, "v0.1.0")
		res := e.auto(t, 0)
		res.want(t, autoUpgraded, "", "kein Dienst lief")
		if res.Restarted != nil || len(e.asked) != 0 {
			t.Errorf("ohne config: %+v, gefragt %v", res, e.asked)
		}
		if b, _ := os.ReadFile(e.exe); string(b) != string(e.newBin) {
			t.Errorf("Binary nicht ersetzt: %q", b)
		}
		assertNoUpgradeTemp(t, filepath.Dir(e.exe))
		if got := e.preflightCalls(t); len(got) != 1 ||
			got[0] != "upgrade --preflight --json|"+filepath.Join(e.dir, "config", "kephalaion", "config.yaml") {
			t.Errorf("Aufrufe des neuen Binarys: %q", got)
		}
	})
	t.Run("Dienst läuft nicht", func(t *testing.T) {
		e := newAutoEnv(t, "v0.1.0")
		e.initNode(t)
		e.sd.active = false
		e.sd.calls = nil
		res := e.auto(t, 0)
		res.want(t, autoUpgraded, "")
		if res.Restarted != nil || strings.Join(e.asked, ",") != "vm" {
			t.Errorf("ohne Dienst: %+v, gefragt %v", res, e.asked)
		}
		for _, c := range e.sd.calls {
			if strings.Contains(c, "restart") {
				t.Errorf("Neustart: %s", c)
			}
		}
		assertNoUpgradeTemp(t, filepath.Dir(e.exe))
	})
}

// Stufe 13: Läuft der Dienst nach dem Neustart nicht mit der Zielversion an,
// kommt das alte Binary zurück und der Dienst wird erneut gestartet: failed,
// restart, rolled_back true — restarted ist auch dann true. Scheitert der
// Rückweg, ist rolled_back false, und hint nennt den Weg von Hand.
func TestUpgradeAutoRestartFailed(t *testing.T) {
	t.Run("Node meldet nicht die Zielversion", func(t *testing.T) {
		e := newAutoEnv(t, "v0.1.0")
		e.initNode(t)
		e.probe = func(string) (nodeInfo, error) {
			if !e.sd.active {
				return nodeInfo{}, errors.New("nicht erreichbar")
			}
			return nodeInfo{Version: "v0.1.0"}, nil
		}
		e.sd.calls = nil
		res := e.auto(t, 1)
		res.want(t, autoFailed, reasonRestart, "das alte Binary ist zurück")
		if res.Restarted == nil || !*res.Restarted || res.RolledBack == nil || !*res.RolledBack ||
			!strings.Contains(res.Error, "meldet Version v0.1.0, erwartet v0.2.0") {
			t.Errorf("restart: %+v", res)
		}
		if b, _ := os.ReadFile(e.exe); string(b) != "alt" {
			t.Errorf("Binary nicht zurück: %q", b)
		}
		assertNoUpgradeTemp(t, filepath.Dir(e.exe))
		if got := strings.Count(strings.Join(e.sd.calls, "\n"), "systemctl --user restart kephalaion.service"); got != 2 {
			t.Errorf("%d Neustarts, erwartet 2: %q", got, e.sd.calls)
		}
	})
	t.Run("Dienst startet nicht", func(t *testing.T) {
		e := newAutoEnv(t, "v0.1.0")
		e.initNode(t)
		e.sd.restartErr = errors.New("Job failed")
		res := e.auto(t, 1)
		res.want(t, autoFailed, reasonRestart, "den Dienst von Hand starten", "systemctl --user restart kephalaion.service")
		if res.Restarted == nil || !*res.Restarted || res.RolledBack == nil || *res.RolledBack {
			t.Errorf("restart: %+v", res)
		}
		if b, _ := os.ReadFile(e.exe); string(b) != "alt" {
			t.Errorf("Binary nicht zurück: %q", b)
		}
		assertNoUpgradeTemp(t, filepath.Dir(e.exe))
	})
	t.Run("Rückweg scheitert", func(t *testing.T) {
		e := newAutoEnv(t, "v0.1.0")
		e.initNode(t)
		e.probe = func(string) (nodeInfo, error) { return nodeInfo{}, errors.New("nicht erreichbar") }
		swapped := false
		e.rename = func(_, newpath string) error {
			if newpath == e.exe && swapped {
				return errors.New("Gerät beschäftigt")
			}
			swapped = newpath == e.exe
			return nil
		}
		res := e.auto(t, 1)
		res.want(t, autoFailed, reasonRestart, "von Hand zurück: mv ", " "+e.exe+" && systemctl --user restart")
		if res.Restarted == nil || !*res.Restarted || res.RolledBack == nil || *res.RolledBack ||
			!strings.Contains(res.Error, "Rückweg gescheitert") {
			t.Errorf("restart: %+v", res)
		}
		// Das neue Binary bleibt, der Rückweg liegt für den Weg von Hand da.
		if b, _ := os.ReadFile(e.exe); string(b) != string(e.newBin) {
			t.Errorf("Binary %q", b)
		}
		left, _ := filepath.Glob(filepath.Join(filepath.Dir(e.exe), upgrade.TempPattern))
		if len(left) != 1 || !strings.Contains(res.Hint, left[0]) {
			t.Fatalf("Rückweg %v, hint %s", left, res.Hint)
		}
		if b, _ := os.ReadFile(left[0]); string(b) != "alt" {
			t.Errorf("Rückweg %q", b)
		}
	})
}

// Die Frage an einen echten Hub über HTTP, mit der Anmeldung des Nodes: Der
// Hub nennt sein Release (Etappe 1); älter heißt hub_older, ein gesperrter
// Node hub_unknown, gleich oder neuer geht weiter. node.db bleibt
// unverändert — keine hub_id gemerkt.
func TestUpgradeAutoAsksRealHub(t *testing.T) {
	e := newAutoEnv(t, "v0.1.0")
	autoAskHub = askHubRelease
	runT(t, "node", "init").want(t, 0)
	e.cfgPath = filepath.Join(e.dir, "config", "kephalaion", "config.yaml")
	e.nodeDB = filepath.Join(e.dir, "data", "kephalaion", "node.db")
	loc, err := config.Locate("")
	if err != nil {
		t.Fatal(err)
	}
	e.installUnit(t, serveCallFor(e.exe, loc))
	e.sd.active = true

	hubCfg := filepath.Join(e.dir, "hub", "config.yaml")
	runT(t, "hub", "init", "--db", "sqlite://"+filepath.Join(e.dir, "hub", "hub.db"), "--config", hubCfg).want(t, 0)
	r := runT(t, "hub", "node", "add", "wsl", "--config", hubCfg)
	r.want(t, 0)
	srv := httptest.NewServer(httpapi.NewHandler(replication.New(hubStore(t, hubCfg))))
	t.Cleanup(srv.Close)
	runIn(t, tokenFrom(t, r.out), "node", "hub", "add", "vm", "--node", "wsl", "--transport", "http", "--address",
		srv.URL, "--token-stdin").want(t, 0)
	before := fileState(t, e.nodeDB)

	old := buildinfo.Version
	t.Cleanup(func() { buildinfo.Version = old })
	buildinfo.Version = "v0.1.5"
	e.auto(t, 0).want(t, autoSkipped, reasonHubOlder, "Hub vm hat v0.1.5")
	e.unchanged(t, before)

	runT(t, "hub", "node", "lock", "wsl", "--config", hubCfg).want(t, 0)
	e.auto(t, 0).want(t, autoSkipped, reasonHubUnknown, "Hub vm antwortet nicht")
	e.unchanged(t, before)
	runT(t, "hub", "node", "unlock", "wsl", "--config", hubCfg).want(t, 0)

	buildinfo.Version = "v0.2.0"
	e.auto(t, 0).want(t, autoUpgraded, "")
	if fileState(t, e.nodeDB) != before {
		t.Error("node.db verändert")
	}
	ns := nodeStore(t, e.cfgPath)
	if h, err := ns.Hub(context.Background(), "vm"); err != nil || h.HubID != "" {
		t.Errorf("hub_id gemerkt: %+v, %v", h, err)
	}
}

// --auto gibt es nicht mit --check oder --version; mit und ohne --json.
func TestUpgradeAutoUsage(t *testing.T) {
	isolate(t)
	runT(t, "upgrade", "--auto", "--check").want(t, 2, "--auto gibt es nicht mit --check oder --version")
	runT(t, "upgrade", "--auto", "--version", "v0.2.0").want(t, 2, "--auto gibt es nicht mit --check oder --version")
	runT(t, "upgrade", "--auto", "--json", "--check").want(t, 2, "--auto gibt es nicht")
	runT(t, "upgrade", "--auto", "--preflight", "--json").want(t, 2, "--preflight gibt es nur mit --json")
	runT(t, "upgrade", "--help").want(t, 0, "kephalaion upgrade --auto [--json]", "--preflight", "outcome",
		"rolled_back", "auch für --auto mit einem Binary, das --auto noch")
}
