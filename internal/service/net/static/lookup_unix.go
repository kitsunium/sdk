//go:build unix

// Package static — the lookup failures a Unix kernel reports for a name.
package static

import (
	"errors"
	"syscall"
)

// platformNameRefused reports the two refusals of a name a Unix kernel does
// not report as "does not exist": a component that is a file used as a
// directory, and a component longer than the file system holds.
func platformNameRefused(err error) bool {
	//: ENOTDIR for "index.html/x", ENAMETOOLONG past NAME_MAX.
	return errors.Is(err, syscall.ENOTDIR) || errors.Is(err, syscall.ENAMETOOLONG)
}
