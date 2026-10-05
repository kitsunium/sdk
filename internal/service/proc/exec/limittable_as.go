//go:build unix && !openbsd

package exec

import (
	"syscall"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

// addPlatformLimits adds the address-space limit (RLIMIT_AS) to the table.
func addPlatformLimits(limits map[coreproc.Resource]int) {
	//: RLIMIT_AS caps the process virtual address space on these platforms.
	limits[coreproc.ResourceAS] = syscall.RLIMIT_AS
}
