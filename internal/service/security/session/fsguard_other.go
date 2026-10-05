//go:build !(linux || darwin || freebsd || openbsd || netbsd || dragonfly)

package session

import "io/fs"

// unsupportedPlatform is what [plantable] reports having read where it cannot
// read anything.
const unsupportedPlatform string = "unsupported-platform"

// platformNative reports that this GOOS lacks at least one of the mechanics
// the file store requires. NewFileStore reads it and refuses at CONSTRUCTION,
// so the refusal arrives where the program is wired rather than at the first
// login.
const platformNative bool = false

// plantable answers "plantable" for every directory. It is unreachable — the
// constructor refuses this platform before it audits a path — and it fails
// CLOSED rather than guessing, because on Windows os.Stat synthesises 0777 for
// every writable directory: a mode says nothing there, and the question has an
// answer only in a DACL this file does not read.
func plantable(_ fs.FileMode) (yes bool, observed string) {
	//: the refusal is the one answer that cannot be wrong in the direction
	//: that matters.
	return true, unsupportedPlatform
}
