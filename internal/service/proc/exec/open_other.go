//go:build !(linux && !mips && !mipsle && !mips64 && !mips64le) && !windows

package exec

import (
	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

// openProcess refuses: darwin, the BSDs, illumos, Solaris and Linux on mips
// have no process handle the stdlib reaches.
func openProcess(_ int) (coreproc.Process, error) {
	//: no race-free handle to hold.
	return nil, coreproc.UnsupportedPlatform
}
