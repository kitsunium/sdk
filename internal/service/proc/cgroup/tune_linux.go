package cgroup

import (
	"slices"
	"strconv"
	"strings"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// The cgroup v2 interface files GroupTuner writes
// (Documentation/admin-guide/cgroup-v2.rst).
const (
	// memoryMinFile is the hard memory protection.
	memoryMinFile string = "memory.min"
	// memoryLowFile is the best-effort memory protection.
	memoryLowFile string = "memory.low"
	// memoryHighFile is the memory throttle.
	memoryHighFile string = "memory.high"
	// memorySwapMaxFile is the swap limit.
	memorySwapMaxFile string = "memory.swap.max"
	// memoryZswapMaxFile is the zswap pool limit.
	memoryZswapMaxFile string = "memory.zswap.max"
	// memoryZswapWritebackFile allows zswap writeback to swap (Linux 6.8).
	memoryZswapWritebackFile string = "memory.zswap.writeback"
	// memoryOOMGroupFile makes the OOM killer take the whole group.
	memoryOOMGroupFile string = "memory.oom.group"
	// cpuWeightFile is the group's CPU share.
	cpuWeightFile string = "cpu.weight"
	// cpuMaxBurstFile is the CPU time borrowable above cpu.max's quota.
	cpuMaxBurstFile string = "cpu.max.burst"
	// cpusetCPUsFile lists the CPUs the group may run on.
	cpusetCPUsFile string = "cpuset.cpus"
	// cpusetMemsFile lists the memory nodes the group may allocate on.
	cpusetMemsFile string = "cpuset.mems"
	// ioWeightFile is the group's I/O share.
	ioWeightFile string = "io.weight"
	// ioLatencyFile is the I/O latency protection, per device.
	ioLatencyFile string = "io.latency"
	// weightMin and weightMax bound cpu.weight and io.weight.
	weightMin int64 = 1
	weightMax int64 = 10000
	// ioWeightDefault prefixes io.weight's default for every device.
	ioWeightDefault string = "default "
	// ioLatencyTarget prefixes io.latency's target, in microseconds.
	ioLatencyTarget string = " target="
	// flagOn and flagOff are the boolean files' values.
	flagOn  string = "1"
	flagOff string = "0"
	// listSeparator and rangeSeparator write a cpuset list: "0-3,8".
	listSeparator  string = ","
	rangeSeparator string = "-"
)

// SetMemoryMin writes memory.min (systemd MemoryMin=), the memory the group
// keeps whatever the pressure; a negative value writes "max".
func (g *controlGroup) SetMemoryMin(bytes int64) error {
	//: the byte count, or "max".
	return g.writeController(memoryMinFile, maxOrValue(bytes))
}

// SetMemoryLow writes memory.low (systemd MemoryLow=), the memory reclaimed
// from the group only when nothing else can be; a negative value writes
// "max".
func (g *controlGroup) SetMemoryLow(bytes int64) error {
	//: the byte count, or "max".
	return g.writeController(memoryLowFile, maxOrValue(bytes))
}

// SetMemoryHigh writes memory.high (systemd MemoryHigh=), the throttle
// above which the group's allocations are slowed and reclaimed; a negative
// value writes "max".
func (g *controlGroup) SetMemoryHigh(bytes int64) error {
	//: the byte count, or "max".
	return g.writeController(memoryHighFile, maxOrValue(bytes))
}

// SetMemorySwapMax writes memory.swap.max (systemd MemorySwapMax=); a
// negative value writes "max".
func (g *controlGroup) SetMemorySwapMax(bytes int64) error {
	//: the byte count, or "max".
	return g.writeController(memorySwapMaxFile, maxOrValue(bytes))
}

// SetMemoryZswapMax writes memory.zswap.max (systemd MemoryZSwapMax=); a
// negative value writes "max".
func (g *controlGroup) SetMemoryZswapMax(bytes int64) error {
	//: the byte count, or "max".
	return g.writeController(memoryZswapMaxFile, maxOrValue(bytes))
}

// SetMemoryZswapWriteback writes memory.zswap.writeback (systemd
// MemoryZSwapWriteback=, Linux 6.8): false keeps the group's compressed
// pages from being written back to swap.
func (g *controlGroup) SetMemoryZswapWriteback(enabled bool) error {
	//: "1" or "0".
	return g.writeController(memoryZswapWritebackFile, flagOf(enabled))
}

// SetMemoryOOMGroup writes memory.oom.group: true makes the OOM killer end
// the whole group rather than one task of it (systemd OOMPolicy=kill).
func (g *controlGroup) SetMemoryOOMGroup(enabled bool) error {
	//: "1" or "0".
	return g.writeController(memoryOOMGroupFile, flagOf(enabled))
}

// SetCPUWeight writes cpu.weight (systemd CPUWeight=), the group's share of
// CPU against its siblings, 1 to 10000 (100 by default).
func (g *controlGroup) SetCPUWeight(weight int64) error {
	//: the kernel's range, checked before the write.
	if weight < weightMin || weight > weightMax {
		//: INVALID_SPEC, nothing written.
		return invalidValue(cpuWeightFile, strconv.FormatInt(weight, decimalBase))
	}
	//: the weight in decimal.
	return g.writeController(cpuWeightFile, strconv.FormatInt(weight, decimalBase))
}

// SetCPUMaxBurst writes cpu.max.burst, the CPU time the group may borrow
// above cpu.max's quota in a period, in microseconds; a negative burst is
// InvalidSpec.
func (g *controlGroup) SetCPUMaxBurst(burst time.Duration) error {
	//: a burst is a length of time.
	if burst < 0 {
		//: INVALID_SPEC, nothing written.
		return invalidValue(cpuMaxBurstFile, burst.String())
	}
	//: whole microseconds, as the kernel reads them.
	return g.writeController(cpuMaxBurstFile, strconv.FormatInt(burst.Microseconds(), decimalBase))
}

// SetCPUSetCPUs writes cpuset.cpus (systemd AllowedCPUs=) in the kernel's
// list form, "0-3,8"; an empty list writes "", the parent's CPUs. A
// negative CPU is InvalidSpec.
func (g *controlGroup) SetCPUSetCPUs(cpus []int) error {
	//: the list, or the refusal of a negative entry.
	return g.writeList(cpusetCPUsFile, cpus)
}

// SetCPUSetMems writes cpuset.mems (systemd AllowedMemoryNodes=) in the
// list form SetCPUSetCPUs writes.
func (g *controlGroup) SetCPUSetMems(nodes []int) error {
	//: the list, or the refusal of a negative entry.
	return g.writeList(cpusetMemsFile, nodes)
}

// SetIOWeight writes io.weight's default (systemd IOWeight=), the group's
// share of I/O against its siblings, 1 to 10000 (100 by default).
func (g *controlGroup) SetIOWeight(weight int64) error {
	value := strconv.FormatInt(weight, decimalBase)
	//: the kernel's range, checked before the write.
	if weight < weightMin || weight > weightMax {
		//: INVALID_SPEC, nothing written.
		return invalidValue(ioWeightFile, value)
	}
	//: "default N": every device the group has no line of its own for.
	return g.writeController(ioWeightFile, ioWeightDefault+value)
}

// SetIOLatency writes the io.latency line of the device major:minor
// (systemd IODeviceLatencyTargetSec=): the latency the group's I/O is
// protected to, in microseconds; zero removes it, and a negative target is
// InvalidSpec.
func (g *controlGroup) SetIOLatency(major, minor uint32, target time.Duration) error {
	//: a target is a length of time.
	if target < 0 {
		//: INVALID_SPEC, nothing written.
		return invalidValue(ioLatencyFile, target.String())
	}
	value := unlimited
	//: a target below a microsecond is one microsecond, never a removal.
	if target > 0 {
		value = strconv.FormatInt(int64((target+time.Microsecond-1)/time.Microsecond), decimalBase)
	}
	device := strconv.FormatUint(uint64(major), decimalBase) + devSeparator + strconv.FormatUint(uint64(minor), decimalBase)
	//: "MAJ:MIN target=USEC", or "target=max" to remove the device's line.
	return g.writeController(ioLatencyFile, device+ioLatencyTarget+value)
}

// writeList writes values to file in the kernel's list form, refusing a
// negative entry before the write.
func (g *controlGroup) writeList(file string, values []int) error {
	list, ok := listOf(values)
	//: a CPU or a node is never negative.
	if !ok {
		//: INVALID_SPEC, nothing written.
		return invalidValue(file, list)
	}
	//: the list, empty for the parent's own.
	return g.writeController(file, list)
}

// listOf renders values as the kernel's list — sorted, each run of
// consecutive numbers written "a-b", the runs joined with commas — and
// reports false with the values as given when one is negative.
func listOf(values []int) (string, bool) {
	sorted := slices.Clone(values)
	slices.Sort(sorted)
	sorted = slices.Compact(sorted)
	//: the smallest entry decides whether any is negative.
	if len(sorted) > 0 && sorted[0] < 0 {
		//: the values, rendered for the refusal.
		return intsText(values), false
	}
	var list strings.Builder
	//: one run per pass: from its first entry to its last consecutive one.
	for start := 0; start < len(sorted); {
		end := start
		//: extend the run while the next entry follows on.
		for end+1 < len(sorted) && sorted[end+1] == sorted[end]+1 {
			end++
		}
		//: the separator between two runs.
		if list.Len() > 0 {
			list.WriteString(listSeparator)
		}
		list.WriteString(strconv.Itoa(sorted[start]))
		//: a run of more than one entry.
		if end > start {
			list.WriteString(rangeSeparator)
			list.WriteString(strconv.Itoa(sorted[end]))
		}
		start = end + 1
	}
	//: the list.
	return list.String(), true
}

// intsText renders values as given, comma-separated, for a refusal.
func intsText(values []int) string {
	texts := make([]string, len(values))
	//: each value in decimal.
	for i, v := range values {
		texts[i] = strconv.Itoa(v)
	}
	//: as the caller wrote them.
	return strings.Join(texts, listSeparator)
}

// flagOf renders a boolean interface file's value.
func flagOf(enabled bool) string {
	//: on.
	if enabled {
		return flagOn
	}
	//: off.
	return flagOff
}

// invalidValue wraps the central INVALID_SPEC sentinel with the file a value
// was refused for and the value.
func invalidValue(file, value string) error {
	//: origin wins: the sentinel's own fields, with what was refused.
	return errs.Wrap(coreproc.InvalidSpec, errs.WrapParams{}, errs.String("file", file), errs.String("value", value))
}
