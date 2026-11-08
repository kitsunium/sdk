//go:build !linux && !freebsd

// Package notify_test — the facade contract off Linux and FreeBSD: the
// listener relies on the kernel stamping each datagram with its sender
// (SO_PASSCRED, LOCAL_CREDS_PERSISTENT), so the facade Listen forwards the
// central UnsupportedPlatform sentinel (and no listener/path) elsewhere. Gated
// on the same tag as the service stub so the e2e-cross CI binary validates the
// off-platform path on a real kernel, not via a runtime.GOOS guess.
package notify_test

import (
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/pkg/v1/proc/systemd/notify"
)

// TestListenUnsupportedOffLinux asserts the facade Listen returns the typed
// UnsupportedPlatform sentinel, no listener, and no path off Linux.
func TestListenUnsupportedOffLinux(t *testing.T) {
	t.Parallel()

	l, path, err := notify.Listen()
	//: the facade must hand back no listener off Linux.
	if l != nil {
		//: a non-nil listener off Linux is a contract breach.
		t.Fatalf("Listen off Linux returned a listener, want nil")
	}
	//: the facade must hand back no socket path off Linux.
	if path != "" {
		//: a non-empty path off Linux is a contract breach.
		t.Fatalf("Listen off Linux path = %q, want empty", path)
	}
	//: the facade must forward the central UNSUPPORTED_PLATFORM code.
	if !errs.HasCode(err, coreproc.CodeUnsupportedPlatform) {
		//: a missing code means the facade is not delegating faithfully off Linux.
		t.Fatalf("Listen off Linux = %v, want CodeUnsupportedPlatform", err)
	}
}

// TestListenForUnsupportedOffLinux asserts ListenFor refuses off Linux as
// Listen does (#730).
func TestListenForUnsupportedOffLinux(t *testing.T) {
	t.Parallel()
	l, path, err := notify.ListenFor(1000, 1000)
	if l != nil || path != "" || !errs.HasCode(err, coreproc.CodeUnsupportedPlatform) {
		t.Fatalf("ListenFor off Linux = %v, %q, %v, want UNSUPPORTED_PLATFORM", l, path, err)
	}
}
