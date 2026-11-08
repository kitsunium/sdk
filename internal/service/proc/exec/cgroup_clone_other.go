//go:build unix && !linux

package exec

import (
	"sync/atomic"
	"syscall"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// cloneIntoCgroupRefused is never set off Linux: no clone places a child.
var cloneIntoCgroupRefused atomic.Bool

// cgroupByClone is false off Linux: there is no cgroup to clone into, and
// validateCgroupPath refused a CgroupPath before the spawn.
func cgroupByClone(_ string) bool {
	//: never.
	return false
}

// attachCgroupFD has nothing to attach off Linux.
func attachCgroupFD(_ coreproc.Spec, _ *syscall.SysProcAttr) (release func(), err error) {
	//: nothing opened.
	return func() {}, nil
}

// cloneSpawnError is never reached off Linux; it reports the plain fork/exec
// failure.
func cloneSpawnError(cause error, _, path string, _ *atomic.Bool) (retry bool, err error) {
	//: an ordinary fork/exec failure.
	return false, wrapSpawn(cause, errs.String("path", path))
}

// cgroupByTrampoline reports a non-empty cgroupPath: off Linux nothing else
// could place a child (and validateCgroupPath refused it first).
func cgroupByTrampoline(cgroupPath string) bool {
	//: a placement.
	return cgroupPath != ""
}
