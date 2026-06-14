//go:build !unix

// Package exec_test — non-Unix contract mirror of exec_external_test.go: the
// fork/exec spawn primitive has no portable equivalent off Unix, so Start must
// degrade to the central UnsupportedPlatform sentinel on every non-Unix target
// (windows/plan9/js/wasip1) rather than acting or panicking. The unix build is
// covered by exec_external_test.go; this is its complement so the e2e-vm CI
// binary actually validates the off-platform path on a real non-Unix kernel.
package exec_test

import (
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcexec "github.com/kitsunium/sdk/internal/service/proc/exec"
)

// TestStartUnsupportedOffUnix asserts Start returns the typed UnsupportedPlatform
// sentinel off Unix. A non-empty Path is used so the degrade is the stub's
// platform decision, not the InvalidSpec guard (which the unix build exercises).
func TestStartUnsupportedOffUnix(t *testing.T) {
	t.Parallel()

	_, err := svcexec.Start(t.Context(), coreproc.Spec{Path: "x"})
	//: the no-op stub must surface UNSUPPORTED_PLATFORM, never a nil success.
	if !errs.HasCode(err, coreproc.CodeUnsupportedPlatform) {
		//: a missing code breaks the degrade-gracefully contract off Unix.
		t.Fatalf("Start off Unix = %v, want CodeUnsupportedPlatform", err)
	}
}
