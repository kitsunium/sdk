package nettransport

import (
	corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"
	"github.com/kitsunium/sdk/internal/service/observe/logger/middleware/async"
	"github.com/kitsunium/sdk/internal/service/observe/logger/writer/levelgate"
)

// compose builds the network writer chain: the terminal per-record netSink
// wrapped by async (non-blocking ring + OnDrop back-pressure) and then by
// levelgate (the per-writer MinLevel floor). It returns the chain behind the
// core/observe/logger.Sink interface so each protocol factory only supplies its send +
// close seams and a plain-data NetConfig — the composition order lives here,
// mirroring the dbsink and s3 factories.
func compose(network string, send sendFunc, closer func() error, cfg NetConfig) corelogger.Sink {
	//: terminal per-record sink → async (non-block + OnDrop) → levelgate (floor),
	//: the same ordering dbsink wires so back-pressure precedes the transport.
	base := newNetSink(network, send, closer)
	nonblocking := async.New(base, async.Config{BufferSize: cfg.BufferSize, OnDrop: cfg.OnDrop, OnError: cfg.OnError})
	//: outermost gate drops below-floor records before they reach the ring.
	return levelgate.New(nonblocking, cfg.MinLevel)
}
