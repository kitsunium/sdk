// Package session — ranges 0.2.14.* (the port's verdicts) and 0.3.46.* (the
// engines' own refusals) — ADR 0045, declared here since ADR 0160.
package session

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.2.14.0 - 0.2.14.255

// CodeNotFound identifies a lookup for an identifier that names no live
// session — never minted, already destroyed, or dropped after expiring.
const CodeNotFound errs.Code = 0x00_02_0E_01 // 0.2.14.1

// CodeExpired identifies a session that was found but whose deadline had
// already passed. The record is dropped as it is reported, so the same
// identifier answers CodeNotFound on the next call.
const CodeExpired errs.Code = 0x00_02_0E_02 // 0.2.14.2

// CodeInvalidID identifies an identifier that is not well formed: the wrong
// length, not unpadded base64url, or the zero value.
const CodeInvalidID errs.Code = 0x00_02_0E_03 // 0.2.14.3

// CodeInvalidConfig identifies a store configuration that could never work —
// a non-positive timeout, an idle window that the absolute ceiling makes
// unreachable, a missing directory or key (ADR 0031).
const CodeInvalidConfig errs.Code = 0x00_02_0E_04 // 0.2.14.4

// CodeIdentifierCollision identifies a freshly minted identifier that already
// names a live session. It means the entropy source is broken, and it is
// refused rather than reused: reusing it would hand one caller's session to
// another, and would silently disable the rotation Regenerate exists for.
const CodeIdentifierCollision errs.Code = 0x00_02_0E_05 // 0.2.14.5

// CodeEntropyFailed identifies a failure to read the random bytes an
// identifier is made of. No identifier is minted from a partial read.
const CodeEntropyFailed errs.Code = 0x00_02_0E_06 // 0.2.14.6

// CodeStoreUnavailable identifies a backend that could not be read or written:
// a permission error, a full disk, a vanished directory.
const CodeStoreUnavailable errs.Code = 0x00_02_0E_07 // 0.2.14.7

// CodeSealInvalid identifies a sealed value that did not open. It is the single
// non-oracle verdict for every cause — tampering, truncation, the wrong key,
// the wrong purpose — so a forger learns nothing from which one they hit.
const CodeSealInvalid errs.Code = 0x00_02_0E_08 // 0.2.14.8

// CodeFixationRefused identifies a Save that would have bound a different
// subject to an existing identifier. Changing principal without rotating the
// identifier is session fixation, so the write is refused rather than applied
// or silently reduced to a data-only write.
const CodeFixationRefused errs.Code = 0x00_02_0E_09 // 0.2.14.9

// range: 0.3.46.0 - 0.3.46.255
//
// The refusals specific to the engines in internal/service/security/session —
// the file store's location and lock, the payload caps, the sealer's purpose.
// The range was allocated in the service layer (LL = 3) and is declared here,
// beside the domain's verdicts, since ADR 0160: LL records the layer that
// allocated a range, not the directory its declaration lives in, so the values
// never change.

// CodeRecordCorrupt identifies a stored record that could not be turned back
// into a session: the seal did not open, the frame did not parse, or the
// record named a digest other than the one it was filed under.
const CodeRecordCorrupt errs.Code = 0x00_03_2E_01 // 0.3.46.1

// CodeDirectoryUnsafe identifies a location that cannot hold a session record
// safely — a directory readable beyond its owner, or a filesystem that accepts
// a 0600 request and does not enforce it.
const CodeDirectoryUnsafe errs.Code = 0x00_03_2E_02 // 0.3.46.2

// CodeLockFailed identifies a failure to take the store-wide exclusive lock.
// The operation is refused rather than attempted unserialised.
const CodeLockFailed errs.Code = 0x00_03_2E_03 // 0.3.46.3

// CodePayloadTooLarge identifies a session payload above the store's key-count
// or per-string caps, or a subject above the per-string cap. The bound is
// checked before the write it would fund.
const CodePayloadTooLarge errs.Code = 0x00_03_2E_04 // 0.3.46.4

// CodeInvalidPurpose identifies a sealer built without a purpose string, which
// would silently drop the domain separation between two things sealed under one
// key.
const CodeInvalidPurpose errs.Code = 0x00_03_2E_05 // 0.3.46.5

// CodePathRedirected identifies a store location reached through an
// indirection the file store refuses to follow: a link at the lock file's
// name, a link at a component of Dir planted where any account could have
// planted it, or a Dir that stopped naming the directory the store opened.
const CodePathRedirected errs.Code = 0x00_03_2E_06 // 0.3.46.6
