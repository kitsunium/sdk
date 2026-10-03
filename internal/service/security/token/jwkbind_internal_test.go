// Package token — the shape of a refused binding, pinned.
package token

import "testing"

// TestABindRefusalIsNotANilInterface pins the property boundKeyValue.usable
// exists for, and it is a property of Go's typed nil rather than of this
// package's logic.
//
// bindECJWK ends in `return bindP256Public(public)`. bindP256Public's first
// result is the VALUE type es256Verifying, and bindECJWK's is the interface
// verifyingKey, so Go converts — including on the failure path, where the value
// is a zero es256Verifying. The result is a NON-nil verifyingKey whose verify
// would dereference a nil *ecdsa.PublicKey. bindOctJWK and bindOKPJWK have the
// same shape through hs256Binding and ed25519Verifying.
//
// So `binding != nil` is not a correct test for "bindJWK accepted this key",
// in three families of three. Today all three inner refusals are unreachable —
// jwk validates length, curve and point first — so this is a latent trap and
// not a live defect, and saying so is the point: nothing in the tree can
// currently produce the panic, and this test does not claim otherwise. What it
// pins is the SHAPE, so that if a future edit makes the refusal return a nil
// interface, whoever simplifies boundKeyValue away is told by a test rather
// than by a reviewer's memory.
//
// MUTATION (2026-09-11): the local helper's result type was changed from
// verifyingKey to es256Verifying and the comparison to `binding.key == nil`.
// Observed: the file no longer compiles — `invalid operation: binding == nil
// (mismatched types es256Verifying and untyped nil)` — which is the compiler
// making the same point the test does, and is why the assertion is written
// through an interface-returning helper rather than on the concrete result.
func TestABindRefusalIsNotANilInterface(t *testing.T) {
	t.Parallel()
	//: mirrors bindECJWK's own `return bindP256Public(public)` exactly — the
	//: conversion happens at the return, not at an assignment a reader can
	//: dismiss as an artefact of the test.
	refuse := func() (verifyingKey, error) { return bindP256Public(nil) }
	binding, err := refuse()
	//: a nil public key must be refused; if it were accepted the rest of this
	//: test would be measuring the wrong thing.
	if err == nil {
		t.Fatalf("bindP256Public(nil) returned no error, want KeyUnsuitable")
	}
	//: the trap itself. A nil result here means the refusal now yields a nil
	//: interface, which would make boundKeyValue.usable redundant — a change
	//: worth noticing rather than inheriting.
	if binding == nil {
		t.Fatalf("a refused binding is now a nil verifyingKey; " +
			"boundKeyValue.usable may be simplifiable, and its doc comment is stale")
	}
}
