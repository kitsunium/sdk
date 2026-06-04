// Package clickhouse — chSink, the thin wrapper that adds database-handle
// cleanup to the dbsink-composed chain. dbsink.Compose returns the
// levelgate(async(dbSink)) chain whose Close flushes + joins the drainer, but it
// does not own the *sql.DB; this wrapper closes the handle after the chain drains.
package clickhouse

import (
	"cmp"
	"context"
	"database/sql"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
)

// chSink decorates the composed dbsink chain with ownership of the *sql.DB.
type chSink struct {
	// inner is the levelgate(async(dbSink)) chain from dbsink.Compose.
	inner corelogger.Sink
	// db is the connection pool closed after inner drains.
	db *sql.DB
}

// newCHSink pairs the composed chain with the handle it must close.
func newCHSink(inner corelogger.Sink, db *sql.DB) *chSink {
	//: the wrapper owns the handle for the sink's lifetime.
	return &chSink{inner: inner, db: db}
}

// Write forwards to the composed chain (gate → async ring → batcher).
func (s *chSink) Write(ctx context.Context, r corelogger.RecordEvent, p []byte) (n int, err error) {
	//: delegate; the chain owns level-gating, buffering, and batching.
	return s.inner.Write(ctx, r, p)
}

// Flush forwards a synchronous flush to the composed chain.
func (s *chSink) Flush(ctx context.Context) error {
	//: delegate the caller-facing flush.
	return s.inner.Flush(ctx)
}

// Close drains the chain (final flush + drainer join) then closes the pool. The
// chain's error wins; the pool-close outcome follows (cmp.Or returns first non-nil).
func (s *chSink) Close() error {
	//: drain + join first so no in-flight INSERT outlives the pool.
	ferr := s.inner.Close()
	//: release the connection pool regardless of the drain outcome.
	cerr := s.db.Close()
	//: the drain error takes precedence over the pool-close outcome.
	return cmp.Or(ferr, cerr)
}
