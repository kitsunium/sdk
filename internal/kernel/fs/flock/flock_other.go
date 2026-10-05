//go:build !(linux || darwin || freebsd || openbsd || netbsd || dragonfly || windows)

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
