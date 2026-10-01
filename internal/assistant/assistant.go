package assistant

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/kephalaion/kephalaion/internal/config"
)

// EntryName ist der Name des Eintrags bei jedem Assistenten.
const EntryName = "kephalaion"

// Die Werte von --assistant.
const (
	Claude   = "claude"
	OpenCode = "opencode"
	Codex    = "codex"
	VSCode   = "vscode"
)

// Names sind alle Werte von --assistant in fester Reihenfolge.
var Names = []string{Claude, OpenCode, Codex, VSCode}

// CheckName prüft einen Wert von --assistant.
func CheckName(name string) error {
	for _, n := range Names {
		if n == name {
			return nil
		}
	}
	return fmt.Errorf("--assistant %q: erwartet einen von %s", name, strings.Join(Names, ", "))
}

// Runner führt ein Programm aus und liefert seine Standardausgabe. Endet es
// nicht mit 0, ist err gesetzt und nennt die Fehlerausgabe. Tests ersetzen
// ihn: Kein Test ruft einen echten Assistenten auf.
type Runner func(ctx context.Context, name string, args ...string) (stdout string, err error)

// ExecRunner führt Programme wirklich aus, mit geschlossener Standardeingabe:
// Nichts wartet auf eine Rückfrage.
func ExecRunner(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var out, errOut bytes.Buffer
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, &out, &errOut
	if err := cmd.Run(); err != nil {
		call := filepath.Base(name) + " " + strings.Join(args[:min(len(args), 3)], " ")
		if ctx.Err() != nil {
			return out.String(), fmt.Errorf("%s: keine Antwort in der Frist", call)
		}
		if msg := lastLine(errOut.String()); msg != "" {
			return out.String(), fmt.Errorf("%s: %w: %s", call, err, msg)
		}
		return out.String(), fmt.Errorf("%s: %w", call, err)
	}
	return out.String(), nil
}

// lastLine ist die letzte nicht leere Zeile eines Texts.
func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// Manager trägt den Node bei den Assistenten ein, entfernt ihn und fragt den
// Stand ab. Die Felder sind für Tests überschreibbar; New setzt die echten
// Werte.
type Manager struct {
	// Run ruft die Kommandos der Assistenten auf.
	Run Runner
	// LookPath sucht ein Programm im PATH.
	LookPath func(name string) (string, error)
	// Getenv liest die Umgebung (CLAUDE_CONFIG_DIR, CODEX_HOME,
	// XDG_CONFIG_HOME).
	Getenv func(name string) string
	// Home ist das Heimatverzeichnis.
	Home string
	// Timeout ist die Frist je Aufruf eines Assistenten.
	Timeout time.Duration
}

// New liefert einen Manager für diesen Rechner und diesen User.
func New() *Manager {
	m := &Manager{Run: ExecRunner, LookPath: exec.LookPath, Getenv: os.Getenv, Timeout: 30 * time.Second}
	m.Home, _ = os.UserHomeDir()
	return m
}

// run ruft ein Kommando eines Assistenten mit Frist auf.
func (m *Manager) run(ctx context.Context, name string, args ...string) (string, error) {
	if m.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, m.Timeout)
		defer cancel()
	}
	return m.Run(ctx, name, args...)
}

// Target ist, was eingetragen wird: die Adresse des Nodes, das Binary und
// das Verzeichnis der Token-Dateien — alle aufgelöst und absolut —, dazu die
// Wahl der Hubs.
type Target struct {
	// URL ist die Adresse des MCP-Eingangs, etwa http://127.0.0.1:7433/mcp
	// oder https://<name>/kephalaion/mcp.
	URL string
	// Binary ist der absolute Pfad von kephalaion, für den Helfer.
	Binary string
	// TokensDir ist das Verzeichnis der Token-Dateien.
	TokensDir string
	// Hubs ist die Wahl der Hubs eines Eintrags mit entfernter Adresse: Nur
	// ihre Header-Paare gehen an den Node — fest im Eintrag, ein Hub, der
	// später unter tokens/ hinzukommt, geht nicht mit. nil heißt alle Hubs
	// mit Token-Datei (lokal, Task 022).
	Hubs []string
}

// logins ermittelt die Anmeldungen für das Ziel: mit Wahl der Hubs nur diese.
func (t Target) logins(choice Choice) ([]Login, []SkippedHub, error) {
	return SelectedLogins(t.TokensDir, t.Hubs, choice)
}

