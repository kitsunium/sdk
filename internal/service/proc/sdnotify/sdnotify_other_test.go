//go:build !linux

// Package sdnotify_test — non-Linux listener contract mirror: the supervisor side
// relies on SO_PASSCRED / SCM_CREDENTIALS, a Linux facility, so Listen returns
// the central UnsupportedPlatform sentinel off Linux. The Linux round-trip is
// covered by sdnotify_external_test.go; this complement is built and run off
// Linux (darwin/windows/the BSDs) so the e2e-vm CI binary validates the
// off-platform degrade on a real non-Linux kernel, with no runtime skip.
package sdnotify_test

import (
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	sdnotify "github.com/kitsunium/sdk/internal/service/proc/sdnotify"
)

// TestListenUnsupportedOffLinuxTagged asserts the listener degrades to the typed
// UnsupportedPlatform sentinel off Linux, returning no listener and no path. It
// is the build-tagged complement of the untagged, runtime-gated
// TestListenUnsupportedOffLinux: this one is compiled ONLY off Linux, so the
// e2e-vm CI binary asserts the contract with no t.Skip on a real non-Linux kernel.
func TestListenUnsupportedOffLinuxTagged(t *testing.T) {
	t.Parallel()

	l, path, err := sdnotify.Listen()
	//: the stub must hand back no listener off Linux.
	if l != nil {
		//: a non-nil listener off Linux is a contract breach.
		t.Fatalf("Listen off Linux returned a listener, want nil")
	}
	//: the stub must hand back no socket path off Linux.
	if path != "" {
		//: a non-empty path off Linux is a contract breach.
		t.Fatalf("Listen off Linux path = %q, want empty", path)
	}
	//: the stub must surface the central UNSUPPORTED_PLATFORM code.
	if !errs.HasCode(err, coreproc.CodeUnsupportedPlatform) {
		//: a missing code breaks the degrade-gracefully contract off Linux.
		t.Fatalf("Listen off Linux = %v, want CodeUnsupportedPlatform", err)
	}
}
