// Package lock — range 0.3.51.* (ADR 0052 service/lock block).
package lock

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.51.0 - 0.3.51.255

// CodeLockFenceCorrupt identifies a fence ledger whose contents are not a
// decimal counter.
//
// It is refused rather than repaired. "Repairing" it means restarting the
// counter, and a restarted fencing token is not a fencing token at all: the
// numbers it issues have already been seen by the protected resource, so the
// resource accepts a stale holder's write believing it to be current. A lock
// that cannot prove monotonicity says so instead of issuing numbers that look
// fine.
const CodeLockFenceCorrupt errs.Code = 0x00_03_33_01 // 0.3.51.1

// CodeLockDirectoryUnsafe identifies a lock directory that is world-writable
// without the sticky bit.
//
// The exposure is not the counter — it is the inode. Anyone able to unlink the
// lock file can replace it with a fresh one, after which a new process flocks
// the NEW inode while the old holder still flocks the old one, and the two
// hold "the same" lock simultaneously. The sticky bit is what makes a shared
// directory such as /tmp safe, so it is accepted; its absence is not.
const CodeLockDirectoryUnsafe errs.Code = 0x00_03_33_02 // 0.3.51.2

// CodeLockKeepaliveLost identifies a background renewal that failed.
//
// It marks the point at which the caller stopped holding the lock while still
// running inside the section it protects. It is carried as a context CAUSE
// rather than a return value, because the whole purpose of a keepalive is to
// tell work that is already in progress — and the only channel that reaches
// in-progress work is its context.
const CodeLockKeepaliveLost errs.Code = 0x00_03_33_03 // 0.3.51.3
