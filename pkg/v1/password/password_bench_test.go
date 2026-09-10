package password_test

import (
	"crypto/pbkdf2"
	"crypto/sha256"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/password"
)

// currentIters mirrors the iteration policy service/crypto/pbkdf2pw ships (the
// OWASP fallback for PBKDF2-SHA256). It is duplicated here so the linearity
// benchmarks below can be read against the shipped cost WITHOUT this file
// reaching into an internal package — and so a future policy change makes the
// two numbers disagree visibly rather than silently. derivedLen is the width
// the shipped hasher derives.
const (
	currentIters int = 600_000
	derivedLen   int = 32
)

// Package-level sinks. A PHC string nobody observes is a value the compiler may
// prove dead and delete.
var (
	strSink   string
	bytesSink []byte
	boolSink  bool
	errSink   error
)

// benchPassword is the secret every benchmark stretches. Its length barely
// matters: PBKDF2 hashes it into HMAC's key schedule once and then iterates on
// a fixed-width state, which is the whole point of the construction.
var benchPassword = []byte("correct horse battery staple")

// benchSalt is a fixed 32-byte salt for the bare-PBKDF2 comparisons. Hash draws
// its own random salt per call; a fixed one here keeps the two comparable.
var benchSalt = []byte("0123456789abcdef0123456789abcdef")

// BenchmarkHash is the write side: one stored credential. The number is
// MILLISECONDS and that is the FEATURE, not a defect — a password hash is slow
// on purpose, because the attacker's guess costs exactly what the honest login
// costs. Read it beside BenchmarkBarePBKDF2_1Iter below, which shows the cost
// is the iteration count and nothing else.
func BenchmarkHash(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		strSink, errSink = password.Hash(password.PBKDF2SHA256, benchPassword)
	}
}

// BenchmarkVerify is the login path — the one a request budget has to absorb.
func BenchmarkVerify(b *testing.B) {
	phc, err := password.Hash(password.PBKDF2SHA256, benchPassword)
	if err != nil {
		b.Fatalf("Hash: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		boolSink, errSink = password.Verify(benchPassword, phc)
	}
}

// BenchmarkVerifyWrong is the refusal path, and it MUST cost the same as
// BenchmarkVerify. A wrong password that is refused faster than a right one is
// accepted turns the login endpoint into an oracle; the stored hash is
// recomputed in full and only then compared in constant time, so the two
// numbers landing together is the evidence.
func BenchmarkVerifyWrong(b *testing.B) {
	phc, err := password.Hash(password.PBKDF2SHA256, benchPassword)
	if err != nil {
		b.Fatalf("Hash: %v", err)
	}
	wrong := []byte("correct horse battery stapld")
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		boolSink, errSink = password.Verify(wrong, phc)
	}
}

// BenchmarkVerifyMalformed is the OTHER refusal path, and it is the one an
// unauthenticated caller can reach cheaply by design: a PHC string that does
// not parse is rejected before any stretching, so it must NOT cost
// milliseconds. If it did, a stored-hash lookup miss would be a free
// denial-of-service lever.
func BenchmarkVerifyMalformed(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		boolSink, errSink = password.Verify(benchPassword, "$not-a-phc-string$")
	}
}

// BenchmarkNeedsRehash reads only the iteration field out of the PHC string, so
// it must be nanoseconds. It is called after every successful login, which is
// why that matters.
func BenchmarkNeedsRehash(b *testing.B) {
	phc, err := password.Hash(password.PBKDF2SHA256, benchPassword)
	if err != nil {
		b.Fatalf("Hash: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		boolSink = password.NeedsRehash(phc)
	}
}

// BenchmarkBarePBKDF2_1Iter and BenchmarkBarePBKDF2_CurrentIters are the
// measurement that turns "slow" into a TUNING KNOB. The first is the fixed cost
// of one PBKDF2 round; the second is the shipped policy. If the second is
// currentIters times the first, then the cost is the iteration count — which
// means an operator who needs a different budget changes exactly one number,
// and can compute the new cost without re-running anything.
func BenchmarkBarePBKDF2_1Iter(b *testing.B) { benchBarePBKDF2(b, 1) }

func BenchmarkBarePBKDF2_CurrentIters(b *testing.B) { benchBarePBKDF2(b, currentIters) }

func benchBarePBKDF2(b *testing.B, iters int) {
	b.Helper()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		bytesSink, errSink = pbkdf2.Key(sha256.New, string(benchPassword), benchSalt, iters, derivedLen)
	}
}
