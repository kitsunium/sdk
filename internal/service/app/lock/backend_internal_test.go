package lock

import (
	"runtime"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/fs/flock"
)

// TestTheLockAndItsHardeningShareAPlatform pins ADR 0082's pairing across the
// package boundary the kernel lock now sits behind.
//
// While flock_*.go and nofollow_*.go lived side by side in this package,
// "identical build tags" was a fact a reader could see. The lock moved to
// internal/kernel/fs/flock, so the two tag sets are in two packages and
// nothing but this test keeps them equal: a GOOS added to the kernel's set
// alone would build a locker whose lock file any planted link could redirect,
// and one added to nofollow_*.go alone would harden a locker that cannot lock.
//
// It carries no build constraint and asserts on every GOOS — true and true on
// Unix and Windows, false and false on illumos and Solaris, where e2e-cross
// runs this package to prove the refusal.
func TestTheLockAndItsHardeningShareAPlatform(t *testing.T) {
	t.Parallel()
	//: the kernel's tag set and nofollow_*.go's, on this GOOS.
	if flock.Native != hardenedOpen {
		t.Fatalf("on %s the kernel file lock is %v and the hardened open is %v — a platform gains both or neither (ADR 0082)",
			runtime.GOOS, flock.Native, hardenedOpen)
	}
	//: and the constructor's gate is that agreement, nothing looser.
	if platformNative != flock.Native {
		t.Fatalf("on %s platformNative = %v, want %v", runtime.GOOS, platformNative, flock.Native)
	}
}
