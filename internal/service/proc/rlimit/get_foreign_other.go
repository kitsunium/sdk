//go:build unix && !linux

package rlimit

import coreproc "github.com/kitsunium/sdk/internal/core/proc"

// getForeign is refused off Linux: getrlimit(2) reads the caller only, as
// setrlimit(2) sets it only (rlimit_unix.go's rule).
func getForeign(_, _ int, _ coreproc.Resource) (coreproc.LimitValue, error) {
	//: the uniform answer of a platform without the mechanic.
	return coreproc.LimitValue{}, coreproc.UnsupportedPlatform
}
