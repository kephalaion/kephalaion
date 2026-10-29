package store

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/kephalaion/kephalaion/internal/ident"
	"github.com/kephalaion/kephalaion/internal/setupcode"
	"github.com/kephalaion/kephalaion/internal/sqlitedb"
)

// Schlüssel der settings des Hubs, die er kennt und prüft (Task 034). Beide
// stehen im Einrichtungscode eines Nodes.
const (
	// SettingPublicURL ist die Adresse, unter der Nodes anderer Rechner den
	// Hub erreichen: https://host[:port][/pfad], meist bis /hub.
	SettingPublicURL = "public_url"
	// SettingAlias ist der Alias, unter dem Nodes diesen Hub eintragen
	// (Namensregel wie ein Hub-Alias am Node).
	SettingAlias = "alias"
)

// ErrUnknownSetting meldet einen Schlüssel, den der Hub nicht kennt.
var ErrUnknownSetting = errors.New("unbekannter Schlüssel")

// settingChecks prüft den Wert je bekanntem Schlüssel.
var settingChecks = map[string]func(string) error{
	SettingPublicURL: setupcode.CheckPublicURL,
	SettingAlias:     func(v string) error { return ident.CheckName("Hub-Alias", v) },
}

// SettingKeys sind die bekannten Schlüssel, sortiert.
func SettingKeys() []string {
	keys := make([]string, 0, len(settingChecks))
	for k := range settingChecks {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// CheckSetting prüft Schlüssel und Wert, wie config set sie verlangt.
func CheckSetting(key, value string) error {
	check, ok := settingChecks[key]
	if !ok {
		return fmt.Errorf("%w %q; der Hub kennt: %v", ErrUnknownSetting, key, SettingKeys())
	}
	if err := check(value); err != nil {
		return fmt.Errorf("%s: %w", key, err)
	}
	return nil
}

// CheckSettings prüft die settings eines Imports: die Werte der bekannten
// Schlüssel wie config set. Unbekannte Schlüssel bleiben, wie sie sind —
// wie am Node; ein Export trägt, was in der Tabelle steht.
func CheckSettings(settings map[string]string) error {
	for _, k := range SettingKeys() {
		if v, ok := settings[k]; ok {
			if err := CheckSetting(k, v); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *sqliteStore) SetSetting(ctx context.Context, key, value string) error {
	if err := CheckSetting(key, value); err != nil {
		return err
	}
	return sqlitedb.SetSetting(ctx, s.db, key, value)
}

func (s *sqliteStore) UnsetSetting(ctx context.Context, key string) (bool, error) {
	if _, ok := settingChecks[key]; !ok {
		return false, fmt.Errorf("%w %q; der Hub kennt: %v", ErrUnknownSetting, key, SettingKeys())
	}
	return sqlitedb.UnsetSetting(ctx, s.db, key)
}
