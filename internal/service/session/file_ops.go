// Package session — the file store's reading half, and the lock around it.
package session

import (
	"cmp"
	"context"
	"errors"
	"io/fs"
	"os"
	"time"

	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"

	coresession "github.com/kitsunium/sdk/internal/core/session"
)

// withLock runs fn under the store-wide exclusive lock.
//
// Every operation — including Load, which slides the idle window and therefore
// writes — goes through here. Without it, two processes sharing the directory
// would read the same record, each apply its own change, and each publish a
// whole record atomically: two atomic writes, one lost update. rename(2) makes
// the PUBLICATION indivisible; only the lock makes the read-modify-write cycle
// indivisible, and they are different guarantees.
//
// Both waits observe ctx (ADR 0073). The in-process gate is a channel rather
// than a sync.Mutex because a Mutex cannot be abandoned, and the flock is
// LOCK_NB plus a poll on the injected clock because a blocking LOCK_EX parks
// the thread inside a syscall no cancellation reaches. A request whose caller
// has hung up stops waiting for a lock nobody will read the result of.
func (f *fileStore) withLock(ctx context.Context, fn func() error) (err error) {
	//: cancellation is checked before the wait, so a caller who has already
	//: given up does not queue behind someone else's operation.
	if ctxErr := checkContext(ctx); ctxErr != nil {
		//: StoreUnavailable — retryable.
		return ctxErr
	}
	//: the in-process half FIRST — flock does not exclude goroutines that
	//: share this descriptor (see fileStore.gate). Held for the whole cycle,
	//: so it is released only after the flock is.
	if gateErr := f.enter(ctx); gateErr != nil {
		//: the caller's own context, as StoreUnavailable.
		return gateErr
	}
	defer f.leave()
	//: the cross-process half, polled rather than blocked on.
	if lockErr := f.takeFlock(ctx); lockErr != nil {
		//: LockFailed — without serialisation the store cannot keep its word,
		//: so the operation is refused rather than attempted unlocked — or
		//: StoreUnavailable when it was the caller who left.
		return lockErr
	}
	//: released on every path, including a panic in fn.
	defer func() {
		//: a release that fails leaves the store wedged for every other
		//: operation, so it is CHECKED rather than discarded — and reported
		//: only when fn had nothing worse to say.
		if unlockErr := unlockFile(f.lock); unlockErr != nil {
			//: LockFailed, subordinate to fn's own verdict.
			err = cmp.Or(err, wrapAs(LockFailed, unlockErr))
		}
	}()
	//: and one last look before the section runs. Every wait above is a select,
	//: and a select whose cancellation becomes ready at the same instant as the
	//: acquisition picks between them at random — so a caller that had already
	//: gone could still have its record read, its idle window slid and its
	//: record republished. The check is here rather than only at entry because
	//: that is where the race leaves it: cheap, and it makes "a caller who
	//: leaves gets STORE_UNAVAILABLE" true in the one case where it was a coin
	//: toss.
	if ctxErr := checkContext(ctx); ctxErr != nil {
		//: StoreUnavailable — the lock is released by the defers above.
		return ctxErr
	}
	//: fn owns the critical section.
	return fn()
}

// noDone is the never-ready channel a nil context stands in for. A nil
// receive-only channel blocks forever, which is exactly what "no cancellation"
// means in a select.
var noDone <-chan struct{}

// enter takes the in-process gate, or reports the caller's own departure.
//
// The gate is a one-slot channel and not a sync.Mutex for one reason: a Mutex
// has no abandonable Lock. A goroutine parked in mu.Lock() cannot be told its
// request was cancelled, so a store shared by a handful of handlers turned one
// slow operation into a queue nobody could leave.
func (f *fileStore) enter(ctx context.Context) error {
	//: a nil context is read as "no deadline", matching checkContext.
	if ctx == nil {
		//: an unabandonable wait is then the caller's own choice.
		f.gate <- struct{}{}
		//: entered.
		return nil
	}
	select {
	//: the slot was free, or its holder has just left.
	case f.gate <- struct{}{}:
		//: entered.
		return nil
	//: the caller stopped waiting.
	case <-ctx.Done():
		//: the cause travels as a field; the typed shape stays uniform.
		return wrapAs(coresession.StoreUnavailable, ctx.Err(), kerrs.String("op", "gate"))
	}
}