// entry ist der Eintrag eines Assistenten, wie er sein soll: die Adresse und
// die Anmeldungen je Hub.
type entry struct {
	Target
	logins []Login
}

// choice ist die Wahl, die der Eintrag ausdrücklich trägt: je Hub mit
// Anmeldung der Account.
func (e entry) choice() Choice {
	c := Choice{}
	for _, l := range e.logins {
		c[l.Hub] = l.Account
	}
	return c
}

// helper ist die Kommandozeile des Helfers für diesen Eintrag.
func (e entry) helper() string {
	return ShellJoin(HelperArgs(e.Binary, e.TokensDir, e.Hubs, e.choice()))
}

// found ist der Eintrag kephalaion, wie er bei einem Assistenten steht.
type found struct {
	// exists: Es gibt einen Eintrag kephalaion, gleich welchen Inhalts.
	exists bool
	// choice ist die Wahl je Hub, die der Eintrag trägt — leer bei fremdem
	// Inhalt.
	choice Choice
	// differs sagt, warum der Eintrag nicht der gewünschte ist; leer heißt:
	// Er stimmt. Gefüllt erst nach compare.
	differs string
	// warnings nennt, was am Eintrag auffällt, ohne dass add ihn ändern
	// müsste (bei OpenCode Verweise auf fehlende Token-Dateien).
	warnings []string
	// raw ist, was der Assistent trägt, für compare.
	raw any
}

// client ist ein Assistent mit eigenem Eintrag.
type client interface {
	// read liest den Eintrag kephalaion.
	read(ctx context.Context) (found, error)
	// compare sagt, warum der gelesene Eintrag vom gewünschten abweicht; leer
	// heißt gleich.
	compare(f found, want entry) string
	// write trägt den gewünschten Eintrag ein und ersetzt einen vorhandenen.
	write(ctx context.Context, f found, want entry) error
	// remove entfernt den Eintrag.
	remove(ctx context.Context, f found) error
	// needsLogin sagt, ob der Eintrag ohne Anmeldung nicht bestehen kann
	// (OpenCode: ohne Token-Datei kein Eintrag).
	needsLogin() bool
}

// client liefert den Assistenten name, wenn sein Programm im PATH liegt.
func (m *Manager) client(name string) (client, bool) {
	path, err := m.LookPath(name)
	if err != nil {
		return nil, false
	}
	switch name {
	case Claude:
		return &claudeClient{m: m, bin: path}, true
	case OpenCode:
		return &opencodeClient{m: m, bin: path}, true
	case Codex:
		return &codexClient{m: m, bin: path}, true
	}
	return nil, false
}

// clientNames sind die Assistenten mit eigenem Eintrag, in fester
// Reihenfolge.
var clientNames = []string{Claude, OpenCode, Codex}

// Outcome ist, was bei einem Assistenten geschah.
type Outcome string

// Die Ausgänge je Assistent.
const (
	// Registered: eingetragen oder ersetzt.
	Registered Outcome = "eingetragen"
	// Unchanged: Der Eintrag stimmte schon, oder es gab nichts zu entfernen.
	Unchanged Outcome = "unverändert"
	// Skipped: nichts getan — nicht gefunden, kein Eintrag bei einem
	// automatischen Anstoß, keine Token-Datei, VS Code.
	Skipped Outcome = "übergangen"
	// Removed: Der Eintrag ist entfernt.
	Removed Outcome = "entfernt"
	// Failed: ein Fehler bei diesem Assistenten.
	Failed Outcome = "Fehler"
)

// Result ist der Ausgang bei einem Assistenten.
type Result struct {
	Assistant string
	Outcome   Outcome
	// Replaced: Es stand schon ein Eintrag da, der ersetzt wurde.
	Replaced bool
	// Logins sind die Anmeldungen des Eintrags.
	Logins []Login
	// Detail sagt, warum übergangen wurde oder was am Eintrag abwich.
	Detail string
	Err    error
}

// Report ist das Ergebnis von Add oder Remove.
type Report struct {
	Results []Result
	// SkippedHubs sind Hubs, die in einem Eintrag fehlen, mit dem Grund.
	SkippedHubs []SkippedHub
	// Notes nennen Widersprüche zwischen den Einträgen.
	Notes []string
	// NoLogin: Es gibt keine Anmeldung (keine Token-Datei), und deshalb
	// wurde nirgends neu eingetragen.
	NoLogin bool
}

