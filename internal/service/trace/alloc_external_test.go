//go:build !race

package trace_test

import (
	"net/http"
	"net/http/httptest"
	"runtime"
	"runtime/debug"
	"testing"

	coretrace "github.com/kitsunium/sdk/internal/core/trace"
	svctrace "github.com/kitsunium/sdk/internal/service/trace"
)

// allocRuns is how many requests each budget is measured over. It is large
// enough that an amortised allocation — one landing on a growth step rather than
// on every call — has happened several times by the end, which is the whole
// reason a TOTAL is counted rather than a per-call average.
const allocRuns int = 500

// allocRuntimeWarmup is how many requests warmRuntimeCaches spends on a
// THROWAWAY handler before any window is measured. It is not about this package.
//
// The rationale is recorded in full in internal/service/writer/file's copy: the
// runtime builds its per-call-site interface-switch caches LAZILY, gated behind
// `cheaprand()&1023 != 0`, so about one miss in 1024 pays for the build and
// buildInterfaceSwitchCache allocates. The result is a handful of allocations
// landing at an unpredictable point roughly a thousand calls into the process,
// attributable to no line in this package.
//
// The handler is THROWAWAY and that is load-bearing rather than tidy: the cache
// is the runtime's, per call site and process-global, so any handler can pay for
// it — while what this file polices is per request through the handler under
// test. Warming through the one under test would hide an accumulating regression
// inside its own warmup.
const allocRuntimeWarmup int = 30000

// The measured per-request allocation budgets, from BENCH.md §1. They are
// CEILINGS rather than equalities: a change that removes an allocation should
// not fail a test, it should update this constant downward and say so.
//
// They are absolute rather than a delta because the control this middleware is
// measured against — the same handler with no middleware at all — is 0
// allocations, so a delta and an absolute are the same number here. That is not
// true of the writer guards this file borrows its method from, and it is why
// theirs are deltas and these are not.
const (
	// allocBudgetSampled is a request that starts a RECORDING root span: no
	// inbound traceparent, AlwaysSample. Twelve, itemised in BENCH.md §1.3.
	allocBudgetSampled int = 12
	// allocBudgetUnsampled is the same request against NeverSample. It is the
	// number that governs a service running a low sampling ratio, because it
	// is what the overwhelming majority of its requests pay.
	allocBudgetUnsampled int = 10
)

// mallocsOver reports the TOTAL number of heap allocations f performs across
// runs calls, rather than the per-call average.
//
// testing.AllocsPerRun is deliberately not used, and the reason is recorded in
// full in internal/service/writer/levelgate's copy of this helper: its last line
// divides as INTEGERS, so any defect allocating less than once per call reports
// exactly 0.0.
func mallocsOver(runs int, f func()) uint64 {
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(1))
	//: collection is held off for the window. A collection is not free of
	//: allocations from the measured goroutine's point of view; it drains
	//: caches and forces the next calls to miss. The argument is evaluated now
	//: and the previous rate restored on return.
	defer debug.SetGCPercent(debug.SetGCPercent(-1))
	//: warm up so first-call initialisation is not counted as steady state.
	f()
	var before, after runtime.MemStats
	//: no runtime.GC() here, and that is deliberate: an explicit collection
	//: returns before its sweep is finished, so the residual work allocates
	//: INSIDE the window below. testing.AllocsPerRun does not call it either.
	runtime.ReadMemStats(&before)
	for range runs {
		f()
	}
	runtime.ReadMemStats(&after)
	//: Mallocs is cumulative and monotonic, so the difference is the total.
	return after.Mallocs - before.Mallocs
}

// warmRuntimeCaches pays for the runtime's lazily-built interface-switch caches
// on a handler no budget is measured through. See allocRuntimeWarmup.
func warmRuntimeCaches() {
	tracer := svctrace.NewTracer(svctrace.TracerConfig{
		Sampler: svctrace.AlwaysSample,
		Sink:    func(coretrace.SpanValue) {},
	})
	handler := svctrace.ServerMiddleware(tracer)(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) },
	))
	writer := &allocWriter{header: make(http.Header, 4)}
	request := httptest.NewRequest(http.MethodGet, "/warm", nil)
	for range allocRuntimeWarmup {
		handler.ServeHTTP(writer, request)
	}
}

// allocWriter is a http.ResponseWriter that allocates nothing, so every
// allocation the window counts belongs to the middleware.
type allocWriter struct {
	// header is built once and handed out by reference.
	header http.Header
}

// Header returns the one map.
func (w *allocWriter) Header() http.Header {
	//: never a fresh map.
	return w.header
}

// Write discards.
func (w *allocWriter) Write(data []byte) (n int, err error) {
	//: nothing retained.
	return len(data), nil
}

// WriteHeader discards. The middleware's own wrapper is what records the code;
// recording it a second time here would allocate nothing but would put a store
// in the window this file measures.
func (w *allocWriter) WriteHeader(int) {}

// tracedAllocHandler builds the middleware under a sink that discards, so the
// budget is the middleware's and not an exporter's.
func tracedAllocHandler(sampler coretrace.Sampler) http.Handler {
	tracer := svctrace.NewTracer(svctrace.TracerConfig{Sampler: sampler, Sink: func(coretrace.SpanValue) {}})
	return svctrace.ServerMiddleware(tracer)(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) },
	))
}

