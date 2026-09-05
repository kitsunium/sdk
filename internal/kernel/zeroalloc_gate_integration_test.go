//go:build !race

// Package kernel_zeroalloc_test is the SDK-wide zero-allocation invariant gate.
// It lives at the kernel module root (importing the gated packages, embedding
// none) and asserts that every documented zero-alloc hot path actually reports
// AllocsPerOp == 0. A regression — someone adds an allocation to a hot path —
// fails this test and the build.
//
// It carries //go:build !race because testing.Benchmark / AllocsPerOp report a
// spurious +1 alloc under the race detector (the race runtime boxes values), so
// the gate runs in the race-OFF lane only — `bazel test --config=alloc` and
// `make test-alloc`, exactly like the codec allocation-budget gate. The full
// per-package benchmarks (and their BENCH.md numbers) live next to each package;
// these probes are deliberately minimal — just enough to read AllocsPerOp.
package kernel_zeroalloc_test

import (
	"sync"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/buffer"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/kernel/ring"
)

// gateMu serialises the probes.
//
// Two reasons, and the second is the one that bites. Allocation accounting is
// PROCESS-wide, so two testing.Benchmark calls running at once each measure the
// other's allocations and AllocsPerOp stops meaning anything — for a gate whose
// whole output is that number, an unreliable reading is worse than no gate. And
// the probes write the shared sinks below with no synchronisation, which is a
// genuine data race that nothing would ever report: this file carries
// //go:build !race precisely so it never runs under the detector.
//
// Holding it for the measurement keeps t.Parallel() truthful — each subtest
// still yields to the scheduler, and still runs alongside every test that does
// not measure allocations.
var gateMu sync.Mutex

// Sinks defeat dead-store elimination so the probed calls are not optimised away.
var (
	sinkTime     time.Time
	sinkDuration time.Duration
	sinkCode     errs.Code
	sinkString   string
	sinkBool     bool
	sinkBuf      *[]byte
	sinkErr      error
	sinkInt      int
)

// gateSentinel is a representative *Error for the accessor / HasCode probes.
var gateSentinel = errs.Define(
	errs.Pack(2, 3, 4, 5),
	"ZEROALLOC_GATE",
	"zero-alloc gate sentinel",
	"zero-alloc gate private detail",
)

// TestZeroAllocInvariant runs each gated probe programmatically via
// testing.Benchmark and asserts AllocsPerOp == 0.
//
// The table below lists every kernel function whose steady state MUST allocate
// zero. A regression to AllocsPerOp > 0 fails the build. Extend it when a new
// hot path makes a documented zero-alloc claim.
func TestZeroAllocInvariant(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		fn   func(b *testing.B)
	}
	tests := []tc{
		{"ring.TryWrite_Happy", benchRingTryWrite},
		{"ring.TryRead_Happy", benchRingTryRead},
		{"ring.TryWrite_Full", benchRingTryWriteFull},
		{"ring.TryRead_Empty", benchRingTryReadEmpty},
		{"buffer.GetPut_Steadystate", benchBufferGetPut},
		{"clock.System.Now", benchClockNow},
		{"clock.System.Since", benchClockSince},
		{"errs.Code.Pack", benchErrsPack},
		{"errs.Error.Code", benchErrsErrorCode},
		{"errs.Error.Reason", benchErrsErrorReason},
		{"errs.Error.Public", benchErrsErrorPublic},
		{"errs.Error.Private", benchErrsErrorPrivate},
		{"errs.HasCode", benchErrsHasCode},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: one probe at a time: the counters are process-wide and the sinks are
		//: shared, so a concurrent probe corrupts both.
		gateMu.Lock()
		//: programmatic bench — adaptive N amortises GC events to ~0/op.
		result := testing.Benchmark(c.fn)
		gateMu.Unlock()
		if got := result.AllocsPerOp(); got != 0 {
			t.Errorf("%s: AllocsPerOp = %d, want 0 (steady-state zero-alloc invariant)", c.name, got)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// benchRingTryWrite probes the non-saturated TryWrite fast path. Rather than a
// consumer goroutine (which would race on the sinks under this race-off build),
// each iteration writes then immediately drains, so the ring never saturates and
// TryWrite always lands on the non-Full path. Results go to sinks (never `_`) so
// the calls are neither elided nor flagged as discarded errors.
func benchRingTryWrite(b *testing.B) {
	q, err := ring.New[int](1024)
	if err != nil {
		b.Fatalf("New: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		sinkErr = q.TryWrite(1)
		sinkInt, sinkErr = q.TryRead()
	}
}

// benchRingTryRead probes the non-empty TryRead fast path. The ring is seeded
// each iteration (write) so TryRead always lands on the non-Empty path; the
// goroutine-free shape keeps the probe race-free under this race-off build.
func benchRingTryRead(b *testing.B) {
	q, err := ring.New[int](1024)
	if err != nil {
		b.Fatalf("New: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		sinkErr = q.TryWrite(1)
		sinkInt, sinkErr = q.TryRead()
	}
}

// benchRingTryWriteFull probes the saturated TryWrite path: every call returns
// the pre-allocated Full sentinel with no construction.
func benchRingTryWriteFull(b *testing.B) {
	q, err := ring.New[int](8)
	if err != nil {
		b.Fatalf("New: %v", err)
	}
	//: fill to capacity so every benched TryWrite returns Full.
	for q.TryWrite(0) == nil {
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		sinkErr = q.TryWrite(1)
	}
}

// benchRingTryReadEmpty probes the empty TryRead path: every call returns the
// pre-allocated Empty sentinel plus the zero value.
func benchRingTryReadEmpty(b *testing.B) {
	q, err := ring.New[int](8)
	if err != nil {
		b.Fatalf("New: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		sinkInt, sinkErr = q.TryRead()
	}
}

// benchBufferGetPut probes the warm-pool Get/Put round trip. The pool is primed
// before the timer so the first benched Get already recycles.
func benchBufferGetPut(b *testing.B) {
	//: warm the pool so no benched iteration runs the factory.
	buffer.Put(buffer.Get())
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		buf := buffer.Get()
		sinkBuf = buf
		buffer.Put(buf)
	}
}

// benchClockNow probes the per-record wall-clock read.
func benchClockNow(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		sinkTime = clock.System.Now()
	}
}

// benchClockSince probes the elapsed-duration read against a fixed start.
func benchClockSince(b *testing.B) {
	start := clock.System.Now()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		sinkDuration = clock.System.Since(start)
	}
}

// benchErrsPack probes the dotted-quad Code packing (pure bit ops).
func benchErrsPack(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		sinkCode = errs.Pack(2, 3, 4, 5)
	}
}

// benchErrsErrorCode probes the *Error Code accessor (field read).
func benchErrsErrorCode(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		sinkCode = gateSentinel.Code()
	}
}

// benchErrsErrorReason probes the *Error Reason accessor.
func benchErrsErrorReason(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		sinkString = gateSentinel.Reason()
	}
}

// benchErrsErrorPublic probes the *Error Public accessor.
func benchErrsErrorPublic(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		sinkString = gateSentinel.Public()
	}
}

// benchErrsErrorPrivate probes the *Error Private accessor.
func benchErrsErrorPrivate(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		sinkString = gateSentinel.Private()
	}
}

// benchErrsHasCode probes the code-matching walk over a sentinel.
func benchErrsHasCode(b *testing.B) {
	code := gateSentinel.Code()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		sinkBool = errs.HasCode(gateSentinel, code)
	}
}
