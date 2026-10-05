package password

import (
	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"

	// Activates the stdlib PBKDF2-SHA256 hasher. Stdlib-only, so importing
	// pkg/v1/crypto/password pulls zero non-stdlib dependencies.
	_ "github.com/kitsunium/sdk/internal/service/crypto/password/pbkdf2pw"
)

// PBKDF2SHA256 is PBKDF2-SHA256 — the stdlib password stretcher (RFC 8018).
const PBKDF2SHA256 Algorithm = "pbkdf2-sha256"

// Hash returns a PHC-string hash of password using the named scheme, with a
// fresh random salt and the scheme's current cost parameters. An unregistered
// algorithm returns UnknownPasswordAlgorithm. Store the returned string as-is.
func Hash(a Algorithm, password []byte) (phc string, err error) {
	//: convert the domain-typed Algorithm to the core key at the boundary.
	return corecrypto.HashPassword(corecrypto.Algorithm(a), password)
}
