package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kephalaion/kephalaion/internal/assistant/assistanttest"
	"github.com/kephalaion/kephalaion/internal/config"
	"github.com/kephalaion/kephalaion/internal/service"
	"github.com/kephalaion/kephalaion/internal/upgrade"
)

// testNow ist die Zeit, zu der die Tests GitHub fragen.
var testNow = time.Date(2026, 9, 26, 10, 0, 0, 0, time.UTC)

// TestMain hält die Tests vom Rechner fern: Die globale config liegt in einem
// leeren temporären Verzeichnis, damit eine echte /etc/kephalaion/config.yaml
// keinen Test verändert.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "kephalaion-test-")
	if err != nil {
		panic(err)
	}
	config.SystemPath = filepath.Join(dir, "etc", "kephalaion", "config.yaml")
	// Kein Test ruft systemctl oder launchctl: ohne Ersatz sieht status einen
	// Rechner ohne systemd.
	newServiceManager = func() *service.Manager { return noSystemd(dir) }
	// Kein Test ruft einen echten KI-Assistenten auf: ohne Ersatz liegt keiner
	// im PATH, und jeder Aufruf scheitert.
	newAssistantManager = assistanttest.New(dir).Manager
	// Kein Test fragt GitHub: serve und node whoami fragen einen
	// nachgespielten, der v0.2.0 als neuestes Release nennt, zu fester Zeit.
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/kephalaion/kephalaion/releases/latest" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `{"tag_name":"v0.2.0","assets":[]}`)
	}))
	newUpgrader = func(out io.Writer) *upgrade.Upgrader {
		u := upgrade.New(out)
		u.APIBase, u.Client = gh.URL, gh.Client()
		u.Now = func() time.Time { return testNow }
		return u
	}
	code := m.Run()
	gh.Close()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// slow überspringt einen langsamen Test unter go test -short (make
// check-quick, CI auf dev); der vollständige Lauf (make check) nimmt ihn mit.
// Langsam ist, was die Messung so ausweist: mehr als etwa 0,5 s, oder der
// Test wartet auf sync_interval, Timer oder Runden. Dass er serve startet,
// reicht nicht. Kompiliert und von go vet gesehen wird er immer. why sagt,
// worauf er wartet.
func slow(t *testing.T, why string) {
	t.Helper()
	if testing.Short() {
		t.Skip("langsam: " + why)
	}
}

func TestRunHelpAndVersion(t *testing.T) {
	cases := []struct {
		args []string
		code int
		want string
	}{
		{nil, 0, "Kommandos:"},
		{[]string{"help"}, 0, "Kommandos:"},
		{[]string{"version"}, 0, "Plattform:"},
	}
	for _, c := range cases {
		var out, errOut bytes.Buffer
		if got := run(c.args, strings.NewReader(""), &out, &errOut); got != c.code {
			t.Errorf("run(%v) = %d, erwartet %d", c.args, got, c.code)
		}
		if !strings.Contains(out.String(), c.want) {
			t.Errorf("run(%v): Ausgabe ohne %q:\n%s", c.args, c.want, out.String())
		}
	}
}

func TestRunUnknown(t *testing.T) {
	var out, errOut bytes.Buffer
	if got := run([]string{"gibtsnicht"}, strings.NewReader(""), &out, &errOut); got != 2 {
		t.Fatalf("Exit-Code %d, erwartet 2", got)
	}
	if !strings.Contains(errOut.String(), "Unbekanntes Kommando: gibtsnicht") {
		t.Fatalf("Fehlermeldung fehlt:\n%s", errOut.String())
	}
}
