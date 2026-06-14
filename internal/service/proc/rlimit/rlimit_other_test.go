//go:build !linux

// Package rlimit_test — non-Linux contract mirror: setrlimit/prlimit64 semantics
// and the RLIMIT_* table exist only on the Linux build, so every entry point
// degrades to the central UnsupportedPlatform sentinel off Linux. The Linux
// behaviour is covered by rlimit_external_test.go; this complement is built and
// run off Linux (darwin/windows/the BSDs) so the e2e-vm CI binary validates the
// off-platform degrade on a real non-Linux kernel.
package rlimit_test

import (
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/proc/rlimit"
)

// TestApplyUnsupportedOffLinux asserts Apply degrades to UnsupportedPlatform off
// Linux for any resource — the stub rejects before reaching the RLIMIT_* table.
func TestApplyUnsupportedOffLinux(t *testing.T) {
	t.Parallel()

	err := rlimit.Apply(0, map[coreproc.Resource]coreproc.LimitValue{
		//: any resource will do; the stub short-circuits before mapping it.
		coreproc.ResourceNoFile: {Soft: 1024, Hard: 1024},
	})
	//: the stub must surface the central UNSUPPORTED_PLATFORM code.
	if !errs.HasCode(err, coreproc.CodeUnsupportedPlatform) {
		//: a missing code breaks the degrade-gracefully contract off Linux.
		t.Fatalf("Apply off Linux = %v, want CodeUnsupportedPlatform", err)
	}
}

// TestPrepareUnsupportedOffLinux asserts the no-syscall validator likewise
// reports UnsupportedPlatform off Linux, matching what Apply would return.
func TestPrepareUnsupportedOffLinux(t *testing.T) {
	t.Parallel()

	err := rlimit.PrepareSysProcAttr(map[coreproc.Resource]coreproc.LimitValue{
		//: the validator must short-circuit before the platform table off Linux.
		coreproc.ResourceNoFile: {Soft: 1024, Hard: 1024},
	})
	//: the stub validator must surface the central UNSUPPORTED_PLATFORM code.
	if !errs.HasCode(err, coreproc.CodeUnsupportedPlatform) {
		//: a missing code breaks the fail-fast contract off Linux.
		t.Fatalf("PrepareSysProcAttr off Linux = %v, want CodeUnsupportedPlatform", err)
	}
}
