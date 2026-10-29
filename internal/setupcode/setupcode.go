// Package setupcode ist der Einrichtungscode eines Nodes (Task 034): alles,
// was ein Node braucht, um sich an einen Hub anzuschließen, in einer Zeile —
// Fassung, Adresse des Hubs, Alias, Node-Name und Token des Nodes. Der Hub
// erzeugt ihn (hub node add|token), der Node löst ihn ein (node setup) und
// rotiert das Token darin sofort.
//
// Form: Prefix, dahinter JSON als base64url ohne Padding. Der Präfix lässt
// Scanner wie gitleaks einen Code erkennen, wie keph_ ein Token. Das Paket
// ist neutral: Es kennt weder Hub noch Node.
package setupcode

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/kephalaion/kephalaion/internal/ident"
)

// Prefix steht vor jedem Code. Er ist kein Präfix eines Tokens (keph_), so
// dass ein Code nie als Token durchgeht und umgekehrt.
const Prefix = "kephsetup_"

// Version ist die Fassung des Codes, die dieses Binary schreibt und liest.
const Version = 1

// MaxLen begrenzt die Länge eines Codes beim Lesen.
const MaxLen = 4096

// Die Transporte, die eine Adresse im Code ergibt; dieselben Namen wie am
// Node.
const (
	TransportHTTPS = "https"
	TransportHTTP  = "http"
)

var encoding = base64.RawURLEncoding

// Code ist der Inhalt eines Einrichtungscodes.
type Code struct {
	// Address ist die Adresse des Hubs: https://host[:port][/pfad], auf
	// diesem Rechner auch http://localhost[:port][/pfad] (CheckAddress).
	Address string
	// Alias ist der Alias, unter dem der Node den Hub einträgt; leer, wenn
	// der Hub keinen nennt (dann verlangt node setup --alias).
	Alias string
	// Node ist der Name des Nodes am Hub.
	Node string
	// Token ist das Token des Nodes.
	Token string
}

// payload ist die Form im Code.
type payload struct {
	V       int    `json:"v"`
	Address string `json:"address"`
	Alias   string `json:"alias,omitempty"`
	Node    string `json:"node"`
	Token   string `json:"token"`
}

// Check prüft einen Code ohne Netz: Adresse, Alias (wenn gesetzt),
// Node-Name und Token.
func (c Code) Check() error {
	if err := CheckAddress(c.Address); err != nil {
		return err
	}
	if c.Alias != "" {
		if err := ident.CheckName("Hub", c.Alias); err != nil {
			return err
		}
	}
	if err := ident.CheckPrincipalName("Node", c.Node); err != nil {
		return err
	}
	return ident.CheckToken(c.Token)
}

// Transport ist der Transport, den die Adresse ergibt: https, oder http für
// eine Adresse auf diesem Rechner.
func (c Code) Transport() string {
	if strings.HasPrefix(c.Address, "http://") {
		return TransportHTTP
	}
	return TransportHTTPS
}

// Encode schreibt einen Code; er wird vorher geprüft.
func Encode(c Code) (string, error) {
	if err := c.Check(); err != nil {
		return "", err
	}
	data, err := json.Marshal(payload{V: Version, Address: c.Address, Alias: c.Alias, Node: c.Node, Token: c.Token})
	if err != nil {
		return "", err
	}
	return Prefix + encoding.EncodeToString(data), nil
}

// ErrNotCode meldet einen Text, der kein Einrichtungscode ist; die Meldung
// nennt den Text nie, er kann ein Geheimnis sein.
var ErrNotCode = errors.New("kein Einrichtungscode")

