//go:build unix && (386 || arm || mips || mipsle)

package exec

import "syscall"

// maxRSSKB reports the peak resident set size in KiB from the wait4 rusage. On
// a 32-bit GOARCH Rusage.Maxrss is int32, so it is widened to the int64 the
// coreproc.ExitValue.MaxRSS port field expects, then normalised by peakKiB.
func maxRSSKB(ru *syscall.Rusage) int64 {
	//: widen to the port width; every 32-bit Unix target reports kilobytes.
	return peakKiB(int64(ru.Maxrss), rssUnitsPerKiB)
}
