// Package session — declares the sentinel *errs.Error outcomes: the verdicts
// of the port, and the refusals specific to the engines in
// internal/service/security/session (ADR 0160). Each var's name equals its
// errs.Define Reason in SCREAMING_SNAKE form.
//
// Every Public string here is written on the assumption that a third party
// reads it: none of them contains an identifier, a subject, a data key, a
// directory path, or a count. The identifier in particular is a bearer secret,
// so it never appears in a Public, in a Private, or in a Field.
package session

import "github.com/kitsunium/sdk/internal/kernel/errs"

// exitConfig matches sysexits EX_CONFIG (78). A refused store configuration —
// and a refused location or purpose — is permanent: the same call will be
// refused identically forever and the fix is an edit, never a retry.
const exitConfig int = 78

// exitTempFail matches sysexits EX_TEMPFAIL (75). A backend that could not be
// reached may be reachable on the next attempt, and a store-wide lock that
// could not be taken may be free on it, which is what distinguishes both from
// a configuration fault.
const exitTempFail int = 75

// httpUnauthorized is 401. Every verdict that means "this request carries no
// usable session" maps to it, so a framework can route the whole family with
// one errs.HTTPStatusOf call instead of a switch it has to keep in sync.
const httpUnauthorized int = 401

// httpUnavailable is 503 — the store, not the request, is the problem.
const httpUnavailable int = 503

// exitDataErr matches sysexits EX_DATAERR (65). A record that does not decode
// is bad data, not a bad program and not a transient fault.
const exitDataErr int = 65

// httpPayloadTooLarge is 413 — the caller asked to store more than the store
// will hold.
const httpPayloadTooLarge int = 413

