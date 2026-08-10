//go:build !unix

// Package rlimit_test — non-Unix facade contract: setrlimit(2) has no equivalent
// off Unix, so the facade Apply and PrepareSysProcAttr forward the central
// UnsupportedPlatform sentinel there. Gated on the same `!unix` tag as the
// service stub (rlimit_other.go) so the e2e-vm CI binary validates the
// off-platform path on a real non-Unix kernel, not via a runtime.GOOS guess.
//
// `!unix` and NOT `!linux`: the service implements rlimits on every Unix target,
// Linux through prlimit64(2) and the BSDs through setrlimit(2), so a `!linux`
// tag pulled these cases onto darwin and the BSDs, where Apply legitimately
// succeeds and the UnsupportedPlatform expectation never holds.
package rlimit_test

import (
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/pkg/v1/rlimit"
)

// TestApplyUnsupportedOffUnix asserts the facade Apply forwards
// UnsupportedPlatform off Unix for any resource.
func TestApplyUnsupportedOffUnix(t *testing.T) {
	t.Parallel()

	err := rlimit.Apply(0, map[rlimit.Resource]rlimit.Limit{
		//: any resource will do; the stub short-circuits before mapping it.
		coreproc.ResourceNoFile: {Soft: 1024, Hard: 1024},
	})
	//: the facade must forward the central UNSUPPORTED_PLATFORM code.
	if !errs.HasCode(err, coreproc.CodeUnsupportedPlatform) {
		//: a missing code means the facade is not delegating faithfully off Unix.
		t.Fatalf("Apply off Unix = %v, want CodeUnsupportedPlatform", err)
	}
}

// TestPrepareUnsupportedOffUnix asserts the facade no-syscall validator forwards
// UnsupportedPlatform off Unix.
func TestPrepareUnsupportedOffUnix(t *testing.T) {
	t.Parallel()

	err := rlimit.PrepareSysProcAttr(map[rlimit.Resource]rlimit.Limit{
		//: the validator must short-circuit before the platform table off Unix.
		coreproc.ResourceNoFile: {Soft: 1024, Hard: 1024},
	})
	//: the facade must forward the central UNSUPPORTED_PLATFORM code.
	if !errs.HasCode(err, coreproc.CodeUnsupportedPlatform) {
		//: a missing code means the facade is not delegating faithfully off Unix.
		t.Fatalf("PrepareSysProcAttr off Unix = %v, want CodeUnsupportedPlatform", err)
	}
}
