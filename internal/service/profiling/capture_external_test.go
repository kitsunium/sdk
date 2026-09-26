// Package profiling_test — the captures, on the test process itself. The CPU
// tests do not run in parallel: the process has one CPU profiler.
package profiling_test

import (
	"context"
	"errors"
	"io"
	"runtime"
	"runtime/pprof"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/profiling"
)

// spin keeps the sink busy so the compiler keeps the loop.
var spin atomic.Int64

// burn uses a CPU until stop is set.
func burn(stop *atomic.Bool) {
	for !stop.Load() {
		var x int64
		for i := range 50_000 {
			x += int64(i % 7)
		}
		spin.Add(x)
	}
}

// TestACPUProfileChargesTheLabelledBurner captures one second while a
// goroutine labelled "node=burner" spins, and checks the fold charges the
// burner: the labels reach the samples, and the attribution is the caller's.
func TestACPUProfileChargesTheLabelledBurner(t *testing.T) {
	var stop atomic.Bool
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			pprof.Do(context.Background(), pprof.Labels("node", "burner"), func(context.Context) { burn(&stop) })
		})
	}
	p, err := profiling.CaptureCPU(t.Context(), time.Second)
	stop.Store(true)
	wg.Wait()
	if err != nil {
		t.Fatal(err)
	}
	if p.Duration < 900*time.Millisecond || p.Period <= 0 || p.PeriodType.Type != "cpu" {
		t.Fatalf("header: %+v", p)
	}
	f, err := profiling.Fold(p, profiling.FoldConfig{Attribute: byNode})
	if err != nil {
		t.Fatal(err)
	}
	if f.SampleType.Unit != "nanoseconds" || f.Total <= 0 || len(f.Owners) == 0 || f.Owners[0].Owner != "burner" {
		t.Fatalf("fold: total %d, owners %+v", f.Total, f.Owners)
	}
	top := f.Owners[0].Top
	if len(top) == 0 || !strings.HasSuffix(top[0].Function, ".burn") || !strings.HasSuffix(top[0].File, "capture_external_test.go") || top[0].StartLine == 0 {
		t.Errorf("the burner's top function: %+v", top)
	}
	sum := f.Unattributed
	for _, o := range f.Owners {
		sum += o.Value
	}
	if sum != f.Total {
		t.Errorf("owners and unattributed add up to %d, total %d", sum, f.Total)
	}
}

// TestTheCPUProfilerIsOneAtATime pins ProfilerBusy against a profiler anyone
// else started, WindowInvalid, and a cancellation that frees the profiler.
func TestTheCPUProfilerIsOneAtATime(t *testing.T) {
	if err := pprof.StartCPUProfile(io.Discard); err != nil {
		t.Fatal(err)
	}
	_, err := profiling.CaptureCPU(t.Context(), time.Second)
	pprof.StopCPUProfile()
	if !errs.HasCode(err, profiling.CodeProfilerBusy) || errs.HTTPStatusOf(err) != 409 {
		t.Errorf("a capture beside another profiler = %v", err)
	}
	for _, window := range []time.Duration{0, -time.Second, profiling.MaxCPUWindow + time.Nanosecond} {
		if _, err := profiling.CaptureCPU(t.Context(), window); !errs.HasCode(err, profiling.CodeWindowInvalid) {
			t.Errorf("window %v = %v", window, err)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	p, err := profiling.CaptureCPU(ctx, time.Minute)
	if p != nil || !errs.HasCode(err, profiling.CodeCaptureCanceled) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a canceled capture = %v, %v", p, err)
	}
	if _, err := profiling.CaptureCPU(t.Context(), 20*time.Millisecond); err != nil {
		t.Errorf("the profiler was not freed by the cancellation: %v", err)
	}
}

// hoarded keeps what hoard allocates reachable.
var hoarded [][]byte

// hoard allocates sixteen megabytes and keeps them.
//
//go:noinline
func hoard() {
	for range 128 {
		hoarded = append(hoarded, make([]byte, 128<<10))
	}
}

// TestAHeapProfileFindsTheBytesByTheirStack pins the heap capture and the
// attribution by stack. The heap profile is sampled — about one allocation
// per 512 KiB, scaled back up — so the sixteen megabytes hoard holds may read
// as ten or twenty: the test asks where they are, not how many exactly.
func TestAHeapProfileFindsTheBytesByTheirStack(t *testing.T) {
	hoard()
	defer func() { hoarded = nil }()
	p, err := profiling.CaptureHeap()
	if err != nil {
		t.Fatal(err)
	}
	byStack := func(s profiling.SampleValue) string {
		if slices.ContainsFunc(s.Stack, func(f profiling.FrameValue) bool { return strings.HasSuffix(f.Function, ".hoard") }) {
			return "hoard"
		}
		return ""
	}
	f, err := profiling.Fold(p, profiling.FoldConfig{Attribute: byStack})
	if err != nil {
		t.Fatal(err)
	}
	if f.SampleType.Type != "inuse_space" || f.SampleType.Unit != "bytes" {
		t.Fatalf("the heap's default sample type: %+v", f.SampleType)
	}
	var held int64
	for _, o := range f.Owners {
		if o.Owner == "hoard" {
			held = o.Value
		}
	}
	if held < 4<<20 || held > f.Total {
		t.Fatalf("hoard holds %d bytes of %d: %+v", held, f.Total, f.Owners)
	}
	runtime.KeepAlive(hoarded)
}
