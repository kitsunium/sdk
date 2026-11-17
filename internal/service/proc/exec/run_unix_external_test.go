//go:build unix

package exec_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
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

// TestRunCancelledKillsAGroupThatOutlivesItsLeader is #1517 through Run: ctx
// ends on a Setpgid child whose shell honours SIGTERM and whose background
// member ignores it. The shell is reaped within the grace, and Run must still
// leave no member of the group behind — the member is SIGKILLed when the grace
// closes, so it is gone once Run returns.
//
// Goroutine lifecycle: one goroutine runs Run and reports on a buffered
// channel; the test always receives from it.
func TestRunCancelledKillsAGroupThatOutlivesItsLeader(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	trapped := filepath.Join(dir, "trapped")
	pidFile := filepath.Join(dir, "member.pid")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := svcexec.Run(ctx, coreproc.Spec{
			Path:    shellPath,
			Args:    []string{"sh", "-c", "(trap '' TERM; : > '" + trapped + "'; sleep 30) >/dev/null 2>&1 & echo $! > '" + pidFile + "'; wait"},
			Setpgid: true,
		}, coreproc.RunValue{MaxOutputBytes: 64, Grace: 300 * time.Millisecond})
		done <- err
	}()
	member := awaitPIDFile(t, trapped, pidFile)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run = %v, want context.Canceled", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Run did not return after its ctx ended")
	}
	//: an orphan SIGKILLed is a zombie until init reaps it: ESRCH follows
	//: within milliseconds, while a member nothing killed answers for 30 s.
	deadline := time.Now().Add(5 * time.Second)
	for !errors.Is(syscall.Kill(member, 0), syscall.ESRCH) {
		if time.Now().After(deadline) {
			//: leave nothing running behind a failed assertion.
			if kerr := syscall.Kill(member, syscall.SIGKILL); kerr != nil {
				t.Logf("killing the surviving member: %v", kerr)
			}
			t.Fatalf("member %d of the private group survived Run's stop", member)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// awaitPIDFile waits for the member's trap marker, then for the pid its shell
// wrote, failing after ten seconds.
func awaitPIDFile(t *testing.T, marker, pidFile string) int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, merr := os.Stat(marker)
		raw, rerr := os.ReadFile(pidFile)
		if merr == nil && rerr == nil && strings.HasSuffix(string(raw), "\n") {
			pid, perr := strconv.Atoi(strings.TrimSpace(string(raw)))
			if perr != nil {
				t.Fatalf("pid file %q: %v", raw, perr)
			}
			return pid
		}
		if time.Now().After(deadline) {
			t.Fatal("the child never installed its member's trap and recorded its pid")
		}
		time.Sleep(5 * time.Millisecond)
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
