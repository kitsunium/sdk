// Package lock — the file locker: exclusion between PROCESSES on one machine,
// composed with the in-process gate because flock(2) alone provides none
// between goroutines.
package lock

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"

	corelock "github.com/kitsunium/sdk/internal/core/lock"
	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

// lockSuffix names a lock file. The stem is the SHA-256 of the lock name, so
// any name a caller can write maps to exactly one usable filename: no path
// separator escapes the directory, no length limit is hit, and no two distinct
// names collide by way of a filesystem's case folding or Unicode
// normalisation — all three of which turn "two locks" into "one lock" or
// "one lock" into "two", silently.
const lockSuffix string = ".lock"

// fileLocker excludes PROCESSES through flock(2) and GOROUTINES through
// [nameGate].
//
// # Its leases do not expire, and that is the decision
//
// A file lease has no TTL. It is held exactly as long as its descriptor is
// open, and it ends in exactly two ways: the holder releases it, or the
// holder's process dies and the kernel releases it. There is no timer and no
// takeover.
//
// That is the opposite choice from [memoryLocker], and it comes from a real
// difference rather than a preference. A dead process is noticed BY THE
// KERNEL, immediately and reliably, so the failure a TTL exists to recover
// from is already handled here. What a TTL would add is the ability to take
// the lock from a process that is still alive — which is the one thing this
// domain's package comment spends its length warning about, and which no
// amount of fencing makes safe for a resource that cannot check a fence.
//
// The price is stated rather than hidden: a holder that HANGS without dying
// blocks its waiters indefinitely. That is a liveness failure, it is visible
// (every waiter is blocked in Acquire, on a context they chose the deadline
// for), and it is strictly preferable to the alternative — a safety failure in
// which two processes are inside one section and neither is blocked, neither
// logs anything, and the evidence is corrupted data much later.
//
// Consequently a fileLease does NOT implement [corelock.Deadliner]. That
// absence is the API telling a caller which world it is in.
type fileLocker struct {
	// dir holds one lock file per name.
	dir string
	// gate is the in-process half. It is taken BEFORE the flock and released
	// AFTER it, so the two never disagree about who holds what.
	gate *nameGate
	// clk drives the retry wait while another process holds the lock.
	clk clock.Timed
	// poll is the validated interval between attempts.
	poll time.Duration
}

// NewFileLocker returns a [corelock.Locker] whose locks exclude every process
// using the same directory on the same machine.
//
// It refuses, at construction rather than at first use: a configuration it
// cannot honour ([corelock.LockMisconfigured]), a platform without flock(2)
// ([coreproc.UnsupportedPlatform]), and a directory whose lock files any
// account could replace ([LockDirectoryUnsafe]).
func NewFileLocker(cfg FileConfig) (locker corelock.Locker, err error) {
	//: the directory and the poll interval are checked before anything
	//: touches disk.
	if invalid := cfg.validate(); invalid != nil {
		//: LockMisconfigured, naming the field.
		return nil, invalid
	}
	//: ADR 0018: no native mechanic means a typed refusal, never a locker that
	//: reports success while excluding nothing.
	if !platformNative {
		//: the SDK-wide sentinel for exactly this situation.
		return nil, coreproc.UnsupportedPlatform
	}
	//: create it if absent, check it if not.
	if dirErr := prepareDir(cfg.Dir); dirErr != nil {
		//: LockBackendFailed, LockMisconfigured or LockDirectoryUnsafe.
		return nil, dirErr
	}
	//: usable — build the locker.
	return &fileLocker{
		dir:  cfg.Dir,
		gate: newNameGate(),
		clk:  cfg.clockOrSystem(),
		poll: cfg.pollOrDefault(),
	}, nil
}

// Acquire blocks until name is held by this caller or ctx ends.
func (l *fileLocker) Acquire(ctx context.Context, name string) (lease corelock.Lease, err error) {
	//: an empty name is what an uninitialised variable holds; see errors.go.
	if rejected := checkName(name); rejected != nil {
		//: LockNameRejected.
		return nil, rejected
	}
	//: goroutines first. This is the half flock does not provide: measured,
	//: eight goroutines sharing one descriptor were all inside at once (see
	//: nameGate's comment). Taking the gate first also means the flock loop
	//: below only ever contends with OTHER PROCESSES.
	if gateErr := l.gate.enter(ctx, name); gateErr != nil {
		//: the caller's own error.
		return nil, gateErr
	}
	taken, takeErr := l.takeFlock(ctx, name, true)
	//: the gate is only ours to hold while the flock is.
	if takeErr != nil {
		//: give the name back so another goroutine can try.
		l.gate.leave(name)
		//: the failure, unmodified.
		return nil, takeErr
	}
	//: held against every goroutine here and every process on this machine.
	return taken, nil
}

