//go:build windows

package cgroup

import coreproc "github.com/kitsunium/sdk/internal/core/proc"

// Every control group of the proc family reads back what it confines (#932);
// a Job Object also caps the memory of the whole job (#727).
var (
	_ coreproc.GroupReader = (*controlGroupWindows)(nil)
	_ coreproc.JobLimiter  = (*controlGroupWindows)(nil)
)
