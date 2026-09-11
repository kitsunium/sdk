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
	// maxMem caps the PHC-supplied memory cost (KiB) accepted on Verify so a
	// hostile or corrupt stored hash cannot force an extreme allocation —
	// 2 GiB, far above any real policy yet bounded.
	maxMem int = 1 << 21
	// maxTime caps the PHC-supplied time (iteration) cost accepted on Verify.
	maxTime int = 1 << 20
	// maxThreads caps parallelism at the uint8 ceiling argon2 accepts; a value
	// above it would wrap on the uint8 cast (256 -> 0) and silently weaken the hash.
	maxThreads int = 255
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
	//: decode + length-check the salt and digest fields.
	saltRaw, digRaw, decoded := decodeSaltDigest(fields[fieldSalt], fields[fieldDigest])
	//: a bad base64 or off-spec length is corruption.
	if !decoded {
		//: reject.
		return 0, 0, 0, nil, nil, false
	}
	//: a fully validated PHC string.
	return memCost, timeCost, threads, saltRaw, digRaw, true
}

// decodeSaltDigest base64-decodes the salt + digest PHC fields and enforces the
// scheme's fixed lengths. It returns ok=false on a decode error or any length
// other than saltLen / keyLen: this scheme only ever emits a 16-byte salt +
// 32-byte digest, so a deviation is corruption — and pinning the digest length
// stops Verify from recomputing an attacker-chosen, arbitrary-length key.
func decodeSaltDigest(saltField, digestField string) (salt, digest []byte, ok bool) {
	//: un-padded base64 is the PHC convention for both fields.
	saltRaw, serr := base64.RawStdEncoding.DecodeString(saltField)
	digRaw, derr := base64.RawStdEncoding.DecodeString(digestField)
	//: a decode failure or off-spec length is corruption — reject in one guard.
	if serr != nil || derr != nil || len(saltRaw) != saltLen || len(digRaw) != int(keyLen) {
		//: reject.
		return nil, nil, false
	}
	//: both fields are valid and exactly the expected size.
	return saltRaw, digRaw, true
}

// parseParams parses the "m=<n>,t=<n>,p=<n>" argon2 cost field.
func parseParams(field string) (memCost, timeCost uint32, threads uint8, ok bool) {
	//: parse the fixed "m=,t=,p=" shape directly — no CSV split, so no
	//: empty-token ambiguity; Sscanf rejects any structural mismatch.
	var memVal, timeVal, parVal int
	count, serr := fmt.Sscanf(field, "m=%d,t=%d,p=%d", &memVal, &timeVal, &parVal)
	//: reject in one guard — Sscanf must consume all three, the costs must be
	//: in-bounds (validCosts), and the field must re-render exactly (the re-render
	//: rejects trailing junk Sscanf would ignore; short-circuit means the costly
	//: Sprintf only runs once the values are known-good).
	if serr != nil || count != paramCount || !validCosts(memVal, timeVal, parVal) ||
		field != fmt.Sprintf("m=%d,t=%d,p=%d", memVal, timeVal, parVal) {
		//: malformed, out-of-bounds, or non-canonical cost field.
		return 0, 0, 0, false
	}
	//: threads is a uint8; the casts are safe after validCosts bounded them.
	return uint32(memVal), uint32(timeVal), uint8(parVal), true
}

// validCosts reports whether the parsed argon2 m/t/p costs are positive and
// within their Verify-time bounds: p<=255 prevents the uint8 cast wrapping
// (256 -> 0), while maxMem bounds the ALLOCATION a hostile or corrupt PHC
// string can request.
//
// It does NOT bound the work, and the distinction is measured rather than
// assumed. argon2's cost is the PRODUCT m x t, while maxMem and maxTime are
// checked independently, so the pair (m=2 GiB, t=2^20) passes here. Scaling
// the 0.884 ms per MiB per pass measured in BENCH.md, one such stored string
// occupies a verification goroutine for WEEKS — 2.2 s and 2 GiB already at
// t=1. The allocation half of the old wording was true and tight; the work
// half did not follow from it.
//
// Left as it is on purpose: the caps are a compatibility surface, since
// narrowing them makes previously-verifiable stored hashes unverifiable, and
// reaching this path at all requires an attacker who can already write the
// credential store. The fix, if it is wanted, is a bound on the PRODUCT rather
// than a tighter cap on either factor.
func validCosts(memVal, timeVal, parVal int) bool {
	//: every axis must be positive AND under its cap.
	return memVal > 0 && timeVal > 0 && parVal > 0 &&
		memVal <= maxMem && timeVal <= maxTime && parVal <= maxThreads
}
