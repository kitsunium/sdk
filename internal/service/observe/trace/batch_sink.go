package trace

import (
	"context"
	"sync"
	"time"

	coreotel "github.com/kitsunium/sdk/internal/core/observe/otel"
	coretrace "github.com/kitsunium/sdk/internal/core/observe/trace"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/kernel/plugin"
)

// The four defaults of a BatchSink are the OpenTelemetry trace SDK
// specification's own for the batching span processor
// (https://opentelemetry.io/docs/specs/otel/trace/sdk/#batching-processor):
// maxQueueSize 2048, scheduledDelayMillis 5000, exportTimeoutMillis 30000 and
// maxExportBatchSize 512. A zero field in BatchSinkConfig takes them, so a zero
// value is the specification's processor rather than an inert one (ADR 0031).
const (
	// DefaultBatchMaxQueueSize bounds how many finished spans wait for export.
	DefaultBatchMaxQueueSize int = 2048
	// DefaultBatchScheduledDelay is the longest a queued span waits before an export starts.
	DefaultBatchScheduledDelay time.Duration = 5 * time.Second
	// DefaultBatchExportTimeout bounds one export, on the sink's clock.
	DefaultBatchExportTimeout time.Duration = coretrace.DefaultExportTimeout
	// DefaultBatchMaxExportBatchSize bounds how many spans one export carries.
	DefaultBatchMaxExportBatchSize int = 512
)

// flushRequest is one ForceFlush handed to the exporting goroutine: reply
// receives the outcome, and is buffered so the goroutine never waits on a
// caller whose context has already ended.
type flushRequest struct {
	reply chan error
}

// BatchSink is the batching span processor of the OpenTelemetry trace SDK
// specification, as a SpanSink: a span that ends is queued, and one goroutine
// hands the queue to a SpanExporter in batches — when a batch is full, when the
// scheduled delay has passed since the last export, on ForceFlush and on
// Shutdown — so no span end ever waits on the network.
//
// # Bounded, counted, never silent
//
// The queue holds at most MaxQueueSize spans. A span that finds it full is
// DROPPED and counted, as one that ends after Shutdown is: Dropped is the number
// to alert on. A span whose export failed is counted by Failed and reported to
// OnError; it is not retried — a retry policy wraps the exporter, where it can be
// seen and tuned.
//
// # Every wait is bounded, on the injected clock
//
// An export runs under ExportTimeout on the sink's clock: an exporter that
// implements ContextSpanExporter receives a context that ends when the clock
// reaches it, and one that implements only SpanExporter is called through Export
// and is bounded by nothing but itself. ForceFlush and Shutdown wait for the
// exporter at most until their own context ends, and say so with
// BatchFlushIncomplete.
//
// Recording a span copies it into a slot allocated at construction: the path a
// span end takes allocates nothing, which BENCH.md measures and an allocation
// gate holds.
type BatchSink struct {
	// exporter receives every batch; withContext is the same exporter when it
	// implements the sibling, nil when it does not. Both immutable.
	exporter    coretrace.SpanExporter
	withContext coretrace.ContextSpanExporter
	// resource and scope are stamped on every batch. Immutable.
	resource coreotel.ResourceValue
	scope    coreotel.ScopeValue
	// the resolved bounds. Immutable.
	maxBatch int
	delay    time.Duration
	timeout  time.Duration
	clk      clock.Timed
	onError  func(err error)

	// mu guards the ring and the three counters. It is a sync.Mutex for the
	// reason Recorder.mu is one: the busy side — a span ending — is exclusive.
	mu sync.Mutex
	// ring holds the queued spans, allocated once at construction: recording
	// copies a span into a slot and allocates nothing.
	ring []coretrace.SpanValue
	// head is the oldest queued slot; count how many are queued.
	head, count int
	// closed is set by Shutdown; a span that ends afterwards is dropped.
	closed bool
	// dropped and failed are cumulative, as Recorder.dropped is.
	dropped, failed uint64

	// ready carries "a batch is full" to the goroutine; one buffered slot, so
	// a span end never waits and a burst wakes it once.
	ready chan struct{}
	// flushes carries ForceFlush requests to the goroutine.
	flushes chan flushRequest
	// stop is closed once, by the first Shutdown.
	stop     chan struct{}
	stopOnce sync.Once
	// done is closed when the goroutine has exported the last batch and ended;
	// lastErr is the first failure of that last flush, written before done
	// closes and read only after. doneOnce states that done closes once.
	done     chan struct{}
	doneOnce sync.Once
	lastErr  error
}

