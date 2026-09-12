// External test fixture: the Identity double the integration suite verifies
// against when a case is not about ssh key material.
package entitlement_test

import coreent "github.com/kitsunium/sdk/internal/core/entitlement"

// stubIdentity is an Identity whose three answers the case sets directly.
//
// Even here, where a real SSHIdentity is available, most cases are about the
// roster, the cache or the CI seat — and driving those through real key files
// couples them to a format they are not testing.
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
