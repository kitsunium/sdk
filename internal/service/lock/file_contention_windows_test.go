//go:build windows

package lock_test

import (
	"context"
	"errors"
	"os"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/kitsunium/sdk/internal/kernel/clock"

	svclock "github.com/kitsunium/sdk/internal/service/lock"
)

// This file carries the SAME build constraint as flock_windows.go, so it runs
// everywhere the Windows backend exists and nowhere it does not. That is not a
// rule-12 exclusion of the kind that hides a test: the lane that runs it is the
// `windows` job of .github/workflows/e2e-cross.yml, which executes
// `go test ./lock` on a real windows-latest kernel. The Linux Bazel gate
// compiles neither the backend nor this file.
//
// The bindings below are the TEST's own, deliberately separate from the ones
// the backend uses. A foreign holder has to be a second HANDLE taking the same
// range, and building that by hand is what makes the measurements in this file
// measurements rather than assertions about our own code.

// kernel32 range-locking entry points, bound lazily (syscall.NewLazyDLL, no
// golang.org/x/sys — the ADR 0018 discipline). Both are stable kernel32
// exports present since Windows NT 3.1.
var (
	testKernel32     = syscall.NewLazyDLL("kernel32.dll")
	testLockFileEx   = testKernel32.NewProc("LockFileEx")
	testUnlockFileEx = testKernel32.NewProc("UnlockFileEx")
)

// LockFileEx flags (fileapi.h / winbase.h) and the winerror.h codes the calls
// below distinguish. Hand-declared and cited, never imported.
const (
	testFailImmediately uintptr = 0x00000001 // LOCKFILE_FAIL_IMMEDIATELY
	testExclusiveLock   uintptr = 0x00000002 // LOCKFILE_EXCLUSIVE_LOCK
	testAllBytes        uintptr = 0xFFFFFFFF // MAXDWORD, low and high: the whole file

	testErrorLockViolation syscall.Errno = 33 // ERROR_LOCK_VIOLATION
)

// testHighOffset is the alternative range measured beside the whole-file one:
// a single byte far past any plausible ledger. See
// TestAHighOffsetRangeLeavesTheLedgerUnprotected for what it costs.
const testHighOffset uint64 = 1 << 62

// lockRange takes an exclusive, non-blocking range lock on file and reports
// whether it was granted. It returns (false, nil) only for the documented
// contention answer, so a genuine call failure can never be read as "busy".
func lockRange(t *testing.T, file *os.File, offset uint64, low, high uintptr) (granted bool, err error) {
	t.Helper()
	overlapped := new(syscall.Overlapped)
	overlapped.Offset = uint32(offset)
	overlapped.OffsetHigh = uint32(offset >> 32)
	r1, _, errno := testLockFileEx.Call(
		uintptr(file.Fd()),
		testExclusiveLock|testFailImmediately,
		0,
		low,
		high,
		uintptr(unsafe.Pointer(overlapped)),
	)
	//: a non-zero return is the grant.
	if r1 != 0 {
		//: held.
		return true, nil
	}
	//: the one failure that means "someone else holds it".
	if errors.Is(errno, testErrorLockViolation) {
		//: contended, not broken.
		return false, nil
	}
	//: anything else is a real failure of the call.
	return false, errno
}

// unlockRange releases what [lockRange] took.
func unlockRange(t *testing.T, file *os.File, offset uint64, low, high uintptr) {
	t.Helper()
	overlapped := new(syscall.Overlapped)
	overlapped.Offset = uint32(offset)
	overlapped.OffsetHigh = uint32(offset >> 32)
	r1, _, errno := testUnlockFileEx.Call(
		uintptr(file.Fd()),
		0,
		low,
		high,
		uintptr(unsafe.Pointer(overlapped)),
	)
	if r1 == 0 {
		t.Errorf("UnlockFileEx = %v", errno)
	}
}

