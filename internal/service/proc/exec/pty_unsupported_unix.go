//go:build unix && !linux && !darwin && !freebsd

package exec

import (
	"os"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// checkPTY refuses StdioPTY: OpenBSD's PTMGET ioctl is refused outside libc
// (pinsyscalls), NetBSD and DragonFly are unmeasured, and illumos and Solaris
// unlock a terminal with libc's STREAMS pushes, which the stdlib cannot make.
func checkPTY(spec coreproc.Spec) error {
	//: only the terminal mode is refused.
	if spec.Stdio == coreproc.StdioPTY {
		//: refused before anything is spawned.
		return coreproc.UnsupportedPlatform
	}
	//: every other mode spawns.
	return nil
}

// TTY is nil: no child here has a terminal.
func (h *handle) TTY() *os.File {
	//: StdioPTY was refused.
	return nil
}

// Resize is PTY_FAILED: no child here has a terminal.
func (h *handle) Resize(_, _ uint16) error {
	//: StdioPTY was refused.
	return wrapPTYFailed(nil, errs.Int("pid", h.pid), errs.String("why", "not started with StdioPTY"))
}

// Size is PTY_FAILED: no child here has a terminal.
func (h *handle) Size() (coreproc.PTYValue, error) {
	//: StdioPTY was refused.
	return coreproc.PTYValue{}, wrapPTYFailed(nil, errs.Int("pid", h.pid), errs.String("why", "not started with StdioPTY"))
}
