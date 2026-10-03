//go:build !(linux || darwin || freebsd || openbsd || netbsd || dragonfly || windows)

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
package flock

import (
	"errors"
	"os"
)

// Native reports that this GOOS has no file lock. A caller reads it and
// refuses where it is wired, rather than at the first contended section.
const Native bool = false

// tryLock reports errors.ErrUnsupported: there is no primitive to call.
func tryLock(_ *os.File) (held bool, err error) {
	//: the standard library's own word for an operation this platform lacks.
	return false, errors.ErrUnsupported
}

// unlock reports errors.ErrUnsupported, as [tryLock] does and for the same
// reason.
func unlock(_ *os.File) error {
	//: uniform contract even where the capability is absent.
	return errors.ErrUnsupported
}