// openLockFile opens path the way the backend does and closes it with the test.
func openLockFile(t *testing.T, path string) *os.File {
	t.Helper()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("opening the lock file = %v", err)
	}
	t.Cleanup(func() {
		//: an already-closed file reports ErrClosed, which is not a failure of
		//: the test that closed it on purpose.
		if closeErr := file.Close(); closeErr != nil && !errors.Is(closeErr, os.ErrClosed) {
			t.Errorf("closing the lock file = %v", closeErr)
		}
	})
	return file
}

// holdForeignRangeLock takes the whole-file exclusive lock on a SEPARATE
// HANDLE and returns a function that releases it.
//
// A separate handle is exactly what another process holds, as far as the
// kernel is concerned: LockFileEx exclusion is per handle, not per process —
// which is the measurement TestLockFileExRefusesASecondHandleInTheSameProcess
// makes rather than assumes. So this is a faithful stand-in that needs no
// subprocess, and it is the Windows twin of holdForeignFlock.
func holdForeignRangeLock(t *testing.T, path string) func() {
	t.Helper()
	file := openLockFile(t, path)
	granted, err := lockRange(t, file, 0, testAllBytes, testAllBytes)
	if err != nil || !granted {
		t.Fatalf("taking the foreign range lock = (%t, %v), want (true, nil)", granted, err)
	}
	released := false
	return func() {
		//: idempotent, because the successful path releases early and the
		//: deferred call still runs.
		if released {
			return
		}
		released = true
		unlockRange(t, file, 0, testAllBytes, testAllBytes)
		if closeErr := file.Close(); closeErr != nil {
			t.Errorf("closing the lock file = %v", closeErr)
		}
	}
}

// TestLockFileExRefusesASecondHandleInTheSameProcess is this platform's
// answer to the measurement ADR 0052 §D7 made for flock(2), and it comes out
// the OPPOSITE way.
//
// flock(2) is per OPEN FILE DESCRIPTION: two descriptions in one process do
// exclude each other, but the same description re-locked is a lock CONVERSION
// that succeeds immediately — which is why eight goroutines sharing one
// descriptor were all inside one section at once. LockFileEx is per HANDLE and
// the documented rule is explicit: "If the locking process opens the file a
// second time, it cannot access the specified region through this second
// handle until it unlocks the region." A second handle in the SAME process is
// refused exactly as another process's would be.
//
// Nothing in the file locker relies on that — it takes the in-process gate
// first on every platform — but the two kernels disagreeing on the one
// property a naive backend would lean on is precisely why the gate is a
// guarantee rather than an observation.
func TestLockFileExRefusesASecondHandleInTheSameProcess(t *testing.T) {
	t.Parallel()
	path := lockFilePath(t.TempDir(), "job")
	first := openLockFile(t, path)
	granted, err := lockRange(t, first, 0, testAllBytes, testAllBytes)
	if err != nil || !granted {
		t.Fatalf("the first LockFileEx = (%t, %v), want (true, nil)", granted, err)
	}
	defer unlockRange(t, first, 0, testAllBytes, testAllBytes)

	second := openLockFile(t, path)
	again, againErr := lockRange(t, second, 0, testAllBytes, testAllBytes)
	if againErr != nil {
		t.Fatalf("the second LockFileEx = %v, want ERROR_LOCK_VIOLATION", againErr)
	}
	if again {
		t.Fatal("a second handle in the same process took a range another handle holds — the opposite of the documented rule, and the backend's contention answer would be wrong")
	}
}