// Decode liest einen Code und prüft ihn wie Check. Eine andere Fassung als
// Version ist ein eigener Fehler; Felder, die Fassung 1 nicht kennt, sind
// ungültig.
func Decode(s string) (Code, error) {
	s = strings.TrimSpace(s)
	rest, ok := strings.CutPrefix(s, Prefix)
	if !ok {
		if strings.HasPrefix(s, ident.TokenPrefix) {
			return Code{}, fmt.Errorf("%w: das ist ein Token (%s…), kein Code (%s…)", ErrNotCode, ident.TokenPrefix, Prefix)
		}
		return Code{}, fmt.Errorf("%w: beginnt nicht mit %s", ErrNotCode, Prefix)
	}
	if len(s) > MaxLen {
		return Code{}, fmt.Errorf("%w: länger als %d Zeichen", ErrNotCode, MaxLen)
	}
	data, err := encoding.Strict().DecodeString(rest)
	if err != nil {
		return Code{}, fmt.Errorf("%w: nach %s wird base64url ohne Padding erwartet — unvollständig kopiert?", ErrNotCode, Prefix)
	}
	var head struct {
		V int `json:"v"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return Code{}, fmt.Errorf("%w: Inhalt nicht lesbar", ErrNotCode)
	}
	if head.V == 0 {
		return Code{}, fmt.Errorf("%w: ohne Fassung", ErrNotCode)
	}
	if head.V != Version {
		return Code{}, fmt.Errorf("Einrichtungscode in Fassung %d; dieses Binary kennt Fassung %d — "+
			"kephalaion upgrade?", head.V, Version)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var p payload
	if err := dec.Decode(&p); err != nil || dec.More() {
		return Code{}, fmt.Errorf("%w: Inhalt passt nicht zu Fassung %d", ErrNotCode, Version)
	}
	c := Code{Address: p.Address, Alias: p.Alias, Node: p.Node, Token: p.Token}
	if err := c.Check(); err != nil {
		// Die Meldungen von ident nennen kein Token.
		return Code{}, fmt.Errorf("Einrichtungscode: %w", err)
	}
	return c, nil
}

// Mask liefert die gekürzte Anzeige eines Codes: Prefix, … und die letzten
// vier Zeichen. Ein leerer Code bleibt leer.
func Mask(s string) string {
	if s == "" {
		return ""
	}
	if len(s) <= len(Prefix)+4 {
		return Prefix + "…"
	}
	return Prefix + "…" + s[len(s)-4:]
}

// loopback sind die Hosts, die http erlaubt: nur dieser Rechner — dieselbe
// Regel wie am Node für Transport http.
var loopback = map[string]bool{"localhost": true, "127.0.0.1": true, "::1": true}

// CheckAddress prüft die Adresse eines Hubs für einen Code: https://host
// [:port][/pfad], oder http:// auf localhost, 127.0.0.1 bzw. ::1 — ohne
// Query, Fragment und User, wie node hub add --address. Einen bestimmten
// Pfad verlangt es nicht; die Adresse eines serve endet auf /hub.
func CheckAddress(addr string) error {
	u, err := url.Parse(addr)
	if err == nil && u.Host != "" && u.RawQuery == "" && u.Fragment == "" && u.User == nil && !u.ForceQuery {
		switch {
		case u.Scheme == "https":
			return nil
		case u.Scheme == "http" && loopback[u.Hostname()]:
			return nil
		}
	}
	return fmt.Errorf("Adresse %q: erwartet https://host[:port][/pfad] (etwa https://host/kephalaion/hub), "+
		"auf diesem Rechner auch http://localhost:<port>/hub — ohne Query und ohne User", addr)
}

// CheckPublicURL prüft die Adresse, unter der Nodes anderer Rechner den Hub
// erreichen (settings public_url): nur https, sonst wie CheckAddress.
func CheckPublicURL(addr string) error {
	if !strings.HasPrefix(addr, "https://") {
		return fmt.Errorf("Adresse %q: public_url ist die Adresse für Nodes anderer Rechner, "+
			"https://host[:port][/pfad] (etwa https://host/kephalaion/hub)", addr)
	}
	return CheckAddress(addr)
}
