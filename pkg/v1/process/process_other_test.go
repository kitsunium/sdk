//go:build !unix && !windows

// Package process_test — the facade contract where there is no spawn backend:
// neither Unix nor Windows (plan9, js, wasip1), the facade Start must forward
// the central UnsupportedPlatform sentinel unchanged. Gated on the same
// `!unix && !windows` tag as the service stub — Windows has a CreateProcess
// backend of its own, asserted by process_windows_test.go — so the build tag,
// not a runtime.GOOS guess, decides which contract a binary checks.
package process_test

import (
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/pkg/v1/process"
)

// TestStartUnsupportedOffUnix asserts the facade Start returns the typed
// UnsupportedPlatform sentinel where no spawn backend exists. A non-empty Path proves the degrade is
// the platform decision, not the InvalidSpec guard.
func TestStartUnsupportedOffUnix(t *testing.T) {
	t.Parallel()

	_, err := process.Start(t.Context(), process.Spec{Path: "x"})
	//: the facade must pass the central UNSUPPORTED_PLATFORM code straight through.
	if !errs.HasCode(err, coreproc.CodeUnsupportedPlatform) {
		//: a missing code means the facade is not delegating faithfully off Unix.
		t.Fatalf("Start off Unix = %v, want CodeUnsupportedPlatform", err)
	}
}
