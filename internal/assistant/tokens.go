// Package assistant meldet den Node bei den KI-Assistenten des Users als
// MCP-Server an: die Adresse aus listen, je Hub das Header-Paar aus den
// Token-Dateien. Neutral: Es kennt weder Hub noch Node, nur die config, die
// Token-Dateien und die Konfigurationen der Assistenten.
//
// Das Token steht in keiner Konfiguration eines Assistenten. Claude Code und
// Codex rufen bei jeder Verbindung den Helfer auf (kephalaion node mcp
// headers), OpenCode liest die Token-Datei über einen Verweis ({file:…}).
package assistant

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kephalaion/kephalaion/internal/config"
	"github.com/kephalaion/kephalaion/internal/ident"
)

// Die Präfixe der Header eines Header-Paars; dahinter steht der Alias des
// Hubs.
const (
	AccountHeaderPrefix = "X-Keph-Account-"
	TokenHeaderPrefix   = "X-Keph-Token-"
)

// tokenSuffix ist die Endung einer Token-Datei: <account>.token.
const tokenSuffix = ".token"

// TokensDir ist das Verzeichnis der Token-Dateien: tokens/ neben der config
// des Users, wie node dir und die Erweiterung für VS Code es lesen.
func TokensDir() (string, error) {
	p, err := config.UserPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(p), "tokens"), nil
}

// TokenFile ist die Token-Datei eines Accounts: <tokensDir>/<hub>/<account>.token.
func TokenFile(tokensDir, hub, account string) string {
	return filepath.Join(tokensDir, hub, account+tokenSuffix)
}

