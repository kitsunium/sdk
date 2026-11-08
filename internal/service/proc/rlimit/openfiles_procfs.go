//go:build linux || solaris || darwin

package rlimit

import (
	"os"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// openFiles counts the entries of the descriptor directory the kernel keeps
// for the caller — /proc/self/fd on Linux, illumos and Solaris, /dev/fd on
// darwin — less the one the listing itself holds open.
func openFiles() (int, error) {
	entries, err := os.ReadDir(selfFDDir)
	//: no descriptor directory to read.
	if err != nil {
		return 0, errs.Wrap(err, errs.WrapParams{
			Code:     coreproc.CodeRlimitFailed,
			Reason:   "RLIMIT_FAILED",
			Public:   "Could not read the resource limit",
			Private:  "service/proc/rlimit.OpenFiles: listing the descriptor directory failed",
			ExitCode: exitOSErr,
		}, errs.String("dir", selfFDDir))
	}
	//: the listing's own descriptor is not the caller's.
	return len(entries) - 1, nil
}
