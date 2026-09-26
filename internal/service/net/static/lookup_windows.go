//go:build windows

// Package static — the lookup failures Windows reports for a name.
package static

import (
	"errors"
	"syscall"
)

// The Win32 errors CreateFile answers for a name it cannot hold. syscall
// exports none of them; ERROR_PATH_NOT_FOUND, which Windows answers for a file
// used as a directory, already reads as fs.ErrNotExist.
const (
	// errorInvalidName is ERROR_INVALID_NAME: a character a file name cannot
	// hold — * ? " < > | — or a component too long.
	errorInvalidName syscall.Errno = 123
	// errorBadPathname is ERROR_BAD_PATHNAME.
	errorBadPathname syscall.Errno = 161
	// errorFilenameExcedRange is ERROR_FILENAME_EXCED_RANGE: a name past the
	// length the file system holds.
	errorFilenameExcedRange syscall.Errno = 206
	// errorDirectory is ERROR_DIRECTORY: a directory name that is not valid.
	errorDirectory syscall.Errno = 267
)

// platformNameRefused reports the refusals of a name Windows does not report
// as "does not exist".
func platformNameRefused(err error) bool {
	//: each is the name's fault, never the tree's.
	return errors.Is(err, errorInvalidName) || errors.Is(err, errorBadPathname) ||
		errors.Is(err, errorFilenameExcedRange) || errors.Is(err, errorDirectory)
}
