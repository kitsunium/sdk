// Package s3 — the batching terminal Sink. AWS-free: it talks to S3 only through
// the uploader seam, so the batching/flush logic is unit-tested with a fake
// (the real AWS adapter lives in client.go). Records are coalesced into one
// uploaded object per batch via the generic kernel batcher — flushed when the
// buffered bytes reach MaxBatchBytes, on the FlushEvery ticker, or on Flush /
// Close. The per-batch object key and the byte-weight both ride in the deliver
// closure / WeightOf, so the coalescing/flush/ticker machinery is the shared
// kernel/batcher (ADR 0014), not a hand-rolled copy.
package s3

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/kernel/batcher"
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

// s3Sink coalesces formatted records into batched object uploads. The
// coalesce/flush/ticker lifecycle is delegated to a kernel batcher whose items
// are the per-record payloads; the deliver closure concatenates them, mints the
// object key, and uploads.
type s3Sink struct {
	// up is the upload seam (real AWS closure or a test fake).
	up uploadFunc
	// batch is the generic coalescing buffer; each item is one record payload.
	batch *batcher.Batcher[[]byte]
	// prefix is prepended to every generated object key.
	prefix string
	// onError surfaces background upload failures; never nil after newS3Sink.
	onError func(err error)
	// now supplies timestamps for object keys (injectable for tests).
	now func() time.Time

	// mu guards seq against concurrent deliver closures (eager-flush + ticker).
	mu  sync.Mutex
	seq uint64
}

// newS3Sink builds a batching sink over up. When flushEvery > 0 the underlying
// batcher spawns a ticker goroutine that flushes on the interval; the goroutine
// is joined by Close.
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
		up:      up,
		prefix:  prefix,
		onError: onError,
		now:     time.Now,
	}
	//: byte-weight each payload so MaxBatchBytes bounds the coalesced object.
	s.batch = batcher.NewBatcher(s.deliver, batcher.Config[[]byte]{
		MaxWeight:  int64(maxBatchBytes),
		WeightOf:   payloadBytes,
		FlushEvery: flushEvery,
		OnError:    onError,
	})
	//: hand back the constructed batching sink.
	return s
}

// Write appends the formatted payload to the current batch; the batcher uploads
// eagerly once the buffered bytes reach the cap. It never blocks on the network
// — the producer is decoupled by the async middleware wrapping this sink, and a
// cap-triggered upload failure is routed to onError, not returned.
func (s *s3Sink) Write(ctx context.Context, _ corelogger.RecordEvent, p []byte) (n int, err error) {
	//: clone the payload; the caller may reuse p after Write returns.
	item := slices.Clone(p)
	//: a cap-flush failure surfaces through the observer, never the caller.
	if aerr := s.batch.Add(ctx, item); aerr != nil {
		//: surface the background failure via the configured hook.
		s.onError(aerr)
	}
	//: report the payload as accepted regardless of the upload outcome.
	return len(p), nil
}

// Flush uploads the pending batch synchronously, returning any upload error to
// the caller.
func (s *s3Sink) Flush(ctx context.Context) error {
	//: caller-facing flush propagates the error rather than routing to onError.
	return s.batch.Flush(ctx)
}

// Close stops the ticker (if any), uploads the final batch, and returns the
// flush error.
func (s *s3Sink) Close() error {
	//: background context — Close has no caller ctx; the batcher joins the ticker.
	return s.batch.Close(context.Background())
}

// payloadBytes reports a record payload's byte weight, so MaxBatchBytes caps
// the coalesced object size (the batcher sums this over the pending batch).
func payloadBytes(p []byte) int64 {
	//: the byte length is the object-size contribution of this record.
	return int64(len(p))
}

// deliver concatenates a batch of record payloads into one object body, mints a
// unique key, and uploads it. It runs outside the batcher's lock, so a slow
// PutObject never blocks producers.
func (s *s3Sink) deliver(ctx context.Context, items [][]byte) error {
	//: concatenate the per-record payloads into a single object body.
	var body []byte
	//: walk the batch in arrival order so the object body preserves it.
	for _, p := range items {
		//: append each record's bytes in batch order.
		body = append(body, p...)
	}
	//: mint a unique key under the lock; seq is shared by eager-flush + ticker.
	s.mu.Lock()
	s.seq++
	key := fmt.Sprintf("%s%d-%d.log", s.prefix, s.now().UnixNano(), s.seq)
	s.mu.Unlock()
	//: upload the coalesced object body under the generated key.
	if uerr := s.up(ctx, key, body); uerr != nil {
		//: wrap the SDK cause as the typed PutFailed sentinel (errors.Is reaches
		//: it). ExitCode mirrors the PutFailed Define so the wrapped error keeps
		//: the I/O exit status (74) instead of decaying to the default 70.
		return errs.Wrap(uerr, errs.WrapParams{
			Code:     CodeS3PutFailed,
			Reason:   "PUT_FAILED",
			Public:   "S3 writer failed to upload a log batch",
			Private:  "third-party/aws/writer/s3: PutObject failed while flushing a batch",
			ExitCode: exitIOErr,
		})
	}
	//: happy path — batch delivered.
	return nil
}
