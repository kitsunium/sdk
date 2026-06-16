//go:build unix && !linux && !openbsd

// Package rlimit — RLIMIT_AS mapping for the non-Linux Unix targets whose stdlib
// syscall exports it (Darwin, FreeBSD, NetBSD, DragonFly). OpenBSD has no
// address-space rlimit — RLIMIT_AS is absent from its kernel ABI — so it supplies
// the no-op in rlimit_table_openbsd.go instead, leaving ResourceAS unmapped
// (UnknownResource) rather than failing to compile on the missing constant.
package rlimit

import (
	"syscall"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

// addPlatformLimits adds the address-space limit (RLIMIT_AS) to the table.
func addPlatformLimits(limits map[coreproc.Resource]int) {
	//: RLIMIT_AS caps the process virtual address space on these platforms.
	limits[coreproc.ResourceAS] = syscall.RLIMIT_AS
}
