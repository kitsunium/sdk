//go:build !unix && !windows

// Package signal — non-Unix Relay stub: kill(2)-style group/pid delivery is not
// available off Unix, so Relay degrades to the typed UnsupportedPlatform
// sentinel instead of acting.
package signal

import (
	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

// Relay is unsupported off Unix: there is no portable kill(2) for pid/process
// group delivery, so it drains nothing and returns UnsupportedPlatform. The
// signature matches the Unix build so callers compile everywhere.
func Relay(src <-chan coreproc.Signal, target Target) error {
	//: no kill(2) on this platform; report the typed unsupported sentinel.
	_ = src
	//: target is meaningless without a delivery primitive.
	_ = target
	//: degrade rather than panic — the contract is a typed error, never a crash.
	return coreproc.UnsupportedPlatform
}
