//go:build unix && !linux && !darwin && !freebsd && !openbsd

package exec

import (
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Test_Start_ptyRefused pins #934's refusal row: NetBSD, DragonFly, illumos and
// Solaris refuse StdioPTY before spawning.
func Test_Start_ptyRefused(t *testing.T) {
	t.Parallel()
	_, err := Start(t.Context(), coreproc.Spec{Path: shellPath, Args: []string{"sh", "-c", "exit 0"}, Stdio: coreproc.StdioPTY})
	if !errs.HasCode(err, coreproc.CodeUnsupportedPlatform) {
		t.Fatalf("Start(StdioPTY) = %v, want UNSUPPORTED_PLATFORM", err)
	}
}
