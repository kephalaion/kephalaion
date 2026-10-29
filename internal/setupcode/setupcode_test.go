package setupcode

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/kephalaion/kephalaion/internal/ident"
)

func newToken(t *testing.T) string {
	t.Helper()
	tok, err := ident.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func TestRoundTrip(t *testing.T) {
	tok := newToken(t)
	for _, c := range []Code{
		{Address: "https://vm.example/kephalaion/hub", Alias: "vm", Node: "sebastian-mac", Token: tok},
		{Address: "https://vm.example:8443/hub", Node: "laptop", Token: tok},
		{Address: "http://localhost:7434/hub", Alias: "lokal", Node: "laptop", Token: tok},
	} {
		s, err := Encode(c)
		if err != nil {
			t.Fatalf("Encode(%+v): %v", c, err)
		}
		if !strings.HasPrefix(s, Prefix) || strings.ContainsAny(s, " \n=+/") {
			t.Errorf("Form %q", s)
		}
		if strings.Contains(s, tok) {
			t.Error("das Token steht lesbar im Code")
		}
		got, err := Decode(" " + s + "\n")
		if err != nil {
			t.Fatalf("Decode: %v", err)
		}
		if got != c {
			t.Errorf("Rundweg: %+v, erwartet %+v", got, c)
		}
		want := TransportHTTPS
		if strings.HasPrefix(c.Address, "http://") {
			want = TransportHTTP
		}
		if got.Transport() != want {
			t.Errorf("Transport %s, erwartet %s", got.Transport(), want)
		}
		if m := Mask(s); m != Prefix+"…"+s[len(s)-4:] || strings.Contains(m, tok) {
			t.Errorf("Mask %q", m)
		}
	}
	if Mask("") != "" || Mask(Prefix) != Prefix+"…" {
		t.Error("Mask kurzer Werte")
	}
}

func TestEncodeChecks(t *testing.T) {
	tok := newToken(t)
	for _, c := range []Code{
		{Address: "http://vm.example/hub", Node: "laptop", Token: tok},
		{Address: "https://vm.example/hub?x=1", Node: "laptop", Token: tok},
		{Address: "https://u@vm.example/hub", Node: "laptop", Token: tok},
		{Address: "ftp://vm.example/hub", Node: "laptop", Token: tok},
		{Address: "https:///hub", Node: "laptop", Token: tok},
		{Address: "https://vm.example/hub", Alias: "Vm", Node: "laptop", Token: tok},
		{Address: "https://vm.example/hub", Node: "admin", Token: tok},
		{Address: "https://vm.example/hub", Node: "", Token: tok},
		{Address: "https://vm.example/hub", Node: "laptop", Token: "keph_kurz"},
	} {
		if _, err := Encode(c); err == nil {
			t.Errorf("Encode(%+v) angenommen", c)
		}
	}
}

// raw baut einen Code aus beliebigem JSON.
func raw(json string) string { return Prefix + base64.RawURLEncoding.EncodeToString([]byte(json)) }

func TestDecodeBroken(t *testing.T) {
	tok := newToken(t)
	good, err := Encode(Code{Address: "https://vm.example/hub", Alias: "vm", Node: "laptop", Token: tok})
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"leer":            "",
		"ohne Präfix":     strings.TrimPrefix(good, Prefix),
		"ein Token":       tok,
		"abgeschnitten":   good[:len(good)-5],
		"mit Padding":     good + "==",
		"kein base64":     Prefix + "ä!",
		"kein JSON":       raw("nicht json"),
		"Fassung 2":       raw(`{"v":2,"address":"https://vm.example/hub","node":"laptop","token":"` + tok + `"}`),
		"ohne Fassung":    raw(`{"address":"https://vm.example/hub","node":"laptop","token":"` + tok + `"}`),
		"fremdes Feld":    raw(`{"v":1,"address":"https://vm.example/hub","node":"laptop","token":"` + tok + `","ca":"x"}`),
		"zwei Werte":      raw(`{"v":1,"address":"https://vm.example/hub","node":"laptop","token":"` + tok + `"}{}`),
		"http entfernt":   raw(`{"v":1,"address":"http://vm.example/hub","node":"laptop","token":"` + tok + `"}`),
		"Alias ungültig":  raw(`{"v":1,"address":"https://vm.example/hub","alias":"a:b","node":"laptop","token":"` + tok + `"}`),
		"Node fehlt":      raw(`{"v":1,"address":"https://vm.example/hub","token":"` + tok + `"}`),
		"Token ungültig":  raw(`{"v":1,"address":"https://vm.example/hub","node":"laptop","token":"keph_x"}`),
		"Token fehlt":     raw(`{"v":1,"address":"https://vm.example/hub","node":"laptop"}`),
		"zu lang":         Prefix + strings.Repeat("A", MaxLen),
		"Adresse mit Usr": raw(`{"v":1,"address":"https://a@vm.example/hub","node":"laptop","token":"` + tok + `"}`),
	}
	for name, s := range cases {
		_, err := Decode(s)
		if err == nil {
			t.Errorf("%s: angenommen", name)
			continue
		}
		if strings.Contains(err.Error(), tok) || (len(s) > 20 && strings.Contains(err.Error(), s)) {
			t.Errorf("%s: die Meldung nennt Token oder Code: %v", name, err)
		}
	}
	if _, err := Decode(tok); !errors.Is(err, ErrNotCode) || !strings.Contains(err.Error(), "Token") {
		t.Errorf("Token statt Code: %v", err)
	}
	if _, err := Decode(raw(`{"v":2}`)); err == nil || !strings.Contains(err.Error(), "Fassung 2") {
		t.Errorf("Fassung 2: %v", err)
	}
}

func TestCheckPublicURL(t *testing.T) {
	for addr, ok := range map[string]bool{
		"https://vm.example/kephalaion/hub": true,
		"https://vm.example":                true,
		"http://localhost:7434/hub":         false,
		"https://vm.example/hub?x":          false,
		"vm.example/hub":                    false,
	} {
		if err := CheckPublicURL(addr); (err == nil) != ok {
			t.Errorf("CheckPublicURL(%q) = %v", addr, err)
		}
	}
	for addr, ok := range map[string]bool{
		"http://localhost:7434/hub": true,
		"http://127.0.0.1:7434/hub": true,
		"http://[::1]:7434/hub":     true,
		"http://10.0.0.1/hub":       false,
	} {
		if err := CheckAddress(addr); (err == nil) != ok {
			t.Errorf("CheckAddress(%q) = %v", addr, err)
		}
	}
}
