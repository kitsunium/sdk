// Package argon2id_test prices a password hash whose cost is the point. Nothing
// here is overhead to be trimmed: argon2id is slow and memory-hungry on purpose,
// so that an attacker's guess costs what an honest login costs. What a benchmark
// can usefully add is the CALIBRATION an operator actually needs — what each of
// the three knobs (memory, time, parallelism) buys and costs on real hardware —
// plus the reference arm the SDK ships by default, PBKDF2-SHA256, measured in
// the same process under the same load.
package argon2id_test

import (
	"testing"

	"golang.org/x/crypto/argon2"

	"github.com/kitsunium/sdk/internal/service/crypto/pbkdf2pw"
	"github.com/kitsunium/sdk/third-party/x-crypto/argon2id"
)

const (
	// shippedMem is the memory cost this package ships, in KiB (19 MiB, the
	// OWASP 2023 first-choice parameter set).
	shippedMem uint32 = 19456
	// shippedTime is the shipped iteration (time) cost.
	shippedTime uint32 = 2
	// shippedThreads is the shipped parallelism degree.
	shippedThreads uint8 = 1
	// benchKeyLen is the derived digest length in bytes, matching the package.
	benchKeyLen uint32 = 32
)

// benchPassword is the fixture credential. Constant, so the measurement is
// reproducible; it is not a secret and never leaves this file.
var benchPassword = []byte("correct horse battery staple")

// benchWrongPassword is the same length as benchPassword, so a length-dependent
// implementation could not be mistaken for a constant-time one.
var benchWrongPassword = []byte("correct horse battery stapleX")

// benchDigest is a package-level sink for derived keys, so the compiler cannot
// elide the derivation being measured.
var benchDigest []byte

// benchOK is a package-level sink for Verify's boolean result.
var benchOK bool

// benchPHC is a package-level sink for produced PHC strings.
var benchPHC string

// costPoint is one point on a calibration ladder: a label plus the three argon2
// cost parameters it configures.
type costPoint struct {
	name    string
	mem     uint32
	time    uint32
	threads uint8
}

// memoryLadder walks the memory cost with time and parallelism held at the
// shipped values. Memory is argon2's defining defence, so this is the knob an
// operator turns first.
func memoryLadder() []costPoint {
	//: 19 MiB is the shipped policy; the ladder brackets it by a factor of
	//: about seven in each direction so the shape is visible, not extrapolated.
	return []costPoint{
		{"m=8MiB", 8 << 10, shippedTime, shippedThreads},
		{"m=16MiB", 16 << 10, shippedTime, shippedThreads},
		{"m=19MiB-shipped", shippedMem, shippedTime, shippedThreads},
		{"m=32MiB", 32 << 10, shippedTime, shippedThreads},
		{"m=64MiB", 64 << 10, shippedTime, shippedThreads},
		{"m=128MiB", 128 << 10, shippedTime, shippedThreads},
	}
}

// timeLadder walks the iteration count with memory and parallelism held at the
// shipped values.
func timeLadder() []costPoint {
	//: t=1 is the RFC 9106 low-memory floor; t=6 is well past any current
	//: guidance, which is what makes the linearity check meaningful.
	return []costPoint{
		{"t=1", shippedMem, 1, shippedThreads},
		{"t=2-shipped", shippedMem, shippedTime, shippedThreads},
		{"t=3", shippedMem, 3, shippedThreads},
		{"t=4", shippedMem, 4, shippedThreads},
		{"t=6", shippedMem, 6, shippedThreads},
	}
}

// threadLadder walks parallelism with memory and time held at the shipped
// values. Parallelism divides the SAME matrix across lanes, so it buys latency
// and costs no additional memory — which is the fact this ladder exists to show.
func threadLadder() []costPoint {
	//: this box has 8 cores, so p=8 is the last point where a lane can own one.
	return []costPoint{
		{"p=1-shipped", shippedMem, shippedTime, shippedThreads},
		{"p=2", shippedMem, shippedTime, 2},
		{"p=4", shippedMem, shippedTime, 4},
		{"p=8", shippedMem, shippedTime, 8},
	}
}

// benchLadder runs one calibration ladder through the bare argon2.IDKey — the
// knob itself, with the PHC facade held out so the numbers are the KDF's.
func benchLadder(b *testing.B, points []costPoint) {
	b.Helper()
	salt := make([]byte, 16)
	for _, p := range points {
		b.Run(p.name, func(b *testing.B) {
			b.ReportAllocs()
			//: the sub-benchmark NAME carries the configured parameter, which
			//: is why no extra metric is reported: b.Loop resets the timer on
			//: its first call and a ResetTimer clears extra metrics, so a
			//: ReportMetric placed before the loop is silently dropped.
			for b.Loop() {
				benchDigest = argon2.IDKey(benchPassword, salt, p.time, p.mem, p.threads, benchKeyLen)
			}
		})
	}
}