// NewBatchSink builds a BatchSink from cfg, applying every clamp once, and starts
// the one goroutine that exports — Shutdown is what ends it.
//
// A missing exporter is REFUSED with BatchExporterMissing rather than defaulted:
// there is no destination that could be right for a caller who named none, and a
// sink that discarded every span would be the silent loss ADR 0031 forbids.
func NewBatchSink(cfg BatchSinkConfig) (sink *BatchSink, err error) {
	//: a nil, typed-nil or uncomparable exporter cannot serve a call; there is
	//: no default destination, so it is refused (ADR 0031).
	if cfg.Exporter == nil || plugin.Unusable(cfg.Exporter) != "" {
		//: refuse before starting anything.
		return nil, coretrace.BatchExporterMissing
	}
	resolved := cfg.resolved()
	withContext, _ := resolved.Exporter.(coretrace.ContextSpanExporter)
	sink = &BatchSink{
		exporter:    resolved.Exporter,
		withContext: withContext,
		resource:    coretrace.NormalizeResource(resolved.Resource),
		scope:       coretrace.NormalizeScope(resolved.Scope),
		maxBatch:    resolved.MaxExportBatchSize,
		delay:       resolved.ScheduledDelay,
		timeout:     resolved.ExportTimeout,
		clk:         resolved.Clock,
		onError:     resolved.OnError,
		ring:        make([]coretrace.SpanValue, resolved.MaxQueueSize),
		ready:       make(chan struct{}, 1),
		flushes:     make(chan flushRequest),
		stop:        make(chan struct{}),
		done:        make(chan struct{}),
	}
	//: the one goroutine that exports; Shutdown ends it.
	go sink.run()
	//: wired and running.
	return sink, nil
}

// Sink returns the core/observe/trace.SpanSink to hand to a TracerConfig. Every
// call returns the same bound method; recording takes one short lock and never
// waits on the exporter.
func (b *BatchSink) Sink() coretrace.SpanSink {
	//: a bound method value — the func port a TracerConfig takes.
	return b.record
}

// Resource returns the producing resource this sink stamps on every batch,
// already normalised, so a Tracer and its sink can be given ONE resource
// literal.
func (b *BatchSink) Resource() coreotel.ResourceValue {
	//: normalised at construction.
	return b.resource
}

// record is the SpanSink itself: one short lock, a copy into a slot that
// already exists, and a non-blocking wake-up when a batch is full.
func (b *BatchSink) record(span coretrace.SpanValue) {
	b.mu.Lock()
	//: full, or shut down: refused and counted, never silent.
	if b.closed || b.count == len(b.ring) {
		b.dropped++
		b.mu.Unlock()
		//: nothing stored.
		return
	}
	b.ring[(b.head+b.count)%len(b.ring)] = span
	b.count++
	full := b.count >= b.maxBatch
	b.mu.Unlock()
	//: a full batch wakes the goroutine; an already pending wake-up suffices.
	if full {
		select {
		case b.ready <- struct{}{}:
			//: the goroutine will export.
		default:
			//: a wake-up is already pending.
		}
	}
}

