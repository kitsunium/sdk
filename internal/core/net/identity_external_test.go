// Package net_test — the opaque TLS identity value: what it refuses at
// construction, what it never lets out, and what it hands to the handshake.
package net_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"slices"
	"strings"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// pemPair holds freshly minted PEM material for one test certificate.
type pemPair struct {
	CertPEM []byte
	KeyPEM  []byte
}

// newSelfSigned mints a throwaway self-signed certificate so the tests never
// depend on fixture files that could silently expire.
func newSelfSigned(t *testing.T) pemPair {
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
	return pemPair{
		CertPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		KeyPEM:  pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
	}
}

// newSelfSignedKeyOnly returns a PEM private key unrelated to any test
// certificate, so a pairing test can assert that a mismatched key is refused.
func newSelfSignedKeyOnly(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
}

// TestNewIdentityValue pins everything the constructor refuses, which is the
// whole reason this domain type exists.
//
// The trust-bundle cases are the trap: x509.CertPool.AppendCertsFromPEM reports
// failure through a boolean that callers routinely discard, so an empty,
// truncated or key-only bundle yields a pool that verifies NOTHING while every
// call appears to succeed. The keypair cases close the mirror-image hole, where
// half-supplied material lets mTLS quietly become plain TLS. Both must fail at
// construction, not at the first handshake.
func TestNewIdentityValue(t *testing.T) {
	t.Parallel()
	material := newSelfSigned(t)
	foreignKey := newSelfSignedKeyOnly(t)

	type tc struct {
		name string
		//: built from the shared material so each case reads as a scenario
		//: rather than as a pile of byte slices.
		params corenet.IdentityParams
		//: a non-nil trust pool must be installed on the client config.
		wantVerify bool
		wantErr    bool
	}
	tests := []tc{
		{
			name:   "no material at all is a verify-only identity",
			params: corenet.IdentityParams{},
		},
		{
			name:       "a real certificate as the trust bundle",
			params:     corenet.IdentityParams{RootsPEM: material.CertPEM},
			wantVerify: true,
		},
		{
			//: an empty bundle is indistinguishable from an absent one and
			//: falls back to the platform trust store.
			name:   "an empty but non-nil trust bundle",
			params: corenet.IdentityParams{RootsPEM: []byte{}},
		},
		{
			name:    "garbage where a trust bundle belongs",
			params:  corenet.IdentityParams{RootsPEM: []byte("not a pem bundle at all")},
			wantErr: true,
		},
		{
			name:    "well-formed PEM carrying no certificate",
			params:  corenet.IdentityParams{RootsPEM: material.KeyPEM},
			wantErr: true,
		},
		{
			name:    "a truncated certificate block",
			params:  corenet.IdentityParams{RootsPEM: material.CertPEM[:len(material.CertPEM)/2]},
			wantErr: true,
		},
		{
			name:   "a matching keypair",
			params: corenet.IdentityParams{CertPEM: material.CertPEM, KeyPEM: material.KeyPEM},
		},
		{
			name:    "a certificate with no key",
			params:  corenet.IdentityParams{CertPEM: material.CertPEM},
			wantErr: true,
		},
		{
			name:    "a key with no certificate",
			params:  corenet.IdentityParams{KeyPEM: material.KeyPEM},
			wantErr: true,
		},
		{
			name:    "a key belonging to another certificate",
			params:  corenet.IdentityParams{CertPEM: material.CertPEM, KeyPEM: foreignKey},
			wantErr: true,
		},
		{
			name:   "an explicit TLS 1.2 floor",
			params: corenet.IdentityParams{MinVersion: tls.VersionTLS12},
		},
		{
			//: RFC 8996 deprecates 1.0/1.1; refusing beats warning, because a
			//: warning in a log nobody reads is not a mitigation.
			name:    "a TLS 1.1 floor",
			params:  corenet.IdentityParams{MinVersion: tls.VersionTLS11},
			wantErr: true,
		},
		{
			name:    "a TLS 1.0 floor",
			params:  corenet.IdentityParams{MinVersion: tls.VersionTLS10},
			wantErr: true,
		},
		{
			//: demanding a client certificate with nothing to verify it against
			//: would reject every peer at handshake time; fail loudly instead.
			name: "mutual TLS with no client CA bundle",
			params: corenet.IdentityParams{
				CertPEM: material.CertPEM, KeyPEM: material.KeyPEM, RequireClientCert: true,
			},
			wantErr: true,
		},
		{
			name: "mutual TLS with a client CA bundle",
			params: corenet.IdentityParams{
				CertPEM: material.CertPEM, KeyPEM: material.KeyPEM,
				ClientCAPEM: material.CertPEM, RequireClientCert: true,
			},
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		id, err := corenet.NewIdentityValue(c.params)
		if c.wantErr {
			//: an unusable configuration must be refused with the domain's
			//: typed sentinel, never accepted as a silently empty trust store.
			if !errs.HasCode(err, corenet.CodeTLSMaterialInvalid) {
				t.Fatalf("NewIdentityValue(%s) = %v, want TLS_MATERIAL_INVALID", c.name, err)
			}
			return
		}
		if err != nil {
			t.Fatalf("NewIdentityValue(%s) = %v, want nil", c.name, err)
		}
		//: a nil RootCAs means "platform trust store"; a non-nil one means the
		//: bundle we supplied.
		if got := id.ClientConfig().RootCAs != nil; got != c.wantVerify {
			t.Errorf("RootCAs installed = %v, want %v", got, c.wantVerify)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_IdentityValue_String pins that the value-formatting verbs redact.
func Test_IdentityValue_String(t *testing.T) {
	t.Parallel()
	material := newSelfSigned(t)
	id, err := corenet.NewIdentityValue(corenet.IdentityParams{
		CertPEM: material.CertPEM, KeyPEM: material.KeyPEM, RootsPEM: material.CertPEM,
	})
	if err != nil {
		t.Fatalf("NewIdentityValue = %v, want nil", err)
	}

	type tc struct {
		name string
		verb string
	}
	tests := []tc{
		{"the default verb", "%v"},
		{"the string verb", "%s"},
		{"the plus verb", "%+v"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := fmt.Sprintf(c.verb, id)
		if got != "<redacted>" {
			t.Fatalf("%s rendered %q, want <redacted>", c.verb, got)
		}
		//: belt and braces — no PEM armour may appear whatever the rendering.
		if strings.Contains(got, "PRIVATE KEY") || strings.Contains(got, "CERTIFICATE") {
			t.Fatalf("%s leaked TLS material: %q", c.verb, got)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
	//: the method itself, not only what fmt does with it.
	if got := id.String(); got != "<redacted>" {
		t.Errorf("String() = %q, want <redacted>", got)
	}
}

// Test_IdentityValue_GoString pins the verb that actually leaks in practice:
// fmt bypasses String for Go-syntax formatting and would otherwise dump the
// unexported fields, key material included.
func Test_IdentityValue_GoString(t *testing.T) {
	t.Parallel()
	material := newSelfSigned(t)

	type tc struct {
		name   string
		params corenet.IdentityParams
	}
	tests := []tc{
		{"an identity holding a keypair", corenet.IdentityParams{
			CertPEM: material.CertPEM, KeyPEM: material.KeyPEM,
		}},
		{"an identity holding a trust bundle", corenet.IdentityParams{
			RootsPEM: material.CertPEM,
		}},
		{"the zero identity", corenet.IdentityParams{}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		id, err := corenet.NewIdentityValue(c.params)
		if err != nil {
			t.Fatalf("NewIdentityValue = %v, want nil", err)
		}
		got := fmt.Sprintf("%#v", id)
		if got != "<redacted>" {
			t.Fatalf("%%#v rendered %q, want <redacted>", got)
		}
		if strings.Contains(got, "PRIVATE KEY") || strings.Contains(got, "CERTIFICATE") {
			t.Fatalf("%%#v leaked TLS material: %q", got)
		}
		if goStr := id.GoString(); goStr != "<redacted>" {
			t.Errorf("GoString() = %q, want <redacted>", goStr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_IdentityValue_IsZero pins the discriminator a caller uses to decide
// whether TLS was configured at all.
func Test_IdentityValue_IsZero(t *testing.T) {
	t.Parallel()
	material := newSelfSigned(t)

	type tc struct {
		name     string
		build    func(t *testing.T) corenet.IdentityValue
		wantZero bool
	}
	tests := []tc{
		{
			name:     "the declared zero value",
			build:    func(*testing.T) corenet.IdentityValue { return corenet.IdentityValue{} },
			wantZero: true,
		},
		{
			//: a constructed identity always carries a resolved floor, so even
			//: an empty params set is no longer the zero value.
			name: "an identity built from empty params",
			build: func(t *testing.T) corenet.IdentityValue {
				t.Helper()
				id, err := corenet.NewIdentityValue(corenet.IdentityParams{})
				if err != nil {
					t.Fatalf("NewIdentityValue = %v, want nil", err)
				}
				return id
			},
			wantZero: false,
		},
		{
			name: "an identity holding a keypair",
			build: func(t *testing.T) corenet.IdentityValue {
				t.Helper()
				id, err := corenet.NewIdentityValue(corenet.IdentityParams{
					CertPEM: material.CertPEM, KeyPEM: material.KeyPEM,
				})
				if err != nil {
					t.Fatalf("NewIdentityValue = %v, want nil", err)
				}
				return id
			},
			wantZero: false,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := c.build(t).IsZero(); got != c.wantZero {
			t.Errorf("IsZero() = %v, want %v", got, c.wantZero)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_IdentityValue_ClientConfig pins the isolation the method promises.
//
// Minting a fresh *tls.Config is not enough on its own: a new outer struct whose
// Certificates slice and RootCAs pool still point at the identity's own leaves
// cfg.Certificates[0] = other and cfg.RootCAs.AddCert(evil) reaching every other
// configuration that identity has minted, including ones already serving
// traffic. Trust material is exactly the thing an opaque value must not let a
// caller reach.
func Test_IdentityValue_ClientConfig(t *testing.T) {
	t.Parallel()
	material := newSelfSigned(t)

	type tc struct {
		name    string
		params  corenet.IdentityParams
		wantMin uint16
	}
	tests := []tc{
		{
			name: "a fully configured identity",
			params: corenet.IdentityParams{
				CertPEM: material.CertPEM, KeyPEM: material.KeyPEM,
				RootsPEM: material.CertPEM, ClientCAPEM: material.CertPEM,
				NextProtos: []string{"h2", "http/1.1"}, ServerName: "kitsune",
			},
			wantMin: tls.VersionTLS13,
		},
		{
			//: unset means the domain default, not the stdlib's older floor.
			name:    "an identity with no explicit floor",
			params:  corenet.IdentityParams{NextProtos: []string{"h2"}, ServerName: "kitsune"},
			wantMin: tls.VersionTLS13,
		},
		{
			name: "an identity pinned to TLS 1.2",
			params: corenet.IdentityParams{
				MinVersion: tls.VersionTLS12, NextProtos: []string{"h2"}, ServerName: "kitsune",
			},
			wantMin: tls.VersionTLS12,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		id, err := corenet.NewIdentityValue(c.params)
		if err != nil {
			t.Fatalf("NewIdentityValue = %v, want nil", err)
		}

		first := id.ClientConfig()
		second := id.ClientConfig()
		//: the honoured floor must reach the config the handshake uses.
		if first.MinVersion != c.wantMin {
			t.Errorf("MinVersion = %x, want %x", first.MinVersion, c.wantMin)
		}
		if first.ServerName != c.params.ServerName {
			t.Errorf("ServerName = %q, want %q", first.ServerName, c.params.ServerName)
		}

		//: mutate the first configuration as a careless caller would.
		first.ServerName = "hijacked"
		first.InsecureSkipVerify = true
		for i := range first.Certificates {
			first.Certificates[i].Leaf = nil
			first.Certificates[i].Certificate = nil
		}
		for i := range first.NextProtos {
			first.NextProtos[i] = "spdy/3"
		}

		//: the second must be untouched by any of it.
		if second.ServerName != c.params.ServerName {
			t.Errorf("ServerName leaked across calls: %q", second.ServerName)
		}
		if second.InsecureSkipVerify {
			t.Error("InsecureSkipVerify leaked across calls")
		}
		for i := range second.Certificates {
			if second.Certificates[i].Certificate == nil {
				t.Error("clearing one config's certificate chain emptied another's — " +
					"the two share a backing array")
			}
		}
		for i, want := range c.params.NextProtos {
			if second.NextProtos[i] != want {
				t.Errorf("ALPN[%d] = %q after the first config was rewritten, want %q",
					i, second.NextProtos[i], want)
			}
		}
		//: and the trust pools must be independent objects, or AddCert on
		//: either would change what the other verifies.
		if first.RootCAs != nil && first.RootCAs == second.RootCAs {
			t.Error("two configs share one *x509.CertPool")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_IdentityValue_ServerConfig pins the accept side: mutual TLS is switched
// on only when it was asked for AND can be enforced, and the client-CA pool is
// as private per call as the root pool is.
func Test_IdentityValue_ServerConfig(t *testing.T) {
	t.Parallel()
	material := newSelfSigned(t)

	type tc struct {
		name       string
		params     corenet.IdentityParams
		wantAuth   tls.ClientAuthType
		wantMin    uint16
		wantCAPool bool
	}
	tests := []tc{
		{
			name: "mutual TLS",
			params: corenet.IdentityParams{
				CertPEM: material.CertPEM, KeyPEM: material.KeyPEM,
				ClientCAPEM: material.CertPEM, RequireClientCert: true,
			},
			wantAuth:   tls.RequireAndVerifyClientCert,
			wantMin:    tls.VersionTLS13,
			wantCAPool: true,
		},
		{
			//: a client CA bundle without RequireClientCert means "verify it if
			//: offered", never "demand it".
			name: "a client CA bundle without the requirement",
			params: corenet.IdentityParams{
				CertPEM: material.CertPEM, KeyPEM: material.KeyPEM, ClientCAPEM: material.CertPEM,
			},
			wantAuth:   tls.NoClientCert,
			wantMin:    tls.VersionTLS13,
			wantCAPool: true,
		},
		{
			name:     "server authentication only",
			params:   corenet.IdentityParams{CertPEM: material.CertPEM, KeyPEM: material.KeyPEM},
			wantAuth: tls.NoClientCert,
			wantMin:  tls.VersionTLS13,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		id, err := corenet.NewIdentityValue(c.params)
		if err != nil {
			t.Fatalf("NewIdentityValue = %v, want nil", err)
		}

		first := id.ServerConfig()
		second := id.ServerConfig()
		if first.ClientAuth != c.wantAuth {
			t.Errorf("ClientAuth = %v, want %v", first.ClientAuth, c.wantAuth)
		}
		if first.MinVersion != c.wantMin {
			t.Errorf("MinVersion = %x, want %x", first.MinVersion, c.wantMin)
		}
		if (first.ClientCAs != nil) != c.wantCAPool {
			t.Errorf("ClientCAs installed = %v, want %v", first.ClientCAs != nil, c.wantCAPool)
		}
		//: two server configs must not share one client-CA pool.
		if first.ClientCAs != nil && first.ClientCAs == second.ClientCAs {
			t.Error("two server configs share one client-CA pool")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestZeroIdentityStillPinsAModernFloor pins that the zero value is safe: a
// caller who never configured TLS must not silently inherit the stdlib default,
// which is older than anything this domain will serve.
func TestZeroIdentityStillPinsAModernFloor(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		cfg  func(corenet.IdentityValue) *tls.Config
	}
	tests := []tc{
		{"the dial side", corenet.IdentityValue.ClientConfig},
		{"the accept side", corenet.IdentityValue.ServerConfig},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var id corenet.IdentityValue
		if !id.IsZero() {
			t.Fatal("the zero value must report IsZero")
		}
		if got := c.cfg(id).MinVersion; got != tls.VersionTLS13 {
			t.Errorf("zero-value MinVersion on %s = %x, want TLS 1.3", c.name, got)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestIdentityDoesNotRetainCallerSlices pins that construction copies what the
// caller handed in. Retaining IdentityParams.NextProtos let whoever built the
// params keep mutating the identity afterwards, through a value whose whole
// contract is that it is opaque and immutable once built.
func TestIdentityDoesNotRetainCallerSlices(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		protos []string
		mutate func(protos []string)
	}
	tests := []tc{
		{"overwriting the first entry", []string{"h2", "http/1.1"}, func(p []string) { p[0] = "spdy/3" }},
		{"overwriting the last entry", []string{"h2", "http/1.1"}, func(p []string) { p[1] = "spdy/3" }},
		{"clearing every entry", []string{"h2", "http/1.1"}, func(p []string) { clear(p) }},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: keep a private copy of what the caller declared, since the mutation
		//: below is deliberately destructive.
		want := slices.Clone(c.protos)

		id, err := corenet.NewIdentityValue(corenet.IdentityParams{NextProtos: c.protos})
		if err != nil {
			t.Fatalf("NewIdentityValue = %v, want nil", err)
		}
		//: mutate the slice the caller still holds.
		c.mutate(c.protos)

		got := id.ClientConfig().NextProtos
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("the identity's ALPN[%d] became %q when the caller's slice changed, "+
					"want %q — construction retained the caller's array", i, got[i], want[i])
			}
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
