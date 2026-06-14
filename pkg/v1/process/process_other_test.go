//go:build !unix

// Package process_test — non-Unix facade contract: the keystone spawn primitive
// has no portable equivalent off Unix, so the facade Start must forward the
// central UnsupportedPlatform sentinel unchanged on every non-Unix target. Gated
// on the same `!unix` tag as the service stub so the e2e-vm CI binary validates
// the off-platform path on a real non-Unix kernel, not via a runtime.GOOS guess.
package process_test

import (
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/pkg/v1/process"
)

// TestStartUnsupportedOffUnix asserts the facade Start returns the typed
// UnsupportedPlatform sentinel off Unix. A non-empty Path proves the degrade is
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
