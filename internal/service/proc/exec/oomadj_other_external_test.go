//go:build unix && !linux

package exec_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/proc/exec"
)

// TestStartRefusesOOMScoreAdjOffLinux is #941's acceptance on darwin, the
// BSDs, illumos and Solaris: a Spec with an OOM-killer bias is refused by
// Start with UnsupportedPlatform and its program never runs. Before the fix
// the child ran, then the procfs write failed and the child was torn down with
// RLIMIT_FAILED.
func TestStartRefusesOOMScoreAdjOffLinux(t *testing.T) {
	t.Parallel()
	marker := filepath.Join(t.TempDir(), "ran")
	adj := 0
	spec := coreproc.Spec{Path: shellPath, Args: []string{shellPath, "-c", `: > "$0"`, marker}, OOMScoreAdj: &adj}
	p, err := exec.Start(context.Background(), spec)
	//: a refusal hands back no process.
	if p != nil {
		_, _ = p.Wait()
	}
	//: the typed answer of a platform without the knob.
	if !errs.HasCode(err, coreproc.CodeUnsupportedPlatform) {
		t.Fatalf("Start(OOMScoreAdj) = %v, want UNSUPPORTED_PLATFORM", err)
	}
	//: nothing was spawned: the program never ran.
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatalf("the refused program ran (marker: %v)", statErr)
	}
}
