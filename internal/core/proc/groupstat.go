package proc

import "time"

// GroupStatValue is what a control group's tree has used, as GroupReader.Stat
// read it. A counter the backend cannot read is zero, and Partial says that
// some are: memory.peak before Linux 5.19, a controller not enabled for the
// group, and on Windows and FreeBSD everything their kernels do not count.
type GroupStatValue struct {
	// CPUUsage is the CPU time the group's tasks used, user and system.
	CPUUsage time.Duration
	// CPUUser is the user-mode share of CPUUsage.
	CPUUser time.Duration
	// CPUSystem is the kernel-mode share of CPUUsage.
	CPUSystem time.Duration
	// ThrottledTime is how long cpu.max held the group's tasks back.
	ThrottledTime time.Duration
	// NrThrottled is how many periods cpu.max throttled the group in.
	NrThrottled int64
	// MemoryCurrent is the memory the group holds now, in bytes.
	MemoryCurrent int64
	// MemoryPeak is the most memory the group held, in bytes (Linux 5.19's
	// memory.peak; Windows' peak job memory).
	MemoryPeak int64
	// MemoryEvents counts what the group's memory limits did.
	MemoryEvents MemoryEventsValue
	// PidsCurrent is how many tasks the group holds now.
	PidsCurrent int64
	// IO is the I/O the group did, one entry per device.
	IO []IOStatValue
	// Partial reports that at least one counter could not be read and is zero.
	Partial bool
}

// MemoryEventsValue counts what a control group's memory limits did — Linux's
// memory.events.
type MemoryEventsValue struct {
	// Low counts reclaims under memory.low's protection.
	Low int64
	// High counts the times the group was throttled above memory.high.
	High int64
	// Max counts the times the group hit memory.max.
	Max int64
	// OOM counts the times the group ran out of memory.
	OOM int64
	// OOMKill counts the tasks the OOM killer ended in the group.
	OOMKill int64
}

// IOStatValue is one device's I/O by a control group — a line of Linux's
// io.stat.
type IOStatValue struct {
	// Major is the device's major number.
	Major uint32
	// Minor is the device's minor number.
	Minor uint32
	// RBytes is the bytes read.
	RBytes uint64
	// WBytes is the bytes written.
	WBytes uint64
	// RIOs is the read operations.
	RIOs uint64
	// WIOs is the write operations.
	WIOs uint64
}
