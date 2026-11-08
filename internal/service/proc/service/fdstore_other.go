//go:build !linux

package service

import (
	"os"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

// The descriptor store is Linux's (resolveStore refuses it elsewhere): these
// are never reached, and say so when they are.

// hangupWatch is no watch off Linux.
type hangupWatch struct{}

// newHangupWatch has no epoll to open.
func newHangupWatch() (*hangupWatch, error) {
	//: no watch.
	return nil, coreproc.UnsupportedPlatform
}

// add watches nothing.
func (*hangupWatch) add(*os.File, uint64) bool {
	//: unwatched.
	return false
}

// remove has nothing to remove.
func (*hangupWatch) remove(*os.File) {}

// start has nothing to watch.
func (*hangupWatch) start(func(uint64) bool) {}

// stop has nothing to stop.
func (*hangupWatch) stop() {}

// sameFile tells nothing apart off Linux.
func sameFile(_, _ *os.File) bool {
	//: never the same.
	return false
}

// fileID names nothing off Linux.
func fileID(os.FileInfo) (device, inode uint64) {
	//: unknown.
	return 0, 0
}
