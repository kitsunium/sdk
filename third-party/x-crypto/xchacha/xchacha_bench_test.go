// Package xchacha_test benchmarks the XChaCha20-Poly1305 scheme against the
// SDK's dep-free default, AES-256-GCM (internal/service/crypto/aesgcm). Both
// implement the same core/crypto.AEAD port and produce the same self-framed
// box, so every row here is scheme against scheme with the framing, the key
// handling and the typed-error wrapping held constant. The question the file
// exists to answer is when the 192-bit nonce is worth the swap.
package xchacha_test

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"testing"

	"golang.org/x/crypto/chacha20poly1305"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
	"github.com/kitsunium/sdk/internal/service/crypto/aesgcm"
	"github.com/kitsunium/sdk/third-party/x-crypto/xchacha"
)

const (
	// benchKeyByte fills the fixed 32-byte benchmark key. A constant key keeps
	// the measurement deterministic; it is never a secret here.
	benchKeyByte byte = 0x9
	// benchPlainByte fills the plaintext. Content is irrelevant to an AEAD's
	// cost — both ciphers are counter-mode stream constructions — so a constant
	// byte keeps the corpus reproducible.
	benchPlainByte byte = 0xAB
	// benchSmall is the small-message size: a session token, a cache entry, a
	// row-level encrypted column. This is where per-call setup dominates.
	benchSmall int = 64
	// benchKiB is the 1 KiB size: a typical structured record.
	benchKiB int = 1 << 10
	// benchLarge is 64 KiB — one TLS-record-ish chunk, the size at which the
	// per-byte cost has taken over completely.
	benchLarge int = 64 << 10
	// benchBulk is 1 MiB, the family's bulk reference size (see
	// pkg/v1/crypto/BENCH.md, which quotes AES-GCM at the same size).
	benchBulk int = 1 << 20
)

// benchBox is a package-level sink for sealed output so the compiler cannot
// elide the work being measured.
var benchBox []byte

// benchPlain is a package-level sink for opened plaintext, same reason.
var benchPlain []byte

// benchAEAD is a package-level sink for constructed cipher.AEAD values, so the
// construction benchmarks cannot be optimised away.
var benchAEAD cipher.AEAD

// benchSize names one payload size measured by every row in this file.
type benchSize struct {
	name string
	size int
}

// benchSizes is the size ladder every Seal/Open row walks. Four points, chosen
// so the crossover between per-call and per-byte cost is visible rather than
// asserted.
func benchSizes() []benchSize {
	//: 64 B is per-call-dominated, 1 MiB is per-byte-dominated; the two in
	//: between show where the transition happens.
	return []benchSize{
		{"64B", benchSmall},
		{"1KiB", benchKiB},
		{"64KiB", benchLarge},
		{"1MiB", benchBulk},
	}
}

// benchKey builds the fixed 32-byte key both schemes take.
func benchKey(b *testing.B) corecrypto.Key {
	b.Helper()
	//: KeyLen is 32 for both schemes, so one key serves the whole file.
	key, err := corecrypto.NewKey(bytes.Repeat([]byte{benchKeyByte}, corecrypto.KeyLen))
	if err != nil {
		b.Fatalf("NewKey: %v", err)
	}
	return key
}

// benchSeal runs the size ladder through one scheme's Seal.
func benchSeal(b *testing.B, scheme corecrypto.AEAD) {
	b.Helper()
	key := benchKey(b)
	for _, size := range benchSizes() {
		b.Run(size.name, func(b *testing.B) {
			//: built inside the row so no loop-local is captured by this
			//: closure; it is set up before b.Loop and therefore untimed.
			plaintext := bytes.Repeat([]byte{benchPlainByte}, size.size)
			b.ReportAllocs()
			//: SetBytes makes Go report MB/s of PLAINTEXT, the only unit
			//: comparable across two schemes with different overhead.
			b.SetBytes(int64(size.size))
			for b.Loop() {
				box, err := scheme.Seal(key, plaintext, nil)
				if err != nil {
					b.Fatalf("Seal: %v", err)
				}
				benchBox = box
			}
		})
	}
}

// benchOpen runs the size ladder through one scheme's Open, over a box that
// scheme sealed itself.
func benchOpen(b *testing.B, scheme corecrypto.AEAD) {
	b.Helper()
	key := benchKey(b)
	for _, size := range benchSizes() {
		b.Run(size.name, func(b *testing.B) {
			//: sealed inside the row so no loop-local is captured; b.Loop
			//: resets the timer, so this seed seal is not measured.
			box, err := scheme.Seal(key, bytes.Repeat([]byte{benchPlainByte}, size.size), nil)
			if err != nil {
				b.Fatalf("seed Seal: %v", err)
			}
			b.ReportAllocs()
			//: the rate is quoted per plaintext byte so Seal and Open rows of
			//: the same size are directly comparable.
			b.SetBytes(int64(size.size))
			for b.Loop() {
				out, oerr := scheme.Open(key, box, nil)
				if oerr != nil {
					b.Fatalf("Open: %v", oerr)
				}
				benchPlain = out
			}
		})
	}
}