// leave releases the in-process gate.
func (f *fileStore) leave() {
	//: the slot is always ours here — enter is the only writer, and it is
	//: paired with exactly one leave.
	<-f.gate
}

// takeFlock polls for the cross-process lock until it has it or ctx ends.
//
// The poll interval is the caller's (FileConfig.Poll), armed on the injected
// clock, so a test drives contention without sleeping and production waits on
// the real one.
func (f *fileStore) takeFlock(ctx context.Context) error {
	//: attempt, wait, attempt again — never a blocking flock(2), which parks
	//: the thread inside a syscall no cancellation can reach.
	for {
		taken, flockErr := tryLockExclusive(f.lock)
		//: the call itself failed.
		if flockErr != nil {
			//: LockFailed.
			return wrapAs(LockFailed, flockErr)
		}
		//: ours.
		if taken {
			//: acquired.
			return nil
		}
		//: another process holds it: wait out one interval, or the caller's
		//: patience.
		if waitErr := f.waitPoll(ctx); waitErr != nil {
			//: StoreUnavailable, carrying the caller's context error.
			return waitErr
		}
	}
}

// waitPoll waits one poll interval or until ctx ends.
func (f *fileStore) waitPoll(ctx context.Context) error {
	timer := f.clk.NewTimer(f.poll)
	defer timer.Stop()
	//: a nil context never selects, so it waits out the interval exactly as a
	//: caller with no deadline expects.
	done := noDone
	//: the caller's own cancellation, where there is one.
	if ctx != nil {
		done = ctx.Done()
	}
	select {
	//: the caller stopped waiting.
	case <-done:
		//: the cause travels as a field; the typed shape stays uniform.
		return wrapAs(coresession.StoreUnavailable, ctx.Err(), kerrs.String("op", "lock"))
	//: try again.
	case <-timer.C():
		//: another attempt.
		return nil
	}
}

// checkContext reports a cancelled or expired context as a retryable backend
// fault. It is the file store's answer only: the memory store never blocks, so
// it has nothing to interrupt (see memoryStore.New).
func checkContext(ctx context.Context) error {
	//: a nil context is read as "no deadline" rather than panicking on Err.
	if ctx == nil {
		//: nothing to check.
		return nil
	}
	//: cancellation and deadline both surface here.
	if ctxErr := ctx.Err(); ctxErr != nil {
		//: the cause travels as a field; the typed shape stays uniform.
		return wrapAs(coresession.StoreUnavailable, ctxErr, kerrs.String("op", "context"))
	}
	//: still live.
	return nil
}

// liveLocked reads a record and drops it if it has expired. The caller MUST
// hold the store lock.
func (f *fileStore) liveLocked(id coresession.ID, now time.Time) (rec record, err error) {
	digest := id.Digest()
	rec, readErr := f.readLocked(digest)
	//: NotFound, RecordCorrupt or StoreUnavailable.
	if readErr != nil {
		//: no partial record escapes alongside an error.
		return record{}, readErr
	}
	//: the earlier of the two deadlines decides.
	if !f.win.live(rec, now) {
		//: drop as we report — a dead record must not survive a clock that
		//: moves backwards, and the next call answers NotFound. A removal that
		//: fails is CHECKED, and then subordinate: the caller's session has
		//: expired either way, and Expired is the more useful of the two
		//: answers. The next sweep retries the removal.
		if removeErr := f.removeLocked(digest); removeErr != nil {
			//: Expired still wins.
			return record{}, cmp.Or(wrapAs(coresession.Expired, nil), removeErr)
		}
		//: Expired.
		return record{}, wrapAs(coresession.Expired, nil)
	}
	//: a live record.
	return rec, nil
}

