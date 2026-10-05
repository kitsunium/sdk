//go:build !(linux || darwin || freebsd || openbsd || netbsd || dragonfly || windows)

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
