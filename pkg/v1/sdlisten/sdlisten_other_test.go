//go:build !unix

// Package sdlisten_test — non-Unix facade contract: socket activation rests on
// Unix fd inheritance, so the facade Files/Prepare forward the central
// UnsupportedPlatform sentinel off Unix. There is no Unix facade test today (the
// service layer carries the round-trip), so this file is the facade's sole
// off-platform contract assertion, built and run by the e2e-vm CI binary on a
// real non-Unix kernel.
package sdlisten_test

import (
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/pkg/v1/sdlisten"
)

// TestFilesUnsupportedOffUnix asserts the facade service side returns no fds and
// the typed UnsupportedPlatform sentinel off Unix.
func TestFilesUnsupportedOffUnix(t *testing.T) {
	t.Parallel()

	files, err := sdlisten.Files(false)
	//: the facade must recover no sockets off Unix.
	if files != nil {
		//: a non-nil slice off Unix is a contract breach.
		t.Fatalf("Files off Unix = %v, want nil", files)
	}
	//: the facade must forward the central UNSUPPORTED_PLATFORM code.
	if !errs.HasCode(err, coreproc.CodeUnsupportedPlatform) {
		//: a missing code means the facade is not delegating faithfully off Unix.
		t.Fatalf("Files off Unix = %v, want CodeUnsupportedPlatform", err)
	}
}

// TestPrepareUnsupportedOffUnix asserts the facade activator side forwards
// UnsupportedPlatform off Unix.
func TestPrepareUnsupportedOffUnix(t *testing.T) {
	t.Parallel()

	err := sdlisten.Prepare(&sdlisten.Spec{Path: "x"}, nil)
	//: the facade must forward the central UNSUPPORTED_PLATFORM code.
	if !errs.HasCode(err, coreproc.CodeUnsupportedPlatform) {
		//: a missing code means the facade is not delegating faithfully off Unix.
		t.Fatalf("Prepare off Unix = %v, want CodeUnsupportedPlatform", err)
	}
}
