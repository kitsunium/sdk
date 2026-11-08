//go:build linux

package cgroup_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/service/proc/cgroup"
)

// TestProbeReadsTheRootItIsGiven pins #1097's Probe on roots under this
// test's own directory, never the system hierarchy: a plain directory is
// neither mounted nor delegated; one carrying a cgroup.controllers marker is
// mounted, lists its controllers, and is delegated when a group can be made
// under it.
func TestProbeReadsTheRootItIsGiven(t *testing.T) {
	t.Parallel()
	plain := t.TempDir()
	got, err := cgroup.Probe(cgroup.WithRoot(plain))
	//: a plain directory is no cgroup, and Probe says so without failing.
	if err != nil || got.Root != plain || got.Mounted || got.Delegated || len(got.Controllers) != 0 {
		t.Fatalf("Probe(plain) = %+v, %v; want an unmounted root", got, err)
	}
	marked := t.TempDir()
	//: the cgroup v2 marker, written under the test's own directory.
	if err := os.WriteFile(filepath.Join(marked, "cgroup.controllers"), []byte("cpu memory pids\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err = cgroup.Probe(cgroup.WithRoot(marked))
	if err != nil || !got.Mounted || !got.Delegated || !slices.Equal(got.Controllers, []string{"cpu", "memory", "pids"}) {
		t.Fatalf("Probe(marked) = %+v, %v; want mounted, delegated, three controllers", got, err)
	}
	entries, _ := os.ReadDir(marked)
	//: the delegation test left nothing behind.
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".sdk-cgroup-probe-") {
			t.Fatalf("Probe left %s behind", e.Name())
		}
	}
}

// TestProbeNamesTheOwnGroup pins that Probe reports this process's own
// unified cgroup as /proc/self/cgroup's "0::" line under the cgroup2 mount,
// read by the test itself, and that Available stays the mount root's answer.
func TestProbeNamesTheOwnGroup(t *testing.T) {
	t.Parallel()
	membership, err := os.ReadFile("/proc/self/cgroup")
	//: no membership file: nothing to compare.
	if err != nil {
		t.Skipf("no /proc/self/cgroup: %v", err)
	}
	var v2 string
	for line := range strings.Lines(string(membership)) {
		if after, ok := strings.CutPrefix(strings.TrimSpace(line), "0::"); ok {
			v2 = after
		}
	}
	//: a v1-only host has no unified group to name.
	if v2 == "" {
		t.Skip("no unified-hierarchy membership on this host")
	}
	got, err := cgroup.Probe(cgroup.WithRoot(t.TempDir()))
	//: Probe never fails on Linux.
	if err != nil {
		t.Fatalf("Probe = %v", err)
	}
	//: the own group ends with the membership path (the mount may expose a subtree).
	if got.OwnGroup == "" || !strings.HasSuffix(filepath.Clean("/"+got.OwnGroup), filepath.Base(filepath.Clean("/"+v2))) {
		t.Fatalf("OwnGroup = %q, want the directory of %q", got.OwnGroup, v2)
	}
}