// ForceFlush exports every span queued when it is called, in batches, and
// returns once the exporter has answered for each of them or ctx has ended,
// whichever is first. It returns the first export failure, or
// BatchFlushIncomplete wrapping the context's error. After Shutdown it has
// nothing to flush and returns nil.
func (b *BatchSink) ForceFlush(ctx context.Context) error {
	request := flushRequest{reply: make(chan error, 1)}
	//: hand the request over, unless the sink has ended or the caller left.
	select {
	case b.flushes <- request:
		//: accepted; wait for the answer below.
	case <-b.done:
		//: shut down: nothing is queued any more.
		return nil
	case <-ctx.Done():
		//: the caller's bound ended before the goroutine was free.
		return incomplete(ctx.Err())
	}
	//: the exporter's answer, or the caller's bound.
	select {
	case err := <-request.reply:
		//: every span queued at the call was handed to the exporter.
		return err
	case <-ctx.Done():
		//: the flush continues on its own, each export still bounded.
		return incomplete(ctx.Err())
	}
}

// Shutdown stops accepting spans, exports what is queued, and ends the
// exporting goroutine. It returns the first export failure of that last
// flush, or BatchFlushIncomplete when ctx ends first — the goroutine then
// finishes on its own, each export still bounded by ExportTimeout. A second
// call waits for the same end and returns nil.
func (b *BatchSink) Shutdown(ctx context.Context) error {
	first := false
	b.stopOnce.Do(func() {
		b.mu.Lock()
		b.closed = true
		b.mu.Unlock()
		close(b.stop)
		first = true
	})
	//: the last flush's outcome, or the caller's bound.
	select {
	case <-b.done:
		//: only the first Shutdown reports the last flush.
		if first {
			//: nil, or its first export failure.
			return b.lastErr
		}
		//: a second call has nothing to report.
		return nil
	case <-ctx.Done():
		//: the goroutine finishes on its own.
		return incomplete(ctx.Err())
	}
}

// Dropped reports how many spans were refused since construction — the queue
// was full, or the sink was shut down. It is cumulative.
func (b *BatchSink) Dropped() uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	//: cumulative.
	return b.dropped
}

// Failed reports how many spans were handed to an export that returned an
// error, since construction. It is cumulative.
func (b *BatchSink) Failed() uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	//: cumulative.
	return b.failed
}

// Len reports how many spans are queued and not yet handed to the exporter.
func (b *BatchSink) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	//: queued, not yet taken by an export.
	return b.count
}

// run is the exporting goroutine: it waits for a full batch, the scheduled
// delay, a ForceFlush or Shutdown, exports, and re-arms the delay after each
// export, as the specification's scheduledDelay is the delay between two
// consecutive exports.
//
// None of the channels it receives from is ever closed but stop; each receive
// still reads the comma-ok form, and a closed one would end the loop rather
// than spin it.
func (b *BatchSink) run() {
	defer b.doneOnce.Do(func() { close(b.done) })
	timer := b.clk.NewTimer(b.delay)
	defer timer.Stop()
	//: one wake-up per turn; the delay is re-armed after each.
	for b.serve(timer) {
		timer.Reset(b.delay)
	}
}

// serve waits for one wake-up and exports for it, and reports whether the
// goroutine goes on.
func (b *BatchSink) serve(timer clock.Timer) bool {
	select {
	case _, open := <-b.ready:
		//: export full batches only; a partial one waits for the delay.
		if open {
			b.exportFull()
		}
		//: a closed channel would only spin the loop: it ends it.
		return open
	case _, open := <-timer.C():
		//: the delay has passed: everything queued goes.
		if open {
			b.exportAll()
		}
		//: a clock's timer channel is never closed.
		return open
	case request, open := <-b.flushes:
		//: everything queued goes, and the caller hears how it went.
		if open {
			request.reply <- b.exportAll()
		}
		//: never closed.
		return open
	case <-b.stop:
		//: the last flush; closed is set, so nothing new arrives.
		b.lastErr = b.exportAll()
		//: done closes as run returns.
		return false
	}
}

// exportFull exports while at least one full batch is queued.
func (b *BatchSink) exportFull() {
	//: a batch exported may leave another full one behind.
	for b.Len() >= b.maxBatch {
		//: failures are counted and reported inside.
		b.exportBatch()
	}
}

