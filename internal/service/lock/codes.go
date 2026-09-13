// Package lock — range 0.3.51.* (ADR 0052 service/lock block).
package lock

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.51.0 - 0.3.51.255

// CodeLockFenceCorrupt identifies a fence ledger no further token can be
// issued from: its contents are not a decimal counter (`condition=unparseable`)
// or they are the one counter with no successor (`condition=exhausted`).
//
// It is refused rather than repaired. "Repairing" it means restarting the
// counter, and a restarted fencing token is not a fencing token at all: the
// numbers it issues have already been seen by the protected resource, so the
// resource accepts a stale holder's write believing it to be current. A lock
// that cannot prove monotonicity says so instead of issuing numbers that look
// fine.
//
// The two conditions share a code because they share that remedy exactly: stop,
// and have a human look at the ledger. Splitting them would ask every caller to
// learn a second code for the same instruction.
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

// CodeLockPathRedirected identifies a lock path that is not a file but an
// indirection to one: a symbolic link on Unix, a reparse point on Windows.
//
// The lock filename is the SHA-256 of the lock name, which makes it
// derived from no caller-supplied string, and in the same stroke PREDICTABLE —
// and predictable is what the attack needs.
// An indirection planted there sends the flock and the fencing ledger to a
// file the attacker chose, so the victim's lock and the attacker's own lock
// cover different inodes while both report success: two processes inside one
// section, neither blocked, nothing logged. It also hands the attacker the
// fencing token, since the ledger the victim increments is the attacker's file.
//
// It is NOT the core's LOCK_BACKEND_FAILED. Nothing failed — a deliberate
// substitution succeeded, and reading it as a medium fault invites the one
// response that is wrong here, a retry. It is its own code for the same reason
// [CodeLockDirectoryUnsafe] is: the remedy is a human looking at the directory.
const CodeLockPathRedirected errs.Code = 0x00_03_33_04 // 0.3.51.4

// CodeLockFileReplaced identifies a held lock whose FILE is no longer the one
// its name leads to: the entry was unlinked, or replaced by a different file,
// while this holder still had it open and locked.
//
// The exclusion is gone at that moment, silently. The holder's flock still
// covers an inode nothing names, so the next process to acquire creates a
// fresh file, locks THAT, reads a fence ledger that starts again at zero, and
// is told it holds the lock — while the first holder is still inside the
// section believing the same thing. Measured: two holders, two inodes, both
// reporting fence 1.
//
// It is separate from [CodeLockDirectoryUnsafe] because it is a different
// question asked at a different time. That code refuses a directory at
// construction on its mode; this one reports a lock that WAS taken and is no
// longer exclusive, in a directory whose mode the rule accepts — 0777|sticky,
// which is what /tmp is, lets the entry's own owner unlink it.
//
// It is reported and never repaired. Re-acquiring would hand the caller a
// second lease over the new file while the old one is still locked, which is
// the split-brain spelled deliberately.
const CodeLockFileReplaced errs.Code = 0x00_03_33_05 // 0.3.51.5
