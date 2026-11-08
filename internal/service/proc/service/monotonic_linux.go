//go:build linux

package service

import (
	"syscall"
	"unsafe"
)

// clockMonotonic is CLOCK_MONOTONIC's id.
const clockMonotonic uintptr = 1

// monotonicMicros is CLOCK_MONOTONIC in microseconds, the clock
// MONOTONIC_USEC= is stamped on; 0 when the kernel refuses the call.
func monotonicMicros() uint64 {
	var ts syscall.Timespec
	_, _, errno := syscall.Syscall(syscall.SYS_CLOCK_GETTIME, clockMonotonic, uintptr(unsafe.Pointer(&ts)), 0)
	//: refused.
	if errno != 0 {
		//: unknown.
		return 0
	}
	//: microseconds.
	return uint64(ts.Nano()) / 1000
}
