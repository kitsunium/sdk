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
//
// Package flock — the honest answer on the platforms with no file lock at all
// (ADR 0018 §(a): a uniform typed answer where a platform has no native
// mechanic, never a silent drop and never a build break).
//
// This set used to include Windows, on the argument that LockFileEx is not
// flock(2): it locks a byte range rather than a file, its locks are mandatory
// rather than advisory, and its rules for a second lock inside one process
// differ again. All three are true, and all three are measured on a real
// Windows kernel rather than recited. What was wrong was the conclusion — that
// a primitive behaving *almost* like flock is worth less than none. One whose
// every difference is measured, written down, and pinned by a test that fails
// when it changes is not an approximation; it is a second implementation of the
// same contract, and it is flock_windows.go (ADR 0081).
//
// What is left here is the set of GOOS values with no equivalent primitive in
// Go's syscall package — js, wasip1, plan9, aix, solaris and illumos. They get
// errors.ErrUnsupported from every call, and a caller that read [Native] at
// construction never makes one.
//
// Package flock — flock(2), on the kernels that have it.
//
// flock(2) is not POSIX. It exists on Linux and on every BSD (macOS included)
// with the same shape, and the tag set above is exactly where Go's syscall
// package declares it — android and ios reach this file through the linux and
// darwin tags. The lock belongs to the open file DESCRIPTION: a dup(2)'d
// descriptor shares it, a second open(2) of the same path does not, and the
// kernel drops it when the last descriptor on the description closes — which a
// process's death does. It is ADVISORY: it binds every process that takes it,
// and nothing else.
//
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
