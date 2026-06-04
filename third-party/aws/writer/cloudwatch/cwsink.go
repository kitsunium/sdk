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

// eventOverheadBytes is the fixed per-event cost PutLogEvents adds to the UTF-8
// message length when summing a batch against its 1 MiB payload ceiling.
const eventOverheadBytes int = 26

// maxBatchBytes caps the summed event weight per PutLogEvents call. It sits
// safely under the AWS hard limit of one mebibyte so a count-capped or
// byte-capped batch never trips InvalidParameterException (V84).
const maxBatchBytes int64 = 1_000_000

// maxBatchSpan caps the time distance between the earliest and latest event in
// one PutLogEvents call; AWS rejects a batch spanning more than 24 hours (V85).
const maxBatchSpan time.Duration = 24 * time.Hour

// maxEventPast is how far in the past an event timestamp may sit before
// PutLogEvents rejects it; an older event is dropped and routed to OnError.
const maxEventPast time.Duration = 14 * 24 * time.Hour

// maxEventFuture is how far ahead of now an event timestamp may sit before
// PutLogEvents rejects it; a further-future event is dropped and routed to
// OnError.
const maxEventFuture time.Duration = 2 * time.Hour

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
	//: cap on BOTH event count AND summed payload bytes so neither the 10000-event
	//: nor the 1 MiB PutLogEvents ceiling can be breached before an eager flush
	//: (V84). WeightOf mirrors the AWS accounting: message bytes + 26/event.
	s.batch = batcher.NewBatcher(s.deliverBatch, batcher.Config[cwEvent]{
		MaxItems:   maxBatchEvents,
		MaxWeight:  maxBatchBytes,
		WeightOf:   eventWeight,
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

// deliverBatch enforces every PutLogEvents constraint before delivery: it drops
// events whose timestamps fall outside the per-event window (V85), stable-sorts
// the survivors chronologically (PutLogEvents requires ordering), then splits
// them into sub-batches that each honour the event-count, 1 MiB-byte, and 24h-
// span ceilings before delivering each one. It runs outside the batcher's lock,
// so a slow PutLogEvents never blocks producers.
func (s *cwSink) deliverBatch(ctx context.Context, batch []cwEvent) error {
	//: drop out-of-window events first (each routed to OnError) so only AWS-
	//: acceptable timestamps reach the span/byte/count splitter below.
	kept := s.filterWindow(batch)
	//: nothing survived the window filter — no PutLogEvents call to make.
	if len(kept) == 0 {
		//: empty after filtering is a successful no-op.
		return nil
	}
	//: PutLogEvents rejects a batch whose events are not in chronological order;
	//: Write appends in arrival order and zero-time records fall back to now, so
	//: out-of-order RecordEvent.Time can interleave. Sort by ts (stable, so equal
	//: timestamps keep arrival order) before splitting.
	slices.SortStableFunc(kept, func(a, b cwEvent) int {
		//: compare the event timestamps; ascending chronological order.
		return a.ts.Compare(b.ts)
	})
	//: deliver each ceiling-bounded sub-batch in order.
	for _, sub := range splitBatch(kept) {
		//: a sub-batch delivery failure aborts and surfaces to the flush caller.
		if perr := s.deliver(ctx, sub); perr != nil {
			//: wrap the SDK cause as the typed PutFailed sentinel (errors.Is
			//: reaches it). ExitCode mirrors the PutFailed Define so the wrapped
			//: error keeps the I/O exit status (74) instead of decaying to 70.
			return errs.Wrap(perr, errs.WrapParams{
				Code:     CodeCWPutFailed,
				Reason:   "PUT_FAILED",
				Public:   "CloudWatch writer failed to deliver a log batch",
				Private:  "third-party/aws/writer/cloudwatch: PutLogEvents failed while flushing a batch",
				ExitCode: exitIOErr,
			})
		}
	}
	//: happy path — every sub-batch delivered.
	return nil
}

// filterWindow returns the events whose timestamps fall inside the PutLogEvents
// per-event window (no older than 14 days, no more than 2 hours ahead). Each
// dropped event is surfaced via onError as a typed EventRejected so an operator
// backfilling logs learns why an event vanished, while the rest of the batch
// still ships.
func (s *cwSink) filterWindow(batch []cwEvent) []cwEvent {
	//: now anchors both the past and future bounds for this delivery.
	now := s.now()
	oldest, newest := now.Add(-maxEventPast), now.Add(maxEventFuture)
	kept := batch[:0]
	//: walk the batch, keeping in-window events and reporting the rest.
	for _, e := range batch {
		//: an out-of-window timestamp is dropped rather than failing the batch.
		if e.ts.Before(oldest) || e.ts.After(newest) {
			//: surface the drop so the loss is observable, not silent.
			s.onError(errs.Wrap(EventRejected, errs.WrapParams{
				Code:    CodeCWEventRejected,
				Reason:  "EVENT_REJECTED",
				Public:  "CloudWatch writer dropped a log event with an out-of-range timestamp",
				Private: "third-party/aws/writer/cloudwatch: event timestamp outside the 14d-past / 2h-future PutLogEvents window",
			}))
			continue
		}
		//: in-window — retain for delivery.
		kept = append(kept, e)
	}
	//: the filtered, in-window events.
	return kept
}

// splitBatch partitions a chronologically sorted batch into sub-batches that
// each honour the PutLogEvents ceilings: at most defaultMaxBatchEvents events,
// at most maxBatchBytes summed payload weight, and at most a maxBatchSpan time
// distance between the first and last event. The input order is preserved.
func splitBatch(sorted []cwEvent) [][]cwEvent {
	var (
		out   [][]cwEvent
		start int
		bytes int64
	)
	//: scan the sorted events, cutting a sub-batch each time adding the next
	//: event would breach the count, byte, or 24h-span ceiling.
	for i, e := range sorted {
		w := eventWeight(e)
		//: an existing sub-batch that this event would over-fill is cut first.
		if i > start && batchFull(i-start, bytes, w, sorted[start].ts, e.ts) {
			out = append(out, sorted[start:i])
			start, bytes = i, 0
		}
		//: accumulate the event's weight into the current sub-batch.
		bytes += w
	}
	//: emit the trailing sub-batch (always non-empty for a non-empty input).
	return append(out, sorted[start:])
}

// batchFull reports whether appending one more event would push the current
// sub-batch past any PutLogEvents ceiling: the event count, the summed byte
// weight, or the 24h span between the sub-batch's first event and the candidate.
func batchFull(count int, bytes, next int64, first, cand time.Time) bool {
	//: the event-count ceiling forbids more than defaultMaxBatchEvents per call.
	countFull := count >= defaultMaxBatchEvents
	//: the 1 MiB byte ceiling forbids the summed weight from crossing the cap.
	byteFull := bytes+next > maxBatchBytes
	//: the 24h-span ceiling forbids first..candidate exceeding maxBatchSpan.
	spanFull := cand.Sub(first) > maxBatchSpan
	//: any ceiling crossing forces a cut before this event.
	return countFull || byteFull || spanFull
}

// eventWeight reports an event's PutLogEvents payload contribution: the UTF-8
// message length plus the fixed 26-byte per-event overhead AWS adds when summing
// a batch against the 1 MiB ceiling (V84).
func eventWeight(e cwEvent) int64 {
	//: message bytes plus the fixed per-event overhead.
	return int64(len(e.msg) + eventOverheadBytes)
}
