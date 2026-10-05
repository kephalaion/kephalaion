package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kephalaion/kephalaion/internal/buildinfo"
	"github.com/kephalaion/kephalaion/internal/upgrade"
)

// fakeReleases spielt GitHub mit einem Release v0.2.0 für linux/amd64 nach
// und lenkt newUpgrader darauf; das „installierte“ Binary liegt in einem
// temporären Verzeichnis und hat die Version installed.
func fakeReleases(t *testing.T, installed string) (exe string, srv *httptest.Server) {
	t.Helper()
	return fakeReleasesWith(t, installed, []byte("zwei"))
}

// fakeReleasesWith ist fakeReleases mit bin als neuem Binary.
func fakeReleasesWith(t *testing.T, installed string, bin []byte) (exe string, srv *httptest.Server) {
	t.Helper()
	sum := sha256.Sum256(bin)
	name := upgrade.AssetName("linux", "amd64")
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/kephalaion/kephalaion/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"tag_name":"v0.2.0","assets":[{"name":%q,"browser_download_url":"%s/d/%s"},`+
			`{"name":"SHA256SUMS","browser_download_url":"%s/d/SHA256SUMS"}]}`, name, srv.URL, name, srv.URL)
	})
	mux.HandleFunc("/d/"+name, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(bin) })
	mux.HandleFunc("/d/SHA256SUMS", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s  %s\n", hex.EncodeToString(sum[:]), name)
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// Aufgelöst wie in upgrade: Auf macOS liegt das temporäre Verzeichnis
	// hinter einem Link.
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	exe = filepath.Join(tmp, "kephalaion")
	if err := os.WriteFile(exe, []byte("alt"), 0o755); err != nil {
		t.Fatal(err)
	}
	old := newUpgrader
	newUpgrader = func(out io.Writer) *upgrade.Upgrader {
		u := upgrade.New(out)
		u.APIBase, u.Client = srv.URL, srv.Client()
		u.Current = buildinfo.Info{Version: installed, OS: "linux", Arch: "amd64"}
		u.Executable = func() (string, error) { return exe, nil }
		return u
	}
	t.Cleanup(func() { newUpgrader = old })
	return exe, srv
}

// Nach dem Ersetzen startet upgrade den laufenden Dienst pro User neu.
func TestUpgradeRestartsUserService(t *testing.T) {
	dir := isolate(t)
	f := withSystemd(t, dir)
	exe, _ := fakeReleases(t, "v0.1.0")

	// Kein Dienst: nur ersetzen.
	runT(t, "upgrade").want(t, 0, "Aktualisiert: v0.1.0 → v0.2.0 ("+exe+")")
	for _, c := range f.calls {
		if strings.Contains(c, "restart") {
			t.Errorf("Neustart ohne laufenden Dienst: %s", c)
		}
	}

	// Dienst pro User läuft: neu starten.
	if err := os.WriteFile(exe, []byte("alt"), 0o755); err != nil {
		t.Fatal(err)
	}
	f.active = true
	f.calls = nil
	runT(t, "upgrade").want(t, 0, "Aktualisiert: v0.1.0 → v0.2.0", "Dienst neu gestartet (systemctl --user restart kephalaion.service).")
	if !contains(strings.Join(f.calls, "\n"), "systemctl --user restart kephalaion.service") {
		t.Errorf("kein Neustart: %q", f.calls)
	}

	// Scheitert der Neustart: Exit 1, das Binary ist trotzdem neu.
	if err := os.WriteFile(exe, []byte("alt"), 0o755); err != nil {
		t.Fatal(err)
	}
	f.restartErr = fmt.Errorf("Job failed")
	runT(t, "upgrade").want(t, 1, "Das neue Binary ist installiert, der Dienst ließ sich aber nicht neu starten",
		"Von Hand: systemctl --user restart kephalaion.service")
	if b, _ := os.ReadFile(exe); string(b) != "zwei" {
		t.Errorf("Binary %q", b)
	}

	// Läuft die System-Unit: nur der Hinweis.
	if err := os.WriteFile(exe, []byte("alt"), 0o755); err != nil {
		t.Fatal(err)
	}
	f.active, f.restartErr, f.system = false, nil, "active"
	f.calls = nil
	runT(t, "upgrade").want(t, 0, "benutzt das neue Binary erst nach: sudo systemctl restart kephalaion")
	for _, c := range f.calls {
		if strings.Contains(c, "restart") {
			t.Errorf("Neustart der System-Unit: %s", c)
		}
	}
}

// upgrade --check --json gibt den Report aus; scheitert die Frage, steht der
// Grund im JSON, Exit 1.
func TestUpgradeCheckJSON(t *testing.T) {
	dir := isolate(t)
	exe, srv := fakeReleases(t, "v0.1.0")
	r := runT(t, "upgrade", "--check", "--json")
	r.want(t, 0)
	var rep upgrade.Report
	if err := json.Unmarshal([]byte(r.out), &rep); err != nil {
		t.Fatalf("%v:\n%s", err, r.out)
	}
	if rep.State != upgrade.StateOK || rep.Version != "v0.1.0" || rep.Latest != "v0.2.0" || !rep.UpdateAvailable ||
		!rep.SelfUpgrade || rep.Method != upgrade.MethodSelf || rep.Command != "kephalaion upgrade" || rep.CheckedAt == "" {
		t.Errorf("Report %+v", rep)
	}
	if b, _ := os.ReadFile(exe); string(b) != "alt" {
		t.Errorf("--check hat ersetzt: %q", b)
	}

	// Text: dieselben Angaben.
	runT(t, "upgrade", "--check").want(t, 0, "Neue Version verfügbar: v0.1.0 → v0.2.0", "Selbst ersetzen: ja",
		"Weg: kephalaion upgrade")

	// Die globale config gilt, kein Schreibrecht: der Weg des Verwalters.
	if os.Geteuid() != 0 {
		writeSystemConfig(t, filepath.Join(dir, "var"))
		if err := os.Chmod(filepath.Dir(exe), 0o555); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(filepath.Dir(exe), 0o755) })
		r = runT(t, "upgrade", "--check", "--json")
		r.want(t, 0, `"self_upgrade": false`, `"method": "admin"`,
			`"command": "sudo kephalaion upgrade && sudo systemctl restart kephalaion"`)
		runT(t, "upgrade").want(t, 1, "kein Schreibrecht in "+filepath.Dir(exe), "Weg: globale Installation",
			"bleibt unverändert")
	}

	srv.Close()
	r = runT(t, "upgrade", "--check", "--json")
	r.want(t, 1, `"state": "failed"`, `"error": "keine Verbindung`)
	if !strings.Contains(r.errOut, "upgrade: keine Verbindung") {
		t.Errorf("stderr: %s", r.errOut)
	}

	runT(t, "upgrade", "--json").want(t, 2, "--json gibt es nur mit --check")
	runT(t, "upgrade", "--check", "--json", "--version", "v0.2.0").want(t, 2, "--json gibt es nur mit --check und ohne --version")
	runT(t, "upgrade", "--help").want(t, 0, "Exit-Code:", "self_upgrade")
}
