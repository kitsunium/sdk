// Package profiling — hosts the captures: the process's CPU over a bounded
// window, and its live heap.
package profiling

import (
	"bytes"
	"context"
	"errors"
	"runtime"
	"runtime/pprof"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// CaptureCPU samples the process's CPU for window and returns the profile,
// decoded. The runtime has ONE CPU profiler per process: while a capture —
// this one, or anyone's pprof.StartCPUProfile, net/http/pprof's included — is
// going, another is refused with [ProfilerBusy] rather than queued.
//
// A window that is not positive or exceeds [MaxCPUWindow] is refused with
// [WindowInvalid]. A context that ends before the window stops the profiler
// and returns [CaptureCanceled], joined with the context's error, and no
// profile: a partial window would read as the whole one.
//
// Each sample carries the pprof labels its goroutine had — set with pprof.Do
// or pprof.SetGoroutineLabels — which is how [Fold] can charge CPU to whoever
// was doing the work.
func CaptureCPU(ctx context.Context, window time.Duration) (*ProfileValue, error) {
	//: a window with no length, or one that would hold the profiler too long.
	if window <= 0 || window > MaxCPUWindow {
		//: the window is a field; the bound is in the Public text.
		return nil, errs.Wrap(WindowInvalid, errs.WrapParams{}, errs.String("window", window.String()))
	}
	var buf bytes.Buffer
	//: the runtime refuses a second profiler: that IS the busy verdict.
	if err := pprof.StartCPUProfile(&buf); err != nil {
		//: the runtime's own sentence goes to the chain, not to Public.
		return nil, errors.Join(ProfilerBusy, err)
	}
	timer := time.NewTimer(window)
	defer timer.Stop()
	//: the window, or the caller's end.
	select {
	//: the window is complete.
	case <-timer.C:
	//: the caller stopped waiting: no partial profile.
	case <-ctx.Done():
		pprof.StopCPUProfile()
		//: the context's own error stays reachable through errors.Is.
		return nil, errors.Join(CaptureCanceled, ctx.Err())
	}
	pprof.StopCPUProfile()
	//: the runtime's bytes, decoded.
	return Parse(buf.Bytes())
}

// CaptureHeap returns the live heap as the runtime's memory profiler last saw
// it, after a garbage collection, so the in-use figures describe what is
// reachable now rather than as of the previous cycle.
//
// A heap profile is SAMPLED: the runtime records about one allocation per
// runtime.MemProfileRate bytes (512 KiB by default) and scales each record
// back up, so a figure is an estimate — eight megabytes held may read as six
// or ten. Ask where the bytes are, not how many exactly. Heap samples carry
// no pprof labels; a caller charges them to an owner by their stack.
func CaptureHeap() (*ProfileValue, error) {
	runtime.GC()
	var buf bytes.Buffer
	//: the runtime writes the gzipped protocol-buffer form at debug 0.
	if err := pprof.Lookup("heap").WriteTo(&buf, 0); err != nil {
		//: the runtime's error goes to the chain.
		return nil, errs.Wrap(err, errs.WrapParams{
			Code: CodeCaptureFailed, Reason: "CAPTURE_FAILED", Public: CaptureFailed.Public(), Private: CaptureFailed.Private(),
		}, errs.String("profile", "heap"))
	}
	//: the runtime's bytes, decoded.
	return Parse(buf.Bytes())
}
