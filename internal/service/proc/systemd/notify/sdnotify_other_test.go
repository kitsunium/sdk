//go:build !linux && !freebsd

// Package notify_test — the listener contract off Linux and FreeBSD: the
// supervisor side relies on the kernel stamping every datagram with its sender
// (SO_PASSCRED, LOCAL_CREDS_PERSISTENT), so Listen returns the central
// UnsupportedPlatform sentinel elsewhere. This complement is built and run on
// darwin, Windows, OpenBSD, NetBSD, illumos and Solaris so the e2e-cross CI
// binary validates the degrade on a real kernel, with no runtime skip.
package notify_test

import (
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	notify "github.com/kitsunium/sdk/internal/service/proc/systemd/notify"
)

// TestListenUnsupportedOffLinuxTagged asserts the listener degrades to the typed
// UnsupportedPlatform sentinel off Linux, returning no listener and no path. It
// is the build-tagged complement of the untagged, runtime-gated
// TestListenUnsupportedOffLinux: this one is compiled ONLY off Linux, so the
// e2e-cross CI binary asserts the contract with no t.Skip on a real non-Linux kernel.
func TestListenUnsupportedOffLinuxTagged(t *testing.T) {
	t.Parallel()

	l, path, err := notify.Listen()
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

// TestListenForUnsupportedOffLinuxTagged asserts ListenFor's typed refusal
// off Linux (#730).
func TestListenForUnsupportedOffLinuxTagged(t *testing.T) {
	t.Parallel()
	l, path, err := notify.ListenFor(1000, 1000)
	if l != nil || path != "" || !errs.HasCode(err, coreproc.CodeUnsupportedPlatform) {
		t.Fatalf("ListenFor off Linux = %v, %q, %v, want UNSUPPORTED_PLATFORM", l, path, err)
	}
}
