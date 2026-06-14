//go:build !unix

// Package signal_test — non-Unix facade contract: Relay needs kill(2), which has
// no portable equivalent off Unix, so the facade Relay forwards the central
// UnsupportedPlatform sentinel off Unix (Parse/String/Notify stay portable).
// Gated on the same `!unix` tag as the service stub so the e2e-vm CI binary
// validates the off-platform path on a real non-Unix kernel.
package signal_test

import (
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/pkg/v1/signal"
)

// TestRelayUnsupportedOffUnix asserts the facade Relay degrades to the typed
// UnsupportedPlatform sentinel off Unix.
func TestRelayUnsupportedOffUnix(t *testing.T) {
	t.Parallel()

	//: a closed empty source still triggers the platform check on this build.
	src := make(chan signal.Signal)
	close(src)

	//: off Unix there is no kill(2); the facade must forward the typed sentinel.
	err := signal.Relay(src, signal.Target(1))
	//: a missing code means the facade is not delegating faithfully off Unix.
	if !errs.HasCode(err, coreproc.CodeUnsupportedPlatform) {
		//: the contract is UNSUPPORTED_PLATFORM, never a panic or a nil success.
		t.Fatalf("Relay off Unix = %v, want CodeUnsupportedPlatform", err)
	}
}
