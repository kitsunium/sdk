//go:build !unix

package rlimit_test

import (
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/proc/rlimit"
)

// TestKeepRaisedNoFileUnsupportedOffUnix asserts KeepRaisedNoFile degrades to
// UnsupportedPlatform where the Go runtime has no open-file limit to restore.
func TestKeepRaisedNoFileUnsupportedOffUnix(t *testing.T) {
	t.Parallel()
	_, err := rlimit.KeepRaisedNoFile()
	if !errs.HasCode(err, coreproc.CodeUnsupportedPlatform) {
		t.Fatalf("KeepRaisedNoFile off Unix = %v, want CodeUnsupportedPlatform", err)
	}
}
