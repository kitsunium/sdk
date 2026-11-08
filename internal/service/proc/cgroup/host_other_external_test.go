//go:build !linux

package cgroup_test

import (
	"testing"

	"github.com/kitsunium/sdk/internal/service/proc/cgroup"
)

// liveHost is where the conformance suite may make a group: off Linux there is
// no root to name — Windows names a Job Object and FreeBSD an rctl subject — so
// the root is empty and the options none, and the host lets the suite when
// Available says so.
func liveHost(t *testing.T) (root string, opts []cgroup.Option, ok bool) {
	t.Helper()
	return "", nil, cgroup.Available()
}

// childrenGet reports whether a group made under root gets controller's files:
// off Linux no controller is enabled in a parent, so every write the suite
// makes is its own.
func childrenGet(root, controller string) (bool, error) {
	return true, nil
}
