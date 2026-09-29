package kit_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// BenchmarkAFreshProcessRunsADefaultCommand is what a status line pays per
// render when nothing is kept warm: exec a product binary — the smallest one,
// testdata/cliprobe: one service, one default fail-safe command — and wait
// for it to exit. It sees what BenchmarkMainRunsADefaultCommand cannot: the
// binary's load and every package's init. p50 and p99 are reported beside the
// mean.
func BenchmarkAFreshProcessRunsADefaultCommand(b *testing.B) {
	goTool, err := exec.LookPath("go")
	if err != nil {
		b.Skip("no go toolchain to build the probe")
	}
	bin := filepath.Join(b.TempDir(), "cliprobe")
	build := exec.Command(goTool, "build", "-o", bin, "./testdata/cliprobe")
	build.Env = append(os.Environ(), "GOWORK=off")
	if out, err := build.CombinedOutput(); err != nil {
		b.Fatalf("building the probe: %v\n%s", err, out)
	}
	took := make([]time.Duration, 0, b.N)
	for b.Loop() {
		begun := time.Now()
		if out, err := exec.Command(bin, "--width", "120").Output(); err != nil || string(out) != "ok\n" {
			b.Fatalf("the probe: %q, %v", out, err)
		}
		took = append(took, time.Since(begun))
	}
	slices.Sort(took)
	b.ReportMetric(float64(took[len(took)/2].Microseconds()), "p50-µs")
	b.ReportMetric(float64(took[len(took)*99/100].Microseconds()), "p99-µs")
}
