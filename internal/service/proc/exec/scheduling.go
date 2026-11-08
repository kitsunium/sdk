package exec

import (
	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

// maxIOPriority is the lowest I/O level within a class: the kernel's eight
// levels run from 0 (highest) to 7.
const maxIOPriority int = 7

// checkScheduling refuses a Spec.Scheduling that names no defined policy or
// class, or an I/O level outside 0..7, with InvalidSpec, then one this platform
// cannot apply with UnsupportedPlatform — both before anything is spawned. A
// nil value asks for nothing.
func checkScheduling(sched *coreproc.SchedulingValue) error {
	//: nothing requested.
	if sched == nil {
		//: an ordinary spawn.
		return nil
	}
	//: an undefined enum value or level is a malformed spec on every platform.
	if !knownPolicy(sched.Policy) || !knownIOClass(sched.IOClass) || sched.IOPriority < 0 || sched.IOPriority > maxIOPriority {
		//: the bare sentinel: a contract violation, no cause.
		return coreproc.InvalidSpec
	}
	//: what this platform can apply.
	return checkSchedulingPlatform(sched)
}

// knownPolicy reports whether p is one of the defined SchedPolicy values.
func knownPolicy(p coreproc.SchedPolicy) bool {
	//: the five policies Linux numbers 0, 1, 2, 3 and 5.
	switch p {
	case coreproc.SchedOther, coreproc.SchedFIFO, coreproc.SchedRR, coreproc.SchedBatch, coreproc.SchedIdle:
		//: defined.
		return true
	default:
		//: 4 (SCHED_ISO, never merged) and anything else.
		return false
	}
}

// knownIOClass reports whether c is one of the defined IOClass values.
func knownIOClass(c coreproc.IOClass) bool {
	//: the contiguous range IOClassNone..IOClassIdle.
	return c >= coreproc.IOClassNone && c <= coreproc.IOClassIdle
}
