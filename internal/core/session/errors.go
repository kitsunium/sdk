// Package session — declares the sentinel *errs.Error port outcomes. Each
// var's name equals its errs.Define Reason in SCREAMING_SNAKE form.
//
// Every Public string here is written on the assumption that a third party
// reads it: none of them contains an identifier, a subject, a data key, a
// directory path, or a count. The identifier in particular is a bearer secret,
// so it never appears in a Public, in a Private, or in a Field.
package session

import "github.com/kitsunium/sdk/internal/kernel/errs"

// exitConfig matches sysexits EX_CONFIG (78). A refused store configuration is
// permanent: the same Config will be refused identically forever and the fix is
// an edit at the call site, never a retry.
const exitConfig int = 78

// exitTempFail matches sysexits EX_TEMPFAIL (75). A backend that could not be
// reached may be reachable on the next attempt, which is what distinguishes it
// from a configuration fault.
const exitTempFail int = 75

// httpUnauthorized is 401. Every verdict that means "this request carries no
// usable session" maps to it, so a framework can route the whole family with
// one errs.HTTPStatusOf call instead of a switch it has to keep in sync.
const httpUnauthorized int = 401

// httpUnavailable is 503 — the store, not the request, is the problem.
const httpUnavailable int = 503

var (
	// NotFound is returned by Load, and by Regenerate, for an identifier that
	// names no live session.
	NotFound = errs.Define(CodeNotFound, "NOT_FOUND",
		"No session matches that identifier",
		"core/session: no live record under the presented identifier's digest — never minted, destroyed, or swept",
		errs.WithHTTPStatus(httpUnauthorized))

	// Expired is returned when the record existed but its deadline had passed.
	// It is distinct from NotFound because the two need different operator
	// responses — a spike in Expired is a timeout that is too short, a spike in
	// NotFound is a store that is losing records. Telling them apart leaks
	// nothing usable: an identifier is 256 random bits, so an attacker cannot
	// reach either answer by guessing.
	Expired = errs.Define(CodeExpired, "EXPIRED",
		"That session has expired",
		"core/session: the record's effective deadline — the earlier of the idle and absolute deadlines — is in the past; the record is dropped as this is reported",
		errs.WithHTTPStatus(httpUnauthorized))

	// InvalidID is returned by ParseID and NewID for a malformed identifier,
	// and by any Store method handed the zero ID.
	InvalidID = errs.Define(CodeInvalidID, "INVALID_ID",
		"That is not a valid session identifier",
		"core/session: identifier must be exactly IDLen bytes in unpadded base64url; the decoder's own message is dropped because it can echo attacker input",
		errs.WithHTTPStatus(httpUnauthorized))

	// InvalidConfig is returned by every store constructor for a configuration
	// it cannot honour. ADR 0031: a zero timeout is refused at construction,
	// never accepted as "expires immediately" — a store where every session is
	// already dead is the inert-policy failure, and it would present as a
	// login loop nobody can debug.
	InvalidConfig = errs.Define(CodeInvalidConfig, "INVALID_CONFIG",
		"The session store configuration is not usable",
		"core/session: a timeout is non-positive, the idle window is not shorter than the absolute ceiling, or a required directory or key is missing; the fields name which",
		errs.WithExitCode(exitConfig))

	// IdentifierCollision is returned by New and Regenerate when the minted
	// identifier already names a live session. At 256 bits this cannot happen
	// by chance, so it means the entropy source is broken — and the honest
	// answer to a broken entropy source is to refuse, not to carry on with a
	// predictable identifier.
	IdentifierCollision = errs.Define(CodeIdentifierCollision, "IDENTIFIER_COLLISION",
		"The session store could not mint a unique identifier",
		"core/session: a freshly minted identifier already names a live record — at 256 bits this is a broken random source, not chance; nothing is overwritten")

	// EntropyFailed is returned when the random source could not be read.
	EntropyFailed = errs.Define(CodeEntropyFailed, "ENTROPY_FAILED",
		"The session store could not generate a secure identifier",
		"core/session: the random source returned an error or a short read; no identifier is minted from partial entropy")

	// StoreUnavailable is returned when the backend itself failed. It carries
	// EX_TEMPFAIL and 503 because retrying is meaningful, which is exactly what
	// separates it from InvalidConfig.
	StoreUnavailable = errs.Define(CodeStoreUnavailable, "STORE_UNAVAILABLE",
		"The session store is unavailable",
		"core/session: the backend could not be read or written; the fields name the operation, never the path or the identifier",
		errs.WithExitCode(exitTempFail), errs.WithHTTPStatus(httpUnavailable))

	// SealInvalid is returned by Sealer.Open for every failure, without
	// distinguishing them. One verdict for tampering, truncation, a wrong key
	// and a wrong purpose is what keeps Open from becoming an oracle — the same
	// posture crypto.DecryptionFailed takes for the AEAD it is built on.
	SealInvalid = errs.Define(CodeSealInvalid, "SEAL_INVALID",
		"That session value could not be opened",
		"core/session: sealed value failed to open — tampered, truncated, wrong key or wrong purpose, deliberately not distinguished",
		errs.WithHTTPStatus(httpUnauthorized))

	// FixationRefused is returned by Save when the value's subject differs from
	// the stored record's. Rebinding a principal onto an identifier that is
	// already in circulation IS session fixation, so the write is refused
	// loudly. Regenerate is the operation that was wanted: it mints a new
	// identifier, carries the data across, and destroys the old record.
	FixationRefused = errs.Define(CodeFixationRefused, "FIXATION_REFUSED",
		"A session's subject cannot be changed without a new identifier",
		"core/session: Save was given a subject the stored record does not have; call Regenerate, which rotates the identifier as it rebinds")
)
