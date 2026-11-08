package proc

import "strconv"

// Controller is a cgroup v2 controller: String is its name as the kernel
// spells it in cgroup.controllers and cgroup.subtree_control. The constants
// are the controllers of Linux 6.12; the zero value is none and is not
// Known.
type Controller uint8

const (
	// ControllerCPU distributes CPU time: cpu.weight, cpu.max, cpu.max.burst.
	ControllerCPU Controller = iota + 1
	// ControllerCPUSet pins tasks to CPUs and memory nodes: cpuset.cpus,
	// cpuset.mems.
	ControllerCPUSet
	// ControllerIO distributes block I/O: io.weight, io.max, io.latency.
	ControllerIO
	// ControllerMemory bounds memory: memory.min/low/high/max, the swap and
	// zswap limits, memory.oom.group.
	ControllerMemory
	// ControllerPids bounds the number of tasks: pids.max.
	ControllerPids
	// ControllerHugeTLB bounds huge-page use: hugetlb.<size>.max.
	ControllerHugeTLB
	// ControllerRDMA bounds RDMA resources: rdma.max.
	ControllerRDMA
	// ControllerMisc bounds scalar resources the kernel lists: misc.max.
	ControllerMisc
)

// controllerNames holds each controller's kernel name, indexed by its value;
// the zero value's entry is empty.
var controllerNames = [...]string{
	ControllerCPU:     "cpu",
	ControllerCPUSet:  "cpuset",
	ControllerIO:      "io",
	ControllerMemory:  "memory",
	ControllerPids:    "pids",
	ControllerHugeTLB: "hugetlb",
	ControllerRDMA:    "rdma",
	ControllerMisc:    "misc",
}

// String returns the controller's kernel name, "memory", or
// "controller(N)" for a value no constant holds.
func (c Controller) String() string {
	//: a constant's own name.
	if c.Known() {
		return controllerNames[c]
	}
	//: the number, so a stray value is still readable.
	return "controller(" + strconv.Itoa(int(c)) + ")"
}

// Known reports whether c is one of the Controller constants.
func (c Controller) Known() bool {
	//: within the table and not the unminted zero.
	return int(c) < len(controllerNames) && controllerNames[c] != ""
}

// GroupEventsValue is a control group's state as GroupWatcher.Watch reads it
// after each change: its cgroup.events keys and its memory.events counters.
type GroupEventsValue struct {
	// MemoryEvents counts what the group's memory limits did — memory.events,
	// zero where the memory controller is not enabled for the group.
	MemoryEvents MemoryEventsValue
	// Populated reports that the group or a group beneath it holds a live
	// process (cgroup.events "populated").
	Populated bool
	// Frozen reports that the group is frozen (cgroup.events "frozen").
	Frozen bool
}