// removeLocked deletes a record file and flushes the directory, so the removal
// survives a power cut — for Destroy, the difference between a revocation and
// a revocation a crash can undo. An already-absent file is success: the caller
// asked for it to be gone and it is. The directory is flushed on that path
// too, because the call that DID unlink it may be the one whose flush failed,
// and a retried Destroy must be able to make that revocation durable. The
// caller MUST hold the store lock.
func (f *fileStore) removeLocked(digest string) error {
	//: the unlink first; the flush only makes sense once it has happened.
	if unlinkErr := f.unlinkLocked(digest); unlinkErr != nil {
		//: InvalidID or StoreUnavailable; nothing changed on disk.
		return unlinkErr
	}
	//: gone from every reader's view; now make that survive a crash.
	return f.flushLocked("sync-dir-remove")
}

// unlinkLocked deletes a record file WITHOUT flushing the directory — the half
// of removeLocked a sweep repeats per record before flushing once for the whole
// pass. An already-absent file is success. The caller MUST hold the store lock.
func (f *fileStore) unlinkLocked(digest string) error {
	path, pathErr := recordPath(f.dir, digest)
	//: a malformed digest never reaches the filesystem.
	if pathErr != nil {
		//: InvalidID.
		return pathErr
	}
	removeErr := os.Remove(path)
	//: idempotence: destroying twice is not a fault.
	if removeErr == nil || errors.Is(removeErr, fs.ErrNotExist) {
		//: gone either way.
		return nil
	}
	//: anything else is the backend failing.
	return wrapAs(coresession.StoreUnavailable, removeErr, kerrs.String("op", "remove"))
}

// New mints an anonymous session and writes it.
func (f *fileStore) New(ctx context.Context) (session coresession.SessionValue, err error) {
	id, mintErr := mintID(f.source)
	//: minted before the lock, so a slow entropy source blocks nobody else.
	if mintErr != nil {
		//: EntropyFailed.
		return coresession.SessionValue{}, mintErr
	}
	var rec record
	lockErr := f.withLock(ctx, func() error {
		//: a file already under this digest means the random source repeated
		//: itself; overwriting it would hand one caller another's session.
		if f.exists(id.Digest()) {
			//: IdentifierCollision — nothing is written.
			return wrapAs(coresession.IdentifierCollision, nil)
		}
		rec = f.win.mint(id.Digest(), "", f.win.clk.Now())
		//: seal, write beside, rename into place.
		return f.writeLocked(rec)
	})
	//: any failure yields the zero session and no file.
	if lockErr != nil {
		//: the typed verdict.
		return coresession.SessionValue{}, lockErr
	}
	//: the value carries the identifier; the file never does.
	return f.win.build(id, rec)
}

// exists reports whether a record file is present. The caller MUST hold the
// store lock.
func (f *fileStore) exists(digest string) bool {
	path, pathErr := recordPath(f.dir, digest)
	//: a malformed digest names no record.
	if pathErr != nil {
		//: nothing there.
		return false
	}
	_, statErr := os.Stat(path)
	//: any stat that is not "absent" counts as present — including a stat that
	//: failed for another reason, because minting over a file we cannot read is
	//: the one outcome that must not happen.
	return !errors.Is(statErr, fs.ErrNotExist)
}

// Load resolves id and slides its idle window.
func (f *fileStore) Load(ctx context.Context, id coresession.ID) (session coresession.SessionValue, err error) {
	//: the zero identifier names nothing and never reaches the filesystem.
	if id.IsZero() {
		//: InvalidID.
		return coresession.SessionValue{}, coresession.InvalidID
	}
	var rec record
	lockErr := f.withLock(ctx, func() error {
		now := f.win.clk.Now()
		live, liveErr := f.liveLocked(id, now)
		//: NotFound, Expired, RecordCorrupt or StoreUnavailable.
		if liveErr != nil {
			//: propagate unchanged.
			return liveErr
		}
		//: sliding IS the idle timeout — and it is why Load takes the exclusive
		//: lock rather than a shared one.
		rec = f.win.slide(live, now)
		//: persist the slide, or the window would reset on every restart.
		return f.writeLocked(rec)
	})
	//: any failure yields the zero session.
	if lockErr != nil {
		//: the typed verdict.
		return coresession.SessionValue{}, lockErr
	}
	//: re-attach the identifier the caller presented.
	return f.win.build(id, rec)
}
