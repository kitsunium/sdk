// Package net — the identity's private accessors. All three exist to keep a
// caller from reaching the identity's own state through a config it was handed,
// and all three have a nil/zero branch whose meaning is load-bearing.
package net

import (
	"crypto/tls"
	"testing"
)

// Test_IdentityValue_clonedRoots pins that nil stays nil and anything else is
// copied. nil is a meaningful value here — it tells crypto/tls to use the
// platform trust store — and is deliberately distinct from an empty pool, which
// trusts nothing at all.
func Test_IdentityValue_clonedRoots(t *testing.T) {
	t.Parallel()
	certPEM, _ := mintPEM(t)

	type tc struct {
		name    string
		bundle  []byte
		wantNil bool
	}
	tests := []tc{
		{name: "no bundle keeps the platform trust store", wantNil: true},
		{name: "a real bundle is copied", bundle: certPEM},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		id, err := NewIdentityValue(IdentityParams{RootsPEM: c.bundle})
		if err != nil {
			t.Fatalf("NewIdentityValue = %v, want nil", err)
		}

		first := id.clonedRoots()
		second := id.clonedRoots()
		if (first == nil) != c.wantNil {
			t.Fatalf("clonedRoots() nil = %v, want %v", first == nil, c.wantNil)
		}
		if c.wantNil {
			return
		}
		//: two calls must not hand out the same object, or AddCert on one
		//: config would change what every other one verifies.
		if first == second {
			t.Error("clonedRoots() returned the same pool twice")
		}
		if first == id.roots {
			t.Error("clonedRoots() handed out the identity's own pool")
		}
		//: the copy must still carry what the original did.
		if len(first.Subjects()) != len(id.roots.Subjects()) { //nolint:staticcheck // comparing counts, not using the deprecated data
			t.Error("the cloned pool lost a certificate")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_IdentityValue_clonedClientCAs pins the same contract on the accept side.
// Here nil means "this identity accepts no client certificates", which is why
// NewIdentityValue refuses RequireClientCert without a bundle.
func Test_IdentityValue_clonedClientCAs(t *testing.T) {
	t.Parallel()
	certPEM, keyPEM := mintPEM(t)

	type tc struct {
		name    string
		params  IdentityParams
		wantNil bool
	}
	tests := []tc{
		{name: "no client CA bundle", params: IdentityParams{}, wantNil: true},
		{
			name:   "a client CA bundle",
			params: IdentityParams{CertPEM: certPEM, KeyPEM: keyPEM, ClientCAPEM: certPEM},
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		id, err := NewIdentityValue(c.params)
		if err != nil {
			t.Fatalf("NewIdentityValue = %v, want nil", err)
		}

		first := id.clonedClientCAs()
		second := id.clonedClientCAs()
		if (first == nil) != c.wantNil {
			t.Fatalf("clonedClientCAs() nil = %v, want %v", first == nil, c.wantNil)
		}
		if c.wantNil {
			return
		}
		if first == second {
			t.Error("clonedClientCAs() returned the same pool twice")
		}
		if first == id.clientCAs {
			t.Error("clonedClientCAs() handed out the identity's own pool")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_IdentityValue_resolvedMinVersion pins that the accessor never returns
// zero. A zero MinVersion in a *tls.Config means "the stdlib default", which is
// older than anything this domain will serve — so the zero-value identity has
// to answer for itself rather than defer.
func Test_IdentityValue_resolvedMinVersion(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		id   IdentityValue
		want uint16
	}
	tests := []tc{
		{"the zero value", IdentityValue{}, defaultMinVersion},
		{"an explicit TLS 1.2 floor", IdentityValue{minVersion: tls.VersionTLS12}, tls.VersionTLS12},
		{"an explicit TLS 1.3 floor", IdentityValue{minVersion: tls.VersionTLS13}, tls.VersionTLS13},
		//: a floor set past anything crypto/tls knows is still honoured; the
		//: accessor resolves, it does not re-validate what construction checked.
		{"a floor above TLS 1.3", IdentityValue{minVersion: tls.VersionTLS13 + 1}, tls.VersionTLS13 + 1},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := c.id.resolvedMinVersion()
		if got != c.want {
			t.Errorf("resolvedMinVersion() = %x, want %x", got, c.want)
		}
		//: whatever the input, a zero would silently hand the handshake back to
		//: the stdlib default.
		if got == 0 {
			t.Error("resolvedMinVersion() returned zero")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
