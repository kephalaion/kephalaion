package assistant

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kephalaion/kephalaion/internal/config"
	"github.com/kephalaion/kephalaion/internal/ident"
)

// Der Standard-Account je Hub (Task 037): tokens/<hub>/default neben den
// Token-Dateien, eine Zeile mit dem Namen des Accounts, nie ein Token. Alle
// Clients dieses Rechners lesen ihn — die CLI (node mcp, node dir, node
// client), der Helfer bei jeder Verbindung und die Erweiterung für VS Code.
// Er steht nicht in der config: Die beschreibt die Installation, und bei
// einer globalen gibt es keine des Users; tokens/ gibt es bei beiden Arten
// und im Devcontainer, der es einbindet.

// defaultName ist der Name der Datei mit dem Standard im Verzeichnis eines
// Hubs. Accounts zählt nur <account>.token; diese Datei stört dort nicht.
const defaultName = "default"

// pendingSuffix ist die Endung eines wartenden Tokens: <account>.token.pending.
const pendingSuffix = tokenSuffix + ".pending"

// DefaultFile ist die Datei mit dem Standard-Account eines Hubs:
// <tokensDir>/<hub>/default.
func DefaultFile(tokensDir, hub string) string {
	return filepath.Join(tokensDir, hub, defaultName)
}

// ReadDefault liest den Standard-Account eines Hubs. Fehlt die Datei, ist das
// Ergebnis leer, ohne Fehler. Hält sie keinen gültigen Account-Namen, ist das
// ein Fehler — er nennt den Pfad, nie den Inhalt.
func ReadDefault(tokensDir, hub string) (string, error) {
	path := DefaultFile(tokensDir, hub)
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("Standard %s nicht lesbar: %w", path, pathErrReason(err))
	}
	defer f.Close()
	line, err := bufio.NewReader(io.LimitReader(f, 256)).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("Standard %s nicht lesbar: %w", path, err)
	}
	name := strings.TrimSpace(line)
	if ident.CheckPrincipalName("Account", name) != nil {
		return "", fmt.Errorf("Standard %s: kein gültiger Account-Name in der Datei", path)
	}
	return name, nil
}

// WriteDefault setzt den Standard-Account eines Hubs: der Name und eine
// Zeilenschaltung, 0600, atomar geschrieben.
func WriteDefault(tokensDir, hub, account string) error {
	if err := ident.CheckName("Hub", hub); err != nil {
		return err
	}
	if err := ident.CheckPrincipalName("Account", account); err != nil {
		return err
	}
	return config.WriteFileAtomic(DefaultFile(tokensDir, hub), []byte(account+"\n"), 0o600)
}

// RemoveDefault entfernt den Standard-Account eines Hubs; removed sagt, ob
// es einen gab.
func RemoveDefault(tokensDir, hub string) (removed bool, err error) {
	err = os.Remove(DefaultFile(tokensDir, hub))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

// PendingAccounts nennt die Accounts mit wartendem Token in hubDir
// (<account>.token.pending), nach Name; ein fehlendes Verzeichnis ist leer.
func PendingAccounts(hubDir string) ([]string, error) {
	entries, err := os.ReadDir(hubDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if name, ok := strings.CutSuffix(e.Name(), pendingSuffix); ok && !e.IsDir() &&
			ident.CheckPrincipalName("Account", name) == nil {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out, nil
}

// How sagt, woher die Wahl des Accounts eines Hubs kommt — in der
// Reihenfolge, in der jeder Client wählt.
type How string

const (
	// ByOverride: die eigene Überschreibung des Clients (--account,
	// kephalaion.accounts).
	ByOverride How = "override"
	// ByDefault: der Standard-Account des Hubs.
	ByDefault How = "default"
	// ByOnly: die einzige Token-Datei des Hubs.
	ByOnly How = "only"
	// Missing: Wahl fehlt — mehrere Token-Dateien ohne Überschreibung und
	// ohne Standard, oder keine.
	Missing How = "missing"
)

// Resolution ist die Wahl des Accounts für einen Hub, ohne Token.
type Resolution struct {
	Hub string
	// Account ist der gewählte Account; leer bei Missing.
	Account string
	How     How
	// File ist die Token-Datei des Accounts, absolut; leer, wenn es keine
	// gibt — bei Missing und bei einer Überschreibung ohne Token-Datei.
	File string
	// Candidates sind die Accounts mit Token-Datei, Pending die mit
	// wartendem Token (.pending), beide nach Name. Eine .pending allein macht
	// keinen Kandidaten.
	Candidates []string
	Pending    []string
	// Notes sind Hinweise nur zum Anzeigen, ohne Token: ein Standard ohne
	// Token-Datei, ein Standard, der sich nicht lesen lässt.
	Notes []string
	// Reason sagt, warum der Hub keine Anmeldung bekommt; leer, wenn er eine
	// bekommt oder schlicht keine Token-Datei hat.
	Reason string
}

// Resolve wählt den Account eines Hubs nach der Reihenfolge jedes Clients:
// die Überschreibung (override, leer heißt keine), der Standard, die einzige
// Token-Datei. Eine Überschreibung ohne Token-Datei bleibt die Wahl, ohne
// Rückfall; ein Standard ohne Token-Datei wird genannt, und die nächste Regel
// greift. Trifft nichts zu, fehlt die Wahl. Gelesen werden nur Namen, kein
// Token.
func Resolve(tokensDir, hub, override string) Resolution {
	r := Resolution{Hub: hub, How: Missing, Candidates: []string{}, Pending: []string{}}
	hubDir := filepath.Join(tokensDir, hub)
	accounts, err := Accounts(hubDir)
	if err != nil {
		r.Reason = fmt.Sprintf("Token-Dateien nicht lesbar: %v", err)
		return r
	}
	if accounts != nil {
		r.Candidates = accounts
	}
	if pending, err := PendingAccounts(hubDir); err == nil && pending != nil {
		r.Pending = pending
	}
	has := func(account string) bool {
		for _, a := range accounts {
			if a == account {
				return true
			}
		}
		return false
	}
	if override != "" {
		r.Account, r.How = override, ByOverride
		file := TokenFile(tokensDir, hub, override)
		if has(override) {
			r.File = file
			return r
		}
		r.Reason = fmt.Sprintf("der gewählte Account %s hat keine Token-Datei (%s)", override, file)
		return r
	}
	def, err := ReadDefault(tokensDir, hub)
	switch {
	case err != nil:
		r.Notes = append(r.Notes, err.Error()+"; es gilt die nächste Regel")
	case def != "" && has(def):
		r.Account, r.How, r.File = def, ByDefault, TokenFile(tokensDir, hub, def)
		return r
	case def != "":
		r.Notes = append(r.Notes, fmt.Sprintf("der Standard %s hat keine Token-Datei (%s); es gilt die nächste Regel",
			def, TokenFile(tokensDir, hub, def)))
	}
	switch len(accounts) {
	case 0:
	case 1:
		if err := ident.CheckPrincipalName("Account", accounts[0]); err != nil {
			r.Reason = err.Error()
			return r
		}
		r.Account, r.How, r.File = accounts[0], ByOnly, TokenFile(tokensDir, hub, accounts[0])
	default:
		r.Reason = fmt.Sprintf("mehrere Accounts mit Token-Datei (%s), keiner gewählt und kein Standard — Wahl fehlt",
			strings.Join(accounts, ", "))
	}
	return r
}

// pathErrReason ist der Grund eines Dateifehlers ohne den Pfad.
func pathErrReason(err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}
