// Internal tests: the possession proof, and the fingerprint the roster
// authorised reaching the port that has to bind to it.
package entitlement

import (
	"crypto/ed25519"
	"errors"
	"testing"
	"time"

	coreent "github.com/kitsunium/sdk/internal/core/entitlement"
)

// boundFingerprint is what the roster publishes and the identity presents in
// every row below. A constant rather than a literal per row: no row here is
// about a mismatch the ENGINE catches, so none may differ by accident.
const boundFingerprint string = "SHA256:bound"

// recordingBoundIdentity is an Identity that also implements
// coreent.BoundProver, and records which of the two proof calls it received.
//
// Recording rather than merely answering, because the property under test is
// WHICH call the engine makes and WHAT it passes: a double that only returned an
// error would pass identically whether the engine bound the proof or not.
type recordingBoundIdentity struct {
	// subject is who this machine claims to be.
	subject string
	// fingerprint is what Fingerprint answers, matched by the engine against
	// the roster before any proof is asked for.
	fingerprint string
	// proveErr is what the BOUND call returns, so a row can refuse from there.
	proveErr error
	// unbound counts calls to the three-method proof, which must be zero
	// whenever the sibling is present.
	unbound int
	// boundWith records the authorised value every bound call received, in
	// order. A slice rather than a string so "called twice" is visible.
	boundWith []string
}

// Discover reports the subject this double claims to be.
func (r *recordingBoundIdentity) Discover() (string, error) {
	//: The case decides who this machine is.
	return r.subject, nil
}

// Fingerprint reports what this double presents.
func (r *recordingBoundIdentity) Fingerprint(string) (string, error) {
	//: The case decides what this machine presents.
	return r.fingerprint, nil
}

// ProvePossession records that the UNBOUND call was the one the engine made.
func (r *recordingBoundIdentity) ProvePossession(string) error {
	r.unbound++
	//: An identity that can bind must never be asked the weaker question, so
	//: this path answering at all is the defect a row detects.
	return nil
}

// ProvePossessionFor records the authorised value the engine handed over.
func (r *recordingBoundIdentity) ProvePossessionFor(_, authorised string) error {
	r.boundWith = append(r.boundWith, authorised)
	//: The case decides whether the bound proof holds.
	return r.proveErr
}

// _ asserts at compile time that the double really is both, so a row about the
// sibling cannot silently be a row about the fallback.
var (
	_ coreent.Identity    = (*recordingBoundIdentity)(nil)
	_ coreent.BoundProver = (*recordingBoundIdentity)(nil)
)

// recordingIdentity is an Identity that is NOT a BoundProver, and records that
// the unbound call is what it received.
//
// The fallback needs its own double: asserting on recordingBoundIdentity with
// the sibling "turned off" is not expressible, because satisfaction is a
// property of the TYPE and not of a field.
type recordingIdentity struct {
	// subject is who this machine claims to be.
	subject string
	// fingerprint is what Fingerprint answers.
	fingerprint string
	// unbound counts calls to the three-method proof.
	unbound int
}

// Discover reports the subject this double claims to be.
func (r *recordingIdentity) Discover() (string, error) {
	//: The case decides who this machine is.
	return r.subject, nil
}

// Fingerprint reports what this double presents.
func (r *recordingIdentity) Fingerprint(string) (string, error) {
	//: The case decides what this machine presents.
	return r.fingerprint, nil
}

// ProvePossession records the call and proves.
func (r *recordingIdentity) ProvePossession(string) error {
	r.unbound++
	//: The only proof a three-method identity can offer.
	return nil
}

// _ asserts the double satisfies the port and deliberately NOT the sibling.
var _ coreent.Identity = (*recordingIdentity)(nil)

