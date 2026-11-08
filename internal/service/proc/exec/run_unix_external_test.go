//go:build unix

package exec_test

import (
	"context"
	"errors"
	"runtime"
	"syscall"
	"testing"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcexec "github.com/kitsunium/sdk/internal/service/proc/exec"
)

// TestWaitContext pins the bounded wait of #935: with a ctx that expires while
// the child sleeps, WaitContext returns ctx's error and leaves the child
// running; a later Wait still reports the real exit.
func TestWaitContext(t *testing.T) {
	t.Parallel()
	p, err := svcexec.Start(t.Context(), coreproc.Spec{Path: shellPath, Args: []string{"sh", "-c", "sleep 0.5; exit 4"}})
	if err != nil {
		t.Fatalf("Start = %v", err)
	}
	cw, ok := p.(coreproc.ContextWaiter)
	if !ok {
		t.Fatalf("the handle %T does not implement ContextWaiter", p)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	if _, werr := cw.WaitContext(ctx); !errors.Is(werr, context.DeadlineExceeded) {
		t.Fatalf("WaitContext = %v, want context.DeadlineExceeded", werr)
	}
	exit, werr := p.Wait()
	if werr != nil || exit.Code != 4 {
		t.Fatalf("Wait after WaitContext = (%+v, %v), want the real exit 4", exit, werr)
	}
	again, werr := cw.WaitContext(t.Context())
	if werr != nil || again.Code != 4 {
		t.Fatalf("WaitContext after the exit = (%+v, %v), want the memoised exit 4", again, werr)
	}
}

// TestRunBoundsTheOutput is #935's bomb: a child writing 64 MiB into a 1 MiB
// ceiling exits 0 with exactly 1 MiB of stdout kept and Truncated set, and the
// test process allocates less than 4 MiB doing it — the rest is read and
// discarded, so the child never blocks on a full pipe. It runs alone so the
// allocation count is its own.
func TestRunBoundsTheOutput(t *testing.T) {
	const ceiling = 1 << 20
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	result, err := svcexec.Run(t.Context(), coreproc.Spec{
		Path: shellPath,
		Args: []string{"sh", "-c", "/bin/dd if=/dev/zero bs=1048576 count=64 2>/dev/null"},
	}, coreproc.RunValue{MaxOutputBytes: ceiling})
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatalf("Run = %v", err)
	}
	if result.Exit.Code != 0 || !result.Truncated || len(result.Stdout) != ceiling {
		t.Fatalf("Run = exit %d, truncated %v, %d bytes of stdout; want 0, true, %d", result.Exit.Code, result.Truncated, len(result.Stdout), ceiling)
	}
	if grew := after.TotalAlloc - before.TotalAlloc; grew >= 4<<20 {
		t.Errorf("Run allocated %d bytes for a 1 MiB ceiling, want < 4 MiB", grew)
	}
}

// TestRunCancelledStopsTheChild pins Run's cancellation: ctx ends while the
// child runs, Run stops it — SIGTERM, then the grace — and returns what it had
// with ctx's error.
func TestRunCancelledStopsTheChild(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	result, err := svcexec.Run(ctx, coreproc.Spec{
		Path: shellPath,
		Args: []string{"sh", "-c", "echo started; exec sleep 30"},
	}, coreproc.RunValue{MaxOutputBytes: 64, Grace: 5 * time.Second})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run = %v, want context.DeadlineExceeded", err)
	}
	if !result.Exit.Signaled || result.Exit.Signal != coreproc.Signal(syscall.SIGTERM) {
		t.Errorf("exit = %+v, want a death by SIGTERM", result.Exit)
	}
	if string(result.Stdout) != "started\n" {
		t.Errorf("stdout = %q, want what the child wrote before the stop", result.Stdout)
	}
}

// TestRunRefusesAZeroCeiling pins ADR 0031: a ceiling of zero would keep
// nothing, so Run refuses it before spawning.
func TestRunRefusesAZeroCeiling(t *testing.T) {
	t.Parallel()
	_, err := svcexec.Run(t.Context(), coreproc.Spec{Path: shellPath}, coreproc.RunValue{})
	if !errs.HasCode(err, coreproc.CodeInvalidSpec) {
		t.Fatalf("Run(MaxOutputBytes 0) = %v, want INVALID_SPEC", err)
	}
}
