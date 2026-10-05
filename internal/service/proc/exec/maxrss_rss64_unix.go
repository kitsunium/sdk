//go:build unix && !386 && !arm && !mips && !mipsle

package exec

import "syscall"

// maxRSSKB reports the peak resident set size in kilobytes from the wait4
// rusage. On a 64-bit GOARCH Rusage.Maxrss is already int64, so the value is
// passed through without a cast (a cast here would be a redundant conversion).
func maxRSSKB(ru *syscall.Rusage) int64 {
	//: the kernel reports peak RSS in kilobytes on Unix; pass it through.
	return ru.Maxrss
}
