//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

// Package session_test — the two waits the file store makes, and the fact that
// a caller can leave either one.
//
// Both need a lock that is actually held, so they are tagged like the store
// itself: where flock(2) does not exist the constructor refuses and there is
// nothing to contend for.
package session_test

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	coresession "github.com/kitsunium/sdk/internal/core/session"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcsession "github.com/kitsunium/sdk/internal/service/session"
)

// holdTheLock takes the store's flock from a SECOND open file description, the
// way another process does, and returns the release.
//
// A second description is what makes this a real contention rather than a
// no-op: flock on the SAME description is a lock conversion that succeeds at
// once — measured in internal/service/lock, eight goroutines inside one
// counted section on every run — which is exactly why the store also carries
// an in-process gate.
func holdTheLock(t *testing.T, dir string) (release func()) {
	t.Helper()
	file, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("opening the lock file: %v", err)
	}
	if err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("taking the lock from a second description: %v", err)
	}
	return sync.OnceFunc(func() {
		if uerr := syscall.Flock(int(file.Fd()), syscall.LOCK_UN); uerr != nil {
			t.Logf("releasing the held lock: %v", uerr)
		}
		if cerr := file.Close(); cerr != nil {
			t.Logf("closing the held lock: %v", cerr)
		}
	})
}

// absentID mints a well-formed identifier no record was ever stored under, so
// Load reaches the lock and stops there — which is where every case below is
// looking.
func absentID(t *testing.T) coresession.ID {
	t.Helper()
	raw := make([]byte, coresession.IDLen)
	//: a constant pattern: the value never leaves this process, and a random
	//: one would make a failure harder to read for no property gained.
	for i := range raw {
		raw[i] = byte(i + 1)
	}
	id, err := coresession.NewID(raw)
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}
	return id
}

// TestACallerWhoLeftStopsWaitingForTheLock is the decision ADR 0073 records.
//
// The store-wide lock used to be taken with a blocking flock(2), which parks
// the thread inside a syscall no cancellation can reach. A request whose client
// had hung up — or whose deadline had passed — kept waiting for a lock nobody
// would read the result of, and the goroutine came back only when some other
// process released it. Under a handler pool that is how a slow neighbour
// becomes an unavailable service.
//
// The lock is now attempted without blocking and the wait happens on the
// injected clock, where a cancelled context wins the select.
//
// Seen failing against the blocking LOCK_EX: the operation never returned and
// the test binary was killed by its own timeout, the goroutine parked in
// syscall.Flock under session.(*fileStore).withLock.
// # Goroutine lifetime
//
// One goroutine parks on the lock. It ends when the cancel below reaches it,
// and the receive after it is what proves that happened.
func TestACallerWhoLeftStopsWaitingForTheLock(t *testing.T) {
	t.Parallel()
	clk := clock.NewManualClock(time.Unix(1700000000, 0))
	fixture := newFileFixture(t, clk)
	release := holdTheLock(t, fixture.dir)
	defer release()
	ctx, cancel := context.WithCancel(t.Context())
	failed := make(chan error, 1)
	go func() {
		//: any operation goes through the same lock; Load is the cheapest.
		_, err := fixture.store.Load(ctx, absentID(t))
		failed <- err
	}()
	//: the operation is parked on its poll timer before the caller leaves —
	//: proving the wait is the LOCK's, not a race with the goroutine starting.
	clk.BlockUntil(1)
	cancel()
	err := <-failed
	//: the store could not serve the request, and says so as the retryable
	//: backend fault every other unavailability uses.
	if !errs.HasCode(err, coresession.CodeStoreUnavailable) {
		t.Errorf("a cancelled caller got %v, want STORE_UNAVAILABLE", err)
	}
}

// TestTheLockIsTakenOnceTheHolderLeaves is the other half: the wait is a poll,
// so it must end in success as well as in refusal. Without this, "cancellable"
// would be satisfied by a store that never acquires the lock at all.
// # Goroutine lifetime
//
// One goroutine performs the operation while the test holds the lock. It ends
// when the operation returns, which the release plus one clock step below
// cause, and the receive is what waits for it.
func TestTheLockIsTakenOnceTheHolderLeaves(t *testing.T) {
	t.Parallel()
	clk := clock.NewManualClock(time.Unix(1700000000, 0))
	fixture := newFileFixture(t, clk)
	release := holdTheLock(t, fixture.dir)
	created := make(chan error, 1)
	go func() {
		_, err := fixture.store.New(t.Context())
		created <- err
	}()
	//: parked on the poll, with the lock genuinely held elsewhere.
	clk.BlockUntil(1)
	release()
	//: one interval later the next attempt runs, and this time it wins.
	clk.Advance(svcsession.DefaultPoll)
	if err := <-created; err != nil {
		t.Errorf("New after the holder left: %v, want it to acquire the lock", err)
	}
}

