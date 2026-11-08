//go:build (linux && !mips && !mipsle && !mips64 && !mips64le) || windows

package exec

import (
	"syscall"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// wrapForeignWait is the WaitFailed an opened process answers: only a parent
// collects an exit status, so there is none to wait for.
func wrapForeignWait(pid int) error {
	//: ECHILD is what wait4 says of a process that is not a child.
	return wrapWait(syscall.ECHILD, errs.Int("pid", pid), errs.String("why", "not a child of this process"))
}
