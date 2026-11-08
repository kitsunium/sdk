package self

import (
	"syscall"
	"time"
)

// cpuTime returns the kernel's count of the user and kernel time the process
// has consumed, from GetProcessTimes, and false; should the call fail, the
// runtime's estimate and true.
func cpuTime() (used time.Duration, estimated bool) {
	var creation, exit, kernel, user syscall.Filetime
	process, err := syscall.GetCurrentProcess()
	//: the pseudo-handle never fails, but the API says it may.
	if err == nil {
		err = syscall.GetProcessTimes(process, &creation, &exit, &kernel, &user)
	}
	//: the runtime's estimate, and say so.
	if err != nil {
		return runtimeCPU(), true
	}
	//: user plus kernel, in the 100 ns ticks a FILETIME counts.
	return time.Duration(filetimeTicks(kernel)+filetimeTicks(user)) * filetimeTick, false
}

// filetimeTick is a FILETIME's unit.
const filetimeTick time.Duration = 100

// filetimeTicks reads a FILETIME as a count of its ticks.
func filetimeTicks(ft syscall.Filetime) int64 {
	//: the two halves of the 64-bit count.
	return int64(ft.HighDateTime)<<32 | int64(ft.LowDateTime)
}
