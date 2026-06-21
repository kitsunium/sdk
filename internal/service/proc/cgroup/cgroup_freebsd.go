//go:build freebsd

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
package cgroup

import (
	"errors"
	"os"
	"strconv"
	"sync"
	"syscall"
	"unsafe"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// FreeBSD syscall numbers (sys/kern/syscalls.master). Hand-declared to keep the
// package dep-light, mirroring the Windows backend's hand-cited kernel32 ABI.
const (
	sysRctlGetRacct   uintptr = 525
	sysRctlAddRule    uintptr = 528
	sysRctlRemoveRule uintptr = 529
)

// exitOSErr is sysexits.h EX_OSERR (71), restated to carry the cgroup sentinels'
// exit semantics when wrapping an rctl errno cause.
const exitOSErr int = 71

// pctScale converts a quota/period CPU fraction into rctl's pcpu percent unit.
const pctScale int64 = 100

// controlGroupFreeBSD is the FreeBSD core/proc.Group: the tracked member PIDs
// plus the stored limit rules re-applied to each member on Add.
type controlGroupFreeBSD struct {
	mu     sync.Mutex
	pids   []int
	memMax int64 // bytes; negative = unset
	cpuPct int64 // percent; negative = unset
}

// available reports whether the platform offers control groups by probing RACCT:
// it queries the current process's resource accounting via rctl_get_racct(2). A
// kernel built without `options RACCT`/`RCTL` reports the facility unsupported,
// so available() returns false rather than lying (a later Create would then have
// every rule fail). Any other outcome — success, ERANGE on the tiny probe
// buffer, EPERM — means rctl exists, so it defaults to true and never hides a
// working backend.
func available() bool {
	//: NUL-terminated "process:<pid>" filter for this process's accounting.
	filter := append([]byte("process:"+strconv.Itoa(os.Getpid())), 0)
	//: a 1-byte sink is intentionally too small — we only care about the errno.
	var out [1]byte
	//: rctl_get_racct(filter, len, out, len).
	_, _, errno := syscall.Syscall6(sysRctlGetRacct,
		uintptr(unsafe.Pointer(&filter[0])), uintptr(len(filter)),
		uintptr(unsafe.Pointer(&out[0])), uintptr(len(out)), 0, 0)
	//: ENOSYS / EOPNOTSUPP mean RACCT is not compiled in — unsupported.
	if errno == syscall.ENOSYS || errno == syscall.EOPNOTSUPP {
		//: honestly report the facility absent.
		return false
	}
	//: every other outcome means rctl exists — default to supported.
	return true
}

// createGroup creates an empty rctl-backed group. name/opts are cgroup-v2 path
// concepts with no rctl analogue, so they are accepted for parity and ignored.
func createGroup(_ string, _ ...Option) (g coreproc.Group, err error) {
	//: a fresh group starts with no members and no stored limits.
	return &controlGroupFreeBSD{memMax: -1, cpuPct: -1}, nil
}

// addRule applies one rctl rule string via rctl_add_rule(2).
func addRule(rule string) error {
	//: NUL-terminate the rule; inbuflen counts the terminator.
	buf := append([]byte(rule), 0)
	//: rctl_add_rule(inbuf, inbuflen, NULL, 0).
	_, _, errno := syscall.Syscall6(sysRctlAddRule,
		uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), 0, 0, 0, 0)
	//: a non-zero errno means the rule was rejected (or RACCT/RCTL is off).
	if errno != 0 {
		//: surface the rctl cause through the central CGROUP_WRITE_FAILED.
		return cgroupErr(errno, coreproc.CodeCgroupWriteFailed, "CGROUP_WRITE_FAILED",
			"Could not write the cgroup controller file",
			"service/proc/cgroup: rctl_add_rule failed")
	}
	//: the rule is in force.
	return nil
}

// removeRule removes every rctl rule matching the filter via rctl_remove_rule(2).
func removeRule(filter string) error {
	//: NUL-terminate the filter; inbuflen counts the terminator.
	buf := append([]byte(filter), 0)
	//: rctl_remove_rule(inbuf, inbuflen, NULL, 0).
	_, _, errno := syscall.Syscall6(sysRctlRemoveRule,
		uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), 0, 0, 0, 0)
	//: a non-zero errno means the removal failed.
	if errno != 0 {
		//: surface the rctl cause through the central CGROUP_DELETE_FAILED.
		return cgroupErr(errno, coreproc.CodeCgroupDeleteFailed, "CGROUP_DELETE_FAILED",
			"Could not delete the control group",
			"service/proc/cgroup: rctl_remove_rule failed")
	}
	//: the matching rules are gone.
	return nil
}

// applyTo (re)applies every stored limit to pid as a per-process rctl rule.
func (g *controlGroupFreeBSD) applyTo(pid int) error {
	//: a stored memory cap becomes a vmemoryuse:deny rule.
	if g.memMax >= 0 {
		//: process:PID:vmemoryuse:deny=BYTES.
		if err := addRule("process:" + strconv.Itoa(pid) + ":vmemoryuse:deny=" + strconv.FormatInt(g.memMax, 10)); err != nil {
			//: propagate the typed write failure.
			return err
		}
	}
	//: a stored CPU cap becomes a pcpu:deny rule.
	if g.cpuPct >= 0 {
		//: process:PID:pcpu:deny=PERCENT.
		if err := addRule("process:" + strconv.Itoa(pid) + ":pcpu:deny=" + strconv.FormatInt(g.cpuPct, 10)); err != nil {
			//: propagate the typed write failure.
			return err
		}
	}
	//: every stored limit now binds pid.
	return nil
}

