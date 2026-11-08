//go:build freebsd

package cgroup

import coreproc "github.com/kitsunium/sdk/internal/core/proc"

// Every control group of the proc family reads back what it confines (#932).
var _ coreproc.GroupReader = (*controlGroupFreeBSD)(nil)
