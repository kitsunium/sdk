// Package checks_test — the crypto conformance group.
package checks_test

import (
	"testing"

	"github.com/kitsunium/sdk/e2e/checks"
)

// TestCrypto pins the crypto group's registration.
//
// Every primitive the SDK exposes has to be exercised on the host: a cipher that
// compiles everywhere and produces a wrong tag on one architecture is exactly
// what a conformance run exists to find, and a primitive dropped from the group
// is one nobody proves works there.
func TestCrypto(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// wantDomain is the label every Result must carry.
		wantDomain string
		// minChecks is the smallest number of checks the group may register.
		minChecks int
	}
	tests := []tc{
		//: AEAD (round trip and wrong AAD), hash, sign, KDF, password, MAC, agree.
		{name: "the crypto group", wantDomain: "crypto", minChecks: 8},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		group := checks.Crypto()

		assertGroup(t, group, c.wantDomain)

		if len(group.Checks) < c.minChecks {
			t.Fatalf("the group registers %d checks, want at least %d — a primitive "+
				"dropped from the group is one nobody proves works on the host",
				len(group.Checks), c.minChecks)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
