//go:build !unix

// Package rlimit_test — non-Unix contract mirror: setrlimit(2) is a Unix mechanic
// with no equivalent on these targets (Windows, plan9, js/wasm), so every entry
// point degrades to the central UnsupportedPlatform sentinel. Native Unix
// behaviour is covered by rlimit_unix_test.go; this complement runs on the
// non-Unix CI lanes so the off-platform degrade is validated on a real kernel.
package rlimit_test

import (
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/proc/rlimit"
)

// TestApplyUnsupportedOffUnix asserts Apply degrades to UnsupportedPlatform on a
// non-Unix target for any resource — the stub rejects before any RLIMIT_* table.
func TestApplyUnsupportedOffUnix(t *testing.T) {
	t.Parallel()

	err := rlimit.Apply(0, map[coreproc.Resource]coreproc.LimitValue{
		//: any resource will do; the stub short-circuits before mapping it.
		coreproc.ResourceNoFile: {Soft: 1024, Hard: 1024},
	})
	//: the stub must surface the central UNSUPPORTED_PLATFORM code.
	if !errs.HasCode(err, coreproc.CodeUnsupportedPlatform) {
		//: a missing code breaks the degrade-gracefully contract off Unix.
		t.Fatalf("Apply off Unix = %v, want CodeUnsupportedPlatform", err)
	}
}

// TestPrepareUnsupportedOffUnix asserts the no-syscall validator likewise reports
// UnsupportedPlatform on a non-Unix target, matching what Apply would return.
func TestPrepareUnsupportedOffUnix(t *testing.T) {
	t.Parallel()

	err := rlimit.PrepareSysProcAttr(map[coreproc.Resource]coreproc.LimitValue{
		//: the validator must short-circuit before the platform table off Unix.
		coreproc.ResourceNoFile: {Soft: 1024, Hard: 1024},
	})
	//: the stub validator must surface the central UNSUPPORTED_PLATFORM code.
	if !errs.HasCode(err, coreproc.CodeUnsupportedPlatform) {
		//: a missing code breaks the fail-fast contract off Unix.
		t.Fatalf("PrepareSysProcAttr off Unix = %v, want CodeUnsupportedPlatform", err)
	}
}
