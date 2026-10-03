// Package lock — declares the sentinel *errs.Error outcomes of the domain: the
// port's, and those the concrete lockers emit. Each var's name equals its
// errs.Define Reason in SCREAMING_SNAKE form.
package lock

import "github.com/kitsunium/sdk/internal/kernel/errs"

// exitConfig matches sysexits EX_CONFIG (78). A locker refused at construction
// is a permanent configuration fault: the same arguments will be refused
// forever, and the fix is a code change at the call site, never a retry. An
// unsafe lock directory is one too — a deployment fault: the same path will be
// refused until someone changes it, and no retry helps.
const exitConfig int = 78

// exitDataErr matches sysexits EX_DATAERR (65). A rejected name is malformed
// input to the locker, not a broken locker and not a broken configuration; a
// corrupt fence ledger is bad data on disk, for the same reason.
const exitDataErr int = 65

var (
	// LockMisconfigured is returned by a locker CONSTRUCTOR whose
	// configuration it cannot honour.
	//
	// The load-bearing case is a non-positive lease TTL, and it is refused
	// rather than defaulted for a reason that does not apply to most policies:
	// the two natural readings of a zero TTL — "already expired" and "never
	// expires" — are exact opposites, and a locker built on the first one
	// still answers every call successfully while excluding nobody at all.
	// Nothing downstream can tell that apart from a lock nobody contends.
	//
	// Refusing at CONSTRUCTION rather than at the first Acquire is the other
	// half: it puts the failure where the program is wired, in the process's
	// first second, instead of in the middle of the first contended section.
	LockMisconfigured = errs.Define(CodeLockMisconfigured, "LOCK_MISCONFIGURED",
		"The lock cannot be built from the given configuration",
		"core/app/lock: a locker constructor received a TTL, a directory or a poll interval it cannot honour; the fields name the option and the value",
		errs.WithExitCode(exitConfig))

	// LockNotHeld is returned by Extend or Release when the caller's lease has
	// been taken over by another holder.
	//
	// From Extend it is the single most important error this domain can
	// produce: it is the SDK telling a caller that it is executing inside a
	// critical section someone else also believes they own. It must abort the
	// protected work, not log and continue — the lock cannot be re-taken
	// "quickly" to fix it, because the other holder is already inside.
	//
	// From Release it means the call deliberately released NOTHING. The
	// alternative — unlocking whatever currently sits under that name — would
	// have this caller end another caller's critical section on its way out.
	LockNotHeld = errs.Define(CodeLockNotHeld, "LOCK_NOT_HELD",
		"The lease is no longer held by this caller",
		"core/app/lock: Extend or Release ran against a lease whose lock has since been acquired by another holder; the fields name the lock and the fencing tokens involved")

	// LockBackendFailed is returned when the locker itself could not serve the
	// operation. A lock being HELD is not this — TryAcquire reports that as
	// (nil, false, nil).
	LockBackendFailed = errs.Define(CodeLockBackendFailed, "LOCK_BACKEND_FAILED",
		"The lock backend could not serve the operation",
		"core/app/lock: a locker failed for a reason of its own rather than reporting the lock as held; the fields name the operation and the lock")

	// LockNameRejected is returned for a name the locker refuses.
	//
	// The empty string is the case that matters, and it is refused because it
	// is what an uninitialised variable holds. Accepting it would funnel every
	// caller that forgot to set a name through one shared lock — correct-looking,
	// silent, and indistinguishable from contention nobody can explain.
	LockNameRejected = errs.Define(CodeLockNameRejected, "LOCK_NAME_REJECTED",
		"The lock name is not usable as given",
		"core/app/lock: Acquire or TryAcquire received an empty name or one the backend cannot represent; the fields name the offending part",
		errs.WithExitCode(exitDataErr))

	// The outcomes the concrete lockers emit — the in-process locker and the file
	// locker of internal/service/app/lock. They are declared here, with the
	// port's, so that the domain's codes and sentinels are in one place
	// (ADR 0160).

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
		"service/app/lock: the lock file's contents are not a decimal fencing counter, or are the one counter that cannot be advanced without wrapping; the fields name the path, the offending bytes' length and which of the two conditions it is",
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
		"service/app/lock: the directory is world-writable and not sticky, so its lock files can be replaced by any account; the fields name the path and the mode",
		errs.WithExitCode(exitConfig))

	// LockKeepaliveLost is the context CAUSE published by the Keepalive of
	// internal/service/app/lock when a renewal fails.
	//
	// It is deliberately not a return value. A keepalive exists to inform work
	// that has ALREADY STARTED, and the only channel that reaches such work is
	// its context — so the loss arrives as a cancellation the protected
	// operation is already selecting on, and `context.Cause` says why.
	LockKeepaliveLost = errs.Define(CodeLockKeepaliveLost, "LOCK_KEEPALIVE_LOST",
		"The lease could not be renewed and is no longer held",
		"service/app/lock: a background Extend failed, so the derived context was cancelled with this cause; the fields name the lock and the fencing token that was lost")

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
		"service/app/lock: a symbolic link or reparse point occupies the lock file's path, so the lock and its fencing ledger would land on a file chosen by whoever planted it; the fields name the path, the kind of indirection and what the kernel reported",
		errs.WithExitCode(exitConfig))

	// LockFileReplaced is returned when the file a lease holds is no longer
	// the file its name leads to.
	//
	// It is the one exposure of the file locker that is DETECTED rather than
	// prevented, and the doc comment on internal/service/app/lock's identity.go
	// says so at length. In a world-writable sticky directory the account that
	// created the lock file owns that entry, so it may unlink it — including
	// while another process holds the lock. No flag on the open stops that:
	// the descriptor outlives the name by design, on every kernel.
	//
	// What ships instead is that the holder finds out. Acquire refuses a lock
	// whose file was swapped between the open and the flock, and Extend
	// refuses once the swap has happened — so a Keepalive cancels the
	// protected work's context with this cause rather than letting it run on
	// inside a section it no longer owns.
	LockFileReplaced = errs.Define(CodeLockFileReplaced, "LOCK_FILE_REPLACED",
		"The lock file is no longer the file its name leads to",
		"service/app/lock: the lock file this lease holds was unlinked or replaced while it was held, so the next acquisition locks a different inode and a fresh fencing ledger; the fields name the path, the lock, the fencing token and which of the two conditions was observed",
		errs.WithExitCode(exitConfig))
)
