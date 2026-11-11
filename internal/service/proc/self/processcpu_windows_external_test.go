//go:build windows

package self_test

import (
	"syscall"
	"time"
)

// processCPU is the process's user and kernel CPU time as GetProcessTimes
// reports it, read by the test itself, and whether the call answered.
func processCPU() (time.Duration, bool) {
	var creation, exit, kernel, user syscall.Filetime
	process, err := syscall.GetCurrentProcess()
	if err == nil {
		err = syscall.GetProcessTimes(process, &creation, &exit, &kernel, &user)
	}
	if err != nil {
		return 0, false
	}
	return time.Duration(fileTicks(kernel)+fileTicks(user)) * 100, true
}

// fileTicks reads a FILETIME as its count of 100 ns ticks.
func fileTicks(ft syscall.Filetime) int64 {
	return int64(ft.HighDateTime)<<32 | int64(ft.LowDateTime)
}
