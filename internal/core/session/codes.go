// Package session — range 0.2.14.* (ADR 0045 core/session block).
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
