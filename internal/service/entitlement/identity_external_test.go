// External test fixture: the Identity double the black-box suite verifies
// against.
package entitlement_test

import coreent "github.com/kitsunium/sdk/internal/core/entitlement"

// stubIdentity is an Identity whose three answers the case sets directly.
//
// Its internal twin carries the argument for why it exists; this is the copy
// the black-box suite needs, since a test in entitlement_test cannot reach an
// unexported type.
type stubIdentity struct {
	subject     string
	discoverErr error
	fingerprint string
	printErr    error
	proveErr    error
}

// Discover returns the configured subject, or the configured refusal.
func (s stubIdentity) Discover() (string, error) {
	//: the case decides who this machine claims to be.
	return s.subject, s.discoverErr
}

// Fingerprint returns the configured fingerprint, or the configured refusal.
func (s stubIdentity) Fingerprint(string) (string, error) {
	//: the case decides what this machine presents.
	return s.fingerprint, s.printErr
}

// ProvePossession returns the configured outcome.
func (s stubIdentity) ProvePossession(string) error {
	//: the case decides whether the proof holds.
	return s.proveErr
}

// _ asserts at compile time that the double still satisfies the port.
var _ coreent.Identity = stubIdentity{}
