// Package pbkdf2pw registers the "pbkdf2-sha256" password-hashing scheme
// (ADR 0013). Importing the package (typically a blank import via
// pkg/v1/password) self-registers the scheme so crypto.HashPassword /
// crypto.VerifyPassword resolve. It is stdlib-only (crypto/pbkdf2 + crypto/sha256
// + crypto/rand + crypto/subtle, all Go 1.26), so it pulls zero non-stdlib deps
// and is the default that keeps pkg/v1/password consumers dep-light.
//
// PBKDF2 is the stdlib password stretcher. argon2id (memory-hard, the OWASP
// first choice) lives under third-party/x-crypto as an opt-in scheme — import
// it explicitly when you can afford the x/crypto dependency. PBKDF2-SHA256 at
// 600 000 iterations is the OWASP-recommended fallback where argon2 is absent.
//
// Stored hashes use the PHC string format:
//
//	$pbkdf2-sha256$i=600000$<b64-salt>$<b64-digest>
package pbkdf2pw

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

const (
	// algorithm is the canonical registry key and PHC id segment.
	algorithm corecrypto.Algorithm = "pbkdf2-sha256"
	// saltLen is the random salt length in bytes (128-bit).
	saltLen int = 16
	// keyLen is the derived digest length in bytes (256-bit).
	keyLen int = 32
	// currentIters is the present iteration policy (OWASP 2023 for PBKDF2-SHA256).
	currentIters int = 600_000
	// maxIters caps the PHC-supplied iteration count accepted on Verify so a
	// hostile or corrupt stored hash cannot force unbounded CPU work. It sits
	// far above currentIters to leave ratchet headroom, yet stays bounded.
	maxIters int = 100_000_000
	// phcFields is the number of `$`-delimited fields in a well-formed hash.
	phcFields int = 5
	// fieldID indexes the scheme id after a split on "$" (index 0 is the empty
	// prefix before the first "$").
	fieldID int = 1
	// fieldCost indexes the "i=<n>" iteration-count field.
	fieldCost int = 2
	// fieldSalt indexes the base64 salt field.
	fieldSalt int = 3
	// fieldDigest indexes the base64 digest field.
	fieldDigest int = 4
)

// PasswordHasher is the registered PBKDF2-SHA256 scheme singleton (no init();
// package-level var initialiser, mirroring the codec/AEAD convention).
var PasswordHasher = corecrypto.RegisterPasswordHasher(pbkdf2PW{})

// pbkdf2PW implements core/crypto.PasswordHasher over crypto/pbkdf2 with SHA-256.
type pbkdf2PW struct{}

// Algorithm reports the canonical algorithm key (also the PHC id).
func (pbkdf2PW) Algorithm() corecrypto.Algorithm {
	//: the literal key consumers pass to HashPassword and the PHC id segment.
	return algorithm
}

// Hash draws a fresh salt and returns the PHC-string hash of password at the
// current iteration policy. The only realistic failure is a crypto/rand fault.
func (pbkdf2PW) Hash(password []byte) (phc string, err error) {
	//: a fresh per-hash salt — never reused across passwords.
	var salt [saltLen]byte
	//: fill it from crypto/rand; a fault here is a host entropy problem.
	if _, rerr := rand.Read(salt[:]); rerr != nil {
		//: wrap the cause as the typed PasswordHashFailed sentinel.
		return "", errs.Wrap(rerr, errs.WrapParams{
			Code:    corecrypto.CodePasswordHashFailed,
			Reason:  "PASSWORD_HASH_FAILED",
			Public:  "Could not gather entropy for password hashing",
			Private: "service/crypto/pbkdf2pw.Hash: crypto/rand.Read failed while generating a salt",
		})
	}
	//: derive the digest at the current iteration policy.
	digest, kerr := pbkdf2.Key(sha256.New, string(password), salt[:], currentIters, keyLen)
	//: PBKDF2 only errors on invalid params (unreachable with our constants).
	if kerr != nil {
		//: surface it as the typed hash failure rather than a raw error.
		return "", corecrypto.PasswordHashFailed
	}
	//: encode the self-describing PHC string for storage.
	return encodePHC(salt[:], digest, currentIters), nil
}

