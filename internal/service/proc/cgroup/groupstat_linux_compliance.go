//go:build linux

package cgroup

import coreproc "github.com/kitsunium/sdk/internal/core/proc"

// Every control group of the proc family reads back what it confines (#932);
// a Linux one is also a node of the cgroup v2 hierarchy with the interface
// files Group leaves out, a state to watch and members to signal (#727).
var (
	_ coreproc.GroupReader   = (*controlGroup)(nil)
	_ coreproc.GroupTuner    = (*controlGroup)(nil)
	_ coreproc.GroupTree     = (*controlGroup)(nil)
	_ coreproc.GroupWatcher  = (*controlGroup)(nil)
	_ coreproc.GroupSignaler = (*controlGroup)(nil)
)
