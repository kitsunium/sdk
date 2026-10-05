package ssh

import (
	"golang.org/x/crypto/ssh"

	"github.com/kitsunium/sdk/internal/kernel/errs"

	"github.com/kitsunium/sdk/framework/entitlement"
)

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
// ProvePossessionFor is the stronger one and is what the framework's engine
// reaches for; this method stays because the port is frozen at three methods
// and a caller holding a entitlement.Identity must keep working.
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
// This is entitlement.BoundProver, and the binding is the whole of what it adds. The
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
		return refuse(entitlement.ErrKeyMismatch,
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
