//go:build unix

package exec

import (
	"runtime"
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Test_checkLimitsOOMScoreAdj pins ADR 0018's uniform refusal for the
// OOM-killer bias (#941): on Linux the field passes the pre-spawn check, and
// on every other Unix it is UnsupportedPlatform before the fork — never a
// child that runs unbiased until a procfs write fails after its start.
func Test_checkLimitsOOMScoreAdj(t *testing.T) {
	t.Parallel()
	adj := 0
	err := checkLimits(coreproc.Spec{OOMScoreAdj: &adj})
	//: the procfs knob's kernel.
	if runtime.GOOS == "linux" {
		if err != nil || !oomScoreAdjNative {
			t.Fatalf("checkLimits(OOMScoreAdj) on linux = %v (native %v), want nil", err, oomScoreAdjNative)
		}
		return
	}
	//: any other Unix: refused, typed, before anything is spawned.
	if oomScoreAdjNative || !errs.HasCode(err, coreproc.CodeUnsupportedPlatform) {
		t.Fatalf("checkLimits(OOMScoreAdj) on %s = %v (native %v), want UNSUPPORTED_PLATFORM", runtime.GOOS, err, oomScoreAdjNative)
	}
}
