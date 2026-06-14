//go:build !linux

// Package cgroup_test — non-Linux facade contract: cgroup v2 is Linux-only, so
// the facade Create forwards the central UnsupportedPlatform sentinel (and no
// handle) and Available reports false off Linux. Gated on the same `!linux` tag
// as the service stub so the e2e-vm CI binary validates the off-platform path on
// a real non-Linux kernel, not via a runtime.GOOS guess.
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