// TestLockFileExRefusesTheSameHandleReLocking is the direct counterpart of
// flock(2)'s lock conversion, and it is the row where the two kernels differ
// most.
//
// flock(LOCK_EX) on a description that already holds LOCK_EX returns success
// immediately. LockFileEx on a range the SAME handle already holds is refused:
// "Exclusive locks cannot overlap an existing locked region of a file."
//
// The backend never re-locks a handle it holds — it opens one handle per
// acquisition — but a future refactor towards the natural, efficient
// one-handle-for-the-locker's-lifetime design would turn a silent no-op on
// Unix into a hard failure here. Both are wrong; only one is visible. The test
// records which is which.
func TestLockFileExRefusesTheSameHandleReLocking(t *testing.T) {
	t.Parallel()
	path := lockFilePath(t.TempDir(), "job")
	file := openLockFile(t, path)
	granted, err := lockRange(t, file, 0, testAllBytes, testAllBytes)
	if err != nil || !granted {
		t.Fatalf("the first LockFileEx = (%t, %v), want (true, nil)", granted, err)
	}
	defer unlockRange(t, file, 0, testAllBytes, testAllBytes)

	again, againErr := lockRange(t, file, 0, testAllBytes, testAllBytes)
	if againErr != nil {
		t.Fatalf("re-locking the same handle = %v, want ERROR_LOCK_VIOLATION", againErr)
	}
	if again {
		t.Fatal("re-locking the same handle succeeded — LockFileEx converted the lock the way flock(2) does, and this platform's row in the matrix is wrong")
	}
}

// TestTheRangeLockIsMandatoryAgainstAForeignHandle pins the difference the
// package comment names first: these locks are MANDATORY, not advisory.
//
// flock(2) excludes only other flock callers; anyone may read or write the
// file regardless. LockFileEx is enforced by the kernel against ordinary I/O,
// so a foreign handle's read of the locked region fails. That is why the
// backend locks the WHOLE file rather than a byte nobody uses: the fencing
// ledger lives at offset 0, and covering it is the only way the mandatory
// semantics buy anything the advisory ones do not.
func TestTheRangeLockIsMandatoryAgainstAForeignHandle(t *testing.T) {
	t.Parallel()
	path := lockFilePath(t.TempDir(), "job")
	holder := openLockFile(t, path)
	if _, err := holder.WriteAt([]byte("7\n"), 0); err != nil {
		t.Fatalf("seeding the ledger = %v", err)
	}
	granted, err := lockRange(t, holder, 0, testAllBytes, testAllBytes)
	if err != nil || !granted {
		t.Fatalf("LockFileEx = (%t, %v), want (true, nil)", granted, err)
	}
	defer unlockRange(t, holder, 0, testAllBytes, testAllBytes)

	foreign := openLockFile(t, path)
	var buf [8]byte
	if _, readErr := foreign.ReadAt(buf[:], 0); readErr == nil {
		t.Fatal("a foreign handle read the locked ledger — the lock is behaving as advisory, and the ledger has no more protection here than under flock(2)")
	}
	if _, writeErr := foreign.WriteAt([]byte("1\n"), 0); writeErr == nil {
		t.Fatal("a foreign handle overwrote the locked ledger — a non-holder can reset the fencing counter")
	}
}

// TestTheLedgerSurfaceWorksThroughTheLockingHandle is the check the fencing
// ledger needs and the one most likely to break.
//
// fence.go reads and writes the SAME file that carries the lock region,
// through exactly four operations — ReadAt, Truncate, WriteAt, Sync — and
// under a MANDATORY lock covering offset 0 every one of them is I/O on a
// locked range. The documented exemption is per handle: the handle that placed
// the lock keeps full access. If any of the four were refused, the backend
// would take the lock and then fail to mint a fence on every acquisition.
func TestTheLedgerSurfaceWorksThroughTheLockingHandle(t *testing.T) {
	t.Parallel()
	path := lockFilePath(t.TempDir(), "job")
	file := openLockFile(t, path)
	if _, err := file.WriteAt([]byte("41\n"), 0); err != nil {
		t.Fatalf("seeding the ledger = %v", err)
	}
	granted, err := lockRange(t, file, 0, testAllBytes, testAllBytes)
	if err != nil || !granted {
		t.Fatalf("LockFileEx = (%t, %v), want (true, nil)", granted, err)
	}
	defer unlockRange(t, file, 0, testAllBytes, testAllBytes)

	var buf [64]byte
	read, readErr := file.ReadAt(buf[:], 0)
	if readErr != nil && read == 0 {
		t.Fatalf("ReadAt through the locking handle = %v, want the ledger", readErr)
	}
	if string(buf[:read]) != "41\n" {
		t.Fatalf("ReadAt through the locking handle = %q, want %q", buf[:read], "41\n")
	}
	//: Truncate is SetEndOfFile on a range this handle holds a lock over — the
	//: one of the four with no documented exemption of its own.
	if truncErr := file.Truncate(0); truncErr != nil {
		t.Fatalf("Truncate through the locking handle = %v — the backend cannot shorten its own ledger and a smaller token would leave the tail of a larger one behind", truncErr)
	}
	if _, writeErr := file.WriteAt([]byte("42\n"), 0); writeErr != nil {
		t.Fatalf("WriteAt through the locking handle = %v", writeErr)
	}
	if syncErr := file.Sync(); syncErr != nil {
		t.Fatalf("Sync through the locking handle = %v — a fence that is not durable before the lease is handed out is reissued after a crash", syncErr)
	}
}

