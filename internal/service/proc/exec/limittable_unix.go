//go:build unix

// Package exec — Unix Resource→RLIMIT_* table. Only the constants the stdlib
// syscall package exports on every Unix target are mapped; RLIMIT_NPROC and
// RLIMIT_MEMLOCK live in golang.org/x/sys (banned here), so those resources stay
// unmapped and surface UnknownResource rather than a wrong limit.
package exec

import (
	"syscall"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

// buildResourceLimits returns the Resource→RLIMIT_* map. It is a function rather
// than a literal so the platform constants resolve under the unix build tag and
// the package keeps to the no-init rule via a package-level var initialiser.
func buildResourceLimits() map[coreproc.Resource]int {
	//: map only the RLIMIT_* constants stdlib exports on every Unix target.
	return map[coreproc.Resource]int{
		coreproc.ResourceNoFile: syscall.RLIMIT_NOFILE,
		coreproc.ResourceCore:   syscall.RLIMIT_CORE,
		coreproc.ResourceAS:     syscall.RLIMIT_AS,
		coreproc.ResourceCPU:    syscall.RLIMIT_CPU,
		coreproc.ResourceFSize:  syscall.RLIMIT_FSIZE,
		coreproc.ResourceData:   syscall.RLIMIT_DATA,
		coreproc.ResourceStack:  syscall.RLIMIT_STACK,
	}
}
