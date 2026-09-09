// Package lock — declares the sentinel *errs.Error values the concrete lockers
// emit. Each var's name equals its errs.Define Reason in SCREAMING_SNAKE form.
package lock

import "github.com/kitsunium/sdk/internal/kernel/errs"

// exitDataErr matches sysexits EX_DATAERR (65). A corrupt fence ledger is bad
// data on disk, not a broken program and not a broken configuration.
const exitDataErr int = 65

// exitConfig matches sysexits EX_CONFIG (78). An unsafe directory is a
// deployment fault: the same path will be refused until someone changes its
// mode, and no retry helps.
const exitConfig int = 78

var (
	// LockFenceCorrupt is returned when the on-disk fence ledger cannot be
	// read as a decimal counter.
	//
	// Nothing is repaired and no lease is granted. Restarting the counter
	// would hand out numbers the protected resource has already accepted,
	// which turns the one mechanism that survives a stalled holder into a
	// mechanism that endorses one.
	LockFenceCorrupt = errs.Define(CodeLockFenceCorrupt, "LOCK_FENCE_CORRUPT",
		"The lock's fencing ledger is not readable",
		"service/lock: the lock file's contents are not a decimal fencing counter; the fields name the path and the offending bytes' length",
		errs.WithExitCode(exitDataErr))

	// LockDirectoryUnsafe is returned by NewFileLocker for a directory that is
	// world-writable without the sticky bit.
	//
	// Such a directory lets any account unlink the lock file and create a new
	// one in its place. The next process flocks a different inode from the one
	// the current holder is flocking, both are told they hold the lock, and
	// neither can observe the other — the exact failure this domain exists to
	// prevent, reachable without any race at all.
	LockDirectoryUnsafe = errs.Define(CodeLockDirectoryUnsafe, "LOCK_DIRECTORY_UNSAFE",
		"The lock directory's permissions do not protect the lock files",
		"service/lock: the directory is world-writable and not sticky, so its lock files can be replaced by any account; the fields name the path and the mode",
		errs.WithExitCode(exitConfig))

	// LockKeepaliveLost is the context CAUSE published by [Keepalive] when a
	// renewal fails.
	//
	// It is deliberately not a return value. A keepalive exists to inform work
	// that has ALREADY STARTED, and the only channel that reaches such work is
	// its context — so the loss arrives as a cancellation the protected
	// operation is already selecting on, and `context.Cause` says why.
	LockKeepaliveLost = errs.Define(CodeLockKeepaliveLost, "LOCK_KEEPALIVE_LOST",
		"The lease could not be renewed and is no longer held",
		"service/lock: a background Extend failed, so the derived context was cancelled with this cause; the fields name the lock and the fencing token that was lost")
)
