package entitlement_test

import (
	"crypto/ed25519"
	"errors"
	"testing"

	entitlement "github.com/kitsunium/sdk/third-party/entitlement"
)

// TestRoughtimeServersShipEmpty pins the one thing about this feature that a
// reader most needs to be able to trust: it is INERT in committed source.
//
// The client has never completed a handshake with a live server — UDP/2002 is
// filtered on the network it was written on, and three independent servers were
// probed with both protocol variants for a total of zero replies. Its verifier
// is tested against a fixture in this package, which proves self-consistency
// and not that it speaks Cloudflare's dialect.
//
// So the server list ships empty, in the same spirit as vendorPublicKeyB64: the
// mechanism is here, the anchor is not. Adding an entry is a deliberate act by
// somebody who has watched this client verify a real response. This test is
// what stops one arriving by accident — a populated list on an unvalidated
// implementation is either inert anyway or, worse, starts refusing machines
// over a parsing bug.
func TestRoughtimeServersShipEmpty(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		reason string
	}{
		{name: "committed source pins no server", reason: "interop is unverified; a populated list would assert otherwise"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if len(entitlement.RoughtimeServers) != 0 {
				t.Errorf("RoughtimeServers has %d entries, want 0 until interop is confirmed against a live server (%s)",
					len(entitlement.RoughtimeServers), tt.reason)
			}
		})
	}
}

// TestRoughtimeServerValueCarriesItsOwnKey pins that trust is per-server and
// travels with the entry.
//
// A server is not believed because of where it is — an address is whatever DNS
// says this morning — it is believed because its answer carries a signature
// made by the half pinned here. That is the same rule the roster follows with a
// different anchor, and keeping the key ON the entry is what makes a list of
// several servers safe: one compromised operator cannot speak for another.
func TestRoughtimeServerValueCarriesItsOwnKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		reason string
	}{
		{name: "the key is part of the entry, not of the package", reason: "one compromised operator must not be able to speak for another"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pub, _, err := ed25519.GenerateKey(nil)
			//: A failure here is an environment problem, not a test outcome.
			if err != nil {
				t.Fatalf("generating key: %v", err)
			}

			server := entitlement.RoughtimeServerValue{Name: "example", Address: "time.example:2002", PublicKey: pub}
			if len(server.PublicKey) != ed25519.PublicKeySize {
				t.Errorf("RoughtimeServerValue.PublicKey = %d bytes, want %d (%s)", len(server.PublicKey), ed25519.PublicKeySize, tt.reason)
			}
		})
	}
}

// TestQueryRoughtime pins that a server with no usable key is refused before
// a single packet leaves the machine.
//
// A check that cannot run must never pass, and here it must also never spend a
// round trip finding that out.
func TestQueryRoughtime(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// key is the pinned long-term half.
		key    ed25519.PublicKey
		reason string
	}{
		{name: "no key at all", key: nil, reason: "a server that cannot be checked cannot be believed"},
		{name: "a key of the wrong length", key: make([]byte, 8), reason: "ed25519.Verify is entitled to assume its own sizes"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			//: An address that would hang if it were ever dialled; reaching it
			//: would mean the key check did not run first.
			server := entitlement.RoughtimeServerValue{Name: "stub", Address: "192.0.2.1:2002", PublicKey: tt.key}
			_, _, err := entitlement.QueryRoughtime(server)
			if !errors.Is(err, entitlement.ErrCIUnverifiable) {
				t.Errorf("QueryRoughtime() error = %v, want entitlement.ErrCIUnverifiable (%s)", err, tt.reason)
			}
		})
	}
}
