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
func (f *fileStore) withLock(ctx context.Context, fn func() error) (err error) {
	//: cancellation is checked before the wait, so a caller who has already
	//: given up does not queue behind someone else's operation.
	if ctxErr := checkContext(ctx); ctxErr != nil {
		//: StoreUnavailable — retryable.
		return ctxErr
	}
	//: the in-process half FIRST — flock does not exclude goroutines that
	//: share this descriptor (see fileStore.mu). Held for the whole cycle,
	//: so it is released only after the flock is.
	f.mu.Lock()
	defer f.mu.Unlock()
	//: blocks until the lock is free; it is held for microseconds.
	if lockErr := lockExclusive(f.lock); lockErr != nil {
		//: LockFailed — without serialisation the store cannot keep its word,
		//: so the operation is refused rather than attempted unlocked.
		return wrapAs(LockFailed, lockErr)
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
	//: fn owns the critical section.
	return fn()
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

// removeLocked deletes a record file. An already-absent file is success: the
// caller asked for it to be gone and it is. The caller MUST hold the store lock.
func (f *fileStore) removeLocked(digest string) error {
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