// TestServerMiddlewareStaysWithinItsPerRequestAllocationBudget is the guard
// behind the only number in BENCH.md that decides anything.
//
// A traced request's LATENCY surcharge is ~1.3 % of a full SDK HTTP request and
// nobody would reject a middleware over it. Its ALLOCATION surcharge is +19 %
// against a 63-allocation baseline, and at high RPS that is a GC-pressure story
// — so the allocation count, not the nanoseconds, is what decides whether
// tracing belongs in a default middleware stack. A regression here is therefore
// a regression in the argument, not just in a number.
//
// It counts a TOTAL over allocRuns requests rather than calling
// testing.AllocsPerRun, whose integer division reports 0.0 for anything
// allocating less than once per call.
//
// MUTATION: naming the span `r.Method+" "+r.URL.EscapedPath()` instead of
// r.Method — which is not an invented defect but the exact mistake this
// middleware's own doc comment spends ten lines warning against, and the one a
// reviewer who has not read it would ask FOR. Observed: "a sampled request
// performed 6500 allocations over 500 requests (13.00 per request), budget 12"
// and "an unsampled request performed 5500 allocations over 500 requests (11.00
// per request), budget 10" — the concatenation is one allocation and both arms
// name it. Restored; http_server.go is byte-identical to its committed form and
// the test passes again.
//
// A FIRST mutation was tried and DISCARDED because it did not model a defect:
// adding a fifth attribute to the literal at http_server.go:120 left the count
// at 12, since a wider slice literal is still one allocation and so is the
// clone SortAttrs makes of it. It moved B/op and not allocs/op. A budget
// expressed in allocations is deliberately blind to that, and this comment is
// where a reader finds out.
func TestServerMiddlewareStaysWithinItsPerRequestAllocationBudget(t *testing.T) {
	warmRuntimeCaches()
	cases := []struct {
		name    string
		sampler coretrace.Sampler
		budget  int
	}{
		{name: "a sampled request", sampler: svctrace.AlwaysSample, budget: allocBudgetSampled},
		{name: "an unsampled request", sampler: svctrace.NeverSample, budget: allocBudgetUnsampled},
	}
	for _, testCase := range cases {
		runAllocBudgetCase(t, testCase.name, testCase.sampler, testCase.budget)
	}
}

// runAllocBudgetCase measures one arm of the budget table.
func runAllocBudgetCase(t *testing.T, name string, sampler coretrace.Sampler, budget int) {
	t.Helper()
	handler := tracedAllocHandler(sampler)
	writer := &allocWriter{header: make(http.Header, 4)}
	request := httptest.NewRequest(http.MethodGet, "/v1/orders/42?page=2", nil)
	total := mallocsOver(allocRuns, func() {
		handler.ServeHTTP(writer, request)
	})
	//: the ceiling is per request, so the total is compared against runs × budget.
	if total > uint64(allocRuns*budget) {
		t.Errorf("%s performed %d allocations over %d requests (%.2f per request), budget %d",
			name, total, allocRuns, float64(total)/float64(allocRuns), budget)
	}
}

// TestRecordingAtCapacityAllocatesNothing pins the property overflow accounting
// exists for: past MaxSpans a span is refused, and refusing it must cost less
// than keeping it — otherwise a recorder nobody is draining, which is the exact
// situation the bound exists to survive, becomes the most expensive state the
// type has.
//
// MUTATION: making the overflow visible the obvious way — a
// `droppedNames []string` field and `r.droppedNames = append(r.droppedNames,
// span.Name)` beside the `r.dropped++`, which is what anyone would write when
// asked "which spans are we losing?". Observed: "recording 500 spans into a full
// recorder performed 9 allocations, want 0".
//
// NINE, not 500, and that number is the argument for this whole helper: append
// grows geometrically, so 500 appends onto a nil slice cost about ten
// allocations and not five hundred. testing.AllocsPerRun would have divided 9 by
// 500 as INTEGERS and reported **0.0 allocations per run for a mutation that
// really does allocate** — the guard would have passed, the drop path would have
// grown an unbounded slice, and the first symptom would have been the OOM the
// MaxSpans bound exists to prevent. Restored; recorder.go is byte-identical to
// its committed form and the test passes again.
func TestRecordingAtCapacityAllocatesNothing(t *testing.T) {
	const capacity int = 64
	recorder := svctrace.NewRecorder(svctrace.RecorderConfig{MaxSpans: capacity})
	sink := recorder.Sink()
	value := coretrace.SpanValue{Name: "GET", Kind: coretrace.SpanKindServer}
	//: fill it, so every call in the window below takes the drop branch.
	for range capacity {
		sink(value)
	}
	if recorder.Len() != capacity {
		t.Fatalf("the recorder holds %d spans, want %d — the window would measure appends", recorder.Len(), capacity)
	}
	total := mallocsOver(allocRuns, func() {
		sink(value)
	})
	if total != 0 {
		t.Errorf("recording %d spans into a full recorder performed %d allocations, want 0", allocRuns, total)
	}
	//: and the refusals were counted, or the window measured a no-op.
	if recorder.Dropped() < uint64(allocRuns) {
		t.Errorf("Dropped = %d after %d refused spans, want at least that many", recorder.Dropped(), allocRuns)
	}
}
