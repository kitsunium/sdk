package hmacsha2

import (
	"crypto/hmac"
	"crypto/sha256"
	"hash"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
)

// algorithm is the canonical registry key for HMAC-SHA256.
const algorithm corecrypto.Algorithm = "hmac-sha256"

// MAC is the registered HMAC-SHA256 scheme singleton (no init(); package-level
// var initialiser, mirroring the codec/AEAD convention).
var MAC = corecrypto.RegisterMAC(hmacSHA256{})

// hmacSHA256 implements core/crypto.MAC over crypto/hmac with SHA-256.
type hmacSHA256 struct{}

// Algorithm reports the canonical algorithm key.
func (hmacSHA256) Algorithm() corecrypto.Algorithm {
	//: the literal key consumers pass to MACTag/MACVerify.
	return algorithm
}

// Tag returns the HMAC-SHA256 authentication tag over message under key. The
// redacting Key pins the length so this cannot fail; key.Bytes() goes straight
// to the MAC and is never logged.
func (hmacSHA256) Tag(key corecrypto.Key, message []byte) []byte {
	//: fresh keyed MAC, then absorb the whole message in one pass.
	mac := hmac.New(sha256.New, key.Bytes())
	//: Write on a hash.Hash never returns an error (documented contract).
	mac.Write(message)
	//: append the digest onto a nil base to return a fresh tag slice.
	return mac.Sum(nil)
}

// Verify reports whether tag authenticates message under key, comparing with
// hmac.Equal so the check is constant-time and never a timing oracle.
func (h hmacSHA256) Verify(key corecrypto.Key, message, tag []byte) bool {
	//: recompute the expected tag, then compare in constant time.
	want := h.Tag(key, message)
	//: hmac.Equal is the constant-time comparison mandated by the MAC contract.
	return hmac.Equal(want, tag)
}

// New returns a fresh streaming hash.Hash keyed by key for incremental tagging
// over large inputs, paralleling Hasher.New.
func (hmacSHA256) New(key corecrypto.Key) hash.Hash {
	//: a keyed HMAC the caller drives via Write/Sum.
	return hmac.New(sha256.New, key.Bytes())
}
