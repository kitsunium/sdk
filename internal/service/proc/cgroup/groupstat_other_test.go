//go:build !linux && !windows && !freebsd

package cgroup_test

import (
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/proc/cgroup"
)

// TestNoGroupToReadHere pins #932's row where no backend exists: Create is
// UNSUPPORTED_PLATFORM, so there is no Group to assert a GroupReader on.
func TestNoGroupToReadHere(t *testing.T) {
	t.Parallel()
	g, err := cgroup.Create("probe")
	if g != nil || !errs.HasCode(err, coreproc.CodeUnsupportedPlatform) {
		t.Fatalf("Create = %v, %v; want no group and UNSUPPORTED_PLATFORM", g, err)
	}
}
