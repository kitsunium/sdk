//go:build !linux && !freebsd

package exec

import (
	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

// checkSchedulingPlatform refuses a policy other than SchedOther and any I/O
// class: darwin, OpenBSD, NetBSD, DragonFly, illumos, Solaris and Windows have
// no stdlib path to either. The zero value asks for nothing and passes.
func checkSchedulingPlatform(sched *coreproc.SchedulingValue) error {
	//: only the normal policy and no I/O class are honourable here.
	if sched.Policy != coreproc.SchedOther || sched.Priority != 0 || sched.IOClass != coreproc.IOClassNone {
		//: refused before the spawn.
		return coreproc.UnsupportedPlatform
	}
	//: nothing to apply.
	return nil
}

// applyScheduling has nothing to apply: checkSchedulingPlatform let only the
// zero value through.
func applyScheduling(_ int, _ *coreproc.SchedulingValue) error {
	//: the inherited scheduling stands.
	return nil
}
