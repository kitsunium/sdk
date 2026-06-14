//go:build unix

// Package exec — Unix umask / rlimit handling for the spawn. The Go runtime
// exposes no SysProcAttr hook to run setrlimit(2)/umask(2) in the child between
// fork and exec, so a Spec that requests these is honoured precisely where
// stdlib allows and otherwise surfaces a typed error rather than being silently
// dropped (the SDK's "no silent field loss" rule). See CLAUDE.md §Limitations.
package exec

import (
	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

// resourceLimits maps each coreproc.Resource to its platform RLIMIT_* constant,
// backing the spec validation. It is a flat lookup so checking a requested
// resource stays O(1) rather than a high-complexity switch.
var resourceLimits = buildResourceLimits()

// checkLimits validates the rlimit/umask fields of spec before the spawn and
// returns the typed error the caller must surface. An unmapped Resource is
// UnknownResource; any honourable-only-in-child field (a mapped Rlimit or a
// non-nil Umask) is RlimitFailed because the stdlib spawn cannot run
// setrlimit/umask in the child between fork and exec. A spec requesting none of
// these returns nil and spawns unaffected.
func checkLimits(spec coreproc.Spec) error {
	//: every requested resource must first name a real platform RLIMIT_*.
	for res := range spec.Rlimits {
		//: an undefined or unmapped resource is a caller error, checked first.
		if _, ok := resourceLimits[res]; !ok {
			//: bare sentinel: the resource names nothing this platform can set.
			return coreproc.UnknownResource
		}
	}
	//: a mapped Rlimit or a Umask can only be honoured by running setrlimit/umask
	//: in the child between fork and exec, which the stdlib spawn cannot do — so
	//: fail with a typed error rather than silently dropping the requested field.
	if len(spec.Rlimits) > 0 || spec.Umask != nil {
		//: bare sentinel: the field is valid but unhonourable on this runtime.
		return coreproc.RlimitFailed
	}
	//: no in-child-only attributes requested — the spawn proceeds unaffected.
	return nil
}
