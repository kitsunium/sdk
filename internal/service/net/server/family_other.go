//go:build !windows

// Package server — every served socket family is left to the kernel here.
package server

// platformLacks reports whether this platform has no socket for a family the
// engine serves everywhere else. Off Windows the answer is left to the kernel:
// a family it refuses fails the bind and says why, as it always has.
func platformLacks(_ string) bool {
	//: nothing is refused ahead of the bind on this platform.
	return false
}
