//go:build !unix

package rlimit

import (
	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

// keepRaisedNoFile is the non-Unix stub: the Go runtime raises and restores
// RLIMIT_NOFILE only on Unix, so there is nothing to keep here.
func keepRaisedNoFile() (kept coreproc.LimitValue, err error) {
	//: the bare sentinel carries the no-cause UNSUPPORTED_PLATFORM error.
	return coreproc.LimitValue{}, coreproc.UnsupportedPlatform
}
