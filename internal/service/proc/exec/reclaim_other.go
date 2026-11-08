//go:build !linux

package exec

import (
	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// reclaimChild is refused off Linux: no /proc names a child's parent there.
func reclaimChild(pid int) (coreproc.Process, error) {
	//: UNSUPPORTED_PLATFORM naming the call.
	return nil, errs.Wrap(coreproc.UnsupportedPlatform, errs.WrapParams{}, errs.String("op", "Reclaim"), errs.Int("pid", pid))
}