// TestAGoroutineWaitingOnTheGateCanLeaveToo covers the in-process half. The
// gate used to be a sync.Mutex, and a goroutine parked in Lock cannot be told
// its caller has gone: the flock could be made cancellable and a handler pool
// would still queue behind one slow operation with no way out.
//
// The lock is held from outside, so the first operation parks on the POLL while
// holding the gate, and the second parks on the GATE — which is the wait under
// test. The cancellation must happen AFTER it is parked there, or the early
// context check at the top of withLock answers instead and the gate is never
// exercised: that is what the synctest bubble buys, since synctest.Wait returns
// only once every other goroutine is durably blocked.
//
// Seen failing with the gate's wait made unabandonable — the plain channel send
// that a sync.Mutex's Lock is: "panic: deadlock: all goroutines in bubble are
// blocked", the second caller parked in session.(*fileStore).enter.
// # Goroutine lifetime
//
// Two goroutines, both inside the synctest bubble, which is what makes their
// end assertable: the second is released by the cancel, the first by the
// release and the clock step, and a bubble cannot close while either remains.
func TestAGoroutineWaitingOnTheGateCanLeaveToo(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		clk := clock.NewManualClock(time.Unix(1700000000, 0))
		fixture := newFileFixture(t, clk)
		release := holdTheLock(t, fixture.dir)
		defer release()
		first := make(chan error, 1)
		go func() {
			_, err := fixture.store.Load(context.Background(), absentID(t))
			first <- err
		}()
		//: the first caller holds the gate and is parked on its poll timer.
		clk.BlockUntil(1)
		ctx, cancel := context.WithCancel(context.Background())
		second := make(chan error, 1)
		go func() {
			//: no timer of its own: this one is parked on the gate.
			_, err := fixture.store.Load(ctx, absentID(t))
			second <- err
		}()
		//: both are blocked now — the second on the gate, which is the only
		//: place it can be.
		synctest.Wait()
		cancel()
		//: it returns without the first caller having moved, which is the whole
		//: assertion — the lock is still held and no clock has advanced.
		if err := <-second; !errs.HasCode(err, coresession.CodeStoreUnavailable) {
			t.Errorf("a caller cancelled while waiting on the gate got %v, want STORE_UNAVAILABLE", err)
		}
		select {
		case err := <-first:
			t.Fatalf("the first caller returned early (%v); it should still hold the gate", err)
		default:
		}
		//: let the first caller finish: a bubble cannot end with a goroutine
		//: still blocked in it, and this one is parked on its poll by design.
		release()
		clk.Advance(svcsession.DefaultPoll)
		if err := <-first; err != nil && !errs.HasCode(err, coresession.CodeNotFound) {
			t.Errorf("the first caller ended with %v, want the absent record's own verdict", err)
		}
	})
}

// lateCancel is a context that reports itself live the FIRST time it is asked
// and cancelled afterwards, with a nil Done channel so no select ever sees it.
//
// It models one instant that cannot otherwise be reached from outside: the
// caller was still waiting when withLock checked at entry, and had gone by the
// time the lock was in hand. Every wait in between is a select, and a select
// whose cancellation becomes ready at the same moment as the acquisition picks
// between them at random — so the real race exists and lands here.
type lateCancel struct {
	context.Context
	asked atomic.Int64
}

// Err reports live once, then cancelled.
func (c *lateCancel) Err() error {
	//: the first ask is withLock's entry check, which must pass.
	if c.asked.Add(1) == 1 {
		//: still live.
		return nil
	}
	//: gone by the time the lock is held.
	return context.Canceled
}

// Done never fires, so the gate and the poll cannot take the cancellation arm:
// what the test drives is the CHECK, not the waits, which have tests of their
// own above.
func (c *lateCancel) Done() <-chan struct{} { return nil }

// TestACallerThatLeavesWhileAcquiringDoesNotRunTheSection pins the check that
// closes the race the three waits leave open. Without it, a caller that had
// already gone could still have its record read, its idle window slid and its
// record republished — work nobody would read, done under a lock everyone else
// is waiting for.
//
// Nothing holds the lock here, so every wait succeeds at once and the only
// thing between this caller and the critical section is the check under test.
//
// Seen failing with the post-acquisition check removed: New returned <nil> and
// a record was created for a caller that had gone.
func TestACallerThatLeavesWhileAcquiringDoesNotRunTheSection(t *testing.T) {
	t.Parallel()
	clk := clock.NewManualClock(time.Unix(1700000000, 0))
	fixture := newFileFixture(t, clk)
	ctx := &lateCancel{Context: t.Context()}
	_, err := fixture.store.New(ctx)
	if !errs.HasCode(err, coresession.CodeStoreUnavailable) {
		t.Errorf("a caller that left while acquiring got %v, want STORE_UNAVAILABLE", err)
	}
	//: and nothing was written for it.
	if got := fixture.records(t); len(got) != 0 {
		t.Errorf("the store holds %d record(s), want none — the section ran", len(got))
	}
}
