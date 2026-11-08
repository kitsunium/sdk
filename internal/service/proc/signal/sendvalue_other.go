//go:build !linux && !freebsd

package signal

import coreproc "github.com/kitsunium/sdk/internal/core/proc"

// sendValue is refused where the SDK binds no value-carrying send: darwin has
// no sigqueue(2), and the other platforms are unverified.
func sendValue(_ Target, _ coreproc.Signal, _ int) error {
	//: the uniform answer of a platform without the mechanic.
	return coreproc.UnsupportedPlatform
}
