//go:build !unix

// Package sdlisten_test — non-Unix contract mirror: socket activation rests on
// Unix file-descriptor inheritance, so every entry point degrades to the central
// UnsupportedPlatform sentinel off Unix. The end-to-end round-trip is covered by
// sdlisten_external_test.go on Unix; this complement is built and run off Unix
// (windows/plan9/js/wasip1) so the e2e-vm CI binary validates the off-platform
// degrade on a real non-Unix kernel.
package sdlisten_test

import (
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/proc/sdlisten"
)

// TestFilesUnsupportedOffUnix asserts the service side returns no fds and the
// typed UnsupportedPlatform sentinel off Unix.
func TestFilesUnsupportedOffUnix(t *testing.T) {
	t.Parallel()

	files, err := sdlisten.Files(false)
	//: the stub must recover no sockets off Unix.
	if files != nil {
		//: a non-nil slice off Unix is a contract breach.
		t.Fatalf("Files off Unix = %v, want nil", files)
	}
	//: the stub must surface the central UNSUPPORTED_PLATFORM code.
	if !errs.HasCode(err, coreproc.CodeUnsupportedPlatform) {
		//: a missing code breaks the degrade-gracefully contract off Unix.
		t.Fatalf("Files off Unix = %v, want CodeUnsupportedPlatform", err)
	}
}

// TestPrepareUnsupportedOffUnix asserts the activator side likewise reports
// UnsupportedPlatform off Unix — an activator cannot hand off fds where
// inheritance is unavailable.
func TestPrepareUnsupportedOffUnix(t *testing.T) {
	t.Parallel()

	err := sdlisten.Prepare(&coreproc.Spec{Path: "x"}, nil)
	//: the activator stub must surface the central UNSUPPORTED_PLATFORM code.
	if !errs.HasCode(err, coreproc.CodeUnsupportedPlatform) {
		//: a missing code breaks the degrade-gracefully contract off Unix.
		t.Fatalf("Prepare off Unix = %v, want CodeUnsupportedPlatform", err)
	}
}
