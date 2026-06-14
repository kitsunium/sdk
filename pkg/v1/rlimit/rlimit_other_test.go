//go:build !linux

// Package rlimit_test — non-Linux facade contract: setrlimit/prlimit64 live only
// on the Linux build, so the facade Apply and PrepareSysProcAttr forward the
// central UnsupportedPlatform sentinel off Linux. Gated on the same `!linux` tag
// as the service stub so the e2e-vm CI binary validates the off-platform path on
// a real non-Linux kernel, not via a runtime.GOOS guess.
package rlimit_test

import (
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/pkg/v1/rlimit"
)

// TestApplyUnsupportedOffLinux asserts the facade Apply forwards
// UnsupportedPlatform off Linux for any resource.
func TestApplyUnsupportedOffLinux(t *testing.T) {
	t.Parallel()

	err := rlimit.Apply(0, map[rlimit.Resource]rlimit.Limit{
		//: any resource will do; the stub short-circuits before mapping it.
		coreproc.ResourceNoFile: {Soft: 1024, Hard: 1024},
	})
	//: the facade must forward the central UNSUPPORTED_PLATFORM code.
	if !errs.HasCode(err, coreproc.CodeUnsupportedPlatform) {
		//: a missing code means the facade is not delegating faithfully off Linux.
		t.Fatalf("Apply off Linux = %v, want CodeUnsupportedPlatform", err)
	}
}

// TestPrepareUnsupportedOffLinux asserts the facade no-syscall validator forwards
// UnsupportedPlatform off Linux.
func TestPrepareUnsupportedOffLinux(t *testing.T) {
	t.Parallel()

	err := rlimit.PrepareSysProcAttr(map[rlimit.Resource]rlimit.Limit{
		//: the validator must short-circuit before the platform table off Linux.
		coreproc.ResourceNoFile: {Soft: 1024, Hard: 1024},
	})
	//: the facade must forward the central UNSUPPORTED_PLATFORM code.
	if !errs.HasCode(err, coreproc.CodeUnsupportedPlatform) {
		//: a missing code means the facade is not delegating faithfully off Linux.
		t.Fatalf("PrepareSysProcAttr off Linux = %v, want CodeUnsupportedPlatform", err)
	}
}
