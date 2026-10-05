//go:build windows

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
