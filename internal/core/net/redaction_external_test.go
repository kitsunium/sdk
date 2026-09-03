package net_test

import (
	"crypto/tls"
	"fmt"
	"strings"
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// TestIdentityRedactsUnderEveryVerb pins that no fmt verb can spill TLS material.
// %#v is the one that matters: fmt bypasses String for Go-syntax formatting and
// would otherwise dump the unexported fields, which is how key material leaks in
// practice.
func TestIdentityRedactsUnderEveryVerb(t *testing.T) {
	t.Parallel()
	material := newSelfSigned(t)
	id, err := corenet.NewIdentityValue(corenet.IdentityParams{
		CertPEM: material.CertPEM, KeyPEM: material.KeyPEM, RootsPEM: material.CertPEM,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	verbs := []string{"%v", "%s", "%#v", "%+v"}
	for _, verb := range verbs {
		t.Run(verb, func(t *testing.T) {
			t.Parallel()
			runRedactionCase(t, verb, id)
		})
	}
}

// runRedactionCase renders the identity with one verb and asserts full redaction.
func runRedactionCase(t *testing.T, verb string, id corenet.IdentityValue) {
	t.Helper()
	got := fmt.Sprintf(verb, id)
	if got != "<redacted>" {
		t.Fatalf("%s rendered %q, want \"<redacted>\"", verb, got)
	}
	//: belt and braces — no PEM armour may appear whatever the rendering.
	if strings.Contains(got, "PRIVATE KEY") || strings.Contains(got, "CERTIFICATE") {
		t.Fatalf("%s leaked TLS material: %q", verb, got)
	}
}

// keyPairCase describes one certificate/key pairing scenario.
type keyPairCase struct {
	name    string
	cert    func(p pemPair) []byte
	key     func(p pemPair) []byte
	wantErr bool
}

// TestNewIdentityKeyPairing pins that a half-supplied keypair is refused at
// construction rather than surfacing much later as an opaque handshake failure.
func TestNewIdentityKeyPairing(t *testing.T) {
	t.Parallel()
	none := func(pemPair) []byte { return nil }
	cert := func(p pemPair) []byte { return p.CertPEM }
	key := func(p pemPair) []byte { return p.KeyPEM }
	cases := []keyPairCase{
		{name: "no material at all is a verify-only identity", cert: none, key: none, wantErr: false},
		{name: "certificate without key", cert: cert, key: none, wantErr: true},
		{name: "key without certificate", cert: none, key: key, wantErr: true},
		{name: "matching pair", cert: cert, key: key, wantErr: false},
		{name: "mismatched pair", cert: cert, key: func(pemPair) []byte {
			return newSelfSignedKeyOnly()
		}, wantErr: true},
	}
	material := newSelfSigned(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runKeyPairCase(t, tc, material)
		})
	}
}

// runKeyPairCase executes one keyPairCase against NewIdentity.
func runKeyPairCase(t *testing.T, tc keyPairCase, material pemPair) {
	t.Helper()
	_, err := corenet.NewIdentityValue(corenet.IdentityParams{
		CertPEM: tc.cert(material), KeyPEM: tc.key(material),
	})
	if tc.wantErr {
		if !errs.HasCode(err, corenet.CodeTLSMaterialInvalid) {
			t.Fatalf("expected TLS_MATERIAL_INVALID, got %v", err)
		}
		return
	}
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestIdentityVersionFloor pins the TLS version policy: unset means 1.3, and the
// versions deprecated by RFC 8996 are refused rather than warned about.
func TestIdentityVersionFloor(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		in      uint16
		want    uint16
		wantErr bool
	}{
		{name: "unset defaults to TLS 1.3", in: 0, want: tls.VersionTLS13},
		{name: "TLS 1.0 refused", in: tls.VersionTLS10, wantErr: true},
		{name: "TLS 1.1 refused", in: tls.VersionTLS11, wantErr: true},
		{name: "TLS 1.2 honoured", in: tls.VersionTLS12, want: tls.VersionTLS12},
		{name: "TLS 1.3 honoured", in: tls.VersionTLS13, want: tls.VersionTLS13},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			id, err := corenet.NewIdentityValue(corenet.IdentityParams{MinVersion: tc.in})
			if tc.wantErr {
				if !errs.HasCode(err, corenet.CodeTLSMaterialInvalid) {
					t.Fatalf("expected TLS_MATERIAL_INVALID, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got := id.ClientConfig().MinVersion; got != tc.want {
				t.Fatalf("MinVersion = %x, want %x", got, tc.want)
			}
		})
	}
}

// TestZeroIdentityStillPinsAModernFloor pins that the zero value is safe: a
// caller who never configured TLS must not silently inherit the stdlib default.
func TestZeroIdentityStillPinsAModernFloor(t *testing.T) {
	t.Parallel()
	var id corenet.IdentityValue
	if !id.IsZero() {
		t.Fatal("the zero value must report IsZero")
	}
	if got := id.ClientConfig().MinVersion; got != tls.VersionTLS13 {
		t.Fatalf("zero-value MinVersion = %x, want TLS 1.3", got)
	}
	if got := id.ServerConfig().MinVersion; got != tls.VersionTLS13 {
		t.Fatalf("zero-value server MinVersion = %x, want TLS 1.3", got)
	}
}

// TestMutualTLSRequiresAClientCABundle pins that a server cannot be configured to
// demand a client certificate it has no way to verify — that configuration would
// reject every peer at handshake time instead of failing loudly at startup.
func TestMutualTLSRequiresAClientCABundle(t *testing.T) {
	t.Parallel()
	material := newSelfSigned(t)
	_, err := corenet.NewIdentityValue(corenet.IdentityParams{
		CertPEM: material.CertPEM, KeyPEM: material.KeyPEM, RequireClientCert: true,
	})
	if !errs.HasCode(err, corenet.CodeTLSMaterialInvalid) {
		t.Fatalf("expected TLS_MATERIAL_INVALID, got %v", err)
	}
	id, err := corenet.NewIdentityValue(corenet.IdentityParams{
		CertPEM:           material.CertPEM,
		KeyPEM:            material.KeyPEM,
		ClientCAPEM:       material.CertPEM,
		RequireClientCert: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := id.ServerConfig().ClientAuth; got != tls.RequireAndVerifyClientCert {
		t.Fatalf("ClientAuth = %v, want RequireAndVerifyClientCert", got)
	}
}

// TestConfigsAreFreshPerCall pins that a caller mutating a returned *tls.Config
// cannot affect any other user of the same identity.
func TestConfigsAreFreshPerCall(t *testing.T) {
	t.Parallel()
	id, err := corenet.NewIdentityValue(corenet.IdentityParams{ServerName: "kitsune"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	first := id.ClientConfig()
	first.ServerName = "hijacked"
	first.InsecureSkipVerify = true
	second := id.ClientConfig()
	if second.ServerName != "kitsune" {
		t.Fatalf("ServerName leaked across calls: %q", second.ServerName)
	}
	if second.InsecureSkipVerify {
		t.Fatal("InsecureSkipVerify leaked across calls")
	}
}
