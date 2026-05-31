// Package cloudwatch — the batching terminal Sink. AWS-free: it talks to
// CloudWatch Logs only through the deliverFunc seam, so the batching/flush logic
// is unit-tested with a fake (the real AWS adapter lives in client.go). Each
// record becomes one log event (carrying its RecordEvent.Time); events are
// coalesced via the generic kernel batcher and delivered when the batch reaches
// the event cap, on the FlushEvery ticker, or on Flush / Close. The
// PutLogEvents chronological-order requirement is honoured by the deliver
// closure (a stable sort by timestamp), so the coalescing/flush/ticker
// machinery is the shared kernel/batcher (ADR 0014), not a hand-rolled copy.
package cloudwatch

import (
	"context"
	"slices"
	"time"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/kernel/batcher"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// defaultMaxBatchEvents bounds the in-memory batch; CloudWatch caps a single
// PutLogEvents call at 10000 events, so the default stays well under that.
const defaultMaxBatchEvents int = 1000

// deliverFunc is the seam over CloudWatch PutLogEvents: it delivers a batch,
// returning a non-nil error to fail it. A func type (not an interface) keeps
// the real AWS call an anonymous closure in client.go — confining the SDK there
// — while tests inject a recording closure, so the batching logic needs no
// network and the seam carries no untestable named method.
type deliverFunc func(ctx context.Context, events []cwEvent) error

// cwSink coalesces records into batched PutLogEvents deliveries. The
// coalesce/flush/ticker lifecycle is delegated to a kernel batcher whose items
// are the per-record events; the deliver closure reorders them chronologically
// and delivers.
type cwSink struct {
	// deliver is the delivery seam (real AWS closure or a test fake).
	deliver deliverFunc
	// batch is the generic coalescing buffer; each item is one log event.
	batch *batcher.Batcher[cwEvent]
	// onError surfaces background delivery failures; never nil after newCWSink.
	onError func(err error)
	// now supplies the fallback timestamp for zero-time records.
	now func() time.Time
}

// newCWSink builds a batching sink over deliver. When flushEvery > 0 the
// underlying batcher spawns a ticker goroutine joined by Close.
func newCWSink(deliver deliverFunc, maxBatchEvents int, flushEvery time.Duration, onError func(error)) *cwSink {
	//: substitute the default cap when the caller left it at zero.
	if maxBatchEvents <= 0 {
		//: a conservative default well under the CloudWatch 10000-event ceiling.
		maxBatchEvents = defaultMaxBatchEvents
	}
	//: degrade a nil error hook to a no-op so flush paths stay branch-free.
	if onError == nil {
		//: discard delivery errors when the caller wired no observer.
		onError = func(error) {}
	}
	s := &cwSink{
		deliver: deliver,
		onError: onError,
		now:     time.Now,
	}
	//: count-weight each event so the event cap bounds the PutLogEvents batch.
	s.batch = batcher.NewBatcher(s.deliverBatch, batcher.Config[cwEvent]{
		MaxItems:   maxBatchEvents,
		FlushEvery: flushEvery,
		OnError:    onError,
	})
	//: hand back the constructed batching sink.
	return s
}

// Write appends one event for the record; the batcher delivers eagerly once the
// batch reaches the event cap. It never blocks the producer (async decouples
// it); a cap-flush failure is routed to onError, not returned.
func (s *cwSink) Write(ctx context.Context, r corelogger.RecordEvent, p []byte) (n int, err error) {
	//: choose the record's time, falling back to now for a zero timestamp.
	ts := r.Time
	//: a zero RecordEvent.Time means the handler left it unset.
	if ts.IsZero() {
		//: substitute the wall clock so every event carries a timestamp.
		ts = s.now()
	}
	//: a cap-flush failure surfaces through the observer, never the caller.
	if aerr := s.batch.Add(ctx, cwEvent{ts: ts, msg: string(p)}); aerr != nil {
		//: surface the background failure via the configured hook.
		s.onError(aerr)
	}
	//: report the payload as accepted regardless of the delivery outcome.
	return len(p), nil
}

// Flush delivers the pending batch synchronously, returning any error.
func (s *cwSink) Flush(ctx context.Context) error {
	//: caller-facing flush propagates the error rather than routing to onError.
	return s.batch.Flush(ctx)
}

// Close stops the ticker (if any), delivers the final batch, and returns the
// flush error.
func (s *cwSink) Close() error {
	//: background context — Close has no caller ctx; the batcher joins the ticker.
	return s.batch.Close(context.Background())
}

// deliverBatch reorders a batch chronologically and delivers it. It runs
// outside the batcher's lock, so a slow PutLogEvents never blocks producers.
func (s *cwSink) deliverBatch(ctx context.Context, batch []cwEvent) error {
	//: PutLogEvents rejects a batch whose events are not in chronological
	//: order; Write appends in arrival order and zero-time records fall back
	//: to now, so out-of-order RecordEvent.Time can interleave. Sort by ts
	//: (stable, so equal timestamps keep arrival order) before delivery.
	slices.SortStableFunc(batch, func(a, b cwEvent) int {
		//: compare the event timestamps; ascending chronological order.
		return a.ts.Compare(b.ts)
	})
	//: deliver the chronologically ordered batch to CloudWatch.
	if perr := s.deliver(ctx, batch); perr != nil {
		//: wrap the SDK cause as the typed PutFailed sentinel (errors.Is reaches
		//: it). ExitCode mirrors the PutFailed Define so the wrapped error keeps
		//: the I/O exit status (74) instead of decaying to the default 70.
		return errs.Wrap(perr, errs.WrapParams{
			Code:     CodeCWPutFailed,
			Reason:   "PUT_FAILED",
			Public:   "CloudWatch writer failed to deliver a log batch",
			Private:  "third-party/aws/writer/cloudwatch: PutLogEvents failed while flushing a batch",
			ExitCode: exitIOErr,
		})
	}
	//: happy path — batch delivered.
	return nil
}
