// Package crypto — white-box wiring check for the Deriver registry.
package crypto

import (
	"slices"
	"strings"
	"testing"
)

// : the stub must satisfy Deriver at compile time, exactly as a real scheme does.
var _ Deriver = (*stubDeriver)(nil)

// stubDeriver is a comparable in-package Deriver for wiring checks.
type stubDeriver struct {
	id  Algorithm
	tag int
}

// Algorithm keys the stub in the registry.
func (s stubDeriver) Algorithm() Algorithm { return s.id }

// Derive returns a zero subkey of the requested length; the bytes are not what
// this file pins.
func (stubDeriver) Derive(_, _ []byte, _ string, length int) ([]byte, error) {
	return make([]byte, length), nil
}

// Test_derivers pins that the package-level registry is the one the public
// accessors read, and that it names its own registrar. The verb surfaces only
// in a boot-time panic — the one moment nobody can afford a message pointing at
// the wrong registrar, which is exactly what a copy-pasted registry file gives.
func Test_derivers(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		alg  Algorithm
	}
	tests := []tc{
		{"a fresh algorithm", "stub-kdf-wiring-a"},
		{"another fresh algorithm", "stub-kdf-wiring-b"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: publish straight into the private registry, bypassing the public
		//: registrar, so what is under test is the wiring and nothing else.
		if err := derivers.publish(c.alg, stubDeriver{id: c.alg}); err != nil {
			t.Fatalf("publish(%q) = %v, want nil", c.alg, err)
		}
		if _, ok := LookupDeriver(c.alg); !ok {
			t.Errorf("LookupDeriver(%q) missed what derivers.publish stored", c.alg)
		}
		if !slices.Contains(AvailableDerivers(), c.alg) {
			t.Errorf("AvailableDerivers() omits %q", c.alg)
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
	const clash Algorithm = "stub-kdf-wiring-clash"
	if err := derivers.publish(clash, stubDeriver{id: clash, tag: 1}); err != nil {
		t.Fatalf("publish(%q) = %v, want nil", clash, err)
	}
	err := derivers.publish(clash, stubDeriver{id: clash, tag: 2})
	if err == nil {
		t.Fatalf("a distinct value under %q published without conflict", clash)
	}
	if !strings.Contains(err.Error(), "RegisterDeriver") {
		t.Errorf("the conflict on %q reads %q, want it to name RegisterDeriver", clash, err.Error())
	}
}