// Failed sagt, ob ein Assistent einen Fehler meldete oder ein Hub übergangen
// wurde: Exit-Code 1.
func (r Report) Failed() bool {
	if len(r.SkippedHubs) > 0 {
		return true
	}
	for _, res := range r.Results {
		if res.Outcome == Failed {
			return true
		}
	}
	return false
}

// AddOptions sind die Optionen von Add.
type AddOptions struct {
	// Assistants beschränkt auf die genannten; leer heißt alle gefundenen.
	Assistants []string
	// Choice ist --account <hub>=<account>.
	Choice Choice
	// DryRun: nur melden, was geschähe.
	DryRun bool
	// Auto ist ein automatischer Anstoß (install.sh, rotate, check): Er ändert
	// nur Assistenten mit Eintrag; hat keiner der gefundenen einen, trägt er
	// bei allen gefundenen ein.
	Auto bool
}

// selection liefert die Assistenten, um die es geht: die genannten oder
// alle mit eigenem Eintrag.
func selection(named []string) []string {
	if len(named) == 0 {
		return clientNames
	}
	var out []string
	seen := map[string]bool{}
	for _, n := range Names {
		for _, want := range named {
			if want == n && !seen[n] {
				seen[n] = true
				out = append(out, n)
			}
		}
	}
	return out
}

// vscodeHint sagt, wie VS Code zu seinem Eintrag kommt.
const vscodeHint = "über die Erweiterung für VS Code; hier ist nichts einzutragen"

// notFound ist der Grund, wenn das Programm eines Assistenten fehlt.
func notFound(name string) string {
	return fmt.Sprintf("nicht gefunden (%s liegt nicht im PATH)", name)
}

// state ist ein Assistent mit seinem gelesenen Eintrag.
type state struct {
	name string
	c    client
	f    found
	err  error
}

// readAll liest die Einträge aller gefundenen Assistenten — auch der nicht
// genannten, denn ihre Wahl der Accounts zählt für die genannten (siehe
// resolveChoice). selected sind die gefundenen aus names; results nennt die
// übrigen aus names (VS Code, nicht gefunden).
func (m *Manager) readAll(ctx context.Context, names []string) (selected, all []*state, results []Result) {
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}
	for _, name := range Names {
		if name == VSCode {
			if want[name] {
				results = append(results, Result{Assistant: name, Outcome: Skipped, Detail: vscodeHint})
			}
			continue
		}
		c, ok := m.client(name)
		if !ok {
			if want[name] {
				results = append(results, Result{Assistant: name, Outcome: Skipped, Detail: notFound(name)})
			}
			continue
		}
		s := &state{name: name, c: c}
		s.f, s.err = c.read(ctx)
		all = append(all, s)
		if want[name] {
			selected = append(selected, s)
		}
	}
	return selected, all, results
}

