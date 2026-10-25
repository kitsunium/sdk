//go:build !linux && !darwin && !dragonfly && !freebsd && !netbsd && !openbsd && !windows

package server

// kernelRelease is empty: on illumos and Solaris the standard library's
// syscall package reads no release, and the SDK imports nothing else (ADR
// 0156).
func kernelRelease() string {
	//: unknown.
	return ""
}