// TryAcquire attempts the acquisition once and never waits.
func (l *fileLocker) TryAcquire(ctx context.Context, name string) (lease corelock.Lease, held bool, err error) {
	//: same refusal as Acquire, for the same reason.
	if rejected := checkName(name); rejected != nil {
		//: LockNameRejected.
		return nil, false, rejected
	}
	//: a cancelled caller gets its own error rather than a lock it cannot use.
	if ctx.Err() != nil {
		//: the caller's deadline, reported as the caller's error.
		return nil, false, ctx.Err()
	}
	//: another goroutine of this process holds it — that is the whole answer,
	//: and it is not a failure.
	if !l.gate.tryEnter(name) {
		//: held elsewhere.
		return nil, false, nil
	}
	taken, takeErr := l.takeFlock(ctx, name, false)
	//: either a real failure or "another process holds it".
	if takeErr != nil || taken == nil {
		//: the gate must not outlive the attempt.
		l.gate.leave(name)
		//: nil,false,err covers both.
		return nil, false, takeErr
	}
	//: held.
	return taken, true, nil
}

// takeFlock opens the lock file, takes the flock — waiting only when wait is
// true — and records the acquisition's fencing token.
//
// It returns (nil, nil) ONLY when wait is false and another process holds the
// lock. The caller holds the gate on entry and owns releasing it on failure.
func (l *fileLocker) takeFlock(ctx context.Context, name string, wait bool) (lease corelock.Lease, err error) {
	path := l.pathFor(name)
	//: the descriptor IS the lock, so it must survive this function on the
	//: success path and MUST NOT on any other. `kept` is what distinguishes
	//: them, and the defer below is what makes every early return — present
	//: and future — give the descriptor back without repeating the cleanup.
	//: Both flags are declared BEFORE the open so the defer can be armed on
	//: the very next line.
	kept, locked := false, false
	file, openErr := os.OpenFile(path, os.O_CREATE|os.O_RDWR, lockFileMode)
	defer func() {
		//: an open that failed left no descriptor, and the success path keeps
		//: the one it got — the lease owns it from then on.
		if file == nil || kept {
			//: nothing to release.
			return
		}
		//: give the flock up while the descriptor is still valid, then close.
		//: Neither result is discarded: they are folded into the error already
		//: being returned, which is the one the caller actually needs.
		unlockErr := releaseIfLocked(file, locked)
		closeErr := file.Close()
		err = foldAbandon(err, path, unlockErr, closeErr)
	}()
	//: the medium refused before any lock was involved.
	if openErr != nil {
		//: LockBackendFailed.
		return nil, backendFailed("open", path, openErr)
	}
	locked, err = l.contend(ctx, file, wait)
	//: an error, or a non-blocking attempt that lost. Both leave the deferred
	//: abandon to clean up; nil,nil is "another process holds it".
	if err != nil || !locked {
		//: the deferred abandon returns the caller's outcome unchanged.
		return nil, err
	}
	minted, mintErr := l.mintLease(file, path, name)
	//: the fence could not be advanced — the lock is useless without it.
	if mintErr != nil {
		//: LockFenceCorrupt or LockBackendFailed, plus any release failure.
		return nil, mintErr
	}
	//: held, fenced, and durable: the descriptor is now the lease's.
	kept = true
	//: the lease the caller releases through.
	return minted, nil
}

// contend takes the flock, retrying on this locker's clock while wait is true.
func (l *fileLocker) contend(ctx context.Context, file *os.File, wait bool) (held bool, err error) {
	//: attempt, sleep, attempt again — never a blocking flock(2), which parks
	//: the thread inside a syscall no cancellation can reach.
	for {
		taken, flockErr := flockTry(file)
		//: the call itself failed.
		if flockErr != nil {
			//: LockBackendFailed.
			return false, backendFailed("flock", file.Name(), flockErr)
		}
		//: ours.
		if taken {
			//: acquired.
			return true, nil
		}
		//: a non-blocking caller has its answer.
		if !wait {
			//: held elsewhere.
			return false, nil
		}
		//: wait out the poll interval, or the caller's patience.
		if sleepErr := l.sleep(ctx); sleepErr != nil {
			//: the caller's own error.
			return false, sleepErr
		}
	}
}

// sleep waits one poll interval or until ctx ends.
func (l *fileLocker) sleep(ctx context.Context) error {
	//: the wait runs on the injected clock, so a ManualClock makes contention
	//: deterministic and no test in this package sleeps.
	timer := l.clk.NewTimer(l.poll)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		//: the caller's patience ran out.
		return ctx.Err()
	case <-timer.C():
		//: try again.
		return nil
	}
}

