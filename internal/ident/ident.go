// Package ident hält, was Hub und Node beide über Namen und Token wissen: die
// Namensregel für Collections, Nodes und Hub-Aliase, die Adresse
// <hub>:<collection>, das Token-Format keph_…, seinen Hash und die gekürzte
// Anzeige, dazu die Pfadregeln für die Namen von Dokumenten. Es kennt weder
// Hub noch Node und steht beiden offen, später auch den Accounts.
package ident

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// namePattern ist die Namensregel: klein, beginnt mit Buchstabe oder Ziffer,
// höchstens 63 Zeichen, kein Doppelpunkt.
var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)

// reservedPrefix darf kein Name tragen, in keiner Schreibweise: Er ist für
// SYSTEM:-Zeilen reserviert.
const reservedPrefix = "system"

// CheckName prüft einen Namen gegen die Namensregel. what nennt die Art im
// Fehler, etwa „Collection“ oder „Node“.
func CheckName(what, name string) error {
	if name == "" {
		return fmt.Errorf("%s: Name fehlt", what)
	}
	if strings.HasPrefix(strings.ToLower(name), reservedPrefix) {
		return fmt.Errorf("%s %q: Namen mit dem Präfix %q sind reserviert", what, name, reservedPrefix)
	}
	if !namePattern.MatchString(name) {
		return fmt.Errorf("%s %q: ungültiger Name; erlaubt sind a–z, 0–9, '.', '_' und '-', "+
			"am Anfang a–z oder 0–9, höchstens 63 Zeichen", what, name)
	}
	return nil
}

// AdminName ist der Name, unter dem das Protokoll des Hubs den Verwalter
// führt (die CLI am Hub). Er ist als Account- und Node-Name reserviert, damit
// `account` und `carrier` in actions eindeutig bleiben. Collections und
// Hub-Aliase dürfen so heißen.
const AdminName = "admin"

// CheckPrincipalName prüft den Namen eines Accounts oder Nodes: die
// Namensregel wie CheckName, dazu ist AdminName reserviert. Account- und
// Node-Namen sind am Hub gemeinsam eindeutig; das prüft der Hub-Store.
func CheckPrincipalName(what, name string) error {
	if err := CheckName(what, name); err != nil {
		return err
	}
	if name == AdminName {
		return fmt.Errorf("%s %q: der Name ist reserviert — er steht im Protokoll des Hubs für den Verwalter", what, name)
	}
	return nil
}

// LogName liefert einen Namen für ein Log: den Namen selbst, wenn er der
// Namensregel folgt, sonst eine Maske — so kommt nichts Fremdes ins Log, kein
// Steuerzeichen und kein versehentlich als Name geschicktes Token.
func LogName(name string) string {
	if name == "" {
		return "-"
	}
	if namePattern.MatchString(name) {
		return name
	}
	return "(ungültig)"
}

// ParseAddress zerlegt eine Adresse <hub>:<collection> und prüft beide Teile
// gegen die Namensregel.
func ParseAddress(addr string) (hub, collection string, err error) {
	hub, collection, ok := strings.Cut(addr, ":")
	if !ok {
		return "", "", fmt.Errorf("Adresse %q: erwartet <hub>:<collection>", addr)
	}
	if err := CheckName("Hub", hub); err != nil {
		return "", "", fmt.Errorf("Adresse %q: %w", addr, err)
	}
	if err := CheckName("Collection", collection); err != nil {
		return "", "", fmt.Errorf("Adresse %q: %w", addr, err)
	}
	return hub, collection, nil
}

// Address liefert die Adresse <hub>:<collection>.
func Address(hub, collection string) string { return hub + ":" + collection }

// TokenPrefix steht vor jedem Token; Scanner wie gitleaks erkennen daran ein
// eingechecktes Token.
const TokenPrefix = "keph_"

// tokenBytes ist die Länge des Geheimnisses: 256 Bit.
const tokenBytes = 32

var tokenEncoding = base64.RawURLEncoding

// NewToken erzeugt ein Token: keph_ und 32 Zufallsbytes, base64url ohne
// Padding.
func NewToken() (string, error) {
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("Token erzeugen: %w", err)
	}
	return TokenPrefix + tokenEncoding.EncodeToString(b), nil
}

// CheckToken prüft das Format eines Tokens. Der Fehler nennt das Token nicht.
func CheckToken(token string) error {
	secret, ok := strings.CutPrefix(token, TokenPrefix)
	if !ok {
		return fmt.Errorf("ungültiges Token: beginnt nicht mit %s", TokenPrefix)
	}
	b, err := tokenEncoding.Strict().DecodeString(secret)
	if err != nil || len(b) != tokenBytes {
		return fmt.Errorf("ungültiges Token: nach %s werden %d Bytes base64url ohne Padding erwartet", TokenPrefix, tokenBytes)
	}
	return nil
}

// HashToken liefert sha256(token) als Hex, so wie es gespeichert wird.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// MaskToken liefert die gekürzte Anzeige eines Tokens: keph_… und die letzten
// vier Zeichen. Ein leeres Token bleibt leer.
func MaskToken(token string) string {
	if token == "" {
		return ""
	}
	if len(token) <= len(TokenPrefix)+4 {
		return TokenPrefix + "…"
	}
	return TokenPrefix + "…" + token[len(token)-4:]
}

// SystemPrefix steht vor den Namen der Zeilen in documents, die nur der Hub
// selbst schreibt, etwa SYSTEM:A:<account>. Er ist die einzige Ausnahme von
// „der Name ist ein Pfad“; Groß- und Kleinschreibung zählt wie überall im
// Namen.
const SystemPrefix = "SYSTEM:"

