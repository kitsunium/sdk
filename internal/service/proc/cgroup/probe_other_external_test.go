//go:build !linux

package cgroup_test

import (
	"runtime"
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/proc/cgroup"
)

// TestProbeOffLinux pins #1097's rows off Linux: Job Objects (Windows) and
// rctl (FreeBSD, when RACCT is in the kernel) need no delegation and have no
// hierarchy — Mounted and Delegated agree with Available, OwnGroup is empty;
// everywhere else Probe is UNSUPPORTED_PLATFORM.
func TestProbeOffLinux(t *testing.T) {
	t.Parallel()
	got, err := cgroup.Probe()
	switch runtime.GOOS {
	case "windows", "freebsd":
		if err != nil || got.Mounted != cgroup.Available() || got.Delegated != cgroup.Available() || got.OwnGroup != "" {
			t.Fatalf("Probe on %s = %+v, %v; want Mounted = Delegated = Available(), no own group", runtime.GOOS, got, err)
		}
	default:
		if !errs.HasCode(err, coreproc.CodeUnsupportedPlatform) {
			t.Fatalf("Probe on %s = %v, want UNSUPPORTED_PLATFORM", runtime.GOOS, err)
		}
	}
}