// mintLease advances the on-disk fencing counter and builds the lease.
func (l *fileLocker) mintLease(file *os.File, path, name string) (lease corelock.Lease, err error) {
	previous, readErr := readFence(file, path)
	//: a ledger that is not a counter is refused, never reset.
	if readErr != nil {
		//: LockFenceCorrupt or LockBackendFailed.
		return nil, readErr
	}
	fence := previous + 1
	//: durable BEFORE the caller is told it holds the lock: a token issued and
	//: then lost to a crash would be reissued to the next holder.
	if writeErr := writeFence(file, path, fence); writeErr != nil {
		//: LockBackendFailed.
		return nil, writeErr
	}
	//: the handle the caller releases through.
	return newFileLease(l, file, name, fence), nil
}

// pathFor maps a lock name to its file.
func (l *fileLocker) pathFor(name string) string {
	sum := sha256.Sum256([]byte(name))
	//: hex of the digest: fixed length, no separators, no case folding, and
	//: nothing of the caller's name on disk where a backup or a crash report
	//: would carry it.
	return filepath.Join(l.dir, hex.EncodeToString(sum[:])+lockSuffix)
}

// release gives the flock and the gate back, in that order.
func (l *fileLocker) release(file *os.File, name string) error {
	unlockErr := flockUnlock(file)
	closeErr := file.Close()
	//: the gate is released LAST: while it is held, no goroutine of this
	//: process can reach the flock, so the window in which the flock is free
	//: and the gate is not cannot be entered from inside this process.
	l.gate.leave(name)
	//: a failed unlock is reported even though the close released it anyway,
	//: because a filesystem that refuses LOCK_UN is telling us something.
	if unlockErr != nil {
		//: LockBackendFailed.
		return backendFailed("funlock", name, unlockErr)
	}
	//: a close that fails after a successful unlock has already given the lock
	//: up, but it is still a medium fault the caller should see.
	if closeErr != nil {
		//: LockBackendFailed.
		return backendFailed("close", name, closeErr)
	}
	//: released.
	return nil
}

// foldAbandon folds a failure to give an unusable descriptor back into cause,
// which is the error the caller actually needs.
//
// Nothing is discarded here. A failed unlock or close cannot change whether
// the lock was taken — that question is already answered by cause — but a
// filesystem refusing either is a fault worth carrying, so it travels as a
// FIELD on the returned error rather than replacing it. When cause is nil (the
// TryAcquire "another process holds it" answer) a clean abandon reports nil,
// keeping that outcome an answer rather than an error.
func foldAbandon(cause error, path string, unlockErr, closeErr error) error {
	//: nothing went wrong on the way out: the caller's own outcome stands.
	if unlockErr == nil && closeErr == nil {
		//: cause unchanged — possibly nil, which is the "held elsewhere" answer.
		return cause
	}
	//: no cause of its own: the abandon failure IS the failure.
	if cause == nil {
		//: LockBackendFailed, naming what went wrong on the way out.
		return backendFailed("abandon", path, firstNonNil(unlockErr, closeErr))
	}
	//: origin wins, so cause keeps its code and reason and gains the detail.
	return kerrs.Wrap(cause, kerrs.WrapParams{},
		kerrs.String("abandon_error", abandonDetail(unlockErr, closeErr)))
}

// releaseIfLocked unlocks file only when this caller actually holds the lock.
func releaseIfLocked(file *os.File, locked bool) error {
	//: unlocking a descriptor we never locked is not an error on any platform
	//: here, but calling it would still be a lie in the code.
	if !locked {
		//: nothing to release.
		return nil
	}
	//: give the flock up while the descriptor is still valid.
	return flockUnlock(file)
}

// abandonDetail renders the unlock and close failures as one field value.
func abandonDetail(unlockErr, closeErr error) string {
	//: both failed — name both, because they have different causes.
	if unlockErr != nil && closeErr != nil {
		//: unlock first, in the order they were attempted.
		return "unlock: " + unlockErr.Error() + "; close: " + closeErr.Error()
	}
	//: only the unlock failed.
	if unlockErr != nil {
		//: the single reason.
		return "unlock: " + unlockErr.Error()
	}
	//: only the close failed.
	return "close: " + closeErr.Error()
}

// firstNonNil returns the first non-nil of two errors.
func firstNonNil(first, second error) error {
	//: the unlock failure is the more informative of the two when both exist,
	//: so it is listed first; cmp.Or returns the first non-zero value.
	return cmp.Or(first, second)
}
