// Package crypto — the AEAD port implemented by each concrete algorithm.
package crypto

// AEAD is an authenticated-encryption-with-associated-data scheme. Each
// implementation owns its complete self-describing wire framing
// ([Version][id][nonce][ciphertext||tag]) so the registry can dispatch Open by
// reading the 1-byte id, and so the nonce — generated from crypto/rand inside
// Seal — never reaches a caller. Implementations MUST be safe for concurrent
// use and MUST return the shared non-oracle DecryptionFailed for every Open
// failure (bad tag, bad header, wrong key) so they cannot become an oracle.
//
// IFACE-PLUGIN: the registry hands plug-in AEAD instances back to callers behind
// this interface; concrete scheme types stay unexported in their own packages.
type AEAD interface {
	// Algorithm reports the canonical key under which this scheme registers.
	Algorithm() Algorithm
	// ID reports the 1-byte wire identifier embedded in the box header so Open
	// can resolve the scheme without the caller naming it.
	ID() byte
	// Seal encrypts plaintext, binding aad, and returns the self-framed box
	// (Version + id + random nonce + ciphertext||tag).
	Seal(key Key, plaintext, aad []byte) (box []byte, err error)
	// Open reverses Seal: it validates the framing, decrypts, and verifies aad,
	// returning the plaintext or the non-oracle DecryptionFailed.
	Open(key Key, box, aad []byte) (plaintext []byte, err error)
}
