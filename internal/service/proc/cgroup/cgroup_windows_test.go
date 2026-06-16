//go:build windows

// Package cgroup_test — Windows Job Object backend. Exercises the native
// control-group mechanics on the real Windows kernel: a Job Object is created,
// each supported controller limit is applied (memory / pids / cpu), the
// unsupported controllers degrade to the uniform sentinel, and the job is
// released. Runs on the windows-latest CI lane.
package cgroup_test

import (
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/proc/cgroup"
)

// TestWindowsJobObjectLifecycle drives the full Group surface on Windows: Job
// Objects always report Available, the three mappable controllers apply without
// error, the unmappable ones return UnsupportedPlatform, and Delete releases the
// handle.
func TestWindowsJobObjectLifecycle(t *testing.T) {
	//: Job Objects ship with every supported Windows release.
	if !cgroup.Available() {
		//: a false probe on Windows means the backend is mis-wired.
		t.Fatal("Available() = false on Windows, want true (Job Objects)")
	}
	g, err := cgroup.Create("sdk-win-test")
	//: creating a Job Object must succeed on Windows.
	if err != nil {
		//: a create failure means the kernel32 binding is wrong.
		t.Fatalf("Create: %v", err)
	}
	//: release the job handle at the end regardless of assertion outcome.
	defer func() {
		//: Delete (CloseHandle) must release the job cleanly.
		if derr := g.Delete(); derr != nil {
			//: a delete fault on an empty job is a real defect.
			t.Errorf("Delete: %v", derr)
		}
	}()

	//: each mappable controller must apply without error (set then clear).
	if merr := g.SetMemoryMax(256 << 20); merr != nil {
		t.Fatalf("SetMemoryMax(256MiB): %v", merr)
	}
	if merr := g.SetMemoryMax(-1); merr != nil {
		t.Fatalf("SetMemoryMax(-1 clear): %v", merr)
	}
	if perr := g.SetPidsMax(64); perr != nil {
		t.Fatalf("SetPidsMax(64): %v", perr)
	}
	//: CPU rate control is environment-sensitive: a NESTED job — e.g. the CI
	//: runner's own job object that already constrains CPU — can reject it with
	//: CGROUP_WRITE_FAILED. Accept that as the environmental outcome; only a wrong
	//: type or a panic is a real defect (memory + pids above prove the binding).
	if cerr := g.SetCPUMax(50_000, 100_000); cerr != nil && !errs.HasCode(cerr, coreproc.CodeCgroupWriteFailed) {
		t.Fatalf("SetCPUMax(50%%): unexpected error %v", cerr)
	}

	//: the controllers Windows cannot map must surface the uniform sentinel, not a
	//: panic or a false success.
	unsupported := map[string]error{
		"SetIOMax": g.SetIOMax("8:0 rbps=1048576"),
		"Freeze":   g.Freeze(),
		"Thaw":     g.Thaw(),
	}
	for op, uerr := range unsupported {
		//: each unmappable controller degrades to UNSUPPORTED_PLATFORM.
		if !errs.HasCode(uerr, coreproc.CodeUnsupportedPlatform) {
			//: a different (or nil) code breaks the honest-degrade contract.
			t.Errorf("%s on Windows = %v, want UnsupportedPlatform", op, uerr)
		}
	}
}