// Test_Service_matchSubject_handsTheAuthorisedFingerprintToAPortThatCanBindIt
// is the audit's second finding, stated as what does and does not cross the
// port.
//
// # What was missing
//
// matchSubject called Fingerprint, compared the answer ITSELF against
// roster.SubjectFor(subject).Fingerprint, and then called ProvePossession. The
// authorised value never traversed the port, so an implementation could not bind
// its proof to it: material replaced between the two calls was signed for, and
// the strongest thing a consumer could do unaided was remember the key object
// across the two calls and refuse a proof with nothing remembered — detecting a
// change without ever learning which key it was supposed to hold. The
// ktn-linter consumer pushed that detection to its limit and documented the
// ceiling in its own pkg/license/CLAUDE.md.
//
// # What each row discriminates
//
// The first row fails if the engine keeps calling the unbound method, or calls
// the bound one with anything but the roster's published value — an engine that
// passed the subject, or "", would satisfy a weaker assertion. The second fails
// if the bound refusal is swallowed. The third fails if the fallback was dropped,
// which would break every three-method identity in existence.
func Test_Service_matchSubject_handsTheAuthorisedFingerprintToAPortThatCanBindIt(t *testing.T) {
	t.Parallel()

	base := time.Now().Truncate(time.Second)
	roster := &coreent.RosterValue{
		IssuedAt:  base.Add(-time.Hour),
		ExpiresAt: base.Add(time.Hour),
		Subjects:  map[string]coreent.SubjectValue{sampleSubject: {Fingerprint: boundFingerprint}},
	}

	t.Run("a port that can bind is handed the roster's own value", func(t *testing.T) {
		t.Parallel()

		identity := &recordingBoundIdentity{subject: sampleSubject, fingerprint: boundFingerprint}
		svc := &Service{identity: identity}

		if _, err := svc.matchSubject(roster, sampleSubject, base); err != nil {
			t.Fatalf("matchSubject() error = %v, want nil", err)
		}
		//: Exactly one bound call, carrying the fingerprint the ROSTER
		//: publishes. Anything else means the value did not cross the port.
		if len(identity.boundWith) != 1 || identity.boundWith[0] != boundFingerprint {
			t.Fatalf("ProvePossessionFor received %q, want exactly one call with %q — "+
				"the authorised fingerprint did not cross the port", identity.boundWith, boundFingerprint)
		}
		//: And the weaker question was never asked. An engine that asked both
		//: would let an implementation that refuses the bound proof be
		//: overridden by the one that cannot see the binding.
		if identity.unbound != 0 {
			t.Errorf("ProvePossession called %d times on an identity that can bind, want 0", identity.unbound)
		}
	})

	t.Run("a bound refusal refuses the verification", func(t *testing.T) {
		t.Parallel()

		identity := &recordingBoundIdentity{
			subject:     sampleSubject,
			fingerprint: boundFingerprint,
			proveErr:    coreent.ErrNoPossession,
		}
		svc := &Service{identity: identity}

		_, err := svc.matchSubject(roster, sampleSubject, base)
		//: The refusal travels verbatim: the engine adds no verdict of its own
		//: to a possession answer, which is the port's to give.
		if !errors.Is(err, coreent.ErrNoPossession) {
			t.Fatalf("matchSubject() error = %v, want %v — a bound refusal must refuse", err, coreent.ErrNoPossession)
		}
	})

	t.Run("a three-method port keeps the call it always had", func(t *testing.T) {
		t.Parallel()

		identity := &recordingIdentity{subject: sampleSubject, fingerprint: boundFingerprint}
		svc := &Service{identity: identity}

		if _, err := svc.matchSubject(roster, sampleSubject, base); err != nil {
			t.Fatalf("matchSubject() error = %v, want nil", err)
		}
		//: The fallback is not a weakening and it is not optional: every
		//: implementation written against the frozen port lands here, and an
		//: engine that stopped calling it would break all of them.
		if identity.unbound != 1 {
			t.Errorf("ProvePossession called %d times on a three-method identity, want 1", identity.unbound)
		}
	})
}

// Test_Verify_aBoundProofRefusalIsTheVerificationsRefusal carries the same
// property through the whole engine rather than through one method.
//
// matchSubject is reachable from Verify through authorise, past the roster
// fetch, the ratchet, the version floor and the CI seat. A binding that held in
// matchSubject and was lost on the way out would be the kind of defect this
// domain has paid for before — the ratchet guarded storage and not acceptance
// for exactly that reason.
func Test_Verify_aBoundProofRefusalIsTheVerificationsRefusal(t *testing.T) {
	//: Both variables cleared, so the ambient environment cannot send this
	//: down the CI path. t.Setenv is why this test is sequential.
	t.Setenv(actionsTokenURLEnv, "")
	t.Setenv(actionsTokenBearerEnv, "")

	vendorPub, vendorPriv, keyErr := ed25519.GenerateKey(nil)
	//: A failure here is an environment problem, not a test outcome.
	if keyErr != nil {
		t.Fatalf("generating vendor key: %v", keyErr)
	}

	base := time.Now().Truncate(time.Second)
	roster := coreent.RosterValue{
		IssuedAt:  base.Add(-time.Hour),
		ExpiresAt: base.Add(time.Hour),
		Subjects:  map[string]coreent.SubjectValue{sampleSubject: {Fingerprint: boundFingerprint}},
	}
	identity := &recordingBoundIdentity{
		subject:     sampleSubject,
		fingerprint: boundFingerprint,
		proveErr:    coreent.ErrNoPossession,
	}

	_, err := NewServiceWithGetter(
		stubRoundTripper{bundle: signedBundle(t, vendorPriv, roster)},
		identity, vendorPub, &testProduct).Verify(base)

	if !errors.Is(err, coreent.ErrNoPossession) {
		t.Fatalf("Verify() error = %v, want %v", err, coreent.ErrNoPossession)
	}
	//: And it reached the bound call with the roster's value, so the row is
	//: about the binding rather than about any refusal anywhere upstream.
	if len(identity.boundWith) != 1 || identity.boundWith[0] != boundFingerprint {
		t.Errorf("ProvePossessionFor received %q, want exactly one call with %q", identity.boundWith, boundFingerprint)
	}
}
