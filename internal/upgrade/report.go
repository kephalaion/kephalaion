package upgrade

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// Die Zustände eines Reports (Report.State).
const (
	// StateOK: GitHub hat geantwortet; Latest und UpdateAvailable gelten.
	StateOK = "ok"
	// StateUnchecked: noch nicht gefragt (serve vor der ersten Prüfung).
	StateUnchecked = "unchecked"
	// StateFailed: Die letzte Frage an GitHub ist gescheitert; Error sagt,
	// warum.
	StateFailed = "failed"
)

// Die Wege eines Upgrades (Report.Method).
const (
	// MethodSelf: Das Binary ersetzt sich selbst — kephalaion upgrade.
	MethodSelf = "self"
	// MethodExplicit: ein dev build, der sich selbst ersetzen kann, aber nur
	// mit ausdrücklicher Version — kephalaion upgrade --version vX.Y.Z.
	MethodExplicit = "explicit"
	// MethodAdmin: globale Installation ohne Schreibrecht — der Verwalter,
	// über Ansible oder sudo kephalaion upgrade && sudo systemctl restart
	// kephalaion.
	MethodAdmin = "admin"
	// MethodManual: kein Schreibrecht und keine globale Installation — wer
	// im Verzeichnis des Binarys schreiben darf.
	MethodManual = "manual"
)

// Report ist, was upgrade --check --json ausgibt und was whoami im Feld
// update zeigt: installierte und neueste Version, ob es ein Update gibt, ob
// sich dieses Binary selbst ersetzen kann und wie das Upgrade geht. Die
// Angaben zum Weg (self_upgrade, method, command, hint) kennt der Prozess
// selbst; latest und update_available nur, wenn GitHub geantwortet hat
// (state ok).
type Report struct {
	// State ist ok, unchecked oder failed.
	State string `json:"state"`
	// Error sagt bei unchecked und failed, warum latest fehlt.
	Error string `json:"error,omitempty"`
	// CheckedAt ist der Zeitpunkt der Frage an GitHub, RFC 3339 in UTC; fehlt
	// bei unchecked.
	CheckedAt string `json:"checked_at,omitempty"`
	// Version ist die installierte Version, bei einem dev build dev.
	Version  string `json:"version"`
	DevBuild bool   `json:"dev_build"`
	// Latest ist das neueste Release ohne Suffix.
	Latest string `json:"latest,omitempty"`
	// UpdateAvailable: Latest ist neuer als Version. Bei einem dev build nie
	// — ob er ersetzt wird, entscheidet der Mensch (--version).
	UpdateAvailable bool `json:"update_available"`
	// SelfUpgrade: Der Prozess darf im Verzeichnis des Binarys schreiben,
	// kephalaion upgrade kann es also ersetzen.
	SelfUpgrade bool `json:"self_upgrade"`
	// Method ist self, explicit, admin oder manual.
	Method string `json:"method"`
	// Command ist der Befehl für das Upgrade; leer bei manual.
	Command string `json:"command,omitempty"`
	// Hint sagt den Weg in einem Satz.
	Hint string `json:"hint"`
}

// Report fragt GitHub nach dem neuesten Release und liefert, was upgrade
// --check meldet. Scheitert die Frage, ist State failed und Error gesetzt;
// die Angaben zum Weg stehen trotzdem darin.
func (u *Upgrader) Report(ctx context.Context) Report {
	l, _ := u.Latest(ctx)
	return l.Report
}

// Local liefert den Report ohne Frage an GitHub: Version, Schreibrecht, Weg;
// State unchecked.
func (u *Upgrader) Local() Report {
	_, isDev := u.current()
	self, dir, _ := u.access()
	r := Report{State: StateUnchecked, Error: "noch nicht geprüft", Version: u.Current.Version, DevBuild: isDev,
		SelfUpgrade: self}
	r.Method, r.Command, r.Hint = u.way(self, dir, "", isDev)
	return r
}

func (u *Upgrader) now() time.Time {
	if u.Now != nil {
		return u.Now()
	}
	return time.Now()
}

// access prüft, ob sich dieses Binary selbst ersetzen lässt: eine Probedatei
// nach TempPattern im Verzeichnis des Binarys, wie beim Ersetzen. why sagt
// sonst, warum nicht.
func (u *Upgrader) access() (ok bool, dir, why string) {
	exe, err := u.executablePath()
	if err != nil {
		return false, "", err.Error()
	}
	dir = filepath.Dir(exe)
	ok, why = probeWrite(dir)
	return ok, dir, why
}

// probeWrite legt in dir eine Probedatei an und entfernt sie wieder.
func probeWrite(dir string) (ok bool, why string) {
	f, err := os.CreateTemp(dir, TempPattern)
	if err != nil {
		if errors.Is(err, fs.ErrPermission) {
			return false, "kein Schreibrecht in " + dir
		}
		return false, fmt.Sprintf("in %s lässt sich nicht schreiben (%v)", dir, err)
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return true, ""
}

// way sagt, wie das Upgrade geht: self, ein dev build nur mit Version,
// global der Verwalter, sonst wer im Verzeichnis schreiben darf. target ist
// die Zielversion für den Befehl; leer, wenn sie noch unbekannt ist.
func (u *Upgrader) way(self bool, dir, target string, isDev bool) (method, command, hint string) {
	version := target
	if version == "" {
		version = "vX.Y.Z"
	}
	switch {
	case self && isDev:
		command = "kephalaion upgrade --version " + version
		return MethodExplicit, command, "dieser dev build wird nur mit ausdrücklicher Version ersetzt: " + command
	case self:
		command = "kephalaion upgrade"
		return MethodSelf, command, command + " (ersetzt dieses Binary und startet einen laufenden Dienst pro User neu)"
	case u.System:
		command = "sudo kephalaion upgrade"
		if isDev {
			command += " --version " + version
		}
		command += " && sudo systemctl restart kephalaion"
		return MethodAdmin, command, "globale Installation — das Upgrade macht der Verwalter: über Ansible (Version " +
			"anheben) oder " + command
	}
	where := dir
	if where == "" {
		where = "seinem Verzeichnis"
	}
	return MethodManual, "", "kein Schreibrecht in " + where + " — ersetzen kann dieses Binary nur, wer dort schreiben " +
		"darf (kephalaion upgrade mit dessen Rechten, oder wie es installiert wurde)"
}

// Summary ist der Report in einer Zeile, wie whoami ihn im Textteil zeigt.
func (r Report) Summary() string {
	switch r.State {
	case StateUnchecked:
		return "Update: " + r.Error
	case StateFailed:
		return "Update: Prüfung gescheitert (" + r.CheckedAt + "): " + r.Error
	}
	switch {
	case r.UpdateAvailable:
		return fmt.Sprintf("Update: %s verfügbar (installiert %s, geprüft %s); Weg: %s", r.Latest, r.Version,
			r.CheckedAt, r.Hint)
	case r.DevBuild:
		return fmt.Sprintf("Update: dev build, neuestes Release %s (geprüft %s); Weg: %s", r.Latest, r.CheckedAt, r.Hint)
	}
	return fmt.Sprintf("Update: keins, %s ist aktuell (neuestes Release %s, geprüft %s)", r.Version, r.Latest, r.CheckedAt)
}
