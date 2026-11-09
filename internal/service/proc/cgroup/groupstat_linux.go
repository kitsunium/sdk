package cgroup

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// The cgroup v2 files Stat reads (Documentation/admin-guide/cgroup-v2.rst).
const (
	// cpuStatFile is the cpu controller's flat-keyed usage, present on every group.
	cpuStatFile string = "cpu.stat"
	// memoryCurrentFile is the memory the group holds now.
	memoryCurrentFile string = "memory.current"
	// memoryPeakFile is the most memory the group held (Linux 5.19).
	memoryPeakFile string = "memory.peak"
	// memoryEventsFile counts what the group's memory limits did.
	memoryEventsFile string = "memory.events"
	// pidsCurrentFile is how many tasks the group holds now.
	pidsCurrentFile string = "pids.current"
	// ioStatFile is the I/O per device, one nested-keyed line each.
	ioStatFile string = "io.stat"
	// devSeparator splits "MAJ:MIN" in io.stat.
	devSeparator string = ":"
	// keyValueSeparator splits "key=value" in io.stat.
	keyValueSeparator string = "="
	// int64Bits and uint32Bits are the widths the counters parse into.
	int64Bits  int = 64
	uint32Bits int = 32
	// exitIOErr is sysexits.h EX_IOERR (74), the exit status CGROUP_READ_FAILED
	// carries; restated to wrap a cause without re-Defining the central code.
	exitIOErr int = 74
)

// Stat reads the group's counters.
func (g *controlGroup) Stat() (coreproc.GroupStatValue, error) {
	//: the group's own files.
	return statFrom(os.ReadFile, g.dir)
}

// Members lists the pids in the cgroup.procs of the group and of every group
// beneath it — what Contains and Signal reach, and what cgroup.events'
// populated counts: a process a delegated service moved into a group of its
// own is still the group's (S0g, #734). The group's own file must be read; a
// group beneath that goes during the walk is skipped.
func (g *controlGroup) Members() ([]int, error) {
	//: the subtree, CGROUP_READ_FAILED when the group itself is gone.
	return treeMembers(g.dir)
}

// statFrom reads the counters of the group at dir through readFile: cpu.stat
// must be there (the cpu controller's usage is on every group), every other
// file is optional and a missing one sets Partial.
func statFrom(readFile func(string) ([]byte, error), dir string) (coreproc.GroupStatValue, error) {
	var stat coreproc.GroupStatValue
	cpu, err := readFile(filepath.Join(dir, cpuStatFile))
	//: no cpu.stat: the group is gone.
	if err != nil {
		//: typed, with the directory.
		return stat, cgroupReadFailed(err, dir)
	}
	flatKeyed(string(cpu), func(key string, value int64) { cpuStat(&stat, key, value) })
	optional := func(name string, read func(content string)) {
		content, rerr := readFile(filepath.Join(dir, name))
		//: a controller not enabled, or a kernel too old for the file.
		if rerr != nil {
			stat.Partial = true
			return
		}
		read(string(content))
	}
	optional(memoryCurrentFile, func(c string) { stat.MemoryCurrent = single(c) })
	optional(memoryPeakFile, func(c string) { stat.MemoryPeak = single(c) })
	optional(pidsCurrentFile, func(c string) { stat.PidsCurrent = single(c) })
	optional(memoryEventsFile, func(c string) {
		flatKeyed(c, func(key string, value int64) { memoryEvent(&stat.MemoryEvents, key, value) })
	})
	optional(ioStatFile, func(c string) { stat.IO = ioStat(c) })
	//: every counter that could be read.
	return stat, nil
}

// cpuStat applies one cpu.stat key, whose times are microseconds.
func cpuStat(stat *coreproc.GroupStatValue, key string, value int64) {
	switch key {
	//: user and system together.
	case "usage_usec":
		stat.CPUUsage = time.Duration(value) * time.Microsecond
	//: user mode.
	case "user_usec":
		stat.CPUUser = time.Duration(value) * time.Microsecond
	//: kernel mode.
	case "system_usec":
		stat.CPUSystem = time.Duration(value) * time.Microsecond
	//: periods throttled.
	case "nr_throttled":
		stat.NrThrottled = value
	//: time throttled.
	case "throttled_usec":
		stat.ThrottledTime = time.Duration(value) * time.Microsecond
	//: a key the SDK does not read (nr_periods, the burst counters).
	default:
	}
}

