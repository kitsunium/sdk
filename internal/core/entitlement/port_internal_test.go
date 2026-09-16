// Internal tests: the port's freeze, asserted rather than commented.
package entitlement

import "testing"

// threeMethodDouble is the shape every downstream implementation of the port
// has: Discover, Fingerprint, ProvePossession, and nothing else.
//
// It exists to BE assigned, which is the whole test. A contributor who folds
// BoundProver's method back into Identity fails the named assertion below
// before reaching review — which is what ADR 0039 §2 requires, because the
// comment that says "do not" is exactly what that ADR was written to replace.
type threeMethodDouble struct{}

// Discover answers the first of the three.
func (threeMethodDouble) Discover() (string, error) {
	//: The double answers; what it answers is irrelevant here.
	return "", nil
}

// Fingerprint answers the second of the three.
func (threeMethodDouble) Fingerprint(string) (string, error) {
	//: The double answers; what it answers is irrelevant here.
	return "", nil
}

// ProvePossession answers the third of the three.
func (threeMethodDouble) ProvePossession(string) error {
	//: A nil error is the proof, and this double always proves.
	return nil
}

// Two declarations, both compile-time assertions of the SAME freeze, in the two
// forms a downstream implementer actually writes.
//
// Package level rather than inside a test body so the freeze fails the BUILD
// rather than one test run, which is the stronger of the two and what ADR 0039
// §2 asks for. The named one exists so the tests below can READ it: a guard
// nothing names is a guard a reviewer has to already know about.
var (
	// _ is the pointer form, which is how a consumer with a stateful key
	// custody declares it.
	_ Identity = (*threeMethodDouble)(nil)
	// threeMethodPort is the value form, and the one the tests read.
	threeMethodPort Identity = threeMethodDouble{}
)

// TestAThreeMethodDoubleStillSatisfiesIdentity pins the freeze.
//
// Identity is reachable through a pkg/v1 type alias, so ADR 0039 binds and
// ADR 0040 §4 says the version does not matter: Go satisfies interfaces
// STRUCTURALLY, so any downstream type with these three methods is an Identity
// without importing anything or declaring intent — and adding a fourth method,
// or a parameter to one of the three, breaks every one of them at compile time
// with no deprecation window.
//
// The assignment below is the assertion. It does not compile if Identity grows.
func TestAThreeMethodDoubleStillSatisfiesIdentity(t *testing.T) {
	t.Parallel()

	//: The declaration of threeMethodPort is what asserts the freeze; reading
	//: it here is what gives that assertion a name a reviewer can find. A nil
	//: interface would mean the double never became an Identity at all.
	if threeMethodPort == nil {
		t.Fatal("a three-method double assigned to Identity read back nil")
	}
}

// TestAThreeMethodDoubleIsNotABoundProver pins the other half: the sibling is a
// SEPARATE contract, and its absence is what a consumer discovers.
//
// If BoundProver's method were ever folded into Identity, this assertion would
// start passing for the wrong reason — every Identity would be a BoundProver —
// and the test above would already have failed to compile. The two together say
// "three methods, and a fourth capability that is opted into".
func TestAThreeMethodDoubleIsNotABoundProver(t *testing.T) {
	t.Parallel()

	//: The type assertion the engine itself uses. It must fail here, or the
	//: fallback path the engine keeps for unbound identities is unreachable
	//: and untested.
	if _, ok := threeMethodPort.(BoundProver); ok {
		t.Fatal("a three-method double satisfies BoundProver — the sibling has been folded into the port")
	}
}
