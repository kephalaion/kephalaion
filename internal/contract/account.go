package contract

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/kephalaion/kephalaion/internal/ident"
)

// AccountRowPrefix steht vor dem Namen der Zeile eines Accounts in
// documents: SYSTEM:A:<account>, je Account und Collection eine Zeile. Die
// Zeilen gleichen sich wie jede andere ab; der Node prüft seine Clients
// gegen sie.
const AccountRowPrefix = "SYSTEM:A:"

// AccountRowName liefert den Namen der Zeile eines Accounts.
func AccountRowName(account string) string { return AccountRowPrefix + account }

// AccountOfRow liefert den Account einer Zeile, oder ok false, wenn der Name
// keine Account-Zeile ist.
func AccountOfRow(name string) (account string, ok bool) {
	account, ok = strings.CutPrefix(name, AccountRowPrefix)
	return account, ok && account != ""
}

// Rights sind die Rechte eines Accounts in einer Collection über read hinaus;
// read ergibt sich aus der Zeile selbst. write und supersede sind unabhängig:
// write betrifft Eigenes, supersede Fremdes. Vendor sind die Scopes
// vendor/<name>: Unter vendor/<name>/ zählt allein der Scope — write ist dort
// weder nötig noch genügt es, und der Urheber spielt keine Rolle (siehe
// MayWrite). Die Liste ist sortiert und ohne Doppel; leer und fehlend sind
// dasselbe (NormalizeRights).
type Rights struct {
	Write     bool     `json:"write"`
	Supersede bool     `json:"supersede"`
	Vendor    []string `json:"vendor,omitempty"`
}

// String nennt die Rechte, read immer zuerst, die Scopes zuletzt: „read,
// write, vendor/k-playbook“.
func (r Rights) String() string {
	out := []string{"read"}
	if r.Write {
		out = append(out, "write")
	}
	if r.Supersede {
		out = append(out, "supersede")
	}
	for _, v := range r.Vendor {
		out = append(out, VendorDir+"/"+v)
	}
	return strings.Join(out, ", ")
}

// Equal sagt, ob zwei Rechte dieselben sind; die Scopes zählen als Menge.
func (r Rights) Equal(o Rights) bool {
	return r.Write == o.Write && r.Supersede == o.Supersede && slices.Equal(vendorSet(r.Vendor), vendorSet(o.Vendor))
}

// HasVendor sagt, ob die Rechte den Scope vendor/<name> tragen.
func (r Rights) HasVendor(name string) bool { return slices.Contains(r.Vendor, name) }

// vendorSet liefert die Scopes sortiert und ohne Doppel, nil wenn leer.
func vendorSet(list []string) []string {
	if len(list) == 0 {
		return nil
	}
	out := slices.Clone(list)
	slices.Sort(out)
	return slices.Compact(out)
}

// CheckVendorName prüft den Namen eines Scopes vendor/<name>: die
// Namensregel wie bei Collections (ident.CheckName).
func CheckVendorName(name string) error {
	return ident.CheckName("Scope "+VendorDir+"/<name>", name)
}

// NormalizeRights prüft die Rechte und bringt sie in die gespeicherte Form:
// jeder Scope nach der Namensregel, die Liste sortiert und ohne Doppel, leer
// wird nil. So sind gleiche Rechte auch als JSON gleich.
func NormalizeRights(r Rights) (Rights, error) {
	for _, v := range r.Vendor {
		if err := CheckVendorName(v); err != nil {
			return Rights{}, err
		}
	}
	r.Vendor = vendorSet(r.Vendor)
	return r, nil
}

// VendorDir ist das oberste Verzeichnis jeder Collection für mitgelieferte
// Vorlagen: vendor/<name>/… (docs/konzept.md, „vendor/“). Nur diese
// Schreibweise ist besonders.
const VendorDir = "vendor"

// VendorOf ordnet einen Dokumentnamen der Regel für vendor/ zu: Liegt er
// unter vendor/<name>/ — erstes Segment vendor, zweites <name>, darunter
// weitere —, ist vendor dieser <name>; dort zählt allein der Scope. Ist der
// Name genau vendor oder liegt er direkt in vendor/ (vendor/x.md), ist
// reserved wahr: Dort schreibt über einen Node niemand. Alles andere folgt
// der allgemeinen Regel (beides leer). Ein Verzeichnis bewertet
// WritableUnder nach dem, was darunter läge.
func VendorOf(name string) (vendor string, reserved bool) {
	if name == VendorDir {
		return "", true
	}
	rest, ok := strings.CutPrefix(name, VendorDir+"/")
	if !ok {
		return "", false
	}
	vendor, _, deeper := strings.Cut(rest, "/")
	if !deeper || vendor == "" {
		return "", true
	}
	return vendor, false
}

// Denial ist der Grund, aus dem Rechte ein Schreiben nicht erlauben — für
// die Meldung des Hubs (forbidden) und damit Hub und Node dieselbe Regel
// anwenden.
type Denial int

const (
	// DenyWrite: Neues oder Eigenes, write fehlt.
	DenyWrite Denial = iota + 1
	// DenySupersede: Fremdes, supersede fehlt.
	DenySupersede
	// DenyVendor: unter vendor/<name>/, der Scope fehlt.
	DenyVendor
	// DenyReserved: vendor selbst oder direkt in vendor/; dort schreibt
	// niemand.
	DenyReserved
)

