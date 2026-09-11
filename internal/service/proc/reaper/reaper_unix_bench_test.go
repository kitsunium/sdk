//go:build unix

// Package reaper — what a PID1 supervisor pays. Every interesting call here ends
// in wait4(2) or prctl(2), so the measurement's job is to separate the kernel's
// price from the package's own bookkeeping and to price the one thing a caller
// controls: how often ReapOnce is called when there is nothing to reap.
package reaper_test

import (
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/service/proc/reaper"
)

// Sinks defeat dead-code elimination on the value-returning entry points.
var (
	errSink    error
	intSink    int
	boolSink   bool
	reaperSink coreproc.Reaper
)

// BenchmarkReapOnce_NoChildren is the floor a polling supervisor pays: one
// non-blocking wait4(-1, WNOHANG) that returns ECHILD because this process has
// no children. It is the honest cost of "check whether anything died", and the
// number a caller needs before putting ReapOnce on a timer.
func BenchmarkReapOnce_NoChildren(b *testing.B) {
	r := reaper.New()
	if _, err := r.ReapOnce(); err != nil {
		b.Skipf("ReapOnce is not usable here: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		n, err := r.ReapOnce()
		intSink, errSink = n, err
	}
}

// BenchmarkReapOnce_WithObserver adds the WithOnReap callback, which fires once
// per sweep. It exists to show that the observer is not a hidden cost.
func BenchmarkReapOnce_WithObserver(b *testing.B) {
	seen := 0
	r := reaper.New(reaper.WithOnReap(func(n int) { seen += n }))
	if _, err := r.ReapOnce(); err != nil {
		b.Skipf("ReapOnce is not usable here: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		n, err := r.ReapOnce()
		intSink, errSink = n, err
	}
	intSink = seen
}

// BenchmarkNew prices construction. A reaper creates no channels until Start, so
// this should be one small allocation and nothing else.
func BenchmarkNew(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		reaperSink = reaper.New()
	}
}

// BenchmarkNew_WithOption prices the Option fold, which is the only work New
// does beyond the allocation.
func BenchmarkNew_WithOption(b *testing.B) {
	opt := reaper.WithOnReap(func(int) {})
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		reaperSink = reaper.New(opt)
	}
}

// BenchmarkIsPID1 is a getpid(2) — cheap, but a syscall nonetheless, and this
// row is here so nobody puts it in a loop believing it is a constant.
func BenchmarkIsPID1(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		boolSink = reaper.IsPID1()
	}
}

// BenchmarkStartStop_Cycle is the full lifecycle: one goroutine launched, one
// os/signal SIGCHLD subscription installed, two drains (the initial one and the
// final one), the subscription detached and the goroutine joined. A supervisor
// pays this once; a test suite that constructs a reaper per case pays it per
// case, which is why it is measured.
func BenchmarkStartStop_Cycle(b *testing.B) {
	r := reaper.New()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		r.Start()
		r.Stop()
	}
}

// BenchmarkSetChildSubreaper is prctl(PR_SET_CHILD_SUBREAPER, 1), re-armed on a
// process that is already armed after the first iteration — the kernel accepts
// it idempotently, so the loop measures the call and not a state change. It
// skips where the platform has no prctl (every Unix that is not Linux).
func BenchmarkSetChildSubreaper(b *testing.B) {
	if err := reaper.SetChildSubreaper(); err != nil {
		if errs.HasCode(err, coreproc.CodeUnsupportedPlatform) {
			b.Skip("PR_SET_CHILD_SUBREAPER does not exist on this platform")
		}
		b.Skipf("PR_SET_CHILD_SUBREAPER is refused here: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		errSink = reaper.SetChildSubreaper()
	}
}
