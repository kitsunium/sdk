//go:build !(linux || darwin || freebsd || openbsd || netbsd || dragonfly || windows)

// Package lock — the plain open on the platforms that have no file lock at all
// (ADR 0018 §(a)).
//
// js, wasip1, plan9, aix, solaris and illumos reach this file. [NewFileLocker]
// refuses them at CONSTRUCTION through platformNative, so nothing here is
// reachable; it exists because the package must COMPILE on every GOOS, which
// is ADR 0018's build bar, and because the build-tag set is the one the
// kernel's file lock uses for its own refusal (internal/kernel/fs/flock's
// flock_other.go) rather than a fourth shape invented here.
//
// Solaris does have O_NOFOLLOW and is nevertheless served by this file. That
// is deliberate: giving it the Unix open would mean the kernel lock's tag sets
// and nofollow_*.go's disagree, which is how a platform ends up with one half
// of a pair. A platform gains both or neither, and it gains them by acquiring
// a working file lock first — TestTheLockAndItsHardeningShareAPlatform fails
// the day the two sets part.
package lock

import "os"

// hardenedOpen reports that this GOOS has no open the file locker trusts to
// refuse an indirection — and, as TestTheLockAndItsHardeningShareAPlatform
// pins, no file lock either, so platformNative is false here twice over.
const hardenedOpen bool = false

// openLockFile opens the lock file with no protection against an indirection
// planted at path, because no such protection is portable here and the
// constructor has already refused this platform.
func openLockFile(path string) (file *os.File, err error) {
	opened, openErr := os.OpenFile(path, os.O_CREATE|os.O_RDWR, lockFileMode)
	//: the medium refused.
	if openErr != nil {
		//: LOCK_BACKEND_FAILED.
		return nil, backendFailed("open", path, openErr)
	}
	//: the caller owns the returned descriptor — unreachable in practice,
	//: uniform in shape.
	return opened, nil
}
