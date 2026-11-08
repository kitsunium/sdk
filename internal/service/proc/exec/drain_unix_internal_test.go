//go:build unix

package exec

import (
	"errors"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// syncBuffer is a capture sink a test can read while the copier writes.
type syncBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

// Write appends p under the lock.
func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// String returns what was written so far.
func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// waitResult is one Wait outcome, carried over a channel.
type waitResult struct {
	exit coreproc.ExitValue
	err  error
}

// waitAsync runs Wait in a goroutine and hands its outcome over.
func waitAsync(h *handle) <-chan waitResult {
	out := make(chan waitResult, 1)
	go func() {
		exit, err := h.Wait()
		out <- waitResult{exit, err}
	}()
	return out
}

// TestWait_drainGraceBoundsTheCopierJoin spawns capture children whose leader
// exits while a grandchild may still hold its stdout (#1088). Each drain runs
// on a ManualClock, so the drain grace closes only when the test advances it:
//   - a grandchild holding stdout: Wait returns the real exit with
//     STDIO_DRAIN_TIMEOUT naming stdout once the default grace closes, the
//     parent's read end is closed, and no copier or reap goroutine outlives it
//     — on the base it hung until the grandchild exited;
//   - a grandchild that closed stdout before the leader exited: every byte is
//     delivered and Wait returns nil, the clock never advanced;
//   - a negative DrainGrace keeps the unbounded join: Wait returns nil once
//     the grandchild's own exit closes the pipe, the clock never advanced.
//
// It runs alone so the goroutine count it compares is its own.
func TestWait_drainGraceBoundsTheCopierJoin(t *testing.T) {
	type tc struct {
		name       string
		program    string
		grace      time.Duration
		advance    bool
		wantOutput string
		wantHeld   string
	}
	tests := []tc{
		{"a grandchild holding stdout", "echo hi; sleep 3 & exit 0", 0, true, "hi\n", "stdout"},
		{"a grandchild that let go first", "sleep 3 >/dev/null 2>&1 & echo done", 0, false, "done\n", ""},
		{"a negative grace waits for the grandchild", "sleep 0.3 & echo slow", -1, false, "slow\n", ""},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		before := runtime.NumGoroutine()
		var out syncBuffer
		h := startDirect(t, coreproc.Spec{
			Path:       shellPath,
			Args:       []string{"sh", "-c", c.program},
			Stdio:      coreproc.StdioCapture,
			Stdout:     &out,
			DrainGrace: c.grace,
		})
		manual := clock.NewManualClock(time.Unix(0, 0))
		h.stdio.drainClk = manual
		done := waitAsync(h)
		if c.advance {
			//: the drain timer is the only one this handle arms.
			manual.BlockUntil(1)
			//: the copier reads what the leader wrote on its own schedule; wait
			//: for it before the grace closes, this test is about the expiry.
			for deadline := time.Now().Add(5 * time.Second); out.String() != c.wantOutput && time.Now().Before(deadline); {
				time.Sleep(time.Millisecond)
			}
			manual.Advance(defaultDrainGrace)
		}
		var got waitResult
		select {
		case got = <-done:
		case <-time.After(10 * time.Second):
			t.Fatalf("Wait did not return: the copier join is unbounded")
		}
		if got.exit.Code != 0 || got.exit.Signaled {
			t.Errorf("exit = %+v, want the leader's own 0", got.exit)
		}
		if out.String() != c.wantOutput {
			t.Errorf("captured %q, want %q", out.String(), c.wantOutput)
		}
		if c.wantHeld == "" {
			if got.err != nil {
				t.Fatalf("Wait = %v, want nil", got.err)
			}
			return
		}
		if !errs.HasCode(got.err, coreproc.CodeStdioDrainTimeout) {
			t.Fatalf("Wait = %v, want STDIO_DRAIN_TIMEOUT", got.err)
		}
		if held := errField(got.err, "held"); !strings.Contains(held, c.wantHeld) {
			t.Errorf("STDIO_DRAIN_TIMEOUT held = %q, want it to name %s", held, c.wantHeld)
		}
		if _, err := h.stdio.outSrc[0].Read(make([]byte, 1)); !errors.Is(err, os.ErrClosed) {
			t.Errorf("Read on the parent's read end = %v, want os.ErrClosed", err)
		}
		deadline := time.Now().Add(2 * time.Second)
		for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		if n := runtime.NumGoroutine(); n > before {
			t.Errorf("goroutines = %d after Wait, %d before Start: a copier or reap goroutine leaked", n, before)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			runCase(t, c)
		})
	}
}

// errField returns the string field key of the *errs.Error in err's chain.
func errField(err error, key string) string {
	var e *errs.Error
	if !errors.As(err, &e) {
		return ""
	}
	for _, f := range e.Fields() {
		if f.Key() == key {
			return f.StringValue()
		}
	}
	return ""
}
