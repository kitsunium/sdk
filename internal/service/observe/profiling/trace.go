package profiling

import (
	"bytes"
	"context"
	"errors"
	"io"
	"runtime/trace"
	"sync"
	"time"

	coreprofiling "github.com/kitsunium/sdk/internal/core/observe/profiling"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// CaptureTrace records the process's execution trace (runtime/trace) for
// window and returns its bytes — the runtime's format, read by go tool trace,
// not parsed here. The runtime has one tracer: while one runs, another capture
// is refused with TraceBusy. A window outside (0, MaxTraceWindow] is
// WindowInvalid; a context that ends first stops the tracer and returns
// CaptureCanceled and no bytes. It runs beside a flight recorder.
func CaptureTrace(ctx context.Context, window time.Duration) ([]byte, error) {
	//: the window is measured on the wall clock.
	return captureTrace(ctx, clock.System, window)
}

// captureTrace is CaptureTrace on clk, which a white-box test sets to a
// ManualClock.
func captureTrace(ctx context.Context, clk clock.Waiter, window time.Duration) ([]byte, error) {
	//: a window with no length, or one too heavy to hold in memory.
	if window <= 0 || window > MaxTraceWindow {
		//: the window is a field.
		return nil, errs.Wrap(coreprofiling.WindowInvalid, errs.WrapParams{}, errs.String("window", window.String()))
	}
	var buf bytes.Buffer
	//: the runtime refuses a second tracer: that is the busy verdict.
	if err := trace.Start(&buf); err != nil {
		//: the runtime's sentence goes to the chain, not to Public.
		return nil, errors.Join(coreprofiling.TraceBusy, err)
	}
	timer := clk.NewTimer(window)
	defer timer.Stop()
	select {
	//: the window is complete.
	case <-timer.C():
	//: the caller stopped waiting: no partial trace.
	case <-ctx.Done():
		trace.Stop()
		//: the context's own error stays reachable.
		return nil, errors.Join(coreprofiling.CaptureCanceled, ctx.Err())
	}
	trace.Stop()
	//: the runtime's bytes, unparsed.
	return buf.Bytes(), nil
}

// FlightConfigValue sizes a flight recorder's window. A zero field takes the
// runtime's default (ADR 0031): 10 seconds, 10 MiB.
type FlightConfigValue struct {
	// MinAge is how far back the window reaches at least; MaxBytes overrides
	// it when the two disagree.
	MinAge time.Duration
	// MaxBytes caps the window's size in bytes.
	MaxBytes uint64
}

// flightRecorder is the runtime's flight recorder behind the FlightRecorder
// port.
type flightRecorder struct {
	// recorder is the runtime's.
	recorder *trace.FlightRecorder
	// writing serialises Snapshot: a concurrent one is refused, not queued.
	writing sync.Mutex
}

// StartFlightRecorder starts the process's flight recorder: the most recent
// execution trace kept in a bounded ring, snapshotted on demand — what the
// process was doing in the seconds before a latency spike or a watchdog. The
// runtime keeps one: a second is TraceBusy. A zero MinAge or MaxBytes takes
// the runtime's default (10 s, 10 MiB).
//
// IFACE-OPAQUE: the runtime's recorder stays behind the port, so the busy rule
// of a snapshot is the SDK's (TraceBusy) and a test can hold a double.
func StartFlightRecorder(cfg FlightConfigValue) (FlightRecorder, error) {
	recorder := trace.NewFlightRecorder(trace.FlightRecorderConfig{MinAge: max(cfg.MinAge, 0), MaxBytes: cfg.MaxBytes})
	//: the runtime keeps one flight recorder at a time.
	if err := recorder.Start(); err != nil {
		//: the runtime's sentence goes to the chain.
		return nil, errors.Join(coreprofiling.TraceBusy, err)
	}
	//: recording.
	return &flightRecorder{recorder: recorder}, nil
}

// Snapshot writes the recorder's window to w, in the runtime's trace format.
// A snapshot while another is being written is TraceBusy rather than the
// runtime's text; a stopped recorder is CaptureFailed.
func (r *flightRecorder) Snapshot(w io.Writer) (int64, error) {
	//: another snapshot is being written: refused, not queued.
	if !r.writing.TryLock() {
		return 0, coreprofiling.TraceBusy
	}
	defer r.writing.Unlock()
	n, err := r.recorder.WriteTo(w)
	//: a stopped recorder, or the writer's failure.
	if err != nil {
		return n, errs.Wrap(err, errs.WrapParams{
			Code:    coreprofiling.CodeCaptureFailed,
			Reason:  "CAPTURE_FAILED",
			Public:  coreprofiling.CaptureFailed.Public(),
			Private: coreprofiling.CaptureFailed.Private(),
		}, errs.String("profile", "trace"))
	}
	//: the window, written.
	return n, nil
}

// Stop ends the recording. It is idempotent.
func (r *flightRecorder) Stop() {
	//: idempotent: the runtime's Stop of a stopped recorder does nothing.
	r.recorder.Stop()
}