// Accounts nennt die Accounts mit Token-Datei in hubDir (<account>.token,
// .pending übergangen), nach Name; ein fehlendes Verzeichnis ist leer.
func Accounts(hubDir string) ([]string, error) {
	entries, err := os.ReadDir(hubDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if name, ok := strings.CutSuffix(e.Name(), tokenSuffix); ok && !e.IsDir() {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out, nil
}

// Hubs nennt die Hubs mit einem Verzeichnis unter tokensDir, nach Name. Ein
// Verzeichnis, dessen Name kein gültiger Alias ist, zählt nicht; ein
// fehlendes tokensDir ist leer.
func Hubs(tokensDir string) ([]string, error) {
	entries, err := os.ReadDir(tokensDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() && ident.CheckName("Hub", e.Name()) == nil {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}

// ReadTokenFile liest ein Token aus der ersten Zeile einer Datei und prüft
// sein Format. Die Fehler nennen den Pfad, nie den Inhalt.
func ReadTokenFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("Token-Datei: %w", err)
	}
	defer f.Close()
	line, err := bufio.NewReader(f).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("Token-Datei %s: %w", path, err)
	}
	token := strings.TrimSpace(line)
	if err := ident.CheckToken(token); err != nil {
		return "", fmt.Errorf("Token-Datei %s: %w", path, err)
	}
	return token, nil
}

// Choice ist die Wahl des Accounts je Hub (--account <hub>=<account>).
type Choice map[string]string

// ParseChoice liest die Werte von --account <hub>=<account>. Ein Hub darf nur
// einmal stehen.
func ParseChoice(values []string) (Choice, error) {
	c := Choice{}
	for _, v := range values {
		hub, account, ok := strings.Cut(v, "=")
		if !ok || hub == "" || account == "" {
			return nil, fmt.Errorf("--account %q: erwartet <hub>=<account>", v)
		}
		if err := ident.CheckName("Hub", hub); err != nil {
			return nil, fmt.Errorf("--account %q: %w", v, err)
		}
		if err := ident.CheckPrincipalName("Account", account); err != nil {
			return nil, fmt.Errorf("--account %q: %w", v, err)
		}
		if prev, dup := c[hub]; dup && prev != account {
			return nil, fmt.Errorf("--account: für den Hub %s stehen zwei Accounts da (%s, %s)", hub, prev, account)
		}
		c[hub] = account
	}
	return c, nil
}

// Hubs nennt die Hubs der Wahl, nach Name.
func (c Choice) Hubs() []string {
	out := make([]string, 0, len(c))
	for hub := range c {
		out = append(out, hub)
	}
	sort.Strings(out)
	return out
}

// Equal sagt, ob zwei Wahlen dieselben Accounts für dieselben Hubs nennen.
func (c Choice) Equal(o Choice) bool {
	if len(c) != len(o) {
		return false
	}
	for hub, account := range c {
		if o[hub] != account {
			return false
		}
	}
	return true
}

// Login ist die Anmeldung für einen Hub: der Account und seine Token-Datei.
type Login struct {
	Hub     string
	Account string
	// File ist die Token-Datei, absolut.
	File string
	// How sagt, woher die Wahl kommt: Überschreibung, Standard oder einzige
	// Token-Datei (Task 037).
	How How
	// Notes sind Hinweise zur Wahl, etwa ein Standard ohne Token-Datei.
	Notes []string
}

// SkippedHub ist ein Hub, der keine Anmeldung bekommt, mit dem Grund — ohne
// Token.
type SkippedHub struct {
	Hub string
	// Accounts sind die Accounts mit Token-Datei, wenn mehrere da sind und
	// die Wahl fehlt (keine Überschreibung, kein Standard); sonst leer.
	Accounts []string
	Reason   string
	// Notes sind Hinweise zur Wahl, etwa ein Standard ohne Token-Datei.
	Notes []string
}

// Logins ermittelt je Hub die Anmeldung nach der Reihenfolge der Wahl
// (Resolve): die Überschreibung aus choice, der Standard
// (<tokensDir>/<hub>/default), die einzige Token-Datei. Ein Hub mit
// gewähltem Account ohne Token-Datei oder mit mehreren Accounts ohne Wahl
// und ohne Standard wird übergangen und genannt; ein Hub ohne Token-Datei und
// ohne Wahl fehlt still. Gelesen werden nur die Namen, kein Token.
func Logins(tokensDir string, choice Choice) (logins []Login, skipped []SkippedHub, err error) {
	hubs, err := Hubs(tokensDir)
	if err != nil {
		return nil, nil, err
	}
	seen := map[string]bool{}
	for _, h := range hubs {
		seen[h] = true
	}
	for _, h := range choice.Hubs() {
		if !seen[h] {
			hubs = append(hubs, h)
		}
	}
	sort.Strings(hubs)
	for _, hub := range hubs {
		r := Resolve(tokensDir, hub, choice[hub])
		switch {
		case r.File != "":
			logins = append(logins, Login{Hub: hub, Account: r.Account, File: r.File, How: r.How, Notes: r.Notes})
		case r.Reason != "":
			s := SkippedHub{Hub: hub, Reason: r.Reason, Notes: r.Notes}
			if r.How == Missing && len(r.Candidates) > 1 {
				s.Accounts = r.Candidates
			}
			skipped = append(skipped, s)
		}
	}
	return logins, skipped, nil
}

// SelectedLogins ist Logins für eine Wahl der Hubs: nur diese Hubs, nach
// Alias; ein gewählter Hub ohne Token-Datei und ohne Wahl des Accounts wird
// übergangen und genannt. Ohne Wahl (nil) alle Hubs wie Logins.
func SelectedLogins(tokensDir string, hubs []string, choice Choice) (logins []Login, skipped []SkippedHub, err error) {
	if hubs == nil {
		return Logins(tokensDir, choice)
	}
	want := map[string]bool{}
	for _, h := range hubs {
		want[h] = true
	}
	sub := Choice{}
	for hub, account := range choice {
		if want[hub] {
			sub[hub] = account
		}
	}
	all, skip, err := Logins(tokensDir, sub)
	if err != nil {
		return nil, nil, err
	}
	got := map[string]bool{}
	for _, l := range all {
		if want[l.Hub] {
			logins = append(logins, l)
			got[l.Hub] = true
		}
	}
	for _, s := range skip {
		if want[s.Hub] {
			skipped = append(skipped, s)
			got[s.Hub] = true
		}
	}
	for _, h := range hubs {
		if !got[h] {
			skipped = append(skipped, SkippedHub{Hub: h, Reason: fmt.Sprintf("gewählt, aber keine Token-Datei unter %s",
				filepath.Join(tokensDir, h))})
		}
	}
	sort.Slice(skipped, func(i, j int) bool { return skipped[i].Hub < skipped[j].Hub })
	return logins, skipped, nil
}

// Headers liest die Tokens der Anmeldungen und liefert die Header-Paare: je
// Hub X-Keph-Account-<alias> und X-Keph-Token-<alias>. Ein Hub, dessen
// Token-Datei sich nicht lesen lässt oder kein Token hält, fehlt und wird
// genannt. Das Ergebnis ist die einzige Stelle mit Tokens im Klartext.
func Headers(logins []Login) (headers map[string]string, skipped []SkippedHub) {
	headers = map[string]string{}
	for _, l := range logins {
		token, err := ReadTokenFile(l.File)
		if err != nil {
			skipped = append(skipped, SkippedHub{Hub: l.Hub, Reason: err.Error()})
			continue
		}
		headers[AccountHeaderPrefix+l.Hub] = l.Account
		headers[TokenHeaderPrefix+l.Hub] = token
	}
	return headers, skipped
}

// NodeURL ist die Adresse des MCP-Eingangs eines Nodes, der auf listen
// lauscht: http://<listen><path>; ein listen ohne Host (":7433") meint
// diesen Rechner.
func NodeURL(listen, path string) string {
	if strings.HasPrefix(listen, ":") {
		listen = "127.0.0.1" + listen
	}
	return "http://" + listen + path
}
