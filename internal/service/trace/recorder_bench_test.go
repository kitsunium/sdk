package trace_test

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	coremetrics "github.com/kitsunium/sdk/internal/core/metrics"
	coretrace "github.com/kitsunium/sdk/internal/core/trace"
	svctrace "github.com/kitsunium/sdk/internal/service/trace"
)

// benchDrainEvery is how many records a goroutine makes before draining.
//
// It exists so the APPEND arm measures an append and not a slice growing without
// bound: a real deployment drains on a collection interval, and a benchmark that
// never drained would spend its time in memmove and report the lock as free.
// The drain rate is per RECORD and not per goroutine — every record advances
// exactly one goroutine's counter — so the -cpu arms stay comparable.
const benchDrainEvery int = 1024

// benchAppendMaxSpans is the bound the APPEND arm runs under.
//
// It is far above benchDrainEvery × 8 so the append arm never reaches the drop
// branch at any -cpu value; an arm that started dropping partway would be
// measuring two different functions and averaging them.
const benchAppendMaxSpans int = 1 << 20

// benchCapacity is the bound the DROP arm runs under, and it is the shipped
// default rather than a small number chosen to make the fill quick: the drop
// branch is what a Recorder does at load between two collections, so the row is
// the documented behaviour and not a corner.
const benchCapacity int = svctrace.DefaultMaxSpans

// Typed sinks — never `var sink any`, which would box every value stored and
// charge the allocation to the code under test.
var (
	lenSink     int
	droppedSink uint64
	// parallelLenSink is what a RunParallel body writes: every worker stores
	// its last reading when it finishes, concurrently, so a plain int there is
	// a data race the detector reports under -race.
	parallelLenSink atomic.Int64
)

// benchSpanValue is the span every arm records: five attributes and a status,
// which is what the server middleware produces. Built once, outside every timed
// loop, so no arm is charged for building it.
var benchSpanValue = coretrace.SpanValue{
	Context: coretrace.SpanContextValue{
		TraceID: coretrace.TraceID{
			0x4b, 0xf9, 0x2f, 0x35, 0x77, 0xb3, 0x4d, 0xa6,
			0xa3, 0xce, 0x92, 0x9d, 0x0e, 0x0e, 0x47, 0x36,
		},
		SpanID: coretrace.SpanID{0x00, 0xf0, 0x67, 0xaa, 0x0b, 0xa9, 0x02, 0xb7},
		Flags:  coretrace.TraceFlags(0).WithSampled(true),
	},
	Name:      "GET",
	Kind:      coretrace.SpanKindServer,
	StartTime: time.Unix(1_700_000_000, 0),
	EndTime:   time.Unix(1_700_000_000, 1_000_000),
	Attrs: coremetrics.SortAttrs([]coremetrics.AttrValue{
		coremetrics.String("http.request.method", "GET"),
		coremetrics.String("url.path", "/v1/orders/42"),
		coremetrics.String("url.scheme", "https"),
		coremetrics.String("server.address", "api.example.com"),
		coremetrics.Int64("http.response.status_code", 200),
	}),
}

// rwmutexRecorder is the CONTROL, and it is the code this package SHIPPED until
// BENCH.md §2 measured it: Recorder's record/Collect/Len with sync.RWMutex where
// the field now holds a sync.Mutex, and every other line unchanged.
//
// It stays in the tree rather than being deleted with the change, because the
// price of a rejected design is only checkable while both arms can be run in one
// process, on one box, in one afternoon. A lock comparison assembled from two
// git revisions measured on a machine with a moving load is a comparison of the
// machine.
type rwmutexRecorder struct {
	// maxSpans is the resolved bound, exactly as the real Recorder's is.
	maxSpans int

	// mu is the whole point of this type: the shipped Recorder now declares a
	// sync.Mutex here.
	mu sync.RWMutex
	// spans holds the finished spans awaiting collection, in end order.
	spans []coretrace.SpanValue
	// dropped counts spans refused since construction.
	dropped uint64
}

// newRWMutexRecorder mirrors NewRecorder's one clamp.
func newRWMutexRecorder(maxSpans int) *rwmutexRecorder {
	//: a non-positive bound is not "unbounded" (ADR 0031).
	if maxSpans <= 0 {
		//: the resolved bound.
		maxSpans = svctrace.DefaultMaxSpans
	}
	//: no resource or scope: neither is touched on the paths measured here.
	return &rwmutexRecorder{maxSpans: maxSpans}
}

