// Package session — the guard that keeps the store's platform gate inside the
// kernel lock's.
package session

import (
	"runtime"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/fs/flock"
)

// TestTheStoreIsBuiltOnlyWhereTheKernelCanLock pins the one relation between
// this package's platform gate and the kernel's: wherever NewFileStore builds a
// store, internal/kernel/fs/flock has a lock to serialise it with.
//
// The two sets are deliberately NOT equal — the kernel locks on Windows and
// this store refuses Windows for its permissions and its directory flush — so
// the assertion is an implication, not an equality. What it catches is a
// fsguard_unix.go tag set widened to a GOOS where the kernel has no lock: every
// operation there would fail with errors.ErrUnsupported at its first
// read-modify-write instead of being refused where the program is wired.
func TestTheStoreIsBuiltOnlyWhereTheKernelCanLock(t *testing.T) {
	t.Parallel()
	//: a store this GOOS builds, over a lock this GOOS does not have.
	if platformNative && !flock.Native {
		t.Fatalf("on %s the file store is built but the kernel has no file lock — every operation would fail at its first lock", runtime.GOOS)
	}
}
