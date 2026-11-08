package self

import (
	"os"
	"strconv"
	"strings"
)

// The procfs files the kernel's view of the process is read from.
const (
	// statmFile's second field is the resident set, in pages.
	statmFile string = "/proc/self/statm"
	// statusFile carries VmHWM (the peak resident set, kB) and Threads.
	statusFile string = "/proc/self/status"
	// fdDir lists the open descriptors.
	fdDir string = "/proc/self/fd"
	// statmResidentField is the resident set's index in statm.
	statmResidentField int = 1
	// kibibyte is the unit status writes its sizes in.
	kibibyte uint64 = 1024
	// peakKey and threadsKey are the status lines read.
	peakKey    string = "VmHWM:"
	threadsKey string = "Threads:"
)

// kernelMemory fills the kernel's view: the resident set from statm, its peak
// and the thread count from status, the descriptors from /proc/self/fd.
func kernelMemory(stats *StatsValue) {
	//: the resident set, in pages.
	if statm, err := os.ReadFile(statmFile); err == nil {
		fields := strings.Fields(string(statm))
		//: the kernel's count, in bytes.
		if len(fields) > statmResidentField {
			//: a count of pages, or nothing to report.
			if pages, err := strconv.ParseUint(fields[statmResidentField], 10, 64); err == nil {
				stats.RSSBytes = pages * uint64(os.Getpagesize())
				stats.KernelMemory = true
			}
		}
	}
	//: the peak and the threads.
	if status, err := os.ReadFile(statusFile); err == nil {
		readStatus(stats, string(status))
	}
	//: the descriptors, less the listing's own.
	if entries, err := os.ReadDir(fdDir); err == nil {
		stats.OpenFiles = len(entries) - 1
	}
}

// readStatus reads VmHWM and Threads from /proc/self/status.
func readStatus(stats *StatsValue, status string) {
	//: one "Key:\tvalue" per line.
	for line := range strings.Lines(status) {
		fields := strings.Fields(line)
		//: a line with no value.
		if len(fields) < 2 {
			continue
		}
		value, err := strconv.ParseUint(fields[1], 10, 64)
		//: not a count.
		if err != nil {
			continue
		}
		switch fields[0] {
		//: the peak resident set, in kB.
		case peakKey:
			stats.PeakRSSBytes = value * kibibyte
		//: the kernel threads.
		case threadsKey:
			stats.Threads = int(value)
		default:
		}
	}
}
