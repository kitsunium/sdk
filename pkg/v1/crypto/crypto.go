//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/crypto .

// Package crypto is the authenticated-encryption facade: Seal and Open with a
// hidden nonce.
//
// One key, two verbs. [Seal] encrypts a byte slice into a self-describing box;
// [Open] turns the box back into plaintext. The nonce is generated, embedded,
// and stripped for you — there is no nonce parameter to misuse, and the secure
// path is the only path.
//
//	k, _   := crypto.NewKey(key32)            // exactly 32 bytes, copied + redacting
//	box, _ := crypto.Seal(k, []byte("hi"), nil) // nonce generated + hidden inside box
//	pt, _  := crypto.Open(k, box, nil)          // []byte("hi")
//
// # Hard to misuse, by design
//
//   - The nonce never appears in the API. [Seal] draws it from crypto/rand and
//     embeds it; [Open] strips it. Nonce reuse is structurally impossible.
//   - [NewKey] rejects any input that is not exactly [KeyLen] (32) bytes with
//     InvalidKey — never silent truncation — and copies the bytes defensively.
//   - [Key] redacts: its %v / %s / %#v output is always "<redacted>", and key
//     material never reaches an error's public or private message.
//   - [Open] returns one undifferentiated error for every failure (bad tag,
//     tampered framing, wrong key), so it cannot be used as an oracle.
//
// # Associated data (aad)
//
// The optional aad argument is authenticated but NOT encrypted: it is bound into
// the tag so Open fails if it differs, but it is not stored in the box. Use it
// to tie a ciphertext to its context (a record id, a table name, a format tag)
// so a box cannot be replayed into a different context. Pass nil when unused.
//
// # Algorithms
//
// The default is AES-256-GCM (stdlib, hardware-accelerated, FIPS-track) and is
// active out of the box — importing this package activates it with zero
// non-stdlib dependencies. [SealAs] selects a specific [Algorithm]; additional
// schemes (e.g. XChaCha20-Poly1305) activate via their own blank import.
//
// # Wire format
//
// A box is [1-byte version][1-byte algorithm-id][nonce][ciphertext || tag]. The
// version and per-algorithm id are frozen post-v1.0.0, so a box sealed today
// opens tomorrow. Open reads the id to pick the algorithm — the caller never
// names it.
//
// # Streaming large payloads
//
// [Seal] is whole-buffer: it holds the full plaintext and box in memory. For
// payloads too large to buffer, [SealStream] / [OpenStream] wrap an io.Writer /
// io.Reader and process the data in fixed 64 KiB authenticated chunks under a
// disjoint, frozen wire format (stream version 0x02). The chunked construction
// (a random per-stream salt + a counter nonce + a final-chunk flag) is
// truncation-resistant and never surfaces a chunk's plaintext before it
// authenticates. The streaming frame is bespoke and SDK-owned (not age/libsodium
// interop) and stays stdlib-only, so it preserves the dep-light invariant.
package crypto

import (
	"io"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"

	// Activates the default AES-256-GCM scheme. Stdlib-only, so importing
	// pkg/v1/crypto pulls zero non-stdlib dependencies.
	_ "github.com/kitsunium/sdk/internal/service/crypto/aesgcm"
	"github.com/kitsunium/sdk/internal/service/crypto/keyenvelope"

	// Activates the stdlib streaming AES-256-GCM scheme behind SealStream /
	// OpenStream. Stdlib-only, so it preserves the dep-light invariant.
	_ "github.com/kitsunium/sdk/internal/service/crypto/streamaead"
)

// streamAlgorithm is the frozen algorithm key of the stdlib streaming scheme
// SealStream / OpenStream dispatch to (activated by the blank import above).
const streamAlgorithm Algorithm = "aes-256-gcm-stream"

// KeyLen is the required symmetric key length in bytes (256-bit).
const KeyLen int = corecrypto.KeyLen

// AESGCM is the default algorithm: AES-256-GCM. Active out of the box.
const AESGCM Algorithm = "aes-256-gcm"

// XChaCha20Poly1305 is the 192-bit-nonce AEAD, preferred for high-volume
// random-nonce workloads. Pass it to SealAs after a blank import of
// third-party/x-crypto/xchacha (which alone pulls golang.org/x/crypto):
//
//	import _ "github.com/kitsunium/sdk/third-party/x-crypto/xchacha"
//	box, _ := crypto.SealAs(crypto.XChaCha20Poly1305, k, pt, aad)
const XChaCha20Poly1305 Algorithm = "xchacha20poly1305"

