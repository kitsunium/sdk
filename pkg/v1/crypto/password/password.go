package password

import (
	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"

	// Activates the stdlib PBKDF2-SHA256 hasher. Stdlib-only, so importing
	// pkg/v1/crypto/password pulls zero non-stdlib dependencies.
	_ "github.com/kitsunium/sdk/internal/service/crypto/password/pbkdf2pw"
)

// Algorithm is the stable identifier of a password-hashing scheme; it is also
// the PHC id segment of hashes that scheme produces. It is a defined type
// distinct from the other crypto-family Algorithm types (hash, mac, kdf, …), so
// the compiler rejects feeding a hash or KDF constant into a password call
// (V104) — the seven registries are separate keyspaces, and the type system now
// enforces that separation the way typed Format/Level discipline does elsewhere.
type Algorithm corecrypto.Algorithm

// PBKDF2SHA256 is PBKDF2-SHA256 — the stdlib password stretcher (RFC 8018).
const PBKDF2SHA256 Algorithm = "pbkdf2-sha256"

// Hash returns a PHC-string hash of password using the named scheme, with a
// fresh random salt and the scheme's current cost parameters. An unregistered
// algorithm returns UnknownPasswordAlgorithm. Store the returned string as-is.
func Hash(a Algorithm, password []byte) (phc string, err error) {
	//: convert the domain-typed Algorithm to the core key at the boundary.
	return corecrypto.HashPassword(corecrypto.Algorithm(a), password)
}
