//go:build !(linux || darwin || freebsd || openbsd || netbsd || dragonfly || windows)

// Package lock — the plain open on the platforms that have no file lock at all
// (ADR 0018 §(a)).
//
// js/wasm, plan9, aix, solaris and ios reach this file. [NewFileLocker]
// refuses them at CONSTRUCTION through platformNative, so nothing here is
// reachable; it exists because the package must COMPILE on every GOOS, which
// is ADR 0018's build bar, and because the build-tag set is the one
// flock_other.go already uses rather than a fourth shape invented here.
//
// Solaris does have O_NOFOLLOW and is nevertheless served by this file. That
// is deliberate: giving it the Unix open would mean the tag sets of
// flock_*.go and nofollow_*.go disagree, which is how a platform ends up with
// one half of a pair. A platform gains both files or neither, and it gains
// them by acquiring a working flock backend first.
package lock

import "os"

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