// reapply pushes a freshly-stored limit to every current member.
func (g *controlGroupFreeBSD) reapply() error {
	//: walk each tracked member and reconcile its rules with the stored limits.
	for _, pid := range g.pids {
		//: drop the member's existing rules FIRST so a lowered or cleared cap
		//: takes effect — rctl_add_rule only adds, it never replaces, so without
		//: this a reduced limit would leave the prior (larger) rule in force.
		if err := removeRule("process:" + strconv.Itoa(pid)); err != nil {
			//: stop at the first removal the kernel rejects.
			return err
		}
		//: re-apply the current (possibly reduced or empty) limit set.
		if err := g.applyTo(pid); err != nil {
			//: stop at the first member the kernel rejects.
			return err
		}
	}
	//: all members now carry exactly the stored limits.
	return nil
}

// SetMemoryMax stores the per-process committed-memory cap and re-applies it to
// every current member. A negative value clears the cap.
func (g *controlGroupFreeBSD) SetMemoryMax(bytes int64) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	//: store the cap (negative = unset) and re-apply to members.
	g.memMax = bytes
	//: push the new limit to every tracked member.
	return g.reapply()
}

// SetCPUMax stores a pcpu cap derived from quota/period (e.g. 50000/100000 →
// 50%). A negative quota or non-positive period clears the cap.
func (g *controlGroupFreeBSD) SetCPUMax(quota, period int64) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	//: a valid quota/period becomes a percent; otherwise the cap is cleared.
	if quota >= 0 && period > 0 {
		//: pcpu percent = quota/period * 100.
		g.cpuPct = quota * pctScale / period
	} else {
		//: clear the stored CPU cap.
		g.cpuPct = -1
	}
	//: push the new limit to every tracked member.
	return g.reapply()
}

// SetPidsMax has no per-process rctl resource (maxproc is user/loginclass/jail
// scoped), so it degrades to UnsupportedPlatform.
func (g *controlGroupFreeBSD) SetPidsMax(_ int64) error {
	//: rctl maxproc is not a process-subject resource — honest typed degrade.
	return coreproc.UnsupportedPlatform
}

// SetIOMax takes a cgroup-format spec string with no portable rctl mapping, so
// it degrades to UnsupportedPlatform.
func (g *controlGroupFreeBSD) SetIOMax(_ string) error {
	//: the cgroup io.max spec has no rctl analogue — honest typed degrade.
	return coreproc.UnsupportedPlatform
}

// Add tracks pid as a member and applies every stored limit to it.
func (g *controlGroupFreeBSD) Add(pid int) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	//: bind the current limit set BEFORE tracking pid, so a failed apply never
	//: leaves a half-joined member that Kill/Delete would later act on.
	if err := g.applyTo(pid); err != nil {
		//: surface the typed write failure without recording membership.
		return err
	}
	//: only a successfully-confined process becomes a tracked member.
	g.pids = append(g.pids, pid)
	//: pid now carries every stored limit.
	return nil
}

// Kill SIGKILLs every member (rctl has no atomic group-kill primitive).
func (g *controlGroupFreeBSD) Kill() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	//: firstErr keeps the first genuine kill failure so the sweep still
	//: attempts every member before returning.
	var firstErr error
	//: walk each member and deliver SIGKILL.
	for _, pid := range g.pids {
		//: ESRCH (already gone) is the only benign outcome; surface anything
		//: else (EPERM/EINVAL) rather than reporting a false success.
		if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) && firstErr == nil {
			//: capture the first real failure with the typed cgroup sentinel.
			firstErr = cgroupErr(err, coreproc.CodeCgroupWriteFailed, "CGROUP_WRITE_FAILED",
				"Could not write the cgroup controller file",
				"service/proc/cgroup.Kill: syscall.Kill failed")
		}
	}
	//: nil when every member was signalled (or already gone).
	return firstErr
}

// Freeze has no rctl equivalent, so it degrades to UnsupportedPlatform.
func (g *controlGroupFreeBSD) Freeze() error {
	//: rctl cannot quiesce a process set — honest typed degrade.
	return coreproc.UnsupportedPlatform
}

// Thaw mirrors Freeze: no rctl equivalent.
func (g *controlGroupFreeBSD) Thaw() error {
	//: rctl cannot resume a process set — honest typed degrade.
	return coreproc.UnsupportedPlatform
}

// Delete removes every member's rctl rules (the rmdir analogue). Members keep
// running with their limits lifted, matching the cgroup "move processes out
// first" contract.
func (g *controlGroupFreeBSD) Delete() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	//: walk each member and drop its process-scoped rules.
	for _, pid := range g.pids {
		//: remove all rules filtered by this process subject.
		if err := removeRule("process:" + strconv.Itoa(pid)); err != nil {
			//: stop at the first removal the kernel rejects.
			return err
		}
	}
	//: clear the membership set; the group is now empty.
	g.pids = nil
	//: every member's rules are gone.
	return nil
}

// cgroupErr wraps an rctl errno cause in a central cgroup sentinel, restating the
// sentinel's code/reason/public verbatim with a FreeBSD-accurate private detail.
func cgroupErr(cause error, code errs.Code, reason, public, private string) error {
	//: restate the central sentinel's fields; the code is never re-Defined here.
	return errs.Wrap(cause, errs.WrapParams{
		Code:     code,
		Reason:   reason,
		Public:   public,
		Private:  private,
		ExitCode: exitOSErr,
	})
}
