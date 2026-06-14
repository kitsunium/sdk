// Package proc — the ExitValue value type: the outcome of a finished process.
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
	// MaxRSS is the peak resident set size in kilobytes, as reported by wait4's
	// rusage; it is 0 when the platform does not supply it.
	MaxRSS int64
}

// Success reports whether the process exited normally with status zero.
func (e ExitValue) Success() bool {
	//: a clean exit is a normal (non-signalled) termination with status zero.
	return !e.Signaled && e.Code == 0
}
