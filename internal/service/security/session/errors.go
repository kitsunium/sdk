// Package session — declares the sentinel *errs.Error store outcomes. Each
// var's name equals its errs.Define Reason in SCREAMING_SNAKE form.
//
// As in core/security/session, no Public string here names a path, an identifier, a
// subject or a data key: a Public is read by third parties, and everything a
// session store handles is either a secret or somebody's personal data.
package session

import "github.com/kitsunium/sdk/internal/kernel/errs"

// exitConfig matches sysexits EX_CONFIG (78). A refused location or a refused
// purpose is a permanent fault: the same call will be refused identically, and
// the fix is an edit.
const exitConfig int = 78

// exitTempFail matches sysexits EX_TEMPFAIL (75). A lock that could not be
// taken may be free on the next attempt.
const exitTempFail int = 75

// exitDataErr matches sysexits EX_DATAERR (65). A record that does not decode
// is bad data, not a bad program and not a transient fault.
const exitDataErr int = 65

// httpUnauthorized is 401 — the request carries no usable session.
const httpUnauthorized int = 401

// httpPayloadTooLarge is 413 — the caller asked to store more than the store
// will hold.
const httpPayloadTooLarge int = 413

var (
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
	// single verdict for every record this store did not write, so a planted
	// link reads exactly like any other tampering.
	PathRedirected = errs.Define(CodePathRedirected, "PATH_REDIRECTED",
		"The session store location is reached through a link",
		"service/security/session: an indirection the store refuses — a link at the lock file, a link at a component of Dir planted where any account could, or Dir swapped while opening; the fields name the component, the kind and the target",
		errs.WithExitCode(exitConfig))
)
