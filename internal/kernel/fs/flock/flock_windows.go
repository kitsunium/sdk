//go:build windows

// Package flock — LockFileEx, on the kernel that has no flock(2) (ADR 0081).
//
// LockFileEx is the nearest primitive and it is genuinely a different one, so
// this file states every difference rather than smoothing it over. A lock is
// the one primitive whose failures are invisible at the moment they happen and
// expensive at every later moment, and an emulation that behaves almost like
// flock(2) is worth less than none, because its caller stops looking. Each row
// was measured on a real windows-latest kernel before the first backend over it
// was written (ADR 0081):
//
//	                                       flock(2)                 LockFileEx
//	two separate opens, one process        excludes                 excludes
//	the SAME description locked again      succeeds — a conversion  refused — ERROR_LOCK_VIOLATION
//	another process while held             excludes                 excludes
//	enforced against unrelated I/O         no — advisory            yes — mandatory
//	scope                                  the whole file           a byte range: here, every byte
//
// # It locks a byte RANGE, so the range is the whole file
//
// Offset zero, 2^64-1 bytes, spelled MAXDWORD in both length halves — the
// documented idiom for "everything", and legal past end-of-file ("Locking a
// region that goes beyond the current end-of-file position is not an error").
// Any smaller range leaves the bytes outside it readable and writable by every
// other handle while the lock is held, which is a decision about the file's
// contents and so the caller's, not this package's.
//
// # Its locks are MANDATORY, not advisory
//
// The kernel enforces them against ordinary reads and writes: while the lock
// is held, another handle's read or write of the file fails with
// ERROR_LOCK_VIOLATION — a foreign `type` of a held file included. The holder
// is exempt: the documented rule is per HANDLE, and the handle that placed the
// lock keeps full access to the range, so the holder reads and writes the file
// through the very descriptor that carries the lock.
//
// # A second lock through the same handle is REFUSED, where flock(2) CONVERTS
//
// This is the row where the two kernels are opposites. flock(LOCK_EX) on a
// description that already holds LOCK_EX succeeds immediately; LockFileEx on a
// range an overlapping lock already covers is refused, whichever handle asks
// and whichever process owns it. Exclusion between the goroutines of one
// process therefore falls out of the kernel here and does not on Unix, and a
// caller that wants the same behaviour on both keeps a gate of its own.
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
