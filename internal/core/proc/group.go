// Package proc — the Group port: a cgroup v2 control group handle.
package proc

// Group is a handle to a cgroup v2 control group used to confine a process tree:
// set controller limits, attach processes, and remove the group. Implementations
// live in internal/service/proc/*; it is Linux-only and non-Linux builds return
// a no-op satisfying this interface with UNSUPPORTED_PLATFORM.
type Group interface {
	// SetMemoryMax writes memory.max (systemd MemoryMax=); a negative value
	// means "max" (no limit).
	SetMemoryMax(bytes int64) error
	// SetCPUMax writes cpu.max as "quota period" (systemd CPUQuota=); a negative
	// quota means "max".
	SetCPUMax(quota, period int64) error
	// SetPidsMax writes pids.max (systemd TasksMax=); a negative value means
	// "max".
	SetPidsMax(n int64) error
	// SetIOMax writes one io.max line verbatim (systemd IO*Max=), e.g.
	// "8:0 rbps=1048576".
	SetIOMax(spec string) error
	// Add moves the process pid into this group by writing cgroup.procs.
	Add(pid int) error
	// Delete removes the (empty) control group; callers move processes out first.
	Delete() error
}