// BenchmarkSeal measures XChaCha20-Poly1305 Seal across the size ladder.
func BenchmarkSeal(b *testing.B) {
	benchSeal(b, xchacha.AEAD)
}

// BenchmarkOpen measures XChaCha20-Poly1305 Open across the size ladder.
func BenchmarkOpen(b *testing.B) {
	benchOpen(b, xchacha.AEAD)
}

// BenchmarkAESGCMSeal is the reference arm: the SDK's dep-free default sealing
// the same plaintexts through the same port, in the same process and under the
// same machine load.
func BenchmarkAESGCMSeal(b *testing.B) {
	benchSeal(b, aesgcm.AEAD)
}

// BenchmarkAESGCMOpen is the reference arm for Open.
func BenchmarkAESGCMOpen(b *testing.B) {
	benchOpen(b, aesgcm.AEAD)
}

// BenchmarkNewAEADXChaCha measures what XChaCha pays per call to turn 32 key
// bytes into a cipher.AEAD. The port is (Key, []byte) -> []byte, so it has
// nowhere to keep a prepared cipher and both schemes rebuild one every call.
func BenchmarkNewAEADXChaCha(b *testing.B) {
	keyBytes := bytes.Repeat([]byte{benchKeyByte}, corecrypto.KeyLen)
	b.ReportAllocs()
	for b.Loop() {
		aead, err := chacha20poly1305.NewX(keyBytes)
		if err != nil {
			b.Fatalf("NewX: %v", err)
		}
		benchAEAD = aead
	}
}

// BenchmarkNewAEADAESGCM is the same measurement for AES-256-GCM: the key
// schedule plus the GCM tables, which pkg/v1/crypto/BENCH.md already charged
// with 66 % of a 64-byte Seal.
func BenchmarkNewAEADAESGCM(b *testing.B) {
	keyBytes := bytes.Repeat([]byte{benchKeyByte}, corecrypto.KeyLen)
	b.ReportAllocs()
	for b.Loop() {
		block, err := aes.NewCipher(keyBytes)
		if err != nil {
			b.Fatalf("NewCipher: %v", err)
		}
		gcm, gerr := cipher.NewGCM(block)
		if gerr != nil {
			b.Fatalf("NewGCM: %v", gerr)
		}
		benchAEAD = gcm
	}
}

// BenchmarkBareXChaChaSeal64B seals with the cipher hoisted out of the loop. The
// gap against BenchmarkSeal/64B is exactly what the port's shape costs — the
// construction, the defensive key copy and the box framing — separated from the
// encryption itself.
func BenchmarkBareXChaChaSeal64B(b *testing.B) {
	keyBytes := bytes.Repeat([]byte{benchKeyByte}, corecrypto.KeyLen)
	aead, err := chacha20poly1305.NewX(keyBytes)
	if err != nil {
		b.Fatalf("NewX: %v", err)
	}
	plaintext := bytes.Repeat([]byte{benchPlainByte}, benchSmall)
	nonce := make([]byte, chacha20poly1305.NonceSizeX)
	b.ReportAllocs()
	b.SetBytes(int64(benchSmall))
	for b.Loop() {
		//: a fixed nonce is a catastrophic mistake in production and is used
		//: here ONLY because no ciphertext leaves this loop; the measurement is
		//: the cipher's cost, and drawing entropy is measured by the SDK rows.
		benchBox = aead.Seal(nil, nonce, plaintext, nil)
	}
}

// BenchmarkBareAESGCMSeal64B is the same hoisted measurement for AES-256-GCM.
func BenchmarkBareAESGCMSeal64B(b *testing.B) {
	keyBytes := bytes.Repeat([]byte{benchKeyByte}, corecrypto.KeyLen)
	block, err := aes.NewCipher(keyBytes)
	if err != nil {
		b.Fatalf("NewCipher: %v", err)
	}
	gcm, gerr := cipher.NewGCM(block)
	if gerr != nil {
		b.Fatalf("NewGCM: %v", gerr)
	}
	plaintext := bytes.Repeat([]byte{benchPlainByte}, benchSmall)
	nonce := make([]byte, gcm.NonceSize())
	b.ReportAllocs()
	b.SetBytes(int64(benchSmall))
	for b.Loop() {
		//: same fixed-nonce caveat as the XChaCha arm above — nothing sealed
		//: here is ever opened, stored or transmitted.
		benchBox = gcm.Seal(nil, nonce, plaintext, nil)
	}
}