// exportAll exports the spans queued when it is called, in batches, and
// returns the first failure. It is bounded by that count, not by an empty
// queue: spans that keep ending while it exports wait for the next export, so
// a ForceFlush under steady traffic still returns.
func (b *BatchSink) exportAll() error {
	var first error
	//: what was queued at entry; a span ending meanwhile joins a later export.
	for remaining := b.Len(); remaining > 0; remaining -= b.maxBatch {
		//: keep the first failure, keep exporting the rest.
		if err := b.exportBatch(); err != nil && first == nil {
			first = err
		}
	}
	//: nil, or the first failure.
	return first
}

// exportBatch takes up to maxBatch spans off the queue and exports them. The
// batch is a slice of its own: an exporter may hold it, and the ring slot it
// came from is cleared so the queue holds no reference to an exported span.
func (b *BatchSink) exportBatch() error {
	b.mu.Lock()
	n := min(b.count, b.maxBatch)
	batch := make([]coretrace.SpanValue, n)
	for i := range batch {
		slot := (b.head + i) % len(b.ring)
		batch[i] = b.ring[slot]
		b.ring[slot] = coretrace.SpanValue{}
	}
	b.head = (b.head + n) % len(b.ring)
	b.count -= n
	b.mu.Unlock()
	//: an empty queue exports nothing.
	if n == 0 {
		//: no call.
		return nil
	}
	err := b.export(coretrace.SpansValue{Resource: b.resource, Scope: b.scope, Spans: batch})
	//: success: nothing to count.
	if err == nil {
		//: shipped.
		return nil
	}
	b.mu.Lock()
	b.failed += uint64(n)
	b.mu.Unlock()
	//: rule 2: an untyped cause from a third-party exporter is typed here;
	//: a typed one keeps its own code (origin wins).
	wrapped := errs.Wrap(err, errs.WrapParams{
		Code:    coretrace.CodeExportFailed,
		Reason:  "EXPORT_FAILED",
		Public:  "The trace exporter failed to ship the spans",
		Private: "service/observe/trace.BatchSink: the exporter returned an error",
	}, errs.Int("spans", n))
	//: reported, then counted above; never retried here.
	if b.onError != nil {
		//: the caller's hook.
		b.onError(wrapped)
	}
	//: the batch's verdict.
	return wrapped
}

// export hands one batch to the exporter, bounded by the export timeout on
// the sink's clock when the exporter can honour a context.
//
// Goroutine lifecycle: one watcher per bounded export, started here; it ends
// when the clock reaches the bound — cancelling the export's context — or when
// the export returns first, and export joins it before returning, so none
// outlives the call.
func (b *BatchSink) export(spans coretrace.SpansValue) error {
	//: no context to honour: the exporter bounds itself or nothing does.
	if b.withContext == nil {
		//: the bare port.
		return b.exporter.Export(spans)
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	timer := b.clk.NewTimer(b.timeout)
	finished := make(chan struct{})
	watched := make(chan struct{})
	//: the watcher ends the context when the sink's clock reaches the bound,
	//: so a ManualClock decides it and a test waits on nothing.
	go func() {
		defer close(watched)
		select {
		case <-timer.C():
			//: the bound is reached.
			cancel(context.DeadlineExceeded)
		case <-finished:
			//: the export returned first.
		}
	}()
	err := b.withContext.ExportContext(ctx, spans)
	close(finished)
	<-watched
	timer.Stop()
	//: the exporter's verdict.
	return err
}

// incomplete wraps the context error a ForceFlush or Shutdown ended on.
func incomplete(cause error) error {
	//: the typed sentinel, the context's error on the trail.
	return errs.Wrap(cause, errs.WrapParams{
		Code:    coretrace.CodeBatchFlushIncomplete,
		Reason:  "BATCH_FLUSH_INCOMPLETE",
		Public:  "The batch span sink did not finish exporting before the deadline",
		Private: "service/observe/trace.BatchSink: the caller's context ended before the queue was exported",
	})
}
