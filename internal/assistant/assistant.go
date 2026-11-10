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

// execWaitDelay ist, wie lange ExecRunner nach dem Ende eines Programms
// höchstens noch auf dessen Ausgabe wartet. Ein Kindprozess, den das
// Programm zurücklässt, erbt Standard- und Fehlerausgabe; ohne diese Grenze
// wartete Run — auch nach dem Kill an der Frist —, bis auch er endet.
const execWaitDelay = time.Second

// ExecRunner führt Programme wirklich aus, mit geschlossener Standardeingabe:
// Nichts wartet auf eine Rückfrage. Er kehrt spätestens an der Frist von ctx
// zurück, auch wenn das Programm Kindprozesse zurücklässt, die seine Ausgabe
// offen halten.
func ExecRunner(ctx context.Context, name string, args ...string) (string, error) {
	return execRun(ctx, execWaitDelay, name, args...)
}

// execRun ist ExecRunner mit wählbarer Wartezeit auf die Ausgabe (für Tests).
// Mit Frist wird das Programm um wait vor ihr beendet: Die Zeit, die Ausgabe
// danach noch zu lesen, liegt in der Frist, die damit die Obergrenze bleibt.
// Endet das Programm mit 0 und hält nur ein zurückgelassener Kindprozess die
// Ausgabe offen, gilt es nach wait als erfolgreich, mit dem bis dahin
// Gelesenen.
func execRun(ctx context.Context, wait time.Duration, name string, args ...string) (string, error) {
	if deadline, ok := ctx.Deadline(); ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, deadline.Add(-wait))
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = wait
	var out, errOut bytes.Buffer
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, &out, &errOut
	err := cmd.Run()
	if errors.Is(err, exec.ErrWaitDelay) {
		// Das meldet os/exec nur, wenn das Programm von selbst mit 0 endete.
		err = nil
	}
	if err != nil {
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

// choice ist die Wahl, die der Helfer ausdrücklich trägt (Task 037): nur
// die Hubs, deren Account ausdrücklich gewählt ist (--account oder die Wahl
// im eigenen Eintrag). Alle übrigen löst der Helfer bei jeder Verbindung
// auf — Standard, einzige Token-Datei —, ein neuer Standard wirkt dort also
// ohne add.
func (e entry) choice() Choice {
	c := Choice{}
	for _, l := range e.logins {
		if l.How == ByOverride {
			c[l.Hub] = l.Account
		}
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
	// Inhalt. Beim Helfer ist es die ausdrückliche (--account), bei OpenCode
	// jeder Account im Eintrag: Dort steht er immer fest, ausdrücklich oder
	// aufgelöst ist nicht zu unterscheiden.
	choice Choice
	// url ist die Adresse, die der Eintrag nennt; leer bei fremdem Inhalt.
	url string
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
	// Follow nennt je Hub den bisherigen Standard (gab es keinen: die bisher
	// einzige Token-Datei), für node account default (Task 037): Ein
	// OpenCode-Eintrag, der ihn trägt, folgt dem neuen Standard; einer mit
	// anderem Account bleibt als ausdrückliche Wahl.
	Follow map[string]string
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
// genannten, denn ob einer schon einen Eintrag hat, zählt für den
// automatischen Anstoß. selected sind die gefundenen aus names; results nennt
// die übrigen aus names (VS Code, nicht gefunden).
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
		choice := resolveChoice(t.TokensDir, s, opts.Choice, opts.Follow)
		logins, skip, err := t.logins(choice)
		if err != nil {
			return Report{}, err
		}
		rep.Notes = appendNew(rep.Notes, hubNotes(logins, skip)...)
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
	// Deviations nennen je Hub eine ausdrückliche Wahl im Eintrag, die vom
	// Standard abweicht (Task 037).
	Deviations []string
	Err        error
}

// Status zeigt je Assistent, ob der Node eingetragen ist, fehlt oder
// abweicht — gemessen an dem, was Add ohne --account eintragen würde — und
// jede ausdrückliche Wahl, die vom Standard abweicht. notes sind die
// Hinweise zur Wahl je Hub (etwa ein Standard ohne Token-Datei).
func (m *Manager) Status(ctx context.Context, t Target, assistants []string) (out []Status, hubs []SkippedHub,
	notes []string, err error) {
	names := assistants
	if len(names) == 0 {
		names = Names
	}
	names = selection(names)
	states, _, results := m.readAll(ctx, names)
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
		choice := resolveChoice(t.TokensDir, s, nil, nil)
		logins, skip, err := t.logins(choice)
		if err != nil {
			return nil, nil, nil, err
		}
		notes = appendNew(notes, hubNotes(logins, skip)...)
		for _, sk := range skip {
			skipped[sk.Hub] = sk
		}
		st.Logins, st.Warnings = logins, s.f.warnings
		for _, l := range logins {
			if l.How != ByOverride {
				continue
			}
			if r := Resolve(t.TokensDir, l.Hub, ""); r.How == ByDefault && r.Account != l.Account {
				st.Deviations = append(st.Deviations, fmt.Sprintf("ausdrückliche Wahl %s=%s im Eintrag, nicht der "+
					"Standard %s", l.Hub, l.Account, r.Account))
			}
		}
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
	for _, n := range names {
		out = append(out, byName[n])
	}
	for _, hub := range sortedKeys(skipped) {
		hubs = append(hubs, skipped[hub])
	}
	return out, hubs, notes, nil
}

// hubNotes sind die Hinweise zur Wahl aus Anmeldungen und übergangenen Hubs,
// je mit dem Hub davor.
func hubNotes(logins []Login, skipped []SkippedHub) []string {
	var out []string
	for _, l := range logins {
		for _, n := range l.Notes {
			out = appendNew(out, "Hub "+l.Hub+": "+n)
		}
	}
	for _, s := range skipped {
		for _, n := range s.Notes {
			out = appendNew(out, "Hub "+s.Hub+": "+n)
		}
	}
	return out
}

// resolveChoice ermittelt die ausdrückliche Wahl je Hub für den Eintrag
// eines Assistenten (Task 037): zuerst --account, dann die Wahl im eigenen
// Eintrag — beim Helfer sein --account, bei OpenCode jeder Account im
// Eintrag, solange seine Token-Datei liegt. Ein OpenCode-Account, der der
// bisherige Standard aus follow ist (node account default), gilt nicht als
// Wahl: Der Eintrag folgt dem neuen Standard. Aus den Einträgen anderer
// Assistenten wird nichts übernommen; alles Übrige wählt Resolve — der
// Standard, die einzige Token-Datei.
func resolveChoice(tokensDir string, self *state, explicit, follow Choice) Choice {
	choice := Choice{}
	for hub, account := range explicit {
		choice[hub] = account
	}
	for hub, own := range self.f.choice {
		if _, ok := choice[hub]; ok || own == "" {
			continue
		}
		if self.name == OpenCode && follow[hub] == own {
			continue
		}
		if fi, err := os.Stat(TokenFile(tokensDir, hub, own)); err == nil && !fi.IsDir() {
			choice[hub] = own
		}
	}
	return choice
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

// EntryChoice ist, was der Eintrag eines Assistenten trägt (Task 037): ob es
// ihn gibt, seine Adresse und die Wahl der Accounts — beim Helfer (Claude
// Code, Codex) die ausdrückliche (--account), bei OpenCode jeden Account im
// Eintrag.
type EntryChoice struct {
	Assistant string
	Exists    bool
	URL       string
	Choice    Choice
	Err       error
}

// Entries liest die Einträge aller gefundenen Assistenten mit eigenem
// Eintrag, in fester Reihenfolge; ein Assistent, der nicht im PATH liegt,
// fehlt.
func (m *Manager) Entries(ctx context.Context) []EntryChoice {
	_, all, _ := m.readAll(ctx, nil)
	out := make([]EntryChoice, 0, len(all))
	for _, s := range all {
		out = append(out, EntryChoice{Assistant: s.name, Exists: s.f.exists, URL: s.f.url, Choice: s.f.choice, Err: s.err})
	}
	return out
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
