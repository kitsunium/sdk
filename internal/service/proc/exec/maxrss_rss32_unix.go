//go:build unix && (386 || arm || mips || mipsle)

package exec

import "syscall"

// maxRSSKB reports the peak resident set size in kilobytes from the wait4
// rusage. On a 32-bit GOARCH Rusage.Maxrss is int32, so it is widened to the
// int64 the coreproc.ExitValue.MaxRSS port field expects.
func maxRSSKB(ru *syscall.Rusage) int64 {
	//: the kernel reports peak RSS in kilobytes on Unix; widen to the port width.
	return int64(ru.Maxrss)
}
