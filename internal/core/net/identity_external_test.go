package net_test

import (
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// trustCase describes one CA-bundle acceptance scenario.
type trustCase struct {
	name       string
	bundle     func(p pemPair) []byte
	wantErr    bool
	wantVerify bool // a non-nil pool must be installed on the client config
}

// TestNewIdentityRejectsUnusableTrustBundle pins the trap this domain exists to
// close: x509.CertPool.AppendCertsFromPEM reports failure through a boolean that
// callers routinely discard, so an empty, truncated, or key-only bundle yields a
// pool that verifies NOTHING while every call appears to succeed. Each unusable
// bundle below must produce TLS_MATERIAL_INVALID, never a silently empty pool.
func TestNewIdentityRejectsUnusableTrustBundle(t *testing.T) {
	t.Parallel()
	cases := []trustCase{
		{
			name:       "absent bundle falls back to the platform trust store",
			bundle:     func(pemPair) []byte { return nil },
			wantErr:    false,
			wantVerify: false,
		},
		{
			name:    "empty but non-nil bundle is indistinguishable from absent",
			bundle:  func(pemPair) []byte { return []byte{} },
			wantErr: false,
		},
		{
			name:    "garbage bytes are not PEM at all",
			bundle:  func(pemPair) []byte { return []byte("not a pem bundle at all") },
			wantErr: true,
		},
		{
			name:    "well-formed PEM carrying no certificate",
			bundle:  func(p pemPair) []byte { return p.KeyPEM },
			wantErr: true,
		},
		{
			name: "truncated certificate block",
			bundle: func(p pemPair) []byte {
				return p.CertPEM[:len(p.CertPEM)/2]
			},
			wantErr: true,
		},
		{
			name:       "a real certificate is accepted",
			bundle:     func(p pemPair) []byte { return p.CertPEM },
			wantErr:    false,
			wantVerify: true,
		},
	}
	material := newSelfSigned(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runTrustCase(t, tc, material)
		})
	}
}

// runTrustCase executes one trustCase against NewIdentity.
func runTrustCase(t *testing.T, tc trustCase, material pemPair) {
	t.Helper()
	id, err := corenet.NewIdentityValue(corenet.IdentityParams{RootsPEM: tc.bundle(material)})
	if tc.wantErr {
		//: an unusable bundle must be refused with the domain's typed sentinel.
		if err == nil {
			t.Fatal("expected TLS_MATERIAL_INVALID, got a usable identity — an empty trust store would verify nothing")
		}
		if !errs.HasCode(err, corenet.CodeTLSMaterialInvalid) {
			t.Fatalf("expected code %v, got %v", corenet.CodeTLSMaterialInvalid, err)
		}
		return
	}
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	//: a nil RootCAs means "platform trust store"; a non-nil one means our bundle.
	if got := id.ClientConfig().RootCAs != nil; got != tc.wantVerify {
		t.Fatalf("RootCAs installed = %v, want %v", got, tc.wantVerify)
	}
}

// TestConfigsDoNotShareMutableState pins the isolation ClientConfig and
// ServerConfig promise.
//
// Minting a fresh *tls.Config is not enough on its own: a new outer struct
// whose Certificates slice and RootCAs pool still point at the identity's own
// leaves cfg.Certificates[0] = other and cfg.RootCAs.AddCert(evil) reaching
// every other configuration that identity has minted, including ones already
// serving traffic. Trust material is exactly the thing an opaque value must not
// let a caller reach.
func TestConfigsDoNotShareMutableState(t *testing.T) {
	t.Parallel()
	material := newSelfSigned(t)
	id, err := corenet.NewIdentityValue(corenet.IdentityParams{
		CertPEM: material.CertPEM, KeyPEM: material.KeyPEM,
		RootsPEM: material.CertPEM, ClientCAPEM: material.CertPEM,
		NextProtos: []string{"h2"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	first := id.ClientConfig()
	second := id.ClientConfig()
	//: mutate the first configuration as a careless caller would.
	first.Certificates[0].Leaf = nil
	first.Certificates[0].Certificate = nil
	first.NextProtos[0] = "http/1.1"

	//: the second must be untouched by any of it.
	if second.Certificates[0].Certificate == nil {
		t.Fatal("clearing one config's certificate chain emptied another's — " +
			"the two share a backing array")
	}
	if got := second.NextProtos[0]; got != "h2" {
		t.Fatalf("second config's ALPN = %q after the first was rewritten, want \"h2\"", got)
	}
	//: and the trust pools must be independent objects.
	if first.RootCAs == second.RootCAs {
		t.Fatal("two configs share one *x509.CertPool — AddCert on either " +
			"changes what the other verifies")
	}
	serverSide := id.ServerConfig()
	if serverSide.ClientCAs == id.ServerConfig().ClientCAs {
		t.Fatal("two server configs share one client-CA pool")
	}
}

// TestIdentityDoesNotRetainCallerSlices pins that construction copies what the
// caller handed in.
//
// Retaining IdentityParams.NextProtos let whoever built the params keep
// mutating the identity afterwards, through a value whose whole contract is
// that it is opaque and immutable once built.
func TestIdentityDoesNotRetainCallerSlices(t *testing.T) {
	t.Parallel()
	protos := []string{"h2", "http/1.1"}
	id, err := corenet.NewIdentityValue(corenet.IdentityParams{NextProtos: protos})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	//: mutate the slice the caller still holds.
	protos[0] = "spdy/3"
	if got := id.ClientConfig().NextProtos[0]; got != "h2" {
		t.Fatalf("the identity's ALPN became %q when the caller's slice changed, "+
			"want \"h2\" — construction retained the caller's array", got)
	}
}
