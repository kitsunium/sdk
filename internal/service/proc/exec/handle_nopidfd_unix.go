//go:build unix && !(linux && !mips && !mipsle && !mips64 && !mips64le)

package exec

import (
	"os"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

// Handle reports no kernel handle: darwin, the BSDs, illumos and Solaris have
// none the stdlib reaches (FreeBSD's pdfork is not reachable through
// ForkExec), nor has Linux on mips here.
func (h *handle) Handle() (*os.File, bool) {
	//: nothing to hand out.
	return nil, false
}

// SignalHandle is UnsupportedPlatform where no kernel handle exists.
func (h *handle) SignalHandle(_ coreproc.Signal) error {
	//: Signal is the pid-addressed alternative.
	return coreproc.UnsupportedPlatform
}
