//go:build !unix

// Package self — the CPU time the process used, where the kernel's count is
// not at hand.
package self

import "time"

// cpuTime returns the Go runtime's estimate of the CPU time the process has
// consumed, and true: there is no getrusage(2) here — Windows, Plan 9, wasm —
// so the kernel's count is not available through the standard library.
func cpuTime() (used time.Duration, estimated bool) {
	//: every CPU class but idle, as the runtime accounts for them.
	return runtimeCPU(), true
}
