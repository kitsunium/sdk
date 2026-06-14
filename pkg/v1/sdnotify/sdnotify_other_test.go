//go:build !linux

// Package sdnotify_test — non-Linux facade contract: the listener relies on
// SO_PASSCRED, a Linux facility, so the facade Listen forwards the central
// UnsupportedPlatform sentinel (and no listener/path) off Linux. Gated on the
// same `!linux` tag as the service stub so the e2e-vm CI binary validates the
// off-platform path on a real non-Linux kernel, not via a runtime.GOOS guess.
package sdnotify_test

import (
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/pkg/v1/sdnotify"
)

// TestListenUnsupportedOffLinux asserts the facade Listen returns the typed
// UnsupportedPlatform sentinel, no listener, and no path off Linux.
func TestListenUnsupportedOffLinux(t *testing.T) {
	t.Parallel()

	l, path, err := sdnotify.Listen()
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