// record mirrors Recorder.record line for line.
func (r *rwmutexRecorder) record(span coretrace.SpanValue) {
	//: the buffer is shared by every goroutine that ends a span.
	r.mu.Lock()
	defer r.mu.Unlock()
	//: past the bound, the newest span is refused and counted.
	if len(r.spans) >= r.maxSpans {
		//: visible overflow, never silent loss.
		r.dropped++
		//: nothing is stored.
		return
	}
	//: kept, in the order it ended.
	r.spans = append(r.spans, span)
}

// collect mirrors Recorder.Collect's drain, minus the payload envelope neither
// arm's timed loop reads.
func (r *rwmutexRecorder) collect() {
	//: swap the buffer out under the lock.
	r.mu.Lock()
	//: a fresh nil rather than a truncated reuse, exactly as Collect does.
	r.spans = nil
	r.mu.Unlock()
}

// length mirrors what Recorder.Len used to be — the SHARED read that was the
// whole argument for the read-write lock.
func (r *rwmutexRecorder) length() int {
	//: the shared side, which the shipped Recorder no longer has.
	r.mu.RLock()
	defer r.mu.RUnlock()
	//: the pending count.
	return len(r.spans)
}

// filledRecorder returns a Recorder already holding maxSpans spans, so every
// record in the timed loop takes the drop branch.
func filledRecorder(b *testing.B, maxSpans int) *svctrace.Recorder {
	b.Helper()
	recorder := svctrace.NewRecorder(svctrace.RecorderConfig{MaxSpans: maxSpans})
	sink := recorder.Sink()
	for range maxSpans {
		sink(benchSpanValue)
	}
	if recorder.Len() != maxSpans {
		b.Fatalf("the recorder holds %d spans, want %d — the drop arm would measure appends", recorder.Len(), maxSpans)
	}
	return recorder
}

// filledRWMutexRecorder is filledRecorder for the control.
func filledRWMutexRecorder(b *testing.B, maxSpans int) *rwmutexRecorder {
	b.Helper()
	recorder := newRWMutexRecorder(maxSpans)
	for range maxSpans {
		recorder.record(benchSpanValue)
	}
	if recorder.length() != maxSpans {
		b.Fatalf("the control holds %d spans, want %d", recorder.length(), maxSpans)
	}
	return recorder
}

// pollUntil spins a reader on poll until the returned stop function is called,
// and does not return until that reader has actually exited.
//
// Lifecycle: pollUntil starts exactly ONE goroutine and owns it. The goroutine
// runs until stop() sets the atomic flag, then closes `done` and terminates;
// stop() blocks on `done`, so it cannot return while the reader is still
// touching the recorder. There is no path on which the goroutine outlives its
// caller, and no benchmark arm can leak a poller into the next one — which is
// the whole reason stop() waits instead of merely signalling.
//
// It exists because the field comment the shipped RWMutex was justified by named
// a SPECIFIC scenario — "pure reads an operator may poll while spans are still
// arriving" — and neither a read-only arm nor a write-only arm measures it. It
// is the mixed shape, it is the one the lock was chosen for, and it is where the
// lock lost by an order of magnitude.
//
// The poller here has a 100 % duty cycle, which no operator has. That makes this
// the WORST case rather than the typical one, and the at-capacity pair is the
// row to quote for a Recorder nobody is polling.
func pollUntil(poll func() int) (stop func()) {
	//: the reader watches this and the caller waits on done.
	var running atomic.Bool
	running.Store(true)
	done := make(chan struct{})
	go func() {
		//: signal the caller once this goroutine is really finished.
		defer close(done)
		//: goroutine-local, published once so the poll cannot be elided.
		last := 0
		for running.Load() {
			last = poll()
		}
		lenSink = last
	}()
	//: the caller stops the reader and waits for it, so no poller from one
	//: arm survives into the next.
	return func() {
		//: ask it to stop.
		running.Store(false)
		//: and do not return until it has.
		<-done
	}
}

// BenchmarkRecorderRecordAtCapacity prices the LOCK and almost nothing else: the
// recorder is already full, so the critical section is one length comparison and
// one increment on both arms. Whatever separates this row from its control is
// the lock, because there is nothing else left in it.
//
// Run it with `-cpu=1,2,4,8`: record is called from every request goroutine,
// which is the contention this row exists to expose.
func BenchmarkRecorderRecordAtCapacity(b *testing.B) {
	sink := filledRecorder(b, benchCapacity).Sink()
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			sink(benchSpanValue)
		}
	})
}