// WriteDenial sagt, warum ein Name mit diesen Rechten nicht geschrieben
// werden darf; Vendor ist bei DenyVendor der fehlende Scope.
type WriteDenial struct {
	Kind   Denial
	Vendor string
}

// Error nennt den Grund kurz: „write fehlt“, „supersede fehlt“, „Scope
// vendor/k-playbook fehlt“, „direkt in vendor/ schreibt niemand“. Wer das
// Dokument angelegt hat, ergänzt der Aufrufer.
func (d *WriteDenial) Error() string {
	switch d.Kind {
	case DenyWrite:
		return "write fehlt"
	case DenySupersede:
		return "supersede fehlt"
	case DenyVendor:
		return "Scope " + VendorDir + "/" + d.Vendor + " fehlt"
	case DenyReserved:
		return "direkt in " + VendorDir + "/ schreibt niemand"
	}
	return "Recht fehlt"
}

// MayWrite ist die eine Regel, welches Recht ein Dokumentname braucht — für
// create, write, delete und rename über einen Node, je betroffenem Dokument
// (bei rename mit altem und neuem Namen), und für writable am Node:
//
//   - unter vendor/<name>/: allein der Scope vendor/<name>; write ist dort
//     weder nötig noch genügt es, own spielt keine Rolle;
//   - genau vendor oder direkt in vendor/: niemand;
//   - sonst: write für Neues und Eigenes (own), supersede für Fremdes.
//
// SYSTEM:-Namen kommen hier nicht an; sie lehnt ident.CheckDocName ab. Der
// Rückgabewert ist nil, wenn das Schreiben erlaubt ist, sonst der Grund.
func (r Rights) MayWrite(name string, own bool) *WriteDenial {
	vendor, reserved := VendorOf(name)
	switch {
	case reserved:
		return &WriteDenial{Kind: DenyReserved}
	case vendor != "":
		if r.HasVendor(vendor) {
			return nil
		}
		return &WriteDenial{Kind: DenyVendor, Vendor: vendor}
	case own:
		if r.Write {
			return nil
		}
		return &WriteDenial{Kind: DenyWrite}
	}
	if r.Supersede {
		return nil
	}
	return &WriteDenial{Kind: DenySupersede}
}

// Writable sagt für ein Dokument, ob der Account es nach MayWrite anlegen
// oder als Eigenes ändern dürfte — was read am Node als writable meldet:
// unter vendor/<name>/ der Scope, direkt in vendor/ falsch, sonst write.
func (r Rights) Writable(name string) bool { return r.MayWrite(name, true) == nil }

// WritableUnder sagt für ein Verzeichnis (leer: die Wurzel der Collection),
// ob der Account darunter Dokumente anlegen dürfte — bewertet an einem
// Namen darunter: vendor selbst ist falsch, vendor/<name> und alles darunter
// hängt am Scope, sonst write.
func (r Rights) WritableUnder(dir string) bool {
	if dir != "" {
		dir = strings.TrimSuffix(dir, "/") + "/"
	}
	return r.Writable(dir + "x")
}

// AccountContent ist der Inhalt einer Account-Zeile: der Hash des Tokens
// (sha256 in Hex), der User, dem der Account gehört, und die Rechte in dieser
// Collection. Der Hub führt Hash und User maßgeblich in seiner Tabelle
// accounts; die Zeile trägt eine Kopie.
type AccountContent struct {
	Hash   string `json:"hash"`
	User   string `json:"user"`
	Rights Rights `json:"rights"`
}

var hashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// IsTokenHash sagt, ob h ein sha256 in Hex ist, wie Token-Hashes gespeichert
// werden (64 Zeichen 0-9a-f).
func IsTokenHash(h string) bool { return hashPattern.MatchString(h) }

// EncodeAccountContent liefert den Inhalt einer Account-Zeile als JSON-Text,
// immer in derselben Form; die Scopes stehen nur, wenn es welche gibt, sortiert
// und ohne Doppel.
func EncodeAccountContent(c AccountContent) (string, error) {
	if !IsTokenHash(c.Hash) {
		return "", fmt.Errorf("Account-Zeile: Hash ist kein sha256 in Hex")
	}
	if c.User == "" {
		return "", fmt.Errorf("Account-Zeile: user fehlt")
	}
	c.Rights.Vendor = vendorSet(c.Rights.Vendor)
	b, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// DecodeAccountContent liest den Inhalt einer Account-Zeile und prüft den
// Hash und dass ein User dasteht — eine Zeile ohne user (von einem Hub vor
// Task 006) ist ein Fehler dieser Zeile. Der Fehler nennt den Inhalt nicht.
// Ein fehlendes vendor ist eine leere Liste (ein Hub vor Task 016).
func DecodeAccountContent(s string) (AccountContent, error) {
	var c AccountContent
	if err := json.Unmarshal([]byte(s), &c); err != nil {
		return AccountContent{}, fmt.Errorf("Account-Zeile: Inhalt ist kein gültiges JSON")
	}
	if !IsTokenHash(c.Hash) {
		return AccountContent{}, fmt.Errorf("Account-Zeile: Hash ist kein sha256 in Hex")
	}
	if c.User == "" {
		return AccountContent{}, fmt.Errorf("Account-Zeile: user fehlt")
	}
	c.Rights.Vendor = vendorSet(c.Rights.Vendor)
	return c, nil
}
