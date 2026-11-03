//go:build linux

package async_test

import (
	"syscall"
	"testing"
	"time"

	corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"
	"github.com/kitsunium/sdk/internal/core/observe/logger/level"
	"github.com/kitsunium/sdk/internal/service/observe/logger/middleware/async"
)

// processCPU is the CPU time, user and system, the whole process has used.
func processCPU(t *testing.T) time.Duration {
	t.Helper()
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		t.Fatalf("getrusage: %v", err)
	}
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}

// TestAnIdleAsyncSinkCostsNoCPU (#806): an async sink with nothing to deliver
// parks its drainer, so over an idle half second the process spends a few
// milliseconds of CPU at most — the drainer that spun on Gosched spent the
// whole half second of one P. Not parallel: the measure is the process's, and
// a parallel test would be counted with it.
func TestAnIdleAsyncSinkCostsNoCPU(t *testing.T) {
	down := &recordingSink{}
	s := async.New(down, async.Config{})
	defer func() { _ = s.Close() }()
	rec := corelogger.RecordEvent{Level: level.Info}
	for range 8 {
		if _, err := s.Write(t.Context(), rec, []byte("warm\n")); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	if err := s.Flush(t.Context()); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	const idle = 500 * time.Millisecond
	before := processCPU(t)
	time.Sleep(idle)
	used := processCPU(t) - before
	//: 5 % of the window: the collector and the runtime's own timers fit in it.
	if used > idle/20 {
		t.Fatalf("an idle async sink used %v of CPU over %v (want under %v)", used, idle, idle/20)
	}
	//: and it still delivers once woken.
	if _, err := s.Write(t.Context(), rec, []byte("after\n")); err != nil {
		t.Fatalf("Write after idle: %v", err)
	}
	if err := s.Flush(t.Context()); err != nil {
		t.Fatalf("Flush after idle: %v", err)
	}
	if got := down.writes.Load(); got != 9 {
		t.Fatalf("downstream saw %d writes, want 9", got)
	}
}
