// Package cgroup_test — black-box tests for the public cgroup facade.
package cgroup_test

import (
	"runtime"
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/pkg/v1/cgroup"
)

// Create must agree with Available(): a caller that gates on the predicate and
// then calls Create should never be told one thing and handed another. Where
// the hierarchy is missing the facade must degrade to the typed sentinel and NO
// handle — a non-nil handle beside an error is the breach that matters, because
// a caller checking only err would then use it.
func TestCreateDelegates(t *testing.T) {
	t.Parallel()
	runCase := func(t *testing.T, name string) {
		t.Helper()
		g, err := cgroup.Create(name)
		//: on a delegated host Create genuinely works, and the contract to pin
		//: is the agreement with the predicate, not a refusal.
		if cgroup.Available() {
			if err != nil {
				t.Fatalf("Create = %v on a host where Available() is true", err)
			}
			if g == nil {
				t.Fatal("Create returned no handle and no error")
			}
			//: leave nothing behind; the group is empty, so Delete succeeds.
			if derr := g.Delete(); derr != nil {
				t.Errorf("Delete after Create = %v, want nil", derr)
			}
			return
		}
		if g != nil {
			t.Fatalf("Create returned a handle on a host where Available() is false")
		}
		//: off Linux the stub short-circuits before the hierarchy check.
		want := coreproc.CodeCgroupUnavailable
		if runtime.GOOS != "linux" {
			want = coreproc.CodeUnsupportedPlatform
		}
		if !errs.HasCode(err, want) {
			t.Fatalf("Create = %v, want code %v", err, want)
		}
	}
	type tc struct {
		name  string
		group string
	}
	//: distinct group names so the delegated branch cannot collide with itself.
	tests := []tc{
		{"a plain name", "sdk-facade-test-a"},
		{"a name with a dash", "sdk-facade-test-b"},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c.group)
		})
	}
}

// WithRoot must reach Create and change which directory is probed, rather than
// being accepted and ignored — an option that silently does nothing is worse
// than one that errors.
func TestWithRootOption(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		root string
	}
	tests := []tc{
		{"an absent absolute root", "/nonexistent/sdk/cgroup/root"},
		{"a root that is a file, not a directory", "/etc/hostname"},
		{"an empty root", ""},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		g, err := cgroup.Create("g", cgroup.WithRoot(c.root))
		if g != nil {
			t.Fatalf("Create with root %q returned a handle", c.root)
		}
		want := coreproc.CodeCgroupUnavailable
		if runtime.GOOS != "linux" {
			want = coreproc.CodeUnsupportedPlatform
		}
		if !errs.HasCode(err, want) {
			t.Fatalf("Create(root %q) = %v, want code %v", c.root, err, want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
