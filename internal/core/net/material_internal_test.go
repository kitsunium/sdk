// Package net — the TLS material parsing helpers.
package net

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// mintPEM returns a throwaway self-signed certificate and its key, so the tests
// never depend on fixture files that could silently expire.
func mintPEM(t *testing.T) (certPEM, keyPEM []byte) {
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

// Test_parseKeyPair pins the half-configured case. Both halves empty is a valid
// client posture; exactly one half present is a mistake that would otherwise
// surface much later as a confusing handshake failure, with mTLS having quietly
// become plain TLS in between.
func Test_parseKeyPair(t *testing.T) {
	t.Parallel()
	certPEM, keyPEM := mintPEM(t)
	otherCert, otherKey := mintPEM(t)

	type tc struct {
		name     string
		cert     []byte
		key      []byte
		wantCert int
		wantErr  bool
	}
	tests := []tc{
		{name: "neither half is a valid client posture"},
		{name: "a matching pair", cert: certPEM, key: keyPEM, wantCert: 1},
		{name: "a certificate with no key", cert: certPEM, wantErr: true},
		{name: "a key with no certificate", key: keyPEM, wantErr: true},
		{name: "a key belonging to another certificate", cert: certPEM, key: otherKey, wantErr: true},
		{name: "junk where a certificate belongs", cert: []byte("not a pem block"), key: keyPEM, wantErr: true},
		{name: "the two halves swapped", cert: otherKey, key: otherCert, wantErr: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got, err := parseKeyPair(c.cert, c.key)
		if c.wantErr {
			if !errs.HasCode(err, CodeTLSMaterialInvalid) {
				t.Fatalf("parseKeyPair(%s) = %v, want TLS_MATERIAL_INVALID", c.name, err)
			}
			//: the key bytes must never reach the error, which is logged.
			for _, f := range errs.FieldsOf(err) {
				if f.Key() == "cause" {
					t.Errorf("the error carries a cause field that may quote key bytes: %v", f)
				}
			}
			return
		}
		if err != nil {
			t.Fatalf("parseKeyPair(%s) = %v, want nil", c.name, err)
		}
		if len(got) != c.wantCert {
			t.Errorf("parseKeyPair(%s) returned %d certificates, want %d", c.name, len(got), c.wantCert)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_parsePool pins the trap this whole file exists to close.
// x509.CertPool.AppendCertsFromPEM reports success through a boolean that is
// almost universally discarded, so a bundle that is empty, truncated, or
// accidentally a private key produces a pool that parses fine and verifies
// nothing — surfacing later as a trusted connection to an untrusted peer.
func Test_parsePool(t *testing.T) {
	t.Parallel()
	certPEM, keyPEM := mintPEM(t)

	type tc struct {
		name    string
		bundle  []byte
		wantNil bool
		wantErr bool
	}
	tests := []tc{
		{name: "no bundle means the platform trust store", wantNil: true},
		{name: "a usable bundle", bundle: certPEM},
		{name: "junk", bundle: []byte("definitely not a certificate\n"), wantErr: true},
		//: the accident this rule was written for: a key file handed to the CA
		//: slot parses as PEM and yields a pool that trusts nothing.
		{name: "a private key where a bundle belongs", bundle: keyPEM, wantErr: true},
		{name: "a truncated PEM block", bundle: certPEM[:len(certPEM)/2], wantErr: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got, err := parsePool(c.bundle, "roots")
		if c.wantErr {
			if !errs.HasCode(err, CodeTLSMaterialInvalid) {
				t.Fatalf("parsePool(%s) = %v, want TLS_MATERIAL_INVALID", c.name, err)
			}
			//: a refused bundle must yield no pool, or a caller checking only
			//: the pool would install one that verifies nothing.
			if got != nil {
				t.Errorf("parsePool(%s) returned a pool beside the error", c.name)
			}
			return
		}
		if err != nil {
			t.Fatalf("parsePool(%s) = %v, want nil", c.name, err)
		}
		//: nil means "platform trust store" and is deliberately distinct from
		//: an empty pool that trusts nothing.
		if (got == nil) != c.wantNil {
			t.Errorf("parsePool(%s) returned nil = %v, want %v", c.name, got == nil, c.wantNil)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_resolveMinVersion pins that unset means the domain default rather than
// the stdlib's older floor, and that TLS 1.0/1.1 are refused rather than warned
// about — RFC 8996 deprecates them, and a warning in a log nobody reads is not
// a mitigation.
func Test_resolveMinVersion(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		in      uint16
		want    uint16
		wantErr bool
	}
	tests := []tc{
		{name: "unset applies the domain default", in: 0, want: defaultMinVersion},
		{name: "TLS 1.2 is accepted", in: tls.VersionTLS12, want: tls.VersionTLS12},
		{name: "TLS 1.3 is accepted", in: tls.VersionTLS13, want: tls.VersionTLS13},
		{name: "TLS 1.1 is refused", in: tls.VersionTLS11, wantErr: true},
		{name: "TLS 1.0 is refused", in: tls.VersionTLS10, wantErr: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got, err := resolveMinVersion(c.in)
		if c.wantErr {
			if !errs.HasCode(err, CodeTLSMaterialInvalid) {
				t.Fatalf("resolveMinVersion(%x) = %v, want TLS_MATERIAL_INVALID", c.in, err)
			}
			//: a refused version must not come back as a usable floor.
			if got != 0 {
				t.Errorf("resolveMinVersion(%x) returned %x beside the error", c.in, got)
			}
			return
		}
		if err != nil {
			t.Fatalf("resolveMinVersion(%x) = %v, want nil", c.in, err)
		}
		if got != c.want {
			t.Errorf("resolveMinVersion(%x) = %x, want %x", c.in, got, c.want)
		}
		//: whatever comes back must be at or above the accepted minimum.
		if got < minAcceptedVersion {
			t.Errorf("resolveMinVersion(%x) = %x, below the accepted minimum %x", c.in, got, minAcceptedVersion)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