// seedPHC produces one stored hash at the shipped policy for the Verify arms.
func seedPHC(b *testing.B) string {
	b.Helper()
	phc, err := argon2id.PasswordHasher.Hash(benchPassword)
	if err != nil {
		b.Fatalf("seed Hash: %v", err)
	}
	return phc
}

// BenchmarkHash measures the shipped policy end to end: a fresh crypto/rand
// salt, the derivation, and the PHC assembly. This is the number a capacity plan
// is built on.
func BenchmarkHash(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		phc, err := argon2id.PasswordHasher.Hash(benchPassword)
		if err != nil {
			b.Fatalf("Hash: %v", err)
		}
		benchPHC = phc
	}
}

// BenchmarkVerify measures a successful login: parse the stored PHC, recompute
// at ITS parameters, compare in constant time.
func BenchmarkVerify(b *testing.B) {
	phc := seedPHC(b)
	b.ReportAllocs()
	for b.Loop() {
		ok, err := argon2id.PasswordHasher.Verify(benchPassword, phc)
		if err != nil {
			b.Fatalf("Verify: %v", err)
		}
		benchOK = ok
	}
}

// BenchmarkVerifyWrong measures a rejected login. Against BenchmarkVerify it
// corroborates that the endpoint is not a timing oracle — the guarantee itself
// is structural (subtle.ConstantTimeCompare over a full recomputation), and a
// benchmark can only rule out a gap large enough to matter operationally.
func BenchmarkVerifyWrong(b *testing.B) {
	phc := seedPHC(b)
	b.ReportAllocs()
	for b.Loop() {
		ok, err := argon2id.PasswordHasher.Verify(benchWrongPassword, phc)
		if err != nil {
			b.Fatalf("Verify: %v", err)
		}
		benchOK = ok
	}
}

// BenchmarkVerifyMalformed measures the refusal path for a stored hash that does
// not parse. It must be orders of magnitude cheaper than a real verify, or a
// corrupt database row becomes a free denial-of-service lever.
func BenchmarkVerifyMalformed(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		ok, err := argon2id.PasswordHasher.Verify(benchPassword, "$argon2id$v=19$not-a-cost-field")
		if err == nil {
			b.Fatal("Verify accepted a malformed PHC string")
		}
		benchOK = ok
	}
}

// BenchmarkNeedsRehash measures the upgrade-on-verify probe. It parses the PHC
// and compares three integers; it never derives a key.
func BenchmarkNeedsRehash(b *testing.B) {
	phc := seedPHC(b)
	b.ReportAllocs()
	for b.Loop() {
		benchOK = argon2id.PasswordHasher.NeedsRehash(phc)
	}
}

// BenchmarkBareIDKey is the shipped policy with the SDK facade removed: no
// salt draw, no base64, no PHC assembly. The gap against BenchmarkHash is
// everything this package adds.
func BenchmarkBareIDKey(b *testing.B) {
	salt := make([]byte, 16)
	b.ReportAllocs()
	for b.Loop() {
		benchDigest = argon2.IDKey(benchPassword, salt, shippedTime, shippedMem, shippedThreads, benchKeyLen)
	}
}

// BenchmarkCalibrateMemory walks the memory cost — the parameter that is the
// whole defence, and the one -benchmem does report, because argon2 allocates the
// entire block matrix in a single make.
func BenchmarkCalibrateMemory(b *testing.B) {
	benchLadder(b, memoryLadder())
}

// BenchmarkCalibrateTime walks the iteration count.
func BenchmarkCalibrateTime(b *testing.B) {
	benchLadder(b, timeLadder())
}

// BenchmarkCalibrateThreads walks the parallelism degree.
func BenchmarkCalibrateThreads(b *testing.B) {
	benchLadder(b, threadLadder())
}

// BenchmarkPBKDF2Hash is the reference arm: the password hash a consumer gets
// WITHOUT this dependency (internal/service/crypto/pbkdf2pw, PBKDF2-SHA256 at
// 600 000 iterations), measured in the same process under the same load.
func BenchmarkPBKDF2Hash(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		phc, err := pbkdf2pw.PasswordHasher.Hash(benchPassword)
		if err != nil {
			b.Fatalf("pbkdf2 Hash: %v", err)
		}
		benchPHC = phc
	}
}

// BenchmarkPBKDF2Verify is the reference arm for a successful login.
func BenchmarkPBKDF2Verify(b *testing.B) {
	phc, err := pbkdf2pw.PasswordHasher.Hash(benchPassword)
	if err != nil {
		b.Fatalf("seed pbkdf2 Hash: %v", err)
	}
	b.ReportAllocs()
	for b.Loop() {
		ok, verr := pbkdf2pw.PasswordHasher.Verify(benchPassword, phc)
		if verr != nil {
			b.Fatalf("pbkdf2 Verify: %v", verr)
		}
		benchOK = ok
	}
}
