// Package entitlement — the ssh implementation of the core Identity port.
package entitlement

import (
	"golang.org/x/crypto/ssh"

	"github.com/kitsunium/sdk/internal/kernel/errs"

	coreent "github.com/kitsunium/sdk/internal/core/entitlement"
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
//
// It proves possession of whatever is in the key directory when it is called,
// which is the only claim the three-method port can carry.
// ProvePossessionFor is the stronger one and is what the SDK's own engine
// reaches for; this method stays because the port is frozen at three methods
// and a caller holding a coreent.Identity must keep working.
func (s *SSHIdentity) ProvePossession(subject string) error {
	pub, loadErr := LoadPublicKey(s.dir, subject)
	//: The positive branch first, because cmp.Or is not usable on this pair:
	//: it evaluates both arguments, so a failed load would still reach answer
	//: with a nil public half. Same objection the signer load carried before
	//: it moved into answer.
	if loadErr == nil {
		//: Sign with the private half beside it.
		return s.answer(subject, pub)
	}
	//: Without the published half there is nothing to prove possession OF;
	//: propagate the absent-licence case.
	return loadErr
}

// ProvePossessionFor answers the challenge with the subject's private half, and
// refuses unless the published half is the one the roster AUTHORISED.
//
// This is coreent.BoundProver, and the binding is the whole of what it adds. The
// engine's own comparison happens between its Fingerprint call and its proof
// call, so material replaced in that window was signed for and nothing noticed:
// a rotation mid-verification, a volume remounted, a `<uuid>.pub` swapped by
// anything that can write the key directory. Re-reading the directory HERE and
// comparing against the value the roster published closes it, because the
// comparison and the signature now happen against one read.
//
// It is not a second fingerprint POLICY. Fingerprint's contract is that the
// roster's spelling is compared by byte equality and never parsed, and this
// compares the identical rendering to the identical value. A refusal is
// ErrKeyMismatch — "a local key that does not match the published fingerprint",
// which is exactly what happened — so the taxonomy does not grow by one entry.
func (s *SSHIdentity) ProvePossessionFor(subject, authorised string) error {
	pub, loadErr := LoadPublicKey(s.dir, subject)
	//: Without the published half there is nothing to prove possession OF, and
	//: absence must not be reported as the wrong key: one sends an operator to
	//: enrolment, the other to a rotation that will not help.
	if loadErr != nil {
		//: Propagate the absent-licence case.
		return loadErr
	}
	//: The bind. Byte equality against the roster's own spelling, on the read
	//: the signature below is taken over — not on an earlier one a caller made.
	//: The published value is NOT named in the refusal: it is a particular, and
	//: quoting it back confirms it to whoever presented it.
	present := Fingerprint(pub)
	//: Not the key the roster approved, whatever else is true of it.
	if present != authorised {
		//: Refuse an identity the roster did not authorise.
		return refuse(coreent.ErrKeyMismatch,
			errs.String("stage", "bind_authorised_fingerprint"),
			errs.String("subject", subject),
			errs.String("condition", "the published half in the key directory is not the one the roster authorised"))
	}
	//: Authorised, so sign with the private half beside it.
	return s.answer(subject, pub)
}

// answer signs the possession challenge for pub with the subject's private half.
//
// Shared by both proof methods rather than copied into each, because the
// challenge is the part that must not differ between them: the bound method adds
// a comparison BEFORE the proof and changes nothing about the proof itself.
func (s *SSHIdentity) answer(subject string, pub ssh.PublicKey) error {
	//: cmp.Or is not usable here: it evaluates both arguments, so a failed
	//: load would still reach ProvePossession with a nil signer.
	signer, signerErr := SignerFromFile(s.dir, subject)
	//: A key we cannot load cannot answer a challenge.
	if signerErr != nil {
		//: Name the stage so an operator tells a missing key from a
		//: mismatched one — the wrap the source implementation carried here.
		return annotate(signerErr,
			errs.String("stage", "load_private_half"),
			errs.String("subject", subject))
	}

	//: The last gate, and the one that makes publication safe.
	return ProvePossession(signer, pub)
}
