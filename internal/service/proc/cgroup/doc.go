// Package cgroup — creates and manages cgroup v2 control groups.
//
// This file holds the platform-neutral surface: Available and Create delegate
// to the build-tagged available / createGroup implementations (cgroup_linux.go
// on Linux, cgroup_other.go everywhere else). The returned Group satisfies the
// core proc.Group port — set memory/cpu/pids/io ceilings, attach processes, and
// remove the group — and is implemented only against the unified cgroup v2
// hierarchy. Non-Linux builds and unprivileged/non-delegated Linux hosts degrade
// to the typed proc sentinels rather than panicking.
//
// Package cgroup — FreeBSD control groups via rctl(8) (RACCT/RCTL). FreeBSD has
// no cgroup v2 hierarchy; the native resource-limit mechanic is rctl, whose
// rules are strings of the form "subject:subject-id:resource:action=amount"
// applied through the rctl_add_rule(2) / rctl_remove_rule(2) syscalls. We bind
// those syscalls directly (dep-light, no golang.org/x/sys — the same discipline
// as the Windows Job Object backend), hand-citing the syscall numbers from
// FreeBSD sys/kern/syscalls.master.
//
// Mapping to the core/proc.Group port (a group here is a tracked PID set; rctl
// rules are per-process, so limits are stored and (re)applied to each member):
//   - SetMemoryMax → process:PID:vmemoryuse:deny=N
//   - SetCPUMax    → process:PID:pcpu:deny=PCT   (PCT = quota/period*100)
//   - Add          → apply every stored rule to PID
//   - Kill         → SIGKILL every member
//   - Delete       → rctl_remove_rule "process:PID" for every member
//
// SetPidsMax (rctl maxproc is a user/loginclass/jail resource, never a
// per-process one), SetIOMax (the spec string is cgroup-format), and
// Freeze/Thaw (rctl has no quiesce) return the uniform UnsupportedPlatform
// sentinel — exactly as the Windows backend degrades its non-mappable verbs.
//
// Package cgroup — Linux cgroup v2 control-group implementation.
//
// Package cgroup — degrade stub for platforms with no control-group facility:
// cgroup v2 is Linux-only, Windows has its own Job Object backend
// (cgroup_windows.go), and FreeBSD has its own rctl backend
// (cgroup_freebsd.go), so this covers darwin and the remaining BSDs
// (OpenBSD/NetBSD/DragonFly), where no equivalent exists.
//
// Package cgroup — Windows control groups via Job Objects. A Win32 Job Object is
// the native, kernel-enforced equivalent of a cgroup v2 control group: it caps a
// set of assigned processes' memory / CPU / process-count and can terminate them
// atomically. We bind the kernel32 entry points directly (syscall.NewLazyDLL, no
// golang.org/x/sys per the dep-light invariant) with the ABI struct layouts
// hand-declared and cited from the Win32 headers (winnt.h / jobapi2.h), the same
// discipline the proc trampoline already uses.
//
// Mapping to the core/proc.Group port:
//   - Create        → CreateJobObjectW
//   - SetMemoryMax  → JOBOBJECT_EXTENDED_LIMIT_INFORMATION.ProcessMemoryLimit
//   - SetCPUMax     → JOBOBJECT_CPU_RATE_CONTROL_INFORMATION (hard cap)
//   - SetPidsMax    → JOBOBJECT_BASIC_LIMIT_INFORMATION.ActiveProcessLimit
//   - Add           → OpenProcess + AssignProcessToJobObject
//   - Kill          → TerminateJobObject
//   - Delete        → CloseHandle
//
// SetIOMax and Freeze/Thaw have no stable, generally-available Job Object
// equivalent, so they return the uniform UnsupportedPlatform sentinel.
//
// Package cgroup — the Option functional-option type and its accumulator.
package cgroup