// Verify recomputes the digest with the stored salt + iteration count and
// compares in constant time. A malformed phc returns InvalidPasswordHash; a
// genuine mismatch is (false, nil).
func (pbkdf2PW) Verify(password []byte, phc string) (ok bool, err error) {
	//: decode the stored parameters; a parse failure is data corruption.
	iters, salt, want, decoded := decodePHC(phc)
	//: reject an unparseable stored hash before any work.
	if !decoded {
		//: server-side corruption, not a password mismatch.
		return false, corecrypto.InvalidPasswordHash
	}
	//: recompute with the SAME params + output length as the stored digest.
	got, kerr := pbkdf2.Key(sha256.New, string(password), salt, iters, len(want))
	//: a derivation error on stored params means the hash is malformed.
	if kerr != nil {
		//: treat as corruption, never as a mismatch.
		return false, corecrypto.InvalidPasswordHash
	}
	//: constant-time compare; a mismatch is (false, nil), never an oracle.
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// NeedsRehash reports whether phc was produced below the current iteration
// policy. A malformed phc reports false (Verify surfaces the real error).
func (pbkdf2PW) NeedsRehash(phc string) bool {
	//: read only the iteration count; salt/digest are irrelevant here.
	iters, _, _, decoded := decodePHC(phc)
	//: an unparseable hash is left untouched.
	if !decoded {
		//: nothing to upgrade.
		return false
	}
	//: a hash below the current policy should be re-hashed on next login.
	return iters < currentIters
}

// encodePHC renders the PHC string `$pbkdf2-sha256$i=<iters>$<b64salt>$<b64digest>`
// using un-padded base64 (the PHC convention).
func encodePHC(salt, digest []byte, iters int) string {
	//: un-padded base64 is the PHC standard for salt + digest fields.
	return fmt.Sprintf("$%s$i=%d$%s$%s", algorithm, iters,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(digest))
}

// decodePHC parses a `$pbkdf2-sha256$i=<n>$<b64salt>$<b64digest>` string. It
// returns ok=false for any field that is missing, mislabelled, or invalid.
func decodePHC(phc string) (iters int, salt, digest []byte, ok bool) {
	//: layout is ["", id, "i=N", b64salt, b64digest].
	fields := strings.Split(phc, "$")
	//: the field count + id must match this scheme exactly.
	if len(fields) != phcFields || fields[fieldID] != string(algorithm) {
		//: not a hash this scheme produced.
		return 0, nil, nil, false
	}
	//: the cost field must be "i=<n>".
	iterStr, found := strings.CutPrefix(fields[fieldCost], "i=")
	//: a missing cost label is malformed.
	if !found {
		//: reject.
		return 0, nil, nil, false
	}
	//: parse the iteration count; it must be a positive, bounded integer.
	n, perr := strconv.Atoi(iterStr)
	//: non-numeric, non-positive, or above the cap (unbounded-work DoS) is malformed.
	if perr != nil || n <= 0 || n > maxIters {
		//: reject.
		return 0, nil, nil, false
	}
	//: decode + length-check the salt and digest fields.
	saltRaw, digRaw, decoded := decodeSaltDigest(fields[fieldSalt], fields[fieldDigest])
	//: a bad base64 or off-spec length is corruption.
	if !decoded {
		//: reject.
		return 0, nil, nil, false
	}
	//: a fully validated PHC string.
	return n, saltRaw, digRaw, true
}

// decodeSaltDigest base64-decodes the salt + digest PHC fields and enforces the
// scheme's fixed lengths. It returns ok=false on a decode error or any length
// other than saltLen / keyLen: this scheme only ever emits a 16-byte salt +
// 32-byte digest, so a deviation is corruption — and pinning the digest length
// stops Verify from deriving an attacker-chosen, arbitrary-length key.
func decodeSaltDigest(saltField, digestField string) (salt, digest []byte, ok bool) {
	//: un-padded base64 is the PHC convention for both fields.
	saltRaw, serr := base64.RawStdEncoding.DecodeString(saltField)
	digRaw, derr := base64.RawStdEncoding.DecodeString(digestField)
	//: a decode failure or off-spec length is corruption — reject in one guard.
	if serr != nil || derr != nil || len(saltRaw) != saltLen || len(digRaw) != keyLen {
		//: reject.
		return nil, nil, false
	}
	//: both fields are valid and exactly the expected size.
	return saltRaw, digRaw, true
}
