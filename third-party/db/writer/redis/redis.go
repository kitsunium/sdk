// Package redis registers the "redis" writer factory (ADR 0015): a log sink that
// appends records to a Redis Stream via pipelined XADD over a Unix-domain socket
// (local-protocol). Importing the package self-registers the factory (no
// init()), so writer.Open("redis", writer.RedisStreamConfig{…}) resolves. It is a
// dep-light third-party integration: the github.com/redis/go-redis/v9 import is
// confined to client.go and lives in the ROOT module only, so pkg/v1 consumers
// never pull the driver into their graph.
//
// Credentials: RedisStreamConfig.Credentials is OPTIONAL (a socket-local Redis
// may have no AUTH) and, when set, supplied programmatically — like the AWS
// writers, there is no config-file Decoder because a live credential cannot be
// expressed safely in YAML.
package redis

import (
	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/writer"
	"github.com/kitsunium/sdk/internal/service/writer/dbsink"
)

// writerName is the canonical registry key.
const writerName = "redis"

// Writer is the registered redis factory singleton. The blank assignment runs
// writer.Register at package load (no init()), mirroring the codec convention.
var Writer = writer.Register(&redisFactory{})

// redisFactory builds a Redis Stream Sink from a writer.RedisStreamConfig.
type redisFactory struct{}

// Name reports the canonical key "redis".
func (*redisFactory) Name() writer.Name {
	//: the literal key consumers pass in a writer spec.
	return writerName
}

// Open validates cfg, builds the client, and composes the batching chain. A
// wrong config type yields the shared WriterConfigInvalid; a missing
// socket/stream or unresolvable credential surfaces the redis ClientInitFailed.
func (*redisFactory) Open(cfg writer.Config) (sink corelogger.Sink, err error) {
	//: reject a mismatched config type with the shared sentinel.
	c, ok := cfg.(writer.RedisStreamConfig)
	//: the type assertion guards the rest of the construction.
	if !ok {
		//: surface the documented config-type-mismatch sentinel.
		return nil, writer.WriterConfigInvalid
	}
	//: build the lazy client + the XADD deliver closure.
	exec, closeFn, cerr := newClient(c)
	//: forward a construction failure (origin wins; never echoes the socket).
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
	//: wrap so Close also releases the client after the chain drains.
	return newRedisSink(composed, closeFn), nil
}
