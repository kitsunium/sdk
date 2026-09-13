//go:build windows

// Package lock — the one operating-system mechanic the file locker needs, on
// the platform that does not have it (ADR 0081).
//
// There is no flock(2) on Windows. LockFileEx is the nearest primitive and it
// is genuinely a different one, so this file states every difference rather
// than smoothing it over. A lock is the single primitive whose failures are
// invisible at the moment they happen and expensive at every later moment, and
// an emulation that behaves almost like flock is worth less than no emulation,
// because the caller stops looking.
//
// # It locks a byte RANGE, not a file
//
// A range therefore has to be chosen, and the choice is the WHOLE file: offset
// zero, 2^64-1 bytes, spelled MAXDWORD in both length halves — the documented
// idiom for "everything", and legal past end-of-file ("Locking a region that
// goes beyond the current end-of-file position is not an error").
//
// The alternative — one byte at an offset nothing will ever occupy — keeps the
// lock file readable by anyone during an incident, and is rejected. The
// fencing ledger lives at offset 0 of this very file, and a range that did not
// cover it would leave the counter writable by every non-holder. Covering it
// is the only way the mandatory semantics below buy anything at all;
// TestAHighOffsetRangeLeavesTheLedgerUnprotected measures exactly what the
// other choice gives up.
//
// # Its locks are MANDATORY, not advisory
//
// The kernel enforces them against ordinary reads and writes, so the range
// lock changes the behaviour of unrelated I/O on the same file. Two
// consequences, both deliberate:
//
//   - The fencing ledger is STRICTLY BETTER protected here than under
//     flock(2), where nothing stops a non-holder from rewriting the counter.
//   - A foreign `type` of a HELD lock file fails with ERROR_LOCK_VIOLATION.
//     fence.go writes the counter in decimal so it can be read during an
//     incident; on Windows that is true only while nobody holds the lock —
//     which is the incident where it is actually wanted, since a holder that
//     crashed is a holder whose lock the kernel has already released.
//
// The holder itself is exempt: the documented rule is per HANDLE, and the
// handle that placed the lock keeps full access to the range. That is what
// lets [readFence] and [writeFence] work through the very descriptor carrying
// the lock, and TestTheLedgerSurfaceWorksThroughTheLockingHandle pins all four
// operations they use.
//
// # A second open by the same process is REFUSED, where flock(2) CONVERTS
//
// This is the row where the two kernels are opposites, and it is the row ADR
// 0052 §D7 was written about. flock(LOCK_EX) on a description that already
// holds LOCK_EX succeeds immediately, which is why eight goroutines sharing
// one descriptor were all inside one section at once. LockFileEx on a range an
// overlapping lock already covers is refused, whichever handle asks and
// whichever process owns it.
//
// The file locker's in-process gate is therefore not load-bearing for
// exclusion here — it is load-bearing for everything else. It is what keeps
// one process's goroutines QUEUED on a channel rather than polling a range
// their own process holds; it is what makes the package's behaviour the same
// on both kernels, so a suite that passes on Linux means something here; and
// it is what keeps the natural "one handle for the locker's lifetime" refactor
// from turning a silent no-op on Unix into a hard failure on Windows. The gate
// stays, for a different reason than the one that put it there.
package lock

import (
	"errors"
	"os"
	"syscall"
	"unsafe"
)

// platformNative reports that this GOOS has a native file-range lock. The file
// locker's constructor reads it and builds a locker rather than refusing.
const platformNative bool = true

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
	// reach, exactly as a blocking flock(2) does, and Acquire's ctx would stop
	// meaning anything. The poll loop above this file is what makes it mean
	// something.
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

// flockTry takes an exclusive range lock on the whole of file without
// blocking. It reports (true, nil) when the lock is now held, (false, nil)
// when it is held elsewhere, and (false, err) when the call itself failed.
//
// Only ERROR_LOCK_VIOLATION is read as contention. ERROR_IO_PENDING — which a
// LockFileEx on an ASYNCHRONOUS handle can return — is deliberately NOT, and
// the distinction matters: it means the request is still outstanding and may
// be granted later, so reading it as "held elsewhere" would hand the caller a
// refusal while the kernel went on to give this process the lock. os.OpenFile
// never opens an asynchronous handle, so the case is unreachable; treating it
// as a failure is what keeps it unreachable rather than silently wrong.
func flockTry(file *os.File) (held bool, err error) {
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

// flockUnlock releases the range lock taken by [flockTry].
//
// Closing the handle would also release it, and the file locker closes it
// immediately afterwards. Unlocking explicitly first keeps the two events
// ordered on purpose: the lock is given up while the handle is still valid, so
// a failure to release is reportable rather than swallowed by a close that
// "worked".
//
// The range must match the one taken exactly. Windows refuses a partial
// unlock, which is a property worth having here: a range that drifted from
// [flockTry]'s would fail loudly instead of leaving a lock behind.
func flockUnlock(file *os.File) error {
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
