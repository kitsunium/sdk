package proc

import "strconv"

// SchedPolicy is a CPU scheduling policy (Spec.Scheduling). Its values are
// Linux's SCHED_* numbers; the zero value SchedOther is the normal time-sharing
// policy.
type SchedPolicy int

// IOClass is an I/O scheduling class (Spec.Scheduling). Its values are Linux's
// IOPRIO_CLASS_* numbers; the zero value IOClassNone sets none.
type IOClass int

const (
	// SchedOther is the normal time-sharing policy (SCHED_OTHER) — the zero value.
	SchedOther SchedPolicy = 0
	// SchedFIFO is the first-in first-out realtime policy (SCHED_FIFO).
	SchedFIFO SchedPolicy = 1
	// SchedRR is the round-robin realtime policy (SCHED_RR).
	SchedRR SchedPolicy = 2
	// SchedBatch is the policy for CPU-bound batch work (SCHED_BATCH).
	SchedBatch SchedPolicy = 3
	// SchedIdle runs the child only when nothing else wants the CPU (SCHED_IDLE).
	SchedIdle SchedPolicy = 5
	// IOClassNone sets no I/O class, the kernel deriving one from the CPU niceness — the zero value.
	IOClassNone IOClass = 0
	// IOClassRealTime is the realtime I/O class (IOPRIO_CLASS_RT).
	IOClassRealTime IOClass = 1
	// IOClassBestEffort is the best-effort I/O class (IOPRIO_CLASS_BE).
	IOClassBestEffort IOClass = 2
	// IOClassIdle gets I/O only when no other process wants the disk (IOPRIO_CLASS_IDLE).
	IOClassIdle IOClass = 3
)

var (
	// schedPolicyNames maps each defined policy to its lowercase name.
	schedPolicyNames = map[SchedPolicy]string{
		SchedOther: "other",
		SchedFIFO:  "fifo",
		SchedRR:    "rr",
		SchedBatch: "batch",
		SchedIdle:  "idle",
	}

	// ioClassNames maps each defined class to its lowercase name.
	ioClassNames = map[IOClass]string{
		IOClassNone:       "none",
		IOClassRealTime:   "realtime",
		IOClassBestEffort: "besteffort",
		IOClassIdle:       "idle",
	}
)

// SchedulingValue is the CPU scheduling policy and I/O priority a child is
// spawned under (Spec.Scheduling). Its zero value asks for the normal policy and
// no I/O class: SchedOther at priority 0, IOClassNone.
type SchedulingValue struct {
	// Policy is the CPU scheduling policy.
	Policy SchedPolicy
	// Priority is the static priority of SchedFIFO and SchedRR (1 to 99 on Linux,
	// 0 to 31 on FreeBSD); it must be 0 for every other policy.
	Priority int
	// IOClass is the I/O scheduling class; IOClassNone leaves the kernel's
	// default, derived from the CPU niceness.
	IOClass IOClass
	// IOPriority is the level within IOClassRealTime or IOClassBestEffort, from 0
	// (highest) to 7 (lowest).
	IOPriority int
}

// String returns the lowercase policy name ("other", "fifo", "rr", "batch",
// "idle"), or "schedpolicy(N)" for an undefined value.
func (p SchedPolicy) String() string {
	//: a defined policy has its canonical name.
	if name, ok := schedPolicyNames[p]; ok {
		//: table hit.
		return name
	}
	//: an undefined value stays legible with its number.
	return "schedpolicy(" + strconv.Itoa(int(p)) + ")"
}

// String returns the lowercase class name ("none", "realtime", "besteffort",
// "idle"), or "ioclass(N)" for an undefined value.
func (c IOClass) String() string {
	//: a defined class has its canonical name.
	if name, ok := ioClassNames[c]; ok {
		//: table hit.
		return name
	}
	//: an undefined value stays legible with its number.
	return "ioclass(" + strconv.Itoa(int(c)) + ")"
}
