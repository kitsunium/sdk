// Package entitlement — the ssh implementation of the core Identity port.
package entitlement

import (
	"fmt"
)

// SSHIdentity proves a machine's identity from the ssh key material already in
// a user's key directory.
//
// It lives under third-party/ because golang.org/x/crypto/ssh brings
// golang.org/x/term and through it golang.org/x/sys, which is banned SDK-wide
// (ADR 0078). A consumer that wants it opts into the root module and its
// dependency graph; one that already handles its own key material implements
// the three-method port instead and inherits nothing.
//
// Why ssh key material rather than a keypair this package would mint: the whole
// point is that possession is proven against something the user ALREADY has and
// already protects. A file this package invented would need a lifecycle — where
// it lives, who may read it, what happens on rotation — that the user's own key
// directory already has.
type SSHIdentity struct {
	// dir is the key directory, conventionally ~/.ssh.
	dir string
}

// NewSSHIdentity returns an Identity reading from dir. An empty dir resolves to
// the user's conventional key directory.
func NewSSHIdentity(dir string) *SSHIdentity {
	//: an unset directory means "wherever this user's keys normally live".
	if dir == "" {
		dir = DefaultSSHDir()
	}

	//: the identity the verifier will question.
	return &SSHIdentity{dir: dir}
}

// Discover returns the subject this machine is enrolled as, refusing rather
// than choosing when several identities are present.
func (s *SSHIdentity) Discover() (subject string, err error) {
	//: delegate to the directory scan, which owns the naming convention.
	return DiscoverSubject(s.dir)
}

// Fingerprint returns the published fingerprint of the subject's public half,
// in the spelling the roster uses.
func (s *SSHIdentity) Fingerprint(subject string) (fingerprint string, err error) {
	pub, loadErr := LoadPublicKey(s.dir, subject)
	//: A subject with no readable published half has no fingerprint.
	if loadErr != nil {
		//: Propagate the absent-licence case.
		return "", loadErr
	}

	//: The roster compares this by byte equality and never parses it.
	return Fingerprint(pub), nil
}

// ProvePossession answers the challenge with the subject's private half.
//
// A nil error IS the proof. There is deliberately nothing to inspect: anything
// returned here would be a second thing a caller had to verify.
func (s *SSHIdentity) ProvePossession(subject string) error {
	pub, loadErr := LoadPublicKey(s.dir, subject)
	//: Without the published half there is nothing to prove possession OF.
	if loadErr != nil {
		//: Propagate the absent-licence case.
		return loadErr
	}
	//: cmp.Or is not usable here: it evaluates both arguments, so a failed
	//: load would still reach ProvePossession with a nil signer.
	signer, signerErr := SignerFromFile(s.dir, subject)
	//: A key we cannot load cannot answer a challenge.
	if signerErr != nil {
		//: Name the stage so an operator tells a missing key from a
		//: mismatched one — the wrap the source implementation carried here.
		return fmt.Errorf("loading private half for %s: %w", subject, signerErr)
	}

	//: The last gate, and the one that makes publication safe.
	return ProvePossession(signer, pub)
}
