package lifecycle_test

import (
	"context"
	"errors"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/lifecycle"
)

// sinks so no start or stop can be proven unused and elided.
var (
	lcSink  lifecycle.Lifecycle
	errSink error
)

// errBenchStart is the failure the unwind benchmarks provoke. Package-level so
// constructing it never lands inside a timed loop.
var errBenchStart = errors.New("bench: component failed to start")

// noopStart and noopStop are components that do nothing, so every number below
// is the ENGINE's own cost with the work it orders held at zero — which is what
// a caller needs in order to know whether the ordering machinery is a rounding
// error against their own startup, which it should be.
func noopStart(context.Context) error { return nil }
func noopStop(context.Context) error  { return nil }

// benchLifecycle builds an engine carrying n do-nothing components.
func benchLifecycle(b *testing.B, n int) lifecycle.Lifecycle {
	b.Helper()
	lc := lifecycle.New(lifecycle.Config{})
	for i := range n {
		name := "component-" + string(rune('a'+i%26)) + string(rune('a'+i/26))
		if err := lc.Add(lifecycle.Component{Name: name, Start: noopStart, Stop: noopStop}); err != nil {
			b.Fatalf("Add: %v", err)
		}
	}
	return lc
}

// BenchmarkStartStop_1 through _32 are the whole lifecycle: bring up in
// registration order, tear down in reverse. It runs ONCE per process, so these
// numbers matter only as a proportion of a real startup — and the point of
// measuring is to show that proportion is negligible.
func BenchmarkStartStop_1(b *testing.B)  { benchStartStop(b, 1) }
func BenchmarkStartStop_8(b *testing.B)  { benchStartStop(b, 8) }
func BenchmarkStartStop_32(b *testing.B) { benchStartStop(b, 32) }

func benchStartStop(b *testing.B, n int) {
	b.Helper()
	ctx := b.Context()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		b.StopTimer()
		lc := benchLifecycle(b, n)
		b.StartTimer()
		if err := lc.Start(ctx); err != nil {
			b.Fatalf("Start: %v", err)
		}
		if err := lc.Stop(ctx); err != nil {
			b.Fatalf("Stop: %v", err)
		}
		lcSink = lc
	}
}

// BenchmarkAdd measures registration alone, which happens once per component
// at wiring time and is the only part a caller writes in a loop.
func BenchmarkAdd(b *testing.B) {
	lc := lifecycle.New(lifecycle.Config{})
	i := 0
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		//: a fresh engine every 4096 adds keeps the name set from growing
		//: without bound; the reset is untimed.
		if i&4095 == 0 {
			b.StopTimer()
			lc = lifecycle.New(lifecycle.Config{})
			b.StartTimer()
		}
		name := "c" + string(rune('a'+i%26)) + string(rune('a'+(i/26)%26)) + string(rune('a'+(i/676)%26))
		errSink = lc.Add(lifecycle.Component{Name: name, Start: noopStart, Stop: noopStop})
		i++
	}
	lcSink = lc
}

// BenchmarkStart_PartialUnwind is the path ADR 0050 exists for: the last
// component fails, so everything already started must be stopped before Start
// returns — through the SAME code an ordinary Stop uses, over contexts detached
// from the cancellation that caused the failure. It is the expensive path and
// the one nobody exercises in production until the day it matters.
func BenchmarkStart_PartialUnwind(b *testing.B) {
	ctx := b.Context()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		b.StopTimer()
		lc := benchLifecycle(b, 31)
		if err := lc.Add(lifecycle.Component{
			Name:  "the-one-that-fails",
			Start: func(context.Context) error { return errBenchStart },
			Stop:  noopStop,
		}); err != nil {
			b.Fatalf("Add: %v", err)
		}
		b.StartTimer()
		errSink = lc.Start(ctx)
		lcSink = lc
	}
	if errSink == nil {
		b.Fatal("a failing component started cleanly")
	}
}
