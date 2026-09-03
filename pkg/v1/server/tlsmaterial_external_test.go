package server_test

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	stdnet "net"
	"testing"
	"time"
)

// mintCert returns PEM certificate and key bytes for a throwaway identity.
//
// SANs, not CommonName: Go refuses to verify a certificate relying on the
// legacy field, so a CommonName-only fixture fails the handshake.
func mintCert(t *testing.T) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "kitsunium-test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth,
		},
		DNSNames:    []string{"kitsunium-test"},
		IPAddresses: []stdnet.IP{stdnet.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(nil, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
}

// dialTLS completes a real handshake against addr and returns the first line.
func dialTLS(t *testing.T, addr string, cfg *tls.Config) string {
	t.Helper()
	c, err := tls.Dial("tcp", addr, cfg)
	if err != nil {
		t.Fatalf("tls dial: %v", err)
	}
	defer func() {
		if cerr := c.Close(); cerr != nil {
			t.Errorf("close: %v", cerr)
		}
	}()
	if derr := c.SetReadDeadline(time.Now().Add(3 * time.Second)); derr != nil {
		t.Fatalf("set deadline: %v", derr)
	}
	reply, rerr := bufio.NewReader(c).ReadString('\n')
	if rerr != nil {
		t.Fatalf("read: %v", rerr)
	}
	return reply
}
