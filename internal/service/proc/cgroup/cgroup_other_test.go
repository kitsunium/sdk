//go:build !linux && !windows

// Package cgroup_test — degrade contract mirror for targets with no control-group
// facility: cgroup v2 is Linux-only and Windows has a Job Object backend
// (cgroup_windows_test.go), so this covers darwin and the BSDs, where the stub
// reports Available() == false and Create returns the central UnsupportedPlatform
// sentinel. The full Linux lifecycle is covered by cgroup_external_test.go.
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
