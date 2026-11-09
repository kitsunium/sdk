//go:build linux

package cgroup_test

import (
	"os"
	"path/filepath"
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/pkg/v1/proc/cgroup"
)

// TestNestedGroupThroughTheFacade pins #727 through the public names, on a
// fake root under the test's own directory: a nested name, the controllers
// enabled on the way, the Linux group's four siblings, and a controller the
// root does not offer refused typed.
func TestNestedGroupThroughTheFacade(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "cgroup.controllers"), []byte("cpu memory pids\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	g, err := cgroup.Create("unit/main", cgroup.WithRoot(root), cgroup.WithControllers(cgroup.ControllerMemory))
	if err != nil {
		t.Fatalf("Create = %v", err)
	}
	tree, ok := g.(cgroup.GroupTree)
	if !ok || tree.Path() != filepath.Join(root, "unit", "main") {
		t.Fatalf("GroupTree %v, Path %q", ok, tree.Path())
	}
	if _, ok := g.(cgroup.GroupTuner); !ok {
		t.Error("the Linux group is no GroupTuner")
	}
	if _, ok := g.(cgroup.GroupWatcher); !ok {
		t.Error("the Linux group is no GroupWatcher")
	}
	if _, ok := g.(cgroup.GroupSignaler); !ok {
		t.Error("the Linux group is no GroupSignaler")
	}
	if got, _ := os.ReadFile(filepath.Join(root, "unit", "cgroup.subtree_control")); string(got) != "+memory" {
		t.Errorf("unit/cgroup.subtree_control = %q, want +memory", got)
	}
	if _, err := cgroup.Create("other", cgroup.WithRoot(root), cgroup.WithControllers(cgroup.ControllerIO)); !errs.HasCode(err, coreproc.CodeCgroupControllerAbsent) {
		t.Errorf("an io the root does not offer = %v, want CGROUP_CONTROLLER_ABSENT", err)
	}
}
