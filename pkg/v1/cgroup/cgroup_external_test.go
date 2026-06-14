// Package cgroup_test — black-box tests for the public cgroup facade.
package cgroup_test

import (
	"runtime"
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/pkg/v1/cgroup"
)

// TestCreateDelegates asserts the facade forwards to the service and degrades
// gracefully: on a non-delegated host Create returns the typed sentinel and no
// handle, never a panic.
func TestCreateDelegates(t *testing.T) {
	//: the live confinement path needs delegation; assert the contract otherwise.
	if cgroup.Available() {
		//: a delegated host is exercised by the service-level test; skip the dup.
		t.Skip("cgroup v2 delegated; service test covers the live path")
	}
	g, err := cgroup.Create("sdk-facade-test")
	//: an unavailable host must not return a usable handle.
	if g != nil {
		//: a non-nil handle here is a contract breach.
		t.Fatalf("Create returned a handle on an unavailable host")
	}
	//: pick the platform-correct sentinel.
	want := coreproc.CodeCgroupUnavailable
	//: off Linux the stub returns UNSUPPORTED_PLATFORM.
	if runtime.GOOS != "linux" {
		//: the non-Linux stub never reaches the hierarchy check.
		want = coreproc.CodeUnsupportedPlatform
	}
	//: the facade must surface the same typed code the service returns.
	if !errs.HasCode(err, want) {
		//: a mismatch means the facade is not delegating faithfully.
		t.Fatalf("Create = %v, want code %v", err, want)
	}
}

// TestWithRootOption asserts the WithRoot option is wired through to Create and
// changes which directory is probed: an obviously-absent root yields the typed
// unavailable error rather than a panic.
func TestWithRootOption(t *testing.T) {
	t.Parallel()
	g, err := cgroup.Create("g", cgroup.WithRoot("/nonexistent/sdk/cgroup/root"))
	//: an absent root cannot yield a handle.
	if g != nil {
		//: a non-nil handle against a bogus root is a contract breach.
		t.Fatalf("Create with bogus root returned a handle")
	}
	//: select the platform-correct sentinel.
	want := coreproc.CodeCgroupUnavailable
	//: off Linux the stub short-circuits before touching the root.
	if runtime.GOOS != "linux" {
		//: the non-Linux stub returns UNSUPPORTED_PLATFORM.
		want = coreproc.CodeUnsupportedPlatform
	}
	//: the bogus root must surface the expected typed code.
	if !errs.HasCode(err, want) {
		//: a missing code breaks the degrade-gracefully contract.
		t.Fatalf("Create(bogus root) = %v, want code %v", err, want)
	}
}
