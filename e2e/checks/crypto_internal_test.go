// Package checks — the crypto conformance checks.
package checks

import (
	"testing"
)

// The crypto checks are pure computation — no shell, no kernel facility, no
// permission — so unlike the process and cgroup domains they can be exercised
// here as well as in the conformance binary. That makes them the cheapest place
// to catch the regression the whole harness exists for: a primitive that still
// compiles and no longer produces the right bytes.

// Test_cryptoAEADRoundTrip pins that sealing and opening returns the plaintext.
func Test_cryptoAEADRoundTrip(t *testing.T) {
	t.Parallel()
	if problem := passingRow(cryptoAEADRoundTrip(), cryptoDomain); problem != nil {
		t.Fatal(problem)
	}
}

// Test_cryptoAEADWrongAAD pins the half that matters more: associated data that
// does not match must make Open FAIL. An AEAD that opens anyway has silently
// become an unauthenticated cipher.
func Test_cryptoAEADWrongAAD(t *testing.T) {
	t.Parallel()
	if problem := passingRow(cryptoAEADWrongAAD(), cryptoDomain); problem != nil {
		t.Fatal(problem)
	}
}

// Test_cryptoHashDeterministic pins that the same input hashes to the same
// digest — the property every signature and every integrity check downstream is
// built on.
func Test_cryptoHashDeterministic(t *testing.T) {
	t.Parallel()
	if problem := passingRow(cryptoHashDeterministic(), cryptoDomain); problem != nil {
		t.Fatal(problem)
	}
}

// Test_cryptoSignVerify pins the sign/verify round trip. A verifier that accepts
// everything passes every test that only signs and verifies its own output,
// which is why the check exists as a conformance probe rather than a unit test.
func Test_cryptoSignVerify(t *testing.T) {
	t.Parallel()
	if problem := passingRow(cryptoSignVerify(), cryptoDomain); problem != nil {
		t.Fatal(problem)
	}
}

// Test_cryptoKDFDeterministic pins that a KDF is reproducible: the same secret
// and salt must derive the same key, or nothing encrypted on one host can be
// read on another.
func Test_cryptoKDFDeterministic(t *testing.T) {
	t.Parallel()
	if problem := passingRow(cryptoKDFDeterministic(), cryptoDomain); problem != nil {
		t.Fatal(problem)
	}
}

// Test_cryptoPasswordRoundTrip pins hash-then-verify for a password, including
// that a wrong password is refused — the direction a broken comparison passes.
func Test_cryptoPasswordRoundTrip(t *testing.T) {
	t.Parallel()
	if problem := passingRow(cryptoPasswordRoundTrip(), cryptoDomain); problem != nil {
		t.Fatal(problem)
	}
}

// Test_cryptoMACRoundTrip pins that a MAC authenticates its message and rejects
// a tampered one.
func Test_cryptoMACRoundTrip(t *testing.T) {
	t.Parallel()
	if problem := passingRow(cryptoMACRoundTrip(), cryptoDomain); problem != nil {
		t.Fatal(problem)
	}
}

// Test_cryptoAgreeSharedKey pins that both sides of a key agreement arrive at
// the SAME key. Two parties that derive different keys fail later, at the first
// message, with an error that names decryption rather than agreement.
func Test_cryptoAgreeSharedKey(t *testing.T) {
	t.Parallel()
	if problem := passingRow(cryptoAgreeSharedKey(), cryptoDomain); problem != nil {
		t.Fatal(problem)
	}
}
