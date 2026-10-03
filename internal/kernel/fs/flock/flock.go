// Package flock takes an exclusive lock on a whole file WITHOUT waiting for it,
// and gives it back: flock(2) on the Unix kernels that have it, LockFileEx over
// every byte of the file on Windows (ADR 0081, ADR 0159).
//
// # Never a blocking call
//
// [TryLock] answers at once — held, held elsewhere, or a failure of the call —
// and there is no blocking variant. That is the point of the package rather
// than a gap in it. A blocking flock(2), and a LockFileEx without
// LOCKFILE_FAIL_IMMEDIATELY, park the calling thread inside a syscall no
// context cancellation can reach: a caller whose deadline expired would keep
// waiting for a lock it no longer wants, and its goroutine would not come back
// until some other process let go. A caller that must wait polls TryLock on a
// clock of its own, which is how every file lock in the SDK waits (ADR 0052,
// ADR 0073).
//
// # One contract over two primitives, and the row where they are opposites
//
// Both kernels give the same answer where exclusion between PROCESSES is
// decided: a second open description of the file — another process, or a
// second os.OpenFile of the same path in this one — is refused while the first
// holds the lock, and the holder's death releases it. They give OPPOSITE
// answers to a second lock taken through the SAME description: flock(2)
// converts it and returns at once, LockFileEx refuses it. So neither kernel
// excludes the goroutines of one process that share a descriptor in a way a
// caller could rely on everywhere, and this package does not pretend to: a
// caller that shares one descriptor between goroutines puts a gate of its own
// in front of TryLock. TestTheSameDescriptionIsConvertedOnUnixAndRefusedOnWindows
// pins both answers.
//
// The Windows lock is also MANDATORY where flock(2) is advisory, and it covers
// a byte range — here every byte of the file. flock_windows.go states each
// difference and what it was measured on; what a caller builds on them is the
// caller's to say.
//
// # Where neither exists
//
// [Native] is false on every GOOS with neither primitive — js, wasip1, plan9,
// aix, solaris, illumos — and there TryLock and Unlock return
// errors.ErrUnsupported. A caller reads Native where it is wired and refuses
// there, under its own code (ADR 0018 §(a)); the package compiles on every
// GOOS so that the refusal can be written at all.
//
// # What it returns
//
// The kernel's own answer. Contention is (false, nil), never an error; any
// other failure is the errno itself, unwrapped, so a caller wraps it once in
// its own vocabulary — the convention pathchain set for this family. The
// package owns no error code.
package flock

import "os"

// TryLock takes an exclusive lock on the whole of file without waiting.
//
// It reports (true, nil) when the lock is now held through file, (false, nil)
// when another open description holds it, and (false, err) when the call
// itself failed: the kernel's errno, or errors.ErrUnsupported where [Native] is
// false. Contention is an answer and never an error, so a caller cannot mistake
// a busy lock for a broken one, nor the reverse.
func TryLock(file *os.File) (held bool, err error) {
	//: the platform's primitive, chosen by build tag.
	return tryLock(file)
}

// Unlock releases the lock [TryLock] took through file.
//
// Closing the descriptor would release it too. Unlocking explicitly first
// keeps the two events apart on purpose: the lock is given up while the
// descriptor is still valid, so a failure to release is reportable rather than
// hidden behind a close that "worked".
func Unlock(file *os.File) error {
	//: the platform's primitive, chosen by build tag.
	return unlock(file)
}
