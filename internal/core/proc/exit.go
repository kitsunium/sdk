package proc

import "time"

// ExitValue is the immutable outcome of a finished process: its exit status, the
// signal that terminated it (if any), and resource-usage accounting. It is the
// value returned by Process.Wait.
type ExitValue struct {
	// Code is the exit status (0–255) when the process exited normally; it is -1
	// when the process was terminated by a signal.
	Code int
	// Signal is the signal that terminated the process, or the zero Signal when
	// it exited normally.
	Signal Signal
	// Signaled reports whether the process was terminated by a signal rather
	// than exiting on its own.
	Signaled bool
	// UserTime is the CPU time spent in user mode.
	UserTime time.Duration
	// SystemTime is the CPU time spent in kernel mode on the process's behalf.
	SystemTime time.Duration
	// MaxRSS is the peak resident set size in KiB on every platform, read from
	// wait4's rusage: darwin and ios report bytes, which are converted to KiB
	// rounding up; Linux and the BSDs report KiB, passed through. It is 0 when
	// the platform does not supply it (illumos, Solaris and Windows).
	MaxRSS int64
	// CoreDumped reports whether the signalled death left a core image
	// (WaitStatus.CoreDump). It is false for a normal exit, and wherever the
	// platform cannot say (Windows).
	CoreDumped bool
	// Usage is the rest of wait4's resource accounting — page faults, block I/O
	// and context switches — read from the same rusage as UserTime and MaxRSS.
	// It is carried by value, so building an ExitValue allocates nothing and
	// one an observer keeps is never changed by a later collection. Its Known
	// is false where the platform does not supply it (illumos, Solaris and
	// Windows).
	Usage UsageValue
}

// UsageValue is the resource accounting wait4 returns beside CPU time and peak
// RSS: the page faults, block I/O operations and context switches of the
// finished process. It is the value ExitValue.Usage carries.
type UsageValue struct {
	// Known reports whether the platform filled the counters. It is false on
	// illumos, Solaris and Windows, where every counter reads zero, so a usage
	// nobody measured is never read as an idle process.
	Known bool
	// MinorFaults counts the page faults served without I/O (ru_minflt).
	MinorFaults uint64
	// MajorFaults counts the page faults that needed I/O (ru_majflt).
	MajorFaults uint64
	// InBlock counts the block input operations (ru_inblock).
	InBlock uint64
	// OutBlock counts the block output operations (ru_oublock).
	OutBlock uint64
	// VoluntaryCtxSw counts the times the process gave up the CPU (ru_nvcsw).
	VoluntaryCtxSw uint64
	// InvoluntaryCtxSw counts the times the scheduler took the CPU away (ru_nivcsw).
	InvoluntaryCtxSw uint64
}

// Success reports whether the process exited normally with status zero. Its
// receiver is a pointer: an ExitValue carries Usage by value, past the size
// worth copying for a question.
func (e *ExitValue) Success() bool {
	//: a clean exit is a normal (non-signalled) termination with status zero.
	return !e.Signaled && e.Code == 0
}
