// Package argon2id registers the "argon2id" password-hashing scheme (ADR 0013).
// It is the memory-hard, OWASP-first-choice password hash — but it depends on
// golang.org/x/crypto, so it lives under third-party/x-crypto as an OPT-IN
// scheme (mirroring xchacha vs the stdlib aesgcm default). Blank-import this
// package to register it, then crypto.HashPassword("argon2id", …) resolves;
// crypto.VerifyPassword already verifies any registered scheme's stored hashes.
//
// Stored hashes use the standard argon2 PHC string:
//
//	$argon2id$v=19$m=19456,t=2,p=1$<b64-salt>$<b64-digest>
package argon2id

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strings"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"golang.org/x/crypto/argon2"
)

const (
	// algorithm is the canonical registry key and PHC id segment.
	algorithm corecrypto.Algorithm = "argon2id"
	// argonVersion is the argon2 version embedded as "v=19" (0x13), the only
	// version golang.org/x/crypto/argon2 implements.
	argonVersion int = 19
	// currentMem is the memory cost in KiB (OWASP 2023: 19 MiB).
	currentMem uint32 = 19456
	// currentTime is the iteration (time) cost (OWASP 2023).
	currentTime uint32 = 2
	// currentThreads is the parallelism degree (OWASP 2023).
	currentThreads uint8 = 1
	// saltLen is the random salt length in bytes (128-bit).
	saltLen int = 16
	// keyLen is the derived digest length in bytes (256-bit).
	keyLen uint32 = 32
	// phcFields is the field count of a well-formed argon2id PHC string.
	phcFields int = 6
	// fieldID indexes the scheme id after a split on "$" (0 is the empty prefix).
	fieldID int = 1
	// fieldVersion indexes the "v=19" version field.
	fieldVersion int = 2
	// fieldParams indexes the "m=…,t=…,p=…" cost field.
	fieldParams int = 3
	// fieldSalt indexes the base64 salt field.
	fieldSalt int = 4
	// fieldDigest indexes the base64 digest field.
	fieldDigest int = 5
	// paramCount is the number of cost parameters (m, t, p) parsed from the field.
	paramCount int = 3
)

// PasswordHasher is the registered argon2id scheme singleton (no init();
// package-level var initialiser, mirroring the codec/AEAD convention).
var PasswordHasher = corecrypto.RegisterPasswordHasher(argon2idPW{})

// argon2idPW implements core/crypto.PasswordHasher over golang.org/x/crypto.
type argon2idPW struct{}

// Algorithm reports the canonical algorithm key (also the PHC id).
func (argon2idPW) Algorithm() corecrypto.Algorithm {
	//: the literal key consumers pass to HashPassword and the PHC id segment.
	return algorithm
}

// Hash draws a fresh salt and returns the PHC-string hash of password at the
// current cost policy. The only realistic failure is a crypto/rand fault.
func (argon2idPW) Hash(password []byte) (phc string, err error) {
	//: a fresh per-hash salt — never reused across passwords.
	var salt [saltLen]byte
	//: fill it from crypto/rand; a fault here is a host entropy problem.
	if _, rerr := rand.Read(salt[:]); rerr != nil {
		//: wrap the cause as the typed PasswordHashFailed sentinel.
		return "", errs.Wrap(rerr, errs.WrapParams{
			Code:    corecrypto.CodePasswordHashFailed,
			Reason:  "PASSWORD_HASH_FAILED",
			Public:  "Could not gather entropy for password hashing",
			Private: "third-party/x-crypto/argon2id.Hash: crypto/rand.Read failed while generating a salt",
		})
	}
	//: derive the digest at the current memory/time/parallelism policy.
	digest := argon2.IDKey(password, salt[:], currentTime, currentMem, currentThreads, keyLen)
	//: encode the self-describing PHC string for storage.
	return encodePHC(salt[:], digest, currentMem, currentTime, currentThreads), nil
}

