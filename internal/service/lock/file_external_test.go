package lock_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/errs"

	corelock "github.com/kitsunium/sdk/internal/core/lock"
	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	svclock "github.com/kitsunium/sdk/internal/service/lock"
)

// lockFilePath mirrors the locker's own name-to-path mapping so a test can
// reach the ledger it wrote.
func lockFilePath(dir, name string) string {
	sum := sha256.Sum256([]byte(name))
	return filepath.Join(dir, hex.EncodeToString(sum[:])+".lock")
}

// newFileLocker builds a file locker in a fresh directory, skipping the test
// where the platform has no flock(2) — the SAME condition the constructor
// refuses on, so the skip can never hide a real failure.
func newFileLocker(t *testing.T, dir string) corelock.Locker {
	t.Helper()
	locker, err := svclock.NewFileLocker(svclock.FileConfig{Dir: dir})
	if errors.Is(err, coreproc.UnsupportedPlatform) {
		t.Skip("no flock(2) on this platform — the constructor refuses, which is the contract")
	}
	if err != nil {
		t.Fatalf("NewFileLocker = %v, want a locker", err)
	}
	return locker
}

// releaseOrFail gives a lease back and fails the test if the locker refuses.
// It exists so no test discards a Release error: a release that reports
// LOCK_NOT_HELD means the test's own assumptions about who holds what have
// already diverged, which is worth failing on rather than ignoring.
func releaseOrFail(t *testing.T, lease corelock.Lease) {
	t.Helper()
	if err := lease.Release(context.Background()); err != nil {
		t.Errorf("Release = %v, want nil", err)
	}
}

func TestNewFileLockerRefusesAnEmptyDir(t *testing.T) {
	t.Parallel()
	locker, err := svclock.NewFileLocker(svclock.FileConfig{})
	if locker != nil {
		t.Fatal("a file locker was built with no directory — its SCOPE would be a value nobody chose")
	}
	if !errs.HasCode(err, corelock.CodeLockMisconfigured) {
		t.Fatalf("NewFileLocker = %v, want LOCK_MISCONFIGURED", err)
	}
}

func TestNewFileLockerRefusesANegativePoll(t *testing.T) {
	t.Parallel()
	locker, err := svclock.NewFileLocker(svclock.FileConfig{Dir: t.TempDir(), Poll: -time.Second})
	if locker != nil {
		t.Fatal("a file locker was built with a negative poll interval")
	}
	if !errs.HasCode(err, corelock.CodeLockMisconfigured) {
		t.Fatalf("NewFileLocker = %v, want LOCK_MISCONFIGURED", err)
	}
}

// TestTheFileLockExcludesGoroutines is the regression guard for the
// measurement this backend is built on.
//
// flock(2) is per OPEN FILE DESCRIPTION. Re-locking a description that already
// holds LOCK_EX is a lock CONVERSION that returns success immediately, so a
// store holding one descriptor for its lifetime gets ZERO exclusion between
// its own goroutines — measured on linux/amd64 with eight goroutines around a
// counted section: all eight were inside at once, every run. Correct between
// processes, useless between goroutines, and invisible to any test that only
// spawns processes.
//
// The locker therefore takes an in-process gate before the flock. This test
// counts occupancy the way that measurement did, and fails if it ever exceeds
// one.
//
// Goroutine lifecycle: exactly `goroutines` contenders, each running `rounds`
// acquire/release cycles and then returning. Every one is joined by wg.Wait
// before the assertion, so none outlives the test.
func TestTheFileLockExcludesGoroutines(t *testing.T) {
	t.Parallel()
	locker := newFileLocker(t, t.TempDir())
	const goroutines int = 8
	const rounds int = 40
	var mu sync.Mutex
	inside, maxInside := 0, 0
	var wg sync.WaitGroup
	for range goroutines {
		wg.Go(func() {
			for range rounds {
				lease, err := locker.Acquire(t.Context(), "shared")
				if err != nil {
					t.Errorf("Acquire = %v", err)
					return
				}
				mu.Lock()
				inside++
				if inside > maxInside {
					maxInside = inside
				}
				mu.Unlock()
				mu.Lock()
				inside--
				mu.Unlock()
				if relErr := lease.Release(t.Context()); relErr != nil {
					t.Errorf("Release = %v", relErr)
					return
				}
			}
		})
	}
	wg.Wait()
	if maxInside != 1 {
		t.Fatalf("%d goroutines were inside the section at once — flock alone does not exclude goroutines, and the in-process gate is what does", maxInside)
	}
}