// Add trägt den Node bei den Assistenten ein. Ein richtiger Eintrag bleibt
// unverändert; ein Eintrag kephalaion mit anderem Inhalt wird ersetzt.
func (m *Manager) Add(ctx context.Context, t Target, opts AddOptions) (Report, error) {
	var rep Report
	states, all, results := m.readAll(ctx, selection(opts.Assistants))
	// „Hat noch keiner der gefundenen einen Eintrag“ zählt über alle
	// gefundenen, nicht nur die genannten.
	anyEntry := false
	for _, s := range all {
		anyEntry = anyEntry || (s.err == nil && s.f.exists)
	}
	skipped := map[string]SkippedHub{}
	noLogin := true
	for _, s := range states {
		res := Result{Assistant: s.name}
		if s.err != nil {
			res.Outcome, res.Err = Failed, s.err
			results = append(results, res)
			continue
		}
		choice, notes := resolveChoice(t.TokensDir, s, all, opts.Choice)
		rep.Notes = appendNew(rep.Notes, notes...)
		logins, skip, err := t.logins(choice)
		if err != nil {
			return Report{}, err
		}
		for _, sk := range skip {
			skipped[sk.Hub] = sk
		}
		noLogin = noLogin && len(logins) == 0
		want := entry{Target: t, logins: logins}
		res.Logins = logins
		switch {
		case opts.Auto && anyEntry && !s.f.exists:
			res.Outcome, res.Detail = Skipped, "kein Eintrag; ein automatischer Anstoß ändert nur eingetragene Assistenten"
		case len(logins) == 0 && !s.f.exists:
			res.Outcome, res.Detail = Skipped, "keine Token-Datei; ohne Anmeldung wird nicht neu eingetragen"
		case len(logins) == 0 && s.c.needsLogin():
			// Der Eintrag verwiese nur noch auf fehlende Token-Dateien.
			res.Detail = "keine Token-Datei mehr"
			res.Outcome = Removed
			if !opts.DryRun {
				if err := s.c.remove(ctx, s.f); err != nil {
					res.Outcome, res.Err = Failed, err
				}
			}
		default:
			diff := ""
			if s.f.exists {
				diff = s.c.compare(s.f, want)
			}
			if s.f.exists && diff == "" {
				res.Outcome = Unchanged
				break
			}
			res.Outcome, res.Replaced, res.Detail = Registered, s.f.exists, diff
			if !opts.DryRun {
				if err := s.c.write(ctx, s.f, want); err != nil {
					res.Outcome, res.Err = Failed, err
				}
			}
		}
		results = append(results, res)
	}
	rep.Results = sortResults(results)
	for _, hub := range sortedKeys(skipped) {
		rep.SkippedHubs = append(rep.SkippedHubs, skipped[hub])
	}
	rep.NoLogin = noLogin && len(states) > 0
	return rep, nil
}

// Remove entfernt den Eintrag kephalaion bei den Assistenten.
func (m *Manager) Remove(ctx context.Context, assistants []string, dryRun bool) Report {
	states, _, results := m.readAll(ctx, selection(assistants))
	for _, s := range states {
		res := Result{Assistant: s.name}
		switch {
		case s.err != nil:
			res.Outcome, res.Err = Failed, s.err
		case !s.f.exists:
			res.Outcome, res.Detail = Unchanged, "kein Eintrag"
		default:
			res.Outcome = Removed
			if !dryRun {
				if err := s.c.remove(ctx, s.f); err != nil {
					res.Outcome, res.Err = Failed, err
				}
			}
		}
		results = append(results, res)
	}
	return Report{Results: sortResults(results)}
}

// EntryState ist der Zustand des Eintrags bei einem Assistenten.
type EntryState string

// Die Zustände, die status zeigt.
const (
	// StateRegistered: Der Eintrag ist da und stimmt.
	StateRegistered EntryState = "eingetragen"
	// StateMissing: Es gibt keinen Eintrag.
	StateMissing EntryState = "fehlt"
	// StateDiffers: Es gibt einen Eintrag, den add ändern würde.
	StateDiffers EntryState = "weicht ab"
	// StateNotFound: Das Programm des Assistenten liegt nicht im PATH.
	StateNotFound EntryState = "nicht gefunden"
	// StateExtension: VS Code — den Node meldet die Erweiterung.
	StateExtension EntryState = "über die Erweiterung"
	// StateUnknown: Der Zustand ließ sich nicht feststellen.
	StateUnknown EntryState = "unbekannt"
)

// Status ist der Stand bei einem Assistenten.
type Status struct {
	Assistant string
	State     EntryState
	// Logins sind die Anmeldungen, die der Eintrag trägt bzw. tragen sollte.
	Logins []Login
	// Detail sagt, worin der Eintrag abweicht, bei VS Code, ob die
	// Erweiterung installiert ist.
	Detail string
	// Warnings nennen, was am Eintrag auffällt.
	Warnings []string
	Err      error
}