// defaultAlgorithm is the scheme Seal uses when the caller does not pick one.
const defaultAlgorithm Algorithm = AESGCM

// Algorithm is the stable identifier of an AEAD scheme. Use it with SealAs. It
// is a defined type distinct from the other crypto-family Algorithm types (hash,
// mac, sign, …), so the compiler rejects feeding a hash or signature constant
// into an AEAD call (V104) — the seven registries are separate keyspaces, and
// the type system now enforces that separation the way typed Format/Level
// discipline does elsewhere.
type Algorithm corecrypto.Algorithm

// Key is an opaque, redacting 256-bit symmetric key. Build one with NewKey; its
// String output is always "<redacted>".
type Key = corecrypto.Key

// NewKey builds a Key from raw, which must be exactly KeyLen (32) bytes. A wrong
// length returns InvalidKey; the bytes are copied defensively.
func NewKey(raw []byte) (key Key, err error) {
	//: delegate to the core constructor; this façade adds no behaviour.
	return corecrypto.NewKey(raw)
}

// Seal encrypts plaintext under k using the default algorithm (AES-256-GCM),
// binding aad, and returns a self-describing box. The nonce is generated and
// embedded for you. Pass nil aad when unused.
func Seal(k Key, plaintext, aad []byte) (box []byte, err error) {
	//: convert the domain-typed Algorithm to the core key at the boundary.
	return corecrypto.Seal(corecrypto.Algorithm(defaultAlgorithm), k, plaintext, aad)
}

// SealAs is Seal with an explicit algorithm — e.g. a scheme activated by its own
// blank import. An unregistered algorithm returns UnknownAlgorithm.
func SealAs(a Algorithm, k Key, plaintext, aad []byte) (box []byte, err error) {
	//: convert the domain-typed Algorithm to the core key at the boundary.
	return corecrypto.Seal(corecrypto.Algorithm(a), k, plaintext, aad)
}

// Open decrypts a box produced by Seal/SealAs under k, verifying aad, and
// returns the plaintext. The algorithm is read from the box — the caller never
// names it. Any failure returns the single non-oracle DecryptionFailed.
func Open(k Key, box, aad []byte) (plaintext []byte, err error) {
	//: the core dispatcher sniffs the box's algorithm id and verifies.
	return corecrypto.Open(k, box, aad)
}

// SealStream wraps dst so writes are sealed under k with aad in fixed 64 KiB
// authenticated chunks. The returned io.WriteCloser buffers and seals chunks as
// they fill; Close writes the final authenticated chunk and MUST be called to
// produce a valid stream. Pass nil aad when unused.
func SealStream(dst io.Writer, k Key, aad []byte) (sealed io.WriteCloser, err error) {
	//: convert the domain-typed Algorithm to the core key at the boundary.
	return corecrypto.SealStream(corecrypto.Algorithm(streamAlgorithm), k, dst, aad)
}

// OpenStream wraps src so reads are opened under k with aad, decrypting the
// chunked stream produced by SealStream. The returned io.Reader never surfaces a
// chunk's plaintext before it authenticates, returns EOF only after the final
// chunk verifies, and surfaces a truncated stream as StreamTruncated. Pass nil
// aad when unused.
func OpenStream(src io.Reader, k Key, aad []byte) (opened io.Reader, err error) {
	//: convert the domain-typed Algorithm to the core key at the boundary.
	return corecrypto.OpenStream(corecrypto.Algorithm(streamAlgorithm), k, src, aad)
}

// WrapKey seals the data key dek at rest under passphrase, returning the frozen
// "$kenv$" envelope string. The KEK is stretched from passphrase with
// PBKDF2-SHA256 and the dek is sealed under AES-256-GCM; the envelope header is
// bound as AAD so tampering is detected on UnwrapKey.
func WrapKey(passphrase []byte, dek Key) (envelope string, err error) {
	//: delegate to the service emitter; this façade adds no behaviour.
	return keyenvelope.WrapKey(passphrase, dek)
}

// UnwrapKey recovers the data key sealed in envelope under passphrase. A
// structurally invalid envelope returns InvalidKeyEnvelope; a wrong passphrase
// returns the non-oracle DecryptionFailed.
func UnwrapKey(passphrase []byte, envelope string) (dek Key, err error) {
	//: delegate to the service emitter; this façade adds no behaviour.
	return keyenvelope.UnwrapKey(passphrase, envelope)
}
