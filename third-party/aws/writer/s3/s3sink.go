// Package s3 — the batching terminal Sink. AWS-free: it talks to S3 only through
// the uploader seam, so the batching/flush logic is unit-tested with a fake
// (the real AWS adapter lives in client.go). Records are coalesced into one
// uploaded object per batch — flushed when the buffer reaches MaxBatchBytes, on
// the FlushEvery ticker, or on Flush / Close.
package s3

import (
	"context"
	"fmt"
	"sync"
	"time"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// defaultMaxBatchBytes bounds the in-memory batch when the config leaves
// MaxBatchBytes at its zero value.
const defaultMaxBatchBytes int = 1 << 20 // 1 MiB

// uploadFunc is the seam over S3 PutObject: it sends body under key, returning
// a non-nil error to fail the batch. A func type (not an interface) keeps the
// real AWS call an anonymous closure in client.go — confining the SDK there —
// while tests inject a recording closure, so the batching logic needs no
// network and the seam carries no untestable named method.
type uploadFunc func(ctx context.Context, key string, body []byte) error

// s3Sink coalesces formatted records into batched object uploads.
type s3Sink struct {
	// up is the upload seam (real AWS closure or a test fake).
	up uploadFunc
	// prefix is prepended to every generated object key.
	prefix string
	// maxBatchBytes forces an upload once the buffer reaches it.
	maxBatchBytes int
	// onError surfaces background upload failures; never nil after newS3Sink.
	onError func(err error)
	// now supplies timestamps for object keys (injectable for tests).
	now func() time.Time

	// mu guards buf + seq against the ticker, Write, Flush and Close.
	mu  sync.Mutex
	buf []byte
	seq uint64

	// stop/done drive the optional FlushEvery ticker goroutine; the Once
	// guards make the channel closes safe under concurrent Close calls.
	stop     chan struct{}
	stopOnce sync.Once
	done     chan struct{}
	doneOnce sync.Once
	ticking  bool
}

// newS3Sink builds a batching sink over up. When flushEvery > 0 it spawns a
// ticker goroutine that flushes on the interval; the goroutine is joined by
// Close.
func newS3Sink(up uploadFunc, prefix string, maxBatchBytes int, flushEvery time.Duration, onError func(error)) *s3Sink {
	//: substitute the default cap when the caller left it at zero.
	if maxBatchBytes <= 0 {
		//: 1 MiB comfortably absorbs a burst without unbounded growth.
		maxBatchBytes = defaultMaxBatchBytes
	}
	//: degrade a nil error hook to a no-op so flush paths stay branch-free.
	if onError == nil {
		//: discard upload errors when the caller wired no observer.
		onError = func(error) {}
	}
	s := &s3Sink{
		up:            up,
		prefix:        prefix,
		maxBatchBytes: maxBatchBytes,
		onError:       onError,
		now:           time.Now,
		stop:          make(chan struct{}),
		done:          make(chan struct{}),
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

// Write appends the formatted payload to the current batch and triggers an
// upload when the batch reaches its byte cap. It never blocks on the network —
// the producer is decoupled by the async middleware wrapping this sink, and a
// cap-triggered upload failure is routed to onError, not returned.
func (s *s3Sink) Write(ctx context.Context, _ corelogger.RecordEvent, p []byte) (n int, err error) {
	//: append under the lock; the ticker / Flush share buf.
	s.mu.Lock()
	s.buf = append(s.buf, p...)
	//: decide whether this append crossed the upload threshold.
	full := len(s.buf) >= s.maxBatchBytes
	s.mu.Unlock()
	//: flush eagerly when the batch is full so memory stays bounded.
	if full {
		//: forward the caller's ctx; route a cap-flush failure to the observer.
		if ferr := s.flushOnce(ctx); ferr != nil {
			//: surface the background failure via the configured hook.
			s.onError(ferr)
		}
	}
	//: report the payload as accepted regardless of the upload outcome.
	return len(p), nil
}

// Flush uploads the pending batch synchronously, returning any upload error to
// the caller.
func (s *s3Sink) Flush(ctx context.Context) error {
	//: caller-facing flush propagates the error rather than routing to onError.
	return s.flushOnce(ctx)
}

// Close stops the ticker (if any), uploads the final batch, and returns the
// flush error.
func (s *s3Sink) Close() error {
	//: join the ticker goroutine first so it cannot race the final flush.
	if s.ticking {
		//: idempotent stop signal so a double Close never panics.
		s.stopOnce.Do(func() { close(s.stop) })
		//: wait for the loop to acknowledge exit.
		<-s.done
	}
	//: upload whatever remains; background context — Close has no caller ctx.
	return s.flushOnce(context.Background())
}

// flushOnce swaps out the pending batch under the lock and uploads it outside
// the lock. An empty batch is a no-op; the upload error is returned to the
// caller (Write / loop route it to onError, Flush / Close propagate it).
func (s *s3Sink) flushOnce(ctx context.Context) error {
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
	s.seq++
	key := fmt.Sprintf("%s%d-%d.log", s.prefix, s.now().UnixNano(), s.seq)
	s.mu.Unlock()
	//: upload outside the lock so a slow PutObject never blocks producers.
	if uerr := s.up(ctx, key, batch); uerr != nil {
		//: wrap the SDK cause as the typed PutFailed sentinel (errors.Is reaches it).
		return errs.Wrap(uerr, errs.WrapParams{
			Code:    CodeS3PutFailed,
			Reason:  "PUT_FAILED",
			Public:  "S3 writer failed to upload a log batch",
			Private: "third-party/aws/writer/s3: PutObject failed while flushing a batch",
		})
	}
	//: happy path — batch delivered.
	return nil
}

// loop runs the FlushEvery ticker until Close signals stop.
func (s *s3Sink) loop(every time.Duration) {
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
