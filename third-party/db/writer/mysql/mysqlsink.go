// Package mysql — mysqlSink, the thin wrapper that adds database-handle cleanup
// to the dbsink-composed chain. dbsink.Compose returns the
// levelgate(async(dbSink)) chain whose Close flushes + joins the drainer, but it
// does not own the *sql.DB; this wrapper closes the handle after the chain drains
// so the connection pool is released exactly once.
package mysql

import (
	"cmp"
	"context"
	"database/sql"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
)

// mysqlSink decorates the composed dbsink chain with ownership of the *sql.DB.
type mysqlSink struct {
	// inner is the levelgate(async(dbSink)) chain from dbsink.Compose.
	inner corelogger.Sink
	// db is the connection pool closed after inner drains.
	db *sql.DB
}

// newMySQLSink pairs the composed chain with the handle it must close.
func newMySQLSink(inner corelogger.Sink, db *sql.DB) *mysqlSink {
	//: the wrapper owns the handle for the sink's lifetime.
	return &mysqlSink{inner: inner, db: db}
}

// Write forwards to the composed chain (gate → async ring → batcher).
func (s *mysqlSink) Write(ctx context.Context, r corelogger.RecordEvent, p []byte) (n int, err error) {
	//: delegate; the chain owns level-gating, buffering, and batching.
	return s.inner.Write(ctx, r, p)
}

// Flush forwards a synchronous flush to the composed chain.
func (s *mysqlSink) Flush(ctx context.Context) error {
	//: delegate the caller-facing flush.
	return s.inner.Flush(ctx)
}

// Close drains the chain (final flush + drainer join) and then closes the
// connection pool. The chain's error wins; the pool close is reported only when
// the chain closed cleanly.
func (s *mysqlSink) Close() error {
	//: drain + join first so no in-flight INSERT outlives the pool.
	ferr := s.inner.Close()
	//: release the connection pool regardless of the drain outcome.
	cerr := s.db.Close()
	//: the drain error is the more meaningful one — surface it first, then the
	//: pool-close outcome (cmp.Or returns the first non-nil).
	return cmp.Or(ferr, cerr)
}
