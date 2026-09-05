// Package crypto — white-box wiring check for the Hasher registry.
package crypto

import (
	"crypto/sha256"
	"hash"
	"slices"
	"strings"
	"testing"
)

// : the stub must satisfy Hasher at compile time, exactly as a real scheme does.
var _ Hasher = (*stubHasher)(nil)

// stubHasher is a comparable in-package Hasher for wiring checks.
type stubHasher struct {
	id  Algorithm
	tag int
}

// Algorithm keys the stub in the registry.
func (s stubHasher) Algorithm() Algorithm { return s.id }

// New returns a real hash so a caller that resolves the stub still works.
func (stubHasher) New() hash.Hash { return sha256.New() }

// Test_hashers pins that the package-level registry is the one the public
// accessors read, and that it names its own registrar. The verb surfaces only
// in a boot-time panic — the one moment nobody can afford a message pointing at
// the wrong registrar, which is exactly what a copy-pasted registry file gives.
func Test_hashers(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		alg  Algorithm
	}
	tests := []tc{
		{"a fresh algorithm", "stub-hash-wiring-a"},
		{"another fresh algorithm", "stub-hash-wiring-b"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: publish straight into the private registry, bypassing the public
		//: registrar, so what is under test is the wiring and nothing else.
		if err := hashers.publish(c.alg, stubHasher{id: c.alg}); err != nil {
			t.Fatalf("publish(%q) = %v, want nil", c.alg, err)
		}
		//: the public accessors must read that very map.
		if _, ok := LookupHasher(c.alg); !ok {
			t.Errorf("LookupHasher(%q) missed what hashers.publish stored", c.alg)
		}
		if !slices.Contains(AvailableHashers(), c.alg) {
			t.Errorf("AvailableHashers() omits %q", c.alg)
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
	const clash Algorithm = "stub-hash-wiring-clash"
	if err := hashers.publish(clash, stubHasher{id: clash, tag: 1}); err != nil {
		t.Fatalf("publish(%q) = %v, want nil", clash, err)
	}
	err := hashers.publish(clash, stubHasher{id: clash, tag: 2})
	if err == nil {
		t.Fatalf("a distinct value under %q published without conflict", clash)
	}
	if !strings.Contains(err.Error(), "RegisterHasher") {
		t.Errorf("the conflict on %q reads %q, want it to name RegisterHasher", clash, err.Error())
	}
}
