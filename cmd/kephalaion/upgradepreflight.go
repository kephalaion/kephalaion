package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/kephalaion/kephalaion/internal/buildinfo"
	"github.com/kephalaion/kephalaion/internal/config"
	"github.com/kephalaion/kephalaion/internal/sqlitedb"
)

// upgrade --preflight --json ist die Probe, die upgrade --auto mit dem
// heruntergeladenen Binary macht, bevor es das installierte ersetzt (Task
// 036): Passt das neue Binary zu den Datenbanken dieses Rechners? Es öffnet
// je Rolle der geltenden config die Datenbank mit store.Open (Rolle und
// Schemafassung über CheckInfo) und schließt sie sofort; geändert wird
// nichts, auch nicht neben einem laufenden serve. Die Antwort ist ok oder
// veto. Ein Veto trägt reason und hint aus diesem Binary: So meldet ein
// künftiges Release einen Schritt von Hand, den das installierte, ältere
// Binary nicht kennen muss.

// Das Ergebnis der Probe (preflightReport.Result).
const (
	preflightOK   = "ok"
	preflightVeto = "veto"
)

// Die Gründe eines Vetos (preflightReport.Reason), die dieses Binary kennt.
// upgrade --auto unterscheidet nur schema von allen anderen — auch von
// Gründen, die erst ein künftiges Release einführt.
const (
	// vetoSchema: Die Schemafassung einer Datenbank passt nicht zu diesem
	// Binary.
	vetoSchema = "schema"
	// vetoDatabase: Die Datenbank einer Rolle lässt sich nicht prüfen —
	// sie fehlt, ist nicht lesbar oder gehört zu einer anderen Rolle.
	vetoDatabase = "database"
	// vetoConfig: Die config ist für dieses Binary nicht lesbar.
	vetoConfig = "config"
)

// Der Zustand einer Rolle in der Probe (preflightRole.State).
const (
	roleCheckOK     = "ok"
	roleCheckSchema = "schema"
	roleCheckError  = "error"
)

// preflightReport ist die Antwort von upgrade --preflight --json.
type preflightReport struct {
	// Result ist ok oder veto.
	Result string `json:"result"`
	// Reason ist nur bei veto gesetzt: schema, database oder config — ein
	// künftiges Release darf weitere nennen.
	Reason string `json:"reason,omitempty"`
	// Hint sagt bei veto in einem Satz, was zu tun ist.
	Hint string `json:"hint,omitempty"`
	// Version ist die Version des Binarys, das die Probe macht.
	Version string `json:"version"`
	// Config ist die geltende config; leer, wenn ihr Ort unbekannt ist.
	Config string `json:"config,omitempty"`
	// Roles sind die eingerichteten Rollen mit ihrem Zustand; ohne config
	// leer.
	Roles map[string]preflightRole `json:"roles"`
}

// preflightRole ist der Zustand einer Rolle: ok, schema mit den Fassungen
// want (dieses Binary) und got (die Datenbank) oder error mit dem Fehler.
type preflightRole struct {
	State string `json:"state"`
	Want  string `json:"want,omitempty"`
	Got   string `json:"got,omitempty"`
	Error string `json:"error,omitempty"`
}

// preflight macht die Probe für die geltende config: die Suche wie bei jedem
// Kommando, also auch KEPHALAION_CONFIG, das upgrade --auto setzt.
func preflight(ctx context.Context) preflightReport {
	rep := preflightReport{Result: preflightOK, Version: buildinfo.Get().Version, Roles: map[string]preflightRole{}}
	veto := func(reason, hint string) preflightReport {
		rep.Result, rep.Reason, rep.Hint = preflightVeto, reason, hint
		return rep
	}
	loc, err := config.Locate("")
	if err != nil {
		return veto(vetoConfig, fmt.Sprintf("Ort der config nicht ermittelbar (%v)", err))
	}
	rep.Config = loc.Path
	cfg, _, err := config.Load(loc.Path)
	if err != nil {
		return veto(vetoConfig, fmt.Sprintf("Release %s kann die config nicht lesen (%v); vor dem Upgrade die config "+
			"anpassen, siehe die Release-Notizen", rep.Version, err))
	}
	var schema, broken []string
	for _, r := range config.Roles {
		sec := cfg.Section(r)
		if sec == nil {
			continue
		}
		st := checkRoleDB(ctx, r, sec)
		rep.Roles[string(r)] = st
		switch st.State {
		case roleCheckSchema:
			schema = append(schema, fmt.Sprintf("%s: Datenbank %s, erwartet %s", r, orDash(st.Got), st.Want))
		case roleCheckError:
			broken = append(broken, fmt.Sprintf("%s: %s", r, st.Error))
		}
	}
	switch {
	case len(schema) > 0:
		return veto(vetoSchema, fmt.Sprintf("Release %s erwartet eine andere Schemafassung (%s); das Upgrade geht "+
			"nur von Hand: docs/installation.md, „Neue Schemafassung“", rep.Version, strings.Join(schema, "; ")))
	case len(broken) > 0:
		return veto(vetoDatabase, fmt.Sprintf("Datenbank nicht prüfbar (%s); erst die Einrichtung reparieren "+
			"(kephalaion status), dann erneut", strings.Join(broken, "; ")))
	}
	return rep
}

// checkRoleDB öffnet die Datenbank einer Rolle mit store.Open und schließt
// sie sofort wieder.
func checkRoleDB(ctx context.Context, r config.Role, sec *config.Section) preflightRole {
	s, err := openSection(ctx, r, sec)
	if err != nil {
		var sv *sqlitedb.SchemaVersionError
		if errors.As(err, &sv) {
			return preflightRole{State: roleCheckSchema, Want: sv.Want, Got: sv.Got, Error: err.Error()}
		}
		return preflightRole{State: roleCheckError, Error: err.Error()}
	}
	if err := s.Close(); err != nil {
		return preflightRole{State: roleCheckError, Error: err.Error()}
	}
	return preflightRole{State: roleCheckOK}
}