// TestAHighOffsetRangeLeavesTheLedgerUnprotected measures the alternative
// range the backend did NOT take, so the ADR's rejection rests on this kernel
// rather than on reasoning.
//
// Locking one byte far past end-of-file is legal ("Locking a region that goes
// beyond the current end-of-file position is not an error") and it keeps the
// ledger readable by anyone during an incident — the property the whole-file
// range costs. What it gives up is measured here: a foreign handle can still
// rewrite the counter, so the mandatory semantics are paid for and not used.
func TestAHighOffsetRangeLeavesTheLedgerUnprotected(t *testing.T) {
	t.Parallel()
	path := lockFilePath(t.TempDir(), "job")
	holder := openLockFile(t, path)
	if _, err := holder.WriteAt([]byte("7\n"), 0); err != nil {
		t.Fatalf("seeding the ledger = %v", err)
	}
	granted, err := lockRange(t, holder, testHighOffset, 1, 0)
	if err != nil || !granted {
		t.Fatalf("LockFileEx past end-of-file = (%t, %v), want (true, nil)", granted, err)
	}
	defer unlockRange(t, holder, testHighOffset, 1, 0)

	//: exclusion still works — the range is what excludes, not the bytes.
	second := openLockFile(t, path)
	again, againErr := lockRange(t, second, testHighOffset, 1, 0)
	if againErr != nil {
		t.Fatalf("the second LockFileEx past end-of-file = %v", againErr)
	}
	if again {
		t.Fatal("a high-offset range did not exclude a second handle")
	}
	//: and the ledger is wide open, which is the whole cost of this choice.
	if _, writeErr := second.WriteAt([]byte("1\n"), 0); writeErr != nil {
		t.Fatalf("a foreign handle could not rewrite the unlocked ledger (%v) — the measurement this test exists to make no longer holds", writeErr)
	}
}

// TestTryAcquireLosesToAForeignHolder pins the half the range lock really does
// provide: exclusion against a handle this locker does not own.
func TestTryAcquireLosesToAForeignHolder(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	locker := newFileLocker(t, dir)
	release := holdForeignRangeLock(t, lockFilePath(dir, "job"))
	defer release()
	lease, held, err := locker.TryAcquire(t.Context(), "job")
	if err != nil {
		t.Fatalf("TryAcquire = %v, want no error", err)
	}
	if held || lease != nil {
		t.Fatal("TryAcquire granted a lock another handle holds")
	}
}

// TestAcquireHonoursItsContextWhileAForeignHolderWaitsOut pins the reason the
// locker polls instead of asking the kernel to block.
//
// LockFileEx without LOCKFILE_FAIL_IMMEDIATELY parks the calling thread inside
// a wait no context cancellation can reach, exactly as a blocking flock(2)
// does, so an Acquire with a deadline would ignore it entirely. The poll loop
// is what makes ctx mean something, and it runs on the injected clock so this
// test never sleeps.
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
	release := holdForeignRangeLock(t, lockFilePath(dir, "job"))
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
// of the same loop: the poll fires, the range is now free, and the waiter gets
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
	release := holdForeignRangeLock(t, lockFilePath(dir, "job"))

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