// Status zeigt je Assistent, ob der Node eingetragen ist, fehlt oder
// abweicht — gemessen an dem, was Add ohne --account eintragen würde.
func (m *Manager) Status(ctx context.Context, t Target, assistants []string) ([]Status, []SkippedHub, error) {
	names := assistants
	if len(names) == 0 {
		names = Names
	}
	names = selection(names)
	states, all, results := m.readAll(ctx, names)
	byName := map[string]Status{}
	for _, r := range results {
		st := Status{Assistant: r.Assistant, State: StateNotFound}
		if r.Assistant == VSCode {
			st.State, st.Detail = StateExtension, m.vscodeDetail()
		}
		byName[r.Assistant] = st
	}
	skipped := map[string]SkippedHub{}
	for _, s := range states {
		st := Status{Assistant: s.name}
		if s.err != nil {
			st.State, st.Err = StateUnknown, s.err
			byName[s.name] = st
			continue
		}
		choice, _ := resolveChoice(t.TokensDir, s, all, nil)
		logins, skip, err := t.logins(choice)
		if err != nil {
			return nil, nil, err
		}
		for _, sk := range skip {
			skipped[sk.Hub] = sk
		}
		st.Logins, st.Warnings = logins, s.f.warnings
		switch {
		case !s.f.exists:
			st.State = StateMissing
		default:
			if st.Detail = s.c.compare(s.f, entry{Target: t, logins: logins}); st.Detail != "" {
				st.State = StateDiffers
			} else {
				st.State = StateRegistered
			}
		}
		byName[s.name] = st
	}
	var out []Status
	for _, n := range names {
		out = append(out, byName[n])
	}
	var hubs []SkippedHub
	for _, hub := range sortedKeys(skipped) {
		hubs = append(hubs, skipped[hub])
	}
	return out, hubs, nil
}

// resolveChoice ermittelt die Wahl je Hub für den Eintrag eines Assistenten:
// zuerst --account, dann der eigene Eintrag, dann die anderen Einträge. Ein
// gewählter Account aus einem Eintrag, dessen Token-Datei fehlt, gilt als
// keine Wahl; widersprechen sich die anderen, gilt der Hub als ohne Wahl und
// notes nennt sie.
func resolveChoice(tokensDir string, self *state, all []*state, explicit Choice) (choice Choice, notes []string) {
	choice = Choice{}
	hubs := map[string]bool{}
	for hub, account := range explicit {
		choice[hub] = account
	}
	has := func(hub, account string) bool {
		fi, err := os.Stat(TokenFile(tokensDir, hub, account))
		return err == nil && !fi.IsDir()
	}
	for _, s := range all {
		for hub := range s.f.choice {
			hubs[hub] = true
		}
	}
	for _, hub := range sortedKeys(hubs) {
		if _, ok := choice[hub]; ok {
			continue
		}
		if own := self.f.choice[hub]; own != "" && has(hub, own) {
			choice[hub] = own
			continue
		}
		others := map[string][]string{}
		for _, s := range all {
			if s == self {
				continue
			}
			if a := s.f.choice[hub]; a != "" && has(hub, a) {
				others[a] = append(others[a], s.name)
			}
		}
		switch len(others) {
		case 0:
		case 1:
			for a := range others {
				choice[hub] = a
			}
		default:
			var parts []string
			for _, a := range sortedKeys(others) {
				parts = append(parts, fmt.Sprintf("%s (%s)", a, strings.Join(others[a], ", ")))
			}
			notes = append(notes, fmt.Sprintf("Hub %s: die Einträge nennen verschiedene Accounts: %s", hub, strings.Join(parts, "; ")))
		}
	}
	return choice, notes
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func appendNew(list []string, items ...string) []string {
	for _, it := range items {
		dup := false
		for _, have := range list {
			dup = dup || have == it
		}
		if !dup {
			list = append(list, it)
		}
	}
	return list
}

// sortResults ordnet die Ergebnisse in der festen Reihenfolge der
// Assistenten.
func sortResults(results []Result) []Result {
	order := map[string]int{}
	for i, n := range Names {
		order[n] = i
	}
	sort.SliceStable(results, func(i, j int) bool { return order[results[i].Assistant] < order[results[j].Assistant] })
	return results
}

// readFile liest eine Konfigurationsdatei; eine fehlende ist leer, exists
// false.
func readFile(path string) (data []byte, exists bool, err error) {
	data, err = os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}

// writeFile schreibt eine Konfigurationsdatei eines Assistenten atomar. Ist
// path ein Symlink, wird sein Ziel geschrieben, nicht der Link ersetzt; die
// Rechte einer vorhandenen Datei bleiben, eine neue bekommt 0600.
func writeFile(path string, data []byte) error {
	target := path
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		target = resolved
	}
	perm := fs.FileMode(0o600)
	if fi, err := os.Stat(target); err == nil {
		perm = fi.Mode().Perm()
	}
	return config.WriteFileAtomic(target, data, perm)
}
