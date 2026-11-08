//go:build !(linux && !mips && !mipsle && !mips64 && !mips64le) && !darwin && !dragonfly && !freebsd && !netbsd && !openbsd

package exec

import (
	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

// attachProcess refuses: illumos, Solaris and Windows have no handle this
// package reaches that observes the end of a process it did not spawn — Linux
// holds a pidfd, darwin and the BSDs a kqueue knote (#729).
func attachProcess(_ int, _ coreproc.AttachValue) (coreproc.Process, error) {
	//: no race-free handle to hold.
	return nil, coreproc.UnsupportedPlatform
}

// attachPIDFile refuses before reading anything: the file would name a
// process attachProcess cannot hold.
func attachPIDFile(_ string, _ coreproc.AttachValue) (coreproc.Process, error) {
	//: no race-free handle to hold.
	return nil, coreproc.UnsupportedPlatform
}
