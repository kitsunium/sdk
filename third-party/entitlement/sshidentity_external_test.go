// External tests: the ssh implementation of the bound possession proof, over
// real key files, because the window it closes is a window in the FILESYSTEM.
package entitlement_test

import (
	"errors"
	"testing"

	entitlement "github.com/kitsunium/sdk/third-party/entitlement"

	coreent "github.com/kitsunium/sdk/internal/core/entitlement"
)

// boundSubject is the canonical UUID the bound-proof cases enrol under.
const boundSubject string = "44444444-5555-4666-8777-888888888888"

// TestProvePossessionForBindsTheProofToTheAuthorisedFingerprint pins what the
// fourth method buys over the three, and it is not a stricter fingerprint
// policy — it is that the comparison and the signature happen against ONE read
// of the key directory.
//
// # The window
//
// The engine asks Fingerprint what this machine presents, compares that answer
// against the roster, and then asks for a proof. Anything that can write the key
// directory gets to act in between: a rotation landing mid-verification, a
// volume remounted, a `<uuid>.pub` swapped on purpose. The three-method port
// signs with whatever the second call finds and has no way to notice, because
// the authorised value never reaches it.
//
// The middle case performs exactly that swap, between the two calls, over real
// files — and asserts the UNBOUND proof still accepts it. That control is what
// makes the case a measurement of what the sibling adds rather than a test that
// would also pass against an implementation refusing everything.
func TestProvePossessionForBindsTheProofToTheAuthorisedFingerprint(t *testing.T) {
	t.Parallel()

	t.Run("the authorised key proves possession", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		pub, _ := writeKeyPair(t, dir, boundSubject, ownerOnly)
		identity := entitlement.NewSSHIdentity(dir)

		//: The value a roster would publish for this subject, rendered by the
		//: one function whose spelling the roster's contract is about.
		authorised := entitlement.Fingerprint(pub)
		if err := identity.ProvePossessionFor(boundSubject, authorised); err != nil {
			t.Fatalf("ProvePossessionFor() error = %v, want nil — the enrolled key must still prove", err)
		}
	})

	t.Run("a key swapped after the fingerprint was read does not", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		writeKeyPair(t, dir, boundSubject, ownerOnly)
		identity := entitlement.NewSSHIdentity(dir)

		//: Step 1: the engine reads what this machine presents and compares it
		//: against the roster. This is the value the roster authorised.
		authorised, printErr := identity.Fingerprint(boundSubject)
		//: A failure here is a broken fixture, not the property under test.
		if printErr != nil {
			t.Fatalf("Fingerprint() error = %v, want nil", printErr)
		}

		//: Step 2: BOTH halves replaced, in the window between the comparison
		//: and the proof. A consistent pair, so nothing about the material is
		//: malformed — it is simply not the key that was authorised, which is
		//: the only thing distinguishing this from an ordinary verification.
		writeKeyPair(t, dir, boundSubject, ownerOnly)

		//: Step 3: the proof. The bound method refuses.
		err := identity.ProvePossessionFor(boundSubject, authorised)
		if !errors.Is(err, coreent.ErrKeyMismatch) {
			t.Fatalf("ProvePossessionFor() error = %v, want %v — a key swapped between the "+
				"comparison and the proof was signed for", err, coreent.ErrKeyMismatch)
		}
		//: The control, and the reason this case is a measurement: the
		//: three-method proof ACCEPTS the swapped key, because nothing about
		//: the roster's decision ever reaches it.
		if unbound := identity.ProvePossession(boundSubject); unbound != nil {
			t.Fatalf("ProvePossession() error = %v, want nil — the case's premise is that the "+
				"unbound proof cannot see the swap", unbound)
		}
	})

	t.Run("an unenrolled subject reports no licence, not a mismatch", func(t *testing.T) {
		t.Parallel()

		identity := entitlement.NewSSHIdentity(t.TempDir())

		//: Absence must not be reported as the wrong key: one sends an operator
		//: to enrolment, the other to a rotation that will not help.
		err := identity.ProvePossessionFor(boundSubject, "SHA256:whatever")
		if !errors.Is(err, coreent.ErrNoLicense) {
			t.Fatalf("ProvePossessionFor() error = %v, want %v", err, coreent.ErrNoLicense)
		}
	})
}