// BenchmarkRecorderRecordAtCapacity_RWMutexControl is the rejected design on the
// same row.
func BenchmarkRecorderRecordAtCapacity_RWMutexControl(b *testing.B) {
	recorder := filledRWMutexRecorder(b, benchCapacity)
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			recorder.record(benchSpanValue)
		}
	})
	b.StopTimer()
	droppedSink = recorder.dropped
}

// BenchmarkRecorderRecordAppend is the same call with the span actually STORED,
// drained every benchDrainEvery records the way a collection interval drains it.
// It is the row to quote for the real cost of ending a span; the at-capacity
// pair is the row that isolates the lock.
//
// It allocates, so it must be run in a process of its own — see BENCH.md
// §Discarded rows for the run that proved it.
func BenchmarkRecorderRecordAppend(b *testing.B) {
	recorder := svctrace.NewRecorder(svctrace.RecorderConfig{MaxSpans: benchAppendMaxSpans})
	sink := recorder.Sink()
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		//: a goroutine-local counter, so the drain rate is per record rather
		//: than per goroutine and the -cpu arms stay comparable.
		since := 0
		for pb.Next() {
			sink(benchSpanValue)
			since++
			//: the collection interval, modelled.
			if since == benchDrainEvery {
				//: drain and start the next window.
				recorder.Collect()
				since = 0
			}
		}
	})
	b.StopTimer()
	droppedSink = recorder.Dropped()
}

// BenchmarkRecorderRecordAppend_RWMutexControl is the rejected design on the
// same row.
func BenchmarkRecorderRecordAppend_RWMutexControl(b *testing.B) {
	recorder := newRWMutexRecorder(benchAppendMaxSpans)
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		//: see the shipped twin.
		since := 0
		for pb.Next() {
			recorder.record(benchSpanValue)
			since++
			//: the collection interval, modelled.
			if since == benchDrainEvery {
				//: drain and start the next window.
				recorder.collect()
				since = 0
			}
		}
	})
	b.StopTimer()
	droppedSink = recorder.dropped
}

// BenchmarkRecorderRecordUnderPoller is the shape the RWMutex was chosen for: N
// goroutines ending spans while ONE operator polls Len. It is the row that
// decided the change, and the row where the rejected control loses by 9x.
func BenchmarkRecorderRecordUnderPoller(b *testing.B) {
	recorder := filledRecorder(b, benchCapacity)
	sink := recorder.Sink()
	stop := pollUntil(recorder.Len)
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			sink(benchSpanValue)
		}
	})
	b.StopTimer()
	stop()
}

// BenchmarkRecorderRecordUnderPoller_RWMutexControl is the rejected design on
// the same row.
func BenchmarkRecorderRecordUnderPoller_RWMutexControl(b *testing.B) {
	recorder := filledRWMutexRecorder(b, benchCapacity)
	stop := pollUntil(recorder.length)
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			recorder.record(benchSpanValue)
		}
	})
	b.StopTimer()
	stop()
}

// BenchmarkRecorderLen is the READ path, and it is the row that says what the
// change COST rather than what it bought: with four or more goroutines doing
// nothing but reading, the rejected control wins.
//
// It is deliberately the most favourable case the read-write lock can be given —
// N readers, no writer at all — because the honest way to publish a trade is to
// give the losing side its best shot. What makes the trade acceptable is not
// that this row is close, it is that Collect DRAINS, so this type has exactly
// one reader and the scenario is one its own contract excludes.
func BenchmarkRecorderLen(b *testing.B) {
	recorder := filledRecorder(b, benchCapacity)
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		//: goroutine-local, published once after the loop.
		last := 0
		for pb.Next() {
			last = recorder.Len()
		}
		parallelLenSink.Store(int64(last))
	})
}

// BenchmarkRecorderLen_RWMutexControl is the rejected design on the same row,
// and the only row it wins.
func BenchmarkRecorderLen_RWMutexControl(b *testing.B) {
	recorder := filledRWMutexRecorder(b, benchCapacity)
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		//: see the shipped twin.
		last := 0
		for pb.Next() {
			last = recorder.length()
		}
		parallelLenSink.Store(int64(last))
	})
}
