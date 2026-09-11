//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package lock_test

import (
	"context"
	"errors"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"

	svclock "github.com/kitsunium/sdk/internal/service/lock"
)

// This file carries the SAME build constraint as flock_unix.go, so it runs
// everywhere the file locker exists and nowhere it does not. That is not a
// rule-12 exclusion: there is no configuration in which the code under test
// is built and this file is not.

// holdForeignFlock takes an exclusive flock on a SEPARATE open file
// description of the lock file and returns a function that releases it.
//
// A separate description is exactly what another process holds, as far as the
// kernel is concerned: flock is per-description, not per-process, so this is a
// faithful stand-in that needs no subprocess. (The converse — that two
// descriptions in one process exclude each other — was measured on
// linux/amd64 before this backend was written; see nameGate's comment for the
// measurement that does NOT hold, which is the one the gate exists for.)
func holdForeignFlock(t *testing.T, path string) func() {
	t.Helper()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("opening the lock file = %v", err)
	}
	if flockErr := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); flockErr != nil {
		if closeErr := file.Close(); closeErr != nil {
			t.Errorf("closing the lock file = %v", closeErr)
		}
		t.Fatalf("taking the foreign flock = %v", flockErr)
	}
	released := false
	return func() {
		//: idempotent, because the successful path releases early and the
		//: deferred call still runs.
		if released {
			return
		}
		released = true
		if unlockErr := syscall.Flock(int(file.Fd()), syscall.LOCK_UN); unlockErr != nil {
			t.Errorf("releasing the foreign flock = %v", unlockErr)
		}
		if closeErr := file.Close(); closeErr != nil {
			t.Errorf("closing the lock file = %v", closeErr)
		}
	}
}

// TestTryAcquireLosesToAForeignHolder pins the half flock really does provide:
// exclusion against a description this locker does not own.
func TestTryAcquireLosesToAForeignHolder(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	locker := newFileLocker(t, dir)
	release := holdForeignFlock(t, lockFilePath(dir, "job"))
	defer release()
	lease, held, err := locker.TryAcquire(t.Context(), "job")
	if err != nil {
		t.Fatalf("TryAcquire = %v, want no error", err)
	}
	if held || lease != nil {
		t.Fatal("TryAcquire granted a lock another description holds")
	}
}

// TestAcquireHonoursItsContextWhileAForeignHolderWaitsOut pins the reason the
// locker polls instead of calling a blocking flock(2).
//
// A blocking LOCK_EX parks the thread inside a syscall no cancellation can
// reach, so an Acquire with a deadline would ignore it entirely — the caller
// would wait for the holder, not for its own budget. The poll loop is what
// makes ctx mean something, and it runs on the injected clock so this test
// never sleeps.
//
// Goroutine lifecycle: one contender, which returns as soon as its Acquire
// reports the cancellation. The test receives that error before asserting, so
// the goroutine is joined by the channel receive rather than left running.
func TestAcquireHonoursItsContextWhileAForeignHolderWaitsOut(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	manual := clock.NewManualClock(time.Unix(0, 0).UTC())
	locker, err := svclock.NewFileLocker(svclock.FileConfig{Dir: dir, Clock: manual, Poll: time.Second})
	if err != nil {
		t.Fatalf("NewFileLocker = %v", err)
	}
	release := holdForeignFlock(t, lockFilePath(dir, "job"))
	defer release()

	ctx, cancel := context.WithCancel(t.Context())
	failed := make(chan error, 1)
	go func() {
		_, acqErr := locker.Acquire(ctx, "job")
		failed <- acqErr
	}()
	//: the poll timer is armed — the goroutine is genuinely waiting.
	manual.BlockUntil(1)
	cancel()
	if acqErr := <-failed; !errors.Is(acqErr, context.Canceled) {
		t.Fatalf("Acquire = %v, want context.Canceled", acqErr)
	}
	//: and the gate must have been given back, or the name would stay held by
	//: a caller that never got it.
	release()
	lease, held, tryErr := locker.TryAcquire(t.Context(), "job")
	if tryErr != nil || !held {
		t.Fatalf("TryAcquire after the abandoned wait = (%t, %v), want (true, nil) — the abandoned Acquire leaked its gate", held, tryErr)
	}
	releaseOrFail(t, lease)
}

// TestAcquireTakesTheLockWhenTheForeignHolderLeaves pins the successful side
// of the same loop: the poll fires, the flock is now free, and the waiter gets
// it — driven by the clock, not by a sleep.
//
// Goroutine lifecycle: one contender, which releases the lease and then sends
// its fence on a buffered channel before returning. The test's receive is what
// joins it, and the release happens first so nothing touches t after the send.
func TestAcquireTakesTheLockWhenTheForeignHolderLeaves(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	manual := clock.NewManualClock(time.Unix(0, 0).UTC())
	locker, err := svclock.NewFileLocker(svclock.FileConfig{Dir: dir, Clock: manual, Poll: time.Second})
	if err != nil {
		t.Fatalf("NewFileLocker = %v", err)
	}
	release := holdForeignFlock(t, lockFilePath(dir, "job"))

	acquired := make(chan uint64, 1)
	go func() {
		lease, acqErr := locker.Acquire(t.Context(), "job")
		if acqErr != nil {
			close(acquired)
			return
		}
		fence := lease.Fence()
		//: release BEFORE the send: after it, the test may have finished and
		//: touching t from here would be a use-after-test.
		if relErr := lease.Release(context.Background()); relErr != nil {
			t.Errorf("Release = %v", relErr)
		}
		acquired <- fence
	}()
	manual.BlockUntil(1)
	release()
	manual.Advance(time.Second)
	fence, ok := <-acquired
	if !ok {
		t.Fatal("the waiting Acquire failed instead of taking the freed lock")
	}
	if fence != 1 {
		t.Fatalf("Fence = %d, want 1", fence)
	}
}
