//go:build unix && !openbsd

// Package exec — RLIMIT_AS mapping for the platforms whose stdlib syscall exports
// it (Linux, Darwin, FreeBSD, NetBSD, DragonFly). OpenBSD has no address-space
// rlimit — RLIMIT_AS is absent from its kernel ABI — so it provides the no-op in
// limittable_openbsd.go instead, leaving ResourceAS unmapped (UnknownResource).
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
