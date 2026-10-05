package rlimit

import (
	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

// apply is Apply's body: decl_gen.go writes Apply, from the
// design, as one call of it.
func apply(pid int, limits map[coreproc.Resource]coreproc.LimitValue) error {
	//: delegate to the platform implementation chosen by build tag.
	return applyLimits(pid, limits)
}

// prepareSysProcAttr is PrepareSysProcAttr's body: decl_gen.go writes PrepareSysProcAttr, from the
// design, as one call of it.
func prepareSysProcAttr(limits map[coreproc.Resource]coreproc.LimitValue) error {
	//: delegate validation to the platform implementation.
	return prepareLimits(limits)
}
