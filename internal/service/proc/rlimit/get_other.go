//go:build !unix

package rlimit

import coreproc "github.com/kitsunium/sdk/internal/core/proc"

// getLimit is refused where setrlimit(2) has no equivalent.
func getLimit(_ int, _ coreproc.Resource) (coreproc.LimitValue, error) {
	//: the uniform answer of a platform without the mechanic.
	return coreproc.LimitValue{}, coreproc.UnsupportedPlatform
}
