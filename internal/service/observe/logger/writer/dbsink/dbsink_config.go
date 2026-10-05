package dbsink

import (
	corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"
	"github.com/kitsunium/sdk/internal/service/observe/logger/middleware/async"
	"github.com/kitsunium/sdk/internal/service/observe/logger/writer/levelgate"
)

// Compose builds the driver-agnostic DB writer chain: the terminal batching
// dbSink wrapped by async (non-blocking ring + OnDrop back-pressure) and then by
// levelgate (the per-writer MinLevel floor). It returns the chain behind the
// core/observe/logger.Sink interface so a concrete driver adapter only supplies its
// execBatch closure and a plain-data Config — the composition order lives here,
// mirroring the s3 factory. A nil exec is a programming error: the returned sink
// would deliver nothing, so callers MUST pass a real seam.
//
// Compose is the package's only exported constructor (IFACE-PLUGIN): the dbSink
// concrete type stays unexported and concrete drivers compose, never embed, it.
func Compose(exec execBatch, cfg Config) corelogger.Sink {
	//: terminal batching sink → async (non-block + OnDrop) → levelgate (floor),
	//: the same ordering the s3 factory wires so back-pressure precedes batching.
	base := newDBSink(exec, cfg.MaxRows, cfg)
	nonblocking := async.New(base, async.Config{BufferSize: cfg.BufferSize, OnDrop: cfg.OnDrop})
	//: outermost gate drops below-floor records before they reach the ring.
	return levelgate.New(nonblocking, cfg.MinLevel)
}
