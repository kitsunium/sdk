//go:build windows

// Package exec — Windows cgroup-placement guard. A Spec.CgroupPath names a
// cgroup v2 directory, a Linux-only concept; Windows confines via Job Object
// assignment through the cgroup port (a handle, not a path), so a non-empty path
// at spawn has no meaning here and is rejected with the uniform sentinel.
package exec

import coreproc "github.com/kitsunium/sdk/internal/core/proc"

// validateCgroupPath accepts only an empty CgroupPath on Windows; a non-empty
// path requests Linux cgroup v2 placement that this platform cannot honour.
func validateCgroupPath(path string) error {
	//: no placement requested — nothing to reject.
	if path == "" {
		//: an empty path is the common, honourable case.
		return nil
	}
	//: a cgroup path is Linux-only; degrade honestly rather than ignore it.
	return coreproc.UnsupportedPlatform
}
