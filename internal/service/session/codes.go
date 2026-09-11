// Package session — range 0.3.46.* (ADR 0045 service/session block).
package session

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.46.0 - 0.3.46.255

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
