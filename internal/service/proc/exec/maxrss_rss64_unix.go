//go:build unix && !386 && !arm && !mips && !mipsle

package exec

import "syscall"

// maxRSSKB reports the peak resident set size in KiB from the wait4 rusage. On
// a 64-bit GOARCH Rusage.Maxrss is already int64, so no widening cast is
// needed; the unit is normalised by peakKiB, which passes a kilobyte figure
// through and converts darwin's bytes.
func maxRSSKB(ru *syscall.Rusage) int64 {
	//: Linux and the BSDs report kilobytes, darwin and ios report bytes.
	return peakKiB(ru.Maxrss, rssUnitsPerKiB)
}
