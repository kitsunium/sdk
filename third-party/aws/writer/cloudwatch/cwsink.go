// Package cloudwatch — the batching terminal Sink. AWS-free: it talks to
// CloudWatch Logs only through the deliverFunc seam, so the batching/flush logic
// is unit-tested with a fake (the real AWS adapter lives in client.go). Each
// record becomes one log event (carrying its RecordEvent.Time); events are
// coalesced and delivered when the batch reaches the event cap, on the
// FlushEvery ticker, or on Flush / Close.
package cloudwatch

import (
	"context"
	"slices"
	"sync"
	"time"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
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

// cwSink coalesces records into batched PutLogEvents deliveries.
type cwSink struct {
	// deliver is the delivery seam (real AWS closure or a test fake).
	deliver deliverFunc
	// maxBatchEvents forces a delivery once the batch reaches it.
	maxBatchEvents int
	// onError surfaces background delivery failures; never nil after newCWSink.
	onError func(err error)
	// now supplies the fallback timestamp for zero-time records.
	now func() time.Time

	// mu guards buf against the ticker, Write, Flush and Close.
	mu  sync.Mutex
	buf []cwEvent

	// stop/done drive the optional FlushEvery ticker goroutine; the Once
	// guards make the channel closes safe under concurrent Close calls.
	stop     chan struct{}
	stopOnce sync.Once
	done     chan struct{}
	doneOnce sync.Once
	ticking  bool
}

// newCWSink builds a batching sink over pt. When flushEvery > 0 it spawns a
// ticker goroutine joined by Close.
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
		deliver:        deliver,
		maxBatchEvents: maxBatchEvents,
		onError:        onError,
		now:            time.Now,
		stop:           make(chan struct{}),
		done:           make(chan struct{}),
	}
	//: only run the ticker when a positive interval was requested.
	if flushEvery > 0 {
		//: mark ticking so Close knows to join the goroutine.
		s.ticking = true
		//: background flusher bounds batch latency to flushEvery.
		go s.loop(flushEvery)
	}
	//: hand back the constructed batching sink.
	return s
}

// Write appends one event for the record and delivers the batch when it reaches
// the event cap. It never blocks the producer (async decouples it); a cap-flush
// failure is routed to onError, not returned.
func (s *cwSink) Write(ctx context.Context, r corelogger.RecordEvent, p []byte) (n int, err error) {
	//: choose the record's time, falling back to now for a zero timestamp.
	ts := r.Time
	//: a zero RecordEvent.Time means the handler left it unset.
	if ts.IsZero() {
		//: substitute the wall clock so every event carries a timestamp.
		ts = s.now()
	}
	//: append the event under the lock; the ticker / Flush share buf.
	s.mu.Lock()
	s.buf = append(s.buf, cwEvent{ts: ts, msg: string(p)})
	full := len(s.buf) >= s.maxBatchEvents
	s.mu.Unlock()
	//: deliver eagerly when the batch is full so memory stays bounded.
	if full {
		//: forward the caller's ctx; route a cap-flush failure to the observer.
		if ferr := s.flushOnce(ctx); ferr != nil {
			//: surface the background failure via the configured hook.
			s.onError(ferr)
		}
	}
	//: report the payload as accepted regardless of the delivery outcome.
	return len(p), nil
}

// Flush delivers the pending batch synchronously, returning any error.
func (s *cwSink) Flush(ctx context.Context) error {
	//: caller-facing flush propagates the error rather than routing to onError.
	return s.flushOnce(ctx)
}

// Close stops the ticker (if any), delivers the final batch, and returns the
// flush error.
func (s *cwSink) Close() error {
	//: join the ticker goroutine first so it cannot race the final flush.
	if s.ticking {
		//: idempotent stop signal so a double Close never panics.
		s.stopOnce.Do(func() { close(s.stop) })
		//: wait for the loop to acknowledge exit.
		<-s.done
	}
	//: deliver whatever remains; background context — Close has no caller ctx.
	return s.flushOnce(context.Background())
}

// flushOnce swaps out the pending batch under the lock and delivers it outside
// the lock. An empty batch is a no-op.
func (s *cwSink) flushOnce(ctx context.Context) error {
	//: take ownership of the pending batch under the lock.
	s.mu.Lock()
	//: nothing buffered — release and report success.
	if len(s.buf) == 0 {
		s.mu.Unlock()
		//: empty-batch fast path.
		return nil
	}
	//: hand the batch off and reset; a nil buf starts a fresh backing array.
	batch := s.buf
	s.buf = nil
	s.mu.Unlock()
	//: PutLogEvents rejects a batch whose events are not in chronological
	//: order; Write appends in arrival order and zero-time records fall back
	//: to now, so out-of-order RecordEvent.Time can interleave. Sort by ts
	//: (stable, so equal timestamps keep arrival order) before delivery.
	slices.SortStableFunc(batch, func(a, b cwEvent) int {
		//: compare the event timestamps; ascending chronological order.
		return a.ts.Compare(b.ts)
	})
	//: deliver outside the lock so a slow PutLogEvents never blocks producers.
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

// loop runs the FlushEvery ticker until Close signals stop.
func (s *cwSink) loop(every time.Duration) {
	//: signal Close that the goroutine has fully exited (guarded for safety).
	defer s.doneOnce.Do(func() { close(s.done) })
	//: periodic flusher bounds how long a partial batch waits.
	t := time.NewTicker(every)
	defer t.Stop()
	//: drain on every tick; exit promptly on stop.
	for {
		select {
		//: interval elapsed — flush whatever has accumulated.
		case <-t.C:
			//: route a tick-flush failure to the observer.
			if ferr := s.flushOnce(context.Background()); ferr != nil {
				//: surface the background failure via the configured hook.
				s.onError(ferr)
			}
		//: Close requested — let Close perform the final flush.
		case <-s.stop:
			//: terminal exit; deferred close(done) unblocks Close.
			return
		}
	}
}
