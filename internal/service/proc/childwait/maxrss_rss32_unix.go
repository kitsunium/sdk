//go:build unix && (386 || arm || mips || mipsle)

package childwait

import "syscall"

// MaxRSSKB reports the peak resident set size in KiB from a wait4 rusage. On a
// 32-bit GOARCH Rusage.Maxrss is int32, so it is widened to the int64
// ExitValue.MaxRSS documents, then normalised.
func MaxRSSKB(ru *syscall.Rusage) int64 {
	//: widen to the port width; every 32-bit Unix target reports kilobytes.
	return peakKiB(int64(ru.Maxrss), rssUnitsPerKiB)
}
