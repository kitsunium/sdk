package worker_test

import (
	"sync/atomic"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/worker"
)

// sinks so the compiler cannot prove a daemon or a channel unused.
var (
	daemonSink *worker.LoopDaemon
	doneSink   <-chan struct{}
)

// BenchmarkStart_Stop is the number a caller needs before deciding whether a
// daemon is a per-request object (it is not) or a startup-time one. It covers
// the whole lifecycle: allocate two channels, spawn a goroutine, close stop,
// and JOIN — so it is bounded below by the scheduler's round trip, not by any
// code in this package.
func BenchmarkStart_Stop(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		d := worker.Start(func(stop <-chan struct{}) { <-stop })
		d.Stop()
	}
}

// BenchmarkStart_StopAlreadyExited is the same lifecycle with the loop
// returning immediately, so Stop's join usually finds `done` already closed.
// The delta against Start_Stop is how much of that number is the hand-off
// rather than the setup.
func BenchmarkStart_StopAlreadyExited(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		d := worker.Start(func(<-chan struct{}) {})
		d.Stop()
	}
}

// BenchmarkStop_Repeat prices the idempotence guarantee. After the first call
// every Stop is a sync.Once fast path plus a receive on a closed channel —
// this is the cost of the defensive `defer d.Stop()` a caller will write.
func BenchmarkStop_Repeat(b *testing.B) {
	d := worker.Start(func(stop <-chan struct{}) { <-stop })
	d.Stop()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		d.Stop()
	}
}

// BenchmarkDone is the accessor: a field read, and the benchmark exists to pin
// it as one — Done must never be tempted into building anything.
func BenchmarkDone(b *testing.B) {
	d := worker.Start(func(stop <-chan struct{}) { <-stop })
	defer d.Stop()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		doneSink = d.Done()
	}
}

// BenchmarkLoopIteration_StopCheck measures the daemon's per-iteration
// overhead as a busy loop actually pays it: one non-blocking select on the
// stop channel. This is the tax a caller's loop body carries for being
// stoppable, and it is the number that says whether the check belongs in an
// inner loop or an outer one.
func BenchmarkLoopIteration_StopCheck(b *testing.B) {
	var iterations atomic.Int64
	//: the daemon runs its own loop; the timed loop below only waits for it to
	//: reach b.N iterations, so ns/op is per DAEMON iteration.
	target := int64(b.N)
	d := worker.Start(func(stop <-chan struct{}) {
		for {
			select {
			case <-stop:
				return
			default:
			}
			if iterations.Add(1) >= target {
				return
			}
		}
	})
	b.ReportAllocs()
	<-d.Done()
	b.StopTimer()
	daemonSink = d
}

// BenchmarkStart_StopParallel checks that nothing in the lifecycle serialises
// across daemons: each goroutine owns its own channels and sync.Once, so N
// concurrent start/stop cycles should not contend at all.
func BenchmarkStart_StopParallel(b *testing.B) {
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			d := worker.Start(func(stop <-chan struct{}) { <-stop })
			d.Stop()
		}
	})
}
