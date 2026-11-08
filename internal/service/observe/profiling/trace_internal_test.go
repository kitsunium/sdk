package profiling

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"

	coreprofiling "github.com/kitsunium/sdk/internal/core/observe/profiling"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// traceMu keeps this file's tests apart: the runtime has one tracer and one
// flight recorder per process.
var traceMu sync.Mutex

// TestCaptureTraceOnAManualClock pins #1022's capture: a 100 ms window ends
// when the clock advances, without sleeping, and returns the runtime's trace
// header; a second capture during it is TRACE_BUSY; a window out of bounds is
// WINDOW_INVALID.
func TestCaptureTraceOnAManualClock(t *testing.T) {
	traceMu.Lock()
	defer traceMu.Unlock()
	clk := clock.NewManualClock(time.Unix(0, 0))
	type result struct {
		data []byte
		err  error
	}
	done := make(chan result, 1)
	go func() {
		data, err := captureTrace(context.Background(), clk, 100*time.Millisecond)
		done <- result{data, err}
	}()
	clk.BlockUntil(1)
	//: the tracer is the process's one.
	if _, err := captureTrace(context.Background(), clk, time.Millisecond); !errs.HasCode(err, coreprofiling.CodeTraceBusy) {
		t.Fatalf("a second capture = %v, want TRACE_BUSY", err)
	}
	clk.Advance(100 * time.Millisecond)
	got := <-done
	if got.err != nil || !bytes.HasPrefix(got.data, []byte("go 1.")) {
		t.Fatalf("CaptureTrace = %d bytes (%q...), %v; want the trace header", len(got.data), got.data[:min(len(got.data), 8)], got.err)
	}
	for _, window := range []time.Duration{0, -time.Second, MaxTraceWindow + 1} {
		if _, err := captureTrace(context.Background(), clk, window); !errs.HasCode(err, coreprofiling.CodeWindowInvalid) {
			t.Fatalf("CaptureTrace(%v) = %v, want WINDOW_INVALID", window, err)
		}
	}
}

// TestFlightRecorder pins #1022's flight recorder: a zero config records, a
// snapshot after some goroutine activity writes bytes, a second recorder is
// TRACE_BUSY, a capture beside it succeeds, and two concurrent snapshots
// leave one TRACE_BUSY.
func TestFlightRecorder(t *testing.T) {
	traceMu.Lock()
	defer traceMu.Unlock()
	recorder, err := StartFlightRecorder(FlightConfigValue{})
	if err != nil {
		t.Fatalf("StartFlightRecorder = %v", err)
	}
	defer recorder.Stop()
	if _, err := StartFlightRecorder(FlightConfigValue{}); !errs.HasCode(err, coreprofiling.CodeTraceBusy) {
		t.Fatalf("a second recorder = %v, want TRACE_BUSY", err)
	}
	var wg sync.WaitGroup
	for range 64 {
		wg.Go(func() { time.Sleep(time.Millisecond) })
	}
	wg.Wait()
	clk := clock.NewManualClock(time.Unix(0, 0))
	done := make(chan error, 1)
	go func() { _, cerr := captureTrace(context.Background(), clk, time.Millisecond); done <- cerr }()
	clk.BlockUntil(1)
	clk.Advance(time.Millisecond)
	//: a capture subscribes beside the flight recorder.
	if err := <-done; err != nil {
		t.Fatalf("CaptureTrace beside a flight recorder = %v", err)
	}
	var buf bytes.Buffer
	if n, err := recorder.Snapshot(&buf); err != nil || n == 0 || buf.Len() == 0 {
		t.Fatalf("Snapshot = %d, %v; want bytes", n, err)
	}
	inner, ok := recorder.(*flightRecorder)
	if !ok {
		t.Fatal("StartFlightRecorder returned another implementation")
	}
	inner.writing.Lock()
	//: one snapshot in progress: the next is refused.
	if _, err := recorder.Snapshot(&bytes.Buffer{}); !errs.HasCode(err, coreprofiling.CodeTraceBusy) {
		t.Fatalf("a concurrent Snapshot = %v, want TRACE_BUSY", err)
	}
	inner.writing.Unlock()
	recorder.Stop()
	recorder.Stop()
}