// TestFileLeaseIsNotADeadliner is the negative half of the ADR 0039 sibling
// check, and it carries the backend's central decision.
//
// A file lease has no TTL: it is held until Release or until the holder's
// process dies and the kernel releases it. Nothing can take it from a live
// holder, so there is no deadline to report — and the ABSENCE of Deadliner is
// how a caller learns that, from the API rather than from a comment. If this
// ever started passing, callers would begin renewing and fencing a lease that
// needs neither, and the assertion would stop distinguishing the two backends.
func TestFileLeaseIsNotADeadliner(t *testing.T) {
	t.Parallel()
	locker := newFileLocker(t, t.TempDir())
	lease, err := locker.Acquire(t.Context(), "job")
	if err != nil {
		t.Fatalf("Acquire = %v", err)
	}
	defer releaseOrFail(t, lease)
	if _, ok := lease.(corelock.Deadliner); ok {
		t.Fatal("a file lease reports a deadline — but its lock cannot expire, so the deadline would be a fiction")
	}
}

// TestTheFenceSurvivesTheLocker pins the property that makes a file fence
// usable as a fencing token at all: it lives on disk, so it is monotone across
// every process that ever held the lock, not merely across one locker's
// lifetime.
func TestTheFenceSurvivesTheLocker(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	first := newFileLocker(t, dir)
	lease, err := first.Acquire(t.Context(), "job")
	if err != nil {
		t.Fatalf("Acquire = %v", err)
	}
	if lease.Fence() != 1 {
		t.Fatalf("Fence = %d, want 1", lease.Fence())
	}
	if relErr := lease.Release(t.Context()); relErr != nil {
		t.Fatalf("Release = %v", relErr)
	}
	//: a brand-new locker over the same directory — the shape a restart takes.
	second := newFileLocker(t, dir)
	again, err := second.Acquire(t.Context(), "job")
	if err != nil {
		t.Fatalf("Acquire = %v", err)
	}
	defer releaseOrFail(t, again)
	if again.Fence() != 2 {
		t.Fatalf("Fence after a restart = %d, want 2 — a fence that restarts reissues numbers the resource has already accepted", again.Fence())
	}
}

// TestACorruptFenceLedgerIsRefused pins the refuse-rather-than-repair rule.
// Resetting the counter would hand out numbers the protected resource has
// already seen and accepted, which turns the one mechanism that survives a
// stalled holder into one that endorses it.
func TestACorruptFenceLedgerIsRefused(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	locker := newFileLocker(t, dir)
	if err := os.WriteFile(lockFilePath(dir, "job"), []byte("not-a-counter\n"), 0o600); err != nil {
		t.Fatalf("seeding the ledger = %v", err)
	}
	lease, err := locker.Acquire(t.Context(), "job")
	if lease != nil {
		t.Fatal("a lease was granted over a ledger whose monotonicity cannot be proved")
	}
	if !errs.HasCode(err, svclock.CodeLockFenceCorrupt) {
		t.Fatalf("Acquire = %v, want LOCK_FENCE_CORRUPT", err)
	}
	//: and the refusal must not leave the name held: the gate is given back.
	_, held, tryErr := locker.TryAcquire(t.Context(), "job")
	if tryErr == nil && held {
		t.Fatal("the failed acquisition left the lock held")
	}
}

func TestFileReleaseIsIdempotentAndExtendFollowsIt(t *testing.T) {
	t.Parallel()
	locker := newFileLocker(t, t.TempDir())
	lease, err := locker.Acquire(t.Context(), "job")
	if err != nil {
		t.Fatalf("Acquire = %v", err)
	}
	//: while held, Extend re-asserts ownership and cannot fail: nothing can
	//: take a held flock away.
	if extErr := lease.Extend(t.Context()); extErr != nil {
		t.Fatalf("Extend while held = %v, want nil", extErr)
	}
	if relErr := lease.Release(t.Context()); relErr != nil {
		t.Fatalf("first Release = %v, want nil", relErr)
	}
	if relErr := lease.Release(t.Context()); relErr != nil {
		t.Fatalf("second Release = %v, want nil", relErr)
	}
	//: after Release the lease genuinely holds nothing, and says so.
	if extErr := lease.Extend(t.Context()); !errs.HasCode(extErr, corelock.CodeLockNotHeld) {
		t.Fatalf("Extend after Release = %v, want LOCK_NOT_HELD", extErr)
	}
}

func TestFileTryAcquireReportsHeldWithoutAnError(t *testing.T) {
	t.Parallel()
	locker := newFileLocker(t, t.TempDir())
	first, err := locker.Acquire(t.Context(), "job")
	if err != nil {
		t.Fatalf("Acquire = %v", err)
	}
	defer releaseOrFail(t, first)
	lease, held, err := locker.TryAcquire(t.Context(), "job")
	if err != nil {
		t.Fatalf("TryAcquire = %v, want no error — 'held elsewhere' is an answer, not a failure", err)
	}
	if held || lease != nil {
		t.Fatal("TryAcquire granted a lock that was already held")
	}
}
