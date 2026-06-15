//go:build unix && !linux

// Package exec — cgroup placement on Unix platforms that are NOT Linux (the
// BSDs, Darwin). cgroup v2 is a Linux mechanism with no portable equivalent, so
// a non-empty Spec.CgroupPath is rejected up front with UnsupportedPlatform
// rather than silently ignored — silently dropping the placement would run the
// target unconfined, the exact failure the feature exists to prevent. The
// trampoline never reaches applyCgroupPlacement here because validateCgroupPath
// fails the spawn first; it is defined only to keep the unix build self-contained.
package exec

import coreproc "github.com/kitsunium/sdk/internal/core/proc"

// validateCgroupPath rejects a non-empty CgroupPath on a non-Linux Unix host
// with the uniform UnsupportedPlatform contract. An empty path is a no-op.
func validateCgroupPath(path string) error {
	//: no placement requested — nothing to reject.
	if path == "" {
		//: the spawn proceeds normally with no cgroup involvement.
		return nil
	}
	//: cgroup v2 has no equivalent here; surface the uniform off-platform error
	//: instead of running the target unconfined.
	return coreproc.UnsupportedPlatform
}

// applyCgroupPlacement is unreachable on this platform (validateCgroupPath fails
// the spawn before the trampoline runs); it returns a diagnostic for the
// defensive case rather than silently succeeding.
func applyCgroupPlacement(path string) string {
	//: an empty path is a no-op even on the defensive path.
	if path == "" {
		//: nothing to place.
		return ""
	}
	//: should never run — the parent rejected the path pre-spawn.
	return "cgroup placement unsupported on this platform"
}
