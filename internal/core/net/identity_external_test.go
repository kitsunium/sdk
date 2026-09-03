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
	id, err := corenet.NewIdentity(corenet.IdentityParams{RootsPEM: tc.bundle(material)})
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
