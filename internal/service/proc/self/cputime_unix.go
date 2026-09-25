//go:build unix

// Package self — the CPU time the process used, as the kernel counts it.
package self

import (
	"syscall"
	"time"
)

// cpuTime returns the user and system CPU time the process has consumed, from
// getrusage(2), and false. Should the call fail — it does not, for
// RUSAGE_SELF — it falls back to the runtime's estimate and reports true.
func cpuTime() (used time.Duration, estimated bool) {
	var usage syscall.Rusage
	//: the kernel's own accounting, which counts every thread.
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
		//: the runtime's estimate, and say so.
		return runtimeCPU(), true
	}
	//: user plus system, as the kernel measured them.
	return time.Duration(usage.Utime.Nano() + usage.Stime.Nano()), false
}
