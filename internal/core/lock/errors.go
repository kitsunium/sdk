// Package lock — declares the sentinel *errs.Error port outcomes. Each var's
// name equals its errs.Define Reason in SCREAMING_SNAKE form.
package lock

import "github.com/kitsunium/sdk/internal/kernel/errs"

// exitConfig matches sysexits EX_CONFIG (78). A locker refused at construction
// is a permanent configuration fault: the same arguments will be refused
// forever, and the fix is a code change at the call site, never a retry.
const exitConfig int = 78

// exitDataErr matches sysexits EX_DATAERR (65). A rejected name is malformed
// input to the locker, not a broken locker and not a broken configuration.
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
		"core/lock: a locker constructor received a TTL, a directory or a poll interval it cannot honour; the fields name the option and the value",
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
		"core/lock: Extend or Release ran against a lease whose lock has since been acquired by another holder; the fields name the lock and the fencing tokens involved")

	// LockBackendFailed is returned when the locker itself could not serve the
	// operation. A lock being HELD is not this — TryAcquire reports that as
	// (nil, false, nil).
	LockBackendFailed = errs.Define(CodeLockBackendFailed, "LOCK_BACKEND_FAILED",
		"The lock backend could not serve the operation",
		"core/lock: a locker failed for a reason of its own rather than reporting the lock as held; the fields name the operation and the lock")

	// LockNameRejected is returned for a name the locker refuses.
	//
	// The empty string is the case that matters, and it is refused because it
	// is what an uninitialised variable holds. Accepting it would funnel every
	// caller that forgot to set a name through one shared lock — correct-looking,
	// silent, and indistinguishable from contention nobody can explain.
	LockNameRejected = errs.Define(CodeLockNameRejected, "LOCK_NAME_REJECTED",
		"The lock name is not usable as given",
		"core/lock: Acquire or TryAcquire received an empty name or one the backend cannot represent; the fields name the offending part",
		errs.WithExitCode(exitDataErr))
)
