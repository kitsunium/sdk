package telemetry_test

import (
	"testing"

	"github.com/kitsunium/sdk/framework/telemetry"
)

// BenchmarkEmit is a producer's cost: the ring's slot claimed and the event
// copied — the ring drops once full, which is the steady state of a process
// nobody attached to and the cheapest path.
func BenchmarkEmit(b *testing.B) {
	ex, err := telemetry.NewExporter(&telemetry.ExporterConfig{Path: "/tmp/never-opened.sock", Buffer: telemetry.MaxBuffer})
	if err != nil {
		b.Fatal(err)
	}
	ev := telemetry.Event{Kind: telemetry.KindSpan, Op: telemetry.OpRequest, Node: 1, Duration: 42}
	b.ReportAllocs()
	for b.Loop() {
		ex.Emit(&ev)
	}
}

// BenchmarkEmitNop is what a product pays with no exporter configured.
func BenchmarkEmitNop(b *testing.B) {
	ev := telemetry.Event{Kind: telemetry.KindSpan}
	b.ReportAllocs()
	for b.Loop() {
		telemetry.Nop.Emit(&ev)
	}
}

// BenchmarkEmitParallel is eight producers on one ring.
func BenchmarkEmitParallel(b *testing.B) {
	ex, err := telemetry.NewExporter(&telemetry.ExporterConfig{Path: "/tmp/never-opened.sock", Buffer: telemetry.MaxBuffer})
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		ev := telemetry.Event{Kind: telemetry.KindSpan, Op: telemetry.OpRequest, Node: 1}
		for pb.Next() {
			ex.Emit(&ev)
		}
	})
}
