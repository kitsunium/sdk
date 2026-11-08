//go:build unix && !linux

package exec

// oomScoreAdjNative reports that no OOM-killer bias can be written here: the
// procfs file is Linux's.
const oomScoreAdjNative bool = false
