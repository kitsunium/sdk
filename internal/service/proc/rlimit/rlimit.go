// Package rlimit — applies per-process setrlimit(2) resource ceilings.
//
// This file holds the platform-neutral surface: the exported Apply and
// PrepareSysProcAttr entry points delegate to the build-tagged applyLimits /
// prepareLimits implementations (rlimit_linux.go on Linux, rlimit_other.go
// everywhere else). Core declares the Resource enum and LimitValue pair; this
// package maps each Resource to the platform RLIMIT_* constant and issues the
// syscall, wrapping failures in the central proc sentinels.
package rlimit

import (
	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

// Apply sets the soft/hard ceilings in limits on the process identified by pid.
// A pid of 0 (or the caller's own pid) targets the calling process via
// setrlimit(2); any other pid targets that process via prlimit64(2), which
// needs CAP_SYS_RESOURCE. An unmapped Resource yields UnknownResource and a
// failing syscall yields RlimitFailed; off Linux every call yields
// UnsupportedPlatform.
func Apply(pid int, limits map[coreproc.Resource]coreproc.LimitValue) error {
	//: delegate to the platform implementation chosen by build tag.
	return applyLimits(pid, limits)
}

// PrepareSysProcAttr validates limits and reports whether they can be applied,
// returning the same typed errors Apply would. It performs no syscall and
// mutates nothing: Go's os/exec SysProcAttr carries no rlimit field, so the
// caller applies limits post-fork from the child (see Apply with pid 0) rather
// than declaratively through SysProcAttr. Use it to fail fast — at Spec
// construction time — on a Resource this platform cannot honour, before a
// process is ever spawned.
func PrepareSysProcAttr(limits map[coreproc.Resource]coreproc.LimitValue) error {
	//: delegate validation to the platform implementation.
	return prepareLimits(limits)
}
