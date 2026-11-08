//go:build unix

package exec

import (
	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

// resourceLimits maps each coreproc.Resource to its platform RLIMIT_* constant,
// backing the spec validation. It is a flat lookup so checking a requested
// resource stays O(1) rather than a high-complexity switch.
var resourceLimits = buildResourceLimits()

// checkLimits validates the rlimit/umask fields of spec before the spawn and
// returns the typed error the caller must surface. An Rlimits key naming a
// resource with no stdlib RLIMIT_* mapping (ResourceNProc / ResourceMemLock) is
// UnknownResource. Every mappable Rlimit and a non-nil Umask are honourable via
// the trampoline, so they pass validation here and are applied pre-exec. An
// OOMScoreAdj off Linux is UnsupportedPlatform: the bias is a procfs write on
// the live pid, so elsewhere it could only fail after the child had already
// run (ADR 0018, amended 2026-11-04). A spec requesting none of these returns
// nil and spawns unaffected.
func checkLimits(spec coreproc.Spec) error {
	//: every requested resource must name a real platform RLIMIT_*; an unmapped
	//: resource is a caller error surfaced before the spawn rather than dropped.
	for res := range spec.Rlimits {
		//: an undefined or unmapped resource names nothing this platform can set.
		if _, ok := resourceLimits[res]; !ok {
			//: bare sentinel: the resource is not settable on this platform.
			return coreproc.UnknownResource
		}
	}
	//: the OOM-killer bias has no kernel to receive it here: refused before
	//: the fork, never after the child ran unbiased.
	if spec.OOMScoreAdj != nil && !oomScoreAdjNative {
		//: the uniform answer of a platform without the mechanic.
		return coreproc.UnsupportedPlatform
	}
	//: mapped Rlimits and a non-nil Umask are applied pre-exec by the re-exec
	//: trampoline (see trampoline_unix.go), so no honourable field is rejected.
	return nil
}
