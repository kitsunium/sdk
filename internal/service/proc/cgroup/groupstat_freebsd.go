package cgroup

import (
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// The rctl_get_racct(2) reading of one member: "key=value" pairs joined by
// commas, the amounts in the units rctl(8) documents.
const (
	// racctBufferSize holds every resource rctl accounts for one process.
	racctBufferSize int = 4096
	// racctSeparator joins the pairs.
	racctSeparator string = ","
	// racctAssign splits a pair.
	racctAssign string = "="
	// racctCPUTime is the member's CPU time in seconds.
	racctCPUTime string = "cputime"
	// racctMemoryUse is the member's resident memory in bytes.
	racctMemoryUse string = "memoryuse"
	// racctBase and racctBits are how an amount parses: decimal, 64 bits.
	racctBase int = 10
	racctBits int = 64
	// exitIOErr is sysexits.h EX_IOERR (74), the exit status CGROUP_READ_FAILED
	// carries; restated to wrap a cause without re-Defining the central code.
	exitIOErr int = 74
)

// Stat sums what rctl accounts for each tracked member: its CPU time and its
// resident memory. rctl keeps no group of its own, so CPU is whole seconds,
// there is no peak, no throttling, no events and no I/O: the value is Partial.
func (g *controlGroupFreeBSD) Stat() (coreproc.GroupStatValue, error) {
	members, _ := g.Members()
	stat := coreproc.GroupStatValue{PidsCurrent: int64(len(members)), Partial: true}
	//: one accounting per member; a member that exited counts nothing.
	for _, pid := range members {
		cpu, memory, err := racct(pid)
		//: gone since it was added.
		if err != nil {
			continue
		}
		stat.CPUUsage += cpu
		stat.MemoryCurrent += memory
	}
	//: the sums, partial by nature.
	return stat, nil
}

// Members lists the tracked pids.
func (g *controlGroupFreeBSD) Members() ([]int, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	//: a copy: the set moves with Add and Delete.
	return slices.Clone(g.pids), nil
}

// racct reads one process's CPU time and resident memory.
func racct(pid int) (cpu time.Duration, memory int64, err error) {
	filter := append([]byte("process:"+strconv.Itoa(pid)), 0)
	out := make([]byte, racctBufferSize)
	//: rctl_get_racct(filter, len, out, len).
	_, _, errno := syscall.Syscall6(sysRctlGetRacct,
		uintptr(unsafe.Pointer(&filter[0])), uintptr(len(filter)),
		uintptr(unsafe.Pointer(&out[0])), uintptr(len(out)), 0, 0)
	//: the member exited, or rctl refused.
	if errno != 0 {
		return 0, 0, errs.Wrap(errno, errs.WrapParams{
			Code:     coreproc.CodeCgroupReadFailed,
			Reason:   "CGROUP_READ_FAILED",
			Public:   "Could not read the control group's counters",
			Private:  "service/proc/cgroup: rctl_get_racct failed",
			ExitCode: exitIOErr,
		}, errs.Int("pid", pid))
	}
	text, _, _ := strings.Cut(string(out), "\x00")
	//: one pair at a time.
	for pair := range strings.SplitSeq(text, racctSeparator) {
		key, raw, _ := strings.Cut(pair, racctAssign)
		value, perr := strconv.ParseInt(raw, racctBase, racctBits)
		//: not a count.
		if perr != nil {
			continue
		}
		switch key {
		case racctCPUTime:
			cpu = time.Duration(value) * time.Second
		case racctMemoryUse:
			memory = value
		default:
		}
	}
	//: what rctl accounts.
	return cpu, memory, nil
}
