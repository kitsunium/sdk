// Package dbsink — the Config value type plus Compose, the single entry point a
// concrete driver adapter uses to build the levelgate(async(dbSink)) chain.
// Keeping the composition order here (not in each driver) means every DB writer
// inherits the same back-pressure + level-floor wiring as the s3 factory, with
// only the execBatch seam and the plain-data Config varying per driver.
package dbsink

import (
	"time"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/logger/level"
	"github.com/kitsunium/sdk/internal/service/logger/middleware/async"
	"github.com/kitsunium/sdk/internal/service/writer/levelgate"
)

// Config tunes the database sink shell. Every field is optional: the zero Config
// batches by the default row cap, flushes only on Flush / Close, drops on a
// saturated ring with no observer, and inherits the handler-global level. It
// carries NO driver type — only plain data — so a concrete adapter's own config
// (e.g. a future logger.MySQLConfig) can map onto it without leaking a driver
// into the dep-light layers.
type Config struct {
	// MaxRows forces a flush once the pending batch reaches this many records;
	// a non-positive value applies the shell's default (defaultMaxRows). A DB
	// insert is bounded by statement / pipeline row count, so the shell batches
	// by count rather than by byte size.
	MaxRows int
	// FlushEvery bounds how long a partial batch waits before it is delivered;
	// the zero value delivers only on an explicit Flush / Close.
	FlushEvery time.Duration
	// MinLevel is the optional per-writer severity floor; the zero value
	// (level.Info) inherits the handler-global level and adds no per-record gate.
	MinLevel level.Level
	// BufferSize is the async ring capacity handed to the async middleware; a
	// non-positive value applies async's own default (1024 slots).
	BufferSize int
	// OnDrop, when non-nil, is invoked with the count of records discarded
	// because the non-blocking ring saturated. Wire it to a metric counter: the
	// hot path never blocks on a slow database, so drops are how back-pressure
	// surfaces.
	OnDrop func(dropped int)
	// OnError, when non-nil, is invoked for every failed batch delivery seen by
	// the background drainer / ticker. Without it those errors are lost (the
	// producer cannot be blocked on a database failure).
	OnError func(err error)
}

// Compose builds the driver-agnostic DB writer chain: the terminal batching
// dbSink wrapped by async (non-blocking ring + OnDrop back-pressure) and then by
// levelgate (the per-writer MinLevel floor). It returns the chain behind the
// core/logger.Sink interface so a concrete driver adapter only supplies its
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
