//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/password .

// Package password is the password-storage facade: slow, salted, self-describing
// hashes with transparent upgrade-on-verify.
//
//	phc, _   := password.Hash(password.PBKDF2SHA256, []byte(pw))   // store phc
//	ok, _    := password.Verify([]byte(pw), phc)                   // login check
//	if ok && password.NeedsRehash(phc) {                           // policy grew?
//	    phc, _ = password.Hash(password.PBKDF2SHA256, []byte(pw))  // re-store
//	}
//
// [Verify] and [NeedsRehash] read the scheme from the stored PHC string, so no
// algorithm argument is needed — the hash fully describes how to check it.
//
// # This is the ONLY surface for human passwords
//
// Password hashing is deliberately SLOW and salted. Never run a raw [hash.Sum]
// or [kdf.Subkey] over a password — those are fast and assume high entropy. This
// facade is the one place a low-entropy human secret belongs. [Verify] compares
// in constant time; a mismatch is (false, nil), never an error.
//
// # Algorithms
//
// Importing this package activates PBKDF2-SHA256 with zero non-stdlib deps:
//
//   - [PBKDF2SHA256] — PBKDF2-SHA256 at 600 000 iterations (OWASP fallback).
//
// argon2id (memory-hard, the OWASP first choice) is an opt-in scheme under
// third-party/x-crypto — blank-import it when you can take the x/crypto dep, then
// hash with its Algorithm; Verify still works on either scheme's stored hashes.
//
// # Stable algorithm strings
//
// The [Algorithm] constants are frozen post-v1.0.0; the PHC id segment equals
// the Algorithm, so stored hashes stay verifiable across releases.
package password

import (
	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"

	// Activates the stdlib PBKDF2-SHA256 hasher. Stdlib-only, so importing
	// pkg/v1/password pulls zero non-stdlib dependencies.
	_ "github.com/kitsunium/sdk/internal/service/crypto/pbkdf2pw"
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

// Verify reports whether password matches the stored PHC hash, comparing in
// constant time. The scheme is read from phc. A malformed phc or unregistered
// scheme returns an error; a genuine mismatch is (false, nil).
func Verify(password []byte, phc string) (ok bool, err error) {
	//: delegate to the core registry dispatcher (scheme read from the PHC id).
	return corecrypto.VerifyPassword(password, phc)
}

// NeedsRehash reports whether the stored PHC hash was produced with cost
// parameters weaker than its scheme's current policy — call it after a
// successful Verify to transparently upgrade the stored hash.
func NeedsRehash(phc string) bool {
	//: delegate to the core registry dispatcher.
	return corecrypto.NeedsRehash(phc)
}
