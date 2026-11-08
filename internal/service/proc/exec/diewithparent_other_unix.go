//go:build unix && !linux && !freebsd

package exec

import (
	"syscall"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

// checkDieWithParent refuses a non-zero DieWithParent: darwin, OpenBSD, NetBSD,
// DragonFly, illumos and Solaris have no parent-death signal the stdlib can
// set, and dropping the request would leave a child the caller believes is
// bound to the supervisor running after it.
func checkDieWithParent(sig coreproc.Signal) error {
	//: the zero Signal requests nothing, and nothing is refused.
	if sig == 0 {
		//: an ordinary spawn.
		return nil
	}
	//: refused before anything is spawned.
	return coreproc.UnsupportedPlatform
}

// setDieWithParent is never reached with a non-zero sig here:
// checkDieWithParent refused it before the spawn.
func setDieWithParent(_ *syscall.SysProcAttr, _ coreproc.Signal) {
	//: nothing to set on a kernel without the mechanism.
}