// Grenzen für den Namen eines Dokuments, in Bytes.
const (
	MaxDocNameBytes    = 1024
	MaxDocSegmentBytes = 255
)

// IsSystemName sagt, ob ein Dokumentname eine SYSTEM:-Zeile bezeichnet.
func IsSystemName(name string) bool { return strings.HasPrefix(name, SystemPrefix) }

// CheckDocName prüft den Namen eines Dokuments nach den Pfadregeln: relativ,
// Segmente durch '/' getrennt, kein '/' am Anfang oder Ende, kein leeres
// Segment, kein '.' oder '..'; UTF-8 ohne Steuerzeichen und ohne '\';
// höchstens MaxDocNameBytes gesamt und MaxDocSegmentBytes je Segment.
// SYSTEM:-Namen lehnt sie ab — die schreibt nur der Hub selbst.
func CheckDocName(name string) error {
	if name == "" {
		return errors.New("Dokument: Name fehlt")
	}
	if !utf8.ValidString(name) {
		return fmt.Errorf("Dokument %q: der Name ist kein gültiges UTF-8", name)
	}
	if IsSystemName(name) {
		return fmt.Errorf("Dokument %q: Namen mit dem Präfix %s sind dem Hub vorbehalten", name, SystemPrefix)
	}
	if len(name) > MaxDocNameBytes {
		return fmt.Errorf("Dokument %q: der Name ist länger als %d Bytes", name, MaxDocNameBytes)
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return fmt.Errorf("Dokument %q: der Name enthält ein Steuerzeichen", name)
		}
		if r == '\\' {
			return fmt.Errorf("Dokument %q: der Name enthält '\\'; Verzeichnisse trennt '/'", name)
		}
	}
	if strings.HasPrefix(name, "/") || strings.HasSuffix(name, "/") {
		return fmt.Errorf("Dokument %q: der Name beginnt oder endet mit '/'; erwartet ist ein relativer Pfad", name)
	}
	for _, seg := range strings.Split(name, "/") {
		switch {
		case seg == "":
			return fmt.Errorf("Dokument %q: der Name enthält ein leeres Segment ('//')", name)
		case seg == "." || seg == "..":
			return fmt.Errorf("Dokument %q: '.' und '..' sind als Segment nicht erlaubt", name)
		case len(seg) > MaxDocSegmentBytes:
			return fmt.Errorf("Dokument %q: ein Segment ist länger als %d Bytes", name, MaxDocSegmentBytes)
		}
	}
	return nil
}

// DocDirPrefix prüft ein Verzeichnis für list oder import und liefert es als
// Präfix mit genau einem '/' am Ende; das Wurzelverzeichnis ("" oder "/")
// ergibt "". Ein '/' am Ende der Angabe ist erlaubt.
func DocDirPrefix(dir string) (string, error) {
	if dir == "" || dir == "/" {
		return "", nil
	}
	trimmed := strings.TrimSuffix(dir, "/")
	if err := CheckDocName(trimmed); err != nil {
		return "", fmt.Errorf("Verzeichnis %q: %w", dir, err)
	}
	return trimmed + "/", nil
}

// DocAncestors liefert die Verzeichnisse über einem Namen, von oben nach
// unten: für a/b/c.md also a und a/b. Keines davon darf selbst ein Dokument
// sein.
func DocAncestors(name string) []string {
	var out []string
	for i := 0; i < len(name); i++ {
		if name[i] == '/' {
			out = append(out, name[:i])
		}
	}
	return out
}

// CheckRename prüft ein Umbenennen von name nach newName nach den
// Pfadregeln: beide Namen gültig (CheckDocName), das Ziel weder gleich der
// Quelle noch in ihr — x nach x/y machte ein Verzeichnis zu seinem eigenen
// Unterverzeichnis. Ob name ein Dokument oder ein Verzeichnis ist und ob das
// Ziel frei ist, weiß erst die Datenbank.
func CheckRename(name, newName string) error {
	if err := CheckDocName(name); err != nil {
		return err
	}
	if err := CheckDocName(newName); err != nil {
		return fmt.Errorf("neuer Name: %w", err)
	}
	if newName == name {
		return fmt.Errorf("Dokument %q: der neue Name ist der alte", name)
	}
	if strings.HasPrefix(newName, name+"/") {
		return fmt.Errorf("Dokument %q: der neue Name %q liegt darunter; nichts kann in sich selbst wandern", name, newName)
	}
	return nil
}

// DocRenamed liefert den Namen, den doc bekommt, wenn name nach newName
// umbenannt wird: newName selbst, wenn doc = name; liegt doc unter dem
// Verzeichnis name, newName mit dem Rest darunter. ok ist false, wenn doc
// weder name ist noch darunter liegt.
func DocRenamed(doc, name, newName string) (renamed string, ok bool) {
	if doc == name {
		return newName, true
	}
	rest, ok := strings.CutPrefix(doc, name+"/")
	if !ok {
		return "", false
	}
	return newName + "/" + rest, true
}

// DocChild liefert den Eintrag, unter dem ein Name im Verzeichnis prefix
// erscheint (prefix wie von DocDirPrefix): das nächste Segment und, ob es ein
// Unterverzeichnis ist. ok ist false, wenn der Name nicht unter prefix liegt.
func DocChild(prefix, name string) (child string, isDir, ok bool) {
	rest, ok := strings.CutPrefix(name, prefix)
	if !ok || rest == "" {
		return "", false, false
	}
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		return rest[:i], true, true
	}
	return rest, false, true
}
