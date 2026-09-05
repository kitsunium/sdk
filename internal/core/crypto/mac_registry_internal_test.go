// Package crypto — white-box wiring check for the MAC registry.
package crypto

import (
	"crypto/hmac"
	"crypto/sha256"
	"hash"
	"slices"
	"strings"
	"testing"
)

// : the stub must satisfy MAC at compile time, exactly as a real scheme does.
var _ MAC = (*stubMAC)(nil)

// stubMAC is a comparable in-package MAC for wiring checks.
type stubMAC struct {
	id  Algorithm
	tag int
}

// Algorithm keys the stub in the registry.
func (s stubMAC) Algorithm() Algorithm { return s.id }

// Tag produces a real HMAC so a caller that resolves the stub still works.
func (stubMAC) Tag(key Key, message []byte) []byte {
	m := hmac.New(sha256.New, key.Bytes())
	m.Write(message)
	return m.Sum(nil)
}

// Verify compares in constant time, as the port requires.
func (s stubMAC) Verify(key Key, message, tag []byte) bool {
	return hmac.Equal(s.Tag(key, message), tag)
}

// New returns a fresh keyed stream.
func (stubMAC) New(key Key) hash.Hash { return hmac.New(sha256.New, key.Bytes()) }

// Test_macs pins that the package-level registry is the one the public
// accessors read, and that it names its own registrar. The verb surfaces only
// in a boot-time panic — the one moment nobody can afford a message pointing at
// the wrong registrar, which is exactly what a copy-pasted registry file gives.
func Test_macs(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		alg  Algorithm
	}
	tests := []tc{
		{"a fresh algorithm", "stub-mac-wiring-a"},
		{"another fresh algorithm", "stub-mac-wiring-b"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: publish straight into the private registry, bypassing the public
		//: registrar, so what is under test is the wiring and nothing else.
		if err := macs.publish(c.alg, stubMAC{id: c.alg}); err != nil {
			t.Fatalf("publish(%q) = %v, want nil", c.alg, err)
		}
		if _, ok := LookupMAC(c.alg); !ok {
			t.Errorf("LookupMAC(%q) missed what macs.publish stored", c.alg)
		}
		if !slices.Contains(AvailableMACs(), c.alg) {
			t.Errorf("AvailableMACs() omits %q", c.alg)
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
	const clash Algorithm = "stub-mac-wiring-clash"
	if err := macs.publish(clash, stubMAC{id: clash, tag: 1}); err != nil {
		t.Fatalf("publish(%q) = %v, want nil", clash, err)
	}
	err := macs.publish(clash, stubMAC{id: clash, tag: 2})
	if err == nil {
		t.Fatalf("a distinct value under %q published without conflict", clash)
	}
	if !strings.Contains(err.Error(), "RegisterMAC") {
		t.Errorf("the conflict on %q reads %q, want it to name RegisterMAC", clash, err.Error())
	}
}
