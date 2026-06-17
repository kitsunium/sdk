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
	"syscall"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// cgroupProcsFile is the cgroup v2 control file that attaches a process to the
// group when its pid is written (one pid per write).
const cgroupProcsFile string = "cgroup.procs"

// cgroup2SuperMagic is the statfs f_type of the unified cgroup v2 filesystem
// (CGROUP2_SUPER_MAGIC, the ASCII "cgrp"). Typed uint64 and compared against
// uint64(st.Type) so the arch-dependent Statfs_t.Type field (int64 on amd64/arm64,
// int32 on 386/arm) widens cleanly — the conversion is real on every GOARCH (the
// source is signed, the target unsigned), so it is never a redundant cast.
const cgroup2SuperMagic uint64 = 0x63677270

// cgroupProcsPerm is the perm passed to os.WriteFile; cgroup.procs always exists
// so the mode is never used to create it — it mirrors the cgroup package's
// controllerPerm for consistency.
const cgroupProcsPerm os.FileMode = 0o755

// validateCgroupPath checks, before any spawn, that a non-empty Spec.CgroupPath
// names an existing directory that ACTUALLY lives on the cgroup v2 filesystem.
// An empty path is a no-op (nil). A missing path, a non-directory, or a directory
// not on a cgroup2 mount surfaces CgroupUnavailable. The statfs magic check is
// load-bearing: a plain directory (even one containing a file literally named
// cgroup.procs) must NOT pass, or the trampoline's pid-write would silently no-op
// and the child would run UNCONFINED while Start reported success. The writability
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
	var st syscall.Statfs_t
	//: a statfs failure (path vanished, permission) is itself a usability refusal.
	if serr := syscall.Statfs(path, &st); serr != nil {
		//: surface CgroupUnavailable wrapping the statfs cause.
		return wrapCgroupUnavailable(serr, errs.String("cgroup_path", path))
	}
	//: the directory must sit on the cgroup2 filesystem — a normal directory that
	//: merely contains a cgroup.procs file is NOT confinement and is rejected here.
	if uint64(st.Type) != cgroup2SuperMagic {
		//: not a cgroup2 mount — refuse rather than spawn an unconfined child.
		return wrapCgroupUnavailable(nil, errs.String("cgroup_path", path),
			errs.String("reason", "path is not on a cgroup2 filesystem"))
	}
	//: the path is a real cgroup v2 directory; the trampoline will place the pid.
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
