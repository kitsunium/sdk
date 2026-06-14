//go:build !linux

// Package cgroup_test — non-Linux contract mirror: cgroup v2 is Linux-only, so
// the stub reports Available() == false and Create returns the central
// UnsupportedPlatform sentinel on every non-Linux target. The full lifecycle is
// covered by cgroup_external_test.go on Linux; this complement is built and run
// off Linux (darwin/windows/the BSDs) so the e2e-vm CI binary validates the
// off-platform degrade on a real non-Linux kernel.
package cgroup_test

import (
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/proc/cgroup"
)

// TestCreateUnsupportedOffLinux asserts Create returns UnsupportedPlatform and no
// handle off Linux — the stub never reaches the hierarchy check.
func TestCreateUnsupportedOffLinux(t *testing.T) {
	t.Parallel()

	g, err := cgroup.Create("sdk-test-other")
	//: the non-Linux stub must never hand back a usable handle.
	if g != nil {
		//: a non-nil handle off Linux is a contract breach.
		t.Fatalf("Create off Linux returned a handle, want nil")
	}
	//: the stub must surface the central UNSUPPORTED_PLATFORM code.
	if !errs.HasCode(err, coreproc.CodeUnsupportedPlatform) {
		//: a missing code breaks the degrade-gracefully contract off Linux.
		t.Fatalf("Create off Linux = %v, want CodeUnsupportedPlatform", err)
	}
}

// TestAvailableFalseOffLinux asserts the delegation probe reports false off Linux
// without panicking — there is no unified cgroup v2 hierarchy to find.
func TestAvailableFalseOffLinux(t *testing.T) {
	t.Parallel()

	//: the probe must report false unconditionally off Linux.
	if cgroup.Available() {
		//: a true result off Linux means the stub is mis-wired.
		t.Fatalf("Available off Linux = true, want false")
	}
}
