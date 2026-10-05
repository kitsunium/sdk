//go:build windows

package flock

import (
	"errors"
	"os"
	"syscall"
	"unsafe"
)

// Native reports that this GOOS has a file lock: LockFileEx, here.
const Native bool = true

// kernel32 range-locking entry points, bound lazily (resolved on first Call).
// Both are stable kernel32 exports present since Windows NT 3.1, bound with
// syscall.NewLazyDLL and no golang.org/x/sys — the ADR 0018 discipline, and
// the same one internal/service/proc/{exec,cgroup,signal} already follow.
var (
	modKernel32      = syscall.NewLazyDLL("kernel32.dll")
	procLockFileEx   = modKernel32.NewProc("LockFileEx")
	procUnlockFileEx = modKernel32.NewProc("UnlockFileEx")
)

// LockFileEx flags (winbase.h) and the winerror.h code that means contention.
// Hand-declared and cited, never imported.
const (
	// lockfileFailImmediately is LOCKFILE_FAIL_IMMEDIATELY. Without it the
	// call parks the calling thread inside a wait no context cancellation can
	// reach, exactly as a blocking flock(2) does — see the package comment.
	lockfileFailImmediately uintptr = 0x00000001
	// lockfileExclusiveLock is LOCKFILE_EXCLUSIVE_LOCK. Its absence is a
	// SHARED lock, which would let every caller in.
	lockfileExclusiveLock uintptr = 0x00000002
	// allBytes is MAXDWORD, passed as both halves of the length so the range
	// is the whole 2^64-1-byte file from offset zero.
	allBytes uintptr = 0xFFFFFFFF
	// errorLockViolation is ERROR_LOCK_VIOLATION, the documented answer when
	// LOCKFILE_FAIL_IMMEDIATELY is set and the range is held elsewhere.
	errorLockViolation syscall.Errno = 33
)

// tryLock is [TryLock] over LockFileEx: an exclusive range lock on the whole
// of file, without waiting.
//
// Only ERROR_LOCK_VIOLATION is read as contention. ERROR_IO_PENDING — which a
// LockFileEx on an ASYNCHRONOUS handle can return — is deliberately NOT, and
// the distinction matters: it means the request is still outstanding and may
// be granted later, so reading it as "held elsewhere" would hand the caller a
// refusal while the kernel went on to give this process the lock. os.OpenFile
// never opens an asynchronous handle, so the case is unreachable; treating it
// as a failure is what keeps it unreachable rather than silently wrong.
func tryLock(file *os.File) (held bool, err error) {
	//: the range starts at offset zero, so the zero value is the whole of it.
	//: It is passed by pointer into a syscall; LazyProc.Call carries
	//: //go:uintptrescapes, which is what keeps the value alive across it.
	overlapped := new(syscall.Overlapped)
	r1, _, errno := procLockFileEx.Call(
		uintptr(file.Fd()),
		lockfileExclusiveLock|lockfileFailImmediately,
		0,
		allBytes,
		allBytes,
		uintptr(unsafe.Pointer(overlapped)),
	)
	//: a non-zero return is the grant. Call's error is never nil — it is
	//: Errno(0) on success — so the return value is what decides.
	if r1 != 0 {
		//: acquired.
		return true, nil
	}
	//: the ordinary "someone else has it" answer, not a fault.
	if errors.Is(errno, errorLockViolation) {
		//: held elsewhere.
		return false, nil
	}
	//: anything else is a real failure of the call.
	return false, errno
}

// unlock is [Unlock] over UnlockFileEx.
//
// The range must match the one taken exactly. Windows refuses a partial
// unlock, which is a property worth having here: a range that drifted from
// [tryLock]'s would fail loudly instead of leaving a lock behind.
func unlock(file *os.File) error {
	overlapped := new(syscall.Overlapped)
	r1, _, errno := procUnlockFileEx.Call(
		uintptr(file.Fd()),
		0,
		allBytes,
		allBytes,
		uintptr(unsafe.Pointer(overlapped)),
	)
	//: released.
	if r1 != 0 {
		//: nothing to report.
		return nil
	}
	//: the medium refused to give the range back.
	return errno
}
