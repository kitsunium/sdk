package otlpjson

import (
	corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"
	"github.com/kitsunium/sdk/internal/core/observe/logger/level"
	"github.com/kitsunium/sdk/internal/core/observe/logger/writer"
	coreotel "github.com/kitsunium/sdk/internal/core/observe/otel"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/observe/logger/middleware/async"
	"github.com/kitsunium/sdk/internal/service/observe/logger/writer/levelgate"
)

const (
	// writerName is the canonical registry key.
	writerName = "otlpjson"
	// DefaultBatchMax is how many records one document carries when
	// Config.BatchMax is zero: the async drainer hands the sink up to this many.
	DefaultBatchMax int = 64
)

// Writer is the registered "otlpjson" factory singleton. The blank assignment
// runs writer.Register at package load (no init()).
var Writer = writer.Register(&factory{})

// Config tunes the "otlpjson" writer. Exporter is required; every other field
// is optional.
type Config struct {
	// Exporter receives each batch as one OTLP/JSON document: the exporter
	// NewOTLPHTTPExporter returns, or forward.NewLogExporter's adapter, which
	// keeps it in a durable queue. It must not retain doc past its return.
	Exporter corelogger.DocumentExporter
	// Resource identifies the producer, carried once per document.
	Resource coreotel.ResourceValue
	// Scope identifies the instrumentation, carried once per document.
	Scope coreotel.ScopeValue
	// MinLevel is the optional per-writer severity floor; the zero value
	// (level.Info) inherits the handler-global level.
	MinLevel level.Level
	// BatchMax is how many records one document carries at most; zero is
	// DefaultBatchMax.
	BatchMax int
	// BufferSize is the async ring capacity; a non-positive value applies
	// async's own default.
	BufferSize int
	// OnDrop, when non-nil, is invoked with the count of records discarded
	// because the non-blocking ring saturated.
	OnDrop func(dropped int)
	// OnError, when non-nil, is invoked for each record of a batch the
	// exporter refused; nil discards the failure.
	OnError func(err error)
}

// factory builds the writer from a Config.
type factory struct{}

// Name reports the canonical key "otlpjson".
func (*factory) Name() writer.Name {
	//: the registry key.
	return writerName
}

// Open validates cfg and composes levelgate(async(otlpSink)): the level floor
// first, then the ring, whose drainer hands the sink up to BatchMax records
// at a time. A wrong config type, or one with no Exporter, is the shared
// WriterConfigInvalid.
func (*factory) Open(cfg writer.Config) (sink corelogger.Sink, err error) {
	c, ok := cfg.(Config)
	//: not this writer's configuration.
	if !ok {
		return nil, writer.WriterConfigInvalid
	}
	//: nothing to export to.
	if c.Exporter == nil {
		return nil, errs.Wrap(writer.WriterConfigInvalid, errs.WrapParams{}, errs.String("writer", writerName), errs.String("field", "Exporter"))
	}
	batchMax := c.BatchMax
	//: the documented default.
	if batchMax <= 0 {
		batchMax = DefaultBatchMax
	}
	nonblocking := async.New(newSink(&c), async.Config{BufferSize: c.BufferSize, OnDrop: c.OnDrop, OnError: c.OnError, BatchMax: batchMax})
	//: the floor drops below-floor records before the ring.
	return levelgate.New(nonblocking, c.MinLevel), nil
}
