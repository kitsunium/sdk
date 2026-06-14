//go:build unix

// Package exec — Unix best-effort post-start attributes: scheduling priority
// (Nice), OOM-killer bias (OOMScoreAdj). Each is applied after fork/exec and
// surfaces a typed error when the host refuses, rather than silently dropping a
// requested field.
package exec

import (
	"errors"
	"strconv"
	"syscall"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// prioProcess is the PRIO_PROCESS "which" selector for setpriority(2); it is not
// exported by package syscall on every Unix, so it is named here as the portable
// POSIX constant value 0.
const prioProcess int = 0

// applyNice sets the child's scheduling priority via setpriority(2). A nil Nice
// leaves the inherited value untouched; a refusal (e.g. lowering niceness
// without privilege) surfaces as RLIMIT_FAILED rather than a silent no-op.
func applyNice(pid int, nice *int) error {
	//: a nil Nice means "leave at the inherited default" — nothing to do.
	if nice == nil {
		//: no scheduling adjustment requested.
		return nil
	}
	//: setpriority(PRIO_PROCESS, pid, nice) applies the systemd Nice= analogue.
	if err := syscall.Setpriority(prioProcess, pid, *nice); err != nil {
		//: wrap the setpriority cause under the central RLIMIT_FAILED fields.
		return wrapRlimit(err, errs.Int("pid", pid), errs.Int("nice", *nice))
	}
	//: the scheduling priority was accepted by the kernel.
	return nil
}

// applyOOMScoreAdj writes the child's /proc/<pid>/oom_score_adj. It is
// Linux-only behaviour; on a Unix kernel without procfs the write fails and is
// reported, never silently ignored. A nil pointer leaves the default untouched.
func applyOOMScoreAdj(pid int, adj *int) error {
	//: a nil pointer means "leave the inherited OOM bias" — nothing to do.
	if adj == nil {
		//: no OOM adjustment requested.
		return nil
	}
	path := "/proc/" + strconv.Itoa(pid) + "/oom_score_adj"
	//: write the decimal score; the kernel validates the -1000..1000 range.
	if err := writeProcFile(path, strconv.Itoa(*adj)); err != nil {
		//: wrap the procfs write cause under the central RLIMIT_FAILED fields.
		return wrapRlimit(err, errs.Int("pid", pid), errs.Int("oom_score_adj", *adj))
	}
	//: the OOM bias was written to procfs.
	return nil
}

// containsESRCH reports whether err's chain carries the syscall.ESRCH errno,
// the "no such process" signal that a group already exited.
func containsESRCH(err error) bool {
	//: unwrap to the bottom errno and compare against ESRCH directly.
	return errors.Is(err, syscall.ESRCH)
}
