// Package redis — redisSink, the thin wrapper that adds client cleanup to the
// dbsink-composed chain. It holds a closeFn (not the *redis.Client) so the
// driver type stays confined to client.go; Close drains the chain then releases
// the client exactly once.
package redis

import (
	"cmp"
	"context"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
)

// redisSink decorates the composed dbsink chain with ownership of the client's
// close seam.
type redisSink struct {
	// inner is the levelgate(async(dbSink)) chain from dbsink.Compose.
	inner corelogger.Sink
	// closeFn releases the underlying Redis client (rdb.Close).
	closeFn func() error
}

// newRedisSink pairs the composed chain with the client's close seam.
func newRedisSink(inner corelogger.Sink, closeFn func() error) *redisSink {
	//: the wrapper owns the close seam for the sink's lifetime.
	return &redisSink{inner: inner, closeFn: closeFn}
}

// Write forwards to the composed chain (gate → async ring → batcher).
func (s *redisSink) Write(ctx context.Context, r corelogger.RecordEvent, p []byte) (n int, err error) {
	//: delegate; the chain owns level-gating, buffering, and batching.
	return s.inner.Write(ctx, r, p)
}

// Flush forwards a synchronous flush to the composed chain.
func (s *redisSink) Flush(ctx context.Context) error {
	//: delegate the caller-facing flush.
	return s.inner.Flush(ctx)
}

// Close drains the chain (final flush + drainer join) and then releases the
// client. The chain's error wins; the client-close outcome follows.
func (s *redisSink) Close() error {
	//: drain + join first so no in-flight XADD outlives the client.
	ferr := s.inner.Close()
	//: release the client regardless of the drain outcome.
	cerr := s.closeFn()
	//: cmp.Or returns the first non-nil — the drain error takes precedence.
	return cmp.Or(ferr, cerr)
}
