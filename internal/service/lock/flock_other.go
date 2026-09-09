//go:build !(linux || darwin || freebsd || openbsd || netbsd || dragonfly)

// Package lock — the honest refusal on platforms without flock(2) (ADR 0018
// §(a): a uniform typed sentinel where a platform has no native mechanic,
// never a silent drop and never a build break).
//
// # Why Windows is refused rather than approximated
//
// Windows has LockFileEx, and it is tempting to call it the same thing. It is
// not. LockFileEx locks a BYTE RANGE rather than a file, and its locks are
// MANDATORY rather than advisory: the kernel enforces them against reads and
// writes, so a range lock changes the behaviour of unrelated I/O on the same
// file. A file-scope emulation would also have to decide what happens when the
// same process opens the file twice, where LockFileEx's rules differ again.
//
// Every one of those differences is a place where the emulation would behave
// almost like flock. "Almost" is the whole problem: a lock is the one
// primitive whose failures are invisible at the moment they happen and
// expensive at every later moment. A backend that excluded correctly in
// testing and not under one particular interleaving is worth less than no
// backend at all, because the caller would have stopped looking.
//
// So the constructor returns the typed proc.UnsupportedPlatform and the caller
// picks the in-process locker, an external coordinator, or another host. The
// gap is real, has a known closure — a LockFileEx backend with its own
// semantics documented and its own tests — and is listed in this package's
// CLAUDE.md §Platform matrix rather than left to be discovered.
package lock

import (
	"os"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

// platformNative reports that this GOOS has no flock(2). NewFileLocker reads
// it and refuses at CONSTRUCTION, so the refusal arrives where the program is
// wired rather than at the first contended section.
const platformNative bool = false

// flockTry reports the shared UnsupportedPlatform sentinel. It is unreachable
// in practice — the constructor refuses first — and exists so the package
// compiles on every GOOS, which is ADR 0018's build bar.
func flockTry(_ *os.File) (held bool, err error) {
	//: the same typed answer every unimplemented primitive gives, everywhere.
	return false, coreproc.UnsupportedPlatform
}

// flockUnlock reports the shared UnsupportedPlatform sentinel, as [flockTry]
// does and for the same reason.
func flockUnlock(_ *os.File) error {
	//: uniform contract even where the capability is absent.
	return coreproc.UnsupportedPlatform
}