var (
	// NotFound is returned by Load, and by Regenerate, for an identifier that
	// names no live session.
	NotFound = errs.Define(CodeNotFound, "NOT_FOUND",
		"No session matches that identifier",
		"core/security/session: no live record under the presented identifier's digest — never minted, destroyed, or swept",
		errs.WithHTTPStatus(httpUnauthorized))

	// Expired is returned when the record existed but its deadline had passed.
	// It is distinct from NotFound because the two need different operator
	// responses — a spike in Expired is a timeout that is too short, a spike in
	// NotFound is a store that is losing records. Telling them apart leaks
	// nothing usable: an identifier is 256 random bits, so an attacker cannot
	// reach either answer by guessing.
	Expired = errs.Define(CodeExpired, "EXPIRED",
		"That session has expired",
		"core/security/session: the record's effective deadline — the earlier of the idle and absolute deadlines — is in the past; the record is dropped as this is reported",
		errs.WithHTTPStatus(httpUnauthorized))

	// InvalidID is returned by ParseID and NewID for a malformed identifier,
	// and by any Store method handed the zero ID.
	InvalidID = errs.Define(CodeInvalidID, "INVALID_ID",
		"That is not a valid session identifier",
		"core/security/session: identifier must be exactly IDLen bytes in unpadded base64url; the decoder's own message is dropped because it can echo attacker input",
		errs.WithHTTPStatus(httpUnauthorized))

	// InvalidConfig is returned by every store constructor for a configuration
	// it cannot honour. ADR 0031: a zero timeout is refused at construction,
	// never accepted as "expires immediately" — a store where every session is
	// already dead is the inert-policy failure, and it would present as a
	// login loop nobody can debug.
	InvalidConfig = errs.Define(CodeInvalidConfig, "INVALID_CONFIG",
		"The session store configuration is not usable",
		"core/security/session: a timeout is non-positive, the idle window is not shorter than the absolute ceiling, or a required directory or key is missing; the fields name which",
		errs.WithExitCode(exitConfig))

	// IdentifierCollision is returned by New and Regenerate when the minted
	// identifier already names a live session. At 256 bits this cannot happen
	// by chance, so it means the entropy source is broken — and the honest
	// answer to a broken entropy source is to refuse, not to carry on with a
	// predictable identifier.
	IdentifierCollision = errs.Define(CodeIdentifierCollision, "IDENTIFIER_COLLISION",
		"The session store could not mint a unique identifier",
		"core/security/session: a freshly minted identifier already names a live record — at 256 bits this is a broken random source, not chance; nothing is overwritten")

	// EntropyFailed is returned when the random source could not be read.
	EntropyFailed = errs.Define(CodeEntropyFailed, "ENTROPY_FAILED",
		"The session store could not generate a secure identifier",
		"core/security/session: the random source returned an error or a short read; no identifier is minted from partial entropy")

	// StoreUnavailable is returned when the backend itself failed. It carries
	// EX_TEMPFAIL and 503 because retrying is meaningful, which is exactly what
	// separates it from InvalidConfig.
	StoreUnavailable = errs.Define(CodeStoreUnavailable, "STORE_UNAVAILABLE",
		"The session store is unavailable",
		"core/security/session: the backend could not be read or written; the fields name the operation, never the path or the identifier",
		errs.WithExitCode(exitTempFail), errs.WithHTTPStatus(httpUnavailable))

	// SealInvalid is returned by Sealer.Open for every failure, without
	// distinguishing them. One verdict for tampering, truncation, a wrong key
	// and a wrong purpose is what keeps Open from becoming an oracle — the same
	// posture crypto.DecryptionFailed takes for the AEAD it is built on.
	SealInvalid = errs.Define(CodeSealInvalid, "SEAL_INVALID",
		"That session value could not be opened",
		"core/security/session: sealed value failed to open — tampered, truncated, wrong key or wrong purpose, deliberately not distinguished",
		errs.WithHTTPStatus(httpUnauthorized))

	// FixationRefused is returned by Save when the value's subject differs from
	// the stored record's. Rebinding a principal onto an identifier that is
	// already in circulation IS session fixation, so the write is refused
	// loudly. Regenerate is the operation that was wanted: it mints a new
	// identifier, carries the data across, and destroys the old record.
	FixationRefused = errs.Define(CodeFixationRefused, "FIXATION_REFUSED",
		"A session's subject cannot be changed without a new identifier",
		"core/security/session: Save was given a subject the stored record does not have; call Regenerate, which rotates the identifier as it rebinds")

	// The engines' own refusals (0.3.46.*). They are raised by
	// internal/service/security/session — their Private names that package, the
	// one that raises them — and declared here since ADR 0160, so every code of
	// the domain is in one place. As above, no Public string names a path, an
	// identifier, a subject or a data key: everything a session store handles
	// is either a secret or somebody's personal data.

	// RecordCorrupt is returned when a stored record cannot be read back. It
	// is deliberately ONE verdict for four causes — a tampered file, a
	// truncated file, the wrong store key, and a file moved onto another
	// session's digest — because distinguishing them would tell whoever
	// arranged the tampering which half of the attempt already worked.
	//
	// It maps to 401, not 500: whatever happened to the file, the caller's
	// session is not usable, and that is what the request needs to know.
	RecordCorrupt = errs.Define(CodeRecordCorrupt, "RECORD_CORRUPT",
		"That session could not be read",
		"service/security/session: record failed to open or decode — tampered, truncated, wrong key, or filed under another digest; deliberately not distinguished",
		errs.WithExitCode(exitDataErr), errs.WithHTTPStatus(httpUnauthorized))

	// DirectoryUnsafe is returned by NewFileStore for a location that cannot
	// hold a session record safely, and during a write for a filesystem that
	// accepted a 0600 request without enforcing it.
	//
	// It refuses rather than repairing. A chmod would hide how long the records
	// were exposed, and on the filesystems this check exists to catch it would
	// report success while changing nothing — a repair that cannot be verified
	// is not a repair.
	DirectoryUnsafe = errs.Define(CodeDirectoryUnsafe, "DIRECTORY_UNSAFE",
		"The session store location is not private enough to use",
		"service/security/session: the directory or a record carries a group or world permission bit; refused rather than chmod'ed, and the fields carry the mode that was required",
		errs.WithExitCode(exitConfig))

	// LockFailed is returned when the store-wide exclusive lock could not be
	// taken. The operation is refused rather than run unserialised: without the
	// lock the store cannot promise that a read-modify-write is indivisible,
	// and quietly dropping that promise is worse than failing the request.
	LockFailed = errs.Define(CodeLockFailed, "LOCK_FAILED",
		"The session store is busy",
		"service/security/session: flock on the store-wide lock descriptor failed; the operation was refused rather than run without serialisation",
		errs.WithExitCode(exitTempFail))

	// PayloadTooLarge is returned by Save for a payload above the store's caps,
	// and by Regenerate — in both stores, before anything is minted — for a
	// subject longer than the per-string cap, which the file store's frame
	// could not read back. A session store holds per-user state on the request
	// path; an unbounded payload there is a memory-exhaustion vector with a
	// very cheap trigger, so the bound is a refusal rather than a truncation —
	// silently dropping keys would report success for a write that did not
	// happen, and a truncated subject would name a different principal.
	PayloadTooLarge = errs.Define(CodePayloadTooLarge, "PAYLOAD_TOO_LARGE",
		"That session holds more data than the store accepts",
		"service/security/session: payload or subject exceeds the key-count or per-string cap; the offending key or subject is NOT named because it is caller data",
		errs.WithHTTPStatus(httpPayloadTooLarge))

	// InvalidPurpose is returned by NewSealer for an empty purpose. The purpose
	// is the domain separator between two things sealed under one key; reading
	// "" as "no separation needed" would be an inert policy in ADR 0031's exact
	// sense, and there is no default the SDK could invent that would mean
	// anything.
	InvalidPurpose = errs.Define(CodeInvalidPurpose, "INVALID_PURPOSE",
		"A sealer needs a non-empty purpose",
		"service/security/session: NewSealer needs a purpose string to bind as additional authenticated data; an empty one would drop domain separation silently",
		errs.WithExitCode(exitConfig))

	// PathRedirected is returned by NewFileStore when the store's location is
	// reached through an indirection it refuses to follow.
	//
	// Three shapes, one remedy — a human looks at the directory, and no retry
	// helps, which is why it is not StoreUnavailable: a symbolic link at the
	// lock file's name, which would put the store-wide lock — and, when it
	// dangles, a file the store creates — wherever the link points; a link at
	// a component of Dir planted in a directory any account can write, which
	// would move every record into a tree its planter chose; and a Dir that no
	// longer names the directory the store opened, because it was swapped
	// between the check and the open.
	//
	// A link at a RECORD's name is not this: that one is RecordCorrupt, the
	// single verdict for every record the store did not write, so a planted
	// link reads exactly like any other tampering.
	PathRedirected = errs.Define(CodePathRedirected, "PATH_REDIRECTED",
		"The session store location is reached through a link",
		"service/security/session: an indirection the store refuses — a link at the lock file, a link at a component of Dir planted where any account could, or Dir swapped while opening; the fields name the component, the kind and the target",
		errs.WithExitCode(exitConfig))
)