// Verify recomputes the digest with the stored salt + cost parameters and
// compares in constant time. A malformed phc returns InvalidPasswordHash; a
// genuine mismatch is (false, nil).
func (argon2idPW) Verify(password []byte, phc string) (ok bool, err error) {
	//: decode the stored parameters; a parse failure is data corruption.
	memCost, timeCost, threads, salt, want, decoded := decodePHC(phc)
	//: reject an unparseable stored hash before any work.
	if !decoded {
		//: server-side corruption, not a password mismatch.
		return false, corecrypto.InvalidPasswordHash
	}
	//: recompute with the SAME params + output length as the stored digest.
	got := argon2.IDKey(password, salt, timeCost, memCost, threads, uint32(len(want)))
	//: constant-time compare; a mismatch is (false, nil), never an oracle.
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// NeedsRehash reports whether phc was produced below the current cost policy on
// any parameter. A malformed phc reports false (Verify surfaces the real error).
func (argon2idPW) NeedsRehash(phc string) bool {
	//: read the cost parameters; salt/digest are irrelevant here.
	memCost, timeCost, threads, _, _, decoded := decodePHC(phc)
	//: an unparseable hash is left untouched.
	if !decoded {
		//: nothing to upgrade.
		return false
	}
	//: weaker on ANY axis (memory, time, parallelism) means re-hash on next login.
	return memCost < currentMem || timeCost < currentTime || threads < currentThreads
}

// encodePHC renders the standard argon2id PHC string with un-padded base64.
func encodePHC(salt, digest []byte, memCost, timeCost uint32, threads uint8) string {
	//: un-padded base64 is the PHC standard for salt + digest fields.
	return fmt.Sprintf("$%s$v=%d$m=%d,t=%d,p=%d$%s$%s", algorithm, argonVersion,
		memCost, timeCost, threads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(digest))
}

// decodePHC parses an argon2id PHC string. It returns ok=false for any field
// that is missing, mislabelled, the wrong version, or invalid base64.
func decodePHC(phc string) (memCost, timeCost uint32, threads uint8, salt, digest []byte, ok bool) {
	//: layout is ["", id, "v=19", "m=…,t=…,p=…", b64salt, b64digest].
	fields := strings.Split(phc, "$")
	//: the field count, id, and version must all match this scheme.
	if len(fields) != phcFields || fields[fieldID] != string(algorithm) || fields[fieldVersion] != fmt.Sprintf("v=%d", argonVersion) {
		//: not a hash this scheme produced.
		return 0, 0, 0, nil, nil, false
	}
	//: parse the comma-separated m/t/p cost parameters.
	memCost, timeCost, threads, paramsOK := parseParams(fields[fieldParams])
	//: a malformed cost field is corruption.
	if !paramsOK {
		//: reject.
		return 0, 0, 0, nil, nil, false
	}
	//: decode the salt + digest from un-padded base64.
	saltRaw, serr := base64.RawStdEncoding.DecodeString(fields[fieldSalt])
	digRaw, derr := base64.RawStdEncoding.DecodeString(fields[fieldDigest])
	//: either decode failing means the stored hash is corrupt.
	if serr != nil || derr != nil {
		//: reject.
		return 0, 0, 0, nil, nil, false
	}
	//: a fully validated PHC string.
	return memCost, timeCost, threads, saltRaw, digRaw, true
}

// parseParams parses the "m=<n>,t=<n>,p=<n>" argon2 cost field.
func parseParams(field string) (memCost, timeCost uint32, threads uint8, ok bool) {
	//: parse the fixed "m=,t=,p=" shape directly — no CSV split, so no
	//: empty-token ambiguity; Sscanf rejects any structural mismatch.
	var memVal, timeVal, parVal int
	count, serr := fmt.Sscanf(field, "m=%d,t=%d,p=%d", &memVal, &timeVal, &parVal)
	//: reject in one guard — all three must parse, be positive, and re-render to
	//: the exact field (the re-render rejects trailing junk Sscanf would ignore;
	//: short-circuit means it only runs once the values are known-good).
	if serr != nil || count != paramCount || memVal <= 0 || timeVal <= 0 || parVal <= 0 ||
		field != fmt.Sprintf("m=%d,t=%d,p=%d", memVal, timeVal, parVal) {
		//: malformed, non-positive, or non-canonical cost field.
		return 0, 0, 0, false
	}
	//: threads is a uint8; the casts are safe after the positive-range check.
	return uint32(memVal), uint32(timeVal), uint8(parVal), true
}
