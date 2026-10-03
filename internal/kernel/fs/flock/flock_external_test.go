package flock_test

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/fs/flock"
)

// This file carries NO build constraint: every test below asserts something on
// every GOOS the package compiles for — the lock's behaviour where [flock.Native]
// is true, and errors.ErrUnsupported from every call where it is false. Nothing
// is skipped, so a lane that runs it on a kernel without a file lock (illumos,
// Solaris) proves the refusal rather than reporting a green tick for nothing.

// openDescription opens path as a NEW open file description — the unit both
// kernels lock — and closes it with the test.
func openDescription(t *testing.T, path string) *os.File {
	t.Helper()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("opening %s = %v", path, err)
	}
	t.Cleanup(func() {
		//: an already-closed file reports ErrClosed, which is not a failure of
		//: a test that closed it on purpose.
		if closeErr := file.Close(); closeErr != nil && !errors.Is(closeErr, os.ErrClosed) {
			t.Errorf("closing %s = %v", path, closeErr)
		}
	})
	return file
}

// mustTryLock asserts one TryLock answer, both halves of it.
func mustTryLock(t *testing.T, what string, file *os.File, want bool) {
	t.Helper()
	held, err := flock.TryLock(file)
	//: contention is an ANSWER: an error here is a failure of the call, never
	//: "held elsewhere".
	if err != nil {
		t.Fatalf("%s: TryLock = (%v, %v), want (%v, nil)", what, held, err, want)
	}
	if held != want {
		t.Fatalf("%s: TryLock held = %v, want %v", what, held, want)
	}
}

// mustUnlock asserts a release succeeded.
func mustUnlock(t *testing.T, what string, file *os.File) {
	t.Helper()
	if err := flock.Unlock(file); err != nil {
		t.Fatalf("%s: Unlock = %v", what, err)
	}
}

// assertUnsupported pins the answer every call gives where there is no
// primitive: errors.ErrUnsupported, and never a held lock.
func assertUnsupported(t *testing.T, file *os.File) {
	t.Helper()
	held, err := flock.TryLock(file)
	if held || !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("TryLock on %s = (%v, %v), want (false, ErrUnsupported)", runtime.GOOS, held, err)
	}
	if unlockErr := flock.Unlock(file); !errors.Is(unlockErr, errors.ErrUnsupported) {
		t.Fatalf("Unlock on %s = %v, want ErrUnsupported", runtime.GOOS, unlockErr)
	}
}

// TestNativeIsTrueExactlyWhereAKernelLockExists pins the platform matrix the
// package comment states: flock(2) on Linux and the BSDs (android and ios
// through the linux and darwin tags), LockFileEx on Windows, and nothing
// anywhere else. A caller's whole refusal rests on this one constant.
func TestNativeIsTrueExactlyWhereAKernelLockExists(t *testing.T) {
	t.Parallel()
	want := false
	switch runtime.GOOS {
	//: flock(2) — android and ios through the linux and darwin tags — and
	//: LockFileEx.
	case "linux", "android", "darwin", "ios", "freebsd", "openbsd", "netbsd", "dragonfly", "windows":
		want = true
	}
	if flock.Native != want {
		t.Fatalf("Native on %s = %v, want %v", runtime.GOOS, flock.Native, want)
	}
}

// TestASecondDescriptionIsRefusedUntilTheFirstLetsGo is the exclusion itself,
// on the shape both kernels agree on: two separate opens of one file. The
// second is refused — as an answer, not an error — while the first holds the
// lock, and takes it once the first has let go.
func TestASecondDescriptionIsRefusedUntilTheFirstLetsGo(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "a.lock")
	first, second := openDescription(t, path), openDescription(t, path)
	//: no primitive, no lock — and a uniform answer saying so.
	if !flock.Native {
		assertUnsupported(t, first)
		return
	}
	mustTryLock(t, "the first description", first, true)
	mustTryLock(t, "a second description while the first holds it", second, false)
	mustUnlock(t, "the first description", first)
	mustTryLock(t, "the second description once the first let go", second, true)
	mustUnlock(t, "the second description", second)
}

// TestTheSameDescriptionIsConvertedOnUnixAndRefusedOnWindows pins the row where
// the two kernels are opposites (ADR 0081): a second lock through the very
// description that holds it is a conversion that returns at once under
// flock(2), and ERROR_LOCK_VIOLATION under LockFileEx — read here, as
// everywhere, as "held elsewhere".
//
// It is the reason the package promises nothing about goroutines sharing one
// descriptor: on Unix they would all be "inside" at once, on Windows the
// second would be told the lock is busy. Either way the descriptor is released
// by ONE Unlock, which the last two lines prove by handing the lock on.
func TestTheSameDescriptionIsConvertedOnUnixAndRefusedOnWindows(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "a.lock")
	holder, other := openDescription(t, path), openDescription(t, path)
	//: no primitive, no lock — and a uniform answer saying so.
	if !flock.Native {
		assertUnsupported(t, holder)
		return
	}
	mustTryLock(t, "the first lock", holder, true)
	//: the opposite answers, each a measurement of its own kernel.
	mustTryLock(t, "the same description locked again", holder, runtime.GOOS != "windows")
	mustUnlock(t, "the holder", holder)
	mustTryLock(t, "another description after one Unlock", other, true)
	mustUnlock(t, "the other description", other)
}

// TestAFailedCallIsAnErrorAndNeverContention drives the third answer, on a
// descriptor that is no longer one: the kernel refuses the call (EBADF on Unix,
// an invalid handle on Windows), and that must come back as an error — read as
// "held elsewhere", a caller polling for the lock would wait forever on a file
// it can never lock.
func TestAFailedCallIsAnErrorAndNeverContention(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "a.lock")
	closed := openDescription(t, path)
	if err := closed.Close(); err != nil {
		t.Fatalf("closing the descriptor = %v", err)
	}
	held, err := flock.TryLock(closed)
	//: not held, and an error rather than the contention answer.
	if held || err == nil {
		t.Fatalf("TryLock on a closed descriptor = (%v, %v), want (false, an error)", held, err)
	}
	//: the release half refuses the same descriptor the same way.
	if unlockErr := flock.Unlock(closed); unlockErr == nil {
		t.Fatalf("Unlock on a closed descriptor = nil, want an error")
	}
}
