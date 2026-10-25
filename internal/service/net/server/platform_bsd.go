//go:build darwin || dragonfly || freebsd || netbsd || openbsd

package server

import "syscall"

// kernelRelease is sysctl kern.osrelease, "24.6.0" on darwin say; empty when
// the call fails.
func kernelRelease() string {
	release, err := syscall.Sysctl("kern.osrelease")
	//: unreadable counts as unknown.
	if err != nil {
		//: unknown.
		return ""
	}
	//: the release.
	return release
}
