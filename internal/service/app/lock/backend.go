// Package lock — the kernel lock the file locker stands on, and what each
// kernel's version of it means for THIS domain.
//
// The lock itself is internal/kernel/fs/flock: flock(2) on the Unix kernels,
// LockFileEx over every byte of the file on Windows (ADR 0081), and never a
// blocking call — Acquire polls it on the locker's own clock, so its context
// means something. That package states how the two kernels differ. What
// follows is what each difference costs or buys the file locker, which no
// primitive can say on its behalf.
//
// # The gate: load-bearing on Unix, kept on Windows for other reasons
//
// A second lock through the same descriptor is a CONVERSION under flock(2) and
// a REFUSAL under LockFileEx (ADR 0052 §D7 is about the first). So on Windows
// the in-process gate ([nameGate]) is not load-bearing for exclusion — the
// kernel already refuses a second goroutine — and it stays for everything
// else: it keeps one process's goroutines QUEUED on a channel rather than
// polling a range their own process holds; it makes the package behave the
// same on both kernels, so a suite that passes on Linux means something there;
// and it keeps the natural "one handle for the locker's lifetime" refactor
// from turning a silent no-op on Unix into a hard failure on Windows.
//
// # The fencing ledger is inside the locked range
//
// The Windows lock is a byte range, and the kernel primitive takes the whole
// file. That is the choice this domain needs: the ledger lives at offset 0 of
// the very file carrying the lock, and a range that skipped it — one byte at
// an offset nothing will ever occupy — would leave the counter writable by
// every non-holder; TestAHighOffsetRangeLeavesTheLedgerUnprotected measures
// what that alternative gives up. Because the lock is MANDATORY there, the
// ledger is strictly better protected than under flock(2), where nothing stops
// a non-holder from rewriting it, and the holder is exempt per HANDLE, so
// [readFence] and [writeFence] work through the descriptor that placed the
// lock (TestTheLedgerSurfaceWorksThroughTheLockingHandle). The price: a
// foreign `type` of a HELD lock file fails with ERROR_LOCK_VIOLATION, so the
// decimal ledger fence.go writes is readable during an incident only while
// nobody holds the lock — which is the incident it is wanted in, since a
// holder that crashed is a holder whose lock the kernel already released.
//
// # A platform gains the lock and its hardening together, or neither
//
// The lock path is a file, never a link to one (ADR 0082), and that is
// nofollow_*.go's to enforce. A platform with the kernel lock and without the
// hardened open would build a locker any planted link could redirect, so the
// gate below needs both halves, and TestTheLockAndItsHardeningShareAPlatform
// fails the day the kernel's tag set and nofollow_*.go's stop agreeing.
//
// Where neither exists — js, wasip1, plan9, aix, solaris, illumos — the
// constructor returns the typed proc.UnsupportedPlatform and the caller picks
// the in-process locker, an external coordinator, or another host (ADR 0018
// §(a)). The gap is listed in this package's CLAUDE.md §Platform matrix
// rather than left to be discovered.
package lock

import "github.com/kitsunium/sdk/internal/kernel/fs/flock"

// platformNative reports that this GOOS has both halves the file locker rests
// on: the kernel's file lock and an open that refuses an indirection planted
// at the lock path. NewFileLocker reads it and refuses at CONSTRUCTION where it
// is false, so the refusal arrives where the program is wired rather than at
// the first contended section — and no locker is ever built that would report
// success while excluding nothing.
const platformNative bool = flock.Native && hardenedOpen
