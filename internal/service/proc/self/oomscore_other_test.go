//go:build !linux

package self_test

import (
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/proc/self"
)

// TestSetOOMScoreAdjUnsupported pins the refusal off Linux.
func TestSetOOMScoreAdjUnsupported(t *testing.T) {
	t.Parallel()
	if err := self.SetOOMScoreAdj(100); !errs.HasCode(err, coreproc.CodeUnsupportedPlatform) {
		t.Fatalf("SetOOMScoreAdj off Linux = %v, want UNSUPPORTED_PLATFORM", err)
	}
}