// memoryEvent applies one memory.events key.
func memoryEvent(events *coreproc.MemoryEventsValue, key string, value int64) {
	switch key {
	//: reclaims under memory.low.
	case "low":
		events.Low = value
	//: throttles above memory.high.
	case "high":
		events.High = value
	//: hits of memory.max.
	case "max":
		events.Max = value
	//: out-of-memory events.
	case "oom":
		events.OOM = value
	//: tasks the OOM killer ended.
	case "oom_kill":
		events.OOMKill = value
	//: a key the SDK does not read (oom_group_kill, sock_throttled).
	default:
	}
}

// flatKeyed calls visit for each "key value" line of a flat-keyed file.
func flatKeyed(content string, visit func(key string, value int64)) {
	//: one pair per line.
	for line := range strings.Lines(content) {
		key, raw, found := strings.Cut(strings.TrimSpace(line), " ")
		value, err := strconv.ParseInt(strings.TrimSpace(raw), decimalBase, int64Bits)
		//: a pair the kernel wrote.
		if found && err == nil {
			visit(key, value)
		}
	}
}

// single reads a one-value file; "max" or anything unparsable is zero.
func single(content string) int64 {
	value, err := strconv.ParseInt(strings.TrimSpace(content), decimalBase, int64Bits)
	//: not a count.
	if err != nil {
		return 0
	}
	//: the count.
	return value
}

// ioStat reads io.stat: "MAJ:MIN rbytes=.. wbytes=.. rios=.. wios=.. ...".
func ioStat(content string) []coreproc.IOStatValue {
	var devices []coreproc.IOStatValue
	//: one device per line.
	for line := range strings.Lines(content) {
		fields := strings.Fields(line)
		//: a blank line.
		if len(fields) == 0 {
			continue
		}
		device, ok := ioDevice(fields[0])
		//: a device the kernel did not name as MAJ:MIN.
		if !ok {
			continue
		}
		//: the nested keys the value names.
		for _, field := range fields[1:] {
			ioKey(&device, field)
		}
		devices = append(devices, device)
	}
	//: every device listed.
	return devices
}

// ioDevice reads "MAJ:MIN".
func ioDevice(field string) (coreproc.IOStatValue, bool) {
	majorText, minorText, found := strings.Cut(field, devSeparator)
	major, merr := strconv.ParseUint(majorText, decimalBase, uint32Bits)
	minor, nerr := strconv.ParseUint(minorText, decimalBase, uint32Bits)
	//: both numbers, or no device.
	return coreproc.IOStatValue{Major: uint32(major), Minor: uint32(minor)}, found && merr == nil && nerr == nil
}

// ioKey applies one "key=value" of an io.stat line.
func ioKey(device *coreproc.IOStatValue, field string) {
	key, raw, _ := strings.Cut(field, keyValueSeparator)
	value, err := strconv.ParseUint(raw, decimalBase, int64Bits)
	targets := map[string]*uint64{"rbytes": &device.RBytes, "wbytes": &device.WBytes, "rios": &device.RIOs, "wios": &device.WIOs}
	//: one of the four the value names; dbytes and dios are not read.
	if target, known := targets[key]; known && err == nil {
		*target = value
	}
}

// cgroupReadFailed wraps cause in the central CGROUP_READ_FAILED sentinel.
func cgroupReadFailed(cause error, dir string) error {
	//: origin wins on a typed cause; the errno keeps the central code.
	return errs.Wrap(cause, errs.WrapParams{
		Code:     coreproc.CodeCgroupReadFailed,
		Reason:   "CGROUP_READ_FAILED",
		Public:   "Could not read the control group's counters",
		Private:  "service/proc/cgroup: reading the group's cpu.stat, cgroup.procs or job accounting failed",
		ExitCode: exitIOErr,
	}, errs.String("dir", dir))
}
