//go:build linux

// Package exec — Linux pre-exec cgroup v2 placement. When a Spec sets
// CgroupPath, the child must join that control group BEFORE it execs the target,
// so the controller limits (memory.max, pids.max, …) bind from the first
// instruction rather than after a post-spawn Group.Add(pid) race. The parent
// validates the path up front (validateCgroupPath); the trampoline then writes
// its own pid into <path>/cgroup.procs (applyCgroupPlacement) between the rlimit
// step and execve. cgroups are Linux-only, so this file is the only place that
// touches them; the !linux sibling rejects a non-empty path as UnsupportedPlatform.
package exec

import (
	"os"
	"path/filepath"
	"strconv"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// cgroupProcsFile is the cgroup v2 control file that attaches a process to the
// group when its pid is written (one pid per write).
const cgroupProcsFile string = "cgroup.procs"

// cgroupProcsPerm is the perm passed to os.WriteFile; cgroup.procs always exists
// so the mode is never used to create it — it mirrors the cgroup package's
// controllerPerm for consistency.
const cgroupProcsPerm os.FileMode = 0o755

// validateCgroupPath checks, before any spawn, that a non-empty Spec.CgroupPath
// names an existing cgroup v2 directory the child can be placed into. An empty
// path is a no-op (nil). A missing path, a non-directory, or a directory lacking
// the cgroup.procs control file surfaces CgroupUnavailable. The writability
// ("delegated") check is deferred to the trampoline write, which surfaces
// CgroupWriteFailed — a stat cannot reliably predict a cgroupfs write refusal.
func validateCgroupPath(path string) error {
	//: an empty path means the caller wants no cgroup placement — nothing to check.
	if path == "" {
		//: no placement requested; the spawn proceeds normally.
		return nil
	}
	info, err := os.Stat(path)
	//: the target must be an existing directory under the cgroup v2 hierarchy; a
	//: missing path or non-directory is not a usable cgroup — typed refusal.
	if err != nil || !info.IsDir() {
		//: surface CgroupUnavailable with the offending path for diagnosis.
		return wrapCgroupUnavailable(err, errs.String("cgroup_path", path))
	}
	//: every cgroup v2 directory exposes a cgroup.procs control file; its absence
	//: means this is not a delegated cgroup v2 node.
	if _, perr := os.Stat(filepath.Join(path, cgroupProcsFile)); perr != nil {
		//: no cgroup.procs — not a cgroup v2 directory; typed refusal pre-spawn.
		return wrapCgroupUnavailable(perr, errs.String("cgroup_path", path))
	}
	//: the path is a usable cgroup v2 directory; the trampoline will place the pid.
	return nil
}

// applyCgroupPlacement writes the calling (trampoline) process's pid into
// <path>/cgroup.procs, moving it — and thus the target it is about to exec — into
// the cgroup before execve. Returns "" on success or a diagnostic message the
// trampoline reports to the parent as a CgroupWriteFailed handshake. An empty
// path is a no-op (the trampoline only calls this when a path was encoded).
func applyCgroupPlacement(path string) string {
	//: defensive: an empty path is not a placement request.
	if path == "" {
		//: nothing to place; report success.
		return ""
	}
	//: cgroup.procs takes one pid per write to attach the writer to the group.
	procs := filepath.Join(path, cgroupProcsFile)
	//: write THIS process's pid — preserved across the imminent execve — so the
	//: target starts already inside the group.
	if err := os.WriteFile(procs, []byte(strconv.Itoa(os.Getpid())), cgroupProcsPerm); err != nil {
		//: a refused write (not delegated / controller off) must fail the spawn,
		//: never run the target unconfined.
		return "cgroup placement " + procs + ": " + err.Error()
	}
	//: the process — and the target it execs — is now a member of the cgroup.
	return ""
}
