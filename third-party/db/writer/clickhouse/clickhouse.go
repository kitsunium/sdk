// Package clickhouse registers the "clickhouse" writer factory (ADR 0015): a
// database log sink that batches records into multi-row INSERTs over the
// ClickHouse native protocol. Importing the package self-registers the factory
// (no init()), so writer.Open("clickhouse", writer.ClickHouseConfig{…}) resolves.
// It is a dep-light third-party integration: the clickhouse-go/v2 import is
// confined to client.go and lives in the ROOT module only, so pkg/v1 consumers
// never pull the driver into their graph.
//
// Credentials: ClickHouseConfig.Credentials is OPTIONAL (empty falls back to the
// default user) and supplied programmatically; like the AWS writers, there is no
// config-file Decoder because a live credential cannot be expressed in YAML.
//
// Table schema: the destination table must have columns (ts DateTime, level
// String, message String). The table name is validated as a plain identifier and
// interpolated into the INSERT; every value is bound via placeholders.
package clickhouse

import (
	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/writer"
	"github.com/kitsunium/sdk/internal/service/writer/dbsink"
)

// writerName is the canonical registry key.
const writerName = "clickhouse"

// Writer is the registered clickhouse factory singleton. The blank assignment
// runs writer.Register at package load (no init()), mirroring the codec convention.
var Writer = writer.Register(&clickhouseFactory{})

// clickhouseFactory builds a ClickHouse Sink from a writer.ClickHouseConfig.
type clickhouseFactory struct{}

// Name reports the canonical key "clickhouse".
func (*clickhouseFactory) Name() writer.Name {
	//: the literal key consumers pass in a writer spec.
	return writerName
}

// Open validates cfg, builds the database client, and composes the batching
// chain. A wrong config type yields the shared WriterConfigInvalid; an invalid
// table / unresolvable credential surfaces the clickhouse ClientInitFailed.
func (*clickhouseFactory) Open(cfg writer.Config) (sink corelogger.Sink, err error) {
	//: reject a mismatched config type with the shared sentinel.
	c, ok := cfg.(writer.ClickHouseConfig)
	//: the type assertion guards the rest of the construction.
	if !ok {
		//: surface the documented config-type-mismatch sentinel.
		return nil, writer.WriterConfigInvalid
	}
	//: build the lazy sql handle + the INSERT deliver closure.
	db, exec, cerr := newClient(c)
	//: forward a construction failure (origin wins; never echoes credentials).
	if cerr != nil {
		//: ClientInitFailed already carries the right code/reason.
		return nil, cerr
	}
	//: compose the driver-agnostic batching chain over the deliver closure.
	composed := dbsink.Compose(exec, dbsink.Config{
		MaxRows:    c.MaxRows,
		FlushEvery: c.FlushEvery,
		MinLevel:   c.MinLevel,
		BufferSize: c.BufferSize,
		OnDrop:     c.OnDrop,
		OnError:    c.OnError,
	})
	//: wrap so Close also releases the connection pool after the chain drains.
	return newCHSink(composed, db), nil
}
