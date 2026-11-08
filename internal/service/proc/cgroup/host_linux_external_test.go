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

// liveRootEnv names a delegated cgroup v2 directory a lane hands to the test.
const liveRootEnv string = "KITSUNIUM_CGROUP_TEST_ROOT"

// mountedRoot is where the cgroup v2 hierarchy is mounted: the root Available
// answers for.
const mountedRoot string = "/sys/fs/cgroup"

// delegatedRoot returns the cgroup v2 directory this host delegates to the
// suite, or "" and why there is none. It is, in this order: the directory
// KITSUNIUM_CGROUP_TEST_ROOT names, when Probe finds it delegated (a lane's
// root whose children may take controllers — and one that is set but not
// delegated is no reason to look further); the mount, when Available (root on
// the linux-init lanes); this process's own group, when Probe says it is
// delegated (`systemd-run --user --scope -p Delegate=yes go test …`, where
// Available is false and Create(name, WithRoot(own)) works).
func delegatedRoot() (root, why string) {
	if root := os.Getenv(liveRootEnv); root != "" {
		if p, err := cgroup.Probe(cgroup.WithRoot(root)); err == nil && p.Delegated {
			return root, ""
		}
		return "", liveRootEnv + "=" + root + " is not a delegated cgroup v2 directory"
	}
	if cgroup.Available() {
		return mountedRoot, ""
	}
	if p, err := cgroup.Probe(); err == nil && p.OwnGroup != "" {
		if own, oerr := cgroup.Probe(cgroup.WithRoot(p.OwnGroup)); oerr == nil && own.Delegated {
			return p.OwnGroup, ""
		}
	}
	return "", "no cgroup v2 subtree is delegated to this test"
}

// liveRoot returns a cgroup v2 root delegated to this test, or skips.
func liveRoot(t *testing.T) string {
	t.Helper()
	root, why := delegatedRoot()
	if root == "" {
		t.Skip(why)
	}
	return root
}

// liveHost is where the conformance suite may make a group: the root, the
// options Create takes there, and whether this host lets it at all — false on a
// host that delegates nothing, where every live test asserts the typed refusal.
func liveHost(t *testing.T) (root string, opts []cgroup.Option, ok bool) {
	t.Helper()
	root, _ = delegatedRoot()
	if root == "" {
		return "", nil, false
	}
	return root, []cgroup.Option{cgroup.WithRoot(root)}, true
}

// childrenGet reports whether a group made under root gets controller's files:
// root's subtree_control lists it. Under a delegated scope it does not — the
// scope holds the test, so it cannot hand a controller down (cgroup v2's
// no-internal-process rule) — and a controller write there is refused typed.
func childrenGet(root, controller string) (bool, error) {
	enabled, err := os.ReadFile(filepath.Join(root, "cgroup.subtree_control"))
	if err != nil {
		return false, err
	}
	return slices.Contains(strings.Fields(string(enabled)), controller), nil
}
