//go:build unix && !386 && !arm && !mips && !mipsle

package childwait

import "syscall"

// MaxRSSKB reports the peak resident set size in KiB from a wait4 rusage. On a
// 64-bit GOARCH Rusage.Maxrss is already int64; the unit is normalised — a
// kilobyte figure passes through, darwin's bytes are converted, rounding up.
func MaxRSSKB(ru *syscall.Rusage) int64 {
	//: Linux and the BSDs report kilobytes, darwin and ios report bytes.
	return peakKiB(ru.Maxrss, rssUnitsPerKiB)
}
