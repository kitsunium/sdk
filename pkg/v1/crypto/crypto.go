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
package crypto

import (
	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"

	// Activates the default AES-256-GCM scheme. Stdlib-only, so importing
	// pkg/v1/crypto pulls zero non-stdlib dependencies.
	_ "github.com/kitsunium/sdk/internal/service/crypto/aesgcm"
)

// KeyLen is the required symmetric key length in bytes (256-bit).
const KeyLen int = corecrypto.KeyLen

// AESGCM is the default algorithm: AES-256-GCM. Active out of the box.
const AESGCM Algorithm = "aes-256-gcm"

// defaultAlgorithm is the scheme Seal uses when the caller does not pick one.
const defaultAlgorithm Algorithm = AESGCM

// Algorithm is the stable identifier of an AEAD scheme. Use it with SealAs.
type Algorithm = corecrypto.Algorithm

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
	//: dispatch to the default scheme via the core registry.
	return corecrypto.Seal(defaultAlgorithm, k, plaintext, aad)
}

// SealAs is Seal with an explicit algorithm — e.g. a scheme activated by its own
// blank import. An unregistered algorithm returns UnknownAlgorithm.
func SealAs(a Algorithm, k Key, plaintext, aad []byte) (box []byte, err error) {
	//: caller-chosen scheme; UnknownAlgorithm if its package was not imported.
	return corecrypto.Seal(a, k, plaintext, aad)
}

// Open decrypts a box produced by Seal/SealAs under k, verifying aad, and
// returns the plaintext. The algorithm is read from the box — the caller never
// names it. Any failure returns the single non-oracle DecryptionFailed.
func Open(k Key, box, aad []byte) (plaintext []byte, err error) {
	//: the core dispatcher sniffs the box's algorithm id and verifies.
	return corecrypto.Open(k, box, aad)
}
