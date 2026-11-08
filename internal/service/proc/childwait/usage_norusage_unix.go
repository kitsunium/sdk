//go:build illumos || solaris

package childwait

import (
	"syscall"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

// UsageOf reports no counters on illumos and Solaris: their wait4 leaves the
// rusage counters unfilled, as it leaves ru_maxrss at 0, so the value is nil
// rather than a row of zeros that would read as an idle process.
func UsageOf(_ *syscall.Rusage) *coreproc.UsageValue {
	//: nothing supplied.
	return nil
}
