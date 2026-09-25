//go:build windows

// Package cgroup_test — the facade contract on Windows, where a control group is
// a Job Object: Available is true, Create hands back a working Group, the
// controllers a Job Object can express apply, and the ones it cannot degrade to
// UnsupportedPlatform instead of pretending. cgroup_other_test.go asserted the
// blanket refusal here until the first Windows run of the whole suite showed
// the backend it had missed.
package cgroup_test

import (
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/pkg/v1/cgroup"
)

// TestAJobObjectThroughTheFacade drives the Group surface through the public
// names on the real kernel.
func TestAJobObjectThroughTheFacade(t *testing.T) {
	t.Parallel()
	//: Job Objects ship with every supported Windows.
	if !cgroup.Available() {
		t.Fatal("Available() = false on windows, want true: Job Objects ship with every supported release")
	}
	g, err := cgroup.Create("sdk-facade-windows")
	//: a Job Object stands behind the Group.
	if err != nil || g == nil {
		t.Fatalf("Create = (%v, %v), want a Job Object", g, err)
	}
	defer func() {
		//: released when the test ends.
		if derr := g.Delete(); derr != nil {
			t.Errorf("Delete = %v, want nil", derr)
		}
	}()
	//: a controller a Job Object expresses applies, and clears.
	if merr := g.SetMemoryMax(256 << 20); merr != nil {
		t.Fatalf("SetMemoryMax(256 MiB) = %v", merr)
	}
	//: and a limit of -1 clears it.
	if merr := g.SetMemoryMax(-1); merr != nil {
		t.Fatalf("SetMemoryMax(-1) = %v", merr)
	}
	//: the process count applies too.
	if perr := g.SetPidsMax(64); perr != nil {
		t.Fatalf("SetPidsMax(64) = %v", perr)
	}
	//: the ones it cannot express say so, typed, rather than succeed silently.
	for op, uerr := range map[string]error{
		"SetIOMax": g.SetIOMax("8:0 rbps=1048576"),
		"Freeze":   g.Freeze(),
		"Thaw":     g.Thaw(),
	} {
		//: each refused by its typed code.
		if !errs.HasCode(uerr, coreproc.CodeUnsupportedPlatform) {
			t.Errorf("%s on a Job Object = %v, want UNSUPPORTED_PLATFORM", op, uerr)
		}
	}
}
