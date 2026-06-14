//go:build unix

// Package exec — Unix Resource→RLIMIT_* table. Only the constants the stdlib
// syscall package exports are mapped; RLIMIT_NPROC and RLIMIT_MEMLOCK live in
// golang.org/x/sys (banned here), so those resources stay unmapped and surface
// UnknownResource rather than a wrong limit. The address-space limit (RLIMIT_AS)
// is present on every supported target EXCEPT OpenBSD, so it is added by a
// build-tagged addPlatformLimits (limittable_as.go / limittable_openbsd.go)
// rather than referenced here — that keeps this file compiling on OpenBSD.
package exec

import (
	"syscall"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

// buildResourceLimits returns the Resource→RLIMIT_* map. It is a function rather
// than a literal so the platform constants resolve under the unix build tag and
// the package keeps to the no-init rule via a package-level var initialiser.
func buildResourceLimits() map[coreproc.Resource]int {
	//: the RLIMIT_* constants stdlib exports on every supported Unix target.
	limits := map[coreproc.Resource]int{
		coreproc.ResourceNoFile: syscall.RLIMIT_NOFILE,
		coreproc.ResourceCore:   syscall.RLIMIT_CORE,
		coreproc.ResourceCPU:    syscall.RLIMIT_CPU,
		coreproc.ResourceFSize:  syscall.RLIMIT_FSIZE,
		coreproc.ResourceData:   syscall.RLIMIT_DATA,
		coreproc.ResourceStack:  syscall.RLIMIT_STACK,
	}
	//: add the platform-specific resources (RLIMIT_AS where the kernel has it).
	addPlatformLimits(limits)
	//: the assembled Resource→RLIMIT_* table for this platform.
	return limits
}
