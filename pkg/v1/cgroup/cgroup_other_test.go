//go:build !linux && !windows && !freebsd

// Package cgroup_test — the facade contract where there is no control-group
// backend at all (darwin, OpenBSD, NetBSD, DragonFly): Create forwards the
// central UnsupportedPlatform sentinel (and no handle) and Available reports
// false. Gated on the same `!linux && !windows && !freebsd` tag as the service
// stub: Windows has a Job Object backend (asserted by cgroup_windows_test.go)
// and FreeBSD an rctl one, and this file carried a bare `!linux` — asserting the
// refusal on both — until the first Windows run of the whole suite found it.
package cgroup_test

import (
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/pkg/v1/cgroup"
)

// TestCreateUnsupportedOffLinux asserts the facade Create returns
// UnsupportedPlatform and no handle off Linux.
func TestCreateUnsupportedOffLinux(t *testing.T) {
	t.Parallel()

	g, err := cgroup.Create("sdk-facade-other")
	//: the facade must never hand back a usable handle off Linux.
	if g != nil {
		//: a non-nil handle off Linux is a contract breach.
		t.Fatalf("Create off Linux returned a handle, want nil")
	}
	//: the facade must forward the central UNSUPPORTED_PLATFORM code.
	if !errs.HasCode(err, coreproc.CodeUnsupportedPlatform) {
		//: a missing code means the facade is not delegating faithfully off Linux.
		t.Fatalf("Create off Linux = %v, want CodeUnsupportedPlatform", err)
	}
}

// TestAvailableFalseOffLinux asserts the facade probe reports false off Linux
// without panicking.
func TestAvailableFalseOffLinux(t *testing.T) {
	t.Parallel()

	//: the facade probe must report false unconditionally off Linux.
	if cgroup.Available() {
		//: a true result off Linux means the facade is mis-wired.
		t.Fatalf("Available off Linux = true, want false")
	}
}
