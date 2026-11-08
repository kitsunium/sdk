//go:build unix && !(linux && (amd64 || arm64))

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

// TestStartRefusesSandboxOffLinux is #728's guarantee on darwin, the BSDs,
// illumos, Solaris and Linux off amd64 and arm64: a Spec asking for a sandbox
// is refused by Start with UnsupportedPlatform before anything is spawned, and
// its program never runs — a sandbox asked for and not applied must never run
// the program unconfined.
func TestStartRefusesSandboxOffLinux(t *testing.T) {
	t.Parallel()
	marker := filepath.Join(t.TempDir(), "ran")
	spec := coreproc.Spec{
		Path: shellPath, Args: []string{shellPath, "-c", `: > "$0"`, marker},
		Sandbox: &coreproc.SandboxValue{NoNewPrivileges: true},
	}
	p, err := exec.Start(context.Background(), spec)
	//: a refusal hands back no process.
	if p != nil {
		_, _ = p.Wait()
	}
	//: the typed answer of a platform without the mechanism.
	if !errs.HasCode(err, coreproc.CodeUnsupportedPlatform) {
		t.Fatalf("Start(Sandbox) = %v, want UNSUPPORTED_PLATFORM", err)
	}
	//: nothing was spawned: the program never ran.
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatalf("the refused program ran (marker: %v)", statErr)
	}
	//: LandlockABI says the same.
	if _, abiErr := exec.LandlockABI(); !errs.HasCode(abiErr, coreproc.CodeUnsupportedPlatform) {
		t.Fatalf("LandlockABI() = %v, want UNSUPPORTED_PLATFORM", abiErr)
	}
}
