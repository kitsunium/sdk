//go:build unix && !linux

package self

import "syscall"

// kernelMemory fills what getrusage(2) gives off Linux: the peak resident set.
// The current resident set, the thread count and the descriptor count need a
// procfs or a sysctl the SDK does not bind here, so they stay zero and
// KernelMemory false.
func kernelMemory(stats *StatsValue) {
	var usage syscall.Rusage
	//: the kernel's peak.
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err == nil {
		stats.PeakRSSBytes = uint64(usage.Maxrss) * maxRSSUnit
	}
}
