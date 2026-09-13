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
	// LockFenceCorrupt is returned when the on-disk fence ledger cannot yield
	// the next token: it does not read as a decimal counter, or it reads as
	// the one counter that has no successor.
	//
	// Nothing is repaired and no lease is granted. Restarting the counter
	// would hand out numbers the protected resource has already accepted,
	// which turns the one mechanism that survives a stalled holder into a
	// mechanism that endorses one. A counter allowed to wrap does the same
	// thing, silently and in order.
	LockFenceCorrupt = errs.Define(CodeLockFenceCorrupt, "LOCK_FENCE_CORRUPT",
		"The lock's fencing ledger is not readable",
		"service/lock: the lock file's contents are not a decimal fencing counter, or are the one counter that cannot be advanced without wrapping; the fields name the path, the offending bytes' length and which of the two conditions it is",
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

	// LockPathRedirected is returned by the file locker when the lock path is
	// an indirection rather than a file: a symbolic link on Unix, a reparse
	// point on Windows.
	//
	// The substitution merges two locks into one or splits one into two, with
	// no error on any path: the victim's lock lands on the attacker's target,
	// the attacker's own lock lands on the real name, and both processes are
	// told they hold it. The fencing token goes with it — the ledger the
	// victim increments is the attacker's file, so the number handed to the
	// protected resource is a number the attacker chose.
	//
	// The directory rule does not reach this. [LockDirectoryUnsafe] refuses a
	// directory in which an existing entry can be UNLINKED by anyone; this
	// attack creates an entry at a name nobody has taken yet, which the sticky
	// bit permits and 0777|sticky — what /tmp is — explicitly accepts.
	LockPathRedirected = errs.Define(CodeLockPathRedirected, "LOCK_PATH_REDIRECTED",
		"The lock path is a link rather than a file",
		"service/lock: a symbolic link or reparse point occupies the lock file's path, so the lock and its fencing ledger would land on a file chosen by whoever planted it; the fields name the path, the kind of indirection and what the kernel reported",
		errs.WithExitCode(exitConfig))
)
