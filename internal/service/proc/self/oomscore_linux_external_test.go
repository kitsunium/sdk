//go:build linux

package self_test

import (
	"os"
	osexec "os/exec"
	"strconv"
	"strings"
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/proc/self"
)

// oomHelperEnv makes the test binary run as the helper of
// TestSetOOMScoreAdj: the bias is process-wide and, once raised, cannot be
// lowered again without privilege, so it is changed in a process of its own.
const oomHelperEnv string = "SDK_SELF_OOM_HELPER"

// TestSetOOMScoreAdj runs a helper — this test binary again — that raises its
// own OOM-killer bias, reads it back from /proc/self/oom_score_adj, and asks
// for a value past the kernel's range, which must be RLIMIT_FAILED (#940).
func TestSetOOMScoreAdj(t *testing.T) {
	//: the helper half: set, read back, report.
	if os.Getenv(oomHelperEnv) != "" {
		runOOMHelper(t)
		return
	}
	t.Parallel()
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable = %v", err)
	}
	cmd := osexec.Command(self, "-test.run=^TestSetOOMScoreAdj$", "-test.count=1")
	cmd.Env = append(os.Environ(), oomHelperEnv+"=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helper = %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "oom_score_adj=900") {
		t.Fatalf("helper report %q, want oom_score_adj=900", out)
	}
}

// runOOMHelper raises the bias to 900 and reads it back.
func runOOMHelper(t *testing.T) {
	t.Helper()
	if err := self.SetOOMScoreAdj(900); err != nil {
		t.Fatalf("SetOOMScoreAdj(900) = %v", err)
	}
	raw, err := os.ReadFile("/proc/self/oom_score_adj")
	if err != nil {
		t.Fatalf("reading back = %v", err)
	}
	if err := self.SetOOMScoreAdj(5000); !errs.HasCode(err, coreproc.CodeRlimitFailed) {
		t.Fatalf("SetOOMScoreAdj(5000) = %v, want RLIMIT_FAILED", err)
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
	if _, err := os.Stdout.WriteString("oom_score_adj=" + strconv.Itoa(n) + "\n"); err != nil {
		t.Fatalf("writing the report = %v", err)
	}
}
