// Internal test fixture: the Identity double the suite verifies against.
package entitlement

import coreent "github.com/kitsunium/sdk/internal/core/entitlement"

// stubIdentity is an Identity whose three answers the case sets directly.
//
// The suite used to hand the Service an ssh directory and let it read real key
// files. That coupled every roster, cache and CI test to the ssh key FORMAT —
// which is exactly the coupling the port removed. A case that is about roster
// freshness now says so by setting one field.
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

// _ asserts at compile time that the double still satisfies the port, so a
// method added to Identity fails here rather than in twenty-three test files.
var _ coreent.Identity = stubIdentity{}
