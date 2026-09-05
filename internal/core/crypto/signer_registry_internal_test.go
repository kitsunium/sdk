// Package crypto — white-box wiring check for the Signer registry.
package crypto

import (
	"slices"
	"strings"
	"testing"
)

// : the stub must satisfy Signer at compile time, exactly as a real scheme does.
var _ Signer = (*stubSigner)(nil)

// stubSigner is a comparable in-package Signer for wiring checks.
type stubSigner struct {
	id  Algorithm
	tag int
}

// Algorithm keys the stub in the registry.
func (s stubSigner) Algorithm() Algorithm { return s.id }

// GenerateKey returns fixed material; the bytes are not what this file pins.
func (stubSigner) GenerateKey() (pub, priv []byte, err error) {
	return []byte("pub"), []byte("priv"), nil
}

// Sign returns a fixed signature.
func (stubSigner) Sign(_, _ []byte) ([]byte, error) { return []byte("sig"), nil }

// Verify accepts only the signature Sign produces.
func (stubSigner) Verify(_, _, sig []byte) bool { return string(sig) == "sig" }

// Test_signers pins that the package-level registry is the one the public
// accessors read, and that it names its own registrar. The verb surfaces only
// in a boot-time panic — the one moment nobody can afford a message pointing at
// the wrong registrar, which is exactly what a copy-pasted registry file gives.
func Test_signers(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		alg  Algorithm
	}
	tests := []tc{
		{"a fresh algorithm", "stub-sign-wiring-a"},
		{"another fresh algorithm", "stub-sign-wiring-b"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: publish straight into the private registry, bypassing the public
		//: registrar, so what is under test is the wiring and nothing else.
		if err := signers.publish(c.alg, stubSigner{id: c.alg}); err != nil {
			t.Fatalf("publish(%q) = %v, want nil", c.alg, err)
		}
		if _, ok := LookupSigner(c.alg); !ok {
			t.Errorf("LookupSigner(%q) missed what signers.publish stored", c.alg)
		}
		if !slices.Contains(AvailableSigners(), c.alg) {
			t.Errorf("AvailableSigners() omits %q", c.alg)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
	//: the conflict message is the only place the registrar verb ever
	//: surfaces, and it surfaces at boot — a copy-pasted registry file would
	//: send the reader to the wrong source file at the worst moment.
	const clash Algorithm = "stub-sign-wiring-clash"
	if err := signers.publish(clash, stubSigner{id: clash, tag: 1}); err != nil {
		t.Fatalf("publish(%q) = %v, want nil", clash, err)
	}
	err := signers.publish(clash, stubSigner{id: clash, tag: 2})
	if err == nil {
		t.Fatalf("a distinct value under %q published without conflict", clash)
	}
	if !strings.Contains(err.Error(), "RegisterSigner") {
		t.Errorf("the conflict on %q reads %q, want it to name RegisterSigner", clash, err.Error())
	}
}
