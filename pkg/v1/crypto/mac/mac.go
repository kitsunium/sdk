package mac

import (
	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"

	// Activates the stdlib HMAC-SHA256 scheme. Stdlib-only, so importing
	// pkg/v1/crypto/mac pulls zero non-stdlib dependencies.
	_ "github.com/kitsunium/sdk/internal/service/crypto/mac/hmacsha2"
)

// HMACSHA256 is HMAC (RFC 2104) over SHA-256 — the detached-MAC default.
const HMACSHA256 Algorithm = "hmac-sha256"

// Tag returns the authentication tag over message under key for the named
// scheme. An unregistered algorithm returns UnknownMACAlgorithm; a registered
// scheme cannot fail (the redacting Key pins the length).
func Tag(a Algorithm, key Key, message []byte) (tag []byte, err error) {
	//: convert the domain-typed Algorithm to the core key at the boundary.
	return corecrypto.MACTag(corecrypto.Algorithm(a), key, message)
}

// Verify reports whether tag authenticates message under key for the named
// scheme, using a constant-time comparison. An unregistered algorithm returns
// (false, UnknownMACAlgorithm); a bad tag is (false, nil).
func Verify(a Algorithm, key Key, message, tag []byte) (ok bool, err error) {
	//: convert the domain-typed Algorithm to the core key at the boundary.
	return corecrypto.MACVerify(corecrypto.Algorithm(a), key, message, tag)
}
