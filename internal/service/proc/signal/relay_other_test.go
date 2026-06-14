//go:build !unix

// Package signal_test — non-Unix Relay contract: the stub must return the typed
// UnsupportedPlatform sentinel rather than acting or panicking.
package signal_test

import (
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcsignal "github.com/kitsunium/sdk/internal/service/proc/signal"
)

// TestRelayUnsupported asserts Relay degrades to UnsupportedPlatform off Unix.
func TestRelayUnsupported(t *testing.T) {
	t.Parallel()

	//: a closed empty source still triggers the platform check on this build.
	src := make(chan coreproc.Signal)
	close(src)

	//: off Unix there is no kill(2); Relay must report the typed sentinel.
	err := svcsignal.Relay(src, svcsignal.Target(1))
	//: the contract is UNSUPPORTED_PLATFORM, never a panic or a nil success.
	if !errs.HasCode(err, coreproc.CodeUnsupportedPlatform) {
		t.Fatalf("Relay err = %v, want CodeUnsupportedPlatform", err)
	}
}
