// Package testcert erzeugt Zertifikate für Tests, ohne Netz und ohne Dateien:
// eine eigene CA und Server-Zertifikate darunter — für einen TLS-Testserver,
// der wie „Proxy plus Hub“ dasteht, und für einen Client, der ihn mit der
// richtigen oder einer falschen CA prüft. Nur Tests importieren das Paket; in
// das Binary kommt es nicht.
//
// Das Paket ist neutral und kennt weder Hub noch Node.
package testcert

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"testing"
	"time"
)

// CA ist eine Zertifizierungsstelle für Tests.
type CA struct {
	Cert *x509.Certificate
	Key  *ecdsa.PrivateKey
	// PEM ist das Zertifikat der CA, wie es in --ca-file stünde.
	PEM string
}

// NewCA legt eine CA mit dem Namen name an, zehn Jahre gültig.
func NewCA(t testing.TB, name string) *CA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial(t),
		Subject:               pkix.Name{CommonName: name},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return &CA{Cert: cert, Key: key, PEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))}
}

// Pool liefert die CA als Pool für tls.Config.RootCAs.
func (ca *CA) Pool() *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AddCert(ca.Cert)
	return pool
}

// Server stellt ein Server-Zertifikat für hosts aus: IP-Adressen werden
// IP-SANs, alles andere DNS-SANs. Gültig von notBefore bis notAfter; ein
// abgelaufenes entsteht mit einem notAfter in der Vergangenheit.
func (ca *CA) Server(t testing.TB, hosts []string, notBefore, notAfter time.Time) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial(t),
		Subject:      pkix.Name{CommonName: "Testserver"},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.Cert, &key.PublicKey, ca.Key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}
}

// ServerNow stellt ein Server-Zertifikat für hosts aus, das jetzt gilt, ein
// Jahr lang.
func (ca *CA) ServerNow(t testing.TB, hosts ...string) tls.Certificate {
	t.Helper()
	now := time.Now()
	return ca.Server(t, hosts, now.Add(-time.Hour), now.AddDate(1, 0, 0))
}

// ServerExpired stellt ein Server-Zertifikat für hosts aus, das seit einem
// Tag abgelaufen ist.
func (ca *CA) ServerExpired(t testing.TB, hosts ...string) tls.Certificate {
	t.Helper()
	now := time.Now()
	return ca.Server(t, hosts, now.AddDate(-1, 0, 0), now.Add(-24*time.Hour))
}

func serial(t testing.TB) *big.Int {
	t.Helper()
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 100))
	if err != nil {
		t.Fatal(err)
	}
	return n
}
