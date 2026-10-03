//go:build !(linux || darwin || freebsd || openbsd || netbsd || dragonfly || windows)

// Package lock — the honest refusal on platforms with no file-range lock at
// all (ADR 0018 §(a): a uniform typed sentinel where a platform has no native
// mechanic, never a silent drop and never a build break).
//
// This file used to carry Windows, and the argument for keeping it here was
// that LockFileEx is not flock(2): it locks a byte range rather than a file,
// its locks are mandatory rather than advisory, and its rules for a second
// open by the same process differ again. All three are true, and all three are
// now measured on a real Windows kernel rather than recited. What was wrong
// was the conclusion — that a backend behaving *almost* like flock is worth
// less than none. A backend whose every difference is measured, written down,
// and pinned by a test that fails when it changes is not an approximation; it
// is a second implementation of the same contract. It landed in
// flock_windows.go, and ADR 0081 records what each difference cost.
//
// What is left here is the set of GOOS values with no equivalent primitive at
// all — js/wasm, plan9, aix, solaris, ios. For them the constructor returns
// the typed proc.UnsupportedPlatform and the caller picks the in-process
// locker, an external coordinator, or another host. The gap is listed in this
// package's CLAUDE.md §Platform matrix rather than left to be discovered.
package lock

import (
	"os"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

// platformNative reports that this GOOS has no file-range lock. NewFileLocker
// reads it and refuses at CONSTRUCTION, so the refusal arrives where the
// program is wired rather than at the first contended section.
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
