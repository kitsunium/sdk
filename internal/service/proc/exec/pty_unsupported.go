//go:build !linux && !darwin && !freebsd && !openbsd

package exec

import (
	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

// buildPTYStdio is never reached with a terminal to build: checkPTY (Unix) and
// checkUnsupportedSpec (Windows) refused StdioPTY before the stdio was wired.
func buildPTYStdio(_ *stdioState, _ *coreproc.PTYValue) error {
	//: no pseudo-terminal this package can allocate here.
	return coreproc.UnsupportedPlatform
}
