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
	// Kill atomically SIGKILLs every process in the group by writing "1" to
	// cgroup.kill (systemd KillMode=control-group; kernel >= 5.14). Unlike a
	// process-group kill, nothing escapes — a member that called setsid is still
	// in the control group. Returns UnsupportedPlatform when cgroup.kill is absent
	// (older kernels), so callers can fall back to SignalGroup.
	Kill() error
	// Freeze quiesces the whole group by writing "1" to cgroup.freeze (kernel
	// >= 5.2), stopping every member so a consistent signal sweep or snapshot can
	// run. Returns UnsupportedPlatform when cgroup.freeze is absent. Idempotent.
	Freeze() error
	// Thaw resumes a frozen group by writing "0" to cgroup.freeze. Returns
	// UnsupportedPlatform when cgroup.freeze is absent. Idempotent.
	Thaw() error
	// Delete removes the (empty) control group; callers move processes out first.
	Delete() error
}
