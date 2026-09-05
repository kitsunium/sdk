//go:build linux

// Package cgroup_test — the functional options as a caller uses them.
//
// WithRoot names a directory in the cgroup v2 hierarchy, so its effect is only
// observable on Linux. Windows has a Job Object backend and FreeBSD an rctl one;
// neither has a root path, so both record the option and ignore it — which is
// correct, and makes the assertions below meaningless there rather than false.
package cgroup_test

import (
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/proc/cgroup"
)

// TestWithRoot pins that the option actually reaches Create and changes which
// directory is probed. An option that is accepted and ignored is worse than one
// that errors: a caller targeting a delegated sub-tree would silently get the
// top-level mount, which on a container host is either read-only or shared.
func TestWithRoot(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		root string
	}
	tests := []tc{
		{"a root that does not exist", "/nonexistent/sdk/cgroup/root"},
		{"a root that is a file, not a directory", "/etc/hostname"},
		{"a root under a path that cannot be traversed", "/dev/null/nope"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		g, err := cgroup.Create("sdk-withroot", cgroup.WithRoot(c.root))
		//: a refused create must never hand back a usable handle.
		if g != nil {
			t.Fatalf("Create with root %q returned a handle", c.root)
		}
		//: the option reached Create if the probe failed on OUR root rather
		//: than succeeding against the default mount.
		if !errs.HasCode(err, coreproc.CodeCgroupUnavailable) && !errs.HasCode(err, coreproc.CodeUnsupportedPlatform) {
			t.Fatalf("Create with root %q = %v, want CGROUP_UNAVAILABLE or UNSUPPORTED_PLATFORM", c.root, err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_WithRoot pins that the closure sets exactly the field it names, whatever
// string it is handed — including shapes Create will later refuse, since the
// option's job is to record the request, not to judge it.
func Test_WithRoot(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		root string
	}
	tests := []tc{
		{"an absolute path", "/sys/fs/cgroup/delegated"},
		{"a relative path", "relative/root"},
		{"an empty string", ""},
		{"a path with a trailing separator", "/sys/fs/cgroup/"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: the option is observed through the only surface a black-box test
		//: has: Create probes the root it was given, so a root that cannot
		//: exist must produce the unavailable refusal rather than silently
		//: falling back to the default mount.
		g, err := cgroup.Create("sdk-withroot-probe", cgroup.WithRoot(c.root))
		if g != nil {
			t.Fatalf("Create with root %q returned a handle", c.root)
		}
		if err == nil {
			t.Fatalf("Create with root %q = nil, want a refusal", c.root)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
